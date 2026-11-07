package vectorraster

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

type sourceMeasure struct {
	width           uint32
	height          uint32
	pixels          uint64
	paths           uint64
	commands        uint64
	conceptualEdges uint64
	samples         uint64
	workUnits       uint64
	canonicalUpper  uint64
	workingBytes    uint64
	retainedBytes   uint64
}

func contextFailure(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return rasterError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func validateAndMeasure(ctx context.Context, source documentv2.VectorSource, limits Limits) (sourceMeasure, error) {
	if source.Profile != documentv2.VectorProfilePathViewportPXV2 {
		return sourceMeasure{}, rasterError(CodeProfileUnsupported, "source/profile", ErrProfileUnsupported)
	}
	if source.Viewport == nil {
		return sourceMeasure{}, rasterError(CodeSourceInvalid, "source/viewport", ErrSourceInvalid)
	}
	width, height := source.Viewport.WidthPixels, source.Viewport.HeightPixels
	if width == 0 || width > documentv2.RasterViewportMaxDimension {
		return sourceMeasure{}, rasterError(CodeSourceInvalid, "source/viewport/width_pixels", ErrSourceInvalid)
	}
	if height == 0 || height > documentv2.RasterViewportMaxDimension {
		return sourceMeasure{}, rasterError(CodeSourceInvalid, "source/viewport/height_pixels", ErrSourceInvalid)
	}
	if width > limits.MaxWidth || height > limits.MaxHeight {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/viewport/dimensions", ErrResourceLimit)
	}
	pixels, ok := checkedMul(uint64(width), uint64(height))
	if !ok || pixels > limits.MaxPixels || pixels > maxInt() {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/viewport/pixels", ErrResourceLimit)
	}
	retained, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok || retained > limits.MaxRetainedBytes {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/viewport/retained_bytes", ErrResourceLimit)
	}
	if err := validateRect(source.ViewBox, "source/view_box", limits.MaxExactBits); err != nil {
		return sourceMeasure{}, err
	}
	if rationalSign(source.ViewBox.Width) <= 0 || rationalSign(source.ViewBox.Height) <= 0 {
		return sourceMeasure{}, rasterError(CodeSourceInvalid, "source/view_box", ErrSourceInvalid)
	}
	if len(source.Paths) == 0 {
		return sourceMeasure{}, rasterError(CodeSourceInvalid, "source/paths", ErrSourceInvalid)
	}
	if uint64(len(source.Paths)) > limits.MaxPaths {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/paths", ErrResourceLimit)
	}

	measure := sourceMeasure{
		width: width, height: height, pixels: pixels, paths: uint64(len(source.Paths)),
		canonicalUpper: 4096, retainedBytes: retained,
	}
	for pathIndex, path := range source.Paths {
		if pathIndex&255 == 0 {
			if err := contextFailure(ctx, "source/paths"); err != nil {
				return sourceMeasure{}, err
			}
		}
		pathPrefix := fmt.Sprintf("source/paths/%d", pathIndex)
		id := string(path.ID)
		if !utf8.ValidString(id) || strings.TrimSpace(id) == "" {
			return sourceMeasure{}, rasterError(CodeSourceInvalid, pathPrefix+"/id", ErrSourceInvalid)
		}
		escapedID, ok := checkedMul(uint64(len(id)), 6)
		if !ok {
			return sourceMeasure{}, rasterError(CodeResourceLimit, pathPrefix+"/id", ErrResourceLimit)
		}
		measure.canonicalUpper, ok = checkedAdd(measure.canonicalUpper, escapedID)
		if !ok {
			return sourceMeasure{}, rasterError(CodeResourceLimit, "source/canonical_bytes", ErrResourceLimit)
		}
		if path.Fill == nil {
			return sourceMeasure{}, rasterError(CodeSourceInvalid, pathPrefix+"/fill", ErrSourceInvalid)
		}
		if path.Stroke != nil {
			return sourceMeasure{}, rasterError(CodeSourceInvalid, pathPrefix+"/stroke", ErrSourceInvalid)
		}
		if path.FillRule != documentv2.VectorFillNonZero && path.FillRule != documentv2.VectorFillEvenOdd {
			return sourceMeasure{}, rasterError(CodeSourceInvalid, pathPrefix+"/fill_rule", ErrSourceInvalid)
		}
		if len(path.Commands) == 0 {
			return sourceMeasure{}, rasterError(CodeSourceInvalid, pathPrefix+"/commands", ErrSourceInvalid)
		}
		var okAdd bool
		measure.commands, okAdd = checkedAdd(measure.commands, uint64(len(path.Commands)))
		if !okAdd || measure.commands > limits.MaxCommands {
			return sourceMeasure{}, rasterError(CodeResourceLimit, "source/commands", ErrResourceLimit)
		}

		open := false
		for commandIndex, command := range path.Commands {
			if commandIndex&1023 == 0 {
				if err := contextFailure(ctx, pathPrefix+"/commands"); err != nil {
					return sourceMeasure{}, err
				}
			}
			commandPrefix := fmt.Sprintf("%s/commands/%d", pathPrefix, commandIndex)
			switch command.Op {
			case documentv2.VectorMoveTo:
				if len(command.Points) != 1 {
					return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/points", ErrSourceInvalid)
				}
				if open {
					if err := addConceptualEdge(&measure, limits, commandPrefix); err != nil {
						return sourceMeasure{}, err
					}
				}
				open = true
			case documentv2.VectorLineTo:
				if len(command.Points) != 1 {
					return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/points", ErrSourceInvalid)
				}
				if !open {
					return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/op", ErrSourceInvalid)
				}
				if err := addConceptualEdge(&measure, limits, commandPrefix); err != nil {
					return sourceMeasure{}, err
				}
			case documentv2.VectorClose:
				if command.Points != nil {
					return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/points", ErrSourceInvalid)
				}
				if !open {
					return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/op", ErrSourceInvalid)
				}
				if err := addConceptualEdge(&measure, limits, commandPrefix); err != nil {
					return sourceMeasure{}, err
				}
				open = false
			case documentv2.VectorCubicTo:
				return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/op", ErrSourceInvalid)
			default:
				return sourceMeasure{}, rasterError(CodeSourceInvalid, commandPrefix+"/op", ErrSourceInvalid)
			}
			for pointIndex, point := range command.Points {
				if err := validatePoint(point, fmt.Sprintf("%s/points/%d", commandPrefix, pointIndex), limits.MaxExactBits); err != nil {
					return sourceMeasure{}, err
				}
			}
		}
		if open {
			if err := addConceptualEdge(&measure, limits, pathPrefix+"/commands"); err != nil {
				return sourceMeasure{}, err
			}
		}
	}

	pathBytes, ok := checkedMul(measure.paths, 512)
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/canonical_bytes", ErrResourceLimit)
	}
	commandBytes, ok := checkedMul(measure.commands, 512)
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/canonical_bytes", ErrResourceLimit)
	}
	measure.canonicalUpper, ok = checkedAdd(measure.canonicalUpper, pathBytes)
	if ok {
		measure.canonicalUpper, ok = checkedAdd(measure.canonicalUpper, commandBytes)
	}
	if !ok || measure.canonicalUpper > limits.MaxCanonicalSourceBytes {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/canonical_bytes", ErrResourceLimit)
	}

	measure.samples, ok = checkedMul(pixels, 64)
	if ok {
		measure.samples, ok = checkedMul(measure.samples, measure.paths)
	}
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/sample_count", ErrResourceLimit)
	}
	sampleRows, ok := checkedMul(uint64(height), 8)
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/work_units", ErrResourceLimit)
	}
	edgeRows, ok := checkedMul(measure.conceptualEdges, sampleRows)
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/work_units", ErrResourceLimit)
	}
	edgeRows, ok = checkedMul(edgeRows, uint64(1+ceilLog2(measure.conceptualEdges+1)))
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/work_units", ErrResourceLimit)
	}
	pathPixels, ok := checkedMul(pixels, measure.paths)
	if ok {
		// One unit each for clearing the reusable coverage plane and visiting the complete
		// destination during ordered composition. The 64 path-sample units conservatively cover
		// scan-interval coverage writes as well as semantic sample classification.
		pathPixels, ok = checkedMul(pathPixels, 2)
	}
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/work_units", ErrResourceLimit)
	}
	measure.workUnits, ok = checkedAdd(measure.samples, pathPixels)
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, edgeRows)
	}
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, measure.commands)
	}
	if ok {
		measure.workUnits, ok = checkedAdd(measure.workUnits, measure.conceptualEdges)
	}
	if !ok || measure.workUnits > limits.MaxWorkUnits {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/work_units", ErrResourceLimit)
	}

	staging, ok := checkedMul(pixels, logicalBytesPerStagingPixel+logicalBytesPerCoveragePixel)
	if !ok {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/working_bytes", ErrResourceLimit)
	}
	exactBytes := (uint64(limits.MaxExactBits) + 7) / 8
	edgeUnit, ok := checkedMul(exactBytes, 16)
	if ok {
		edgeUnit, ok = checkedAdd(edgeUnit, 640)
	}
	geometry, geometryOK := checkedMul(measure.conceptualEdges, edgeUnit)
	canonicalWorking, canonicalOK := checkedMul(measure.canonicalUpper, logicalCanonicalizationMultiplier)
	if !ok || !geometryOK || !canonicalOK {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/working_bytes", ErrResourceLimit)
	}
	measure.workingBytes, ok = checkedAdd(staging, geometry)
	if ok {
		measure.workingBytes, ok = checkedAdd(measure.workingBytes, canonicalWorking)
	}
	if !ok || measure.workingBytes > limits.MaxWorkingBytes {
		return sourceMeasure{}, rasterError(CodeResourceLimit, "source/working_bytes", ErrResourceLimit)
	}
	// A valid path contributes at least one conceptual edge, and edgeUnit's conservative
	// per-edge overhead covers this temporary ID index. Build it only after the complete working
	// budget passes so a deliberately tiny MaxWorkingBytes cannot trigger a path-sized map first.
	if err := validateUniquePathIDs(ctx, source.Paths); err != nil {
		return sourceMeasure{}, err
	}
	return measure, contextFailure(ctx, "source")
}

