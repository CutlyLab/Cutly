package sfntglyf

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestDecodeGDEFFoundationVersionsAbsentAndFailureZero(t *testing.T) {
	points := make([]uint32, 8)
	for _, minor := range []uint16{0, 2, 3} {
		raw := layoutTestHeader(minor)
		foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
		if err != nil {
			t.Fatalf("minor %d: %v", minor, err)
		}
		if foundation.majorVersion != 1 || foundation.minorVersion != minor || stats.nodes != 1 || len(foundation.nodes) != 1 {
			t.Fatalf("minor %d: foundation=%#v stats=%#v", minor, foundation, stats)
		}
	}

	absent, absentStats, err := decodeGDEFFoundation(context.Background(), nil, 8, points, layoutDecodeLimits{})
	if err != nil || absent == nil || absent.majorVersion != 0 || absentStats != (layoutDecodeStats{}) {
		t.Fatalf("absent: foundation=%#v stats=%#v err=%v", absent, absentStats, err)
	}

	for _, raw := range [][]byte{
		layoutTestHeader(0)[:11], layoutTestHeader(2)[:13], layoutTestHeader(3)[:17],
		layoutTestWords(0, 1), layoutTestWords(1, 1, 0, 0, 0, 0),
	} {
		layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
	}

	variable := layoutTestHeader(3)
	layoutTestPut32(variable, 14, 18)
	_, _, err = decodeGDEFFoundation(context.Background(), variable, 8, points, layoutDecodeLimits{})
	if !layoutUnsupportedVariation(err) {
		t.Fatalf("item variation store error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	foundation, stats, err := decodeGDEFFoundation(ctx, layoutTestHeader(0), 8, points, layoutDecodeLimits{})
	if foundation != nil || stats != (layoutDecodeStats{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled published state: foundation=%v stats=%#v err=%v", foundation, stats, err)
	}
	foundation, stats, err = decodeGDEFFoundation(nil, layoutTestHeader(0), 8, points, layoutDecodeLimits{})
	if foundation != nil || stats != (layoutDecodeStats{}) || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context published state: foundation=%v stats=%#v err=%v", foundation, stats, err)
	}
	layoutRequireDecodeFailure(t, layoutTestHeader(0), 8, points[:7], layoutDecodeLimits{})
}

func TestDecodeGDEFFoundationClassesAttachAndOwnership(t *testing.T) {
	raw := layoutTestClassAttachFixture()
	points := make([]uint32, 8)
	points[5] = 4
	foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(layoutTestClassValues(foundation.glyphClasses, 8), []uint16{0, 0, 1, 2, 3, 4, 0, 0}) {
		t.Fatalf("glyph classes = %v", foundation.glyphClasses)
	}
	if !slices.Equal(layoutTestClassValues(foundation.markAttachClasses, 8), []uint16{0, 0, 0, 0, 0, 2, 0, 0}) {
		t.Fatalf("mark attach classes = %v", foundation.markAttachClasses)
	}
	if len(foundation.attachments) != 1 || foundation.attachments[0].glyphID != 5 ||
		!slices.Equal(foundation.attachments[0].pointIndices, []uint16{1, 3}) {
		t.Fatalf("attachments = %#v", foundation.attachments)
	}
	if stats.classDefTables != 2 || stats.classFormat1Values != 4 || stats.classRanges != 1 ||
		stats.classAssignments != 5 || stats.coverageTables != 1 || stats.coverageRanges != 1 ||
		stats.attachmentGlyphReferences != 1 || stats.attachPointTables != 1 || stats.attachmentPoints != 2 {
		t.Fatalf("stats = %#v", stats)
	}
	if stats.maxStructuralDepth != 2 || stats.directBytes != uint64(len(raw)) {
		t.Fatalf("span stats = %#v", stats)
	}
	resources := stats.foundationResourceStats()
	if resources.nodes != stats.nodes || resources.classAssignments != 5 || resources.attachmentPoints != 2 {
		t.Fatalf("resource adapter = %#v", resources)
	}

	wantClasses := layoutTestClassValues(foundation.glyphClasses, 8)
	wantPoints := append([]uint16(nil), foundation.attachments[0].pointIndices...)
	for index := range raw {
		raw[index] ^= 0xff
	}
	points[5] = 0
	if !slices.Equal(layoutTestClassValues(foundation.glyphClasses, 8), wantClasses) || !slices.Equal(foundation.attachments[0].pointIndices, wantPoints) {
		t.Fatal("foundation retained caller-owned input")
	}
}

func TestDecodeGDEFLigCaretAllFormats(t *testing.T) {
	raw := layoutTestMixedCaretFixture()
	points := make([]uint32, 8)
	points[5] = 3
	foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(foundation.ligatureCarets) != 1 || len(foundation.ligatureCarets[0].carets) != 3 {
		t.Fatalf("ligatures = %#v", foundation.ligatureCarets)
	}
	carets := foundation.ligatureCarets[0].carets
	if carets[0].kind != gdefCaretCoordinate || carets[0].coordinate != 100 ||
		carets[1].kind != gdefCaretPoint || carets[1].pointIndex != 2 ||
		carets[2].kind != gdefCaretCoordinateDevice || carets[2].coordinate != 200 || carets[2].device == nil ||
		!slices.Equal(carets[2].device.deltas, []int16{1, 2, -1, 0}) {
		t.Fatalf("carets = %#v", carets)
	}
	if stats.caretValueTables != 3 || stats.caretValueReferences != 3 || stats.caretPointReferences != 1 ||
		stats.deviceTables != 1 || stats.deviceDeltas != 4 || stats.deviceWords != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestDecodeGDEFDeviceFormatsAndSignedPacking(t *testing.T) {
	raw, deviceStarts := layoutTestAllDeviceFormatsFixture()
	points := make([]uint32, 8)
	foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	carets := foundation.ligatureCarets[0].carets
	want := [][]int16{
		{-2, -1, 0, 1, -2, -1, 0, 1, 1},
		{1, 2, -1, 0},
		{127, -128, -1},
	}
	for index := range want {
		if carets[index].device == nil || !slices.Equal(carets[index].device.deltas, want[index]) {
			t.Fatalf("device %d = %#v", index, carets[index].device)
		}
	}
	if stats.deviceTables != 3 || stats.deviceDeltas != 16 || stats.deviceWords != 5 {
		t.Fatalf("stats = %#v", stats)
	}

	// Format 2 has four four-bit deltas, which fill its packed word exactly.
	// Formats 1 and 3 leave padding bits that the strict profile rejects.
	for _, index := range []int{0, 2} {
		start := deviceStarts[index]
		mutated := append([]byte(nil), raw...)
		lastWord := start + []int{8, 6, 8}[index]
		mutated[lastWord+1] |= 1
		t.Run(fmt.Sprintf("format_%d_padding", index+1), func(t *testing.T) {
			layoutRequireDecodeFailure(t, mutated, 8, points, layoutDecodeLimits{})
		})
	}
	variation := append([]byte(nil), raw...)
	layoutTestPut16(variation, deviceStarts[0]+4, 0x8000)
	foundation, zero, err := decodeGDEFFoundation(context.Background(), variation, 8, points, layoutDecodeLimits{})
	if foundation != nil || zero != (layoutDecodeStats{}) || !layoutUnsupportedVariation(err) {
		t.Fatalf("variation index: foundation=%v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestDecodeGDEFMarkSetsOddOffsetExactAliasAndOffset32(t *testing.T) {
	raw := layoutTestAliasedMarkSetsFixture(15, 12, 2)
	points := make([]uint32, 8)
	foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(foundation.markGlyphSets) != 2 || !slices.Equal(layoutTestCoverageGlyphs(foundation.markGlyphSets[0]), []uint16{3, 7}) ||
		!slices.Equal(layoutTestCoverageGlyphs(foundation.markGlyphSets[1]), []uint16{3, 7}) {
		t.Fatalf("sets = %#v", foundation.markGlyphSets)
	}
	if stats.nodes != 3 || stats.edges != 3 || stats.aliasReferences != 1 || stats.markGlyphSetReferences != 2 {
		t.Fatalf("stats = %#v", stats)
	}
	if foundation.nodes[1].start != 15 || foundation.nodes[2].start != 27 {
		t.Fatalf("node order = %#v", foundation.nodes)
	}

	high := layoutTestAliasedMarkSetsFixture(14, 0x10008, 1)
	highFoundation, _, err := decodeGDEFFoundation(context.Background(), high, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(highFoundation.markGlyphSets) != 1 || !slices.Equal(layoutTestCoverageGlyphs(highFoundation.markGlyphSets[0]), []uint16{3, 7}) {
		t.Fatalf("Offset32 set = %#v", highFoundation.markGlyphSets)
	}
}

func TestDecodeGDEFExactAliasUsesPerReferencePointBounds(t *testing.T) {
	points := make([]uint32, 8)
	points[5], points[6] = 10, 4
	attach := layoutTestAliasedAttachFixture()
	foundation, stats, err := decodeGDEFFoundation(context.Background(), attach, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(foundation.attachments) != 2 || stats.aliasReferences != 1 {
		t.Fatalf("attach alias foundation=%#v stats=%#v", foundation, stats)
	}
	invalidPoints := append([]uint32(nil), points...)
	invalidPoints[6] = 3
	layoutRequireDecodeFailure(t, attach, 8, invalidPoints, layoutDecodeLimits{})

	ligature := layoutTestAliasedLigGlyphFixture()
	foundation, stats, err = decodeGDEFFoundation(context.Background(), ligature, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(foundation.ligatureCarets) != 2 || stats.aliasReferences != 1 || stats.ligGlyphTables != 1 ||
		stats.caretValueTables != 1 || stats.caretValueReferences != 2 || stats.caretPointReferences != 2 ||
		foundation.ligatureCarets[1].carets[0].pointIndex != 3 {
		t.Fatalf("lig alias foundation=%#v stats=%#v", foundation, stats)
	}
	layoutRequireDecodeFailure(t, ligature, 8, invalidPoints, layoutDecodeLimits{})
}

func TestDecodeGDEFHostileOrderingFormatsAndBounds(t *testing.T) {
	points := make([]uint32, 8)
	points[5] = 4
	base := layoutTestClassAttachFixture()
	mutations := []func([]byte){
		func(raw []byte) { layoutTestPut16(raw, 24, 5) },   // GlyphClassDef class > 4.
		func(raw []byte) { layoutTestPut16(raw, 56, 256) }, // MarkAttach class > 255.
		func(raw []byte) { layoutTestPut16(raw, 36, 1) },   // Duplicate AttachPoint index.
		func(raw []byte) { layoutTestPut16(raw, 38, 3) },   // Unknown Coverage format.
		func(raw []byte) { layoutTestPut16(raw, 46, 1) },   // Coverage range starts at wrong index.
	}
	for index, mutate := range mutations {
		raw := append([]byte(nil), base...)
		mutate(raw)
		if foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{}); err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
			t.Fatalf("mutation %d accepted: foundation=%#v stats=%#v err=%v", index, foundation, stats, err)
		}
	}
	pointBound := make([]uint32, 8)
	pointBound[5] = 3
	layoutRequireDecodeFailure(t, base, 8, pointBound, layoutDecodeLimits{})

	mixed := layoutTestMixedCaretFixture()
	unknownCaret := append([]byte(nil), mixed...)
	layoutTestPut16(unknownCaret, 32, 4)
	layoutRequireDecodeFailure(t, unknownCaret, 8, points, layoutDecodeLimits{})
	pointOOB := append([]byte(nil), mixed...)
	layoutTestPut16(pointOOB, 38, 4)
	layoutRequireDecodeFailure(t, pointOOB, 8, points, layoutDecodeLimits{})

	devices, _ := layoutTestAllDeviceFormatsFixture()
	unsorted := append([]byte(nil), devices...)
	layoutTestPut16(unsorted, 50, 50)
	layoutRequireDecodeFailure(t, unsorted, 8, make([]uint32, 8), layoutDecodeLimits{})

	mark := layoutTestAliasedMarkSetsFixture(14, 8, 1)
	layoutTestPut16(mark, 14, 2)
	layoutRequireDecodeFailure(t, mark, 8, make([]uint32, 8), layoutDecodeLimits{})
}

func TestDecodeGDEFCoverageAndClassDefAdversarialRecords(t *testing.T) {
	points := make([]uint32, 8)

	coverage1 := layoutTestAliasedMarkSetsFixture(14, 8, 1)
	coverage1Start := 22
	coverage1Mutations := []struct {
		name   string
		mutate func([]byte)
	}{
		{"duplicate", func(raw []byte) { layoutTestPut16(raw, coverage1Start+6, 3) }},
		{"descending", func(raw []byte) { layoutTestPut16(raw, coverage1Start+6, 2) }},
		{"glyph_equals_num_glyphs", func(raw []byte) { layoutTestPut16(raw, coverage1Start+6, 8) }},
	}
	for _, test := range coverage1Mutations {
		t.Run("coverage_format1_"+test.name, func(t *testing.T) {
			raw := append([]byte(nil), coverage1...)
			test.mutate(raw)
			layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
		})
	}

	coverage2 := layoutTestCoverageFormat2Fixture()
	coverage2Start := 22
	coverage2Mutations := []struct {
		name   string
		mutate func([]byte)
	}{
		{"start_after_end", func(raw []byte) { layoutTestPut16(raw, coverage2Start+4, 3) }},
		{"overlap", func(raw []byte) { layoutTestPut16(raw, coverage2Start+10, 2) }},
		{"bad_later_coverage_index", func(raw []byte) { layoutTestPut16(raw, coverage2Start+14, 1) }},
	}
	for _, test := range coverage2Mutations {
		t.Run("coverage_format2_"+test.name, func(t *testing.T) {
			raw := append([]byte(nil), coverage2...)
			test.mutate(raw)
			layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
		})
	}

	class1 := layoutTestClassAttachFixture()
	class1Mutations := []struct {
		name   string
		mutate func([]byte)
	}{
		{"unknown_format", func(raw []byte) { layoutTestPut16(raw, 12, 3) }},
		{"format1_end_overflow", func(raw []byte) { layoutTestPut16(raw, 14, 5) }},
	}
	for _, test := range class1Mutations {
		t.Run("class_"+test.name, func(t *testing.T) {
			raw := append([]byte(nil), class1...)
			test.mutate(raw)
			layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
		})
	}

	class2 := layoutTestClassFormat2Fixture()
	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"start_after_end", func(raw []byte) { layoutTestPut16(raw, 16, 3) }},
		{"overlap", func(raw []byte) { layoutTestPut16(raw, 22, 2) }},
		{"class_too_large", func(raw []byte) { layoutTestPut16(raw, 26, 5) }},
	} {
		t.Run("class_format2_"+test.name, func(t *testing.T) {
			raw := append([]byte(nil), class2...)
			test.mutate(raw)
			layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
		})
	}
}

func TestDecodeGDEFCompactRangeOwnershipAtMaxGlyphCount(t *testing.T) {
	const glyphCount = uint32(65_535)
	points := make([]uint32, glyphCount)
	classes, classStats, err := decodeGDEFFoundation(context.Background(), layoutTestLargeClassRangeFixture(),
		glyphCount, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if classes.glyphClasses == nil || len(classes.glyphClasses.values) != 0 || len(classes.glyphClasses.ranges) != 1 ||
		classes.glyphClasses.classAt(0) != 1 || classes.glyphClasses.classAt(glyphCount-1) != 1 ||
		classStats.classAssignments != uint64(glyphCount) || classStats.retainedBytes > 4_096 {
		t.Fatalf("compact classes=%#v stats=%#v", classes.glyphClasses, classStats)
	}
	coverage, coverageStats, err := decodeGDEFFoundation(context.Background(), layoutTestLargeCoverageRangeFixture(),
		glyphCount, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(coverage.markGlyphSets) != 1 || len(coverage.markGlyphSets[0].glyphs) != 0 ||
		len(coverage.markGlyphSets[0].ranges) != 1 || coverage.markGlyphSets[0].glyphCount != glyphCount ||
		coverageStats.coverageGlyphs != uint64(glyphCount) || coverageStats.retainedBytes > 4_096 {
		t.Fatalf("compact coverage=%#v stats=%#v", coverage.markGlyphSets, coverageStats)
	}
	first, firstOK := coverage.markGlyphSets[0].glyphAt(0)
	last, lastOK := coverage.markGlyphSets[0].glyphAt(uint64(glyphCount - 1))
	if !firstOK || !lastOK || first != 0 || uint32(last) != glyphCount-1 {
		t.Fatalf("coverage lookup first=%d/%t last=%d/%t", first, firstOK, last, lastOK)
	}
}

func TestDecodeGDEFDedicatedTableCountAndDepthLimits(t *testing.T) {
	tests := []struct {
		name   string
		raw    []byte
		points []uint32
		count  func(layoutDecodeStats) uint64
		set    func(*layoutDecodeLimits, uint64)
	}{
		{name: "coverage_tables", raw: layoutTestMultipleMarkCoveragesFixture(), points: make([]uint32, 8),
			count: func(v layoutDecodeStats) uint64 { return v.coverageTables },
			set:   func(v *layoutDecodeLimits, n uint64) { v.maxCoverageTables = n }},
		{name: "class_tables", raw: layoutTestClassAttachFixture(), points: func() []uint32 { v := make([]uint32, 8); v[5] = 4; return v }(),
			count: func(v layoutDecodeStats) uint64 { return v.classDefTables },
			set:   func(v *layoutDecodeLimits, n uint64) { v.maxClassDefTables = n }},
		{name: "device_tables", raw: func() []byte { v, _ := layoutTestAllDeviceFormatsFixture(); return v }(), points: make([]uint32, 8),
			count: func(v layoutDecodeStats) uint64 { return v.deviceTables },
			set:   func(v *layoutDecodeLimits, n uint64) { v.maxDeviceTables = n }},
		{name: "attach_point_tables", raw: layoutTestTwoAttachPointsFixture(), points: func() []uint32 { v := make([]uint32, 8); v[5], v[6] = 1, 1; return v }(),
			count: func(v layoutDecodeStats) uint64 { return v.attachPointTables },
			set:   func(v *layoutDecodeLimits, n uint64) { v.maxAttachPointTables = n }},
		{name: "lig_glyph_tables", raw: layoutTestTwoLigGlyphsFixture(), points: make([]uint32, 8),
			count: func(v layoutDecodeStats) uint64 { return v.ligGlyphTables },
			set:   func(v *layoutDecodeLimits, n uint64) { v.maxLigGlyphTables = n }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, stats, err := decodeGDEFFoundation(context.Background(), test.raw, 8, test.points, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			count := test.count(stats)
			if count < 2 {
				t.Fatalf("fixture count=%d stats=%#v", count, stats)
			}
			exact := layoutDecodeLimits{}
			test.set(&exact, count)
			if _, _, err := decodeGDEFFoundation(context.Background(), test.raw, 8, test.points, exact); err != nil {
				t.Fatalf("N rejected: %v", err)
			}
			below := layoutDecodeLimits{}
			test.set(&below, count-1)
			if foundation, zero, err := decodeGDEFFoundation(context.Background(), test.raw, 8, test.points, below); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("N-1 foundation=%#v stats=%#v err=%v", foundation, zero, err)
			}
		})
	}

	raw := layoutTestMixedCaretFixture()
	points := make([]uint32, 8)
	points[5] = 3
	_, stats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil || stats.maxStructuralDepth < 2 {
		t.Fatalf("depth stats=%#v err=%v", stats, err)
	}
	if _, _, err := decodeGDEFFoundation(context.Background(), raw, 8, points,
		layoutDecodeLimits{maxStructuralDepth: stats.maxStructuralDepth}); err != nil {
		t.Fatalf("depth N rejected: %v", err)
	}
	if foundation, zero, err := decodeGDEFFoundation(context.Background(), raw, 8, points,
		layoutDecodeLimits{maxStructuralDepth: stats.maxStructuralDepth - 1}); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("depth N-1 foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestDecodeGDEFRequiredOffsetsAndDeviceHeaders(t *testing.T) {
	points := make([]uint32, 8)
	points[5], points[6] = 8, 8

	for _, test := range []struct {
		name   string
		base   func() []byte
		mutate func([]byte)
	}{
		{
			name:   "attach_coverage_null",
			base:   layoutTestClassAttachFixture,
			mutate: func(raw []byte) { layoutTestPut16(raw, 26, 0) },
		},
		{
			name:   "attach_point_null",
			base:   layoutTestClassAttachFixture,
			mutate: func(raw []byte) { layoutTestPut16(raw, 30, 0) },
		},
		{
			name:   "lig_coverage_null",
			base:   layoutTestMixedCaretFixture,
			mutate: func(raw []byte) { layoutTestPut16(raw, 12, 0) },
		},
		{
			name:   "lig_glyph_null",
			base:   layoutTestMixedCaretFixture,
			mutate: func(raw []byte) { layoutTestPut16(raw, 16, 0) },
		},
		{
			name:   "caret_null",
			base:   layoutTestMixedCaretFixture,
			mutate: func(raw []byte) { layoutTestPut16(raw, 26, 0) },
		},
		{
			name:   "device_null",
			base:   layoutTestMixedCaretFixture,
			mutate: func(raw []byte) { layoutTestPut16(raw, 44, 0) },
		},
		{
			name:   "mark_set_coverage_null",
			base:   func() []byte { return layoutTestAliasedMarkSetsFixture(14, 8, 1) },
			mutate: func(raw []byte) { layoutTestPut32(raw, 18, 0) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := test.base()
			test.mutate(raw)
			layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
		})
	}

	devices, starts := layoutTestAllDeviceFormatsFixture()
	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"end_before_start", func(raw []byte) { layoutTestPut16(raw, starts[0]+2, 9) }},
		{"format_zero", func(raw []byte) { layoutTestPut16(raw, starts[0]+4, 0) }},
		{"format_four", func(raw []byte) { layoutTestPut16(raw, starts[0]+4, 4) }},
		{"reserved_8001", func(raw []byte) { layoutTestPut16(raw, starts[0]+4, 0x8001) }},
	} {
		t.Run("device_"+test.name, func(t *testing.T) {
			raw := append([]byte(nil), devices...)
			test.mutate(raw)
			layoutRequireDecodeFailure(t, raw, 8, points, layoutDecodeLimits{})
		})
	}
}

func TestLayoutSpanGraphAliasOverlapTypePunAndCycle(t *testing.T) {
	newGraph := func() *layoutSpanGraph {
		limits, err := normalizeLayoutDecodeLimits(layoutDecodeLimits{})
		if err != nil {
			t.Fatal(err)
		}
		reader := newLayoutReader(context.Background(), make([]byte, 64), newLayoutResourceMeter(64, 1, limits))
		return reader.graph
	}

	graph := newGraph()
	first, alias, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanCoverage, layoutDecodeParams{first: 1}, "test/first")
	if err != nil || alias {
		t.Fatalf("first begin = %v %v", alias, err)
	}
	if err := graph.complete(first, &gdefCoverage{}, "test/first"); err != nil {
		t.Fatal(err)
	}
	aliased, alias, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanCoverage, layoutDecodeParams{first: 1}, "test/alias")
	if err != nil || !alias || aliased != first {
		t.Fatalf("exact alias = %p %v %v", aliased, alias, err)
	}
	if _, _, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanDevice, layoutDecodeParams{first: 1}, "test/type_pun"); err == nil {
		t.Fatal("type-pun alias accepted")
	}
	if _, _, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanCoverage, layoutDecodeParams{first: 2}, "test/param_pun"); err == nil {
		t.Fatal("decode-parameter alias accepted")
	}

	graph = newGraph()
	active, _, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanCoverage, layoutDecodeParams{}, "test/active")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := graph.begin(active, layoutEdgeRef{role: layoutEdgeMarkSetCoverage}, 8, 16, layoutSpanCoverage, layoutDecodeParams{}, "test/cycle"); err == nil {
		t.Fatal("active exact cycle accepted")
	}

	for _, span := range [][2]uint64{{12, 20}, {10, 14}} {
		graph = newGraph()
		left, _, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanCoverage, layoutDecodeParams{}, "test/left")
		if err != nil {
			t.Fatal(err)
		}
		_ = graph.complete(left, &gdefCoverage{}, "test/left")
		right, _, err := graph.begin(nil, layoutEdgeRef{}, span[0], span[1], layoutSpanDevice, layoutDecodeParams{}, "test/right")
		if err != nil {
			t.Fatal(err)
		}
		_ = graph.complete(right, &gdefDevice{}, "test/right")
		if _, _, err := graph.validateAndSnapshot("test/snapshot"); err == nil {
			t.Fatalf("overlap %v accepted", span)
		}
	}

	graph = newGraph()
	left, _, err := graph.begin(nil, layoutEdgeRef{}, 8, 16, layoutSpanCoverage, layoutDecodeParams{}, "test/adjacent_left")
	if err != nil {
		t.Fatal(err)
	}
	_ = graph.complete(left, &gdefCoverage{}, "test/adjacent_left")
	right, _, err := graph.begin(nil, layoutEdgeRef{}, 16, 20, layoutSpanDevice, layoutDecodeParams{}, "test/adjacent_right")
	if err != nil {
		t.Fatal(err)
	}
	_ = graph.complete(right, &gdefDevice{}, "test/adjacent_right")
	if _, _, err := graph.validateAndSnapshot("test/adjacent"); err != nil {
		t.Fatalf("adjacent spans rejected: %v", err)
	}
}

