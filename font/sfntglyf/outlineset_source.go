package sfntglyf

import "context"

func validateOutlineSetSourceReceipt(source *GeometryFont) error {
	if source == nil || source.source == nil || source.source.font == nil || len(source.glyphs) == 0 ||
		len(source.glyphs) != len(source.source.glyphs) || uint64(len(source.glyphs)) > uint64(^uint32(0)) {
		return fontError(CodeOutlineSetInputInvalid, "outline_set/source", ErrOutlineSetInputInvalid)
	}
	r := source.receipt
	o := source.source.receipt
	if r.SchemaVersion != GeometryReceiptSchemaVersion || r.Profile != GeometryReceiptProfileV1 ||
		r.DescriptorHashProfile != GeometryDescriptorHashProfileV1 || r.DescriptorHash != GeometryDescriptorHash() ||
		r.GeometryHashProfile != GeometryHashProfileV1 || r.GeometryHash == "" ||
		r.SourceProfile != o.Profile || r.SourceDescriptorHashProfile != o.DescriptorHashProfile ||
		r.SourceDescriptorHash != o.DescriptorHash || r.SourceGraphHashProfile != o.GraphHashProfile ||
		r.SourceGraphHash != o.GraphHash || r.SourceCanonicalBinaryBytes != o.CanonicalBinaryBytes ||
		r.SourceRetainedBytes != o.RetainedBytes || r.UpstreamContentHashProfile != o.UpstreamContentProfile ||
		r.UpstreamContentHash != o.UpstreamContentHash || r.GlyphCount != uint32(len(source.glyphs)) ||
		r.GeometryStatus != GeometryValidatedStatusV1 || r.BBoxStatus != GeometryBBoxStatusV1 ||
		r.HeadBBoxStatus != GeometryHeadBBoxStatusV1 || r.HheaStatus != GeometryHheaStatusV1 ||
		(r.HeadLSBStatus != GeometryHeadLSBSetV1 && r.HeadLSBStatus != GeometryHeadLSBUnsetV1) ||
		r.MetricsStatus != GeometryMetricsStatusV1 || r.HintingStatus != GeometryHintingStatusV1 ||
		r.LayoutStatus != GeometryLayoutStatusV1 || r.CommandStatus != GeometryCommandStatusV1 ||
		r.CapabilityStatus != GeometryCapabilityStatusV1 || r.ExactBitsLimit == 0 ||
		r.MaxObservedExactBits > r.ExactBitsLimit ||
		r.CanonicalBinaryBytes == 0 || r.WorkUnits == 0 || r.WorkingBytes == 0 || r.RetainedBytes == 0 {
		return fontError(CodeOutlineSetInputInvalid, "outline_set/source/receipt", ErrOutlineSetInputInvalid)
	}
	if r.ExpandedPointInstances != o.ExpandedPointInstances ||
		r.ExpandedContourInstances != o.ExpandedContourInstances || r.ExpandedEdgeVisits != o.ExpandedEdgeVisits ||
		r.MaxComponentDepth != o.ObservedMaxComponentDepth || r.ExpandedGlyphInstances < uint64(r.GlyphCount) {
		return fontError(CodeOutlineSetInputInvalid, "outline_set/source/counts", ErrOutlineSetInputInvalid)
	}
	exactLower, ok := geometryExactOperationLowerBound(source.source)
	if !ok || r.ExactOpCount < exactLower || r.ExactLimbUnits < r.ExactOpCount {
		return fontError(CodeOutlineSetInputInvalid, "outline_set/source/exact_counts", ErrOutlineSetInputInvalid)
	}
	topology := geometryTopologyStats{
		expandedGlyphInstances: r.ExpandedGlyphInstances, maxExpandedGlyphsPerRoot: r.MaxExpandedGlyphInstancesPerRoot,
		transformPointVisits: r.TransformPointVisits, maxTransformVisitsPerGlyph: r.MaxTransformVisitsPerGlyph,
		maxExpandedPointsPerGlyph:   r.MaxExpandedPointsPerGlyph,
		maxExpandedContoursPerGlyph: r.MaxExpandedContoursPerGlyph, maxComponentDepth: r.MaxComponentDepth,
	}
	expectedWork, workOK := geometryWorkBase(source.source, topology, r.ExactOpCount, r.ExactLimbUnits)
	if workOK {
		expectedWork, workOK = checkedAdd(expectedWork, r.CanonicalBinaryBytes)
	}
	expectedWorking, workingOK := geometryWorkingBytes(uint64(r.GlyphCount), topology, r.ExactBitsLimit)
	expectedRetained, retainedOK := geometryRetainedBytes(source.source, r)
	if !workOK || expectedWork != r.WorkUnits || !workingOK || expectedWorking != r.WorkingBytes ||
		!retainedOK || expectedRetained != r.RetainedBytes || r.ExactBitsLimit > HardMaxGeometryExactBits {
		return fontError(CodeOutlineSetInputInvalid, "outline_set/source/resource_binding", ErrOutlineSetInputInvalid)
	}
	return nil
}

