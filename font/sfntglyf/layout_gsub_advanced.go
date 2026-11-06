package sfntglyf

func (value *layoutReader) parseGSUBChainedContext(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubChainedContext, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	switch format {
	case 1:
		return value.parseGSUBChainedContext1(parent, edge, start, numGlyphs, lookupCount, path)
	case 2:
		return value.parseGSUBChainedContext2(parent, edge, start, numGlyphs, lookupCount, path)
	case 3:
		return value.parseGSUBChainedContext3(parent, edge, start, numGlyphs, lookupCount, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
}

func (value *layoutReader) parseGSUBChainedContext1(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubChainedContext, error) {
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+4, path+"/rule_set_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 6, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBChainContext, gsubContextParams(1, numGlyphs, lookupCount), path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedContext](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageOffset), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGSUBCoverage, rawOffset: uint32(coverageOffset)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	if uint32(count) != coverage.glyphCount {
		return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.contextRuleSetReferences, uint64(count), path+"/rule_sets"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(80+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/rule_sets/allocate"); err != nil {
		return nil, err
	}
	ruleSets, err := layoutMakeSlice[*gsubChainedRuleSet](uint64(count), path+"/rule_sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/rule_sets"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+6+2*index, path+"/rule_set_offsets")
		if rawOffset == 0 {
			continue
		}
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/rule_set_offsets", index))
		if err != nil {
			return nil, err
		}
		ruleSet, err := value.parseGSUBChainedRuleSet(node, layoutEdgeRef{role: layoutEdgeGSUBChainRuleSet, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, lookupCount, layoutPath(path+"/rule_sets", index))
		if err != nil {
			return nil, err
		}
		ruleSets[index] = ruleSet
	}
	result := &gsubChainedContext{format: 1, coverage: coverage, ruleSets: ruleSets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBChainedRuleSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubChainedRuleSet, error) {
	count, err := value.u16(start, path+"/rule_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBChainRuleSet, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(lookupCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedRuleSet](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.contextRuleSets, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.contextRuleReferences, uint64(count), path+"/rules"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/rules/allocate"); err != nil {
		return nil, err
	}
	rules, err := layoutMakeSlice[*gsubChainedRule](uint64(count), path+"/rules")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/rules"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+2+2*index, path+"/rule_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/rule_offsets", index))
		if err != nil {
			return nil, err
		}
		rule, err := value.parseGSUBChainedRule(node, layoutEdgeRef{role: layoutEdgeGSUBChainRule, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, lookupCount, layoutPath(path+"/rules", index))
		if err != nil {
			return nil, err
		}
		rules[index] = rule
	}
	result := &gsubChainedRuleSet{rules: rules}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

type gsubChainedRuleLayout struct {
	backtrackStart uint64
	backtrackCount uint16
	inputStart     uint64
	inputCount     uint16
	lookaheadStart uint64
	lookaheadCount uint16
	recordStart    uint64
	recordCount    uint16
	end            uint64
}

func (value *layoutReader) gsubChainedRuleLayout(start uint64, path string) (gsubChainedRuleLayout, error) {
	var result gsubChainedRuleLayout
	cursor := start
	count, err := value.gsubCursorU16(&cursor, path+"/backtrack_count")
	if err != nil {
		return result, err
	}
	result.backtrackCount, result.backtrackStart = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/backtrack_sequence"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/input_count")
	if err != nil {
		return result, err
	}
	if count == 0 {
		return result, fontError(codeLayoutInvalid, path+"/input_count", errLayoutInvalid)
	}
	result.inputCount, result.inputStart = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count-1), 2, path+"/input_sequence"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/lookahead_count")
	if err != nil {
		return result, err
	}
	result.lookaheadCount, result.lookaheadStart = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/lookahead_sequence"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/lookup_count")
	if err != nil {
		return result, err
	}
	result.recordCount, result.recordStart = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 4, path+"/lookup_records"); err != nil {
		return result, err
	}
	result.end = cursor
	return result, nil
}

