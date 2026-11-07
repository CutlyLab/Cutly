package sfntglyf

import "context"

// decodeGSUBFoundation is a private additive validation stage. It intentionally
// does not upgrade the public Font status or expose a shaping API.
func decodeGSUBFoundation(ctx context.Context, gsub []byte, numGlyphs uint32, gdef *gdefFoundation, limits layoutDecodeLimits) (foundation *gsubFoundation, stats layoutDecodeStats, err error) {
	defer func() {
		if recover() != nil {
			foundation = nil
			stats = layoutDecodeStats{}
			err = fontError(CodeAllocationFailed, "layout/gsub", ErrAllocationFailed)
		}
		if err != nil {
			foundation = nil
			stats = layoutDecodeStats{}
		}
	}()
	return decodeGSUBFoundationInner(ctx, gsub, numGlyphs, gdef, limits)
}

func decodeGSUBFoundationInner(ctx context.Context, gsub []byte, numGlyphs uint32, gdef *gdefFoundation, limits layoutDecodeLimits) (*gsubFoundation, layoutDecodeStats, error) {
	if err := contextFailure(ctx, "layout/gsub/context"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	normalized, err := normalizeLayoutDecodeLimits(limits)
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if numGlyphs > 0xffff || uint64(numGlyphs) > uint64(maxInt()) || uint64(len(gsub)) > uint64(^uint32(0)) {
		return nil, layoutDecodeStats{}, fontError(codeLayoutInputInvalid, "layout/gsub/input", errLayoutInputInvalid)
	}
	if uint64(len(gsub)) > normalized.maxTableBytes {
		return nil, layoutDecodeStats{}, fontError(codeLayoutResource, "layout/gsub/input/table_bytes", ErrResourceLimit)
	}
	if len(gsub) == 0 {
		return &gsubFoundation{}, layoutDecodeStats{}, nil
	}
	meter := newLayoutResourceMeterForTable(uint64(len(gsub)), uint64(numGlyphs), normalized, layoutGSUBTag)
	reader := newLayoutReaderForTable(ctx, gsub, meter, layoutGSUBTag)
	major, err := reader.u16(0, "layout/gsub/major_version")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	minor, err := reader.u16(2, "layout/gsub/minor_version")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if major != 1 || (minor != 0 && minor != 1) {
		return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gsub/version", errLayoutUnsupported)
	}
	headerSize := uint64(10)
	if minor == 1 {
		headerSize = 14
	}
	if _, err := reader.bytes(0, headerSize, "layout/gsub/header"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if minor == 1 {
		featureVariations, _ := reader.u32(10, "layout/gsub/feature_variations_offset")
		if featureVariations != 0 {
			return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gsub/feature_variations", errLayoutUnsupported)
		}
	}
	header, alias, err := reader.graph.begin(nil, layoutEdgeRef{}, 0, headerSize, layoutSpanGSUBHeader, layoutDecodeParams{first: uint64(major), second: uint64(minor)}, "layout/gsub/header")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if alias {
		return nil, layoutDecodeStats{}, fontError(codeLayoutInvariant, "layout/gsub/header/alias", ErrInvariantFailed)
	}
	if err := meter.reserveRetained(128, "layout/gsub/foundation"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	scriptOffset, _ := reader.u16(4, "layout/gsub/script_list_offset")
	featureOffset, _ := reader.u16(6, "layout/gsub/feature_list_offset")
	lookupOffset, _ := reader.u16(8, "layout/gsub/lookup_list_offset")
	lookupStart, err := reader.childOffset(0, uint64(lookupOffset), "layout/gsub/lookup_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	lookups, err := reader.parseGSUBLookupList(header, layoutEdgeRef{role: layoutEdgeGSUBLookupList, rawOffset: uint32(lookupOffset)}, lookupStart, numGlyphs, gdef, "layout/gsub/lookup_list")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	featureStart, err := reader.childOffset(0, uint64(featureOffset), "layout/gsub/feature_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	features, err := reader.parseFeatureList(header, layoutEdgeRef{role: layoutEdgeGSUBFeatureList, rawOffset: uint32(featureOffset)}, featureStart, uint16(len(lookups)), "layout/gsub/feature_list")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	scriptStart, err := reader.childOffset(0, uint64(scriptOffset), "layout/gsub/script_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	scripts, err := reader.parseScriptList(header, layoutEdgeRef{role: layoutEdgeGSUBScriptList, rawOffset: uint32(scriptOffset)}, scriptStart, uint16(len(features)), "layout/gsub/script_list")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	logical, err := buildGSUBLogicalLookupGraph(ctx, lookups, meter)
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	result := &gsubFoundation{majorVersion: major, minorVersion: minor, scripts: scripts, features: features, lookups: lookups, logical: logical}
	if err := reader.graph.complete(header, result, "layout/gsub/header"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	nodes, edges, err := reader.graph.validateAndSnapshot("layout/gsub/spans")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	result.nodes, result.edges = nodes, edges
	if err := contextFailure(ctx, "layout/gsub/publish"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	return result, meter.stats, nil
}

func (value *layoutReader) parseGSUBLookupList(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, gdef *gdefFoundation, path string) ([]*gsubLookup, error) {
	count, err := value.u16(start, path+"/count")
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
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanLookupList, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[[]*gsubLookup](node, path)
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.lookupReferences, uint64(count), path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*8, path+"/lookups"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/lookups/allocate"); err != nil {
		return nil, err
	}
	lookups, err := layoutMakeSlice[*gsubLookup](uint64(count), path+"/lookups")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/lookups"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+2+2*index, path+"/offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/offsets", index))
		if err != nil {
			return nil, err
		}
		lookup, err := value.parseGSUBLookup(node, layoutEdgeRef{role: layoutEdgeLookup, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, count, gdef, layoutPath(path+"/lookups", index))
		if err != nil {
			return nil, err
		}
		lookups[index] = lookup
	}
	if err := value.graph.complete(node, lookups, path); err != nil {
		return nil, err
	}
	return lookups, nil
}

func (value *layoutReader) parseGSUBLookup(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, gdef *gdefFoundation, path string) (*gsubLookup, error) {
	lookupType, err := value.u16(start, path+"/type")
	if err != nil {
		return nil, err
	}
	flags, err := value.u16(start+2, path+"/flags")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+4, path+"/subtable_count")
	if err != nil {
		return nil, err
	}
	if lookupType < 1 || lookupType > 8 || count == 0 || flags&0x00e0 != 0 {
		return nil, fontError(codeLayoutInvalid, path+"/header", errLayoutInvalid)
	}
	hasFilteringSet := flags&0x0010 != 0
	fixed := uint64(6)
	if hasFilteringSet {
		fixed += 2
	}
	end, err := layoutSpanEnd(start, fixed, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	if err := validateGSUBLookupFlags(flags, hasFilteringSet, start+6+2*uint64(count), value, gdef, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanLookup, layoutDecodeParams{first: uint64(lookupType), second: uint64(flags)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gsubLookup](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.lookupTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.subtableReferences, uint64(count), path+"/subtables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(48+uint64(count)*16, path); err != nil {
		return nil, err
	}
	result := &gsubLookup{lookupType: lookupType, flags: flags}
	if hasFilteringSet {
		set, _ := value.u16(start+6+2*uint64(count), path+"/mark_filtering_set")
		result.markFilteringSet = &set
	}
	if err := contextFailure(value.ctx, path+"/subtables/allocate"); err != nil {
		return nil, err
	}
	result.subtables, err = layoutMakeSlice[gsubSubtable](uint64(count), path+"/subtables")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/subtables"); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+6+2*index, path+"/subtable_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/subtable_offsets", index))
		if err != nil {
			return nil, err
		}
		subtable, err := value.parseGSUBSubtable(node, layoutEdgeRef{role: layoutEdgeLookupSubtable, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, lookupType, numGlyphs, lookupCount, layoutPath(path+"/subtables", index))
		if err != nil {
			return nil, err
		}
		if lookupType == 7 && index != 0 {
			first, firstOK := result.subtables[0].(*gsubExtension)
			current, currentOK := subtable.(*gsubExtension)
			if !firstOK || !currentOK || first.extensionLookupType != current.extensionLookupType {
				return nil, fontError(codeLayoutInvalid, path+"/extension_lookup_type", errLayoutInvalid)
			}
		}
		result.subtables[index] = subtable
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func validateGSUBLookupFlags(flags uint16, hasFilteringSet bool, filteringSetOffset uint64, reader *layoutReader, gdef *gdefFoundation, path string) error {
	if flags&(0x0002|0x0004|0x0008) != 0 && (gdef == nil || gdef.glyphClasses == nil) {
		return fontError(codeLayoutInvalid, path+"/glyph_class_def", errLayoutInvalid)
	}
	if flags&0xff00 != 0 && (gdef == nil || gdef.markAttachClasses == nil) {
		return fontError(codeLayoutInvalid, path+"/mark_attach_class_def", errLayoutInvalid)
	}
	if hasFilteringSet {
		set, err := reader.u16(filteringSetOffset, path+"/mark_filtering_set")
		if err != nil {
			return err
		}
		if gdef == nil || int(set) >= len(gdef.markGlyphSets) {
			return fontError(codeLayoutInvalid, path+"/mark_filtering_set", errLayoutInvalid)
		}
	}
	return nil
}
