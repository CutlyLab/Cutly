package sfntglyf

import "context"

type gsubFoundationResources struct {
	workUnits     uint64
	workingBytes  uint64
	retainedBytes uint64
}

func buildGSUBFoundation(ctx context.Context, source *OutlineSet, limits gsubFoundationLimits) (
	result *gsubValidationFoundation, receipt gsubFoundationReceipt, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = gsubFoundationReceipt{}
			err = fontError(CodeAllocationFailed, "layout/gsub_foundation/build/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = gsubFoundationReceipt{}
		}
	}()
	if err = contextFailure(ctx, "layout/gsub_foundation/build/context"); err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	normalized, err := normalizeGSUBFoundationLimits(limits)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	parentLimits := normalized.LayoutFoundation
	if normalized.MaxWorkUnits < parentLimits.MaxWorkUnits {
		parentLimits.MaxWorkUnits = normalized.MaxWorkUnits
	}
	if normalized.MaxWorkingBytes < parentLimits.MaxWorkingBytes {
		parentLimits.MaxWorkingBytes = normalized.MaxWorkingBytes
	}
	if normalized.MaxRetainedBytes < parentLimits.MaxRetainedBytes {
		parentLimits.MaxRetainedBytes = normalized.MaxRetainedBytes
	}
	parent, parentReceipt, err := buildLayoutFoundation(ctx, source, parentLimits)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	if parent == nil || parent.source != source || parent.receipt != parentReceipt {
		return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/parent", ErrInvariantFailed)
	}

	// Rebind again so GDEF dependencies and GSUB bytes are proven from one live
	// OutlineSet -> Geometry -> Outline -> Font replay, rather than from caller
	// receipts or an operation-local view retained by the parent build.
	rebindLimits, err := admitLayoutFoundationSourceReceipt(source, parentLimits)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	rebound, err := rebindOutlineSetSource(ctx, source, rebindLimits)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	sourceBinding, tables, err := bindLayoutFoundationSource(source, rebound)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	if sourceBinding != parent.sourceBinding || tables != parent.tables {
		return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/live_binding", ErrInvariantFailed)
	}
	pointCounts, decodePointCounts, err := copyLayoutFoundationPointCounts(ctx, rebound, sourceBinding)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	if len(pointCounts) != len(parent.realPointCounts) {
		return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/point_counts", ErrInvariantFailed)
	}
	for index := range pointCounts {
		if pointCounts[index] != parent.realPointCounts[index] {
			return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/point_counts", ErrInvariantFailed)
		}
	}

	var liveGDEF *gdefFoundation
	if tables[0].present {
		gdefBytes, err := layoutFoundationDirectTableView(rebound.font, rebound.gdef)
		if err != nil {
			return nil, gsubFoundationReceipt{}, err
		}
		decodeLimits, err := layoutFoundationDecodeLimits(parentLimits, sourceBinding, tables)
		if err != nil {
			return nil, gsubFoundationReceipt{}, err
		}
		var gdefStats layoutDecodeStats
		liveGDEF, gdefStats, err = decodeGDEFFoundation(ctx, gdefBytes, sourceBinding.glyphCount, decodePointCounts, decodeLimits)
		if err != nil {
			return nil, gsubFoundationReceipt{}, err
		}
		parentGDEF, ok := parent.gdef.(*gdefFoundation)
		if !ok || parentGDEF == nil || liveGDEF == nil || parentGDEF.majorVersion != liveGDEF.majorVersion ||
			parentGDEF.minorVersion != liveGDEF.minorVersion {
			return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/gdef_binding", ErrInvariantFailed)
		}
		wantStats := layoutFoundationStatsFromReceipt(parent.receipt)
		wantStats.layoutTableBytes = uint64(tables[0].length)
		if gdefStats.foundationResourceStats() != wantStats {
			return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/gdef_stats", ErrInvariantFailed)
		}
	} else if parent.gdef != nil {
		return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/gdef_absence", ErrInvariantFailed)
	}

	var gsub *gsubFoundation
	var gsubBytes []byte
	var stats layoutDecodeStats
	if tables[1].present {
		if tables[1].length == 0 {
			return nil, gsubFoundationReceipt{}, fontError(codeLayoutInputInvalid, "layout/gsub_foundation/build/gsub/empty", errLayoutInputInvalid)
		}
		gsubBytes, err = layoutFoundationDirectTableView(rebound.font, rebound.gsub)
		if err != nil {
			return nil, gsubFoundationReceipt{}, err
		}
		decodeLimits, err := gsubFoundationDecodeLimits(normalized)
		if err != nil {
			return nil, gsubFoundationReceipt{}, err
		}
		gsub, stats, err = decodeGSUBFoundation(ctx, gsubBytes, sourceBinding.glyphCount, liveGDEF, decodeLimits)
		if err != nil {
			return nil, gsubFoundationReceipt{}, err
		}
		if gsub == nil {
			return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/gsub/model", ErrInvariantFailed)
		}
	}

	foundationHash, canonicalBytes, err := hashGSUBFoundation(ctx, parent, sourceBinding, tables,
		gsub, gsubBytes, stats, normalized.MaxCanonicalBinaryBytes)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	receipt = makeGSUBFoundationReceipt(parent, sourceBinding, tables, gsub, stats, foundationHash, canonicalBytes)
	resources, err := checkGSUBFoundationResources(parent.receipt, stats, receipt, normalized)
	if err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	receipt.WorkUnits = resources.workUnits
	receipt.WorkingBytes = resources.workingBytes
	receipt.RetainedBytes = resources.retainedBytes
	boundStats := stats
	boundStats.workUnits, boundStats.workingBytes, boundStats.retainedBytes = 0, 0, 0
	if gsubFoundationStatsFromReceipt(receipt) != boundStats {
		return nil, gsubFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gsub_foundation/build/receipt", ErrInvariantFailed)
	}
	if err = contextFailure(ctx, "layout/gsub_foundation/build/publish"); err != nil {
		return nil, gsubFoundationReceipt{}, err
	}
	result = &gsubValidationFoundation{parent: parent, gsub: gsub, receipt: receipt}
	return result, receipt, nil
}

