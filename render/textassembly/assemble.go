package textassembly

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

type assemblyMeasure struct {
	lines, glyphs, uniqueGlyphs uint64
	contours, commands, points  uint64
	maxGlyphsPerLine            uint64
	maxCommandsPerGlyph         uint64
	bridgeWorking               uint64
	bridgeWork                  uint64
}

func Assemble(ctx context.Context, request Request, limits Limits) (
	result *Source,
	receipt Receipt,
	err error,
) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = Receipt{}
			err = assemblyError(CodeInvariantFailed, "assembly/panic", ErrInvariantFailed)
		}
		if err != nil {
			result = nil
			receipt = Receipt{}
		}
	}()
	if err = contextFailure(ctx, "assembly/context"); err != nil {
		return nil, Receipt{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, Receipt{}, err
	}
	if request.Text == nil || request.Provider == nil {
		return nil, Receipt{}, assemblyError(CodeInputInvalid, "assembly/request", ErrInputInvalid)
	}
	frozenReceipt, err := validateFrozenTextSource(request.Text)
	if err != nil {
		return nil, Receipt{}, err
	}
	providerReceipt := request.Provider.Receipt()
	if err := validateProviderReceipt(providerReceipt, frozenReceipt); err != nil {
		return nil, Receipt{}, err
	}
	layoutSourceLimits, err := capLayoutSourceLimits(normalized, frozenReceipt)
	if err != nil {
		return nil, Receipt{}, err
	}

	// Bind the complete live font/layout/outline lineage before executing shaping.
	layoutSource, sourceReceipt, err := request.Provider.BindLayoutSource(ctx, layoutSourceLimits)
	if err != nil {
		return nil, Receipt{}, mapFontChildError("assembly/layout_source", err)
	}
	if layoutSource == nil || layoutSource.Receipt() != sourceReceipt ||
		layoutSource.UnitsPerEm() != sourceReceipt.UnitsPerEm ||
		layoutSource.HheaAscender() != sourceReceipt.HheaAscender ||
		layoutSource.GlyphCount() != sourceReceipt.GlyphCount {
		return nil, Receipt{}, assemblyError(CodeInvariantFailed, "assembly/layout_source/object", ErrInvariantFailed)
	}
	if err := validateLayoutSourceReceipt(sourceReceipt, providerReceipt, frozenReceipt); err != nil {
		return nil, Receipt{}, err
	}
	shapeLimits, err := capShapeLimits(ctx, normalized, frozenReceipt, providerReceipt, sourceReceipt)
	if err != nil {
		return nil, Receipt{}, err
	}

	direction, err := shapeDirection(request.Text.Direction())
	if err != nil {
		return nil, Receipt{}, err
	}
	shaped, shapeReceipt, err := request.Provider.Shape(ctx, sfntglyf.ShapeInput{
		Text: request.Text.Text(), Language: request.Text.Language(), Script: request.Text.Script(), Direction: direction,
	}, shapeLimits)
	if err != nil {
		return nil, Receipt{}, mapFontChildError("assembly/shape", err)
	}
	if shaped == nil || shaped.Receipt() != shapeReceipt {
		return nil, Receipt{}, assemblyError(CodeInvariantFailed, "assembly/shape/object", ErrInvariantFailed)
	}
	if err := validateShapeReceipt(shapeReceipt, providerReceipt, sourceReceipt, frozenReceipt); err != nil {
		return nil, Receipt{}, err
	}
	if err := preAdmitShapeScalars(ctx, frozenReceipt, providerReceipt, sourceReceipt, shapeReceipt, layoutSource.GlyphCount(), normalized); err != nil {
		return nil, Receipt{}, err
	}

	view, err := shaped.View()
	if err != nil {
		return nil, Receipt{}, assemblyError(CodeInvariantFailed, "assembly/shaped_view", errors.Join(ErrInvariantFailed, err))
	}
	measure, glyphIDs, err := inspectShapedClosure(ctx, view, shapeReceipt, layoutSource, normalized)
	if err != nil {
		return nil, Receipt{}, err
	}
	if err := inspectOutlineClosure(ctx, layoutSource, glyphIDs, &measure, normalized); err != nil {
		return nil, Receipt{}, err
	}
	if err := admitBridgeResources(&measure, normalized); err != nil {
		return nil, Receipt{}, err
	}
	fontSize := request.Text.FontSize()
	lineHeight := request.Text.LineHeight()
	viewport := request.Text.Viewport()
	fillValue := request.Text.Fill()
	fill := color.RGBA16Input{R: fillValue.R, G: fillValue.G, B: fillValue.B, A: fillValue.A}
	layoutLimits, admittedBindingBytes, err := prepareLayoutAdmission(
		ctx, normalized, frozenReceipt, providerReceipt, sourceReceipt, shapeReceipt,
		measure, fontSize, viewport, fill,
	)
	if err != nil {
		return nil, Receipt{}, err
	}
	lines, outlines, err := materializeLayoutInput(ctx, view, layoutSource, glyphIDs, measure)
	if err != nil {
		return nil, Receipt{}, err
	}

	layout, layoutReceipt, err := textlayout.Build(ctx, textlayout.Input{
		TextProfile: frozenReceipt.TextProfile, RunInputProfile: frozenReceipt.RunInputProfile,
		ShapedStreamProfile: textlayout.NormalizedShapedStreamProfileV1, LayoutProfile: frozenReceipt.LayoutProfile,
		FontProfile: frozenReceipt.FontProfile, OutlineProfile: frozenReceipt.OutlineProfile,
		Text: request.Text.Text(), Viewport: viewport, FontSize: fontSize, LineHeight: lineHeight,
		Language: request.Text.Language(), Script: request.Text.Script(), Direction: request.Text.Direction(),
		Alignment: request.Text.Alignment(), UnitsPerEm: uint32(layoutSource.UnitsPerEm()),
		HheaAscender: int32(layoutSource.HheaAscender()), Lines: lines, Outlines: outlines,
		Binding: textlayout.Binding{
			TextSemanticHashProfile:  frozenReceipt.TextSemanticHashProfile,
			TextSemanticHash:         frozenReceipt.TextSemanticHash,
			LayoutInputHashProfile:   frozenReceipt.LayoutInputHashProfile,
			LayoutInputHash:          frozenReceipt.LayoutInputHash,
			FontContentHash:          frozenReceipt.FontContentHash,
			UnicodeTableProfile:      frozenReceipt.UnicodeValidationProfile,
			UnicodeTableHashProfile:  frozenReceipt.UnicodeValidationHashProfile,
			UnicodeTableHash:         frozenReceipt.UnicodeValidationTableHash,
			ShapeProviderProfile:     sourceReceipt.ShapeProviderProfile,
			ShapeProviderFingerprint: sourceReceipt.DependencyFingerprint,
		},
	}, layoutLimits)
	if err != nil {
		return nil, Receipt{}, mapLayoutChildError("assembly/layout", err)
	}
	if layout == nil || layout.Receipt() != layoutReceipt {
		return nil, Receipt{}, assemblyError(CodeInvariantFailed, "assembly/layout/object", ErrInvariantFailed)
	}
	if err := validateLayoutReceipt(layoutReceipt, shapeReceipt, sourceReceipt, frozenReceipt, measure, fontSize, viewport); err != nil {
		return nil, Receipt{}, err
	}
	viewLayout, viewErr := layout.View()
	if viewErr != nil || layout.FontSize() != fontSize || viewLayout.Viewport() != viewport {
		return nil, Receipt{}, assemblyError(CodeInvariantFailed, "assembly/layout/object_values", errors.Join(ErrInvariantFailed, viewErr))
	}

	receipt = makeReceipt(frozenReceipt, providerReceipt, sourceReceipt, shapeReceipt, layoutReceipt, measure, fontSize, viewport, fill)
	_, bindingBytes, err := hashAssembly(ctx, receipt)
	if err != nil {
		return nil, Receipt{}, err
	}
	canonical, ok := checkedAdd(sourceReceipt.CanonicalBinaryBytes, shapeReceipt.CanonicalBinaryBytes)
	if ok {
		canonical, ok = checkedAdd(canonical, layoutReceipt.CanonicalBinaryBytes)
	}
	if ok {
		canonical, ok = checkedAdd(canonical, bindingBytes)
	}
	work, workOK := checkedAdd(sourceReceipt.WorkUnits, shapeReceipt.WorkUnits)
	if workOK {
		work, workOK = checkedAdd(work, layoutReceipt.WorkUnits)
	}
	if workOK {
		work, workOK = checkedAdd(work, measure.bridgeWork)
	}
	if workOK {
		work, workOK = checkedAdd(work, bindingBytes)
	}
	retained, retainedOK := checkedAdd(frozenReceipt.RetainedBytes, sourceReceipt.RetainedBytes)
	if retainedOK {
		retained, retainedOK = checkedAdd(retained, layoutReceipt.RetainedBytes)
	}
	if retainedOK {
		var overhead uint64
		overhead, retainedOK = sum64(assemblyFixedRetainedBytes, receiptStringBytes(receipt), assemblyDigestStringBytes)
		if retainedOK {
			retained, retainedOK = checkedAdd(retained, overhead)
		}
	}
	working, workingOK := sum64(
		frozenReceipt.RetainedBytes, sourceReceipt.WorkingBytes, shapeReceipt.WorkingBytes,
		layoutReceipt.WorkingBytes, measure.bridgeWorking,
	)
	if workingOK && retained > working {
		working = retained
	}
	if !ok || !workOK || !retainedOK || canonical > normalized.MaxCanonicalBinaryBytes ||
		!workingOK || work > normalized.MaxWorkUnits || working > normalized.MaxWorkingBytes || retained > normalized.MaxRetainedBytes {
		return nil, Receipt{}, assemblyError(CodeResourceLimit, "assembly/resources", ErrResourceLimit)
	}
	receipt.CanonicalBinaryBytes = canonical
	receipt.WorkUnits = work
	receipt.WorkingBytes = working
	receipt.RetainedBytes = retained
	finalHash, finalBindingBytes, err := hashAssembly(ctx, receipt)
	if err != nil {
		return nil, Receipt{}, err
	}
	if bindingBytes != admittedBindingBytes || finalBindingBytes != bindingBytes {
		return nil, Receipt{}, assemblyError(CodeInvariantFailed, "assembly/hash/bytes", ErrInvariantFailed)
	}
	receipt.AssemblyHash = finalHash
	cloneReceiptStrings(&receipt)
	if err := contextFailure(ctx, "assembly/publish"); err != nil {
		return nil, Receipt{}, err
	}
	result = &Source{frozen: request.Text, layoutSource: layoutSource, layout: layout, fill: fill, receipt: receipt}
	return result, receipt, nil
}

