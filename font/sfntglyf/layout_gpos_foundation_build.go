package sfntglyf

import "context"

type gposFoundationResources struct {
	workUnits     uint64
	workingBytes  uint64
	retainedBytes uint64
}

func buildGPOSFoundation(ctx context.Context, source *OutlineSet, limits gposFoundationLimits) (
	result *gposValidationFoundation, receipt gposFoundationReceipt, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = gposFoundationReceipt{}
			err = fontError(CodeAllocationFailed, "layout/gpos_foundation/build/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = gposFoundationReceipt{}
		}
	}()
	if err = contextFailure(ctx, "layout/gpos_foundation/build/context"); err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	normalized, err := normalizeGPOSFoundationLimits(limits)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	parentLimits := normalized.GSUBFoundation
	if normalized.MaxWorkUnits < parentLimits.MaxWorkUnits {
		parentLimits.MaxWorkUnits = normalized.MaxWorkUnits
	}
	if normalized.MaxWorkingBytes < parentLimits.MaxWorkingBytes {
		parentLimits.MaxWorkingBytes = normalized.MaxWorkingBytes
	}
	if normalized.MaxRetainedBytes < parentLimits.MaxRetainedBytes {
		parentLimits.MaxRetainedBytes = normalized.MaxRetainedBytes
	}
	parent, parentReceipt, err := buildGSUBFoundation(ctx, source, parentLimits)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	if parent == nil || parent.parent == nil || parent.parent.source != source || parent.receipt != parentReceipt {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/parent", ErrInvariantFailed)
	}

	// This third live rebind is the atomic GDEF+GSUB+GPOS proof. Neither the
	// parent model nor a caller-provided receipt can substitute for live bytes.
	rebindLimits, err := admitLayoutFoundationSourceReceipt(source, parentLimits.LayoutFoundation)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	rebound, err := rebindOutlineSetSource(ctx, source, rebindLimits)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	sourceBinding, tables, err := bindLayoutFoundationSource(source, rebound)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	if sourceBinding != parent.parent.sourceBinding || tables != parent.parent.tables {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/live_binding", ErrInvariantFailed)
	}
	pointCounts, decodePointCounts, err := copyLayoutFoundationPointCounts(ctx, rebound, sourceBinding)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	if len(pointCounts) != len(parent.parent.realPointCounts) {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/point_counts", ErrInvariantFailed)
	}
	for index := range pointCounts {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/build/point_counts"); err != nil {
				return nil, gposFoundationReceipt{}, err
			}
		}
		if pointCounts[index] != parent.parent.realPointCounts[index] {
			return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/point_counts", ErrInvariantFailed)
		}
	}

	var liveGDEF *gdefFoundation
	if tables[0].present {
		gdefBytes, err := layoutFoundationDirectTableView(rebound.font, rebound.gdef)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		decodeLimits, err := layoutFoundationDecodeLimits(parentLimits.LayoutFoundation, sourceBinding, tables)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		var gdefStats layoutDecodeStats
		liveGDEF, gdefStats, err = decodeGDEFFoundation(ctx, gdefBytes, sourceBinding.glyphCount, decodePointCounts, decodeLimits)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		parentGDEF, ok := parent.parent.gdef.(*gdefFoundation)
		if !ok || parentGDEF == nil || liveGDEF == nil || parentGDEF.majorVersion != liveGDEF.majorVersion ||
			parentGDEF.minorVersion != liveGDEF.minorVersion {
			return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/gdef_binding", ErrInvariantFailed)
		}
		wantStats := layoutFoundationStatsFromReceipt(parent.parent.receipt)
		wantStats.layoutTableBytes = uint64(tables[0].length)
		if gdefStats.foundationResourceStats() != wantStats {
			return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/gdef_stats", ErrInvariantFailed)
		}
	} else if parent.parent.gdef != nil {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/gdef_absence", ErrInvariantFailed)
	}

	var liveGSUB *gsubFoundation
	var gsubBytes []byte
	var gsubStats layoutDecodeStats
	if tables[1].present {
		if tables[1].length == 0 {
			return nil, gposFoundationReceipt{}, fontError(codeLayoutInputInvalid, "layout/gpos_foundation/build/gsub/empty", errLayoutInputInvalid)
		}
		gsubBytes, err = layoutFoundationDirectTableView(rebound.font, rebound.gsub)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		decodeLimits, err := gsubFoundationDecodeLimits(parentLimits)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		liveGSUB, gsubStats, err = decodeGSUBFoundation(ctx, gsubBytes, sourceBinding.glyphCount, liveGDEF, decodeLimits)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
	} else if parent.gsub != nil {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/gsub_absence", ErrInvariantFailed)
	}
	liveGSUBHash, liveGSUBCanonicalBytes, err := hashGSUBFoundation(ctx, parent.parent, sourceBinding, tables,
		liveGSUB, gsubBytes, gsubStats, parentLimits.MaxCanonicalBinaryBytes)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	if liveGSUBHash != parent.receipt.FoundationHash || liveGSUBCanonicalBytes != parent.receipt.CanonicalBinaryBytes {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/gsub_canonical_binding", ErrInvariantFailed)
	}

	var gpos *gposFoundation
	var gposBytes []byte
	var stats layoutDecodeStats
	receiptModel := (*gposFoundation)(nil)
	if tables[2].present {
		// Only the presence-dependent status string matters for this admission
		// receipt. Version and decoder counts carry no retained strings.
		receiptModel = &gposFoundation{}
	}
	admissionReceipt := makeGPOSFoundationReceipt(parent, sourceBinding, tables, receiptModel,
		layoutDecodeStats{}, layoutFoundationDigestPlaceholder, 0)
	retainedBase, ok := checkedAdd(parent.receipt.RetainedBytes, gposFoundationFixedRetainedBytes)
	if ok {
		retainedBase, ok = checkedAdd(retainedBase, gposFoundationReceiptStringBytes(admissionReceipt))
	}
	if !ok || retainedBase > normalized.MaxRetainedBytes {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/retained", ErrResourceLimit)
	}
	if tables[2].present {
		if tables[2].length == 0 {
			return nil, gposFoundationReceipt{}, fontError(codeLayoutInputInvalid, "layout/gpos_foundation/build/gpos/empty", errLayoutInputInvalid)
		}
		gposBytes, err = layoutFoundationDirectTableView(rebound.font, rebound.gpos)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		decodeLimits, err := gposFoundationDecodeLimits(normalized, parent.receipt, retainedBase)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		gpos, stats, err = decodeGPOSFoundation(ctx, gposBytes, sourceBinding.glyphCount, decodePointCounts, liveGDEF, decodeLimits)
		if err != nil {
			return nil, gposFoundationReceipt{}, err
		}
		if gpos == nil {
			return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/gpos/model", ErrInvariantFailed)
		}
	}

	workBase, ok := checkedAdd(parent.receipt.WorkUnits, stats.workUnits)
	if !ok || workBase >= normalized.MaxWorkUnits {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/work", ErrResourceLimit)
	}
	canonicalLimit := normalized.MaxWorkUnits - workBase
	if canonicalLimit > normalized.MaxCanonicalBinaryBytes {
		canonicalLimit = normalized.MaxCanonicalBinaryBytes
	}
	foundationHash, canonicalBytes, err := hashGPOSFoundation(ctx, parent, sourceBinding, tables,
		gpos, gposBytes, stats, canonicalLimit)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	receipt = makeGPOSFoundationReceipt(parent, sourceBinding, tables, gpos, stats, foundationHash, canonicalBytes)
	resources, err := checkGPOSFoundationResources(parent.receipt, stats, receipt, normalized)
	if err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	receipt.WorkUnits = resources.workUnits
	receipt.WorkingBytes = resources.workingBytes
	receipt.RetainedBytes = resources.retainedBytes
	boundStats := stats
	boundStats.workUnits, boundStats.workingBytes, boundStats.retainedBytes = 0, 0, 0
	if gposStatsFromReceipt(receipt.Stats) != boundStats {
		return nil, gposFoundationReceipt{}, fontError(codeLayoutInvariant, "layout/gpos_foundation/build/receipt", ErrInvariantFailed)
	}
	if err = contextFailure(ctx, "layout/gpos_foundation/build/publish"); err != nil {
		return nil, gposFoundationReceipt{}, err
	}
	result = &gposValidationFoundation{parent: parent, gpos: gpos, receipt: receipt}
	return result, receipt, nil
}

