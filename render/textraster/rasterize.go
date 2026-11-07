package textraster

import (
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// Rasterize live-binds a trusted static-glyf LayoutSource to one immutable caller-supplied Layout, then
// atomically publishes an immutable linear-premultiplied surface and scalar receipt. Every
// failure returns a nil Surface and the strictly zero Receipt. It proves the concrete layout was
// rasterized with source-bound outlines; it does not prove that the layout came from the source's
// ShapeProvider. The receipt records that distinction explicitly.
func Rasterize(ctx context.Context, request Request, limits Limits) (
	surface *cpucompositor.Surface, receipt Receipt, err error,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			surface = nil
			receipt = Receipt{}
			err = rasterError(CodeInvariantFailed, "raster/panic", fmt.Errorf("%w: %v", ErrInvariantFailed, recovered))
		}
		if err != nil {
			surface = nil
			receipt = Receipt{}
		}
	}()
	if ctx == nil {
		return nil, Receipt{}, rasterError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, Receipt{}, err
	}
	if err := validateLimits(limits); err != nil {
		return nil, Receipt{}, err
	}
	if request.Source == nil || request.Layout == nil {
		return nil, Receipt{}, rasterError(CodeInputInvalid, "request", ErrInputInvalid)
	}
	return rasterizeBound(ctx, liveSourceAccess{source: request.Source}, request, limits)
}

