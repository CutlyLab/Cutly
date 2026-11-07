package sfntglyf

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	gotextfont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/harfbuzz"
	"github.com/go-text/typesetting/language"

	"github.com/CutlyLab/Cutly/internal/buildidentity"
)

type shapeLineInput struct {
	start   int
	end     int
	scalars uint64
}

type shapeInputScan struct {
	lineCount              uint64
	scalarCount            uint64
	maxLineAdmissionGlyphs uint64
	admissionGlyphs        uint64
	harfBuzzOps            uint64
}

type shapeAdmission struct {
	glyphs        uint64
	canonical     uint64
	workUnits     uint64
	workingBytes  uint64
	retainedBytes uint64
}

// Shape executes the pinned go-text HarfBuzz port with explicit run properties. The returned
// stream is normalized to Cutly's logical signed-pen contract and owns all exposed records.
func (provider *ShapeProvider) Shape(ctx context.Context, input ShapeInput, limits ShapeLimits) (
	result *ShapedText, receipt ShapeReceipt, err error,
) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = ShapeReceipt{}
			err = fontError(CodeAllocationFailed, "shape/run/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = ShapeReceipt{}
		}
	}()
	if err = contextFailure(ctx, "shape/run/context"); err != nil {
		return nil, ShapeReceipt{}, err
	}
	normalized, err := normalizeShapeLimits(limits)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	if provider == nil || provider.graph == nil || provider.source == nil || provider.base == nil ||
		provider.receipt.ProviderHash == "" || provider.gsubLookupExpansion == 0 {
		return nil, ShapeReceipt{}, fontError(CodeShapeProviderInvalid, "shape/run/provider", ErrShapeProviderInvalid)
	}
	if err := validateShapeDependencyIdentity(); err != nil {
		return nil, ShapeReceipt{}, err
	}
	expectedHash, expectedBytes, err := hashShapeProviderBinding(ctx, provider.receipt)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	if expectedHash != provider.receipt.ProviderHash || expectedBytes != provider.receipt.CanonicalBinaryBytes {
		return nil, ShapeReceipt{}, fontError(CodeShapeProviderInvalid, "shape/run/provider_receipt", ErrShapeProviderInvalid)
	}
	scan, err := scanShapeInput(
		ctx, input, normalized, provider.gsubLookupExpansion, provider.gsubTopLevelApplications,
	)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	inputCanonical, err := shapeInputCanonicalBytes(provider.receipt, input)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	admission, err := preAdmitShapeRun(provider.receipt, input, scan, inputCanonical, normalized)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}

	// Ownership and hashing happen only after every portable resource limit has admitted the run.
	// Script and direction map to package constants; Language gets one bounded caller-independent
	// copy. Text is never retained by the result.
	input = ownShapeRunProperties(input)
	inputHash, hashedInputCanonical, err := hashShapeInput(ctx, provider.receipt, input)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	if hashedInputCanonical != inputCanonical {
		return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/admission/input_canonical", ErrShapeInvariant)
	}

	// Only after every count, maxLen/maxOps, canonical, work, working, retained, and output-bound
	// check passes may the complete graph replay and go-text execution perform proportional work.
	source, core, err := replayShapeLayoutGraph(ctx, provider.graph)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	if source != provider.source || !shapeProviderReceiptMatchesLineage(provider.receipt, provider.graph.receipt, source.receipt, core.receipt) {
		return nil, ShapeReceipt{}, fontError(CodeShapeProviderInvalid, "shape/run/provider_lineage", ErrShapeProviderInvalid)
	}
	if err := precheckShapeMappings(ctx, input.Text, core, provider.base); err != nil {
		return nil, ShapeReceipt{}, err
	}
	lines, err := materializeShapeLines(ctx, input.Text, scan)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}

	script, _ := language.ParseScript(input.Script)
	direction := harfbuzz.LeftToRight
	if input.Direction == ShapeDirectionRTL {
		direction = harfbuzz.RightToLeft
	}
	shapedLines := make([]shapedLineRecord, len(lines))
	outputGlyphs := uint64(0)
	for lineIndex, line := range lines {
		shapedLines[lineIndex].byteStart = uint64(line.start)
		shapedLines[lineIndex].byteEnd = uint64(line.end)
		if line.start == line.end {
			continue
		}
		if err := contextFailure(ctx, "shape/run/lines"); err != nil {
			return nil, ShapeReceipt{}, err
		}
		face := gotextfont.NewFace(provider.base)
		hbFont := harfbuzz.NewFont(face)
		scale := int64(core.unitsPerEm) * 64
		if scale <= 0 || scale > math.MaxInt32 {
			return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/scale", ErrShapeInvariant)
		}
		hbFont.XScale, hbFont.YScale = int32(scale), int32(scale)
		buffer := harfbuzz.NewBuffer()
		buffer.ClusterLevel = harfbuzz.MonotoneCharacters
		buffer.Flags = harfbuzz.Bot | harfbuzz.Eot
		buffer.Props = harfbuzz.SegmentProperties{
			Language: language.NewLanguage(input.Language), Script: script, Direction: direction,
		}
		for relative, character := range input.Text[line.start:line.end] {
			buffer.AddRune(character, line.start+relative)
		}
		if err := contextFailure(ctx, "shape/run/go_text/before"); err != nil {
			return nil, ShapeReceipt{}, err
		}
		buffer.Shape(hbFont, nil)
		if err := contextFailure(ctx, "shape/run/go_text/after"); err != nil {
			return nil, ShapeReceipt{}, err
		}
		if len(buffer.Info) != len(buffer.Pos) || len(buffer.Info) == 0 ||
			uint64(len(buffer.Info)) > normalized.MaxGlyphsPerLine {
			return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/go_text/output", ErrShapeInvariant)
		}
		nextGlyphs, ok := checkedAdd(outputGlyphs, uint64(len(buffer.Info)))
		if !ok || nextGlyphs > normalized.MaxGlyphs {
			return nil, ShapeReceipt{}, fontError(CodeShapeResourceLimit, "shape/run/resources/glyphs", ErrResourceLimit)
		}
		if nextGlyphs > admission.glyphs {
			return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/admission/glyph_bound", ErrShapeInvariant)
		}
		outputGlyphs = nextGlyphs
		glyphs := make([]ShapedGlyphRecord, len(buffer.Info))
		for outputIndex := range buffer.Info {
			rawIndex := outputIndex
			if input.Direction == ShapeDirectionRTL {
				rawIndex = len(buffer.Info) - 1 - outputIndex
			}
			info, position := buffer.Info[rawIndex], buffer.Pos[rawIndex]
			if info.Glyph == 0 || uint64(info.Glyph) >= uint64(core.numGlyphs) ||
				position.YAdvance != 0 || info.Cluster < line.start || info.Cluster >= line.end ||
				!utf8ByteBoundary(input.Text, info.Cluster) {
				return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/go_text/glyph", ErrShapeInvariant)
			}
			xAdvance, xOffset := int64(position.XAdvance), int64(position.XOffset)
			if input.Direction == ShapeDirectionRTL {
				xAdvance = -xAdvance
				xOffset -= int64(position.XAdvance)
			}
			glyphs[outputIndex] = ShapedGlyphRecord{
				GlyphID: uint32(info.Glyph), ClusterByteOffset: uint64(info.Cluster),
				XAdvance: Q26_6(xAdvance), YAdvance: 0,
				XOffset: Q26_6(xOffset), YOffset: Q26_6(position.YOffset),
			}
			if outputIndex > 0 && glyphs[outputIndex-1].ClusterByteOffset > glyphs[outputIndex].ClusterByteOffset {
				return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/go_text/clusters", ErrShapeInvariant)
			}
		}
		if glyphs[0].ClusterByteOffset != uint64(line.start) {
			return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/go_text/first_cluster", ErrShapeInvariant)
		}
		shapedLines[lineIndex].glyphs = glyphs
	}

	outputHash, outputCanonical, err := hashShapeOutput(ctx, provider.receipt, input, shapedLines)
	if err != nil {
		return nil, ShapeReceipt{}, err
	}
	canonical, err := shapeCheckedAdd(inputCanonical, outputCanonical, "shape/run/resources/canonical")
	if err != nil {
		return nil, ShapeReceipt{}, fontError(CodeShapeResourceLimit, "shape/run/resources/canonical", errors.Join(ErrResourceLimit, err))
	}
	if canonical > admission.canonical {
		return nil, ShapeReceipt{}, fontError(CodeShapeInvariant, "shape/run/admission/canonical_bound", ErrShapeInvariant)
	}
	receipt = makeShapeReceiptIdentity(provider.receipt, input, inputHash, outputHash)
	receipt.TextBytes, receipt.LineCount, receipt.InputScalarCount = uint64(len(input.Text)), scan.lineCount, scan.scalarCount
	receipt.OutputGlyphCount, receipt.AdmissionGlyphBound = outputGlyphs, admission.glyphs
	receipt.CanonicalBinaryBytes, receipt.AdmissionCanonicalBytes = canonical, admission.canonical
	receipt.WorkUnits, receipt.WorkingBytes, receipt.RetainedBytes =
		admission.workUnits, admission.workingBytes, admission.retainedBytes
	if err = contextFailure(ctx, "shape/run/publish"); err != nil {
		return nil, ShapeReceipt{}, err
	}
	result = &ShapedText{lines: shapedLines, receipt: receipt}
	return result, receipt, nil
}

