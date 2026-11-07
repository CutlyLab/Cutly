package textraster

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

type hashSink struct {
	ctx     context.Context
	digest  hash.Hash
	written uint64
	err     error
}

func newHashSink(ctx context.Context) *hashSink { return &hashSink{ctx: ctx, digest: sha256.New()} }

func (sink *hashSink) bytes(value []byte) {
	if sink.err != nil {
		return
	}
	if err := contextFailure(sink.ctx, "raster/hash"); err != nil {
		sink.err = err
		return
	}
	next, ok := checkedAdd(sink.written, uint64(len(value)))
	if !ok {
		sink.err = rasterError(CodeResourceLimit, "raster/hash/bytes", ErrResourceLimit)
		return
	}
	_, _ = sink.digest.Write(value)
	sink.written = next
}

func (sink *hashSink) str(value string) {
	sink.u64(uint64(len(value)))
	sink.bytes([]byte(value))
}

func (sink *hashSink) u8(value uint8) { sink.bytes([]byte{value}) }

func (sink *hashSink) u16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	sink.bytes(encoded[:])
}

func (sink *hashSink) u32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	sink.bytes(encoded[:])
}

func (sink *hashSink) u64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	sink.bytes(encoded[:])
}

func (sink *hashSink) i64(value int64) { sink.u64(uint64(value)) }

func (sink *hashSink) finish() (string, uint64, error) {
	if sink.err != nil {
		return "", 0, sink.err
	}
	if err := contextFailure(sink.ctx, "raster/hash"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(sink.digest.Sum(nil)), sink.written, nil
}

func hashCoverage(ctx context.Context, width, height uint32, coverage []uint8) (string, uint64, error) {
	sink := newHashSink(ctx)
	sink.str(CoverageHashProfileV1)
	sink.u32(width)
	sink.u32(height)
	sink.u64(uint64(len(coverage)))
	const chunk = 64 << 10
	for offset := 0; offset < len(coverage); offset += chunk {
		end := offset + chunk
		if end > len(coverage) {
			end = len(coverage)
		}
		sink.bytes(coverage[offset:end])
	}
	return sink.finish()
}

func hashPixels(ctx context.Context, pixels []cpucompositor.Pixel) (string, uint64, error) {
	digest := sha256.New()
	var encoded [8]byte
	for index, pixel := range pixels {
		if index&8191 == 0 {
			if err := contextFailure(ctx, "raster/pixel_hash"); err != nil {
				return "", 0, err
			}
		}
		binary.BigEndian.PutUint16(encoded[0:2], pixel.R)
		binary.BigEndian.PutUint16(encoded[2:4], pixel.G)
		binary.BigEndian.PutUint16(encoded[4:6], pixel.B)
		binary.BigEndian.PutUint16(encoded[6:8], pixel.A)
		_, _ = digest.Write(encoded[:])
	}
	bytes, ok := checkedMul(uint64(len(pixels)), 8)
	if !ok {
		return "", 0, rasterError(CodeResourceLimit, "raster/pixel_hash/bytes", ErrResourceLimit)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), bytes, nil
}

func hashBinding(ctx context.Context, receipt Receipt) (string, uint64, error) {
	sink := newHashSink(ctx)
	sink.str(BindingHashProfileV1)
	sink.u32(ProfileSchemaVersion)
	sink.u32(uint32(receipt.SchemaVersion))
	sink.str(receipt.Profile)
	sink.str(receipt.RasterProfile)
	sink.str(receipt.DescriptorHashProfile)
	sink.str(receipt.DescriptorHash)
	sink.str(receipt.LayoutSourceProfile)
	sink.str(receipt.LayoutSourceHashProfile)
	sink.str(receipt.LayoutSourceHash)
	sink.str(receipt.ShapeProviderProfile)
	sink.str(receipt.ShapeProviderHashProfile)
	sink.str(receipt.ShapeProviderHash)
	sink.str(receipt.DependencyFingerprintProfile)
	sink.str(receipt.DependencyFingerprint)
	sink.str(receipt.FontContentHashProfile)
	sink.str(receipt.FontContentHash)
	sink.str(receipt.ShapingProvenanceStatus)
	sink.str(receipt.LayoutReceiptProfile)
	sink.str(receipt.LayoutHashProfile)
	sink.str(receipt.LayoutHash)
	sink.str(receipt.GlyphStreamHashProfile)
	sink.str(receipt.GlyphStreamHash)
	sink.str(receipt.OutlineSetHashProfile)
	sink.str(receipt.OutlineSetHash)
	sink.str(receipt.PlacementHashProfile)
	sink.str(receipt.PlacementHash)
	sink.str(receipt.BindingHashProfile)
	sink.str(receipt.CoverageHashProfile)
	sink.str(receipt.CoverageHash)
	sink.str(receipt.PaintProfile)
	sink.str(receipt.KernelProfile)
	sink.str(receipt.SurfaceProfile)
	sink.str(receipt.PixelHashProfile)
	sink.str(receipt.PixelHash)
	sink.i64(receipt.FontSizeNumerator)
	sink.i64(receipt.FontSizeDenominator)
	sink.u32(receipt.WidthPixels)
	sink.u32(receipt.HeightPixels)
	sink.u16(receipt.FillR)
	sink.u16(receipt.FillG)
	sink.u16(receipt.FillB)
	sink.u16(receipt.FillA)
	sink.u64(receipt.PlacementCount)
	sink.u64(receipt.UniqueGlyphCount)
	sink.u64(receipt.ContourCount)
	sink.u64(receipt.CommandCount)
	sink.u64(receipt.UniqueSegmentCount)
	sink.u64(receipt.SegmentCount)
	sink.u64(receipt.SampleCount)
	sink.u64(receipt.CanonicalBinaryBytes)
	sink.u64(receipt.WorkUnits)
	sink.u64(receipt.WorkingBytes)
	sink.u64(receipt.RetainedBytes)
	return sink.finish()
}
