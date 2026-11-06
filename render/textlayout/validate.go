package textlayout

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

var canonicalVersionedTokenPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9][0-9]*$`)
var languageTagPattern = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`)

type inputMeasure struct {
	width           uint32
	height          uint32
	pixels          uint64
	textBytes       uint64
	lines           uint64
	glyphs          uint64
	uniqueGlyphs    uint64
	commands        uint64
	points          uint64
	contours        uint64
	glyphBinary     uint64
	outlineBinary   uint64
	placementBinary uint64
	layoutBinary    uint64
	canonicalBytes  uint64
	receiptStrings  uint64
	workUnits       uint64
	workingBase     uint64
	workingBytes    uint64
	retainedBytes   uint64
}

func contextFailure(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return layoutError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func validateAndMeasure(ctx context.Context, input Input, limits Limits) (inputMeasure, error) {
	if input.LayoutProfile != ProfileV1 || input.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		input.RunInputProfile != documentv2.TextRunInputProfileV1 || input.ShapedStreamProfile != NormalizedShapedStreamProfileV1 ||
		input.FontProfile != documentv2.TextFontProfileStaticGlyfV1 ||
		input.OutlineProfile != documentv2.TextOutlineProfileQ26_6V1 {
		return inputMeasure{}, layoutError(CodeProfileUnsupported, "input/profiles", ErrProfileUnsupported)
	}
	if err := validateBinding(input.Binding); err != nil {
		return inputMeasure{}, err
	}
	if !utf8.ValidString(input.Text) {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/text", ErrInputInvalid)
	}
	textBytes := uint64(len(input.Text))
	if textBytes > limits.MaxTextBytes {
		return inputMeasure{}, layoutError(CodeResourceLimit, "input/text", ErrResourceLimit)
	}
	if input.Viewport.WidthPixels == 0 || input.Viewport.WidthPixels > documentv2.RasterViewportMaxDimension ||
		input.Viewport.HeightPixels == 0 || input.Viewport.HeightPixels > documentv2.RasterViewportMaxDimension {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/viewport", ErrInputInvalid)
	}
	if input.Viewport.WidthPixels > limits.MaxWidth || input.Viewport.HeightPixels > limits.MaxHeight {
		return inputMeasure{}, layoutError(CodeResourceLimit, "input/viewport", ErrResourceLimit)
	}
	pixels, ok := checkedMul(uint64(input.Viewport.WidthPixels), uint64(input.Viewport.HeightPixels))
	if !ok || pixels > limits.MaxPixels {
		return inputMeasure{}, layoutError(CodeResourceLimit, "input/viewport/pixels", ErrResourceLimit)
	}
	if input.FontSize.Validate() != nil || input.FontSize.Numerator <= 0 {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/font_size", ErrInputInvalid)
	}
	if input.LineHeight.Validate() != nil || input.LineHeight.Numerator <= 0 {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/line_height", ErrInputInvalid)
	}
	if err := validateRationalBits(input.FontSize, limits.MaxExactBits, "input/font_size"); err != nil {
		return inputMeasure{}, err
	}
	if err := validateRationalBits(input.LineHeight, limits.MaxExactBits, "input/line_height"); err != nil {
		return inputMeasure{}, err
	}
	if input.UnitsPerEm < 16 || input.UnitsPerEm > HardMaxUnitsPerEm {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/units_per_em", ErrInputInvalid)
	}
	if input.HheaAscender < math.MinInt16 || input.HheaAscender > math.MaxInt16 {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/hhea_ascender", ErrInputInvalid)
	}
	if input.Direction != documentv2.TextDirectionLTR && input.Direction != documentv2.TextDirectionRTL {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/direction", ErrInputInvalid)
	}
	if input.Alignment != documentv2.TextAlignmentStart && input.Alignment != documentv2.TextAlignmentCenter && input.Alignment != documentv2.TextAlignmentEnd {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/alignment", ErrInputInvalid)
	}
	if !validLanguageTag(input.Language) ||
		input.Script != documentv2.TextScriptLatin && input.Script != documentv2.TextScriptHan && input.Script != documentv2.TextScriptArabic {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/run_properties", ErrInputInvalid)
	}

	expectedLines := uint64(1)
	for index := 0; index < len(input.Text); index++ {
		if index&4095 == 0 {
			if err := contextFailure(ctx, "input/text"); err != nil {
				return inputMeasure{}, err
			}
		}
		if input.Text[index] == '\n' {
			expectedLines++
		}
	}
	if expectedLines > limits.MaxLines || expectedLines > maxInt() {
		return inputMeasure{}, layoutError(CodeResourceLimit, "input/lines", ErrResourceLimit)
	}
	if uint64(len(input.Lines)) != expectedLines {
		return inputMeasure{}, layoutError(CodeInputInvalid, "input/lines", ErrInputInvalid)
	}
	measure := inputMeasure{
		width: input.Viewport.WidthPixels, height: input.Viewport.HeightPixels,
		pixels: pixels, textBytes: textBytes, lines: expectedLines,
	}
	cursor := 0
	for lineIndex, line := range input.Lines {
		if lineIndex&255 == 0 {
			if err := contextFailure(ctx, "input/lines"); err != nil {
				return inputMeasure{}, err
			}
		}
		end := cursor
		for end < len(input.Text) && input.Text[end] != '\n' {
			if end&4095 == 0 {
				if err := contextFailure(ctx, fmt.Sprintf("input/lines/%d/byte_range", lineIndex)); err != nil {
					return inputMeasure{}, err
				}
			}
			end++
		}
		if line.ByteStart != uint64(cursor) || line.ByteEnd != uint64(end) {
			return inputMeasure{}, layoutError(CodeInputInvalid, fmt.Sprintf("input/lines/%d/byte_range", lineIndex), ErrInputInvalid)
		}
		if uint64(len(line.Glyphs)) > limits.MaxGlyphsPerLine {
			return inputMeasure{}, layoutError(CodeResourceLimit, fmt.Sprintf("input/lines/%d/glyphs", lineIndex), ErrResourceLimit)
		}
		measure.glyphs, ok = checkedAdd(measure.glyphs, uint64(len(line.Glyphs)))
		if !ok || measure.glyphs > limits.MaxGlyphs || measure.glyphs > maxInt() {
			return inputMeasure{}, layoutError(CodeResourceLimit, "input/glyphs", ErrResourceLimit)
		}
		if (end == cursor) != (len(line.Glyphs) == 0) {
			return inputMeasure{}, layoutError(CodeInputInvalid, fmt.Sprintf("input/lines/%d/glyphs", lineIndex), ErrInputInvalid)
		}
		var previous uint64
		for glyphIndex, glyph := range line.Glyphs {
			if glyphIndex&1023 == 0 {
				if err := contextFailure(ctx, fmt.Sprintf("input/lines/%d/glyphs", lineIndex)); err != nil {
					return inputMeasure{}, err
				}
			}
			path := fmt.Sprintf("input/lines/%d/glyphs/%d", lineIndex, glyphIndex)
			if glyph.GlyphID == 0 || glyph.YAdvance != 0 {
				return inputMeasure{}, layoutError(CodeInputInvalid, path, ErrInputInvalid)
			}
			if glyph.ClusterByteOffset < uint64(cursor) || glyph.ClusterByteOffset >= uint64(end) ||
				glyph.ClusterByteOffset >= uint64(len(input.Text)) || !utf8.RuneStart(input.Text[glyph.ClusterByteOffset]) {
				return inputMeasure{}, layoutError(CodeInputInvalid, path+"/cluster_byte_offset", ErrInputInvalid)
			}
			if glyphIndex > 0 && glyph.ClusterByteOffset < previous {
				return inputMeasure{}, layoutError(CodeInputInvalid, path+"/cluster_byte_offset", ErrInputInvalid)
			}
			previous = glyph.ClusterByteOffset
		}
		if end > cursor {
			anchor := line.Glyphs[0].ClusterByteOffset
			if anchor != uint64(cursor) {
				return inputMeasure{}, layoutError(CodeInputInvalid, fmt.Sprintf("input/lines/%d/glyphs/cluster_anchor", lineIndex), ErrInputInvalid)
			}
		}
		cursor = end
		if cursor < len(input.Text) {
			cursor++
		}
	}
	if cursor != len(input.Text) {
		return inputMeasure{}, layoutError(CodeInvariantFailed, "input/lines", ErrInvariantFailed)
	}

	if uint64(len(input.Outlines)) > limits.MaxUniqueGlyphs || uint64(len(input.Outlines)) > maxInt() {
		return inputMeasure{}, layoutError(CodeResourceLimit, "input/outlines", ErrResourceLimit)
	}
	measure.uniqueGlyphs = uint64(len(input.Outlines))
	var previousGlyphID uint32
	for outlineIndex, outline := range input.Outlines {
		if outlineIndex&255 == 0 {
			if err := contextFailure(ctx, "input/outlines"); err != nil {
				return inputMeasure{}, err
			}
		}
		path := fmt.Sprintf("input/outlines/%d", outlineIndex)
		if outline.GlyphID == 0 || outlineIndex > 0 && outline.GlyphID <= previousGlyphID {
			return inputMeasure{}, layoutError(CodeInputInvalid, path+"/glyph_id", ErrInputInvalid)
		}
		previousGlyphID = outline.GlyphID
		if uint64(len(outline.Commands)) > limits.MaxCommandsPerGlyph {
			return inputMeasure{}, layoutError(CodeResourceLimit, path+"/commands", ErrResourceLimit)
		}
		measure.commands, ok = checkedAdd(measure.commands, uint64(len(outline.Commands)))
		if !ok || measure.commands > limits.MaxCommands || measure.commands > maxInt() {
			return inputMeasure{}, layoutError(CodeResourceLimit, "input/outlines/commands", ErrResourceLimit)
		}
		open := false
		for commandIndex, command := range outline.Commands {
			if commandIndex&1023 == 0 {
				if err := contextFailure(ctx, path+"/commands"); err != nil {
					return inputMeasure{}, err
				}
			}
			commandPath := fmt.Sprintf("%s/commands/%d", path, commandIndex)
			pointCount := 0
			switch command.Op {
			case MoveTo:
				pointCount = 1
				if open {
					return inputMeasure{}, layoutError(CodeInputInvalid, commandPath+"/op", ErrInputInvalid)
				}
				open = true
				measure.contours, ok = checkedAdd(measure.contours, 1)
				if !ok || measure.contours > limits.MaxContours {
					return inputMeasure{}, layoutError(CodeResourceLimit, "input/outlines/contours", ErrResourceLimit)
				}
			case LineTo:
				pointCount = 1
				if !open {
					return inputMeasure{}, layoutError(CodeInputInvalid, commandPath+"/op", ErrInputInvalid)
				}
			case QuadTo:
				pointCount = 2
				if !open {
					return inputMeasure{}, layoutError(CodeInputInvalid, commandPath+"/op", ErrInputInvalid)
				}
			case Close:
				if !open {
					return inputMeasure{}, layoutError(CodeInputInvalid, commandPath+"/op", ErrInputInvalid)
				}
				open = false
			default:
				return inputMeasure{}, layoutError(CodeInputInvalid, commandPath+"/op", ErrInputInvalid)
			}
			if len(command.Points) != pointCount {
				return inputMeasure{}, layoutError(CodeInputInvalid, commandPath+"/points", ErrInputInvalid)
			}
			measure.points, ok = checkedAdd(measure.points, uint64(pointCount))
			if !ok {
				return inputMeasure{}, layoutError(CodeResourceLimit, "input/outlines/points", ErrResourceLimit)
			}
		}
		if open {
			return inputMeasure{}, layoutError(CodeInputInvalid, path+"/commands", ErrInputInvalid)
		}
	}

	if err := finishMeasure(input, &measure, limits); err != nil {
		return inputMeasure{}, err
	}
	if err := computeCanonicalMeasure(input, &measure, limits); err != nil {
		return inputMeasure{}, err
	}
	if err := validateOutlineClosure(ctx, input); err != nil {
		return inputMeasure{}, err
	}
	return measure, contextFailure(ctx, "input")
}

func finishMeasure(input Input, measure *inputMeasure, limits Limits) error {
	placements, ok := checkedMul(measure.glyphs, logicalPlacementBytes)
	outlines, outlinesOK := checkedMul(measure.uniqueGlyphs, logicalOutlineBytes)
	commands, commandsOK := checkedMul(measure.commands, logicalCommandBytes)
	measure.receiptStrings, ok = receiptStringBackingBytes(input)
	if !ok || !outlinesOK || !commandsOK {
		return layoutError(CodeResourceLimit, "input/retained_bytes", ErrResourceLimit)
	}
	measure.retainedBytes, ok = checkedAdd(logicalLayoutRecordBytes, placements)
	if ok {
		measure.retainedBytes, ok = checkedAdd(measure.retainedBytes, outlines)
	}
	if ok {
		measure.retainedBytes, ok = checkedAdd(measure.retainedBytes, commands)
	}
	if ok {
		measure.retainedBytes, ok = checkedAdd(measure.retainedBytes, measure.receiptStrings)
	}
	if !ok || measure.retainedBytes > limits.MaxRetainedBytes {
		return layoutError(CodeResourceLimit, "input/retained_bytes", ErrResourceLimit)
	}
	flagBytes, ok := checkedMul(measure.uniqueGlyphs, logicalReferenceFlagBytes)
	if !ok {
		return layoutError(CodeResourceLimit, "input/working_bytes", ErrResourceLimit)
	}
	measure.workingBase, ok = checkedAdd(measure.retainedBytes, logicalWorkingFixedBytes)
	if ok {
		measure.workingBase, ok = checkedAdd(measure.workingBase, flagBytes)
	}
	if !ok || measure.workingBase > limits.MaxWorkingBytes {
		return layoutError(CodeResourceLimit, "input/working_bytes", ErrResourceLimit)
	}
	measure.workUnits, ok = checkedMul(measure.glyphs, 10)
	commandWork, commandOK := checkedMul(measure.commands, 4)
	lookupWork, lookupOK := checkedMul(measure.glyphs, uint64(bits.Len64(measure.uniqueGlyphs)))
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, commandWork)
	}
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, measure.textBytes)
	}
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, measure.lines)
	}
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, measure.uniqueGlyphs)
	}
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, lookupWork)
	}
	if !ok || !commandOK || !lookupOK || measure.workUnits > limits.MaxWorkUnits {
		return layoutError(CodeResourceLimit, "input/work_units", ErrResourceLimit)
	}
	return nil
}

