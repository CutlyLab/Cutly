package sfntglyf

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type gsubAdvancedLookupSpec struct {
	lookupType uint16
	subtables  [][]byte
}

func TestDecodeGSUBContextualFormatsAndLogicalDAG(t *testing.T) {
	tests := []struct {
		name       string
		lookupType uint16
		subtable   []byte
		format     uint16
	}{
		{name: "context_format_1", lookupType: 5, subtable: gsubTestContext1(3, []uint16{4}, []gsubSequenceLookupRecord{{sequenceIndex: 0, lookupIndex: 1}}), format: 1},
		{name: "context_format_2", lookupType: 5, subtable: gsubTestContext2(3, []uint16{0}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}}), format: 2},
		{name: "context_format_3", lookupType: 5, subtable: gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}}), format: 3},
		{name: "chain_format_1", lookupType: 6, subtable: gsubTestChain1(3, []uint16{2}, []uint16{4}, []uint16{5}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}}), format: 1},
		{name: "chain_format_2", lookupType: 6, subtable: gsubTestChain2(3, []uint16{1}, []uint16{0}, []uint16{1}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}}), format: 2},
		{name: "chain_format_3", lookupType: 6, subtable: gsubTestChain3([]uint16{2}, []uint16{3, 4}, []uint16{5}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}}), format: 3},
		{name: "extension_context", lookupType: 7, subtable: gsubTestExtension(5, gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{sequenceIndex: 0, lookupIndex: 1}})), format: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
				{lookupType: test.lookupType, subtables: [][]byte{test.subtable}},
				{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
			})
			foundation, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if len(foundation.logical.edges) != 1 || foundation.logical.edges[0].fromLookup != 0 || foundation.logical.edges[0].toLookup != 1 ||
				!slices.Equal(foundation.logical.topological, []uint16{0, 1}) || foundation.logical.maxDepth != 1 || foundation.logical.maxExpansion != 1 {
				t.Fatalf("logical=%#v", foundation.logical)
			}
			if stats.sequenceLookupRecords != 1 || stats.logicalLookupEdges != 1 || stats.logicalLookupDepth != 1 || stats.logicalExpansion != 1 {
				t.Fatalf("stats=%#v", stats)
			}
			subtable := foundation.lookups[0].subtables[0]
			if extension, ok := subtable.(*gsubExtension); ok {
				if extension.extensionLookupType != 5 {
					t.Fatalf("extension=%#v", extension)
				}
				subtable = extension.subtable
			}
			switch typed := subtable.(type) {
			case *gsubContext:
				if typed.format != test.format {
					t.Fatalf("context format=%d", typed.format)
				}
			case *gsubChainedContext:
				if typed.format != test.format {
					t.Fatalf("chain format=%d", typed.format)
				}
			default:
				t.Fatalf("subtable=%T", subtable)
			}
			// Every variable value retained by the successful model is independent
			// of the caller's encoded byte ownership.
			for index := range raw {
				raw[index] ^= 0xff
			}
			if foundation.logical.edges[0].toLookup != 1 {
				t.Fatal("logical model aliased input bytes")
			}
		})
	}
}

func TestDecodeGSUBReverseChainingAndExtensionConsistency(t *testing.T) {
	reverse := gsubTestReverse(3, []uint16{2}, []uint16{4}, 9)
	raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 8, subtables: [][]byte{reverse}}})
	foundation, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	table := foundation.lookups[0].subtables[0].(*gsubReverseChain)
	if !slices.Equal(table.substitutes, []uint16{9}) || len(table.backtrackCoverages) != 1 || len(table.lookaheadCoverages) != 1 || stats.reverseSubtables != 1 {
		t.Fatalf("reverse=%#v stats=%#v", table, stats)
	}
	if len(foundation.logical.edges) != 0 || len(foundation.logical.topological) != 0 || stats.logicalLookupEdges != 0 {
		t.Fatalf("unexpected logical graph=%#v stats=%#v", foundation.logical, stats)
	}

	mixed := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 7, subtables: [][]byte{
		gsubTestExtension(1, gsubTestSingle1(3, 1)),
		gsubTestExtension(8, reverse),
	}}})
	if result, zero, err := decodeGSUBFoundation(context.Background(), mixed, 16, nil, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("mixed extension types accepted: result=%#v stats=%#v err=%v", result, zero, err)
	}
}

