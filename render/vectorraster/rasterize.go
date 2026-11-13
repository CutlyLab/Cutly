package vectorraster

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const channelOne uint64 = 65_535

type ratPoint struct {
	x big.Rat
	y big.Rat
}

type rasterEdge struct {
	x1       big.Rat
	y1       big.Rat
	x2       big.Rat
	y2       big.Rat
	startRow int
	endRow   int
	winding  int64
	ordinal  int
}

type rasterPath struct {
	rule  documentv2.VectorFillRule
	paint cpucompositor.Pixel
	edges []rasterEdge
}

type crossing struct {
	x       big.Rat
	winding int64
	ordinal int
}

// Rasterize validates and borrows source until return, then atomically returns one immutable
// Surface and its receipt. The caller must not mutate source's slices or pointers concurrently.
// Every failure returns a nil Surface and the zero Receipt.
func Rasterize(ctx context.Context, source documentv2.VectorSource, limits Limits) (
	surface *cpucompositor.Surface, receipt Receipt, err error,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			surface = nil
			receipt = Receipt{}
			err = rasterError(CodeAllocationFailed, "raster", fmt.Errorf("%w: %v", ErrAllocationFailed, recovered))
		}
		if err != nil {
			surface = nil
			receipt = Receipt{}
		}
	}()

	if ctx == nil {
		return nil, Receipt{}, rasterError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, Receipt{}, err
	}
	if err := validateLimits(limits); err != nil {
		return nil, Receipt{}, err
	}
	measure, err := validateAndMeasure(ctx, source, limits)
	if err != nil {
		return nil, Receipt{}, err
	}

	canonicalSource, err := videoedit.CanonicalJSON(source)
	if err != nil {
		return nil, Receipt{}, rasterError(CodeSourceInvalid, "source/canonical", errors.Join(ErrSourceInvalid, err))
	}
	if uint64(len(canonicalSource)) > limits.MaxCanonicalSourceBytes || uint64(len(canonicalSource)) > measure.canonicalUpper {
		return nil, Receipt{}, rasterError(CodeResourceLimit, "source/canonical_bytes", ErrResourceLimit)
	}
	sourceHash, err := hashCanonicalSource(ctx, canonicalSource)
	if err != nil {
		return nil, Receipt{}, err
	}
	canonicalBytes := uint64(len(canonicalSource))
	canonicalSource = nil
	if err := contextFailure(ctx, "source/canonical"); err != nil {
		return nil, Receipt{}, err
	}

	exact := exactContext{maxBits: limits.MaxExactBits}
	transform, err := newMeetTransform(exact, source)
	if err != nil {
		return nil, Receipt{}, err
	}
	paths, effectiveEdges, err := buildGeometry(ctx, exact, transform, source, int(measure.height)*8)
	if err != nil {
		return nil, Receipt{}, err
	}
	if err := contextFailure(ctx, "raster/preflight"); err != nil {
		return nil, Receipt{}, err
	}

	pixels := make([]cpucompositor.Pixel, int(measure.pixels))
	coverage := make([]uint8, int(measure.pixels))
	width := int(measure.width)
	height := int(measure.height)
	for pathIndex := range paths {
		if err := contextFailure(ctx, fmt.Sprintf("source/paths/%d", pathIndex)); err != nil {
			return nil, Receipt{}, err
		}
		clear(coverage)
		if err := rasterizePath(ctx, exact, &paths[pathIndex], width, height, coverage); err != nil {
			return nil, Receipt{}, err
		}
		if err := compositeCoverage(ctx, pixels, coverage, paths[pathIndex].paint, pathIndex); err != nil {
			return nil, Receipt{}, err
		}
	}
	if err := contextFailure(ctx, "raster/pixels"); err != nil {
		return nil, Receipt{}, err
	}
	pixelHash, err := hashPixels(ctx, pixels)
	if err != nil {
		return nil, Receipt{}, err
	}
	if err := contextFailure(ctx, "surface"); err != nil {
		return nil, Receipt{}, err
	}

	surfaceLimits := cpucompositor.Limits{
		MaxWidth: width, MaxHeight: height, MaxBytes: measure.retainedBytes,
	}
	result, err := cpucompositor.NewSurfaceFromPixelsWithLimits(width, height, pixels, surfaceLimits)
	if err != nil {
		return nil, Receipt{}, surfaceConstructionError(err)
	}
	if err := contextFailure(ctx, "raster/result"); err != nil {
		return nil, Receipt{}, err
	}

	resultReceipt := Receipt{
		SchemaVersion: ReceiptSchemaVersion, Profile: ReceiptProfileV1, RasterProfile: ProfileV1,
		SourceProfile: source.Profile, SourceHashProfile: SourceHashProfileV1,
		SourceSemanticHash: sourceHash, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		WidthPixels: measure.width, HeightPixels: measure.height, PixelHash: pixelHash,
		PathCount: measure.paths, CommandCount: measure.commands,
		ConceptualEdgeCount: measure.conceptualEdges, EffectiveEdgeCount: effectiveEdges,
		SampleCount: measure.samples, WorkUnits: measure.workUnits,
		CanonicalSourceBytes: canonicalBytes, WorkingBytes: measure.workingBytes,
		RetainedBytes: measure.retainedBytes,
	}
	return result, resultReceipt, nil
}

