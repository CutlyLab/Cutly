package cpucompositor

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

type weightWire struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

type goldenCase struct {
	Name           string      `json:"name"`
	Operation      string      `json:"operation"`
	Source         []Pixel     `json:"source,omitempty"`
	Destination    []Pixel     `json:"destination,omitempty"`
	Outgoing       []Pixel     `json:"outgoing,omitempty"`
	Incoming       []Pixel     `json:"incoming,omitempty"`
	Layers         [][]Pixel   `json:"layers,omitempty"`
	Weight         *weightWire `json:"weight,omitempty"`
	OutgoingWeight *weightWire `json:"outgoing_weight,omitempty"`
	IncomingWeight *weightWire `json:"incoming_weight,omitempty"`
	Expected       []Pixel     `json:"expected"`
}

type goldenVectors struct {
	Profile string       `json:"profile"`
	Width   int          `json:"width"`
	Height  int          `json:"height"`
	Cases   []goldenCase `json:"cases"`
}

func TestCanonicalGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/kernel-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture goldenVectors
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Profile != KernelProfileV1 || len(fixture.Cases) == 0 {
		t.Fatalf("incomplete fixture: %+v", fixture)
	}
	canonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		t.Fatalf("golden vectors are not canonical\nwant %s\n got %s", canonical, bytes.TrimSpace(raw))
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			var output *Surface
			var operationErr error
			switch test.Operation {
			case "opacity":
				output, operationErr = ApplyOpacity(mustSurface(t, fixture.Width, fixture.Height, test.Source), mustWeight(t, *test.Weight))
			case "dissolve":
				output, operationErr = Dissolve(
					mustSurface(t, fixture.Width, fixture.Height, test.Outgoing),
					mustSurface(t, fixture.Width, fixture.Height, test.Incoming),
					mustWeight(t, *test.OutgoingWeight), mustWeight(t, *test.IncomingWeight),
				)
			case "source_over":
				output, operationErr = CompositeOver(
					mustSurface(t, fixture.Width, fixture.Height, test.Destination),
					mustSurface(t, fixture.Width, fixture.Height, test.Source),
				)
			case "layers":
				layers := make([]*Surface, len(test.Layers))
				for index := range test.Layers {
					layers[index] = mustSurface(t, fixture.Width, fixture.Height, test.Layers[index])
				}
				output, operationErr = CompositeLayers(layers)
			default:
				t.Fatalf("unknown operation %q", test.Operation)
			}
			if operationErr != nil {
				t.Fatal(operationErr)
			}
			assertPixels(t, output, test.Expected)
		})
	}
}

func TestOperationsAreAliasSafeAndFailBeforePublishing(t *testing.T) {
	source := mustSurface(t, 1, 1, []Pixel{{R: 1, G: 3, B: 4, A: 5}})
	var before bytes.Buffer
	if err := source.WriteCanonical(&before); err != nil {
		t.Fatal(err)
	}
	opacity, err := ApplyOpacity(source, mustWeight(t, weightWire{1, 2}))
	if err != nil {
		t.Fatal(err)
	}
	if opacity == source {
		t.Fatal("opacity reused input surface")
	}
	dissolved, err := Dissolve(source, source, mustWeight(t, weightWire{1, 2}), mustWeight(t, weightWire{1, 2}))
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, dissolved, []Pixel{{R: 1, G: 3, B: 4, A: 5}})
	composited, err := CompositeLayers([]*Surface{source, source})
	if err != nil {
		t.Fatal(err)
	}
	if composited == source {
		t.Fatal("layers reused input surface")
	}
	var after bytes.Buffer
	if err := source.WriteCanonical(&after); err != nil || !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatalf("operation mutated input: %x != %x err=%v", before.Bytes(), after.Bytes(), err)
	}

	other := mustSurface(t, 2, 1, []Pixel{{}, {}})
	if output, err := CompositeOver(source, other); output != nil || !errors.Is(err, ErrSurfaceMismatch) {
		t.Fatalf("mismatch output=%v err=%v", output, err)
	}
	if output, err := CompositeLayers(nil); output != nil || !errors.Is(err, ErrNoLayers) {
		t.Fatalf("empty layers output=%v err=%v", output, err)
	}
	if output, err := ApplyOpacity(source, evaluator.ExactRational{}); output != nil || !errors.Is(err, ErrInvalidWeight) {
		t.Fatalf("invalid opacity output=%v err=%v", output, err)
	}
	for _, invalid := range []evaluator.ExactRational{mustAnyWeight(t, 2, 1), mustAnyWeight(t, -1, 1)} {
		if output, err := ApplyOpacity(source, invalid); output != nil || !errors.Is(err, ErrInvalidWeight) {
			t.Fatalf("out-of-range opacity output=%v err=%v", output, err)
		}
	}
	if output, err := Dissolve(source, source, mustWeight(t, weightWire{1, 2}), mustWeight(t, weightWire{1, 4})); output != nil || !errors.Is(err, ErrInvalidWeight) {
		t.Fatalf("non-complementary dissolve output=%v err=%v", output, err)
	}
}