func TestDecodeGSUBAdvancedRejectsDomainsOffsetsAndCycles(t *testing.T) {
	aliasedContext := gsubTestContext3([]uint16{3, 4}, nil)
	layoutTestPut16(aliasedContext, 8, uint16(aliasedContext[6])<<8|uint16(aliasedContext[7]))
	aliasedRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{aliasedContext}}})
	aliasedFoundation, aliasedStats, err := decodeGSUBFoundation(context.Background(), aliasedRaw, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	aliased := aliasedFoundation.lookups[0].subtables[0].(*gsubContext)
	if len(aliased.coverages) != 2 || aliased.coverages[0] != aliased.coverages[1] || aliasedStats.aliasReferences != 1 {
		t.Fatalf("exact coverage alias not retained: table=%#v stats=%#v", aliased, aliasedStats)
	}
	aliasedClasses := gsubTestChain2(3, nil, nil, nil, nil)
	classOffset := uint16(aliasedClasses[4])<<8 | uint16(aliasedClasses[5])
	layoutTestPut16(aliasedClasses, 6, classOffset)
	layoutTestPut16(aliasedClasses, 8, classOffset)
	classAliasRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 6, subtables: [][]byte{aliasedClasses}}})
	classAliasFoundation, classAliasStats, err := decodeGSUBFoundation(context.Background(), classAliasRaw, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	classAlias := classAliasFoundation.lookups[0].subtables[0].(*gsubChainedContext)
	if classAlias.backtrackClassDef != classAlias.inputClassDef || classAlias.inputClassDef != classAlias.lookaheadClassDef || classAliasStats.aliasReferences != 2 {
		t.Fatalf("role-shared ClassDef alias not retained: table=%#v stats=%#v", classAlias, classAliasStats)
	}

	partialOverlap := gsubTestContext3([]uint16{3, 4}, nil)
	firstCoverage := uint16(partialOverlap[6])<<8 | uint16(partialOverlap[7])
	layoutTestPut16(partialOverlap, 8, firstCoverage+2)
	badExtensionOffset := gsubTestExtension(1, gsubTestSingle1(3, 1))
	layoutTestPut32(badExtensionOffset, 4, ^uint32(0))
	classSetMismatch := gsubTestContext2(3, nil, nil)
	layoutTestPut16(classSetMismatch, 6, 1)
	reverseCardinality := gsubTestReverse(3, nil, nil, 4)
	layoutTestPut16(reverseCardinality, 8, 2)
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "context_sequence_index",
			raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
				{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})}},
				{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
			}),
		},
		{
			name: "context_lookup_index",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}}}),
		},
		{
			name: "context_glyph_domain",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{gsubTestContext1(3, []uint16{16}, nil)}}}),
		},
		{
			name: "context_class_domain",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{gsubTestContext2(3, []uint16{2}, nil)}}}),
		},
		{
			name: "chain_backtrack_glyph_domain",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 6, subtables: [][]byte{gsubTestChain1(3, []uint16{16}, nil, nil, nil)}}}),
		},
		{
			name: "chain_lookahead_class_domain",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 6, subtables: [][]byte{gsubTestChain2(3, nil, nil, []uint16{2}, nil)}}}),
		},
		{
			name: "extension_of_extension",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 7, subtables: [][]byte{gsubTestExtension(7, gsubTestExtension(1, gsubTestSingle1(3, 1)))}}}),
		},
		{
			name: "coverage_partial_overlap",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{partialOverlap}}}),
		},
		{
			name: "context_class_set_cardinality",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{classSetMismatch}}}),
		},
		{
			name: "extension_offset32_oob",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 7, subtables: [][]byte{badExtensionOffset}}}),
		},
		{
			name: "reverse_substitute_domain",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 8, subtables: [][]byte{gsubTestReverse(3, nil, nil, 16)}}}),
		},
		{
			name: "reverse_coverage_cardinality",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 8, subtables: [][]byte{reverseCardinality}}}),
		},
		{
			name: "logical_self_cycle",
			raw:  gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 0}})}}}),
		},
		{
			name: "logical_two_lookup_cycle",
			raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
				{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
				{lookupType: 6, subtables: [][]byte{gsubTestChain3(nil, []uint16{4}, nil, []gsubSequenceLookupRecord{{lookupIndex: 0}})}},
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			foundation, stats, err := decodeGSUBFoundation(context.Background(), test.raw, 16, nil, layoutDecodeLimits{})
			if err == nil || foundation != nil || stats != (layoutDecodeStats{}) {
				t.Fatalf("invalid advanced table published: foundation=%#v stats=%#v err=%v", foundation, stats, err)
			}
		})
	}
}