func surfaceConstructionError(err error) error {
	if errors.Is(err, cpucompositor.ErrAllocationFailed) {
		return rasterError(CodeAllocationFailed, "surface", errors.Join(ErrAllocationFailed, err))
	}
	return rasterError(CodeSurfaceFailed, "surface", errors.Join(ErrSurfaceFailed, err))
}

type meetTransform struct {
	exact   exactContext
	scale   big.Rat
	offsetX big.Rat
	offsetY big.Rat
	viewX   big.Rat
	viewY   big.Rat
}

func newMeetTransform(exact exactContext, source documentv2.VectorSource) (meetTransform, error) {
	viewX, err := exact.fromModel(source.ViewBox.X, "source/view_box/x")
	if err != nil {
		return meetTransform{}, err
	}
	viewY, err := exact.fromModel(source.ViewBox.Y, "source/view_box/y")
	if err != nil {
		return meetTransform{}, err
	}
	viewWidth, err := exact.fromModel(source.ViewBox.Width, "source/view_box/width")
	if err != nil {
		return meetTransform{}, err
	}
	viewHeight, err := exact.fromModel(source.ViewBox.Height, "source/view_box/height")
	if err != nil {
		return meetTransform{}, err
	}
	viewportWidth, err := exact.integer(uint64(source.Viewport.WidthPixels), "source/viewport/width_pixels")
	if err != nil {
		return meetTransform{}, err
	}
	viewportHeight, err := exact.integer(uint64(source.Viewport.HeightPixels), "source/viewport/height_pixels")
	if err != nil {
		return meetTransform{}, err
	}
	scaleX, err := exact.quo(&viewportWidth, &viewWidth, "raster/meet/scale_x")
	if err != nil {
		return meetTransform{}, err
	}
	scaleY, err := exact.quo(&viewportHeight, &viewHeight, "raster/meet/scale_y")
	if err != nil {
		return meetTransform{}, err
	}
	var scale big.Rat
	scale.Set(&scaleX)
	if scaleY.Cmp(&scaleX) < 0 {
		scale.Set(&scaleY)
	}
	scaledWidth, err := exact.mul(&scale, &viewWidth, "raster/meet/scaled_width")
	if err != nil {
		return meetTransform{}, err
	}
	scaledHeight, err := exact.mul(&scale, &viewHeight, "raster/meet/scaled_height")
	if err != nil {
		return meetTransform{}, err
	}
	gapX, err := exact.sub(&viewportWidth, &scaledWidth, "raster/meet/gap_x")
	if err != nil {
		return meetTransform{}, err
	}
	gapY, err := exact.sub(&viewportHeight, &scaledHeight, "raster/meet/gap_y")
	if err != nil {
		return meetTransform{}, err
	}
	offsetX, err := exact.half(&gapX, "raster/meet/offset_x")
	if err != nil {
		return meetTransform{}, err
	}
	offsetY, err := exact.half(&gapY, "raster/meet/offset_y")
	if err != nil {
		return meetTransform{}, err
	}
	return meetTransform{
		exact: exact, scale: scale, offsetX: offsetX, offsetY: offsetY,
		viewX: viewX, viewY: viewY,
	}, nil
}

