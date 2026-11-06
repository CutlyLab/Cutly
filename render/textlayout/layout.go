package textlayout

import (
	"context"
	"fmt"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

type lineGeometry struct {
	baseline   Q26_6
	advance    Q26_6
	initialPen Q26_6
}

// Build validates one complete explicit shaped-input buffer and atomically publishes an immutable
// layout. Input is borrowed until return. Every returned error has a nil Layout and zero Receipt;
// unexpected programming panics are deliberately not converted into allocation failures.
func Build(ctx context.Context, input Input, limits Limits) (
	layout *Layout,
	receipt Receipt,
	err error,
) {
	defer func() {
		if err != nil {
			layout = nil
			receipt = Receipt{}
		}
	}()

	if ctx == nil {
		return nil, Receipt{}, layoutError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, Receipt{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, Receipt{}, err
	}
	measure, err := validateAndMeasure(ctx, input, normalized)
	if err != nil {
		return nil, Receipt{}, err
	}

	// Hashing doubles as the exact-geometry preflight. It streams directly into SHA-256, so no
	// retained Layout storage is allocated until every exact operation and byte budget succeeds.
	exact := exactContext{maxBits: normalized.MaxExactBits}
	glyphHash, glyphBytes, err := hashGlyphStream(ctx, input)
	if err != nil {
		return nil, Receipt{}, err
	}
	outlineHash, outlineBytes, err := hashScaledOutlineSet(ctx, exact, input)
	if err != nil {
		return nil, Receipt{}, err
	}
	placementHash, placementBytes, err := hashPlacementStream(ctx, exact, input, glyphHash)
	if err != nil {
		return nil, Receipt{}, err
	}
	if glyphBytes != measure.glyphBinary || outlineBytes != measure.outlineBinary || placementBytes != measure.placementBinary {
		return nil, Receipt{}, layoutError(CodeInvariantFailed, "hash/preflight_bytes", ErrInvariantFailed)
	}
	receipt = baseReceipt(input, measure, glyphHash, outlineHash, placementHash)
	var layoutBytes uint64
	receipt.LayoutHash, layoutBytes, err = hashLayoutBinding(ctx, input, receipt)
	if err != nil {
		return nil, Receipt{}, err
	}
	if layoutBytes != measure.layoutBinary {
		return nil, Receipt{}, layoutError(CodeInvariantFailed, "hash/layout/bytes", ErrInvariantFailed)
	}
	if total, sumOK := sumCanonicalBytes(glyphBytes, outlineBytes, placementBytes, layoutBytes); !sumOK || total != measure.canonicalBytes {
		return nil, Receipt{}, layoutError(CodeInvariantFailed, "hash/layout/bytes", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, "layout/preallocate"); err != nil {
		return nil, Receipt{}, err
	}

	layout, err = materializeLayout(ctx, exact, input, measure, receipt)
	if err != nil {
		return nil, Receipt{}, err
	}
	if err := contextFailure(ctx, "layout/publish"); err != nil {
		return nil, Receipt{}, err
	}
	return layout, receipt, nil
}

func normalizeLimits(value Limits) (Limits, error) {
	if value == (Limits{}) {
		value = DefaultLimits()
	}
	if err := validateLimits(value); err != nil {
		return Limits{}, err
	}
	return value, nil
}

func baseReceipt(input Input, measure inputMeasure, glyphHash, outlineHash, placementHash string) Receipt {
	return Receipt{
		SchemaVersion: ReceiptSchemaVersion, Profile: ReceiptProfileV1,
		LayoutProfile: ProfileV1, TextProfile: documentv2.TextProfilePinnedFontViewportPXV2,
		RunInputProfile: documentv2.TextRunInputProfileV1, ShapedStreamProfile: NormalizedShapedStreamProfileV1,
		FontProfile: documentv2.TextFontProfileStaticGlyfV1, OutlineProfile: documentv2.TextOutlineProfileQ26_6V1,
		TextSemanticHashProfile:  documentv2.TextSemanticHashProfileV1,
		TextSemanticHash:         strings.Clone(input.Binding.TextSemanticHash),
		LayoutInputHashProfile:   documentv2.TextLayoutInputHashProfileV1,
		LayoutInputHash:          strings.Clone(input.Binding.LayoutInputHash),
		FontContentHash:          strings.Clone(input.Binding.FontContentHash),
		UnicodeTableProfile:      documentv2.TextUnicodeValidationTableProfileV1,
		UnicodeTableHashProfile:  documentv2.TextUnicodeValidationTableHashProfileV1,
		UnicodeTableHash:         documentv2.TextUnicodeValidationTableHashV1,
		ShapeProviderProfile:     strings.Clone(input.Binding.ShapeProviderProfile),
		ShapeProviderFingerprint: strings.Clone(input.Binding.ShapeProviderFingerprint),
		GlyphStreamHashProfile:   GlyphStreamHashProfileV1, GlyphStreamHash: glyphHash,
		OutlineSetHashProfile: OutlineSetHashProfileV1, OutlineSetHash: outlineHash,
		PlacementHashProfile: PlacementHashProfileV1, PlacementHash: placementHash,
		LayoutHashProfile: LayoutHashProfileV1,
		WidthPixels:       measure.width, HeightPixels: measure.height,
		LineCount: measure.lines, GlyphCount: measure.glyphs, UniqueGlyphCount: measure.uniqueGlyphs,
		ContourCount: measure.contours, CommandCount: measure.commands,
		CanonicalBinaryBytes: measure.canonicalBytes, WorkUnits: measure.workUnits,
		WorkingBytes: measure.workingBytes, RetainedBytes: measure.retainedBytes,
	}
}

func sumCanonicalBytes(values ...uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		var ok bool
		total, ok = checkedAdd(total, value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func materializeLayout(
	ctx context.Context,
	exact exactContext,
	input Input,
	measure inputMeasure,
	receipt Receipt,
) (*Layout, error) {
	result := &Layout{
		viewport:   input.Viewport,
		placements: make([]GlyphPlacement, int(measure.glyphs)),
		outlines:   make([]layoutOutline, int(measure.uniqueGlyphs)),
		receipt:    receipt,
	}
	commands := make([]layoutCommand, int(measure.commands))
	commandOffset := 0
	for outlineIndex, outline := range input.Outlines {
		if outlineIndex&255 == 0 {
			if err := contextFailure(ctx, "layout/outlines"); err != nil {
				return nil, err
			}
		}
		end := commandOffset + len(outline.Commands)
		result.outlines[outlineIndex] = layoutOutline{
			glyphID:  outline.GlyphID,
			commands: commands[commandOffset:end:end],
		}
		for commandIndex, command := range outline.Commands {
			if commandIndex&1023 == 0 {
				if err := contextFailure(ctx, outlinePointPath(outlineIndex, commandIndex, 0)); err != nil {
					return nil, err
				}
			}
			target := &commands[commandOffset+commandIndex]
			target.opTag = outlineOpTag(command.Op)
			target.pointCount = uint8(len(command.Points))
			for pointIndex, point := range command.Points {
				path := outlinePointPath(outlineIndex, commandIndex, pointIndex)
				x, err := exact.scaleQ(point.X, input.FontSize, input.UnitsPerEm, path+"/x")
				if err != nil {
					return nil, err
				}
				y, err := exact.scaleQ(point.Y, input.FontSize, input.UnitsPerEm, path+"/y")
				if err != nil {
					return nil, err
				}
				target.points[pointIndex] = Point{X: x, Y: y}
			}
		}
		commandOffset = end
	}
	if commandOffset != len(commands) {
		return nil, layoutError(CodeInvariantFailed, "layout/outlines/commands", ErrInvariantFailed)
	}

	placementOffset := 0
	for lineIndex, line := range input.Lines {
		geometry, err := computeLineGeometry(ctx, exact, input, line, lineIndex)
		if err != nil {
			return nil, err
		}
		pen := geometry.initialPen
		for glyphIndex, glyph := range line.Glyphs {
			if placementOffset&1023 == 0 {
				if err := contextFailure(ctx, "layout/placements"); err != nil {
					return nil, err
				}
			}
			placement, advance, placementErr := placeGlyph(exact, input, lineIndex, glyphIndex, glyph, pen, geometry.baseline)
			if placementErr != nil {
				return nil, placementErr
			}
			result.placements[placementOffset] = placement
			placementOffset++
			pen, err = addQ(pen, advance, glyphPath(lineIndex, glyphIndex)+"/pen")
			if err != nil {
				return nil, err
			}
		}
	}
	if placementOffset != len(result.placements) {
		return nil, layoutError(CodeInvariantFailed, "layout/placements", ErrInvariantFailed)
	}
	return result, nil
}

func computeLineGeometry(ctx context.Context, exact exactContext, input Input, line ShapedLine, lineIndex int) (lineGeometry, error) {
	baseline, err := exact.baseline(
		input.HheaAscender, input.FontSize, input.LineHeight, input.UnitsPerEm,
		uint64(lineIndex), linePath(lineIndex)+"/baseline",
	)
	if err != nil {
		return lineGeometry{}, err
	}
	var advance Q26_6
	for glyphIndex, glyph := range line.Glyphs {
		if glyphIndex&1023 == 0 {
			if err := contextFailure(ctx, linePath(lineIndex)+"/advance"); err != nil {
				return lineGeometry{}, err
			}
		}
		scaled, scaleErr := exact.scaleQ(glyph.XAdvance, input.FontSize, input.UnitsPerEm, glyphPath(lineIndex, glyphIndex)+"/x_advance")
		if scaleErr != nil {
			return lineGeometry{}, scaleErr
		}
		advance, err = addQ(advance, scaled, linePath(lineIndex)+"/advance")
		if err != nil {
			return lineGeometry{}, err
		}
	}
	if input.Direction == documentv2.TextDirectionLTR && advance < 0 ||
		input.Direction == documentv2.TextDirectionRTL && advance > 0 {
		return lineGeometry{}, layoutError(CodeInputInvalid, linePath(lineIndex)+"/advance", ErrInputInvalid)
	}
	left, right := Q26_6(0), Q26_6(0)
	if advance < 0 {
		left = advance
	} else {
		right = advance
	}
	width := Q26_6(int64(input.Viewport.WidthPixels) << 6)
	var initial Q26_6
	switch input.Alignment {
	case documentv2.TextAlignmentStart:
		if input.Direction == documentv2.TextDirectionLTR {
			initial, err = subQ(0, left, linePath(lineIndex)+"/initial_pen")
		} else {
			initial, err = subQ(width, right, linePath(lineIndex)+"/initial_pen")
		}
	case documentv2.TextAlignmentEnd:
		if input.Direction == documentv2.TextDirectionLTR {
			initial, err = subQ(width, right, linePath(lineIndex)+"/initial_pen")
		} else {
			initial, err = subQ(0, left, linePath(lineIndex)+"/initial_pen")
		}
	case documentv2.TextAlignmentCenter:
		var numerator Q26_6
		numerator, err = subQ(width, left, linePath(lineIndex)+"/center_numerator")
		if err == nil {
			numerator, err = subQ(numerator, right, linePath(lineIndex)+"/center_numerator")
		}
		if err == nil {
			var rounded int64
			rounded, err = exact.halfEven(int64(numerator), linePath(lineIndex)+"/initial_pen")
			initial = Q26_6(rounded)
		}
	default:
		return lineGeometry{}, layoutError(CodeInvariantFailed, linePath(lineIndex)+"/alignment", ErrInvariantFailed)
	}
	if err != nil {
		return lineGeometry{}, err
	}
	return lineGeometry{baseline: baseline, advance: advance, initialPen: initial}, nil
}

func placeGlyph(
	exact exactContext,
	input Input,
	lineIndex, glyphIndex int,
	glyph ShapedGlyph,
	pen, baseline Q26_6,
) (GlyphPlacement, Q26_6, error) {
	path := glyphPath(lineIndex, glyphIndex)
	xOffset, err := exact.scaleQ(glyph.XOffset, input.FontSize, input.UnitsPerEm, path+"/x_offset")
	if err != nil {
		return GlyphPlacement{}, 0, err
	}
	yOffset, err := exact.scaleQ(glyph.YOffset, input.FontSize, input.UnitsPerEm, path+"/y_offset")
	if err != nil {
		return GlyphPlacement{}, 0, err
	}
	advance, err := exact.scaleQ(glyph.XAdvance, input.FontSize, input.UnitsPerEm, path+"/x_advance")
	if err != nil {
		return GlyphPlacement{}, 0, err
	}
	originX, err := addQ(pen, xOffset, path+"/origin_x")
	if err != nil {
		return GlyphPlacement{}, 0, err
	}
	baselineY, err := subQ(baseline, yOffset, path+"/baseline_y")
	if err != nil {
		return GlyphPlacement{}, 0, err
	}
	return GlyphPlacement{
		LineIndex: uint32(lineIndex), GlyphIndex: uint64(glyphIndex), GlyphID: glyph.GlyphID,
		ClusterByteOffset: glyph.ClusterByteOffset, OriginX: originX, BaselineY: baselineY,
	}, advance, nil
}

func addQ(left, right Q26_6, path string) (Q26_6, error) {
	result, ok := checkedAddInt64(int64(left), int64(right))
	if !ok {
		return 0, layoutError(CodeExactLimit, path, ErrExactLimit)
	}
	return Q26_6(result), nil
}

func subQ(left, right Q26_6, path string) (Q26_6, error) {
	result, ok := checkedSubInt64(int64(left), int64(right))
	if !ok {
		return 0, layoutError(CodeExactLimit, path, ErrExactLimit)
	}
	return Q26_6(result), nil
}

func linePath(lineIndex int) string { return fmt.Sprintf("input/lines/%d", lineIndex) }

func glyphPath(lineIndex, glyphIndex int) string {
	return fmt.Sprintf("input/lines/%d/glyphs/%d", lineIndex, glyphIndex)
}

func outlinePointPath(outlineIndex, commandIndex, pointIndex int) string {
	return fmt.Sprintf("input/outlines/%d/commands/%d/points/%d", outlineIndex, commandIndex, pointIndex)
}
