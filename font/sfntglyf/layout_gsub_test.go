package sfntglyf

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestDecodeGSUBVersionsFeatureParamsAndFailureZero(t *testing.T) {
	for _, minor := range []uint16{0, 1} {
		raw := gsubTestTable(minor, 1, 0, gsubTestTag("cv01"), gsubTestCVParams(), gsubTestSingle1(3, 2))
		foundation, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
		if err != nil {
			t.Fatalf("minor %d: %v", minor, err)
		}
		if foundation.majorVersion != 1 || foundation.minorVersion != minor || len(foundation.scripts) != 1 || len(foundation.features) != 1 || len(foundation.lookups) != 1 {
			t.Fatalf("minor %d: foundation=%#v", minor, foundation)
		}
		params, ok := foundation.features[0].params.(*layoutCVFeatureParams)
		if !ok || !slices.Equal(params.characters, []uint32{0x41, 0x1f600}) || params.featureUILabelNameID != 1 {
			t.Fatalf("minor %d params=%#v", minor, foundation.features[0].params)
		}
		if stats.gsubBytes != uint64(len(raw)) || stats.gdefBytes != 0 || stats.featureCharacters != 2 || stats.nodes == 0 || stats.edges == 0 {
			t.Fatalf("minor %d stats=%#v", minor, stats)
		}
		for _, node := range foundation.nodes {
			if node.tableTag != layoutGSUBTag {
				t.Fatalf("node table tag = %#x", node.tableTag)
			}
		}
	}

	absent, stats, err := decodeGSUBFoundation(context.Background(), nil, 16, nil, layoutDecodeLimits{})
	if err != nil || absent == nil || absent.majorVersion != 0 || stats != (layoutDecodeStats{}) {
		t.Fatalf("absent=%#v stats=%#v err=%v", absent, stats, err)
	}

	variable := gsubTestTable(1, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	layoutTestPut32(variable, 10, 14)
	if foundation, zero, err := decodeGSUBFoundation(context.Background(), variable, 16, nil, layoutDecodeLimits{}); foundation != nil || zero != (layoutDecodeStats{}) || !layoutUnsupportedVariation(err) {
		t.Fatalf("feature variations: foundation=%v stats=%#v err=%v", foundation, zero, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if foundation, zero, err := decodeGSUBFoundation(ctx, gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1)), 16, nil, layoutDecodeLimits{}); foundation != nil || zero != (layoutDecodeStats{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled published: foundation=%v stats=%#v err=%v", foundation, zero, err)
	}
	if foundation, zero, err := decodeGSUBFoundation(nil, []byte{0}, 16, nil, layoutDecodeLimits{}); foundation != nil || zero != (layoutDecodeStats{}) || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context published: foundation=%v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestDecodeGSUBLookupTypesOneThroughFour(t *testing.T) {
	tests := []struct {
		name       string
		lookupType uint16
		subtable   []byte
		check      func(*testing.T, gsubSubtable)
	}{
		{name: "single_format_1_negative_delta", lookupType: 1, subtable: gsubTestSingle1(3, -1), check: func(t *testing.T, table gsubSubtable) {
			single := table.(*gsubSingle)
			if single.format != 1 || single.delta != -1 || single.coverage.glyphCount != 1 {
				t.Fatalf("single=%#v", single)
			}
		}},
		{name: "single_format_2", lookupType: 1, subtable: gsubTestSingle2([]uint16{2, 4}, []uint16{5, 6}), check: func(t *testing.T, table gsubSubtable) {
			if single := table.(*gsubSingle); !slices.Equal(single.substitutes, []uint16{5, 6}) {
				t.Fatalf("single=%#v", single)
			}
		}},
		{name: "multiple", lookupType: 2, subtable: gsubTestMultiple(3, []uint16{4, 5}), check: func(t *testing.T, table gsubSubtable) {
			if multiple := table.(*gsubMultiple); len(multiple.sequences) != 1 || !slices.Equal(multiple.sequences[0].glyphs, []uint16{4, 5}) {
				t.Fatalf("multiple=%#v", multiple)
			}
		}},
		{name: "alternate_empty", lookupType: 3, subtable: gsubTestAlternate(3, nil), check: func(t *testing.T, table gsubSubtable) {
			if alternate := table.(*gsubAlternate); len(alternate.sets) != 1 || len(alternate.sets[0].glyphs) != 0 {
				t.Fatalf("alternate=%#v", alternate)
			}
		}},
		{name: "ligature_component_one", lookupType: 4, subtable: gsubTestLigature(3, 7, nil), check: func(t *testing.T, table gsubSubtable) {
			if ligature := table.(*gsubLigatureSubst).sets[0].ligatures[0]; ligature.glyph != 7 || len(ligature.components) != 0 {
				t.Fatalf("ligature=%#v", ligature)
			}
		}},
		{name: "ligature_components", lookupType: 4, subtable: gsubTestLigature(3, 7, []uint16{4, 5}), check: func(t *testing.T, table gsubSubtable) {
			if ligature := table.(*gsubLigatureSubst).sets[0].ligatures[0]; !slices.Equal(ligature.components, []uint16{4, 5}) {
				t.Fatalf("ligature=%#v", ligature)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := gsubTestTable(0, test.lookupType, 0, gsubTestTag("liga"), nil, test.subtable)
			foundation, _, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, foundation.lookups[0].subtables[0])
			// Successful models own all variable arrays.
			for index := range raw {
				raw[index] ^= 0xff
			}
			test.check(t, foundation.lookups[0].subtables[0])
		})
	}
}

func TestDecodeGSUBSingleDeltaModuloAndFeatureAliasPolicy(t *testing.T) {
	wrapped := gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(65_534, 2))
	if foundation, _, err := decodeGSUBFoundation(context.Background(), wrapped, 65_535, nil, layoutDecodeLimits{}); err != nil || foundation == nil {
		t.Fatalf("valid modulo wrap rejected: foundation=%#v err=%v", foundation, err)
	}
	invalidWrap := gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(0, -1))
	if foundation, zero, err := decodeGSUBFoundation(context.Background(), invalidWrap, 65_535, nil, layoutDecodeLimits{}); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("result glyph 65535 accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}

	aliased := gsubTestAliasedFeatures(false)
	foundation, stats, err := decodeGSUBFoundation(context.Background(), aliased, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(foundation.features) != 2 || foundation.features[0].feature != foundation.features[1].feature || foundation.features[0].params != nil || foundation.features[1].params != nil || stats.aliasReferences == 0 {
		t.Fatalf("zero-param feature alias=%#v stats=%#v", foundation.features, stats)
	}
	featureStart := int(uint16(aliased[6])<<8 | uint16(aliased[7]))
	duplicate := append([]byte(nil), aliased...)
	copy(duplicate[featureStart+8:featureStart+12], duplicate[featureStart+2:featureStart+6])
	if foundation, _, err := decodeGSUBFoundation(context.Background(), duplicate, 16, nil, layoutDecodeLimits{}); err != nil || len(foundation.features) != 2 || foundation.features[0].tag != foundation.features[1].tag {
		t.Fatalf("duplicate feature tags rejected: foundation=%#v err=%v", foundation, err)
	}

	typePunned := gsubTestAliasedFeatures(true)
	if foundation, zero, err := decodeGSUBFoundation(context.Background(), typePunned, 16, nil, layoutDecodeLimits{}); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("cv/ss params type-pun accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestDecodeGSUBCommonTablesAndHeaderRejections(t *testing.T) {
	base := gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	lookupListStart := int(uint16(base[8])<<8 | uint16(base[9]))
	featureListStart := int(uint16(base[6])<<8 | uint16(base[7]))
	tests := []struct {
		name        string
		mutate      func([]byte)
		unsupported bool
	}{
		{name: "null_script_list", mutate: func(raw []byte) { layoutTestPut16(raw, 4, 0) }},
		{name: "dflt_without_default", mutate: func(raw []byte) { layoutTestPut16(raw, 18, 0) }},
		{name: "feature_lookup_oob", mutate: func(raw []byte) { layoutTestPut16(raw, featureListStart+12, 1) }},
		{name: "reserved_lookup_flag", mutate: func(raw []byte) { layoutTestPut16(raw, lookupListStart+6, 0x20) }},
		{name: "null_subtable", mutate: func(raw []byte) { layoutTestPut16(raw, lookupListStart+10, 0) }},
		{name: "invalid_lookup_type", mutate: func(raw []byte) { layoutTestPut16(raw, lookupListStart+4, 9) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := append([]byte(nil), base...)
			test.mutate(raw)
			foundation, zero, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
			if err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
			}
			if test.unsupported && !layoutUnsupportedVariation(err) {
				t.Fatalf("error=%v, want unsupported", err)
			}
		})
	}

	badNamedParameter := gsubTestCVParams()
	layoutTestPut16(badNamedParameter, 10, 1)
	if foundation, zero, err := decodeGSUBFoundation(context.Background(), gsubTestTable(0, 1, 0, gsubTestTag("cv01"), badNamedParameter, gsubTestSingle1(3, 1)), 16, nil, layoutDecodeLimits{}); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("zero named params with nonzero first name accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestDecodeGSUBScriptAndLangSysOrdering(t *testing.T) {
	validScripts := gsubTestTableWithScriptList(gsubTestScriptList(
		[]uint32{gsubTestTag("arab"), gsubTestTag("latn")},
	))
	if foundation, _, err := decodeGSUBFoundation(context.Background(), validScripts, 16, nil, layoutDecodeLimits{}); err != nil || foundation == nil || len(foundation.scripts) != 2 {
		t.Fatalf("sorted scripts rejected: foundation=%#v err=%v", foundation, err)
	}
	validLanguages := gsubTestTableWithScriptList(gsubTestLangSysScriptList(
		[]uint32{gsubTestTag("ENG "), gsubTestTag("TRK ")},
	))
	if foundation, _, err := decodeGSUBFoundation(context.Background(), validLanguages, 16, nil, layoutDecodeLimits{}); err != nil || foundation == nil || len(foundation.scripts) != 1 || len(foundation.scripts[0].script.languages) != 2 {
		t.Fatalf("sorted languages rejected: foundation=%#v err=%v", foundation, err)
	}

	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "script_unsorted", raw: gsubTestTableWithScriptList(gsubTestScriptList([]uint32{gsubTestTag("latn"), gsubTestTag("arab")}))},
		{name: "script_duplicate", raw: gsubTestTableWithScriptList(gsubTestScriptList([]uint32{gsubTestTag("latn"), gsubTestTag("latn")}))},
		{name: "language_unsorted", raw: gsubTestTableWithScriptList(gsubTestLangSysScriptList([]uint32{gsubTestTag("TRK "), gsubTestTag("ENG ")}))},
		{name: "language_duplicate", raw: gsubTestTableWithScriptList(gsubTestLangSysScriptList([]uint32{gsubTestTag("ENG "), gsubTestTag("ENG ")}))},
	} {
		t.Run(test.name, func(t *testing.T) {
			foundation, stats, err := decodeGSUBFoundation(context.Background(), test.raw, 16, nil, layoutDecodeLimits{})
			if err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
				t.Fatalf("invalid order published state: foundation=%#v stats=%#v err=%v", foundation, stats, err)
			}
		})
	}
}

func TestLayoutCommonClassDefUsesOneFullDomainIdentity(t *testing.T) {
	limits, err := normalizeLayoutDecodeLimits(layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	raw := layoutTestWords(1, 0, 2, 0, 5)
	meter := newLayoutResourceMeterForTable(uint64(len(raw)), 2, limits, layoutGSUBTag)
	reader := newLayoutReaderForTable(context.Background(), raw, meter, layoutGSUBTag)
	first, err := reader.parseLayoutClassDef(nil, layoutEdgeRef{}, 0, "test/class_def")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.parseLayoutClassDef(nil, layoutEdgeRef{}, 0, "test/class_def_alias")
	if err != nil || second != first || layoutClassDefMax(first) != 5 || meter.stats.aliasReferences != 1 {
		t.Fatalf("alias=%p/%p max=%d stats=%#v err=%v", first, second, layoutClassDefMax(first), meter.stats, err)
	}
	nodes, _, err := reader.graph.validateAndSnapshot("test/class_def_graph")
	if err != nil || len(nodes) != 1 || nodes[0].kind != layoutSpanClassDef || nodes[0].paramSecond != uint64(^uint16(0)) || nodes[0].tableTag != layoutGSUBTag {
		t.Fatalf("nodes=%#v err=%v", nodes, err)
	}
}

func TestDecodeGSUBSSParamsFlagsAndUnsupported(t *testing.T) {
	ss := gsubTestTable(0, 1, 0, gsubTestTag("ss20"), layoutTestWords(0, 300), gsubTestSingle1(3, 1))
	foundation, _, err := decodeGSUBFoundation(context.Background(), ss, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if params, ok := foundation.features[0].params.(*layoutSSFeatureParams); !ok || params.uiLabelNameID != 300 {
		t.Fatalf("ss params=%#v", foundation.features[0].params)
	}

	unsupported := gsubTestTable(0, 1, 0, gsubTestTag("liga"), []byte{0, 0}, gsubTestSingle1(3, 1))
	if _, _, err := decodeGSUBFoundation(context.Background(), unsupported, 16, nil, layoutDecodeLimits{}); !layoutUnsupportedVariation(err) {
		t.Fatalf("unknown feature params error=%v", err)
	}
	badSS := gsubTestTable(0, 1, 0, gsubTestTag("ss01"), layoutTestWords(1, 300), gsubTestSingle1(3, 1))
	if _, _, err := decodeGSUBFoundation(context.Background(), badSS, 16, nil, layoutDecodeLimits{}); !layoutUnsupportedVariation(err) {
		t.Fatalf("ss version error=%v", err)
	}

	flags := uint16(0x0002 | 0x0100 | 0x0010)
	withFlags := gsubTestTable(0, 1, flags, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	if _, _, err := decodeGSUBFoundation(context.Background(), withFlags, 16, nil, layoutDecodeLimits{}); err == nil {
		t.Fatal("GDEF-dependent flags accepted without GDEF")
	}
	gdef := &gdefFoundation{glyphClasses: &gdefClassDef{}, markAttachClasses: &gdefClassDef{}, markGlyphSets: []*gdefCoverage{{}}}
	foundation, _, err = decodeGSUBFoundation(context.Background(), withFlags, 16, gdef, layoutDecodeLimits{})
	if err != nil || foundation.lookups[0].markFilteringSet == nil || *foundation.lookups[0].markFilteringSet != 0 {
		t.Fatalf("flags foundation=%#v err=%v", foundation, err)
	}
}

func TestDecodeGSUBLookupFlagBindings(t *testing.T) {
	for _, flag := range []uint16{0x0002, 0x0004, 0x0008} {
		raw := gsubTestTable(0, 1, flag, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
		if foundation, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{}); err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
			t.Fatalf("glyph-class flag %#x accepted without GDEF: foundation=%#v stats=%#v err=%v", flag, foundation, stats, err)
		}
		gdef := &gdefFoundation{glyphClasses: &gdefClassDef{}}
		if foundation, _, err := decodeGSUBFoundation(context.Background(), raw, 16, gdef, layoutDecodeLimits{}); err != nil || foundation == nil {
			t.Fatalf("glyph-class flag %#x rejected with GDEF: foundation=%#v err=%v", flag, foundation, err)
		}
	}

	markAttach := gsubTestTable(0, 1, 0x0100, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	if foundation, stats, err := decodeGSUBFoundation(context.Background(), markAttach, 16, nil, layoutDecodeLimits{}); err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
		t.Fatalf("mark attachment class accepted without GDEF: foundation=%#v stats=%#v err=%v", foundation, stats, err)
	}
	if foundation, _, err := decodeGSUBFoundation(context.Background(), markAttach, 16, &gdefFoundation{markAttachClasses: &gdefClassDef{}}, layoutDecodeLimits{}); err != nil || foundation == nil {
		t.Fatalf("mark attachment class rejected with GDEF: foundation=%#v err=%v", foundation, err)
	}

	filtering := gsubTestTable(0, 1, 0x0010, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	if foundation, stats, err := decodeGSUBFoundation(context.Background(), filtering, 16, nil, layoutDecodeLimits{}); err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
		t.Fatalf("mark filtering set accepted without GDEF: foundation=%#v stats=%#v err=%v", foundation, stats, err)
	}
	filterGDEF := &gdefFoundation{markGlyphSets: []*gdefCoverage{{}}}
	if foundation, _, err := decodeGSUBFoundation(context.Background(), filtering, 16, filterGDEF, layoutDecodeLimits{}); err != nil || foundation == nil || foundation.lookups[0].markFilteringSet == nil || *foundation.lookups[0].markFilteringSet != 0 {
		t.Fatalf("mark filtering set rejected with GDEF: foundation=%#v err=%v", foundation, err)
	}
	outOfRange := append([]byte(nil), filtering...)
	lookupListStart := int(uint16(outOfRange[8])<<8 | uint16(outOfRange[9]))
	layoutTestPut16(outOfRange, lookupListStart+12, 1)
	if foundation, stats, err := decodeGSUBFoundation(context.Background(), outOfRange, 16, filterGDEF, layoutDecodeLimits{}); err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
		t.Fatalf("out-of-range mark filtering set accepted: foundation=%#v stats=%#v err=%v", foundation, stats, err)
	}

	rightToLeft := gsubTestTable(0, 1, 0x0001, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	if foundation, _, err := decodeGSUBFoundation(context.Background(), rightToLeft, 16, nil, layoutDecodeLimits{}); err != nil || foundation == nil {
		t.Fatalf("GSUB right-to-left qualifier should be retained and ignored: foundation=%#v err=%v", foundation, err)
	}
}

func TestDecodeGSUBRejectsMalformedSimpleTables(t *testing.T) {
	tests := []struct {
		name       string
		lookupType uint16
		subtable   []byte
	}{
		{name: "single_result_out_of_range", lookupType: 1, subtable: gsubTestSingle1(15, 1)},
		{name: "single_cardinality", lookupType: 1, subtable: gsubTestSingle2([]uint16{2}, []uint16{5, 6})},
		{name: "multiple_deletion", lookupType: 2, subtable: gsubTestMultiple(3, nil)},
		{name: "alternate_output_out_of_range", lookupType: 3, subtable: gsubTestAlternate(3, []uint16{16})},
		{name: "ligature_output_out_of_range", lookupType: 4, subtable: gsubTestLigature(3, 16, nil)},
		{name: "ligature_component_out_of_range", lookupType: 4, subtable: gsubTestLigature(3, 7, []uint16{16})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := gsubTestTable(0, test.lookupType, 0, gsubTestTag("liga"), nil, test.subtable)
			foundation, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
			if err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
				t.Fatalf("accepted: foundation=%#v stats=%#v err=%v", foundation, stats, err)
			}
		})
	}

	badCV := gsubTestCVParams()
	badCV[len(badCV)-3], badCV[len(badCV)-2], badCV[len(badCV)-1] = 0x11, 0, 0
	if foundation, stats, err := decodeGSUBFoundation(context.Background(), gsubTestTable(0, 1, 0, gsubTestTag("cv99"), badCV, gsubTestSingle1(3, 1)), 16, nil, layoutDecodeLimits{}); err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
		t.Fatalf("invalid Unicode scalar accepted: %#v %#v %v", foundation, stats, err)
	}
}

