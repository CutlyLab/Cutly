package textassembly

import (
	"context"
	"errors"
	"math/bits"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

func capLayoutSourceLimits(limits Limits, frozen evaluator.FrozenTextSourceReceipt) (sfntglyf.LayoutSourceLimits, error) {
	result := limits.LayoutSource
	working, ok := remainingBudget(limits.MaxWorkingBytes, frozen.RetainedBytes)
	if !ok {
		return sfntglyf.LayoutSourceLimits{}, resourceError("assembly/preflight/layout_source/working")
	}
	retainedReserve, ok := sum64(frozen.RetainedBytes, assemblyFixedRetainedBytes, assemblyDigestStringBytes)
	if !ok {
		return sfntglyf.LayoutSourceLimits{}, resourceError("assembly/preflight/layout_source/retained")
	}
	retained, ok := remainingBudget(limits.MaxRetainedBytes, retainedReserve)
	if !ok {
		return sfntglyf.LayoutSourceLimits{}, resourceError("assembly/preflight/layout_source/retained")
	}
	result.MaxCanonicalBinaryBytes = min64(result.MaxCanonicalBinaryBytes, limits.MaxCanonicalBinaryBytes)
	result.MaxWorkUnits = min64(result.MaxWorkUnits, limits.MaxWorkUnits)
	result.MaxWorkingBytes = min64(result.MaxWorkingBytes, working)
	result.MaxRetainedBytes = min64(result.MaxRetainedBytes, retained)
	return result, nil
}

func capShapeLimits(
	ctx context.Context,
	limits Limits,
	frozen evaluator.FrozenTextSourceReceipt,
	provider sfntglyf.ShapeProviderReceipt,
	source sfntglyf.LayoutSourceReceipt,
) (sfntglyf.ShapeLimits, error) {
	result := limits.Shape
	placeholder := "sha256:" + strings.Repeat("0", 64)
	provisionalShape := sfntglyf.ShapeReceipt{
		InputHashProfile: sfntglyf.ShapeInputHashProfileV1, InputHash: placeholder,
		OutputProfile:     sfntglyf.ShapeOutputProfileV1,
		OutputHashProfile: sfntglyf.ShapeOutputHashProfileV1, OutputHash: placeholder,
	}
	provisionalLayout := textlayout.Receipt{
		Profile:                textlayout.ReceiptProfileV1,
		GlyphStreamHashProfile: textlayout.GlyphStreamHashProfileV1, GlyphStreamHash: placeholder,
		OutlineSetHashProfile: textlayout.OutlineSetHashProfileV1, OutlineSetHash: placeholder,
		PlacementHashProfile: textlayout.PlacementHashProfileV1, PlacementHash: placeholder,
		LayoutHashProfile: textlayout.LayoutHashProfileV1, LayoutHash: placeholder,
	}
	fontSize := documentv2.Rational{Numerator: frozen.FontSizeNumerator, Denominator: frozen.FontSizeDenominator}
	viewport := documentv2.RasterViewport{WidthPixels: frozen.ViewportWidthPixels, HeightPixels: frozen.ViewportHeightPixels}
	fill := color.RGBA16Input{R: frozen.FillR, G: frozen.FillG, B: frozen.FillB, A: frozen.FillA}
	provisional := makeReceipt(
		frozen, provider, source, provisionalShape, provisionalLayout, assemblyMeasure{}, fontSize, viewport, fill,
	)
	_, bindingBytes, err := hashAssembly(ctx, provisional)
	if err != nil {
		return sfntglyf.ShapeLimits{}, err
	}
	canonicalReserve, ok := sum64(source.CanonicalBinaryBytes, bindingBytes, 1)
	if !ok {
		return sfntglyf.ShapeLimits{}, resourceError("assembly/preflight/shape/canonical")
	}
	canonical, ok := remainingBudget(limits.MaxCanonicalBinaryBytes, canonicalReserve)
	if !ok {
		return sfntglyf.ShapeLimits{}, resourceError("assembly/preflight/shape/canonical")
	}
	workReserve, ok := sum64(source.WorkUnits, bindingBytes, 1)
	if !ok {
		return sfntglyf.ShapeLimits{}, resourceError("assembly/preflight/shape/work")
	}
	work, ok := remainingBudget(limits.MaxWorkUnits, workReserve)
	if !ok {
		return sfntglyf.ShapeLimits{}, resourceError("assembly/preflight/shape/work")
	}
	baseWorking, ok := sum64(frozen.RetainedBytes, source.WorkingBytes, assemblyFixedWorkingBytes, 64<<10)
	if !ok {
		return sfntglyf.ShapeLimits{}, resourceError("assembly/preflight/shape/working")
	}
	working, ok := remainingBudget(limits.MaxWorkingBytes, baseWorking)
	if !ok {
		return sfntglyf.ShapeLimits{}, resourceError("assembly/preflight/shape/working")
	}
	result.MaxTextBytes = min64(result.MaxTextBytes, limits.Layout.MaxTextBytes)
	result.MaxLines = min64(result.MaxLines, min64(limits.MaxLines, limits.Layout.MaxLines))
	result.MaxGlyphs = min64(result.MaxGlyphs, min64(limits.MaxGlyphs, limits.Layout.MaxGlyphs))
	result.MaxGlyphsPerLine = min64(result.MaxGlyphsPerLine, min64(limits.MaxGlyphs, limits.Layout.MaxGlyphsPerLine))
	result.MaxCanonicalBytes = min64(result.MaxCanonicalBytes, canonical)
	result.MaxWorkUnits = min64(result.MaxWorkUnits, work)
	result.MaxWorkingBytes = min64(result.MaxWorkingBytes, working)
	return result, nil
}

// preAdmitShapeScalars runs before the first assembly-owned proportional map or slice allocation.
func preAdmitShapeScalars(
	ctx context.Context,
	frozen evaluator.FrozenTextSourceReceipt,
	provider sfntglyf.ShapeProviderReceipt,
	source sfntglyf.LayoutSourceReceipt,
	shape sfntglyf.ShapeReceipt,
	glyphDomain uint32,
	limits Limits,
) error {
	if shape.LineCount == 0 || shape.LineCount > limits.MaxLines || shape.OutputGlyphCount > limits.MaxGlyphs {
		return resourceError("assembly/preflight/shape/counts")
	}
	uniqueUpper := shape.OutputGlyphCount
	uniqueLimit := min64(limits.MaxUniqueGlyphs, limits.Layout.MaxUniqueGlyphs)
	if uniqueUpper > uniqueLimit {
		uniqueUpper = uniqueLimit
	}
	if domain := uint64(glyphDomain); uniqueUpper > domain {
		uniqueUpper = domain
	}
	placeholder := "sha256:" + strings.Repeat("0", 64)
	provisionalLayout := textlayout.Receipt{
		Profile:                textlayout.ReceiptProfileV1,
		GlyphStreamHashProfile: textlayout.GlyphStreamHashProfileV1, GlyphStreamHash: placeholder,
		OutlineSetHashProfile: textlayout.OutlineSetHashProfileV1, OutlineSetHash: placeholder,
		PlacementHashProfile: textlayout.PlacementHashProfileV1, PlacementHash: placeholder,
		LayoutHashProfile: textlayout.LayoutHashProfileV1, LayoutHash: placeholder,
	}
	fontSize := documentv2.Rational{Numerator: frozen.FontSizeNumerator, Denominator: frozen.FontSizeDenominator}
	viewport := documentv2.RasterViewport{WidthPixels: frozen.ViewportWidthPixels, HeightPixels: frozen.ViewportHeightPixels}
	fill := color.RGBA16Input{R: frozen.FillR, G: frozen.FillG, B: frozen.FillB, A: frozen.FillA}
	provisionalMeasure := assemblyMeasure{lines: shape.LineCount, glyphs: shape.OutputGlyphCount, uniqueGlyphs: uniqueUpper}
	provisional := makeReceipt(frozen, provider, source, shape, provisionalLayout, provisionalMeasure, fontSize, viewport, fill)
	_, bindingBytes, err := hashAssembly(ctx, provisional)
	if err != nil {
		return err
	}
	canonical, ok := sum64(source.CanonicalBinaryBytes, shape.CanonicalBinaryBytes, bindingBytes)
	if !ok || canonical >= limits.MaxCanonicalBinaryBytes {
		return resourceError("assembly/preflight/shape/canonical")
	}
	work, ok := sum64(source.WorkUnits, shape.WorkUnits, bindingBytes)
	if !ok || work >= limits.MaxWorkUnits {
		return resourceError("assembly/preflight/shape/work")
	}
	perGlyphCommands := min64(source.MaxCommandsPerGlyph, limits.Layout.MaxCommandsPerGlyph)
	commandUpper, ok := checkedMul(uniqueUpper, perGlyphCommands)
	if !ok || commandUpper > source.CommandCount {
		commandUpper = source.CommandCount
	}
	commandLimit := min64(limits.MaxCommands, limits.Layout.MaxCommands)
	if commandUpper > commandLimit {
		commandUpper = commandLimit
	}
	pointUpper, ok := checkedMul(commandUpper, 2)
	if !ok || pointUpper > source.CommandPointOccurrences {
		pointUpper = source.CommandPointOccurrences
	}
	if pointUpper > limits.MaxCommandPointOccurrences {
		pointUpper = limits.MaxCommandPointOccurrences
	}
	bridge, bridgeWork, ok := bridgeCountCharge(
		shape.LineCount, shape.OutputGlyphCount, uniqueUpper, commandUpper, pointUpper,
	)
	if !ok {
		return resourceError("assembly/preflight/bridge/overflow")
	}
	working, ok := sum64(frozen.RetainedBytes, source.WorkingBytes, shape.WorkingBytes, bridge)
	if !ok || working > limits.MaxWorkingBytes {
		return resourceError("assembly/preflight/bridge/working")
	}
	work, ok = checkedAdd(work, bridgeWork)
	if !ok || work >= limits.MaxWorkUnits {
		return resourceError("assembly/preflight/bridge/work")
	}
	overhead, ok := sum64(assemblyFixedRetainedBytes, receiptStringBytes(provisional), assemblyDigestStringBytes)
	if !ok {
		return resourceError("assembly/preflight/bridge/retained")
	}
	retained, ok := sum64(frozen.RetainedBytes, source.RetainedBytes, overhead)
	if !ok || retained >= limits.MaxRetainedBytes {
		return resourceError("assembly/preflight/bridge/retained")
	}
	return nil
}

func prepareLayoutAdmission(
	ctx context.Context,
	limits Limits,
	frozen evaluator.FrozenTextSourceReceipt,
	provider sfntglyf.ShapeProviderReceipt,
	source sfntglyf.LayoutSourceReceipt,
	shape sfntglyf.ShapeReceipt,
	measure assemblyMeasure,
	fontSize documentv2.Rational,
	viewport documentv2.RasterViewport,
	fill color.RGBA16Input,
) (textlayout.Limits, uint64, error) {
	placeholder := "sha256:" + strings.Repeat("0", 64)
	provisionalLayout := textlayout.Receipt{
		Profile:                textlayout.ReceiptProfileV1,
		GlyphStreamHashProfile: textlayout.GlyphStreamHashProfileV1, GlyphStreamHash: placeholder,
		OutlineSetHashProfile: textlayout.OutlineSetHashProfileV1, OutlineSetHash: placeholder,
		PlacementHashProfile: textlayout.PlacementHashProfileV1, PlacementHash: placeholder,
		LayoutHashProfile: textlayout.LayoutHashProfileV1, LayoutHash: placeholder,
	}
	provisional := makeReceipt(frozen, provider, source, shape, provisionalLayout, measure, fontSize, viewport, fill)
	_, bindingBytes, err := hashAssembly(ctx, provisional)
	if err != nil {
		return textlayout.Limits{}, 0, err
	}
	baseCanonical, ok := sum64(source.CanonicalBinaryBytes, shape.CanonicalBinaryBytes, bindingBytes)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/canonical")
	}
	canonical, ok := remainingBudget(limits.MaxCanonicalBinaryBytes, baseCanonical)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/canonical")
	}
	baseWork, ok := sum64(source.WorkUnits, shape.WorkUnits, measure.bridgeWork, bindingBytes)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/work")
	}
	work, ok := remainingBudget(limits.MaxWorkUnits, baseWork)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/work")
	}
	baseWorking, ok := sum64(
		frozen.RetainedBytes, source.WorkingBytes, shape.WorkingBytes, measure.bridgeWorking,
	)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/working")
	}
	working, ok := remainingBudget(limits.MaxWorkingBytes, baseWorking)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/working")
	}
	overhead, ok := sum64(assemblyFixedRetainedBytes, receiptStringBytes(provisional), assemblyDigestStringBytes)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/retained")
	}
	baseRetained, ok := sum64(frozen.RetainedBytes, source.RetainedBytes, overhead)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/retained")
	}
	retained, ok := remainingBudget(limits.MaxRetainedBytes, baseRetained)
	if !ok {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/retained")
	}
	result := limits.Layout
	result.MaxLines = min64(result.MaxLines, limits.MaxLines)
	result.MaxGlyphs = min64(result.MaxGlyphs, limits.MaxGlyphs)
	result.MaxUniqueGlyphs = min64(result.MaxUniqueGlyphs, limits.MaxUniqueGlyphs)
	result.MaxCommands = min64(result.MaxCommands, limits.MaxCommands)
	result.MaxCanonicalBinaryBytes = min64(result.MaxCanonicalBinaryBytes, canonical)
	result.MaxWorkUnits = min64(result.MaxWorkUnits, work)
	result.MaxWorkingBytes = min64(result.MaxWorkingBytes, working)
	result.MaxRetainedBytes = min64(result.MaxRetainedBytes, retained)
	pixels, pixelsOK := checkedMul(uint64(viewport.WidthPixels), uint64(viewport.HeightPixels))
	if !pixelsOK || viewport.WidthPixels > result.MaxWidth || viewport.HeightPixels > result.MaxHeight ||
		pixels > result.MaxPixels || frozen.TextBytes > result.MaxTextBytes ||
		measure.lines > result.MaxLines || measure.maxGlyphsPerLine > result.MaxGlyphsPerLine ||
		measure.glyphs > result.MaxGlyphs || measure.uniqueGlyphs > result.MaxUniqueGlyphs ||
		measure.maxCommandsPerGlyph > result.MaxCommandsPerGlyph || measure.commands > result.MaxCommands ||
		measure.contours > result.MaxContours {
		return textlayout.Limits{}, 0, resourceError("assembly/preflight/layout/counts")
	}
	return result, bindingBytes, nil
}