func TestDecodeGDEFResourceBoundaries(t *testing.T) {
	classRaw := layoutTestClassAttachFixture()
	classPoints := make([]uint32, 8)
	classPoints[5] = 4
	classStats := layoutDecodeBaseline(t, classRaw, classPoints)
	layoutTestLimitBoundary(t, classRaw, classPoints, uint64(len(classRaw)),
		func(limits *layoutDecodeLimits, value uint64) { limits.maxTableBytes = value })
	layoutTestLimitBoundary(t, classRaw, classPoints, classStats.classAssignments,
		func(limits *layoutDecodeLimits, value uint64) { limits.maxClassAssignments = value })
	layoutTestLimitBoundary(t, classRaw, classPoints, classStats.attachmentPoints,
		func(limits *layoutDecodeLimits, value uint64) { limits.maxAttachmentPoints = value })

	markRaw := layoutTestAliasedMarkSetsFixture(15, 12, 2)
	markPoints := make([]uint32, 8)
	markStats := layoutDecodeBaseline(t, markRaw, markPoints)
	layoutTestLimitBoundary(t, markRaw, markPoints, markStats.coverageGlyphs,
		func(limits *layoutDecodeLimits, value uint64) { limits.maxCoverageGlyphs = value })

	deviceRaw, _ := layoutTestAllDeviceFormatsFixture()
	devicePoints := make([]uint32, 8)
	deviceStats := layoutDecodeBaseline(t, deviceRaw, devicePoints)
	boundaries := []struct {
		name  string
		value uint64
		set   func(*layoutDecodeLimits, uint64)
	}{
		{"nodes", deviceStats.nodes, func(l *layoutDecodeLimits, v uint64) { l.maxNodes = v }},
		{"edges", deviceStats.edges, func(l *layoutDecodeLimits, v uint64) { l.maxEdges = v }},
		{"device_deltas", deviceStats.deviceDeltas, func(l *layoutDecodeLimits, v uint64) { l.maxDeviceDeltas = v }},
		{"carets", deviceStats.caretValueReferences, func(l *layoutDecodeLimits, v uint64) { l.maxCarets = v }},
		{"work", deviceStats.workUnits, func(l *layoutDecodeLimits, v uint64) { l.maxWorkUnits = v }},
		{"working", deviceStats.workingBytes, func(l *layoutDecodeLimits, v uint64) { l.maxWorkingBytes = v }},
		{"retained", deviceStats.retainedBytes, func(l *layoutDecodeLimits, v uint64) { l.maxRetainedBytes = v }},
	}
	for _, boundary := range boundaries {
		t.Run(boundary.name, func(t *testing.T) {
			layoutTestLimitBoundary(t, deviceRaw, devicePoints, boundary.value, boundary.set)
		})
	}
}

