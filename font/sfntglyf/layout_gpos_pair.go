package sfntglyf

func (value *layoutReader) parseGPOSPair(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gposPair, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	switch format {
	case 1:
		return value.parseGPOSPair1(parent, edge, start, numGlyphs, path)
	case 2:
		return value.parseGPOSPair2(parent, edge, start, numGlyphs, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
}

func (value *layoutReader) parseGPOSPair1(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gposPair, error) {
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	format1, err := value.u16(start+4, path+"/value_format_1")
	if err != nil {
		return nil, err
	}
	format2, err := value.u16(start+6, path+"/value_format_2")
	if err != nil {
		return nil, err
	}
	if _, err := gposValueRecordSize(format1, path+"/value_1"); err != nil {
		return nil, err
	}
	if _, err := gposValueRecordSize(format2, path+"/value_2"); err != nil {
		return nil, err
	}
	count, err := value.u16(start+8, path+"/pair_set_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 10, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(1)<<48 | uint64(format1)<<32 | uint64(format2)<<16 | uint64(numGlyphs), second: uint64(count)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSPair, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposPair](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageOffset), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSCoverage, rawOffset: uint32(coverageOffset)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	if coverage.glyphCount != uint32(count) {
		return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.pairSetTables, uint64(count), path+"/pair_sets"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*8+64, path+"/pair_sets"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/pair_sets/allocate"); err != nil {
		return nil, err
	}
	sets, err := layoutMakeSlice[*gposPairSet](uint64(count), path+"/pair_sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/pair_sets"); err != nil {
			return nil, err
		}
		raw, _ := value.u16(start+10+2*index, path+"/pair_set_offsets")
		childStart, err := value.childOffset(start, uint64(raw), layoutPath(path+"/pair_set_offsets", index))
		if err != nil {
			return nil, err
		}
		set, err := value.parseGPOSPairSet(node, layoutEdgeRef{role: layoutEdgeGPOSPairSet, ordinal: uint32(index), rawOffset: uint32(raw)}, childStart, format1, format2, numGlyphs, layoutPath(path+"/pair_sets", index))
		if err != nil {
			return nil, err
		}
		sets[index] = set
	}
	result := &gposPair{format: 1, coverage: coverage, valueFormat1: format1, valueFormat2: format2, pairSets: sets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGPOSPairSet(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, format1, format2 uint16, numGlyphs uint32, path string) (*gposPairSet, error) {
	count, err := value.u16(start, path+"/pair_value_count")
	if err != nil {
		return nil, err
	}
	size1, err := gposValueRecordSize(format1, path+"/value_1")
	if err != nil {
		return nil, err
	}
	size2, err := gposValueRecordSize(format2, path+"/value_2")
	if err != nil {
		return nil, err
	}
	recordSize, ok := checkedAdd(2, size1)
	if ok {
		recordSize, ok = checkedAdd(recordSize, size2)
	}
	if !ok {
		return nil, fontError(codeLayoutInvalid, path+"/record_size", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), recordSize, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(format1)<<48 | uint64(format2)<<32 | uint64(numGlyphs), second: uint64(count)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSPairSet, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposPairSet](node, path)
	}
	if err := value.meter.observe(&value.meter.stats.pairValueRecords, uint64(count), path+"/pair_values"); err != nil {
		return nil, err
	}
	valueCount, ok := checkedMultiply(uint64(count), 2)
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/value_count", ErrResourceLimit)
	}
	if err := value.admitGPOSValueRecords(valueCount, path+"/values"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*16+24, path+"/records"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/records/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[gposPairValueRecord](uint64(count), path+"/records")
	if err != nil {
		return nil, err
	}
	var previous uint16
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/records"); err != nil {
			return nil, err
		}
		recordStart := start + 2 + recordSize*index
		second, _ := value.u16(recordStart, layoutPath(path+"/records", index)+"/second_glyph")
		if uint32(second) >= numGlyphs || (index != 0 && second <= previous) {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/records", index)+"/second_glyph", errLayoutInvalid)
		}
		value1, err := value.parseGPOSValueRecord(node, recordStart+2, start, format1, index*2, layoutPath(path+"/records", index)+"/value_1")
		if err != nil {
			return nil, err
		}
		value2, err := value.parseGPOSValueRecord(node, recordStart+2+size1, start, format2, index*2+1, layoutPath(path+"/records", index)+"/value_2")
		if err != nil {
			return nil, err
		}
		records[index] = gposPairValueRecord{secondGlyph: second, value1: value1, value2: value2}
		previous = second
	}
	result := &gposPairSet{records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGPOSPair2(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gposPair, error) {
	if _, err := value.bytes(start, 16, path+"/header"); err != nil {
		return nil, err
	}
	coverageOffset := mustLayoutU16(value, start+2, path+"/coverage_offset")
	format1 := mustLayoutU16(value, start+4, path+"/value_format_1")
	format2 := mustLayoutU16(value, start+6, path+"/value_format_2")
	classDef1Offset := mustLayoutU16(value, start+8, path+"/class_def_1_offset")
	classDef2Offset := mustLayoutU16(value, start+10, path+"/class_def_2_offset")
	class1Count := mustLayoutU16(value, start+12, path+"/class_1_count")
	class2Count := mustLayoutU16(value, start+14, path+"/class_2_count")
	if class1Count == 0 || class2Count == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/class_count", errLayoutInvalid)
	}
	size1, err := gposValueRecordSize(format1, path+"/value_1")
	if err != nil {
		return nil, err
	}
	size2, err := gposValueRecordSize(format2, path+"/value_2")
	if err != nil {
		return nil, err
	}
	recordSize, ok := checkedAdd(size1, size2)
	if !ok {
		return nil, fontError(codeLayoutInvalid, path+"/record_size", errLayoutInvalid)
	}
	recordCount, ok := checkedMultiply(uint64(class1Count), uint64(class2Count))
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/class_pairs", ErrResourceLimit)
	}
	end, err := layoutSpanEnd(start, 16, recordCount, recordSize, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(2)<<48 | uint64(format1)<<32 | uint64(format2)<<16 | uint64(numGlyphs), second: uint64(class1Count)<<16 | uint64(class2Count)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSPair, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposPair](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.classPairRecords, recordCount, path+"/class_pairs"); err != nil {
		return nil, err
	}
	valueCount, ok := checkedMultiply(recordCount, 2)
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/value_count", ErrResourceLimit)
	}
	if err := value.admitGPOSValueRecords(valueCount, path+"/values"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(recordCount*16+80, path+"/class_pairs"); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageOffset), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSCoverage, rawOffset: uint32(coverageOffset)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	class1Start, err := value.childOffset(start, uint64(classDef1Offset), path+"/class_def_1_offset")
	if err != nil {
		return nil, err
	}
	classDef1, err := value.parseLayoutClassDef(node, layoutEdgeRef{role: layoutEdgeGPOSClassDef1, rawOffset: uint32(classDef1Offset)}, class1Start, path+"/class_def_1")
	if err != nil {
		return nil, err
	}
	class2Start, err := value.childOffset(start, uint64(classDef2Offset), path+"/class_def_2_offset")
	if err != nil {
		return nil, err
	}
	classDef2, err := value.parseLayoutClassDef(node, layoutEdgeRef{role: layoutEdgeGPOSClassDef2, rawOffset: uint32(classDef2Offset)}, class2Start, path+"/class_def_2")
	if err != nil {
		return nil, err
	}
	if layoutClassDefMax(classDef1) >= class1Count || layoutClassDefMax(classDef2) >= class2Count {
		return nil, fontError(codeLayoutInvalid, path+"/class_domain", errLayoutInvalid)
	}
	if err := value.meter.charge(&value.meter.stats.orderedRecords, uint64(coverage.glyphCount), value.meter.limits.maxOrderedRecords, path+"/coverage_class_validation"); err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(coverage.glyphCount); index++ {
		if err := value.checkContext(index, path+"/coverage_class_validation"); err != nil {
			return nil, err
		}
		glyph, ok := coverage.glyphAt(index)
		if !ok || classDef1.classAt(uint32(glyph)) >= class1Count {
			return nil, fontError(codeLayoutInvalid, path+"/coverage_class_domain", errLayoutInvalid)
		}
	}
	if err := contextFailure(value.ctx, path+"/class_pairs/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[gposClassPairRecord](recordCount, path+"/class_pairs")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < recordCount; index++ {
		if err := value.checkContext(index, path+"/class_pairs"); err != nil {
			return nil, err
		}
		recordStart := start + 16 + recordSize*index
		value1, err := value.parseGPOSValueRecord(node, recordStart, start, format1, index*2, layoutPath(path+"/class_pairs", index)+"/value_1")
		if err != nil {
			return nil, err
		}
		value2, err := value.parseGPOSValueRecord(node, recordStart+size1, start, format2, index*2+1, layoutPath(path+"/class_pairs", index)+"/value_2")
		if err != nil {
			return nil, err
		}
		records[index] = gposClassPairRecord{value1: value1, value2: value2}
	}
	result := &gposPair{
		format: 2, coverage: coverage, valueFormat1: format1, valueFormat2: format2,
		classDef1: classDef1, classDef2: classDef2, class1Count: class1Count,
		class2Count: class2Count, classRecords: records,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
