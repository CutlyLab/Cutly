package sfntglyf

import (
	"context"
	"math/big"
	"testing"
)

type outlineSetOraclePoint struct {
	x, y    *big.Rat
	onCurve bool
}

func TestOutlineSetAllFlagPatternsAgainstIndependentCommandOracle(t *testing.T) {
	for pointCount := 1; pointCount <= 8; pointCount++ {
		for mask := 0; mask < 1<<pointCount; mask++ {
			exact := exactGlyphGeometry{points: make([]exactGeometryPoint, pointCount), endPoints: []uint32{uint32(pointCount - 1)}}
			for index := range exact.points {
				xNumerator := int64(2*index + 1)
				yNumerator := -int64(2*index + 1)
				exact.points[index] = exactGeometryPoint{
					x:       geometryDyadic{numerator: big.NewInt(xNumerator), shift: 7},
					y:       geometryDyadic{numerator: big.NewInt(yNumerator), shift: 8},
					onCurve: mask&(1<<index) != 0,
				}
			}
			wantCommands, wantStats := outlineSetIndependentOracle(exact)
			got := make([]outlineSetCommand, 0, len(wantCommands))
			mathContext := newGeometryExactContext(context.Background(), 528, 10_000, 1_000_000)
			if err := materializeOutlineSetGlyph(context.Background(), 0, exact, wantStats, mathContext, &got); err != nil {
				t.Fatalf("points=%d mask=%08b: %v", pointCount, mask, err)
			}
			if len(got) != len(wantCommands) {
				t.Fatalf("points=%d mask=%08b got=%d want=%d", pointCount, mask, len(got), len(wantCommands))
			}
			for index := range got {
				if got[index] != wantCommands[index] {
					t.Fatalf("points=%d mask=%08b command=%d got=%+v want=%+v", pointCount, mask, index, got[index], wantCommands[index])
				}
			}
		}
	}
}

func TestOutlineSetMultiContourResetsPendingState(t *testing.T) {
	exact := exactGlyphGeometry{
		points: []exactGeometryPoint{
			{x: geometryDyadic{numerator: big.NewInt(1)}, y: geometryDyadic{numerator: big.NewInt(0)}, onCurve: false},
			{x: geometryDyadic{numerator: big.NewInt(2)}, y: geometryDyadic{numerator: big.NewInt(0)}, onCurve: true},
			{x: geometryDyadic{numerator: big.NewInt(3)}, y: geometryDyadic{numerator: big.NewInt(0)}, onCurve: false},
		},
		endPoints: []uint32{0, 2},
	}
	want, stats := outlineSetIndependentOracle(exact)
	got := make([]outlineSetCommand, 0, len(want))
	if err := materializeOutlineSetGlyph(context.Background(), 0, exact, stats,
		newGeometryExactContext(context.Background(), 528, 100, 10_000), &got); err != nil {
		t.Fatal(err)
	}
	wantOps := []OutlineCommandOp{OutlineMoveTo, OutlineQuadTo, OutlineClose, OutlineMoveTo, OutlineQuadTo, OutlineClose}
	if len(got) != len(wantOps) {
		t.Fatalf("commands=%d", len(got))
	}
	for index, op := range wantOps {
		if got[index].op != op || got[index] != want[index] {
			t.Fatalf("command[%d]=%+v want=%+v", index, got[index], want[index])
		}
	}
}

func outlineSetIndependentOracle(exact exactGlyphGeometry) ([]outlineSetCommand, outlineSetGlyphStats) {
	commands := make([]outlineSetCommand, 0)
	var stats outlineSetGlyphStats
	start := 0
	for _, encodedEnd := range exact.endPoints {
		end := int(encodedEnd)
		raw := make([]outlineSetOraclePoint, end-start+1)
		for index := range raw {
			source := exact.points[start+index]
			raw[index] = outlineSetOraclePoint{x: outlineSetOracleRat(source.x), y: outlineSetOracleRat(source.y), onCurve: source.onCurve}
		}

		// Expand the circular ring first. This deliberately does not mirror the
		// production pending-off state machine: each off/off edge gets an explicit
		// exact on-curve midpoint before command selection.
		ring := make([]outlineSetOraclePoint, 0, len(raw)*2)
		rawPositions := make([]int, len(raw))
		seamMidpoint := -1
		for index, point := range raw {
			rawPositions[index] = len(ring)
			ring = append(ring, point)
			next := raw[(index+1)%len(raw)]
			if !point.onCurve && !next.onCurve {
				ring = append(ring, outlineSetOracleMidpoint(point, next))
				stats.implied++
				if index == len(raw)-1 {
					seamMidpoint = len(ring) - 1
				}
			}
		}
		startIndex := rawPositions[0]
		if !raw[0].onCurve {
			if raw[len(raw)-1].onCurve {
				startIndex = rawPositions[len(raw)-1]
			} else {
				startIndex = seamMidpoint
			}
		}
		rotated := append(append(make([]outlineSetOraclePoint, 0, len(ring)), ring[startIndex:]...), ring[:startIndex]...)
		if len(rotated) == 0 || !rotated[0].onCurve {
			panic("invalid independent expanded ring")
		}
		startPoint := outlineSetOracleRoundedPoint(rotated[0])
		commands = append(commands, outlineSetCommand{op: OutlineMoveTo, pointCount: 1, points: [2]OutlinePoint{startPoint}})
		for index := 1; index < len(rotated); {
			if rotated[index].onCurve {
				commands = append(commands, outlineSetCommand{op: OutlineLineTo, pointCount: 1,
					points: [2]OutlinePoint{outlineSetOracleRoundedPoint(rotated[index])}})
				stats.lines++
				index++
			} else {
				endpoint := rotated[0]
				if index+1 < len(rotated) {
					endpoint = rotated[index+1]
					if !endpoint.onCurve {
						panic("adjacent off points survived independent ring expansion")
					}
				}
				commands = append(commands, outlineSetCommand{op: OutlineQuadTo, pointCount: 2,
					points: [2]OutlinePoint{outlineSetOracleRoundedPoint(rotated[index]), outlineSetOracleRoundedPoint(endpoint)}})
				stats.quads++
				index += 2
			}
		}
		commands = append(commands, outlineSetCommand{op: OutlineClose})
		stats.contours++
		start = end + 1
	}
	stats.drawing = stats.lines + stats.quads
	stats.commands = uint64(len(commands))
	stats.commandPoints = stats.contours + stats.lines + 2*stats.quads
	return commands, stats
}

func outlineSetOracleMidpoint(left, right outlineSetOraclePoint) outlineSetOraclePoint {
	x := new(big.Rat).Add(new(big.Rat).Set(left.x), right.x)
	y := new(big.Rat).Add(new(big.Rat).Set(left.y), right.y)
	return outlineSetOraclePoint{x: x.Quo(x, big.NewRat(2, 1)), y: y.Quo(y, big.NewRat(2, 1)), onCurve: true}
}

func outlineSetOracleRoundedPoint(value outlineSetOraclePoint) OutlinePoint {
	return OutlinePoint{X: outlineSetOracleQ26(value.x), Y: outlineSetOracleQ26(value.y)}
}
