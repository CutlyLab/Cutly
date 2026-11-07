package evaluator

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestCubicKeyframeCanonicalVectors(t *testing.T) {
	type expectedRational struct {
		Numerator   string `json:"numerator"`
		Denominator string `json:"denominator"`
	}
	type sample struct {
		At    documentv2.RationalTime `json:"at"`
		Value expectedRational        `json:"value"`
	}
	type vectors struct {
		Profile string                  `json:"profile"`
		Lane    documentv2.KeyframeLane `json:"lane"`
		Samples []sample                `json:"samples"`
	}
	var fixture vectors
	decodeFixture(t, "keyframe-evaluator-v1.vectors.json", &fixture)
	if fixture.Profile != documentv2.KeyframeEvaluationEndpointClampV1 {
		t.Fatalf("profile = %q", fixture.Profile)
	}
	for index, vector := range fixture.Samples {
		actual, err := EvaluateLane(fixture.Lane, vector.At)
		if err != nil {
			t.Fatalf("sample %d: %v", index, err)
		}
		if actual.Scalar == nil || actual.Scalar.Numerator().String() != vector.Value.Numerator ||
			actual.Scalar.Denominator().String() != vector.Value.Denominator {
			t.Fatalf("sample %d = %+v, want %s/%s", index, actual.Scalar, vector.Value.Numerator, vector.Value.Denominator)
		}
	}
}

func TestCubicEvaluationKeepsValueBeyondWireInt64(t *testing.T) {
	lane := cubicFixtureLane(t)
	value, err := EvaluateLane(lane, documentv2.RationalTime{Value: 1, Rate: 3})
	if err != nil {
		t.Fatal(err)
	}
	if value.Scalar == nil || value.Scalar.Denominator().IsInt64() {
		t.Fatalf("expected arbitrary-precision denominator, got %+v", value.Scalar)
	}
	if _, err := value.Scalar.DocumentRational(); !errors.Is(err, ErrValueNotWireSafe) {
		t.Fatalf("wire conversion error = %v", err)
	}
}

func TestCubicQ32BoundaryContract(t *testing.T) {
	linear := documentv2.CubicBezier{
		X1: documentv2.Rational{Numerator: 1, Denominator: 3},
		Y1: documentv2.Rational{Numerator: 1, Denominator: 3},
		X2: documentv2.Rational{Numerator: 2, Denominator: 3},
		Y2: documentv2.Rational{Numerator: 2, Denominator: 3},
	}
	// x(u)=y(u)=u. Q0.32 lower-bound selection therefore returns ceil(2^32/3)/2^32,
	// not the unquantized mathematical value 1/3.
	linearWeight := cubicBezierWeight(big.NewRat(1, 3), linear)
	if got, want := linearWeight.RatString(), "715827883/2147483648"; got != want {
		t.Fatalf("Q0.32 lower bound = %s, want %s", got, want)
	}

	denominator := new(big.Int).Lsh(big.NewInt(1), 32)
	maximum := new(big.Int).Sub(new(big.Int).Set(denominator), big.NewInt(1))
	cube := func(value *big.Int) *big.Int {
		return new(big.Int).Exp(value, big.NewInt(3), nil)
	}
	xAtMaximum := new(big.Rat).SetFrac(cube(maximum), cube(denominator))
	edge := documentv2.CubicBezier{
		X1: documentv2.Rational{Numerator: 0, Denominator: 1},
		Y1: documentv2.Rational{Numerator: 1, Denominator: 1},
		X2: documentv2.Rational{Numerator: 0, Denominator: 1},
		Y2: documentv2.Rational{Numerator: 1, Denominator: 1},
	}
	// Equality at the largest interior x must select that interior grid point; only p>x(max)
	// takes the explicit u=1 branch.
	atMaximum := cubicBezierWeight(xAtMaximum, edge)
	denominatorCube := cube(denominator)
	wantAtMaximum := new(big.Rat).SetFrac(
		new(big.Int).Sub(new(big.Int).Set(denominatorCube), big.NewInt(1)),
		denominatorCube,
	)
	if atMaximum.Cmp(wantAtMaximum) != 0 {
		t.Fatalf("maximum-interior equality = %s, want %s", atMaximum.RatString(), wantAtMaximum.RatString())
	}

	beyondMaximum := new(big.Rat).SetFrac(
		new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 40), big.NewInt(1)),
		new(big.Int).Lsh(big.NewInt(1), 40),
	)
	if got := cubicBezierWeight(beyondMaximum, edge); got.Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatalf("p>x(max) branch = %s, want 1", got.RatString())
	}
}

func TestExactRationalAccessorsAreDefensive(t *testing.T) {
	value := exactFromStrings(t, "7", "11")
	numerator := value.Numerator()
	denominator := value.Denominator()
	numerator.SetInt64(99)
	denominator.SetInt64(101)
	if got := value.String(); got != "7/11" {
		t.Fatalf("accessor mutation changed value: %s", got)
	}
}