func (transform meetTransform) point(value documentv2.Point, path string) (ratPoint, error) {
	sourceX, err := transform.exact.fromModel(value.X, path+"/x")
	if err != nil {
		return ratPoint{}, err
	}
	sourceY, err := transform.exact.fromModel(value.Y, path+"/y")
	if err != nil {
		return ratPoint{}, err
	}
	relativeX, err := transform.exact.sub(&sourceX, &transform.viewX, path+"/mapped_x")
	if err != nil {
		return ratPoint{}, err
	}
	relativeY, err := transform.exact.sub(&sourceY, &transform.viewY, path+"/mapped_y")
	if err != nil {
		return ratPoint{}, err
	}
	scaledX, err := transform.exact.mul(&transform.scale, &relativeX, path+"/mapped_x")
	if err != nil {
		return ratPoint{}, err
	}
	scaledY, err := transform.exact.mul(&transform.scale, &relativeY, path+"/mapped_y")
	if err != nil {
		return ratPoint{}, err
	}
	mappedX, err := transform.exact.add(&transform.offsetX, &scaledX, path+"/mapped_x")
	if err != nil {
		return ratPoint{}, err
	}
	mappedY, err := transform.exact.add(&transform.offsetY, &scaledY, path+"/mapped_y")
	if err != nil {
		return ratPoint{}, err
	}
	return ratPoint{x: mappedX, y: mappedY}, nil
}

func buildGeometry(
	ctx context.Context,
	exact exactContext,
	transform meetTransform,
	source documentv2.VectorSource,
	sampleRows int,
) ([]rasterPath, uint64, error) {
	paths := make([]rasterPath, len(source.Paths))
	var effective uint64
	var visits uint64
	for pathIndex, sourcePath := range source.Paths {
		path := &paths[pathIndex]
		path.rule = sourcePath.FillRule
		path.paint = color.PaintRGBA16ToPixel(color.RGBA16Input{
			R: sourcePath.Fill.R, G: sourcePath.Fill.G, B: sourcePath.Fill.B, A: sourcePath.Fill.A,
		})
		path.edges = make([]rasterEdge, 0, len(sourcePath.Commands)+1)
		var start, current ratPoint
		open := false
		for commandIndex, command := range sourcePath.Commands {
			visits++
			if visits&1023 == 0 {
				if err := contextFailure(ctx, "raster/geometry"); err != nil {
					return nil, 0, err
				}
			}
			commandPath := fmt.Sprintf("source/paths/%d/commands/%d", pathIndex, commandIndex)
			switch command.Op {
			case documentv2.VectorMoveTo:
				if open {
					added, err := appendRasterEdge(exact, &path.edges, current, start, sampleRows, commandPath+"/implicit_close")
					if err != nil {
						return nil, 0, err
					}
					if added {
						effective++
					}
				}
				mapped, err := transform.point(command.Points[0], commandPath+"/points/0")
				if err != nil {
					return nil, 0, err
				}
				start = clonePoint(mapped)
				current = clonePoint(mapped)
				open = true
			case documentv2.VectorLineTo:
				mapped, err := transform.point(command.Points[0], commandPath+"/points/0")
				if err != nil {
					return nil, 0, err
				}
				added, err := appendRasterEdge(exact, &path.edges, current, mapped, sampleRows, commandPath)
				if err != nil {
					return nil, 0, err
				}
				if added {
					effective++
				}
				current = clonePoint(mapped)
			case documentv2.VectorCubicTo:
				// Flatten the cubic bezier (current, cp1, cp2, endpoint) into line
				// segments, then append edges. The segment count adapts to the curve's
				// bounding-box diagonal so long curves get proportionally more segments.
				cp1, err := transform.point(command.Points[0], commandPath+"/points/0")
				if err != nil {
					return nil, 0, err
				}
				cp2, err := transform.point(command.Points[1], commandPath+"/points/1")
				if err != nil {
					return nil, 0, err
				}
				end, err := transform.point(command.Points[2], commandPath+"/points/2")
				if err != nil {
					return nil, 0, err
				}
				segments := cubicSegmentCount(exact, current, cp1, cp2, end, DefaultMaxCubicSegments)
				previous := clonePoint(current)
				for segment := 1; segment <= segments; segment++ {
					t := big.NewRat(int64(segment), int64(segments))
					point := cubicPoint(exact, current, cp1, cp2, end, t)
					added, err := appendRasterEdge(exact, &path.edges, previous, point, sampleRows, commandPath)
					if err != nil {
						return nil, 0, err
					}
					if added {
						effective++
					}
					previous = clonePoint(point)
				}
				current = clonePoint(end)
			case documentv2.VectorClose:
				added, err := appendRasterEdge(exact, &path.edges, current, start, sampleRows, commandPath)
				if err != nil {
					return nil, 0, err
				}
				if added {
					effective++
				}
				open = false
			}
		}
		if open {
			added, err := appendRasterEdge(exact, &path.edges, current, start, sampleRows, fmt.Sprintf("source/paths/%d/implicit_close", pathIndex))
			if err != nil {
				return nil, 0, err
			}
			if added {
				effective++
			}
		}
	}
	return paths, effective, contextFailure(ctx, "raster/geometry")
}

