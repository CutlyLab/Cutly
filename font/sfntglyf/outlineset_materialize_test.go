package sfntglyf

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestMaterializeOutlineSetMinimalEmptyAndViews(t *testing.T) {
	geometry := outlineSetTestGeometry(t, [][]byte{nil, nil}, outlineTestMaxp{}, geometryTestBBox{}, 0, 0, 0)
	set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if set == nil || receipt.Profile != OutlineSetReceiptProfileV1 || receipt.OutlineProfile != OutlineSetOutlineProfileV1 ||
		receipt.DescriptorHash != OutlineSetDescriptorHash() || receipt.OutlineSetHash == "" || receipt.GlyphCount != 2 ||
		receipt.CommandInstances != 0 || receipt.ImpliedPointInstances != 0 || receipt.CanonicalBinaryBytes == 0 ||
		receipt.SourceGeometryHash != geometry.receipt.GeometryHash || receipt.SourceGraphHash != geometry.receipt.SourceGraphHash ||
		receipt.CapabilityStatus != OutlineSetCapabilityStatusV1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	if set.Receipt() != receipt || set.GlyphCount() != 2 {
		t.Fatal("accessor drift")
	}
	for glyphID := uint32(0); glyphID < 2; glyphID++ {
		view, ok := set.Glyph(glyphID)
		if !ok || view.GlyphID() != glyphID || view.ContourCount() != 0 || view.CommandCount() != 0 {
			t.Fatalf("gid=%d view=%+v ok=%t", glyphID, view, ok)
		}
		if _, ok := view.Command(0); ok {
			t.Fatal("empty command accepted")
		}
	}
	if _, ok := set.Glyph(2); ok {
		t.Fatal("out-of-range glyph accepted")
	}
	var nilSet *OutlineSet
	if nilSet.Receipt() != (OutlineSetReceipt{}) || nilSet.GlyphCount() != 0 {
		t.Fatal("nil receiver drift")
	}
	if _, ok := nilSet.Glyph(0); ok {
		t.Fatal("nil glyph accepted")
	}

	const readers = 32
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func() {
			defer wait.Done()
			if set.Receipt() != receipt {
				t.Error("concurrent receipt drift")
			}
			if view, ok := set.Glyph(0); !ok || view.CommandCount() != 0 {
				t.Error("concurrent view drift")
			}
		}()
	}
	wait.Wait()
}

