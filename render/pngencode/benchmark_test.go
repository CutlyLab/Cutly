package pngencode

import (
	"context"
	"testing"
)

func BenchmarkEncodeStoredPNG720p(b *testing.B) {
	const width, height = 1280, 720
	input := outputFrame(b, width, height, nil)
	limits := DefaultLimits()
	limits.MaxWidth, limits.MaxHeight = width, height
	limits.MaxRawRGBABytes = width * height * 4
	b.SetBytes(width * height * 4)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		result, err := Encode(context.Background(), input, limits)
		if err != nil || result.Artifact == nil {
			b.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