func normalizeShapeLimits(value ShapeLimits) (ShapeLimits, error) {
	defaults := DefaultShapeLimits()
	fields := []struct {
		value *uint64
		def   uint64
		hard  uint64
	}{
		{&value.MaxTextBytes, defaults.MaxTextBytes, HardMaxShapeTextBytes},
		{&value.MaxLines, defaults.MaxLines, HardMaxShapeLines},
		{&value.MaxScalarsPerLine, defaults.MaxScalarsPerLine, HardMaxShapeScalarsPerLine},
		{&value.MaxScalars, defaults.MaxScalars, HardMaxShapeScalars},
		{&value.MaxGlyphsPerLine, defaults.MaxGlyphsPerLine, HardMaxShapeGlyphsPerLine},
		{&value.MaxGlyphs, defaults.MaxGlyphs, HardMaxShapeGlyphs},
		{&value.MaxCanonicalBytes, defaults.MaxCanonicalBytes, HardMaxShapeCanonicalBytes},
		{&value.MaxWorkUnits, defaults.MaxWorkUnits, HardMaxShapeWorkUnits},
		{&value.MaxWorkingBytes, defaults.MaxWorkingBytes, HardMaxShapeWorkingBytes},
		{&value.MaxRetainedBytes, defaults.MaxRetainedBytes, HardMaxShapeRetainedBytes},
	}
	for _, field := range fields {
		if *field.value == 0 {
			*field.value = field.def
		}
		if *field.value > field.hard {
			return ShapeLimits{}, fontError(CodeShapeLimitsInvalid, "shape/limits", ErrLimitsInvalid)
		}
	}
	return value, nil
}