func finishCanonicalMeasure(measure *inputMeasure, canonicalBytes uint64, limits Limits) error {
	if canonicalBytes > limits.MaxCanonicalBinaryBytes {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}
	working, ok := checkedAdd(measure.workingBase, canonicalBytes)
	if !ok || working > limits.MaxWorkingBytes {
		return layoutError(CodeResourceLimit, "input/working_bytes", ErrResourceLimit)
	}
	measure.workingBytes = working
	return nil
}

func validateOutlineClosure(ctx context.Context, input Input) error {
	if err := contextFailure(ctx, "input/outlines/references"); err != nil {
		return err
	}
	referenced := make([]bool, len(input.Outlines))
	var visits uint64
	for lineIndex, line := range input.Lines {
		for glyphIndex, glyph := range line.Glyphs {
			visits++
			if visits&1023 == 0 {
				if err := contextFailure(ctx, "input/glyphs"); err != nil {
					return err
				}
			}
			left, right := 0, len(input.Outlines)
			for left < right {
				middle := left + (right-left)/2
				if input.Outlines[middle].GlyphID < glyph.GlyphID {
					left = middle + 1
				} else {
					right = middle
				}
			}
			if left >= len(input.Outlines) || input.Outlines[left].GlyphID != glyph.GlyphID {
				return layoutError(CodeInputInvalid, glyphPath(lineIndex, glyphIndex)+"/glyph_id", ErrInputInvalid)
			}
			referenced[left] = true
		}
	}
	for index, exists := range referenced {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "input/outlines/references"); err != nil {
				return err
			}
		}
		if !exists {
			return layoutError(CodeInputInvalid, fmt.Sprintf("input/outlines/%d/glyph_id", index), ErrInputInvalid)
		}
	}
	return contextFailure(ctx, "input/outlines")
}

