package sfntglyf

import (
	"context"
	"errors"
	"fmt"
)

// outlineSetRebindEvidence is deliberately private and contains no encoded-byte
// view. A later layout-table gate may use the rebound scalar records to create
// an operation-local bounded reader, but no slice aliases the owned Font bytes.
type outlineSetRebindEvidence struct {
	font             *Font
	outline          *OutlineFont
	contentHash      string
	coreWorkUnits    uint64
	coreWorkingBytes uint64
	gdef             outlineSetRebindTableEvidence
	gsub             outlineSetRebindTableEvidence
	gpos             outlineSetRebindTableEvidence
}

type outlineSetRebindTableEvidence struct {
	present     bool
	record      tableRecord
	contentHash string
}

type outlineSetRebindAdmission struct {
	limits      OutlineSetLimits
	coreWork    uint64
	coreWorking uint64
}

// rebindOutlineSetSource revalidates the complete private lineage retained by
// an OutlineSet. It publishes no capability and never exposes font/table bytes.
// The helper is operation-local and exists for the later strict layout gate.
func rebindOutlineSetSource(ctx context.Context, set *OutlineSet, limits OutlineSetLimits) (evidence outlineSetRebindEvidence, err error) {
	defer func() {
		if recover() != nil {
			evidence = outlineSetRebindEvidence{}
			err = fontError(CodeAllocationFailed, "outline_set/rebind/allocation", ErrAllocationFailed)
		}
	}()
	if err := contextFailure(ctx, "outline_set/rebind/context"); err != nil {
		return outlineSetRebindEvidence{}, err
	}
	normalized, err := normalizeOutlineSetLimits(limits)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	admission, err := admitOutlineSetSourceRebind(ctx, set, normalized)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	evidence, err = rebindOutlineSetCore(ctx, set.source.source.font)
	if err != nil {
		if errors.Is(err, ErrCanceled) || errors.Is(err, ErrContextInvalid) || errors.Is(err, ErrAllocationFailed) {
			return outlineSetRebindEvidence{}, err
		}
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core")
	}
	evidence.coreWorkUnits = admission.coreWork
	evidence.coreWorkingBytes = admission.coreWorking

	replayLimits := admission.limits
	if admission.coreWork > replayLimits.MaxWorkUnits {
		return outlineSetRebindEvidence{}, outlineSetResource("outline_set/rebind/resources/core_work")
	}
	replayLimits.MaxWorkUnits -= admission.coreWork
	// Replaying an already-published set uses the exact ceiling bound into that
	// set's receipt, rather than a possibly looser caller ceiling.
	replayLimits.MaxExactBits = set.receipt.ExactBitsLimit
	preflight, working, err := outlineSetPreflightSource(set.source, replayLimits)
	if err != nil {
		return outlineSetRebindEvidence{}, normalizeOutlineSetRebindError(err)
	}
	if working != set.receipt.WorkingBytes {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/working_binding")
	}
	topology, err := validateOutlineSetSourceGraphAndTopology(ctx, set.source, replayLimits, preflight, working)
	if err != nil {
		return outlineSetRebindEvidence{}, normalizeOutlineSetRebindError(err)
	}
	if !outlineSetGeometryTopologyMatchesReceipt(topology, set.receipt) {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/topology_binding")
	}
	commands, err := computeOutlineSetTopology(ctx, set.source.source, replayLimits)
	if err != nil {
		return outlineSetRebindEvidence{}, normalizeOutlineSetRebindError(err)
	}
	if !outlineSetCommandTopologyMatchesReceipt(commands, set.receipt) {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/command_topology_binding")
	}
	canonicalBytes, ok := outlineSetCanonicalBytes(set.source, commands)
	if !ok || canonicalBytes != set.receipt.CanonicalBinaryBytes || canonicalBytes > replayLimits.MaxCanonicalBinaryBytes {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/canonical_binding")
	}
	materialExactLower, ok := outlineSetMaterializationExactLower(set.source, commands)
	if !ok || materialExactLower != set.receipt.MaterializationExactOpCount {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/material_exact_binding")
	}
	exactLower, ok := checkedAdd(set.source.receipt.ExactOpCount, materialExactLower)
	if !ok {
		return outlineSetRebindEvidence{}, outlineSetResource("outline_set/rebind/resources/exact_lower_bound")
	}
	limbLower, ok := checkedAdd(set.source.receipt.ExactLimbUnits, materialExactLower)
	if !ok {
		return outlineSetRebindEvidence{}, outlineSetResource("outline_set/rebind/resources/limb_lower_bound")
	}
	canonicalLower, ok := checkedAdd(set.source.receipt.CanonicalBinaryBytes, canonicalBytes)
	if !ok {
		return outlineSetRebindEvidence{}, outlineSetResource("outline_set/rebind/resources/canonical_lower_bound")
	}
	work, err := newOutlineSetWorkMeter(set.source, topology, commands, exactLower, limbLower, canonicalLower, replayLimits)
	if err != nil {
		return outlineSetRebindEvidence{}, normalizeOutlineSetRebindError(err)
	}
	if err := replayOutlineSetSource(ctx, set, topology, commands, work, replayLimits); err != nil {
		return outlineSetRebindEvidence{}, normalizeOutlineSetRebindError(err)
	}
	evidence.outline = set.source.source
	if err := contextFailure(ctx, "outline_set/rebind/publish"); err != nil {
		return outlineSetRebindEvidence{}, err
	}
	return evidence, nil
}

func (value outlineSetRebindEvidence) expandedRealPointCount(glyphID uint32) (uint64, bool) {
	if value.outline == nil || uint64(glyphID) >= uint64(len(value.outline.glyphs)) {
		return 0, false
	}
	return value.outline.glyphs[glyphID].expandedPoints, true
}

