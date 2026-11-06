package sfntglyf

const layoutFoundationFixedWorkingBytes uint64 = 64 << 10
const layoutFoundationFixedRetainedBytes uint64 = 32 << 10

type layoutFoundationResourceStats struct {
	layoutTableBytes          uint64
	nodes                     uint64
	edges                     uint64
	aliasReferences           uint64
	maxStructuralDepth        uint32
	directBytes               uint64
	coverageTables            uint64
	coverageFormat1Glyphs     uint64
	coverageRanges            uint64
	coveredGlyphs             uint64
	classDefTables            uint64
	classFormat1Values        uint64
	classRanges               uint64
	classAssignments          uint64
	deviceTables              uint64
	deviceDeltaValues         uint64
	deviceWords               uint64
	attachLists               uint64
	attachPointTables         uint64
	attachmentGlyphReferences uint64
	attachmentPoints          uint64
	ligCaretLists             uint64
	ligGlyphTables            uint64
	ligatureGlyphReferences   uint64
	caretValueTables          uint64
	caretValueReferences      uint64
	caretPointReferences      uint64
	markGlyphSetsTables       uint64
	markGlyphSetReferences    uint64
}

type layoutFoundationResources struct {
	workUnits     uint64
	workingBytes  uint64
	retainedBytes uint64
}

func layoutFoundationStatsFromReceipt(value layoutFoundationReceipt) layoutFoundationResourceStats {
	return layoutFoundationResourceStats{
		layoutTableBytes: layoutFoundationReceiptLayoutTableBytes(value),
		nodes:            value.NodeCount, edges: value.EdgeCount, aliasReferences: value.AliasReferenceCount,
		maxStructuralDepth: value.MaxStructuralDepth, directBytes: value.DirectBytes,
		coverageTables: value.CoverageTableCount, coverageFormat1Glyphs: value.CoverageFormat1Glyphs,
		coverageRanges: value.CoverageRangeRecords, coveredGlyphs: value.CoveredGlyphInstances,
		classDefTables: value.ClassDefTableCount, classFormat1Values: value.ClassFormat1Values,
		classRanges: value.ClassRangeRecords, classAssignments: value.ClassAssignmentInstances,
		deviceTables: value.DeviceTableCount, deviceDeltaValues: value.DeviceDeltaValues,
		deviceWords: value.DeviceWordCount, attachLists: value.AttachListCount,
		attachPointTables: value.AttachPointTableCount, attachmentGlyphReferences: value.AttachmentGlyphReferences,
		attachmentPoints: value.AttachmentPointIndices, ligCaretLists: value.LigCaretListCount,
		ligGlyphTables: value.LigGlyphTableCount, ligatureGlyphReferences: value.LigatureGlyphReferences,
		caretValueTables: value.CaretValueTableCount, caretValueReferences: value.CaretValueReferences,
		caretPointReferences: value.CaretPointReferences, markGlyphSetsTables: value.MarkGlyphSetsTableCount,
		markGlyphSetReferences: value.MarkGlyphSetReferences,
	}
}

func layoutFoundationReceiptLayoutTableBytes(value layoutFoundationReceipt) uint64 {
	var total uint64
	for _, table := range value.Tables {
		if !table.Present {
			continue
		}
		next, ok := checkedAdd(total, uint64(table.Length))
		if !ok {
			return ^uint64(0)
		}
		total = next
	}
	return total
}

func layoutFoundationComputeResources(source layoutSourceBinding, stats layoutFoundationResourceStats,
	canonicalBytes uint64, receipt layoutFoundationReceipt) (layoutFoundationResources, bool) {
	work, ok := layoutFoundationWorkUnits(source, stats, canonicalBytes)
	if !ok {
		return layoutFoundationResources{}, false
	}
	working, ok := layoutFoundationWorkingBytes(source, stats)
	if !ok {
		return layoutFoundationResources{}, false
	}
	retained, ok := layoutFoundationRetainedBytes(source, stats, receipt)
	if !ok {
		return layoutFoundationResources{}, false
	}
	return layoutFoundationResources{workUnits: work, workingBytes: working, retainedBytes: retained}, true
}

