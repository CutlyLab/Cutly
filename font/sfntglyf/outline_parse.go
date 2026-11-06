package sfntglyf

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
)

const (
	simpleOnCurve       byte = 0x01
	simpleXShort        byte = 0x02
	simpleYShort        byte = 0x04
	simpleRepeat        byte = 0x08
	simpleXSamePositive byte = 0x10
	simpleYSamePositive byte = 0x20
	simpleOverlap       byte = 0x40
	simpleReserved      byte = 0x80

	componentArgWords       uint16 = 0x0001
	componentArgsXY         uint16 = 0x0002
	componentRoundXY        uint16 = 0x0004
	componentScale          uint16 = 0x0008
	componentMore           uint16 = 0x0020
	componentXYScale        uint16 = 0x0040
	componentTwoByTwo       uint16 = 0x0080
	componentInstructions   uint16 = 0x0100
	componentUseMyMetrics   uint16 = 0x0200
	componentOverlap        uint16 = 0x0400
	componentScaledOffset   uint16 = 0x0800
	componentUnscaledOffset uint16 = 0x1000
	componentReserved       uint16 = 0xe010
)

type outlineCursor struct {
	data []byte
	pos  int
}

func (value *outlineCursor) remaining() int { return len(value.data) - value.pos }

func (value *outlineCursor) take(length int) ([]byte, bool) {
	if length < 0 || value.pos < 0 || value.pos > len(value.data) || length > len(value.data)-value.pos {
		return nil, false
	}
	result := value.data[value.pos : value.pos+length]
	value.pos += length
	return result, true
}

func (value *outlineCursor) u8() (byte, bool) {
	raw, ok := value.take(1)
	if !ok {
		return 0, false
	}
	return raw[0], true
}

func (value *outlineCursor) u16() (uint16, bool) {
	raw, ok := value.take(2)
	if !ok {
		return 0, false
	}
	return binary.BigEndian.Uint16(raw), true
}

func scanOutlineGlyph(ctx context.Context, body []byte, glyphID, numGlyphs uint32, declared outlineDeclaredMaxp, resources outlineResourceContext, stats *outlineScanStats) (result outlineGlyph, err error) {
	defer func() {
		if recover() != nil {
			result = outlineGlyph{}
			err = fontError(CodeAllocationFailed, fmt.Sprintf("outline/glyphs/%d/allocation", glyphID), ErrAllocationFailed)
		}
	}()
	if len(body) == 0 {
		stats.emptyRangeGlyphs++
		return outlineGlyph{kind: OutlineGlyphEmptyRange}, nil
	}
	if len(body) < 10 || uint64(len(body)) > uint64(math.MaxUint32) {
		return outlineGlyph{}, outlineGlyphError(glyphID, "header")
	}
	if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/header", glyphID)); err != nil {
		return outlineGlyph{}, err
	}
	contours := int16(binary.BigEndian.Uint16(body[0:2]))
	base := outlineGlyph{
		sourceBytes: uint32(len(body)), numberOfContours: contours,
		xMin: int16(binary.BigEndian.Uint16(body[2:4])), yMin: int16(binary.BigEndian.Uint16(body[4:6])),
		xMax: int16(binary.BigEndian.Uint16(body[6:8])), yMax: int16(binary.BigEndian.Uint16(body[8:10])),
	}
	if base.xMin > base.xMax || base.yMin > base.yMax {
		return outlineGlyph{}, outlineGlyphError(glyphID, "bbox")
	}
	switch {
	case contours >= 0:
		return scanSimpleOutlineGlyph(ctx, body, glyphID, base, declared, resources, stats)
	case contours == -1:
		return scanCompoundOutlineGlyph(ctx, body, glyphID, numGlyphs, base, declared, resources, stats)
	default:
		return outlineGlyph{}, outlineGlyphError(glyphID, "number_of_contours")
	}
}

