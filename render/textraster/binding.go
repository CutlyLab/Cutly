package textraster

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

type rasterPoint struct{ x, y int64 }

type segmentKind uint8

const (
	segmentLine segmentKind = iota + 1
	segmentQuad
)

type rasterSegment struct {
	kind       segmentKind
	p0, p1, p2 rasterPoint
}

type sourceCommand struct {
	op     sfntglyf.OutlineCommandOp
	points [2]sfntglyf.OutlinePoint
	count  uint8
}

type sourceGlyph struct {
	id       uint32
	contours uint64
	commands []sourceCommand
}

type sourceAccess interface {
	receipt() sfntglyf.LayoutSourceReceipt
	unitsPerEm() uint16
	hheaAscender() int16
	glyphCount() uint32
	glyph(context.Context, uint32, uint64) (sourceGlyph, error)
}

type liveSourceAccess struct{ source *sfntglyf.LayoutSource }

func (value liveSourceAccess) receipt() sfntglyf.LayoutSourceReceipt { return value.source.Receipt() }
func (value liveSourceAccess) unitsPerEm() uint16                    { return value.source.UnitsPerEm() }
func (value liveSourceAccess) hheaAscender() int16                   { return value.source.HheaAscender() }
func (value liveSourceAccess) glyphCount() uint32                    { return value.source.GlyphCount() }

func (value liveSourceAccess) glyph(ctx context.Context, glyphID uint32, maxCommands uint64) (sourceGlyph, error) {
	view, ok := value.source.GlyphOutline(glyphID)
	if !ok || view.GlyphID() != glyphID {
		return sourceGlyph{}, rasterError(CodeBindingInvalid, "request/source/glyph", ErrBindingInvalid)
	}
	count := view.CommandCount()
	if count > maxCommands || count > uint64(^uint(0)>>1) {
		return sourceGlyph{}, rasterError(CodeResourceLimit, "request/source/glyph/commands", ErrResourceLimit)
	}
	if err := contextFailure(ctx, "request/source/glyph/commands"); err != nil {
		return sourceGlyph{}, err
	}
	commands := make([]sourceCommand, int(count))
	for index := range commands {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "request/source/glyph/commands"); err != nil {
				return sourceGlyph{}, err
			}
		}
		command, found := view.Command(uint64(index))
		if !found {
			return sourceGlyph{}, rasterError(CodeBindingInvalid, "request/source/glyph/commands", ErrBindingInvalid)
		}
		commands[index].op = command.Op()
		commands[index].count = command.PointCount()
		if commands[index].count > 2 {
			return sourceGlyph{}, rasterError(CodeBindingInvalid, "request/source/glyph/commands/points", ErrBindingInvalid)
		}
		for pointIndex := uint8(0); pointIndex < commands[index].count; pointIndex++ {
			point, found := command.Point(pointIndex)
			if !found {
				return sourceGlyph{}, rasterError(CodeBindingInvalid, "request/source/glyph/commands/points", ErrBindingInvalid)
			}
			commands[index].points[pointIndex] = point
		}
	}
	return sourceGlyph{id: glyphID, contours: view.ContourCount(), commands: commands}, nil
}

type glyphGeometry struct {
	id       uint32
	contours uint64
	commands uint64
	segments []rasterSegment
}

type placedGeometry struct {
	glyphID  uint32
	segments []rasterSegment
}

type rasterMeasure struct {
	width, height  uint32
	pixels         uint64
	placements     uint64
	uniqueGlyphs   uint64
	contours       uint64
	commands       uint64
	uniqueSegments uint64
	segments       uint64
	samples        uint64
	workUnits      uint64
	workingBytes   uint64
	retainedBytes  uint64
	canonicalUpper uint64
}

type boundInput struct {
	sourceReceipt sfntglyf.LayoutSourceReceipt
	layoutReceipt textlayout.Receipt
	fontSize      documentv2.Rational
	measure       rasterMeasure
	placements    []placedGeometry
}

