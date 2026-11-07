package textassembly

import (
	"context"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

// RasterizeStatic rasterizes the exact private Layout assembled from the live provider and uses
// only the frozen Text source's static fill. No caller-controlled layout, outline, placement, or
// paint can enter the authenticated path.
func RasterizeStatic(ctx context.Context, source *Source, limits RasterLimits) (
	surface *cpucompositor.Surface,
	receipt RasterReceipt,
	err error,
) {
	defer func() {
		if recover() != nil {
			surface = nil
			receipt = RasterReceipt{}
			err = assemblyError(CodeInvariantFailed, "raster/panic", ErrInvariantFailed)
		}
		if err != nil {
			surface = nil
			receipt = RasterReceipt{}
		}
	}()
	if err = contextFailure(ctx, "raster/context"); err != nil {
		return nil, RasterReceipt{}, err
	}
	normalized, err := normalizeRasterLimits(limits)
	if err != nil {
		return nil, RasterReceipt{}, err
	}
	if err := validateSourceForRaster(ctx, source); err != nil {
		return nil, RasterReceipt{}, err
	}
	innerLimits, admittedBindingBytes, err := prepareRasterAdmission(ctx, normalized, source.receipt)
	if err != nil {
		return nil, RasterReceipt{}, err
	}
	fontSize := source.frozen.FontSize()
	surface, inner, err := textraster.Rasterize(ctx, textraster.Request{
		Source: source.layoutSource, Layout: source.layout, FontSize: fontSize, Fill: source.fill,
	}, innerLimits)
	if err != nil {
		return nil, RasterReceipt{}, mapRasterChildError("raster/inner", err)
	}
	if err := validateInnerRaster(source.receipt, inner); err != nil {
		return nil, RasterReceipt{}, err
	}
	pixelHash, err := surface.CanonicalHashContext(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, RasterReceipt{}, assemblyError(CodeCanceled, "raster/surface/hash", errors.Join(ErrCanceled, err))
		}
		return nil, RasterReceipt{}, assemblyError(CodeInvariantFailed, "raster/surface/hash", errors.Join(ErrInvariantFailed, err))
	}
	if pixelHash != inner.PixelHash || surface.Width() != int(inner.WidthPixels) || surface.Height() != int(inner.HeightPixels) {
		return nil, RasterReceipt{}, assemblyError(CodeInvariantFailed, "raster/surface", ErrInvariantFailed)
	}
	receipt = RasterReceipt{
		SchemaVersion: SchemaVersion, Profile: RasterReceiptProfileV1,
		DescriptorHashProfile: DescriptorHashProfileV1, DescriptorHash: DescriptorHash(),
		BindingHashProfile: RasterBindingHashProfileV1, AssemblyHashProfile: AssemblyHashProfileV1,
		AssemblyHash: source.receipt.AssemblyHash, InnerReceiptProfile: inner.Profile,
		InnerRasterProfile: inner.RasterProfile, InnerBindingHashProfile: inner.BindingHashProfile,
		InnerBindingHash: inner.BindingHash, InnerShapingStatus: inner.ShapingProvenanceStatus,
		ShapingProvenanceStatus: ShapingProvenanceStatusV1, PaintProvenanceStatus: PaintProvenanceStatusV1,
		CoverageHashProfile: inner.CoverageHashProfile, CoverageHash: inner.CoverageHash,
		PixelHashProfile: inner.PixelHashProfile, PixelHash: inner.PixelHash,
		WidthPixels: inner.WidthPixels, HeightPixels: inner.HeightPixels,
	}
	_, bindingBytes, err := hashRaster(ctx, receipt)
	if err != nil {
		return nil, RasterReceipt{}, err
	}
	canonical, ok := checkedAdd(inner.CanonicalBinaryBytes, bindingBytes)
	work, workOK := checkedAdd(inner.WorkUnits, bindingBytes)
	overhead, retainedOK := sum64(assemblyFixedRetainedBytes, rasterReceiptStringBytes(receipt), assemblyDigestStringBytes)
	retained := uint64(0)
	if retainedOK {
		retained, retainedOK = checkedAdd(inner.RetainedBytes, overhead)
	}
	working := max64(inner.WorkingBytes, retained)
	if !ok || !workOK || !retainedOK || canonical > normalized.MaxCanonicalBinaryBytes ||
		work > normalized.MaxWorkUnits || working > normalized.MaxWorkingBytes || retained > normalized.MaxRetainedBytes {
		return nil, RasterReceipt{}, assemblyError(CodeResourceLimit, "raster/resources", ErrResourceLimit)
	}
	receipt.CanonicalBinaryBytes = canonical
	receipt.WorkUnits = work
	receipt.WorkingBytes = working
	receipt.RetainedBytes = retained
	bindingHash, finalBindingBytes, err := hashRaster(ctx, receipt)
	if err != nil {
		return nil, RasterReceipt{}, err
	}
	if bindingBytes != admittedBindingBytes || finalBindingBytes != bindingBytes {
		return nil, RasterReceipt{}, assemblyError(CodeInvariantFailed, "raster/hash/bytes", ErrInvariantFailed)
	}
	receipt.BindingHash = bindingHash
	cloneRasterReceiptStrings(&receipt)
	if err := contextFailure(ctx, "raster/publish"); err != nil {
		return nil, RasterReceipt{}, err
	}
	return surface, receipt, nil
}