func admitOutlineSetSourceRebind(ctx context.Context, set *OutlineSet, limits OutlineSetLimits) (outlineSetRebindAdmission, error) {
	if set == nil || set.source == nil || set.source.source == nil || set.source.source.font == nil {
		return outlineSetRebindAdmission{}, outlineSetRebindInput("outline_set/rebind/source")
	}
	font := set.source.source.font
	coreWork, coreWorking, err := validateOutlineSetCoreReceipt(font)
	if err != nil {
		return outlineSetRebindAdmission{}, err
	}
	// Untrusted receipt values may reject but never admit. This early lower-bound
	// gate prevents a tiny caller budget from triggering O(G)/O(K) recounts.
	totalWork, ok := checkedAdd(coreWork, set.receipt.WorkUnits)
	if !ok || totalWork > limits.MaxWorkUnits {
		return outlineSetRebindAdmission{}, outlineSetResource("outline_set/rebind/resources/work_lower_bound")
	}
	working := coreWorking
	if set.receipt.WorkingBytes > working {
		working = set.receipt.WorkingBytes
	}
	if working > limits.MaxWorkingBytes {
		return outlineSetRebindAdmission{}, outlineSetResource("outline_set/rebind/resources/working_lower_bound")
	}
	if set.receipt.RetainedBytes > limits.MaxRetainedBytes {
		return outlineSetRebindAdmission{}, outlineSetResource("outline_set/rebind/resources/retained")
	}
	if err := validateOutlineSetOutlineReceipt(set.source.source); err != nil {
		return outlineSetRebindAdmission{}, err
	}
	if err := validateOutlineSetSourceReceipt(set.source); err != nil {
		return outlineSetRebindAdmission{}, err
	}
	if err := validatePublishedOutlineSetReceipt(ctx, set, limits); err != nil {
		return outlineSetRebindAdmission{}, err
	}
	totalWork, ok = checkedAdd(coreWork, set.receipt.WorkUnits)
	if !ok || totalWork > limits.MaxWorkUnits {
		return outlineSetRebindAdmission{}, outlineSetResource("outline_set/rebind/resources/work_lower_bound")
	}
	working = coreWorking
	if set.receipt.WorkingBytes > working {
		working = set.receipt.WorkingBytes
	}
	if working > limits.MaxWorkingBytes {
		return outlineSetRebindAdmission{}, outlineSetResource("outline_set/rebind/resources/working_lower_bound")
	}
	if set.receipt.RetainedBytes > limits.MaxRetainedBytes {
		return outlineSetRebindAdmission{}, outlineSetResource("outline_set/rebind/resources/retained")
	}
	return outlineSetRebindAdmission{limits: limits, coreWork: coreWork, coreWorking: coreWorking}, nil
}

func validateOutlineSetCoreReceipt(font *Font) (uint64, uint64, error) {
	if font == nil || len(font.encoded) == 0 || len(font.tables) == 0 || uint64(len(font.tables)) > uint64(HardMaxTables) ||
		uint64(len(font.mappings)) > HardMaxCmapMappings {
		return 0, 0, outlineSetRebindInput("outline_set/rebind/core/source")
	}
	r := font.receipt
	if r.SchemaVersion != ReceiptSchemaVersion || r.Profile != ReceiptProfileV1 ||
		r.ContentHashProfile != ContentHashProfileV1 || !validOutlineSetDigest(r.ContentHash) ||
		r.TableHashProfile != TableHashProfileV1 || r.CmapProfile != CmapProfileV1 ||
		r.LayoutTableStatus != LayoutTableStatusV1 || r.GlyfTableStatus != GlyfTableStatusV1 ||
		r.MetricExtremaStatus != DeclaredStatusV1 || r.EncodedBytes != uint64(len(font.encoded)) ||
		r.TableCount != uint32(len(font.tables)) || uint64(len(r.Tables)) != uint64(r.TableCount) ||
		r.NumGlyphs != font.numGlyphs || r.UnitsPerEm != font.unitsPerEm || r.HheaAscender != font.hheaAscender ||
		r.NumberOfHMetrics != font.numberOfHMetrics || r.IndexToLocFormat != font.indexToLocFormat ||
		r.CmapMappingCount != uint64(len(font.mappings)) || r.NumGlyphs == 0 || r.TableCount == 0 ||
		r.EncodedBytes > HardMaxEncodedBytes || r.TableCount > HardMaxTables || r.NumGlyphs > HardMaxGlyphs ||
		r.CmapRecordCount > HardMaxCmapRecords || r.CmapFormat4Segments > HardMaxCmapGroups ||
		r.CmapFormat12Groups > HardMaxCmapGroups ||
		r.CmapMappingSlots > HardMaxCmapMappings || r.CmapMappingCount > HardMaxCmapMappings ||
		r.WorkUnits > HardMaxWorkUnits || r.WorkingBytes > HardMaxWorkingBytes || r.RetainedBytes > HardMaxRetainedBytes {
		return 0, 0, outlineSetRebindInput("outline_set/rebind/core/receipt")
	}
	for index, table := range r.Tables {
		record := font.tables[index]
		if len(table.Tag) != 4 || !validOutlineSetDigest(table.ContentHash) || table.Tag != string(record.tag[:]) ||
			table.Offset != record.offset || table.Length != record.length || table.Checksum != record.checksum {
			return 0, 0, outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/core/tables/%d", index))
		}
	}
	if len(r.OpaqueLayoutTables) > 3 {
		return 0, 0, outlineSetRebindInput("outline_set/rebind/core/opaque_layout_tables")
	}
	work, ok := outlineWeightedSum(0,
		[2]uint64{r.EncodedBytes, 8}, [2]uint64{uint64(r.TableCount), 64},
		[2]uint64{uint64(r.NumGlyphs), 4}, [2]uint64{uint64(r.CmapRecordCount), 32},
		[2]uint64{r.CmapFormat4Segments, 16}, [2]uint64{r.CmapFormat12Groups, 16},
		[2]uint64{r.CmapMappingSlots, 2})
	if !ok || work != r.WorkUnits {
		return 0, 0, outlineSetRebindInput("outline_set/rebind/core/work_binding")
	}
	working, ok := outlineWeightedSum(logicalFixedWorkingBytes, [2]uint64{r.CmapMappingSlots, 16})
	if !ok || working != r.WorkingBytes {
		return 0, 0, outlineSetRebindInput("outline_set/rebind/core/working_binding")
	}
	retained, ok := outlineWeightedSum(logicalFixedRetainedBytes,
		[2]uint64{r.EncodedBytes, 1}, [2]uint64{uint64(r.TableCount), logicalTableBytes},
		[2]uint64{r.CmapMappingCount, logicalMappingBytes})
	if !ok || retained != r.RetainedBytes {
		return 0, 0, outlineSetRebindInput("outline_set/rebind/core/retained_binding")
	}
	// validateContainer owns both the directory slice and a physical-order copy.
	// Charge two conservative logical table records before either make occurs.
	containerScratch, ok := checkedMultiply(uint64(r.TableCount), 2*logicalTableBytes)
	if ok {
		containerScratch, ok = checkedAdd(containerScratch, logicalFixedWorkingBytes)
	}
	if !ok {
		return 0, 0, outlineSetResource("outline_set/rebind/resources/core_working")
	}
	// During cmap replay the verified directory remains live and validateCmap
	// creates a second canonical mapping slice. The source Font mappings are
	// retained input; only the replayed slice is charged as operation working.
	directoryLive, ok := checkedMultiply(uint64(r.TableCount), logicalTableBytes)
	temporaryMappings, mappingsOK := checkedMultiply(r.CmapMappingCount, logicalMappingBytes)
	if ok {
		working, ok = checkedAdd(working, directoryLive)
	}
	if ok && mappingsOK {
		working, ok = checkedAdd(working, temporaryMappings)
	} else {
		ok = false
	}
	if !ok {
		return 0, 0, outlineSetResource("outline_set/rebind/resources/core_working")
	}
	if working > containerScratch {
		containerScratch = working
	}
	return work, containerScratch, nil
}