func scanShapeInput(
	ctx context.Context, input ShapeInput, limits ShapeLimits,
	gsubLookupExpansion, gsubTopLevelApplications uint64,
) (shapeInputScan, error) {
	return scanShapeInputWithUTF8Validator(
		ctx, input, limits, gsubLookupExpansion, gsubTopLevelApplications, utf8.ValidString,
	)
}

type shapeUTF8Validator func(string) bool

func scanShapeInputWithUTF8Validator(
	ctx context.Context, input ShapeInput, limits ShapeLimits,
	gsubLookupExpansion, gsubTopLevelApplications uint64, validateUTF8 shapeUTF8Validator,
) (shapeInputScan, error) {
	// Byte admission must precede utf8.ValidString: the latter is an O(Text) scan with no
	// cancellation hook. This ordering makes an over-limit caller fail in O(1) before any
	// proportional validation work.
	if uint64(len(input.Text)) > limits.MaxTextBytes {
		return shapeInputScan{}, fontError(CodeShapeResourceLimit, "shape/input/text_bytes", ErrResourceLimit)
	}
	if validateUTF8 == nil || !validateUTF8(input.Text) ||
		!validShapeLanguage(input.Language) ||
		(input.Script != ShapeScriptLatin && input.Script != ShapeScriptHan && input.Script != ShapeScriptArabic) ||
		(input.Direction != ShapeDirectionLTR && input.Direction != ShapeDirectionRTL) {
		return shapeInputScan{}, fontError(CodeShapeInputInvalid, "shape/input", ErrShapeInputInvalid)
	}
	if err := contextFailure(ctx, "shape/input/scan/start"); err != nil {
		return shapeInputScan{}, err
	}
	if gsubLookupExpansion == 0 {
		return shapeInputScan{}, fontError(CodeShapeProviderInvalid, "shape/input/admission/expansion", ErrShapeProviderInvalid)
	}
	result := shapeInputScan{lineCount: 1}
	lineScalars := uint64(0)
	finishLine := func() error {
		if lineScalars == 0 {
			return nil
		}
		// P covers the pinned Unicode 17 maximum recursive canonical decomposition (4)
		// plus one BOT dotted circle. H is go-text's exact 64x/min-16384 maxLen. Each
		// top-level lookup can grow by at most gsubLookupExpansion; once a lookup starts
		// above H, go-text stops GSUB. The minimum of the unconstrained lookup-count path
		// and maxLen path is therefore a safe final bound.
		normalizedGlyphs, multiplied := checkedMultiply(lineScalars, shapeNormalizationMaxExpansion)
		if !multiplied {
			return fontError(CodeShapeResourceLimit, "shape/input/admission/glyphs", ErrResourceLimit)
		}
		normalizedGlyphs, ok := checkedAdd(normalizedGlyphs, 1)
		if !ok {
			return fontError(CodeShapeResourceLimit, "shape/input/admission/dotted_circle", ErrResourceLimit)
		}
		hbMaxLen, multiplied := checkedMultiply(lineScalars, shapeHarfBuzzMaxLenFactor)
		if !multiplied {
			return fontError(CodeShapeResourceLimit, "shape/input/admission/max_len", ErrResourceLimit)
		}
		if hbMaxLen < shapeHarfBuzzMaxLenMinimum {
			hbMaxLen = shapeHarfBuzzMaxLenMinimum
		}
		ceiling := limits.MaxGlyphsPerLine + 1
		if ceiling == 0 || ceiling > HardMaxShapeGlyphs+1 {
			ceiling = HardMaxShapeGlyphs + 1
		}
		lookupGrowth := shapeSaturatingPow(gsubLookupExpansion, gsubTopLevelApplications, ceiling)
		byApplications := shapeSaturatingMultiply(normalizedGlyphs, lookupGrowth, ceiling)
		byMaxLen := shapeSaturatingMultiply(hbMaxLen, gsubLookupExpansion, ceiling)
		if byMaxLen < normalizedGlyphs {
			byMaxLen = normalizedGlyphs
		}
		lineBound := byApplications
		if byMaxLen < lineBound {
			lineBound = byMaxLen
		}
		if lineBound > limits.MaxGlyphsPerLine {
			return fontError(CodeShapeResourceLimit, "shape/input/admission/glyphs_per_line", ErrResourceLimit)
		}
		if lineBound > result.maxLineAdmissionGlyphs {
			result.maxLineAdmissionGlyphs = lineBound
		}
		var addErr error
		result.admissionGlyphs, addErr = shapeCheckedAdd(result.admissionGlyphs, lineBound, "shape/input/admission/glyphs")
		if addErr != nil || result.admissionGlyphs > limits.MaxGlyphs {
			return fontError(CodeShapeResourceLimit, "shape/input/admission/glyphs", errors.Join(ErrResourceLimit, addErr))
		}
		operations, multiplied := checkedMultiply(lineScalars, shapeHarfBuzzMaxOpsFactor)
		if !multiplied {
			return fontError(CodeShapeResourceLimit, "shape/input/admission/max_ops", ErrResourceLimit)
		}
		if operations < shapeHarfBuzzMaxOpsMinimum {
			operations = shapeHarfBuzzMaxOpsMinimum
		}
		result.harfBuzzOps, addErr = shapeCheckedAdd(result.harfBuzzOps, operations, "shape/input/admission/max_ops")
		return addErr
	}
	for byteOffset, character := range input.Text {
		if byteOffset&((32<<10)-1) == 0 {
			if err := contextFailure(ctx, "shape/input/scan"); err != nil {
				return shapeInputScan{}, err
			}
		}
		if character == '\n' {
			if err := finishLine(); err != nil {
				return shapeInputScan{}, err
			}
			result.lineCount++
			if result.lineCount > limits.MaxLines || result.lineCount > uint64(maxInt()) {
				return shapeInputScan{}, fontError(CodeShapeResourceLimit, "shape/input/lines", ErrResourceLimit)
			}
			lineScalars = 0
			continue
		}
		lineScalars++
		result.scalarCount++
		if lineScalars > limits.MaxScalarsPerLine || result.scalarCount > limits.MaxScalars {
			return shapeInputScan{}, fontError(CodeShapeResourceLimit, "shape/input/scalars", ErrResourceLimit)
		}
	}
	if err := finishLine(); err != nil {
		return shapeInputScan{}, err
	}
	if err := contextFailure(ctx, "shape/input/scan/end"); err != nil {
		return shapeInputScan{}, err
	}
	return result, nil
}

