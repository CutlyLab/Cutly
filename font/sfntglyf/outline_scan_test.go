package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
)

type outlineTestMaxp struct {
	points, contours                       uint16
	compositePoints, compositeContours     uint16
	instructions, componentElements, depth uint16
}

type outlineTestPoint struct {
	x, y    int16
	onCurve bool
}

func TestDecodeOutlineFontMinimalAndOwnership(t *testing.T) {
	tables := minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	))
	encoded := buildTestSFNT(t, tables)
	font, _, err := Parse(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range encoded {
		encoded[index] ^= 0xff
	}
	outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if outline == nil || receipt.Profile != OutlineReceiptProfileV1 ||
		receipt.GlyphCount != 2 || receipt.EmptyRangeGlyphCount != 2 ||
		receipt.SimpleGlyphCount != 0 || receipt.CompoundGlyphCount != 0 ||
		receipt.GraphHashProfile != OutlineGraphHashProfileV1 || receipt.GraphHash == "" ||
		receipt.GeometryStatus != OutlineGeometryStatusV1 || receipt.MaxpStatus != OutlineMaxpStatusV1 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if outline.Receipt() != receipt || outline.GlyphCount() != 2 {
		t.Fatal("outline receipt/accessor drift")
	}
	for glyphID := uint32(0); glyphID < 2; glyphID++ {
		info, ok := outline.GlyphInfo(glyphID)
		if !ok || info.Kind != OutlineGlyphEmptyRange || info.SourceBytes != 0 {
			t.Fatalf("glyph %d info=%+v ok=%t", glyphID, info, ok)
		}
	}
	if _, ok := outline.GlyphInfo(2); ok {
		t.Fatal("out-of-range glyph accepted")
	}
	const readers = 32
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func() {
			defer wait.Done()
			if outline.Receipt() != receipt {
				t.Error("concurrent receipt drift")
			}
			if info, ok := outline.GlyphInfo(1); !ok || info.Kind != OutlineGlyphEmptyRange {
				t.Error("concurrent info drift")
			}
		}()
	}
	wait.Wait()
}

func TestDecodeOutlineFontSimpleCompoundAndPointMatching(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1, 3}, []outlineTestPoint{
		{0, 0, true}, {100, 0, false}, {100, 100, true}, {0, 100, true},
	}, []byte{0xaa, 0x55}, true, 1)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY | componentMore, glyphID: 1},
		{flags: 0, glyphID: 1, arg1: 0, arg2: 1},
	}, nil, 2)
	tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
		points: 4, contours: 2, compositePoints: 8, compositeContours: 4,
		instructions: 2, componentElements: 2, depth: 1,
	})
	font, _, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.EmptyRangeGlyphCount != 1 || receipt.SimpleGlyphCount != 1 || receipt.CompoundGlyphCount != 1 ||
		receipt.ZeroContourGlyphCount != 0 || receipt.RawPointCount != 4 || receipt.RawContourCount != 2 ||
		receipt.ComponentRecordCount != 2 || receipt.PointMatchComponentCount != 1 ||
		receipt.DefaultedUnscaledCount != 1 || receipt.InstructionBytes != 2 ||
		receipt.ExpandedPointInstances != 12 || receipt.ExpandedContourInstances != 6 ||
		receipt.ExpandedEdgeVisits != 2 || receipt.ObservedMaxCompositePoints != 8 ||
		receipt.ObservedMaxCompositeContours != 4 || receipt.ObservedMaxComponentDepth != 1 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	simpleInfo, _ := outline.GlyphInfo(1)
	compoundInfo, _ := outline.GlyphInfo(2)
	if simpleInfo.Kind != OutlineGlyphSimple || simpleInfo.RawPointCount != 4 || simpleInfo.RawContourCount != 2 ||
		!simpleInfo.OverlapDeclared || compoundInfo.Kind != OutlineGlyphCompound ||
		compoundInfo.DirectComponentCount != 2 || compoundInfo.ExpandedPointCount != 8 || compoundInfo.ComponentDepth != 1 {
		t.Fatalf("simple=%+v compound=%+v", simpleInfo, compoundInfo)
	}
	if got := outline.glyphs[2].components[1]; got.argumentMode != outlineArgumentPoint || got.arg1 != 0 || got.arg2 != 1 {
		t.Fatalf("point component decoded incorrectly: %+v", got)
	}
	oracleHash, oracleBytes := oracleOutlineGraphHash(font, outline)
	if receipt.GraphHash != oracleHash || receipt.CanonicalBinaryBytes != oracleBytes {
		t.Fatalf("graph oracle hash=%s bytes=%d receipt=%+v", oracleHash, oracleBytes, receipt)
	}
}