func validateBinding(value Binding) error {
	for _, field := range []struct {
		path  string
		value string
	}{
		{"input/binding/text_semantic_hash", value.TextSemanticHash},
		{"input/binding/layout_input_hash", value.LayoutInputHash},
		{"input/binding/font_content_hash", value.FontContentHash},
		{"input/binding/unicode_table_hash", value.UnicodeTableHash},
		{"input/binding/shape_provider_fingerprint", value.ShapeProviderFingerprint},
	} {
		if !canonicalSHA256(field.value) {
			return layoutError(CodeInputInvalid, field.path, ErrInputInvalid)
		}
	}
	if value.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 {
		return layoutError(CodeInputInvalid, "input/binding/text_semantic_hash_profile", ErrInputInvalid)
	}
	if value.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 {
		return layoutError(CodeInputInvalid, "input/binding/layout_input_hash_profile", ErrInputInvalid)
	}
	if value.UnicodeTableProfile != documentv2.TextUnicodeValidationTableProfileV1 ||
		value.UnicodeTableHashProfile != documentv2.TextUnicodeValidationTableHashProfileV1 ||
		value.UnicodeTableHash != documentv2.TextUnicodeValidationTableHashV1 {
		return layoutError(CodeInputInvalid, "input/binding/unicode", ErrInputInvalid)
	}
	if !validProfileToken(value.ShapeProviderProfile) {
		return layoutError(CodeInputInvalid, "input/binding/shape_provider_profile", ErrInputInvalid)
	}
	return nil
}

func validateRationalBits(value documentv2.Rational, maxBits uint32, path string) error {
	numerator := new(big.Int).Abs(new(big.Int).SetInt64(value.Numerator))
	denominator := new(big.Int).SetInt64(value.Denominator)
	if uint32(numerator.BitLen()) > maxBits || uint32(denominator.BitLen()) > maxBits {
		return layoutError(CodeExactLimit, path, ErrExactLimit)
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validProfileToken(value string) bool {
	if len(value) > maxProfileTokenBytes || !canonicalVersionedTokenPattern.MatchString(value) {
		return false
	}
	versionStart := strings.LastIndex(value, ".v")
	for _, segment := range strings.FieldsFunc(value[:versionStart], func(character rune) bool {
		return character == '.' || character == '_' || character == '-'
	}) {
		if len(segment) == 0 || segment[0] != 'v' {
			continue
		}
		if len(segment) == 1 {
			return false
		}
		digits := true
		for _, character := range segment[1:] {
			if character < '0' || character > '9' {
				digits = false
				break
			}
		}
		if digits {
			return false
		}
	}
	return true
}

func validLanguageTag(value string) bool {
	return len(value) <= HardMaxLanguageBytes && languageTagPattern.MatchString(value)
}