func materializeShapeLines(ctx context.Context, text string, scan shapeInputScan) ([]shapeLineInput, error) {
	lines := make([]shapeLineInput, 0, int(scan.lineCount))
	start, scalars := 0, uint64(0)
	for index, character := range text {
		if index&((32<<10)-1) == 0 {
			if err := contextFailure(ctx, "shape/input/lines/materialize"); err != nil {
				return nil, err
			}
		}
		if character == '\n' {
			lines = append(lines, shapeLineInput{start: start, end: index, scalars: scalars})
			start, scalars = index+1, 0
			continue
		}
		scalars++
	}
	lines = append(lines, shapeLineInput{start: start, end: len(text), scalars: scalars})
	if uint64(len(lines)) != scan.lineCount {
		return nil, fontError(CodeShapeInvariant, "shape/input/lines/materialize", ErrShapeInvariant)
	}
	return lines, nil
}

func preAdmitShapeRun(
	provider ShapeProviderReceipt, input ShapeInput, scan shapeInputScan,
	inputCanonical uint64, limits ShapeLimits,
) (shapeAdmission, error) {
	outputCanonical, err := shapeOutputCanonicalUpper(provider, input, scan.lineCount, scan.admissionGlyphs)
	if err != nil {
		return shapeAdmission{}, err
	}
	canonical, err := shapeCheckedAdd(inputCanonical, outputCanonical, "shape/run/admission/canonical")
	if err != nil || canonical > limits.MaxCanonicalBytes {
		return shapeAdmission{}, fontError(CodeShapeResourceLimit, "shape/run/admission/canonical", errors.Join(ErrResourceLimit, err))
	}
	work, working, retained, err := shapeRunResources(
		provider, scan.lineCount, uint64(len(input.Text)), scan.scalarCount, scan.admissionGlyphs,
		canonical, scan.maxLineAdmissionGlyphs, scan.harfBuzzOps,
		shapeReceiptStringBytesForInput(provider, input),
	)
	if err != nil {
		return shapeAdmission{}, err
	}
	if work > limits.MaxWorkUnits || working > limits.MaxWorkingBytes || retained > limits.MaxRetainedBytes {
		return shapeAdmission{}, fontError(CodeShapeResourceLimit, "shape/run/admission/resources", ErrResourceLimit)
	}
	return shapeAdmission{
		glyphs: scan.admissionGlyphs, canonical: canonical, workUnits: work,
		workingBytes: working, retainedBytes: retained,
	}, nil
}