func TestEvaluatorArbitraryPrecisionWeightFeedsKernelWithoutFloat(t *testing.T) {
	one := documentv2.Rational{Numerator: 1, Denominator: 1}
	zero := documentv2.Rational{Numerator: 0, Denominator: 1}
	lane := documentv2.KeyframeLane{
		Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &zero}, Interpolation: documentv2.InterpolationCubic,
				Easing: &documentv2.CubicBezier{X1: documentv2.Rational{Numerator: 1, Denominator: 4}, Y1: zero, X2: documentv2.Rational{Numerator: 3, Denominator: 4}, Y2: one}},
			{Offset: rt(1, 1), Value: documentv2.KeyframeValue{Scalar: &one}},
		},
	}
	evaluated, err := evaluator.EvaluateLane(lane, rt(1, 3))
	if err != nil || evaluated.Scalar == nil {
		t.Fatalf("evaluate cubic: value=%+v err=%v", evaluated, err)
	}
	if evaluated.Scalar.Denominator().IsInt64() {
		t.Fatalf("test did not exercise arbitrary precision: %s", evaluated.Scalar)
	}
	source := mustSurface(t, 1, 1, []Pixel{{R: 1234, G: 2345, B: 3456, A: 4567}})
	first, err := ApplyOpacity(source, *evaluated.Scalar)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyOpacity(source, *evaluated.Scalar)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, _ := first.CanonicalHash()
	secondHash, _ := second.CanonicalHash()
	if firstHash != secondHash {
		t.Fatalf("arbitrary-precision result drift: %s != %s", firstHash, secondHash)
	}
}

func TestSourceOverPreservesPremultipliedInvariant(t *testing.T) {
	opaque := Pixel{R: 65535, G: 65535, B: 65535, A: 65535}
	transparent := Pixel{}
	if got, err := sourceOverPixel(opaque, opaque); err != nil || got != opaque {
		t.Fatalf("opaque extreme wrapped: %+v", got)
	}
	if got, err := sourceOverPixel(opaque, transparent); err != nil || got != opaque {
		t.Fatalf("transparent source changed destination: %+v", got)
	}
	if got, err := sourceOverPixel(transparent, opaque); err != nil || got != opaque {
		t.Fatalf("opaque source changed at extreme: %+v", got)
	}
	random := rand.New(rand.NewSource(1))
	for iteration := 0; iteration < 10000; iteration++ {
		destination := randomPixel(random)
		source := randomPixel(random)
		result, err := sourceOverPixel(destination, source)
		want := Pixel{
			R: sourceOverOracle(t, destination.R, source.R, source.A),
			G: sourceOverOracle(t, destination.G, source.G, source.A),
			B: sourceOverOracle(t, destination.B, source.B, source.A),
			A: sourceOverOracle(t, destination.A, source.A, source.A),
		}
		if err != nil || !result.valid() || result != want {
			t.Fatalf("iteration %d: dst=%+v src=%+v result=%+v want=%+v err=%v", iteration, destination, source, result, want, err)
		}
	}
}

