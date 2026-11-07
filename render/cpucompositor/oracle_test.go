package cpucompositor

import (
	"errors"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/evaluator"
)

// TestRoundQuantizersMatchIndependentRationalOracle exhausts a dense small domain that includes
// exact half ties on both even and odd lower integers. The oracle compares rational distances to
// the adjacent integers; it deliberately does not duplicate the production quotient/remainder
// implementation.
func TestRoundQuantizersMatchIndependentRationalOracle(t *testing.T) {
	for denominator := int64(1); denominator <= 32; denominator++ {
		for numerator := int64(0); numerator <= denominator*128; numerator++ {
			n := big.NewInt(numerator)
			d := big.NewInt(denominator)
			want, wantOK := rationalRoundEvenOracle(n, d)
			got, err := roundUint16Even(n, d)
			if (err == nil) != wantOK || wantOK && got != want {
				t.Fatalf("big quantizer %d/%d = %d, err=%v; want %d, ok=%t", numerator, denominator, got, err, want, wantOK)
			}
			gotRatio, ratioErr := roundRatioUint16Even(uint64(numerator), uint64(denominator))
			if (ratioErr == nil) != wantOK || wantOK && gotRatio != want {
				t.Fatalf("uint64 quantizer %d/%d = %d, err=%v; want %d, ok=%t", numerator, denominator, gotRatio, ratioErr, want, wantOK)
			}
		}
	}

	for _, test := range []struct {
		numerator   int64
		denominator int64
		want        uint16
		wantOK      bool
	}{
		{numerator: 131069, denominator: 2, want: 65534, wantOK: true}, // 65534.5 -> even 65534
		{numerator: 131070, denominator: 2, want: 65535, wantOK: true},
		{numerator: 131071, denominator: 2, wantOK: false}, // 65535.5 -> 65536, fail closed
	} {
		got, err := roundUint16Even(big.NewInt(test.numerator), big.NewInt(test.denominator))
		if (err == nil) != test.wantOK || test.wantOK && got != test.want {
			t.Fatalf("boundary %d/%d = %d, err=%v; want %d, ok=%t", test.numerator, test.denominator, got, err, test.want, test.wantOK)
		}
	}
	if _, err := roundRatioUint16Even(1, 0); !errors.Is(err, ErrInvalidWeight) {
		t.Fatalf("zero denominator error = %v", err)
	}
}

func TestOpacityExhaustiveSmallDomainMatchesOracleAndPreservesPremultiplication(t *testing.T) {
	for denominator := int64(1); denominator <= 16; denominator++ {
		for numerator := int64(0); numerator <= denominator; numerator++ {
			weight := smallWeight(numerator, denominator)
			for alpha := uint16(0); alpha <= 31; alpha++ {
				for channel := uint16(0); channel <= alpha; channel++ {
					input := Pixel{R: channel, G: channel, B: channel, A: alpha}
					got, err := scalePixel(input, weight)
					if err != nil {
						t.Fatalf("scale %+v by %d/%d: %v", input, numerator, denominator, err)
					}
					wantChannel := mustOracleRound(t, productBig(uint64(channel), uint64(numerator)), big.NewInt(denominator))
					wantAlpha := mustOracleRound(t, productBig(uint64(alpha), uint64(numerator)), big.NewInt(denominator))
					want := Pixel{R: wantChannel, G: wantChannel, B: wantChannel, A: wantAlpha}
					if got != want || !got.valid() {
						t.Fatalf("scale %+v by %d/%d = %+v, want %+v", input, numerator, denominator, got, want)
					}
				}
			}
		}
	}
}

