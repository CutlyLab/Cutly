package sfntglyf

import (
	"context"
	"errors"
	"fmt"
)

// MaterializeOutlineSet revalidates exact real-control geometry, synthesizes
// canonical TrueType implied points and publishes immutable font-space Q26.6
// Move/Line/Quad/Close commands. It performs no shaping, layout or raster work.
func MaterializeOutlineSet(ctx context.Context, source *GeometryFont, limits OutlineSetLimits) (result *OutlineSet, receipt OutlineSetReceipt, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, receipt = nil, OutlineSetReceipt{}
			err = fontError(CodeAllocationFailed, "outline_set/allocation", ErrAllocationFailed)
			return
		}
		if err != nil {
			result, receipt = nil, OutlineSetReceipt{}
			err = normalizeOutlineSetError(err)
		}
	}()
	if err := contextFailure(ctx, "outline_set/context"); err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	if source == nil {
		return nil, OutlineSetReceipt{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source", ErrOutlineSetInputInvalid)
	}
	normalized, err := normalizeOutlineSetLimits(limits)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	preflight, working, err := outlineSetPreflightSource(source, normalized)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	topology, err := validateOutlineSetSourceGraphAndTopology(ctx, source, normalized, preflight, working)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	commandTopology, err := computeOutlineSetTopology(ctx, source.source, normalized)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	canonicalBytes, ok := outlineSetCanonicalBytes(source, commandTopology)
	if !ok || canonicalBytes > normalized.MaxCanonicalBinaryBytes {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/canonical_bytes")
	}
	retainedLower, ok := outlineSetRetainedLowerBound(source, uint64(source.receipt.GlyphCount), commandTopology.commands)
	if !ok || retainedLower > normalized.MaxRetainedBytes {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/retained")
	}
	materialExactLower, ok := outlineSetMaterializationExactLower(source, commandTopology)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/exact_lower_bound")
	}
	exactLower, ok := checkedAdd(source.receipt.ExactOpCount, materialExactLower)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/exact_lower_bound")
	}
	limbLower, ok := checkedAdd(source.receipt.ExactLimbUnits, materialExactLower)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/limb_lower_bound")
	}
	canonicalTotalLower, ok := checkedAdd(source.receipt.CanonicalBinaryBytes, canonicalBytes)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/canonical_lower_bound")
	}
	work, err := newOutlineSetWorkMeter(source, topology, commandTopology, exactLower, limbLower, canonicalTotalLower, normalized)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}

	commands := make([]outlineSetCommand, 0, int(commandTopology.commands))
	glyphs := make([]outlineSetGlyphRange, len(source.glyphs))
	declarations, err := readGeometryDeclarations(source.source)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	headLSBStatus := GeometryHeadLSBUnsetV1
	if declarations.headFlags&0x0002 != 0 {
		headLSBStatus = GeometryHeadLSBSetV1
	}
	metricSources, metricRedirects, err := resolveGeometryMetricSources(ctx, source.source)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	if metricRedirects != source.receipt.MetricRedirectGlyphCount {
		return nil, OutlineSetReceipt{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/metric_redirects", ErrOutlineSetInputInvalid)
	}
	sourceExact := newGeometryExactContextWithWork(ctx, normalized.MaxExactBits, normalized.MaxExactOps, normalized.MaxExactLimbUnits, work)
	commandExact := newGeometryExactContextWithWork(ctx, normalized.MaxExactBits, normalized.MaxExactOps, normalized.MaxExactLimbUnits, work)
	sourceHash, err := newGeometryHashWriterWithWork(ctx, source.source, source.receipt.GlyphCount,
		source.receipt.CanonicalBinaryBytes, work)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	global := geometryGlobalMetrics{}
	for glyphIndex, raw := range source.source.glyphs {
		if glyphIndex%64 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline_set/glyphs/%d", glyphIndex)); err != nil {
				return nil, OutlineSetReceipt{}, err
			}
		}
		expanded, err := expandExactGlyph(ctx, source.source, uint32(glyphIndex), sourceExact)
		if err != nil {
			return nil, OutlineSetReceipt{}, err
		}
		xMin, yMin, xMax, yMax, err := exactGeometryBBox(sourceExact, expanded, uint32(glyphIndex))
		if err != nil {
			return nil, OutlineSetReceipt{}, err
		}
		metric, ok := source.source.font.HorizontalMetric(uint32(glyphIndex))
		if !ok {
			return nil, OutlineSetReceipt{}, outlineSetInvariant("outline_set/source/hmtx")
		}
		verified := geometryGlyph{
			kind: raw.kind, hasContours: len(expanded.endPoints) > 0,
			xMin: xMin, yMin: yMin, xMax: xMax, yMax: yMax,
			advanceWidth: metric.AdvanceWidth, leftSideBearing: metric.LeftSideBearing,
			effectiveMetricSource: metricSources[glyphIndex],
		}
		if verified != source.glyphs[glyphIndex] {
			return nil, OutlineSetReceipt{}, fontError(CodeOutlineSetInputInvalid, fmt.Sprintf("outline_set/source/glyphs/%d", glyphIndex), ErrOutlineSetInputInvalid)
		}
		if declarations.headFlags&0x0002 != 0 && verified.xMin != verified.leftSideBearing {
			return nil, OutlineSetReceipt{}, fontError(CodeOutlineSetInputInvalid, fmt.Sprintf("outline_set/source/glyphs/%d/head_lsb", glyphIndex), ErrOutlineSetInputInvalid)
		}
		if err := global.addGlyph(verified); err != nil {
			return nil, OutlineSetReceipt{}, err
		}
		if err := sourceHash.writeGlyph(uint32(glyphIndex), raw, expanded, verified); err != nil {
			return nil, OutlineSetReceipt{}, err
		}
		start := uint64(len(commands))
		if err := materializeOutlineSetGlyph(ctx, uint32(glyphIndex), expanded, commandTopology.glyphs[glyphIndex], commandExact, &commands); err != nil {
			return nil, OutlineSetReceipt{}, err
		}
		count := uint64(len(commands)) - start
		glyphs[glyphIndex] = outlineSetGlyphRange{commandStart: start, commandCount: count, contours: uint64(len(expanded.endPoints))}
	}
	if uint64(len(commands)) != commandTopology.commands {
		return nil, OutlineSetReceipt{}, outlineSetInvariant("outline_set/commands/count")
	}
	if err := validateGeometryDeclarations(declarations, global); err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	if global.glyphsWithContours != source.receipt.GlyphsWithContours ||
		global.headXMin != source.receipt.HeadXMin || global.headYMin != source.receipt.HeadYMin ||
		global.headXMax != source.receipt.HeadXMax || global.headYMax != source.receipt.HeadYMax ||
		global.minLSB != source.receipt.MinLSB || global.minRSB != source.receipt.MinRSB || global.xExtent != source.receipt.XExtent ||
		headLSBStatus != source.receipt.HeadLSBStatus {
		return nil, OutlineSetReceipt{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/global_metrics", ErrOutlineSetInputInvalid)
	}
	sourceGeometryHash, sourceGeometryBytes, err := sourceHash.finish(
		global.headXMin, global.headYMin, global.headXMax, global.headYMax,
		global.minLSB, global.minRSB, global.xExtent, headLSBStatus)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	sourceStats := sourceExact.Stats()
	if sourceGeometryHash != source.receipt.GeometryHash || sourceGeometryBytes != source.receipt.CanonicalBinaryBytes ||
		sourceStats.OperationCount != source.receipt.ExactOpCount || sourceStats.LimbUnits != source.receipt.ExactLimbUnits ||
		sourceStats.MaxExactBits != source.receipt.MaxObservedExactBits {
		return nil, OutlineSetReceipt{}, fontError(CodeOutlineSetInputInvalid, "outline_set/source/geometry_binding", ErrOutlineSetInputInvalid)
	}
	commandStats := commandExact.Stats()
	if commandStats.OperationCount != materialExactLower {
		return nil, OutlineSetReceipt{}, outlineSetInvariant("outline_set/commands/exact_operation_binding")
	}
	hashWriter, err := newOutlineSetHashWriter(ctx, source, normalized.MaxCanonicalBinaryBytes, work)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	for glyphID, glyph := range glyphs {
		if err := hashWriter.writeGlyph(uint32(glyphID), glyph, commands); err != nil {
			return nil, OutlineSetReceipt{}, err
		}
	}
	outlineHash, observedCanonical, err := hashWriter.finish(source, commandTopology)
	if err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	if observedCanonical != canonicalBytes {
		return nil, OutlineSetReceipt{}, outlineSetInvariant("outline_set/hash/canonical_binding")
	}
	totalOps, ok := checkedAdd(sourceStats.OperationCount, commandStats.OperationCount)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/exact_operations")
	}
	totalLimbs, ok := checkedAdd(sourceStats.LimbUnits, commandStats.LimbUnits)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/exact_limbs")
	}
	canonicalTotal, ok := checkedAdd(sourceGeometryBytes, observedCanonical)
	if !ok {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/canonical_total")
	}
	computedWork, ok := outlineSetWorkBase(source, topology, commandTopology, totalOps, totalLimbs, canonicalTotal)
	if !ok || computedWork > normalized.MaxWorkUnits {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/work")
	}
	meterWork, meterOK := work.bindFinal(totalOps, totalLimbs, canonicalTotal)
	if !meterOK || meterWork != computedWork {
		return nil, OutlineSetReceipt{}, outlineSetInvariant("outline_set/resources/work_binding")
	}
	maxBits := sourceStats.MaxExactBits
	if commandStats.MaxExactBits > maxBits {
		maxBits = commandStats.MaxExactBits
	}
	receipt = OutlineSetReceipt{
		SchemaVersion: OutlineSetReceiptSchemaVersion, Profile: OutlineSetReceiptProfileV1,
		OutlineProfile: OutlineSetOutlineProfileV1,
		SourceProfile:  source.receipt.Profile, SourceDescriptorHashProfile: source.receipt.DescriptorHashProfile,
		SourceDescriptorHash: source.receipt.DescriptorHash, SourceGeometryHashProfile: source.receipt.GeometryHashProfile,
		SourceGeometryHash: source.receipt.GeometryHash, SourceGeometryCanonicalBinaryBytes: sourceGeometryBytes,
		SourceGraphHashProfile: source.receipt.SourceGraphHashProfile, SourceGraphHash: source.receipt.SourceGraphHash,
		SourceGraphCanonicalBinaryBytes: source.receipt.SourceCanonicalBinaryBytes,
		SourceRetainedBytes:             source.receipt.RetainedBytes, UpstreamContentHashProfile: source.receipt.UpstreamContentHashProfile,
		UpstreamContentHash:   source.receipt.UpstreamContentHash,
		DescriptorHashProfile: OutlineSetDescriptorHashProfileV1, DescriptorHash: OutlineSetDescriptorHash(),
		OutlineSetHashProfile: OutlineSetHashProfileV1, OutlineSetHash: outlineHash,
		GeometryStatus: OutlineSetGeometryStatusV1, ImpliedPointStatus: OutlineSetImpliedPointStatusV1,
		CommandStatus: OutlineSetCommandStatusV1, RoundingStatus: OutlineSetRoundingStatusV1,
		HintingStatus: OutlineSetHintingStatusV1, LayoutStatus: OutlineSetLayoutStatusV1,
		RasterStatus: OutlineSetRasterStatusV1, CapabilityStatus: OutlineSetCapabilityStatusV1,
		GlyphCount: source.receipt.GlyphCount, GlyphsWithContours: commandTopology.glyphsWithContours,
		ExpandedPointInstances:   source.receipt.ExpandedPointInstances,
		ExpandedContourInstances: source.receipt.ExpandedContourInstances,
		ExpandedEdgeVisits:       source.receipt.ExpandedEdgeVisits,
		ExpandedGlyphInstances:   topology.expandedGlyphInstances, TransformPointVisits: topology.transformPointVisits,
		MaxExpandedGlyphInstancesPerRoot: topology.maxExpandedGlyphsPerRoot,
		MaxTransformVisitsPerGlyph:       topology.maxTransformVisitsPerGlyph,
		MaxExpandedPointsPerGlyph:        topology.maxExpandedPointsPerGlyph,
		MaxExpandedContoursPerGlyph:      topology.maxExpandedContoursPerGlyph, MaxComponentDepth: topology.maxComponentDepth,
		ImpliedPointInstances: commandTopology.implied, LineCommandInstances: commandTopology.lines,
		QuadCommandInstances: commandTopology.quads, DrawingCommandInstances: commandTopology.drawing,
		CommandInstances: commandTopology.commands, CommandPointOccurrences: commandTopology.commandPoints,
		MaxImpliedPointsPerGlyph:           commandTopology.maxImpliedPerGlyph,
		MaxDrawingCommandsPerGlyph:         commandTopology.maxDrawingPerGlyph,
		MaxCommandsPerGlyph:                commandTopology.maxCommandsPerGlyph,
		MaxCommandPointOccurrencesPerGlyph: commandTopology.maxCommandPointsPerGlyph,
		ExactBitsLimit:                     normalized.MaxExactBits, SourceExactOpCount: sourceStats.OperationCount,
		SourceExactLimbUnits: sourceStats.LimbUnits, SourceMaxObservedExactBits: sourceStats.MaxExactBits,
		MaterializationExactOpCount:   commandStats.OperationCount,
		MaterializationExactLimbUnits: commandStats.LimbUnits,
		MaterializationMaxExactBits:   commandStats.MaxExactBits,
		ExactOpCount:                  totalOps, ExactLimbUnits: totalLimbs, MaxObservedExactBits: maxBits,
		CanonicalBinaryBytes: observedCanonical, WorkUnits: computedWork, WorkingBytes: working,
	}
	retained, ok := outlineSetRetainedBytes(source, uint64(receipt.GlyphCount), receipt.CommandInstances, receipt)
	if !ok || retained > normalized.MaxRetainedBytes {
		return nil, OutlineSetReceipt{}, outlineSetResource("outline_set/resources/retained")
	}
	receipt.RetainedBytes = retained
	if outlineSetReceiptStringBytes(receipt) == 0 || receipt.OutlineSetHash == "" {
		return nil, OutlineSetReceipt{}, outlineSetInvariant("outline_set/receipt")
	}
	if err := contextFailure(ctx, "outline_set/publish"); err != nil {
		return nil, OutlineSetReceipt{}, err
	}
	result = &OutlineSet{source: source, glyphs: glyphs, commands: commands, receipt: receipt}
	return result, result.Receipt(), nil
}