func clonePoint(value ratPoint) ratPoint {
	var result ratPoint
	result.x.Set(&value.x)
	result.y.Set(&value.y)
	return result
}

func appendRasterEdge(
	exact exactContext,
	edges *[]rasterEdge,
	first, second ratPoint,
	sampleRows int,
	path string,
) (bool, error) {
	comparison := first.y.Cmp(&second.y)
	if comparison == 0 {
		return false, nil
	}
	low, high := &first.y, &second.y
	winding := int64(1)
	if comparison > 0 {
		low, high = &second.y, &first.y
		winding = -1
	}
	start, err := firstSampleAtOrAbove(exact, low, sampleRows, path+"/start_row")
	if err != nil {
		return false, err
	}
	end, err := firstSampleAtOrAbove(exact, high, sampleRows, path+"/end_row")
	if err != nil {
		return false, err
	}
	if start >= end {
		return false, nil
	}
	var edge rasterEdge
	edge.x1.Set(&first.x)
	edge.y1.Set(&first.y)
	edge.x2.Set(&second.x)
	edge.y2.Set(&second.y)
	edge.startRow = start
	edge.endRow = end
	edge.winding = winding
	edge.ordinal = len(*edges)
	*edges = append(*edges, edge)
	return true, nil
}

func firstSampleAtOrAbove(exact exactContext, value *big.Rat, count int, path string) (int, error) {
	return exact.sampleLowerBound(value, count, path)
}