func TestLayoutDecodeLimitEnvelopeMatchesFoundationProfile(t *testing.T) {
	defaults := defaultLayoutDecodeLimits()
	foundationDefaults := defaultLayoutFoundationLimits()
	if defaults.maxTableBytes != foundationDefaults.MaxLayoutTableBytes ||
		defaults.maxNodes != foundationDefaults.MaxNodes || defaults.maxEdges != foundationDefaults.MaxEdges ||
		defaults.maxAliasReferences != foundationDefaults.MaxAliasReferences ||
		defaults.maxCoverageGlyphs != foundationDefaults.MaxCoveredGlyphs ||
		defaults.maxClassAssignments != foundationDefaults.MaxClassAssignments ||
		defaults.maxAttachmentPoints != foundationDefaults.MaxAttachmentPoints ||
		defaults.maxCarets != foundationDefaults.MaxCaretValues ||
		defaults.maxMarkGlyphSets != foundationDefaults.MaxMarkGlyphSets ||
		defaults.maxDeviceDeltas != foundationDefaults.MaxDeviceDeltaValues ||
		defaults.maxWorkUnits != foundationDefaults.MaxWorkUnits ||
		defaults.maxWorkingBytes != foundationDefaults.MaxWorkingBytes ||
		defaults.maxRetainedBytes != foundationDefaults.MaxRetainedBytes {
		t.Fatalf("decoder defaults drifted: decoder=%#v foundation=%#v", defaults, foundationDefaults)
	}
	hard := layoutDecodeLimits{
		maxTableBytes: hardMaxLayoutFoundationTableBytes,
		maxNodes:      hardMaxLayoutFoundationNodes, maxEdges: hardMaxLayoutFoundationEdges,
		maxAliasReferences:  hardMaxLayoutFoundationAliases,
		maxCoverageGlyphs:   hardMaxLayoutFoundationCoverageGlyphs,
		maxClassAssignments: hardMaxLayoutFoundationClassAssignments,
		maxAttachmentPoints: hardMaxLayoutFoundationAttachmentPoints,
		maxCarets:           hardMaxLayoutFoundationCaretValues, maxMarkGlyphSets: hardMaxLayoutFoundationMarkGlyphSets,
		maxDeviceDeltas: hardMaxLayoutFoundationDeviceDeltas, maxWorkUnits: hardMaxLayoutFoundationWorkUnits,
		maxWorkingBytes: hardMaxLayoutFoundationWorkingBytes, maxRetainedBytes: hardMaxLayoutFoundationRetainedBytes,
	}
	if _, err := normalizeLayoutDecodeLimits(hard); err != nil {
		t.Fatalf("foundation hard envelope rejected: %v", err)
	}
}

