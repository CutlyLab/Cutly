package referencevisual

import (
	"math/bits"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

// validTextAssetReceiptDeep independently closes the concrete textlayer manifest receipt against
// a freshly frozen evaluator asset and every public assembly/raster profile and resource lower
// bound. The concrete provider remains the live trust root; this function prevents its serialized
// manifest from becoming a self-consistent but cross-document claim.
func validTextAssetReceiptDeep(
	receipt textlayer.AssetReceipt,
	asset evaluator.SnapshotAsset,
	raw documentv2.Asset,
	limits textlayer.Limits,
) bool {
	if raw.Text == nil || raw.Text.Viewport == nil {
		return false
	}
	text := raw.Text
	assembly := receipt.Assembly
	raster := receipt.Raster
	identity := receipt.SurfaceIdentity
	providerDescriptor := sfntglyf.ShapeProviderDescriptor()
	minimumAssembly, minimumAssemblyOK := minimumTextAssemblyResources(assembly)
	minimumRaster, minimumRasterOK := minimumTextRasterResources(asset.TextViewport, assembly.GlyphCount)
	if asset.ID != receipt.AssetID || asset.Index != receipt.AssetIndex || asset.Kind != documentv2.AssetKindText ||
		asset.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 || asset.TextBindingHash != receipt.FrozenTextBindingHash ||
		asset.TextFill != text.Fill || asset.TextViewport != receipt.RasterViewport || asset.TextFontAssetID != receipt.FontAssetID ||
		asset.TextFontAssetIndex != receipt.FontAssetIndex || asset.TextFontContentHash != receipt.FontContentHash ||
		asset.TextFontProfile != documentv2.TextFontProfileStaticGlyfV1 || asset.TextRunInputProfile != documentv2.TextRunInputProfileV1 ||
		asset.TextLayoutProfile != documentv2.TextLayoutProfileUnwrappedV1 || asset.TextOutlineProfile != documentv2.TextOutlineProfileQ26_6V1 ||
		asset.TextRasterProfile != documentv2.TextRasterProfileSupersample8x8V1 || asset.TextPaintProfile != documentv2.TextPaintProfileRGBA16SRGBV1 ||
		asset.TextCoverageHashProfile != documentv2.TextCoverageHashProfileV1 || asset.TextUnicodeVersion != documentv2.TextUnicodeDataVersionV1 ||
		asset.TextUnicodeValidationProfile != documentv2.TextUnicodeValidationTableProfileV1 ||
		asset.TextUnicodeValidationHashProfile != documentv2.TextUnicodeValidationTableHashProfileV1 ||
		asset.TextUnicodeValidationTableHash != documentv2.TextUnicodeValidationTableHashV1 ||
		asset.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 || asset.TextSemanticHash != receipt.TextSemanticHash ||
		asset.TextLayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 || asset.TextLayoutInputHash != receipt.LayoutInputHash {
		return false
	}
	assemblyLimits := limits.Assembly
	if !minimumAssemblyOK || assembly.SchemaVersion != textassembly.SchemaVersion || assembly.Profile != textassembly.ReceiptProfileV1 ||
		assembly.DescriptorHashProfile != textassembly.DescriptorHashProfileV1 || assembly.DescriptorHash != textassembly.DescriptorHash() ||
		assembly.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || !canonicalSHA256(assembly.AssemblyHash) ||
		assembly.FrozenTextProfile != evaluator.FrozenTextSourceProfileV1 ||
		assembly.FrozenTextBindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 ||
		assembly.FrozenTextBindingHash != asset.TextBindingHash ||
		assembly.DocumentHashProfile != evaluator.FrozenTextSourceDocumentHashProfileV1 || !canonicalSHA256(assembly.DocumentHash) ||
		assembly.TextAssetID != string(asset.ID) || assembly.TextAssetIndex != uint64(asset.Index) ||
		assembly.FontAssetID != string(asset.TextFontAssetID) || assembly.FontAssetIndex != uint64(asset.TextFontAssetIndex) ||
		assembly.FontContentHashProfile != sfntglyf.ContentHashProfileV1 || assembly.FontContentHash != asset.TextFontContentHash ||
		assembly.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 || assembly.TextSemanticHash != asset.TextSemanticHash ||
		assembly.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 || assembly.LayoutInputHash != asset.TextLayoutInputHash ||
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
		assembly.WidthPixels != asset.TextViewport.WidthPixels || assembly.HeightPixels != asset.TextViewport.HeightPixels ||
		assembly.FontSizeNumerator != text.FontSize.Numerator || assembly.FontSizeDenominator != text.FontSize.Denominator ||
		assembly.FillR != asset.TextFill.R || assembly.FillG != asset.TextFill.G ||
		assembly.FillB != asset.TextFill.B || assembly.FillA != asset.TextFill.A ||
		assembly.LineCount == 0 || assembly.LineCount > assemblyLimits.MaxLines || assembly.LineCount > assemblyLimits.Shape.MaxLines ||
		assembly.LineCount > assemblyLimits.Layout.MaxLines || assembly.GlyphCount > assemblyLimits.MaxGlyphs ||
		assembly.GlyphCount > assemblyLimits.Shape.MaxGlyphs || assembly.GlyphCount > assemblyLimits.Layout.MaxGlyphs ||
		assembly.UniqueGlyphCount > assemblyLimits.MaxUniqueGlyphs || assembly.UniqueGlyphCount > assemblyLimits.Layout.MaxUniqueGlyphs ||
		assembly.UniqueGlyphCount > assembly.GlyphCount || assembly.ContourCount > assemblyLimits.LayoutSource.MaxContours ||
		assembly.ContourCount > assemblyLimits.Layout.MaxContours || assembly.CommandCount > assemblyLimits.MaxCommands ||
		assembly.CommandCount > assemblyLimits.LayoutSource.MaxCommands || assembly.CommandCount > assemblyLimits.Layout.MaxCommands ||
		assembly.CommandPointOccurrences > assemblyLimits.MaxCommandPointOccurrences ||
		assembly.CommandPointOccurrences > assemblyLimits.LayoutSource.MaxCommandPointOccurrences ||
		!validTextReceiptResourcesWithin(assembly.CanonicalBinaryBytes, assembly.WorkUnits, assembly.WorkingBytes, assembly.RetainedBytes,
			assemblyLimits.MaxCanonicalBinaryBytes, assemblyLimits.MaxWorkUnits, assemblyLimits.MaxWorkingBytes, assemblyLimits.MaxRetainedBytes) ||
		assembly.CanonicalBinaryBytes < minimumAssembly.canonical || assembly.WorkUnits < minimumAssembly.work ||
		assembly.WorkingBytes < minimumAssembly.working || assembly.RetainedBytes < minimumAssembly.retained {
		return false
	}
	rasterLimits := limits.Raster
	innerRasterLimits := rasterLimits.Raster
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
		raster.WidthPixels != asset.TextViewport.WidthPixels || raster.HeightPixels != asset.TextViewport.HeightPixels ||
		asset.TextViewport.WidthPixels > innerRasterLimits.MaxWidth || asset.TextViewport.HeightPixels > innerRasterLimits.MaxHeight ||
		assembly.GlyphCount > innerRasterLimits.MaxPlacements || assembly.UniqueGlyphCount > innerRasterLimits.MaxUniqueGlyphs ||
		assembly.CommandCount > innerRasterLimits.MaxCommands ||
		!validTextReceiptResourcesWithin(raster.CanonicalBinaryBytes, raster.WorkUnits, raster.WorkingBytes, raster.RetainedBytes,
			rasterLimits.MaxCanonicalBinaryBytes, rasterLimits.MaxWorkUnits, rasterLimits.MaxWorkingBytes, rasterLimits.MaxRetainedBytes) ||
		raster.CanonicalBinaryBytes < minimumRaster.canonical || raster.WorkUnits < minimumRaster.work ||
		raster.WorkingBytes < minimumRaster.working || raster.RetainedBytes < minimumRaster.retained ||
		identity.WidthPixels != asset.TextViewport.WidthPixels || identity.HeightPixels != asset.TextViewport.HeightPixels ||
		identity.PixelHashProfile != textlayer.SurfaceHashProfileV1 || identity.PixelHash != raster.PixelHash ||
		identity.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return false
	}
	pixels, ok := checkedMul(uint64(asset.TextViewport.WidthPixels), uint64(asset.TextViewport.HeightPixels))
	if !ok {
		return false
	}
	bytes, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
	return ok && pixels <= innerRasterLimits.MaxPixels && bytes == receipt.SurfaceBytes &&
		bytes <= limits.MaxTotalStaticSurfaceBytes && assembly.DocumentHash != ""
}