func validateFrozenTextSource(value *evaluator.FrozenTextSource) (evaluator.FrozenTextSourceReceipt, error) {
	receipt := value.Receipt()
	fontSize, lineHeight, viewport, fill := value.FontSize(), value.LineHeight(), value.Viewport(), value.Fill()
	if receipt.SchemaVersion != evaluator.FrozenTextSourceSchemaVersion ||
		receipt.Profile != evaluator.FrozenTextSourceReceiptProfileV1 ||
		receipt.SourceProfile != evaluator.FrozenTextSourceProfileV1 ||
		receipt.DescriptorHashProfile != evaluator.FrozenTextSourceDescriptorHashProfileV1 ||
		receipt.DescriptorHash != evaluator.FrozenTextSourceDescriptorHash() ||
		receipt.BindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 || !canonicalDigest(receipt.BindingHash) ||
		receipt.DocumentHashProfile != evaluator.FrozenTextSourceDocumentHashProfileV1 ||
		receipt.BindingStatus != evaluator.FrozenTextSourceBindingStatusV1 ||
		receipt.CapabilityStatus != evaluator.FrozenTextSourceCapabilityStatusV1 ||
		receipt.AssetKind != documentv2.AssetKindText || receipt.FontAssetKind != documentv2.AssetKindFont ||
		receipt.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		receipt.EvaluatorProfile != documentv2.EvaluatorTextPinnedFontViewportPXV2 ||
		receipt.FontProfile != documentv2.TextFontProfileStaticGlyfV1 ||
		receipt.RunInputProfile != documentv2.TextRunInputProfileV1 || receipt.LayoutProfile != textlayout.ProfileV1 ||
		receipt.OutlineProfile != documentv2.TextOutlineProfileQ26_6V1 ||
		receipt.RasterProfile != documentv2.TextRasterProfileSupersample8x8V1 ||
		receipt.PaintProfile != documentv2.TextPaintProfileRGBA16SRGBV1 ||
		receipt.CoverageHashProfile != documentv2.TextCoverageHashProfileV1 ||
		receipt.UnicodeVersion != documentv2.TextUnicodeDataVersionV1 ||
		receipt.UnicodeValidationProfile != documentv2.TextUnicodeValidationTableProfileV1 ||
		receipt.UnicodeValidationHashProfile != documentv2.TextUnicodeValidationTableHashProfileV1 ||
		receipt.UnicodeValidationTableHash != documentv2.TextUnicodeValidationTableHashV1 ||
		receipt.CanonicalSourceHashProfile != evaluator.FrozenTextSourceCanonicalSourceHashProfileV1 ||
		receipt.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		receipt.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
		value.AssetID() != receipt.AssetID || value.AssetIndex() != receipt.AssetIndex ||
		value.Profile() != receipt.TextProfile || value.FontAssetID() != receipt.FontAssetID ||
		value.FontAssetIndex() != receipt.FontAssetIndex || value.FontContentHash() != receipt.FontContentHash ||
		fontSize.Numerator != receipt.FontSizeNumerator || fontSize.Denominator != receipt.FontSizeDenominator ||
		lineHeight.Numerator != receipt.LineHeightNumerator || lineHeight.Denominator != receipt.LineHeightDenominator ||
		viewport.WidthPixels != receipt.ViewportWidthPixels || viewport.HeightPixels != receipt.ViewportHeightPixels ||
		fill.R != receipt.FillR || fill.G != receipt.FillG || fill.B != receipt.FillB || fill.A != receipt.FillA ||
		value.Language() != receipt.Language || value.Script() != receipt.Script ||
		value.Direction() != receipt.Direction || value.Alignment() != receipt.Alignment ||
		value.AssetID() == "" || value.FontAssetID() == "" ||
		uint64(len(value.Text())) != receipt.TextBytes || !canonicalDigest(receipt.DocumentHash) ||
		!canonicalDigest(receipt.CanonicalSourceHash) ||
		!canonicalDigest(receipt.FontContentHash) || !canonicalDigest(receipt.TextSemanticHash) ||
		!canonicalDigest(receipt.LayoutInputHash) || receipt.DocumentCanonicalBytes == 0 ||
		receipt.CanonicalSourceBytes == 0 || receipt.CanonicalSourceBytes > receipt.CanonicalSourceUpperBytes ||
		receipt.TextSemanticCanonicalBytes == 0 || receipt.TextSemanticCanonicalBytes > receipt.TextSemanticUpperBytes ||
		receipt.LayoutInputCanonicalBytes == 0 || receipt.LayoutInputCanonicalBytes > receipt.LayoutInputUpperBytes ||
		receipt.BindingCanonicalBytes == 0 || receipt.CanonicalBinaryBytes == 0 ||
		receipt.WorkUnits == 0 || receipt.WorkingBytes == 0 || receipt.RetainedBytes == 0 {
		return evaluator.FrozenTextSourceReceipt{}, assemblyError(CodeInputInvalid, "assembly/frozen_text", ErrInputInvalid)
	}
	return receipt, nil
}

