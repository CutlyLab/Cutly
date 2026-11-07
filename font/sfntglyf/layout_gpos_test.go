package sfntglyf

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestDecodeGPOSLookupTypesOneThroughNine(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	tests := []struct {
		name       string
		lookupType uint16
		subtable   []byte
		check      func(*testing.T, gposSubtable)
	}{
		{name: "single_1", lookupType: 1, subtable: gposTestSingle1(3, 0x0001, []uint16{7}, nil), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposSingle)
			if value.format != 1 || value.values[0].xPlacement != 7 {
				t.Fatalf("single=%#v", value)
			}
		}},
		{name: "single_2", lookupType: 1, subtable: gposTestSingle2([]uint16{3, 4}, 0x0002, []uint16{5, 0xfffa}), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposSingle)
			if value.format != 2 || len(value.values) != 2 || value.values[1].yPlacement != -6 {
				t.Fatalf("single=%#v", value)
			}
		}},
		{name: "pair_1", lookupType: 2, subtable: gposTestPair1(3, 4), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposPair)
			if value.format != 1 || value.pairSets[0].records[0].secondGlyph != 4 || value.pairSets[0].records[0].value1.xAdvance != -20 {
				t.Fatalf("pair=%#v", value)
			}
		}},
		{name: "pair_2", lookupType: 2, subtable: gposTestPair2(3, 4), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposPair)
			if value.format != 2 || value.class1Count != 2 || len(value.classRecords) != 4 || value.classRecords[3].value1.xAdvance != -30 {
				t.Fatalf("pair=%#v", value)
			}
		}},
		{name: "cursive", lookupType: 3, subtable: gposTestCursive(3, 1, 0), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposCursive)
			if value.records[0].entry == nil || value.records[0].exit == nil {
				t.Fatalf("cursive=%#v", value)
			}
		}},
		{name: "mark_base", lookupType: 4, subtable: gposTestMarkBase(3, 4, 1, 0), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposMarkBase)
			if value.marks.records[0].class != 0 || value.bases.records[0][0] == nil {
				t.Fatalf("mark base=%#v", value)
			}
		}},
		{name: "mark_ligature", lookupType: 5, subtable: gposTestMarkLigature(3, 4), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposMarkLigature)
			if len(value.ligatures.attaches[0].components) != 1 || value.ligatures.attaches[0].components[0][0] == nil {
				t.Fatalf("mark ligature=%#v", value)
			}
		}},
		{name: "mark_mark", lookupType: 6, subtable: gposTestMarkMark(3, 4), check: func(t *testing.T, table gposSubtable) {
			value := table.(*gposMarkMark)
			if value.marks1.records[0].anchor == nil || value.marks2.records[0][0] == nil {
				t.Fatalf("mark mark=%#v", value)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := gsubTestTable(0, test.lookupType, 0, gsubTestTag("kern"), nil, test.subtable)
			foundation, stats, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if stats.gposBytes != uint64(len(raw)) || stats.gsubBytes != 0 || stats.gposSubtables == 0 {
				t.Fatalf("stats=%#v", stats)
			}
			for _, node := range foundation.nodes {
				if node.tableTag != layoutGPOSTag {
					t.Fatalf("node tag=%#x", node.tableTag)
				}
			}
			test.check(t, foundation.lookups[0].subtables[0])
			for index := range raw {
				raw[index] ^= 0xff
			}
			test.check(t, foundation.lookups[0].subtables[0])
		})
	}

	for _, test := range []struct {
		name       string
		lookupType uint16
		subtable   []byte
	}{
		{name: "context_1", lookupType: 7, subtable: gsubTestContext1(3, []uint16{4}, []gsubSequenceLookupRecord{{lookupIndex: 1}})},
		{name: "context_2", lookupType: 7, subtable: gsubTestContext2(3, []uint16{0}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})},
		{name: "context_3", lookupType: 7, subtable: gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})},
		{name: "chain_1", lookupType: 8, subtable: gsubTestChain1(3, []uint16{2}, []uint16{4}, []uint16{5}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})},
		{name: "chain_2", lookupType: 8, subtable: gsubTestChain2(3, []uint16{1}, []uint16{0}, []uint16{1}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})},
		{name: "chain_3", lookupType: 8, subtable: gsubTestChain3([]uint16{2}, []uint16{3, 4}, []uint16{5}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})},
		{name: "extension_context", lookupType: 9, subtable: gsubTestExtension(7, gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
				{lookupType: test.lookupType, subtables: [][]byte{test.subtable}},
				{lookupType: 1, subtables: [][]byte{gposTestSingle1(6, 0, nil, nil)}},
			})
			foundation, stats, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if len(foundation.logical.edges) != 1 || foundation.logical.edges[0].toLookup != 1 ||
				!slices.Equal(foundation.logical.topological, []uint16{0, 1}) || stats.logicalLookupEdges != 1 {
				t.Fatalf("logical=%#v stats=%#v", foundation.logical, stats)
			}
		})
	}
}

