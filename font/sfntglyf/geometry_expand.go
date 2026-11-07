package sfntglyf

import (
	"context"
	"fmt"
)

type exactGeometryPoint struct {
	x       geometryDyadic
	y       geometryDyadic
	onCurve bool
}

type exactGlyphGeometry struct {
	points    []exactGeometryPoint
	endPoints []uint32
}

func expandExactGlyph(ctx context.Context, source *OutlineFont, glyphID uint32, exact *geometryExactContext) (exactGlyphGeometry, error) {
	if source == nil || uint64(glyphID) >= uint64(len(source.glyphs)) {
		return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, "geometry/expand/glyph_id", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, fmt.Sprintf("geometry/expand/%d", glyphID)); err != nil {
		return exactGlyphGeometry{}, err
	}
	glyph := source.glyphs[glyphID]
	if glyph.expandedPoints > uint64(maxInt()) || glyph.expandedContours > uint64(maxInt()) {
		return exactGlyphGeometry{}, fontError(CodeGeometryResourceLimit, fmt.Sprintf("geometry/expand/%d/size", glyphID), ErrResourceLimit)
	}
	switch glyph.kind {
	case OutlineGlyphEmptyRange:
		return exactGlyphGeometry{}, nil
	case OutlineGlyphSimple:
		return expandExactSimpleGlyph(ctx, glyph, glyphID, exact)
	case OutlineGlyphCompound:
		return expandExactCompoundGlyph(ctx, source, glyph, glyphID, exact)
	default:
		return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/expand/%d/kind", glyphID), ErrInvariantFailed)
	}
}

func expandExactSimpleGlyph(ctx context.Context, glyph outlineGlyph, glyphID uint32, exact *geometryExactContext) (exactGlyphGeometry, error) {
	if uint64(len(glyph.points)) != glyph.expandedPoints || uint64(len(glyph.endPoints)) != glyph.expandedContours {
		return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/expand/%d/simple_counts", glyphID), ErrInvariantFailed)
	}
	result := exactGlyphGeometry{
		points:    make([]exactGeometryPoint, len(glyph.points)),
		endPoints: append([]uint32(nil), glyph.endPoints...),
	}
	for index, point := range glyph.points {
		if index%1024 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("geometry/expand/%d/simple/%d", glyphID, index)); err != nil {
				return exactGlyphGeometry{}, err
			}
		}
		x, err := exact.integer(int64(point.x), fmt.Sprintf("geometry/expand/%d/simple/%d/x", glyphID, index))
		if err != nil {
			return exactGlyphGeometry{}, err
		}
		y, err := exact.integer(int64(point.y), fmt.Sprintf("geometry/expand/%d/simple/%d/y", glyphID, index))
		if err != nil {
			return exactGlyphGeometry{}, err
		}
		result.points[index] = exactGeometryPoint{x: x, y: y, onCurve: point.onCurve}
	}
	return result, nil
}

func expandExactCompoundGlyph(ctx context.Context, source *OutlineFont, glyph outlineGlyph, glyphID uint32, exact *geometryExactContext) (exactGlyphGeometry, error) {
	// Do not reserve the full root size at every recursive level: a one-child
	// depth chain would otherwise retain depth*root unused capacity. Incremental
	// append keeps ancestor storage tied to already accumulated, disjoint point
	// instances and preserves the profile's three-root-buffer working bound.
	result := exactGlyphGeometry{}
	for componentIndex, component := range glyph.components {
		if componentIndex%256 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("geometry/expand/%d/components/%d", glyphID, componentIndex)); err != nil {
				return exactGlyphGeometry{}, err
			}
		}
		child, err := expandExactGlyph(ctx, source, component.glyphID, exact)
		if err != nil {
			return exactGlyphGeometry{}, err
		}
		transformed, err := transformExactComponent(ctx, child.points, component, glyphID, componentIndex, exact)
		if err != nil {
			return exactGlyphGeometry{}, err
		}
		if component.argumentMode == outlineArgumentPoint {
			if len(transformed) == 0 || component.arg1 < 0 || component.arg2 < 0 ||
				uint64(component.arg1) >= uint64(len(result.points)) || uint64(component.arg2) >= uint64(len(transformed)) {
				return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/expand/%d/components/%d/point_match", glyphID, componentIndex), ErrInvariantFailed)
			}
			translationX, err := exact.subtract(result.points[component.arg1].x, transformed[component.arg2].x,
				fmt.Sprintf("geometry/expand/%d/components/%d/point_match/x", glyphID, componentIndex))
			if err != nil {
				return exactGlyphGeometry{}, err
			}
			translationY, err := exact.subtract(result.points[component.arg1].y, transformed[component.arg2].y,
				fmt.Sprintf("geometry/expand/%d/components/%d/point_match/y", glyphID, componentIndex))
			if err != nil {
				return exactGlyphGeometry{}, err
			}
			for pointIndex := range transformed {
				if pointIndex%1024 == 0 {
					if err := contextFailure(ctx, fmt.Sprintf("geometry/expand/%d/components/%d/attachment/%d", glyphID, componentIndex, pointIndex)); err != nil {
						return exactGlyphGeometry{}, err
					}
				}
				transformed[pointIndex].x, err = exact.add(transformed[pointIndex].x, translationX,
					fmt.Sprintf("geometry/expand/%d/components/%d/attachment/%d/x", glyphID, componentIndex, pointIndex))
				if err != nil {
					return exactGlyphGeometry{}, err
				}
				transformed[pointIndex].y, err = exact.add(transformed[pointIndex].y, translationY,
					fmt.Sprintf("geometry/expand/%d/components/%d/attachment/%d/y", glyphID, componentIndex, pointIndex))
				if err != nil {
					return exactGlyphGeometry{}, err
				}
			}
		} else if component.argumentMode != outlineArgumentXY {
			return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/expand/%d/components/%d/argument_mode", glyphID, componentIndex), ErrInvariantFailed)
		}
		pointBase := uint64(len(result.points))
		nextPointCount, ok := checkedAdd(pointBase, uint64(len(transformed)))
		if !ok || nextPointCount > uint64(maxInt()) {
			return exactGlyphGeometry{}, fontError(CodeGeometryResourceLimit, fmt.Sprintf("geometry/expand/%d/components/%d/points", glyphID, componentIndex), ErrResourceLimit)
		}
		result.points = append(result.points, transformed...)
		for _, endPoint := range child.endPoints {
			shifted, ok := checkedAdd(pointBase, uint64(endPoint))
			if !ok || shifted > uint64(^uint32(0)) {
				return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/expand/%d/components/%d/end_points", glyphID, componentIndex), ErrInvariantFailed)
			}
			result.endPoints = append(result.endPoints, uint32(shifted))
		}
	}
	if uint64(len(result.points)) != glyph.expandedPoints || uint64(len(result.endPoints)) != glyph.expandedContours {
		return exactGlyphGeometry{}, fontError(CodeGeometryInvariant, fmt.Sprintf("geometry/expand/%d/compound_counts", glyphID), ErrInvariantFailed)
	}
	return result, nil
}

