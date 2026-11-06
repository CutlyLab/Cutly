package wavencode

import (
	"context"
	"testing"
)

func BenchmarkEncodeOneSecondStereo48k(b *testing.B) {
	values := make([][2]string, 48_000)
	for index := range values {
		values[index] = [2]string{"1/4", "-1/4"}
	}
	pcm := quantize(b, 48_000, 0, values)
	b.ReportAllocs()
	b.SetBytes(int64(pcm.FrameCount * 4))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, err := Encode(context.Background(), pcm, DefaultLimits())
		if err != nil || result.Artifact == nil {
			b.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
