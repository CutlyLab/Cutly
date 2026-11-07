package sfntglyf

type GeometryReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`

	SourceProfile               string `json:"source_profile"`
	SourceDescriptorHashProfile string `json:"source_descriptor_hash_profile"`
	SourceDescriptorHash        string `json:"source_descriptor_hash"`
	SourceGraphHashProfile      string `json:"source_graph_hash_profile"`
	SourceGraphHash             string `json:"source_graph_hash"`
	SourceCanonicalBinaryBytes  uint64 `json:"source_canonical_binary_bytes"`
	SourceRetainedBytes         uint64 `json:"source_retained_bytes"`
	UpstreamContentHashProfile  string `json:"upstream_content_hash_profile"`
	UpstreamContentHash         string `json:"upstream_content_hash"`

	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`
	GeometryHashProfile   string `json:"geometry_hash_profile"`
	GeometryHash          string `json:"geometry_hash"`

	GeometryStatus   string `json:"geometry_status"`
	BBoxStatus       string `json:"bbox_status"`
	HeadBBoxStatus   string `json:"head_bbox_status"`
	HheaStatus       string `json:"hhea_status"`
	HeadLSBStatus    string `json:"head_lsb_status"`
	MetricsStatus    string `json:"metrics_status"`
	HintingStatus    string `json:"hinting_status"`
	LayoutStatus     string `json:"layout_status"`
	CommandStatus    string `json:"command_status"`
	CapabilityStatus string `json:"capability_status"`

	GlyphCount                       uint32 `json:"glyph_count"`
	GlyphsWithContours               uint64 `json:"glyphs_with_contours"`
	MetricRedirectGlyphCount         uint64 `json:"metric_redirect_glyph_count"`
	ExpandedPointInstances           uint64 `json:"expanded_point_instances"`
	ExpandedContourInstances         uint64 `json:"expanded_contour_instances"`
	ExpandedEdgeVisits               uint64 `json:"expanded_edge_visits"`
	ExpandedGlyphInstances           uint64 `json:"expanded_glyph_instances"`
	TransformPointVisits             uint64 `json:"transform_point_visits"`
	MaxExpandedGlyphInstancesPerRoot uint64 `json:"max_expanded_glyph_instances_per_root"`
	MaxTransformVisitsPerGlyph       uint64 `json:"max_transform_visits_per_glyph"`
	MaxExpandedPointsPerGlyph        uint64 `json:"max_expanded_points_per_glyph"`
	MaxExpandedContoursPerGlyph      uint64 `json:"max_expanded_contours_per_glyph"`
	MaxComponentDepth                uint32 `json:"max_component_depth"`
	ExactBitsLimit                   uint32 `json:"exact_bits_limit"`
	ExactOpCount                     uint64 `json:"exact_op_count"`
	ExactLimbUnits                   uint64 `json:"exact_limb_units"`
	MaxObservedExactBits             uint32 `json:"max_observed_exact_bits"`

	HeadXMin int32 `json:"head_x_min"`
	HeadYMin int32 `json:"head_y_min"`
	HeadXMax int32 `json:"head_x_max"`
	HeadYMax int32 `json:"head_y_max"`
	MinLSB   int32 `json:"min_lsb"`
	MinRSB   int32 `json:"min_rsb"`
	XExtent  int32 `json:"x_max_extent"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

type GeometryGlyphInfo struct {
	Kind                  OutlineGlyphKind `json:"kind"`
	HasContours           bool             `json:"has_contours"`
	XMin                  int16            `json:"x_min"`
	YMin                  int16            `json:"y_min"`
	XMax                  int16            `json:"x_max"`
	YMax                  int16            `json:"y_max"`
	AdvanceWidth          uint16           `json:"advance_width"`
	LeftSideBearing       int16            `json:"left_side_bearing"`
	EffectiveMetricSource uint32           `json:"effective_metric_source"`
}

type geometryGlyph struct {
	kind                   OutlineGlyphKind
	hasContours            bool
	xMin, yMin, xMax, yMax int16
	advanceWidth           uint16
	leftSideBearing        int16
	effectiveMetricSource  uint32
}

type GeometryFont struct {
	source  *OutlineFont
	glyphs  []geometryGlyph
	receipt GeometryReceipt
}

func (value *GeometryFont) Receipt() GeometryReceipt {
	if value == nil {
		return GeometryReceipt{}
	}
	return value.receipt
}

func (value *GeometryFont) GlyphCount() uint32 {
	if value == nil {
		return 0
	}
	return uint32(len(value.glyphs))
}

func (value *GeometryFont) GlyphInfo(glyphID uint32) (GeometryGlyphInfo, bool) {
	if value == nil || uint64(glyphID) >= uint64(len(value.glyphs)) {
		return GeometryGlyphInfo{}, false
	}
	glyph := value.glyphs[glyphID]
	return GeometryGlyphInfo{
		Kind: glyph.kind, HasContours: glyph.hasContours,
		XMin: glyph.xMin, YMin: glyph.yMin, XMax: glyph.xMax, YMax: glyph.yMax,
		AdvanceWidth: glyph.advanceWidth, LeftSideBearing: glyph.leftSideBearing,
		EffectiveMetricSource: glyph.effectiveMetricSource,
	}, true
}