func rasterizeBound(ctx context.Context, source sourceAccess, request Request, limits Limits) (*cpucompositor.Surface, Receipt, error) {
	bound, err := bindRequest(ctx, source, request.Layout, request.FontSize, limits)
	if err != nil {
		return nil, Receipt{}, err
	}
	coverage, err := rasterCoverage(ctx, exactContext{maxBits: limits.MaxExactBits}, bound)
	if err != nil {
		return nil, Receipt{}, err
	}
	coverageHash, coverageBytes, err := hashCoverage(ctx, bound.measure.width, bound.measure.height, coverage)
	if err != nil {
		return nil, Receipt{}, err
	}
	paint := color.PaintRGBA16ToPixel(request.Fill)
	if err := contextFailure(ctx, "raster/paint"); err != nil {
		return nil, Receipt{}, err
	}
	pixels := make([]cpucompositor.Pixel, int(bound.measure.pixels))
	for index, covered := range coverage {
		if index&8191 == 0 {
			if err := contextFailure(ctx, "raster/paint"); err != nil {
				return nil, Receipt{}, err
			}
		}
		pixels[index] = cpucompositor.Pixel{
			R: roundCoverage(uint64(paint.R) * uint64(covered)),
			G: roundCoverage(uint64(paint.G) * uint64(covered)),
			B: roundCoverage(uint64(paint.B) * uint64(covered)),
			A: roundCoverage(uint64(paint.A) * uint64(covered)),
		}
	}
	pixelHash, pixelBytes, err := hashPixels(ctx, pixels)
	if err != nil {
		return nil, Receipt{}, err
	}
	sourceReceipt := bound.sourceReceipt
	layoutReceipt := bound.layoutReceipt
	resultReceipt := Receipt{
		SchemaVersion: ReceiptSchemaVersion, Profile: ReceiptProfileV1, RasterProfile: ProfileV1,
		DescriptorHashProfile: DescriptorHashProfileV1, DescriptorHash: DescriptorHash(),
		LayoutSourceProfile:     sourceReceipt.LayoutSourceProfile,
		LayoutSourceHashProfile: sourceReceipt.LayoutSourceHashProfile, LayoutSourceHash: sourceReceipt.LayoutSourceHash,
		ShapeProviderProfile: sourceReceipt.ShapeProviderProfile, ShapeProviderHashProfile: sourceReceipt.ShapeProviderHashProfile,
		ShapeProviderHash:            sourceReceipt.ShapeProviderHash,
		DependencyFingerprintProfile: sourceReceipt.DependencyFingerprintProfile,
		DependencyFingerprint:        sourceReceipt.DependencyFingerprint,
		FontContentHashProfile:       sourceReceipt.FontContentHashProfile, FontContentHash: sourceReceipt.FontContentHash,
		ShapingProvenanceStatus: ShapingProvenanceStatusV1,
		LayoutReceiptProfile:    layoutReceipt.Profile, LayoutHashProfile: layoutReceipt.LayoutHashProfile,
		LayoutHash: layoutReceipt.LayoutHash, GlyphStreamHashProfile: layoutReceipt.GlyphStreamHashProfile,
		GlyphStreamHash: layoutReceipt.GlyphStreamHash, OutlineSetHashProfile: layoutReceipt.OutlineSetHashProfile,
		OutlineSetHash: layoutReceipt.OutlineSetHash, PlacementHashProfile: layoutReceipt.PlacementHashProfile,
		PlacementHash:      layoutReceipt.PlacementHash,
		BindingHashProfile: BindingHashProfileV1, CoverageHashProfile: CoverageHashProfileV1,
		CoverageHash: coverageHash, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		PixelHashProfile: PixelHashProfileV1, PixelHash: pixelHash,
		FontSizeNumerator:   request.FontSize.Numerator,
		FontSizeDenominator: request.FontSize.Denominator, WidthPixels: bound.measure.width,
		HeightPixels: bound.measure.height, FillR: request.Fill.R, FillG: request.Fill.G,
		FillB: request.Fill.B, FillA: request.Fill.A, PlacementCount: bound.measure.placements,
		UniqueGlyphCount: bound.measure.uniqueGlyphs, ContourCount: bound.measure.contours,
		CommandCount: bound.measure.commands, UniqueSegmentCount: bound.measure.uniqueSegments,
		SegmentCount: bound.measure.segments,
		SampleCount:  bound.measure.samples, WorkUnits: bound.measure.workUnits,
		WorkingBytes: bound.measure.workingBytes, RetainedBytes: bound.measure.retainedBytes,
	}
	_, bindingBytes, err := hashBinding(ctx, resultReceipt)
	if err != nil {
		return nil, Receipt{}, err
	}
	canonicalBytes, ok := checkedAdd(coverageBytes, pixelBytes)
	if ok {
		canonicalBytes, ok = checkedAdd(canonicalBytes, bindingBytes)
	}
	if !ok || canonicalBytes > bound.measure.canonicalUpper || canonicalBytes > limits.MaxCanonicalBinaryBytes {
		return nil, Receipt{}, rasterError(CodeResourceLimit, "raster/canonical_binary_bytes", ErrResourceLimit)
	}
	resultReceipt.CanonicalBinaryBytes = canonicalBytes
	bindingHash, finalBindingBytes, err := hashBinding(ctx, resultReceipt)
	if err != nil {
		return nil, Receipt{}, err
	}
	if finalBindingBytes != bindingBytes {
		return nil, Receipt{}, rasterError(CodeInvariantFailed, "raster/binding_hash", ErrInvariantFailed)
	}
	resultReceipt.BindingHash = bindingHash
	cloneReceiptStrings(&resultReceipt)
	if err := contextFailure(ctx, "raster/surface"); err != nil {
		return nil, Receipt{}, err
	}
	surfaceLimits := cpucompositor.Limits{
		MaxWidth: int(bound.measure.width), MaxHeight: int(bound.measure.height), MaxBytes: bound.measure.retainedBytes,
	}
	result, err := cpucompositor.NewSurfaceFromPixelsWithLimits(int(bound.measure.width), int(bound.measure.height), pixels, surfaceLimits)
	if err != nil {
		if errors.Is(err, cpucompositor.ErrAllocationFailed) {
			return nil, Receipt{}, rasterError(CodeAllocationFailed, "raster/surface", errors.Join(ErrAllocationFailed, err))
		}
		return nil, Receipt{}, rasterError(CodeSurfaceFailed, "raster/surface", errors.Join(ErrSurfaceFailed, err))
	}
	if err := contextFailure(ctx, "raster/result"); err != nil {
		return nil, Receipt{}, err
	}
	return result, resultReceipt, nil
}

func roundCoverage(numerator uint64) uint16 {
	quotient := numerator / 64
	remainder := numerator % 64
	if remainder > 32 || remainder == 32 && quotient&1 != 0 {
		quotient++
	}
	return uint16(quotient)
}

func contextFailure(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return rasterError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}