func gposFoundationDecodeLimits(limits gposFoundationLimits, parent gsubFoundationReceipt, retainedBase uint64) (layoutDecodeLimits, error) {
	result, err := gsubFoundationDecodeLimits(limits.GSUBFoundation)
	if err != nil {
		return layoutDecodeLimits{}, err
	}
	if parent.WorkUnits >= limits.MaxWorkUnits {
		return layoutDecodeLimits{}, fontError(codeLayoutResource, "layout/gpos_foundation/decode/work", ErrResourceLimit)
	}
	if limits.MaxWorkingBytes <= gposFoundationFixedWorkingBytes {
		return layoutDecodeLimits{}, fontError(codeLayoutResource, "layout/gpos_foundation/decode/working", ErrResourceLimit)
	}
	if retainedBase >= limits.MaxRetainedBytes {
		return layoutDecodeLimits{}, fontError(codeLayoutResource, "layout/gpos_foundation/decode/retained", ErrResourceLimit)
	}
	// Bound the decoder against the remaining whole-operation budget before
	// any GPOS-proportional allocation. Canonical bytes are reserved later by
	// constraining the streaming hash sink to the remaining work budget.
	result.maxWorkUnits = limits.MaxWorkUnits - parent.WorkUnits
	result.maxWorkingBytes = limits.MaxWorkingBytes - gposFoundationFixedWorkingBytes
	result.maxRetainedBytes = limits.MaxRetainedBytes - retainedBase
	return normalizeLayoutDecodeLimits(result)
}

