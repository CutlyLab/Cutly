package textlayout

// computeCanonicalMeasure derives the exact byte size of all four canonical preimages without
// allocating a hash state or retained storage. Every encoded scalar has a fixed width, so scaling
// changes values but never sizes.
func computeCanonicalMeasure(input Input, measure *inputMeasure, limits Limits) error {
	binding, ok := canonicalStringsSize(
		input.Binding.TextSemanticHashProfile,
		input.Binding.TextSemanticHash,
		input.Binding.LayoutInputHashProfile,
		input.Binding.LayoutInputHash,
		input.Binding.FontContentHash,
		input.Binding.UnicodeTableProfile,
		input.Binding.UnicodeTableHashProfile,
		input.Binding.UnicodeTableHash,
		input.Binding.ShapeProviderProfile,
		input.Binding.ShapeProviderFingerprint,
	)
	if !ok {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}

	glyphStrings, ok := canonicalStringsSize(
		GlyphStreamHashProfileV1,
		input.LayoutProfile,
		input.TextProfile,
		input.RunInputProfile,
		input.ShapedStreamProfile,
		input.FontProfile,
		input.OutlineProfile,
		input.Text,
		input.Language,
		input.Script,
		string(input.Direction),
	)
	glyphLines, linesOK := checkedMul(measure.lines, 24)
	glyphRecords, glyphsOK := checkedMul(measure.glyphs, 44)
	measure.glyphBinary, ok = sumCanonicalBytes(glyphStrings, 4, binding, 4, 8, glyphLines, glyphRecords)
	if !ok || !linesOK || !glyphsOK {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}

	outlineStrings, ok := canonicalStringsSize(
		OutlineSetHashProfileV1,
		input.LayoutProfile,
		input.FontProfile,
		input.OutlineProfile,
		input.Binding.FontContentHash,
	)
	outlineRecords, outlinesOK := checkedMul(measure.uniqueGlyphs, 12)
	commandRecords, commandsOK := checkedMul(measure.commands, 9)
	pointRecords, pointsOK := checkedMul(measure.points, 16)
	measure.outlineBinary, ok = sumCanonicalBytes(outlineStrings, 4, 16, 4, 8, outlineRecords, commandRecords, pointRecords)
	if !ok || !outlinesOK || !commandsOK || !pointsOK {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}

	placementStrings, ok := canonicalStringsSize(
		PlacementHashProfileV1,
		input.LayoutProfile,
		canonicalDigestPlaceholder,
		string(input.Direction),
		string(input.Alignment),
	)
	placementLines, linesOK := checkedMul(measure.lines, 52)
	placementRecords, placementsOK := checkedMul(measure.glyphs, 40)
	measure.placementBinary, ok = sumCanonicalBytes(placementStrings, 4, 8, 32, 4, 4, 8, placementLines, placementRecords)
	if !ok || !linesOK || !placementsOK {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}

	layoutStrings, ok := canonicalStringsSize(
		LayoutHashProfileV1,
		ReceiptProfileV1,
		input.LayoutProfile,
		input.TextProfile,
		input.RunInputProfile,
		input.ShapedStreamProfile,
		input.FontProfile,
		input.OutlineProfile,
		GlyphStreamHashProfileV1,
		canonicalDigestPlaceholder,
		OutlineSetHashProfileV1,
		canonicalDigestPlaceholder,
		PlacementHashProfileV1,
		canonicalDigestPlaceholder,
		LayoutHashProfileV1,
		input.Language,
		input.Script,
		string(input.Direction),
		string(input.Alignment),
	)
	measure.layoutBinary, ok = sumCanonicalBytes(layoutStrings, 8, binding, 8, 32, 4, 4, 72)
	if !ok {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}
	measure.canonicalBytes, ok = sumCanonicalBytes(
		measure.glyphBinary,
		measure.outlineBinary,
		measure.placementBinary,
		measure.layoutBinary,
	)
	if !ok {
		return layoutError(CodeResourceLimit, "input/canonical_binary_bytes", ErrResourceLimit)
	}
	return finishCanonicalMeasure(measure, measure.canonicalBytes, limits)
}

const canonicalDigestPlaceholder = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func receiptStringBackingBytes(input Input) (uint64, bool) {
	return rawStringsSize(
		ReceiptProfileV1,
		ProfileV1,
		input.TextProfile,
		input.RunInputProfile,
		input.ShapedStreamProfile,
		input.FontProfile,
		input.OutlineProfile,
		input.Binding.TextSemanticHashProfile,
		input.Binding.TextSemanticHash,
		input.Binding.LayoutInputHashProfile,
		input.Binding.LayoutInputHash,
		input.Binding.FontContentHash,
		input.Binding.UnicodeTableProfile,
		input.Binding.UnicodeTableHashProfile,
		input.Binding.UnicodeTableHash,
		input.Binding.ShapeProviderProfile,
		input.Binding.ShapeProviderFingerprint,
		GlyphStreamHashProfileV1,
		canonicalDigestPlaceholder,
		OutlineSetHashProfileV1,
		canonicalDigestPlaceholder,
		PlacementHashProfileV1,
		canonicalDigestPlaceholder,
		LayoutHashProfileV1,
		canonicalDigestPlaceholder,
	)
}

func rawStringsSize(values ...string) (uint64, bool) {
	var total uint64
	for _, value := range values {
		var ok bool
		total, ok = checkedAdd(total, uint64(len(value)))
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func canonicalStringsSize(values ...string) (uint64, bool) {
	var total uint64
	for _, value := range values {
		encoded, ok := checkedAdd(8, uint64(len(value)))
		if !ok {
			return 0, false
		}
		total, ok = checkedAdd(total, encoded)
		if !ok {
			return 0, false
		}
	}
	return total, true
}
