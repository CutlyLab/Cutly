package sfntglyf

func (value *layoutReader) parseGSUBSimpleSubtable(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, lookupType uint16, numGlyphs uint32, path string) (gsubSubtable, error) {
	switch lookupType {
	case 1:
		return value.parseGSUBSingle(parent, edge, start, numGlyphs, path)
	case 2:
		return value.parseGSUBMultiple(parent, edge, start, numGlyphs, path)
	case 3:
		return value.parseGSUBAlternate(parent, edge, start, numGlyphs, path)
	case 4:
		return value.parseGSUBLigatureSubst(parent, edge, start, numGlyphs, path)
	default:
		return nil, fontError(codeLayoutUnsupported, path+"/lookup_type", errLayoutUnsupported)
	}
}

func (value *layoutReader) parseGSUBSingle(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubSingle, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	var end uint64
	switch format {
	case 1:
		end = start + 6
	case 2:
		count, err := value.u16(start+4, path+"/glyph_count")
		if err != nil {
			return nil, err
		}
		end, err = layoutSpanEnd(start, 6, uint64(count), 2, path)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBSingle, layoutDecodeParams{first: uint64(format), second: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubSingle](node, path)
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
	result := &gsubSingle{format: format, coverage: coverage}
	if format == 1 {
		result.delta, _ = value.i16(start+4, path+"/delta")
		if err := value.meter.chargeSubstitutes(uint64(coverage.glyphCount), path); err != nil {
			return nil, err
		}
		for index := uint64(0); index < uint64(coverage.glyphCount); index++ {
			if err := value.checkContext(index, path+"/delta_outputs"); err != nil {
				return nil, err
			}
			glyph, ok := coverage.glyphAt(index)
			if !ok {
				return nil, fontError(codeLayoutInvariant, path+"/coverage_binding", ErrInvariantFailed)
			}
			substitute := uint16(int32(glyph) + int32(result.delta))
			if uint32(substitute) >= numGlyphs {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/delta_outputs", index), errLayoutInvalid)
			}
		}
		if err := value.meter.reserveRetained(32, path); err != nil {
			return nil, err
		}
	} else {
		count, _ := value.u16(start+4, path+"/glyph_count")
		if uint32(count) != coverage.glyphCount {
			return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
		}
		if err := value.meter.chargeSubstitutes(uint64(count), path); err != nil {
			return nil, err
		}
		if err := value.meter.reserveRetained(32+uint64(count)*2, path); err != nil {
			return nil, err
		}
		if err := contextFailure(value.ctx, path+"/substitutes/allocate"); err != nil {
			return nil, err
		}
		result.substitutes, err = layoutMakeSlice[uint16](uint64(count), path+"/substitutes")
		if err != nil {
			return nil, err
		}
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path+"/substitutes"); err != nil {
				return nil, err
			}
			glyph, _ := value.u16(start+6+2*index, path+"/substitutes")
			if uint32(glyph) >= numGlyphs {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/substitutes", index), errLayoutInvalid)
			}
			result.substitutes[index] = glyph
		}
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBMultiple(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubMultiple, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	if format != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	coverageOffset, _ := value.u16(start+2, path+"/coverage_offset")
	count, err := value.u16(start+4, path+"/sequence_count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBMultiple, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubMultiple](node, path)
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
	if err := value.meter.chargeOrdered(&value.meter.stats.sequenceReferences, uint64(count), path+"/sequences"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(32+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/sequences/allocate"); err != nil {
		return nil, err
	}
	sequences, err := layoutMakeSlice[*gsubSequence](uint64(count), path+"/sequences")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/sequences"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+6+2*index, path+"/sequence_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/sequence_offsets", index))
		if err != nil {
			return nil, err
		}
		sequence, err := value.parseGSUBSequence(node, layoutEdgeRef{role: layoutEdgeGSUBSequence, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, layoutPath(path+"/sequences", index))
		if err != nil {
			return nil, err
		}
		sequences[index] = sequence
	}
	result := &gsubMultiple{coverage: coverage, sequences: sequences}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBSequence(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubSequence, error) {
	count, err := value.u16(start, path+"/glyph_count")
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/glyph_count", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBSequence, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubSequence](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.sequenceTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeSubstitutes(uint64(count), path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*2, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/glyphs/allocate"); err != nil {
		return nil, err
	}
	glyphs, err := layoutMakeSlice[uint16](uint64(count), path+"/glyphs")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/glyphs"); err != nil {
			return nil, err
		}
		glyph, _ := value.u16(start+2+2*index, path+"/glyphs")
		if uint32(glyph) >= numGlyphs {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/glyphs", index), errLayoutInvalid)
		}
		glyphs[index] = glyph
	}
	result := &gsubSequence{glyphs: glyphs}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBAlternate(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubAlternate, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	if format != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	coverageOffset, _ := value.u16(start+2, path+"/coverage_offset")
	count, err := value.u16(start+4, path+"/alternate_set_count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBAlternate, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubAlternate](node, path)
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
	if err := value.meter.chargeOrdered(&value.meter.stats.alternateSetReferences, uint64(count), path+"/sets"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(32+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/sets/allocate"); err != nil {
		return nil, err
	}
	sets, err := layoutMakeSlice[*gsubAlternateSet](uint64(count), path+"/sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/sets"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+6+2*index, path+"/set_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/set_offsets", index))
		if err != nil {
			return nil, err
		}
		set, err := value.parseGSUBAlternateSet(node, layoutEdgeRef{role: layoutEdgeGSUBAlternateSet, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, layoutPath(path+"/sets", index))
		if err != nil {
			return nil, err
		}
		sets[index] = set
	}
	result := &gsubAlternate{coverage: coverage, sets: sets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBAlternateSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubAlternateSet, error) {
	count, err := value.u16(start, path+"/glyph_count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBAlternateSet, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubAlternateSet](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.alternateSetTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeSubstitutes(uint64(count), path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*2, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/glyphs/allocate"); err != nil {
		return nil, err
	}
	glyphs, err := layoutMakeSlice[uint16](uint64(count), path+"/glyphs")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/glyphs"); err != nil {
			return nil, err
		}
		glyph, _ := value.u16(start+2+2*index, path+"/glyphs")
		if uint32(glyph) >= numGlyphs {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/glyphs", index), errLayoutInvalid)
		}
		glyphs[index] = glyph
	}
	result := &gsubAlternateSet{glyphs: glyphs}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBLigatureSubst(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubLigatureSubst, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	if format != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	coverageOffset, _ := value.u16(start+2, path+"/coverage_offset")
	count, err := value.u16(start+4, path+"/ligature_set_count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBLigatureSubst, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubLigatureSubst](node, path)
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
	if err := value.meter.chargeOrdered(&value.meter.stats.ligatureSetReferences, uint64(count), path+"/sets"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(32+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/sets/allocate"); err != nil {
		return nil, err
	}
	sets, err := layoutMakeSlice[*gsubLigatureSet](uint64(count), path+"/sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/sets"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+6+2*index, path+"/set_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/set_offsets", index))
		if err != nil {
			return nil, err
		}
		set, err := value.parseGSUBLigatureSet(node, layoutEdgeRef{role: layoutEdgeGSUBLigatureSet, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, layoutPath(path+"/sets", index))
		if err != nil {
			return nil, err
		}
		sets[index] = set
	}
	result := &gsubLigatureSubst{coverage: coverage, sets: sets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBLigatureSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubLigatureSet, error) {
	count, err := value.u16(start, path+"/ligature_count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBLigatureSet, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubLigatureSet](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.ligatureSetTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.ligatureReferences, uint64(count), path+"/ligatures"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*8, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/ligatures/allocate"); err != nil {
		return nil, err
	}
	ligatures, err := layoutMakeSlice[*gsubLigature](uint64(count), path+"/ligatures")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/ligatures"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+2+2*index, path+"/ligature_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/ligature_offsets", index))
		if err != nil {
			return nil, err
		}
		ligature, err := value.parseGSUBLigature(node, layoutEdgeRef{role: layoutEdgeGSUBLigature, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, layoutPath(path+"/ligatures", index))
		if err != nil {
			return nil, err
		}
		ligatures[index] = ligature
	}
	result := &gsubLigatureSet{ligatures: ligatures}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGSUBLigature(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gsubLigature, error) {
	glyph, err := value.u16(start, path+"/ligature_glyph")
	if err != nil {
		return nil, err
	}
	componentCount, err := value.u16(start+2, path+"/component_count")
	if err != nil {
		return nil, err
	}
	if componentCount == 0 || uint32(glyph) >= numGlyphs {
		return nil, fontError(codeLayoutInvalid, path+"/header", errLayoutInvalid)
	}
	count := uint64(componentCount - 1)
	end, err := layoutSpanEnd(start, 4, count, 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGSUBLigature, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubLigature](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.ligatureTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeSubstitutes(1, path); err != nil {
		return nil, err
	}
	if err := value.meter.chargeLigatureComponents(count, path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+count*2, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/components/allocate"); err != nil {
		return nil, err
	}
	components, err := layoutMakeSlice[uint16](count, path+"/components")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < count; index++ {
		if err := value.checkContext(index, path+"/components"); err != nil {
			return nil, err
		}
		component, _ := value.u16(start+4+2*index, path+"/components")
		if uint32(component) >= numGlyphs {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/components", index), errLayoutInvalid)
		}
		components[index] = component
	}
	result := &gsubLigature{glyph: glyph, components: components}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
