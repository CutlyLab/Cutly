package sfntglyf

func (value *layoutReader) parseGPOSMarkArray(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*gposMarkArray, error) {
	count, err := value.u16(start, path+"/mark_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 4, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSMarkArray, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposMarkArray](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.markArrayTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.markRecords, uint64(count), path+"/records"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.anchorReferences, uint64(count), path+"/anchors"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*16+24, path+"/records"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/records/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[gposMarkRecord](uint64(count), path+"/records")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/records"); err != nil {
			return nil, err
		}
		recordStart := start + 2 + 4*index
		class := mustLayoutU16(value, recordStart, layoutPath(path+"/records", index)+"/class")
		raw := mustLayoutU16(value, recordStart+2, layoutPath(path+"/records", index)+"/anchor_offset")
		anchor, err := value.parseGPOSAnchorOffset(node, start, raw, layoutEdgeGPOSMarkAnchor, uint32(index), true, layoutPath(path+"/records", index)+"/anchor")
		if err != nil {
			return nil, err
		}
		records[index] = gposMarkRecord{class: class, anchor: anchor}
	}
	result := &gposMarkArray{records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader *layoutReader) validateGPOSMarkArray(value *gposMarkArray, coverage *gdefCoverage, classCount uint16, pointCounts []uint32, path string) error {
	if value == nil || coverage == nil || classCount == 0 || uint32(len(value.records)) != coverage.glyphCount {
		return fontError(codeLayoutInvalid, path+"/cardinality", errLayoutInvalid)
	}
	if err := reader.meter.charge(&reader.meter.stats.orderedRecords, uint64(len(value.records)), reader.meter.limits.maxOrderedRecords, path+"/semantic_validation"); err != nil {
		return err
	}
	for index, record := range value.records {
		if err := reader.checkContext(uint64(index), path+"/semantic_validation"); err != nil {
			return err
		}
		if record.class >= classCount || record.anchor == nil {
			return fontError(codeLayoutInvalid, layoutPath(path+"/records", uint64(index))+"/class", errLayoutInvalid)
		}
		glyph, ok := coverage.glyphAt(uint64(index))
		if !ok {
			return fontError(codeLayoutInvariant, path+"/coverage", ErrInvariantFailed)
		}
		if err := validateGPOSAnchorPoint(record.anchor, glyph, pointCounts, layoutPath(path+"/records", uint64(index))+"/anchor"); err != nil {
			return err
		}
	}
	return nil
}

