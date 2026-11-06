package documentv2

import (
	"sort"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	EvaluatorTextV1                     = "cutly.evaluator.text.v1"
	EvaluatorTextPinnedFontViewportPXV2 = "cutly.evaluator.text-pinned-font-viewport-px.v2"
	EvaluatorVectorPathV1               = "cutly.evaluator.vector-path.v1"
	EvaluatorVectorPathViewportPXV2     = "cutly.evaluator.vector-path-viewport-px.v2"
)

const (
	TextFontProfileStaticGlyfV1       = "cutly.font.sfnt-single-face-static-glyf.v1"
	TextRunInputProfileV1             = "cutly.text-run.explicit-lines.v1"
	TextLayoutProfileUnwrappedV1      = "cutly.layout.text-unwrapped-top-baseline-logical-align.v1"
	TextOutlineProfileQ26_6V1         = "cutly.font-outline.q26_6-nearest-even.v1"
	TextRasterProfileSupersample8x8V1 = "cutly.raster.text-glyf-unhinted-supersample8x8.v1"
	TextPaintProfileRGBA16SRGBV1      = "cutly.color.rgba16-srgb-straight-paint-to-linear-bt709-premultiplied-rgba16.v1"
	TextCoverageHashProfileV1         = "cutly.hash.sha256.text-coverage-u8.v1"
	TextSemanticHashProfileV1         = "cutly.hash.sha256.text-source-font-profiles.v1"
	TextLayoutInputHashProfileV1      = "cutly.hash.sha256.text-layout-input-source.v1"
	TextUnicodeDataVersionV1          = "17.0.0"
)

// SourceDescriptor covers typed render expressions that are assets rather than optional
// PropertySet members. Their values are mandatory, so unlike a property descriptor they have no
// absence default. This split prevents callers from mistaking text shaping or vector paths for an
// opaque clip property.
type SourceDescriptor struct {
	Profile                      string                 `json:"profile"`
	AssetKind                    AssetKind              `json:"asset_kind"`
	ValueType                    string                 `json:"value_type"`
	EvaluatorProfile             string                 `json:"evaluator_profile"`
	RequiredCapability           videoedit.CapabilityID `json:"required_capability"`
	RequiredAssetKinds           []AssetKind            `json:"required_asset_kinds,omitempty"`
	KeyframeTargets              []KeyframeTarget       `json:"keyframe_targets,omitempty"`
	KeyframeProfile              string                 `json:"keyframe_profile,omitempty"`
	KeyframeCapability           videoedit.CapabilityID `json:"keyframe_capability,omitempty"`
	KeyframeSemantics            string                 `json:"keyframe_semantics,omitempty"`
	ValueSemantics               string                 `json:"value_semantics"`
	AgentDescription             string                 `json:"agent_description"`
	FontProfile                  string                 `json:"font_profile,omitempty"`
	RunInputProfile              string                 `json:"run_input_profile,omitempty"`
	ShapingProfile               string                 `json:"shaping_profile,omitempty"`
	RunInputSemantics            string                 `json:"run_input_semantics,omitempty"`
	LayoutProfile                string                 `json:"layout_profile,omitempty"`
	OutlineProfile               string                 `json:"outline_profile,omitempty"`
	RasterProfile                string                 `json:"raster_profile,omitempty"`
	PaintProfile                 string                 `json:"paint_profile,omitempty"`
	CoverageHashProfile          string                 `json:"coverage_hash_profile,omitempty"`
	SemanticHashProfile          string                 `json:"semantic_hash_profile,omitempty"`
	SemanticHashSemantics        string                 `json:"semantic_hash_semantics,omitempty"`
	LayoutInputHashProfile       string                 `json:"layout_input_hash_profile,omitempty"`
	LayoutInputHashSemantics     string                 `json:"layout_input_hash_semantics,omitempty"`
	UnicodeVersion               string                 `json:"unicode_version,omitempty"`
	UnicodeValidationProfile     string                 `json:"unicode_validation_profile,omitempty"`
	UnicodeValidationHashProfile string                 `json:"unicode_validation_hash_profile,omitempty"`
	UnicodeValidationTableHash   string                 `json:"unicode_validation_table_hash,omitempty"`
	UnicodeValidationProvenance  string                 `json:"unicode_validation_provenance,omitempty"`
	MaxUTF8Bytes                 uint64                 `json:"max_utf8_bytes,omitempty"`
	MaxLines                     uint64                 `json:"max_lines,omitempty"`
	MaxScalarsPerLine            uint64                 `json:"max_scalars_per_line,omitempty"`
	MaxViewportDimension         uint32                 `json:"max_viewport_dimension,omitempty"`
	FailureSemantics             string                 `json:"failure_semantics,omitempty"`
	ExcludedResponsibilities     string                 `json:"excluded_responsibilities,omitempty"`
}

