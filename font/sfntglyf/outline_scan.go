package sfntglyf

import (
	"context"
	"encoding/binary"
	"fmt"
)

type outlineScanStats struct {
	emptyRangeGlyphs      uint64
	zeroContourGlyphs     uint64
	simpleGlyphs          uint64
	compoundGlyphs        uint64
	rawPoints             uint64
	rawContours           uint64
	componentRecords      uint64
	instructionGlyphs     uint64
	instructionBytes      uint64
	paddingBytes          uint64
	pointMatchComponents  uint64
	scaledOffsets         uint64
	unscaledOffsets       uint64
	defaultedOffsets      uint64
	useMyMetrics          uint64
	overlapSimpleGlyphs   uint64
	overlapCompoundGlyphs uint64
	expandedPoints        uint64
	expandedContours      uint64
	expandedEdges         uint64
	maxExpandedEdges      uint64
	maxPoints             uint32
	maxContours           uint32
	maxCompositePoints    uint32
	maxCompositeContours  uint32
	maxInstructionBytes   uint32
	maxComponentElements  uint32
	maxComponentDepth     uint32
}

type outlineDeclaredMaxp struct {
	maxPoints            uint32
	maxContours          uint32
	maxCompositePoints   uint32
	maxCompositeContours uint32
	maxInstructionBytes  uint32
	maxComponentElements uint32
	maxComponentDepth    uint32
}

