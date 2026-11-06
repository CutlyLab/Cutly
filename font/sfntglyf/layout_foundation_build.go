package sfntglyf

import (
	"context"
	"math"
)

const layoutFoundationDigestPlaceholder = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// buildLayoutFoundation is the private, atomic GDEF foundation gate. It does
// not publish a shaping capability: GSUB and GPOS remain opaque, content-bound
// inputs until their complete graph contract is implemented.
func buildLayoutFoundation(ctx context.Context, source *OutlineSet, limits layoutFoundationLimits) (
	result *layoutFoundation, receipt layoutFoundationReceipt, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = layoutFoundationReceipt{}
			err = fontError(CodeAllocationFailed, "layout_foundation/build/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = layoutFoundationReceipt{}
		}
	}()
	if err = contextFailure(ctx, "layout_foundation/build/context"); err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	normalized, err := normalizeLayoutFoundationLimits(limits)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	rebindLimits, err := admitLayoutFoundationSourceReceipt(source, normalized)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	rebound, err := rebindOutlineSetSource(ctx, source, rebindLimits)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	sourceBinding, tables, err := bindLayoutFoundationSource(source, rebound)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}

	tableBytesTotal, err := layoutFoundationBoundTableBytes(tables)
	if err != nil || tableBytesTotal > normalized.MaxLayoutTableBytes {
		if err == nil {
			err = fontError(CodeResourceLimit, "layout_foundation/build/tables/bytes", ErrResourceLimit)
		}
		return nil, layoutFoundationReceipt{}, err
	}
	var admissionGDEF *gdefFoundation
	if tables[0].present {
		if tables[0].length == 0 {
			return nil, layoutFoundationReceipt{}, layoutFoundationBuildInput("layout_foundation/build/gdef/empty")
		}
		admissionGDEF = &gdefFoundation{}
	}
	admissionStats := layoutFoundationResourceStats{layoutTableBytes: tableBytesTotal}
	admissionCanonical, ok := layoutFoundationCanonicalBytes(sourceBinding, tables, admissionStats)
	if !ok {
		return nil, layoutFoundationReceipt{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/canonical", ErrResourceLimit)
	}
	admissionReceipt := makeLayoutFoundationReceipt(sourceBinding, tables, admissionGDEF, admissionStats,
		layoutFoundationDigestPlaceholder, admissionCanonical)
	if _, err = checkLayoutFoundationResources(sourceBinding, admissionStats, admissionCanonical,
		admissionReceipt, normalized); err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	if err = contextFailure(ctx, "layout_foundation/build/points/admit"); err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	pointCounts, decodePointCounts, err := copyLayoutFoundationPointCounts(ctx, rebound, sourceBinding)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}

	var gdef *gdefFoundation
	var decodeStats layoutDecodeStats
	var gdefBytes []byte
	if tables[0].present {
		gdefBytes, err = layoutFoundationDirectTableView(rebound.font, rebound.gdef)
		if err != nil {
			return nil, layoutFoundationReceipt{}, err
		}
		decodeLimits, decodeLimitErr := layoutFoundationDecodeLimits(normalized, sourceBinding, tables)
		if decodeLimitErr != nil {
			return nil, layoutFoundationReceipt{}, decodeLimitErr
		}
		gdef, decodeStats, err = decodeGDEFFoundation(ctx, gdefBytes, sourceBinding.glyphCount, decodePointCounts, decodeLimits)
		if err != nil {
			return nil, layoutFoundationReceipt{}, err
		}
		if gdef == nil {
			return nil, layoutFoundationReceipt{}, layoutFoundationHashInvariant("layout_foundation/build/gdef/model")
		}
	}

	stats := decodeStats.foundationResourceStats()
	stats.layoutTableBytes = tableBytesTotal
	expectedCanonicalBytes, ok := layoutFoundationCanonicalBytes(sourceBinding, tables, stats)
	if !ok {
		return nil, layoutFoundationReceipt{}, fontError(CodeResourceLimit, "layout_foundation/build/canonical/size", ErrResourceLimit)
	}
	provisional := makeLayoutFoundationReceipt(sourceBinding, tables, gdef, stats,
		layoutFoundationDigestPlaceholder, expectedCanonicalBytes)
	// Exact canonical size is known from parser stats. Gate every final resource
	// before the canonical projection allocates its first node or edge.
	if _, err = checkLayoutFoundationResources(sourceBinding, stats, expectedCanonicalBytes, provisional, normalized); err != nil {
		return nil, layoutFoundationReceipt{}, err
	}

	canonical, err := layoutFoundationCanonicalEvidenceFromGDEF(ctx, sourceBinding, tables, gdef, gdefBytes, stats)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	foundationHash, canonicalBytes, err := hashLayoutFoundation(ctx, canonical, expectedCanonicalBytes)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	if canonicalBytes != expectedCanonicalBytes {
		return nil, layoutFoundationReceipt{}, layoutFoundationHashInvariant("layout_foundation/build/canonical/binding")
	}
	receipt = makeLayoutFoundationReceipt(sourceBinding, tables, gdef, stats, foundationHash, canonicalBytes)
	resources, err := checkLayoutFoundationResources(sourceBinding, stats, canonicalBytes, receipt, normalized)
	if err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	receipt.WorkUnits = resources.workUnits
	receipt.WorkingBytes = resources.workingBytes
	receipt.RetainedBytes = resources.retainedBytes
	if layoutFoundationStatsFromReceipt(receipt) != stats {
		return nil, layoutFoundationReceipt{}, layoutFoundationHashInvariant("layout_foundation/build/receipt/stats")
	}

	if err = contextFailure(ctx, "layout_foundation/build/publish"); err != nil {
		return nil, layoutFoundationReceipt{}, err
	}
	var retainedGDEF layoutFoundationGDEFModel
	if gdef != nil {
		retainedGDEF = gdef
	}
	result = &layoutFoundation{
		source: source, sourceBinding: sourceBinding, tables: tables,
		realPointCounts: pointCounts, gdef: retainedGDEF, receipt: receipt,
	}
	return result, receipt, nil
}