func prepareRasterAdmission(
	ctx context.Context,
	limits RasterLimits,
	assembly Receipt,
) (textraster.Limits, uint64, error) {
	placeholder := "sha256:" + strings.Repeat("0", 64)
	provisional := RasterReceipt{
		SchemaVersion: SchemaVersion, Profile: RasterReceiptProfileV1,
		DescriptorHashProfile: DescriptorHashProfileV1, DescriptorHash: DescriptorHash(),
		BindingHashProfile: RasterBindingHashProfileV1, AssemblyHashProfile: AssemblyHashProfileV1,
		AssemblyHash: assembly.AssemblyHash, InnerReceiptProfile: textraster.ReceiptProfileV1,
		InnerRasterProfile: textraster.ProfileV1, InnerBindingHashProfile: textraster.BindingHashProfileV1,
		InnerBindingHash: placeholder, InnerShapingStatus: textraster.ShapingProvenanceStatusV1,
		ShapingProvenanceStatus: ShapingProvenanceStatusV1, PaintProvenanceStatus: PaintProvenanceStatusV1,
		CoverageHashProfile: textraster.CoverageHashProfileV1, CoverageHash: placeholder,
		PixelHashProfile: textraster.PixelHashProfileV1, PixelHash: placeholder,
		WidthPixels: assembly.WidthPixels, HeightPixels: assembly.HeightPixels,
	}
	_, bindingBytes, err := hashRaster(ctx, provisional)
	if err != nil {
		return textraster.Limits{}, 0, err
	}
	innerCanonical, innerCanonicalUpper, ok := innerRasterCanonicalCharges(assembly)
	if !ok {
		return textraster.Limits{}, 0, resourceError("raster/preflight/canonical")
	}
	outerCanonical, ok := checkedAdd(innerCanonical, bindingBytes)
	if !ok || outerCanonical > limits.MaxCanonicalBinaryBytes {
		return textraster.Limits{}, 0, resourceError("raster/preflight/canonical")
	}
	work, ok := remainingBudget(limits.MaxWorkUnits, bindingBytes)
	if !ok {
		return textraster.Limits{}, 0, resourceError("raster/preflight/work")
	}
	overhead, ok := sum64(assemblyFixedRetainedBytes, rasterReceiptStringBytes(provisional), assemblyDigestStringBytes)
	if !ok {
		return textraster.Limits{}, 0, resourceError("raster/preflight/retained")
	}
	retained, ok := remainingBudget(limits.MaxRetainedBytes, overhead)
	if !ok {
		return textraster.Limits{}, 0, resourceError("raster/preflight/retained")
	}
	workingRetained, ok := remainingBudget(limits.MaxWorkingBytes, overhead)
	if !ok {
		return textraster.Limits{}, 0, resourceError("raster/preflight/working")
	}
	result := limits.Raster
	if result.MaxCanonicalBinaryBytes < innerCanonicalUpper {
		return textraster.Limits{}, 0, resourceError("raster/preflight/canonical")
	}
	result.MaxCanonicalBinaryBytes = innerCanonicalUpper
	result.MaxWorkUnits = min64(result.MaxWorkUnits, work)
	result.MaxWorkingBytes = min64(result.MaxWorkingBytes, limits.MaxWorkingBytes)
	result.MaxRetainedBytes = min64(result.MaxRetainedBytes, min64(retained, workingRetained))
	if result.MaxRetainedBytes < 8 {
		return textraster.Limits{}, 0, resourceError("raster/preflight/retained")
	}
	return result, bindingBytes, nil
}

