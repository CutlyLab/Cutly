package vectorraster

import (
	"context"
	"math/big"
	"math/rand"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

type oraclePoint struct{ x, y *big.Rat }
type oracleEdge struct{ first, second oraclePoint }

func assertMatchesOracle(t *testing.T, source documentv2.VectorSource) {
	t.Helper()
	surface, _, err := Rasterize(context.Background(), source, DefaultLimits())
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	want := slowOracle(source)
	if surface.Width() != int(source.Viewport.WidthPixels) || surface.Height() != int(source.Viewport.HeightPixels) {
		t.Fatalf("surface dimensions = %dx%d", surface.Width(), surface.Height())
	}
	for y := 0; y < surface.Height(); y++ {
		for x := 0; x < surface.Width(); x++ {
			got := mustPixel(t, surface, x, y)
			if expected := want[y*surface.Width()+x]; got != expected {
				t.Fatalf("pixel(%d,%d) = %+v, oracle %+v", x, y, got, expected)
			}
		}
	}
}

// slowOracle is intentionally a direct point-by-point implementation. It does not share the
// production transform, active-edge scanline, crossing thresholds, coverage aggregation or
// integer quantizer.
func slowOracle(source documentv2.VectorSource) []cpucompositor.Pixel {
	width, height := int(source.Viewport.WidthPixels), int(source.Viewport.HeightPixels)
	viewX := modelRat(source.ViewBox.X)
	viewY := modelRat(source.ViewBox.Y)
	viewWidth := modelRat(source.ViewBox.Width)
	viewHeight := modelRat(source.ViewBox.Height)
	viewportWidth := new(big.Rat).SetInt64(int64(width))
	viewportHeight := new(big.Rat).SetInt64(int64(height))
	scaleX := new(big.Rat).Quo(viewportWidth, viewWidth)
	scaleY := new(big.Rat).Quo(viewportHeight, viewHeight)
	scale := new(big.Rat).Set(scaleX)
	if scaleY.Cmp(scaleX) < 0 {
		scale.Set(scaleY)
	}
	offsetX := new(big.Rat).Quo(
		new(big.Rat).Sub(viewportWidth, new(big.Rat).Mul(scale, viewWidth)), big.NewRat(2, 1),
	)
	offsetY := new(big.Rat).Quo(
		new(big.Rat).Sub(viewportHeight, new(big.Rat).Mul(scale, viewHeight)), big.NewRat(2, 1),
	)
	mapPoint := func(value documentv2.Point) oraclePoint {
		x := new(big.Rat).Add(offsetX, new(big.Rat).Mul(scale, new(big.Rat).Sub(modelRat(value.X), viewX)))
		y := new(big.Rat).Add(offsetY, new(big.Rat).Mul(scale, new(big.Rat).Sub(modelRat(value.Y), viewY)))
		return oraclePoint{x: x, y: y}
	}

	result := make([]cpucompositor.Pixel, width*height)
	for _, path := range source.Paths {
		edges := oraclePathEdges(path, mapPoint)
		paint := color.PaintRGBA16ToPixel(color.RGBA16Input{
			R: path.Fill.R, G: path.Fill.G, B: path.Fill.B, A: path.Fill.A,
		})
		for pixelY := 0; pixelY < height; pixelY++ {
			for pixelX := 0; pixelX < width; pixelX++ {
				coverage := uint64(0)
				for sampleY := 0; sampleY < 8; sampleY++ {
					y := big.NewRat(int64(pixelY*16+sampleY*2+1), 16)
					for sampleX := 0; sampleX < 8; sampleX++ {
						x := big.NewRat(int64(pixelX*16+sampleX*2+1), 16)
						winding, parity := int64(0), false
						for _, edge := range edges {
							direction := edge.first.y.Cmp(edge.second.y)
							if direction == 0 {
								continue
							}
							lowY, highY := edge.first.y, edge.second.y
							weight := int64(1)
							if direction > 0 {
								lowY, highY = edge.second.y, edge.first.y
								weight = -1
							}
							if y.Cmp(lowY) < 0 || y.Cmp(highY) >= 0 {
								continue
							}
							t := new(big.Rat).Quo(new(big.Rat).Sub(y, edge.first.y), new(big.Rat).Sub(edge.second.y, edge.first.y))
							crossingX := new(big.Rat).Add(edge.first.x, new(big.Rat).Mul(new(big.Rat).Sub(edge.second.x, edge.first.x), t))
							if crossingX.Cmp(x) > 0 {
								winding += weight
								parity = !parity
							}
						}
						inside := parity
						if path.FillRule == documentv2.VectorFillNonZero {
							inside = winding != 0
						}
						if inside {
							coverage++
						}
					}
				}
				sourcePixel := cpucompositor.Pixel{
					R: oracleRoundUint16(uint64(paint.R)*coverage, 64),
					G: oracleRoundUint16(uint64(paint.G)*coverage, 64),
					B: oracleRoundUint16(uint64(paint.B)*coverage, 64),
					A: oracleRoundUint16(uint64(paint.A)*coverage, 64),
				}
				index := pixelY*width + pixelX
				result[index] = oracleSourceOver(result[index], sourcePixel)
			}
		}
	}
	return result
}

func oraclePathEdges(path documentv2.VectorPath, mapPoint func(documentv2.Point) oraclePoint) []oracleEdge {
	var result []oracleEdge
	var start, current oraclePoint
	open := false
	closeOpen := func() {
		if open {
			result = append(result, oracleEdge{first: current, second: start})
			open = false
		}
	}
	for _, command := range path.Commands {
		switch command.Op {
		case documentv2.VectorMoveTo:
			closeOpen()
			mapped := mapPoint(command.Points[0])
			start = oraclePoint{x: new(big.Rat).Set(mapped.x), y: new(big.Rat).Set(mapped.y)}
			current = oraclePoint{x: new(big.Rat).Set(mapped.x), y: new(big.Rat).Set(mapped.y)}
			open = true
		case documentv2.VectorLineTo:
			mapped := mapPoint(command.Points[0])
			result = append(result, oracleEdge{first: current, second: mapped})
			current = oraclePoint{x: new(big.Rat).Set(mapped.x), y: new(big.Rat).Set(mapped.y)}
		case documentv2.VectorClose:
			closeOpen()
		}
	}
	closeOpen()
	return result
}

func modelRat(value documentv2.Rational) *big.Rat {
	return new(big.Rat).SetFrac64(value.Numerator, value.Denominator)
}

func oracleRoundUint16(numerator, denominator uint64) uint16 {
	n := new(big.Int).SetUint64(numerator)
	d := new(big.Int).SetUint64(denominator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(n, d, remainder)
	doubled := new(big.Int).Lsh(remainder, 1)
	if comparison := doubled.Cmp(d); comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return uint16(quotient.Uint64())
}

func oracleSourceOver(destination, source cpucompositor.Pixel) cpucompositor.Pixel {
	sourceChannels := [4]uint16{source.R, source.G, source.B, source.A}
	destinationChannels := [4]uint16{destination.R, destination.G, destination.B, destination.A}
	var output [4]uint16
	for index := range output {
		numerator := new(big.Int).Mul(new(big.Int).SetUint64(uint64(sourceChannels[index])), big.NewInt(65_535))
		destinationTerm := new(big.Int).Mul(
			new(big.Int).SetUint64(uint64(destinationChannels[index])),
			new(big.Int).SetUint64(65_535-uint64(source.A)),
		)
		numerator.Add(numerator, destinationTerm)
		output[index] = oracleRoundBigUint16(numerator, big.NewInt(65_535))
	}
	return cpucompositor.Pixel{R: output[0], G: output[1], B: output[2], A: output[3]}
}

func oracleRoundBigUint16(numerator, denominator *big.Int) uint16 {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	comparison := new(big.Int).Lsh(remainder, 1).Cmp(denominator)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return uint16(quotient.Uint64())
}

func TestRasterizeMatchesSlowOracleRandomSmall(t *testing.T) {
	random := rand.New(rand.NewSource(0xC0717))
	for iteration := 0; iteration < 80; iteration++ {
		width := uint32(1 + random.Intn(4))
		height := uint32(1 + random.Intn(4))
		pathCount := 1 + random.Intn(3)
		paths := make([]documentv2.VectorPath, pathCount)
		for pathIndex := range paths {
			pointCount := 2 + random.Intn(5)
			commands := make([]documentv2.VectorCommand, 0, pointCount+1)
			for pointIndex := 0; pointIndex < pointCount; pointIndex++ {
				x := q(int64(random.Intn(int(width)*8+17)-8), 8)
				y := q(int64(random.Intn(int(height)*8+17)-8), 8)
				if pointIndex == 0 {
					commands = append(commands, move(x, y))
				} else {
					commands = append(commands, line(x, y))
				}
			}
			if random.Intn(2) == 0 {
				commands = append(commands, closeCommand())
			}
			rule := documentv2.VectorFillNonZero
			if random.Intn(2) == 0 {
				rule = documentv2.VectorFillEvenOdd
			}
			paths[pathIndex] = documentv2.VectorPath{
				ID: documentv2.ID("random-" + string(rune('a'+pathIndex))), FillRule: rule,
				Fill:     testColor(uint16(random.Intn(65_536)), uint16(random.Intn(65_536)), uint16(random.Intn(65_536)), uint16(random.Intn(65_536))),
				Commands: commands,
			}
		}
		source := testSource(width, height, paths...)
		assertMatchesOracle(t, source)
	}
}