func TestOutlineSimpleRepeatCompressionAndZeroContourInstructions(t *testing.T) {
	zeroContour := make([]byte, 12)
	binary.BigEndian.PutUint16(zeroContour[10:12], 2)
	zeroContour = append(zeroContour, 0xaa, 0xbb, 0, 0)
	repeated := make([]byte, 14)
	binary.BigEndian.PutUint16(repeated[0:2], 1)
	binary.BigEndian.PutUint16(repeated[2:4], 1)
	binary.BigEndian.PutUint16(repeated[6:8], 3)
	binary.BigEndian.PutUint16(repeated[10:12], 2)
	// instructionLength at [12:14] is zero. One physical flag covers three points.
	repeated = append(repeated, simpleOnCurve|simpleXShort|simpleXSamePositive|simpleYSamePositive|simpleRepeat, 2)
	repeated = append(repeated, 1, 1, 1)
	font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, zeroContour, repeated}, outlineTestMaxp{
		points: 3, contours: 1, instructions: 2,
	}))
	outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SimpleGlyphCount != 2 || receipt.ZeroContourGlyphCount != 1 || receipt.RawPointCount != 3 ||
		receipt.InstructionGlyphCount != 1 || receipt.InstructionBytes != 2 || receipt.PaddingBytes != 2 {
		t.Fatalf("receipt=%+v", receipt)
	}
	points := outline.glyphs[2].points
	if len(points) != 3 || points[0].x != 1 || points[1].x != 2 || points[2].x != 3 ||
		points[0].y != 0 || !points[0].onCurve {
		t.Fatalf("points=%+v", points)
	}
}

func TestOutlineCompoundOffsetModesSignedXYAndMetadata(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{0, 0, true}}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY | componentScaledOffset | componentOverlap | componentUseMyMetrics, glyphID: 1, arg1: -128, arg2: 127},
		{flags: componentArgsXY | componentUnscaledOffset, glyphID: 1},
		{flags: componentArgsXY, glyphID: 1},
	}, []byte{0x99}, 0)
	font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
		points: 1, contours: 1, compositePoints: 3, compositeContours: 3,
		instructions: 1, componentElements: 3, depth: 1,
	}))
	outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ScaledOffsetCount != 1 || receipt.UnscaledOffsetCount != 1 || receipt.DefaultedUnscaledCount != 1 ||
		receipt.UseMyMetricsCount != 1 || receipt.OverlapCompoundGlyphCount != 1 || receipt.InstructionBytes != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	first := outline.glyphs[2].components[0]
	if first.arg1 != -128 || first.arg2 != 127 || first.argumentMode != outlineArgumentXY {
		t.Fatalf("signed XY args=%+v", first)
	}
}

func TestOutlineSharedDAGCountsRepeatedReferences(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{1, 2, true}}, nil, false, 0)
	first := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)
	diamond := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1}, {flags: componentArgsXY, glyphID: 2},
	}, nil, 0)
	font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, simple, first, diamond}, outlineTestMaxp{
		points: 1, contours: 1, compositePoints: 2, compositeContours: 2, componentElements: 2, depth: 2,
	}))
	outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := outline.GlyphInfo(3)
	if info.ExpandedPointCount != 2 || info.ExpandedContourCount != 2 || info.ExpandedEdgeVisits != 3 || info.ComponentDepth != 2 ||
		receipt.ExpandedPointInstances != 4 || receipt.ExpandedEdgeVisits != 4 {
		t.Fatalf("info=%+v receipt=%+v", info, receipt)
	}
}