func validateProviderReceipt(provider sfntglyf.ShapeProviderReceipt, frozen evaluator.FrozenTextSourceReceipt) error {
	descriptor := sfntglyf.ShapeProviderDescriptor()
	if provider.SchemaVersion != sfntglyf.ShapeProviderSchemaVersion || provider.Profile != sfntglyf.ShapeProviderReceiptProfileV1 ||
		provider.ProviderProfile != sfntglyf.ShapeProviderProfileV1 || provider.ProviderHashProfile != sfntglyf.ShapeProviderHashProfileV1 ||
		provider.DescriptorHashProfile != sfntglyf.ShapeProviderDescriptorHashProfileV1 ||
		provider.DescriptorHash != sfntglyf.ShapeProviderDescriptorHash() ||
		provider.DependencyLockProfile != descriptor.DependencyLockProfile ||
		provider.DependencyFingerprintProfile != descriptor.DependencyFingerprintProfile ||
		provider.DependencyFingerprint != descriptor.DependencyFingerprint ||
		provider.GoTextModuleVersion != sfntglyf.ShapeGoTextModuleVersionV1 ||
		provider.EmbeddedHarfBuzzVersion != sfntglyf.ShapeEmbeddedHarfBuzzVersionV1 ||
		provider.EmbeddedHarfBuzzCommit != sfntglyf.ShapeEmbeddedHarfBuzzCommitV1 ||
		provider.BindingStatus != sfntglyf.ShapeProviderBindingStatusV1 ||
		provider.CapabilityStatus != sfntglyf.ShapeProviderCapabilityStatusV1 ||
		provider.LayoutGraphProfile != sfntglyf.LayoutGraphReceiptProfileV1 ||
		provider.LayoutGraphHashProfile != sfntglyf.LayoutGraphCanonicalHashProfileV1 || !canonicalDigest(provider.LayoutGraphHash) ||
		provider.OutlineSetHashProfile != sfntglyf.OutlineSetHashProfileV1 || !canonicalDigest(provider.OutlineSetHash) ||
		provider.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
		provider.ClusterPolicy != sfntglyf.ShapeClusterPolicyV1 || provider.FeaturePolicy != sfntglyf.ShapeFeaturePolicyV1 ||
		provider.DirectionNormalization != sfntglyf.ShapeDirectionNormalizationProfileV1 ||
		!canonicalDigest(provider.ProviderHash) || provider.FontContentHash != frozen.FontContentHash ||
		provider.UnitsPerEm < 16 || provider.UnitsPerEm > 16_384 || provider.GlyphCount < 2 ||
		provider.CanonicalBinaryBytes == 0 || provider.WorkUnits == 0 || provider.WorkingBytes == 0 || provider.RetainedBytes == 0 {
		return assemblyError(CodeBinding, "assembly/provider", ErrBinding)
	}
	return nil
}