func validateSourceForRaster(ctx context.Context, source *Source) error {
	if source == nil || source.frozen == nil || source.layoutSource == nil || source.layout == nil ||
		source.receipt.SchemaVersion != SchemaVersion || source.receipt.Profile != ReceiptProfileV1 ||
		source.receipt.DescriptorHash != DescriptorHash() || source.receipt.AssemblyHashProfile != AssemblyHashProfileV1 ||
		!canonicalDigest(source.receipt.AssemblyHash) || source.receipt.ShapingProvenanceStatus != ShapingProvenanceStatusV1 ||
		source.receipt.PaintProvenanceStatus != PaintProvenanceStatusV1 ||
		source.receipt.FrozenTextBindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 ||
		source.receipt.DocumentHashProfile != evaluator.FrozenTextSourceDocumentHashProfileV1 ||
		source.receipt.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		source.receipt.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 {
		return assemblyError(CodeInputInvalid, "raster/source", ErrInputInvalid)
	}
	expected, _, err := hashAssembly(ctx, source.receipt)
	if err != nil {
		return err
	}
	if expected != source.receipt.AssemblyHash {
		return assemblyError(CodeBinding, "raster/source/hash", ErrBinding)
	}
	frozenReceipt, err := validateFrozenTextSource(source.frozen)
	if err != nil || frozenReceipt.BindingHash != source.receipt.FrozenTextBindingHash ||
		frozenReceipt.FontContentHash != source.receipt.FontContentHash {
		return assemblyError(CodeBinding, "raster/source/frozen", errors.Join(ErrBinding, err))
	}
	layoutReceipt := source.layout.Receipt()
	layoutSourceReceipt := source.layoutSource.Receipt()
	if layoutReceipt.LayoutHash != source.receipt.LayoutHash ||
		layoutReceipt.GlyphStreamHash != source.receipt.GlyphStreamHash ||
		layoutSourceReceipt.LayoutSourceHash != source.receipt.LayoutSourceHash ||
		layoutSourceReceipt.ShapeProviderHash != source.receipt.ShapeProviderHash ||
		layoutSourceReceipt.FontContentHashProfile != source.receipt.FontContentHashProfile {
		return assemblyError(CodeBinding, "raster/source/lineage", ErrBinding)
	}
	fill := source.frozen.Fill()
	if source.fill.R != fill.R || source.fill.G != fill.G || source.fill.B != fill.B || source.fill.A != fill.A ||
		source.receipt.FillR != fill.R || source.receipt.FillG != fill.G ||
		source.receipt.FillB != fill.B || source.receipt.FillA != fill.A {
		return assemblyError(CodeBinding, "raster/source/fill", ErrBinding)
	}
	return nil
}

