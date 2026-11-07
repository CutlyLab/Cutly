package sfntglyf

func (value *layoutReader) parseGSUBSubtable(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, lookupType uint16, numGlyphs uint32, lookupCount uint16, path string) (gsubSubtable, error) {
	switch lookupType {
	case 1, 2, 3, 4:
		return value.parseGSUBSimpleSubtable(parent, edge, start, lookupType, numGlyphs, path)
	case 5:
		return value.parseGSUBContext(parent, edge, start, numGlyphs, lookupCount, path)
	case 6:
		return value.parseGSUBChainedContext(parent, edge, start, numGlyphs, lookupCount, path)
	case 7:
		return value.parseGSUBExtension(parent, edge, start, numGlyphs, lookupCount, path)
	case 8:
		return value.parseGSUBReverseChain(parent, edge, start, numGlyphs, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/lookup_type", errLayoutInvalid)
	}
}

func (value *layoutReader) gsubCursorU16(cursor *uint64, path string) (uint16, error) {
	result, err := value.u16(*cursor, path)
	if err != nil {
		return 0, err
	}
	next, ok := checkedAdd(*cursor, 2)
	if !ok {
		return 0, fontError(codeLayoutInvalid, path, errLayoutInvalid)
	}
	*cursor = next
	return result, nil
}

func (value *layoutReader) gsubCursorSkip(cursor *uint64, count, width uint64, path string) error {
	size, ok := checkedMultiply(count, width)
	if !ok {
		return fontError(codeLayoutInvalid, path, errLayoutInvalid)
	}
	if _, err := value.bytes(*cursor, size, path); err != nil {
		return err
	}
	next, ok := checkedAdd(*cursor, size)
	if !ok {
		return fontError(codeLayoutInvalid, path, errLayoutInvalid)
	}
	*cursor = next
	return nil
}

func gsubContextParams(format uint16, numGlyphs uint32, lookupCount uint16) layoutDecodeParams {
	return layoutDecodeParams{
		first:  uint64(format)<<32 | uint64(numGlyphs),
		second: uint64(lookupCount),
	}
}

func (value *layoutReader) gsubReadGlyphs(start, count uint64, numGlyphs uint32, path string) ([]uint16, error) {
	if err := value.meter.chargeContextGlyphs(count, path); err != nil {
		return nil, err
	}
	bytes, err := layoutRetainedArrayBytes(count, 2, path)
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(bytes, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/allocate"); err != nil {
		return nil, err
	}
	result, err := layoutMakeSlice[uint16](count, path)
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < count; index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		glyph, err := value.u16(start+2*index, layoutPath(path, index))
		if err != nil {
			return nil, err
		}
		if uint32(glyph) >= numGlyphs {
			return nil, fontError(codeLayoutInvalid, layoutPath(path, index), errLayoutInvalid)
		}
		result[index] = glyph
	}
	return result, nil
}

func (value *layoutReader) gsubReadClasses(start, count uint64, maximum uint16, path string) ([]uint16, error) {
	if err := value.meter.chargeContextClasses(count, path); err != nil {
		return nil, err
	}
	bytes, err := layoutRetainedArrayBytes(count, 2, path)
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(bytes, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/allocate"); err != nil {
		return nil, err
	}
	result, err := layoutMakeSlice[uint16](count, path)
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < count; index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		class, err := value.u16(start+2*index, layoutPath(path, index))
		if err != nil {
			return nil, err
		}
		if class > maximum {
			return nil, fontError(codeLayoutInvalid, layoutPath(path, index), errLayoutInvalid)
		}
		result[index] = class
	}
	return result, nil
}

func (value *layoutReader) gsubReadSequenceLookupRecords(start, count uint64, inputGlyphCount uint16, lookupCount uint16, path string) ([]gsubSequenceLookupRecord, error) {
	if err := value.meter.chargeSequenceLookups(count, path); err != nil {
		return nil, err
	}
	bytes, err := layoutRetainedArrayBytes(count, 4, path)
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(bytes, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/allocate"); err != nil {
		return nil, err
	}
	result, err := layoutMakeSlice[gsubSequenceLookupRecord](count, path)
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < count; index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		recordStart := start + 4*index
		sequenceIndex, err := value.u16(recordStart, layoutPath(path, index)+"/sequence_index")
		if err != nil {
			return nil, err
		}
		lookupIndex, err := value.u16(recordStart+2, layoutPath(path, index)+"/lookup_index")
		if err != nil {
			return nil, err
		}
		if sequenceIndex >= inputGlyphCount || lookupIndex >= lookupCount {
			return nil, fontError(codeLayoutInvalid, layoutPath(path, index), errLayoutInvalid)
		}
		result[index] = gsubSequenceLookupRecord{sequenceIndex: sequenceIndex, lookupIndex: lookupIndex}
	}
	return result, nil
}