func TestDecodeGSUBResourceLimitsExact(t *testing.T) {
	raw := gsubTestTable(0, 2, 0, gsubTestTag("liga"), nil, gsubTestMultiple(3, []uint16{4, 5, 6}))
	_, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		value uint64
		set   func(*layoutDecodeLimits, uint64)
	}{
		{name: "table_bytes", value: uint64(len(raw)), set: func(l *layoutDecodeLimits, n uint64) { l.maxTableBytes = n }},
		{name: "substitutes", value: stats.substituteGlyphReferences, set: func(l *layoutDecodeLimits, n uint64) { l.maxSubstituteGlyphs = n }},
		{name: "ordered", value: stats.orderedRecords, set: func(l *layoutDecodeLimits, n uint64) { l.maxOrderedRecords = n }},
		{name: "nodes", value: stats.nodes, set: func(l *layoutDecodeLimits, n uint64) { l.maxNodes = n }},
		{name: "edges", value: stats.edges, set: func(l *layoutDecodeLimits, n uint64) { l.maxEdges = n }},
		{name: "work", value: stats.workUnits, set: func(l *layoutDecodeLimits, n uint64) { l.maxWorkUnits = n }},
		{name: "working", value: stats.workingBytes, set: func(l *layoutDecodeLimits, n uint64) { l.maxWorkingBytes = n }},
		{name: "retained", value: stats.retainedBytes, set: func(l *layoutDecodeLimits, n uint64) { l.maxRetainedBytes = n }},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := layoutDecodeLimits{}
			test.set(&limits, test.value)
			if _, _, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, limits); err != nil {
				t.Fatalf("exact N rejected: %v", err)
			}
			if test.value > 1 {
				limits = layoutDecodeLimits{}
				test.set(&limits, test.value-1)
				if foundation, zero, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, limits); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
					t.Fatalf("N-1 accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
				}
			}
		})
	}

	t.Run("structural_depth", func(t *testing.T) {
		if stats.maxStructuralDepth < 2 {
			t.Fatalf("unexpected structural depth %d", stats.maxStructuralDepth)
		}
		limits := layoutDecodeLimits{maxStructuralDepth: stats.maxStructuralDepth}
		if _, _, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, limits); err != nil {
			t.Fatalf("exact N rejected: %v", err)
		}
		limits.maxStructuralDepth--
		if foundation, zero, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, limits); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
			t.Fatalf("N-1 accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
		}
	})
}

