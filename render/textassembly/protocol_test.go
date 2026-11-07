package textassembly_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/render/textassembly"
)

func TestCanonicalProtocolFixturesAndIndependentHashes(t *testing.T) {
	descriptorCanonical := textassembly.DescriptorCanonicalJSON()
	if prefix := []byte(`{"schema_version":1,"profile":"cutly.render.text-assembly.static-glyf.v1","receipt_profile":`); !bytes.HasPrefix(descriptorCanonical, prefix) {
		t.Fatalf("descriptor field-order prefix=%s", descriptorCanonical)
	}
	wantDescriptor, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read descriptor fixture: %v\nactual=%s", err, descriptorCanonical)
	}
	wantDescriptor = bytes.TrimSuffix(wantDescriptor, []byte("\n"))
	if !bytes.Equal(descriptorCanonical, wantDescriptor) {
		t.Errorf("descriptor fixture drift\nactual=%s\nwant=%s", descriptorCanonical, wantDescriptor)
	}
	if got := oracleDescriptorHash(descriptorCanonical); got != textassembly.DescriptorHash() {
		t.Fatalf("descriptor hash=%q want=%q", textassembly.DescriptorHash(), got)
	}

	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenText(t, ctx, fontHash)
	source, assembly, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, textassembly.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, raster, err := textassembly.RasterizeStatic(ctx, source, textassembly.DefaultRasterLimits())
	if err != nil {
		t.Fatal(err)
	}
	fixture := struct {
		Assembly textassembly.Receipt       `json:"assembly"`
		Raster   textassembly.RasterReceipt `json:"raster"`
	}{Assembly: assembly, Raster: raster}
	actualReceipt, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	wantReceipt, err := os.ReadFile("testdata/minimal-receipt-v1.canonical.json")
	if err != nil {
		t.Fatalf("read receipt fixture: %v\nactual=%s", err, actualReceipt)
	}
	wantReceipt = bytes.TrimSuffix(wantReceipt, []byte("\n"))
	if !bytes.Equal(actualReceipt, wantReceipt) {
		t.Fatalf("receipt fixture drift\nactual=%s\nwant=%s", actualReceipt, wantReceipt)
	}
	if got := oracleAssemblyHash(assembly); got != assembly.AssemblyHash {
		t.Fatalf("assembly hash=%q oracle=%q", assembly.AssemblyHash, got)
	}
	if got := oracleRasterHash(raster); got != raster.BindingHash {
		t.Fatalf("raster hash=%q oracle=%q", raster.BindingHash, got)
	}
}

func TestDescriptorIsDefensiveValueCopy(t *testing.T) {
	first := textassembly.Descriptor()
	first.Profile = "mutated"
	first.DefaultLimits.MaxGlyphs = 1
	first.DefaultRasterLimits.MaxWorkUnits = 1
	second := textassembly.Descriptor()
	if second.Profile != textassembly.ProfileV1 || second.DefaultLimits != textassembly.DefaultLimits() ||
		second.DefaultRasterLimits != textassembly.DefaultRasterLimits() {
		t.Fatalf("descriptor mutated through returned value: %+v", second)
	}
	canonical := textassembly.DescriptorCanonicalJSON()
	canonical[0] ^= 0xff
	if bytes.Equal(canonical, textassembly.DescriptorCanonicalJSON()) {
		t.Fatal("DescriptorCanonicalJSON returned aliased storage")
	}
}

func oracleDescriptorHash(canonical []byte) string {
	sink := newOracleSink()
	sink.str(textassembly.DescriptorHashProfileV1)
	sink.raw(canonical)
	return sink.digest()
}

func oracleAssemblyHash(value textassembly.Receipt) string {
	sink := newOracleSink()
	sink.str(textassembly.AssemblyHashProfileV1)
	sink.u32(textassembly.SchemaVersion)
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
	return sink.digest()
}

func oracleRasterHash(value textassembly.RasterReceipt) string {
	sink := newOracleSink()
	sink.str(textassembly.RasterBindingHashProfileV1)
	sink.u32(textassembly.SchemaVersion)
	sink.u32(uint32(value.SchemaVersion))
	for _, field := range []string{
		value.Profile, value.DescriptorHashProfile, value.DescriptorHash, value.BindingHashProfile,
		value.AssemblyHashProfile, value.AssemblyHash, value.InnerReceiptProfile, value.InnerRasterProfile,
		value.InnerBindingHashProfile, value.InnerBindingHash, value.InnerShapingStatus,
		value.ShapingProvenanceStatus, value.PaintProvenanceStatus, value.CoverageHashProfile,
		value.CoverageHash, value.PixelHashProfile, value.PixelHash,
	} {
		sink.str(field)
	}
	sink.u32(value.WidthPixels)
	sink.u32(value.HeightPixels)
	sink.u64(value.CanonicalBinaryBytes)
	sink.u64(value.WorkUnits)
	sink.u64(value.WorkingBytes)
	sink.u64(value.RetainedBytes)
	return sink.digest()
}

type oracleSink struct{ hash hash.Hash }

func newOracleSink() *oracleSink { return &oracleSink{hash: sha256.New()} }

func (sink *oracleSink) raw(value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = sink.hash.Write(length[:])
	_, _ = sink.hash.Write(value)
}

func (sink *oracleSink) str(value string) { sink.raw([]byte(value)) }

func (sink *oracleSink) u16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	_, _ = sink.hash.Write(encoded[:])
}

func (sink *oracleSink) u32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = sink.hash.Write(encoded[:])
}

func (sink *oracleSink) u64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = sink.hash.Write(encoded[:])
}

func (sink *oracleSink) i64(value int64) { sink.u64(uint64(value)) }

func (sink *oracleSink) digest() string {
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil))
}