func TestDissolveExhaustiveSmallDomainMatchesOracleAndPreservesPremultiplication(t *testing.T) {
	for denominator := int64(1); denominator <= 8; denominator++ {
		for incomingNumerator := int64(0); incomingNumerator <= denominator; incomingNumerator++ {
			outgoingNumerator := denominator - incomingNumerator
			outgoingWeight := smallWeight(outgoingNumerator, denominator)
			incomingWeight := smallWeight(incomingNumerator, denominator)
			if !weightsSumToOne(outgoingWeight, incomingWeight) {
				t.Fatalf("complement rejected: %d/%d + %d/%d", outgoingNumerator, denominator, incomingNumerator, denominator)
			}
			for outgoingAlpha := uint16(0); outgoingAlpha <= 7; outgoingAlpha++ {
				for outgoingChannel := uint16(0); outgoingChannel <= outgoingAlpha; outgoingChannel++ {
					outgoing := Pixel{R: outgoingChannel, G: outgoingChannel, B: outgoingChannel, A: outgoingAlpha}
					for incomingAlpha := uint16(0); incomingAlpha <= 7; incomingAlpha++ {
						for incomingChannel := uint16(0); incomingChannel <= incomingAlpha; incomingChannel++ {
							incoming := Pixel{R: incomingChannel, G: incomingChannel, B: incomingChannel, A: incomingAlpha}
							got, err := dissolvePixel(outgoing, incoming, outgoingWeight, incomingWeight)
							if err != nil {
								t.Fatalf("dissolve %+v/%+v: %v", outgoing, incoming, err)
							}
							wantChannel := dissolveOracle(t, outgoingChannel, incomingChannel, outgoingNumerator, incomingNumerator, denominator)
							wantAlpha := dissolveOracle(t, outgoingAlpha, incomingAlpha, outgoingNumerator, incomingNumerator, denominator)
							want := Pixel{R: wantChannel, G: wantChannel, B: wantChannel, A: wantAlpha}
							if got != want || !got.valid() {
								t.Fatalf("dissolve %+v/%+v at %d/%d = %+v, want %+v", outgoing, incoming, incomingNumerator, denominator, got, want)
							}
						}
					}
				}
			}
		}
	}
}

func TestDissolveConsumesEvaluatorComplementaryWeightsWithoutFloat(t *testing.T) {
	outgoing := mustSurface(t, 1, 1, []Pixel{{R: 65535, G: 1, A: 65535}})
	incoming := mustSurface(t, 1, 1, []Pixel{{G: 65535, B: 2, A: 65535}})
	outgoingWeight := mustWeight(t, weightWire{Numerator: 1, Denominator: 3})
	incomingWeight := mustWeight(t, weightWire{Numerator: 2, Denominator: 3})
	got, err := Dissolve(outgoing, incoming, outgoingWeight, incomingWeight)
	if err != nil {
		t.Fatal(err)
	}
	want := Pixel{
		R: dissolveOracle(t, 65535, 0, 1, 2, 3),
		G: dissolveOracle(t, 1, 65535, 1, 2, 3),
		B: dissolveOracle(t, 0, 2, 1, 2, 3),
		A: 65535,
	}
	assertPixels(t, got, []Pixel{want})
}

func TestFullUint16OpacityAndDissolveMatchOracle(t *testing.T) {
	random := rand.New(rand.NewSource(709))
	for iteration := 0; iteration < 2000; iteration++ {
		outgoing := randomPixel(random)
		incoming := randomPixel(random)
		denominator := int64(random.Intn(65535) + 1)
		incomingNumerator := int64(random.Intn(int(denominator) + 1))
		outgoingNumerator := denominator - incomingNumerator

		scaled, err := scalePixel(outgoing, smallWeight(incomingNumerator, denominator))
		if err != nil {
			t.Fatalf("iteration %d opacity: %v", iteration, err)
		}
		wantScaled := Pixel{
			R: dissolveOracle(t, 0, outgoing.R, 0, incomingNumerator, denominator),
			G: dissolveOracle(t, 0, outgoing.G, 0, incomingNumerator, denominator),
			B: dissolveOracle(t, 0, outgoing.B, 0, incomingNumerator, denominator),
			A: dissolveOracle(t, 0, outgoing.A, 0, incomingNumerator, denominator),
		}
		if scaled != wantScaled || !scaled.valid() {
			t.Fatalf("iteration %d opacity %+v = %+v, want %+v", iteration, outgoing, scaled, wantScaled)
		}

		dissolved, err := dissolvePixel(
			outgoing,
			incoming,
			smallWeight(outgoingNumerator, denominator),
			smallWeight(incomingNumerator, denominator),
		)
		if err != nil {
			t.Fatalf("iteration %d dissolve: %v", iteration, err)
		}
		wantDissolved := Pixel{
			R: dissolveOracle(t, outgoing.R, incoming.R, outgoingNumerator, incomingNumerator, denominator),
			G: dissolveOracle(t, outgoing.G, incoming.G, outgoingNumerator, incomingNumerator, denominator),
			B: dissolveOracle(t, outgoing.B, incoming.B, outgoingNumerator, incomingNumerator, denominator),
			A: dissolveOracle(t, outgoing.A, incoming.A, outgoingNumerator, incomingNumerator, denominator),
		}
		if dissolved != wantDissolved || !dissolved.valid() {
			t.Fatalf("iteration %d dissolve %+v/%+v = %+v, want %+v", iteration, outgoing, incoming, dissolved, wantDissolved)
		}
	}
}

