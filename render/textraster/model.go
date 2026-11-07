package textraster

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

type Request struct {
	Source *sfntglyf.LayoutSource `json:"-"`
	// Layout is caller supplied. Rasterize authenticates its scaled outline geometry against Source,
	// but this low-level profile does not authenticate that glyph selection or placement came from
	// Source's ShapeProvider. Use the assembly layer when shape provenance is required.
	Layout   *textlayout.Layout  `json:"-"`
	FontSize documentv2.Rational `json:"font_size"`
	Fill     color.RGBA16Input   `json:"fill"`
}

// Receipt is scalar-only and owns all dynamic strings.
type Receipt struct {
	SchemaVersion         int    `json:"schema_version"`
	Profile               string `json:"profile"`
	RasterProfile         string `json:"raster_profile"`
	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`

	LayoutSourceProfile          string `json:"layout_source_profile"`
	LayoutSourceHashProfile      string `json:"layout_source_hash_profile"`
	LayoutSourceHash             string `json:"layout_source_hash"`
	ShapeProviderProfile         string `json:"shape_provider_profile"`
	ShapeProviderHashProfile     string `json:"shape_provider_hash_profile"`
	ShapeProviderHash            string `json:"shape_provider_hash"`
	DependencyFingerprintProfile string `json:"dependency_fingerprint_profile"`
	DependencyFingerprint        string `json:"dependency_fingerprint"`
	FontContentHashProfile       string `json:"font_content_hash_profile"`
	FontContentHash              string `json:"font_content_hash"`
	ShapingProvenanceStatus      string `json:"shaping_provenance_status"`

	LayoutReceiptProfile   string `json:"layout_receipt_profile"`
	LayoutHashProfile      string `json:"layout_hash_profile"`
	LayoutHash             string `json:"layout_hash"`
	GlyphStreamHashProfile string `json:"glyph_stream_hash_profile"`
	GlyphStreamHash        string `json:"glyph_stream_hash"`
	OutlineSetHashProfile  string `json:"outline_set_hash_profile"`
	OutlineSetHash         string `json:"outline_set_hash"`
	PlacementHashProfile   string `json:"placement_hash_profile"`
	PlacementHash          string `json:"placement_hash"`
	BindingHashProfile     string `json:"binding_hash_profile"`
	BindingHash            string `json:"binding_hash"`
	CoverageHashProfile    string `json:"coverage_hash_profile"`
	CoverageHash           string `json:"coverage_hash"`
	PaintProfile           string `json:"paint_profile"`
	KernelProfile          string `json:"kernel_profile"`
	SurfaceProfile         string `json:"surface_profile"`
	PixelHashProfile       string `json:"pixel_hash_profile"`
	PixelHash              string `json:"pixel_hash"`
	FontSizeNumerator      int64  `json:"font_size_numerator"`
	FontSizeDenominator    int64  `json:"font_size_denominator"`
	WidthPixels            uint32 `json:"width_pixels"`
	HeightPixels           uint32 `json:"height_pixels"`
	FillR                  uint16 `json:"fill_r"`
	FillG                  uint16 `json:"fill_g"`
	FillB                  uint16 `json:"fill_b"`
	FillA                  uint16 `json:"fill_a"`
	PlacementCount         uint64 `json:"placement_count"`
	UniqueGlyphCount       uint64 `json:"unique_glyph_count"`
	ContourCount           uint64 `json:"contour_count"`
	CommandCount           uint64 `json:"command_count"`
	UniqueSegmentCount     uint64 `json:"unique_segment_count"`
	SegmentCount           uint64 `json:"segment_count"`
	SampleCount            uint64 `json:"sample_count"`
	CanonicalBinaryBytes   uint64 `json:"canonical_binary_bytes"`
	WorkUnits              uint64 `json:"work_units"`
	WorkingBytes           uint64 `json:"working_bytes"`
	RetainedBytes          uint64 `json:"retained_bytes"`
}
