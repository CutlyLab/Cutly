package sfntglyf

type OutlineGlyphKind string

const (
	OutlineGlyphEmptyRange OutlineGlyphKind = "empty_range"
	OutlineGlyphSimple     OutlineGlyphKind = "simple"
	OutlineGlyphCompound   OutlineGlyphKind = "compound"
)

type OutlineReceipt struct {
	SchemaVersion          int    `json:"schema_version"`
	Profile                string `json:"profile"`
	UpstreamProfile        string `json:"upstream_profile"`
	UpstreamContentProfile string `json:"upstream_content_profile"`
	UpstreamContentHash    string `json:"upstream_content_hash"`
	UpstreamRetainedBytes  uint64 `json:"upstream_retained_bytes"`
	DescriptorHashProfile  string `json:"descriptor_hash_profile"`
	DescriptorHash         string `json:"descriptor_hash"`
	GraphHashProfile       string `json:"graph_hash_profile"`
	GraphHash              string `json:"graph_hash"`
	GeometryStatus         string `json:"geometry_status"`
	HintingStatus          string `json:"hinting_status"`
	LayoutStatus           string `json:"layout_status"`
	MaxpStatus             string `json:"maxp_status"`
	MetricsStatus          string `json:"metrics_status"`
	RoundXYStatus          string `json:"round_xy_status"`
	PointMatchStatus       string `json:"point_match_status"`
	OffsetStatus           string `json:"offset_status"`

	GlyfBytes                 uint64 `json:"glyf_bytes"`
	GlyphCount                uint32 `json:"glyph_count"`
	EmptyRangeGlyphCount      uint64 `json:"empty_range_glyph_count"`
	ZeroContourGlyphCount     uint64 `json:"zero_contour_glyph_count"`
	SimpleGlyphCount          uint64 `json:"simple_glyph_count"`
	CompoundGlyphCount        uint64 `json:"compound_glyph_count"`
	RawPointCount             uint64 `json:"raw_point_count"`
	RawContourCount           uint64 `json:"raw_contour_count"`
	ComponentRecordCount      uint64 `json:"component_record_count"`
	InstructionGlyphCount     uint64 `json:"instruction_glyph_count"`
	InstructionBytes          uint64 `json:"instruction_bytes"`
	PaddingBytes              uint64 `json:"padding_bytes"`
	PointMatchComponentCount  uint64 `json:"point_match_component_count"`
	ScaledOffsetCount         uint64 `json:"scaled_offset_count"`
	UnscaledOffsetCount       uint64 `json:"unscaled_offset_count"`
	DefaultedUnscaledCount    uint64 `json:"defaulted_unscaled_count"`
	UseMyMetricsCount         uint64 `json:"use_my_metrics_count"`
	OverlapSimpleGlyphCount   uint64 `json:"overlap_simple_glyph_count"`
	OverlapCompoundGlyphCount uint64 `json:"overlap_compound_glyph_count"`

	ExpandedPointInstances   uint64 `json:"expanded_point_instances"`
	ExpandedContourInstances uint64 `json:"expanded_contour_instances"`
	ExpandedEdgeVisits       uint64 `json:"expanded_edge_visits"`
	MaxExpandedEdgesPerGlyph uint64 `json:"max_expanded_edges_per_glyph"`

	ObservedMaxPoints            uint32 `json:"observed_max_points"`
	ObservedMaxContours          uint32 `json:"observed_max_contours"`
	ObservedMaxCompositePoints   uint32 `json:"observed_max_composite_points"`
	ObservedMaxCompositeContours uint32 `json:"observed_max_composite_contours"`
	ObservedMaxInstructionBytes  uint32 `json:"observed_max_instruction_bytes"`
	ObservedMaxComponentElements uint32 `json:"observed_max_component_elements"`
	ObservedMaxComponentDepth    uint32 `json:"observed_max_component_depth"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

type OutlineGlyphInfo struct {
	Kind                 OutlineGlyphKind `json:"kind"`
	SourceBytes          uint32           `json:"source_bytes"`
	RawPointCount        uint32           `json:"raw_point_count"`
	RawContourCount      uint32           `json:"raw_contour_count"`
	DirectComponentCount uint32           `json:"direct_component_count"`
	ExpandedPointCount   uint64           `json:"expanded_point_count"`
	ExpandedContourCount uint64           `json:"expanded_contour_count"`
	ExpandedEdgeVisits   uint64           `json:"expanded_edge_visits"`
	ComponentDepth       uint32           `json:"component_depth"`
	InstructionBytes     uint32           `json:"instruction_bytes"`
	PaddingBytes         uint8            `json:"padding_bytes"`
	OverlapDeclared      bool             `json:"overlap_declared"`
}

type outlineRawPoint struct {
	x       int16
	y       int16
	onCurve bool
}

const (
	outlineArgumentXY    uint8 = 1
	outlineArgumentPoint uint8 = 2
)

type outlineComponent struct {
	flags        uint16
	glyphID      uint32
	argumentMode uint8
	arg1         int32
	arg2         int32
	matrix       [4]int16
}

type outlineGlyph struct {
	kind             OutlineGlyphKind
	sourceBytes      uint32
	numberOfContours int16
	xMin             int16
	yMin             int16
	xMax             int16
	yMax             int16
	instructions     []byte
	paddingBytes     uint8
	endPoints        []uint32
	points           []outlineRawPoint
	components       []outlineComponent
	overlapDeclared  bool
	expandedPoints   uint64
	expandedContours uint64
	expandedEdges    uint64
	componentDepth   uint32
}

type OutlineFont struct {
	font    *Font
	glyphs  []outlineGlyph
	receipt OutlineReceipt
}

func (value *OutlineFont) Receipt() OutlineReceipt {
	if value == nil {
		return OutlineReceipt{}
	}
	return value.receipt
}

func (value *OutlineFont) GlyphCount() uint32 {
	if value == nil {
		return 0
	}
	return uint32(len(value.glyphs))
}

func (value *OutlineFont) GlyphInfo(glyphID uint32) (OutlineGlyphInfo, bool) {
	if value == nil || uint64(glyphID) >= uint64(len(value.glyphs)) {
		return OutlineGlyphInfo{}, false
	}
	glyph := value.glyphs[glyphID]
	return OutlineGlyphInfo{
		Kind: glyph.kind, SourceBytes: glyph.sourceBytes,
		RawPointCount: uint32(len(glyph.points)), RawContourCount: uint32(len(glyph.endPoints)),
		DirectComponentCount: uint32(len(glyph.components)),
		ExpandedPointCount:   glyph.expandedPoints, ExpandedContourCount: glyph.expandedContours,
		ExpandedEdgeVisits: glyph.expandedEdges, ComponentDepth: glyph.componentDepth,
		InstructionBytes: uint32(len(glyph.instructions)), PaddingBytes: glyph.paddingBytes,
		OverlapDeclared: glyph.overlapDeclared,
	}, true
}
