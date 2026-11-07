package sfntglyf

import (
	"context"
	"errors"
	"fmt"
)

func validateGeometrySource(ctx context.Context, source *OutlineFont) (uint64, error) {
	if source == nil || source.font == nil {
		return 0, fontError(CodeGeometryInputInvalid, "geometry/source", ErrGeometryInputInvalid)
	}
	receipt := source.receipt
	fontReceipt := source.font.receipt
	glyfRecord, glyfFound := findRecord(source.font.tables, "glyf")
	if fontReceipt.SchemaVersion != ReceiptSchemaVersion || fontReceipt.Profile != ReceiptProfileV1 ||
		fontReceipt.ContentHashProfile != ContentHashProfileV1 || fontReceipt.ContentHash == "" ||
		fontReceipt.GlyfTableStatus != GlyfTableStatusV1 ||
		receipt.SchemaVersion != OutlineReceiptSchemaVersion || receipt.Profile != OutlineReceiptProfileV1 ||
		receipt.DescriptorHashProfile != OutlineDescriptorHashProfileV1 || receipt.DescriptorHash != OutlineDescriptorHash() ||
		receipt.GraphHashProfile != OutlineGraphHashProfileV1 || receipt.GraphHash == "" ||
		receipt.UpstreamProfile != ReceiptProfileV1 || receipt.UpstreamContentProfile != ContentHashProfileV1 ||
		receipt.UpstreamContentHash == "" || receipt.UpstreamContentHash != fontReceipt.ContentHash ||
		receipt.UpstreamContentProfile != fontReceipt.ContentHashProfile || receipt.GlyphCount != source.font.numGlyphs ||
		receipt.UpstreamRetainedBytes != fontReceipt.RetainedBytes || !glyfFound || receipt.GlyfBytes != uint64(glyfRecord.length) ||
		uint64(receipt.GlyphCount) != uint64(len(source.glyphs)) || len(source.glyphs) == 0 ||
		receipt.GeometryStatus != OutlineGeometryStatusV1 || receipt.HintingStatus != OutlineHintingStatusV1 ||
		receipt.LayoutStatus != OutlineLayoutStatusV1 || receipt.MaxpStatus != OutlineMaxpStatusV1 ||
		receipt.MetricsStatus != OutlineMetricsStatusV1 || receipt.RoundXYStatus != OutlineRoundXYStatusV1 ||
		receipt.PointMatchStatus != OutlinePointMatchStatusV1 || receipt.OffsetStatus != OutlineOffsetStatusV1 ||
		receipt.CanonicalBinaryBytes == 0 || receipt.RetainedBytes == 0 {
		return 0, fontError(CodeGeometryInputInvalid, "geometry/source/receipt", ErrGeometryInputInvalid)
	}
	if err := validateGeometrySourceCounts(source); err != nil {
		return 0, err
	}
	graphHash, canonicalBytes, err := hashOutlineGraph(ctx, source.font, source.glyphs, receipt.CanonicalBinaryBytes)
	if err != nil {
		if errors.Is(err, ErrCanceled) || errors.Is(err, ErrContextInvalid) {
			return 0, err
		}
		return 0, fontError(CodeGeometryInputInvalid, "geometry/source/graph", ErrGeometryInputInvalid)
	}
	if graphHash != receipt.GraphHash || canonicalBytes != receipt.CanonicalBinaryBytes {
		return 0, fontError(CodeGeometryInputInvalid, "geometry/source/graph_binding", ErrGeometryInputInvalid)
	}
	return canonicalBytes, nil
}