// admitLayoutFoundationSourceReceipt performs only scalar/pointer/receipt
// checks. Untrusted receipt values may reject here but never admit the source;
// the subsequent live rebind replays every byte and private graph.
func admitLayoutFoundationSourceReceipt(source *OutlineSet, limits layoutFoundationLimits) (OutlineSetLimits, error) {
	if source == nil || source.source == nil || source.source.source == nil || source.source.source.font == nil {
		return OutlineSetLimits{}, layoutFoundationBuildInput("layout_foundation/build/source")
	}
	setReceipt := source.receipt
	fontReceipt := source.source.source.font.receipt
	if setReceipt.GlyphCount == 0 || setReceipt.GlyphCount != fontReceipt.NumGlyphs ||
		uint64(setReceipt.GlyphCount) > uint64(maxInt()) {
		return OutlineSetLimits{}, layoutFoundationBuildInput("layout_foundation/build/source/receipt")
	}

	coreAndSetWork, ok := checkedAdd(fontReceipt.WorkUnits, setReceipt.WorkUnits)
	if !ok || coreAndSetWork > limits.MaxWorkUnits {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/work", ErrResourceLimit)
	}
	coreWorking, ok := layoutFoundationReceiptCoreWorking(fontReceipt)
	if !ok {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/core_working", ErrResourceLimit)
	}
	workingFixed, ok := checkedAdd(layoutFoundationFixedWorkingBytes, layoutFoundationFixedRetainedBytes)
	if !ok {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/working", ErrResourceLimit)
	}
	workingLower, ok := layoutFoundationWeightedSum(workingFixed,
		layoutFoundationTerm{uint64(setReceipt.GlyphCount), 24})
	if !ok {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/working", ErrResourceLimit)
	}
	if coreWorking > workingLower {
		workingLower = coreWorking
	}
	if setReceipt.WorkingBytes > workingLower {
		workingLower = setReceipt.WorkingBytes
	}
	if workingLower > limits.MaxWorkingBytes {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/working", ErrResourceLimit)
	}
	retainedBase, ok := checkedAdd(setReceipt.RetainedBytes, layoutFoundationFixedRetainedBytes)
	if !ok {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/retained", ErrResourceLimit)
	}
	receiptSkeleton := layoutFoundationAdmissionReceipt(source)
	retainedLower, ok := layoutFoundationWeightedSum(retainedBase,
		layoutFoundationTerm{uint64(setReceipt.GlyphCount), 8},
		layoutFoundationTerm{layoutFoundationReceiptStringBytes(receiptSkeleton), 1})
	if !ok || retainedLower > limits.MaxRetainedBytes {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/retained", ErrResourceLimit)
	}

	var layoutBytes uint64
	for _, table := range fontReceipt.Tables {
		if table.Tag != "GDEF" && table.Tag != "GSUB" && table.Tag != "GPOS" {
			continue
		}
		layoutBytes, ok = checkedAdd(layoutBytes, uint64(table.Length))
		if !ok {
			return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/table_bytes", ErrResourceLimit)
		}
	}
	if layoutBytes > limits.MaxLayoutTableBytes {
		return OutlineSetLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/admission/table_bytes", ErrResourceLimit)
	}
	rebindWorking := coreWorking
	if setReceipt.WorkingBytes > rebindWorking {
		rebindWorking = setReceipt.WorkingBytes
	}
	return strictLayoutFoundationRebindLimits(setReceipt, coreAndSetWork, rebindWorking), nil
}