func innerRasterCanonicalCharges(assembly Receipt) (uint64, uint64, bool) {
	pixels, ok := checkedMul(uint64(assembly.WidthPixels), uint64(assembly.HeightPixels))
	if !ok {
		return 0, 0, false
	}
	placeholder := "sha256:" + strings.Repeat("0", 64)
	stringsInBinding := []string{
		textraster.BindingHashProfileV1, textraster.ReceiptProfileV1, textraster.ProfileV1,
		textraster.DescriptorHashProfileV1, textraster.DescriptorHash(), sfntglyf.LayoutSourceProfileV1,
		assembly.LayoutSourceHashProfile, assembly.LayoutSourceHash, assembly.ShapeProviderProfile,
		assembly.ShapeProviderHashProfile, assembly.ShapeProviderHash, assembly.DependencyFingerprintProfile,
		assembly.DependencyFingerprint, assembly.FontContentHashProfile, assembly.FontContentHash,
		textraster.ShapingProvenanceStatusV1, assembly.LayoutReceiptProfile, assembly.LayoutHashProfile,
		assembly.LayoutHash, assembly.GlyphStreamHashProfile, assembly.GlyphStreamHash,
		assembly.ScaledOutlineHashProfile, assembly.ScaledOutlineHash, assembly.PlacementHashProfile,
		assembly.PlacementHash, textraster.BindingHashProfileV1, textraster.CoverageHashProfileV1,
		placeholder, color.ProfileRGBA16SRGBPaintV1, cpucompositor.KernelProfileV1,
		cpucompositor.SurfaceProfileV1, textraster.PixelHashProfileV1, placeholder,
	}
	binding := uint64(4 + 4 + 16 + 8 + 8 + 11*8)
	for _, field := range stringsInBinding {
		increment, addOK := checkedAdd(8, uint64(len(field)))
		if !addOK {
			return 0, 0, false
		}
		binding, addOK = checkedAdd(binding, increment)
		if !addOK {
			return 0, 0, false
		}
	}
	coverage, ok := checkedAdd(8+uint64(len(textraster.CoverageHashProfileV1))+4+4+8, pixels)
	if !ok {
		return 0, 0, false
	}
	pixelBytes, ok := checkedMul(pixels, 8)
	if !ok {
		return 0, 0, false
	}
	actual, ok := sum64(coverage, pixelBytes, binding)
	upper, upperOK := checkedMul(pixels, 9)
	if upperOK {
		upper, upperOK = checkedAdd(upper, 16<<10)
	}
	return actual, upper, ok && upperOK
}