func TestSourceOverExhaustiveSmallDomainMatchesCompleteExpressionOracle(t *testing.T) {
	for sourceAlpha := uint16(0); sourceAlpha <= 31; sourceAlpha++ {
		for sourceChannel := uint16(0); sourceChannel <= sourceAlpha; sourceChannel++ {
			source := Pixel{R: sourceChannel, G: sourceChannel, B: sourceChannel, A: sourceAlpha}
			for destinationAlpha := uint16(0); destinationAlpha <= 31; destinationAlpha++ {
				for destinationChannel := uint16(0); destinationChannel <= destinationAlpha; destinationChannel++ {
					destination := Pixel{R: destinationChannel, G: destinationChannel, B: destinationChannel, A: destinationAlpha}
					got, err := sourceOverPixel(destination, source)
					if err != nil {
						t.Fatalf("source-over dst=%+v src=%+v: %v", destination, source, err)
					}
					wantChannel := sourceOverOracle(t, destinationChannel, sourceChannel, sourceAlpha)
					wantAlpha := sourceOverOracle(t, destinationAlpha, sourceAlpha, sourceAlpha)
					want := Pixel{R: wantChannel, G: wantChannel, B: wantChannel, A: wantAlpha}
					if got != want || !got.valid() {
						t.Fatalf("source-over dst=%+v src=%+v = %+v, want %+v", destination, source, got, want)
					}
				}
			}
		}
	}
}

func TestCanonicalPixelHashBoundaryRequiresManifestGeometryAndProfileBinding(t *testing.T) {
	pixels := []Pixel{{R: 1, A: 2}, {B: 3, A: 4}}
	horizontal := mustSurface(t, 2, 1, pixels)
	vertical := mustSurface(t, 1, 2, pixels)
	horizontalHash, err := horizontal.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	verticalHash, err := vertical.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if horizontalHash != verticalHash {
		t.Fatalf("pixel-byte hash unexpectedly included geometry: %s != %s", horizontalHash, verticalHash)
	}
	contract := Descriptor().CanonicalSerialization
	for _, required := range []string{"Width", "height", "surface profile ID", "kernel profile ID", "must be bound"} {
		if !strings.Contains(contract, required) {
			t.Fatalf("canonical binding contract omits %q: %s", required, contract)
		}
	}
}

