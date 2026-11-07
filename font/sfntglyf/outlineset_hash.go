package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
)

type outlineSetHashSink struct {
	ctx   context.Context
	hash  hash.Hash
	count uint64
	limit uint64
	work  *outlineSetWorkMeter
}

func (value *outlineSetHashSink) raw(raw []byte, path string) error {
	next, ok := checkedAdd(value.count, uint64(len(raw)))
	if !ok || next > value.limit {
		return outlineSetResource("outline_set/hash/canonical_bytes")
	}
	if err := value.work.chargeCanonicalBytes(uint64(len(raw)), path); err != nil {
		return err
	}
	for offset := 0; offset < len(raw); offset += contextStrideBytes {
		if err := contextFailure(value.ctx, path); err != nil {
			return err
		}
		end := offset + contextStrideBytes
		if end > len(raw) {
			end = len(raw)
		}
		_, _ = value.hash.Write(raw[offset:end])
	}
	value.count = next
	return nil
}

func (value *outlineSetHashSink) str(text, path string) error {
	if err := value.u64(uint64(len(text)), path+"/length"); err != nil {
		return err
	}
	return value.raw([]byte(text), path+"/bytes")
}

func (value *outlineSetHashSink) u8(number uint8, path string) error {
	return value.raw([]byte{number}, path)
}

func (value *outlineSetHashSink) u32(number uint32, path string) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *outlineSetHashSink) u64(number uint64, path string) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *outlineSetHashSink) i64(number int64, path string) error {
	return value.u64(uint64(number), path)
}

type outlineSetHashWriter struct {
	sink       outlineSetHashSink
	source     *GeometryFont
	glyphCount uint32
	nextGlyph  uint32
	finished   bool
}

func newOutlineSetHashWriter(ctx context.Context, source *GeometryFont, limit uint64,
	work *outlineSetWorkMeter) (*outlineSetHashWriter, error) {
	if ctx == nil {
		return nil, fontError(CodeContextInvalid, "outline_set/hash/context", ErrContextInvalid)
	}
	if source == nil || uint64(len(source.glyphs)) > uint64(^uint32(0)) || uint32(len(source.glyphs)) != source.receipt.GlyphCount {
		return nil, outlineSetInvariant("outline_set/hash/source")
	}
	value := &outlineSetHashWriter{
		sink:   outlineSetHashSink{ctx: ctx, hash: sha256.New(), limit: limit, work: work},
		source: source, glyphCount: uint32(len(source.glyphs)),
	}
	r := source.receipt
	writes := []func() error{
		func() error { return value.sink.str(OutlineSetHashProfileV1, "outline_set/hash/hash_profile") },
		func() error { return value.sink.u32(OutlineSetProfileSchemaVersion, "outline_set/hash/schema") },
		func() error { return value.sink.str(OutlineSetProfileV1, "outline_set/hash/profile") },
		func() error { return value.sink.str(OutlineSetOutlineProfileV1, "outline_set/hash/outline_profile") },
		func() error { return value.sink.str(OutlineSetDescriptorHash(), "outline_set/hash/descriptor_hash") },
		func() error { return value.sink.str(r.Profile, "outline_set/hash/source_profile") },
		func() error {
			return value.sink.str(r.DescriptorHashProfile, "outline_set/hash/source_descriptor_hash_profile")
		},
		func() error { return value.sink.str(r.DescriptorHash, "outline_set/hash/source_descriptor_hash") },
		func() error {
			return value.sink.str(r.GeometryHashProfile, "outline_set/hash/source_geometry_hash_profile")
		},
		func() error { return value.sink.str(r.GeometryHash, "outline_set/hash/source_geometry_hash") },
		func() error {
			return value.sink.u64(r.CanonicalBinaryBytes, "outline_set/hash/source_geometry_canonical_bytes")
		},
		func() error {
			return value.sink.str(r.SourceGraphHashProfile, "outline_set/hash/source_graph_hash_profile")
		},
		func() error { return value.sink.str(r.SourceGraphHash, "outline_set/hash/source_graph_hash") },
		func() error {
			return value.sink.u64(r.SourceCanonicalBinaryBytes, "outline_set/hash/source_graph_canonical_bytes")
		},
		func() error {
			return value.sink.str(r.UpstreamContentHashProfile, "outline_set/hash/upstream_content_hash_profile")
		},
		func() error { return value.sink.str(r.UpstreamContentHash, "outline_set/hash/upstream_content_hash") },
		func() error { return value.sink.u32(value.glyphCount, "outline_set/hash/glyph_count") },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return nil, err
		}
	}
	return value, nil
}