func bindRequest(ctx context.Context, source sourceAccess, layout *textlayout.Layout, fontSize documentv2.Rational, limits Limits) (boundInput, error) {
	if source == nil || layout == nil {
		return boundInput{}, rasterError(CodeInputInvalid, "request", ErrInputInvalid)
	}
	if fontSize.Validate() != nil || fontSize.Numerator <= 0 {
		return boundInput{}, rasterError(CodeInputInvalid, "request/font_size", ErrInputInvalid)
	}
	if layout.FontSize() != fontSize {
		return boundInput{}, rasterError(CodeBindingInvalid, "request/font_size", ErrBindingInvalid)
	}
	view, err := layout.View()
	if err != nil {
		return boundInput{}, rasterError(CodeBindingInvalid, "request/layout", errors.Join(ErrBindingInvalid, err))
	}
	sourceReceipt := source.receipt()
	layoutReceipt := layout.Receipt()
	if err := validateReceiptBinding(source, sourceReceipt, layoutReceipt, view); err != nil {
		return boundInput{}, err
	}
	viewport := view.Viewport()
	pixels, ok := checkedMul(uint64(viewport.WidthPixels), uint64(viewport.HeightPixels))
	if !ok || viewport.WidthPixels == 0 || viewport.HeightPixels == 0 {
		return boundInput{}, rasterError(CodeInputInvalid, "request/layout/viewport", ErrInputInvalid)
	}
	if viewport.WidthPixels > limits.MaxWidth || viewport.HeightPixels > limits.MaxHeight || pixels > limits.MaxPixels {
		return boundInput{}, rasterError(CodeResourceLimit, "request/layout/viewport", ErrResourceLimit)
	}
	placementCount := uint64(view.PlacementCount())
	if placementCount != layoutReceipt.GlyphCount {
		return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/placements", ErrBindingInvalid)
	}
	if placementCount > limits.MaxPlacements {
		return boundInput{}, rasterError(CodeResourceLimit, "request/layout/placements", ErrResourceLimit)
	}
	if layoutReceipt.CommandCount > limits.MaxCommands || layoutReceipt.UniqueGlyphCount > limits.MaxUniqueGlyphs {
		return boundInput{}, rasterError(CodeResourceLimit, "request/layout/outlines", ErrResourceLimit)
	}
	if err := admitBindingLowerBound(viewport.WidthPixels, pixels, placementCount,
		layoutReceipt.UniqueGlyphCount, layoutReceipt.CommandCount, limits); err != nil {
		return boundInput{}, err
	}

	if err := contextFailure(ctx, "request/layout/placements"); err != nil {
		return boundInput{}, err
	}
	placements := make([]textlayout.GlyphPlacement, view.PlacementCount())
	closureMap := make(map[uint32]struct{}, int(layoutReceipt.UniqueGlyphCount))
	for index := range placements {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "request/layout/placements"); err != nil {
				return boundInput{}, err
			}
		}
		placement, placementErr := view.Placement(index)
		if placementErr != nil || placement.GlyphID == 0 || placement.GlyphID >= source.glyphCount() {
			return boundInput{}, rasterError(CodeBindingInvalid, fmt.Sprintf("request/layout/placements/%d", index), errors.Join(ErrBindingInvalid, placementErr))
		}
		placements[index] = placement
		closureMap[placement.GlyphID] = struct{}{}
	}
	closure := make([]uint32, 0, len(closureMap))
	for glyphID := range closureMap {
		closure = append(closure, glyphID)
	}
	sort.Slice(closure, func(left, right int) bool { return closure[left] < closure[right] })
	if uint64(len(closure)) != layoutReceipt.UniqueGlyphCount || len(closure) != view.OutlineCount() {
		return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/glyph_closure", ErrBindingInvalid)
	}
	for index, glyphID := range closure {
		outline, outlineErr := view.OutlineAt(index)
		if outlineErr != nil || outline.GlyphID() != glyphID {
			return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/glyph_closure", errors.Join(ErrBindingInvalid, outlineErr))
		}
	}

	if err := contextFailure(ctx, "request/layout/segment_preflight"); err != nil {
		return boundInput{}, err
	}
	segmentCounts := make([]uint64, len(closure))
	var uniqueSegments uint64
	for index, glyphID := range closure {
		if err := contextFailure(ctx, fmt.Sprintf("request/layout/outlines/%d", index)); err != nil {
			return boundInput{}, err
		}
		layoutOutline, outlineErr := view.Outline(glyphID)
		if outlineErr != nil {
			return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/outlines", errors.Join(ErrBindingInvalid, outlineErr))
		}
		remainingSegments := limits.MaxSegments - uniqueSegments
		expectedSegments, countErr := countLayoutSegments(ctx, layoutOutline, remainingSegments, index)
		if countErr != nil {
			return boundInput{}, countErr
		}
		segmentCounts[index] = expectedSegments
		var addOK bool
		uniqueSegments, addOK = checkedAdd(uniqueSegments, expectedSegments)
		if !addOK || uniqueSegments > limits.MaxSegments {
			return boundInput{}, rasterError(CodeResourceLimit, "request/layout/segments", ErrResourceLimit)
		}
	}
	var placedSegmentCount uint64
	for index, placement := range placements {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "request/layout/segment_preflight"); err != nil {
				return boundInput{}, err
			}
		}
		pos := sort.Search(len(closure), func(candidate int) bool { return closure[candidate] >= placement.GlyphID })
		if pos >= len(closure) || closure[pos] != placement.GlyphID {
			return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/glyph_closure", ErrBindingInvalid)
		}
		count := segmentCounts[pos]
		var addOK bool
		placedSegmentCount, addOK = checkedAdd(placedSegmentCount, count)
		if !addOK || placedSegmentCount > limits.MaxSegments {
			return boundInput{}, rasterError(CodeResourceLimit, "request/segments", ErrResourceLimit)
		}
	}
	measure, err := measureRaster(viewport.WidthPixels, viewport.HeightPixels, pixels, placementCount,
		uint64(len(closure)), layoutReceipt.ContourCount, layoutReceipt.CommandCount, uniqueSegments, placedSegmentCount, limits)
	if err != nil {
		return boundInput{}, err
	}

	exact := exactContext{maxBits: limits.MaxExactBits}
	if err := contextFailure(ctx, "request/layout/outlines/materialize"); err != nil {
		return boundInput{}, err
	}
	geometry := make(map[uint32]glyphGeometry, len(closure))
	var contourCount, commandCount uint64
	for index, glyphID := range closure {
		if err := contextFailure(ctx, "request/layout/outlines/materialize"); err != nil {
			return boundInput{}, err
		}
		layoutOutline, outlineErr := view.Outline(glyphID)
		if outlineErr != nil {
			return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/outlines", errors.Join(ErrBindingInvalid, outlineErr))
		}
		trusted, glyphErr := source.glyph(ctx, glyphID, uint64(layoutOutline.CommandCount()))
		if glyphErr != nil {
			return boundInput{}, glyphErr
		}
		built, buildErr := bindGlyph(ctx, exact, trusted, layoutOutline, fontSize, source.unitsPerEm(), index)
		if buildErr != nil {
			return boundInput{}, buildErr
		}
		if uint64(len(built.segments)) != segmentCounts[index] {
			return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/segments", ErrBindingInvalid)
		}
		geometry[glyphID] = built
		var addOK bool
		contourCount, addOK = checkedAdd(contourCount, built.contours)
		if !addOK {
			return boundInput{}, rasterError(CodeResourceLimit, "request/layout/contours", ErrResourceLimit)
		}
		commandCount, addOK = checkedAdd(commandCount, built.commands)
		if !addOK || commandCount > limits.MaxCommands {
			return boundInput{}, rasterError(CodeResourceLimit, "request/layout/commands", ErrResourceLimit)
		}
	}
	if contourCount != layoutReceipt.ContourCount || commandCount != layoutReceipt.CommandCount {
		return boundInput{}, rasterError(CodeBindingInvalid, "request/layout/outline_counts", ErrBindingInvalid)
	}
	if err := contextFailure(ctx, "request/geometry"); err != nil {
		return boundInput{}, err
	}
	placed := make([]placedGeometry, len(placements))
	for index, placement := range placements {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "request/geometry"); err != nil {
				return boundInput{}, err
			}
		}
		segments, placeErr := placeSegments(ctx, geometry[placement.GlyphID].segments, placement, index)
		if placeErr != nil {
			return boundInput{}, placeErr
		}
		placed[index] = placedGeometry{glyphID: placement.GlyphID, segments: segments}
	}
	return boundInput{sourceReceipt: sourceReceipt, layoutReceipt: layoutReceipt, fontSize: fontSize, measure: measure, placements: placed}, nil
}