func validateGeometrySourceCounts(source *OutlineFont) error {
	var emptyRanges, zeroContours, simpleGlyphs, compoundGlyphs uint64
	var rawPoints, rawContours, components, instructions, instructionBytes, padding uint64
	var pointMatches, scaledOffsets, unscaledOffsets, defaultOffsets, useMyMetrics uint64
	var overlapSimple, overlapCompound, expandedPoints, expandedContours, expandedEdges, maxExpandedEdges uint64
	var maxPoints, maxContours, maxCompositePoints, maxCompositeContours uint32
	var maxInstructionBytes, maxComponentElements, maxComponentDepth uint32
	for glyphID, glyph := range source.glyphs {
		switch glyph.kind {
		case OutlineGlyphEmptyRange:
			emptyRanges++
			if glyph.sourceBytes != 0 || glyph.numberOfContours != 0 || len(glyph.points) != 0 || len(glyph.endPoints) != 0 || len(glyph.components) != 0 ||
				glyph.expandedPoints != 0 || glyph.expandedContours != 0 || glyph.expandedEdges != 0 {
				return geometrySourceInvariant(glyphID, "empty")
			}
		case OutlineGlyphSimple:
			simpleGlyphs++
			if glyph.numberOfContours < 0 || uint64(len(glyph.points)) != glyph.expandedPoints ||
				uint64(len(glyph.endPoints)) != glyph.expandedContours || len(glyph.components) != 0 {
				return geometrySourceInvariant(glyphID, "simple")
			}
			if glyph.numberOfContours == 0 {
				zeroContours++
			}
			if !checkedGeometrySourceAdd(&rawPoints, uint64(len(glyph.points))) || !checkedGeometrySourceAdd(&rawContours, uint64(len(glyph.endPoints))) {
				return geometrySourceInvariant(glyphID, "simple_count_overflow")
			}
			if len(glyph.points) > int(maxPoints) {
				maxPoints = uint32(len(glyph.points))
			}
			if len(glyph.endPoints) > int(maxContours) {
				maxContours = uint32(len(glyph.endPoints))
			}
			if glyph.overlapDeclared {
				overlapSimple++
			}
		case OutlineGlyphCompound:
			compoundGlyphs++
			if glyph.numberOfContours != -1 || len(glyph.points) != 0 || len(glyph.endPoints) != 0 || len(glyph.components) == 0 {
				return geometrySourceInvariant(glyphID, "compound")
			}
			if !checkedGeometrySourceAdd(&components, uint64(len(glyph.components))) {
				return geometrySourceInvariant(glyphID, "component_count_overflow")
			}
			if glyph.expandedPoints > uint64(maxCompositePoints) {
				if glyph.expandedPoints > uint64(^uint32(0)) {
					return geometrySourceInvariant(glyphID, "composite_points")
				}
				maxCompositePoints = uint32(glyph.expandedPoints)
			}
			if glyph.expandedContours > uint64(maxCompositeContours) {
				if glyph.expandedContours > uint64(^uint32(0)) {
					return geometrySourceInvariant(glyphID, "composite_contours")
				}
				maxCompositeContours = uint32(glyph.expandedContours)
			}
			if len(glyph.components) > int(maxComponentElements) {
				maxComponentElements = uint32(len(glyph.components))
			}
			if glyph.overlapDeclared {
				overlapCompound++
			}
			for _, component := range glyph.components {
				if uint64(component.glyphID) >= uint64(len(source.glyphs)) {
					return geometrySourceInvariant(glyphID, "component_gid")
				}
				if component.argumentMode == outlineArgumentPoint {
					pointMatches++
				}
				if component.flags&componentScaledOffset != 0 {
					scaledOffsets++
				} else if component.flags&componentUnscaledOffset != 0 {
					unscaledOffsets++
				} else if component.argumentMode == outlineArgumentXY {
					defaultOffsets++
				}
				if component.flags&componentUseMyMetrics != 0 {
					useMyMetrics++
				}
			}
		default:
			return geometrySourceInvariant(glyphID, "kind")
		}
		if len(glyph.instructions) > 0 {
			instructions++
		}
		if !checkedGeometrySourceAdd(&instructionBytes, uint64(len(glyph.instructions))) || !checkedGeometrySourceAdd(&padding, uint64(glyph.paddingBytes)) {
			return geometrySourceInvariant(glyphID, "byte_count_overflow")
		}
		if len(glyph.instructions) > int(maxInstructionBytes) {
			maxInstructionBytes = uint32(len(glyph.instructions))
		}
		if !checkedGeometrySourceAdd(&expandedPoints, glyph.expandedPoints) ||
			!checkedGeometrySourceAdd(&expandedContours, glyph.expandedContours) ||
			!checkedGeometrySourceAdd(&expandedEdges, glyph.expandedEdges) {
			return geometrySourceInvariant(glyphID, "expanded_count_overflow")
		}
		if glyph.expandedEdges > maxExpandedEdges {
			maxExpandedEdges = glyph.expandedEdges
		}
		if glyph.componentDepth > maxComponentDepth {
			maxComponentDepth = glyph.componentDepth
		}
	}
	receipt := source.receipt
	if emptyRanges != receipt.EmptyRangeGlyphCount || zeroContours != receipt.ZeroContourGlyphCount ||
		simpleGlyphs != receipt.SimpleGlyphCount || compoundGlyphs != receipt.CompoundGlyphCount ||
		rawPoints != receipt.RawPointCount || rawContours != receipt.RawContourCount || components != receipt.ComponentRecordCount ||
		instructions != receipt.InstructionGlyphCount || instructionBytes != receipt.InstructionBytes || padding != receipt.PaddingBytes ||
		pointMatches != receipt.PointMatchComponentCount || scaledOffsets != receipt.ScaledOffsetCount ||
		unscaledOffsets != receipt.UnscaledOffsetCount || defaultOffsets != receipt.DefaultedUnscaledCount ||
		useMyMetrics != receipt.UseMyMetricsCount || overlapSimple != receipt.OverlapSimpleGlyphCount ||
		overlapCompound != receipt.OverlapCompoundGlyphCount || expandedPoints != receipt.ExpandedPointInstances ||
		expandedContours != receipt.ExpandedContourInstances || expandedEdges != receipt.ExpandedEdgeVisits ||
		maxExpandedEdges != receipt.MaxExpandedEdgesPerGlyph || maxPoints != receipt.ObservedMaxPoints ||
		maxContours != receipt.ObservedMaxContours || maxCompositePoints != receipt.ObservedMaxCompositePoints ||
		maxCompositeContours != receipt.ObservedMaxCompositeContours || maxInstructionBytes != receipt.ObservedMaxInstructionBytes ||
		maxComponentElements != receipt.ObservedMaxComponentElements || maxComponentDepth != receipt.ObservedMaxComponentDepth {
		return fontError(CodeGeometryInputInvalid, "geometry/source/counts", ErrGeometryInputInvalid)
	}
	return nil
}

