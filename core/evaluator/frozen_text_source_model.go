package evaluator

import "github.com/CutlyLab/Cutly/core/documentv2"

// FrozenTextSourceReceipt is scalar-only, unsigned structural evidence for one live-replayed Text
// v2 source. A serialized receipt and its public hash are never authority; downstream operations
// must require the live opaque *FrozenTextSource returned by BindTextSource. The document hash is
// an opaque CompileSnapshot trust anchor and does not independently prove the complete Document.
type FrozenTextSourceReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`
	SourceProfile string `json:"source_profile"`

	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`
	BindingHashProfile    string `json:"binding_hash_profile"`
	BindingHash           string `json:"binding_hash"`

	DocumentHashProfile    string               `json:"document_hash_profile"`
	DocumentHash           string               `json:"document_hash"`
	DocumentCanonicalBytes uint64               `json:"document_canonical_bytes"`
	AssetID                documentv2.ID        `json:"asset_id"`
	AssetIndex             uint64               `json:"asset_index"`
	AssetKind              documentv2.AssetKind `json:"asset_kind"`
	AssetContentHash       string               `json:"asset_content_hash"`

	TextProfile           string                   `json:"text_profile"`
	EvaluatorProfile      string                   `json:"evaluator_profile"`
	FontAssetID           documentv2.ID            `json:"font_asset_id"`
	FontAssetIndex        uint64                   `json:"font_asset_index"`
	FontAssetKind         documentv2.AssetKind     `json:"font_asset_kind"`
	FontContentHash       string                   `json:"font_content_hash"`
	FontProfile           string                   `json:"font_profile"`
	RunInputProfile       string                   `json:"run_input_profile"`
	LayoutProfile         string                   `json:"layout_profile"`
	OutlineProfile        string                   `json:"outline_profile"`
	RasterProfile         string                   `json:"raster_profile"`
	PaintProfile          string                   `json:"paint_profile"`
	CoverageHashProfile   string                   `json:"coverage_hash_profile"`
	FontSizeNumerator     int64                    `json:"font_size_numerator"`
	FontSizeDenominator   int64                    `json:"font_size_denominator"`
	LineHeightNumerator   int64                    `json:"line_height_numerator"`
	LineHeightDenominator int64                    `json:"line_height_denominator"`
	ViewportWidthPixels   uint32                   `json:"viewport_width_pixels"`
	ViewportHeightPixels  uint32                   `json:"viewport_height_pixels"`
	FillR                 uint16                   `json:"fill_r"`
	FillG                 uint16                   `json:"fill_g"`
	FillB                 uint16                   `json:"fill_b"`
	FillA                 uint16                   `json:"fill_a"`
	Language              string                   `json:"language"`
	Script                string                   `json:"script"`
	Direction             documentv2.TextDirection `json:"direction"`
	Alignment             documentv2.TextAlignment `json:"alignment"`

	UnicodeVersion               string `json:"unicode_version"`
	UnicodeValidationProfile     string `json:"unicode_validation_profile"`
	UnicodeValidationHashProfile string `json:"unicode_validation_hash_profile"`
	UnicodeValidationTableHash   string `json:"unicode_validation_table_hash"`

	CanonicalSourceHashProfile string `json:"canonical_source_hash_profile"`
	CanonicalSourceHash        string `json:"canonical_source_hash"`
	CanonicalSourceBytes       uint64 `json:"canonical_source_bytes"`
	CanonicalSourceUpperBytes  uint64 `json:"canonical_source_upper_bytes"`
	TextSemanticHashProfile    string `json:"text_semantic_hash_profile"`
	TextSemanticHash           string `json:"text_semantic_hash"`
	TextSemanticCanonicalBytes uint64 `json:"text_semantic_canonical_bytes"`
	TextSemanticUpperBytes     uint64 `json:"text_semantic_upper_bytes"`
	LayoutInputHashProfile     string `json:"layout_input_hash_profile"`
	LayoutInputHash            string `json:"layout_input_hash"`
	LayoutInputCanonicalBytes  uint64 `json:"layout_input_canonical_bytes"`
	LayoutInputUpperBytes      uint64 `json:"layout_input_upper_bytes"`

	BindingStatus         string `json:"binding_status"`
	CapabilityStatus      string `json:"capability_status"`
	TextBytes             uint64 `json:"text_bytes"`
	BindingCanonicalBytes uint64 `json:"binding_canonical_bytes"`
	CanonicalBinaryBytes  uint64 `json:"canonical_binary_bytes"`
	WorkUnits             uint64 `json:"work_units"`
	WorkingBytes          uint64 `json:"working_bytes"`
	RetainedBytes         uint64 `json:"retained_bytes"`
}