func validateReceiptBinding(source sourceAccess, sourceReceipt sfntglyf.LayoutSourceReceipt, layout textlayout.Receipt, view textlayout.ReadView) error {
	viewport := view.Viewport()
	providerDescriptor := sfntglyf.ShapeProviderDescriptor()
	if sourceReceipt.SchemaVersion != sfntglyf.LayoutSourceSchemaVersion ||
		sourceReceipt.Profile != sfntglyf.LayoutSourceReceiptProfileV1 ||
		sourceReceipt.LayoutSourceProfile != sfntglyf.LayoutSourceProfileV1 ||
		sourceReceipt.DescriptorHashProfile != sfntglyf.LayoutSourceDescriptorHashProfileV1 ||
		sourceReceipt.DescriptorHash != sfntglyf.LayoutSourceDescriptorHash() ||
		sourceReceipt.LayoutSourceHashProfile != sfntglyf.LayoutSourceHashProfileV1 ||
		sourceReceipt.LayoutSourceHash == "" ||
		sourceReceipt.ShapeProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
		sourceReceipt.ShapeProviderHashProfile != sfntglyf.ShapeProviderHashProfileV1 ||
		sourceReceipt.ShapeProviderHash == "" ||
		sourceReceipt.DependencyFingerprintProfile != providerDescriptor.DependencyFingerprintProfile ||
		sourceReceipt.DependencyFingerprint != providerDescriptor.DependencyFingerprint ||
		sourceReceipt.LayoutGraphProfile != sfntglyf.LayoutGraphReceiptProfileV1 ||
		sourceReceipt.LayoutGraphHashProfile != sfntglyf.LayoutGraphCanonicalHashProfileV1 ||
		sourceReceipt.LayoutGraphHash == "" ||
		sourceReceipt.OutlineSetProfile != sfntglyf.OutlineSetReceiptProfileV1 ||
		sourceReceipt.OutlineSetHashProfile != sfntglyf.OutlineSetHashProfileV1 ||
		sourceReceipt.OutlineSetHash == "" ||
		sourceReceipt.FontReceiptProfile != sfntglyf.ReceiptProfileV1 ||
		sourceReceipt.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
		sourceReceipt.FontContentHash == "" ||
		sourceReceipt.BindingStatus != sfntglyf.LayoutSourceBindingStatusV1 ||
		sourceReceipt.OutlineProfile != sfntglyf.OutlineSetOutlineProfileV1 ||
		sourceReceipt.CommandStatus != sfntglyf.OutlineSetCommandStatusV1 ||
		sourceReceipt.RoundingStatus != sfntglyf.OutlineSetRoundingStatusV1 ||
		sourceReceipt.HintingStatus != sfntglyf.OutlineSetHintingStatusV1 ||
		sourceReceipt.LayoutStatus != sfntglyf.LayoutSourceLayoutStatusV1 ||
		sourceReceipt.VariableLayoutStatus != sfntglyf.LayoutGraphVariableLayoutStatusV1 ||
		sourceReceipt.RasterStatus != sfntglyf.LayoutSourceRasterStatusV1 ||
		sourceReceipt.CapabilityStatus != sfntglyf.LayoutSourceCapabilityStatusV1 ||
		sourceReceipt.UnitsPerEm < 16 || sourceReceipt.UnitsPerEm > 16_384 ||
		sourceReceipt.UnitsPerEm != source.unitsPerEm() ||
		sourceReceipt.HheaAscender != source.hheaAscender() ||
		sourceReceipt.GlyphCount != source.glyphCount() {
		return rasterError(CodeBindingInvalid, "request/source/receipt", ErrBindingInvalid)
	}
	if layout.SchemaVersion != textlayout.ReceiptSchemaVersion || layout.Profile != textlayout.ReceiptProfileV1 ||
		layout.LayoutProfile != textlayout.ProfileV1 ||
		layout.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		layout.RunInputProfile != documentv2.TextRunInputProfileV1 ||
		layout.ShapedStreamProfile != textlayout.NormalizedShapedStreamProfileV1 ||
		layout.FontProfile != documentv2.TextFontProfileStaticGlyfV1 ||
		layout.OutlineProfile != documentv2.TextOutlineProfileQ26_6V1 ||
		layout.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		layout.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
		layout.UnicodeTableProfile != documentv2.TextUnicodeValidationTableProfileV1 ||
		layout.UnicodeTableHashProfile != documentv2.TextUnicodeValidationTableHashProfileV1 ||
		layout.UnicodeTableHash != documentv2.TextUnicodeValidationTableHashV1 ||
		layout.LayoutHashProfile != textlayout.LayoutHashProfileV1 ||
		layout.GlyphStreamHashProfile != textlayout.GlyphStreamHashProfileV1 ||
		layout.OutlineSetHashProfile != textlayout.OutlineSetHashProfileV1 ||
		layout.PlacementHashProfile != textlayout.PlacementHashProfileV1 ||
		layout.WidthPixels != viewport.WidthPixels || layout.HeightPixels != viewport.HeightPixels {
		return rasterError(CodeBindingInvalid, "request/layout/receipt", ErrBindingInvalid)
	}
	// textlayout freezes implementation identity to the compiled dependency fingerprint. The
	// concrete provider instance is separately bound into LayoutSourceHash and ShapeProviderHash.
	if layout.FontContentHash != sourceReceipt.FontContentHash ||
		layout.ShapeProviderProfile != sourceReceipt.ShapeProviderProfile ||
		layout.ShapeProviderFingerprint != sourceReceipt.DependencyFingerprint {
		return rasterError(CodeBindingInvalid, "request/source/layout_lineage", ErrBindingInvalid)
	}
	return nil
}

