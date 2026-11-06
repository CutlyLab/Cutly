package sfntglyf

import "context"

// decodeGPOSFoundation validates a static GPOS table into a private immutable
// model. A successful decode is evidence only; it is not a shaping capability.
func decodeGPOSFoundation(ctx context.Context, gpos []byte, numGlyphs uint32, pointCounts []uint32, gdef *gdefFoundation, limits layoutDecodeLimits) (foundation *gposFoundation, stats layoutDecodeStats, err error) {
	defer func() {
		if recover() != nil {
			foundation = nil
			stats = layoutDecodeStats{}
			err = fontError(CodeAllocationFailed, "layout/gpos", ErrAllocationFailed)
		}
		if err != nil {
			foundation = nil
			stats = layoutDecodeStats{}
		}
	}()
	return decodeGPOSFoundationInner(ctx, gpos, numGlyphs, pointCounts, gdef, limits)
}

func decodeGPOSFoundationInner(ctx context.Context, gpos []byte, numGlyphs uint32, pointCounts []uint32, gdef *gdefFoundation, limits layoutDecodeLimits) (*gposFoundation, layoutDecodeStats, error) {
	if err := contextFailure(ctx, "layout/gpos/context"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	normalized, err := normalizeLayoutDecodeLimits(limits)
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if numGlyphs > 0xffff || uint64(numGlyphs) > uint64(maxInt()) || uint64(len(gpos)) > uint64(^uint32(0)) || len(pointCounts) != int(numGlyphs) {
		return nil, layoutDecodeStats{}, fontError(codeLayoutInputInvalid, "layout/gpos/input", errLayoutInputInvalid)
	}
	if uint64(len(gpos)) > normalized.maxTableBytes {
		return nil, layoutDecodeStats{}, fontError(codeLayoutResource, "layout/gpos/input/table_bytes", ErrResourceLimit)
	}
	if len(gpos) == 0 {
		return &gposFoundation{}, layoutDecodeStats{}, nil
	}
	meter := newLayoutResourceMeterForTable(uint64(len(gpos)), uint64(numGlyphs), normalized, layoutGPOSTag)
	reader := newLayoutReaderForTable(ctx, gpos, meter, layoutGPOSTag)
	major, err := reader.u16(0, "layout/gpos/major_version")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	minor, err := reader.u16(2, "layout/gpos/minor_version")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if major != 1 || (minor != 0 && minor != 1) {
		return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gpos/version", errLayoutUnsupported)
	}
	headerSize := uint64(10)
	if minor == 1 {
		headerSize = 14
	}
	if _, err := reader.bytes(0, headerSize, "layout/gpos/header"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if minor == 1 {
		featureVariations, _ := reader.u32(10, "layout/gpos/feature_variations_offset")
		if featureVariations != 0 {
			return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gpos/feature_variations", errLayoutUnsupported)
		}
	}
	header, alias, err := reader.graph.begin(nil, layoutEdgeRef{}, 0, headerSize, layoutSpanGPOSHeader, layoutDecodeParams{first: uint64(major), second: uint64(minor)}, "layout/gpos/header")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if alias {
		return nil, layoutDecodeStats{}, fontError(codeLayoutInvariant, "layout/gpos/header/alias", ErrInvariantFailed)
	}
	if err := meter.reserveRetained(128, "layout/gpos/foundation"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	scriptOffset, _ := reader.u16(4, "layout/gpos/script_list_offset")
	featureOffset, _ := reader.u16(6, "layout/gpos/feature_list_offset")
	lookupOffset, _ := reader.u16(8, "layout/gpos/lookup_list_offset")
	lookupStart, err := reader.childOffset(0, uint64(lookupOffset), "layout/gpos/lookup_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	lookups, err := reader.parseGPOSLookupList(header, layoutEdgeRef{role: layoutEdgeGPOSLookupList, rawOffset: uint32(lookupOffset)}, lookupStart, numGlyphs, pointCounts, gdef, "layout/gpos/lookup_list")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	featureStart, err := reader.childOffset(0, uint64(featureOffset), "layout/gpos/feature_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	features, err := reader.parseFeatureList(header, layoutEdgeRef{role: layoutEdgeGPOSFeatureList, rawOffset: uint32(featureOffset)}, featureStart, uint16(len(lookups)), "layout/gpos/feature_list")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	scriptStart, err := reader.childOffset(0, uint64(scriptOffset), "layout/gpos/script_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	scripts, err := reader.parseScriptList(header, layoutEdgeRef{role: layoutEdgeGPOSScriptList, rawOffset: uint32(scriptOffset)}, scriptStart, uint16(len(features)), "layout/gpos/script_list")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	logical, err := buildGPOSLogicalLookupGraph(ctx, lookups, meter)
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	result := &gposFoundation{majorVersion: major, minorVersion: minor, scripts: scripts, features: features, lookups: lookups, logical: logical}
	if err := reader.graph.complete(header, result, "layout/gpos/header"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	nodes, edges, err := reader.graph.validateAndSnapshot("layout/gpos/spans")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	result.nodes, result.edges = nodes, edges
	if err := contextFailure(ctx, "layout/gpos/publish"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	return result, meter.stats, nil
}

func (value *layoutReader) parseGPOSLookupList(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, gdef *gdefFoundation, path string) ([]*gposLookup, error) {
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
		return layoutValueAs[[]*gposLookup](node, path)
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
	lookups, err := layoutMakeSlice[*gposLookup](uint64(count), path+"/lookups")
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
		lookup, err := value.parseGPOSLookup(node, layoutEdgeRef{role: layoutEdgeLookup, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, numGlyphs, count, pointCounts, gdef, layoutPath(path+"/lookups", index))
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

func (value *layoutReader) parseGPOSLookup(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, pointCounts []uint32, gdef *gdefFoundation, path string) (*gposLookup, error) {
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
	if lookupType < 1 || lookupType > 9 || count == 0 || flags&0x00e0 != 0 {
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
	if err := validateGPOSLookupFlags(flags, hasFilteringSet, start+6+2*uint64(count), value, gdef, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanLookup, layoutDecodeParams{first: uint64(lookupType), second: uint64(flags)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposLookup](node, path)
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
	result := &gposLookup{lookupType: lookupType, flags: flags}
	if hasFilteringSet {
		set, _ := value.u16(start+6+2*uint64(count), path+"/mark_filtering_set")
		result.markFilteringSet = &set
	}
	if err := contextFailure(value.ctx, path+"/subtables/allocate"); err != nil {
		return nil, err
	}
	result.subtables, err = layoutMakeSlice[gposSubtable](uint64(count), path+"/subtables")
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
		subtable, err := value.parseGPOSSubtable(node, layoutEdgeRef{role: layoutEdgeLookupSubtable, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, lookupType, numGlyphs, lookupCount, pointCounts, layoutPath(path+"/subtables", index))
		if err != nil {
			return nil, err
		}
		if lookupType == 9 && index != 0 {
			first, firstOK := result.subtables[0].(*gposExtension)
			current, currentOK := subtable.(*gposExtension)
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

func validateGPOSLookupFlags(flags uint16, hasFilteringSet bool, filteringSetOffset uint64, reader *layoutReader, gdef *gdefFoundation, path string) error {
	if err := validateGSUBLookupFlags(flags, hasFilteringSet, filteringSetOffset, reader, gdef, path); err != nil {
		return err
	}
	markClass := uint16(flags >> 8)
	if markClass != 0 {
		defined, err := reader.layoutClassDefContains(gdef.markAttachClasses, markClass, path+"/mark_attachment_class")
		if err != nil {
			return err
		}
		if !defined {
			return fontError(codeLayoutInvalid, path+"/mark_attachment_class", errLayoutInvalid)
		}
	}
	return nil
}

func (reader *layoutReader) layoutClassDefContains(value *gdefClassDef, class uint16, path string) (bool, error) {
	if value == nil {
		return false, nil
	}
	semanticRecords, ok := checkedAdd(uint64(len(value.values)), uint64(len(value.ranges)))
	if !ok {
		return false, fontError(codeLayoutResource, path+"/semantic_validation", ErrResourceLimit)
	}
	// GDEF ClassDefs may be aliased by every lookup. Charge each semantic
	// binding pass so a small physical table cannot induce unbounded repeated
	// scans through lookup flags.
	if err := reader.meter.charge(&reader.meter.stats.orderedRecords, semanticRecords,
		reader.meter.limits.maxOrderedRecords, path+"/semantic_validation"); err != nil {
		return false, err
	}
	for index, candidate := range value.values {
		if err := reader.checkContext(uint64(index), path+"/semantic_validation"); err != nil {
			return false, err
		}
		if candidate == class {
			return true, nil
		}
	}
	for index, candidate := range value.ranges {
		if err := reader.checkContext(uint64(index), path+"/semantic_validation"); err != nil {
			return false, err
		}
		if candidate.class == class {
			return true, nil
		}
	}
	return false, nil
}

func (value *layoutReader) parseGPOSSubtable(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, lookupType uint16, numGlyphs uint32, lookupCount uint16, pointCounts []uint32, path string) (gposSubtable, error) {
	switch lookupType {
	case 1:
		return value.parseGPOSSingle(parent, edge, start, numGlyphs, path)
	case 2:
		return value.parseGPOSPair(parent, edge, start, numGlyphs, path)
	case 3:
		return value.parseGPOSCursive(parent, edge, start, numGlyphs, pointCounts, path)
	case 4:
		return value.parseGPOSMarkBase(parent, edge, start, numGlyphs, pointCounts, path)
	case 5:
		return value.parseGPOSMarkLigature(parent, edge, start, numGlyphs, pointCounts, path)
	case 6:
		return value.parseGPOSMarkMark(parent, edge, start, numGlyphs, pointCounts, path)
	case 7:
		contextValue, err := value.parseGSUBContext(parent, edge, start, numGlyphs, lookupCount, path)
		if err != nil {
			return nil, err
		}
		return &gposContext{context: contextValue}, nil
	case 8:
		contextValue, err := value.parseGSUBChainedContext(parent, edge, start, numGlyphs, lookupCount, path)
		if err != nil {
			return nil, err
		}
		return &gposChainedContext{context: contextValue}, nil
	case 9:
		return value.parseGPOSExtension(parent, edge, start, numGlyphs, lookupCount, pointCounts, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/lookup_type", errLayoutInvalid)
	}
}

func (value *layoutReader) parseGPOSExtension(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, lookupCount uint16, pointCounts []uint32, path string) (*gposExtension, error) {
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
	if format != 1 || extensionType < 1 || extensionType > 9 || extensionType == 9 {
		return nil, fontError(codeLayoutInvalid, path+"/header", errLayoutInvalid)
	}
	if _, err := value.bytes(start, 8, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(numGlyphs), second: uint64(lookupCount)<<16 | uint64(extensionType)}
	node, alias, err := value.graph.begin(parent, edge, start, start+8, layoutSpanGPOSExtension, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposExtension](node, path)
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
	subtable, err := value.parseGPOSSubtable(node, layoutEdgeRef{role: layoutEdgeGPOSExtension, rawOffset: extensionOffset}, childStart, extensionType, numGlyphs, lookupCount, pointCounts, path+"/extension")
	if err != nil {
		return nil, err
	}
	if subtable == nil || subtable.gposLookupType() != extensionType {
		return nil, fontError(codeLayoutInvariant, path+"/extension_binding", ErrInvariantFailed)
	}
	result := &gposExtension{extensionLookupType: extensionType, subtable: subtable}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