func validateOutlineSetOutlineReceipt(source *OutlineFont) error {
	if source == nil || source.font == nil || len(source.glyphs) == 0 || uint64(len(source.glyphs)) > uint64(^uint32(0)) {
		return outlineSetRebindInput("outline_set/rebind/outline/source")
	}
	r := source.receipt
	f := source.font.receipt
	if r.SchemaVersion != OutlineReceiptSchemaVersion || r.Profile != OutlineReceiptProfileV1 ||
		r.UpstreamProfile != ReceiptProfileV1 || r.UpstreamContentProfile != ContentHashProfileV1 ||
		r.UpstreamContentHash != f.ContentHash || !validOutlineSetDigest(r.UpstreamContentHash) ||
		r.UpstreamRetainedBytes != f.RetainedBytes || r.DescriptorHashProfile != OutlineDescriptorHashProfileV1 ||
		r.DescriptorHash != OutlineDescriptorHash() || r.GraphHashProfile != OutlineGraphHashProfileV1 ||
		!validOutlineSetDigest(r.GraphHash) || r.GeometryStatus != OutlineGeometryStatusV1 ||
		r.HintingStatus != OutlineHintingStatusV1 || r.LayoutStatus != OutlineLayoutStatusV1 ||
		r.MaxpStatus != OutlineMaxpStatusV1 || r.MetricsStatus != OutlineMetricsStatusV1 ||
		r.RoundXYStatus != OutlineRoundXYStatusV1 || r.PointMatchStatus != OutlinePointMatchStatusV1 ||
		r.OffsetStatus != OutlineOffsetStatusV1 || r.GlyphCount != uint32(len(source.glyphs)) ||
		r.GlyphCount != f.NumGlyphs || r.CanonicalBinaryBytes == 0 ||
		r.RawPointCount > HardMaxOutlineRawPoints || r.RawContourCount > HardMaxOutlineRawContours ||
		r.ComponentRecordCount > HardMaxOutlineComponentRecords || r.InstructionBytes > HardMaxOutlineInstructionBytes ||
		r.ObservedMaxComponentDepth > HardMaxOutlineComponentDepth ||
		r.ExpandedPointInstances > HardMaxOutlineExpandedPointInstances ||
		r.ExpandedContourInstances > HardMaxOutlineExpandedContourInstances ||
		r.ExpandedEdgeVisits > HardMaxOutlineExpandedEdgeVisits ||
		r.CanonicalBinaryBytes > HardMaxOutlineCanonicalBinaryBytes || r.WorkUnits > HardMaxOutlineWorkUnits ||
		r.WorkingBytes > HardMaxOutlineWorkingBytes || r.RetainedBytes > HardMaxOutlineRetainedBytes {
		return outlineSetRebindInput("outline_set/rebind/outline/receipt")
	}
	stats := outlineStatsFromReceipt(r)
	workBase, workingBase, retained, ok := outlineResourceBase(f, r.GraphHash, r.GlyfBytes, uint64(r.GlyphCount), stats)
	if !ok {
		return outlineSetRebindInput("outline_set/rebind/outline/resource_binding")
	}
	work, workOK := checkedAdd(workBase, r.CanonicalBinaryBytes)
	working, workingOK := checkedAdd(workingBase, r.CanonicalBinaryBytes)
	if !workOK || !workingOK || work != r.WorkUnits || working != r.WorkingBytes || retained != r.RetainedBytes {
		return outlineSetRebindInput("outline_set/rebind/outline/resource_binding")
	}
	// This is a receipt-only recount: it walks already-owned slice lengths and
	// scalar component records, but performs no hashing or allocation.
	if err := validateGeometrySourceCounts(source); err != nil {
		return outlineSetRebindInput("outline_set/rebind/outline/count_binding")
	}
	return nil
}