// DecodeOutlineFont validates every loca-delimited glyf body and its complete component graph.
// It deliberately does not synthesize implied points, apply component transforms, expose font
// bytes, emit outline commands, shape text, or authorize a rendering capability.
func DecodeOutlineFont(ctx context.Context, font *Font, limits OutlineLimits) (*OutlineFont, OutlineReceipt, error) {
	if err := contextFailure(ctx, "outline/context"); err != nil {
		return nil, OutlineReceipt{}, err
	}
	if font == nil {
		return nil, OutlineReceipt{}, fontError(CodeOutlineInputInvalid, "outline/font", ErrOutlineInputInvalid)
	}
	normalized, err := normalizeOutlineLimits(limits)
	if err != nil {
		return nil, OutlineReceipt{}, err
	}
	if err := validateOutlineUpstream(font); err != nil {
		return nil, OutlineReceipt{}, err
	}
	glyfRecord, found := findRecord(font.tables, "glyf")
	if !found || glyfRecord.offset != font.glyfOffset {
		return nil, OutlineReceipt{}, fontError(CodeOutlineInvariantFailed, "outline/upstream/glyf", ErrInvariantFailed)
	}
	maxpRecord, found := findRecord(font.tables, "maxp")
	if !found {
		return nil, OutlineReceipt{}, fontError(CodeOutlineInvariantFailed, "outline/upstream/maxp", ErrInvariantFailed)
	}
	maxp := tableBytes(font.encoded, maxpRecord)
	if len(maxp) != 32 {
		return nil, OutlineReceipt{}, fontError(CodeOutlineInvariantFailed, "outline/upstream/maxp", ErrInvariantFailed)
	}
	declaredMaxp := readOutlineDeclaredMaxp(maxp)
	resources := outlineResourceContext{
		upstream: font.receipt, glyfBytes: uint64(glyfRecord.length),
		glyphCount: uint64(font.numGlyphs), limits: normalized,
	}
	stats := outlineScanStats{}
	if err := resources.check(stats, "outline/resources/preflight"); err != nil {
		return nil, OutlineReceipt{}, err
	}
	glyphs, err := allocateOutlineGlyphs(font.numGlyphs)
	if err != nil {
		return nil, OutlineReceipt{}, err
	}
	for glyphID := uint32(0); glyphID < font.numGlyphs; glyphID++ {
		if glyphID%256 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("outline/glyphs/%d", glyphID)); err != nil {
				return nil, OutlineReceipt{}, err
			}
		}
		start, end, ok := font.GlyphRange(glyphID)
		if !ok || end < start || uint64(end) > uint64(glyfRecord.length) {
			return nil, OutlineReceipt{}, fontError(CodeOutlineInvariantFailed, fmt.Sprintf("outline/glyphs/%d/range", glyphID), ErrInvariantFailed)
		}
		absoluteStart := uint64(font.glyfOffset) + uint64(start)
		absoluteEnd := uint64(font.glyfOffset) + uint64(end)
		if absoluteEnd < absoluteStart || absoluteEnd > uint64(len(font.encoded)) || absoluteEnd > uint64(maxInt()) {
			return nil, OutlineReceipt{}, fontError(CodeOutlineInvariantFailed, fmt.Sprintf("outline/glyphs/%d/range", glyphID), ErrInvariantFailed)
		}
		glyph, err := scanOutlineGlyph(ctx, font.encoded[int(absoluteStart):int(absoluteEnd)], glyphID, font.numGlyphs, declaredMaxp, resources, &stats)
		if err != nil {
			return nil, OutlineReceipt{}, err
		}
		glyphs[glyphID] = glyph
	}
	if err := validateOutlineGraph(ctx, glyphs, resources, &stats); err != nil {
		return nil, OutlineReceipt{}, err
	}
	if err := validateOutlineMaxp(maxp, stats); err != nil {
		return nil, OutlineReceipt{}, err
	}
	canonicalLimit, err := outlineCanonicalLimit(font.receipt, uint64(glyfRecord.length), uint64(len(glyphs)), stats, normalized)
	if err != nil {
		return nil, OutlineReceipt{}, err
	}
	graphHash, canonicalBytes, err := hashOutlineGraph(ctx, font, glyphs, canonicalLimit)
	if err != nil {
		return nil, OutlineReceipt{}, err
	}
	work, working, retained, err := outlineResourceCharges(font.receipt, graphHash, uint64(glyfRecord.length), uint64(len(glyphs)), stats, canonicalBytes, normalized)
	if err != nil {
		return nil, OutlineReceipt{}, err
	}
	receipt := OutlineReceipt{
		SchemaVersion: OutlineReceiptSchemaVersion, Profile: OutlineReceiptProfileV1,
		UpstreamProfile: font.receipt.Profile, UpstreamContentProfile: font.receipt.ContentHashProfile,
		UpstreamContentHash:   font.receipt.ContentHash,
		UpstreamRetainedBytes: font.receipt.RetainedBytes,
		DescriptorHashProfile: OutlineDescriptorHashProfileV1, DescriptorHash: OutlineDescriptorHash(),
		GraphHashProfile: OutlineGraphHashProfileV1, GraphHash: graphHash,
		GeometryStatus: OutlineGeometryStatusV1, HintingStatus: OutlineHintingStatusV1,
		LayoutStatus: OutlineLayoutStatusV1, MaxpStatus: OutlineMaxpStatusV1,
		MetricsStatus: OutlineMetricsStatusV1, RoundXYStatus: OutlineRoundXYStatusV1,
		PointMatchStatus: OutlinePointMatchStatusV1, OffsetStatus: OutlineOffsetStatusV1,
		GlyfBytes: uint64(glyfRecord.length), GlyphCount: uint32(len(glyphs)),
		EmptyRangeGlyphCount: stats.emptyRangeGlyphs, ZeroContourGlyphCount: stats.zeroContourGlyphs,
		SimpleGlyphCount: stats.simpleGlyphs, CompoundGlyphCount: stats.compoundGlyphs,
		RawPointCount: stats.rawPoints, RawContourCount: stats.rawContours,
		ComponentRecordCount: stats.componentRecords, InstructionGlyphCount: stats.instructionGlyphs,
		InstructionBytes: stats.instructionBytes, PaddingBytes: stats.paddingBytes,
		PointMatchComponentCount: stats.pointMatchComponents, ScaledOffsetCount: stats.scaledOffsets,
		UnscaledOffsetCount: stats.unscaledOffsets, DefaultedUnscaledCount: stats.defaultedOffsets,
		UseMyMetricsCount: stats.useMyMetrics, OverlapSimpleGlyphCount: stats.overlapSimpleGlyphs,
		OverlapCompoundGlyphCount: stats.overlapCompoundGlyphs,
		ExpandedPointInstances:    stats.expandedPoints, ExpandedContourInstances: stats.expandedContours,
		ExpandedEdgeVisits: stats.expandedEdges, MaxExpandedEdgesPerGlyph: stats.maxExpandedEdges,
		ObservedMaxPoints: stats.maxPoints, ObservedMaxContours: stats.maxContours,
		ObservedMaxCompositePoints:   stats.maxCompositePoints,
		ObservedMaxCompositeContours: stats.maxCompositeContours,
		ObservedMaxInstructionBytes:  stats.maxInstructionBytes,
		ObservedMaxComponentElements: stats.maxComponentElements,
		ObservedMaxComponentDepth:    stats.maxComponentDepth,
		CanonicalBinaryBytes:         canonicalBytes, WorkUnits: work, WorkingBytes: working, RetainedBytes: retained,
	}
	if outlineReceiptOwnedStringBytes(receipt) == 0 {
		return nil, OutlineReceipt{}, fontError(CodeOutlineInvariantFailed, "outline/receipt", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, "outline/publish"); err != nil {
		return nil, OutlineReceipt{}, err
	}
	result := &OutlineFont{font: font, glyphs: glyphs, receipt: receipt}
	return result, result.Receipt(), nil
}