func (value *outlineSetHashWriter) writeGlyph(glyphID uint32, glyph outlineSetGlyphRange, commands []outlineSetCommand) error {
	if value == nil || value.finished || glyphID != value.nextGlyph || glyphID >= value.glyphCount {
		return outlineSetInvariant("outline_set/hash/glyph_order")
	}
	if glyph.commandStart > uint64(len(commands)) || glyph.commandCount > uint64(len(commands))-glyph.commandStart {
		return outlineSetInvariant("outline_set/hash/glyph_range")
	}
	path := fmt.Sprintf("outline_set/hash/glyphs/%d", glyphID)
	writes := []func() error{
		func() error { return value.sink.u32(glyphID, path+"/gid") },
		func() error { return value.sink.u64(glyph.contours, path+"/contours") },
		func() error { return value.sink.u64(glyph.commandCount, path+"/commands") },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	end := glyph.commandStart + glyph.commandCount
	for commandIndex := glyph.commandStart; commandIndex < end; commandIndex++ {
		if (commandIndex-glyph.commandStart)%1024 == 0 {
			if err := contextFailure(value.sink.ctx, path+"/commands"); err != nil {
				return err
			}
		}
		command := commands[commandIndex]
		expected := uint8(0)
		switch command.op {
		case OutlineMoveTo, OutlineLineTo:
			expected = 1
		case OutlineQuadTo:
			expected = 2
		case OutlineClose:
			expected = 0
		default:
			return outlineSetInvariant(path + "/op")
		}
		if command.pointCount != expected {
			return outlineSetInvariant(path + "/arity")
		}
		if err := value.sink.u8(uint8(command.op), path+"/op"); err != nil {
			return err
		}
		if err := value.sink.u8(command.pointCount, path+"/point_count"); err != nil {
			return err
		}
		for pointIndex := uint8(0); pointIndex < command.pointCount; pointIndex++ {
			point := command.points[pointIndex]
			if err := value.sink.i64(int64(point.X), path+"/x"); err != nil {
				return err
			}
			if err := value.sink.i64(int64(point.Y), path+"/y"); err != nil {
				return err
			}
		}
	}
	value.nextGlyph++
	return nil
}

func (value *outlineSetHashWriter) finish(source *GeometryFont, commands outlineSetTopologyStats) (string, uint64, error) {
	if value == nil || value.finished || source != value.source || value.nextGlyph != value.glyphCount {
		return "", 0, outlineSetInvariant("outline_set/hash/finalize")
	}
	r := source.receipt
	footer := []uint64{
		r.ExpandedPointInstances, r.ExpandedContourInstances, commands.implied,
		commands.lines, commands.quads, commands.commands, commands.commandPoints,
	}
	for index, number := range footer {
		if err := value.sink.u64(number, fmt.Sprintf("outline_set/hash/footer/%d", index)); err != nil {
			return "", 0, err
		}
	}
	if err := contextFailure(value.sink.ctx, "outline_set/hash/finalize"); err != nil {
		return "", 0, err
	}
	value.finished = true
	return "sha256:" + hex.EncodeToString(value.sink.hash.Sum(nil)), value.sink.count, nil
}