func materializeOutlineSetGlyph(ctx context.Context, glyphID uint32, exact exactGlyphGeometry, expected outlineSetGlyphStats,
	mathContext *geometryExactContext, commands *[]outlineSetCommand) error {
	if commands == nil {
		return outlineSetInvariant("outline_set/materialize/state")
	}
	sink := outlineSetSliceCommandSink{target: commands}
	return materializeOutlineSetGlyphToSink(ctx, glyphID, exact, expected, mathContext, &sink)
}

type outlineSetCommandSink interface {
	commandCount() uint64
	emitOutlineSetCommand(command outlineSetCommand) error
}

type outlineSetSliceCommandSink struct {
	target *[]outlineSetCommand
}

func (value *outlineSetSliceCommandSink) commandCount() uint64 {
	if value == nil || value.target == nil {
		return 0
	}
	return uint64(len(*value.target))
}

func (value *outlineSetSliceCommandSink) emitOutlineSetCommand(command outlineSetCommand) error {
	if value == nil {
		return outlineSetInvariant("outline_set/materialize/state")
	}
	return appendOutlineSetCommand(value.target, command)
}

func materializeOutlineSetGlyphToSink(ctx context.Context, glyphID uint32, exact exactGlyphGeometry, expected outlineSetGlyphStats,
	mathContext *geometryExactContext, commands outlineSetCommandSink) error {
	if commands == nil || mathContext == nil {
		return outlineSetInvariant("outline_set/materialize/state")
	}
	startCommand := commands.commandCount()
	var observed outlineSetGlyphStats
	startPoint := uint64(0)
	for contourIndex, encodedEnd := range exact.endPoints {
		if contourIndex%256 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline_set/materialize/%d/contours/%d", glyphID, contourIndex)); err != nil {
				return err
			}
		}
		endPoint := uint64(encodedEnd)
		if endPoint < startPoint || endPoint >= uint64(len(exact.points)) {
			return outlineSetInvariant("outline_set/materialize/end_points")
		}
		first, last := exact.points[startPoint], exact.points[endPoint]
		var startExact exactGeometryPoint
		traverseStart, traverseEnd := startPoint, endPoint
		if first.onCurve {
			startExact = first
			traverseStart = startPoint + 1
		} else if last.onCurve {
			startExact = last
			traverseEnd = endPoint - 1
		} else {
			var err error
			startExact, err = outlineSetMidpointPoint(mathContext, last, first,
				fmt.Sprintf("outline_set/materialize/%d/contours/%d/start", glyphID, contourIndex))
			if err != nil {
				return err
			}
			observed.implied++
		}
		startRounded, err := outlineSetRoundPoint(mathContext, startExact,
			fmt.Sprintf("outline_set/materialize/%d/contours/%d/start_round", glyphID, contourIndex))
		if err != nil {
			return err
		}
		if err := commands.emitOutlineSetCommand(outlineSetCommand{op: OutlineMoveTo, pointCount: 1, points: [2]OutlinePoint{startRounded}}); err != nil {
			return err
		}
		pending := false
		var pendingExact exactGeometryPoint
		var pendingRounded OutlinePoint
		if traverseStart <= traverseEnd {
			for pointIndex := traverseStart; pointIndex <= traverseEnd; pointIndex++ {
				if (pointIndex-traverseStart)%1024 == 0 {
					if err := contextFailure(ctx, fmt.Sprintf("outline_set/materialize/%d/contours/%d/points/%d", glyphID, contourIndex, pointIndex)); err != nil {
						return err
					}
				}
				point := exact.points[pointIndex]
				rounded, err := outlineSetRoundPoint(mathContext, point,
					fmt.Sprintf("outline_set/materialize/%d/contours/%d/points/%d", glyphID, contourIndex, pointIndex))
				if err != nil {
					return err
				}
				if point.onCurve {
					if pending {
						if err := commands.emitOutlineSetCommand(outlineSetCommand{op: OutlineQuadTo, pointCount: 2, points: [2]OutlinePoint{pendingRounded, rounded}}); err != nil {
							return err
						}
						observed.quads++
						pending = false
					} else {
						if err := commands.emitOutlineSetCommand(outlineSetCommand{op: OutlineLineTo, pointCount: 1, points: [2]OutlinePoint{rounded}}); err != nil {
							return err
						}
						observed.lines++
					}
				} else if pending {
					midpoint, err := outlineSetMidpointPoint(mathContext, pendingExact, point,
						fmt.Sprintf("outline_set/materialize/%d/contours/%d/implied/%d", glyphID, contourIndex, pointIndex))
					if err != nil {
						return err
					}
					midpointRounded, err := outlineSetRoundPoint(mathContext, midpoint,
						fmt.Sprintf("outline_set/materialize/%d/contours/%d/implied/%d/round", glyphID, contourIndex, pointIndex))
					if err != nil {
						return err
					}
					if err := commands.emitOutlineSetCommand(outlineSetCommand{op: OutlineQuadTo, pointCount: 2, points: [2]OutlinePoint{pendingRounded, midpointRounded}}); err != nil {
						return err
					}
					observed.implied++
					observed.quads++
					pendingExact, pendingRounded = point, rounded
				} else {
					pending, pendingExact, pendingRounded = true, point, rounded
				}
			}
		}
		if pending {
			if err := commands.emitOutlineSetCommand(outlineSetCommand{op: OutlineQuadTo, pointCount: 2, points: [2]OutlinePoint{pendingRounded, startRounded}}); err != nil {
				return err
			}
			observed.quads++
		}
		if err := commands.emitOutlineSetCommand(outlineSetCommand{op: OutlineClose}); err != nil {
			return err
		}
		observed.contours++
		startPoint = endPoint + 1
	}
	if startPoint != uint64(len(exact.points)) {
		return outlineSetInvariant("outline_set/materialize/final_endpoint")
	}
	var ok bool
	observed.drawing, ok = checkedAdd(observed.lines, observed.quads)
	if !ok {
		return outlineSetResource("outline_set/materialize/drawing")
	}
	observed.commands = commands.commandCount() - startCommand
	observed.commandPoints, ok = outlineWeightedSum(observed.contours,
		[2]uint64{observed.lines, 1}, [2]uint64{observed.quads, 2})
	if !ok {
		return outlineSetResource("outline_set/materialize/command_points")
	}
	if observed != expected {
		return outlineSetInvariant(fmt.Sprintf("outline_set/materialize/%d/count_binding", glyphID))
	}
	return nil
}