func TestMaterializeOutlineSetCanonicalShortContours(t *testing.T) {
	tests := []struct {
		name    string
		flags   []bool
		xs      []int16
		ops     []OutlineCommandOp
		points  [][]OutlinePoint
		j, l, q uint64
	}{
		{name: "single on", flags: []bool{true}, xs: []int16{0}, ops: []OutlineCommandOp{OutlineMoveTo, OutlineClose}, points: [][]OutlinePoint{{{X: 0}}, nil}},
		{name: "single off", flags: []bool{false}, xs: []int16{0}, ops: []OutlineCommandOp{OutlineMoveTo, OutlineQuadTo, OutlineClose}, points: [][]OutlinePoint{{{X: 0}}, {{X: 0}, {X: 0}}, nil}, j: 1, q: 1},
		{name: "on on", flags: []bool{true, true}, xs: []int16{0, 2}, ops: []OutlineCommandOp{OutlineMoveTo, OutlineLineTo, OutlineClose}, points: [][]OutlinePoint{{{X: 0}}, {{X: 128}}, nil}, l: 1},
		{name: "on off", flags: []bool{true, false}, xs: []int16{0, 2}, ops: []OutlineCommandOp{OutlineMoveTo, OutlineQuadTo, OutlineClose}, points: [][]OutlinePoint{{{X: 0}}, {{X: 128}, {X: 0}}, nil}, q: 1},
		{name: "off on", flags: []bool{false, true}, xs: []int16{0, 2}, ops: []OutlineCommandOp{OutlineMoveTo, OutlineQuadTo, OutlineClose}, points: [][]OutlinePoint{{{X: 128}}, {{X: 0}, {X: 128}}, nil}, q: 1},
		{name: "off off", flags: []bool{false, false}, xs: []int16{0, 2}, ops: []OutlineCommandOp{OutlineMoveTo, OutlineQuadTo, OutlineQuadTo, OutlineClose}, points: [][]OutlinePoint{{{X: 64}}, {{X: 0}, {X: 64}}, {{X: 128}, {X: 64}}, nil}, j: 2, q: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			points := make([]outlineTestPoint, len(test.flags))
			for index := range points {
				points[index] = outlineTestPoint{x: test.xs[index], onCurve: test.flags[index]}
			}
			body := makeOutlineSimpleGlyph([]uint16{uint16(len(points) - 1)}, points, nil, false, 0)
			bbox := geometryTestBBox{xMin: test.xs[0], xMax: test.xs[len(test.xs)-1]}
			geometry := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: uint16(len(points)), contours: 1}, bbox, 0, int16(500-(bbox.xMax-bbox.xMin)), bbox.xMax-bbox.xMin)
			set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
			if err != nil {
				t.Fatal(err)
			}
			view, ok := set.Glyph(1)
			if !ok || view.CommandCount() != uint64(len(test.ops)) || view.ContourCount() != 1 {
				t.Fatalf("view=%+v", view)
			}
			for commandIndex, wantOp := range test.ops {
				command, ok := view.Command(uint64(commandIndex))
				if !ok || command.Op() != wantOp || int(command.PointCount()) != len(test.points[commandIndex]) {
					t.Fatalf("command[%d] op=%v points=%d ok=%t", commandIndex, command.Op(), command.PointCount(), ok)
				}
				for pointIndex, want := range test.points[commandIndex] {
					got, ok := command.Point(uint8(pointIndex))
					if !ok || got != want {
						t.Fatalf("command[%d].point[%d]=%+v want=%+v ok=%t", commandIndex, pointIndex, got, want, ok)
					}
				}
				if _, ok := command.Point(command.PointCount()); ok {
					t.Fatal("out-of-range point accepted")
				}
			}
			if receipt.ImpliedPointInstances != test.j || receipt.LineCommandInstances != test.l || receipt.QuadCommandInstances != test.q ||
				receipt.CommandInstances != uint64(len(test.ops)) || receipt.CommandPointOccurrences != 1+test.l+2*test.q {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}

func TestMaterializeOutlineSetFailureIsAtomic(t *testing.T) {
	body := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 0, onCurve: false}, {x: 2, onCurve: false}}, nil, false, 0)
	geometry := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1}, geometryTestBBox{xMax: 2}, 0, 498, 2)
	baseline, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil || baseline == nil {
		t.Fatal(err)
	}
	tests := []OutlineSetLimits{
		{MaxImpliedPointInstances: receipt.ImpliedPointInstances - 1},
		{MaxCommandInstances: receipt.CommandInstances - 1},
		{MaxCommandPointOccurrences: receipt.CommandPointOccurrences - 1},
		{MaxCommandsPerGlyph: receipt.MaxCommandsPerGlyph - 1},
		{MaxCanonicalBinaryBytes: receipt.CanonicalBinaryBytes - 1},
		{MaxWorkUnits: receipt.WorkUnits - 1},
		{MaxRetainedBytes: receipt.RetainedBytes - 1},
	}
	for index, limits := range tests {
		set, failed, err := MaterializeOutlineSet(context.Background(), geometry, limits)
		if !errors.Is(err, ErrResourceLimit) || set != nil || failed != (OutlineSetReceipt{}) {
			t.Fatalf("case=%d set=%v receipt=%+v err=%v", index, set, failed, err)
		}
	}
}

