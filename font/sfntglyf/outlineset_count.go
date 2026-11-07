package sfntglyf

import (
	"context"
	"fmt"
)

type outlineSetGlyphStats struct {
	contours      uint64
	implied       uint64
	lines         uint64
	quads         uint64
	drawing       uint64
	commands      uint64
	commandPoints uint64
}

type outlineSetTopologyStats struct {
	glyphs                   []outlineSetGlyphStats
	glyphsWithContours       uint64
	implied                  uint64
	lines                    uint64
	quads                    uint64
	drawing                  uint64
	commands                 uint64
	commandPoints            uint64
	maxImpliedPerGlyph       uint64
	maxDrawingPerGlyph       uint64
	maxCommandsPerGlyph      uint64
	maxCommandPointsPerGlyph uint64
}

func computeOutlineSetTopology(ctx context.Context, source *OutlineFont, limits OutlineSetLimits) (outlineSetTopologyStats, error) {
	if source == nil || len(source.glyphs) == 0 {
		return outlineSetTopologyStats{}, fontError(CodeOutlineSetInputInvalid, "outline_set/topology/source", ErrOutlineSetInputInvalid)
	}
	result := outlineSetTopologyStats{glyphs: make([]outlineSetGlyphStats, len(source.glyphs))}
	state := make([]uint8, len(source.glyphs))
	var visit func(uint32) (outlineSetGlyphStats, error)
	visit = func(glyphID uint32) (outlineSetGlyphStats, error) {
		if uint64(glyphID) >= uint64(len(source.glyphs)) {
			return outlineSetGlyphStats{}, outlineSetInvariant("outline_set/topology/glyph_id")
		}
		if state[glyphID] == 2 {
			return result.glyphs[glyphID], nil
		}
		if state[glyphID] == 1 {
			return outlineSetGlyphStats{}, outlineSetInvariant("outline_set/topology/cycle")
		}
		state[glyphID] = 1
		glyph := source.glyphs[glyphID]
		var stats outlineSetGlyphStats
		switch glyph.kind {
		case OutlineGlyphEmptyRange:
			if len(glyph.points) != 0 || len(glyph.endPoints) != 0 || len(glyph.components) != 0 {
				return outlineSetGlyphStats{}, outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/empty", glyphID))
			}
		case OutlineGlyphSimple:
			var err error
			stats, err = countSimpleOutlineCommands(ctx, glyphID, glyph.points, glyph.endPoints)
			if err != nil {
				return outlineSetGlyphStats{}, err
			}
		case OutlineGlyphCompound:
			if len(glyph.components) == 0 {
				return outlineSetGlyphStats{}, outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/components", glyphID))
			}
			for componentIndex, component := range glyph.components {
				if componentIndex%1024 == 0 {
					if err := contextFailure(ctx, fmt.Sprintf("outline_set/topology/glyphs/%d/components/%d", glyphID, componentIndex)); err != nil {
						return outlineSetGlyphStats{}, err
					}
				}
				child, err := visit(component.glyphID)
				if err != nil {
					return outlineSetGlyphStats{}, err
				}
				if !addOutlineSetGlyphStats(&stats, child) {
					return outlineSetGlyphStats{}, outlineSetResource(fmt.Sprintf("outline_set/topology/glyphs/%d/overflow", glyphID))
				}
			}
		default:
			return outlineSetGlyphStats{}, outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/kind", glyphID))
		}
		if err := validateOutlineSetGlyphStats(glyph, stats, glyphID); err != nil {
			return outlineSetGlyphStats{}, err
		}
		if stats.commands > limits.MaxCommandsPerGlyph {
			return outlineSetGlyphStats{}, outlineSetResource(fmt.Sprintf("outline_set/topology/glyphs/%d/commands", glyphID))
		}
		if stats.commandPoints > limits.MaxCommandPointOccurrencesPerGlyph {
			return outlineSetGlyphStats{}, outlineSetResource(fmt.Sprintf("outline_set/topology/glyphs/%d/command_points", glyphID))
		}
		result.glyphs[glyphID] = stats
		state[glyphID] = 2
		return stats, nil
	}

	for glyphIndex := range source.glyphs {
		if glyphIndex%256 == 0 {
			if err := contextFailure(ctx, "outline_set/topology/roots"); err != nil {
				return outlineSetTopologyStats{}, err
			}
		}
		stats, err := visit(uint32(glyphIndex))
		if err != nil {
			return outlineSetTopologyStats{}, err
		}
		if stats.contours > 0 {
			result.glyphsWithContours++
		}
		if !checkedOutlineSetTopologyAdd(&result, stats) {
			return outlineSetTopologyStats{}, outlineSetResource("outline_set/topology/totals/overflow")
		}
		if result.implied > limits.MaxImpliedPointInstances {
			return outlineSetTopologyStats{}, outlineSetResource("outline_set/topology/implied_points")
		}
		if result.drawing > limits.MaxDrawingCommandInstances {
			return outlineSetTopologyStats{}, outlineSetResource("outline_set/topology/drawing_commands")
		}
		if result.commands > limits.MaxCommandInstances {
			return outlineSetTopologyStats{}, outlineSetResource("outline_set/topology/commands")
		}
		if result.commandPoints > limits.MaxCommandPointOccurrences {
			return outlineSetTopologyStats{}, outlineSetResource("outline_set/topology/command_points")
		}
	}
	if result.commands > uint64(maxInt()) || uint64(len(result.glyphs)) > uint64(maxInt()) {
		return outlineSetTopologyStats{}, outlineSetResource("outline_set/topology/representability")
	}
	if result.implied > source.receipt.ExpandedPointInstances || result.drawing > source.receipt.ExpandedPointInstances {
		return outlineSetTopologyStats{}, outlineSetInvariant("outline_set/topology/global_point_bounds")
	}
	commandBound, ok := outlineWeightedSum(source.receipt.ExpandedPointInstances,
		[2]uint64{source.receipt.ExpandedContourInstances, 2})
	if !ok || result.commands > commandBound {
		return outlineSetTopologyStats{}, outlineSetInvariant("outline_set/topology/global_command_bound")
	}
	pointBound, ok := outlineWeightedSum(0,
		[2]uint64{source.receipt.ExpandedPointInstances, 2},
		[2]uint64{source.receipt.ExpandedContourInstances, 1})
	if !ok || result.commandPoints > pointBound {
		return outlineSetTopologyStats{}, outlineSetInvariant("outline_set/topology/global_command_point_bound")
	}
	return result, nil
}

