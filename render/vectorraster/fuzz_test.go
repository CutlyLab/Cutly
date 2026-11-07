package vectorraster

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func FuzzRasterizeMatchesSlowOracle(f *testing.F) {
	f.Add([]byte{1, 1, 3, 0, 0, 8, 8, 16, 0, 0xff, 0x80})
	f.Add([]byte{3, 2, 5, 1, 4, 1, 15, 17, 2, 9, 23, 31, 47})
	f.Add([]byte{2, 3, 2, 0, 0xff, 0xff, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		at := func(index int) byte { return data[index%len(data)] }
		width := uint32(1 + at(0)%3)
		height := uint32(1 + at(1)%3)
		pointCount := 2 + int(at(2)%5)
		commands := make([]documentv2.VectorCommand, 0, pointCount+1)
		for index := 0; index < pointCount; index++ {
			denominator := int64([]int{1, 2, 4, 8}[at(3+index*3)%4])
			xSpan := int(width)*int(denominator) + 5
			ySpan := int(height)*int(denominator) + 5
			x := q(int64(int(at(4+index*3))%(xSpan*2)-xSpan/2), denominator)
			y := q(int64(int(at(5+index*3))%(ySpan*2)-ySpan/2), denominator)
			if index == 0 {
				commands = append(commands, move(x, y))
			} else {
				commands = append(commands, line(x, y))
			}
		}
		if at(6+pointCount*3)&1 != 0 {
			commands = append(commands, closeCommand())
		}
		rule := documentv2.VectorFillNonZero
		if at(7+pointCount*3)&1 != 0 {
			rule = documentv2.VectorFillEvenOdd
		}
		colorOffset := 8 + pointCount*3
		path := documentv2.VectorPath{
			ID: "fuzz", FillRule: rule,
			Fill: testColor(
				uint16(at(colorOffset))<<8|uint16(at(colorOffset+1)),
				uint16(at(colorOffset+2))<<8|uint16(at(colorOffset+3)),
				uint16(at(colorOffset+4))<<8|uint16(at(colorOffset+5)),
				uint16(at(colorOffset+6))<<8|uint16(at(colorOffset+7)),
			),
			Commands: commands,
		}
		source := testSource(width, height, path)
		surface, _, err := Rasterize(context.Background(), source, DefaultLimits())
		if err != nil {
			t.Fatalf("Rasterize valid generated source: %v", err)
		}
		want := slowOracle(source)
		for y := 0; y < int(height); y++ {
			for x := 0; x < int(width); x++ {
				got := mustPixel(t, surface, x, y)
				if expected := want[y*int(width)+x]; got != expected {
					t.Fatalf("pixel(%d,%d) = %+v, oracle %+v; data=%x", x, y, got, expected, data)
				}
			}
		}
	})
}

func FuzzRasterizeHostileRationalsFailClosed(f *testing.F) {
	f.Add(int64(0), int64(0), byte(0))
	f.Add(int64(2), int64(2), byte(1))
	f.Add(int64(-7), int64(-3), byte(2))
	f.Fuzz(func(t *testing.T, numerator, denominator int64, target byte) {
		source := testSource(1, 1, testRectPath("hostile", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(1, 2, 3, 4), true))
		value := documentv2.Rational{Numerator: numerator, Denominator: denominator}
		switch target % 3 {
		case 0:
			source.ViewBox.Width = value
		case 1:
			source.Paths[0].Commands[0].Points[0].X = value
		case 2:
			source.Paths[0].Commands[2].Points[0].Y = value
		}
		surface, receipt, err := Rasterize(context.Background(), source, DefaultLimits())
		if err != nil && (surface != nil || receipt != (Receipt{})) {
			t.Fatalf("error %v returned partial result", err)
		}
	})
}