func TestImmutableSurfaceIsConcurrentReadSafe(t *testing.T) {
	surface := mustSurface(t, 2, 1, []Pixel{{R: 100, G: 200, B: 300, A: 400}, {R: 500, G: 600, B: 700, A: 800}})
	half := mustWeight(t, weightWire{Numerator: 1, Denominator: 2})
	wantHash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	errorsOut := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			hash, hashErr := surface.CanonicalHash()
			if hashErr != nil {
				errorsOut <- hashErr
				return
			}
			if hash != wantHash {
				errorsOut <- errors.New("concurrent canonical hash drift")
				return
			}
			first, firstErr := ApplyOpacity(surface, half)
			if firstErr != nil {
				errorsOut <- firstErr
				return
			}
			second, secondErr := ApplyOpacity(surface, half)
			if secondErr != nil {
				errorsOut <- secondErr
				return
			}
			firstHash, firstHashErr := first.CanonicalHash()
			secondHash, secondHashErr := second.CanonicalHash()
			if firstHashErr != nil || secondHashErr != nil {
				errorsOut <- errors.New("concurrent output hash failed")
				return
			}
			if firstHash != secondHash {
				errorsOut <- errors.New("concurrent operation drift")
			}
		}()
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func FuzzKernelNeverPanics(f *testing.F) {
	f.Add(uint16(1), uint16(2), uint16(3), uint16(4), int64(1), int64(2))
	f.Add(uint16(65535), uint16(65535), uint16(65535), uint16(65535), int64(0), int64(1))
	f.Fuzz(func(t *testing.T, r, g, b, a uint16, numerator, denominator int64) {
		pixel := Pixel{R: r, G: g, B: b, A: a}
		surface, _ := NewSurfaceFromPixels(1, 1, []Pixel{pixel})
		if surface == nil {
			return
		}
		weight, ok := tryWeight(numerator, denominator)
		if !ok {
			return
		}
		_, _ = ApplyOpacity(surface, weight)
		complement, complementOK := tryWeight(denominator-numerator, denominator)
		if complementOK {
			_, _ = Dissolve(surface, surface, weight, complement)
		}
		_, _ = CompositeOver(surface, surface)
		_, _ = CompositeLayers([]*Surface{surface, surface})
		_, _ = surface.CanonicalHash()
	})
}

func mustSurface(t *testing.T, width, height int, pixels []Pixel) *Surface {
	t.Helper()
	surface, err := NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

func mustWeight(t *testing.T, value weightWire) evaluator.ExactRational {
	t.Helper()
	result, ok := tryWeight(value.Numerator, value.Denominator)
	if !ok {
		t.Fatalf("invalid fixture weight %+v", value)
	}
	return result
}

func tryWeight(numerator, denominator int64) (evaluator.ExactRational, bool) {
	if denominator <= 0 || numerator < 0 || numerator > denominator {
		return evaluator.ExactRational{}, false
	}
	rational := documentv2.Rational{Numerator: numerator, Denominator: denominator}
	if rational.Validate() != nil {
		return evaluator.ExactRational{}, false
	}
	lane := documentv2.KeyframeLane{
		Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &rational}}},
	}
	value, err := evaluator.EvaluateLane(lane, rt(0, 1))
	if err != nil || value.Scalar == nil {
		return evaluator.ExactRational{}, false
	}
	return *value.Scalar, true
}

func mustAnyWeight(t *testing.T, numerator, denominator int64) evaluator.ExactRational {
	t.Helper()
	rational := documentv2.Rational{Numerator: numerator, Denominator: denominator}
	if rational.Validate() != nil {
		t.Fatalf("invalid exact test rational %d/%d", numerator, denominator)
	}
	lane := documentv2.KeyframeLane{
		Target: documentv2.KeyframeTargetTransformRotation, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &rational}}},
	}
	value, err := evaluator.EvaluateLane(lane, rt(0, 1))
	if err != nil || value.Scalar == nil {
		t.Fatalf("exact test rational: value=%+v err=%v", value, err)
	}
	return *value.Scalar
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}

func assertPixels(t *testing.T, surface *Surface, expected []Pixel) {
	t.Helper()
	if surface == nil || len(surface.pixels) != len(expected) {
		t.Fatalf("surface=%v pixels=%d, want %d", surface, len(surface.pixels), len(expected))
	}
	for index := range expected {
		if surface.pixels[index] != expected[index] {
			t.Fatalf("pixel %d = %+v, want %+v", index, surface.pixels[index], expected[index])
		}
	}
}

func randomPixel(random *rand.Rand) Pixel {
	alpha := uint16(random.Intn(1 << 16))
	channel := func() uint16 { return uint16(random.Intn(int(alpha) + 1)) }
	return Pixel{R: channel(), G: channel(), B: channel(), A: alpha}
}