func outlineStatsFromReceipt(r OutlineReceipt) outlineScanStats {
	return outlineScanStats{
		emptyRangeGlyphs: r.EmptyRangeGlyphCount, zeroContourGlyphs: r.ZeroContourGlyphCount,
		simpleGlyphs: r.SimpleGlyphCount, compoundGlyphs: r.CompoundGlyphCount,
		rawPoints: r.RawPointCount, rawContours: r.RawContourCount, componentRecords: r.ComponentRecordCount,
		instructionGlyphs: r.InstructionGlyphCount, instructionBytes: r.InstructionBytes, paddingBytes: r.PaddingBytes,
		pointMatchComponents: r.PointMatchComponentCount, scaledOffsets: r.ScaledOffsetCount,
		unscaledOffsets: r.UnscaledOffsetCount, defaultedOffsets: r.DefaultedUnscaledCount,
		useMyMetrics: r.UseMyMetricsCount, overlapSimpleGlyphs: r.OverlapSimpleGlyphCount,
		overlapCompoundGlyphs: r.OverlapCompoundGlyphCount, expandedPoints: r.ExpandedPointInstances,
		expandedContours: r.ExpandedContourInstances, expandedEdges: r.ExpandedEdgeVisits,
		maxExpandedEdges: r.MaxExpandedEdgesPerGlyph, maxPoints: r.ObservedMaxPoints,
		maxContours: r.ObservedMaxContours, maxCompositePoints: r.ObservedMaxCompositePoints,
		maxCompositeContours: r.ObservedMaxCompositeContours, maxInstructionBytes: r.ObservedMaxInstructionBytes,
		maxComponentElements: r.ObservedMaxComponentElements, maxComponentDepth: r.ObservedMaxComponentDepth,
	}
}

func validatePublishedOutlineSetReceipt(ctx context.Context, set *OutlineSet, limits OutlineSetLimits) error {
	if set == nil || set.source == nil || len(set.glyphs) == 0 || uint64(len(set.glyphs)) > uint64(^uint32(0)) ||
		uint64(len(set.commands)) > uint64(maxInt()) {
		return outlineSetRebindInput("outline_set/rebind/set/source")
	}
	r := set.receipt
	s := set.source.receipt
	if r.SchemaVersion != OutlineSetReceiptSchemaVersion || r.Profile != OutlineSetReceiptProfileV1 ||
		r.OutlineProfile != OutlineSetOutlineProfileV1 || r.SourceProfile != s.Profile ||
		r.SourceDescriptorHashProfile != s.DescriptorHashProfile || r.SourceDescriptorHash != s.DescriptorHash ||
		r.SourceGeometryHashProfile != s.GeometryHashProfile || r.SourceGeometryHash != s.GeometryHash ||
		r.SourceGeometryCanonicalBinaryBytes != s.CanonicalBinaryBytes ||
		r.SourceGraphHashProfile != s.SourceGraphHashProfile || r.SourceGraphHash != s.SourceGraphHash ||
		r.SourceGraphCanonicalBinaryBytes != s.SourceCanonicalBinaryBytes || r.SourceRetainedBytes != s.RetainedBytes ||
		r.UpstreamContentHashProfile != s.UpstreamContentHashProfile || r.UpstreamContentHash != s.UpstreamContentHash ||
		r.DescriptorHashProfile != OutlineSetDescriptorHashProfileV1 || r.DescriptorHash != OutlineSetDescriptorHash() ||
		r.OutlineSetHashProfile != OutlineSetHashProfileV1 || !validOutlineSetDigest(r.OutlineSetHash) ||
		!validOutlineSetDigest(r.SourceDescriptorHash) || !validOutlineSetDigest(r.SourceGeometryHash) ||
		!validOutlineSetDigest(r.SourceGraphHash) || !validOutlineSetDigest(r.UpstreamContentHash) ||
		r.GeometryStatus != OutlineSetGeometryStatusV1 || r.ImpliedPointStatus != OutlineSetImpliedPointStatusV1 ||
		r.CommandStatus != OutlineSetCommandStatusV1 || r.RoundingStatus != OutlineSetRoundingStatusV1 ||
		r.HintingStatus != OutlineSetHintingStatusV1 || r.LayoutStatus != OutlineSetLayoutStatusV1 ||
		r.RasterStatus != OutlineSetRasterStatusV1 || r.CapabilityStatus != OutlineSetCapabilityStatusV1 ||
		r.GlyphCount != uint32(len(set.glyphs)) || r.GlyphCount != s.GlyphCount ||
		r.CommandInstances != uint64(len(set.commands)) || r.CanonicalBinaryBytes == 0 ||
		r.ExactBitsLimit == 0 || r.ExactBitsLimit > HardMaxOutlineSetExactBits ||
		r.MaxObservedExactBits > r.ExactBitsLimit || r.SourceMaxObservedExactBits != s.MaxObservedExactBits ||
		r.SourceExactOpCount != s.ExactOpCount || r.SourceExactLimbUnits != s.ExactLimbUnits ||
		r.SourceMaxObservedExactBits > r.MaxObservedExactBits || r.MaterializationMaxExactBits > r.MaxObservedExactBits {
		return outlineSetRebindInput("outline_set/rebind/set/receipt")
	}
	if r.ExpandedPointInstances != s.ExpandedPointInstances || r.ExpandedContourInstances != s.ExpandedContourInstances ||
		r.ExpandedEdgeVisits != s.ExpandedEdgeVisits || r.ExpandedGlyphInstances != s.ExpandedGlyphInstances ||
		r.TransformPointVisits != s.TransformPointVisits ||
		r.MaxExpandedGlyphInstancesPerRoot != s.MaxExpandedGlyphInstancesPerRoot ||
		r.MaxTransformVisitsPerGlyph != s.MaxTransformVisitsPerGlyph ||
		r.MaxExpandedPointsPerGlyph != s.MaxExpandedPointsPerGlyph ||
		r.MaxExpandedContoursPerGlyph != s.MaxExpandedContoursPerGlyph || r.MaxComponentDepth != s.MaxComponentDepth {
		return outlineSetRebindInput("outline_set/rebind/set/source_counts")
	}
	totalOps, ok := checkedAdd(r.SourceExactOpCount, r.MaterializationExactOpCount)
	if !ok || totalOps != r.ExactOpCount {
		return outlineSetRebindInput("outline_set/rebind/set/exact_operation_binding")
	}
	totalLimbs, ok := checkedAdd(r.SourceExactLimbUnits, r.MaterializationExactLimbUnits)
	if !ok || totalLimbs != r.ExactLimbUnits || r.ExactLimbUnits < r.ExactOpCount ||
		r.MaterializationExactLimbUnits < r.MaterializationExactOpCount {
		return outlineSetRebindInput("outline_set/rebind/set/exact_limb_binding")
	}
	maxBits := r.SourceMaxObservedExactBits
	if r.MaterializationMaxExactBits > maxBits {
		maxBits = r.MaterializationMaxExactBits
	}
	if maxBits != r.MaxObservedExactBits {
		return outlineSetRebindInput("outline_set/rebind/set/exact_bits_binding")
	}
	topology := outlineSetGeometryTopologyFromReceipt(r)
	commands := outlineSetCommandTopologyFromReceipt(r)
	materialLower, ok := outlineSetMaterializationExactLower(set.source, commands)
	if !ok || materialLower != r.MaterializationExactOpCount {
		return outlineSetRebindInput("outline_set/rebind/set/material_exact_binding")
	}
	canonicalTotal, ok := checkedAdd(r.SourceGeometryCanonicalBinaryBytes, r.CanonicalBinaryBytes)
	if !ok {
		return outlineSetRebindInput("outline_set/rebind/set/canonical_binding")
	}
	work, ok := outlineSetWorkBase(set.source, topology, commands, r.ExactOpCount, r.ExactLimbUnits, canonicalTotal)
	if !ok || work != r.WorkUnits {
		return outlineSetRebindInput("outline_set/rebind/set/work_binding")
	}
	working, ok := outlineSetWorkingBytes(uint64(r.GlyphCount), topology, r.ExactBitsLimit)
	if !ok || working != r.WorkingBytes {
		return outlineSetRebindInput("outline_set/rebind/set/working_binding")
	}
	retained, ok := outlineSetRetainedBytes(set.source, uint64(r.GlyphCount), r.CommandInstances, r)
	if !ok || retained != r.RetainedBytes {
		return outlineSetRebindInput("outline_set/rebind/set/retained_binding")
	}
	if r.ExpandedPointInstances > limits.MaxExpandedPointInstances ||
		r.ExpandedContourInstances > limits.MaxExpandedContourInstances || r.ExpandedEdgeVisits > limits.MaxExpandedEdgeVisits ||
		r.ExpandedGlyphInstances > limits.MaxExpandedGlyphInstances || r.TransformPointVisits > limits.MaxTransformPointVisits ||
		r.ImpliedPointInstances > limits.MaxImpliedPointInstances || r.DrawingCommandInstances > limits.MaxDrawingCommandInstances ||
		r.CommandInstances > limits.MaxCommandInstances || r.CommandPointOccurrences > limits.MaxCommandPointOccurrences ||
		r.MaxCommandsPerGlyph > limits.MaxCommandsPerGlyph ||
		r.MaxCommandPointOccurrencesPerGlyph > limits.MaxCommandPointOccurrencesPerGlyph ||
		r.ExactBitsLimit > limits.MaxExactBits || r.ExactOpCount > limits.MaxExactOps ||
		r.ExactLimbUnits > limits.MaxExactLimbUnits || r.CanonicalBinaryBytes > limits.MaxCanonicalBinaryBytes {
		return outlineSetResource("outline_set/rebind/resources/receipt")
	}
	return validatePublishedOutlineSetCommands(ctx, set)
}