func scanSimpleOutlineGlyph(ctx context.Context, body []byte, glyphID uint32, glyph outlineGlyph, declared outlineDeclaredMaxp, resources outlineResourceContext, stats *outlineScanStats) (outlineGlyph, error) {
	limits := resources.limits
	glyph.kind = OutlineGlyphSimple
	cursor := outlineCursor{data: body, pos: 10}
	contourCount := uint64(uint16(glyph.numberOfContours))
	if contourCount > uint64(declared.maxContours) {
		return outlineGlyph{}, fontError(CodeOutlineMaxpMismatch, fmt.Sprintf("outline/maxp/glyphs/%d/max_contours", glyphID), ErrOutlineMaxpMismatch)
	}
	if contourCount > limits.MaxRawContours-stats.rawContours {
		return outlineGlyph{}, outlineResourceError(glyphID, "raw_contours")
	}
	projected := *stats
	projected.rawContours += contourCount
	if uint32(contourCount) > projected.maxContours {
		projected.maxContours = uint32(contourCount)
	}
	if err := resources.check(projected, fmt.Sprintf("outline/glyphs/%d/resources/contours", glyphID)); err != nil {
		return outlineGlyph{}, err
	}
	glyph.endPoints = make([]uint32, int(contourCount))
	var pointCount uint64
	for index := uint64(0); index < contourCount; index++ {
		if index%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/end_points/%d", glyphID, index)); err != nil {
				return outlineGlyph{}, err
			}
		}
		value, ok := cursor.u16()
		if !ok {
			return outlineGlyph{}, outlineGlyphError(glyphID, "end_points")
		}
		if index > 0 && uint32(value) <= glyph.endPoints[index-1] {
			return outlineGlyph{}, outlineGlyphError(glyphID, "end_points/order")
		}
		if value == math.MaxUint16 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "point_count")
		}
		glyph.endPoints[index] = uint32(value)
		pointCount = uint64(value) + 1
	}
	if pointCount > limits.MaxRawPoints-stats.rawPoints || pointCount > uint64(maxInt()) {
		return outlineGlyph{}, outlineResourceError(glyphID, "raw_points")
	}
	if pointCount > uint64(declared.maxPoints) {
		return outlineGlyph{}, fontError(CodeOutlineMaxpMismatch, fmt.Sprintf("outline/maxp/glyphs/%d/max_points", glyphID), ErrOutlineMaxpMismatch)
	}
	if contourCount == 0 && len(body) == 10 {
		if glyph.xMin != 0 || glyph.yMin != 0 || glyph.xMax != 0 || glyph.yMax != 0 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "bbox/empty")
		}
		updateSimpleStats(glyph, stats)
		if err := resources.check(*stats, fmt.Sprintf("outline/glyphs/%d/resources", glyphID)); err != nil {
			return outlineGlyph{}, err
		}
		return glyph, nil
	}
	instructionLength, ok := cursor.u16()
	if !ok {
		return outlineGlyph{}, outlineGlyphError(glyphID, "instructions/length")
	}
	if uint32(instructionLength) > declared.maxInstructionBytes {
		return outlineGlyph{}, fontError(CodeOutlineMaxpMismatch, fmt.Sprintf("outline/maxp/glyphs/%d/max_size_of_instructions", glyphID), ErrOutlineMaxpMismatch)
	}
	instructions, ok := cursor.take(int(instructionLength))
	if !ok {
		return outlineGlyph{}, outlineGlyphError(glyphID, "instructions")
	}
	if uint64(instructionLength) > limits.MaxInstructionBytes-stats.instructionBytes {
		return outlineGlyph{}, outlineResourceError(glyphID, "instruction_bytes")
	}
	glyph.instructions = instructions
	projected = *stats
	projected.rawContours += contourCount
	projected.rawPoints += pointCount
	projected.instructionBytes += uint64(instructionLength)
	if uint32(contourCount) > projected.maxContours {
		projected.maxContours = uint32(contourCount)
	}
	if uint32(pointCount) > projected.maxPoints {
		projected.maxPoints = uint32(pointCount)
	}
	if uint32(instructionLength) > projected.maxInstructionBytes {
		projected.maxInstructionBytes = uint32(instructionLength)
	}
	if err := resources.check(projected, fmt.Sprintf("outline/glyphs/%d/resources/points", glyphID)); err != nil {
		return outlineGlyph{}, err
	}
	flags := make([]byte, int(pointCount))
	for logical, physical := 0, 0; logical < len(flags); physical++ {
		if physical%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/flags/%d", glyphID, physical)); err != nil {
				return outlineGlyph{}, err
			}
		}
		flag, ok := cursor.u8()
		if !ok || flag&simpleReserved != 0 || flag&simpleOverlap != 0 && physical != 0 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "flags")
		}
		if flag&simpleOverlap != 0 {
			glyph.overlapDeclared = true
		}
		repeats := 0
		if flag&simpleRepeat != 0 {
			count, ok := cursor.u8()
			if !ok || count == 0 {
				return outlineGlyph{}, outlineGlyphError(glyphID, "flags/repeat")
			}
			repeats = int(count)
		}
		covered := repeats + 1
		if covered > len(flags)-logical {
			return outlineGlyph{}, outlineGlyphError(glyphID, "flags/repeat")
		}
		semantic := flag &^ (simpleRepeat | simpleOverlap)
		for repeat := 0; repeat < covered; repeat++ {
			flags[logical] = semantic
			logical++
		}
	}
	glyph.points = make([]outlineRawPoint, int(pointCount))
	var x int64
	for index, flag := range flags {
		if index%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/coordinates/x/%d", glyphID, index)); err != nil {
				return outlineGlyph{}, err
			}
		}
		delta, ok := readOutlineDelta(&cursor, flag, simpleXShort, simpleXSamePositive)
		if !ok {
			return outlineGlyph{}, outlineGlyphError(glyphID, "coordinates/x")
		}
		x += delta
		if x < math.MinInt16 || x > math.MaxInt16 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "coordinates/x/range")
		}
		glyph.points[index].x = int16(x)
		glyph.points[index].onCurve = flag&simpleOnCurve != 0
	}
	var y int64
	for index, flag := range flags {
		if index%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/coordinates/y/%d", glyphID, index)); err != nil {
				return outlineGlyph{}, err
			}
		}
		delta, ok := readOutlineDelta(&cursor, flag, simpleYShort, simpleYSamePositive)
		if !ok {
			return outlineGlyph{}, outlineGlyphError(glyphID, "coordinates/y")
		}
		y += delta
		if y < math.MinInt16 || y > math.MaxInt16 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "coordinates/y/range")
		}
		glyph.points[index].y = int16(y)
	}
	padding, err := consumeOutlinePadding(&cursor, glyphID)
	if err != nil {
		return outlineGlyph{}, err
	}
	glyph.paddingBytes = padding
	if err := validateSimpleOutlineBBox(ctx, glyphID, glyph); err != nil {
		return outlineGlyph{}, err
	}
	updateSimpleStats(glyph, stats)
	if err := resources.check(*stats, fmt.Sprintf("outline/glyphs/%d/resources", glyphID)); err != nil {
		return outlineGlyph{}, err
	}
	return glyph, nil
}

