package textlayer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textraster"
)

func TestValidateSurfaceCancelsInsideIndependentCanonicalHash(t *testing.T) {
	pixels := make([]cpucompositor.Pixel, 64*64)
	surface, err := cpucompositor.NewSurfaceFromPixelsWithLimits(64, 64, pixels, cpucompositor.Limits{
		MaxWidth: 64, MaxHeight: 64, MaxBytes: uint64(len(pixels)) * cpucompositor.BytesPerPixel,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := surface.CanonicalHashContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	asset := frozenAsset{index: 2, viewport: documentv2.RasterViewport{WidthPixels: 64, HeightPixels: 64}}
	digest := "sha256:" + strings.Repeat("0", 64)
	assembly := textassembly.Receipt{AssemblyHash: digest}
	raster := textassembly.RasterReceipt{
		SchemaVersion: textassembly.SchemaVersion, Profile: textassembly.RasterReceiptProfileV1,
		DescriptorHashProfile: textassembly.DescriptorHashProfileV1, DescriptorHash: textassembly.DescriptorHash(),
		BindingHashProfile: textassembly.RasterBindingHashProfileV1, BindingHash: digest,
		AssemblyHashProfile: textassembly.AssemblyHashProfileV1, AssemblyHash: assembly.AssemblyHash,
		InnerReceiptProfile: textraster.ReceiptProfileV1, InnerRasterProfile: textraster.ProfileV1,
		InnerBindingHashProfile: textraster.BindingHashProfileV1, InnerBindingHash: digest,
		InnerShapingStatus:      textraster.ShapingProvenanceStatusV1,
		ShapingProvenanceStatus: textassembly.ShapingProvenanceStatusV1,
		PaintProvenanceStatus:   textassembly.PaintProvenanceStatusV1,
		CoverageHashProfile:     textraster.CoverageHashProfileV1, CoverageHash: digest,
		WidthPixels: 64, HeightPixels: 64, PixelHashProfile: SurfaceHashProfileV1, PixelHash: hash,
		CanonicalBinaryBytes: 1, WorkUnits: 1, WorkingBytes: 1, RetainedBytes: 1,
	}
	ctx := &cancelHashContext{cancelAt: 3}
	identity, bytes, err := validateSurface(ctx, asset, assembly, surface, raster)
	if identity != (SurfaceIdentity{}) || bytes != 0 || !errors.Is(err, ErrCanceled) || ctx.checks < 3 {
		t.Fatalf("identity=%+v bytes=%d err=%v checks=%d", identity, bytes, err, ctx.checks)
	}
}

type cancelHashContext struct {
	checks   int
	cancelAt int
}

func (*cancelHashContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*cancelHashContext) Done() <-chan struct{}       { return nil }
func (*cancelHashContext) Value(any) any               { return nil }
func (value *cancelHashContext) Err() error {
	value.checks++
	if value.checks >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