// FrozenTextSource owns a detached scalar copy of one TextSource. It deliberately contains no
// snapshot pointer, source pointer, byte view, slice, map, reader, or executable shaping value.
type FrozenTextSource struct {
	assetID         documentv2.ID
	assetIndex      uint64
	profile         string
	text            string
	fontAssetID     documentv2.ID
	fontAssetIndex  uint64
	fontContentHash string
	fontSize        documentv2.Rational
	lineHeight      documentv2.Rational
	viewport        documentv2.RasterViewport
	fill            documentv2.ColorRGBA16
	language        string
	script          string
	direction       documentv2.TextDirection
	alignment       documentv2.TextAlignment
	receipt         FrozenTextSourceReceipt
}

func (value *FrozenTextSource) Receipt() FrozenTextSourceReceipt {
	if value == nil {
		return FrozenTextSourceReceipt{}
	}
	return value.receipt
}

func (value *FrozenTextSource) AssetID() documentv2.ID {
	if value == nil {
		return ""
	}
	return value.assetID
}

func (value *FrozenTextSource) AssetIndex() uint64 {
	if value == nil {
		return 0
	}
	return value.assetIndex
}

func (value *FrozenTextSource) Profile() string {
	if value == nil {
		return ""
	}
	return value.profile
}

func (value *FrozenTextSource) Text() string {
	if value == nil {
		return ""
	}
	return value.text
}

func (value *FrozenTextSource) FontAssetID() documentv2.ID {
	if value == nil {
		return ""
	}
	return value.fontAssetID
}

func (value *FrozenTextSource) FontAssetIndex() uint64 {
	if value == nil {
		return 0
	}
	return value.fontAssetIndex
}

func (value *FrozenTextSource) FontContentHash() string {
	if value == nil {
		return ""
	}
	return value.fontContentHash
}

func (value *FrozenTextSource) FontSize() documentv2.Rational {
	if value == nil {
		return documentv2.Rational{}
	}
	return value.fontSize
}

func (value *FrozenTextSource) LineHeight() documentv2.Rational {
	if value == nil {
		return documentv2.Rational{}
	}
	return value.lineHeight
}

func (value *FrozenTextSource) Viewport() documentv2.RasterViewport {
	if value == nil {
		return documentv2.RasterViewport{}
	}
	return value.viewport
}

func (value *FrozenTextSource) Fill() documentv2.ColorRGBA16 {
	if value == nil {
		return documentv2.ColorRGBA16{}
	}
	return value.fill
}

func (value *FrozenTextSource) Language() string {
	if value == nil {
		return ""
	}
	return value.language
}

func (value *FrozenTextSource) Script() string {
	if value == nil {
		return ""
	}
	return value.script
}

func (value *FrozenTextSource) Direction() documentv2.TextDirection {
	if value == nil {
		return ""
	}
	return value.direction
}

func (value *FrozenTextSource) Alignment() documentv2.TextAlignment {
	if value == nil {
		return ""
	}
	return value.alignment
}

// frozenTextSourceRecord is a dense v2-text record resolved in O(1) through the aligned, 1-based
// FrozenSnapshot.textSourceSlots table. It has value fields only; in particular it cannot retain
// TextSource's LineHeight or Viewport pointers.
type frozenTextSourceRecord struct {
	present bool

	documentHash           string
	documentCanonicalBytes uint64
	assetID                documentv2.ID
	assetIndex             uint64
	assetKind              documentv2.AssetKind
	assetContentHash       string

	textProfile         string
	evaluatorProfile    string
	text                string
	fontAssetID         documentv2.ID
	fontAssetIndex      uint64
	fontAssetKind       documentv2.AssetKind
	fontContentHash     string
	fontProfile         string
	runInputProfile     string
	layoutProfile       string
	outlineProfile      string
	rasterProfile       string
	paintProfile        string
	coverageHashProfile string
	fontSize            documentv2.Rational
	lineHeight          documentv2.Rational
	viewport            documentv2.RasterViewport
	fill                documentv2.ColorRGBA16
	language            string
	script              string
	direction           documentv2.TextDirection
	alignment           documentv2.TextAlignment

	unicodeVersion               string
	unicodeValidationProfile     string
	unicodeValidationHashProfile string
	unicodeValidationTableHash   string

	canonicalSourceHashProfile string
	canonicalSourceHash        string
	canonicalSourceBytes       uint64
	canonicalSourceUpperBytes  uint64
	textSemanticHashProfile    string
	textSemanticHash           string
	textSemanticCanonicalBytes uint64
	textSemanticUpperBytes     uint64
	layoutInputHashProfile     string
	layoutInputHash            string
	layoutInputCanonicalBytes  uint64
	layoutInputUpperBytes      uint64

	bindingHashProfile    string
	bindingHash           string
	bindingCanonicalBytes uint64
	canonicalBinaryBytes  uint64
	workUnits             uint64
	workingBytes          uint64
	retainedBytes         uint64
}