func countSimpleOutlineCommands(ctx context.Context, glyphID uint32, points []outlineRawPoint, endPoints []uint32) (outlineSetGlyphStats, error) {
	var result outlineSetGlyphStats
	start := uint64(0)
	for contourIndex, encodedEnd := range endPoints {
		if contourIndex%256 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline_set/topology/glyphs/%d/contours/%d", glyphID, contourIndex)); err != nil {
				return outlineSetGlyphStats{}, err
			}
		}
		end := uint64(encodedEnd)
		if end < start || end >= uint64(len(points)) {
			return outlineSetGlyphStats{}, outlineSetInvariant(fmt.Sprintf("outline_set/topology/simple/contours/%d", contourIndex))
		}
		first, last := points[start], points[end]
		traverseStart, traverseEnd := start, end
		if first.onCurve {
			traverseStart = start + 1
		} else if last.onCurve {
			traverseEnd = end - 1
		} else {
			result.implied++
		}
		pending := false
		if traverseStart <= traverseEnd {
			for index := traverseStart; index <= traverseEnd; index++ {
				if (index-traverseStart)%1024 == 0 {
					if err := contextFailure(ctx, fmt.Sprintf("outline_set/topology/glyphs/%d/contours/%d/points/%d", glyphID, contourIndex, index)); err != nil {
						return outlineSetGlyphStats{}, err
					}
				}
				if points[index].onCurve {
					if pending {
						result.quads++
						pending = false
					} else {
						result.lines++
					}
				} else if pending {
					result.implied++
					result.quads++
				} else {
					pending = true
				}
			}
		}
		if pending {
			result.quads++
		}
		result.contours++
		start = end + 1
	}
	if start != uint64(len(points)) {
		return outlineSetGlyphStats{}, outlineSetInvariant("outline_set/topology/simple/final_endpoint")
	}
	var ok bool
	result.drawing, ok = checkedAdd(result.lines, result.quads)
	if !ok {
		return outlineSetGlyphStats{}, outlineSetResource("outline_set/topology/simple/drawing_overflow")
	}
	result.commands, ok = outlineWeightedSum(result.drawing, [2]uint64{result.contours, 2})
	if !ok {
		return outlineSetGlyphStats{}, outlineSetResource("outline_set/topology/simple/command_overflow")
	}
	result.commandPoints, ok = outlineWeightedSum(result.contours,
		[2]uint64{result.lines, 1}, [2]uint64{result.quads, 2})
	if !ok {
		return outlineSetGlyphStats{}, outlineSetResource("outline_set/topology/simple/point_overflow")
	}
	return result, nil
}

