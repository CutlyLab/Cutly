package sfntglyf

import (
	"context"
	"errors"
	"sort"
)

type gdefFoundation struct {
	majorVersion      uint16
	minorVersion      uint16
	glyphClasses      *gdefClassDef
	markAttachClasses *gdefClassDef
	attachments       []gdefAttachment
	ligatureCarets    []gdefLigatureCarets
	markGlyphSets     []*gdefCoverage
	nodes             []layoutNodeEvidence
	edges             []layoutEdgeEvidence
}

func (*gdefFoundation) layoutFoundationGDEFModel() {}

type gdefAttachment struct {
	glyphID      uint32
	pointIndices []uint16
}

type gdefLigatureCarets struct {
	glyphID uint32
	carets  []gdefCaretValue
}

type gdefCaretKind uint8

const (
	gdefCaretCoordinate gdefCaretKind = iota + 1
	gdefCaretPoint
	gdefCaretCoordinateDevice
)

type gdefCaretValue struct {
	kind       gdefCaretKind
	coordinate int16
	pointIndex uint16
	device     *gdefDevice
}

type gdefDevice struct {
	startPPEm uint16
	endPPEm   uint16
	format    uint16
	deltas    []int16
}

type gdefCoverageRange struct {
	first         uint16
	last          uint16
	coverageIndex uint32
}

// gdefCoverage retains the physical compact representation. Format 2 ranges
// are never expanded into a glyph-sized slice; ordinal lookup is logarithmic.
type gdefCoverage struct {
	format     uint16
	glyphCount uint32
	glyphs     []uint16
	ranges     []gdefCoverageRange
}

func (value *gdefCoverage) glyphAt(index uint64) (uint16, bool) {
	if value == nil || index >= uint64(value.glyphCount) {
		return 0, false
	}
	if value.format == 1 {
		if index >= uint64(len(value.glyphs)) {
			return 0, false
		}
		return value.glyphs[index], true
	}
	rangeIndex := sort.Search(len(value.ranges), func(rangeIndex int) bool {
		rangeValue := value.ranges[rangeIndex]
		length := uint32(rangeValue.last) - uint32(rangeValue.first) + 1
		return uint64(rangeValue.coverageIndex+length) > index
	})
	if rangeIndex >= len(value.ranges) {
		return 0, false
	}
	rangeValue := value.ranges[rangeIndex]
	if index < uint64(rangeValue.coverageIndex) {
		return 0, false
	}
	delta := uint32(index) - rangeValue.coverageIndex
	if delta > uint32(rangeValue.last)-uint32(rangeValue.first) {
		return 0, false
	}
	return uint16(uint32(rangeValue.first) + delta), true
}

type gdefClassRange struct {
	first uint16
	last  uint16
	class uint16
}

// gdefClassDef preserves format 1's explicit values or format 2's compact
// ranges. Missing glyphs have implicit class zero.
type gdefClassDef struct {
	format     uint16
	startGlyph uint16
	values     []uint16
	ranges     []gdefClassRange
}

func (value *gdefClassDef) classAt(glyphID uint32) uint16 {
	if value == nil || glyphID > uint32(^uint16(0)) {
		return 0
	}
	if value.format == 1 {
		if glyphID < uint32(value.startGlyph) {
			return 0
		}
		index := glyphID - uint32(value.startGlyph)
		if index >= uint32(len(value.values)) {
			return 0
		}
		return value.values[index]
	}
	index := sort.Search(len(value.ranges), func(index int) bool {
		return uint32(value.ranges[index].last) >= glyphID
	})
	if index >= len(value.ranges) || glyphID < uint32(value.ranges[index].first) {
		return 0
	}
	return value.ranges[index].class
}

type gdefAttachList struct{ attachments []gdefAttachment }
type gdefLigCaretList struct{ ligatures []gdefLigatureCarets }
type gdefLigGlyph struct{ carets []gdefCaretValue }
type gdefMarkGlyphSets struct{ sets []*gdefCoverage }
type gdefHeader struct{ majorVersion, minorVersion uint16 }