func layoutDecodeBaseline(t *testing.T, raw []byte, points []uint32) layoutDecodeStats {
	t.Helper()
	_, stats, err := decodeGDEFFoundation(context.Background(), raw, uint32(len(points)), points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func layoutTestLimitBoundary(t *testing.T, raw []byte, points []uint32, value uint64, set func(*layoutDecodeLimits, uint64)) {
	t.Helper()
	if value <= 1 {
		t.Fatalf("boundary value %d cannot test N/N-1", value)
	}
	exact := defaultLayoutDecodeLimits()
	set(&exact, value)
	if _, _, err := decodeGDEFFoundation(context.Background(), raw, uint32(len(points)), points, exact); err != nil {
		t.Fatalf("N boundary rejected: %v", err)
	}
	low := defaultLayoutDecodeLimits()
	set(&low, value-1)
	foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, uint32(len(points)), points, low)
	if err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
		t.Fatalf("N-1 boundary accepted/published: foundation=%v stats=%#v err=%v", foundation, stats, err)
	}
}

func layoutRequireDecodeFailure(t *testing.T, raw []byte, numGlyphs uint32, points []uint32, limits layoutDecodeLimits) {
	t.Helper()
	foundation, stats, err := decodeGDEFFoundation(context.Background(), raw, numGlyphs, points, limits)
	if err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
		t.Fatalf("invalid input accepted/published: foundation=%#v stats=%#v err=%v", foundation, stats, err)
	}
}