func validateOutlineSetGlyphStats(glyph outlineGlyph, stats outlineSetGlyphStats, glyphID uint32) error {
	if stats.contours != glyph.expandedContours || stats.implied > glyph.expandedPoints || stats.drawing > glyph.expandedPoints {
		return outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/count_binding", glyphID))
	}
	if stats.contours > glyph.expandedPoints {
		return outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/nonempty_contours", glyphID))
	}
	lineBound := glyph.expandedPoints - stats.contours
	if stats.lines > lineBound {
		return outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/line_bound", glyphID))
	}
	commandBound, ok := outlineWeightedSum(glyph.expandedPoints, [2]uint64{stats.contours, 2})
	if !ok || stats.commands > commandBound {
		return outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/command_bound", glyphID))
	}
	pointBound, ok := outlineWeightedSum(0, [2]uint64{glyph.expandedPoints, 2}, [2]uint64{stats.contours, 1})
	if !ok || stats.commandPoints > pointBound {
		return outlineSetInvariant(fmt.Sprintf("outline_set/topology/glyphs/%d/command_point_bound", glyphID))
	}
	return nil
}

func addOutlineSetGlyphStats(target *outlineSetGlyphStats, add outlineSetGlyphStats) bool {
	if target == nil {
		return false
	}
	values := []*uint64{&target.contours, &target.implied, &target.lines, &target.quads, &target.drawing, &target.commands, &target.commandPoints}
	increments := []uint64{add.contours, add.implied, add.lines, add.quads, add.drawing, add.commands, add.commandPoints}
	for index := range values {
		next, ok := checkedAdd(*values[index], increments[index])
		if !ok {
			return false
		}
		*values[index] = next
	}
	return true
}

func checkedOutlineSetTopologyAdd(target *outlineSetTopologyStats, add outlineSetGlyphStats) bool {
	values := []*uint64{&target.implied, &target.lines, &target.quads, &target.drawing, &target.commands, &target.commandPoints}
	increments := []uint64{add.implied, add.lines, add.quads, add.drawing, add.commands, add.commandPoints}
	for index := range values {
		next, ok := checkedAdd(*values[index], increments[index])
		if !ok {
			return false
		}
		*values[index] = next
	}
	if add.implied > target.maxImpliedPerGlyph {
		target.maxImpliedPerGlyph = add.implied
	}
	if add.drawing > target.maxDrawingPerGlyph {
		target.maxDrawingPerGlyph = add.drawing
	}
	if add.commands > target.maxCommandsPerGlyph {
		target.maxCommandsPerGlyph = add.commands
	}
	if add.commandPoints > target.maxCommandPointsPerGlyph {
		target.maxCommandPointsPerGlyph = add.commandPoints
	}
	return true
}

func outlineSetResource(path string) error {
	return fontError(CodeOutlineSetResourceLimit, path, ErrResourceLimit)
}

func outlineSetInvariant(path string) error {
	return fontError(CodeOutlineSetInvariant, path, ErrInvariantFailed)
}
