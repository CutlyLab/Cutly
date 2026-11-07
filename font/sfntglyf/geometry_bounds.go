package sfntglyf

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
)

type geometryGlobalMetrics struct {
	hasContours             bool
	glyphsWithContours      uint64
	headXMin, headYMin      int32
	headXMax, headYMax      int32
	minLSB, minRSB, xExtent int32
}

type geometryDeclarations struct {
	headFlags               uint16
	headXMin, headYMin      int16
	headXMax, headYMax      int16
	minLSB, minRSB, xExtent int16
}

func exactGeometryBBox(exact *geometryExactContext, geometry exactGlyphGeometry, glyphID uint32) (int16, int16, int16, int16, error) {
	path := fmt.Sprintf("geometry/bbox/%d", glyphID)
	if len(geometry.points) == 0 {
		if len(geometry.endPoints) != 0 {
			return 0, 0, 0, 0, fontError(CodeGeometryInvariant, path+"/empty_contours", ErrInvariantFailed)
		}
		return 0, 0, 0, 0, nil
	}
	if len(geometry.endPoints) == 0 {
		return 0, 0, 0, 0, fontError(CodeGeometryInvariant, path+"/missing_contours", ErrInvariantFailed)
	}
	minX, minY := geometry.points[0].x, geometry.points[0].y
	maxX, maxY := minX, minY
	for index := 1; index < len(geometry.points); index++ {
		point := geometry.points[index]
		comparison, err := exact.compare(point.x, minX, fmt.Sprintf("%s/%d/min_x", path, index))
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if comparison < 0 {
			minX = point.x
		}
		comparison, err = exact.compare(point.y, minY, fmt.Sprintf("%s/%d/min_y", path, index))
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if comparison < 0 {
			minY = point.y
		}
		comparison, err = exact.compare(point.x, maxX, fmt.Sprintf("%s/%d/max_x", path, index))
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if comparison > 0 {
			maxX = point.x
		}
		comparison, err = exact.compare(point.y, maxY, fmt.Sprintf("%s/%d/max_y", path, index))
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if comparison > 0 {
			maxY = point.y
		}
	}
	floorX, err := exact.floor(minX, path+"/floor_x")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	floorY, err := exact.floor(minY, path+"/floor_y")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	ceilX, err := exact.ceil(maxX, path+"/ceil_x")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	ceilY, err := exact.ceil(maxY, path+"/ceil_y")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	xMin, ok := geometryInt16(floorX)
	if !ok {
		return 0, 0, 0, 0, fontError(CodeGeometryInvalid, path+"/x_min_range", ErrGeometryInvalid)
	}
	yMin, ok := geometryInt16(floorY)
	if !ok {
		return 0, 0, 0, 0, fontError(CodeGeometryInvalid, path+"/y_min_range", ErrGeometryInvalid)
	}
	xMax, ok := geometryInt16(ceilX)
	if !ok {
		return 0, 0, 0, 0, fontError(CodeGeometryInvalid, path+"/x_max_range", ErrGeometryInvalid)
	}
	yMax, ok := geometryInt16(ceilY)
	if !ok {
		return 0, 0, 0, 0, fontError(CodeGeometryInvalid, path+"/y_max_range", ErrGeometryInvalid)
	}
	return xMin, yMin, xMax, yMax, nil
}

func geometryInt16(value *big.Int) (int16, bool) {
	if value == nil || !value.IsInt64() {
		return 0, false
	}
	number := value.Int64()
	if number < math.MinInt16 || number > math.MaxInt16 {
		return 0, false
	}
	return int16(number), true
}