func layoutTestHeader(minor uint16) []byte {
	size := 12
	if minor == 2 {
		size = 14
	} else if minor == 3 {
		size = 18
	}
	result := make([]byte, size)
	layoutTestPut16(result, 0, 1)
	layoutTestPut16(result, 2, minor)
	return result
}

func layoutTestClassAttachFixture() []byte {
	result := layoutTestHeader(0)
	glyphClassOffset := len(result)
	result = append(result, layoutTestWords(1, 2, 4, 1, 2, 3, 4)...)
	attachOffset := len(result)
	result = append(result, layoutTestWords(
		12, 1, 6,
		2, 1, 3,
		2, 1, 5, 5, 0,
	)...)
	markClassOffset := len(result)
	result = append(result, layoutTestWords(2, 1, 5, 5, 2)...)
	layoutTestPut16(result, 4, uint16(glyphClassOffset))
	layoutTestPut16(result, 6, uint16(attachOffset))
	layoutTestPut16(result, 10, uint16(markClassOffset))
	return result
}

func layoutTestMixedCaretFixture() []byte {
	result := layoutTestHeader(0)
	ligOffset := len(result)
	result = append(result, layoutTestWords(
		6, 1, 12,
		1, 1, 5,
		3, 8, 12, 16,
		1, 100,
		2, 2,
		3, 200, 6,
		12, 15, 2, 0x12f0,
	)...)
	layoutTestPut16(result, 8, uint16(ligOffset))
	return result
}