func layoutFoundationAdmissionReceipt(source *OutlineSet) layoutFoundationReceipt {
	setReceipt := source.receipt
	fontReceipt := source.source.source.font.receipt
	result := layoutFoundationReceipt{
		Profile:                     layoutFoundationReceiptProfileV1,
		SourceProfile:               setReceipt.Profile,
		SourceDescriptorHashProfile: setReceipt.DescriptorHashProfile,
		SourceDescriptorHash:        setReceipt.DescriptorHash,
		SourceOutlineSetHashProfile: setReceipt.OutlineSetHashProfile,
		SourceOutlineSetHash:        setReceipt.OutlineSetHash,
		UpstreamContentHashProfile:  fontReceipt.ContentHashProfile,
		UpstreamContentHash:         fontReceipt.ContentHash,
		DescriptorHashProfile:       layoutFoundationDescriptorHashV1,
		DescriptorHash:              layoutFoundationDescriptorIdentityV1,
		FoundationHashProfile:       layoutFoundationCanonicalHashV1,
		FoundationHash:              layoutFoundationDigestPlaceholder,
		LayoutStatus:                layoutFoundationLayoutStatusV1,
		CapabilityStatus:            layoutFoundationCapabilityStatusV1,
		VariationStatus:             layoutFoundationVariationStatusV1,
		DeviceStatus:                layoutFoundationDeviceStatusV1,
		CaretOrderStatus:            layoutFoundationCaretOrderStatusV1,
		GDEFStatus:                  layoutFoundationGDEFAbsentStatusV1,
		GSUBStatus:                  layoutFoundationGSUBTableStatusV1,
		GPOSStatus:                  layoutFoundationGPOSTableStatusV1,
	}
	for index, tag := range layoutFoundationTableTags {
		result.Tables[index].Tag = string(tag[:])
		for _, table := range fontReceipt.Tables {
			if table.Tag != result.Tables[index].Tag {
				continue
			}
			result.Tables[index].Present = true
			result.Tables[index].HashProfile = TableHashProfileV1
			result.Tables[index].ContentHash = table.ContentHash
			if index == 0 {
				result.GDEFStatus = layoutFoundationGDEFValidatedStatusV1
			}
			break
		}
	}
	return result
}