func TestDecodeGPOSExtensionUnderlyingTypesOneThroughEightAndOffset32(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	for _, test := range []struct {
		lookupType uint16
		subtable   []byte
	}{
		{lookupType: 1, subtable: gposTestSingle1(3, 0, nil, nil)},
		{lookupType: 2, subtable: gposTestPair1(3, 4)},
		{lookupType: 3, subtable: gposTestCursive(3, 1, 0)},
		{lookupType: 4, subtable: gposTestMarkBase(3, 4, 1, 0)},
		{lookupType: 5, subtable: gposTestMarkLigature(3, 4)},
		{lookupType: 6, subtable: gposTestMarkMark(3, 4)},
		{lookupType: 7, subtable: gsubTestContext3([]uint16{3}, nil)},
		{lookupType: 8, subtable: gsubTestChain3(nil, []uint16{3}, nil, nil)},
	} {
		t.Run(string(rune('0'+test.lookupType)), func(t *testing.T) {
			raw := gsubTestTable(0, 9, 0, gsubTestTag("test"), nil, gsubTestExtension(test.lookupType, test.subtable))
			foundation, _, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			extension, ok := foundation.lookups[0].subtables[0].(*gposExtension)
			if !ok || extension.extensionLookupType != test.lookupType || extension.subtable == nil || extension.subtable.gposLookupType() != test.lookupType {
				t.Fatalf("extension=%#v", foundation.lookups[0].subtables[0])
			}
		})
	}

	const childOffset = 70_000
	extension32 := layoutTestWords(1, 1)
	extension32 = append(extension32, byte((childOffset>>24)&0xff), byte((childOffset>>16)&0xff), byte((childOffset>>8)&0xff), byte(childOffset&0xff))
	extension32 = append(extension32, make([]byte, childOffset-len(extension32))...)
	extension32 = append(extension32, gposTestSingle1(3, 0, nil, nil)...)
	if result, _, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 9, 0, gsubTestTag("test"), nil, extension32), 16, points, nil, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("Offset32 extension rejected: result=%#v err=%v", result, err)
	}
}