func TestOutlineSimpleBodyRejectsHostileFraming(t *testing.T) {
	base := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{0, 0, true}, {10, 20, true}}, nil, false, 0)
	insert := func(value []byte, at int, extra byte) []byte {
		result := append([]byte(nil), value[:at]...)
		result = append(result, extra)
		return append(result, value[at:]...)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{"short header", base[:9]},
		{"bbox mismatch", func() []byte { v := append([]byte(nil), base...); binary.BigEndian.PutUint16(v[6:8], 9); return v }()},
		{"reserved flag", func() []byte { v := append([]byte(nil), base...); v[14] |= simpleReserved; return v }()},
		{"late overlap", func() []byte { v := append([]byte(nil), base...); v[15] |= simpleOverlap; return v }()},
		{"zero repeat", func() []byte { v := append([]byte(nil), base...); v[14] |= simpleRepeat; return insert(v, 15, 0) }()},
		{"repeat overflow", func() []byte { v := append([]byte(nil), base...); v[14] |= simpleRepeat; return insert(v, 15, 2) }()},
		{"coordinate truncation", base[:len(base)-1]},
		{"nonzero padding", append(append([]byte(nil), base...), 1)},
		{"excess padding", append(append([]byte(nil), base...), 0, 0, 0, 0)},
		{"duplicate endpoints", makeOutlineSimpleGlyph([]uint16{1, 1}, []outlineTestPoint{{0, 0, true}, {1, 1, true}}, nil, false, 0)},
		{"coordinate overflow", func() []byte {
			v := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{32767, 0, true}, {32767, 0, true}}, nil, false, 0)
			// flags begin at 14; the second x delta begins at 18.
			binary.BigEndian.PutUint16(v[18:20], 1)
			return v
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, test.body}, outlineTestMaxp{points: 2, contours: 2}))
			outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
			if !errors.Is(err, ErrOutlineInvalid) || outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("outline=%v receipt=%+v err=%v", outline, receipt, err)
			}
		})
	}
}

func TestOutlineCompoundBodyRejectsHostileFraming(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{0, 0, true}}, nil, false, 0)
	valid := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)
	tests := []struct {
		name string
		body []byte
	}{
		{"first point match", makeOutlineCompoundGlyph([]outlineTestComponent{{flags: 0, glyphID: 1}}, nil, 0)},
		{"round xy", makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY | componentRoundXY, glyphID: 1}}, nil, 0)},
		{"reserved", makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY | 0x2000, glyphID: 1}}, nil, 0)},
		{"transform conflict", makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY | componentScale | componentXYScale, glyphID: 1}}, nil, 0)},
		{"offset conflict", makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY | componentScaledOffset | componentUnscaledOffset, glyphID: 1}}, nil, 0)},
		{"instructions before final", makeOutlineCompoundGlyph([]outlineTestComponent{
			{flags: componentArgsXY | componentInstructions, glyphID: 1}, {flags: componentArgsXY, glyphID: 1},
		}, nil, 0)},
		{"late overlap", makeOutlineCompoundGlyph([]outlineTestComponent{
			{flags: componentArgsXY, glyphID: 1}, {flags: componentArgsXY | componentOverlap, glyphID: 1},
		}, nil, 0)},
		{"gid out of range", makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 3}}, nil, 0)},
		{"truncated", valid[:len(valid)-1]},
		{"nonzero padding", append(append([]byte(nil), valid...), 1)},
		{"excess padding", append(append([]byte(nil), valid...), 0, 0, 0, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, simple, test.body}, outlineTestMaxp{
				points: 1, contours: 1, compositePoints: 2, compositeContours: 2, componentElements: 2, depth: 1,
			}))
			outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
			if !errors.Is(err, ErrOutlineInvalid) || outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("outline=%v receipt=%+v err=%v", outline, receipt, err)
			}
		})
	}
}

