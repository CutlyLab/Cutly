package color

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func BenchmarkConvertSurfaceToRGBA8_720p(b *testing.B) {
	const width, height = 1280, 720
	pixels := make([]cpucompositor.Pixel, width*height)
	for index := range pixels {
		pixels[index] = cpucompositor.Pixel{
			R: uint16(index * 13), G: uint16(index * 29), B: uint16(index * 47), A: 65535,
		}
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		b.Fatal(err)
	}
	limits := OutputLimits{MaxWidth: width, MaxHeight: height, MaxOutputBytes: width * height * 4}
	b.SetBytes(width * height * 4)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		result, convertErr := ConvertSurfaceToRGBA8(context.Background(), surface, limits)
		if convertErr != nil || result.Frame == nil {
			b.Fatalf("result=%+v err=%v", result, convertErr)
		}
	}
}