func validatePublishedOutlineSetCommands(ctx context.Context, set *OutlineSet) error {
	r := set.receipt
	var cursor uint64
	var glyphsWithContours, contours, lines, quads, commandPoints uint64
	var maxDrawing, maxCommands, maxPoints uint64
	for glyphID, glyph := range set.glyphs {
		if glyphID%256 == 0 {
			if err := contextFailure(ctx, "outline_set/rebind/set/glyphs"); err != nil {
				return err
			}
		}
		if glyph.commandStart != cursor || glyph.commandCount > uint64(len(set.commands))-cursor {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/set/glyphs/%d/range", glyphID))
		}
		end, ok := checkedAdd(cursor, glyph.commandCount)
		if !ok || end > uint64(len(set.commands)) {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/set/glyphs/%d/range", glyphID))
		}
		var glyphContours, glyphLines, glyphQuads, glyphPoints uint64
		open := false
		for commandIndex := cursor; commandIndex < end; commandIndex++ {
			if (commandIndex-cursor)%1024 == 0 {
				if err := contextFailure(ctx, "outline_set/rebind/set/commands"); err != nil {
					return err
				}
			}
			command := set.commands[commandIndex]
			switch command.op {
			case OutlineMoveTo:
				if open || command.pointCount != 1 || command.points[1] != (OutlinePoint{}) {
					return outlineSetRebindInput("outline_set/rebind/set/commands/move")
				}
				open = true
				glyphPoints++
			case OutlineLineTo:
				if !open || command.pointCount != 1 || command.points[1] != (OutlinePoint{}) {
					return outlineSetRebindInput("outline_set/rebind/set/commands/line")
				}
				glyphLines++
				glyphPoints++
			case OutlineQuadTo:
				if !open || command.pointCount != 2 {
					return outlineSetRebindInput("outline_set/rebind/set/commands/quad")
				}
				glyphQuads++
				glyphPoints += 2
			case OutlineClose:
				if !open || command.pointCount != 0 || command.points != ([2]OutlinePoint{}) {
					return outlineSetRebindInput("outline_set/rebind/set/commands/close")
				}
				open = false
				glyphContours++
			default:
				return outlineSetRebindInput("outline_set/rebind/set/commands/op")
			}
		}
		if open || glyphContours != glyph.contours || (glyph.contours == 0) != (glyph.commandCount == 0) {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/set/glyphs/%d/contours", glyphID))
		}
		if glyphContours > 0 {
			glyphsWithContours++
		}
		var addOK bool
		contours, addOK = checkedAdd(contours, glyphContours)
		if addOK {
			lines, addOK = checkedAdd(lines, glyphLines)
		}
		if addOK {
			quads, addOK = checkedAdd(quads, glyphQuads)
		}
		if addOK {
			commandPoints, addOK = checkedAdd(commandPoints, glyphPoints)
		}
		if !addOK {
			return outlineSetRebindInput("outline_set/rebind/set/count_overflow")
		}
		drawing, ok := checkedAdd(glyphLines, glyphQuads)
		if !ok {
			return outlineSetRebindInput("outline_set/rebind/set/count_overflow")
		}
		if drawing > maxDrawing {
			maxDrawing = drawing
		}
		if glyph.commandCount > maxCommands {
			maxCommands = glyph.commandCount
		}
		if glyphPoints > maxPoints {
			maxPoints = glyphPoints
		}
		cursor = end
	}
	if cursor != uint64(len(set.commands)) {
		return outlineSetRebindInput("outline_set/rebind/set/final_range")
	}
	drawing, ok := checkedAdd(lines, quads)
	if !ok || glyphsWithContours != r.GlyphsWithContours || contours != r.ExpandedContourInstances ||
		lines != r.LineCommandInstances || quads != r.QuadCommandInstances || drawing != r.DrawingCommandInstances ||
		commandPoints != r.CommandPointOccurrences || maxDrawing != r.MaxDrawingCommandsPerGlyph ||
		maxCommands != r.MaxCommandsPerGlyph || maxPoints != r.MaxCommandPointOccurrencesPerGlyph {
		return outlineSetRebindInput("outline_set/rebind/set/command_counts")
	}
	return nil
}