func (value *layoutReader) parseGPOSBaseArray(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, classCount uint16, path string) (*gposBaseArray, error) {
	count, err := value.u16(start, path+"/base_count")
	if err != nil {
		return nil, err
	}
	cells, ok := checkedMultiply(uint64(count), uint64(classCount))
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/anchor_cells", ErrResourceLimit)
	}
	end, err := layoutSpanEnd(start, 2, cells, 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSBaseArray, layoutDecodeParams{first: uint64(classCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposBaseArray](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.baseArrayTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.baseRecords, cells, path+"/records"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.anchorReferences, cells, path+"/anchors"); err != nil {
		return nil, err
	}
	retained, ok := checkedMultiply(cells, 8)
	if ok {
		rows, rowsOK := checkedMultiply(uint64(count), 24)
		retained, ok = checkedAdd(retained, rows)
		ok = ok && rowsOK
	}
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/retained", ErrResourceLimit)
	}
	if err := value.meter.reserveRetained(retained+24, path+"/records"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/records/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[[]*gposAnchor](uint64(count), path+"/records")
	if err != nil {
		return nil, err
	}
	for row := uint64(0); row < uint64(count); row++ {
		records[row], err = layoutMakeSlice[*gposAnchor](uint64(classCount), layoutPath(path+"/records", row))
		if err != nil {
			return nil, err
		}
		for column := uint64(0); column < uint64(classCount); column++ {
			flat := row*uint64(classCount) + column
			if err := value.checkContext(flat, path+"/anchors"); err != nil {
				return nil, err
			}
			raw := mustLayoutU16(value, start+2+2*flat, layoutPath(path+"/anchors", flat)+"/offset")
			ordinal, err := gposFlattenOrdinal(row, uint64(classCount), column, path)
			if err != nil {
				return nil, err
			}
			anchor, err := value.parseGPOSAnchorOffset(node, start, raw, layoutEdgeGPOSBaseAnchor, ordinal, false, layoutPath(path+"/anchors", flat))
			if err != nil {
				return nil, err
			}
			records[row][column] = anchor
		}
	}
	result := &gposBaseArray{records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader *layoutReader) validateGPOSBaseArray(value *gposBaseArray, coverage *gdefCoverage, classCount uint16, pointCounts []uint32, path string) error {
	if value == nil || coverage == nil || uint32(len(value.records)) != coverage.glyphCount {
		return fontError(codeLayoutInvalid, path+"/cardinality", errLayoutInvalid)
	}
	for row, anchors := range value.records {
		if err := reader.checkContext(uint64(row), path+"/semantic_validation"); err != nil {
			return err
		}
		if len(anchors) != int(classCount) {
			return fontError(codeLayoutInvariant, layoutPath(path+"/records", uint64(row))+"/classes", ErrInvariantFailed)
		}
		glyph, ok := coverage.glyphAt(uint64(row))
		if !ok {
			return fontError(codeLayoutInvariant, path+"/coverage", ErrInvariantFailed)
		}
		if err := reader.meter.charge(&reader.meter.stats.orderedRecords, uint64(len(anchors)), reader.meter.limits.maxOrderedRecords, layoutPath(path+"/semantic_validation", uint64(row))); err != nil {
			return err
		}
		for column, anchor := range anchors {
			if err := reader.checkContext(uint64(column), layoutPath(path+"/semantic_validation", uint64(row))); err != nil {
				return err
			}
			if err := validateGPOSAnchorPoint(anchor, glyph, pointCounts, layoutPath(layoutPath(path+"/records", uint64(row))+"/anchor", uint64(column))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (value *layoutReader) parseGPOSMarkBase(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, path string) (*gposMarkBase, error) {
	if _, err := value.bytes(start, 12, path+"/header"); err != nil {
		return nil, err
	}
	if mustLayoutU16(value, start, path+"/format") != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	markCoverageOffset := mustLayoutU16(value, start+2, path+"/mark_coverage_offset")
	baseCoverageOffset := mustLayoutU16(value, start+4, path+"/base_coverage_offset")
	classCount := mustLayoutU16(value, start+6, path+"/class_count")
	markArrayOffset := mustLayoutU16(value, start+8, path+"/mark_array_offset")
	baseArrayOffset := mustLayoutU16(value, start+10, path+"/base_array_offset")
	if classCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/class_count", errLayoutInvalid)
	}
	params := layoutDecodeParams{first: uint64(numGlyphs), second: uint64(classCount)}
	node, alias, err := value.graph.begin(parent, edge, start, start+12, layoutSpanGPOSMarkBase, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposMarkBase](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(64, path); err != nil {
		return nil, err
	}
	markCoverageStart, err := value.childOffset(start, uint64(markCoverageOffset), path+"/mark_coverage_offset")
	if err != nil {
		return nil, err
	}
	markCoverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSMarkCoverage, rawOffset: uint32(markCoverageOffset)}, markCoverageStart, numGlyphs, path+"/mark_coverage")
	if err != nil {
		return nil, err
	}
	baseCoverageStart, err := value.childOffset(start, uint64(baseCoverageOffset), path+"/base_coverage_offset")
	if err != nil {
		return nil, err
	}
	baseCoverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSBaseCoverage, rawOffset: uint32(baseCoverageOffset)}, baseCoverageStart, numGlyphs, path+"/base_coverage")
	if err != nil {
		return nil, err
	}
	markArrayStart, err := value.childOffset(start, uint64(markArrayOffset), path+"/mark_array_offset")
	if err != nil {
		return nil, err
	}
	marks, err := value.parseGPOSMarkArray(node, layoutEdgeRef{role: layoutEdgeGPOSMarkArray, rawOffset: uint32(markArrayOffset)}, markArrayStart, path+"/mark_array")
	if err != nil {
		return nil, err
	}
	baseArrayStart, err := value.childOffset(start, uint64(baseArrayOffset), path+"/base_array_offset")
	if err != nil {
		return nil, err
	}
	bases, err := value.parseGPOSBaseArray(node, layoutEdgeRef{role: layoutEdgeGPOSBaseArray, rawOffset: uint32(baseArrayOffset)}, baseArrayStart, classCount, path+"/base_array")
	if err != nil {
		return nil, err
	}
	if err := value.validateGPOSMarkArray(marks, markCoverage, classCount, pointCounts, path+"/mark_array"); err != nil {
		return nil, err
	}
	if err := value.validateGPOSBaseArray(bases, baseCoverage, classCount, pointCounts, path+"/base_array"); err != nil {
		return nil, err
	}
	result := &gposMarkBase{markCoverage: markCoverage, baseCoverage: baseCoverage, classCount: classCount, marks: marks, bases: bases}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
