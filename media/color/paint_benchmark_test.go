package color

import "testing"

func BenchmarkPaintRGBA16ToPixel(b *testing.B) {
	benchmarks := []struct {
		name  string
		input RGBA16Input
	}{
		{name: "alpha_zero", input: RGBA16Input{R: 65535, G: 50000, B: 30000}},
		{name: "low_segments", input: RGBA16Input{R: 2650, G: 1700, B: 900, A: 50000}},
		{name: "gray_nonlinear", input: RGBA16Input{R: 50000, G: 50000, B: 50000, A: 50000}},
		{name: "three_nonlinear", input: RGBA16Input{R: 60001, G: 40003, B: 20011, A: 50021}},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				result := PaintRGBA16ToPixel(benchmark.input)
				if result.A != benchmark.input.A {
					b.Fatal("alpha drift")
				}
			}
		})
	}
}