func validateInnerRaster(assembly Receipt, inner textraster.Receipt) error {
	providerDescriptor := sfntglyf.ShapeProviderDescriptor()
	if inner.SchemaVersion != textraster.ReceiptSchemaVersion || inner.Profile != textraster.ReceiptProfileV1 ||
		inner.RasterProfile != textraster.ProfileV1 ||
		inner.DescriptorHashProfile != textraster.DescriptorHashProfileV1 || inner.DescriptorHash != textraster.DescriptorHash() ||
		inner.LayoutSourceProfile != sfntglyf.LayoutSourceProfileV1 ||
		inner.LayoutSourceHashProfile != sfntglyf.LayoutSourceHashProfileV1 ||
		inner.ShapeProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
		inner.ShapeProviderHashProfile != sfntglyf.ShapeProviderHashProfileV1 ||
		inner.DependencyFingerprintProfile != providerDescriptor.DependencyFingerprintProfile ||
		inner.DependencyFingerprint != providerDescriptor.DependencyFingerprint ||
		inner.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
		inner.ShapingProvenanceStatus != textraster.ShapingProvenanceStatusV1 ||
		inner.LayoutReceiptProfile != textlayout.ReceiptProfileV1 ||
		inner.LayoutHashProfile != textlayout.LayoutHashProfileV1 ||
		inner.GlyphStreamHashProfile != textlayout.GlyphStreamHashProfileV1 ||
		inner.OutlineSetHashProfile != textlayout.OutlineSetHashProfileV1 ||
		inner.PlacementHashProfile != textlayout.PlacementHashProfileV1 ||
		inner.BindingHashProfile != textraster.BindingHashProfileV1 ||
		inner.CoverageHashProfile != textraster.CoverageHashProfileV1 ||
		inner.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		inner.KernelProfile != cpucompositor.KernelProfileV1 ||
		inner.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		inner.PixelHashProfile != textraster.PixelHashProfileV1 ||
		inner.LayoutSourceHashProfile != assembly.LayoutSourceHashProfile || inner.LayoutSourceHash != assembly.LayoutSourceHash ||
		inner.ShapeProviderHashProfile != assembly.ShapeProviderHashProfile || inner.ShapeProviderHash != assembly.ShapeProviderHash ||
		inner.DependencyFingerprintProfile != assembly.DependencyFingerprintProfile || inner.DependencyFingerprint != assembly.DependencyFingerprint ||
		inner.FontContentHashProfile != assembly.FontContentHashProfile || inner.FontContentHash != assembly.FontContentHash ||
		inner.LayoutReceiptProfile != assembly.LayoutReceiptProfile ||
		inner.LayoutHashProfile != assembly.LayoutHashProfile || inner.LayoutHash != assembly.LayoutHash ||
		inner.GlyphStreamHashProfile != assembly.GlyphStreamHashProfile || inner.GlyphStreamHash != assembly.GlyphStreamHash ||
		inner.OutlineSetHashProfile != assembly.ScaledOutlineHashProfile || inner.OutlineSetHash != assembly.ScaledOutlineHash ||
		inner.PlacementHashProfile != assembly.PlacementHashProfile || inner.PlacementHash != assembly.PlacementHash ||
		inner.WidthPixels != assembly.WidthPixels || inner.HeightPixels != assembly.HeightPixels ||
		inner.FontSizeNumerator != assembly.FontSizeNumerator || inner.FontSizeDenominator != assembly.FontSizeDenominator ||
		inner.FillR != assembly.FillR || inner.FillG != assembly.FillG || inner.FillB != assembly.FillB || inner.FillA != assembly.FillA ||
		inner.PlacementCount != assembly.GlyphCount || inner.UniqueGlyphCount != assembly.UniqueGlyphCount ||
		inner.ContourCount != assembly.ContourCount || inner.CommandCount != assembly.CommandCount ||
		!canonicalDigest(inner.BindingHash) || !canonicalDigest(inner.CoverageHash) || !canonicalDigest(inner.PixelHash) ||
		inner.CanonicalBinaryBytes == 0 || inner.WorkUnits == 0 || inner.WorkingBytes == 0 || inner.RetainedBytes == 0 {
		return assemblyError(CodeBinding, "raster/inner/receipt", ErrBinding)
	}
	return nil
}

func rasterReceiptStringBytes(value RasterReceipt) uint64 {
	var total uint64
	for _, field := range []string{
		value.Profile, value.DescriptorHashProfile, value.DescriptorHash, value.BindingHashProfile,
		value.AssemblyHashProfile, value.AssemblyHash, value.InnerReceiptProfile, value.InnerRasterProfile,
		value.InnerBindingHashProfile, value.InnerBindingHash,
		value.InnerShapingStatus, value.ShapingProvenanceStatus, value.PaintProvenanceStatus,
		value.CoverageHashProfile, value.CoverageHash, value.PixelHashProfile, value.PixelHash,
	} {
		next, ok := checkedAdd(total, uint64(len(field)))
		if !ok {
			return ^uint64(0)
		}
		total = next
	}
	return total
}