func shapeOutputCanonicalUpper(
	provider ShapeProviderReceipt, input ShapeInput, lineCount, glyphBound uint64,
) (uint64, error) {
	count := uint64(0)
	for _, text := range []string{
		ShapeOutputHashProfileV1, ShapeOutputProfileV1, ShapeProviderProfileV1,
		provider.ProviderHash, provider.DependencyFingerprint, provider.LayoutGraphHash,
		provider.OutlineSetHash, provider.FontContentHash, input.Text, input.Language,
		input.Script, string(input.Direction),
	} {
		increment, ok := checkedAdd(8, uint64(len(text)))
		if !ok {
			return 0, fontError(CodeShapeResourceLimit, "shape/run/admission/canonical", ErrResourceLimit)
		}
		var err error
		count, err = shapeCheckedAdd(count, increment, "shape/run/admission/canonical")
		if err != nil {
			return 0, err
		}
	}
	for _, fixed := range []uint64{4, 8} { // schema u32, line-count u64
		var err error
		count, err = shapeCheckedAdd(count, fixed, "shape/run/admission/canonical")
		if err != nil {
			return 0, err
		}
	}
	lineBytes, ok := checkedMultiply(lineCount, 24)
	if !ok {
		return 0, fontError(CodeShapeResourceLimit, "shape/run/admission/canonical", ErrResourceLimit)
	}
	glyphBytes, ok := checkedMultiply(glyphBound, 44)
	if !ok {
		return 0, fontError(CodeShapeResourceLimit, "shape/run/admission/canonical", ErrResourceLimit)
	}
	var err error
	count, err = shapeCheckedAdd(count, lineBytes, "shape/run/admission/canonical")
	if err == nil {
		count, err = shapeCheckedAdd(count, glyphBytes, "shape/run/admission/canonical")
	}
	return count, err
}