func layoutTestAllDeviceFormatsFixture() ([]byte, []int) {
	result := layoutTestHeader(0)
	ligOffset := len(result)
	result = append(result, layoutTestWords(
		6, 1, 12,
		1, 1, 5,
		3, 8, 24, 38,
	)...)
	ligGlyphStart := ligOffset + 12
	result = append(result, layoutTestWords(3, 100, 6, 10, 18, 1, 0xb1b1, 0x4000)...)
	result = append(result, layoutTestWords(3, 200, 6, 12, 15, 2, 0x12f0)...)
	result = append(result, layoutTestWords(3, 300, 6, 1, 3, 3, 0x7f80, 0xff00)...)
	layoutTestPut16(result, 8, uint16(ligOffset))
	return result, []int{ligGlyphStart + 14, ligGlyphStart + 30, ligGlyphStart + 44}
}

func layoutTestAliasedMarkSetsFixture(markStart int, coverageRelative uint32, setCount uint16) []byte {
	header := layoutTestHeader(2)
	header = append(header, make([]byte, markStart-len(header))...)
	tableSize := 4 + 4*int(setCount)
	total := markStart + int(coverageRelative) + 8
	if minimum := markStart + tableSize; total < minimum {
		total = minimum
	}
	result := make([]byte, total)
	copy(result, header)
	layoutTestPut16(result, 12, uint16(markStart))
	layoutTestPut16(result, markStart, 1)
	layoutTestPut16(result, markStart+2, setCount)
	for index := 0; index < int(setCount); index++ {
		layoutTestPut32(result, markStart+4+4*index, coverageRelative)
	}
	coverageStart := markStart + int(coverageRelative)
	copy(result[coverageStart:], layoutTestWords(1, 2, 3, 7))
	return result
}