func validateUniquePathIDs(ctx context.Context, paths []documentv2.VectorPath) error {
	ids := make(map[documentv2.ID]struct{}, len(paths))
	for pathIndex, path := range paths {
		if pathIndex&255 == 0 {
			if err := contextFailure(ctx, "source/paths"); err != nil {
				return err
			}
		}
		if _, duplicate := ids[path.ID]; duplicate {
			return rasterError(CodeSourceInvalid, fmt.Sprintf("source/paths/%d/id", pathIndex), ErrSourceInvalid)
		}
		ids[path.ID] = struct{}{}
	}
	return nil
}

func addConceptualEdge(measure *sourceMeasure, limits Limits, path string) error {
	var ok bool
	measure.conceptualEdges, ok = checkedAdd(measure.conceptualEdges, 1)
	if !ok || measure.conceptualEdges > limits.MaxEdges {
		return rasterError(CodeResourceLimit, path+"/edges", ErrResourceLimit)
	}
	return nil
}

func validateRect(value documentv2.Rect, path string, maxBits uint32) error {
	values := []struct {
		name  string
		value documentv2.Rational
	}{
		{"x", value.X}, {"y", value.Y}, {"width", value.Width}, {"height", value.Height},
	}
	for _, item := range values {
		if err := validateRational(item.value, path+"/"+item.name, maxBits); err != nil {
			return err
		}
	}
	return nil
}

func validatePoint(value documentv2.Point, path string, maxBits uint32) error {
	if err := validateRational(value.X, path+"/x", maxBits); err != nil {
		return err
	}
	return validateRational(value.Y, path+"/y", maxBits)
}

func validateRational(value documentv2.Rational, path string, maxBits uint32) error {
	if value.Validate() != nil {
		return rasterError(CodeSourceInvalid, path, ErrSourceInvalid)
	}
	numerator := new(big.Int).SetInt64(value.Numerator)
	numerator.Abs(numerator)
	denominator := new(big.Int).SetInt64(value.Denominator)
	if uint32(numerator.BitLen()) > maxBits || uint32(denominator.BitLen()) > maxBits {
		return rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	return nil
}

func rationalSign(value documentv2.Rational) int {
	if value.Numerator < 0 {
		return -1
	}
	if value.Numerator > 0 {
		return 1
	}
	return 0
}

func ceilLog2(value uint64) uint32 {
	if value <= 1 {
		return 0
	}
	var result uint32
	value--
	for value != 0 {
		result++
		value >>= 1
	}
	return result
}