func outlineSetGeometryTopologyFromReceipt(r OutlineSetReceipt) geometryTopologyStats {
	return geometryTopologyStats{
		expandedGlyphInstances:   r.ExpandedGlyphInstances,
		maxExpandedGlyphsPerRoot: r.MaxExpandedGlyphInstancesPerRoot,
		transformPointVisits:     r.TransformPointVisits, maxTransformVisitsPerGlyph: r.MaxTransformVisitsPerGlyph,
		maxExpandedPointsPerGlyph:   r.MaxExpandedPointsPerGlyph,
		maxExpandedContoursPerGlyph: r.MaxExpandedContoursPerGlyph, maxComponentDepth: r.MaxComponentDepth,
	}
}

func outlineSetCommandTopologyFromReceipt(r OutlineSetReceipt) outlineSetTopologyStats {
	return outlineSetTopologyStats{
		glyphsWithContours: r.GlyphsWithContours, implied: r.ImpliedPointInstances,
		lines: r.LineCommandInstances, quads: r.QuadCommandInstances, drawing: r.DrawingCommandInstances,
		commands: r.CommandInstances, commandPoints: r.CommandPointOccurrences,
		maxImpliedPerGlyph: r.MaxImpliedPointsPerGlyph, maxDrawingPerGlyph: r.MaxDrawingCommandsPerGlyph,
		maxCommandsPerGlyph: r.MaxCommandsPerGlyph, maxCommandPointsPerGlyph: r.MaxCommandPointOccurrencesPerGlyph,
	}
}

func outlineSetGeometryTopologyMatchesReceipt(value geometryTopologyStats, r OutlineSetReceipt) bool {
	return value == outlineSetGeometryTopologyFromReceipt(r)
}

func outlineSetCommandTopologyMatchesReceipt(value outlineSetTopologyStats, r OutlineSetReceipt) bool {
	want := outlineSetCommandTopologyFromReceipt(r)
	return value.glyphsWithContours == want.glyphsWithContours && value.implied == want.implied &&
		value.lines == want.lines && value.quads == want.quads && value.drawing == want.drawing &&
		value.commands == want.commands && value.commandPoints == want.commandPoints &&
		value.maxImpliedPerGlyph == want.maxImpliedPerGlyph && value.maxDrawingPerGlyph == want.maxDrawingPerGlyph &&
		value.maxCommandsPerGlyph == want.maxCommandsPerGlyph && value.maxCommandPointsPerGlyph == want.maxCommandPointsPerGlyph
}

func validOutlineSetDigest(value string) bool {
	if len(value) != len("sha256:")+64 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func outlineSetRebindInput(path string) error {
	return fontError(CodeOutlineSetInputInvalid, path, ErrOutlineSetInputInvalid)
}

func normalizeOutlineSetRebindError(err error) error {
	if err == nil || errors.Is(err, ErrCanceled) || errors.Is(err, ErrContextInvalid) || errors.Is(err, ErrAllocationFailed) ||
		errors.Is(err, ErrLimitsInvalid) || errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrOutlineSetCoordinate) {
		return err
	}
	return outlineSetRebindInput("outline_set/rebind/source")
}