func layoutTestAliasedAttachFixture() []byte {
	result := layoutTestHeader(0)
	attachStart := len(result)
	result = append(result, layoutTestWords(
		12, 2, 8, 8,
		1, 3,
		1, 2, 5, 6,
	)...)
	layoutTestPut16(result, 6, uint16(attachStart))
	return result
}

func layoutTestAliasedLigGlyphFixture() []byte {
	result := layoutTestHeader(0)
	ligStart := len(result)
	result = append(result, layoutTestWords(
		16, 2, 8, 8,
		1, 4,
		2, 3,
		1, 2, 5, 6,
	)...)
	layoutTestPut16(result, 8, uint16(ligStart))
	return result
}

func layoutTestCoverageFormat2Fixture() []byte {
	result := layoutTestHeader(2)
	markStart := len(result)
	result = append(result, layoutTestWords(1, 1)...)
	result = append(result, make([]byte, 4)...)
	layoutTestPut32(result, markStart+4, 8)
	result = append(result, layoutTestWords(
		2, 2,
		1, 2, 0,
		4, 5, 2,
	)...)
	layoutTestPut16(result, 12, uint16(markStart))
	return result
}

func layoutTestClassFormat2Fixture() []byte {
	result := layoutTestHeader(0)
	classStart := len(result)
	result = append(result, layoutTestWords(
		2, 2,
		1, 2, 1,
		4, 5, 2,
	)...)
	layoutTestPut16(result, 4, uint16(classStart))
	return result
}

