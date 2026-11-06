package vectorraster

import (
	"context"
	"fmt"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func BenchmarkRasterizeSolidRectangle(b *testing.B) {
	for _, size := range []uint32{16, 64, 256} {
		b.Run(fmt.Sprintf("%dx%d", size, size), func(b *testing.B) {
			source := testSource(size, size, testRectPath(
				"full", q(0, 1), q(0, 1), q(int64(size), 1), q(int64(size), 1),
				testColor(65_535, 32_768, 1_000, 50_000), true,
			))
			b.ReportAllocs()
			b.SetBytes(int64(size) * int64(size) * 8)
			for iteration := 0; iteration < b.N; iteration++ {
				if _, _, err := Rasterize(context.Background(), source, DefaultLimits()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRasterizeComplexPath64(b *testing.B) {
	commands := []documentv2.VectorCommand{move(q(32, 1), q(1, 1))}
	for index := 1; index <= 64; index++ {
		x := int64((index * 29) % 64)
		y := int64((index * 47) % 64)
		commands = append(commands, line(q(x, 1), q(y, 1)))
	}
	commands = append(commands, closeCommand())
	source := testSource(64, 64, documentv2.VectorPath{
		ID: "complex", FillRule: documentv2.VectorFillEvenOdd,
		Fill: testColor(10_000, 30_000, 60_000, 50_000), Commands: commands,
	})
	b.ReportAllocs()
	b.SetBytes(64 * 64 * 8)
	for iteration := 0; iteration < b.N; iteration++ {
		if _, _, err := Rasterize(context.Background(), source, DefaultLimits()); err != nil {
			b.Fatal(err)
		}
	}
}
