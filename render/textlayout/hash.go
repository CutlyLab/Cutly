package textlayout

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
)

// canonicalWriter streams one normative binary preimage into SHA-256. It deliberately never
// materializes the preimage: count is the conceptual byte size charged in Receipt.
type canonicalWriter struct {
	ctx        context.Context
	path       string
	digest     hash.Hash
	count      uint64
	operations uint64
	err        error
	buffer     [8]byte
}

func newCanonicalWriter(ctx context.Context, path, profile string) *canonicalWriter {
	value := &canonicalWriter{ctx: ctx, path: path, digest: sha256.New()}
	value.string(profile)
	return value
}

func (value *canonicalWriter) check() {
	if value.err != nil {
		return
	}
	value.operations++
	if value.operations&1023 == 0 {
		value.err = contextFailure(value.ctx, value.path)
	}
}

func (value *canonicalWriter) addCount(count uint64) bool {
	if value.err != nil {
		return false
	}
	next, ok := checkedAdd(value.count, count)
	if !ok {
		value.err = layoutError(CodeResourceLimit, value.path, ErrResourceLimit)
		return false
	}
	value.count = next
	return true
}

func (value *canonicalWriter) raw(bytes []byte) {
	value.check()
	if !value.addCount(uint64(len(bytes))) {
		return
	}
	_, _ = value.digest.Write(bytes)
}

func (value *canonicalWriter) string(text string) {
	value.u64(uint64(len(text)))
	if value.err != nil || !value.addCount(uint64(len(text))) {
		return
	}
	const chunkBytes = 64 << 10
	for offset := 0; offset < len(text); offset += chunkBytes {
		value.check()
		if value.err != nil {
			return
		}
		end := offset + chunkBytes
		if end > len(text) {
			end = len(text)
		}
		_, _ = io.WriteString(value.digest, text[offset:end])
	}
}

func (value *canonicalWriter) u8(number uint8) {
	value.buffer[0] = number
	value.raw(value.buffer[:1])
}

func (value *canonicalWriter) u32(number uint32) {
	binary.BigEndian.PutUint32(value.buffer[:4], number)
	value.raw(value.buffer[:4])
}

func (value *canonicalWriter) u64(number uint64) {
	binary.BigEndian.PutUint64(value.buffer[:], number)
	value.raw(value.buffer[:])
}

func (value *canonicalWriter) i32(number int32) { value.u32(uint32(number)) }
func (value *canonicalWriter) i64(number int64) { value.u64(uint64(number)) }

func (value *canonicalWriter) finish() (string, uint64, error) {
	if value.err == nil {
		value.err = contextFailure(value.ctx, value.path)
	}
	if value.err != nil {
		return "", 0, value.err
	}
	return "sha256:" + hex.EncodeToString(value.digest.Sum(nil)), value.count, nil
}

func writeBindingCanonical(writer *canonicalWriter, binding Binding) {
	writer.string(binding.TextSemanticHashProfile)
	writer.string(binding.TextSemanticHash)
	writer.string(binding.LayoutInputHashProfile)
	writer.string(binding.LayoutInputHash)
	writer.string(binding.FontContentHash)
	writer.string(binding.UnicodeTableProfile)
	writer.string(binding.UnicodeTableHashProfile)
	writer.string(binding.UnicodeTableHash)
	writer.string(binding.ShapeProviderProfile)
	writer.string(binding.ShapeProviderFingerprint)
}