func rasterizePath(
	ctx context.Context,
	exact exactContext,
	path *rasterPath,
	width, height int,
	coverage []uint8,
) error {
	if len(path.edges) == 0 {
		return nil
	}
	startOrder := make([]int, len(path.edges))
	endOrder := make([]int, len(path.edges))
	positions := make([]int, len(path.edges))
	for index := range path.edges {
		startOrder[index] = index
		endOrder[index] = index
		positions[index] = -1
	}
	sort.Slice(startOrder, func(left, right int) bool {
		leftEdge, rightEdge := path.edges[startOrder[left]], path.edges[startOrder[right]]
		if leftEdge.startRow != rightEdge.startRow {
			return leftEdge.startRow < rightEdge.startRow
		}
		return leftEdge.ordinal < rightEdge.ordinal
	})
	sort.Slice(endOrder, func(left, right int) bool {
		leftEdge, rightEdge := path.edges[endOrder[left]], path.edges[endOrder[right]]
		if leftEdge.endRow != rightEdge.endRow {
			return leftEdge.endRow < rightEdge.endRow
		}
		return leftEdge.ordinal < rightEdge.ordinal
	})
	active := make([]int, 0, len(path.edges))
	crossings := make([]crossing, 0, len(path.edges))
	startCursor, endCursor := 0, 0
	sampleRows := height * 8
	sampleColumns := width * 8
	var coverageWrites uint64
	for row := 0; row < sampleRows; row++ {
		if row&63 == 0 {
			if err := contextFailure(ctx, "raster/scanlines"); err != nil {
				return err
			}
		}
		for endCursor < len(endOrder) && path.edges[endOrder[endCursor]].endRow == row {
			edgeIndex := endOrder[endCursor]
			position := positions[edgeIndex]
			if position < 0 || position >= len(active) {
				return rasterError(CodeSourceInvalid, "raster/active_edges", ErrSourceInvalid)
			}
			last := len(active) - 1
			moved := active[last]
			active[position] = moved
			positions[moved] = position
			active = active[:last]
			positions[edgeIndex] = -1
			endCursor++
		}
		for startCursor < len(startOrder) && path.edges[startOrder[startCursor]].startRow == row {
			edgeIndex := startOrder[startCursor]
			positions[edgeIndex] = len(active)
			active = append(active, edgeIndex)
			startCursor++
		}
		if len(active) == 0 {
			continue
		}
		y, err := exact.sample(row, "raster/sample_y")
		if err != nil {
			return err
		}
		crossings = crossings[:0]
		for activeIndex, edgeIndex := range active {
			if activeIndex&1023 == 0 {
				if err := contextFailure(ctx, "raster/crossings"); err != nil {
					return err
				}
			}
			edge := &path.edges[edgeIndex]
			x, err := crossingX(exact, edge, &y, "raster/crossing_x")
			if err != nil {
				return err
			}
			crossings = append(crossings, crossing{x: x, winding: edge.winding, ordinal: edge.ordinal})
		}
		sort.Slice(crossings, func(left, right int) bool {
			comparison := crossings[left].x.Cmp(&crossings[right].x)
			if comparison != 0 {
				return comparison > 0
			}
			return crossings[left].ordinal < crossings[right].ordinal
		})

		winding := int64(0)
		parity := false
		for index := 0; index < len(crossings); {
			groupEnd := index + 1
			for groupEnd < len(crossings) && crossings[groupEnd].x.Cmp(&crossings[index].x) == 0 {
				groupEnd++
			}
			threshold, err := greatestSampleLessThan(exact, &crossings[index].x, sampleColumns, "raster/crossing_threshold")
			if err != nil {
				return err
			}
			for item := index; item < groupEnd; item++ {
				winding += crossings[item].winding
				parity = !parity
			}
			nextThreshold := -1
			if groupEnd < len(crossings) {
				nextThreshold, err = greatestSampleLessThan(exact, &crossings[groupEnd].x, sampleColumns, "raster/crossing_threshold")
				if err != nil {
					return err
				}
			}
			inside := parity
			if path.rule == documentv2.VectorFillNonZero {
				inside = winding != 0
			}
			low, high := nextThreshold+1, threshold
			if low < 0 {
				low = 0
			}
			if high >= sampleColumns {
				high = sampleColumns - 1
			}
			if inside && low <= high {
				if err := addCoverageRange(ctx, coverage, width, row/8, low, high, &coverageWrites); err != nil {
					return err
				}
			}
			index = groupEnd
		}
	}
	return contextFailure(ctx, "raster/scanlines")
}

func crossingX(exact exactContext, edge *rasterEdge, y *big.Rat, path string) (big.Rat, error) {
	dy, err := exact.sub(&edge.y2, &edge.y1, path+"/dy")
	if err != nil {
		return big.Rat{}, err
	}
	yOffset, err := exact.sub(y, &edge.y1, path+"/y_offset")
	if err != nil {
		return big.Rat{}, err
	}
	t, err := exact.quo(&yOffset, &dy, path+"/t")
	if err != nil {
		return big.Rat{}, err
	}
	dx, err := exact.sub(&edge.x2, &edge.x1, path+"/dx")
	if err != nil {
		return big.Rat{}, err
	}
	xOffset, err := exact.mul(&dx, &t, path+"/x_offset")
	if err != nil {
		return big.Rat{}, err
	}
	return exact.add(&edge.x1, &xOffset, path)
}

