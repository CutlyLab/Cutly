package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

type shapeHashSink struct {
	ctx   context.Context
	hash  hash.Hash
	bytes uint64
	block [4 << 10]byte
}

func newShapeHashSink(ctx context.Context) *shapeHashSink {
	return &shapeHashSink{ctx: ctx, hash: sha256.New()}
}

func (value *shapeHashSink) write(raw []byte, path string) error {
	if err := contextFailure(value.ctx, path); err != nil {
		return err
	}
	next, ok := checkedAdd(value.bytes, uint64(len(raw)))
	if !ok {
		return fontError(CodeShapeResourceLimit, path, ErrResourceLimit)
	}
	value.bytes = next
	_, _ = value.hash.Write(raw)
	return nil
}

func (value *shapeHashSink) u32(number uint32, path string) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], number)
	return value.write(encoded[:], path)
}

func (value *shapeHashSink) u64(number uint64, path string) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], number)
	return value.write(encoded[:], path)
}

func (value *shapeHashSink) i64(number int64, path string) error {
	return value.u64(uint64(number), path)
}

func (value *shapeHashSink) str(text, path string) error {
	if err := value.u64(uint64(len(text)), path+"/length"); err != nil {
		return err
	}
	// Never convert a caller string wholesale to []byte: that conversion escapes through the
	// hash.Hash interface and would allocate in proportion to Text before resource admission.
	// A fixed block keeps hashing memory independent of string length.
	for offset := 0; offset < len(text); {
		end := offset + len(value.block)
		if end > len(text) {
			end = len(text)
		}
		count := copy(value.block[:], text[offset:end])
		if err := value.write(value.block[:count], path); err != nil {
			return err
		}
		offset = end
	}
	return nil
}

func (value *shapeHashSink) finish() (string, uint64) {
	return "sha256:" + hex.EncodeToString(value.hash.Sum(nil)), value.bytes
}

func hashShapeProviderBinding(ctx context.Context, receipt ShapeProviderReceipt) (string, uint64, error) {
	sink := newShapeHashSink(ctx)
	strings := []string{
		ShapeProviderHashProfileV1, receipt.Profile, receipt.ProviderProfile,
		receipt.DescriptorHashProfile, receipt.DescriptorHash,
		receipt.DependencyLockProfile, receipt.DependencyFingerprintProfile, receipt.DependencyFingerprint,
		receipt.GoTextModuleVersion, receipt.EmbeddedHarfBuzzVersion, receipt.EmbeddedHarfBuzzCommit,
		receipt.BindingStatus, receipt.CapabilityStatus, receipt.LayoutGraphProfile,
		receipt.LayoutGraphHashProfile, receipt.LayoutGraphHash,
		receipt.OutlineSetHashProfile, receipt.OutlineSetHash,
		receipt.FontContentHashProfile, receipt.FontContentHash,
		receipt.ClusterPolicy, receipt.FeaturePolicy, receipt.DirectionNormalization,
	}
	for index, text := range strings {
		if err := sink.str(text, "shape_provider/hash/string"); err != nil {
			return "", 0, err
		}
		_ = index
	}
	for _, number := range []uint64{
		uint64(ShapeProviderSchemaVersion), uint64(receipt.UnitsPerEm), uint64(receipt.GlyphCount),
		receipt.EncodedBytes, receipt.CmapMappingCount, receipt.WorkUnits,
		receipt.WorkingBytes, receipt.RetainedBytes,
	} {
		if err := sink.u64(number, "shape_provider/hash/number"); err != nil {
			return "", 0, err
		}
	}
	hash, count := sink.finish()
	return hash, count, nil
}

func hashShapeInput(ctx context.Context, provider ShapeProviderReceipt, input ShapeInput) (string, uint64, error) {
	sink := newShapeHashSink(ctx)
	for _, text := range []string{
		ShapeInputHashProfileV1, ShapeProviderProfileV1, provider.ProviderHash,
		provider.DependencyFingerprint, provider.LayoutGraphHash, provider.OutlineSetHash,
		provider.FontContentHash, input.Text, input.Language, input.Script, string(input.Direction),
	} {
		if err := sink.str(text, "shape/hash/input"); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u32(ShapeProviderSchemaVersion, "shape/hash/input/schema"); err != nil {
		return "", 0, err
	}
	hash, count := sink.finish()
	return hash, count, nil
}

// shapeInputCanonicalBytes computes the exact input hash preimage length using scalar lengths
// only. It is intentionally called before hashShapeInput so low resource limits reject without
// any Text-proportional allocation or hashing work.
func shapeInputCanonicalBytes(provider ShapeProviderReceipt, input ShapeInput) (uint64, error) {
	count := uint64(4) // schema u32
	strings := [...]string{
		ShapeInputHashProfileV1, ShapeProviderProfileV1, provider.ProviderHash,
		provider.DependencyFingerprint, provider.LayoutGraphHash, provider.OutlineSetHash,
		provider.FontContentHash, input.Text, input.Language, input.Script, string(input.Direction),
	}
	for _, text := range strings {
		increment, ok := checkedAdd(8, uint64(len(text)))
		if !ok {
			return 0, fontError(CodeShapeResourceLimit, "shape/hash/input/size", ErrResourceLimit)
		}
		var err error
		count, err = shapeCheckedAdd(count, increment, "shape/hash/input/size")
		if err != nil {
			return 0, err
		}
	}
	return count, nil
}

func hashShapeOutput(ctx context.Context, provider ShapeProviderReceipt, input ShapeInput, lines []shapedLineRecord) (string, uint64, error) {
	sink := newShapeHashSink(ctx)
	for _, text := range []string{
		ShapeOutputHashProfileV1, ShapeOutputProfileV1, ShapeProviderProfileV1,
		provider.ProviderHash, provider.DependencyFingerprint, provider.LayoutGraphHash,
		provider.OutlineSetHash, provider.FontContentHash, input.Text, input.Language,
		input.Script, string(input.Direction),
	} {
		if err := sink.str(text, "shape/hash/output"); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u32(ShapeProviderSchemaVersion, "shape/hash/output/schema"); err != nil {
		return "", 0, err
	}
	if err := sink.u64(uint64(len(lines)), "shape/hash/output/lines"); err != nil {
		return "", 0, err
	}
	for lineIndex, line := range lines {
		if lineIndex&255 == 0 {
			if err := contextFailure(ctx, "shape/hash/output/lines"); err != nil {
				return "", 0, err
			}
		}
		for _, number := range []uint64{line.byteStart, line.byteEnd, uint64(len(line.glyphs))} {
			if err := sink.u64(number, "shape/hash/output/line"); err != nil {
				return "", 0, err
			}
		}
		for glyphIndex, glyph := range line.glyphs {
			if glyphIndex&1023 == 0 {
				if err := contextFailure(ctx, "shape/hash/output/glyphs"); err != nil {
					return "", 0, err
				}
			}
			if err := sink.u32(glyph.GlyphID, "shape/hash/output/glyph/id"); err != nil {
				return "", 0, err
			}
			if err := sink.u64(glyph.ClusterByteOffset, "shape/hash/output/glyph/cluster"); err != nil {
				return "", 0, err
			}
			for _, number := range []int64{
				int64(glyph.XAdvance), int64(glyph.YAdvance), int64(glyph.XOffset), int64(glyph.YOffset),
			} {
				if err := sink.i64(number, "shape/hash/output/glyph/position"); err != nil {
					return "", 0, err
				}
			}
		}
	}
	hash, count := sink.finish()
	return hash, count, nil
}
