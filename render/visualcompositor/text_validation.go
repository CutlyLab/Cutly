package visualcompositor

import (
	"math"
	"math/bits"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

const (
	textReceiptMaxCanonical uint64 = 4 << 30
	textReceiptMaxWork      uint64 = 1 << 62
	textReceiptMaxWorking   uint64 = 16 << 30
	textReceiptMaxRetained  uint64 = 8 << 30
)

// validateTextReceiptBindings treats a serialized textlayer receipt as structural evidence only,
// but still closes every published v1 profile/value pair against the frozen compositor scope.
// Live pixel causality is checked separately by validateTextFrame.
func (program *Program) validateTextReceiptBindings(
	asset frozenAsset,
	request textlayer.Request,
	receipt textlayer.FrameReceipt,
	path string,
) error {
	assembly := receipt.Assembly
	raster := receipt.Raster
	identity := receipt.SurfaceIdentity
	providerDescriptor := sfntglyf.ShapeProviderDescriptor()
	minimumAssembly, minimumAssemblyOK := minimumTextAssemblyResources(assembly)
	minimumRaster, minimumRasterOK := minimumTextRasterResources(asset.viewport, assembly.GlyphCount)
	if !program.config.Source.StaticTextV2 || receipt.ContractVersion != textlayer.ContractVersion || receipt.Request != request ||
		request.ContractVersion != textlayer.ContractVersion || request.DocumentHash != program.documentHash ||
		request.TrackKind != documentv2.TrackKindText || request.AssetKind != documentv2.AssetKindText ||
		request.AssetIndex != asset.index || request.FrozenTextBindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 ||
		request.FrozenTextBindingHash != asset.textBindingHash || request.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		request.TextSemanticHash != asset.textSemanticHash || request.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
		request.LayoutInputHash != asset.textLayoutInputHash || request.FontAssetID != asset.textFontAssetID ||
		request.FontAssetIndex != asset.textFontAssetIndex || request.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
		request.FontContentHash != asset.textFontContentHash || request.RasterViewport != asset.viewport ||
		request.SourceProfile != textlayer.SourceProfileV1 || request.SelectionProfile != textlayer.SelectionProfileV1 ||
		request.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		request.EvaluatorProfile != documentv2.EvaluatorTextPinnedFontViewportPXV2 ||
		request.RasterProfile != documentv2.TextRasterProfileSupersample8x8V1 ||
		request.PaintProfile != color.ProfileRGBA16SRGBPaintV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return compositorError(CodeSourceMismatch, path+"/request", ErrSourceMismatch)
	}
	if !minimumAssemblyOK || assembly.SchemaVersion != textassembly.SchemaVersion || assembly.Profile != textassembly.ReceiptProfileV1 ||
		assembly.DescriptorHashProfile != textassembly.DescriptorHashProfileV1 || assembly.DescriptorHash != textassembly.DescriptorHash() ||
		assembly.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || !canonicalSHA256(assembly.AssemblyHash) ||
		assembly.FrozenTextProfile != evaluator.FrozenTextSourceProfileV1 ||
		assembly.FrozenTextBindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 ||
		assembly.FrozenTextBindingHash != asset.textBindingHash ||
		assembly.DocumentHashProfile != evaluator.FrozenTextSourceDocumentHashProfileV1 || assembly.DocumentHash != program.documentHash ||
		assembly.TextAssetID != string(request.AssetID) || assembly.TextAssetIndex != uint64(asset.index) ||
		assembly.FontAssetID != string(asset.textFontAssetID) || assembly.FontAssetIndex != uint64(asset.textFontAssetIndex) ||
		assembly.FontContentHashProfile != sfntglyf.ContentHashProfileV1 || assembly.FontContentHash != asset.textFontContentHash ||
		assembly.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 || assembly.TextSemanticHash != asset.textSemanticHash ||
		assembly.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 || assembly.LayoutInputHash != asset.textLayoutInputHash ||
		assembly.ShapeProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
		assembly.ShapeProviderHashProfile != sfntglyf.ShapeProviderHashProfileV1 || !canonicalSHA256(assembly.ShapeProviderHash) ||
		assembly.DependencyFingerprintProfile != providerDescriptor.DependencyFingerprintProfile ||
		assembly.DependencyFingerprint != providerDescriptor.DependencyFingerprint ||
		assembly.LayoutGraphHashProfile != sfntglyf.LayoutGraphCanonicalHashProfileV1 || !canonicalSHA256(assembly.LayoutGraphHash) ||
		assembly.SourceOutlineSetHashProfile != sfntglyf.OutlineSetHashProfileV1 || !canonicalSHA256(assembly.SourceOutlineSetHash) ||
		assembly.LayoutSourceHashProfile != sfntglyf.LayoutSourceHashProfileV1 || !canonicalSHA256(assembly.LayoutSourceHash) ||
		assembly.ShapeInputHashProfile != sfntglyf.ShapeInputHashProfileV1 || !canonicalSHA256(assembly.ShapeInputHash) ||
		assembly.ShapeOutputProfile != sfntglyf.ShapeOutputProfileV1 ||
		assembly.ShapeOutputHashProfile != sfntglyf.ShapeOutputHashProfileV1 || !canonicalSHA256(assembly.ShapeOutputHash) ||
		assembly.LayoutReceiptProfile != textlayout.ReceiptProfileV1 ||
		assembly.GlyphStreamHashProfile != textlayout.GlyphStreamHashProfileV1 || !canonicalSHA256(assembly.GlyphStreamHash) ||
		assembly.ScaledOutlineHashProfile != textlayout.OutlineSetHashProfileV1 || !canonicalSHA256(assembly.ScaledOutlineHash) ||
		assembly.PlacementHashProfile != textlayout.PlacementHashProfileV1 || !canonicalSHA256(assembly.PlacementHash) ||
		assembly.LayoutHashProfile != textlayout.LayoutHashProfileV1 || !canonicalSHA256(assembly.LayoutHash) ||
		assembly.ShapingProvenanceStatus != textassembly.ShapingProvenanceStatusV1 ||
		assembly.PaintProvenanceStatus != textassembly.PaintProvenanceStatusV1 || assembly.CapabilityStatus != textassembly.CapabilityStatusV1 ||
		assembly.WidthPixels != asset.viewport.WidthPixels || assembly.HeightPixels != asset.viewport.HeightPixels ||
		assembly.FontSizeNumerator <= 0 || assembly.FontSizeDenominator <= 0 ||
		assembly.FillR != asset.textFill.R || assembly.FillG != asset.textFill.G ||
		assembly.FillB != asset.textFill.B || assembly.FillA != asset.textFill.A ||
		assembly.LineCount == 0 || assembly.LineCount > textlayout.HardMaxLines ||
		assembly.GlyphCount > textlayout.HardMaxGlyphs ||
		assembly.UniqueGlyphCount > textlayout.HardMaxUniqueGlyphs ||
		assembly.UniqueGlyphCount > assembly.GlyphCount || assembly.ContourCount > textlayout.HardMaxContours ||
		assembly.CommandCount > textlayout.HardMaxCommands || assembly.CommandPointOccurrences > math.MaxUint32 ||
		!validTextReceiptResources(assembly.CanonicalBinaryBytes, assembly.WorkUnits, assembly.WorkingBytes, assembly.RetainedBytes) ||
		assembly.CanonicalBinaryBytes < minimumAssembly.canonical || assembly.WorkUnits < minimumAssembly.work ||
		assembly.WorkingBytes < minimumAssembly.working || assembly.RetainedBytes < minimumAssembly.retained {
		return compositorError(CodeSourceMismatch, path+"/assembly", ErrSourceMismatch)
	}
	if !minimumRasterOK || raster.SchemaVersion != textassembly.SchemaVersion || raster.Profile != textassembly.RasterReceiptProfileV1 ||
		raster.DescriptorHashProfile != textassembly.DescriptorHashProfileV1 || raster.DescriptorHash != textassembly.DescriptorHash() ||
		raster.BindingHashProfile != textassembly.RasterBindingHashProfileV1 || !canonicalSHA256(raster.BindingHash) ||
		raster.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || raster.AssemblyHash != assembly.AssemblyHash ||
		raster.InnerReceiptProfile != textraster.ReceiptProfileV1 || raster.InnerRasterProfile != textraster.ProfileV1 ||
		raster.InnerBindingHashProfile != textraster.BindingHashProfileV1 || !canonicalSHA256(raster.InnerBindingHash) ||
		raster.InnerShapingStatus != textraster.ShapingProvenanceStatusV1 ||
		raster.ShapingProvenanceStatus != textassembly.ShapingProvenanceStatusV1 ||
		raster.PaintProvenanceStatus != textassembly.PaintProvenanceStatusV1 ||
		raster.CoverageHashProfile != textraster.CoverageHashProfileV1 || !canonicalSHA256(raster.CoverageHash) ||
		raster.PixelHashProfile != textraster.PixelHashProfileV1 || !canonicalSHA256(raster.PixelHash) ||
		raster.WidthPixels != asset.viewport.WidthPixels || raster.HeightPixels != asset.viewport.HeightPixels ||
		!validTextReceiptResources(raster.CanonicalBinaryBytes, raster.WorkUnits, raster.WorkingBytes, raster.RetainedBytes) ||
		raster.CanonicalBinaryBytes < minimumRaster.canonical || raster.WorkUnits < minimumRaster.work ||
		raster.WorkingBytes < minimumRaster.working || raster.RetainedBytes < minimumRaster.retained ||
		identity.WidthPixels != asset.viewport.WidthPixels || identity.HeightPixels != asset.viewport.HeightPixels ||
		identity.PixelHashProfile != textlayer.SurfaceHashProfileV1 || identity.PixelHash != raster.PixelHash ||
		identity.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return compositorError(CodeSourceMismatch, path+"/raster", ErrSourceMismatch)
	}
	return nil
}

