package cpuraster

import (
	"context"
	"math/rand"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// fastGeometry builds one evaluated transform/crop pair from document-shaped rationals through
// the real property evaluator.
func fastGeometry(t *testing.T, random *rand.Rand, width, height int) (evaluator.Transform, evaluator.ExactRect, bool) {
	t.Helper()
	rational := func(numerator, denominator int64) documentv2.Rational {
		value := documentv2.Rational{Numerator: numerator, Denominator: denominator}
		if value.Validate() != nil {
			return documentv2.Rational{Numerator: 0, Denominator: 1}
		}
		return value
	}
	// Most iterations stay near axis-aligned (always admitted); a quarter exercise rotations,
	// whose rational sine/cosine often exceed the plane bound and correctly fall back.
	rotation := documentv2.Rational{Numerator: 0, Denominator: 1}
	if random.Intn(4) == 0 {
		rotation = rational(int64(random.Intn(1440)-720), 4)
	}
	clip := documentv2.Clip{
		ID: "clip", AssetID: "image",
		TimelineRange: documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}},
		SourceRange:   documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}},
		PropertySet: &documentv2.PropertySet{Transform: &documentv2.TransformProperty{
			Position:        documentv2.Point{X: rational(int64(random.Intn(9)-4), int64(1+random.Intn(6))), Y: rational(int64(random.Intn(9)-4), int64(1+random.Intn(6)))},
			Scale:           documentv2.Point{X: rational(int64(1+random.Intn(5)), int64(1+random.Intn(4))), Y: rational(int64(1+random.Intn(5)), int64(1+random.Intn(4)))},
			RotationDegrees: rotation,
			Anchor:          documentv2.Point{X: rational(int64(random.Intn(5)), 4), Y: rational(int64(random.Intn(5)), 4)},
		}},
	}
	properties, err := evaluator.EvaluateProperties(documentv2.TrackKindVideo, clip, documentv2.Asset{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png"}, documentv2.RationalTime{Value: 0, Rate: 1})
	if err != nil {
		return evaluator.Transform{}, evaluator.ExactRect{}, false
	}
	// The evaluated default crop covers the complete source; the randomized transforms alone
	// exercise every plane arithmetic path, and the crop tap-bounds are identical int64
	// comparisons in both samplers.
	return *properties.Transform, *properties.Crop, true
}

// The fixed-width sampler must be pixel-identical to the big.Int sampler for every geometry it
// admits. The differential drives thousands of randomized transforms through both samplers and
// compares every sampled pixel.
func TestFastSamplerMatchesExactSampler(t *testing.T) {
	random := rand.New(rand.NewSource(20261112))
	const width, height = 12, 9
	source := fastFixtureSurface(t, width, height)
	view, err := source.View()
	if err != nil {
		t.Fatal(err)
	}
	admitted := 0
	for iteration := 0; iteration < 1500; iteration++ {
		transform, crop, ok := fastGeometry(t, random, width, height)
		if !ok {
			continue
		}
		geometry, err := compileGeometry(width, height, transform, crop, DefaultLimits().MaxExactBits)
		if err != nil {
			continue
		}
		if geometry.isPixelIdentity(view, width, height) || geometry.scaleX.Sign() == 0 || geometry.scaleY.Sign() == 0 {
			continue
		}
		exact, err := newExactSampler(geometry, width, height)
		if err != nil {
			continue
		}
		fast := newFastSamplerFromExact(exact, width, height)
		if fast == nil {
			continue
		}
		admitted++
		exact.beginRow()
		fast.beginRow()
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				exactPixel, exactErr := exact.sample(view)
				fastPixel, fastErr := fast.sample(view)
				if (exactErr == nil) != (fastErr == nil) {
					t.Fatalf("iteration %d pixel %d,%d error divergence: exact=%v fast=%v", iteration, x, y, exactErr, fastErr)
				}
				if exactErr == nil && exactPixel != fastPixel {
					t.Fatalf("iteration %d pixel %d,%d: exact=%+v fast=%+v transform=%+v crop=%+v", iteration, x, y, exactPixel, fastPixel, transform, crop)
				}
				exact.advanceColumn()
				fast.advanceColumn()
			}
			exact.advanceRow()
			fast.advanceRow()
		}
	}
	if admitted < 600 {
		t.Fatalf("only %d iterations admitted the fast sampler; the differential is too thin", admitted)
	}
}

// The full rasterization with the fast sampler admitted must be byte-identical to forcing the
// big.Int sampler through the same geometry.
func TestRasterizeFastPathByteIdentical(t *testing.T) {
	random := rand.New(rand.NewSource(20261113))
	for iteration := 0; iteration < 60; iteration++ {
		width, height := 10+random.Intn(8), 7+random.Intn(6)
		source := fastFixtureSurface(t, width, height)
		view, err := source.View()
		if err != nil {
			t.Fatal(err)
		}
		transform, crop, ok := fastGeometry(t, random, width, height)
		if !ok {
			continue
		}
		geometry, err := compileGeometry(width, height, transform, crop, DefaultLimits().MaxExactBits)
		if err != nil || geometry.isPixelIdentity(view, 16, 12) || geometry.scaleX.Sign() == 0 || geometry.scaleY.Sign() == 0 {
			continue
		}
		fastPixels := make([]cpucompositor.Pixel, 16*12)
		exactPixels := make([]cpucompositor.Pixel, 16*12)
		if err := rasterPixels(context.Background(), view, 16, 12, geometry, fastPixels, false); err != nil {
			t.Fatalf("iteration %d fast: %v", iteration, err)
		}
		if err := rasterPixels(context.Background(), view, 16, 12, geometry, exactPixels, true); err != nil {
			t.Fatalf("iteration %d exact: %v", iteration, err)
		}
		for index := range fastPixels {
			if fastPixels[index] != exactPixels[index] {
				t.Fatalf("iteration %d pixel %d diverged: %+v vs %+v", iteration, index, fastPixels[index], exactPixels[index])
			}
		}
	}
}

func fastFixtureSurface(t *testing.T, width, height int) *cpucompositor.Surface {
	t.Helper()
	pixels := make([]cpucompositor.Pixel, width*height)
	state := uint64(0x243F6A8885A308D3)
	for index := range pixels {
		next := func() uint16 {
			state = state*6364136223846793005 + 1442695040888963407
			return uint16(state >> 48)
		}
		alpha := uint16(0xC000 + next()%0x3FFF)
		pixels[index] = cpucompositor.Pixel{R: next() & alpha, G: next() & alpha, B: next() & alpha, A: alpha}
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}