func layoutTestLargeClassRangeFixture() []byte {
	result := layoutTestHeader(0)
	start := len(result)
	result = append(result, layoutTestWords(2, 1, 0, 65_534, 1)...)
	layoutTestPut16(result, 4, uint16(start))
	return result
}

func layoutTestLargeCoverageRangeFixture() []byte {
	result := layoutTestHeader(2)
	markStart := len(result)
	result = append(result, layoutTestWords(1, 1)...)
	result = append(result, make([]byte, 4)...)
	layoutTestPut32(result, markStart+4, 8)
	result = append(result, layoutTestWords(2, 1, 0, 65_534, 0)...)
	layoutTestPut16(result, 12, uint16(markStart))
	return result
}

func layoutTestMultipleMarkCoveragesFixture() []byte {
	result := layoutTestHeader(2)
	markStart := len(result)
	result = append(result, layoutTestWords(1, 3)...)
	result = append(result, make([]byte, 12)...)
	for index, glyph := range []uint16{1, 2, 3} {
		offset := uint32(16 + 6*index)
		layoutTestPut32(result, markStart+4+4*index, offset)
		result = append(result, layoutTestWords(1, 1, glyph)...)
	}
	layoutTestPut16(result, 12, uint16(markStart))
	return result
}

func layoutTestTwoAttachPointsFixture() []byte {
	result := layoutTestHeader(0)
	start := len(result)
	result = append(result, layoutTestWords(
		8, 2, 16, 20,
		1, 2, 5, 6,
		1, 0,
		1, 0,
	)...)
	layoutTestPut16(result, 6, uint16(start))
	return result
}

func layoutTestTwoLigGlyphsFixture() []byte {
	result := layoutTestHeader(0)
	start := len(result)
	result = append(result, layoutTestWords(
		8, 2, 16, 24,
		1, 2, 5, 6,
		1, 4, 1, 100,
		1, 4, 1, 200,
	)...)
	layoutTestPut16(result, 8, uint16(start))
	return result
}

func layoutTestWords(words ...uint16) []byte {
	result := make([]byte, 2*len(words))
	for index, word := range words {
		layoutTestPut16(result, 2*index, word)
	}
	return result
}

func layoutTestPut16(target []byte, offset int, value uint16) {
	target[offset] = byte(value >> 8)
	target[offset+1] = byte(value)
}

func layoutTestPut32(target []byte, offset int, value uint32) {
	target[offset] = byte(value >> 24)
	target[offset+1] = byte(value >> 16)
	target[offset+2] = byte(value >> 8)
	target[offset+3] = byte(value)
}

func layoutTestClassValues(value *gdefClassDef, glyphCount uint32) []uint16 {
	result := make([]uint16, glyphCount)
	for glyphID := range result {
		result[glyphID] = value.classAt(uint32(glyphID))
	}
	return result
}

func layoutTestCoverageGlyphs(value *gdefCoverage) []uint16 {
	if value == nil {
		return nil
	}
	result := make([]uint16, value.glyphCount)
	for index := range result {
		glyph, ok := value.glyphAt(uint64(index))
		if !ok {
			return nil
		}
		result[index] = glyph
	}
	return result
}

func TestDecodeGDEFDeterministicEvidence(t *testing.T) {
	raw := layoutTestClassAttachFixture()
	points := make([]uint32, 8)
	points[5] = 4
	first, firstStats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	second, secondStats, err := decodeGDEFFoundation(context.Background(), raw, 8, points, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.nodes, second.nodes) || !reflect.DeepEqual(first.edges, second.edges) || firstStats != secondStats {
		t.Fatalf("non-deterministic evidence\nfirst=%#v %#v\nsecond=%#v %#v", first.nodes, first.edges, second.nodes, second.edges)
	}
}