type textRasterResourceMinimum struct {
	canonical uint64
	work      uint64
	working   uint64
	retained  uint64
}

// minimumTextAssemblyResources mirrors only the unavoidable public lower bounds of
// textassembly v1. Child source/layout charges are deliberately omitted, so every authentic
// receipt must be at least these values while future additive child accounting remains valid.
func minimumTextAssemblyResources(value textassembly.Receipt) (textRasterResourceMinimum, bool) {
	preimage, ok := textAssemblyHashPreimageBytes(value)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	work := preimage
	for _, charge := range [][2]uint64{
		{value.LineCount, 1},
		{value.GlyphCount, 8},
		{value.UniqueGlyphCount, 8},
		{value.UniqueGlyphCount, uint64(bits.Len64(value.UniqueGlyphCount))},
		{value.CommandCount, 4},
		{value.CommandPointOccurrences, 2},
	} {
		increment, multiplyOK := checkedMul(charge[0], charge[1])
		if !multiplyOK {
			return textRasterResourceMinimum{}, false
		}
		work, ok = checkedAddTextResource(work, increment)
		if !ok {
			return textRasterResourceMinimum{}, false
		}
	}
	working := uint64(64 << 10)
	for _, charge := range [][2]uint64{
		{value.LineCount, 24},
		{value.GlyphCount, 84},
		{value.UniqueGlyphCount, 32},
		{value.CommandCount, 48},
		{value.CommandPointOccurrences, 16},
	} {
		increment, multiplyOK := checkedMul(charge[0], charge[1])
		if !multiplyOK {
			return textRasterResourceMinimum{}, false
		}
		working, ok = checkedAddTextResource(working, increment)
		if !ok {
			return textRasterResourceMinimum{}, false
		}
	}
	stringsOwned, ok := textAssemblyReceiptStringBytes(value)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	retained, ok := checkedAddTextResource(4<<10, stringsOwned)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	retained, ok = checkedAddTextResource(retained, uint64(len("sha256:")+64))
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	return textRasterResourceMinimum{canonical: preimage, work: work, working: working, retained: retained}, true
}