func readGeometryDeclarations(source *OutlineFont) (geometryDeclarations, error) {
	if source == nil || source.font == nil {
		return geometryDeclarations{}, fontError(CodeGeometryInputInvalid, "geometry/tables/source", ErrGeometryInputInvalid)
	}
	headRecord, headOK := findRecord(source.font.tables, "head")
	hheaRecord, hheaOK := findRecord(source.font.tables, "hhea")
	if !headOK || !hheaOK {
		return geometryDeclarations{}, fontError(CodeGeometryInvariant, "geometry/tables/records", ErrInvariantFailed)
	}
	head := tableBytes(source.font.encoded, headRecord)
	hhea := tableBytes(source.font.encoded, hheaRecord)
	if len(head) != 54 || len(hhea) != 36 {
		return geometryDeclarations{}, fontError(CodeGeometryInvariant, "geometry/tables/length", ErrInvariantFailed)
	}
	return geometryDeclarations{
		headFlags: binary.BigEndian.Uint16(head[16:18]),
		headXMin:  int16(binary.BigEndian.Uint16(head[36:38])),
		headYMin:  int16(binary.BigEndian.Uint16(head[38:40])),
		headXMax:  int16(binary.BigEndian.Uint16(head[40:42])),
		headYMax:  int16(binary.BigEndian.Uint16(head[42:44])),
		minLSB:    int16(binary.BigEndian.Uint16(hhea[12:14])),
		minRSB:    int16(binary.BigEndian.Uint16(hhea[14:16])),
		xExtent:   int16(binary.BigEndian.Uint16(hhea[16:18])),
	}, nil
}

func (value *geometryGlobalMetrics) addGlyph(glyph geometryGlyph) error {
	if !glyph.hasContours {
		return nil
	}
	width := int64(glyph.xMax) - int64(glyph.xMin)
	rsb := int64(glyph.advanceWidth) - int64(glyph.leftSideBearing) - width
	extent := int64(glyph.leftSideBearing) + width
	if !value.hasContours {
		value.hasContours = true
		value.headXMin, value.headYMin = int32(glyph.xMin), int32(glyph.yMin)
		value.headXMax, value.headYMax = int32(glyph.xMax), int32(glyph.yMax)
		value.minLSB = int32(glyph.leftSideBearing)
		value.minRSB, value.xExtent = int32(rsb), int32(extent)
	} else {
		if int32(glyph.xMin) < value.headXMin {
			value.headXMin = int32(glyph.xMin)
		}
		if int32(glyph.yMin) < value.headYMin {
			value.headYMin = int32(glyph.yMin)
		}
		if int32(glyph.xMax) > value.headXMax {
			value.headXMax = int32(glyph.xMax)
		}
		if int32(glyph.yMax) > value.headYMax {
			value.headYMax = int32(glyph.yMax)
		}
		if int32(glyph.leftSideBearing) < value.minLSB {
			value.minLSB = int32(glyph.leftSideBearing)
		}
		if int32(rsb) < value.minRSB {
			value.minRSB = int32(rsb)
		}
		if int32(extent) > value.xExtent {
			value.xExtent = int32(extent)
		}
	}
	value.glyphsWithContours++
	return nil
}

func validateGeometryDeclarations(declared geometryDeclarations, computed geometryGlobalMetrics) error {
	if int32(declared.headXMin) != computed.headXMin || int32(declared.headYMin) != computed.headYMin ||
		int32(declared.headXMax) != computed.headXMax || int32(declared.headYMax) != computed.headYMax {
		return fontError(CodeGeometryInvalid, "geometry/head/bbox", ErrGeometryInvalid)
	}
	if computed.minLSB < math.MinInt16 || computed.minLSB > math.MaxInt16 ||
		computed.minRSB < math.MinInt16 || computed.minRSB > math.MaxInt16 ||
		computed.xExtent < math.MinInt16 || computed.xExtent > math.MaxInt16 {
		return fontError(CodeGeometryMetrics, "geometry/hhea/extrema_range", ErrGeometryMetrics)
	}
	if int32(declared.minLSB) != computed.minLSB || int32(declared.minRSB) != computed.minRSB || int32(declared.xExtent) != computed.xExtent {
		return fontError(CodeGeometryMetrics, "geometry/hhea/extrema", ErrGeometryMetrics)
	}
	return nil
}