func TestDecodeGPOSDeviceBasesAndValueDomains(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	device := layoutTestWords(10, 10, 3, 0x0100)
	single := gposTestSingle1(3, 0x0011, []uint16{7, 0}, device)
	// Device follows Coverage and its offset is from the SinglePos subtable.
	layoutTestPut16(single, 8, 16)
	foundation, stats, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 1, 0, gsubTestTag("kern"), nil, single), 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatalf("single=%x err=%v", single, err)
	}
	value := foundation.lookups[0].subtables[0].(*gposSingle).values[0]
	if value.xPlaDevice == nil || !slices.Equal(value.xPlaDevice.deltas, []int16{1}) || stats.deviceTables != 1 {
		t.Fatalf("value=%#v stats=%#v", value, stats)
	}
	allFields := gposTestSingle1(3, 0x00ff, []uint16{1, 2, 3, 4, 28, 28, 28, 28}, device)
	foundation, stats, err = decodeGPOSFoundation(context.Background(), gsubTestTable(0, 1, 0, gsubTestTag("kern"), nil, allFields), 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	value = foundation.lookups[0].subtables[0].(*gposSingle).values[0]
	if value.xPlacement != 1 || value.yPlacement != 2 || value.xAdvance != 3 || value.yAdvance != 4 ||
		value.xPlaDevice == nil || value.yPlaDevice == nil || value.xAdvDevice == nil || value.yAdvDevice == nil ||
		stats.deviceTables != 1 || stats.aliasReferences < 3 {
		t.Fatalf("full ValueRecord order/alias=%#v stats=%#v", value, stats)
	}

	pair := gposTestPair1Device(3, 4, device)
	foundation, _, err = decodeGPOSFoundation(context.Background(), gsubTestTable(0, 2, 0, gsubTestTag("kern"), nil, pair), 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	pairValue := foundation.lookups[0].subtables[0].(*gposPair).pairSets[0].records[0].value1
	if pairValue.xPlaDevice == nil || pairValue.xPlaDevice.deltas[0] != 1 {
		t.Fatalf("PairSet-relative device not decoded: %#v", pairValue)
	}

	anchor3 := gposTestCursiveAnchor3Device(5, device)
	foundation, _, err = decodeGPOSFoundation(context.Background(), gsubTestTable(0, 3, 0, gsubTestTag("curs"), nil, anchor3), 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	entry := foundation.lookups[0].subtables[0].(*gposCursive).records[0].entry
	if entry == nil || entry.format != 3 || entry.xDevice == nil || entry.xDevice.deltas[0] != 1 {
		t.Fatalf("Anchor-relative device not decoded: %#v", entry)
	}

	for _, test := range []struct {
		name       string
		lookupType uint16
		raw        []byte
	}{
		{name: "reserved_value_format", raw: gposTestSingle1(3, 0x0100, nil, nil)},
		{name: "variation_index", raw: func() []byte {
			bad := gposTestSingle1(3, 0x0010, []uint16{0}, layoutTestWords(0, 0, 0x8000))
			layoutTestPut16(bad, 6, 14)
			return bad
		}()},
		{name: "device_padding", raw: func() []byte {
			badDevice := layoutTestWords(10, 10, 1, 0x4001)
			bad := gposTestSingle1(3, 0x0010, []uint16{0}, badDevice)
			layoutTestPut16(bad, 6, 14)
			return bad
		}()},
		{name: "anchor_variation_index", lookupType: 3, raw: gposTestCursiveAnchor3Device(5, layoutTestWords(0, 0, 0x8000))},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookupType := test.lookupType
			if lookupType == 0 {
				lookupType = 1
			}
			result, zero, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, lookupType, 0, gsubTestTag("kern"), nil, test.raw), 16, points, nil, layoutDecodeLimits{})
			if err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("invalid value published: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}
}

func TestDecodeGPOSAnchorPointPerReferenceAlias(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	valid := gposTestMarkBase(3, 4, 2, 7)
	if result, _, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 4, 0, gsubTestTag("mark"), nil, valid), 16, points, nil, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("point N-1 rejected: result=%#v err=%v", result, err)
	}
	points[4] = 7
	if result, zero, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 4, 0, gsubTestTag("mark"), nil, valid), 16, points, nil, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("point N accepted: result=%#v stats=%#v err=%v", result, zero, err)
	}

	// Two Cursive records alias one format-2 Anchor. The second glyph has a
	// smaller point domain, so alias cache reuse must not skip its validation.
	points = gposTestPointCounts(16, 8)
	points[4] = 1
	aliased := gposTestCursiveAlias(3, 4, 1)
	if result, zero, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 3, 0, gsubTestTag("curs"), nil, aliased), 16, points, nil, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("per-reference alias domain skipped: result=%#v stats=%#v err=%v", result, zero, err)
	}

	// Two MarkBase subtables alias the complete MarkArray and BaseArray. The
	// second mark coverage binds the cached format-2 Anchor to a smaller glyph
	// point domain, so array alias reuse must repeat semantic validation.
	points = gposTestPointCounts(16, 8)
	points[4] = 1
	aliasedArrays := gposTestAliasedMarkBaseArrays()
	if result, zero, err := decodeGPOSFoundation(context.Background(), aliasedArrays, 16, points, nil, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("aliased array domain skipped: result=%#v stats=%#v err=%v", result, zero, err)
	}
}