func validateLayoutSourceReceipt(source sfntglyf.LayoutSourceReceipt, provider sfntglyf.ShapeProviderReceipt, frozen evaluator.FrozenTextSourceReceipt) error {
	providerDescriptor := sfntglyf.ShapeProviderDescriptor()
	if source.SchemaVersion != sfntglyf.LayoutSourceSchemaVersion || source.Profile != sfntglyf.LayoutSourceReceiptProfileV1 ||
		source.LayoutSourceProfile != sfntglyf.LayoutSourceProfileV1 || source.LayoutSourceHashProfile != sfntglyf.LayoutSourceHashProfileV1 ||
		source.DescriptorHashProfile != sfntglyf.LayoutSourceDescriptorHashProfileV1 ||
		source.DescriptorHash != sfntglyf.LayoutSourceDescriptorHash() ||
		source.BindingStatus != sfntglyf.LayoutSourceBindingStatusV1 ||
		source.ShapeProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
		source.ShapeProviderHashProfile != sfntglyf.ShapeProviderHashProfileV1 ||
		source.DependencyFingerprintProfile != providerDescriptor.DependencyFingerprintProfile ||
		source.DependencyFingerprint != providerDescriptor.DependencyFingerprint ||
		source.LayoutGraphProfile != sfntglyf.LayoutGraphReceiptProfileV1 ||
		source.LayoutGraphHashProfile != sfntglyf.LayoutGraphCanonicalHashProfileV1 ||
		source.OutlineSetProfile != sfntglyf.OutlineSetReceiptProfileV1 ||
		source.OutlineSetHashProfile != sfntglyf.OutlineSetHashProfileV1 ||
		source.FontReceiptProfile != sfntglyf.ReceiptProfileV1 || source.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
		source.OutlineProfile != sfntglyf.OutlineSetOutlineProfileV1 ||
		source.CommandStatus != sfntglyf.OutlineSetCommandStatusV1 ||
		source.RoundingStatus != sfntglyf.OutlineSetRoundingStatusV1 ||
		source.HintingStatus != sfntglyf.OutlineSetHintingStatusV1 ||
		source.LayoutStatus != sfntglyf.LayoutSourceLayoutStatusV1 ||
		source.VariableLayoutStatus != sfntglyf.LayoutGraphVariableLayoutStatusV1 ||
		source.RasterStatus != sfntglyf.LayoutSourceRasterStatusV1 ||
		source.CapabilityStatus != sfntglyf.LayoutSourceCapabilityStatusV1 ||
		!canonicalDigest(source.LayoutSourceHash) || source.ShapeProviderProfile != provider.ProviderProfile ||
		source.ShapeProviderHashProfile != provider.ProviderHashProfile ||
		source.ShapeProviderHash != provider.ProviderHash || source.DependencyFingerprint != provider.DependencyFingerprint ||
		source.LayoutGraphHashProfile != provider.LayoutGraphHashProfile || source.OutlineSetHashProfile != provider.OutlineSetHashProfile ||
		source.FontContentHashProfile != provider.FontContentHashProfile ||
		source.LayoutGraphHash != provider.LayoutGraphHash || source.OutlineSetHash != provider.OutlineSetHash ||
		source.FontContentHash != provider.FontContentHash || source.FontContentHash != frozen.FontContentHash ||
		source.UnitsPerEm != provider.UnitsPerEm || source.GlyphCount != provider.GlyphCount ||
		source.CanonicalBinaryBytes == 0 || source.WorkUnits == 0 || source.WorkingBytes == 0 || source.RetainedBytes == 0 {
		return assemblyError(CodeBinding, "assembly/layout_source/receipt", ErrBinding)
	}
	return nil
}