func TestMaterializeOutlineSetMidpointBeforeRound(t *testing.T) {
	child := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: 128, onCurve: false}, {x: 640, onCurve: false},
	}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentScale, glyphID: 1, matrix: []int16{1},
	}}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{xMax: 1})
	geometry := outlineSetTestGeometry(t, [][]byte{nil, child, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 2, compositeContours: 1, componentElements: 1, depth: 1,
	}, geometryTestBBox{xMax: 640}, 0, -12, 512)
	set, _, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := set.Glyph(2)
	if !ok || view.CommandCount() != 4 {
		t.Fatalf("view=%+v ok=%t", view, ok)
	}
	want := []struct {
		op     OutlineCommandOp
		points []OutlinePoint
	}{
		{OutlineMoveTo, []OutlinePoint{{X: 2}}},
		{OutlineQuadTo, []OutlinePoint{{X: 0}, {X: 2}}},
		{OutlineQuadTo, []OutlinePoint{{X: 2}, {X: 2}}},
		{OutlineClose, nil},
	}
	for index, expected := range want {
		command, ok := view.Command(uint64(index))
		if !ok || command.Op() != expected.op || int(command.PointCount()) != len(expected.points) {
			t.Fatalf("command[%d] op=%v points=%d", index, command.Op(), command.PointCount())
		}
		for pointIndex, point := range expected.points {
			got, _ := command.Point(uint8(pointIndex))
			if got != point {
				t.Fatalf("command[%d].point[%d]=%+v want=%+v", index, pointIndex, got, point)
			}
		}
	}
}

func TestMaterializeOutlineSetCompoundOrderTransformAndPointAttachment(t *testing.T) {
	child := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: 0, onCurve: true}, {x: 3, onCurve: false},
	}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1},
		{flags: componentScale, glyphID: 1, arg1: 1, arg2: 1, matrix: []int16{0x2000}},
	}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{xMax: 3})
	geometry := outlineSetTestGeometry(t, [][]byte{nil, child, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 4, compositeContours: 2, componentElements: 2, depth: 1,
	}, geometryTestBBox{xMax: 3}, 0, 497, 3)
	set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := set.Glyph(2)
	if !ok || view.ContourCount() != 2 || view.CommandCount() != 6 {
		t.Fatalf("view=%+v ok=%t", view, ok)
	}
	want := []struct {
		op     OutlineCommandOp
		points []OutlinePoint
	}{
		{OutlineMoveTo, []OutlinePoint{{X: 0}}}, {OutlineQuadTo, []OutlinePoint{{X: 192}, {X: 0}}}, {OutlineClose, nil},
		{OutlineMoveTo, []OutlinePoint{{X: 96}}}, {OutlineQuadTo, []OutlinePoint{{X: 192}, {X: 96}}}, {OutlineClose, nil},
	}
	for index, expected := range want {
		command, ok := view.Command(uint64(index))
		if !ok || command.Op() != expected.op || int(command.PointCount()) != len(expected.points) {
			t.Fatalf("command[%d]=%v/%d", index, command.Op(), command.PointCount())
		}
		for pointIndex, point := range expected.points {
			got, _ := command.Point(uint8(pointIndex))
			if got != point {
				t.Fatalf("command[%d].point[%d]=%+v want=%+v", index, pointIndex, got, point)
			}
		}
	}
	if receipt.ExpandedEdgeVisits != 2 || receipt.TransformPointVisits == 0 || receipt.CommandInstances != 9 {
		// Three commands belong to the child root and six to the compound root.
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestOutlineSetFlagTopologyAllPatternsAndLargeAllOff(t *testing.T) {
	for count := 1; count <= 8; count++ {
		for mask := 0; mask < 1<<count; mask++ {
			points := make([]outlineRawPoint, count)
			var off uint64
			for index := range points {
				points[index].onCurve = mask&(1<<index) != 0
				if !points[index].onCurve {
					off++
				}
			}
			stats, err := countSimpleOutlineCommands(context.Background(), 0, points, []uint32{uint32(count - 1)})
			if err != nil {
				t.Fatal(err)
			}
			var implied uint64
			for index := range points {
				next := (index + 1) % len(points)
				if !points[index].onCurve && !points[next].onCurve {
					implied++
				}
			}
			if stats.implied != implied || stats.quads != off || stats.commands != 2+stats.lines+stats.quads ||
				stats.commandPoints != 1+stats.lines+2*stats.quads || stats.implied > uint64(count) ||
				stats.commands > uint64(count+2) || stats.commandPoints > uint64(2*count+1) {
				t.Fatalf("count=%d mask=%08b stats=%+v implied=%d off=%d", count, mask, stats, implied, off)
			}
		}
	}
	points := make([]outlineRawPoint, 65_535)
	stats, err := countSimpleOutlineCommands(context.Background(), 0, points, []uint32{65_534})
	if err != nil {
		t.Fatal(err)
	}
	if stats.implied != 65_535 || stats.quads != 65_535 || stats.commands != 65_537 || stats.commandPoints != 131_071 {
		t.Fatalf("large stats=%+v", stats)
	}
	if DefaultOutlineSetLimits().MaxCommandsPerGlyph < stats.commands {
		t.Fatal("default per-glyph limit is incompatible with max static-glyf contour")
	}
}

func outlineSetTestGeometry(t testing.TB, bodies [][]byte, maxima outlineTestMaxp, bbox geometryTestBBox,
	minLSB, minRSB, extent int16) *GeometryFont {
	t.Helper()
	tables := outlineTestTables(t, bodies, maxima)
	setGeometryDeclarations(tables, bbox, minLSB, minRSB, extent)
	outline := decodeGeometryTestOutline(t, tables)
	geometry, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return geometry
}

func FuzzMaterializeOutlineSetFailureIsAtomic(f *testing.F) {
	tables := outlineTestTables(&fuzzTestingT{f: f}, [][]byte{nil, nil}, outlineTestMaxp{})
	f.Add(buildTestSFNT(&fuzzTestingT{f: f}, tables))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 1<<20 {
			t.Skip()
		}
		font, _, err := Parse(context.Background(), encoded, Limits{MaxEncodedBytes: 1 << 20})
		if err != nil {
			return
		}
		outline, _, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
		if err != nil {
			return
		}
		geometry, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
		if err != nil {
			return
		}
		set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
		if err != nil && (set != nil || receipt != (OutlineSetReceipt{})) {
			t.Fatalf("failure published set=%v receipt=%+v err=%v", set, receipt, err)
		}
	})
}