func layoutFoundationWorkUnits(source layoutSourceBinding, stats layoutFoundationResourceStats,
	canonicalBytes uint64) (uint64, bool) {
	base, ok := checkedAdd(source.coreWorkUnits, source.outlineSetWorkUnits)
	if !ok {
		return 0, false
	}
	classRecords, ok := checkedAdd(stats.classFormat1Values, stats.classRanges)
	if !ok {
		return 0, false
	}
	return layoutFoundationWeightedSum(base,
		layoutFoundationTerm{stats.nodes, 64}, layoutFoundationTerm{stats.edges, 24},
		layoutFoundationTerm{stats.aliasReferences, 8}, layoutFoundationTerm{stats.directBytes, 1},
		layoutFoundationTerm{stats.coveredGlyphs, 4}, layoutFoundationTerm{classRecords, 8},
		layoutFoundationTerm{stats.deviceWords, 4}, layoutFoundationTerm{stats.attachmentGlyphReferences, 8},
		layoutFoundationTerm{stats.attachmentPoints, 4}, layoutFoundationTerm{stats.ligatureGlyphReferences, 8},
		layoutFoundationTerm{stats.caretValueReferences, 8}, layoutFoundationTerm{stats.markGlyphSetReferences, 8},
		layoutFoundationTerm{canonicalBytes, 1})
}

func layoutFoundationWorkingBytes(source layoutSourceBinding, stats layoutFoundationResourceStats) (uint64, bool) {
	ownModel, ok := layoutFoundationOwnModelBytes(uint64(source.glyphCount), stats)
	if !ok {
		return 0, false
	}
	layoutScratch, ok := checkedAdd(layoutFoundationFixedWorkingBytes, ownModel)
	if ok {
		// The own model remains live. Decoder graph maps/snapshots or canonical
		// projection+normalization then add the conservative phase scratch below.
		layoutScratch, ok = layoutFoundationWeightedSum(layoutScratch,
			layoutFoundationTerm{stats.nodes, 256}, layoutFoundationTerm{stats.edges, 192},
			layoutFoundationTerm{stats.aliasReferences, 16}, layoutFoundationTerm{uint64(source.glyphCount), 16})
	}
	if !ok {
		return 0, false
	}
	if source.coreWorkingBytes > layoutScratch {
		layoutScratch = source.coreWorkingBytes
	}
	if source.outlineSetWorkingBytes > layoutScratch {
		layoutScratch = source.outlineSetWorkingBytes
	}
	return layoutScratch, true
}

func layoutFoundationRetainedBytes(source layoutSourceBinding, stats layoutFoundationResourceStats,
	receipt layoutFoundationReceipt) (uint64, bool) {
	ownModel, ok := layoutFoundationOwnModelBytes(uint64(source.glyphCount), stats)
	if !ok {
		return 0, false
	}
	base, ok := checkedAdd(source.outlineSetRetainedBytes, ownModel)
	if !ok {
		return 0, false
	}
	return layoutFoundationWeightedSum(base,
		layoutFoundationTerm{layoutFoundationReceiptStringBytes(receipt), 1})
}

func layoutFoundationOwnModelBytes(glyphCount uint64, stats layoutFoundationResourceStats) (uint64, bool) {
	return layoutFoundationWeightedSum(layoutFoundationFixedRetainedBytes,
		layoutFoundationTerm{stats.nodes, 128}, layoutFoundationTerm{stats.edges, 48},
		layoutFoundationTerm{glyphCount, 8},
		layoutFoundationTerm{stats.coverageFormat1Glyphs, 8}, layoutFoundationTerm{stats.coverageRanges, 24},
		layoutFoundationTerm{stats.classFormat1Values, 8}, layoutFoundationTerm{stats.classRanges, 24},
		layoutFoundationTerm{stats.deviceWords, 4}, layoutFoundationTerm{stats.deviceDeltaValues, 2},
		layoutFoundationTerm{stats.attachmentGlyphReferences, 32}, layoutFoundationTerm{stats.attachmentPoints, 4},
		layoutFoundationTerm{stats.ligatureGlyphReferences, 32},
		layoutFoundationTerm{stats.caretValueReferences, 32}, layoutFoundationTerm{stats.markGlyphSetReferences, 8},
	)
}

