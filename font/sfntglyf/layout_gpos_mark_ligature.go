package sfntglyf

func (value *layoutReader) parseGPOSLigatureArray(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, classCount uint16, path string) (*gposLigatureArray, error) {
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSLigatureArray, layoutDecodeParams{first: uint64(classCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposLigatureArray](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.ligatureArrayTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.charge(&value.meter.stats.orderedRecords, uint64(count), value.meter.limits.maxOrderedRecords, path+"/attach_references"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*8+24, path+"/attaches"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/attaches/allocate"); err != nil {
		return nil, err
	}
	attaches, err := layoutMakeSlice[*gposLigatureAttach](uint64(count), path+"/attaches")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/attaches"); err != nil {
			return nil, err
		}
		raw := mustLayoutU16(value, start+2+2*index, layoutPath(path+"/attaches", index)+"/offset")
		childStart, err := value.childOffset(start, uint64(raw), layoutPath(path+"/attaches", index)+"/offset")
		if err != nil {
			return nil, err
		}
		attach, err := value.parseGPOSLigatureAttach(node, layoutEdgeRef{role: layoutEdgeGPOSLigatureAttach, ordinal: uint32(index), rawOffset: uint32(raw)}, childStart, classCount, layoutPath(path+"/attaches", index))
		if err != nil {
			return nil, err
		}
		attaches[index] = attach
	}
	result := &gposLigatureArray{attaches: attaches}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseGPOSLigatureAttach(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, classCount uint16, path string) (*gposLigatureAttach, error) {
	componentCount, err := value.u16(start, path+"/component_count")
	if err != nil {
		return nil, err
	}
	if componentCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/component_count", errLayoutInvalid)
	}
	cells, ok := checkedMultiply(uint64(componentCount), uint64(classCount))
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSLigatureAttach, layoutDecodeParams{first: uint64(classCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposLigatureAttach](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.ligatureAttachTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.ligatureComponentRecords, cells, path+"/components"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.anchorReferences, cells, path+"/anchors"); err != nil {
		return nil, err
	}
	retained, ok := checkedMultiply(cells, 8)
	rows, rowsOK := checkedMultiply(uint64(componentCount), 24)
	if ok && rowsOK {
		retained, ok = checkedAdd(retained, rows)
	} else {
		ok = false
	}
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/retained", ErrResourceLimit)
	}
	if err := value.meter.reserveRetained(retained+24, path+"/components"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/components/allocate"); err != nil {
		return nil, err
	}
	components, err := layoutMakeSlice[[]*gposAnchor](uint64(componentCount), path+"/components")
	if err != nil {
		return nil, err
	}
	for row := uint64(0); row < uint64(componentCount); row++ {
		components[row], err = layoutMakeSlice[*gposAnchor](uint64(classCount), layoutPath(path+"/components", row))
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
			anchor, err := value.parseGPOSAnchorOffset(node, start, raw, layoutEdgeGPOSLigatureAnchor, ordinal, false, layoutPath(path+"/anchors", flat))
			if err != nil {
				return nil, err
			}
			components[row][column] = anchor
		}
	}
	result := &gposLigatureAttach{components: components}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader *layoutReader) validateGPOSLigatureArray(value *gposLigatureArray, coverage *gdefCoverage, classCount uint16, pointCounts []uint32, path string) error {
	if value == nil || coverage == nil || uint32(len(value.attaches)) != coverage.glyphCount {
		return fontError(codeLayoutInvalid, path+"/cardinality", errLayoutInvalid)
	}
	for index, attach := range value.attaches {
		if err := reader.checkContext(uint64(index), path+"/semantic_validation"); err != nil {
			return err
		}
		if attach == nil || len(attach.components) == 0 {
			return fontError(codeLayoutInvalid, layoutPath(path+"/attaches", uint64(index)), errLayoutInvalid)
		}
		glyph, ok := coverage.glyphAt(uint64(index))
		if !ok {
			return fontError(codeLayoutInvariant, path+"/coverage", ErrInvariantFailed)
		}
		for component, anchors := range attach.components {
			if len(anchors) != int(classCount) {
				return fontError(codeLayoutInvariant, layoutPath(path+"/attaches", uint64(index))+"/classes", ErrInvariantFailed)
			}
			validationPath := layoutPath(layoutPath(path+"/semantic_validation", uint64(index)), uint64(component))
			if err := reader.meter.charge(&reader.meter.stats.orderedRecords, uint64(len(anchors)), reader.meter.limits.maxOrderedRecords, validationPath); err != nil {
				return err
			}
			for class, anchor := range anchors {
				if err := reader.checkContext(uint64(class), validationPath); err != nil {
					return err
				}
				anchorPath := layoutPath(layoutPath(layoutPath(path+"/attaches", uint64(index))+"/components", uint64(component))+"/anchors", uint64(class))
				if err := validateGPOSAnchorPoint(anchor, glyph, pointCounts, anchorPath); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (value *layoutReader) parseGPOSMarkLigature(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, path string) (*gposMarkLigature, error) {
	if _, err := value.bytes(start, 12, path+"/header"); err != nil {
		return nil, err
	}
	if mustLayoutU16(value, start, path+"/format") != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	markCoverageOffset := mustLayoutU16(value, start+2, path+"/mark_coverage_offset")
	ligatureCoverageOffset := mustLayoutU16(value, start+4, path+"/ligature_coverage_offset")
	classCount := mustLayoutU16(value, start+6, path+"/class_count")
	markArrayOffset := mustLayoutU16(value, start+8, path+"/mark_array_offset")
	ligatureArrayOffset := mustLayoutU16(value, start+10, path+"/ligature_array_offset")
	if classCount == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/class_count", errLayoutInvalid)
	}
	node, alias, err := value.graph.begin(parent, edge, start, start+12, layoutSpanGPOSMarkLigature, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(classCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposMarkLigature](node, path)
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
	ligatureCoverageStart, err := value.childOffset(start, uint64(ligatureCoverageOffset), path+"/ligature_coverage_offset")
	if err != nil {
		return nil, err
	}
	ligatureCoverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSLigatureCoverage, rawOffset: uint32(ligatureCoverageOffset)}, ligatureCoverageStart, numGlyphs, path+"/ligature_coverage")
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
	ligatureArrayStart, err := value.childOffset(start, uint64(ligatureArrayOffset), path+"/ligature_array_offset")
	if err != nil {
		return nil, err
	}
	ligatures, err := value.parseGPOSLigatureArray(node, layoutEdgeRef{role: layoutEdgeGPOSLigatureArray, rawOffset: uint32(ligatureArrayOffset)}, ligatureArrayStart, classCount, path+"/ligature_array")
	if err != nil {
		return nil, err
	}
	if err := value.validateGPOSMarkArray(marks, markCoverage, classCount, pointCounts, path+"/mark_array"); err != nil {
		return nil, err
	}
	if err := value.validateGPOSLigatureArray(ligatures, ligatureCoverage, classCount, pointCounts, path+"/ligature_array"); err != nil {
		return nil, err
	}
	result := &gposMarkLigature{
		markCoverage: markCoverage, ligatureCoverage: ligatureCoverage,
		classCount: classCount, marks: marks, ligatures: ligatures,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
