package textassembly

import (
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

type Request struct {
	Text     *evaluator.FrozenTextSource `json:"-"`
	Provider *sfntglyf.ShapeProvider     `json:"-"`
}

// Receipt is scalar-only evidence for one provider-authenticated layout assembled from an opaque
// frozen Text v2 source. ShapeOutputHash and GlyphStreamHash are deliberately separate because
// their profile-defined encodings differ. The source and scaled layout outline hashes are also
// deliberately separate.
type Receipt struct {
	SchemaVersion         int    `json:"schema_version"`
	Profile               string `json:"profile"`
	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`
	AssemblyHashProfile   string `json:"assembly_hash_profile"`
	AssemblyHash          string `json:"assembly_hash"`

	FrozenTextProfile            string `json:"frozen_text_profile"`
	FrozenTextBindingHashProfile string `json:"frozen_text_binding_hash_profile"`
	FrozenTextBindingHash        string `json:"frozen_text_binding_hash"`
	DocumentHashProfile          string `json:"document_hash_profile"`
	DocumentHash                 string `json:"document_hash"`
	TextAssetID                  string `json:"text_asset_id"`
	TextAssetIndex               uint64 `json:"text_asset_index"`
	FontAssetID                  string `json:"font_asset_id"`
	FontAssetIndex               uint64 `json:"font_asset_index"`
	FontContentHashProfile       string `json:"font_content_hash_profile"`
	FontContentHash              string `json:"font_content_hash"`
	TextSemanticHashProfile      string `json:"text_semantic_hash_profile"`
	TextSemanticHash             string `json:"text_semantic_hash"`
	LayoutInputHashProfile       string `json:"layout_input_hash_profile"`
	LayoutInputHash              string `json:"layout_input_hash"`

	ShapeProviderProfile         string `json:"shape_provider_profile"`
	ShapeProviderHashProfile     string `json:"shape_provider_hash_profile"`
	ShapeProviderHash            string `json:"shape_provider_hash"`
	DependencyFingerprintProfile string `json:"dependency_fingerprint_profile"`
	DependencyFingerprint        string `json:"dependency_fingerprint"`
	LayoutGraphHashProfile       string `json:"layout_graph_hash_profile"`
	LayoutGraphHash              string `json:"layout_graph_hash"`
	SourceOutlineSetHashProfile  string `json:"source_outline_set_hash_profile"`
	SourceOutlineSetHash         string `json:"source_outline_set_hash"`
	LayoutSourceHashProfile      string `json:"layout_source_hash_profile"`
	LayoutSourceHash             string `json:"layout_source_hash"`

	ShapeInputHashProfile    string `json:"shape_input_hash_profile"`
	ShapeInputHash           string `json:"shape_input_hash"`
	ShapeOutputProfile       string `json:"shape_output_profile"`
	ShapeOutputHashProfile   string `json:"shape_output_hash_profile"`
	ShapeOutputHash          string `json:"shape_output_hash"`
	LayoutReceiptProfile     string `json:"layout_receipt_profile"`
	GlyphStreamHashProfile   string `json:"glyph_stream_hash_profile"`
	GlyphStreamHash          string `json:"glyph_stream_hash"`
	ScaledOutlineHashProfile string `json:"scaled_outline_hash_profile"`
	ScaledOutlineHash        string `json:"scaled_outline_hash"`
	PlacementHashProfile     string `json:"placement_hash_profile"`
	PlacementHash            string `json:"placement_hash"`
	LayoutHashProfile        string `json:"layout_hash_profile"`
	LayoutHash               string `json:"layout_hash"`

	ShapingProvenanceStatus string `json:"shaping_provenance_status"`
	PaintProvenanceStatus   string `json:"paint_provenance_status"`
	CapabilityStatus        string `json:"capability_status"`
	WidthPixels             uint32 `json:"width_pixels"`
	HeightPixels            uint32 `json:"height_pixels"`
	FontSizeNumerator       int64  `json:"font_size_numerator"`
	FontSizeDenominator     int64  `json:"font_size_denominator"`
	FillR                   uint16 `json:"fill_r"`
	FillG                   uint16 `json:"fill_g"`
	FillB                   uint16 `json:"fill_b"`
	FillA                   uint16 `json:"fill_a"`
	LineCount               uint64 `json:"line_count"`
	GlyphCount              uint64 `json:"glyph_count"`
	UniqueGlyphCount        uint64 `json:"unique_glyph_count"`
	ContourCount            uint64 `json:"contour_count"`
	CommandCount            uint64 `json:"command_count"`
	CommandPointOccurrences uint64 `json:"command_point_occurrences"`
	CanonicalBinaryBytes    uint64 `json:"canonical_binary_bytes"`
	WorkUnits               uint64 `json:"work_units"`
	WorkingBytes            uint64 `json:"working_bytes"`
	RetainedBytes           uint64 `json:"retained_bytes"`
}

// Source is an opaque, immutable provider-authenticated text layout. It intentionally exposes no
// Layout, LayoutSource, shaped stream, outline, or low-level textraster request.
type Source struct {
	frozen       *evaluator.FrozenTextSource
	layoutSource *sfntglyf.LayoutSource
	layout       *textlayout.Layout
	fill         color.RGBA16Input
	receipt      Receipt
}

func (value *Source) Receipt() Receipt {
	if value == nil {
		return Receipt{}
	}
	return value.receipt
}

// RasterReceipt wraps the low-level raster evidence without upgrading that inner receipt's
// explicit caller-layout-unverified claim. Authentication is established by this package's
// private ownership of the exact layout supplied to the low-level rasterizer.
type RasterReceipt struct {
	SchemaVersion           int    `json:"schema_version"`
	Profile                 string `json:"profile"`
	DescriptorHashProfile   string `json:"descriptor_hash_profile"`
	DescriptorHash          string `json:"descriptor_hash"`
	BindingHashProfile      string `json:"binding_hash_profile"`
	BindingHash             string `json:"binding_hash"`
	AssemblyHashProfile     string `json:"assembly_hash_profile"`
	AssemblyHash            string `json:"assembly_hash"`
	InnerReceiptProfile     string `json:"inner_receipt_profile"`
	InnerRasterProfile      string `json:"inner_raster_profile"`
	InnerBindingHashProfile string `json:"inner_binding_hash_profile"`
	InnerBindingHash        string `json:"inner_binding_hash"`
	InnerShapingStatus      string `json:"inner_shaping_status"`
	ShapingProvenanceStatus string `json:"shaping_provenance_status"`
	PaintProvenanceStatus   string `json:"paint_provenance_status"`
	CoverageHashProfile     string `json:"coverage_hash_profile"`
	CoverageHash            string `json:"coverage_hash"`
	PixelHashProfile        string `json:"pixel_hash_profile"`
	PixelHash               string `json:"pixel_hash"`
	WidthPixels             uint32 `json:"width_pixels"`
	HeightPixels            uint32 `json:"height_pixels"`
	CanonicalBinaryBytes    uint64 `json:"canonical_binary_bytes"`
	WorkUnits               uint64 `json:"work_units"`
	WorkingBytes            uint64 `json:"working_bytes"`
	RetainedBytes           uint64 `json:"retained_bytes"`
}