func TestOutlineGraphRejectsCyclesAndPhantomPointMatches(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{0, 0, true}}, nil, false, 0)
	tests := []struct {
		name   string
		bodies [][]byte
	}{
		{"self cycle", [][]byte{nil, makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)}},
		{"two cycle", [][]byte{nil,
			makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 2}}, nil, 0),
			makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0),
		}},
		{"phantom point", [][]byte{nil, simple, makeOutlineCompoundGlyph([]outlineTestComponent{
			{flags: componentArgsXY, glyphID: 1}, {flags: 0, glyphID: 1, arg1: 0, arg2: 1},
		}, nil, 0)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			font := parseOutlineTestFont(t, outlineTestTables(t, test.bodies, outlineTestMaxp{
				points: 1, contours: 1, compositePoints: 2, compositeContours: 2, componentElements: 2, depth: 2,
			}))
			outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
			if !errors.Is(err, ErrOutlineGraphInvalid) || outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("outline=%v receipt=%+v err=%v", outline, receipt, err)
			}
		})
	}
}

func TestOutlineCompoundUnsignedPointArgsAndMatrixDiskOrder(t *testing.T) {
	points := make([]outlineTestPoint, 129)
	for index := range points {
		points[index] = outlineTestPoint{x: int16(index), onCurve: true}
	}
	simple := makeOutlineSimpleGlyph([]uint16{128}, points, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY | componentMore, glyphID: 1},
		{flags: componentTwoByTwo, glyphID: 1, arg1: 128, arg2: 128, matrix: []int16{1, 2, 3, 4}},
	}, nil, 0)
	font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
		points: 129, contours: 1, compositePoints: 258, compositeContours: 2, componentElements: 2, depth: 1,
	}))
	outline, _, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	got := outline.glyphs[2].components[1]
	if got.argumentMode != outlineArgumentPoint || got.arg1 != 128 || got.arg2 != 128 || got.matrix != [4]int16{1, 2, 3, 4} {
		t.Fatalf("component=%+v", got)
	}
}

func TestOutlineMaxpSevenFieldsMustMatchExactly(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{0, 0, true}, {10, 10, true}}, []byte{1}, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)
	correct := outlineTestMaxp{points: 2, contours: 1, compositePoints: 2, compositeContours: 1, instructions: 1, componentElements: 1, depth: 1}
	fields := []struct {
		name   string
		offset int
	}{
		{"points", 6}, {"contours", 8}, {"composite points", 10}, {"composite contours", 12},
		{"instructions", 26}, {"component elements", 28}, {"depth", 30},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			for _, delta := range []int{-1, 1} {
				tables := outlineTestTables(t, [][]byte{nil, simple, compound}, correct)
				value := binary.BigEndian.Uint16(tables["maxp"][field.offset : field.offset+2])
				binary.BigEndian.PutUint16(tables["maxp"][field.offset:field.offset+2], uint16(int(value)+delta))
				font := parseOutlineTestFont(t, tables)
				outline, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
				if !errors.Is(err, ErrOutlineMaxpMismatch) || outline != nil || !isZeroOutlineReceipt(receipt) {
					t.Fatalf("delta=%d outline=%v receipt=%+v err=%v", delta, outline, receipt, err)
				}
			}
		})
	}
}