func layoutFoundationReceiptCoreWorking(receipt Receipt) (uint64, bool) {
	container, ok := layoutFoundationWeightedSum(logicalFixedWorkingBytes,
		layoutFoundationTerm{uint64(receipt.TableCount), 2 * logicalTableBytes})
	if !ok {
		return 0, false
	}
	cmap, ok := layoutFoundationWeightedSum(receipt.WorkingBytes,
		layoutFoundationTerm{uint64(receipt.TableCount), logicalTableBytes},
		layoutFoundationTerm{receipt.CmapMappingCount, logicalMappingBytes})
	if !ok {
		return 0, false
	}
	if cmap > container {
		return cmap, true
	}
	return container, true
}

func strictLayoutFoundationRebindLimits(receipt OutlineSetReceipt, work, working uint64) OutlineSetLimits {
	result := OutlineSetLimits{
		MaxExpandedPointInstances:          layoutFoundationNonzeroLimit(receipt.ExpandedPointInstances),
		MaxExpandedContourInstances:        layoutFoundationNonzeroLimit(receipt.ExpandedContourInstances),
		MaxExpandedEdgeVisits:              layoutFoundationNonzeroLimit(receipt.ExpandedEdgeVisits),
		MaxExpandedGlyphInstances:          layoutFoundationNonzeroLimit(receipt.ExpandedGlyphInstances),
		MaxTransformPointVisits:            layoutFoundationNonzeroLimit(receipt.TransformPointVisits),
		MaxImpliedPointInstances:           layoutFoundationNonzeroLimit(receipt.ImpliedPointInstances),
		MaxDrawingCommandInstances:         layoutFoundationNonzeroLimit(receipt.DrawingCommandInstances),
		MaxCommandInstances:                layoutFoundationNonzeroLimit(receipt.CommandInstances),
		MaxCommandPointOccurrences:         layoutFoundationNonzeroLimit(receipt.CommandPointOccurrences),
		MaxCommandsPerGlyph:                layoutFoundationNonzeroLimit(receipt.MaxCommandsPerGlyph),
		MaxCommandPointOccurrencesPerGlyph: layoutFoundationNonzeroLimit(receipt.MaxCommandPointOccurrencesPerGlyph),
		MaxExactBits:                       receipt.ExactBitsLimit,
		MaxExactOps:                        layoutFoundationNonzeroLimit(receipt.ExactOpCount),
		MaxExactLimbUnits:                  layoutFoundationNonzeroLimit(receipt.ExactLimbUnits),
		MaxCanonicalBinaryBytes:            layoutFoundationNonzeroLimit(receipt.CanonicalBinaryBytes),
		MaxWorkUnits:                       work,
		MaxWorkingBytes:                    working,
		MaxRetainedBytes:                   receipt.RetainedBytes,
	}
	return result
}

func layoutFoundationNonzeroLimit(value uint64) uint64 {
	if value == 0 {
		return 1
	}
	return value
}

func layoutFoundationMinimum(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func copyLayoutFoundationPointCounts(ctx context.Context, rebound outlineSetRebindEvidence,
	source layoutSourceBinding) ([]uint64, []uint32, error) {
	if source.glyphCount == 0 || uint64(source.glyphCount) > uint64(maxInt()) {
		return nil, nil, layoutFoundationBuildInput("layout_foundation/build/points/count")
	}
	if err := contextFailure(ctx, "layout_foundation/build/points/counts/allocate"); err != nil {
		return nil, nil, err
	}
	counts := make([]uint64, int(source.glyphCount))
	if err := contextFailure(ctx, "layout_foundation/build/points/decode_counts/allocate"); err != nil {
		return nil, nil, err
	}
	decodeCounts := make([]uint32, int(source.glyphCount))
	var total uint64
	for glyphID := uint32(0); glyphID < source.glyphCount; glyphID++ {
		if uint64(glyphID)%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/build/points/copy"); err != nil {
				return nil, nil, err
			}
		}
		count, ok := rebound.expandedRealPointCount(glyphID)
		if !ok {
			return nil, nil, layoutFoundationBuildInput("layout_foundation/build/points/source")
		}
		counts[glyphID] = count
		if count > math.MaxUint32 {
			return nil, nil, fontError(CodeResourceLimit, "layout_foundation/build/points/u32", ErrResourceLimit)
		}
		decodeCounts[glyphID] = uint32(count)
		total, ok = checkedAdd(total, count)
		if !ok {
			return nil, nil, fontError(CodeResourceLimit, "layout_foundation/build/points/total", ErrResourceLimit)
		}
	}
	if total != source.realPointInstances {
		return nil, nil, layoutFoundationBuildInput("layout_foundation/build/points/binding")
	}
	return counts, decodeCounts, nil
}