func rebindOutlineSetCore(ctx context.Context, font *Font) (outlineSetRebindEvidence, error) {
	if font == nil {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/font")
	}
	r := font.receipt
	limits := Limits{
		MaxEncodedBytes: r.EncodedBytes, MaxTables: r.TableCount, MaxGlyphs: r.NumGlyphs,
		MaxCmapRecords: r.CmapRecordCount, MaxCmapGroups: r.CmapFormat12Groups,
		MaxCmapMappings: r.CmapMappingSlots, MaxWorkUnits: r.WorkUnits,
		MaxWorkingBytes: r.WorkingBytes, MaxRetainedBytes: r.RetainedBytes,
	}
	container, err := validateContainer(ctx, font.encoded, limits)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	if len(container.records) != len(font.tables) || len(container.records) != len(r.Tables) {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/directory_count")
	}
	for index, record := range container.records {
		tableReceipt := r.Tables[index]
		if record != font.tables[index] || tableReceipt.Tag != string(record.tag[:]) ||
			tableReceipt.Offset != record.offset || tableReceipt.Length != record.length || tableReceipt.Checksum != record.checksum {
			return outlineSetRebindEvidence{}, outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/core/tables/%d/record_binding", index))
		}
	}
	contentHash, err := hashBytes(ctx, font.encoded, "outline_set/rebind/core/content_hash")
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	if contentHash != r.ContentHash {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/content_binding")
	}
	baseWork, err := preflightBaseWork(font.encoded, limits)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	core, err := validateCore(ctx, font.encoded, container.records, limits, baseWork)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	if core.numGlyphs != font.numGlyphs || core.unitsPerEm != font.unitsPerEm ||
		core.hheaAscender != font.hheaAscender || core.numberOfHMetrics != font.numberOfHMetrics ||
		core.indexToLocFormat != font.indexToLocFormat || core.hmtxOffset != font.hmtxOffset ||
		core.locaOffset != font.locaOffset || core.glyfOffset != font.glyfOffset ||
		core.numGlyphs != r.NumGlyphs || core.unitsPerEm != r.UnitsPerEm || core.hheaAscender != r.HheaAscender ||
		core.numberOfHMetrics != r.NumberOfHMetrics || core.indexToLocFormat != r.IndexToLocFormat {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/scalar_binding")
	}
	baseRetained, err := preflightBaseRetained(font.encoded, uint64(len(container.records)), limits)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	cmapRecord, found := findRecord(container.records, "cmap")
	if !found {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/cmap_record")
	}
	cmap, err := validateCmap(ctx, font.encoded, cmapRecord, core.numGlyphs, limits, core.workUnits, baseRetained)
	if err != nil {
		return outlineSetRebindEvidence{}, err
	}
	if cmap.recordCount != r.CmapRecordCount || cmap.format4Segments != r.CmapFormat4Segments ||
		cmap.format12Groups != r.CmapFormat12Groups || cmap.mappingSlots != r.CmapMappingSlots ||
		cmap.workUnits != r.WorkUnits || cmap.workingBytes != r.WorkingBytes || cmap.retainedBytes != r.RetainedBytes ||
		len(cmap.mappings) != len(font.mappings) {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/cmap_binding")
	}
	for index := range cmap.mappings {
		if cmap.mappings[index] != font.mappings[index] {
			return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/cmap_mapping_binding")
		}
	}
	evidence := outlineSetRebindEvidence{font: font, contentHash: contentHash}
	opaque := make([]string, 0, 3)
	for index, record := range container.records {
		if index%64 == 0 {
			if err := contextFailure(ctx, "outline_set/rebind/core/tables"); err != nil {
				return outlineSetRebindEvidence{}, err
			}
		}
		tableReceipt := r.Tables[index]
		tag := string(record.tag[:])
		if tableReceipt.Tag != tag || tableReceipt.Offset != record.offset || tableReceipt.Length != record.length ||
			tableReceipt.Checksum != record.checksum {
			return outlineSetRebindEvidence{}, outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/core/tables/%d/receipt_record", index))
		}
		tableHash, err := hashBytes(ctx, tableBytes(font.encoded, record), fmt.Sprintf("outline_set/rebind/core/tables/%d/hash", index))
		if err != nil {
			return outlineSetRebindEvidence{}, err
		}
		if tableHash != tableReceipt.ContentHash {
			return outlineSetRebindEvidence{}, outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/core/tables/%d/hash_binding", index))
		}
		tableEvidence := outlineSetRebindTableEvidence{present: true, record: record, contentHash: tableHash}
		switch tag {
		case "GDEF":
			evidence.gdef = tableEvidence
			opaque = append(opaque, tag)
		case "GSUB":
			evidence.gsub = tableEvidence
			opaque = append(opaque, tag)
		case "GPOS":
			evidence.gpos = tableEvidence
			opaque = append(opaque, tag)
		}
	}
	if len(opaque) != len(r.OpaqueLayoutTables) {
		return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/opaque_layout_binding")
	}
	for index := range opaque {
		if opaque[index] != r.OpaqueLayoutTables[index] {
			return outlineSetRebindEvidence{}, outlineSetRebindInput("outline_set/rebind/core/opaque_layout_binding")
		}
	}
	return evidence, nil
}

type outlineSetCompareCommandSink struct {
	expected []outlineSetCommand
	next     uint64
}

func (value *outlineSetCompareCommandSink) commandCount() uint64 {
	if value == nil {
		return 0
	}
	return value.next
}

func (value *outlineSetCompareCommandSink) emitOutlineSetCommand(command outlineSetCommand) error {
	if value == nil || value.next >= uint64(len(value.expected)) || value.expected[value.next] != command {
		return outlineSetRebindInput("outline_set/rebind/commands/value_binding")
	}
	value.next++
	return nil
}