func TestDecodeGPOSCommonScriptAndLangSysOrdering(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	for _, test := range []struct {
		name  string
		list  []byte
		valid bool
	}{
		{name: "scripts_sorted", list: gsubTestScriptList([]uint32{layoutDFLTTag, gsubTestTag("latn")}), valid: true},
		{name: "scripts_unsorted", list: gsubTestScriptList([]uint32{gsubTestTag("latn"), layoutDFLTTag})},
		{name: "scripts_duplicate", list: gsubTestScriptList([]uint32{layoutDFLTTag, layoutDFLTTag})},
		{name: "languages_sorted", list: gsubTestLangSysScriptList([]uint32{gsubTestTag("ENG "), gsubTestTag("ZHS ")}), valid: true},
		{name: "languages_unsorted", list: gsubTestLangSysScriptList([]uint32{gsubTestTag("ZHS "), gsubTestTag("ENG ")})},
		{name: "languages_duplicate", list: gsubTestLangSysScriptList([]uint32{gsubTestTag("ENG "), gsubTestTag("ENG ")})},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, zero, err := decodeGPOSFoundation(context.Background(), gposTestTableWithScriptList(test.list), 16, points, nil, layoutDecodeLimits{})
			if test.valid {
				if err != nil || result == nil {
					t.Fatalf("valid ordering rejected: result=%#v err=%v", result, err)
				}
				return
			}
			if err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("invalid ordering published: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}
}

func TestDecodeGPOSCancellationAtEveryCheckpoint(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 9, subtables: [][]byte{gsubTestExtension(7, gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))}},
		{lookupType: 4, subtables: [][]byte{gposTestMarkBase(4, 5, 2, 7)}},
	})
	probe := &geometryCountingContext{}
	if result, _, err := decodeGPOSFoundation(probe, raw, 16, points, nil, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("probe decode failed: result=%#v err=%v", result, err)
	}
	if probe.calls < 2 {
		t.Fatalf("insufficient cancellation checkpoints: %d", probe.calls)
	}
	for cancelAt := 1; cancelAt <= probe.calls; cancelAt++ {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		result, zero, err := decodeGPOSFoundation(ctx, raw, 16, points, nil, layoutDecodeLimits{})
		if !errors.Is(err, ErrCanceled) || result != nil || zero != (layoutDecodeStats{}) {
			t.Fatalf("cancelAt=%d published: result=%#v stats=%#v err=%v", cancelAt, result, zero, err)
		}
	}
}