func hashGlyphStream(ctx context.Context, input Input) (string, uint64, error) {
	writer := newCanonicalWriter(ctx, "hash/glyph_stream", GlyphStreamHashProfileV1)
	writer.u32(ProfileSchemaVersion)
	writer.string(input.LayoutProfile)
	writer.string(input.TextProfile)
	writer.string(input.RunInputProfile)
	writer.string(input.ShapedStreamProfile)
	writer.string(input.FontProfile)
	writer.string(input.OutlineProfile)
	writeBindingCanonical(writer, input.Binding)
	writer.string(input.Text)
	writer.string(input.Language)
	writer.string(input.Script)
	writer.string(string(input.Direction))
	writer.u32(input.UnitsPerEm)
	writer.u64(uint64(len(input.Lines)))
	for lineIndex, line := range input.Lines {
		if lineIndex&255 == 0 {
			writer.check()
		}
		writer.u64(line.ByteStart)
		writer.u64(line.ByteEnd)
		writer.u64(uint64(len(line.Glyphs)))
		for _, glyph := range line.Glyphs {
			writer.u32(glyph.GlyphID)
			writer.u64(glyph.ClusterByteOffset)
			writer.i64(int64(glyph.XAdvance))
			writer.i64(int64(glyph.YAdvance))
			writer.i64(int64(glyph.XOffset))
			writer.i64(int64(glyph.YOffset))
		}
	}
	return writer.finish()
}

func hashScaledOutlineSet(ctx context.Context, exact exactContext, input Input) (string, uint64, error) {
	writer := newCanonicalWriter(ctx, "hash/scaled_outlines", OutlineSetHashProfileV1)
	writer.u32(ProfileSchemaVersion)
	writer.string(input.LayoutProfile)
	writer.string(input.FontProfile)
	writer.string(input.OutlineProfile)
	writer.string(input.Binding.FontContentHash)
	writer.i64(input.FontSize.Numerator)
	writer.i64(input.FontSize.Denominator)
	writer.u32(input.UnitsPerEm)
	writer.u64(uint64(len(input.Outlines)))
	for outlineIndex, outline := range input.Outlines {
		if outlineIndex&255 == 0 {
			writer.check()
		}
		writer.u32(outline.GlyphID)
		writer.u64(uint64(len(outline.Commands)))
		for commandIndex, command := range outline.Commands {
			writer.u8(outlineOpTag(command.Op))
			writer.u64(uint64(len(command.Points)))
			for pointIndex, point := range command.Points {
				path := outlinePointPath(outlineIndex, commandIndex, pointIndex)
				x, err := exact.scaleQ(point.X, input.FontSize, input.UnitsPerEm, path+"/x")
				if err != nil {
					return "", 0, err
				}
				y, err := exact.scaleQ(point.Y, input.FontSize, input.UnitsPerEm, path+"/y")
				if err != nil {
					return "", 0, err
				}
				writer.i64(int64(x))
				writer.i64(int64(y))
			}
		}
	}
	return writer.finish()
}

func hashPlacementStream(
	ctx context.Context,
	exact exactContext,
	input Input,
	glyphStreamHash string,
) (string, uint64, error) {
	writer := newCanonicalWriter(ctx, "hash/placements", PlacementHashProfileV1)
	writer.u32(ProfileSchemaVersion)
	writer.string(input.LayoutProfile)
	writer.string(glyphStreamHash)
	writer.u32(input.Viewport.WidthPixels)
	writer.u32(input.Viewport.HeightPixels)
	writer.i64(input.FontSize.Numerator)
	writer.i64(input.FontSize.Denominator)
	writer.i64(input.LineHeight.Numerator)
	writer.i64(input.LineHeight.Denominator)
	writer.u32(input.UnitsPerEm)
	writer.i32(input.HheaAscender)
	writer.string(string(input.Direction))
	writer.string(string(input.Alignment))
	writer.u64(uint64(len(input.Lines)))
	for lineIndex, line := range input.Lines {
		geometry, err := computeLineGeometry(ctx, exact, input, line, lineIndex)
		if err != nil {
			return "", 0, err
		}
		writer.u32(uint32(lineIndex))
		writer.u64(line.ByteStart)
		writer.u64(line.ByteEnd)
		writer.i64(int64(geometry.baseline))
		writer.i64(int64(geometry.advance))
		writer.i64(int64(geometry.initialPen))
		writer.u64(uint64(len(line.Glyphs)))
		pen := geometry.initialPen
		for glyphIndex, glyph := range line.Glyphs {
			placement, advance, placementErr := placeGlyph(exact, input, lineIndex, glyphIndex, glyph, pen, geometry.baseline)
			if placementErr != nil {
				return "", 0, placementErr
			}
			writePlacementCanonical(writer, placement)
			pen, err = addQ(pen, advance, glyphPath(lineIndex, glyphIndex)+"/pen")
			if err != nil {
				return "", 0, err
			}
		}
		wantEnd, err := addQ(geometry.initialPen, geometry.advance, linePath(lineIndex)+"/end_pen")
		if err != nil || pen != wantEnd {
			if err != nil {
				return "", 0, err
			}
			return "", 0, layoutError(CodeInvariantFailed, linePath(lineIndex)+"/end_pen", ErrInvariantFailed)
		}
	}
	return writer.finish()
}