func TestPublicFailuresExposeStableCodesAndPaths(t *testing.T) {
	assertKernelFailure(t, func() error {
		_, err := NewSurface(0, 1)
		return err
	}(), CodeInvalidDimensions, "surface", ErrInvalidDimensions)
	assertKernelFailure(t, func() error {
		_, err := NewSurfaceWithLimits(2, 1, Limits{MaxWidth: 1, MaxHeight: 1, MaxBytes: 8})
		return err
	}(), CodeResourceLimit, "surface.dimensions", ErrResourceLimit)
	assertKernelFailure(t, func() error {
		_, err := NewSurfaceFromPixels(1, 1, []Pixel{{R: 1}})
		return err
	}(), CodeInvalidPixel, "pixels/0", ErrInvalidPixel)

	valid := mustSurface(t, 1, 1, []Pixel{{}})
	assertKernelFailure(t, func() error {
		_, err := valid.Pixel(1, 0)
		return err
	}(), CodeBounds, "pixel", ErrBounds)
	assertKernelFailure(t, func() error {
		_, err := ApplyOpacity(valid, evaluator.ExactRational{})
		return err
	}(), CodeInvalidWeight, "opacity", ErrInvalidWeight)
	assertKernelFailure(t, func() error {
		_, err := Dissolve(valid, valid, mustWeight(t, weightWire{Numerator: 1, Denominator: 2}), mustWeight(t, weightWire{Numerator: 1, Denominator: 4}))
		return err
	}(), CodeInvalidWeight, "transition.weights", ErrInvalidWeight)
	mismatch := mustSurface(t, 2, 1, []Pixel{{}, {}})
	assertKernelFailure(t, func() error {
		_, err := CompositeOver(valid, mismatch)
		return err
	}(), CodeSurfaceMismatch, "surface", ErrSurfaceMismatch)
	assertKernelFailure(t, func() error {
		_, err := CompositeLayers(nil)
		return err
	}(), CodeNoLayers, "layers", ErrNoLayers)
}

func smallWeight(numerator, denominator int64) exactWeight {
	var result exactWeight
	result.numerator.SetInt64(numerator)
	result.denominator.SetInt64(denominator)
	return result
}

func productBig(left, right uint64) *big.Int {
	return new(big.Int).Mul(new(big.Int).SetUint64(left), new(big.Int).SetUint64(right))
}

func dissolveOracle(t *testing.T, outgoing, incoming uint16, outgoingNumerator, incomingNumerator, denominator int64) uint16 {
	t.Helper()
	numerator := new(big.Int).Add(
		productBig(uint64(outgoing), uint64(outgoingNumerator)),
		productBig(uint64(incoming), uint64(incomingNumerator)),
	)
	return mustOracleRound(t, numerator, big.NewInt(denominator))
}

func sourceOverOracle(t *testing.T, destination, source, sourceAlpha uint16) uint16 {
	t.Helper()
	numerator := new(big.Int).Add(
		productBig(uint64(source), mathMaxUint16),
		productBig(uint64(destination), mathMaxUint16-uint64(sourceAlpha)),
	)
	return mustOracleRound(t, numerator, new(big.Int).SetUint64(mathMaxUint16))
}

func mustOracleRound(t *testing.T, numerator, denominator *big.Int) uint16 {
	t.Helper()
	value, ok := rationalRoundEvenOracle(numerator, denominator)
	if !ok {
		t.Fatalf("oracle result out of uint16 range: %s/%s", numerator, denominator)
	}
	return value
}

func rationalRoundEvenOracle(numerator, denominator *big.Int) (uint16, bool) {
	if numerator == nil || denominator == nil || numerator.Sign() < 0 || denominator.Sign() <= 0 {
		return 0, false
	}
	value := new(big.Rat).SetFrac(new(big.Int).Set(numerator), new(big.Int).Set(denominator))
	lowerInteger := new(big.Int).Quo(value.Num(), value.Denom())
	lower := new(big.Rat).SetInt(new(big.Int).Set(lowerInteger))
	upperInteger := new(big.Int).Add(new(big.Int).Set(lowerInteger), big.NewInt(1))
	upper := new(big.Rat).SetInt(new(big.Int).Set(upperInteger))
	lowerDistance := new(big.Rat).Sub(new(big.Rat).Set(value), lower)
	upperDistance := new(big.Rat).Sub(upper, value)
	result := new(big.Int).Set(lowerInteger)
	comparison := lowerDistance.Cmp(upperDistance)
	if comparison > 0 || comparison == 0 && result.Bit(0) == 1 {
		result.Set(upperInteger)
	}
	if !result.IsUint64() || result.Uint64() > mathMaxUint16 {
		return 0, false
	}
	return uint16(result.Uint64()), true
}

func assertKernelFailure(t *testing.T, err error, code ErrorCode, path string, cause error) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not wrap %v", err, cause)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v has no stable kernel identity", err)
	}
	if typed.Code != code || typed.Path != path {
		t.Fatalf("error identity = %s at %q, want %s at %q", typed.Code, typed.Path, code, path)
	}
}
