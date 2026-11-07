package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
)

type outlineHashWriter struct {
	ctx   context.Context
	hash  hash.Hash
	count uint64
	limit uint64
}

func (value *outlineHashWriter) raw(raw []byte, path string) error {
	next, ok := checkedAdd(value.count, uint64(len(raw)))
	if !ok || next > value.limit {
		return fontError(CodeOutlineResourceLimit, "outline/hash/canonical_bytes", ErrResourceLimit)
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

func (value *outlineHashWriter) str(text, path string) error {
	if err := value.u64(uint64(len(text)), path); err != nil {
		return err
	}
	return value.raw([]byte(text), path)
}

func (value *outlineHashWriter) u8(number uint8, path string) error {
	return value.raw([]byte{number}, path)
}

func (value *outlineHashWriter) u16(number uint16, path string) error {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *outlineHashWriter) u32(number uint32, path string) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *outlineHashWriter) i32(number int32, path string) error {
	return value.u32(uint32(number), path)
}

func (value *outlineHashWriter) u64(number uint64, path string) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], number)
	return value.raw(encoded[:], path)
}

func hashOutlineGraph(ctx context.Context, font *Font, glyphs []outlineGlyph, canonicalLimit uint64) (string, uint64, error) {
	glyfRecord, ok := findRecord(font.tables, "glyf")
	if !ok {
		return "", 0, fontError(CodeOutlineInvariantFailed, "outline/hash/glyf", ErrInvariantFailed)
	}
	writer := outlineHashWriter{ctx: ctx, hash: sha256.New(), limit: canonicalLimit}
	header := []func() error{
		func() error { return writer.str(OutlineGraphHashProfileV1, "outline/hash/profile") },
		func() error { return writer.u32(OutlineProfileSchemaVersion, "outline/hash/schema") },
		func() error { return writer.str(OutlineProfileV1, "outline/hash/outline_profile") },
		func() error { return writer.str(font.receipt.ContentHashProfile, "outline/hash/upstream_profile") },
		func() error { return writer.str(font.receipt.ContentHash, "outline/hash/upstream_hash") },
		func() error { return writer.u32(uint32(len(glyphs)), "outline/hash/glyph_count") },
		func() error { return writer.u64(uint64(glyfRecord.length), "outline/hash/glyf_bytes") },
	}
	for _, write := range header {
		if err := write(); err != nil {
			return "", 0, err
		}
	}
	for glyphID, glyph := range glyphs {
		if glyphID%256 == 0 {
			if err := contextFailure(ctx, "outline/hash/glyphs"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("outline/hash/glyphs/%d", glyphID)
		var kind uint8
		switch glyph.kind {
		case OutlineGlyphEmptyRange:
			kind = 0
		case OutlineGlyphSimple:
			kind = 1
		case OutlineGlyphCompound:
			kind = 2
		default:
			return "", 0, fontError(CodeOutlineInvariantFailed, path+"/kind", ErrInvariantFailed)
		}
		fields := []func() error{
			func() error { return writer.u32(uint32(glyphID), path) },
			func() error { return writer.u8(kind, path) },
			func() error { return writer.u32(glyph.sourceBytes, path) },
			func() error { return writer.i32(int32(glyph.numberOfContours), path) },
			func() error { return writer.i32(int32(glyph.xMin), path) },
			func() error { return writer.i32(int32(glyph.yMin), path) },
			func() error { return writer.i32(int32(glyph.xMax), path) },
			func() error { return writer.i32(int32(glyph.yMax), path) },
			func() error { return writer.u32(uint32(len(glyph.instructions)), path) },
			func() error { return writer.raw(glyph.instructions, path+"/instructions") },
			func() error { return writer.u8(glyph.paddingBytes, path) },
		}
		for _, write := range fields {
			if err := write(); err != nil {
				return "", 0, err
			}
		}
		switch glyph.kind {
		case OutlineGlyphSimple:
			if err := writer.u64(uint64(len(glyph.endPoints)), path); err != nil {
				return "", 0, err
			}
			for _, endPoint := range glyph.endPoints {
				if err := writer.u32(endPoint, path); err != nil {
					return "", 0, err
				}
			}
			if err := writer.u64(uint64(len(glyph.points)), path); err != nil {
				return "", 0, err
			}
			for _, point := range glyph.points {
				if err := writer.i32(int32(point.x), path); err != nil {
					return "", 0, err
				}
				if err := writer.i32(int32(point.y), path); err != nil {
					return "", 0, err
				}
				onCurve := uint8(0)
				if point.onCurve {
					onCurve = 1
				}
				if err := writer.u8(onCurve, path); err != nil {
					return "", 0, err
				}
			}
			overlap := uint8(0)
			if glyph.overlapDeclared {
				overlap = 1
			}
			if err := writer.u8(overlap, path); err != nil {
				return "", 0, err
			}
		case OutlineGlyphCompound:
			if err := writer.u64(uint64(len(glyph.components)), path); err != nil {
				return "", 0, err
			}
			for _, component := range glyph.components {
				componentFields := []func() error{
					func() error { return writer.u16(component.flags, path) },
					func() error { return writer.u32(component.glyphID, path) },
					func() error { return writer.u8(component.argumentMode, path) },
					func() error { return writer.i32(component.arg1, path) },
					func() error { return writer.i32(component.arg2, path) },
				}
				for _, write := range componentFields {
					if err := write(); err != nil {
						return "", 0, err
					}
				}
				for _, coefficient := range component.matrix {
					if err := writer.i32(int32(coefficient), path); err != nil {
						return "", 0, err
					}
				}
			}
			if err := writer.u64(glyph.expandedPoints, path); err != nil {
				return "", 0, err
			}
			if err := writer.u64(glyph.expandedContours, path); err != nil {
				return "", 0, err
			}
			if err := writer.u64(glyph.expandedEdges, path); err != nil {
				return "", 0, err
			}
			if err := writer.u32(glyph.componentDepth, path); err != nil {
				return "", 0, err
			}
		}
	}
	if err := contextFailure(ctx, "outline/hash/finalize"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(writer.hash.Sum(nil)), writer.count, nil
}