func checkGPOSFoundationResources(parent gsubFoundationReceipt, stats layoutDecodeStats,
	receipt gposFoundationReceipt, limits gposFoundationLimits) (gposFoundationResources, error) {
	work, ok := checkedAdd(parent.WorkUnits, stats.workUnits)
	if ok {
		work, ok = checkedAdd(work, receipt.CanonicalBinaryBytes)
	}
	if !ok || work > limits.MaxWorkUnits {
		return gposFoundationResources{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/work", ErrResourceLimit)
	}
	retained, ok := checkedAdd(parent.RetainedBytes, stats.retainedBytes)
	if ok {
		retained, ok = checkedAdd(retained, gposFoundationFixedRetainedBytes)
	}
	if ok {
		retained, ok = checkedAdd(retained, gposFoundationReceiptStringBytes(receipt))
	}
	if !ok || retained > limits.MaxRetainedBytes {
		return gposFoundationResources{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/retained", ErrResourceLimit)
	}
	working, ok := checkedAdd(gposFoundationFixedWorkingBytes, stats.workingBytes)
	if !ok {
		return gposFoundationResources{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/working", ErrResourceLimit)
	}
	working = layoutFoundationMaximum(working, parent.WorkingBytes)
	if working > limits.MaxWorkingBytes {
		return gposFoundationResources{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/working", ErrResourceLimit)
	}
	if receipt.CanonicalBinaryBytes > limits.MaxCanonicalBinaryBytes {
		return gposFoundationResources{}, fontError(codeLayoutResource, "layout/gpos_foundation/resources/canonical", ErrResourceLimit)
	}
	return gposFoundationResources{workUnits: work, workingBytes: working, retainedBytes: retained}, nil
}
