package textassembly

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strings"
)

type bindingSink struct {
	hash  hash.Hash
	bytes uint64
	bad   bool
}

func newBindingSink() *bindingSink { return &bindingSink{hash: sha256.New()} }

func (sink *bindingSink) raw(value []byte) {
	if sink.bad {
		return
	}
	next, ok := checkedAdd(sink.bytes, uint64(len(value)))
	if !ok {
		sink.bad = true
		return
	}
	sink.bytes = next
	_, _ = sink.hash.Write(value)
}

func (sink *bindingSink) u16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	sink.raw(encoded[:])
}

func (sink *bindingSink) u32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	sink.raw(encoded[:])
}

func (sink *bindingSink) u64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	sink.raw(encoded[:])
}

func (sink *bindingSink) i64(value int64) { sink.u64(uint64(value)) }

func (sink *bindingSink) str(value string) {
	sink.u64(uint64(len(value)))
	sink.raw([]byte(value))
}

func (sink *bindingSink) digest() (string, uint64, error) {
	if sink.bad {
		return "", 0, assemblyError(CodeResourceLimit, "binding/bytes", ErrResourceLimit)
	}
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil)), sink.bytes, nil
}

func hashAssembly(ctx context.Context, value Receipt) (string, uint64, error) {
	if err := contextFailure(ctx, "assembly/hash/context"); err != nil {
		return "", 0, err
	}
	sink := newBindingSink()
	sink.str(AssemblyHashProfileV1)
	sink.u32(SchemaVersion)
	sink.u32(uint32(value.SchemaVersion))
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
		sink.str(field)
	}
	sink.u64(value.TextAssetIndex)
	sink.u64(value.FontAssetIndex)
	sink.u32(value.WidthPixels)
	sink.u32(value.HeightPixels)
	sink.i64(value.FontSizeNumerator)
	sink.i64(value.FontSizeDenominator)
	sink.u16(value.FillR)
	sink.u16(value.FillG)
	sink.u16(value.FillB)
	sink.u16(value.FillA)
	for _, field := range []uint64{
		value.LineCount, value.GlyphCount, value.UniqueGlyphCount, value.ContourCount,
		value.CommandCount, value.CommandPointOccurrences, value.CanonicalBinaryBytes,
		value.WorkUnits, value.WorkingBytes, value.RetainedBytes,
	} {
		sink.u64(field)
	}
	if err := contextFailure(ctx, "assembly/hash/publish"); err != nil {
		return "", 0, err
	}
	return sink.digest()
}

func hashRaster(ctx context.Context, value RasterReceipt) (string, uint64, error) {
	if err := contextFailure(ctx, "raster/hash/context"); err != nil {
		return "", 0, err
	}
	sink := newBindingSink()
	sink.str(RasterBindingHashProfileV1)
	sink.u32(SchemaVersion)
	sink.u32(uint32(value.SchemaVersion))
	for _, field := range []string{
		value.Profile, value.DescriptorHashProfile, value.DescriptorHash, value.BindingHashProfile,
		value.AssemblyHashProfile, value.AssemblyHash, value.InnerReceiptProfile, value.InnerRasterProfile,
		value.InnerBindingHashProfile, value.InnerBindingHash,
		value.InnerShapingStatus, value.ShapingProvenanceStatus, value.PaintProvenanceStatus,
		value.CoverageHashProfile, value.CoverageHash, value.PixelHashProfile, value.PixelHash,
	} {
		sink.str(field)
	}
	sink.u32(value.WidthPixels)
	sink.u32(value.HeightPixels)
	sink.u64(value.CanonicalBinaryBytes)
	sink.u64(value.WorkUnits)
	sink.u64(value.WorkingBytes)
	sink.u64(value.RetainedBytes)
	if err := contextFailure(ctx, "raster/hash/publish"); err != nil {
		return "", 0, err
	}
	return sink.digest()
}

func cloneReceiptStrings(value *Receipt) {
	fields := []*string{
		&value.Profile, &value.DescriptorHashProfile, &value.DescriptorHash, &value.AssemblyHashProfile,
		&value.AssemblyHash, &value.FrozenTextProfile, &value.FrozenTextBindingHashProfile,
		&value.FrozenTextBindingHash, &value.DocumentHashProfile, &value.DocumentHash,
		&value.TextAssetID, &value.FontAssetID, &value.FontContentHashProfile, &value.FontContentHash,
		&value.TextSemanticHashProfile, &value.TextSemanticHash, &value.LayoutInputHashProfile,
		&value.LayoutInputHash, &value.ShapeProviderProfile, &value.ShapeProviderHashProfile,
		&value.ShapeProviderHash, &value.DependencyFingerprintProfile, &value.DependencyFingerprint,
		&value.LayoutGraphHashProfile, &value.LayoutGraphHash, &value.SourceOutlineSetHashProfile,
		&value.SourceOutlineSetHash, &value.LayoutSourceHashProfile, &value.LayoutSourceHash,
		&value.ShapeInputHashProfile, &value.ShapeInputHash, &value.ShapeOutputProfile,
		&value.ShapeOutputHashProfile, &value.ShapeOutputHash, &value.LayoutReceiptProfile,
		&value.GlyphStreamHashProfile, &value.GlyphStreamHash, &value.ScaledOutlineHashProfile,
		&value.ScaledOutlineHash, &value.PlacementHashProfile, &value.PlacementHash,
		&value.LayoutHashProfile, &value.LayoutHash, &value.ShapingProvenanceStatus,
		&value.PaintProvenanceStatus, &value.CapabilityStatus,
	}
	for _, field := range fields {
		*field = strings.Clone(*field)
	}
}

func cloneRasterReceiptStrings(value *RasterReceipt) {
	fields := []*string{
		&value.Profile, &value.DescriptorHashProfile, &value.DescriptorHash, &value.BindingHashProfile,
		&value.BindingHash, &value.AssemblyHashProfile, &value.AssemblyHash, &value.InnerReceiptProfile,
		&value.InnerRasterProfile, &value.InnerBindingHashProfile, &value.InnerBindingHash,
		&value.InnerShapingStatus, &value.ShapingProvenanceStatus,
		&value.PaintProvenanceStatus, &value.CoverageHashProfile, &value.CoverageHash,
		&value.PixelHashProfile, &value.PixelHash,
	}
	for _, field := range fields {
		*field = strings.Clone(*field)
	}
}