func FuzzMaterializeOutlineSetStructuredContours(f *testing.F) {
	f.Add(int16(-2), int16(3), int16(1), int16(-1), uint8(0))
	f.Fuzz(func(t *testing.T, x0, x1, y0, y1 int16, flags uint8) {
		x0, x1 = x0%1000, x1%1000
		y0, y1 = y0%1000, y1%1000
		points := []outlineTestPoint{
			{x: x0, y: y0, onCurve: flags&1 != 0}, {x: x1, y: y1, onCurve: flags&2 != 0},
		}
		xMin, xMax := x0, x1
		if xMin > xMax {
			xMin, xMax = xMax, xMin
		}
		yMin, yMax := y0, y1
		if yMin > yMax {
			yMin, yMax = yMax, yMin
		}
		bbox := geometryTestBBox{xMin, yMin, xMax, yMax}
		body := makeOutlineSimpleGlyph([]uint16{1}, points, nil, false, 0)
		width := int32(xMax) - int32(xMin)
		geometry := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1},
			bbox, 0, int16(500-width), int16(width))
		set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
		if err != nil || set == nil {
			t.Fatalf("materialize: %v", err)
		}
		off := uint64(0)
		if flags&1 == 0 {
			off++
		}
		if flags&2 == 0 {
			off++
		}
		implied := uint64(0)
		if flags&3 == 0 {
			implied = 2
		}
		if receipt.QuadCommandInstances != off || receipt.ImpliedPointInstances != implied {
			t.Fatalf("flags=%02b receipt=%+v", flags&3, receipt)
		}
	})
}