func TestDecodeGPOSRequiredAndNullableAttachmentOffsets(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	baseNullable := gposTestMarkBase(3, 4, 1, 0)
	// BaseArray begins at 36; its single BaseAnchor offset is nullable.
	layoutTestPut16(baseNullable, 38, 0)
	if result, _, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 4, 0, gsubTestTag("mark"), nil, baseNullable), 16, points, nil, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("nullable base anchor rejected: result=%#v err=%v", result, err)
	}
	mark2Nullable := gposTestMarkMark(3, 4)
	// Mark2Array begins at 36; Mark2Anchor offsets are nullable.
	layoutTestPut16(mark2Nullable, 38, 0)
	if result, _, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, 6, 0, gsubTestTag("mkmk"), nil, mark2Nullable), 16, points, nil, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("nullable mark2 anchor rejected: result=%#v err=%v", result, err)
	}

	for _, test := range []struct {
		name       string
		lookupType uint16
		raw        []byte
	}{
		{name: "mark_base_mark_anchor", lookupType: 4, raw: func() []byte {
			bad := gposTestMarkBase(3, 4, 1, 0)
			layoutTestPut16(bad, 28, 0)
			return bad
		}()},
		{name: "mark_ligature_attach", lookupType: 5, raw: func() []byte {
			bad := gposTestMarkLigature(3, 4)
			layoutTestPut16(bad, 38, 0)
			return bad
		}()},
		{name: "mark_ligature_components", lookupType: 5, raw: func() []byte {
			bad := gposTestMarkLigature(3, 4)
			layoutTestPut16(bad, 40, 0)
			return bad
		}()},
		{name: "mark_mark_mark1_anchor", lookupType: 6, raw: func() []byte {
			bad := gposTestMarkMark(3, 4)
			layoutTestPut16(bad, 28, 0)
			return bad
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, zero, err := decodeGPOSFoundation(context.Background(), gsubTestTable(0, test.lookupType, 0, gsubTestTag("mark"), nil, test.raw), 16, points, nil, layoutDecodeLimits{})
			if err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("required offset accepted: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}
}

func TestDecodeGPOSRejectsOrderingCardinalityFlagsCyclesAndFailsZero(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	pairDuplicate := gposTestPair1(3, 4)
	// Grow PairSet to two records with a duplicate second glyph.
	pairSetStart := 12
	coverageStart := 20
	secondRecord := append([]byte(nil), pairDuplicate[pairSetStart+2:coverageStart]...)
	pairDuplicate = append(pairDuplicate[:coverageStart], append(secondRecord, pairDuplicate[coverageStart:]...)...)
	layoutTestPut16(pairDuplicate, pairSetStart, 2)
	layoutTestPut16(pairDuplicate, 2, 28)

	selfCycle := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 7, subtables: [][]byte{
		gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 0}}),
	}}})
	mixedExtension := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 9, subtables: [][]byte{
		gsubTestExtension(1, gposTestSingle1(3, 0, nil, nil)),
		gsubTestExtension(3, gposTestCursive(4, 1, 0)),
	}}})
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "pair_second_duplicate", raw: gsubTestTable(0, 2, 0, gsubTestTag("kern"), nil, pairDuplicate)},
		{name: "single_coverage_cardinality", raw: func() []byte {
			bad := gposTestSingle2([]uint16{3}, 1, []uint16{1, 2})
			layoutTestPut16(bad, 6, 2)
			return gsubTestTable(0, 1, 0, gsubTestTag("kern"), nil, bad)
		}()},
		{name: "pair_class_domain", raw: func() []byte {
			bad := gposTestPair2(3, 4)
			layoutTestPut16(bad, 12, 1)
			return gsubTestTable(0, 2, 0, gsubTestTag("kern"), nil, bad)
		}()},
		{name: "context_sequence_index", raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 7, subtables: [][]byte{
			gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 0}}),
		}}})},
		{name: "context_lookup_index", raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 7, subtables: [][]byte{
			gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{sequenceIndex: 0, lookupIndex: 1}}),
		}}})},
		{name: "logical_self_cycle", raw: selfCycle},
		{name: "mixed_extension_type", raw: mixedExtension},
		{name: "extension_of_extension", raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 9, subtables: [][]byte{gsubTestExtension(9, gsubTestExtension(1, gposTestSingle1(3, 0, nil, nil)))}}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, zero, err := decodeGPOSFoundation(context.Background(), test.raw, 16, points, nil, layoutDecodeLimits{})
			if err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("invalid table published: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}

	flags := uint16(0x0100 | 0x0010 | 0x0002)
	flagged := gsubTestTable(0, 1, flags, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil))
	if result, _, err := decodeGPOSFoundation(context.Background(), flagged, 16, points, nil, layoutDecodeLimits{}); err == nil || result != nil {
		t.Fatal("GDEF flags accepted without GDEF")
	}
	gdef := &gdefFoundation{
		glyphClasses:      &gdefClassDef{format: 1, values: []uint16{1}},
		markAttachClasses: &gdefClassDef{format: 1, values: []uint16{1}},
		markGlyphSets:     []*gdefCoverage{{}},
	}
	if result, _, err := decodeGPOSFoundation(context.Background(), flagged, 16, points, gdef, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("bound flags rejected: result=%#v err=%v", result, err)
	}
	badFilteringSet := append([]byte(nil), flagged...)
	lookupListStart := int(uint16(badFilteringSet[8])<<8 | uint16(badFilteringSet[9]))
	// LookupList header (4 bytes) + Lookup header/subtable offset (8 bytes).
	layoutTestPut16(badFilteringSet, lookupListStart+12, 1)
	if result, zero, err := decodeGPOSFoundation(context.Background(), badFilteringSet, 16, points, gdef, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("out-of-range mark filtering set published: result=%#v stats=%#v err=%v", result, zero, err)
	}
	gdef.markAttachClasses.values[0] = 2
	if result, _, err := decodeGPOSFoundation(context.Background(), flagged, 16, points, gdef, layoutDecodeLimits{}); err == nil || result != nil {
		t.Fatal("undefined mark attachment class accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, zero, err := decodeGPOSFoundation(ctx, flagged, 16, points, gdef, layoutDecodeLimits{}); !errors.Is(err, ErrCanceled) || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("canceled decode published: result=%#v stats=%#v err=%v", result, zero, err)
	}
}