type textRasterResourceMinimum struct {
	canonical uint64
	work      uint64
	working   uint64
	retained  uint64
}

func minimumTextAssemblyResources(value textassembly.Receipt) (textRasterResourceMinimum, bool) {
	preimage, ok := textAssemblyHashPreimageBytes(value)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	work := preimage
	for _, charge := range [][2]uint64{
		{value.LineCount, 1}, {value.GlyphCount, 8}, {value.UniqueGlyphCount, 8},
		{value.UniqueGlyphCount, uint64(bits.Len64(value.UniqueGlyphCount))},
		{value.CommandCount, 4}, {value.CommandPointOccurrences, 2},
	} {
		increment, multiplyOK := checkedMul(charge[0], charge[1])
		if !multiplyOK {
			return textRasterResourceMinimum{}, false
		}
		work, ok = checkedAdd(work, increment)
		if !ok {
			return textRasterResourceMinimum{}, false
		}
	}
	working := uint64(64 << 10)
	for _, charge := range [][2]uint64{
		{value.LineCount, 24}, {value.GlyphCount, 84}, {value.UniqueGlyphCount, 32},
		{value.CommandCount, 48}, {value.CommandPointOccurrences, 16},
	} {
		increment, multiplyOK := checkedMul(charge[0], charge[1])
		if !multiplyOK {
			return textRasterResourceMinimum{}, false
		}
		working, ok = checkedAdd(working, increment)
		if !ok {
			return textRasterResourceMinimum{}, false
		}
	}
	stringsOwned, ok := textAssemblyReceiptStringBytes(value)
	if !ok {
		return textRasterResourceMinimum{}, false
	}
	retained, ok := checkedSum(4<<10, stringsOwned, uint64(len("sha256:")+64))
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
	domainBytes, ok := checkedAdd(8, uint64(len(textassembly.AssemblyHashProfileV1)))
	if !ok {
		return 0, false
	}
	result, ok := checkedSum(domainBytes, 8, stringsBytes, 16+8+16+8+80)
	return result, ok
}

