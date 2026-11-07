package sfntglyf

func (value *layoutReader) parseGPOSAnchor(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*gposAnchor, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	var size uint64
	switch format {
	case 1:
		size = 6
	case 2:
		size = 8
	case 3:
		size = 10
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	if _, err := value.bytes(start, size, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, start+size, layoutSpanGPOSAnchor, layoutDecodeParams{first: uint64(format)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposAnchor](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.anchorTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(48, path); err != nil {
		return nil, err
	}
	result := &gposAnchor{
		format: format,
		x:      int16(mustLayoutU16(value, start+2, path+"/x")),
		y:      int16(mustLayoutU16(value, start+4, path+"/y")),
	}
	if format == 2 {
		result.pointIndex = mustLayoutU16(value, start+6, path+"/point_index")
	}
	if format == 3 {
		for slot, field := range []struct {
			offset uint64
			name   string
			target **gdefDevice
		}{
			{start + 6, "x_device", &result.xDevice},
			{start + 8, "y_device", &result.yDevice},
		} {
			raw := mustLayoutU16(value, field.offset, path+"/"+field.name+"_offset")
			if raw == 0 {
				continue
			}
			childStart, err := value.childOffset(start, uint64(raw), path+"/"+field.name+"_offset")
			if err != nil {
				return nil, err
			}
			device, err := value.parseDevice(node, layoutEdgeRef{
				role: layoutEdgeGPOSAnchorDevice, ordinal: uint32(slot), rawOffset: uint32(raw),
			}, childStart, path+"/"+field.name)
			if err != nil {
				return nil, err
			}
			*field.target = device
		}
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func validateGPOSAnchorPoint(anchor *gposAnchor, glyph uint16, pointCounts []uint32, path string) error {
	if anchor == nil {
		return nil
	}
	if int(glyph) >= len(pointCounts) {
		return fontError(codeLayoutInvariant, path+"/glyph", ErrInvariantFailed)
	}
	if anchor.format == 2 && uint32(anchor.pointIndex) >= pointCounts[glyph] {
		return fontError(codeLayoutInvalid, path+"/point_index", errLayoutInvalid)
	}
	return nil
}

func (value *layoutReader) parseGPOSAnchorOffset(parent *layoutSpanNode, base uint64, raw uint16, role layoutEdgeRole, ordinal uint32, required bool, path string) (*gposAnchor, error) {
	if raw == 0 {
		if required {
			return nil, fontError(codeLayoutInvalid, path+"/offset", errLayoutInvalid)
		}
		return nil, nil
	}
	childStart, err := value.childOffset(base, uint64(raw), path+"/offset")
	if err != nil {
		return nil, err
	}
	return value.parseGPOSAnchor(parent, layoutEdgeRef{role: role, ordinal: ordinal, rawOffset: uint32(raw)}, childStart, path)
}

func gposFlattenOrdinal(row, width, column uint64, path string) (uint32, error) {
	base, ok := checkedMultiply(row, width)
	if ok {
		base, ok = checkedAdd(base, column)
	}
	if !ok || base > uint64(^uint32(0)) {
		return 0, fontError(codeLayoutResource, path+"/ordinal", ErrResourceLimit)
	}
	return uint32(base), nil
}

func (value *layoutReader) parseGPOSCursive(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, path string) (*gposCursive, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	if format != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+4, path+"/entry_exit_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 6, uint64(count), 4, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSCursive, layoutDecodeParams{first: uint64(numGlyphs), second: uint64(count)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposCursive](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	anchorSlots, ok := checkedMultiply(uint64(count), 2)
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/anchor_slots", ErrResourceLimit)
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.anchorReferences, anchorSlots, path+"/anchors"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*16+48, path+"/records"); err != nil {
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
	if err := contextFailure(value.ctx, path+"/records/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[gposEntryExitRecord](uint64(count), path+"/records")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/records"); err != nil {
			return nil, err
		}
		glyph, ok := coverage.glyphAt(index)
		if !ok {
			return nil, fontError(codeLayoutInvariant, path+"/coverage", ErrInvariantFailed)
		}
		recordStart := start + 6 + 4*index
		entryRaw := mustLayoutU16(value, recordStart, layoutPath(path+"/records", index)+"/entry_offset")
		exitRaw := mustLayoutU16(value, recordStart+2, layoutPath(path+"/records", index)+"/exit_offset")
		entry, err := value.parseGPOSAnchorOffset(node, start, entryRaw, layoutEdgeGPOSEntryAnchor, uint32(index), false, layoutPath(path+"/records", index)+"/entry")
		if err != nil {
			return nil, err
		}
		exit, err := value.parseGPOSAnchorOffset(node, start, exitRaw, layoutEdgeGPOSExitAnchor, uint32(index), false, layoutPath(path+"/records", index)+"/exit")
		if err != nil {
			return nil, err
		}
		if err := validateGPOSAnchorPoint(entry, glyph, pointCounts, layoutPath(path+"/records", index)+"/entry"); err != nil {
			return nil, err
		}
		if err := validateGPOSAnchorPoint(exit, glyph, pointCounts, layoutPath(path+"/records", index)+"/exit"); err != nil {
			return nil, err
		}
		records[index] = gposEntryExitRecord{entry: entry, exit: exit}
	}
	result := &gposCursive{coverage: coverage, records: records}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