func TestLinearHoldAndExactRightEndpoint(t *testing.T) {
	zero, one, two := rationalPointer(0, 1), rationalPointer(1, 1), rationalPointer(2, 1)
	lane := documentv2.KeyframeLane{
		Target:    documentv2.KeyframeTargetTransformRotation,
		ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: zero}, Interpolation: documentv2.InterpolationHold},
			{Offset: rt(1, 1), Value: documentv2.KeyframeValue{Scalar: one}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(3, 1), Value: documentv2.KeyframeValue{Scalar: two}},
		},
	}
	assertScalar(t, lane, rt(1, 2), "0/1")
	assertScalar(t, lane, rt(1, 1), "1/1")
	assertScalar(t, lane, rt(2, 1), "3/2")
	assertScalar(t, lane, rt(3, 1), "2/1")
}

func TestLaneRejectsTargetTypeAndTargetSpecificRanges(t *testing.T) {
	one := documentv2.Rational{Numerator: 1, Denominator: 1}
	two := documentv2.Rational{Numerator: 2, Denominator: 1}
	negative := documentv2.Rational{Numerator: -1, Denominator: 1}
	threeQuarters := documentv2.Rational{Numerator: 3, Denominator: 4}
	half := documentv2.Rational{Numerator: 1, Denominator: 2}
	tests := []documentv2.KeyframeLane{
		{
			Target: "vendor.unknown", ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &one}}},
		},
		{
			Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValuePoint,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &documentv2.Point{X: one, Y: one}}}},
		},
		{
			Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &two}}},
		},
		{
			Target: documentv2.KeyframeTargetTransformAnchor, ValueType: documentv2.KeyframeValuePoint,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &documentv2.Point{X: two, Y: one}}}},
		},
		{
			Target: documentv2.KeyframeTargetCrop, ValueType: documentv2.KeyframeValueRect,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Rect: &documentv2.Rect{X: threeQuarters, Y: documentv2.Rational{Numerator: 0, Denominator: 1}, Width: half, Height: one}}}},
		},
		{
			Target: documentv2.KeyframeTargetAudioGain, ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &negative}}},
		},
		{
			Target: documentv2.KeyframeTargetAudioPan, ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &two}}},
		},
	}
	for index, lane := range tests {
		if _, err := EvaluateLane(lane, rt(0, 1)); !errors.Is(err, ErrKeyframeInvalid) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

func TestMalformedLaneFailsWithoutPanic(t *testing.T) {
	tests := []documentv2.KeyframeLane{
		{},
		{ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1)}}},
		{ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{
			{Offset: rt(1, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(0, 1)}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(1, 1)}},
		}},
	}
	for index, lane := range tests {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("case %d panicked: %v", index, recovered)
				}
			}()
			if _, err := EvaluateLane(lane, rt(0, 1)); !errors.Is(err, ErrKeyframeInvalid) {
				t.Fatalf("case %d error = %v", index, err)
			}
		}()
	}
}

func FuzzEvaluateLaneNeverPanics(f *testing.F) {
	f.Add(int64(0), int64(1), int64(0), int64(1))
	f.Add(int64(1), int64(3), int64(2), int64(5))
	f.Fuzz(func(t *testing.T, value, rate, numerator, denominator int64) {
		lane := documentv2.KeyframeLane{
			Target:    documentv2.KeyframeTargetTransformRotation,
			ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{{
				Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &documentv2.Rational{Numerator: numerator, Denominator: denominator}},
			}},
		}
		_, _ = EvaluateLane(lane, rt(value, rate))
	})
}

func cubicFixtureLane(t *testing.T) documentv2.KeyframeLane {
	t.Helper()
	var fixture struct {
		Profile string                  `json:"profile"`
		Lane    documentv2.KeyframeLane `json:"lane"`
		Samples []json.RawMessage       `json:"samples"`
	}
	decodeFixture(t, "keyframe-evaluator-v1.vectors.json", &fixture)
	return fixture.Lane
}

func assertScalar(t *testing.T, lane documentv2.KeyframeLane, at documentv2.RationalTime, want string) {
	t.Helper()
	actual, err := EvaluateLane(lane, at)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Scalar == nil || actual.Scalar.String() != want {
		t.Fatalf("at %v = %+v, want %s", at, actual.Scalar, want)
	}
}

func rationalPointer(numerator, denominator int64) *documentv2.Rational {
	return &documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}

func decodeFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := wirejson.Decode(raw, target); err != nil {
		t.Fatal(err)
	}
}

func exactFromStrings(t *testing.T, numerator, denominator string) ExactRational {
	t.Helper()
	n, ok := new(big.Int).SetString(numerator, 10)
	if !ok {
		t.Fatal("bad numerator")
	}
	d, ok := new(big.Int).SetString(denominator, 10)
	if !ok || d.Sign() <= 0 {
		t.Fatal("bad denominator")
	}
	return exactFromBig(new(big.Rat).SetFrac(n, d))
}