func gsubFoundationDecodeLimits(limits gsubFoundationLimits) (layoutDecodeLimits, error) {
	base := limits.LayoutFoundation
	result := layoutDecodeLimits{
		maxTableBytes:         base.MaxLayoutTableBytes,
		maxNodes:              layoutFoundationMinimum(base.MaxNodes, hardLayoutMaxNodes),
		maxEdges:              layoutFoundationMinimum(base.MaxEdges, hardLayoutMaxEdges),
		maxAliasReferences:    layoutFoundationMinimum(base.MaxAliasReferences, hardLayoutMaxAliasReferences),
		maxStructuralDepth:    base.MaxStructuralDepth,
		maxCoverageTables:     base.MaxCoverageTables,
		maxCoverageGlyphs:     layoutFoundationMinimum(base.MaxCoveredGlyphs, hardLayoutMaxCoverageGlyphs),
		maxClassDefTables:     base.MaxClassDefTables,
		maxClassAssignments:   layoutFoundationMinimum(base.MaxClassAssignments, hardLayoutMaxClassAssignments),
		maxDeviceTables:       base.MaxDeviceTables,
		maxAttachmentPoints:   layoutFoundationMinimum(base.MaxAttachmentPoints, hardLayoutMaxAttachmentPoints),
		maxAttachPointTables:  base.MaxAttachPointTables,
		maxLigGlyphTables:     base.MaxLigGlyphTables,
		maxCarets:             layoutFoundationMinimum(base.MaxCaretValues, hardLayoutMaxCarets),
		maxMarkGlyphSets:      layoutFoundationMinimum(base.MaxMarkGlyphSets, hardLayoutMaxMarkGlyphSets),
		maxDeviceDeltas:       layoutFoundationMinimum(base.MaxDeviceDeltaValues, hardLayoutMaxDeviceDeltas),
		maxOrderedRecords:     limits.MaxOrderedRecords,
		maxSubstituteGlyphs:   limits.MaxSubstituteGlyphs,
		maxLigatureComponents: limits.MaxLigatureComponents,
		maxFeatureCharacters:  limits.MaxFeatureCharacters,
		maxContextGlyphs:      limits.MaxContextGlyphs,
		maxSequenceLookups:    limits.MaxSequenceLookups,
		maxLogicalLookupEdges: limits.MaxLogicalLookupEdges,
		maxLogicalLookupDepth: limits.MaxLogicalLookupDepth,
		maxLogicalExpansion:   limits.MaxLogicalExpansion,
		maxWorkUnits:          limits.MaxWorkUnits,
		maxWorkingBytes:       limits.MaxWorkingBytes,
		maxRetainedBytes:      limits.MaxRetainedBytes,
	}
	return normalizeLayoutDecodeLimits(result)
}

func checkGSUBFoundationResources(parent layoutFoundationReceipt, stats layoutDecodeStats,
	receipt gsubFoundationReceipt, limits gsubFoundationLimits) (gsubFoundationResources, error) {
	work, ok := checkedAdd(parent.WorkUnits, stats.workUnits)
	if ok {
		work, ok = checkedAdd(work, receipt.CanonicalBinaryBytes)
	}
	if !ok || work > limits.MaxWorkUnits {
		return gsubFoundationResources{}, fontError(codeLayoutResource, "layout/gsub_foundation/resources/work", ErrResourceLimit)
	}
	retained, ok := checkedAdd(parent.RetainedBytes, stats.retainedBytes)
	if ok {
		retained, ok = checkedAdd(retained, gsubFoundationFixedRetainedBytes)
	}
	if ok {
		retained, ok = checkedAdd(retained, gsubFoundationReceiptStringBytes(receipt))
	}
	if !ok || retained > limits.MaxRetainedBytes {
		return gsubFoundationResources{}, fontError(codeLayoutResource, "layout/gsub_foundation/resources/retained", ErrResourceLimit)
	}
	working, ok := checkedAdd(gsubFoundationFixedWorkingBytes, stats.workingBytes)
	if !ok {
		return gsubFoundationResources{}, fontError(codeLayoutResource, "layout/gsub_foundation/resources/working", ErrResourceLimit)
	}
	working = layoutFoundationMaximum(working, parent.WorkingBytes)
	if working > limits.MaxWorkingBytes {
		return gsubFoundationResources{}, fontError(codeLayoutResource, "layout/gsub_foundation/resources/working", ErrResourceLimit)
	}
	if receipt.CanonicalBinaryBytes > limits.MaxCanonicalBinaryBytes {
		return gsubFoundationResources{}, fontError(codeLayoutResource, "layout/gsub_foundation/resources/canonical", ErrResourceLimit)
	}
	return gsubFoundationResources{workUnits: work, workingBytes: working, retainedBytes: retained}, nil
}