func (value *layoutReader) parseGSUBContext(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubContext, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	switch format {
	case 1:
		return value.parseGSUBContext1(parent, edge, start, numGlyphs, lookupCount, path)
	case 2:
		return value.parseGSUBContext2(parent, edge, start, numGlyphs, lookupCount, path)
	case 3:
		return value.parseGSUBContext3(parent, edge, start, numGlyphs, lookupCount, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
}

func (value *layoutReader) parseGSUBContext1(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubContext, error) {
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBContext, gsubContextParams(1, numGlyphs, lookupCount), path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubContext](node, path)
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
	if err := value.meter.reserveRetained(64+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/rule_sets/allocate"); err != nil {
		return nil, err
	}
	ruleSets, err := layoutMakeSlice[*gsubSequenceRuleSet](uint64(count), path+"/rule_sets")
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
		ruleSet, err := value.parseGSUBSequenceRuleSet(node, layoutEdgeRef{role: layoutEdgeGSUBContextRuleSet, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, lookupCount, layoutPath(path+"/rule_sets", index))
		if err != nil {
			return nil, err
		}
		ruleSets[index] = ruleSet
	}
	result := &gsubContext{format: 1, coverage: coverage, ruleSets: ruleSets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBSequenceRuleSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubSequenceRuleSet, error) {
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBSequenceRuleSet, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(lookupCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubSequenceRuleSet](node, path)
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
	rules, err := layoutMakeSlice[*gsubSequenceRule](uint64(count), path+"/rules")
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
		rule, err := value.parseGSUBSequenceRule(node, layoutEdgeRef{role: layoutEdgeGSUBContextRule, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, lookupCount, layoutPath(path+"/rules", index))
		if err != nil {
			return nil, err
		}
		rules[index] = rule
	}
	result := &gsubSequenceRuleSet{rules: rules}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBSequenceRule(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubSequenceRule, error) {
	glyphCount, err := value.u16(start, path+"/glyph_count")
	if err != nil {
		return nil, err
	}
	recordCount, err := value.u16(start+2, path+"/lookup_count")
	if err != nil {
		return nil, err
	}
	if glyphCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/glyph_count", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, 4, uint64(glyphCount-1), 2, path)
	if err != nil {
		return nil, err
	}
	end, err = layoutSpanEnd(end, 0, uint64(recordCount), 4, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBSequenceRule, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(lookupCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubSequenceRule](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.contextRules, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(48, path); err != nil {
		return nil, err
	}
	inputCount := uint64(glyphCount - 1)
	inputs, err := value.gsubReadGlyphs(start+4, inputCount, numGlyphs, path+"/input_glyphs")
	if err != nil {
		return nil, err
	}
	records, err := value.gsubReadSequenceLookupRecords(start+4+2*inputCount, uint64(recordCount), glyphCount, lookupCount, path+"/lookup_records")
	if err != nil {
		return nil, err
	}
	result := &gsubSequenceRule{inputGlyphs: inputs, records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBContext2(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubContext, error) {
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	classDefOffset, err := value.u16(start+4, path+"/class_def_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+6, path+"/class_rule_set_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 8, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBContext, gsubContextParams(2, numGlyphs, lookupCount), path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubContext](node, path)
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
	classDefStart, err := value.childOffset(start, uint64(classDefOffset), path+"/class_def_offset")
	if err != nil {
		return nil, err
	}
	classDef, err := value.parseLayoutClassDef(node, layoutEdgeRef{role: layoutEdgeGSUBContextClassDef, rawOffset: uint32(classDefOffset)}, classDefStart, path+"/class_def")
	if err != nil {
		return nil, err
	}
	maximum := layoutClassDefMax(classDef)
	if uint64(count) != uint64(maximum)+1 {
		return nil, fontError(codeLayoutInvalid, path+"/class_rule_set_cardinality", errLayoutInvalid)
	}
	for index := uint64(0); index < uint64(coverage.glyphCount); index++ {
		if err := value.checkContext(index, path+"/coverage_classes"); err != nil {
			return nil, err
		}
		glyph, ok := coverage.glyphAt(index)
		if !ok || uint64(classDef.classAt(uint32(glyph))) >= uint64(count) {
			return nil, fontError(codeLayoutInvalid, path+"/coverage_classes", errLayoutInvalid)
		}
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.contextRuleSetReferences, uint64(count), path+"/class_rule_sets"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(64+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/class_rule_sets/allocate"); err != nil {
		return nil, err
	}
	ruleSets, err := layoutMakeSlice[*gsubClassSequenceRuleSet](uint64(count), path+"/class_rule_sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/class_rule_sets"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+8+2*index, path+"/class_rule_set_offsets")
		if rawOffset == 0 {
			continue
		}
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/class_rule_set_offsets", index))
		if err != nil {
			return nil, err
		}
		ruleSet, err := value.parseGSUBClassSequenceRuleSet(node, layoutEdgeRef{role: layoutEdgeGSUBContextRuleSet, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, maximum, lookupCount, layoutPath(path+"/class_rule_sets", index))
		if err != nil {
			return nil, err
		}
		ruleSets[index] = ruleSet
	}
	result := &gsubContext{format: 2, coverage: coverage, classDef: classDef, classRuleSets: ruleSets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBClassSequenceRuleSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, maximum uint16, lookupCount uint16, path string) (*gsubClassSequenceRuleSet, error) {
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
	params := layoutDecodeParams{first: uint64(maximum), second: uint64(lookupCount)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBClassSequenceRuleSet, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubClassSequenceRuleSet](node, path)
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
	rules, err := layoutMakeSlice[*gsubClassSequenceRule](uint64(count), path+"/rules")
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
		rule, err := value.parseGSUBClassSequenceRule(node, layoutEdgeRef{role: layoutEdgeGSUBContextRule, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, maximum, lookupCount, layoutPath(path+"/rules", index))
		if err != nil {
			return nil, err
		}
		rules[index] = rule
	}
	result := &gsubClassSequenceRuleSet{rules: rules}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBClassSequenceRule(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, maximum uint16, lookupCount uint16, path string) (*gsubClassSequenceRule, error) {
	glyphCount, err := value.u16(start, path+"/glyph_count")
	if err != nil {
		return nil, err
	}
	recordCount, err := value.u16(start+2, path+"/lookup_count")
	if err != nil {
		return nil, err
	}
	if glyphCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/glyph_count", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, 4, uint64(glyphCount-1), 2, path)
	if err != nil {
		return nil, err
	}
	end, err = layoutSpanEnd(end, 0, uint64(recordCount), 4, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(maximum), second: uint64(lookupCount)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBClassSequenceRule, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubClassSequenceRule](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.contextRules, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(48, path); err != nil {
		return nil, err
	}
	inputCount := uint64(glyphCount - 1)
	inputs, err := value.gsubReadClasses(start+4, inputCount, maximum, path+"/input_classes")
	if err != nil {
		return nil, err
	}
	records, err := value.gsubReadSequenceLookupRecords(start+4+2*inputCount, uint64(recordCount), glyphCount, lookupCount, path+"/lookup_records")
	if err != nil {
		return nil, err
	}
	result := &gsubClassSequenceRule{inputClasses: inputs, records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBContext3(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, path string) (*gsubContext, error) {
	glyphCount, err := value.u16(start+2, path+"/glyph_count")
	if err != nil {
		return nil, err
	}
	recordCount, err := value.u16(start+4, path+"/lookup_count")
	if err != nil {
		return nil, err
	}
	if glyphCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/glyph_count", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, 6, uint64(glyphCount), 2, path)
	if err != nil {
		return nil, err
	}
	end, err = layoutSpanEnd(end, 0, uint64(recordCount), 4, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBContext, gsubContextParams(3, numGlyphs, lookupCount), path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubContext](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.meter.chargeContextGlyphs(uint64(glyphCount), path+"/input_coverages"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(64+uint64(glyphCount)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/input_coverages/allocate"); err != nil {
		return nil, err
	}
	coverages, err := layoutMakeSlice[*gdefCoverage](uint64(glyphCount), path+"/input_coverages")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(glyphCount); index++ {
		if err := value.checkContext(index, path+"/input_coverages"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+6+2*index, path+"/coverage_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/coverage_offsets", index))
		if err != nil {
			return nil, err
		}
		coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGSUBInputCoverage, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, layoutPath(path+"/input_coverages", index))
		if err != nil {
			return nil, err
		}
		coverages[index] = coverage
	}
	recordStart := start + 6 + 2*uint64(glyphCount)
	records, err := value.gsubReadSequenceLookupRecords(recordStart, uint64(recordCount), glyphCount, lookupCount, path+"/lookup_records")
	if err != nil {
		return nil, err
	}
	result := &gsubContext{format: 3, coverages: coverages, records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