var sourceDescriptorRegistry = []SourceDescriptor{
	{
		Profile: string(CapabilityTextV1), AssetKind: AssetKindText, ValueType: "text_source",
		EvaluatorProfile: EvaluatorTextV1, RequiredCapability: CapabilityTextV1,
		RequiredAssetKinds: []AssetKind{AssetKindFont},
		KeyframeTargets:    []KeyframeTarget{KeyframeTargetTextFill},
		KeyframeProfile:    KeyframeEvaluationEndpointClampV1,
		KeyframeCapability: CapabilityKeyframesV1,
		KeyframeSemantics:  keyframeEndpointClampDescription,
		ValueSemantics:     "Shape the exact UTF-8 text with the pinned font asset, exact font size, explicit language, direction and alignment; U+000A is the only automatic line-break control and fill is unpremultiplied sRGB RGBA16. The source is immutable at every mapped source time: SourceRange preserves edit/migration identity but never requests retime or transition handles. Media preflight fails when the pinned font cannot shape every required glyph; there is no host-font fallback.",
		AgentDescription:   "Deterministic plain text source; it does not accept HTML, CSS, host fonts, locale inference, bidi inference, or missing-glyph substitution.",
	},
	{
		Profile: VectorProfilePathViewportPXV2, AssetKind: AssetKindVector, ValueType: "vector_source",
		EvaluatorProfile: EvaluatorVectorPathViewportPXV2, RequiredCapability: CapabilityVectorPathViewportPXV2,
		ValueSemantics:   "Rasterize into the declared RasterViewport in top-down square-pixel edge coordinates with no host-DPI or physical-size interpretation. ViewBox coordinates remain exact user units and are never pixels: for viewport W,H and viewBox origin vx,vy and size vw,vh, use exact rational s=min(W/vw,H/vh) and map (x,y) to ((W-s*vw)/2+s*(x-vx),(H-s*vh)/2+s*(y-vy)); this is fixed xMidYMid meet. Leave uncovered viewport pixels transparent and clip all coverage to the viewport. Only move_to, line_to and close are accepted; every path is fill-only with an explicit nonzero/evenodd rule. For fill, each open subpath is conceptually closed by a straight segment from its current point to its most recent move_to when the next move_to begins or the path ends; this does not insert a command into canonical bytes. Fill values are inline unpremultiplied sRGB RGBA16 and use cutly.color.rgba16-srgb-straight-paint-to-linear-bt709-premultiplied-rgba16.v1 before paths paint in array order with source-over compositing. Cubic commands and strokes are rejected rather than approximated. The source is immutable at every mapped source time: SourceRange preserves edit/migration identity but never requests retime or transition handles.",
		AgentDescription: "Deterministic viewport-bound fill path source for a first native vector slice; intrinsic raster geometry and viewBox mapping are explicit, with no SVG/XML/CSS, scripts, external references, host DPI, stroke, or curve fallback.",
	},
	{
		Profile: VectorProfilePathV1, AssetKind: AssetKindVector, ValueType: "vector_source",
		EvaluatorProfile: EvaluatorVectorPathV1, RequiredCapability: CapabilityVectorPathV1,
		ValueSemantics:   "Interpret exact rational move, line, cubic and close commands in the declared viewBox. An open subpath is implicitly closed for fill only and remains open for stroke. Every path declares nonzero/evenodd fill rule; every centered stroke declares positive width, butt/round/square cap, miter/round/bevel join and positive miter limit. Paints are inline unpremultiplied sRGB RGBA16; no rasterizer default may alter these rules. The source is immutable at every mapped source time: SourceRange preserves edit/migration identity but never requests retime or transition handles.",
		AgentDescription: "Deterministic path vector source; all pixel-affecting path and stroke rules are explicit, and SVG/XML/CSS, scripts, external references and URL-valued paints are excluded.",
	},
}

