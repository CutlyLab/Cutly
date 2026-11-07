package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
)

type geometryHashSink struct {
	ctx   context.Context
	hash  hash.Hash
	count uint64
	limit uint64
	work  geometryCanonicalWorkCharger
}

type geometryCanonicalWorkCharger interface {
	chargeCanonicalBytes(bytes uint64, path string) error
}

func (value *geometryHashSink) raw(raw []byte, path string) error {
	next, ok := checkedAdd(value.count, uint64(len(raw)))
	if !ok || next > value.limit {
		return fontError(CodeGeometryResourceLimit, "geometry/hash/canonical_bytes", ErrResourceLimit)
	}
	if value.work != nil {
		if err := value.work.chargeCanonicalBytes(uint64(len(raw)), path); err != nil {
			return err
		}
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

func (value *geometryHashSink) str(text, path string) error {
	if err := value.u64(uint64(len(text)), path); err != nil {
		return err
	}
	return value.raw([]byte(text), path)
}

func (value *geometryHashSink) u8(number uint8, path string) error {
	return value.raw([]byte{number}, path)
}

func (value *geometryHashSink) u32(number uint32, path string) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *geometryHashSink) i32(number int32, path string) error {
	return value.u32(uint32(number), path)
}

func (value *geometryHashSink) u64(number uint64, path string) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *geometryHashSink) dyadic(number geometryDyadic, path string) error {
	if number.numerator == nil {
		return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
	}
	if number.isZero() {
		if number.shift != 0 {
			return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
		}
		if err := value.u8(0, path+"/sign"); err != nil {
			return err
		}
		if err := value.u32(0, path+"/shift"); err != nil {
			return err
		}
		return value.u64(0, path+"/magnitude_length")
	}
	if number.shift > 0 && number.numerator.Bit(0) == 0 {
		return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
	}
	magnitude := number.magnitudeBytes()
	if len(magnitude) == 0 || magnitude[0] == 0 {
		return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
	}
	if err := value.u8(number.signTag(), path+"/sign"); err != nil {
		return err
	}
	if err := value.u32(number.shift, path+"/shift"); err != nil {
		return err
	}
	if err := value.u64(uint64(len(magnitude)), path+"/magnitude_length"); err != nil {
		return err
	}
	return value.raw(magnitude, path+"/magnitude")
}

type geometryHashWriter struct {
	writer     geometryHashSink
	glyphCount uint32
	nextGlyph  uint32
	finished   bool
}

func newGeometryHashWriter(ctx context.Context, source *OutlineFont, glyphCount uint32, canonicalLimit uint64) (*geometryHashWriter, error) {
	return newGeometryHashWriterWithWork(ctx, source, glyphCount, canonicalLimit, nil)
}