func checkedGeometrySourceAdd(total *uint64, increment uint64) bool {
	if total == nil {
		return false
	}
	next, ok := checkedAdd(*total, increment)
	if !ok {
		return false
	}
	*total = next
	return true
}

func geometrySourceInvariant(glyphID int, field string) error {
	return fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/source/glyphs/%d/%s", glyphID, field), ErrInvariantFailed)
}

func resolveGeometryMetricSources(ctx context.Context, source *OutlineFont) ([]uint32, uint64, error) {
	count := len(source.glyphs)
	resolved := make([]uint32, count)
	state := make([]uint8, count)
	var resolve func(uint32) (uint32, error)
	resolve = func(glyphID uint32) (uint32, error) {
		if uint64(glyphID) >= uint64(count) {
			return 0, fontError(CodeGeometryInvariant, "geometry/metrics/glyph_id", ErrInvariantFailed)
		}
		if state[glyphID] == 2 {
			return resolved[glyphID], nil
		}
		if state[glyphID] == 1 {
			return 0, fontError(CodeGeometryInvariant, "geometry/metrics/cycle", ErrInvariantFailed)
		}
		state[glyphID] = 1
		winner := glyphID
		redirected := false
		for componentIndex, component := range source.glyphs[glyphID].components {
			if componentIndex%256 == 0 {
				if err := contextFailure(ctx, fmt.Sprintf("geometry/metrics/%d/%d", glyphID, componentIndex)); err != nil {
					return 0, err
				}
			}
			if component.flags&componentUseMyMetrics == 0 {
				continue
			}
			if component.matrix != [4]int16{0x4000, 0, 0, 0x4000} {
				return 0, fontError(CodeGeometryMetrics, fmt.Sprintf("geometry/metrics/%d/%d/matrix", glyphID, componentIndex), ErrGeometryMetrics)
			}
			winner = component.glyphID
			redirected = true
		}
		ultimate := glyphID
		if redirected {
			var err error
			ultimate, err = resolve(winner)
			if err != nil {
				return 0, err
			}
			own, ownOK := source.font.HorizontalMetric(glyphID)
			inherited, inheritedOK := source.font.HorizontalMetric(ultimate)
			if !ownOK || !inheritedOK {
				return 0, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/metrics/%d/hmtx", glyphID), ErrInvariantFailed)
			}
			if own != inherited {
				return 0, fontError(CodeGeometryMetrics, fmt.Sprintf("geometry/metrics/%d/hmtx", glyphID), ErrGeometryMetrics)
			}
		}
		resolved[glyphID] = ultimate
		state[glyphID] = 2
		return ultimate, nil
	}
	var redirectCount uint64
	for glyphID := range source.glyphs {
		if glyphID%256 == 0 {
			if err := contextFailure(ctx, "geometry/metrics/roots"); err != nil {
				return nil, 0, err
			}
		}
		ultimate, err := resolve(uint32(glyphID))
		if err != nil {
			return nil, 0, err
		}
		if ultimate != uint32(glyphID) {
			redirectCount++
		}
	}
	return resolved, redirectCount, nil
}