var textPinnedFontViewportDescriptorV2 = SourceDescriptor{
	Profile: TextProfilePinnedFontViewportPXV2, AssetKind: AssetKindText, ValueType: "text_source",
	EvaluatorProfile: EvaluatorTextPinnedFontViewportPXV2, RequiredCapability: CapabilityTextPinnedFontViewportPXV2,
	RequiredAssetKinds:           []AssetKind{AssetKindFont},
	KeyframeTargets:              []KeyframeTarget{KeyframeTargetTextFill},
	KeyframeProfile:              KeyframeEvaluationEndpointClampV1,
	KeyframeCapability:           CapabilityKeyframesV1,
	KeyframeSemantics:            keyframeEndpointClampDescription,
	FontProfile:                  TextFontProfileStaticGlyfV1,
	RunInputProfile:              TextRunInputProfileV1,
	RunInputSemantics:            "Split only at U+000A and create one horizontal run-input buffer per line. Seed clusters with byte offsets into the original complete UTF-8 string, set explicit script/language/direction and BOT|EOT on every line, and require MONOTONE_CHARACTERS cluster level. Variation coordinates, ppem/device scale and caller feature overrides are empty. GuessSegmentProperties, process locale, bidi inference and font fallback are forbidden. This is a Document-level input contract, not a GSUB/GPOS algorithm: a backend may offer the Text v2 capability only with a separately versioned shape-provider profile and pinned implementation/dependency digest.",
	LayoutProfile:                TextLayoutProfileUnwrappedV1,
	OutlineProfile:               TextOutlineProfileQ26_6V1,
	RasterProfile:                TextRasterProfileSupersample8x8V1,
	PaintProfile:                 TextPaintProfileRGBA16SRGBV1,
	CoverageHashProfile:          TextCoverageHashProfileV1,
	SemanticHashProfile:          TextSemanticHashProfileV1,
	SemanticHashSemantics:        "Hash a canonical envelope containing the complete TextSource (including FontAssetID and fill), pinned font content hash, evaluator/font/run/layout/outline/raster/paint/coverage/layout-input profiles, and the frozen Unicode version/table identity. Font URI, font metadata and unrelated Asset members are excluded.",
	LayoutInputHashProfile:       TextLayoutInputHashProfileV1,
	LayoutInputHashSemantics:     "Hash a canonical envelope containing text, font size, line height, viewport, language, script, direction, alignment, pinned font content hash, evaluator/font/run/layout/outline/raster/coverage profiles, and the frozen Unicode version/table identity. Exclude fill, paint, FontAssetID, font URI and font metadata. It is suitable for grouping equal fill-independent inputs but is not by itself a final shaped-glyph or coverage-mask cache key; a provider must also bind its versioned shaping algorithm/dependency receipt and concrete outline/raster receipts.",
	UnicodeVersion:               TextUnicodeDataVersionV1,
	UnicodeValidationProfile:     TextUnicodeValidationTableProfileV1,
	UnicodeValidationHashProfile: TextUnicodeValidationTableHashProfileV1,
	UnicodeValidationTableHash:   TextUnicodeValidationTableHashV1,
	UnicodeValidationProvenance:  "Checked-in range tables generated from Unicode 17.0.0 Script properties, General_Category Cc/Cf, variation-selector ranges, and DerivedCoreProperties Default_Ignorable_Code_Point; production validation uses only these frozen tables and never the Go toolchain's mutable Unicode tables.",
	MaxUTF8Bytes:                 TextMaxUTF8Bytes,
	MaxLines:                     TextMaxLines,
	MaxScalarsPerLine:            TextMaxScalarsPerLine,
	MaxViewportDimension:         RasterViewportMaxDimension,
	ValueSemantics:               "Consume the exact UTF-8 bytes without Unicode normalization under the declared run-input contract. Font size is the em-square size in viewport pixels. Font-space glyph advances, offsets and glyf outlines are signed y-up Q26.6 values; scale each complete exact font-unit value by fontSize/unitsPerEm and round once to Q26.6 pixels using nearest-even. This horizontal profile requires shaped YAdvance=0 for every glyph; XAdvance supplies the signed pen advance. The first top-down baseline is nearest-even(hhea.ascender*fontSize/unitsPerEm*64); line i is computed directly as nearest-even((hhea.ascender*fontSize/unitsPerEm+i*line_height)*64), never by accumulating rounded baselines. For one line let signed total X advance be A: ltr requires A>=0, rtl requires A<=0, and the alignment advance box is [min(0,A),max(0,A)]. Logical start places that box at the left viewport edge for ltr and right edge for rtl; logical end does the opposite; center places it at the physical center. Negative bearings, glyph offsets and ink bounds never affect alignment. For each glyph before advancing the pen, map viewportX=alignedPenX+scaledXOffset+scaledOutlineX and viewportY=baselineY-scaledYOffset-scaledOutlineY, so positive shaped YOffset moves ink upward; then add the signed scaled XAdvance to the pen. Lines are never automatically wrapped. Rasterize monochrome static TrueType glyf outlines into the declared top-down square-pixel viewport, clip all coverage to it, and apply the evaluator-selected fill exactly once through cutly.color.rgba16-srgb-straight-paint-to-linear-bt709-premultiplied-rgba16.v1 after coverage. The source is immutable at every mapped source time: SourceRange preserves edit identity but never requests retime or transition handles.",
	AgentDescription:             "Pinned-font plain text with an explicit intrinsic viewport and deterministic layout contract; this profile defines semantics and does not permit HTML/CSS, system fonts, locale inference or fallback.",
	FailureSemantics:             "Invalid UTF-8, unsupported or mixed scripts, forbidden controls/format characters, every Unicode 17.0.0 Default_Ignorable_Code_Point value, missing glyphs, missing or mismatched font bindings, unsupported font tables/containers, malformed outlines, resource overflow, cancellation and any profile/hash/scope mismatch fail closed without a partial layout, surface or artifact.",
	ExcludedResponsibilities:     "This T0 descriptor does not select or attest a GSUB/GPOS algorithm or software dependency; a T2 shape-provider profile and pinned dependency digest must close that boundary before a backend offers this capability. Automatic wrapping, bidi inference or mixed-direction runs, Unicode normalization, font fallback, TTC/OTC, WOFF/WOFF2, CFF/CFF2, variable fonts, color fonts, SVG glyphs, hinting, LCD/subpixel rendering, host DPI, rich text, HTML/CSS and text transitions are outside this profile.",
}