func layoutFoundationBoundTableBytes(tables [3]layoutTableBinding) (uint64, error) {
	var total uint64
	for index, table := range tables {
		if table.tag != layoutFoundationTableTags[index] {
			return 0, layoutFoundationHashInvariant("layout_foundation/build/tables/order")
		}
		if !table.present {
			continue
		}
		var ok bool
		total, ok = checkedAdd(total, uint64(table.length))
		if !ok {
			return 0, fontError(CodeResourceLimit, "layout_foundation/build/tables/bytes", ErrResourceLimit)
		}
	}
	return total, nil
}

func layoutFoundationDirectTableView(font *Font, table outlineSetRebindTableEvidence) ([]byte, error) {
	if font == nil || !table.present || table.record.length == 0 {
		return nil, layoutFoundationBuildInput("layout_foundation/build/table/view")
	}
	end, ok := checkedAdd(uint64(table.record.offset), uint64(table.record.length))
	if !ok || end > uint64(len(font.encoded)) || uint64(table.record.offset) > uint64(maxInt()) || end > uint64(maxInt()) {
		return nil, layoutFoundationBuildInput("layout_foundation/build/table/bounds")
	}
	return tableBytes(font.encoded, table.record), nil
}

func layoutFoundationDecodeLimits(limits layoutFoundationLimits, source layoutSourceBinding,
	tables [3]layoutTableBinding) (layoutDecodeLimits, error) {
	sourceWork, ok := checkedAdd(source.coreWorkUnits, source.outlineSetWorkUnits)
	canonicalMinimum, canonicalOK := layoutFoundationCanonicalMinimumBytes(source, tables)
	reservedWork, reserveOK := checkedAdd(sourceWork, canonicalMinimum)
	if !ok || !canonicalOK || !reserveOK || reservedWork >= limits.MaxWorkUnits {
		return layoutDecodeLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/decode/work", ErrResourceLimit)
	}
	ownedPointBytes, ok := checkedMultiply(uint64(source.glyphCount), 8)
	if !ok {
		return layoutDecodeLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/decode/retained", ErrResourceLimit)
	}
	receiptSkeleton := makeLayoutFoundationReceipt(source, tables, &gdefFoundation{},
		layoutFoundationResourceStats{}, layoutFoundationDigestPlaceholder, 0)
	retainedBase, ok := checkedAdd(source.outlineSetRetainedBytes, layoutFoundationFixedRetainedBytes)
	if ok {
		retainedBase, ok = checkedAdd(retainedBase, ownedPointBytes)
	}
	if ok {
		retainedBase, ok = checkedAdd(retainedBase, layoutFoundationReceiptStringBytes(receiptSkeleton))
	}
	if !ok || retainedBase >= limits.MaxRetainedBytes {
		return layoutDecodeLimits{}, fontError(CodeResourceLimit, "layout_foundation/build/decode/retained", ErrResourceLimit)
	}
	result := layoutDecodeLimits{
		maxTableBytes:        limits.MaxLayoutTableBytes,
		maxNodes:             layoutFoundationMinimum(limits.MaxNodes, hardLayoutMaxNodes),
		maxEdges:             layoutFoundationMinimum(limits.MaxEdges, hardLayoutMaxEdges),
		maxAliasReferences:   layoutFoundationMinimum(limits.MaxAliasReferences, hardLayoutMaxAliasReferences),
		maxStructuralDepth:   limits.MaxStructuralDepth,
		maxCoverageTables:    limits.MaxCoverageTables,
		maxCoverageGlyphs:    layoutFoundationMinimum(limits.MaxCoveredGlyphs, hardLayoutMaxCoverageGlyphs),
		maxClassDefTables:    limits.MaxClassDefTables,
		maxClassAssignments:  layoutFoundationMinimum(limits.MaxClassAssignments, hardLayoutMaxClassAssignments),
		maxDeviceTables:      limits.MaxDeviceTables,
		maxAttachmentPoints:  layoutFoundationMinimum(limits.MaxAttachmentPoints, hardLayoutMaxAttachmentPoints),
		maxAttachPointTables: limits.MaxAttachPointTables,
		maxLigGlyphTables:    limits.MaxLigGlyphTables,
		maxCarets:            layoutFoundationMinimum(limits.MaxCaretValues, hardLayoutMaxCarets),
		maxMarkGlyphSets:     layoutFoundationMinimum(limits.MaxMarkGlyphSets, hardLayoutMaxMarkGlyphSets),
		maxDeviceDeltas:      layoutFoundationMinimum(limits.MaxDeviceDeltaValues, hardLayoutMaxDeviceDeltas),
		maxWorkUnits:         hardLayoutMaxWorkUnits,
		maxWorkingBytes:      layoutFoundationMinimum(limits.MaxWorkingBytes, hardLayoutMaxWorkingBytes),
		maxRetainedBytes:     hardLayoutMaxRetainedBytes,
		foundationWorkBase:   reservedWork, foundationWorkLimit: limits.MaxWorkUnits,
		foundationWorkingSource: layoutFoundationMaximum(source.coreWorkingBytes, source.outlineSetWorkingBytes),
		foundationWorkingLimit:  limits.MaxWorkingBytes,
		foundationRetainedBase:  retainedBase, foundationRetainedLimit: limits.MaxRetainedBytes,
	}
	return normalizeLayoutDecodeLimits(result)
}