func replayOutlineSetSource(ctx context.Context, set *OutlineSet, topology geometryTopologyStats,
	commands outlineSetTopologyStats, work *outlineSetWorkMeter, limits OutlineSetLimits) error {
	source := set.source
	declarations, err := readGeometryDeclarations(source.source)
	if err != nil {
		return err
	}
	headLSBStatus := GeometryHeadLSBUnsetV1
	if declarations.headFlags&0x0002 != 0 {
		headLSBStatus = GeometryHeadLSBSetV1
	}
	metricSources, metricRedirects, err := resolveGeometryMetricSources(ctx, source.source)
	if err != nil {
		return err
	}
	if metricRedirects != source.receipt.MetricRedirectGlyphCount {
		return outlineSetRebindInput("outline_set/rebind/geometry/metric_redirects")
	}
	sourceExact := newGeometryExactContextWithWork(ctx, limits.MaxExactBits, limits.MaxExactOps, limits.MaxExactLimbUnits, work)
	commandExact := newGeometryExactContextWithWork(ctx, limits.MaxExactBits, limits.MaxExactOps, limits.MaxExactLimbUnits, work)
	sourceHash, err := newGeometryHashWriterWithWork(ctx, source.source, source.receipt.GlyphCount,
		source.receipt.CanonicalBinaryBytes, work)
	if err != nil {
		return err
	}
	compare := &outlineSetCompareCommandSink{expected: set.commands}
	global := geometryGlobalMetrics{}
	for glyphIndex, raw := range source.source.glyphs {
		if glyphIndex%64 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline_set/rebind/glyphs/%d", glyphIndex)); err != nil {
				return err
			}
		}
		expanded, err := expandExactGlyph(ctx, source.source, uint32(glyphIndex), sourceExact)
		if err != nil {
			return err
		}
		xMin, yMin, xMax, yMax, err := exactGeometryBBox(sourceExact, expanded, uint32(glyphIndex))
		if err != nil {
			return err
		}
		metric, ok := source.source.font.HorizontalMetric(uint32(glyphIndex))
		if !ok {
			return outlineSetRebindInput("outline_set/rebind/geometry/hmtx")
		}
		verified := geometryGlyph{
			kind: raw.kind, hasContours: len(expanded.endPoints) > 0,
			xMin: xMin, yMin: yMin, xMax: xMax, yMax: yMax,
			advanceWidth: metric.AdvanceWidth, leftSideBearing: metric.LeftSideBearing,
			effectiveMetricSource: metricSources[glyphIndex],
		}
		if verified != source.glyphs[glyphIndex] {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/geometry/glyphs/%d", glyphIndex))
		}
		if declarations.headFlags&0x0002 != 0 && verified.xMin != verified.leftSideBearing {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/geometry/glyphs/%d/head_lsb", glyphIndex))
		}
		if err := global.addGlyph(verified); err != nil {
			return err
		}
		if err := sourceHash.writeGlyph(uint32(glyphIndex), raw, expanded, verified); err != nil {
			return err
		}
		rangeValue := set.glyphs[glyphIndex]
		if rangeValue.commandStart != compare.commandCount() || rangeValue.contours != uint64(len(expanded.endPoints)) {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/commands/glyphs/%d/range", glyphIndex))
		}
		start := compare.commandCount()
		if err := materializeOutlineSetGlyphToSink(ctx, uint32(glyphIndex), expanded, commands.glyphs[glyphIndex], commandExact, compare); err != nil {
			return err
		}
		if compare.commandCount()-start != rangeValue.commandCount {
			return outlineSetRebindInput(fmt.Sprintf("outline_set/rebind/commands/glyphs/%d/count", glyphIndex))
		}
	}
	if compare.commandCount() != uint64(len(set.commands)) {
		return outlineSetRebindInput("outline_set/rebind/commands/final_count")
	}
	if err := validateGeometryDeclarations(declarations, global); err != nil {
		return err
	}
	if global.glyphsWithContours != source.receipt.GlyphsWithContours ||
		global.headXMin != source.receipt.HeadXMin || global.headYMin != source.receipt.HeadYMin ||
		global.headXMax != source.receipt.HeadXMax || global.headYMax != source.receipt.HeadYMax ||
		global.minLSB != source.receipt.MinLSB || global.minRSB != source.receipt.MinRSB ||
		global.xExtent != source.receipt.XExtent || headLSBStatus != source.receipt.HeadLSBStatus {
		return outlineSetRebindInput("outline_set/rebind/geometry/global_metrics")
	}
	sourceGeometryHash, sourceGeometryBytes, err := sourceHash.finish(
		global.headXMin, global.headYMin, global.headXMax, global.headYMax,
		global.minLSB, global.minRSB, global.xExtent, headLSBStatus)
	if err != nil {
		return err
	}
	sourceStats := sourceExact.Stats()
	if sourceGeometryHash != source.receipt.GeometryHash || sourceGeometryBytes != source.receipt.CanonicalBinaryBytes ||
		sourceStats.OperationCount != source.receipt.ExactOpCount || sourceStats.LimbUnits != source.receipt.ExactLimbUnits ||
		sourceStats.MaxExactBits != source.receipt.MaxObservedExactBits {
		return outlineSetRebindInput("outline_set/rebind/geometry/hash_binding")
	}
	commandStats := commandExact.Stats()
	if commandStats.OperationCount != set.receipt.MaterializationExactOpCount ||
		commandStats.LimbUnits != set.receipt.MaterializationExactLimbUnits ||
		commandStats.MaxExactBits != set.receipt.MaterializationMaxExactBits {
		return outlineSetRebindInput("outline_set/rebind/commands/exact_binding")
	}
	hashWriter, err := newOutlineSetHashWriter(ctx, source, limits.MaxCanonicalBinaryBytes, work)
	if err != nil {
		return err
	}
	for glyphID, glyph := range set.glyphs {
		if err := hashWriter.writeGlyph(uint32(glyphID), glyph, set.commands); err != nil {
			return err
		}
	}
	outlineHash, canonicalBytes, err := hashWriter.finish(source, commands)
	if err != nil {
		return err
	}
	if outlineHash != set.receipt.OutlineSetHash || canonicalBytes != set.receipt.CanonicalBinaryBytes {
		return outlineSetRebindInput("outline_set/rebind/set/hash_binding")
	}
	totalOps, ok := checkedAdd(sourceStats.OperationCount, commandStats.OperationCount)
	if !ok {
		return outlineSetResource("outline_set/rebind/resources/exact_operations")
	}
	totalLimbs, ok := checkedAdd(sourceStats.LimbUnits, commandStats.LimbUnits)
	if !ok {
		return outlineSetResource("outline_set/rebind/resources/exact_limbs")
	}
	canonicalTotal, ok := checkedAdd(sourceGeometryBytes, canonicalBytes)
	if !ok {
		return outlineSetResource("outline_set/rebind/resources/canonical")
	}
	computedWork, ok := outlineSetWorkBase(source, topology, commands, totalOps, totalLimbs, canonicalTotal)
	if !ok || computedWork != set.receipt.WorkUnits {
		return outlineSetRebindInput("outline_set/rebind/set/work_binding")
	}
	meterWork, meterOK := work.bindFinal(totalOps, totalLimbs, canonicalTotal)
	if !meterOK || meterWork != computedWork {
		return outlineSetRebindInput("outline_set/rebind/set/meter_binding")
	}
	return nil
}