func validateShapeReceipt(shape sfntglyf.ShapeReceipt, provider sfntglyf.ShapeProviderReceipt, source sfntglyf.LayoutSourceReceipt, frozen evaluator.FrozenTextSourceReceipt) error {
	direction, err := shapeDirection(frozen.Direction)
	if err != nil {
		return err
	}
	if shape.SchemaVersion != sfntglyf.ShapeProviderSchemaVersion || shape.Profile != sfntglyf.ShapeReceiptProfileV1 ||
		shape.ProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
		shape.ProviderHashProfile != sfntglyf.ShapeProviderHashProfileV1 || shape.ProviderHash != provider.ProviderHash ||
		shape.DependencyFingerprintProfile != provider.DependencyFingerprintProfile ||
		shape.DependencyFingerprint != provider.DependencyFingerprint ||
		shape.LayoutGraphHashProfile != sfntglyf.LayoutGraphCanonicalHashProfileV1 || shape.LayoutGraphHash != source.LayoutGraphHash ||
		shape.OutlineSetHashProfile != sfntglyf.OutlineSetHashProfileV1 || shape.OutlineSetHash != source.OutlineSetHash ||
		shape.FontContentHashProfile != sfntglyf.ContentHashProfileV1 || shape.FontContentHash != frozen.FontContentHash ||
		shape.OutputProfile != sfntglyf.ShapeOutputProfileV1 || shape.ClusterPolicy != provider.ClusterPolicy ||
		shape.FeaturePolicy != provider.FeaturePolicy || shape.DirectionNormalization != provider.DirectionNormalization ||
		shape.InputHashProfile != sfntglyf.ShapeInputHashProfileV1 || shape.OutputHashProfile != sfntglyf.ShapeOutputHashProfileV1 ||
		shape.Language != frozen.Language || shape.Script != frozen.Script || shape.Direction != direction ||
		shape.TextBytes != frozen.TextBytes || !canonicalDigest(shape.InputHash) || !canonicalDigest(shape.OutputHash) ||
		shape.OutputGlyphCount > shape.AdmissionGlyphBound || shape.CanonicalBinaryBytes == 0 ||
		shape.WorkUnits == 0 || shape.WorkingBytes == 0 || shape.RetainedBytes == 0 {
		return assemblyError(CodeBinding, "assembly/shape/receipt", ErrBinding)
	}
	return nil
}

func inspectShapedClosure(ctx context.Context, view sfntglyf.ShapedTextView, receipt sfntglyf.ShapeReceipt, source *sfntglyf.LayoutSource, limits Limits) (assemblyMeasure, []uint32, error) {
	measure := assemblyMeasure{lines: uint64(view.LineCount())}
	if measure.lines != receipt.LineCount || measure.lines > limits.MaxLines {
		return assemblyMeasure{}, nil, assemblyError(CodeResourceLimit, "assembly/shape/lines", ErrResourceLimit)
	}
	uniqueLimit := min64(limits.MaxUniqueGlyphs, limits.Layout.MaxUniqueGlyphs)
	uniqueCapacity := receipt.OutputGlyphCount
	if uniqueCapacity > uniqueLimit {
		uniqueCapacity = uniqueLimit
	}
	if domain := uint64(source.GlyphCount()); uniqueCapacity > domain {
		uniqueCapacity = domain
	}
	if uniqueCapacity > uint64(^uint(0)>>1) {
		return assemblyMeasure{}, nil, assemblyError(CodeResourceLimit, "assembly/shape/unique_glyphs", ErrResourceLimit)
	}
	closure := make(map[uint32]struct{}, int(uniqueCapacity))
	for lineIndex := 0; lineIndex < view.LineCount(); lineIndex++ {
		if lineIndex&255 == 0 {
			if err := contextFailure(ctx, "assembly/shape/lines"); err != nil {
				return assemblyMeasure{}, nil, err
			}
		}
		line, err := view.Line(lineIndex)
		if err != nil {
			return assemblyMeasure{}, nil, assemblyError(CodeInvariantFailed, "assembly/shape/line", errors.Join(ErrInvariantFailed, err))
		}
		if count := uint64(line.GlyphCount()); count > measure.maxGlyphsPerLine {
			measure.maxGlyphsPerLine = count
		}
		for glyphIndex := 0; glyphIndex < line.GlyphCount(); glyphIndex++ {
			if glyphIndex&1023 == 0 {
				if err := contextFailure(ctx, "assembly/shape/glyphs"); err != nil {
					return assemblyMeasure{}, nil, err
				}
			}
			glyph, err := line.Glyph(glyphIndex)
			if err != nil || glyph.GlyphID == 0 || glyph.GlyphID >= source.GlyphCount() {
				return assemblyMeasure{}, nil, assemblyError(CodeBinding, "assembly/shape/glyph", ErrBinding)
			}
			var addOK bool
			measure.glyphs, addOK = checkedAdd(measure.glyphs, 1)
			if !addOK {
				return assemblyMeasure{}, nil, assemblyError(CodeResourceLimit, "assembly/shape/glyphs", ErrResourceLimit)
			}
			if measure.glyphs > limits.MaxGlyphs {
				return assemblyMeasure{}, nil, assemblyError(CodeResourceLimit, "assembly/shape/glyphs", ErrResourceLimit)
			}
			if _, exists := closure[glyph.GlyphID]; !exists {
				if uint64(len(closure)) >= uniqueLimit {
					return assemblyMeasure{}, nil, assemblyError(CodeResourceLimit, "assembly/shape/unique_glyphs", ErrResourceLimit)
				}
				closure[glyph.GlyphID] = struct{}{}
			}
		}
	}
	if measure.glyphs != receipt.OutputGlyphCount || uint64(len(closure)) > uniqueLimit {
		return assemblyMeasure{}, nil, assemblyError(CodeBinding, "assembly/shape/counts", ErrBinding)
	}
	measure.uniqueGlyphs = uint64(len(closure))
	glyphIDs := make([]uint32, 0, len(closure))
	for glyphID := range closure {
		glyphIDs = append(glyphIDs, glyphID)
	}
	sort.Slice(glyphIDs, func(left, right int) bool { return glyphIDs[left] < glyphIDs[right] })
	return measure, glyphIDs, nil
}