func TestOutlineResourceLimitsExactNAndNMinusOne(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1, 3}, []outlineTestPoint{
		{0, 0, true}, {10, 0, true}, {10, 10, true}, {0, 10, true},
	}, []byte{1, 2}, false, 1)
	compoundTwo := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1}, {flags: componentArgsXY, glyphID: 1},
	}, nil, 0)
	compoundNested := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 2}}, nil, 0)
	font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, simple, compoundTwo, compoundNested}, outlineTestMaxp{
		points: 4, contours: 2, compositePoints: 8, compositeContours: 4,
		instructions: 2, componentElements: 2, depth: 2,
	}))
	_, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RawPointCount != 4 || receipt.RawContourCount != 2 || receipt.ComponentRecordCount != 3 ||
		receipt.InstructionBytes != 2 || receipt.ExpandedPointInstances != 20 ||
		receipt.ExpandedContourInstances != 10 || receipt.ExpandedEdgeVisits != 5 || receipt.MaxExpandedEdgesPerGlyph != 3 {
		t.Fatalf("unexpected resource receipt: %+v", receipt)
	}
	stringBytes := oracleOutlineReceiptStringBytes(receipt)
	wantWork := receipt.GlyfBytes + 32*uint64(receipt.GlyphCount) + 12*receipt.RawPointCount +
		4*receipt.RawContourCount + 32*receipt.ComponentRecordCount + 2*receipt.InstructionBytes +
		receipt.PaddingBytes + 8*receipt.ExpandedEdgeVisits + 4*receipt.ExpandedPointInstances +
		2*receipt.ExpandedContourInstances + receipt.CanonicalBinaryBytes
	wantRetained := receipt.UpstreamRetainedBytes + 32_768 + 128*uint64(receipt.GlyphCount) +
		24*receipt.RawPointCount + 8*receipt.RawContourCount + 64*receipt.ComponentRecordCount + stringBytes
	maxPoints := uint64(receipt.ObservedMaxPoints)
	if uint64(receipt.ObservedMaxCompositePoints) > maxPoints {
		maxPoints = uint64(receipt.ObservedMaxCompositePoints)
	}
	maxContours := uint64(receipt.ObservedMaxContours)
	if uint64(receipt.ObservedMaxCompositeContours) > maxContours {
		maxContours = uint64(receipt.ObservedMaxCompositeContours)
	}
	wantWorking := uint64(65_536) + 32*uint64(receipt.GlyphCount) + 16*receipt.ComponentRecordCount +
		16*maxPoints + 8*maxContours + 8*receipt.MaxExpandedEdgesPerGlyph + receipt.CanonicalBinaryBytes
	if receipt.WorkUnits != wantWork || receipt.RetainedBytes != wantRetained || receipt.WorkingBytes != wantWorking {
		t.Fatalf("resource oracle work=%d retained=%d working=%d receipt=%+v", wantWork, wantRetained, wantWorking, receipt)
	}
	tests := []struct {
		name  string
		exact uint64
		set   func(*OutlineLimits, uint64)
	}{
		{"raw points", 4, func(v *OutlineLimits, n uint64) { v.MaxRawPoints = n }},
		{"raw contours", 2, func(v *OutlineLimits, n uint64) { v.MaxRawContours = n }},
		{"components", 3, func(v *OutlineLimits, n uint64) { v.MaxComponentRecords = n }},
		{"components per glyph", 2, func(v *OutlineLimits, n uint64) { v.MaxComponentsPerGlyph = n }},
		{"instructions", 2, func(v *OutlineLimits, n uint64) { v.MaxInstructionBytes = n }},
		{"depth", 2, func(v *OutlineLimits, n uint64) { v.MaxComponentDepth = uint32(n) }},
		{"expanded points per glyph", 8, func(v *OutlineLimits, n uint64) { v.MaxExpandedPointsPerGlyph = n }},
		{"expanded contours per glyph", 4, func(v *OutlineLimits, n uint64) { v.MaxExpandedContoursPerGlyph = n }},
		{"expanded edges per glyph", 3, func(v *OutlineLimits, n uint64) { v.MaxExpandedEdgesPerGlyph = n }},
		{"point instances", 20, func(v *OutlineLimits, n uint64) { v.MaxExpandedPointInstances = n }},
		{"contour instances", 10, func(v *OutlineLimits, n uint64) { v.MaxExpandedContourInstances = n }},
		{"edge visits", 5, func(v *OutlineLimits, n uint64) { v.MaxExpandedEdgeVisits = n }},
		{"canonical bytes", receipt.CanonicalBinaryBytes, func(v *OutlineLimits, n uint64) { v.MaxCanonicalBinaryBytes = n }},
		{"work", receipt.WorkUnits, func(v *OutlineLimits, n uint64) { v.MaxWorkUnits = n }},
		{"working", receipt.WorkingBytes, func(v *OutlineLimits, n uint64) { v.MaxWorkingBytes = n }},
		{"retained", receipt.RetainedBytes, func(v *OutlineLimits, n uint64) { v.MaxRetainedBytes = n }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exact := OutlineLimits{}
			test.set(&exact, test.exact)
			if _, _, err := DecodeOutlineFont(context.Background(), font, exact); err != nil {
				t.Fatalf("exact N failed: %v", err)
			}
			below := OutlineLimits{}
			test.set(&below, test.exact-1)
			outline, got, err := DecodeOutlineFont(context.Background(), font, below)
			if !errors.Is(err, ErrResourceLimit) || outline != nil || !isZeroOutlineReceipt(got) {
				t.Fatalf("N-1 outline=%v receipt=%+v err=%v", outline, got, err)
			}
		})
	}
}

