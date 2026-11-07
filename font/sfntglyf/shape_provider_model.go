package sfntglyf

import gotextfont "github.com/go-text/typesetting/font"

// ShapeProviderReceipt proves that the pinned go-text parser was constructed only after a
// receipt-exact live replay of the complete LayoutGraph lineage. It deliberately carries no
// executable object, byte view, reader, or caller-controlled dependency identity.
type ShapeProviderReceipt struct {
	SchemaVersion                int    `json:"schema_version"`
	Profile                      string `json:"profile"`
	ProviderProfile              string `json:"provider_profile"`
	DescriptorHashProfile        string `json:"descriptor_hash_profile"`
	DescriptorHash               string `json:"descriptor_hash"`
	ProviderHashProfile          string `json:"provider_hash_profile"`
	ProviderHash                 string `json:"provider_hash"`
	DependencyLockProfile        string `json:"dependency_lock_profile"`
	DependencyFingerprintProfile string `json:"dependency_fingerprint_profile"`
	DependencyFingerprint        string `json:"dependency_fingerprint"`
	GoTextModuleVersion          string `json:"go_text_module_version"`
	EmbeddedHarfBuzzVersion      string `json:"embedded_harfbuzz_version"`
	EmbeddedHarfBuzzCommit       string `json:"embedded_harfbuzz_commit"`
	BindingStatus                string `json:"binding_status"`
	CapabilityStatus             string `json:"capability_status"`
	LayoutGraphProfile           string `json:"layout_graph_profile"`
	LayoutGraphHashProfile       string `json:"layout_graph_hash_profile"`
	LayoutGraphHash              string `json:"layout_graph_hash"`
	OutlineSetHashProfile        string `json:"outline_set_hash_profile"`
	OutlineSetHash               string `json:"outline_set_hash"`
	FontContentHashProfile       string `json:"font_content_hash_profile"`
	FontContentHash              string `json:"font_content_hash"`
	ClusterPolicy                string `json:"cluster_policy"`
	FeaturePolicy                string `json:"feature_policy"`
	DirectionNormalization       string `json:"direction_normalization"`
	UnitsPerEm                   uint16 `json:"units_per_em"`
	GlyphCount                   uint32 `json:"glyph_count"`
	EncodedBytes                 uint64 `json:"encoded_bytes"`
	CmapMappingCount             uint64 `json:"cmap_mapping_count"`
	CanonicalBinaryBytes         uint64 `json:"canonical_binary_bytes"`
	WorkUnits                    uint64 `json:"work_units"`
	WorkingBytes                 uint64 `json:"working_bytes"`
	RetainedBytes                uint64 `json:"retained_bytes"`
}

// ShapeProvider is intentionally opaque. base is go-text's immutable concurrent-safe Font;
// mutable Face, HarfBuzz Font and Buffer values are created afresh inside each Shape call.
type ShapeProvider struct {
	graph                    *LayoutGraph
	source                   *OutlineSet
	base                     *gotextfont.Font
	gsubLookupExpansion      uint64
	gsubTopLevelApplications uint64
	receipt                  ShapeProviderReceipt
}

func (value *ShapeProvider) Receipt() ShapeProviderReceipt {
	if value == nil {
		return ShapeProviderReceipt{}
	}
	return value.receipt
}

type ShapeInput struct {
	Text      string         `json:"text"`
	Language  string         `json:"language"`
	Script    string         `json:"script"`
	Direction ShapeDirection `json:"direction"`
}

type ShapedGlyphRecord struct {
	GlyphID           uint32 `json:"glyph_id"`
	ClusterByteOffset uint64 `json:"cluster_byte_offset"`
	XAdvance          Q26_6  `json:"x_advance"`
	YAdvance          Q26_6  `json:"y_advance"`
	XOffset           Q26_6  `json:"x_offset"`
	YOffset           Q26_6  `json:"y_offset"`
}