func layoutFoundationDecodeWork(base uint64, value layoutDecodeStats) (uint64, bool) {
	classRecords, ok := checkedAdd(value.classFormat1Values, value.classRanges)
	if !ok {
		return 0, false
	}
	// base contains source replay plus the fixed canonical header/footer. A
	// node adds 64 graph units and 42 canonical bytes; an edge adds 24+62;
	// direct payload is visited once by each side.
	return layoutFoundationWeightedSum(base,
		layoutFoundationTerm{value.nodes, 106}, layoutFoundationTerm{value.edges, 86},
		layoutFoundationTerm{value.aliasReferences, 8}, layoutFoundationTerm{value.directBytes, 2},
		layoutFoundationTerm{value.coverageGlyphs, 4}, layoutFoundationTerm{classRecords, 8},
		layoutFoundationTerm{value.deviceWords, 4},
		layoutFoundationTerm{value.attachmentGlyphReferences, 8},
		layoutFoundationTerm{value.attachmentPoints, 4},
		layoutFoundationTerm{value.ligatureGlyphReferences, 8},
		layoutFoundationTerm{value.caretValueReferences, 8},
		layoutFoundationTerm{value.markGlyphSetReferences, 8})
}

func layoutFoundationDecodeRetained(base uint64, value layoutDecodeStats) (uint64, bool) {
	return layoutFoundationWeightedSum(base,
		layoutFoundationTerm{value.nodes, 128}, layoutFoundationTerm{value.edges, 48},
		layoutFoundationTerm{value.coverageFormat1Glyphs, 8}, layoutFoundationTerm{value.coverageRanges, 24},
		layoutFoundationTerm{value.classFormat1Values, 8}, layoutFoundationTerm{value.classRanges, 24},
		layoutFoundationTerm{value.deviceWords, 4}, layoutFoundationTerm{value.deviceDeltas, 2},
		layoutFoundationTerm{value.attachmentGlyphReferences, 32},
		layoutFoundationTerm{value.attachmentPoints, 4},
		layoutFoundationTerm{value.ligatureGlyphReferences, 32},
		layoutFoundationTerm{value.caretValueReferences, 32},
		layoutFoundationTerm{value.markGlyphSetReferences, 8})
}