func makeShapeReceiptIdentity(
	provider ShapeProviderReceipt, input ShapeInput, inputHash, outputHash string,
) ShapeReceipt {
	return ShapeReceipt{
		SchemaVersion: ShapeProviderSchemaVersion, Profile: ShapeReceiptProfileV1,
		ProviderProfile:     ShapeProviderProfileV1,
		ProviderHashProfile: ShapeProviderHashProfileV1, ProviderHash: provider.ProviderHash,
		DependencyFingerprintProfile: buildidentity.TextShapeFingerprintProfileV1,
		DependencyFingerprint:        provider.DependencyFingerprint,
		OutputProfile:                ShapeOutputProfileV1,
		InputHashProfile:             ShapeInputHashProfileV1, InputHash: inputHash,
		OutputHashProfile: ShapeOutputHashProfileV1, OutputHash: outputHash,
		LayoutGraphHashProfile: provider.LayoutGraphHashProfile, LayoutGraphHash: provider.LayoutGraphHash,
		OutlineSetHashProfile: provider.OutlineSetHashProfile, OutlineSetHash: provider.OutlineSetHash,
		FontContentHashProfile: provider.FontContentHashProfile, FontContentHash: provider.FontContentHash,
		ClusterPolicy: ShapeClusterPolicyV1, FeaturePolicy: ShapeFeaturePolicyV1,
		DirectionNormalization: ShapeDirectionNormalizationProfileV1,
		Language:               input.Language, Script: input.Script, Direction: input.Direction,
	}
}

func ownShapeRunProperties(input ShapeInput) ShapeInput {
	input.Language = strings.Clone(input.Language)
	switch input.Script {
	case ShapeScriptLatin:
		input.Script = ShapeScriptLatin
	case ShapeScriptHan:
		input.Script = ShapeScriptHan
	case ShapeScriptArabic:
		input.Script = ShapeScriptArabic
	}
	switch input.Direction {
	case ShapeDirectionLTR:
		input.Direction = ShapeDirectionLTR
	case ShapeDirectionRTL:
		input.Direction = ShapeDirectionRTL
	}
	return input
}

func validShapeLanguage(value string) bool {
	if len(value) < 2 || len(value) > 128 {
		return false
	}
	segmentStart, segmentIndex := 0, 0
	for index := 0; index <= len(value); index++ {
		if index != len(value) && value[index] != '-' {
			continue
		}
		length := index - segmentStart
		if length < 1 || length > 8 || segmentIndex == 0 && length < 2 {
			return false
		}
		for _, character := range value[segmentStart:index] {
			letter := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
			digit := character >= '0' && character <= '9'
			if segmentIndex == 0 && !letter || segmentIndex != 0 && !letter && !digit {
				return false
			}
		}
		segmentStart, segmentIndex = index+1, segmentIndex+1
	}
	return true
}

func precheckShapeMappings(ctx context.Context, text string, core *Font, base *gotextfont.Font) error {
	count := 0
	for _, character := range text {
		if character == '\n' {
			continue
		}
		if count&1023 == 0 {
			if err := contextFailure(ctx, "shape/input/cmap"); err != nil {
				return err
			}
		}
		count++
		coreGlyph, coreOK := core.NominalGlyph(character)
		goTextGlyph, goTextOK := base.NominalGlyph(character)
		if !coreOK || !goTextOK || coreGlyph == 0 || uint32(goTextGlyph) != coreGlyph {
			return fontError(CodeShapeMissingGlyph, "shape/input/cmap", ErrShapeMissingGlyph)
		}
	}
	return nil
}

func utf8ByteBoundary(text string, offset int) bool {
	return offset >= 0 && offset < len(text) && (offset == 0 || text[offset]&0xc0 != 0x80)
}