func readOutlineDelta(cursor *outlineCursor, flag, shortMask, sameMask byte) (int64, bool) {
	if flag&shortMask != 0 {
		value, ok := cursor.u8()
		if !ok {
			return 0, false
		}
		if flag&sameMask != 0 {
			return int64(value), true
		}
		return -int64(value), true
	}
	if flag&sameMask != 0 {
		return 0, true
	}
	value, ok := cursor.u16()
	if !ok {
		return 0, false
	}
	return int64(int16(value)), true
}

func validateSimpleOutlineBBox(ctx context.Context, glyphID uint32, glyph outlineGlyph) error {
	if len(glyph.points) == 0 {
		if glyph.xMin != 0 || glyph.yMin != 0 || glyph.xMax != 0 || glyph.yMax != 0 {
			return outlineGlyphError(glyphID, "bbox/empty")
		}
		return nil
	}
	xMin, xMax := glyph.points[0].x, glyph.points[0].x
	yMin, yMax := glyph.points[0].y, glyph.points[0].y
	for index, point := range glyph.points[1:] {
		if index%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/bbox/%d", glyphID, index)); err != nil {
				return err
			}
		}
		if point.x < xMin {
			xMin = point.x
		}
		if point.x > xMax {
			xMax = point.x
		}
		if point.y < yMin {
			yMin = point.y
		}
		if point.y > yMax {
			yMax = point.y
		}
	}
	if glyph.xMin != xMin || glyph.yMin != yMin || glyph.xMax != xMax || glyph.yMax != yMax {
		return outlineGlyphError(glyphID, "bbox")
	}
	return nil
}