func TestDecodeGSUBAdditionalResourceLimitsExact(t *testing.T) {
	for _, test := range []struct {
		name  string
		raw   []byte
		value func(layoutDecodeStats) uint64
		set   func(*layoutDecodeLimits, uint64)
	}{
		{
			name:  "feature_characters",
			raw:   gsubTestTable(0, 1, 0, gsubTestTag("cv01"), gsubTestCVParams(), gsubTestSingle1(3, 1)),
			value: func(stats layoutDecodeStats) uint64 { return stats.featureCharacters },
			set:   func(limits *layoutDecodeLimits, value uint64) { limits.maxFeatureCharacters = value },
		},
		{
			name:  "ligature_components",
			raw:   gsubTestTable(0, 4, 0, gsubTestTag("liga"), nil, gsubTestLigature(3, 7, []uint16{4, 5})),
			value: func(stats layoutDecodeStats) uint64 { return stats.ligatureComponents },
			set:   func(limits *layoutDecodeLimits, value uint64) { limits.maxLigatureComponents = value },
		},
		{
			name:  "aliases",
			raw:   gsubTestRepeatedSubtableAliases(3),
			value: func(stats layoutDecodeStats) uint64 { return stats.aliasReferences },
			set:   func(limits *layoutDecodeLimits, value uint64) { limits.maxAliasReferences = value },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, stats, err := decodeGSUBFoundation(context.Background(), test.raw, 16, nil, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			value := test.value(stats)
			if value < 2 {
				t.Fatalf("test requires an N-1-representable value, got %d", value)
			}
			limits := layoutDecodeLimits{}
			test.set(&limits, value)
			if _, _, err := decodeGSUBFoundation(context.Background(), test.raw, 16, nil, limits); err != nil {
				t.Fatalf("exact N rejected: %v", err)
			}
			limits = layoutDecodeLimits{}
			test.set(&limits, value-1)
			if foundation, zero, err := decodeGSUBFoundation(context.Background(), test.raw, 16, nil, limits); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("N-1 accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
			}
		})
	}
}

