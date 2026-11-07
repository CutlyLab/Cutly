package cpuraster

import (
	"context"
	"math/big"
	"math/rand"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// TestExactRowStepperMatchesIndependentRationalOracle protects the optimized common-denominator
// iterator against changing the original profile semantics. The oracle deliberately rebuilds the
// inverse matrix and four rational weights independently for each destination pixel.
func TestExactRowStepperMatchesIndependentRationalOracle(t *testing.T) {
	source := testSurface(t, 3, 2, []cpucompositor.Pixel{
		{R: 101, G: 53, B: 29, A: 211}, {R: 1000, G: 500, B: 250, A: 1200}, {R: 17, G: 19, B: 23, A: 31},
		{R: 4000, G: 3000, B: 2000, A: 5000}, {R: 7, G: 5, B: 3, A: 11}, {R: 65535, G: 12345, B: 54321, A: 65535},
	})
	view, err := source.View()
	if err != nil {
		t.Fatal(err)
	}
	positions := []documentv2.Rational{rat(-1, 1), rat(-1, 2), rat(0, 1), rat(1, 3), rat(1, 1)}
	scales := []documentv2.Rational{rat(-2, 1), rat(-1, 2), rat(0, 1), rat(1, 2), rat(1, 1), rat(3, 2)}
	rotations := []documentv2.Rational{rat(-270, 1), rat(-90, 1), rat(-17, 3), rat(0, 1), rat(23, 7), rat(45, 1), rat(90, 1), rat(180, 1), rat(359, 2)}
	anchors := []documentv2.Rational{rat(0, 1), rat(1, 3), rat(1, 2), rat(1, 1)}
	crops := []documentv2.CropProperty{
		{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 1), Height: rat(1, 1)}},
		{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 2), Height: rat(1, 1)}},
		{Rect: documentv2.Rect{X: rat(1, 4), Y: rat(1, 4), Width: rat(1, 2), Height: rat(1, 2)}},
	}
	random := rand.New(rand.NewSource(0x4355544c59))
	for iteration := 0; iteration < 16; iteration++ {
		transformProperty := &documentv2.TransformProperty{
			Position:        point(positions[random.Intn(len(positions))], positions[random.Intn(len(positions))]),
			Scale:           point(scales[random.Intn(len(scales))], scales[random.Intn(len(scales))]),
			RotationDegrees: rotations[random.Intn(len(rotations))],
			Anchor:          point(anchors[random.Intn(len(anchors))], anchors[random.Intn(len(anchors))]),
		}
		cropProperty := crops[random.Intn(len(crops))]
		transform, crop := evaluatedGeometry(t, transformProperty, &cropProperty)
		actual, rasterErr := Rasterize(context.Background(), source, 4, 3, transform, crop, Limits{})
		if rasterErr != nil {
			t.Fatalf("iteration %d raster: %v", iteration, rasterErr)
		}
		geometry, compileErr := compileGeometry(view.Width(), view.Height(), transform, crop, DefaultExactBits)
		if compileErr != nil {
			t.Fatalf("iteration %d compile oracle: %v", iteration, compileErr)
		}
		for y := 0; y < actual.Height(); y++ {
			for x := 0; x < actual.Width(); x++ {
				want, oracleErr := oracleSample(view, geometry, x, y)
				if oracleErr != nil {
					t.Fatalf("iteration %d oracle %d,%d: %v", iteration, x, y, oracleErr)
				}
				got, pixelErr := actual.Pixel(x, y)
				if pixelErr != nil || got != want {
					t.Fatalf("iteration %d pixel %d,%d = %+v, %v; oracle %+v; transform=%+v crop=%+v", iteration, x, y, got, pixelErr, want, transformProperty, cropProperty)
				}
			}
		}
	}
}