func scanCompoundOutlineGlyph(ctx context.Context, body []byte, glyphID, numGlyphs uint32, glyph outlineGlyph, declared outlineDeclaredMaxp, resources outlineResourceContext, stats *outlineScanStats) (outlineGlyph, error) {
	limits := resources.limits
	glyph.kind = OutlineGlyphCompound
	cursor := outlineCursor{data: body, pos: 10}
	var finalFlags uint16
	for componentIndex := 0; ; componentIndex++ {
		if componentIndex%256 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d/components/%d", glyphID, componentIndex)); err != nil {
				return outlineGlyph{}, err
			}
		}
		if uint64(componentIndex) >= limits.MaxComponentsPerGlyph || stats.componentRecords >= limits.MaxComponentRecords {
			return outlineGlyph{}, outlineResourceError(glyphID, "components")
		}
		if uint32(componentIndex)+1 > declared.maxComponentElements {
			return outlineGlyph{}, fontError(CodeOutlineMaxpMismatch, fmt.Sprintf("outline/maxp/glyphs/%d/max_component_elements", glyphID), ErrOutlineMaxpMismatch)
		}
		flags, ok := cursor.u16()
		if !ok {
			return outlineGlyph{}, outlineGlyphError(glyphID, "components/flags")
		}
		child, ok := cursor.u16()
		if !ok || uint32(child) >= numGlyphs {
			return outlineGlyph{}, outlineGlyphError(glyphID, "components/glyph_id")
		}
		if flags&componentReserved != 0 || flags&componentRoundXY != 0 ||
			bits.OnesCount16(flags&(componentScale|componentXYScale|componentTwoByTwo)) > 1 ||
			flags&componentScaledOffset != 0 && flags&componentUnscaledOffset != 0 ||
			flags&componentInstructions != 0 && flags&componentMore != 0 ||
			flags&componentOverlap != 0 && componentIndex != 0 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "components/flags")
		}
		argsXY := flags&componentArgsXY != 0
		if componentIndex == 0 && !argsXY {
			return outlineGlyph{}, outlineGlyphError(glyphID, "components/first_placement")
		}
		if !argsXY && flags&(componentScaledOffset|componentUnscaledOffset) != 0 {
			return outlineGlyph{}, outlineGlyphError(glyphID, "components/point_flags")
		}
		component := outlineComponent{flags: flags, glyphID: uint32(child), matrix: [4]int16{0x4000, 0, 0, 0x4000}}
		if argsXY {
			component.argumentMode = outlineArgumentXY
			if flags&componentArgWords != 0 {
				first, firstOK := cursor.u16()
				second, secondOK := cursor.u16()
				if !firstOK || !secondOK {
					return outlineGlyph{}, outlineGlyphError(glyphID, "components/arguments")
				}
				component.arg1, component.arg2 = int32(int16(first)), int32(int16(second))
			} else {
				first, firstOK := cursor.u8()
				second, secondOK := cursor.u8()
				if !firstOK || !secondOK {
					return outlineGlyph{}, outlineGlyphError(glyphID, "components/arguments")
				}
				component.arg1, component.arg2 = int32(int8(first)), int32(int8(second))
			}
		} else {
			component.argumentMode = outlineArgumentPoint
			if flags&componentArgWords != 0 {
				first, firstOK := cursor.u16()
				second, secondOK := cursor.u16()
				if !firstOK || !secondOK {
					return outlineGlyph{}, outlineGlyphError(glyphID, "components/arguments")
				}
				component.arg1, component.arg2 = int32(first), int32(second)
			} else {
				first, firstOK := cursor.u8()
				second, secondOK := cursor.u8()
				if !firstOK || !secondOK {
					return outlineGlyph{}, outlineGlyphError(glyphID, "components/arguments")
				}
				component.arg1, component.arg2 = int32(first), int32(second)
			}
		}
		if flags&componentScale != 0 {
			scale, ok := cursor.u16()
			if !ok {
				return outlineGlyph{}, outlineGlyphError(glyphID, "components/transform")
			}
			component.matrix = [4]int16{int16(scale), 0, 0, int16(scale)}
		} else if flags&componentXYScale != 0 {
			xScale, xOK := cursor.u16()
			yScale, yOK := cursor.u16()
			if !xOK || !yOK {
				return outlineGlyph{}, outlineGlyphError(glyphID, "components/transform")
			}
			component.matrix = [4]int16{int16(xScale), 0, 0, int16(yScale)}
		} else if flags&componentTwoByTwo != 0 {
			xScale, ok0 := cursor.u16()
			scale01, ok1 := cursor.u16()
			scale10, ok2 := cursor.u16()
			yScale, ok3 := cursor.u16()
			if !ok0 || !ok1 || !ok2 || !ok3 {
				return outlineGlyph{}, outlineGlyphError(glyphID, "components/transform")
			}
			component.matrix = [4]int16{int16(xScale), int16(scale01), int16(scale10), int16(yScale)}
		}
		projected := *stats
		projected.componentRecords++
		if uint32(componentIndex)+1 > projected.maxComponentElements {
			projected.maxComponentElements = uint32(componentIndex) + 1
		}
		if err := resources.check(projected, fmt.Sprintf("outline/glyphs/%d/resources/components", glyphID)); err != nil {
			return outlineGlyph{}, err
		}
		glyph.components = append(glyph.components, component)
		if flags&componentArgsXY == 0 {
			stats.pointMatchComponents++
		}
		if flags&componentScaledOffset != 0 {
			stats.scaledOffsets++
		} else if flags&componentUnscaledOffset != 0 {
			stats.unscaledOffsets++
		} else if argsXY {
			stats.defaultedOffsets++
		}
		if flags&componentUseMyMetrics != 0 {
			stats.useMyMetrics++
		}
		if flags&componentOverlap != 0 {
			glyph.overlapDeclared = true
		}
		stats.componentRecords++
		finalFlags = flags
		if flags&componentMore == 0 {
			break
		}
	}
	if finalFlags&componentInstructions != 0 {
		length, ok := cursor.u16()
		if !ok {
			return outlineGlyph{}, outlineGlyphError(glyphID, "instructions/length")
		}
		if uint32(length) > declared.maxInstructionBytes {
			return outlineGlyph{}, fontError(CodeOutlineMaxpMismatch, fmt.Sprintf("outline/maxp/glyphs/%d/max_size_of_instructions", glyphID), ErrOutlineMaxpMismatch)
		}
		instructions, ok := cursor.take(int(length))
		if !ok {
			return outlineGlyph{}, outlineGlyphError(glyphID, "instructions")
		}
		if uint64(length) > limits.MaxInstructionBytes-stats.instructionBytes {
			return outlineGlyph{}, outlineResourceError(glyphID, "instruction_bytes")
		}
		glyph.instructions = instructions
	}
	padding, err := consumeOutlinePadding(&cursor, glyphID)
	if err != nil {
		return outlineGlyph{}, err
	}
	glyph.paddingBytes = padding
	stats.compoundGlyphs++
	stats.paddingBytes += uint64(padding)
	if len(glyph.instructions) > 0 {
		stats.instructionGlyphs++
	}
	stats.instructionBytes += uint64(len(glyph.instructions))
	if uint32(len(glyph.instructions)) > stats.maxInstructionBytes {
		stats.maxInstructionBytes = uint32(len(glyph.instructions))
	}
	if uint32(len(glyph.components)) > stats.maxComponentElements {
		stats.maxComponentElements = uint32(len(glyph.components))
	}
	if glyph.overlapDeclared {
		stats.overlapCompoundGlyphs++
	}
	if err := resources.check(*stats, fmt.Sprintf("outline/glyphs/%d/resources", glyphID)); err != nil {
		return outlineGlyph{}, err
	}
	return glyph, nil
}

