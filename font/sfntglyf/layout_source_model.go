package sfntglyf

// LayoutSourceReceipt binds the raster-facing outline view to one exact live
// ShapeProvider, LayoutGraph, OutlineSet, and static-glyf font lineage. It is
// deliberately scalar-only and contains no font bytes, readers, slices, or
// go-text values.
type LayoutSourceReceipt struct {
	SchemaVersion       int    `json:"schema_version"`
	Profile             string `json:"profile"`
	LayoutSourceProfile string `json:"layout_source_profile"`

	DescriptorHashProfile   string `json:"descriptor_hash_profile"`
	DescriptorHash          string `json:"descriptor_hash"`
	LayoutSourceHashProfile string `json:"layout_source_hash_profile"`
	LayoutSourceHash        string `json:"layout_source_hash"`

	ShapeProviderProfile         string `json:"shape_provider_profile"`
	ShapeProviderHashProfile     string `json:"shape_provider_hash_profile"`
	ShapeProviderHash            string `json:"shape_provider_hash"`
	DependencyFingerprintProfile string `json:"dependency_fingerprint_profile"`
	DependencyFingerprint        string `json:"dependency_fingerprint"`

	LayoutGraphProfile     string `json:"layout_graph_profile"`
	LayoutGraphHashProfile string `json:"layout_graph_hash_profile"`
	LayoutGraphHash        string `json:"layout_graph_hash"`
	OutlineSetProfile      string `json:"outline_set_profile"`
	OutlineSetHashProfile  string `json:"outline_set_hash_profile"`
	OutlineSetHash         string `json:"outline_set_hash"`
	FontReceiptProfile     string `json:"font_receipt_profile"`
	FontContentHashProfile string `json:"font_content_hash_profile"`
	FontContentHash        string `json:"font_content_hash"`

	BindingStatus        string `json:"binding_status"`
	OutlineProfile       string `json:"outline_profile"`
	CommandStatus        string `json:"command_status"`
	RoundingStatus       string `json:"rounding_status"`
	HintingStatus        string `json:"hinting_status"`
	LayoutStatus         string `json:"layout_status"`
	VariableLayoutStatus string `json:"variable_layout_status"`
	RasterStatus         string `json:"raster_status"`
	CapabilityStatus     string `json:"capability_status"`

	UnitsPerEm              uint16 `json:"units_per_em"`
	HheaAscender            int16  `json:"hhea_ascender"`
	GlyphCount              uint32 `json:"glyph_count"`
	GlyphsWithContours      uint64 `json:"glyphs_with_contours"`
	ContourCount            uint64 `json:"contour_count"`
	CommandCount            uint64 `json:"command_count"`
	CommandPointOccurrences uint64 `json:"command_point_occurrences"`
	MaxCommandsPerGlyph     uint64 `json:"max_commands_per_glyph"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

// LayoutSource is the opaque raster-facing bridge. provider is retained as
// the complete trust root; outlines is the exact immutable OutlineSet already
// retained by that provider. Neither value is exposed to callers.
type LayoutSource struct {
	provider *ShapeProvider
	outlines *OutlineSet
	receipt  LayoutSourceReceipt
}

func (value *LayoutSource) Receipt() LayoutSourceReceipt {
	if value == nil {
		return LayoutSourceReceipt{}
	}
	return value.receipt
}

func (value *LayoutSource) UnitsPerEm() uint16 {
	if value == nil {
		return 0
	}
	return value.receipt.UnitsPerEm
}

func (value *LayoutSource) HheaAscender() int16 {
	if value == nil {
		return 0
	}
	return value.receipt.HheaAscender
}

func (value *LayoutSource) GlyphCount() uint32 {
	if value == nil {
		return 0
	}
	return value.receipt.GlyphCount
}

// GlyphOutline returns a source-bound indexed view. It intentionally does not
// return OutlineSet's existing GlyphOutlineView, so a later consumer cannot
// mix an outline from another otherwise-valid font into this trust root.
func (value *LayoutSource) GlyphOutline(glyphID uint32) (LayoutGlyphOutlineView, bool) {
	if value == nil || value.outlines == nil || uint64(glyphID) >= uint64(len(value.outlines.glyphs)) {
		return LayoutGlyphOutlineView{}, false
	}
	return LayoutGlyphOutlineView{source: value, glyphID: glyphID}, true
}

type LayoutGlyphOutlineView struct {
	source  *LayoutSource
	glyphID uint32
}

func (value LayoutGlyphOutlineView) valid() bool {
	return value.source != nil && value.source.outlines != nil &&
		uint64(value.glyphID) < uint64(len(value.source.outlines.glyphs))
}

func (value LayoutGlyphOutlineView) GlyphID() uint32 {
	if !value.valid() {
		return 0
	}
	return value.glyphID
}

func (value LayoutGlyphOutlineView) ContourCount() uint64 {
	if !value.valid() {
		return 0
	}
	return value.source.outlines.glyphs[value.glyphID].contours
}

func (value LayoutGlyphOutlineView) CommandCount() uint64 {
	if !value.valid() {
		return 0
	}
	return value.source.outlines.glyphs[value.glyphID].commandCount
}

func (value LayoutGlyphOutlineView) Command(index uint64) (LayoutOutlineCommandView, bool) {
	if !value.valid() {
		return LayoutOutlineCommandView{}, false
	}
	rangeValue := value.source.outlines.glyphs[value.glyphID]
	if index >= rangeValue.commandCount {
		return LayoutOutlineCommandView{}, false
	}
	absolute, ok := checkedAdd(rangeValue.commandStart, index)
	if !ok || absolute >= uint64(len(value.source.outlines.commands)) {
		return LayoutOutlineCommandView{}, false
	}
	return LayoutOutlineCommandView{source: value.source, index: absolute}, true
}

type LayoutOutlineCommandView struct {
	source *LayoutSource
	index  uint64
}

func (value LayoutOutlineCommandView) valid() bool {
	return value.source != nil && value.source.outlines != nil &&
		value.index < uint64(len(value.source.outlines.commands))
}

func (value LayoutOutlineCommandView) Op() OutlineCommandOp {
	if !value.valid() {
		return 0
	}
	return value.source.outlines.commands[value.index].op
}

func (value LayoutOutlineCommandView) PointCount() uint8 {
	if !value.valid() {
		return 0
	}
	count := value.source.outlines.commands[value.index].pointCount
	if uint64(count) > uint64(len(value.source.outlines.commands[value.index].points)) {
		return 0
	}
	return count
}

func (value LayoutOutlineCommandView) Point(index uint8) (OutlinePoint, bool) {
	if !value.valid() {
		return OutlinePoint{}, false
	}
	command := value.source.outlines.commands[value.index]
	if uint64(command.pointCount) > uint64(len(command.points)) ||
		index >= command.pointCount || uint64(index) >= uint64(len(command.points)) {
		return OutlinePoint{}, false
	}
	return command.points[index], true
}