type ShapeReceipt struct {
	SchemaVersion                int            `json:"schema_version"`
	Profile                      string         `json:"profile"`
	ProviderProfile              string         `json:"provider_profile"`
	ProviderHashProfile          string         `json:"provider_hash_profile"`
	ProviderHash                 string         `json:"provider_hash"`
	DependencyFingerprintProfile string         `json:"dependency_fingerprint_profile"`
	DependencyFingerprint        string         `json:"dependency_fingerprint"`
	OutputProfile                string         `json:"output_profile"`
	InputHashProfile             string         `json:"input_hash_profile"`
	InputHash                    string         `json:"input_hash"`
	OutputHashProfile            string         `json:"output_hash_profile"`
	OutputHash                   string         `json:"output_hash"`
	LayoutGraphHashProfile       string         `json:"layout_graph_hash_profile"`
	LayoutGraphHash              string         `json:"layout_graph_hash"`
	OutlineSetHashProfile        string         `json:"outline_set_hash_profile"`
	OutlineSetHash               string         `json:"outline_set_hash"`
	FontContentHashProfile       string         `json:"font_content_hash_profile"`
	FontContentHash              string         `json:"font_content_hash"`
	ClusterPolicy                string         `json:"cluster_policy"`
	FeaturePolicy                string         `json:"feature_policy"`
	DirectionNormalization       string         `json:"direction_normalization"`
	Language                     string         `json:"language"`
	Script                       string         `json:"script"`
	Direction                    ShapeDirection `json:"direction"`
	TextBytes                    uint64         `json:"text_bytes"`
	LineCount                    uint64         `json:"line_count"`
	InputScalarCount             uint64         `json:"input_scalar_count"`
	OutputGlyphCount             uint64         `json:"output_glyph_count"`
	AdmissionGlyphBound          uint64         `json:"admission_glyph_bound"`
	CanonicalBinaryBytes         uint64         `json:"canonical_binary_bytes"`
	AdmissionCanonicalBytes      uint64         `json:"admission_canonical_binary_bytes"`
	WorkUnits                    uint64         `json:"work_units"`
	WorkingBytes                 uint64         `json:"working_bytes"`
	RetainedBytes                uint64         `json:"retained_bytes"`
}

type shapedLineRecord struct {
	byteStart uint64
	byteEnd   uint64
	glyphs    []ShapedGlyphRecord
}

type ShapedText struct {
	lines   []shapedLineRecord
	receipt ShapeReceipt
}

func (value *ShapedText) Receipt() ShapeReceipt {
	if value == nil {
		return ShapeReceipt{}
	}
	return value.receipt
}

func (value *ShapedText) View() (ShapedTextView, error) {
	if value == nil {
		return ShapedTextView{}, fontError(CodeShapeInputInvalid, "shape/output", ErrShapeInputInvalid)
	}
	return ShapedTextView{value: value}, nil
}

type ShapedTextView struct{ value *ShapedText }

func (value ShapedTextView) LineCount() int {
	if value.value == nil {
		return 0
	}
	return len(value.value.lines)
}

func (value ShapedTextView) Line(index int) (ShapedLineView, error) {
	if value.value == nil || index < 0 || index >= len(value.value.lines) {
		return ShapedLineView{}, fontError(CodeShapeInputInvalid, "shape/output/lines", ErrShapeInputInvalid)
	}
	return ShapedLineView{value: value.value, index: index}, nil
}

type ShapedLineView struct {
	value *ShapedText
	index int
}

func (value ShapedLineView) valid() bool {
	return value.value != nil && value.index >= 0 && value.index < len(value.value.lines)
}

func (value ShapedLineView) ByteStart() uint64 {
	if !value.valid() {
		return 0
	}
	return value.value.lines[value.index].byteStart
}

func (value ShapedLineView) ByteEnd() uint64 {
	if !value.valid() {
		return 0
	}
	return value.value.lines[value.index].byteEnd
}

func (value ShapedLineView) GlyphCount() int {
	if !value.valid() {
		return 0
	}
	return len(value.value.lines[value.index].glyphs)
}

func (value ShapedLineView) Glyph(index int) (ShapedGlyphRecord, error) {
	if !value.valid() || index < 0 || index >= len(value.value.lines[value.index].glyphs) {
		return ShapedGlyphRecord{}, fontError(CodeShapeInputInvalid, "shape/output/lines/glyphs", ErrShapeInputInvalid)
	}
	return value.value.lines[value.index].glyphs[index], nil
}