func TestDecodeGPOSResourcesExact(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	// Keep every independently gated aggregate above one. A zero limit means
	// "use the default", so a one-entry fixture cannot exercise N-1.
	raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 2, subtables: [][]byte{gposTestPair2(3, 4)}},
		{lookupType: 1, subtables: [][]byte{gposTestSingle2([]uint16{5, 6}, 0, nil)}},
	})
	_, stats, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		value uint64
		set   func(*layoutDecodeLimits, uint64)
	}{
		{name: "table_bytes", value: uint64(len(raw)), set: func(v *layoutDecodeLimits, n uint64) { v.maxTableBytes = n }},
		{name: "nodes", value: stats.nodes, set: func(v *layoutDecodeLimits, n uint64) { v.maxNodes = n }},
		{name: "edges", value: stats.edges, set: func(v *layoutDecodeLimits, n uint64) { v.maxEdges = n }},
		{name: "coverage_glyphs", value: stats.coverageGlyphs, set: func(v *layoutDecodeLimits, n uint64) { v.maxCoverageGlyphs = n }},
		{name: "class_assignments", value: stats.classAssignments, set: func(v *layoutDecodeLimits, n uint64) { v.maxClassAssignments = n }},
		{name: "ordered", value: stats.orderedRecords, set: func(v *layoutDecodeLimits, n uint64) { v.maxOrderedRecords = n }},
		{name: "work", value: stats.workUnits, set: func(v *layoutDecodeLimits, n uint64) { v.maxWorkUnits = n }},
		{name: "working", value: stats.workingBytes, set: func(v *layoutDecodeLimits, n uint64) { v.maxWorkingBytes = n }},
		{name: "retained", value: stats.retainedBytes, set: func(v *layoutDecodeLimits, n uint64) { v.maxRetainedBytes = n }},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := layoutDecodeLimits{}
			test.set(&limits, test.value)
			if _, _, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, limits); err != nil {
				t.Fatalf("N rejected: %v", err)
			}
			limits = layoutDecodeLimits{}
			test.set(&limits, test.value-1)
			if result, zero, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, limits); err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("N-1 accepted: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}
}