// admitBindingLowerBound rejects scalar resource limits before allocating closure, trusted-command
// or geometry storage. The complete charge is recomputed once exact placed-segment counts exist.
func admitBindingLowerBound(width uint32, pixels, placements, uniqueGlyphs, commands uint64, limits Limits) error {
	canonical, ok := checkedMul(pixels, 9)
	if ok {
		canonical, ok = checkedAdd(canonical, 16<<10)
	}
	if !ok || canonical > limits.MaxCanonicalBinaryBytes {
		return rasterError(CodeResourceLimit, "request/canonical_binary_bytes", ErrResourceLimit)
	}
	samples, ok := checkedMul(pixels, 64)
	if !ok {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	commandWork, ok := checkedMul(commands, 16)
	if !ok {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	pixelWork, ok := checkedMul(pixels, 16)
	if !ok {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	scanWork, ok := checkedMul(samples, placements)
	if !ok {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	work, ok := checkedAdd(scanWork, samples)
	if ok {
		work, ok = checkedAdd(work, commandWork)
	}
	if !ok {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	work, ok = checkedAdd(work, pixelWork)
	if !ok {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	work, ok = checkedAdd(work, canonical)
	if !ok || work > limits.MaxWorkUnits {
		return rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	pixelBytes, ok := checkedMul(pixels, 9)
	if !ok {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	placementBytes, ok := checkedMul(placements, 96)
	if !ok {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	closureBytes, ok := checkedMul(uniqueGlyphs, 128)
	if !ok {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	commandBytes, ok := checkedMul(commands, 48)
	if !ok {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	sampleColumns, ok := checkedMul(uint64(width), 8)
	if !ok {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	rowBytes, ok := checkedMul(sampleColumns, 9)
	if !ok {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	working := uint64(64 << 10)
	for _, charge := range []uint64{pixelBytes, placementBytes, closureBytes, commandBytes, rowBytes, 8} {
		working, ok = checkedAdd(working, charge)
		if !ok {
			return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
		}
	}
	if working > limits.MaxWorkingBytes {
		return rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	pixelRetained, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok {
		return rasterError(CodeResourceLimit, "request/retained_bytes", ErrResourceLimit)
	}
	retained, ok := checkedAdd(pixelRetained, 8<<10)
	if !ok || retained > limits.MaxRetainedBytes {
		return rasterError(CodeResourceLimit, "request/retained_bytes", ErrResourceLimit)
	}
	return nil
}

func bindGlyph(ctx context.Context, exact exactContext, trusted sourceGlyph, layout textlayout.OutlineView, fontSize documentv2.Rational, unitsPerEm uint16, outlineIndex int) (glyphGeometry, error) {
	if trusted.id == 0 || trusted.id != layout.GlyphID() || len(trusted.commands) != layout.CommandCount() || unitsPerEm == 0 {
		return glyphGeometry{}, rasterError(CodeBindingInvalid, "request/layout/outlines", ErrBindingInvalid)
	}
	result := glyphGeometry{id: trusted.id, contours: trusted.contours, commands: uint64(len(trusted.commands))}
	var start, current rasterPoint
	open := false
	var observedContours uint64
	for commandIndex, trustedCommand := range trusted.commands {
		if commandIndex&1023 == 0 {
			if err := contextFailure(ctx, "request/layout/outlines/materialize"); err != nil {
				return glyphGeometry{}, err
			}
		}
		path := fmt.Sprintf("request/layout/outlines/%d/commands/%d", outlineIndex, commandIndex)
		layoutCommand, err := layout.Command(commandIndex)
		wantOp, wantPoints := mapOutlineOp(trustedCommand.op)
		if err != nil || wantOp == "" || layoutCommand.Op != wantOp || layoutCommand.PointCount != wantPoints || int(trustedCommand.count) != wantPoints {
			return glyphGeometry{}, rasterError(CodeBindingInvalid, path, errors.Join(ErrBindingInvalid, err))
		}
		var points [2]rasterPoint
		for pointIndex := 0; pointIndex < wantPoints; pointIndex++ {
			sourcePoint := trustedCommand.points[pointIndex]
			x, scaleErr := exact.scaleQ(int64(sourcePoint.X), fontSize, unitsPerEm, path+"/x")
			if scaleErr != nil {
				return glyphGeometry{}, scaleErr
			}
			y, scaleErr := exact.scaleQ(int64(sourcePoint.Y), fontSize, unitsPerEm, path+"/y")
			if scaleErr != nil {
				return glyphGeometry{}, scaleErr
			}
			layoutPoint, pointErr := layout.Point(commandIndex, pointIndex)
			if pointErr != nil || int64(layoutPoint.X) != x || int64(layoutPoint.Y) != y {
				return glyphGeometry{}, rasterError(CodeBindingInvalid, path+"/points", errors.Join(ErrBindingInvalid, pointErr))
			}
			points[pointIndex] = rasterPoint{x: x, y: y}
		}
		switch trustedCommand.op {
		case sfntglyf.OutlineMoveTo:
			if open {
				return glyphGeometry{}, rasterError(CodeBindingInvalid, path, ErrBindingInvalid)
			}
			start, current, open = points[0], points[0], true
			observedContours++
		case sfntglyf.OutlineLineTo:
			if !open {
				return glyphGeometry{}, rasterError(CodeBindingInvalid, path, ErrBindingInvalid)
			}
			result.segments = append(result.segments, rasterSegment{kind: segmentLine, p0: current, p1: points[0]})
			current = points[0]
		case sfntglyf.OutlineQuadTo:
			if !open {
				return glyphGeometry{}, rasterError(CodeBindingInvalid, path, ErrBindingInvalid)
			}
			result.segments = append(result.segments, rasterSegment{kind: segmentQuad, p0: current, p1: points[0], p2: points[1]})
			current = points[1]
		case sfntglyf.OutlineClose:
			if !open {
				return glyphGeometry{}, rasterError(CodeBindingInvalid, path, ErrBindingInvalid)
			}
			result.segments = append(result.segments, rasterSegment{kind: segmentLine, p0: current, p1: start})
			open = false
		}
	}
	if open || observedContours != trusted.contours {
		return glyphGeometry{}, rasterError(CodeBindingInvalid, "request/layout/outlines/contours", ErrBindingInvalid)
	}
	return result, nil
}

func countLayoutSegments(ctx context.Context, layout textlayout.OutlineView, maxSegments uint64, outlineIndex int) (uint64, error) {
	var count uint64
	for commandIndex := 0; commandIndex < layout.CommandCount(); commandIndex++ {
		if commandIndex&1023 == 0 {
			if err := contextFailure(ctx, "request/layout/segment_preflight"); err != nil {
				return 0, err
			}
		}
		command, err := layout.Command(commandIndex)
		if err != nil {
			return 0, rasterError(CodeBindingInvalid, fmt.Sprintf("request/layout/outlines/%d/commands/%d", outlineIndex, commandIndex), errors.Join(ErrBindingInvalid, err))
		}
		switch command.Op {
		case textlayout.MoveTo:
		case textlayout.LineTo, textlayout.QuadTo, textlayout.Close:
			var ok bool
			count, ok = checkedAdd(count, 1)
			if !ok || count > maxSegments {
				return 0, rasterError(CodeResourceLimit, "request/layout/segments", ErrResourceLimit)
			}
		default:
			return 0, rasterError(CodeBindingInvalid, "request/layout/segments", ErrBindingInvalid)
		}
	}
	return count, nil
}

func mapOutlineOp(value sfntglyf.OutlineCommandOp) (textlayout.OutlineCommandOp, int) {
	switch value {
	case sfntglyf.OutlineMoveTo:
		return textlayout.MoveTo, 1
	case sfntglyf.OutlineLineTo:
		return textlayout.LineTo, 1
	case sfntglyf.OutlineQuadTo:
		return textlayout.QuadTo, 2
	case sfntglyf.OutlineClose:
		return textlayout.Close, 0
	default:
		return "", 0
	}
}

func placeSegments(ctx context.Context, source []rasterSegment, placement textlayout.GlyphPlacement, placementIndex int) ([]rasterSegment, error) {
	if err := contextFailure(ctx, "request/geometry/segments"); err != nil {
		return nil, err
	}
	result := make([]rasterSegment, len(source))
	for index, segment := range source {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "request/geometry/segments"); err != nil {
				return nil, err
			}
		}
		result[index].kind = segment.kind
		points := [3]rasterPoint{segment.p0, segment.p1, segment.p2}
		for pointIndex := 0; pointIndex < 3; pointIndex++ {
			if segment.kind == segmentLine && pointIndex == 2 {
				break
			}
			x, ok := checkedAddInt64(int64(placement.OriginX), points[pointIndex].x)
			if !ok {
				return nil, rasterError(CodeExactLimit, fmt.Sprintf("request/placements/%d/segments/%d/x", placementIndex, index), ErrExactLimit)
			}
			y, ok := checkedSubInt64(int64(placement.BaselineY), points[pointIndex].y)
			if !ok {
				return nil, rasterError(CodeExactLimit, fmt.Sprintf("request/placements/%d/segments/%d/y", placementIndex, index), ErrExactLimit)
			}
			placed := rasterPoint{x: x, y: y}
			switch pointIndex {
			case 0:
				result[index].p0 = placed
			case 1:
				result[index].p1 = placed
			case 2:
				result[index].p2 = placed
			}
		}
	}
	return result, nil
}

func measureRaster(width, height uint32, pixels, placements, uniqueGlyphs, contours, commands, uniqueSegments, segments uint64, limits Limits) (rasterMeasure, error) {
	samples, ok := checkedMul(pixels, 64)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/samples", ErrResourceLimit)
	}
	rows, ok := checkedMul(uint64(height), 8)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	sampleColumns, ok := checkedMul(uint64(width), 8)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	rowVisits, ok := checkedMul(rows, segments)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	logColumns := uint64(1)
	for value := sampleColumns; value > 1; value = (value + 1) / 2 {
		logColumns++
	}
	rootFactor, ok := checkedAdd(logColumns, 64)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	rootFactor, ok = checkedMul(rootFactor, 2)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	rootWork, ok := checkedMul(rowVisits, rootFactor)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	scanWork, ok := checkedMul(samples, placements)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	work, ok := checkedAdd(rootWork, scanWork)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	commandWork, ok := checkedMul(commands, 16)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	segmentWork, ok := checkedMul(segments, 8)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	pixelWork, ok := checkedMul(pixels, 16)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	base, ok := checkedAdd(commandWork, segmentWork)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	base, ok = checkedAdd(base, samples)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	base, ok = checkedAdd(base, pixelWork)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	canonicalUpper, ok := checkedMul(pixels, 9)
	if ok {
		canonicalUpper, ok = checkedAdd(canonicalUpper, 16<<10)
	}
	if !ok || canonicalUpper > limits.MaxCanonicalBinaryBytes {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/canonical_binary_bytes", ErrResourceLimit)
	}
	base, ok = checkedAdd(base, canonicalUpper)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	work, ok = checkedAdd(work, base)
	if !ok || work > limits.MaxWorkUnits {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/work", ErrResourceLimit)
	}
	rowBytes, ok := checkedMul(sampleColumns, 9)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	geometryCount, ok := checkedAdd(uniqueSegments, segments)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	geometryBytes, ok := checkedMul(geometryCount, 64)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	pixelWorking, ok := checkedMul(pixels, 9)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	working, ok := checkedAdd(64<<10, pixelWorking)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	rowAndGeometry, ok := checkedAdd(rowBytes, geometryBytes)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	rowAndGeometry, ok = checkedAdd(rowAndGeometry, 8)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	working, ok = checkedAdd(working, rowAndGeometry)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	placementBytes, ok := checkedMul(placements, 96)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	closureBytes, ok := checkedMul(uniqueGlyphs, 128)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	commandBytes, ok := checkedMul(commands, 48)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	for _, charge := range []uint64{placementBytes, closureBytes, commandBytes} {
		working, ok = checkedAdd(working, charge)
		if !ok {
			return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
		}
	}
	if working > limits.MaxWorkingBytes {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/working_bytes", ErrResourceLimit)
	}
	pixelRetained, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/retained_bytes", ErrResourceLimit)
	}
	retained, ok := checkedAdd(pixelRetained, 8<<10)
	if !ok || retained > limits.MaxRetainedBytes {
		return rasterMeasure{}, rasterError(CodeResourceLimit, "request/retained_bytes", ErrResourceLimit)
	}
	return rasterMeasure{width: width, height: height, pixels: pixels, placements: placements,
		uniqueGlyphs: uniqueGlyphs, contours: contours, commands: commands, uniqueSegments: uniqueSegments, segments: segments,
		samples: samples, workUnits: work, workingBytes: working, retainedBytes: retained,
		canonicalUpper: canonicalUpper}, nil
}

func cloneReceiptStrings(value *Receipt) {
	value.Profile = strings.Clone(value.Profile)
	value.RasterProfile = strings.Clone(value.RasterProfile)
	value.DescriptorHashProfile = strings.Clone(value.DescriptorHashProfile)
	value.DescriptorHash = strings.Clone(value.DescriptorHash)
	value.LayoutSourceProfile = strings.Clone(value.LayoutSourceProfile)
	value.LayoutSourceHashProfile = strings.Clone(value.LayoutSourceHashProfile)
	value.LayoutSourceHash = strings.Clone(value.LayoutSourceHash)
	value.ShapeProviderProfile = strings.Clone(value.ShapeProviderProfile)
	value.ShapeProviderHashProfile = strings.Clone(value.ShapeProviderHashProfile)
	value.ShapeProviderHash = strings.Clone(value.ShapeProviderHash)
	value.DependencyFingerprintProfile = strings.Clone(value.DependencyFingerprintProfile)
	value.DependencyFingerprint = strings.Clone(value.DependencyFingerprint)
	value.FontContentHashProfile = strings.Clone(value.FontContentHashProfile)
	value.FontContentHash = strings.Clone(value.FontContentHash)
	value.ShapingProvenanceStatus = strings.Clone(value.ShapingProvenanceStatus)
	value.LayoutReceiptProfile = strings.Clone(value.LayoutReceiptProfile)
	value.LayoutHashProfile = strings.Clone(value.LayoutHashProfile)
	value.LayoutHash = strings.Clone(value.LayoutHash)
	value.GlyphStreamHashProfile = strings.Clone(value.GlyphStreamHashProfile)
	value.GlyphStreamHash = strings.Clone(value.GlyphStreamHash)
	value.OutlineSetHashProfile = strings.Clone(value.OutlineSetHashProfile)
	value.OutlineSetHash = strings.Clone(value.OutlineSetHash)
	value.PlacementHashProfile = strings.Clone(value.PlacementHashProfile)
	value.PlacementHash = strings.Clone(value.PlacementHash)
	value.BindingHashProfile = strings.Clone(value.BindingHashProfile)
	value.BindingHash = strings.Clone(value.BindingHash)
	value.CoverageHashProfile = strings.Clone(value.CoverageHashProfile)
	value.CoverageHash = strings.Clone(value.CoverageHash)
	value.PaintProfile = strings.Clone(value.PaintProfile)
	value.KernelProfile = strings.Clone(value.KernelProfile)
	value.SurfaceProfile = strings.Clone(value.SurfaceProfile)
	value.PixelHashProfile = strings.Clone(value.PixelHashProfile)
	value.PixelHash = strings.Clone(value.PixelHash)
}