func writePlacementCanonical(writer *canonicalWriter, placement GlyphPlacement) {
	writer.u32(placement.LineIndex)
	writer.u64(placement.GlyphIndex)
	writer.u32(placement.GlyphID)
	writer.u64(placement.ClusterByteOffset)
	writer.i64(int64(placement.OriginX))
	writer.i64(int64(placement.BaselineY))
}

func hashLayoutBinding(ctx context.Context, input Input, receipt Receipt) (string, uint64, error) {
	writer := newCanonicalWriter(ctx, "hash/layout", LayoutHashProfileV1)
	writer.u32(ProfileSchemaVersion)
	writer.u32(uint32(receipt.SchemaVersion))
	writer.string(receipt.Profile)
	writer.string(receipt.LayoutProfile)
	writer.string(receipt.TextProfile)
	writer.string(receipt.RunInputProfile)
	writer.string(receipt.ShapedStreamProfile)
	writer.string(receipt.FontProfile)
	writer.string(receipt.OutlineProfile)
	writeBindingCanonical(writer, input.Binding)
	writer.string(receipt.GlyphStreamHashProfile)
	writer.string(receipt.GlyphStreamHash)
	writer.string(receipt.OutlineSetHashProfile)
	writer.string(receipt.OutlineSetHash)
	writer.string(receipt.PlacementHashProfile)
	writer.string(receipt.PlacementHash)
	writer.string(receipt.LayoutHashProfile)
	writer.u32(receipt.WidthPixels)
	writer.u32(receipt.HeightPixels)
	writer.i64(input.FontSize.Numerator)
	writer.i64(input.FontSize.Denominator)
	writer.i64(input.LineHeight.Numerator)
	writer.i64(input.LineHeight.Denominator)
	writer.string(input.Language)
	writer.string(input.Script)
	writer.string(string(input.Direction))
	writer.string(string(input.Alignment))
	writer.u32(input.UnitsPerEm)
	writer.i32(input.HheaAscender)
	writer.u64(receipt.LineCount)
	writer.u64(receipt.GlyphCount)
	writer.u64(receipt.UniqueGlyphCount)
	writer.u64(receipt.ContourCount)
	writer.u64(receipt.CommandCount)
	writer.u64(receipt.CanonicalBinaryBytes)
	writer.u64(receipt.WorkUnits)
	writer.u64(receipt.WorkingBytes)
	writer.u64(receipt.RetainedBytes)
	return writer.finish()
}

func outlineOpTag(operation OutlineCommandOp) uint8 {
	switch operation {
	case MoveTo:
		return 1
	case LineTo:
		return 2
	case QuadTo:
		return 3
	case Close:
		return 4
	default:
		return 0
	}
}

func outlineOpFromTag(tag uint8) OutlineCommandOp {
	switch tag {
	case 1:
		return MoveTo
	case 2:
		return LineTo
	case 3:
		return QuadTo
	case 4:
		return Close
	default:
		return ""
	}
}