func textAssemblyEncodedStringBytes(value textassembly.Receipt) (uint64, bool) {
	var total uint64
	for _, field := range textAssemblyReceiptStrings(value) {
		increment, ok := checkedAdd(8, uint64(len(field)))
		if !ok {
			return 0, false
		}
		total, ok = checkedAdd(total, increment)
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
		total, ok = checkedAdd(total, uint64(len(field)))
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
	if ok {
		perGlyphSamples, ok = checkedMul(perGlyphSamples, glyphCount)
	}
	baseWork, baseOK := checkedMul(pixels, 89)
	work, workOK := checkedSum(perGlyphSamples, baseWork, 16<<10)
	widthCharge, widthOK := checkedMul(uint64(viewport.WidthPixels), 72)
	working, workingOK := checkedSum(64<<10, canonical, widthCharge, 8)
	retainedSurface, retainedOK := checkedMul(pixels, cpucompositor.BytesPerPixel)
	retained, retainedSumOK := checkedAdd(retainedSurface, 8<<10)
	return textRasterResourceMinimum{canonical: canonical, work: work, working: working, retained: retained},
		ok && baseOK && workOK && widthOK && workingOK && retainedOK && retainedSumOK
}

func validTextReceiptResourcesWithin(canonical, work, working, retained, maxCanonical, maxWork, maxWorking, maxRetained uint64) bool {
	return canonical > 0 && canonical <= maxCanonical && maxCanonical <= 4<<30 &&
		work > 0 && work <= maxWork && maxWork <= 1<<62 &&
		working > 0 && working <= maxWorking && maxWorking <= 16<<30 &&
		retained > 0 && retained <= maxRetained && maxRetained <= 8<<30 && working >= retained
}