func checkLayoutFoundationResources(source layoutSourceBinding, stats layoutFoundationResourceStats,
	canonicalBytes uint64, receipt layoutFoundationReceipt, limits layoutFoundationLimits) (layoutFoundationResources, error) {
	if stats.layoutTableBytes > limits.MaxLayoutTableBytes || stats.nodes > limits.MaxNodes || stats.edges > limits.MaxEdges ||
		stats.aliasReferences > limits.MaxAliasReferences || stats.maxStructuralDepth > limits.MaxStructuralDepth ||
		stats.coverageTables > limits.MaxCoverageTables || stats.coveredGlyphs > limits.MaxCoveredGlyphs ||
		stats.classDefTables > limits.MaxClassDefTables || stats.classAssignments > limits.MaxClassAssignments ||
		stats.deviceTables > limits.MaxDeviceTables || stats.deviceDeltaValues > limits.MaxDeviceDeltaValues ||
		stats.attachPointTables > limits.MaxAttachPointTables || stats.attachmentPoints > limits.MaxAttachmentPoints ||
		stats.ligGlyphTables > limits.MaxLigGlyphTables || stats.caretValueReferences > limits.MaxCaretValues ||
		stats.markGlyphSetReferences > limits.MaxMarkGlyphSets || canonicalBytes > limits.MaxCanonicalBinaryBytes {
		return layoutFoundationResources{}, fontError(CodeResourceLimit, "layout_foundation/resources/count", ErrResourceLimit)
	}
	resources, ok := layoutFoundationComputeResources(source, stats, canonicalBytes, receipt)
	if !ok {
		return layoutFoundationResources{}, fontError(CodeResourceLimit, "layout_foundation/resources/overflow", ErrResourceLimit)
	}
	if resources.workUnits > limits.MaxWorkUnits {
		return layoutFoundationResources{}, fontError(CodeResourceLimit, "layout_foundation/resources/work", ErrResourceLimit)
	}
	if resources.workingBytes > limits.MaxWorkingBytes {
		return layoutFoundationResources{}, fontError(CodeResourceLimit, "layout_foundation/resources/working", ErrResourceLimit)
	}
	if resources.retainedBytes > limits.MaxRetainedBytes {
		return layoutFoundationResources{}, fontError(CodeResourceLimit, "layout_foundation/resources/retained", ErrResourceLimit)
	}
	return resources, nil
}

type layoutFoundationTerm struct {
	count  uint64
	weight uint64
}

func layoutFoundationWeightedSum(base uint64, terms ...layoutFoundationTerm) (uint64, bool) {
	result := base
	for _, term := range terms {
		increment, ok := checkedMultiply(term.count, term.weight)
		if !ok {
			return 0, false
		}
		result, ok = checkedAdd(result, increment)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func layoutFoundationReceiptStringBytes(value layoutFoundationReceipt) uint64 {
	strings := []string{
		value.Profile, value.SourceProfile, value.SourceDescriptorHashProfile, value.SourceDescriptorHash,
		value.SourceOutlineSetHashProfile, value.SourceOutlineSetHash, value.UpstreamContentHashProfile,
		value.UpstreamContentHash, value.DescriptorHashProfile, value.DescriptorHash,
		value.FoundationHashProfile, value.FoundationHash, value.LayoutStatus, value.CapabilityStatus,
		value.VariationStatus, value.DeviceStatus, value.CaretOrderStatus, value.GDEFStatus,
		value.GSUBStatus, value.GPOSStatus,
	}
	var total uint64
	for _, text := range strings {
		next, ok := checkedAdd(total, uint64(len(text)))
		if !ok {
			return ^uint64(0)
		}
		total = next
	}
	for _, table := range value.Tables {
		for _, text := range []string{table.Tag, table.HashProfile, table.ContentHash} {
			next, ok := checkedAdd(total, uint64(len(text)))
			if !ok {
				return ^uint64(0)
			}
			total = next
		}
	}
	return total
}