func inspectOutlineClosure(ctx context.Context, source *sfntglyf.LayoutSource, glyphIDs []uint32, measure *assemblyMeasure, limits Limits) error {
	commandLimit := min64(limits.MaxCommands, limits.Layout.MaxCommands)
	for outlineIndex, glyphID := range glyphIDs {
		if outlineIndex&255 == 0 {
			if err := contextFailure(ctx, "assembly/outlines"); err != nil {
				return err
			}
		}
		outline, ok := source.GlyphOutline(glyphID)
		if !ok || outline.GlyphID() != glyphID {
			return assemblyError(CodeBinding, "assembly/outlines/glyph", ErrBinding)
		}
		if outline.CommandCount() > limits.Layout.MaxCommandsPerGlyph {
			return assemblyError(CodeResourceLimit, "assembly/outlines/commands_per_glyph", ErrResourceLimit)
		}
		if outline.CommandCount() > measure.maxCommandsPerGlyph {
			measure.maxCommandsPerGlyph = outline.CommandCount()
		}
		if outline.ContourCount() > limits.Layout.MaxContours-measure.contours {
			return assemblyError(CodeResourceLimit, "assembly/outlines/contours", ErrResourceLimit)
		}
		var addOK bool
		measure.contours, addOK = checkedAdd(measure.contours, outline.ContourCount())
		if !addOK {
			return assemblyError(CodeResourceLimit, "assembly/outlines/contours", ErrResourceLimit)
		}
		for commandIndex := uint64(0); commandIndex < outline.CommandCount(); commandIndex++ {
			if measure.commands >= commandLimit {
				return assemblyError(CodeResourceLimit, "assembly/outlines/commands", ErrResourceLimit)
			}
			if commandIndex&1023 == 0 {
				if err := contextFailure(ctx, "assembly/outlines/commands"); err != nil {
					return err
				}
			}
			command, ok := outline.Command(commandIndex)
			if !ok || !validOutlineCommand(command) {
				return assemblyError(CodeBinding, "assembly/outlines/command", ErrBinding)
			}
			pointCount := uint64(command.PointCount())
			if pointCount > limits.MaxCommandPointOccurrences-measure.points {
				return assemblyError(CodeResourceLimit, "assembly/outlines/points", ErrResourceLimit)
			}
			measure.commands, addOK = checkedAdd(measure.commands, 1)
			if addOK {
				measure.points, addOK = checkedAdd(measure.points, pointCount)
			}
			if !addOK {
				return assemblyError(CodeResourceLimit, "assembly/outlines/resources", ErrResourceLimit)
			}
			if measure.commands > commandLimit || measure.points > limits.MaxCommandPointOccurrences {
				return assemblyError(CodeResourceLimit, "assembly/outlines/resources", ErrResourceLimit)
			}
		}
	}
	return nil
}

func validOutlineCommand(command sfntglyf.LayoutOutlineCommandView) bool {
	switch command.Op() {
	case sfntglyf.OutlineMoveTo, sfntglyf.OutlineLineTo:
		return command.PointCount() == 1
	case sfntglyf.OutlineQuadTo:
		return command.PointCount() == 2
	case sfntglyf.OutlineClose:
		return command.PointCount() == 0
	default:
		return false
	}
}

func admitBridgeResources(measure *assemblyMeasure, limits Limits) error {
	working, work, ok := bridgeCountCharge(
		measure.lines, measure.glyphs, measure.uniqueGlyphs, measure.commands, measure.points,
	)
	if !ok || working > limits.MaxWorkingBytes || work > limits.MaxWorkUnits {
		return assemblyError(CodeResourceLimit, "assembly/bridge/resources", ErrResourceLimit)
	}
	measure.bridgeWorking = working
	measure.bridgeWork = work
	return nil
}

func materializeLayoutInput(ctx context.Context, view sfntglyf.ShapedTextView, source *sfntglyf.LayoutSource, glyphIDs []uint32, measure assemblyMeasure) ([]textlayout.ShapedLine, []textlayout.GlyphOutline, error) {
	lines := make([]textlayout.ShapedLine, view.LineCount())
	for lineIndex := range lines {
		if lineIndex&255 == 0 {
			if err := contextFailure(ctx, "assembly/materialize/lines"); err != nil {
				return nil, nil, err
			}
		}
		line, err := view.Line(lineIndex)
		if err != nil {
			return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/line", err)
		}
		lines[lineIndex] = textlayout.ShapedLine{ByteStart: line.ByteStart(), ByteEnd: line.ByteEnd(), Glyphs: make([]textlayout.ShapedGlyph, line.GlyphCount())}
		for glyphIndex := range lines[lineIndex].Glyphs {
			if glyphIndex&1023 == 0 {
				if err := contextFailure(ctx, "assembly/materialize/glyphs"); err != nil {
					return nil, nil, err
				}
			}
			glyph, err := line.Glyph(glyphIndex)
			if err != nil {
				return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/glyph", err)
			}
			lines[lineIndex].Glyphs[glyphIndex] = textlayout.ShapedGlyph{
				GlyphID: glyph.GlyphID, ClusterByteOffset: glyph.ClusterByteOffset,
				XAdvance: textlayout.Q26_6(glyph.XAdvance), YAdvance: textlayout.Q26_6(glyph.YAdvance),
				XOffset: textlayout.Q26_6(glyph.XOffset), YOffset: textlayout.Q26_6(glyph.YOffset),
			}
		}
	}
	outlines := make([]textlayout.GlyphOutline, len(glyphIDs))
	for outlineIndex, glyphID := range glyphIDs {
		outline, ok := source.GlyphOutline(glyphID)
		if !ok {
			return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/outline", ErrInvariantFailed)
		}
		outlines[outlineIndex] = textlayout.GlyphOutline{GlyphID: glyphID, Commands: make([]textlayout.OutlineCommand, int(outline.CommandCount()))}
		for commandIndex := range outlines[outlineIndex].Commands {
			if commandIndex&1023 == 0 {
				if err := contextFailure(ctx, "assembly/materialize/commands"); err != nil {
					return nil, nil, err
				}
			}
			command, ok := outline.Command(uint64(commandIndex))
			if !ok {
				return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/command", ErrInvariantFailed)
			}
			points := make([]textlayout.Point, int(command.PointCount()))
			for pointIndex := range points {
				point, ok := command.Point(uint8(pointIndex))
				if !ok {
					return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/point", ErrInvariantFailed)
				}
				points[pointIndex] = textlayout.Point{X: textlayout.Q26_6(point.X), Y: textlayout.Q26_6(point.Y)}
			}
			op, ok := layoutOutlineOp(command.Op())
			if !ok {
				return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/op", ErrInvariantFailed)
			}
			outlines[outlineIndex].Commands[commandIndex] = textlayout.OutlineCommand{Op: op, Points: points}
		}
	}
	if uint64(len(outlines)) != measure.uniqueGlyphs {
		return nil, nil, assemblyError(CodeInvariantFailed, "assembly/materialize/count", ErrInvariantFailed)
	}
	return lines, outlines, nil
}