func appendOutlineSetCommand(target *[]outlineSetCommand, command outlineSetCommand) error {
	if target == nil || len(*target) >= cap(*target) {
		return outlineSetInvariant("outline_set/materialize/capacity")
	}
	*target = append(*target, command)
	return nil
}

func normalizeOutlineSetError(err error) error {
	if err == nil || errors.Is(err, ErrCanceled) || errors.Is(err, ErrContextInvalid) || errors.Is(err, ErrAllocationFailed) ||
		errors.Is(err, ErrLimitsInvalid) || errors.Is(err, ErrOutlineSetInputInvalid) || errors.Is(err, ErrOutlineSetCoordinate) {
		return err
	}
	var typed *Error
	if errors.As(err, &typed) {
		switch typed.Code {
		case CodeOutlineSetInputInvalid, CodeOutlineSetLimitsInvalid, CodeOutlineSetResourceLimit,
			CodeOutlineSetCoordinate, CodeOutlineSetInvariant:
			return err
		}
	}
	if errors.Is(err, ErrResourceLimit) {
		return outlineSetResource("outline_set/stage")
	}
	if errors.Is(err, ErrInvariantFailed) {
		return outlineSetInvariant("outline_set/stage")
	}
	return fontError(CodeOutlineSetInputInvalid, "outline_set/source", ErrOutlineSetInputInvalid)
}