func TestDecodeGSUBLogicalResourcesExactAndCancellation(t *testing.T) {
	records := []gsubSequenceLookupRecord{{sequenceIndex: 0, lookupIndex: 2}, {sequenceIndex: 0, lookupIndex: 2}}
	raw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3, 4, 5}, records)}},
		{lookupType: 6, subtables: [][]byte{gsubTestChain3(nil, []uint16{6}, nil, []gsubSequenceLookupRecord{{lookupIndex: 2}})}},
		{lookupType: 1, subtables: [][]byte{gsubTestSingle1(7, 1)}},
	})
	_, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		value uint64
		set   func(*layoutDecodeLimits, uint64)
	}{
		{name: "context_glyphs", value: stats.contextGlyphReferences, set: func(l *layoutDecodeLimits, n uint64) { l.maxContextGlyphs = n }},
		{name: "sequence_lookups", value: stats.sequenceLookupRecords, set: func(l *layoutDecodeLimits, n uint64) { l.maxSequenceLookups = n }},
		{name: "logical_edges", value: stats.logicalLookupEdges, set: func(l *layoutDecodeLimits, n uint64) { l.maxLogicalLookupEdges = n }},
		{name: "logical_expansion", value: stats.logicalExpansion, set: func(l *layoutDecodeLimits, n uint64) { l.maxLogicalExpansion = n }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.value < 2 {
				t.Fatalf("fixture value=%d", test.value)
			}
			limits := layoutDecodeLimits{}
			test.set(&limits, test.value)
			if _, _, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, limits); err != nil {
				t.Fatalf("N rejected: %v", err)
			}
			limits = layoutDecodeLimits{}
			test.set(&limits, test.value-1)
			if foundation, zero, err := decodeGSUBFoundation(context.Background(), raw, 16, nil, limits); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("N-1 accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
			}
		})
	}

	depthRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 6, subtables: [][]byte{gsubTestChain3(nil, []uint16{4}, nil, []gsubSequenceLookupRecord{{lookupIndex: 2}})}},
		{lookupType: 1, subtables: [][]byte{gsubTestSingle1(5, 1)}},
	})
	_, depthStats, err := decodeGSUBFoundation(context.Background(), depthRaw, 16, nil, layoutDecodeLimits{})
	if err != nil || depthStats.logicalLookupDepth != 2 {
		t.Fatalf("depth stats=%#v err=%v", depthStats, err)
	}
	if _, _, err := decodeGSUBFoundation(context.Background(), depthRaw, 16, nil, layoutDecodeLimits{maxLogicalLookupDepth: 2}); err != nil {
		t.Fatalf("depth N rejected: %v", err)
	}
	if foundation, zero, err := decodeGSUBFoundation(context.Background(), depthRaw, 16, nil, layoutDecodeLimits{maxLogicalLookupDepth: 1}); err == nil || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("depth N-1 accepted: foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}

	probe := &geometryCountingContext{}
	if _, _, err := decodeGSUBFoundation(probe, raw, 16, nil, layoutDecodeLimits{}); err != nil {
		t.Fatal(err)
	}
	ctx := &geometryCountingContext{cancelAt: probe.calls - 1}
	if foundation, zero, err := decodeGSUBFoundation(ctx, raw, 16, nil, layoutDecodeLimits{}); !errors.Is(err, ErrCanceled) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("late cancellation published: foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func gsubTestAdvancedTable(specs []gsubAdvancedLookupSpec) []byte {
	if len(specs) == 0 || len(specs) > int(^uint16(0)) {
		panic("advanced GSUB test requires representable lookups")
	}
	scriptList := layoutTestWords(1)
	scriptList = append(scriptList, byte(layoutDFLTTag>>24), byte((layoutDFLTTag>>16)&0xff), byte((layoutDFLTTag>>8)&0xff), byte(layoutDFLTTag&0xff), 0, 8)
	scriptList = append(scriptList, layoutTestWords(4, 0, 0, 0xffff, 1, 0)...)

	featureTag := gsubTestTag("test")
	featureList := layoutTestWords(1)
	featureList = append(featureList, byte(featureTag>>24), byte((featureTag>>16)&0xff), byte((featureTag>>8)&0xff), byte(featureTag&0xff), 0, 8)
	featureList = append(featureList, layoutTestWords(0, uint16(len(specs)))...)
	for index := range specs {
		featureList = append(featureList, layoutTestWords(uint16(index))...)
	}

	lookupList := layoutTestWords(uint16(len(specs)))
	lookupOffset := 2 + 2*len(specs)
	lookupTables := make([][]byte, len(specs))
	for index, spec := range specs {
		if len(spec.subtables) == 0 || len(spec.subtables) > int(^uint16(0)) {
			panic("advanced GSUB test requires subtables")
		}
		lookup := layoutTestWords(spec.lookupType, 0, uint16(len(spec.subtables)))
		subtableOffset := 6 + 2*len(spec.subtables)
		for _, subtable := range spec.subtables {
			lookup = append(lookup, layoutTestWords(uint16(subtableOffset))...)
			subtableOffset += len(subtable)
		}
		for _, subtable := range spec.subtables {
			lookup = append(lookup, subtable...)
		}
		lookupTables[index] = lookup
		lookupList = append(lookupList, layoutTestWords(uint16(lookupOffset))...)
		lookupOffset += len(lookup)
	}
	for _, lookup := range lookupTables {
		lookupList = append(lookupList, lookup...)
	}

	result := make([]byte, 10)
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

func gsubTestCoverage(glyphs ...uint16) []byte {
	result := layoutTestWords(1, uint16(len(glyphs)))
	return append(result, layoutTestWords(glyphs...)...)
}

func gsubTestRecords(records []gsubSequenceLookupRecord) []byte {
	result := make([]byte, 0, 4*len(records))
	for _, record := range records {
		result = append(result, layoutTestWords(record.sequenceIndex, record.lookupIndex)...)
	}
	return result
}

func gsubTestContext1(first uint16, inputs []uint16, records []gsubSequenceLookupRecord) []byte {
	coverage := gsubTestCoverage(first)
	rule := layoutTestWords(uint16(len(inputs)+1), uint16(len(records)))
	rule = append(rule, layoutTestWords(inputs...)...)
	rule = append(rule, gsubTestRecords(records)...)
	ruleSet := layoutTestWords(1, 4)
	ruleSet = append(ruleSet, rule...)
	headerBytes := 8
	result := layoutTestWords(1, uint16(headerBytes), 1, uint16(headerBytes+len(coverage)))
	result = append(result, coverage...)
	return append(result, ruleSet...)
}

func gsubTestContext2(first uint16, inputClasses []uint16, records []gsubSequenceLookupRecord) []byte {
	coverage := gsubTestCoverage(first)
	classDef := layoutTestWords(1, first, 1, 1)
	rule := layoutTestWords(uint16(len(inputClasses)+1), uint16(len(records)))
	rule = append(rule, layoutTestWords(inputClasses...)...)
	rule = append(rule, gsubTestRecords(records)...)
	ruleSet := layoutTestWords(1, 4)
	ruleSet = append(ruleSet, rule...)
	headerBytes := 12
	coverageOffset := headerBytes
	classDefOffset := coverageOffset + len(coverage)
	ruleSetOffset := classDefOffset + len(classDef)
	result := layoutTestWords(2, uint16(coverageOffset), uint16(classDefOffset), 2, 0, uint16(ruleSetOffset))
	result = append(result, coverage...)
	result = append(result, classDef...)
	return append(result, ruleSet...)
}

func gsubTestContext3(inputs []uint16, records []gsubSequenceLookupRecord) []byte {
	headerBytes := 6 + 2*len(inputs) + 4*len(records)
	result := layoutTestWords(3, uint16(len(inputs)), uint16(len(records)))
	offset := headerBytes
	for range inputs {
		result = append(result, layoutTestWords(uint16(offset))...)
		offset += 6
	}
	result = append(result, gsubTestRecords(records)...)
	for _, glyph := range inputs {
		result = append(result, gsubTestCoverage(glyph)...)
	}
	return result
}

func gsubTestChainRule(backtrack, inputs, lookahead []uint16, records []gsubSequenceLookupRecord) []byte {
	result := layoutTestWords(uint16(len(backtrack)))
	result = append(result, layoutTestWords(backtrack...)...)
	result = append(result, layoutTestWords(uint16(len(inputs)+1))...)
	result = append(result, layoutTestWords(inputs...)...)
	result = append(result, layoutTestWords(uint16(len(lookahead)))...)
	result = append(result, layoutTestWords(lookahead...)...)
	result = append(result, layoutTestWords(uint16(len(records)))...)
	return append(result, gsubTestRecords(records)...)
}

func gsubTestChain1(first uint16, backtrack, inputs, lookahead []uint16, records []gsubSequenceLookupRecord) []byte {
	coverage := gsubTestCoverage(first)
	ruleSet := layoutTestWords(1, 4)
	ruleSet = append(ruleSet, gsubTestChainRule(backtrack, inputs, lookahead, records)...)
	headerBytes := 8
	result := layoutTestWords(1, uint16(headerBytes), 1, uint16(headerBytes+len(coverage)))
	result = append(result, coverage...)
	return append(result, ruleSet...)
}

func gsubTestChain2(first uint16, backtrackClasses, inputClasses, lookaheadClasses []uint16, records []gsubSequenceLookupRecord) []byte {
	coverage := gsubTestCoverage(first)
	backtrackClassDef := layoutTestWords(1, 2, 1, 1)
	inputClassDef := layoutTestWords(1, first, 1, 1)
	lookaheadClassDef := layoutTestWords(1, 5, 1, 1)
	ruleSet := layoutTestWords(1, 4)
	ruleSet = append(ruleSet, gsubTestChainRule(backtrackClasses, inputClasses, lookaheadClasses, records)...)
	headerBytes := 16
	coverageOffset := headerBytes
	backtrackOffset := coverageOffset + len(coverage)
	inputOffset := backtrackOffset + len(backtrackClassDef)
	lookaheadOffset := inputOffset + len(inputClassDef)
	ruleSetOffset := lookaheadOffset + len(lookaheadClassDef)
	result := layoutTestWords(2, uint16(coverageOffset), uint16(backtrackOffset), uint16(inputOffset), uint16(lookaheadOffset), 2, 0, uint16(ruleSetOffset))
	result = append(result, coverage...)
	result = append(result, backtrackClassDef...)
	result = append(result, inputClassDef...)
	result = append(result, lookaheadClassDef...)
	return append(result, ruleSet...)
}

func gsubTestChain3(backtrack, inputs, lookahead []uint16, records []gsubSequenceLookupRecord) []byte {
	headerBytes := 2 + 2 + 2*len(backtrack) + 2 + 2*len(inputs) + 2 + 2*len(lookahead) + 2 + 4*len(records)
	offset := headerBytes
	result := layoutTestWords(3, uint16(len(backtrack)))
	for range backtrack {
		result = append(result, layoutTestWords(uint16(offset))...)
		offset += 6
	}
	result = append(result, layoutTestWords(uint16(len(inputs)))...)
	for range inputs {
		result = append(result, layoutTestWords(uint16(offset))...)
		offset += 6
	}
	result = append(result, layoutTestWords(uint16(len(lookahead)))...)
	for range lookahead {
		result = append(result, layoutTestWords(uint16(offset))...)
		offset += 6
	}
	result = append(result, layoutTestWords(uint16(len(records)))...)
	result = append(result, gsubTestRecords(records)...)
	for _, glyph := range backtrack {
		result = append(result, gsubTestCoverage(glyph)...)
	}
	for _, glyph := range inputs {
		result = append(result, gsubTestCoverage(glyph)...)
	}
	for _, glyph := range lookahead {
		result = append(result, gsubTestCoverage(glyph)...)
	}
	return result
}

func gsubTestExtension(lookupType uint16, subtable []byte) []byte {
	result := layoutTestWords(1, lookupType)
	result = append(result, 0, 0, 0, 8)
	return append(result, subtable...)
}

func gsubTestReverse(first uint16, backtrack, lookahead []uint16, substitute uint16) []byte {
	headerBytes := 4 + 2 + 2*len(backtrack) + 2 + 2*len(lookahead) + 2 + 2
	offset := headerBytes
	coverageOffset := offset
	offset += 6
	result := layoutTestWords(1, uint16(coverageOffset), uint16(len(backtrack)))
	for range backtrack {
		result = append(result, layoutTestWords(uint16(offset))...)
		offset += 6
	}
	result = append(result, layoutTestWords(uint16(len(lookahead)))...)
	for range lookahead {
		result = append(result, layoutTestWords(uint16(offset))...)
		offset += 6
	}
	result = append(result, layoutTestWords(1, substitute)...)
	result = append(result, gsubTestCoverage(first)...)
	for _, glyph := range backtrack {
		result = append(result, gsubTestCoverage(glyph)...)
	}
	for _, glyph := range lookahead {
		result = append(result, gsubTestCoverage(glyph)...)
	}
	return result
}