func TestOutlineExpandedPerGlyphLimitsAlsoApplyToSimpleGlyphs(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1, 3}, []outlineTestPoint{
		{0, 0, true}, {10, 0, true}, {10, 10, true}, {0, 10, true},
	}, nil, false, 0)
	font := parseOutlineTestFont(t, outlineTestTables(t, [][]byte{nil, simple}, outlineTestMaxp{points: 4, contours: 2}))
	for name, limits := range map[string]OutlineLimits{
		"points":   {MaxExpandedPointsPerGlyph: 3},
		"contours": {MaxExpandedContoursPerGlyph: 1},
	} {
		t.Run(name, func(t *testing.T) {
			outline, receipt, err := DecodeOutlineFont(context.Background(), font, limits)
			if !errors.Is(err, ErrResourceLimit) || outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("outline=%v receipt=%+v err=%v", outline, receipt, err)
			}
		})
	}
}

func TestOutlineFailurePathsPublishStrictZeroReceipt(t *testing.T) {
	font := parseOutlineTestFont(t, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	)))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name   string
		ctx    context.Context
		font   *Font
		limits OutlineLimits
		want   error
	}{
		{"nil context", nil, font, OutlineLimits{}, ErrContextInvalid},
		{"nil font", context.Background(), nil, OutlineLimits{}, ErrOutlineInputInvalid},
		{"canceled", canceled, font, OutlineLimits{}, ErrCanceled},
		{"invalid limits", context.Background(), font, OutlineLimits{MaxRawPoints: HardMaxOutlineRawPoints + 1}, ErrLimitsInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outline, receipt, err := DecodeOutlineFont(test.ctx, test.font, test.limits)
			if !errors.Is(err, test.want) || outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("outline=%v receipt=%+v err=%v", outline, receipt, err)
			}
		})
	}
}

func TestOutlineCanonicalFixtures(t *testing.T) {
	descriptor := OutlineDescriptorCanonicalJSON()
	fixture, err := os.ReadFile("testdata/outline-profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read profile fixture: %v\ngot=%s", err, descriptor)
	}
	if !bytes.Equal(fixture, append(append([]byte(nil), descriptor...), '\n')) {
		t.Fatalf("profile fixture drift\ngot=%s", descriptor)
	}
	profileDigest := sha256.Sum256(descriptor)
	if got := hex.EncodeToString(profileDigest[:]); got != "efe71261352276448d633988b00fd566bff3b6287358191459f10ed1d0d8bd0b" {
		t.Fatalf("profile canonical hash=%s", got)
	}

	font := parseOutlineTestFont(t, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	)))
	_, receipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptFixture, err := os.ReadFile("testdata/minimal-outline-scan-receipt-v1.canonical.json")
	if err != nil {
		t.Fatalf("read receipt fixture: %v\ngot=%s", err, receiptJSON)
	}
	if !bytes.Equal(receiptFixture, append(append([]byte(nil), receiptJSON...), '\n')) {
		t.Fatalf("receipt fixture drift\ngot=%s", receiptJSON)
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	if got := hex.EncodeToString(receiptDigest[:]); got != "803a3f8ada46c011d80efe5ce90690185c39b7ce8b5365cda627578c1fed1976" {
		t.Fatalf("receipt canonical hash=%s", got)
	}
}

type outlineTestComponent struct {
	flags      uint16
	glyphID    uint16
	arg1, arg2 int32
	matrix     []int16
}

