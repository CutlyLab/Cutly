package sfntglyf

import (
	"context"
	"fmt"
	"math"
)

type outlineDFSFrame struct {
	glyphID uint32
	next    int
}

func validateOutlineGraph(ctx context.Context, glyphs []outlineGlyph, resources outlineResourceContext, stats *outlineScanStats) (err error) {
	limits := resources.limits
	defer func() {
		if recover() != nil {
			err = fontError(CodeAllocationFailed, "outline/graph/allocation", ErrAllocationFailed)
		}
	}()
	colors := make([]uint8, len(glyphs))
	order := make([]uint32, 0, len(glyphs))
	stack := make([]outlineDFSFrame, 0, 64)
	var dfsSteps, edgeSteps uint64
	for root := range glyphs {
		if colors[root] != 0 {
			continue
		}
		colors[root] = 1
		stack = append(stack, outlineDFSFrame{glyphID: uint32(root)})
		for len(stack) > 0 {
			if dfsSteps%2048 == 0 {
				if err := contextFailure(ctx, "outline/graph/dfs"); err != nil {
					return err
				}
			}
			dfsSteps++
			frame := &stack[len(stack)-1]
			components := glyphs[frame.glyphID].components
			if frame.next < len(components) {
				if edgeSteps%2048 == 0 {
					if err := contextFailure(ctx, "outline/graph/dfs/edges"); err != nil {
						return err
					}
				}
				edgeSteps++
				child := components[frame.next].glyphID
				frame.next++
				if uint64(child) >= uint64(len(glyphs)) {
					return fontError(CodeOutlineInvariantFailed, "outline/graph/glyph_id", ErrInvariantFailed)
				}
				switch colors[child] {
				case 0:
					colors[child] = 1
					stack = append(stack, outlineDFSFrame{glyphID: child})
				case 1:
					return fontError(CodeOutlineGraphInvalid, fmt.Sprintf("outline/graph/cycle/%d/%d", frame.glyphID, child), ErrOutlineGraphInvalid)
				}
				continue
			}
			colors[frame.glyphID] = 2
			order = append(order, frame.glyphID)
			stack = stack[:len(stack)-1]
		}
	}
	for index, glyphID := range order {
		if index%1024 == 0 {
			if err := contextFailure(ctx, "outline/graph/dynamic_program"); err != nil {
				return err
			}
		}
		glyph := &glyphs[glyphID]
		if glyph.kind == OutlineGlyphCompound {
			var parentPoints uint64
			var points, contours, edges uint64
			var depth uint32
			for componentIndex, component := range glyph.components {
				if componentIndex%2048 == 0 {
					if err := contextFailure(ctx, fmt.Sprintf("outline/graph/glyphs/%d/components/%d", glyphID, componentIndex)); err != nil {
						return err
					}
				}
				child := &glyphs[component.glyphID]
				if component.argumentMode == outlineArgumentPoint {
					if component.arg1 < 0 || component.arg2 < 0 || uint64(component.arg1) >= parentPoints || uint64(component.arg2) >= child.expandedPoints {
						return fontError(CodeOutlineGraphInvalid, fmt.Sprintf("outline/graph/glyphs/%d/components/%d/point_match", glyphID, componentIndex), ErrOutlineGraphInvalid)
					}
				}
				var ok bool
				points, ok = checkedAdd(points, child.expandedPoints)
				if !ok || points > limits.MaxExpandedPointsPerGlyph {
					return outlineGraphResourceError(glyphID, "expanded_points")
				}
				parentPoints = points
				contours, ok = checkedAdd(contours, child.expandedContours)
				if !ok || contours > limits.MaxExpandedContoursPerGlyph {
					return outlineGraphResourceError(glyphID, "expanded_contours")
				}
				childEdge, ok := checkedAdd(child.expandedEdges, 1)
				if !ok {
					return outlineGraphResourceError(glyphID, "expanded_edges")
				}
				edges, ok = checkedAdd(edges, childEdge)
				if !ok || edges > limits.MaxExpandedEdgesPerGlyph {
					return outlineGraphResourceError(glyphID, "expanded_edges")
				}
				if child.componentDepth == math.MaxUint32 {
					return outlineGraphResourceError(glyphID, "component_depth")
				}
				candidateDepth := child.componentDepth + 1
				if candidateDepth > depth {
					depth = candidateDepth
				}
			}
			if depth > limits.MaxComponentDepth {
				return outlineGraphResourceError(glyphID, "component_depth")
			}
			glyph.expandedPoints, glyph.expandedContours = points, contours
			glyph.expandedEdges, glyph.componentDepth = edges, depth
			if points > math.MaxUint16 || contours > math.MaxUint16 {
				return fontError(CodeOutlineMaxpMismatch, fmt.Sprintf("outline/maxp/glyphs/%d/representability", glyphID), ErrOutlineMaxpMismatch)
			}
			if uint32(points) > stats.maxCompositePoints {
				stats.maxCompositePoints = uint32(points)
			}
			if uint32(contours) > stats.maxCompositeContours {
				stats.maxCompositeContours = uint32(contours)
			}
			if depth > stats.maxComponentDepth {
				stats.maxComponentDepth = depth
			}
		} else {
			glyph.expandedPoints = uint64(len(glyph.points))
			glyph.expandedContours = uint64(len(glyph.endPoints))
		}
		if glyph.expandedPoints > limits.MaxExpandedPointsPerGlyph {
			return outlineGraphResourceError(glyphID, "expanded_points")
		}
		if glyph.expandedContours > limits.MaxExpandedContoursPerGlyph {
			return outlineGraphResourceError(glyphID, "expanded_contours")
		}
		if glyph.expandedEdges > limits.MaxExpandedEdgesPerGlyph {
			return outlineGraphResourceError(glyphID, "expanded_edges")
		}
		if glyph.componentDepth > limits.MaxComponentDepth {
			return outlineGraphResourceError(glyphID, "component_depth")
		}
		var ok bool
		stats.expandedPoints, ok = checkedAdd(stats.expandedPoints, glyph.expandedPoints)
		if !ok || stats.expandedPoints > limits.MaxExpandedPointInstances {
			return outlineGraphResourceError(glyphID, "expanded_point_instances")
		}
		stats.expandedContours, ok = checkedAdd(stats.expandedContours, glyph.expandedContours)
		if !ok || stats.expandedContours > limits.MaxExpandedContourInstances {
			return outlineGraphResourceError(glyphID, "expanded_contour_instances")
		}
		stats.expandedEdges, ok = checkedAdd(stats.expandedEdges, glyph.expandedEdges)
		if !ok || stats.expandedEdges > limits.MaxExpandedEdgeVisits {
			return outlineGraphResourceError(glyphID, "expanded_edge_visits")
		}
		if glyph.expandedEdges > stats.maxExpandedEdges {
			stats.maxExpandedEdges = glyph.expandedEdges
		}
		if err := resources.check(*stats, fmt.Sprintf("outline/graph/glyphs/%d/resources", glyphID)); err != nil {
			return err
		}
	}
	return nil
}

func outlineGraphResourceError(glyphID uint32, suffix string) error {
	return fontError(CodeOutlineResourceLimit, fmt.Sprintf("outline/graph/glyphs/%d/%s", glyphID, suffix), ErrResourceLimit)
}