func transformExactComponent(ctx context.Context, points []exactGeometryPoint, component outlineComponent, glyphID uint32, componentIndex int, exact *geometryExactContext) ([]exactGeometryPoint, error) {
	path := fmt.Sprintf("geometry/expand/%d/components/%d", glyphID, componentIndex)
	matrix := [4]geometryDyadic{}
	for index, encoded := range component.matrix {
		value, err := exact.f2dot14(encoded, fmt.Sprintf("%s/matrix/%d", path, index))
		if err != nil {
			return nil, err
		}
		matrix[index] = value
	}
	var offsetX, offsetY geometryDyadic
	var err error
	if component.argumentMode == outlineArgumentXY {
		offsetX, err = exact.integer(int64(component.arg1), path+"/offset/x")
		if err != nil {
			return nil, err
		}
		offsetY, err = exact.integer(int64(component.arg2), path+"/offset/y")
		if err != nil {
			return nil, err
		}
	}
	result := make([]exactGeometryPoint, len(points))
	for pointIndex, point := range points {
		if pointIndex%1024 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("%s/transform/%d", path, pointIndex)); err != nil {
				return nil, err
			}
		}
		x, y := point.x, point.y
		if component.argumentMode == outlineArgumentXY && component.flags&componentScaledOffset != 0 {
			x, err = exact.add(x, offsetX, fmt.Sprintf("%s/transform/%d/scaled_offset_x", path, pointIndex))
			if err != nil {
				return nil, err
			}
			y, err = exact.add(y, offsetY, fmt.Sprintf("%s/transform/%d/scaled_offset_y", path, pointIndex))
			if err != nil {
				return nil, err
			}
		}
		transformedX, transformedY, err := applyExactMatrix(exact, matrix, x, y, fmt.Sprintf("%s/transform/%d", path, pointIndex))
		if err != nil {
			return nil, err
		}
		if component.argumentMode == outlineArgumentXY && component.flags&componentScaledOffset == 0 {
			transformedX, err = exact.add(transformedX, offsetX, fmt.Sprintf("%s/transform/%d/unscaled_offset_x", path, pointIndex))
			if err != nil {
				return nil, err
			}
			transformedY, err = exact.add(transformedY, offsetY, fmt.Sprintf("%s/transform/%d/unscaled_offset_y", path, pointIndex))
			if err != nil {
				return nil, err
			}
		}
		result[pointIndex] = exactGeometryPoint{x: transformedX, y: transformedY, onCurve: point.onCurve}
	}
	return result, nil
}

func applyExactMatrix(exact *geometryExactContext, matrix [4]geometryDyadic, x, y geometryDyadic, path string) (geometryDyadic, geometryDyadic, error) {
	xx, err := exact.multiply(matrix[0], x, path+"/xscale_x")
	if err != nil {
		return geometryDyadic{}, geometryDyadic{}, err
	}
	xy, err := exact.multiply(matrix[2], y, path+"/scale10_y")
	if err != nil {
		return geometryDyadic{}, geometryDyadic{}, err
	}
	resultX, err := exact.add(xx, xy, path+"/x")
	if err != nil {
		return geometryDyadic{}, geometryDyadic{}, err
	}
	yx, err := exact.multiply(matrix[1], x, path+"/scale01_x")
	if err != nil {
		return geometryDyadic{}, geometryDyadic{}, err
	}
	yy, err := exact.multiply(matrix[3], y, path+"/yscale_y")
	if err != nil {
		return geometryDyadic{}, geometryDyadic{}, err
	}
	resultY, err := exact.add(yx, yy, path+"/y")
	if err != nil {
		return geometryDyadic{}, geometryDyadic{}, err
	}
	return resultX, resultY, nil
}