func greatestSampleLessThan(exact exactContext, value *big.Rat, count int, path string) (int, error) {
	return exact.greatestSampleBelow(value, count, path)
}

func addCoverageRange(
	ctx context.Context,
	coverage []uint8,
	width, pixelY, lowSample, highSample int,
	writes *uint64,
) error {
	firstPixel, lastPixel := lowSample/8, highSample/8
	rowOffset := pixelY * width
	for pixelX := firstPixel; pixelX <= lastPixel; pixelX++ {
		(*writes)++
		if *writes&4095 == 0 {
			if err := contextFailure(ctx, "raster/coverage"); err != nil {
				return err
			}
		}
		start := lowSample
		if pixelStart := pixelX * 8; start < pixelStart {
			start = pixelStart
		}
		end := highSample
		if pixelEnd := pixelX*8 + 7; end > pixelEnd {
			end = pixelEnd
		}
		increment := uint8(end - start + 1)
		index := rowOffset + pixelX
		if uint16(coverage[index])+uint16(increment) > 64 {
			return rasterError(CodeSourceInvalid, "raster/coverage", ErrSourceInvalid)
		}
		coverage[index] += increment
	}
	return nil
}

func compositeCoverage(
	ctx context.Context,
	pixels []cpucompositor.Pixel,
	coverage []uint8,
	paint cpucompositor.Pixel,
	pathIndex int,
) error {
	for index, covered := range coverage {
		if index&4095 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("source/paths/%d/composite", pathIndex)); err != nil {
				return err
			}
		}
		if covered == 0 || paint.A == 0 {
			continue
		}
		source := cpucompositor.Pixel{
			R: roundRatioEven(uint64(paint.R)*uint64(covered), 64),
			G: roundRatioEven(uint64(paint.G)*uint64(covered), 64),
			B: roundRatioEven(uint64(paint.B)*uint64(covered), 64),
			A: roundRatioEven(uint64(paint.A)*uint64(covered), 64),
		}
		pixels[index] = sourceOver(pixels[index], source)
	}
	return contextFailure(ctx, fmt.Sprintf("source/paths/%d/composite", pathIndex))
}

func roundRatioEven(numerator, denominator uint64) uint16 {
	quotient := numerator / denominator
	remainder := numerator % denominator
	doubled := remainder * 2
	if doubled > denominator || doubled == denominator && quotient&1 == 1 {
		quotient++
	}
	return uint16(quotient)
}

func sourceOver(destination, source cpucompositor.Pixel) cpucompositor.Pixel {
	inverseAlpha := channelOne - uint64(source.A)
	sourceChannels := [4]uint16{source.R, source.G, source.B, source.A}
	destinationChannels := [4]uint16{destination.R, destination.G, destination.B, destination.A}
	var output [4]uint16
	for index := range output {
		numerator := uint64(sourceChannels[index])*channelOne + uint64(destinationChannels[index])*inverseAlpha
		output[index] = roundRatioEven(numerator, channelOne)
	}
	return cpucompositor.Pixel{R: output[0], G: output[1], B: output[2], A: output[3]}
}