// decodeGDEFFoundation consumes an operation-local, already table-bounded GDEF byte slice. It
// publishes only normalized owned values and deterministic span evidence: neither the input bytes,
// the point-count slice, the reader nor its maps are retained.
func decodeGDEFFoundation(ctx context.Context, gdef []byte, numGlyphs uint32, expandedRealPointCounts []uint32, limits layoutDecodeLimits) (foundation *gdefFoundation, stats layoutDecodeStats, err error) {
	defer func() {
		if recover() != nil {
			foundation = nil
			stats = layoutDecodeStats{}
			err = fontError(CodeAllocationFailed, "layout/gdef", ErrAllocationFailed)
		}
		if err != nil {
			foundation = nil
			stats = layoutDecodeStats{}
		}
	}()
	return decodeGDEFFoundationInner(ctx, gdef, numGlyphs, expandedRealPointCounts, limits)
}

func decodeGDEFFoundationInner(ctx context.Context, gdef []byte, numGlyphs uint32, expandedRealPointCounts []uint32, limits layoutDecodeLimits) (*gdefFoundation, layoutDecodeStats, error) {
	if err := contextFailure(ctx, "layout/gdef/context"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	normalizedLimits, err := normalizeLayoutDecodeLimits(limits)
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if numGlyphs > 0xffff || uint64(numGlyphs) > uint64(maxInt()) || uint64(len(gdef)) > uint64(^uint32(0)) || len(expandedRealPointCounts) != int(numGlyphs) {
		return nil, layoutDecodeStats{}, fontError(codeLayoutInputInvalid, "layout/gdef/input", errLayoutInputInvalid)
	}
	if uint64(len(gdef)) > normalizedLimits.maxTableBytes {
		return nil, layoutDecodeStats{}, fontError(codeLayoutResource, "layout/gdef/input/table_bytes", ErrResourceLimit)
	}
	if len(gdef) == 0 {
		return &gdefFoundation{}, layoutDecodeStats{}, nil
	}
	meter := newLayoutResourceMeter(uint64(len(gdef)), uint64(numGlyphs), normalizedLimits)
	reader := newLayoutReader(ctx, gdef, meter)
	major, err := reader.u16(0, "layout/gdef/major_version")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	minor, err := reader.u16(2, "layout/gdef/minor_version")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if major != 1 {
		return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gdef/version", errLayoutUnsupported)
	}
	var headerSize uint64
	switch minor {
	case 0:
		headerSize = 12
	case 2:
		headerSize = 14
	case 3:
		headerSize = 18
	default:
		return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gdef/version", errLayoutUnsupported)
	}
	if _, err := reader.bytes(0, headerSize, "layout/gdef/header"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	header, alias, err := reader.graph.begin(nil, layoutEdgeRef{}, 0, headerSize, layoutSpanGDEFHeader, layoutDecodeParams{first: uint64(major), second: uint64(minor)}, "layout/gdef/header")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if alias {
		return nil, layoutDecodeStats{}, fontError(codeLayoutInvariant, "layout/gdef/header/alias", ErrInvariantFailed)
	}
	if err := meter.reserveRetained(128, "layout/gdef/foundation"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	result := &gdefFoundation{majorVersion: major, minorVersion: minor}

	glyphClassOffset, err := reader.u16(4, "layout/gdef/glyph_class_def_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	attachOffset, err := reader.u16(6, "layout/gdef/attach_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	ligCaretOffset, err := reader.u16(8, "layout/gdef/lig_caret_list_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	markAttachOffset, err := reader.u16(10, "layout/gdef/mark_attach_class_def_offset")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	if glyphClassOffset != 0 {
		classes, err := reader.parseClassDef(header, layoutEdgeRef{role: layoutEdgeGlyphClassDef, rawOffset: uint32(glyphClassOffset)}, uint64(glyphClassOffset), layoutSpanGlyphClassDef, 4, "layout/gdef/glyph_class_def")
		if err != nil {
			return nil, layoutDecodeStats{}, err
		}
		result.glyphClasses = classes
	}
	if attachOffset != 0 {
		attach, err := reader.parseAttachList(header, layoutEdgeRef{role: layoutEdgeAttachList, rawOffset: uint32(attachOffset)}, uint64(attachOffset), numGlyphs, expandedRealPointCounts, "layout/gdef/attach_list")
		if err != nil {
			return nil, layoutDecodeStats{}, err
		}
		result.attachments = attach.attachments
	}
	if ligCaretOffset != 0 {
		ligatures, err := reader.parseLigCaretList(header, layoutEdgeRef{role: layoutEdgeLigCaretList, rawOffset: uint32(ligCaretOffset)}, uint64(ligCaretOffset), numGlyphs, expandedRealPointCounts, "layout/gdef/lig_caret_list")
		if err != nil {
			return nil, layoutDecodeStats{}, err
		}
		result.ligatureCarets = ligatures.ligatures
	}
	if markAttachOffset != 0 {
		classes, err := reader.parseClassDef(header, layoutEdgeRef{role: layoutEdgeMarkAttachClassDef, rawOffset: uint32(markAttachOffset)}, uint64(markAttachOffset), layoutSpanMarkAttachClassDef, 255, "layout/gdef/mark_attach_class_def")
		if err != nil {
			return nil, layoutDecodeStats{}, err
		}
		result.markAttachClasses = classes
	}
	if minor >= 2 {
		markSetsOffset, err := reader.u16(12, "layout/gdef/mark_glyph_sets_offset")
		if err != nil {
			return nil, layoutDecodeStats{}, err
		}
		if markSetsOffset != 0 {
			sets, err := reader.parseMarkGlyphSets(header, layoutEdgeRef{role: layoutEdgeMarkGlyphSets, rawOffset: uint32(markSetsOffset)}, uint64(markSetsOffset), numGlyphs, "layout/gdef/mark_glyph_sets")
			if err != nil {
				return nil, layoutDecodeStats{}, err
			}
			result.markGlyphSets = sets.sets
		}
	}
	if minor == 3 {
		itemVarStoreOffset, err := reader.u32(14, "layout/gdef/item_var_store_offset")
		if err != nil {
			return nil, layoutDecodeStats{}, err
		}
		if itemVarStoreOffset != 0 {
			return nil, layoutDecodeStats{}, fontError(codeLayoutUnsupported, "layout/gdef/item_var_store", errLayoutUnsupported)
		}
	}
	if err := reader.graph.complete(header, &gdefHeader{majorVersion: major, minorVersion: minor}, "layout/gdef/header"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	nodes, edges, err := reader.graph.validateAndSnapshot("layout/gdef/spans")
	if err != nil {
		return nil, layoutDecodeStats{}, err
	}
	result.nodes, result.edges = nodes, edges
	if err := contextFailure(ctx, "layout/gdef/publish"); err != nil {
		return nil, layoutDecodeStats{}, err
	}
	return result, meter.stats, nil
}

func (value *layoutReader) parseCoverage(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gdefCoverage, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+2, path+"/count")
	if err != nil {
		return nil, err
	}
	var end uint64
	var glyphCount uint64
	switch format {
	case 1:
		end, err = layoutSpanEnd(start, 4, uint64(count), 2, path)
		glyphCount = uint64(count)
	case 2:
		end, err = layoutSpanEnd(start, 4, uint64(count), 6, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	if format == 2 {
		var previousEnd uint16
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			record := start + 4 + 6*index
			first, _ := value.u16(record, path+"/ranges/start")
			last, _ := value.u16(record+2, path+"/ranges/end")
			coverageIndex, _ := value.u16(record+4, path+"/ranges/coverage_index")
			if first > last || uint32(last) >= numGlyphs || (index != 0 && first <= previousEnd) || uint64(coverageIndex) != glyphCount {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/ranges", index), errLayoutInvalid)
			}
			length := uint64(last) - uint64(first) + 1
			var ok bool
			glyphCount, ok = checkedAdd(glyphCount, length)
			if !ok || glyphCount > uint64(numGlyphs) {
				return nil, fontError(codeLayoutInvalid, path+"/ranges/cardinality", errLayoutInvalid)
			}
			previousEnd = last
		}
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanCoverage, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefCoverage](node, path)
	}
	if err := value.meter.chargeCoverageTable(path); err != nil {
		return nil, err
	}
	if format == 1 {
		if err := value.meter.observe(&value.meter.stats.coverageFormat1Glyphs, glyphCount, path+"/format1_glyphs"); err != nil {
			return nil, err
		}
	} else if err := value.meter.observe(&value.meter.stats.coverageRanges, uint64(count), path+"/range_records"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeCoverage(glyphCount, path); err != nil {
		return nil, err
	}
	retainedCount := glyphCount
	elementBytes := uint64(2)
	if format == 2 {
		retainedCount = uint64(count)
		elementBytes = 8
	}
	bytes, err := layoutRetainedArrayBytes(retainedCount, elementBytes, path)
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(bytes+32, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/compact/allocate"); err != nil {
		return nil, err
	}
	result := &gdefCoverage{format: format, glyphCount: uint32(glyphCount)}
	if format == 1 {
		result.glyphs, err = layoutMakeSlice[uint16](glyphCount, path+"/glyphs")
		if err != nil {
			return nil, err
		}
		var previous uint16
		for index := uint64(0); index < glyphCount; index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			glyph, _ := value.u16(start+4+2*index, path+"/glyphs")
			if uint32(glyph) >= numGlyphs || (index != 0 && glyph <= previous) {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/glyphs", index), errLayoutInvalid)
			}
			result.glyphs[index], previous = glyph, glyph
		}
	} else {
		result.ranges, err = layoutMakeSlice[gdefCoverageRange](uint64(count), path+"/ranges")
		if err != nil {
			return nil, err
		}
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			record := start + 4 + 6*index
			first, _ := value.u16(record, path+"/ranges/start")
			last, _ := value.u16(record+2, path+"/ranges/end")
			coverageIndex, _ := value.u16(record+4, path+"/ranges/coverage_index")
			result.ranges[index] = gdefCoverageRange{
				first: first, last: last, coverageIndex: uint32(coverageIndex),
			}
		}
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseClassDef(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, kind layoutSpanKind, maxClass uint16, path string) (*gdefClassDef, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	var end uint64
	var assignments uint64
	switch format {
	case 1:
		count, readErr := value.u16(start+4, path+"/glyph_count")
		if readErr != nil {
			return nil, readErr
		}
		end, err = layoutSpanEnd(start, 6, uint64(count), 2, path)
		assignments = uint64(count)
	case 2:
		count, readErr := value.u16(start+2, path+"/range_count")
		if readErr != nil {
			return nil, readErr
		}
		end, err = layoutSpanEnd(start, 4, uint64(count), 6, path)
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	if format == 1 {
		first, _ := value.u16(start+2, path+"/start_glyph")
		count, _ := value.u16(start+4, path+"/glyph_count")
		limit, ok := checkedAdd(uint64(first), uint64(count))
		if !ok || limit > value.meter.numGlyphs {
			return nil, fontError(codeLayoutInvalid, path+"/glyph_range", errLayoutInvalid)
		}
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			class, _ := value.u16(start+6+2*index, path+"/classes")
			if class > maxClass {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/classes", index), errLayoutInvalid)
			}
		}
	} else {
		count, _ := value.u16(start+2, path+"/range_count")
		var previousEnd uint16
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			record := start + 4 + 6*index
			first, _ := value.u16(record, path+"/ranges/start")
			last, _ := value.u16(record+2, path+"/ranges/end")
			class, _ := value.u16(record+4, path+"/ranges/class")
			if first > last || uint64(last) >= value.meter.numGlyphs || class > maxClass || (index != 0 && first <= previousEnd) {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/ranges", index), errLayoutInvalid)
			}
			length := uint64(last) - uint64(first) + 1
			var ok bool
			assignments, ok = checkedAdd(assignments, length)
			if !ok || assignments > value.meter.numGlyphs {
				return nil, fontError(codeLayoutInvalid, path+"/ranges/cardinality", errLayoutInvalid)
			}
			previousEnd = last
		}
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, kind, layoutDecodeParams{first: value.meter.numGlyphs, second: uint64(maxClass)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefClassDef](node, path)
	}
	if err := value.meter.chargeClassDefTable(path); err != nil {
		return nil, err
	}
	if format == 1 {
		if err := value.meter.observe(&value.meter.stats.classFormat1Values, assignments, path+"/format1_values"); err != nil {
			return nil, err
		}
	} else {
		rangeCount, _ := value.u16(start+2, path+"/range_count")
		if err := value.meter.observe(&value.meter.stats.classRanges, uint64(rangeCount), path+"/range_records"); err != nil {
			return nil, err
		}
	}
	if err := value.meter.chargeClasses(assignments, path); err != nil {
		return nil, err
	}
	var retainedCount, elementBytes uint64
	if format == 1 {
		retainedCount = assignments
		elementBytes = 2
	} else {
		rangeCount, _ := value.u16(start+2, path+"/range_count")
		retainedCount = uint64(rangeCount)
		elementBytes = 8
	}
	bytes, err := layoutRetainedArrayBytes(retainedCount, elementBytes, path)
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(bytes+32, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/compact/allocate"); err != nil {
		return nil, err
	}
	result := &gdefClassDef{format: format}
	if format == 1 {
		first, _ := value.u16(start+2, path+"/start_glyph")
		count, _ := value.u16(start+4, path+"/glyph_count")
		result.startGlyph = first
		result.values, err = layoutMakeSlice[uint16](uint64(count), path+"/classes")
		if err != nil {
			return nil, err
		}
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			class, _ := value.u16(start+6+2*index, path+"/classes")
			result.values[index] = class
		}
	} else {
		count, _ := value.u16(start+2, path+"/range_count")
		result.ranges, err = layoutMakeSlice[gdefClassRange](uint64(count), path+"/ranges")
		if err != nil {
			return nil, err
		}
		for index := uint64(0); index < uint64(count); index++ {
			if err := value.checkContext(index, path); err != nil {
				return nil, err
			}
			record := start + 4 + 6*index
			first, _ := value.u16(record, path+"/ranges/start")
			last, _ := value.u16(record+2, path+"/ranges/end")
			class, _ := value.u16(record+4, path+"/ranges/class")
			result.ranges[index] = gdefClassRange{first: first, last: last, class: class}
		}
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

// parseLayoutClassDef is the tag-agnostic common ClassDef decoder used by
// contextual GSUB/GPOS. Its decode identity has one fixed full uint16 class
// domain; reference-specific domain checks happen after decoding so an exact
// physical alias cannot become a false type-pun. GDEF keeps its frozen
// role-specific wrapper above, preserving c-a canonical bytes.
func (value *layoutReader) parseLayoutClassDef(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*layoutClassDef, error) {
	return value.parseClassDef(parent, edge, start, layoutSpanClassDef, ^uint16(0), path)
}

func (value *layoutReader) parseAttachList(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, path string) (*gdefAttachList, error) {
	coverageRaw, err := value.u16(start, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+2, path+"/glyph_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 4, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanAttachList, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefAttachList](node, path)
	}
	if err := value.meter.observe(&value.meter.stats.attachLists, 1, path+"/table_count"); err != nil {
		return nil, err
	}
	if err := value.meter.observe(&value.meter.stats.attachmentGlyphReferences, uint64(count), path+"/glyph_references"); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageRaw), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeAttachCoverage, rawOffset: uint32(coverageRaw)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	if uint64(coverage.glyphCount) != uint64(count) {
		return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
	}
	retained, err := layoutRetainedArrayBytes(uint64(count), 32, path+"/attachments")
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(retained+24, path+"/attachments"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/attachments/allocate"); err != nil {
		return nil, err
	}
	attachments, err := layoutMakeSlice[gdefAttachment](uint64(count), path+"/attachments")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+4+2*index, path+"/attach_point_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/attach_point_offsets", index))
		if err != nil {
			return nil, err
		}
		glyphID, found := coverage.glyphAt(index)
		if !found {
			return nil, fontError(codeLayoutInvariant, layoutPath(path+"/coverage_lookup", index), ErrInvariantFailed)
		}
		points, err := value.parseAttachPoint(node, layoutEdgeRef{role: layoutEdgeAttachPoint, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, layoutPath(path+"/attach_points", index))
		if err != nil {
			return nil, err
		}
		for pointIndex, point := range points {
			if uint32(point) >= pointCounts[glyphID] {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/attach_points/bounds", uint64(pointIndex)), errLayoutInvalid)
			}
		}
		attachments[index] = gdefAttachment{glyphID: uint32(glyphID), pointIndices: points}
	}
	result := &gdefAttachList{attachments: attachments}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseAttachPoint(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) ([]uint16, error) {
	count, err := value.u16(start, path+"/point_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanAttachPoint, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[[]uint16](node, path)
	}
	if err := value.meter.chargeAttachPointTable(path); err != nil {
		return nil, err
	}
	if err := value.meter.chargeAttachmentPoints(uint64(count), path); err != nil {
		return nil, err
	}
	bytes, err := layoutRetainedArrayBytes(uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(bytes+24, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/points/allocate"); err != nil {
		return nil, err
	}
	points, err := layoutMakeSlice[uint16](uint64(count), path+"/points")
	if err != nil {
		return nil, err
	}
	var previous uint16
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		point, _ := value.u16(start+2+2*index, path+"/points")
		if index != 0 && point <= previous {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/points", index), errLayoutInvalid)
		}
		points[index], previous = point, point
	}
	if err := value.graph.complete(node, points, path); err != nil {
		return nil, err
	}
	return points, nil
}

func (value *layoutReader) parseLigCaretList(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, pointCounts []uint32, path string) (*gdefLigCaretList, error) {
	coverageRaw, err := value.u16(start, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+2, path+"/lig_glyph_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 4, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanLigCaretList, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefLigCaretList](node, path)
	}
	if err := value.meter.observe(&value.meter.stats.ligCaretLists, 1, path+"/table_count"); err != nil {
		return nil, err
	}
	if err := value.meter.observe(&value.meter.stats.ligatureGlyphReferences, uint64(count), path+"/glyph_references"); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageRaw), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeLigCaretCoverage, rawOffset: uint32(coverageRaw)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	if uint64(coverage.glyphCount) != uint64(count) {
		return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
	}
	retained, err := layoutRetainedArrayBytes(uint64(count), 32, path+"/ligatures")
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(retained+24, path+"/ligatures"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/ligatures/allocate"); err != nil {
		return nil, err
	}
	ligatures, err := layoutMakeSlice[gdefLigatureCarets](uint64(count), path+"/ligatures")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+4+2*index, path+"/lig_glyph_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/lig_glyph_offsets", index))
		if err != nil {
			return nil, err
		}
		caretCount, err := value.u16(childStart, layoutPath(path+"/lig_glyphs/caret_count", index))
		if err != nil {
			return nil, err
		}
		if err := value.meter.chargeCarets(uint64(caretCount), layoutPath(path+"/lig_glyphs", index)); err != nil {
			return nil, err
		}
		glyphID, found := coverage.glyphAt(index)
		if !found {
			return nil, fontError(codeLayoutInvariant, layoutPath(path+"/coverage_lookup", index), ErrInvariantFailed)
		}
		ligGlyph, err := value.parseLigGlyph(node, layoutEdgeRef{role: layoutEdgeLigGlyph, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, layoutPath(path+"/lig_glyphs", index))
		if err != nil {
			return nil, err
		}
		if len(ligGlyph.carets) != int(caretCount) {
			return nil, fontError(codeLayoutInvariant, layoutPath(path+"/lig_glyphs/caret_count", index), ErrInvariantFailed)
		}
		for caretIndex, caret := range ligGlyph.carets {
			if caret.kind == gdefCaretPoint {
				if err := value.meter.observe(&value.meter.stats.caretPointReferences, 1, layoutPath(path+"/lig_glyphs/point_references", uint64(caretIndex))); err != nil {
					return nil, err
				}
				if uint32(caret.pointIndex) >= pointCounts[glyphID] {
					return nil, fontError(codeLayoutInvalid, layoutPath(path+"/lig_glyphs/point_bounds", uint64(caretIndex)), errLayoutInvalid)
				}
			}
		}
		ligatures[index] = gdefLigatureCarets{glyphID: uint32(glyphID), carets: ligGlyph.carets}
	}
	result := &gdefLigCaretList{ligatures: ligatures}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseLigGlyph(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*gdefLigGlyph, error) {
	count, err := value.u16(start, path+"/caret_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanLigGlyph, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefLigGlyph](node, path)
	}
	if err := value.meter.chargeLigGlyphTable(path); err != nil {
		return nil, err
	}
	retained, err := layoutRetainedArrayBytes(uint64(count), 32, path+"/carets")
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(retained+24, path+"/carets"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/carets/allocate"); err != nil {
		return nil, err
	}
	carets, err := layoutMakeSlice[gdefCaretValue](uint64(count), path+"/carets")
	if err != nil {
		return nil, err
	}
	allDirect := true
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u16(start+2+2*index, path+"/caret_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/caret_offsets", index))
		if err != nil {
			return nil, err
		}
		caret, err := value.parseCaretValue(node, layoutEdgeRef{role: layoutEdgeCaretValue, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, layoutPath(path+"/carets", index))
		if err != nil {
			return nil, err
		}
		carets[index] = *caret
		if caret.kind == gdefCaretPoint {
			allDirect = false
		}
	}
	if allDirect {
		for index := 1; index < len(carets); index++ {
			if err := value.checkContext(uint64(index), path+"/carets/order"); err != nil {
				return nil, err
			}
			if carets[index].coordinate <= carets[index-1].coordinate {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/carets/order", uint64(index)), errLayoutInvalid)
			}
		}
	}
	result := &gdefLigGlyph{carets: carets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseCaretValue(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*gdefCaretValue, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	var size uint64
	switch format {
	case 1:
		size = 4
	case 2:
		size = 4
	case 3:
		size = 6
	default:
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, size, 0, 0, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, size, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanCaretValue, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefCaretValue](node, path)
	}
	if err := value.meter.observe(&value.meter.stats.caretValueTables, 1, path+"/table_count"); err != nil {
		return nil, err
	}
	result := &gdefCaretValue{}
	switch format {
	case 1:
		result.kind = gdefCaretCoordinate
		result.coordinate, _ = value.i16(start+2, path+"/coordinate")
	case 2:
		result.kind = gdefCaretPoint
		result.pointIndex, _ = value.u16(start+2, path+"/point_index")
	case 3:
		result.kind = gdefCaretCoordinateDevice
		result.coordinate, _ = value.i16(start+2, path+"/coordinate")
		rawOffset, _ := value.u16(start+4, path+"/device_offset")
		childStart, err := value.childOffset(start, uint64(rawOffset), path+"/device_offset")
		if err != nil {
			return nil, err
		}
		result.device, err = value.parseDevice(node, layoutEdgeRef{role: layoutEdgeCaretDevice, rawOffset: uint32(rawOffset)}, childStart, path+"/device")
		if err != nil {
			return nil, err
		}
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseDevice(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*gdefDevice, error) {
	if _, err := value.bytes(start, 6, path); err != nil {
		return nil, err
	}
	first, _ := value.u16(start, path+"/first")
	second, _ := value.u16(start+2, path+"/second")
	format, _ := value.u16(start+4, path+"/delta_format")
	if format == 0x8000 {
		return nil, fontError(codeLayoutUnsupported, path+"/variation_index", errLayoutUnsupported)
	}
	var bits uint64
	switch format {
	case 1:
		bits = 2
	case 2:
		bits = 4
	case 3:
		bits = 8
	default:
		return nil, fontError(codeLayoutInvalid, path+"/delta_format", errLayoutInvalid)
	}
	if second < first {
		return nil, fontError(codeLayoutInvalid, path+"/size_range", errLayoutInvalid)
	}
	count := uint64(second) - uint64(first) + 1
	perWord := uint64(16) / bits
	wordCount, ok := checkedAdd(count, perWord-1)
	if !ok {
		return nil, fontError(codeLayoutResource, path+"/word_count", ErrResourceLimit)
	}
	wordCount /= perWord
	end, err := layoutSpanEnd(start, 6, wordCount, 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanDevice, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefDevice](node, path)
	}
	if err := value.meter.chargeDeviceTable(path); err != nil {
		return nil, err
	}
	if err := value.meter.observe(&value.meter.stats.deviceWords, wordCount, path+"/word_count"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeDeviceDeltas(count, path); err != nil {
		return nil, err
	}
	retained, err := layoutRetainedArrayBytes(count, 2, path+"/deltas")
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(retained+32, path+"/deltas"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/deltas/allocate"); err != nil {
		return nil, err
	}
	deltas, err := layoutMakeSlice[int16](count, path+"/deltas")
	if err != nil {
		return nil, err
	}
	mask := uint16((uint32(1) << bits) - 1)
	sign := uint16(uint32(1) << (bits - 1))
	modulus := int32(uint32(1) << bits)
	for index := uint64(0); index < count; index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		wordIndex := index / perWord
		word, _ := value.u16(start+6+2*wordIndex, path+"/packed_deltas")
		shift := uint(16 - bits*((index%perWord)+1))
		raw := (word >> shift) & mask
		decoded := int32(raw)
		if raw&sign != 0 {
			decoded -= modulus
		}
		deltas[index] = int16(decoded)
	}
	if used := count % perWord; used != 0 {
		paddingBits := (perWord - used) * bits
		paddingMask := uint16((uint32(1) << paddingBits) - 1)
		last, _ := value.u16(start+6+2*(wordCount-1), path+"/packed_deltas")
		if last&paddingMask != 0 {
			return nil, fontError(codeLayoutInvalid, path+"/padding", errLayoutInvalid)
		}
	}
	result := &gdefDevice{startPPEm: first, endPPEm: second, format: format, deltas: deltas}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseMarkGlyphSets(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gdefMarkGlyphSets, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	if format != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	count, err := value.u16(start+2, path+"/set_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 4, uint64(count), 4, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanMarkGlyphSets, layoutDecodeParams{first: uint64(numGlyphs)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gdefMarkGlyphSets](node, path)
	}
	if err := value.meter.observe(&value.meter.stats.markGlyphSetsTables, 1, path+"/table_count"); err != nil {
		return nil, err
	}
	if err := value.meter.observe(&value.meter.stats.markGlyphSetReferences, uint64(count), path+"/set_references"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeMarkGlyphSets(uint64(count), path); err != nil {
		return nil, err
	}
	retained, err := layoutRetainedArrayBytes(uint64(count), 8, path+"/sets")
	if err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(retained+24, path+"/sets"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/sets/allocate"); err != nil {
		return nil, err
	}
	sets, err := layoutMakeSlice[*gdefCoverage](uint64(count), path+"/sets")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path); err != nil {
			return nil, err
		}
		rawOffset, _ := value.u32(start+4+4*index, path+"/coverage_offsets")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/coverage_offsets", index))
		if err != nil {
			return nil, err
		}
		coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeMarkSetCoverage, ordinal: uint32(index), rawOffset: rawOffset}, childStart, numGlyphs, layoutPath(path+"/sets", index))
		if err != nil {
			return nil, err
		}
		sets[index] = coverage
	}
	result := &gdefMarkGlyphSets{sets: sets}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func layoutUnsupportedVariation(err error) bool {
	return errors.Is(err, errLayoutUnsupported)
}

func (value layoutDecodeStats) foundationResourceStats() layoutFoundationResourceStats {
	return layoutFoundationResourceStats{
		layoutTableBytes: value.gdefBytes,
		nodes:            value.nodes, edges: value.edges, aliasReferences: value.aliasReferences,
		maxStructuralDepth: value.maxStructuralDepth, directBytes: value.directBytes,
		coverageTables: value.coverageTables, coverageFormat1Glyphs: value.coverageFormat1Glyphs,
		coverageRanges: value.coverageRanges, coveredGlyphs: value.coverageGlyphs,
		classDefTables: value.classDefTables, classFormat1Values: value.classFormat1Values,
		classRanges: value.classRanges, classAssignments: value.classAssignments,
		deviceTables: value.deviceTables, deviceDeltaValues: value.deviceDeltas,
		deviceWords: value.deviceWords, attachLists: value.attachLists,
		attachPointTables: value.attachPointTables, attachmentGlyphReferences: value.attachmentGlyphReferences,
		attachmentPoints: value.attachmentPoints, ligCaretLists: value.ligCaretLists,
		ligGlyphTables: value.ligGlyphTables, ligatureGlyphReferences: value.ligatureGlyphReferences,
		caretValueTables: value.caretValueTables, caretValueReferences: value.caretValueReferences,
		caretPointReferences: value.caretPointReferences, markGlyphSetsTables: value.markGlyphSetsTables,
		markGlyphSetReferences: value.markGlyphSetReferences,
	}
}