func shapeProviderReceiptMatchesLineage(provider ShapeProviderReceipt, graph LayoutGraphReceipt, set OutlineSetReceipt, font Receipt) bool {
	return provider.SchemaVersion == ShapeProviderSchemaVersion && provider.Profile == ShapeProviderReceiptProfileV1 &&
		provider.ProviderProfile == ShapeProviderProfileV1 && provider.DescriptorHash == ShapeProviderDescriptorHash() &&
		provider.DependencyFingerprint == buildidentity.TextShapeCandidateFingerprint() &&
		provider.LayoutGraphProfile == graph.Profile && provider.LayoutGraphHashProfile == graph.GraphHashProfile &&
		provider.LayoutGraphHash == graph.GraphHash && provider.OutlineSetHashProfile == set.OutlineSetHashProfile &&
		provider.OutlineSetHash == set.OutlineSetHash && provider.FontContentHashProfile == font.ContentHashProfile &&
		provider.FontContentHash == font.ContentHash && provider.UnitsPerEm == font.UnitsPerEm &&
		provider.GlyphCount == font.NumGlyphs && provider.EncodedBytes == font.EncodedBytes &&
		provider.CmapMappingCount == font.CmapMappingCount && provider.ClusterPolicy == ShapeClusterPolicyV1 &&
		provider.FeaturePolicy == ShapeFeaturePolicyV1 && provider.DirectionNormalization == ShapeDirectionNormalizationProfileV1
}

func shapeRunResources(
	provider ShapeProviderReceipt, lineCount,
	textBytes, scalarCount, glyphCount, canonical, maxLineGlyphs, harfBuzzOps, receiptStrings uint64,
) (uint64, uint64, uint64, error) {
	work := provider.WorkUnits
	glyphWork, ok := checkedMultiply(glyphCount, 64)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape/run/resources/work", ErrResourceLimit)
	}
	for _, increment := range []uint64{textBytes, scalarCount, glyphWork, canonical, harfBuzzOps} {
		var err error
		work, err = shapeCheckedAdd(work, increment, "shape/run/resources/work")
		if err != nil {
			return 0, 0, 0, err
		}
	}
	bufferBytes, ok := checkedMultiply(maxLineGlyphs, shapeLogicalBufferGlyphBytes)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape/run/resources/working", ErrResourceLimit)
	}
	outputBytes, ok := checkedMultiply(glyphCount, shapeLogicalGlyphBytes)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape/run/resources/working", ErrResourceLimit)
	}
	lineBytes, ok := checkedMultiply(lineCount, shapeLogicalLineBytes)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape/run/resources/working", ErrResourceLimit)
	}
	working, err := shapeCheckedAdd(shapeLogicalFixedWorkingBytes, bufferBytes, "shape/run/resources/working")
	if err == nil {
		working, err = shapeCheckedAdd(working, outputBytes, "shape/run/resources/working")
	}
	if err == nil {
		// materializeShapeLines retains one temporary shapeLineInput record for every line
		// while the final shapedLineRecord array is being assembled.
		working, err = shapeCheckedAdd(working, lineBytes, "shape/run/resources/working")
	}
	if err != nil {
		return 0, 0, 0, err
	}
	if working < provider.WorkingBytes {
		working = provider.WorkingBytes
	}
	retained, err := shapeCheckedAdd(shapeLogicalFixedRetainedBytes, lineBytes, "shape/run/resources/retained")
	if err == nil {
		retained, err = shapeCheckedAdd(retained, outputBytes, "shape/run/resources/retained")
	}
	if err == nil {
		retained, err = shapeCheckedAdd(retained, receiptStrings, "shape/run/resources/retained")
	}
	if err != nil {
		return 0, 0, 0, err
	}
	return work, working, retained, nil
}

func shapeReceiptStringBytesForInput(provider ShapeProviderReceipt, input ShapeInput) uint64 {
	return uint64(len(ShapeReceiptProfileV1) + len(ShapeProviderProfileV1) +
		len(ShapeProviderHashProfileV1) + len(provider.ProviderHash) +
		len(buildidentity.TextShapeFingerprintProfileV1) + len(provider.DependencyFingerprint) +
		len(ShapeOutputProfileV1) + len(ShapeInputHashProfileV1) + int(shapeDigestStringBytes) +
		len(ShapeOutputHashProfileV1) + int(shapeDigestStringBytes) + len(provider.LayoutGraphHashProfile) +
		len(provider.LayoutGraphHash) + len(provider.OutlineSetHashProfile) + len(provider.OutlineSetHash) +
		len(provider.FontContentHashProfile) + len(provider.FontContentHash) + len(ShapeClusterPolicyV1) +
		len(ShapeFeaturePolicyV1) + len(ShapeDirectionNormalizationProfileV1) + len(input.Language) +
		len(input.Script) + len(input.Direction))
}