func TestDecodeGPOSLogicalResourcesExact(t *testing.T) {
	points := gposTestPointCounts(16, 8)
	raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 7, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 7, subtables: [][]byte{gsubTestContext3([]uint16{4}, []gsubSequenceLookupRecord{{lookupIndex: 2}})}},
		{lookupType: 1, subtables: [][]byte{gposTestSingle1(5, 0, nil, nil)}},
	})
	_, stats, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.logicalLookupEdges != 2 || stats.logicalLookupDepth != 2 || stats.logicalExpansion != 2 {
		t.Fatalf("logical stats=%#v", stats)
	}
	for _, test := range []struct {
		name  string
		apply func(*layoutDecodeLimits, bool)
	}{
		{name: "edges", apply: func(value *layoutDecodeLimits, below bool) {
			value.maxLogicalLookupEdges = stats.logicalLookupEdges
			if below {
				value.maxLogicalLookupEdges--
			}
		}},
		{name: "depth", apply: func(value *layoutDecodeLimits, below bool) {
			value.maxLogicalLookupDepth = stats.logicalLookupDepth
			if below {
				value.maxLogicalLookupDepth--
			}
		}},
		{name: "expansion", apply: func(value *layoutDecodeLimits, below bool) {
			value.maxLogicalExpansion = stats.logicalExpansion
			if below {
				value.maxLogicalExpansion--
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := layoutDecodeLimits{}
			test.apply(&limits, false)
			if result, _, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, limits); err != nil || result == nil {
				t.Fatalf("N rejected: result=%#v err=%v", result, err)
			}
			limits = layoutDecodeLimits{}
			test.apply(&limits, true)
			if result, zero, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, limits); err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("N-1 accepted: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}
}

func gposTestPointCounts(count int, points uint32) []uint32 {
	result := make([]uint32, count)
	for index := range result {
		result[index] = points
	}
	return result
}

func gposTestSingle1(glyph, format uint16, record []uint16, trailing []byte) []byte {
	recordBytes := layoutTestWords(record...)
	coverageOffset := uint16(6 + len(recordBytes))
	result := layoutTestWords(1, coverageOffset, format)
	result = append(result, recordBytes...)
	result = append(result, gsubTestCoverage(glyph)...)
	return append(result, trailing...)
}

func gposTestSingle2(glyphs []uint16, format uint16, records []uint16) []byte {
	recordBytes := layoutTestWords(records...)
	result := layoutTestWords(2, uint16(8+len(recordBytes)), format, uint16(len(glyphs)))
	result = append(result, recordBytes...)
	return append(result, gsubTestCoverage(glyphs...)...)
}

func gposTestPair1(first, second uint16) []byte {
	result := layoutTestWords(1, 20, 0x0004, 0x0001, 1, 12)
	result = append(result, layoutTestWords(1, second, 0xffec, 5)...)
	return append(result, gsubTestCoverage(first)...)
}

func gposTestPair1Device(first, second uint16, device []byte) []byte {
	// PairSet starts at 12. Its device is at offset 8 from PairSet, proving the
	// immediate-parent base rather than the enclosing PairPos base.
	result := layoutTestWords(1, 26, 0x0010, 0, 1, 12)
	result = append(result, layoutTestWords(1, second, 6)...)
	result = append(result, device...)
	return append(result, gsubTestCoverage(first)...)
}

func gposTestPair2(first, second uint16) []byte {
	result := layoutTestWords(2, 24, 0x0004, 0, 30, 38, 2, 2)
	result = append(result, layoutTestWords(0, 0, 0, 0xffe2)...)
	result = append(result, gsubTestCoverage(first)...)
	result = append(result, layoutTestWords(1, first, 1, 1)...)
	return append(result, layoutTestWords(1, second, 1, 1)...)
}

func gposTestAnchor(format, point uint16) []byte {
	if format == 2 {
		return layoutTestWords(2, 10, 20, point)
	}
	return layoutTestWords(1, 10, 20)
}

func gposTestCursive(glyph, anchorFormat, point uint16) []byte {
	entry := gposTestAnchor(anchorFormat, point)
	exit := gposTestAnchor(1, 0)
	result := layoutTestWords(1, 10, 1, 16, uint16(16+len(entry)))
	result = append(result, gsubTestCoverage(glyph)...)
	result = append(result, entry...)
	return append(result, exit...)
}

func gposTestCursiveAlias(first, second, point uint16) []byte {
	result := layoutTestWords(1, 14, 2, 22, 0, 22, 0)
	result = append(result, gsubTestCoverage(first, second)...)
	return append(result, gposTestAnchor(2, point)...)
}

func gposTestCursiveAnchor3Device(glyph uint16, device []byte) []byte {
	result := layoutTestWords(1, 10, 1, 16, 0)
	result = append(result, gsubTestCoverage(glyph)...)
	result = append(result, layoutTestWords(3, 10, 20, 10, 0)...)
	return append(result, device...)
}

func gposTestAliasedMarkBaseArrays() []byte {
	// Lookup-relative layout:
	//   10,22   MarkBase headers
	//   34..58  four independent Coverages
	//   58      shared MarkArray (format-2 point 1)
	//   72      shared BaseArray
	lookup := layoutTestWords(4, 0, 2, 10, 22)
	lookup = append(lookup,
		layoutTestWords(1, 24, 30, 1, 48, 62)...)
	lookup = append(lookup,
		layoutTestWords(1, 24, 30, 1, 36, 50)...)
	lookup = append(lookup, gsubTestCoverage(3)...)
	lookup = append(lookup, gsubTestCoverage(6)...)
	lookup = append(lookup, gsubTestCoverage(4)...)
	lookup = append(lookup, gsubTestCoverage(7)...)
	lookup = append(lookup, layoutTestWords(1, 0, 6)...)
	lookup = append(lookup, gposTestAnchor(2, 1)...)
	lookup = append(lookup, layoutTestWords(1, 4)...)
	lookup = append(lookup, gposTestAnchor(1, 0)...)

	base := gsubTestTable(0, 4, 0, gsubTestTag("mark"), nil, gposTestMarkBase(3, 6, 1, 0))
	lookupStart := int(uint16(base[8])<<8 | uint16(base[9]))
	result := append([]byte(nil), base[:lookupStart]...)
	result = append(result, layoutTestWords(1, 4)...)
	return append(result, lookup...)
}

func gposTestTableWithScriptList(scriptList []byte) []byte {
	base := gsubTestTable(0, 1, 0, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil))
	featureStart := int(uint16(base[6])<<8 | uint16(base[7]))
	lookupStart := int(uint16(base[8])<<8 | uint16(base[9]))
	result := append([]byte(nil), base[:10]...)
	scriptStart := len(result)
	result = append(result, scriptList...)
	newFeatureStart := len(result)
	result = append(result, base[featureStart:lookupStart]...)
	newLookupStart := len(result)
	result = append(result, base[lookupStart:]...)
	layoutTestPut16(result, 4, uint16(scriptStart))
	layoutTestPut16(result, 6, uint16(newFeatureStart))
	layoutTestPut16(result, 8, uint16(newLookupStart))
	return result
}

func gposTestMarkBase(mark, base, anchorFormat, point uint16) []byte {
	markAnchor := gposTestAnchor(1, 0)
	baseAnchor := gposTestAnchor(anchorFormat, point)
	markArrayOffset := uint16(24)
	markAnchorOffset := uint16(6)
	baseArrayOffset := uint16(int(markArrayOffset) + 6 + len(markAnchor))
	baseAnchorOffset := uint16(4)
	result := layoutTestWords(1, 12, 18, 1, markArrayOffset, baseArrayOffset)
	result = append(result, gsubTestCoverage(mark)...)
	result = append(result, gsubTestCoverage(base)...)
	result = append(result, layoutTestWords(1, 0, markAnchorOffset)...)
	result = append(result, markAnchor...)
	result = append(result, layoutTestWords(1, baseAnchorOffset)...)
	return append(result, baseAnchor...)
}

func gposTestMarkLigature(mark, ligature uint16) []byte {
	markAnchor := gposTestAnchor(1, 0)
	ligatureAnchor := gposTestAnchor(1, 0)
	result := layoutTestWords(1, 12, 18, 1, 24, 36)
	result = append(result, gsubTestCoverage(mark)...)
	result = append(result, gsubTestCoverage(ligature)...)
	result = append(result, layoutTestWords(1, 0, 6)...)
	result = append(result, markAnchor...)
	result = append(result, layoutTestWords(1, 4)...)
	result = append(result, layoutTestWords(1, 4)...)
	return append(result, ligatureAnchor...)
}

func gposTestMarkMark(mark1, mark2 uint16) []byte {
	mark1Anchor := gposTestAnchor(1, 0)
	mark2Anchor := gposTestAnchor(1, 0)
	result := layoutTestWords(1, 12, 18, 1, 24, 36)
	result = append(result, gsubTestCoverage(mark1)...)
	result = append(result, gsubTestCoverage(mark2)...)
	result = append(result, layoutTestWords(1, 0, 6)...)
	result = append(result, mark1Anchor...)
	result = append(result, layoutTestWords(1, 4)...)
	return append(result, mark2Anchor...)
}