func updateSimpleStats(glyph outlineGlyph, stats *outlineScanStats) {
	stats.simpleGlyphs++
	if glyph.numberOfContours == 0 {
		stats.zeroContourGlyphs++
	}
	stats.rawPoints += uint64(len(glyph.points))
	stats.rawContours += uint64(len(glyph.endPoints))
	stats.paddingBytes += uint64(glyph.paddingBytes)
	if len(glyph.instructions) > 0 {
		stats.instructionGlyphs++
	}
	stats.instructionBytes += uint64(len(glyph.instructions))
	if uint32(len(glyph.points)) > stats.maxPoints {
		stats.maxPoints = uint32(len(glyph.points))
	}
	if uint32(len(glyph.endPoints)) > stats.maxContours {
		stats.maxContours = uint32(len(glyph.endPoints))
	}
	if uint32(len(glyph.instructions)) > stats.maxInstructionBytes {
		stats.maxInstructionBytes = uint32(len(glyph.instructions))
	}
	if glyph.overlapDeclared {
		stats.overlapSimpleGlyphs++
	}
}

func consumeOutlinePadding(cursor *outlineCursor, glyphID uint32) (uint8, error) {
	remaining := cursor.remaining()
	if remaining < 0 || remaining > 3 {
		return 0, outlineGlyphError(glyphID, "padding")
	}
	raw, ok := cursor.take(remaining)
	if !ok {
		return 0, outlineGlyphError(glyphID, "padding")
	}
	for _, value := range raw {
		if value != 0 {
			return 0, outlineGlyphError(glyphID, "padding")
		}
	}
	return uint8(remaining), nil
}

func outlineGlyphError(glyphID uint32, suffix string) error {
	return fontError(CodeOutlineInvalid, fmt.Sprintf("outline/glyphs/%d/%s", glyphID, suffix), ErrOutlineInvalid)
}

func outlineResourceError(glyphID uint32, suffix string) error {
	return fontError(CodeOutlineResourceLimit, fmt.Sprintf("outline/glyphs/%d/%s", glyphID, suffix), ErrResourceLimit)
}
