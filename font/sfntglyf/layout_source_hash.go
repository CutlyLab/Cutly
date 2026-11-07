package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

type layoutSourceHashSink struct {
	ctx   context.Context
	hash  hash.Hash
	bytes uint64
}

func newLayoutSourceHashSink(ctx context.Context) *layoutSourceHashSink {
	return &layoutSourceHashSink{ctx: ctx, hash: sha256.New()}
}

func (value *layoutSourceHashSink) write(raw []byte, path string) error {
	if err := contextFailure(value.ctx, path); err != nil {
		return err
	}
	next, ok := checkedAdd(value.bytes, uint64(len(raw)))
	if !ok {
		return fontError(CodeLayoutSourceResource, path, ErrResourceLimit)
	}
	value.bytes = next
	_, _ = value.hash.Write(raw)
	return nil
}

func (value *layoutSourceHashSink) u32(number uint32, path string) error {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], number)
	return value.write(raw[:], path)
}

func (value *layoutSourceHashSink) u64(number uint64, path string) error {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], number)
	return value.write(raw[:], path)
}

func (value *layoutSourceHashSink) i64(number int64, path string) error {
	return value.u64(uint64(number), path)
}

func (value *layoutSourceHashSink) str(text, path string) error {
	if err := value.u64(uint64(len(text)), path+"/length"); err != nil {
		return err
	}
	return value.write([]byte(text), path)
}

func (value *layoutSourceHashSink) finish() (string, uint64) {
	return "sha256:" + hex.EncodeToString(value.hash.Sum(nil)), value.bytes
}

func hashLayoutSourceBinding(ctx context.Context, receipt LayoutSourceReceipt) (string, uint64, error) {
	sink := newLayoutSourceHashSink(ctx)
	strings := [...]string{
		LayoutSourceHashProfileV1, receipt.Profile, receipt.LayoutSourceProfile,
		receipt.DescriptorHashProfile, receipt.DescriptorHash,
		receipt.LayoutSourceHashProfile,
		receipt.ShapeProviderProfile, receipt.ShapeProviderHashProfile, receipt.ShapeProviderHash,
		receipt.DependencyFingerprintProfile, receipt.DependencyFingerprint,
		receipt.LayoutGraphProfile, receipt.LayoutGraphHashProfile, receipt.LayoutGraphHash,
		receipt.OutlineSetProfile, receipt.OutlineSetHashProfile, receipt.OutlineSetHash,
		receipt.FontReceiptProfile, receipt.FontContentHashProfile, receipt.FontContentHash,
		receipt.BindingStatus, receipt.OutlineProfile, receipt.CommandStatus,
		receipt.RoundingStatus, receipt.HintingStatus, receipt.LayoutStatus,
		receipt.VariableLayoutStatus, receipt.RasterStatus, receipt.CapabilityStatus,
	}
	for _, text := range strings {
		if err := sink.str(text, "layout_source/hash/string"); err != nil {
			return "", 0, err
		}
	}
	for _, number := range []uint64{
		uint64(receipt.SchemaVersion), uint64(receipt.UnitsPerEm), uint64(receipt.GlyphCount),
		receipt.GlyphsWithContours, receipt.ContourCount, receipt.CommandCount,
		receipt.CommandPointOccurrences, receipt.MaxCommandsPerGlyph,
		receipt.WorkUnits, receipt.WorkingBytes, receipt.RetainedBytes,
	} {
		if err := sink.u64(number, "layout_source/hash/number"); err != nil {
			return "", 0, err
		}
	}
	if err := sink.i64(int64(receipt.HheaAscender), "layout_source/hash/hhea_ascender"); err != nil {
		return "", 0, err
	}
	hash, count := sink.finish()
	return hash, count, nil
}