func bridgeCountCharge(lines, glyphs, uniqueGlyphs, commands, points uint64) (uint64, uint64, bool) {
	working := assemblyFixedWorkingBytes
	for _, part := range [][2]uint64{
		{lines, assemblyLineBytes}, {glyphs, assemblyGlyphBytes}, {glyphs, assemblyClosureBytes},
		{uniqueGlyphs, assemblyOutlineBytes}, {commands, assemblyCommandBytes}, {points, assemblyPointBytes},
	} {
		charge, ok := checkedMul(part[0], part[1])
		if !ok {
			return 0, 0, false
		}
		working, ok = checkedAdd(working, charge)
		if !ok {
			return 0, 0, false
		}
	}
	glyphWork, ok := checkedMul(glyphs, 8)
	if !ok {
		return 0, 0, false
	}
	uniqueWork, ok := checkedMul(uniqueGlyphs, 8)
	if !ok {
		return 0, 0, false
	}
	sortWork, ok := checkedMul(uniqueGlyphs, uint64(bits.Len64(uniqueGlyphs)))
	if !ok {
		return 0, 0, false
	}
	commandWork, ok := checkedMul(commands, 4)
	if !ok {
		return 0, 0, false
	}
	pointWork, ok := checkedMul(points, 2)
	if !ok {
		return 0, 0, false
	}
	work, ok := sum64(lines, glyphWork, uniqueWork, sortWork, commandWork, pointWork)
	return working, work, ok
}