func validateLayoutReceipt(layout textlayout.Receipt, shape sfntglyf.ShapeReceipt, source sfntglyf.LayoutSourceReceipt, frozen evaluator.FrozenTextSourceReceipt, measure assemblyMeasure, fontSize documentv2.Rational, viewport documentv2.RasterViewport) error {
	providerDescriptor := sfntglyf.ShapeProviderDescriptor()
	if layout.SchemaVersion != textlayout.ReceiptSchemaVersion || layout.Profile != textlayout.ReceiptProfileV1 ||
		layout.LayoutProfile != textlayout.ProfileV1 ||
		layout.TextProfile != frozen.TextProfile || layout.RunInputProfile != frozen.RunInputProfile ||
		layout.ShapedStreamProfile != textlayout.NormalizedShapedStreamProfileV1 || layout.ShapedStreamProfile != shape.OutputProfile ||
		layout.FontProfile != frozen.FontProfile ||
		layout.OutlineProfile != frozen.OutlineProfile || layout.TextSemanticHash != frozen.TextSemanticHash ||
		layout.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		layout.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
		layout.LayoutInputHash != frozen.LayoutInputHash || layout.FontContentHash != source.FontContentHash ||
		layout.UnicodeTableProfile != documentv2.TextUnicodeValidationTableProfileV1 ||
		layout.UnicodeTableHashProfile != documentv2.TextUnicodeValidationTableHashProfileV1 ||
		layout.UnicodeTableHash != documentv2.TextUnicodeValidationTableHashV1 ||
		layout.ShapeProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
		layout.ShapeProviderProfile != source.ShapeProviderProfile ||
		layout.ShapeProviderFingerprint != providerDescriptor.DependencyFingerprint ||
		layout.ShapeProviderFingerprint != source.DependencyFingerprint ||
		layout.GlyphStreamHashProfile != textlayout.GlyphStreamHashProfileV1 || !canonicalDigest(layout.GlyphStreamHash) ||
		layout.OutlineSetHashProfile != textlayout.OutlineSetHashProfileV1 || !canonicalDigest(layout.OutlineSetHash) ||
		layout.PlacementHashProfile != textlayout.PlacementHashProfileV1 || !canonicalDigest(layout.PlacementHash) ||
		layout.LayoutHashProfile != textlayout.LayoutHashProfileV1 ||
		layout.WidthPixels != viewport.WidthPixels || layout.HeightPixels != viewport.HeightPixels ||
		layout.LineCount != measure.lines || layout.GlyphCount != measure.glyphs ||
		layout.UniqueGlyphCount != measure.uniqueGlyphs || layout.ContourCount != measure.contours ||
		layout.CommandCount != measure.commands || fontSize.Numerator != frozen.FontSizeNumerator ||
		fontSize.Denominator != frozen.FontSizeDenominator || !canonicalDigest(layout.LayoutHash) ||
		layout.CanonicalBinaryBytes == 0 || layout.WorkUnits == 0 || layout.WorkingBytes == 0 || layout.RetainedBytes == 0 {
		return assemblyError(CodeBinding, "assembly/layout/receipt", ErrBinding)
	}
	return nil
}