func outlineTestTables(t testFataler, bodies [][]byte, maxima outlineTestMaxp) map[string][]byte {
	t.Helper()
	if len(bodies) < 2 || len(bodies) > 0xffff {
		t.Fatalf("invalid glyph body count %d", len(bodies))
	}
	tables := minimalTestTables(uint16(len(bodies)), makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	))
	binary.BigEndian.PutUint16(tables["head"][50:52], 1)
	loca := make([]byte, 4*(len(bodies)+1))
	var glyf []byte
	for index, body := range bodies {
		binary.BigEndian.PutUint32(loca[index*4:index*4+4], uint32(len(glyf)))
		glyf = append(glyf, body...)
	}
	binary.BigEndian.PutUint32(loca[len(bodies)*4:], uint32(len(glyf)))
	tables["glyf"], tables["loca"] = glyf, loca
	maxp := tables["maxp"]
	binary.BigEndian.PutUint16(maxp[6:8], maxima.points)
	binary.BigEndian.PutUint16(maxp[8:10], maxima.contours)
	binary.BigEndian.PutUint16(maxp[10:12], maxima.compositePoints)
	binary.BigEndian.PutUint16(maxp[12:14], maxima.compositeContours)
	binary.BigEndian.PutUint16(maxp[26:28], maxima.instructions)
	binary.BigEndian.PutUint16(maxp[28:30], maxima.componentElements)
	binary.BigEndian.PutUint16(maxp[30:32], maxima.depth)
	return tables
}

func makeOutlineSimpleGlyph(endPoints []uint16, points []outlineTestPoint, instructions []byte, overlap bool, padding int) []byte {
	result := make([]byte, 10+len(endPoints)*2+2)
	binary.BigEndian.PutUint16(result[0:2], uint16(len(endPoints)))
	if len(points) > 0 {
		xMin, xMax, yMin, yMax := points[0].x, points[0].x, points[0].y, points[0].y
		for _, point := range points[1:] {
			if point.x < xMin {
				xMin = point.x
			}
			if point.x > xMax {
				xMax = point.x
			}
			if point.y < yMin {
				yMin = point.y
			}
			if point.y > yMax {
				yMax = point.y
			}
		}
		binary.BigEndian.PutUint16(result[2:4], uint16(xMin))
		binary.BigEndian.PutUint16(result[4:6], uint16(yMin))
		binary.BigEndian.PutUint16(result[6:8], uint16(xMax))
		binary.BigEndian.PutUint16(result[8:10], uint16(yMax))
	}
	for index, endPoint := range endPoints {
		binary.BigEndian.PutUint16(result[10+index*2:], endPoint)
	}
	at := 10 + len(endPoints)*2
	binary.BigEndian.PutUint16(result[at:at+2], uint16(len(instructions)))
	result = append(result, instructions...)
	flags := make([]byte, len(points))
	for index, point := range points {
		if point.onCurve {
			flags[index] |= simpleOnCurve
		}
	}
	if overlap && len(flags) > 0 {
		flags[0] |= simpleOverlap
	}
	result = append(result, flags...)
	var previous int16
	for _, point := range points {
		delta := int32(point.x) - int32(previous)
		previous = point.x
		result = binary.BigEndian.AppendUint16(result, uint16(int16(delta)))
	}
	previous = 0
	for _, point := range points {
		delta := int32(point.y) - int32(previous)
		previous = point.y
		result = binary.BigEndian.AppendUint16(result, uint16(int16(delta)))
	}
	return append(result, make([]byte, padding)...)
}

func makeOutlineCompoundGlyph(components []outlineTestComponent, instructions []byte, padding int) []byte {
	result := make([]byte, 10)
	binary.BigEndian.PutUint16(result[0:2], 0xffff)
	for index, component := range components {
		flags := component.flags
		if index < len(components)-1 {
			flags |= componentMore
		} else {
			flags &^= componentMore
		}
		if index == len(components)-1 && instructions != nil {
			flags |= componentInstructions
		}
		result = binary.BigEndian.AppendUint16(result, flags)
		result = binary.BigEndian.AppendUint16(result, component.glyphID)
		if flags&componentArgWords != 0 {
			result = binary.BigEndian.AppendUint16(result, uint16(component.arg1))
			result = binary.BigEndian.AppendUint16(result, uint16(component.arg2))
		} else {
			result = append(result, byte(component.arg1), byte(component.arg2))
		}
		for _, value := range component.matrix {
			result = binary.BigEndian.AppendUint16(result, uint16(value))
		}
	}
	if instructions != nil {
		result = binary.BigEndian.AppendUint16(result, uint16(len(instructions)))
		result = append(result, instructions...)
	}
	return append(result, make([]byte, padding)...)
}