func layoutFoundationDecodeWorking(sourceWorking, glyphCount uint64, value layoutDecodeStats) (uint64, bool) {
	stats := value.foundationResourceStats()
	ownModel, ok := layoutFoundationOwnModelBytes(glyphCount, stats)
	if !ok {
		return 0, false
	}
	scratch, ok := checkedAdd(layoutFoundationFixedWorkingBytes, ownModel)
	if ok {
		scratch, ok = layoutFoundationWeightedSum(scratch,
			layoutFoundationTerm{stats.nodes, 256}, layoutFoundationTerm{stats.edges, 192},
			layoutFoundationTerm{stats.aliasReferences, 16}, layoutFoundationTerm{glyphCount, 16})
	}
	if !ok {
		return 0, false
	}
	return layoutFoundationMaximum(sourceWorking, scratch), true
}

func layoutFoundationMaximum(left, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}

// layoutFoundationCanonicalMinimumBytes is the exact canonical byte count for
// the bound scalar header, empty node/edge arrays and the resource footer. Any
// decoded GDEF graph can only add node/edge records and direct-span payload.
func layoutFoundationCanonicalMinimumBytes(source layoutSourceBinding,
	tables [3]layoutTableBinding) (uint64, bool) {
	var total uint64
	add := func(value uint64) bool {
		var ok bool
		total, ok = checkedAdd(total, value)
		return ok
	}
	addString := func(value string) bool {
		length, ok := checkedAdd(8, uint64(len(value)))
		return ok && add(length)
	}
	strings := []string{
		layoutFoundationCanonicalHashV1, layoutFoundationProfileV1, layoutFoundationDescriptorIdentityV1,
		source.outlineSetProfile, source.outlineSetDescriptorHashProfile, source.outlineSetDescriptorHash,
		source.outlineSetHashProfile, source.outlineSetHash, source.fontContentHashProfile, source.fontContentHash,
	}
	if !addString(strings[0]) || !add(4) {
		return 0, false
	}
	for _, value := range strings[1:] {
		if !addString(value) {
			return 0, false
		}
	}
	if !add(4 + 8) { // source glyph count and expanded-real-point count
		return 0, false
	}
	for _, table := range tables {
		if !add(4+1+4+4+4) || !addString(table.hashProfile) || !addString(table.contentHash) {
			return 0, false
		}
	}
	// GDEF presence/version, node count, edge count, max depth and 28 u64
	// resource scalars.
	if !add(1 + 2 + 2 + 8 + 8 + 4 + 28*8) {
		return 0, false
	}
	return total, true
}

func layoutFoundationCanonicalBytes(source layoutSourceBinding, tables [3]layoutTableBinding,
	stats layoutFoundationResourceStats) (uint64, bool) {
	minimum, ok := layoutFoundationCanonicalMinimumBytes(source, tables)
	if !ok {
		return 0, false
	}
	return layoutFoundationWeightedSum(minimum,
		layoutFoundationTerm{stats.nodes, 42}, layoutFoundationTerm{stats.directBytes, 1},
		layoutFoundationTerm{stats.edges, 62})
}