func outlineSetPreflightSource(source *GeometryFont, limits OutlineSetLimits) (geometryTopologyStats, uint64, error) {
	if err := validateOutlineSetSourceReceipt(source); err != nil {
		return geometryTopologyStats{}, 0, err
	}
	r := source.receipt
	if r.ExpandedPointInstances > limits.MaxExpandedPointInstances ||
		r.ExpandedContourInstances > limits.MaxExpandedContourInstances || r.ExpandedEdgeVisits > limits.MaxExpandedEdgeVisits ||
		r.ExpandedGlyphInstances > limits.MaxExpandedGlyphInstances || r.TransformPointVisits > limits.MaxTransformPointVisits {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/source_counts")
	}
	topology := geometryTopologyStats{
		expandedGlyphInstances: r.ExpandedGlyphInstances, maxExpandedGlyphsPerRoot: r.MaxExpandedGlyphInstancesPerRoot,
		transformPointVisits: r.TransformPointVisits, maxTransformVisitsPerGlyph: r.MaxTransformVisitsPerGlyph,
		maxExpandedPointsPerGlyph:   r.MaxExpandedPointsPerGlyph,
		maxExpandedContoursPerGlyph: r.MaxExpandedContoursPerGlyph, maxComponentDepth: r.MaxComponentDepth,
	}
	working, ok := outlineSetWorkingBytes(uint64(r.GlyphCount), topology, limits.MaxExactBits)
	if !ok || working > limits.MaxWorkingBytes {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/working")
	}
	minimumCommands, ok := checkedMultiply(r.ExpandedContourInstances, 2)
	if !ok {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/minimum_commands")
	}
	retained, ok := outlineSetRetainedLowerBound(source, uint64(r.GlyphCount), minimumCommands)
	if !ok || retained > limits.MaxRetainedBytes {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/retained_lower_bound")
	}
	minimumTopology := outlineSetTopologyStats{commands: minimumCommands, commandPoints: r.ExpandedContourInstances}
	outputBytes, ok := outlineSetCanonicalBytes(source, minimumTopology)
	if !ok || outputBytes > limits.MaxCanonicalBinaryBytes {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/canonical_lower_bound")
	}
	materialOps, ok := checkedMultiply(r.ExpandedPointInstances, 2)
	if !ok {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/exact_lower_bound")
	}
	exactLower, ok := checkedAdd(r.ExactOpCount, materialOps)
	if !ok {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/exact_lower_bound")
	}
	limbLower, ok := checkedAdd(r.ExactLimbUnits, materialOps)
	if !ok || exactLower > limits.MaxExactOps || limbLower > limits.MaxExactLimbUnits {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/exact_lower_bound")
	}
	canonicalLower, ok := checkedAdd(r.CanonicalBinaryBytes, outputBytes)
	if !ok {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/canonical_lower_bound")
	}
	work, ok := outlineSetWorkBase(source, topology, minimumTopology, exactLower, limbLower, canonicalLower)
	if !ok || work > limits.MaxWorkUnits {
		return geometryTopologyStats{}, 0, outlineSetResource("outline_set/resources/source_rebind_work_lower_bound")
	}
	return topology, working, nil
}

func validateOutlineSetSourceGraphAndTopology(ctx context.Context, source *GeometryFont, limits OutlineSetLimits,
	preflight geometryTopologyStats, working uint64) (geometryTopologyStats, error) {
	canonical, err := validateGeometrySource(ctx, source.source)
	if err != nil {
		return geometryTopologyStats{}, err
	}
	if canonical != source.receipt.SourceCanonicalBinaryBytes {
		return geometryTopologyStats{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/graph_canonical", ErrOutlineSetInputInvalid)
	}
	geometryLimits := GeometryLimits{
		MaxExpandedPointInstances:   limits.MaxExpandedPointInstances,
		MaxExpandedContourInstances: limits.MaxExpandedContourInstances,
		MaxExpandedEdgeVisits:       limits.MaxExpandedEdgeVisits,
		MaxExpandedGlyphInstances:   limits.MaxExpandedGlyphInstances,
		MaxTransformPointVisits:     limits.MaxTransformPointVisits,
	}
	topology, err := computeGeometryTopology(ctx, source.source, geometryLimits)
	if err != nil {
		return geometryTopologyStats{}, err
	}
	if topology != preflight {
		return geometryTopologyStats{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/topology_binding", ErrOutlineSetInputInvalid)
	}
	verifiedWorking, ok := outlineSetWorkingBytes(uint64(source.receipt.GlyphCount), topology, limits.MaxExactBits)
	if !ok || verifiedWorking != working {
		return geometryTopologyStats{}, outlineSetInvariant("outline_set/resources/working_binding")
	}
	r := source.receipt
	expectedWork, ok := geometryWorkBase(source.source, topology, r.ExactOpCount, r.ExactLimbUnits)
	if ok {
		expectedWork, ok = checkedAdd(expectedWork, r.CanonicalBinaryBytes)
	}
	if !ok || expectedWork != r.WorkUnits {
		return geometryTopologyStats{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/work_binding", ErrOutlineSetInputInvalid)
	}
	expectedWorking, ok := geometryWorkingBytes(uint64(r.GlyphCount), topology, r.ExactBitsLimit)
	if !ok || expectedWorking != r.WorkingBytes {
		return geometryTopologyStats{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/working_binding", ErrOutlineSetInputInvalid)
	}
	expectedRetained, ok := geometryRetainedBytes(source.source, r)
	if !ok || expectedRetained != r.RetainedBytes {
		return geometryTopologyStats{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/retained_binding", ErrOutlineSetInputInvalid)
	}
	return topology, nil
}
