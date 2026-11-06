package cpuraster

import (
	"context"
	"math/big"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func BenchmarkRasterizeReference64x64(b *testing.B) {
	pixels := make([]cpucompositor.Pixel, 64*64)
	for index := range pixels {
		alpha := uint16(index % 65536)
		pixels[index] = cpucompositor.Pixel{R: alpha / 2, G: alpha / 3, B: alpha / 4, A: alpha}
	}
	source, err := cpucompositor.NewSurfaceFromPixels(64, 64, pixels)
	if err != nil {
		b.Fatal(err)
	}
	transform, crop := benchmarkGeometry(b)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := Rasterize(context.Background(), source, 64, 64, transform, crop, Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTrigDefaultExactBitBoundary(b *testing.B) {
	denominator := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), DefaultExactBits), big.NewInt(1))
	numerator := new(big.Int).Quo(new(big.Int).Set(denominator), big.NewInt(3))
	angle := new(big.Rat).SetFrac(numerator, denominator)
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, _, err := sinCosQ48(angle); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRasterizeIdentity64x64(b *testing.B) {
	pixels := make([]cpucompositor.Pixel, 64*64)
	for index := range pixels {
		pixels[index] = cpucompositor.Pixel{A: 65535}
	}
	source, err := cpucompositor.NewSurfaceFromPixels(64, 64, pixels)
	if err != nil {
		b.Fatal(err)
	}
	clip := documentv2.Clip{
		ID: "clip", AssetID: "image",
		TimelineRange: documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}},
		SourceRange:   documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}},
	}
	properties, err := evaluator.EvaluateProperties(documentv2.TrackKindVideo, clip, documentv2.Asset{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png"}, documentv2.RationalTime{Value: 0, Rate: 1})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := Rasterize(context.Background(), source, 64, 64, *properties.Transform, *properties.Crop, Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkGeometry(b *testing.B) (evaluator.Transform, evaluator.ExactRect) {
	b.Helper()
	clip := documentv2.Clip{
		ID: "clip", AssetID: "image",
		TimelineRange: documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}},
		SourceRange:   documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}},
		PropertySet: &documentv2.PropertySet{Transform: &documentv2.TransformProperty{
			Position:        documentv2.Point{X: documentv2.Rational{Numerator: 1, Denominator: 3}, Y: documentv2.Rational{Numerator: -1, Denominator: 5}},
			Scale:           documentv2.Point{X: documentv2.Rational{Numerator: 3, Denominator: 2}, Y: documentv2.Rational{Numerator: 4, Denominator: 3}},
			RotationDegrees: documentv2.Rational{Numerator: 17, Denominator: 3},
			Anchor:          documentv2.Point{X: documentv2.Rational{Numerator: 1, Denominator: 3}, Y: documentv2.Rational{Numerator: 2, Denominator: 3}},
		}},
	}
	properties, err := evaluator.EvaluateProperties(documentv2.TrackKindVideo, clip, documentv2.Asset{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png"}, documentv2.RationalTime{Value: 0, Rate: 1})
	if err != nil {
		b.Fatal(err)
	}
	return *properties.Transform, *properties.Crop
}