func makeReceipt(frozen evaluator.FrozenTextSourceReceipt, provider sfntglyf.ShapeProviderReceipt, source sfntglyf.LayoutSourceReceipt, shape sfntglyf.ShapeReceipt, layout textlayout.Receipt, measure assemblyMeasure, fontSize documentv2.Rational, viewport documentv2.RasterViewport, fill color.RGBA16Input) Receipt {
	return Receipt{
		SchemaVersion: SchemaVersion, Profile: ReceiptProfileV1, DescriptorHashProfile: DescriptorHashProfileV1,
		DescriptorHash: DescriptorHash(), AssemblyHashProfile: AssemblyHashProfileV1,
		FrozenTextProfile: frozen.SourceProfile, FrozenTextBindingHashProfile: frozen.BindingHashProfile,
		FrozenTextBindingHash: frozen.BindingHash, DocumentHashProfile: frozen.DocumentHashProfile,
		DocumentHash: frozen.DocumentHash, TextAssetID: string(frozen.AssetID), TextAssetIndex: frozen.AssetIndex,
		FontAssetID: string(frozen.FontAssetID), FontAssetIndex: frozen.FontAssetIndex,
		FontContentHashProfile: provider.FontContentHashProfile, FontContentHash: frozen.FontContentHash,
		TextSemanticHashProfile: frozen.TextSemanticHashProfile, TextSemanticHash: frozen.TextSemanticHash,
		LayoutInputHashProfile: frozen.LayoutInputHashProfile, LayoutInputHash: frozen.LayoutInputHash,
		ShapeProviderProfile:     provider.ProviderProfile,
		ShapeProviderHashProfile: provider.ProviderHashProfile, ShapeProviderHash: provider.ProviderHash,
		DependencyFingerprintProfile: provider.DependencyFingerprintProfile, DependencyFingerprint: provider.DependencyFingerprint,
		LayoutGraphHashProfile: provider.LayoutGraphHashProfile, LayoutGraphHash: provider.LayoutGraphHash,
		SourceOutlineSetHashProfile: provider.OutlineSetHashProfile, SourceOutlineSetHash: provider.OutlineSetHash,
		LayoutSourceHashProfile: source.LayoutSourceHashProfile, LayoutSourceHash: source.LayoutSourceHash,
		ShapeInputHashProfile: shape.InputHashProfile, ShapeInputHash: shape.InputHash,
		ShapeOutputProfile: shape.OutputProfile, ShapeOutputHashProfile: shape.OutputHashProfile, ShapeOutputHash: shape.OutputHash,
		LayoutReceiptProfile: layout.Profile, GlyphStreamHashProfile: layout.GlyphStreamHashProfile,
		GlyphStreamHash: layout.GlyphStreamHash, ScaledOutlineHashProfile: layout.OutlineSetHashProfile,
		ScaledOutlineHash: layout.OutlineSetHash, PlacementHashProfile: layout.PlacementHashProfile,
		PlacementHash: layout.PlacementHash, LayoutHashProfile: layout.LayoutHashProfile, LayoutHash: layout.LayoutHash,
		ShapingProvenanceStatus: ShapingProvenanceStatusV1, PaintProvenanceStatus: PaintProvenanceStatusV1,
		CapabilityStatus: CapabilityStatusV1, WidthPixels: viewport.WidthPixels, HeightPixels: viewport.HeightPixels,
		FontSizeNumerator: fontSize.Numerator, FontSizeDenominator: fontSize.Denominator,
		FillR: fill.R, FillG: fill.G, FillB: fill.B, FillA: fill.A,
		LineCount: measure.lines, GlyphCount: measure.glyphs, UniqueGlyphCount: measure.uniqueGlyphs,
		ContourCount: measure.contours, CommandCount: measure.commands, CommandPointOccurrences: measure.points,
	}
}

func shapeDirection(value documentv2.TextDirection) (sfntglyf.ShapeDirection, error) {
	switch value {
	case documentv2.TextDirectionLTR:
		return sfntglyf.ShapeDirectionLTR, nil
	case documentv2.TextDirectionRTL:
		return sfntglyf.ShapeDirectionRTL, nil
	default:
		return "", assemblyError(CodeInputInvalid, "assembly/direction", ErrInputInvalid)
	}
}

func layoutOutlineOp(value sfntglyf.OutlineCommandOp) (textlayout.OutlineCommandOp, bool) {
	switch value {
	case sfntglyf.OutlineMoveTo:
		return textlayout.MoveTo, true
	case sfntglyf.OutlineLineTo:
		return textlayout.LineTo, true
	case sfntglyf.OutlineQuadTo:
		return textlayout.QuadTo, true
	case sfntglyf.OutlineClose:
		return textlayout.Close, true
	default:
		return "", false
	}
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return assemblyError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return assemblyError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func canonicalDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[7:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func max64(values ...uint64) uint64 {
	var result uint64
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func sum64(values ...uint64) (uint64, bool) {
	var result uint64
	for _, value := range values {
		var ok bool
		result, ok = checkedAdd(result, value)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func receiptStringBytes(value Receipt) uint64 {
	var total uint64
	for _, field := range []string{
		value.Profile, value.DescriptorHashProfile, value.DescriptorHash, value.AssemblyHashProfile,
		value.FrozenTextProfile, value.FrozenTextBindingHashProfile, value.FrozenTextBindingHash,
		value.DocumentHashProfile, value.DocumentHash, value.TextAssetID, value.FontAssetID,
		value.FontContentHashProfile, value.FontContentHash, value.TextSemanticHashProfile,
		value.TextSemanticHash, value.LayoutInputHashProfile, value.LayoutInputHash,
		value.ShapeProviderProfile, value.ShapeProviderHashProfile, value.ShapeProviderHash,
		value.DependencyFingerprintProfile, value.DependencyFingerprint, value.LayoutGraphHashProfile,
		value.LayoutGraphHash, value.SourceOutlineSetHashProfile, value.SourceOutlineSetHash,
		value.LayoutSourceHashProfile, value.LayoutSourceHash, value.ShapeInputHashProfile,
		value.ShapeInputHash, value.ShapeOutputProfile, value.ShapeOutputHashProfile, value.ShapeOutputHash,
		value.LayoutReceiptProfile, value.GlyphStreamHashProfile, value.GlyphStreamHash,
		value.ScaledOutlineHashProfile, value.ScaledOutlineHash, value.PlacementHashProfile,
		value.PlacementHash, value.LayoutHashProfile, value.LayoutHash, value.ShapingProvenanceStatus,
		value.PaintProvenanceStatus, value.CapabilityStatus,
	} {
		next, ok := checkedAdd(total, uint64(len(field)))
		if !ok {
			return ^uint64(0)
		}
		total = next
	}
	return total
}
