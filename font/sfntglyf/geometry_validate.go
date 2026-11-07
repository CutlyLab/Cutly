package sfntglyf

import (
	"context"
	"fmt"
)

// ValidateOutlineGeometry expands every real raw control point with exact
// dyadic arithmetic and validates glyph, head, hhea and USE_MY_METRICS
// declarations. It deliberately does not synthesize implied points or publish
// commands, Q26.6 coordinates, shaping, layout, raster or text capabilities.
func ValidateOutlineGeometry(ctx context.Context, source *OutlineFont, limits GeometryLimits) (result *GeometryFont, receipt GeometryReceipt, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = GeometryReceipt{}
			err = fontError(CodeAllocationFailed, "geometry/allocation", ErrAllocationFailed)
		}
	}()
	if err := contextFailure(ctx, "geometry/context"); err != nil {
		return nil, GeometryReceipt{}, err
	}
	if source == nil {
		return nil, GeometryReceipt{}, fontError(CodeGeometryInputInvalid, "geometry/source", ErrGeometryInputInvalid)
	}
	normalized, err := normalizeGeometryLimits(limits)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	sourceRebindWorkLower, ok := geometrySourceRebindWorkLowerBound(source)
	if !ok || sourceRebindWorkLower > normalized.MaxWorkUnits {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/source_rebind_work_lower_bound", ErrResourceLimit)
	}
	sourceCanonicalBytes, err := validateGeometrySource(ctx, source)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	declarations, err := readGeometryDeclarations(source)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	headLSBStatus := GeometryHeadLSBUnsetV1
	if declarations.headFlags&0x0002 != 0 {
		headLSBStatus = GeometryHeadLSBSetV1
	}
	retainedLower, ok := geometryRetainedLowerBound(source)
	if !ok || retainedLower > normalized.MaxRetainedBytes {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/retained_lower_bound", ErrResourceLimit)
	}
	canonicalLower, ok := geometryCanonicalLowerBound(source, headLSBStatus)
	if !ok || canonicalLower > normalized.MaxCanonicalBinaryBytes {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/canonical_lower_bound", ErrResourceLimit)
	}
	// The working formula's maxima are already bound by the accepted phase-one
	// receipt. Gate the exact O(G) topology scratch before allocating its slices.
	preflightTopology := geometryTopologyStats{
		maxExpandedPointsPerGlyph:   uint64(source.receipt.ObservedMaxPoints),
		maxExpandedContoursPerGlyph: uint64(source.receipt.ObservedMaxContours),
		maxComponentDepth:           source.receipt.ObservedMaxComponentDepth,
	}
	if uint64(source.receipt.ObservedMaxCompositePoints) > preflightTopology.maxExpandedPointsPerGlyph {
		preflightTopology.maxExpandedPointsPerGlyph = uint64(source.receipt.ObservedMaxCompositePoints)
	}
	if uint64(source.receipt.ObservedMaxCompositeContours) > preflightTopology.maxExpandedContoursPerGlyph {
		preflightTopology.maxExpandedContoursPerGlyph = uint64(source.receipt.ObservedMaxCompositeContours)
	}
	preflightTopology.expandedGlyphInstances, ok = checkedAdd(uint64(len(source.glyphs)), source.receipt.ExpandedEdgeVisits)
	if !ok || preflightTopology.expandedGlyphInstances > normalized.MaxExpandedGlyphInstances {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/expanded_glyph_lower_bound", ErrResourceLimit)
	}
	working, ok := geometryWorkingBytes(uint64(len(source.glyphs)), preflightTopology, normalized.MaxExactBits)
	if !ok || working > normalized.MaxWorkingBytes {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/working", ErrResourceLimit)
	}
	exactOperationLower, ok := geometryExactOperationLowerBound(source)
	if !ok || exactOperationLower > normalized.MaxExactOps || exactOperationLower > normalized.MaxExactLimbUnits {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/exact_lower_bound", ErrResourceLimit)
	}
	lowerWork, ok := geometryWorkBase(source, preflightTopology, exactOperationLower, exactOperationLower)
	if ok {
		lowerWork, ok = checkedAdd(lowerWork, canonicalLower)
	}
	if !ok || lowerWork > normalized.MaxWorkUnits {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/work_lower_bound", ErrResourceLimit)
	}
	topology, err := computeGeometryTopology(ctx, source, normalized)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	verifiedWorking, ok := geometryWorkingBytes(uint64(len(source.glyphs)), topology, normalized.MaxExactBits)
	if !ok || verifiedWorking != working || topology.expandedGlyphInstances != preflightTopology.expandedGlyphInstances {
		return nil, GeometryReceipt{}, fontError(CodeGeometryInvariant, "geometry/resources/working_binding", ErrInvariantFailed)
	}
	workMeter, err := newGeometryWorkMeter(source, topology, exactOperationLower, canonicalLower, normalized.MaxWorkUnits)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	metricSources, metricRedirects, err := resolveGeometryMetricSources(ctx, source)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	glyphs := make([]geometryGlyph, len(source.glyphs))
	exact := newGeometryExactContextWithWork(ctx, normalized.MaxExactBits, normalized.MaxExactOps, normalized.MaxExactLimbUnits, workMeter)
	hashWriter, err := newGeometryHashWriterWithWork(ctx, source, uint32(len(source.glyphs)), normalized.MaxCanonicalBinaryBytes, workMeter)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	global := geometryGlobalMetrics{}
	for glyphID, outline := range source.glyphs {
		if glyphID%64 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("geometry/glyphs/%d", glyphID)); err != nil {
				return nil, GeometryReceipt{}, err
			}
		}
		expanded, err := expandExactGlyph(ctx, source, uint32(glyphID), exact)
		if err != nil {
			return nil, GeometryReceipt{}, err
		}
		xMin, yMin, xMax, yMax, err := exactGeometryBBox(exact, expanded, uint32(glyphID))
		if err != nil {
			return nil, GeometryReceipt{}, err
		}
		if xMin != outline.xMin || yMin != outline.yMin || xMax != outline.xMax || yMax != outline.yMax {
			return nil, GeometryReceipt{}, fontError(CodeGeometryInvalid, fmt.Sprintf("geometry/glyphs/%d/bbox", glyphID), ErrGeometryInvalid)
		}
		metric, ok := source.font.HorizontalMetric(uint32(glyphID))
		if !ok {
			return nil, GeometryReceipt{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/glyphs/%d/hmtx", glyphID), ErrInvariantFailed)
		}
		verified := geometryGlyph{
			kind: outline.kind, hasContours: len(expanded.endPoints) > 0,
			xMin: xMin, yMin: yMin, xMax: xMax, yMax: yMax,
			advanceWidth: metric.AdvanceWidth, leftSideBearing: metric.LeftSideBearing,
			effectiveMetricSource: metricSources[glyphID],
		}
		if declarations.headFlags&0x0002 != 0 && verified.xMin != verified.leftSideBearing {
			return nil, GeometryReceipt{}, fontError(CodeGeometryMetrics, fmt.Sprintf("geometry/glyphs/%d/head_lsb", glyphID), ErrGeometryMetrics)
		}
		if err := global.addGlyph(verified); err != nil {
			return nil, GeometryReceipt{}, err
		}
		if err := hashWriter.writeGlyph(uint32(glyphID), outline, expanded, verified); err != nil {
			return nil, GeometryReceipt{}, err
		}
		glyphs[glyphID] = verified
	}
	if err := validateGeometryDeclarations(declarations, global); err != nil {
		return nil, GeometryReceipt{}, err
	}
	geometryHash, canonicalBytes, err := hashWriter.finish(
		global.headXMin, global.headYMin, global.headXMax, global.headYMax,
		global.minLSB, global.minRSB, global.xExtent, headLSBStatus,
	)
	if err != nil {
		return nil, GeometryReceipt{}, err
	}
	exactStats := exact.Stats()
	work, ok := geometryWorkBase(source, topology, exactStats.OperationCount, exactStats.LimbUnits)
	if ok {
		work, ok = checkedAdd(work, canonicalBytes)
	}
	if !ok || work > normalized.MaxWorkUnits {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/work", ErrResourceLimit)
	}
	meterWork, meterOK := workMeter.bindFinal(exactStats.OperationCount, exactStats.LimbUnits, canonicalBytes)
	if !meterOK || meterWork != work {
		return nil, GeometryReceipt{}, fontError(CodeGeometryInvariant, "geometry/resources/work_binding", ErrInvariantFailed)
	}
	receipt = GeometryReceipt{
		SchemaVersion: GeometryReceiptSchemaVersion, Profile: GeometryReceiptProfileV1,
		SourceProfile:               source.receipt.Profile,
		SourceDescriptorHashProfile: source.receipt.DescriptorHashProfile,
		SourceDescriptorHash:        source.receipt.DescriptorHash,
		SourceGraphHashProfile:      source.receipt.GraphHashProfile,
		SourceGraphHash:             source.receipt.GraphHash,
		SourceCanonicalBinaryBytes:  sourceCanonicalBytes,
		SourceRetainedBytes:         source.receipt.RetainedBytes,
		UpstreamContentHashProfile:  source.receipt.UpstreamContentProfile,
		UpstreamContentHash:         source.receipt.UpstreamContentHash,
		DescriptorHashProfile:       GeometryDescriptorHashProfileV1, DescriptorHash: GeometryDescriptorHash(),
		GeometryHashProfile: GeometryHashProfileV1, GeometryHash: geometryHash,
		GeometryStatus: GeometryValidatedStatusV1, BBoxStatus: GeometryBBoxStatusV1,
		HeadBBoxStatus: GeometryHeadBBoxStatusV1, HheaStatus: GeometryHheaStatusV1,
		HeadLSBStatus: headLSBStatus, MetricsStatus: GeometryMetricsStatusV1,
		HintingStatus: GeometryHintingStatusV1, LayoutStatus: GeometryLayoutStatusV1,
		CommandStatus: GeometryCommandStatusV1, CapabilityStatus: GeometryCapabilityStatusV1,
		GlyphCount: uint32(len(glyphs)), GlyphsWithContours: global.glyphsWithContours,
		MetricRedirectGlyphCount:         metricRedirects,
		ExpandedPointInstances:           source.receipt.ExpandedPointInstances,
		ExpandedContourInstances:         source.receipt.ExpandedContourInstances,
		ExpandedEdgeVisits:               source.receipt.ExpandedEdgeVisits,
		ExpandedGlyphInstances:           topology.expandedGlyphInstances,
		TransformPointVisits:             topology.transformPointVisits,
		MaxExpandedGlyphInstancesPerRoot: topology.maxExpandedGlyphsPerRoot,
		MaxTransformVisitsPerGlyph:       topology.maxTransformVisitsPerGlyph,
		MaxExpandedPointsPerGlyph:        topology.maxExpandedPointsPerGlyph,
		MaxExpandedContoursPerGlyph:      topology.maxExpandedContoursPerGlyph,
		MaxComponentDepth:                topology.maxComponentDepth,
		ExactBitsLimit:                   normalized.MaxExactBits, ExactOpCount: exactStats.OperationCount,
		ExactLimbUnits: exactStats.LimbUnits, MaxObservedExactBits: exactStats.MaxExactBits,
		HeadXMin: global.headXMin, HeadYMin: global.headYMin,
		HeadXMax: global.headXMax, HeadYMax: global.headYMax,
		MinLSB: global.minLSB, MinRSB: global.minRSB, XExtent: global.xExtent,
		CanonicalBinaryBytes: canonicalBytes, WorkUnits: work, WorkingBytes: working,
	}
	retained, ok := geometryRetainedBytes(source, receipt)
	if !ok || retained > normalized.MaxRetainedBytes {
		return nil, GeometryReceipt{}, fontError(CodeGeometryResourceLimit, "geometry/resources/retained", ErrResourceLimit)
	}
	receipt.RetainedBytes = retained
	if geometryReceiptStringBytes(receipt) == 0 || receipt.GeometryHash == "" {
		return nil, GeometryReceipt{}, fontError(CodeGeometryInvariant, "geometry/receipt", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, "geometry/publish"); err != nil {
		return nil, GeometryReceipt{}, err
	}
	result = &GeometryFont{source: source, glyphs: glyphs, receipt: receipt}
	return result, result.Receipt(), nil
}
