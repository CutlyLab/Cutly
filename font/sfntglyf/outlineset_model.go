package sfntglyf

type Q26_6 int64

type OutlinePoint struct {
	X Q26_6 `json:"x"`
	Y Q26_6 `json:"y"`
}

type OutlineCommandOp uint8

const (
	OutlineMoveTo OutlineCommandOp = 1
	OutlineLineTo OutlineCommandOp = 2
	OutlineQuadTo OutlineCommandOp = 3
	OutlineClose  OutlineCommandOp = 4
)

func (value OutlineCommandOp) String() string {
	switch value {
	case OutlineMoveTo:
		return "move_to"
	case OutlineLineTo:
		return "line_to"
	case OutlineQuadTo:
		return "quad_to"
	case OutlineClose:
		return "close"
	default:
		return ""
	}
}

type OutlineSetReceipt struct {
	SchemaVersion  int    `json:"schema_version"`
	Profile        string `json:"profile"`
	OutlineProfile string `json:"outline_profile"`

	SourceProfile                      string `json:"source_profile"`
	SourceDescriptorHashProfile        string `json:"source_descriptor_hash_profile"`
	SourceDescriptorHash               string `json:"source_descriptor_hash"`
	SourceGeometryHashProfile          string `json:"source_geometry_hash_profile"`
	SourceGeometryHash                 string `json:"source_geometry_hash"`
	SourceGeometryCanonicalBinaryBytes uint64 `json:"source_geometry_canonical_binary_bytes"`
	SourceGraphHashProfile             string `json:"source_graph_hash_profile"`
	SourceGraphHash                    string `json:"source_graph_hash"`
	SourceGraphCanonicalBinaryBytes    uint64 `json:"source_graph_canonical_binary_bytes"`
	SourceRetainedBytes                uint64 `json:"source_retained_bytes"`
	UpstreamContentHashProfile         string `json:"upstream_content_hash_profile"`
	UpstreamContentHash                string `json:"upstream_content_hash"`

	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`
	OutlineSetHashProfile string `json:"outline_set_hash_profile"`
	OutlineSetHash        string `json:"outline_set_hash"`

	GeometryStatus     string `json:"geometry_status"`
	ImpliedPointStatus string `json:"implied_point_status"`
	CommandStatus      string `json:"command_status"`
	RoundingStatus     string `json:"rounding_status"`
	HintingStatus      string `json:"hinting_status"`
	LayoutStatus       string `json:"layout_status"`
	RasterStatus       string `json:"raster_status"`
	CapabilityStatus   string `json:"capability_status"`

	GlyphCount                       uint32 `json:"glyph_count"`
	GlyphsWithContours               uint64 `json:"glyphs_with_contours"`
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

	ImpliedPointInstances              uint64 `json:"implied_point_instances"`
	LineCommandInstances               uint64 `json:"line_command_instances"`
	QuadCommandInstances               uint64 `json:"quad_command_instances"`
	DrawingCommandInstances            uint64 `json:"drawing_command_instances"`
	CommandInstances                   uint64 `json:"command_instances"`
	CommandPointOccurrences            uint64 `json:"command_point_occurrences"`
	MaxImpliedPointsPerGlyph           uint64 `json:"max_implied_points_per_glyph"`
	MaxDrawingCommandsPerGlyph         uint64 `json:"max_drawing_commands_per_glyph"`
	MaxCommandsPerGlyph                uint64 `json:"max_commands_per_glyph"`
	MaxCommandPointOccurrencesPerGlyph uint64 `json:"max_command_point_occurrences_per_glyph"`

	ExactBitsLimit                uint32 `json:"exact_bits_limit"`
	SourceExactOpCount            uint64 `json:"source_exact_op_count"`
	SourceExactLimbUnits          uint64 `json:"source_exact_limb_units"`
	SourceMaxObservedExactBits    uint32 `json:"source_max_observed_exact_bits"`
	MaterializationExactOpCount   uint64 `json:"materialization_exact_op_count"`
	MaterializationExactLimbUnits uint64 `json:"materialization_exact_limb_units"`
	MaterializationMaxExactBits   uint32 `json:"materialization_max_observed_exact_bits"`
	ExactOpCount                  uint64 `json:"exact_op_count"`
	ExactLimbUnits                uint64 `json:"exact_limb_units"`
	MaxObservedExactBits          uint32 `json:"max_observed_exact_bits"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

type outlineSetGlyphRange struct {
	commandStart uint64
	commandCount uint64
	contours     uint64
}

type outlineSetCommand struct {
	op         OutlineCommandOp
	pointCount uint8
	points     [2]OutlinePoint
}

type OutlineSet struct {
	source   *GeometryFont
	glyphs   []outlineSetGlyphRange
	commands []outlineSetCommand
	receipt  OutlineSetReceipt
}

type GlyphOutlineView struct {
	set     *OutlineSet
	glyphID uint32
}

type OutlineCommandView struct {
	set   *OutlineSet
	index uint64
}

func (value *OutlineSet) Receipt() OutlineSetReceipt {
	if value == nil {
		return OutlineSetReceipt{}
	}
	return value.receipt
}

func (value *OutlineSet) GlyphCount() uint32 {
	if value == nil || uint64(len(value.glyphs)) > uint64(^uint32(0)) {
		return 0
	}
	return uint32(len(value.glyphs))
}

func (value *OutlineSet) Glyph(glyphID uint32) (GlyphOutlineView, bool) {
	if value == nil || uint64(glyphID) >= uint64(len(value.glyphs)) {
		return GlyphOutlineView{}, false
	}
	return GlyphOutlineView{set: value, glyphID: glyphID}, true
}

func (value GlyphOutlineView) valid() bool {
	return value.set != nil && uint64(value.glyphID) < uint64(len(value.set.glyphs))
}

func (value GlyphOutlineView) GlyphID() uint32 {
	if !value.valid() {
		return 0
	}
	return value.glyphID
}

func (value GlyphOutlineView) ContourCount() uint64 {
	if !value.valid() {
		return 0
	}
	return value.set.glyphs[value.glyphID].contours
}

func (value GlyphOutlineView) CommandCount() uint64 {
	if !value.valid() {
		return 0
	}
	return value.set.glyphs[value.glyphID].commandCount
}

func (value GlyphOutlineView) Command(index uint64) (OutlineCommandView, bool) {
	if !value.valid() {
		return OutlineCommandView{}, false
	}
	rangeValue := value.set.glyphs[value.glyphID]
	if index >= rangeValue.commandCount {
		return OutlineCommandView{}, false
	}
	absolute, ok := checkedAdd(rangeValue.commandStart, index)
	if !ok || absolute >= uint64(len(value.set.commands)) {
		return OutlineCommandView{}, false
	}
	return OutlineCommandView{set: value.set, index: absolute}, true
}

func (value OutlineCommandView) valid() bool {
	return value.set != nil && value.index < uint64(len(value.set.commands))
}

func (value OutlineCommandView) Op() OutlineCommandOp {
	if !value.valid() {
		return 0
	}
	return value.set.commands[value.index].op
}

func (value OutlineCommandView) PointCount() uint8 {
	if !value.valid() {
		return 0
	}
	return value.set.commands[value.index].pointCount
}

func (value OutlineCommandView) Point(index uint8) (OutlinePoint, bool) {
	if !value.valid() {
		return OutlinePoint{}, false
	}
	command := value.set.commands[value.index]
	if index >= command.pointCount {
		return OutlinePoint{}, false
	}
	return command.points[index], true
}