func validateOutlineUpstream(font *Font) error {
	if font.receipt.SchemaVersion != ReceiptSchemaVersion || font.receipt.Profile != ReceiptProfileV1 ||
		font.receipt.ContentHashProfile != ContentHashProfileV1 || font.receipt.ContentHash == "" ||
		font.receipt.GlyfTableStatus != GlyfTableStatusV1 || font.numGlyphs == 0 ||
		uint64(font.numGlyphs) > uint64(maxInt()) || len(font.encoded) == 0 {
		return fontError(CodeOutlineInputInvalid, "outline/font", ErrOutlineInputInvalid)
	}
	return nil
}

func allocateOutlineGlyphs(count uint32) (result []outlineGlyph, err error) {
	if uint64(count) > uint64(maxInt()) {
		return nil, fontError(CodeOutlineResourceLimit, "outline/glyphs", ErrResourceLimit)
	}
	defer func() {
		if recover() != nil {
			result = nil
			err = fontError(CodeAllocationFailed, "outline/glyphs", ErrAllocationFailed)
		}
	}()
	return make([]outlineGlyph, int(count)), nil
}

func validateOutlineMaxp(maxp []byte, stats outlineScanStats) error {
	declaredMaxp := readOutlineDeclaredMaxp(maxp)
	want := []struct {
		path     string
		declared uint32
		got      uint32
	}{
		{"max_points", declaredMaxp.maxPoints, stats.maxPoints},
		{"max_contours", declaredMaxp.maxContours, stats.maxContours},
		{"max_composite_points", declaredMaxp.maxCompositePoints, stats.maxCompositePoints},
		{"max_composite_contours", declaredMaxp.maxCompositeContours, stats.maxCompositeContours},
		{"max_size_of_instructions", declaredMaxp.maxInstructionBytes, stats.maxInstructionBytes},
		{"max_component_elements", declaredMaxp.maxComponentElements, stats.maxComponentElements},
		{"max_component_depth", declaredMaxp.maxComponentDepth, stats.maxComponentDepth},
	}
	for _, value := range want {
		if value.declared != value.got {
			return fontError(CodeOutlineMaxpMismatch, "outline/maxp/"+value.path, ErrOutlineMaxpMismatch)
		}
	}
	return nil
}

func readOutlineDeclaredMaxp(maxp []byte) outlineDeclaredMaxp {
	return outlineDeclaredMaxp{
		maxPoints:            uint32(binary.BigEndian.Uint16(maxp[6:8])),
		maxContours:          uint32(binary.BigEndian.Uint16(maxp[8:10])),
		maxCompositePoints:   uint32(binary.BigEndian.Uint16(maxp[10:12])),
		maxCompositeContours: uint32(binary.BigEndian.Uint16(maxp[12:14])),
		maxInstructionBytes:  uint32(binary.BigEndian.Uint16(maxp[26:28])),
		maxComponentElements: uint32(binary.BigEndian.Uint16(maxp[28:30])),
		maxComponentDepth:    uint32(binary.BigEndian.Uint16(maxp[30:32])),
	}
}
