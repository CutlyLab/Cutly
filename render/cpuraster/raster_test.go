package cpuraster

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestRasterizeIdentityIsPixelExact(t *testing.T) {
	source := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 1, G: 2, B: 3, A: 4}, {R: 100, G: 200, B: 300, A: 400},
		{R: 500, G: 600, B: 700, A: 800}, {R: 900, G: 1000, B: 1100, A: 1200},
	})
	transform, crop := evaluatedGeometry(t, nil, nil)
	result, err := Rasterize(context.Background(), source, 2, 2, transform, crop, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result, []cpucompositor.Pixel{
		{R: 1, G: 2, B: 3, A: 4}, {R: 100, G: 200, B: 300, A: 400},
		{R: 500, G: 600, B: 700, A: 800}, {R: 900, G: 1000, B: 1100, A: 1200},
	})
	if sourceHash, _ := source.CanonicalHash(); sourceHash == "" {
		t.Fatal("source hash is empty")
	} else if resultHash, hashErr := result.CanonicalHash(); hashErr != nil || resultHash != sourceHash {
		t.Fatalf("identity hash = %q, %v; want %q", resultHash, hashErr, sourceHash)
	}
}

func TestRasterizeTranslationBilinearCropMirrorAndDegenerateScale(t *testing.T) {
	red := cpucompositor.Pixel{R: 65535, A: 65535}
	green := cpucompositor.Pixel{G: 65535, A: 65535}
	source := testSurface(t, 2, 1, []cpucompositor.Pixel{red, green})

	t.Run("integer translation", func(t *testing.T) {
		transform, crop := evaluatedGeometry(t, &documentv2.TransformProperty{
			Position: point(rat(1, 1), rat(0, 1)), Scale: point(rat(1, 1), rat(1, 1)),
			RotationDegrees: rat(0, 1), Anchor: point(rat(1, 2), rat(1, 2)),
		}, nil)
		result, err := Rasterize(context.Background(), source, 3, 1, transform, crop, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		assertPixels(t, result, []cpucompositor.Pixel{{}, red, green})
	})

	t.Run("half-pixel bilinear", func(t *testing.T) {
		one := testSurface(t, 1, 1, []cpucompositor.Pixel{{R: 65535, G: 32768, A: 65535}})
		transform, crop := evaluatedGeometry(t, &documentv2.TransformProperty{
			Position: point(rat(1, 2), rat(0, 1)), Scale: point(rat(1, 1), rat(1, 1)),
			RotationDegrees: rat(0, 1), Anchor: point(rat(1, 2), rat(1, 2)),
		}, nil)
		result, err := Rasterize(context.Background(), one, 2, 1, transform, crop, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		assertPixels(t, result, []cpucompositor.Pixel{
			{R: 32768, G: 16384, A: 32768}, {R: 32768, G: 16384, A: 32768},
		})
	})

	t.Run("crop", func(t *testing.T) {
		transform, crop := evaluatedGeometry(t, nil, &documentv2.CropProperty{Rect: documentv2.Rect{
			X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 2), Height: rat(1, 1),
		}})
		result, err := Rasterize(context.Background(), source, 2, 1, transform, crop, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		assertPixels(t, result, []cpucompositor.Pixel{red, {}})
	})

	for _, test := range []struct {
		name     string
		scaleX   documentv2.Rational
		rotation documentv2.Rational
		want     []cpucompositor.Pixel
	}{
		{name: "negative scale mirror", scaleX: rat(-1, 1), rotation: rat(0, 1), want: []cpucompositor.Pixel{green, red}},
		{name: "180 degree rotation", scaleX: rat(1, 1), rotation: rat(180, 1), want: []cpucompositor.Pixel{green, red}},
		{name: "zero scale transparent", scaleX: rat(0, 1), rotation: rat(0, 1), want: []cpucompositor.Pixel{{}, {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, crop := evaluatedGeometry(t, &documentv2.TransformProperty{
				Position: point(rat(0, 1), rat(0, 1)), Scale: point(test.scaleX, rat(1, 1)),
				RotationDegrees: test.rotation, Anchor: point(rat(1, 2), rat(1, 2)),
			}, nil)
			result, err := Rasterize(context.Background(), source, 2, 1, transform, crop, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			assertPixels(t, result, test.want)
		})
	}
}

func TestRasterizeClockwiseQuarterTurnInYDownCoordinates(t *testing.T) {
	source := testSurface(t, 2, 3, []cpucompositor.Pixel{
		{R: 1, A: 10}, {R: 2, A: 10},
		{R: 3, A: 10}, {R: 4, A: 10},
		{R: 5, A: 10}, {R: 6, A: 10},
	})
	// Source center is (1,3/2), target center is (3/2,1), so position=(1/2,-1/2)
	// aligns the rotated footprint to the 3x2 target. Positive rotation is clockwise in y-down.
	transform, crop := evaluatedGeometry(t, &documentv2.TransformProperty{
		Position:        point(rat(1, 2), rat(-1, 2)),
		Scale:           point(rat(1, 1), rat(1, 1)),
		RotationDegrees: rat(90, 1),
		Anchor:          point(rat(1, 2), rat(1, 2)),
	}, nil)
	result, err := Rasterize(context.Background(), source, 3, 2, transform, crop, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result, []cpucompositor.Pixel{
		{R: 5, A: 10}, {R: 3, A: 10}, {R: 1, A: 10},
		{R: 6, A: 10}, {R: 4, A: 10}, {R: 2, A: 10},
	})
}

func TestRasterizeOwnsOutputAndIsConcurrentDeterministic(t *testing.T) {
	source := testSurface(t, 3, 2, []cpucompositor.Pixel{
		{R: 1, A: 2}, {G: 3, A: 4}, {B: 5, A: 6},
		{R: 7, A: 8}, {G: 9, A: 10}, {B: 11, A: 12},
	})
	transform, crop := evaluatedGeometry(t, &documentv2.TransformProperty{
		Position: point(rat(1, 3), rat(-1, 5)), Scale: point(rat(3, 2), rat(4, 3)),
		RotationDegrees: rat(17, 3), Anchor: point(rat(1, 3), rat(2, 3)),
	}, nil)
	want, err := Rasterize(context.Background(), source, 4, 3, transform, crop, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := want.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var group sync.WaitGroup
	errorsSeen := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			got, rasterErr := Rasterize(context.Background(), source, 4, 3, transform, crop, Limits{})
			if rasterErr != nil {
				errorsSeen <- rasterErr
				return
			}
			hash, hashErr := got.CanonicalHash()
			if hashErr != nil || hash != wantHash {
				errorsSeen <- errors.New("concurrent raster differs")
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for concurrentErr := range errorsSeen {
		t.Error(concurrentErr)
	}
}

func TestRasterizeLimitsInvalidInputAndCancellation(t *testing.T) {
	source := testSurface(t, 1, 1, []cpucompositor.Pixel{{A: 1}})
	transform, crop := evaluatedGeometry(t, nil, nil)
	if result, err := Rasterize(nil, source, 1, 1, transform, crop, Limits{}); result != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context result/error = %v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Rasterize(ctx, source, 1, 1, transform, crop, Limits{}); result != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result/error = %v, %v", result, err)
	}
	largePixels := make([]cpucompositor.Pixel, 9*9)
	for index := range largePixels {
		largePixels[index] = cpucompositor.Pixel{A: 1}
	}
	large := testSurface(t, 9, 9, largePixels)
	midFrame := &cancelAfterChecksContext{cancelAt: 4}
	if result, err := Rasterize(midFrame, large, 9, 9, transform, crop, Limits{}); result != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("mid-frame canceled result/error = %v, %v", result, err)
	}
	if result, err := Rasterize(context.Background(), nil, 1, 1, transform, crop, Limits{}); result != nil || !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("nil source result/error = %v, %v", result, err)
	}
	if result, err := Rasterize(context.Background(), source, 1, 1, evaluator.Transform{}, crop, Limits{}); result != nil || !errors.Is(err, ErrGeometryInvalid) {
		t.Fatalf("zero transform result/error = %v, %v", result, err)
	}
	if result, err := Rasterize(context.Background(), source, 1, 1, transform, evaluator.ExactRect{}, Limits{}); result != nil || !errors.Is(err, ErrGeometryInvalid) {
		t.Fatalf("zero crop result/error = %v, %v", result, err)
	}
	if result, err := Rasterize(context.Background(), source, 1, 1, transform, crop, Limits{
		MaxWidth: 1, MaxHeight: 1, MaxOutputPixels: 1, MaxWorkingBytes: 16, MaxExactBits: 1,
	}); result != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("exact-bit limit result/error = %v, %v", result, err)
	}
	for _, test := range []struct {
		name   string
		width  int
		height int
		limits Limits
	}{
		{name: "zero output", width: 0, height: 1, limits: DefaultLimits()},
		{name: "pixel bound", width: 2, height: 1, limits: Limits{MaxWidth: 2, MaxHeight: 1, MaxOutputPixels: 1, MaxWorkingBytes: 32, MaxExactBits: 512}},
		{name: "working bound", width: 2, height: 1, limits: Limits{MaxWidth: 2, MaxHeight: 1, MaxOutputPixels: 2, MaxWorkingBytes: 31, MaxExactBits: 512}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Rasterize(context.Background(), source, test.width, test.height, transform, crop, test.limits)
			if result != nil || err == nil {
				t.Fatalf("result/error = %v, %v", result, err)
			}
		})
	}
}

func TestQuantizedRotationUsesTrueMatrixInverse(t *testing.T) {
	degrees := new(big.Rat).SetFrac64(17, 3)
	sine, cosine, err := sinCosQ48(degrees)
	if err != nil {
		t.Fatal(err)
	}
	determinant := new(big.Rat).Mul(cosine, cosine)
	determinant.Add(determinant, new(big.Rat).Mul(sine, sine))
	if determinant.Cmp(ratOne) == 0 {
		t.Fatal("chosen Q48 angle unexpectedly has exact unit determinant; regression would be ineffective")
	}
	x, y := new(big.Rat).SetFrac64(7, 5), new(big.Rat).SetFrac64(-11, 9)
	forwardX := new(big.Rat).Sub(new(big.Rat).Mul(cosine, x), new(big.Rat).Mul(sine, y))
	forwardY := new(big.Rat).Add(new(big.Rat).Mul(sine, x), new(big.Rat).Mul(cosine, y))
	inverseX := new(big.Rat).Add(new(big.Rat).Mul(cosine, forwardX), new(big.Rat).Mul(sine, forwardY))
	inverseX.Quo(inverseX, determinant)
	inverseY := new(big.Rat).Add(new(big.Rat).Mul(new(big.Rat).Neg(new(big.Rat).Set(sine)), forwardX), new(big.Rat).Mul(cosine, forwardY))
	inverseY.Quo(inverseY, determinant)
	if inverseX.Cmp(x) != 0 || inverseY.Cmp(y) != 0 {
		t.Fatalf("matrix inverse = %s,%s; want %s,%s", inverseX, inverseY, x, y)
	}
}

func TestChannelQuantizerUsesNearestEven(t *testing.T) {
	for _, test := range []struct {
		value *big.Rat
		want  uint16
	}{
		{big.NewRat(1, 2), 0}, {big.NewRat(3, 2), 2}, {big.NewRat(5, 2), 2},
		{big.NewRat(7, 2), 4}, {big.NewRat(65535, 1), 65535},
	} {
		got, err := roundChannel(test.value)
		if err != nil || got != test.want {
			t.Fatalf("roundChannel(%s) = %d, %v; want %d", test.value, got, err, test.want)
		}
	}
	for _, value := range []*big.Rat{big.NewRat(-1, 1), big.NewRat(65536, 1)} {
		if _, err := roundChannel(value); !errors.Is(err, ErrSampleFailed) {
			t.Fatalf("roundChannel(%s) error = %v", value, err)
		}
	}
}

func TestTrigProfileKnownAnglesAndPeriodReduction(t *testing.T) {
	tests := []struct {
		degrees string
		sine    float64
		cosine  float64
	}{
		{"0", 0, 1}, {"90", 1, 0}, {"-90", -1, 0}, {"180", 0, -1},
		{"360", 0, 1}, {"45", math.Sqrt(0.5), math.Sqrt(0.5)},
		{"3600000000045", math.Sqrt(0.5), math.Sqrt(0.5)},
	}
	for _, test := range tests {
		degrees, ok := new(big.Rat).SetString(test.degrees)
		if !ok {
			t.Fatal(test.degrees)
		}
		sine, cosine, err := sinCosQ48(degrees)
		if err != nil {
			t.Fatal(err)
		}
		sineFloat, _ := sine.Float64()
		cosineFloat, _ := cosine.Float64()
		const tolerance = 1.0 / (1 << 47)
		if math.Abs(sineFloat-test.sine) > tolerance || math.Abs(cosineFloat-test.cosine) > tolerance {
			t.Fatalf("angle %s = sin %.17g cos %.17g; want %.17g %.17g", test.degrees, sineFloat, cosineFloat, test.sine, test.cosine)
		}
	}
}

func evaluatedGeometry(t *testing.T, transform *documentv2.TransformProperty, crop *documentv2.CropProperty) (evaluator.Transform, evaluator.ExactRect) {
	t.Helper()
	propertySet := &documentv2.PropertySet{Transform: transform, Crop: crop}
	if transform == nil && crop == nil {
		propertySet = nil
	}
	clip := documentv2.Clip{
		ID: "clip", AssetID: "image", PropertySet: propertySet,
		TimelineRange: documentv2.TimeRange{Start: timeValue(0), Duration: timeValue(1)},
		SourceRange:   documentv2.TimeRange{Start: timeValue(0), Duration: timeValue(1)},
	}
	properties, err := evaluator.EvaluateProperties(documentv2.TrackKindVideo, clip, documentv2.Asset{
		ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png",
	}, timeValue(0))
	if err != nil {
		t.Fatal(err)
	}
	if properties.Transform == nil || properties.Crop == nil {
		t.Fatalf("missing visual geometry: %+v", properties)
	}
	return *properties.Transform, *properties.Crop
}

func testSurface(t *testing.T, width, height int, pixels []cpucompositor.Pixel) *cpucompositor.Surface {
	t.Helper()
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

func assertPixels(t *testing.T, surface *cpucompositor.Surface, want []cpucompositor.Pixel) {
	t.Helper()
	if surface == nil || surface.Width()*surface.Height() != len(want) {
		t.Fatalf("surface dimensions/pixels = %v, want %d", surface, len(want))
	}
	for index, expected := range want {
		got, err := surface.Pixel(index%surface.Width(), index/surface.Width())
		if err != nil || got != expected {
			t.Fatalf("pixel %d = %+v, %v; want %+v", index, got, err, expected)
		}
	}
}

func rat(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func point(x, y documentv2.Rational) documentv2.Point { return documentv2.Point{X: x, Y: y} }

func timeValue(value int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: 1}
}

type cancelAfterChecksContext struct {
	calls    atomic.Int64
	cancelAt int64
}

func (value *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *cancelAfterChecksContext) Done() <-chan struct{}       { return nil }
func (value *cancelAfterChecksContext) Value(any) any               { return nil }
func (value *cancelAfterChecksContext) Err() error {
	if value.calls.Add(1) >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
