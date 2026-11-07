package pcm16le

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/render/audiograph"
)

func BenchmarkQuantize4096Frames(b *testing.B) {
	left, _ := audiograph.NewRatio(-12345, 32768)
	right, _ := audiograph.NewRatio(23457, 32768)
	samples := make([]audiograph.StereoSample, 4096)
	for index := range samples {
		samples[index] = audiograph.StereoSample{Left: left, Right: right}
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(samples) * BytesPerFrame))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := Quantize(context.Background(), 48_000, 0, samples, DefaultLimits()); err != nil {
			b.Fatal(err)
		}
	}
}