func newGeometryHashWriterWithWork(ctx context.Context, source *OutlineFont, glyphCount uint32, canonicalLimit uint64, work geometryCanonicalWorkCharger) (*geometryHashWriter, error) {
	if ctx == nil {
		return nil, fontError(CodeContextInvalid, "geometry/hash/context", ErrContextInvalid)
	}
	if source == nil || source.font == nil || uint64(len(source.glyphs)) > uint64(^uint32(0)) || glyphCount != uint32(len(source.glyphs)) {
		return nil, fontError(CodeGeometryInvariant, "geometry/hash/source", ErrInvariantFailed)
	}
	state := &geometryHashWriter{
		writer:     geometryHashSink{ctx: ctx, hash: sha256.New(), limit: canonicalLimit, work: work},
		glyphCount: glyphCount,
	}
	writes := []func() error{
		func() error { return state.writer.str(GeometryHashProfileV1, "geometry/hash/profile") },
		func() error { return state.writer.u32(GeometryProfileSchemaVersion, "geometry/hash/schema") },
		func() error { return state.writer.str(GeometryProfileV1, "geometry/hash/geometry_profile") },
		func() error { return state.writer.str(GeometryDescriptorHash(), "geometry/hash/descriptor_hash") },
		func() error { return state.writer.str(source.receipt.Profile, "geometry/hash/source_profile") },
		func() error {
			return state.writer.str(source.receipt.DescriptorHash, "geometry/hash/source_descriptor_hash")
		},
		func() error { return state.writer.str(source.receipt.GraphHash, "geometry/hash/source_graph_hash") },
		func() error {
			return state.writer.str(source.receipt.UpstreamContentProfile, "geometry/hash/upstream_content_profile")
		},
		func() error {
			return state.writer.str(source.receipt.UpstreamContentHash, "geometry/hash/upstream_content_hash")
		},
		func() error { return state.writer.u32(state.glyphCount, "geometry/hash/glyph_count") },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (value *geometryHashWriter) writeGlyph(glyphID uint32, sourceGlyph outlineGlyph, exact exactGlyphGeometry, verified geometryGlyph) error {
	if value == nil || value.finished || value.writer.hash == nil || glyphID != value.nextGlyph || glyphID >= value.glyphCount {
		return fontError(CodeGeometryInvariant, "geometry/hash/glyph_order", ErrInvariantFailed)
	}
	path := fmt.Sprintf("geometry/hash/glyphs/%d", glyphID)
	if err := contextFailure(value.writer.ctx, path); err != nil {
		return err
	}
	if sourceGlyph.kind != verified.kind || uint64(len(exact.points)) != sourceGlyph.expandedPoints ||
		uint64(len(exact.endPoints)) != sourceGlyph.expandedContours || verified.hasContours != (len(exact.endPoints) > 0) ||
		verified.effectiveMetricSource >= value.glyphCount {
		return fontError(CodeGeometryInvariant, path+"/binding", ErrInvariantFailed)
	}
	var kind uint8
	switch sourceGlyph.kind {
	case OutlineGlyphEmptyRange:
		kind = 0
	case OutlineGlyphSimple:
		kind = 1
	case OutlineGlyphCompound:
		kind = 2
	default:
		return fontError(CodeGeometryInvariant, path+"/kind", ErrInvariantFailed)
	}
	hasContours := uint8(0)
	if verified.hasContours {
		hasContours = 1
	}
	writes := []func() error{
		func() error { return value.writer.u32(glyphID, path+"/gid") },
		func() error { return value.writer.u8(kind, path+"/kind") },
		func() error { return value.writer.u8(hasContours, path+"/has_contours") },
		func() error { return value.writer.u64(uint64(len(exact.endPoints)), path+"/contour_count") },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	for index, endPoint := range exact.endPoints {
		if (index > 0 && endPoint <= exact.endPoints[index-1]) || uint64(endPoint) >= uint64(len(exact.points)) {
			return fontError(CodeGeometryInvariant, path+"/end_points", ErrInvariantFailed)
		}
		if err := value.writer.u32(endPoint, path+"/end_points"); err != nil {
			return err
		}
	}
	if len(exact.endPoints) > 0 && exact.endPoints[len(exact.endPoints)-1] != uint32(len(exact.points)-1) {
		return fontError(CodeGeometryInvariant, path+"/end_points/final", ErrInvariantFailed)
	}
	if err := value.writer.u64(uint64(len(exact.points)), path+"/point_count"); err != nil {
		return err
	}
	for pointIndex, point := range exact.points {
		pointPath := fmt.Sprintf("%s/points/%d", path, pointIndex)
		if err := value.writer.dyadic(point.x, pointPath+"/x"); err != nil {
			return err
		}
		if err := value.writer.dyadic(point.y, pointPath+"/y"); err != nil {
			return err
		}
		onCurve := uint8(0)
		if point.onCurve {
			onCurve = 1
		}
		if err := value.writer.u8(onCurve, pointPath+"/on_curve"); err != nil {
			return err
		}
	}
	metadata := []func() error{
		func() error { return value.writer.i32(int32(verified.xMin), path+"/x_min") },
		func() error { return value.writer.i32(int32(verified.yMin), path+"/y_min") },
		func() error { return value.writer.i32(int32(verified.xMax), path+"/x_max") },
		func() error { return value.writer.i32(int32(verified.yMax), path+"/y_max") },
		func() error { return value.writer.u32(uint32(verified.advanceWidth), path+"/advance") },
		func() error { return value.writer.i32(int32(verified.leftSideBearing), path+"/lsb") },
		func() error { return value.writer.u32(verified.effectiveMetricSource, path+"/effective_metric_source") },
	}
	for _, write := range metadata {
		if err := write(); err != nil {
			return err
		}
	}
	value.nextGlyph++
	return nil
}

func (value *geometryHashWriter) finish(headXMin, headYMin, headXMax, headYMax, minLSB, minRSB, xExtent int32, headLSBStatus string) (string, uint64, error) {
	if value == nil || value.finished || value.writer.hash == nil || value.nextGlyph != value.glyphCount {
		return "", 0, fontError(CodeGeometryInvariant, "geometry/hash/finalize", ErrInvariantFailed)
	}
	writes := []func() error{
		func() error { return value.writer.i32(headXMin, "geometry/hash/head/x_min") },
		func() error { return value.writer.i32(headYMin, "geometry/hash/head/y_min") },
		func() error { return value.writer.i32(headXMax, "geometry/hash/head/x_max") },
		func() error { return value.writer.i32(headYMax, "geometry/hash/head/y_max") },
		func() error { return value.writer.i32(minLSB, "geometry/hash/hhea/min_lsb") },
		func() error { return value.writer.i32(minRSB, "geometry/hash/hhea/min_rsb") },
		func() error { return value.writer.i32(xExtent, "geometry/hash/hhea/x_max_extent") },
		func() error { return value.writer.str(headLSBStatus, "geometry/hash/head/lsb_status") },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return "", 0, err
		}
	}
	if err := contextFailure(value.writer.ctx, "geometry/hash/finalize"); err != nil {
		return "", 0, err
	}
	value.finished = true
	return "sha256:" + hex.EncodeToString(value.writer.hash.Sum(nil)), value.writer.count, nil
}
