package sfntglyf

func (value *layoutReader) parseGPOSMark2Array(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, classCount uint16, path string) (*gposMark2Array, error) {
	count, err := value.u16(start, path+"/mark_2_count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSMark2Array, layoutDecodeParams{first: uint64(classCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposMark2Array](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.mark2ArrayTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.mark2Records, cells, path+"/records"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.anchorReferences, cells, path+"/anchors"); err != nil {
		return nil, err
	}
	retained, ok := checkedMultiply(cells, 8)
	rows, rowsOK := checkedMultiply(uint64(count), 24)
	if ok && rowsOK {
		retained, ok = checkedAdd(retained, rows)
	} else {
		ok = false
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
			anchor, err := value.parseGPOSAnchorOffset(node, start, raw, layoutEdgeGPOSMark2Anchor, ordinal, false, layoutPath(path+"/anchors", flat))
			if err != nil {
				return nil, err
			}
			records[row][column] = anchor
		}
	}
	result := &gposMark2Array{records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader *layoutReader) validateGPOSMark2Array(value *gposMark2Array, coverage *gdefCoverage, classCount uint16, pointCounts []uint32, path string) error {
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
		validationPath := layoutPath(path+"/semantic_validation", uint64(row))
		if err := reader.meter.charge(&reader.meter.stats.orderedRecords, uint64(len(anchors)), reader.meter.limits.maxOrderedRecords, validationPath); err != nil {
			return err
		}
		for column, anchor := range anchors {
			if err := reader.checkContext(uint64(column), validationPath); err != nil {
				return err
			}
			if err := validateGPOSAnchorPoint(anchor, glyph, pointCounts, layoutPath(layoutPath(path+"/records", uint64(row))+"/anchors", uint64(column))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (value *layoutReader) parseGPOSMarkMark(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, path string) (*gposMarkMark, error) {
	if _, err := value.bytes(start, 12, path+"/header"); err != nil {
		return nil, err
	}
	if mustLayoutU16(value, start, path+"/format") != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	mark1CoverageOffset := mustLayoutU16(value, start+2, path+"/mark_1_coverage_offset")
	mark2CoverageOffset := mustLayoutU16(value, start+4, path+"/mark_2_coverage_offset")
	classCount := mustLayoutU16(value, start+6, path+"/class_count")
	mark1ArrayOffset := mustLayoutU16(value, start+8, path+"/mark_1_array_offset")
	mark2ArrayOffset := mustLayoutU16(value, start+10, path+"/mark_2_array_offset")
	if classCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/class_count", errLayoutInvalid)
	}
	node, alias, err := value.graph.begin(parent, edge, start, start+12, layoutSpanGPOSMarkMark, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(classCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposMarkMark](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(64, path); err != nil {
		return nil, err
	}
	mark1CoverageStart, err := value.childOffset(start, uint64(mark1CoverageOffset), path+"/mark_1_coverage_offset")
	if err != nil {
		return nil, err
	}
	mark1Coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSMarkCoverage, rawOffset: uint32(mark1CoverageOffset)}, mark1CoverageStart, numGlyphs, path+"/mark_1_coverage")
	if err != nil {
		return nil, err
	}
	mark2CoverageStart, err := value.childOffset(start, uint64(mark2CoverageOffset), path+"/mark_2_coverage_offset")
	if err != nil {
		return nil, err
	}
	mark2Coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSMark2Coverage, rawOffset: uint32(mark2CoverageOffset)}, mark2CoverageStart, numGlyphs, path+"/mark_2_coverage")
	if err != nil {
		return nil, err
	}
	mark1ArrayStart, err := value.childOffset(start, uint64(mark1ArrayOffset), path+"/mark_1_array_offset")
	if err != nil {
		return nil, err
	}
	marks1, err := value.parseGPOSMarkArray(node, layoutEdgeRef{role: layoutEdgeGPOSMarkArray, rawOffset: uint32(mark1ArrayOffset)}, mark1ArrayStart, path+"/mark_1_array")
	if err != nil {
		return nil, err
	}
	mark2ArrayStart, err := value.childOffset(start, uint64(mark2ArrayOffset), path+"/mark_2_array_offset")
	if err != nil {
		return nil, err
	}
	marks2, err := value.parseGPOSMark2Array(node, layoutEdgeRef{role: layoutEdgeGPOSMark2Array, rawOffset: uint32(mark2ArrayOffset)}, mark2ArrayStart, classCount, path+"/mark_2_array")
	if err != nil {
		return nil, err
	}
	if err := value.validateGPOSMarkArray(marks1, mark1Coverage, classCount, pointCounts, path+"/mark_1_array"); err != nil {
		return nil, err
	}
	if err := value.validateGPOSMark2Array(marks2, mark2Coverage, classCount, pointCounts, path+"/mark_2_array"); err != nil {
		return nil, err
	}
	result := &gposMarkMark{
		mark1Coverage: mark1Coverage, mark2Coverage: mark2Coverage,
		classCount: classCount, marks1: marks1, marks2: marks2,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