func mapFontChildError(path string, err error) error {
	switch {
	case errors.Is(err, sfntglyf.ErrCanceled), errors.Is(err, sfntglyf.ErrContextInvalid):
		return assemblyError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	case errors.Is(err, sfntglyf.ErrLimitsInvalid):
		return assemblyError(CodeLimitsInvalid, path, errors.Join(ErrLimitsInvalid, err))
	case errors.Is(err, sfntglyf.ErrResourceLimit):
		return assemblyError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	default:
		return assemblyError(CodeBinding, path, errors.Join(ErrBinding, err))
	}
}

func mapLayoutChildError(path string, err error) error {
	switch {
	case errors.Is(err, textlayout.ErrCanceled), errors.Is(err, textlayout.ErrContextInvalid):
		return assemblyError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	case errors.Is(err, textlayout.ErrLimitsInvalid):
		return assemblyError(CodeLimitsInvalid, path, errors.Join(ErrLimitsInvalid, err))
	case errors.Is(err, textlayout.ErrResourceLimit), errors.Is(err, textlayout.ErrExactLimit):
		return assemblyError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	default:
		return assemblyError(CodeBinding, path, errors.Join(ErrBinding, err))
	}
}

func mapRasterChildError(path string, err error) error {
	switch {
	case errors.Is(err, textraster.ErrCanceled), errors.Is(err, textraster.ErrContextInvalid):
		return assemblyError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	case errors.Is(err, textraster.ErrLimitsInvalid):
		return assemblyError(CodeLimitsInvalid, path, errors.Join(ErrLimitsInvalid, err))
	case errors.Is(err, textraster.ErrResourceLimit), errors.Is(err, textraster.ErrExactLimit):
		return assemblyError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	default:
		return assemblyError(CodeBinding, path, errors.Join(ErrBinding, err))
	}
}

func remainingBudget(limit, used uint64) (uint64, bool) {
	if used >= limit {
		return 0, false
	}
	return limit - used, true
}

func min64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func resourceError(path string) error {
	return assemblyError(CodeResourceLimit, path, ErrResourceLimit)
}