func makeLayoutFoundationReceipt(source layoutSourceBinding, tables [3]layoutTableBinding,
	gdef *gdefFoundation, stats layoutFoundationResourceStats, foundationHash string,
	canonicalBytes uint64) layoutFoundationReceipt {
	result := layoutFoundationReceipt{
		SchemaVersion: layoutFoundationSchemaVersion, Profile: layoutFoundationReceiptProfileV1,
		SourceProfile:               source.outlineSetProfile,
		SourceDescriptorHashProfile: source.outlineSetDescriptorHashProfile,
		SourceDescriptorHash:        source.outlineSetDescriptorHash,
		SourceOutlineSetHashProfile: source.outlineSetHashProfile,
		SourceOutlineSetHash:        source.outlineSetHash,
		UpstreamContentHashProfile:  source.fontContentHashProfile,
		UpstreamContentHash:         source.fontContentHash,
		DescriptorHashProfile:       layoutFoundationDescriptorHashV1,
		DescriptorHash:              layoutFoundationDescriptorIdentityV1,
		FoundationHashProfile:       layoutFoundationCanonicalHashV1,
		FoundationHash:              foundationHash,
		LayoutStatus:                layoutFoundationLayoutStatusV1,
		CapabilityStatus:            layoutFoundationCapabilityStatusV1,
		VariationStatus:             layoutFoundationVariationStatusV1,
		DeviceStatus:                layoutFoundationDeviceStatusV1,
		CaretOrderStatus:            layoutFoundationCaretOrderStatusV1,
		GSUBStatus:                  layoutFoundationGSUBTableStatusV1,
		GPOSStatus:                  layoutFoundationGPOSTableStatusV1,
		GlyphCount:                  source.glyphCount, RealPointInstances: source.realPointInstances,
		NodeCount: stats.nodes, EdgeCount: stats.edges, AliasReferenceCount: stats.aliasReferences,
		MaxStructuralDepth: stats.maxStructuralDepth, DirectBytes: stats.directBytes,
		CoverageTableCount: stats.coverageTables, CoverageFormat1Glyphs: stats.coverageFormat1Glyphs,
		CoverageRangeRecords: stats.coverageRanges, CoveredGlyphInstances: stats.coveredGlyphs,
		ClassDefTableCount: stats.classDefTables, ClassFormat1Values: stats.classFormat1Values,
		ClassRangeRecords: stats.classRanges, ClassAssignmentInstances: stats.classAssignments,
		DeviceTableCount: stats.deviceTables, DeviceDeltaValues: stats.deviceDeltaValues,
		DeviceWordCount: stats.deviceWords, AttachListCount: stats.attachLists,
		AttachPointTableCount:     stats.attachPointTables,
		AttachmentGlyphReferences: stats.attachmentGlyphReferences,
		AttachmentPointIndices:    stats.attachmentPoints, LigCaretListCount: stats.ligCaretLists,
		LigGlyphTableCount: stats.ligGlyphTables, LigatureGlyphReferences: stats.ligatureGlyphReferences,
		CaretValueTableCount: stats.caretValueTables, CaretValueReferences: stats.caretValueReferences,
		CaretPointReferences:    stats.caretPointReferences,
		MarkGlyphSetsTableCount: stats.markGlyphSetsTables,
		MarkGlyphSetReferences:  stats.markGlyphSetReferences,
		CanonicalBinaryBytes:    canonicalBytes,
	}
	if gdef == nil {
		result.GDEFStatus = layoutFoundationGDEFAbsentStatusV1
	} else {
		result.GDEFPresent = true
		result.GDEFMajorVersion = gdef.majorVersion
		result.GDEFMinorVersion = gdef.minorVersion
		result.GDEFStatus = layoutFoundationGDEFValidatedStatusV1
	}
	for index, table := range tables {
		result.Tables[index] = layoutFoundationTableReceipt{
			Tag: string(table.tag[:]), Present: table.present, FileOffset: table.fileOffset,
			Length: table.length, Checksum: table.checksum,
			HashProfile: table.hashProfile, ContentHash: table.contentHash,
		}
	}
	return result
}

func layoutFoundationBuildInput(path string) error {
	return fontError(codeLayoutInputInvalid, path, errLayoutInputInvalid)
}