// SourceDescriptors returns a profile-sorted defensive copy.
func SourceDescriptors() []SourceDescriptor {
	result := make([]SourceDescriptor, len(sourceDescriptorRegistry))
	for index, descriptor := range sourceDescriptorRegistry {
		result[index] = cloneSourceDescriptor(descriptor)
	}
	return result
}

// TextPinnedFontViewportDescriptor returns the additive renderable Text v2 contract without
// changing the frozen SourceDescriptors/SemanticDescriptors v1 and v2 canonical surfaces.
func TextPinnedFontViewportDescriptor() SourceDescriptor {
	return cloneSourceDescriptor(textPinnedFontViewportDescriptorV2)
}

// SourceDescriptorsWithTextV2 returns a profile-sorted defensive union of the frozen base source
// registry and the additive Text v2 descriptor.
func SourceDescriptorsWithTextV2() []SourceDescriptor {
	result := SourceDescriptors()
	result = append(result, TextPinnedFontViewportDescriptor())
	sort.Slice(result, func(left, right int) bool { return result[left].Profile < result[right].Profile })
	return result
}

// LookupSourceDescriptor returns a defensive copy of a typed asset expression descriptor.
func LookupSourceDescriptor(profile string) (SourceDescriptor, bool) {
	if profile == TextProfilePinnedFontViewportPXV2 {
		return TextPinnedFontViewportDescriptor(), true
	}
	for _, descriptor := range sourceDescriptorRegistry {
		if descriptor.Profile == profile {
			return cloneSourceDescriptor(descriptor), true
		}
	}
	return SourceDescriptor{}, false
}

func cloneSourceDescriptor(value SourceDescriptor) SourceDescriptor {
	result := value
	result.RequiredAssetKinds = append([]AssetKind(nil), value.RequiredAssetKinds...)
	result.KeyframeTargets = append([]KeyframeTarget(nil), value.KeyframeTargets...)
	return result
}