func hashPixels(ctx context.Context, pixels []cpucompositor.Pixel) (string, error) {
	hash := sha256.New()
	var encoded [8]byte
	for index, pixel := range pixels {
		if index&4095 == 0 {
			if err := contextFailure(ctx, "raster/pixel_hash"); err != nil {
				return "", err
			}
		}
		binary.BigEndian.PutUint16(encoded[0:2], pixel.R)
		binary.BigEndian.PutUint16(encoded[2:4], pixel.G)
		binary.BigEndian.PutUint16(encoded[4:6], pixel.B)
		binary.BigEndian.PutUint16(encoded[6:8], pixel.A)
		_, _ = hash.Write(encoded[:])
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

const canonicalHashChunkBytes = 64 << 10

func hashCanonicalSource(ctx context.Context, canonical []byte) (string, error) {
	hash := sha256.New()
	for offset := 0; offset < len(canonical); offset += canonicalHashChunkBytes {
		if err := contextFailure(ctx, "source/semantic_hash"); err != nil {
			return "", err
		}
		end := offset + canonicalHashChunkBytes
		if end > len(canonical) {
			end = len(canonical)
		}
		_, _ = hash.Write(canonical[offset:end])
	}
	if err := contextFailure(ctx, "source/semantic_hash"); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// cubicSegmentCount returns the number of line segments to approximate the cubic curve,
// proportional to the bounding-box diagonal, clamped to maxSegments.
func cubicSegmentCount(exact exactContext, p0, p1, p2, p3 ratPoint, maxSegments int) int {
	minX, maxX := &p0.x, &p0.x
	minY, maxY := &p0.y, &p0.y
	for _, p := range []*ratPoint{&p1, &p2, &p3} {
		if p.x.Cmp(minX) < 0 {
			minX = &p.x
		}
		if p.x.Cmp(maxX) > 0 {
			maxX = &p.x
		}
		if p.y.Cmp(minY) < 0 {
			minY = &p.y
		}
		if p.y.Cmp(maxY) > 0 {
			maxY = &p.y
		}
	}
	width := new(big.Rat).Sub(maxX, minX)
	height := new(big.Rat).Sub(maxY, minY)
	diagonal := new(big.Rat).Add(width, height)
	// Scale: at least 4 segments, proportional to diagonal, at most maxSegments.
	if diagonal.Cmp(big.NewRat(4, 1)) <= 0 {
		return 4
	}
	// Use the diagonal as a rough segment count.
	num := diagonal.Num()
	den := diagonal.Denom()
	estimate := num.Int64() * 8 / den.Int64()
	if estimate < 4 {
		estimate = 4
	}
	if estimate > int64(maxSegments) {
		estimate = int64(maxSegments)
	}
	return int(estimate)
}

// cubicPoint evaluates the cubic bezier at parameter t (0..1) using exact rational arithmetic.
func cubicPoint(exact exactContext, p0, p1, p2, p3 ratPoint, t *big.Rat) ratPoint {
	// B(t) = (1-t)^3*P0 + 3*(1-t)^2*t*P1 + 3*(1-t)*t^2*P2 + t^3*P3
	oneMinusT := new(big.Rat).Sub(big.NewRat(1, 1), t)
	oneMinusT2 := new(big.Rat).Mul(oneMinusT, oneMinusT)
	oneMinusT3 := new(big.Rat).Mul(oneMinusT2, oneMinusT)
	t2 := new(big.Rat).Mul(t, t)
	t3 := new(big.Rat).Mul(t2, t)

	w0 := oneMinusT3                      // (1-t)^3
	w1 := new(big.Rat).Mul(oneMinusT2, t) // 3*(1-t)^2*t
	w1.Mul(w1, big.NewRat(3, 1))
	w2 := new(big.Rat).Mul(oneMinusT, t2) // 3*(1-t)*t^2
	w2.Mul(w2, big.NewRat(3, 1))
	w3 := t3 // t^3

	var result ratPoint
	result.x.Add(
		new(big.Rat).Mul(w0, &p0.x),
		new(big.Rat).Add(
			new(big.Rat).Mul(w1, &p1.x),
			new(big.Rat).Add(
				new(big.Rat).Mul(w2, &p2.x),
				new(big.Rat).Mul(w3, &p3.x),
			),
		),
	)
	result.y.Add(
		new(big.Rat).Mul(w0, &p0.y),
		new(big.Rat).Add(
			new(big.Rat).Mul(w1, &p1.y),
			new(big.Rat).Add(
				new(big.Rat).Mul(w2, &p2.y),
				new(big.Rat).Mul(w3, &p3.y),
			),
		),
	)
	return result
}