func oracleSample(view cpucompositor.ReadView, geometry compiledGeometry, x, y int) (cpucompositor.Pixel, error) {
	if geometry.scaleX.Sign() == 0 || geometry.scaleY.Sign() == 0 {
		return cpucompositor.Pixel{}, nil
	}
	destinationX := new(big.Rat).SetFrac64(int64(2*x+1), 2)
	destinationY := new(big.Rat).SetFrac64(int64(2*y+1), 2)
	qx := new(big.Rat).Sub(destinationX, geometry.positionX)
	qx.Sub(qx, geometry.anchorX)
	qy := new(big.Rat).Sub(destinationY, geometry.positionY)
	qy.Sub(qy, geometry.anchorY)
	sourceX := new(big.Rat).Add(new(big.Rat).Mul(geometry.cosine, qx), new(big.Rat).Mul(geometry.sine, qy))
	sourceX.Quo(sourceX, geometry.rotationDeterminant)
	sourceX.Quo(sourceX, geometry.scaleX)
	sourceX.Add(sourceX, geometry.anchorX)
	sourceY := new(big.Rat).Add(new(big.Rat).Mul(new(big.Rat).Neg(new(big.Rat).Set(geometry.sine)), qx), new(big.Rat).Mul(geometry.cosine, qy))
	sourceY.Quo(sourceY, geometry.rotationDeterminant)
	sourceY.Quo(sourceY, geometry.scaleY)
	sourceY.Add(sourceY, geometry.anchorY)

	gridX := new(big.Rat).Sub(sourceX, big.NewRat(1, 2))
	gridY := new(big.Rat).Sub(sourceY, big.NewRat(1, 2))
	x0 := oracleFloor(gridX)
	y0 := oracleFloor(gridY)
	fx := new(big.Rat).Sub(gridX, new(big.Rat).SetInt(x0))
	fy := new(big.Rat).Sub(gridY, new(big.Rat).SetInt(y0))
	oneMinusX := new(big.Rat).Sub(big.NewRat(1, 1), fx)
	oneMinusY := new(big.Rat).Sub(big.NewRat(1, 1), fy)
	weights := [4]*big.Rat{
		new(big.Rat).Mul(oneMinusX, oneMinusY), new(big.Rat).Mul(fx, oneMinusY),
		new(big.Rat).Mul(oneMinusX, fy), new(big.Rat).Mul(fx, fy),
	}
	coordinates := [4][2]*big.Int{
		{x0, y0},
		{new(big.Int).Add(new(big.Int).Set(x0), big.NewInt(1)), y0},
		{x0, new(big.Int).Add(new(big.Int).Set(y0), big.NewInt(1))},
		{new(big.Int).Add(new(big.Int).Set(x0), big.NewInt(1)), new(big.Int).Add(new(big.Int).Set(y0), big.NewInt(1))},
	}
	sums := [4]*big.Rat{new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)}
	for index, coordinate := range coordinates {
		if !coordinate[0].IsInt64() || !coordinate[1].IsInt64() {
			continue
		}
		tapX, tapY := coordinate[0].Int64(), coordinate[1].Int64()
		if tapX < 0 || tapY < 0 || tapX >= int64(view.Width()) || tapY >= int64(view.Height()) ||
			!oracleTapInsideCrop(geometry, coordinate[0], coordinate[1]) {
			continue
		}
		pixel, err := view.Pixel(int(tapX), int(tapY))
		if err != nil {
			return cpucompositor.Pixel{}, err
		}
		for channel, value := range [4]uint16{pixel.R, pixel.G, pixel.B, pixel.A} {
			sums[channel].Add(sums[channel], new(big.Rat).Mul(new(big.Rat).SetInt64(int64(value)), weights[index]))
		}
	}
	channels := [4]uint16{}
	for index := range sums {
		value, err := oracleRound(sums[index])
		if err != nil {
			return cpucompositor.Pixel{}, err
		}
		channels[index] = value
	}
	return cpucompositor.Pixel{R: channels[0], G: channels[1], B: channels[2], A: channels[3]}, nil
}

func oracleFloor(value *big.Rat) *big.Int {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.DivMod(value.Num(), value.Denom(), remainder)
	return quotient
}

func oracleTapInsideCrop(geometry compiledGeometry, x, y *big.Int) bool {
	centerX := new(big.Rat).Add(new(big.Rat).SetInt(x), big.NewRat(1, 2))
	centerY := new(big.Rat).Add(new(big.Rat).SetInt(y), big.NewRat(1, 2))
	return centerX.Cmp(geometry.cropLeft) >= 0 && centerX.Cmp(geometry.cropRight) < 0 &&
		centerY.Cmp(geometry.cropTop) >= 0 && centerY.Cmp(geometry.cropBottom) < 0
}

func oracleRound(value *big.Rat) (uint16, error) {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	twice := new(big.Int).Lsh(remainder, 1)
	comparison := twice.Cmp(value.Denom())
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() || quotient.Uint64() > 65535 {
		return 0, ErrSampleFailed
	}
	return uint16(quotient.Uint64()), nil
}

func FuzzRasterizeAxisAlignedNeverPanicsOrReturnsPartial(f *testing.F) {
	f.Add(int8(0), int8(0), int8(1), int8(1), uint8(2), uint8(2), uint8(1), uint8(0))
	f.Add(int8(-127), int8(127), int8(-2), int8(0), uint8(4), uint8(3), uint8(2), uint8(2))
	f.Fuzz(func(t *testing.T, positionX, positionY, scaleX, scaleY int8, targetWidth, targetHeight, anchorChoice, cropChoice uint8) {
		width := int(targetWidth%4) + 1
		height := int(targetHeight%4) + 1
		anchors := []documentv2.Rational{rat(0, 1), rat(1, 2), rat(1, 1)}
		crops := []documentv2.CropProperty{
			{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 1), Height: rat(1, 1)}},
			{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 2), Height: rat(1, 1)}},
			{Rect: documentv2.Rect{X: rat(1, 2), Y: rat(0, 1), Width: rat(1, 2), Height: rat(1, 1)}},
		}
		transformProperty := &documentv2.TransformProperty{
			Position:        point(rat(int64(positionX), 1), rat(int64(positionY), 1)),
			Scale:           point(rat(int64(scaleX%3), 1), rat(int64(scaleY%3), 1)),
			RotationDegrees: rat(0, 1),
			Anchor:          point(anchors[int(anchorChoice)%len(anchors)], anchors[(int(anchorChoice)+1)%len(anchors)]),
		}
		cropProperty := crops[int(cropChoice)%len(crops)]
		transform, crop := evaluatedGeometry(t, transformProperty, &cropProperty)
		source := testSurface(t, 2, 2, []cpucompositor.Pixel{{R: 1, A: 2}, {G: 3, A: 4}, {B: 5, A: 6}, {R: 7, G: 8, B: 9, A: 10}})
		result, err := Rasterize(context.Background(), source, width, height, transform, crop, Limits{
			MaxWidth: 4, MaxHeight: 4, MaxOutputPixels: 16, MaxWorkingBytes: 16 * 16, MaxExactBits: DefaultExactBits,
		})
		if err != nil {
			if result != nil {
				t.Fatalf("error returned partial surface: %v, %v", result, err)
			}
			return
		}
		if result == nil || result.Width() != width || result.Height() != height {
			t.Fatalf("success returned bad surface: %v", result)
		}
	})
}