func TestDecodeGSUBCancellationAfterPartialDecode(t *testing.T) {
	raw := gsubTestTable(0, 4, 0, gsubTestTag("liga"), nil, gsubTestLigature(3, 7, []uint16{4, 5}))
	probe := &geometryCountingContext{}
	if _, _, err := decodeGSUBFoundation(probe, raw, 16, nil, layoutDecodeLimits{}); err != nil {
		t.Fatal(err)
	}
	if probe.calls < 8 {
		t.Fatalf("too few cancellation checkpoints: %d", probe.calls)
	}
	ctx := &geometryCountingContext{cancelAt: probe.calls / 2}
	if foundation, zero, err := decodeGSUBFoundation(ctx, raw, 16, nil, layoutDecodeLimits{}); !errors.Is(err, ErrCanceled) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("partial cancellation published: foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func gsubTestTable(minor, lookupType, flags uint16, featureTag uint32, params, subtable []byte) []byte {
	headerSize := 10
	if minor == 1 {
		headerSize = 14
	}
	scriptList := layoutTestWords(1)
	scriptList = append(scriptList, byte(layoutDFLTTag>>24), byte((layoutDFLTTag>>16)&0xff), byte((layoutDFLTTag>>8)&0xff), byte(layoutDFLTTag&0xff))
	scriptList = append(scriptList, 0, 8)
	scriptList = append(scriptList, layoutTestWords(4, 0, 0, 0xffff, 1, 0)...)

	featureList := layoutTestWords(1)
	featureList = append(featureList, byte(featureTag>>24), byte(featureTag>>16), byte(featureTag>>8), byte(featureTag))
	featureList = append(featureList, 0, 8)
	paramsOffset := uint16(0)
	if params != nil {
		paramsOffset = 6
	}
	featureList = append(featureList, layoutTestWords(paramsOffset, 1, 0)...)
	featureList = append(featureList, params...)

	lookupDirect := uint16(8)
	if flags&0x0010 != 0 {
		lookupDirect = 10
	}
	lookupList := layoutTestWords(1, 4, lookupType, flags, 1, lookupDirect)
	if flags&0x0010 != 0 {
		lookupList = append(lookupList, 0, 0)
	}
	lookupList = append(lookupList, subtable...)

	result := make([]byte, headerSize)
	layoutTestPut16(result, 0, 1)
	layoutTestPut16(result, 2, minor)
	scriptStart := len(result)
	result = append(result, scriptList...)
	featureStart := len(result)
	result = append(result, featureList...)
	lookupStart := len(result)
	result = append(result, lookupList...)
	layoutTestPut16(result, 4, uint16(scriptStart))
	layoutTestPut16(result, 6, uint16(featureStart))
	layoutTestPut16(result, 8, uint16(lookupStart))
	return result
}

func gsubTestTableWithScriptList(scriptList []byte) []byte {
	base := gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
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

func gsubTestScriptList(tags []uint32) []byte {
	const scriptBytes = 12
	result := layoutTestWords(uint16(len(tags)))
	nextOffset := 2 + 6*len(tags)
	for _, tag := range tags {
		result = append(result, byte(tag>>24), byte(tag>>16), byte(tag>>8), byte(tag))
		result = append(result, byte(nextOffset>>8), byte(nextOffset))
		nextOffset += scriptBytes
	}
	for range tags {
		result = append(result, layoutTestWords(4, 0, 0, 0xffff, 1, 0)...)
	}
	return result
}

func gsubTestLangSysScriptList(tags []uint32) []byte {
	const langSysBytes = 8
	script := layoutTestWords(0, uint16(len(tags)))
	nextOffset := 4 + 6*len(tags)
	for _, tag := range tags {
		script = append(script, byte(tag>>24), byte(tag>>16), byte(tag>>8), byte(tag))
		script = append(script, byte(nextOffset>>8), byte(nextOffset))
		nextOffset += langSysBytes
	}
	for range tags {
		script = append(script, layoutTestWords(0, 0xffff, 1, 0)...)
	}
	result := layoutTestWords(1)
	tag := gsubTestTag("latn")
	result = append(result, byte(tag>>24), byte(tag>>16), byte(tag>>8), byte(tag), 0, 8)
	return append(result, script...)
}

func gsubTestRepeatedSubtableAliases(count uint16) []byte {
	if count == 0 {
		panic("alias test requires at least one subtable")
	}
	base := gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	lookupStart := int(uint16(base[8])<<8 | uint16(base[9]))
	result := append([]byte(nil), base[:lookupStart]...)
	lookupWords := []uint16{1, 4, 1, 0, count}
	subtableOffset := uint16(6 + 2*uint32(count))
	for index := uint16(0); index < count; index++ {
		lookupWords = append(lookupWords, subtableOffset)
	}
	result = append(result, layoutTestWords(lookupWords...)...)
	return append(result, gsubTestSingle1(3, 1)...)
}

func gsubTestAliasedFeatures(withParams bool) []byte {
	headerSize := 10
	scriptList := layoutTestWords(1)
	scriptList = append(scriptList, byte(layoutDFLTTag>>24), byte((layoutDFLTTag>>16)&0xff), byte((layoutDFLTTag>>8)&0xff), byte(layoutDFLTTag&0xff), 0, 8)
	scriptList = append(scriptList, layoutTestWords(4, 0, 0, 0xffff, 2, 0, 1)...)

	featureOffset := uint16(14)
	featureList := layoutTestWords(2)
	tags := []uint32{gsubTestTag("ss01"), gsubTestTag("cv01")}
	if withParams {
		tags = []uint32{gsubTestTag("cv01"), gsubTestTag("ss01")}
	}
	for _, tag := range tags {
		featureList = append(featureList, byte(tag>>24), byte((tag>>16)&0xff), byte((tag>>8)&0xff), byte(tag&0xff), byte(featureOffset>>8), byte(featureOffset))
	}
	paramsOffset := uint16(0)
	if withParams {
		paramsOffset = 6
	}
	featureList = append(featureList, layoutTestWords(paramsOffset, 1, 0)...)
	if withParams {
		featureList = append(featureList, gsubTestCVParams()...)
	}

	subtable := gsubTestSingle1(3, 1)
	lookupList := layoutTestWords(1, 4, 1, 0, 1, 8)
	lookupList = append(lookupList, subtable...)
	result := make([]byte, headerSize)
	layoutTestPut16(result, 0, 1)
	scriptStart := len(result)
	result = append(result, scriptList...)
	featureStart := len(result)
	result = append(result, featureList...)
	lookupStart := len(result)
	result = append(result, lookupList...)
	layoutTestPut16(result, 4, uint16(scriptStart))
	layoutTestPut16(result, 6, uint16(featureStart))
	layoutTestPut16(result, 8, uint16(lookupStart))
	return result
}

func gsubTestSingle1(input uint16, delta int16) []byte {
	return layoutTestWords(1, 6, uint16(delta), 1, 1, input)
}

func gsubTestSingle2(inputs, outputs []uint16) []byte {
	result := layoutTestWords(2, uint16(6+2*len(outputs)), uint16(len(outputs)))
	result = append(result, layoutTestWords(outputs...)...)
	result = append(result, layoutTestWords(1, uint16(len(inputs)))...)
	result = append(result, layoutTestWords(inputs...)...)
	return result
}

func gsubTestMultiple(input uint16, outputs []uint16) []byte {
	result := layoutTestWords(1, 8, 1, 14)
	result = append(result, layoutTestWords(1, 1, input)...)
	result = append(result, layoutTestWords(uint16(len(outputs)))...)
	result = append(result, layoutTestWords(outputs...)...)
	return result
}

func gsubTestAlternate(input uint16, outputs []uint16) []byte {
	result := layoutTestWords(1, 8, 1, 14)
	result = append(result, layoutTestWords(1, 1, input)...)
	result = append(result, layoutTestWords(uint16(len(outputs)))...)
	result = append(result, layoutTestWords(outputs...)...)
	return result
}

func gsubTestLigature(input, output uint16, components []uint16) []byte {
	result := layoutTestWords(1, 8, 1, 14)
	result = append(result, layoutTestWords(1, 1, input)...)
	result = append(result, layoutTestWords(1, 4)...)
	result = append(result, layoutTestWords(output, uint16(len(components)+1))...)
	result = append(result, layoutTestWords(components...)...)
	return result
}

func gsubTestCVParams() []byte {
	result := layoutTestWords(0, 1, 2, 3, 0, 0, 2)
	result = append(result, 0, 0, 0x41, 1, 0xf6, 0)
	return result
}

func gsubTestTag(value string) uint32 {
	if len(value) != 4 {
		panic("test tag must have four bytes")
	}
	return uint32(value[0])<<24 | uint32(value[1])<<16 | uint32(value[2])<<8 | uint32(value[3])
}