func textAssemblyHashPreimageBytes(value textassembly.Receipt) (uint64, bool) {
	stringsBytes, ok := textAssemblyEncodedStringBytes(value)
	if !ok {
		return 0, false
	}
	domainBytes, ok := checkedAddTextResource(8, uint64(len(textassembly.AssemblyHashProfileV1)))
	if !ok {
		return 0, false
	}
	result, ok := checkedAddTextResource(domainBytes, 8) // profile schema + receipt schema
	if !ok {
		return 0, false
	}
	result, ok = checkedAddTextResource(result, stringsBytes)
	if !ok {
		return 0, false
	}
	return checkedAddTextResource(result, 16+8+16+8+80) // indices, viewport, font size, fill, counts/resources
}

func textAssemblyEncodedStringBytes(value textassembly.Receipt) (uint64, bool) {
	var total uint64
	for _, field := range textAssemblyReceiptStrings(value) {
		increment, ok := checkedAddTextResource(8, uint64(len(field)))
		if !ok {
			return 0, false
		}
		total, ok = checkedAddTextResource(total, increment)
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func textAssemblyReceiptStringBytes(value textassembly.Receipt) (uint64, bool) {
	var total uint64
	for _, field := range textAssemblyReceiptStrings(value) {
		var ok bool
		total, ok = checkedAddTextResource(total, uint64(len(field)))
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func textAssemblyReceiptStrings(value textassembly.Receipt) []string {
	return []string{
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
	}
}

// minimumTextRasterResources derives the public, unavoidable lower bounds of the authenticated
// textraster v1 stage. The outer textassembly receipt adds its own canonical/work/retained
// overhead, so it can never legitimately be below these inner values.
func minimumTextRasterResources(viewport documentv2.RasterViewport, glyphCount uint64) (textRasterResourceMinimum, bool) {
	pixels, ok := checkedMul(uint64(viewport.WidthPixels), uint64(viewport.HeightPixels))
	if !ok || pixels == 0 {
		return textRasterResourceMinimum{}, false
	}
	canonical, ok := checkedMul(pixels, 9)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	perGlyphSamples, ok := checkedMul(pixels, 64)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	perGlyphSamples, ok = checkedMul(perGlyphSamples, glyphCount)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	baseWork, ok := checkedMul(pixels, 89)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	work, ok := checkedAddTextResource(perGlyphSamples, baseWork)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	work, ok = checkedAddTextResource(work, 16<<10)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	widthCharge, ok := checkedMul(uint64(viewport.WidthPixels), 72)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	working, ok := checkedAddTextResource(64<<10, canonical)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	working, ok = checkedAddTextResource(working, widthCharge)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	working, ok = checkedAddTextResource(working, 8)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	retained, ok := checkedSurfaceBytes(viewport.WidthPixels, viewport.HeightPixels)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	retained, ok = checkedAddTextResource(retained, 8<<10)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	return textRasterResourceMinimum{canonical: canonical, work: work, working: working, retained: retained}, true
}

func checkedAddTextResource(left, right uint64) (uint64, bool) {
	if right > math.MaxUint64-left {
		return 0, false
	}
	return left + right, true
}

func validTextReceiptResources(canonical, work, working, retained uint64) bool {
	return canonical > 0 && canonical <= textReceiptMaxCanonical &&
		work > 0 && work <= textReceiptMaxWork && working > 0 && working <= textReceiptMaxWorking &&
		retained > 0 && retained <= textReceiptMaxRetained && working >= retained
}