func parseOutlineTestFont(t testing.TB, tables map[string][]byte) *Font {
	t.Helper()
	font, _, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return font
}

func isZeroOutlineReceipt(value OutlineReceipt) bool { return value == (OutlineReceipt{}) }

func oracleOutlineReceiptStringBytes(value OutlineReceipt) uint64 {
	values := []string{
		value.Profile, value.UpstreamProfile, value.UpstreamContentProfile, value.UpstreamContentHash,
		value.DescriptorHashProfile, value.DescriptorHash, value.GraphHashProfile, value.GraphHash,
		value.GeometryStatus, value.HintingStatus, value.LayoutStatus, value.MaxpStatus,
		value.MetricsStatus, value.RoundXYStatus, value.PointMatchStatus, value.OffsetStatus,
	}
	var total uint64
	for _, text := range values {
		total += uint64(len(text))
	}
	return total
}

func oracleOutlineGraphHash(font *Font, outline *OutlineFont) (string, uint64) {
	var encoded []byte
	putU8 := func(value uint8) { encoded = append(encoded, value) }
	putU16 := func(value uint16) { encoded = binary.BigEndian.AppendUint16(encoded, value) }
	putU32 := func(value uint32) { encoded = binary.BigEndian.AppendUint32(encoded, value) }
	putI32 := func(value int32) { encoded = binary.BigEndian.AppendUint32(encoded, uint32(value)) }
	putU64 := func(value uint64) { encoded = binary.BigEndian.AppendUint64(encoded, value) }
	putString := func(value string) { putU64(uint64(len(value))); encoded = append(encoded, value...) }
	putString(OutlineGraphHashProfileV1)
	putU32(OutlineProfileSchemaVersion)
	putString(OutlineProfileV1)
	putString(font.receipt.ContentHashProfile)
	putString(font.receipt.ContentHash)
	putU32(uint32(len(outline.glyphs)))
	glyf, _ := findRecord(font.tables, "glyf")
	putU64(uint64(glyf.length))
	for glyphID, glyph := range outline.glyphs {
		putU32(uint32(glyphID))
		switch glyph.kind {
		case OutlineGlyphEmptyRange:
			putU8(0)
		case OutlineGlyphSimple:
			putU8(1)
		case OutlineGlyphCompound:
			putU8(2)
		default:
			panic("unknown outline kind in oracle")
		}
		putU32(glyph.sourceBytes)
		putI32(int32(glyph.numberOfContours))
		putI32(int32(glyph.xMin))
		putI32(int32(glyph.yMin))
		putI32(int32(glyph.xMax))
		putI32(int32(glyph.yMax))
		putU32(uint32(len(glyph.instructions)))
		encoded = append(encoded, glyph.instructions...)
		putU8(glyph.paddingBytes)
		if glyph.kind == OutlineGlyphSimple {
			putU64(uint64(len(glyph.endPoints)))
			for _, endPoint := range glyph.endPoints {
				putU32(endPoint)
			}
			putU64(uint64(len(glyph.points)))
			for _, point := range glyph.points {
				putI32(int32(point.x))
				putI32(int32(point.y))
				if point.onCurve {
					putU8(1)
				} else {
					putU8(0)
				}
			}
			if glyph.overlapDeclared {
				putU8(1)
			} else {
				putU8(0)
			}
		} else if glyph.kind == OutlineGlyphCompound {
			putU64(uint64(len(glyph.components)))
			for _, component := range glyph.components {
				putU16(component.flags)
				putU32(component.glyphID)
				putU8(component.argumentMode)
				putI32(component.arg1)
				putI32(component.arg2)
				for _, coefficient := range component.matrix {
					putI32(int32(coefficient))
				}
			}
			putU64(glyph.expandedPoints)
			putU64(glyph.expandedContours)
			putU64(glyph.expandedEdges)
			putU32(glyph.componentDepth)
		}
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), uint64(len(encoded))
}