func (value *layoutReader) parseGSUBChainedRule(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubChainedRule, error) {
	layout, err := value.gsubChainedRuleLayout(start, path)
	if err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, layout.end, layoutSpanGSUBChainRule, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(lookupCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedRule](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.contextRules, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(80, path); err != nil {
		return nil, err
	}
	backtrack, err := value.gsubReadGlyphs(layout.backtrackStart, uint64(layout.backtrackCount), numGlyphs, path+"/backtrack_glyphs")
	if err != nil {
		return nil, err
	}
	inputs, err := value.gsubReadGlyphs(layout.inputStart, uint64(layout.inputCount-1), numGlyphs, path+"/input_glyphs")
	if err != nil {
		return nil, err
	}
	lookahead, err := value.gsubReadGlyphs(layout.lookaheadStart, uint64(layout.lookaheadCount), numGlyphs, path+"/lookahead_glyphs")
	if err != nil {
		return nil, err
	}
	records, err := value.gsubReadSequenceLookupRecords(layout.recordStart, uint64(layout.recordCount), layout.inputCount, lookupCount, path+"/lookup_records")
	if err != nil {
		return nil, err
	}
	result := &gsubChainedRule{backtrackGlyphs: backtrack, inputGlyphs: inputs, lookaheadGlyphs: lookahead, records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBChainedContext2(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubChainedContext, error) {
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	backtrackOffset, err := value.u16(start+4, path+"/backtrack_class_def_offset")
	if err != nil {
		return nil, err
	}
	inputOffset, err := value.u16(start+6, path+"/input_class_def_offset")
	if err != nil {
		return nil, err
	}
	lookaheadOffset, err := value.u16(start+8, path+"/lookahead_class_def_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+10, path+"/class_rule_set_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 12, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBChainContext, gsubContextParams(2, numGlyphs, lookupCount), path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedContext](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageOffset), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGSUBCoverage, rawOffset: uint32(coverageOffset)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	parseClass := func(raw uint16, role layoutEdgeRole, name string) (*layoutClassDef, error) {
		childStart, err := value.childOffset(start, uint64(raw), path+"/"+name+"_offset")
		if err != nil {
			return nil, err
		}
		return value.parseLayoutClassDef(node, layoutEdgeRef{role: role, rawOffset: uint32(raw)}, childStart, path+"/"+name)
	}
	backtrackClassDef, err := parseClass(backtrackOffset, layoutEdgeGSUBBacktrackClassDef, "backtrack_class_def")
	if err != nil {
		return nil, err
	}
	inputClassDef, err := parseClass(inputOffset, layoutEdgeGSUBInputClassDef, "input_class_def")
	if err != nil {
		return nil, err
	}
	lookaheadClassDef, err := parseClass(lookaheadOffset, layoutEdgeGSUBLookaheadClassDef, "lookahead_class_def")
	if err != nil {
		return nil, err
	}
	backtrackMaximum := layoutClassDefMax(backtrackClassDef)
	inputMaximum := layoutClassDefMax(inputClassDef)
	lookaheadMaximum := layoutClassDefMax(lookaheadClassDef)
	if uint64(count) != uint64(inputMaximum)+1 {
		return nil, fontError(codeLayoutInvalid, path+"/class_rule_set_cardinality", errLayoutInvalid)
	}
	for index := uint64(0); index < uint64(coverage.glyphCount); index++ {
		if err := value.checkContext(index, path+"/coverage_classes"); err != nil {
			return nil, err
		}
		glyph, ok := coverage.glyphAt(index)
		if !ok || uint64(inputClassDef.classAt(uint32(glyph))) >= uint64(count) {
			return nil, fontError(codeLayoutInvalid, path+"/coverage_classes", errLayoutInvalid)
		}
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.contextRuleSetReferences, uint64(count), path+"/class_rule_sets"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(96+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/class_rule_sets/allocate"); err != nil {
		return nil, err
	}
	ruleSets, err := layoutMakeSlice[*gsubChainedClassRuleSet](uint64(count), path+"/class_rule_sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/class_rule_sets"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+12+2*index, path+"/class_rule_set_offsets")
		if rawOffset == 0 {
			continue
		}
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/class_rule_set_offsets", index))
		if err != nil {
			return nil, err
		}
		ruleSet, err := value.parseGSUBChainedClassRuleSet(node, layoutEdgeRef{role: layoutEdgeGSUBChainRuleSet, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount, layoutPath(path+"/class_rule_sets", index))
		if err != nil {
			return nil, err
		}
		ruleSets[index] = ruleSet
	}
	result := &gsubChainedContext{
		format: 2, coverage: coverage, backtrackClassDef: backtrackClassDef,
		inputClassDef: inputClassDef, lookaheadClassDef: lookaheadClassDef, classRuleSets: ruleSets,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func gsubChainedClassParams(backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount uint16) layoutDecodeParams {
	return layoutDecodeParams{
		first:  uint64(backtrackMaximum)<<32 | uint64(inputMaximum)<<16 | uint64(lookaheadMaximum),
		second: uint64(lookupCount),
	}
}

func (value *layoutReader) parseGSUBChainedClassRuleSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount uint16, path string) (*gsubChainedClassRuleSet, error) {
	count, err := value.u16(start, path+"/rule_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := gsubChainedClassParams(backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount)
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBChainClassRuleSet, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedClassRuleSet](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.contextRuleSets, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.contextRuleReferences, uint64(count), path+"/rules"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/rules/allocate"); err != nil {
		return nil, err
	}
	rules, err := layoutMakeSlice[*gsubChainedClassRule](uint64(count), path+"/rules")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/rules"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+2+2*index, path+"/rule_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/rule_offsets", index))
		if err != nil {
			return nil, err
		}
		rule, err := value.parseGSUBChainedClassRule(node, layoutEdgeRef{role: layoutEdgeGSUBChainRule, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount, layoutPath(path+"/rules", index))
		if err != nil {
			return nil, err
		}
		rules[index] = rule
	}
	result := &gsubChainedClassRuleSet{rules: rules}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBChainedClassRule(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount uint16, path string) (*gsubChainedClassRule, error) {
	layout, err := value.gsubChainedRuleLayout(start, path)
	if err != nil {
		return nil, err
	}
	params := gsubChainedClassParams(backtrackMaximum, inputMaximum, lookaheadMaximum, lookupCount)
	node, alias, err := value.graph.begin(parent, edge, start, layout.end, layoutSpanGSUBChainClassRule, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedClassRule](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.contextRules, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(80, path); err != nil {
		return nil, err
	}
	backtrack, err := value.gsubReadClasses(layout.backtrackStart, uint64(layout.backtrackCount), backtrackMaximum, path+"/backtrack_classes")
	if err != nil {
		return nil, err
	}
	inputs, err := value.gsubReadClasses(layout.inputStart, uint64(layout.inputCount-1), inputMaximum, path+"/input_classes")
	if err != nil {
		return nil, err
	}
	lookahead, err := value.gsubReadClasses(layout.lookaheadStart, uint64(layout.lookaheadCount), lookaheadMaximum, path+"/lookahead_classes")
	if err != nil {
		return nil, err
	}
	records, err := value.gsubReadSequenceLookupRecords(layout.recordStart, uint64(layout.recordCount), layout.inputCount, lookupCount, path+"/lookup_records")
	if err != nil {
		return nil, err
	}
	result := &gsubChainedClassRule{backtrackClasses: backtrack, inputClasses: inputs, lookaheadClasses: lookahead, records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

type gsubChainedContext3Layout struct {
	backtrackOffsets uint64
	backtrackCount   uint16
	inputOffsets     uint64
	inputCount       uint16
	lookaheadOffsets uint64
	lookaheadCount   uint16
	recordStart      uint64
	recordCount      uint16
	end              uint64
}

func (value *layoutReader) gsubChainedContext3Layout(start uint64, path string) (gsubChainedContext3Layout, error) {
	var result gsubChainedContext3Layout
	cursor := start + 2
	count, err := value.gsubCursorU16(&cursor, path+"/backtrack_count")
	if err != nil {
		return result, err
	}
	result.backtrackCount, result.backtrackOffsets = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/backtrack_coverage_offsets"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/input_count")
	if err != nil {
		return result, err
	}
	if count == 0 {
		return result, fontError(codeLayoutInvalid, path+"/input_count", errLayoutInvalid)
	}
	result.inputCount, result.inputOffsets = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/input_coverage_offsets"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/lookahead_count")
	if err != nil {
		return result, err
	}
	result.lookaheadCount, result.lookaheadOffsets = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/lookahead_coverage_offsets"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/lookup_count")
	if err != nil {
		return result, err
	}
	result.recordCount, result.recordStart = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 4, path+"/lookup_records"); err != nil {
		return result, err
	}
	result.end = cursor
	return result, nil
}

func (value *layoutReader) gsubParseCoverageOffsets(parent *layoutSpanNode, offsetStart uint64, count uint16, numGlyphs uint32, role layoutEdgeRole, path string) ([]*gdefCoverage, error) {
	if err := value.meter.reserveRetained(uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/allocate"); err != nil {
		return nil, err
	}
	result, err := layoutMakeSlice[*gdefCoverage](uint64(count), path)
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		rawOffset, err := value.u16(offsetStart+2*index, path+"/offsets")
		if err != nil {
			return nil, err
		}
		childStart, err := value.childOffset(parent.start, uint64(rawOffset), layoutPath(path+"/offsets", index))
		if err != nil {
			return nil, err
		}
		coverage, err := value.parseCoverage(parent, layoutEdgeRef{role: role, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, layoutPath(path, index))
		if err != nil {
			return nil, err
		}
		result[index] = coverage
	}
	return result, nil
}

func (value *layoutReader) parseGSUBChainedContext3(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubChainedContext, error) {
	layout, err := value.gsubChainedContext3Layout(start, path)
	if err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, layout.end, layoutSpanGSUBChainContext, gsubContextParams(3, numGlyphs, lookupCount), path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubChainedContext](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	positions := uint64(layout.backtrackCount) + uint64(layout.inputCount) + uint64(layout.lookaheadCount)
	if err := value.meter.chargeContextGlyphs(positions, path+"/coverage_positions"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(96, path); err != nil {
		return nil, err
	}
	backtrack, err := value.gsubParseCoverageOffsets(node, layout.backtrackOffsets, layout.backtrackCount, numGlyphs, layoutEdgeGSUBBacktrackCoverage, path+"/backtrack_coverages")
	if err != nil {
		return nil, err
	}
	inputs, err := value.gsubParseCoverageOffsets(node, layout.inputOffsets, layout.inputCount, numGlyphs, layoutEdgeGSUBInputCoverage, path+"/input_coverages")
	if err != nil {
		return nil, err
	}
	lookahead, err := value.gsubParseCoverageOffsets(node, layout.lookaheadOffsets, layout.lookaheadCount, numGlyphs, layoutEdgeGSUBLookaheadCoverage, path+"/lookahead_coverages")
	if err != nil {
		return nil, err
	}
	records, err := value.gsubReadSequenceLookupRecords(layout.recordStart, uint64(layout.recordCount), layout.inputCount, lookupCount, path+"/lookup_records")
	if err != nil {
		return nil, err
	}
	result := &gsubChainedContext{
		format: 3, backtrackCoverages: backtrack, inputCoverages: inputs,
		lookaheadCoverages: lookahead, records: records,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBExtension(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubExtension, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	extensionType, err := value.u16(start+2, path+"/extension_lookup_type")
	if err != nil {
		return nil, err
	}
	extensionOffset, err := value.u32(start+4, path+"/extension_offset")
	if err != nil {
		return nil, err
	}
	if format != 1 || extensionType < 1 || extensionType > 8 || extensionType == 7 {
		return nil, fontError(codeLayoutInvalid, path+"/header", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, 8, 0, 0, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(numGlyphs), second: uint64(lookupCount)<<16 | uint64(extensionType)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBExtension, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubExtension](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.meter.charge(&value.meter.stats.extensionSubtables, 1, value.meter.limits.maxOrderedRecords, path+"/extensions"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(32, path); err != nil {
		return nil, err
	}
	childStart, err := value.childOffset(start, uint64(extensionOffset), path+"/extension_offset")
	if err != nil {
		return nil, err
	}
	subtable, err := value.parseGSUBSubtable(node, layoutEdgeRef{role: layoutEdgeGSUBExtension, rawOffset: extensionOffset}, childStart, extensionType, numGlyphs, lookupCount, path+"/extension")
	if err != nil {
		return nil, err
	}
	if subtable == nil || subtable.gsubLookupType() != extensionType {
		return nil, fontError(codeLayoutInvariant, path+"/extension_binding", ErrInvariantFailed)
	}
	result := &gsubExtension{extensionLookupType: extensionType, subtable: subtable}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

type gsubReverseLayout struct {
	coverageOffset   uint16
	backtrackOffsets uint64
	backtrackCount   uint16
	lookaheadOffsets uint64
	lookaheadCount   uint16
	substituteStart  uint64
	glyphCount       uint16
	end              uint64
}

func (value *layoutReader) gsubReverseLayout(start uint64, path string) (gsubReverseLayout, error) {
	var result gsubReverseLayout
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return result, err
	}
	if format != 1 {
		return result, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	result.coverageOffset, err = value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return result, err
	}
	cursor := start + 4
	count, err := value.gsubCursorU16(&cursor, path+"/backtrack_count")
	if err != nil {
		return result, err
	}
	result.backtrackCount, result.backtrackOffsets = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/backtrack_coverage_offsets"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/lookahead_count")
	if err != nil {
		return result, err
	}
	result.lookaheadCount, result.lookaheadOffsets = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/lookahead_coverage_offsets"); err != nil {
		return result, err
	}
	count, err = value.gsubCursorU16(&cursor, path+"/glyph_count")
	if err != nil {
		return result, err
	}
	if count == 0 {
		return result, fontError(codeLayoutInvalid, path+"/glyph_count", errLayoutInvalid)
	}
	result.glyphCount, result.substituteStart = count, cursor
	if err := value.gsubCursorSkip(&cursor, uint64(count), 2, path+"/substitutes"); err != nil {
		return result, err
	}
	result.end = cursor
	return result, nil
}

func (value *layoutReader) parseGSUBReverseChain(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubReverseChain, error) {
	layout, err := value.gsubReverseLayout(start, path)
	if err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, layout.end, layoutSpanGSUBReverseChain, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubReverseChain](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.meter.charge(&value.meter.stats.reverseSubtables, 1, value.meter.limits.maxOrderedRecords, path+"/reverse_subtables"); err != nil {
		return nil, err
	}
	positions := uint64(layout.backtrackCount) + 1 + uint64(layout.lookaheadCount)
	if err := value.meter.chargeContextGlyphs(positions, path+"/coverage_positions"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(72, path); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(layout.coverageOffset), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGSUBCoverage, rawOffset: uint32(layout.coverageOffset)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	if coverage.glyphCount != uint32(layout.glyphCount) {
		return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
	}
	backtrack, err := value.gsubParseCoverageOffsets(node, layout.backtrackOffsets, layout.backtrackCount, numGlyphs, layoutEdgeGSUBBacktrackCoverage, path+"/backtrack_coverages")
	if err != nil {
		return nil, err
	}
	lookahead, err := value.gsubParseCoverageOffsets(node, layout.lookaheadOffsets, layout.lookaheadCount, numGlyphs, layoutEdgeGSUBLookaheadCoverage, path+"/lookahead_coverages")
	if err != nil {
		return nil, err
	}
	if err := value.meter.chargeSubstitutes(uint64(layout.glyphCount), path+"/substitutes"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(layout.glyphCount)*2, path+"/substitutes"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/substitutes/allocate"); err != nil {
		return nil, err
	}
	substitutes, err := layoutMakeSlice[uint16](uint64(layout.glyphCount), path+"/substitutes")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(layout.glyphCount); index++ {
		if err := value.checkContext(index, path+"/substitutes"); err != nil {
			return nil, err
		}
		glyph, err := value.u16(layout.substituteStart+2*index, layoutPath(path+"/substitutes", index))
		if err != nil {
			return nil, err
		}
		if uint32(glyph) >= numGlyphs {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/substitutes", index), errLayoutInvalid)
		}
		substitutes[index] = glyph
	}
	result := &gsubReverseChain{coverage: coverage, backtrackCoverages: backtrack, lookaheadCoverages: lookahead, substitutes: substitutes}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
