// Package documentv2 defines Cutly's second-generation canonical editing document.
//
// It deliberately lives beside the v1 videoedit package while the schema gate is incubated.
// Adapters can migrate committed v1 documents without changing the currently shipped transaction
// and renderer contracts. M2 evaluators consume this package directly; it is not an alternate
// mutable source of truth.
package documentv2

import (
	"encoding/json"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

const SchemaVersion = 2

type ID = videoedit.ID
type Revision = videoedit.Revision
type RationalTime = videoedit.RationalTime
type TimeRange = videoedit.TimeRange

type AssetKind string

const (
	AssetKindVideo AssetKind = "video"
	AssetKindAudio AssetKind = "audio"
	// AssetKindImage is an immutable still. Animated image formats must be normalized/probed as
	// video so duration, retime and transition-handle semantics remain explicit.
	AssetKindImage  AssetKind = "image"
	AssetKindFont   AssetKind = "font"
	AssetKindData   AssetKind = "data"
	AssetKindText   AssetKind = "text"
	AssetKindVector AssetKind = "vector"
)

type TrackKind string

const (
	TrackKindVideo      TrackKind = "video"
	TrackKindAudio      TrackKind = "audio"
	TrackKindText       TrackKind = "text"
	TrackKindVector     TrackKind = "vector"
	TrackKindAnnotation TrackKind = "annotation"
)

// Rational is an exact, dimensionless scalar. Valid wire values are reduced, the denominator is
// positive, and zero has the single representation 0/1.
type Rational struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

type Point struct {
	X Rational `json:"x"`
	Y Rational `json:"y"`
}

type Rect struct {
	X      Rational `json:"x"`
	Y      Rational `json:"y"`
	Width  Rational `json:"width"`
	Height Rational `json:"height"`
}

// ColorRGBA16 freezes unpremultiplied sRGB input channels as integers. Conversion to the M2
// linear-light premultiplied compositor is a separately versioned evaluator rule.
type ColorRGBA16 struct {
	R uint16 `json:"r"`
	G uint16 `json:"g"`
	B uint16 `json:"b"`
	A uint16 `json:"a"`
}

type Document struct {
	SchemaVersion int          `json:"schema_version"`
	ID            ID           `json:"id"`
	Revision      Revision     `json:"revision"`
	Assets        []Asset      `json:"assets,omitempty"`
	Timelines     []Timeline   `json:"timelines,omitempty"`
	AudioBuses    []AudioBus   `json:"audio_buses,omitempty"`
	Annotations   []Annotation `json:"annotations,omitempty"`
	// Metadata and annotations are non-rendering intent. Pixel/PCM semantics must use a typed
	// field or an affects_rendering ExtensionProperty with an explicit capability.
	Metadata map[string]json.RawMessage `json:"metadata,omitempty"`
}

type Asset struct {
	ID          ID                         `json:"id"`
	Kind        AssetKind                  `json:"kind"`
	URI         string                     `json:"uri,omitempty"`
	ContentHash string                     `json:"content_hash,omitempty"`
	Duration    *RationalTime              `json:"duration,omitempty"`
	Text        *TextSource                `json:"text,omitempty"`
	Vector      *VectorSource              `json:"vector,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

// TextSource is intentionally a shaping input, not HTML or rich-text markup. FontAssetID must
// reference an immutable font asset. Language and direction are mandatory so shaping does not
// depend on host locale or heuristic bidi detection.
//
// Profile == "" is the frozen legacy cutly.text.v1 spelling. Its optional v2 members must be
// absent, which preserves every existing canonical byte. TextProfilePinnedFontViewportPXV2 is an
// additive pixel contract and requires LineHeight, Viewport and Script explicitly.
type TextSource struct {
	Profile     string          `json:"profile,omitempty"`
	Text        string          `json:"text"`
	FontAssetID ID              `json:"font_asset_id"`
	FontSize    Rational        `json:"font_size"`
	LineHeight  *Rational       `json:"line_height,omitempty"`
	Viewport    *RasterViewport `json:"viewport,omitempty"`
	Fill        ColorRGBA16     `json:"fill"`
	Language    string          `json:"language"`
	Script      string          `json:"script,omitempty"`
	Direction   TextDirection   `json:"direction"`
	Alignment   TextAlignment   `json:"alignment"`
}

const (
	// TextProfilePinnedFontViewportPXV2 freezes the first renderable text-pixel contract. The
	// legacy cutly.text.v1 contract remains represented only by an omitted Profile field.
	TextProfilePinnedFontViewportPXV2 = "cutly.text.pinned-font-viewport-px.v2"

	// These are schema hard gates, not renderer allocation promises. A concrete renderer may
	// advertise smaller limits and must enforce them before allocating or shaping.
	TextMaxUTF8Bytes      = 1 << 20
	TextMaxLines          = 65_536
	TextMaxScalarsPerLine = 65_536
)

const (
	TextScriptLatin  = "Latn"
	TextScriptHan    = "Hani"
	TextScriptArabic = "Arab"
)

type TextDirection string

const (
	TextDirectionLTR TextDirection = "ltr"
	TextDirectionRTL TextDirection = "rtl"
)

type TextAlignment string

const (
	TextAlignmentStart  TextAlignment = "start"
	TextAlignmentCenter TextAlignment = "center"
	TextAlignmentEnd    TextAlignment = "end"
)

const (
	VectorProfilePathV1           = "cutly.vector.path.v1"
	VectorProfilePathViewportPXV2 = "cutly.vector.path-viewport-px.v2"

	// RasterViewportMaxDimension is a schema hard gate, not an allocation promise. Renderers may
	// advertise smaller limits, but no conforming Document v2 viewport exceeds this value.
	RasterViewportMaxDimension uint32 = 65_536
)

// RasterViewport fixes an intrinsic raster surface in top-down, square-pixel edge coordinates:
// x is in [0, WidthPixels] and y is in [0, HeightPixels]. It carries no physical-size or host-DPI
// meaning.
type RasterViewport struct {
	WidthPixels  uint32 `json:"width_pixels"`
	HeightPixels uint32 `json:"height_pixels"`
}

// VectorSource accepts only a deterministic command list with exact coordinates. Arbitrary SVG,
// XML, CSS, scripts, external references and URL-valued paints are not part of this contract.
type VectorSource struct {
	Profile  string          `json:"profile"`
	ViewBox  Rect            `json:"view_box"`
	Viewport *RasterViewport `json:"viewport,omitempty"`
	Paths    []VectorPath    `json:"paths"`
}

type VectorPath struct {
	ID       ID              `json:"id"`
	Commands []VectorCommand `json:"commands"`
	FillRule VectorFillRule  `json:"fill_rule"`
	Fill     *ColorRGBA16    `json:"fill,omitempty"`
	Stroke   *VectorStroke   `json:"stroke,omitempty"`
}

type VectorStroke struct {
	Color      ColorRGBA16    `json:"color"`
	Width      Rational       `json:"width"`
	LineCap    VectorLineCap  `json:"line_cap"`
	LineJoin   VectorLineJoin `json:"line_join"`
	MiterLimit Rational       `json:"miter_limit"`
}

type VectorFillRule string

const (
	VectorFillNonZero VectorFillRule = "nonzero"
	VectorFillEvenOdd VectorFillRule = "evenodd"
)

type VectorLineCap string

const (
	VectorCapButt   VectorLineCap = "butt"
	VectorCapRound  VectorLineCap = "round"
	VectorCapSquare VectorLineCap = "square"
)

type VectorLineJoin string

const (
	VectorJoinMiter VectorLineJoin = "miter"
	VectorJoinRound VectorLineJoin = "round"
	VectorJoinBevel VectorLineJoin = "bevel"
)

type VectorCommandOp string

const (
	VectorMoveTo  VectorCommandOp = "move_to"
	VectorLineTo  VectorCommandOp = "line_to"
	VectorCubicTo VectorCommandOp = "cubic_to"
	VectorClose   VectorCommandOp = "close"
)

// Points arity is fixed by Op: move_to/line_to=1, cubic_to=3, close=0.
type VectorCommand struct {
	Op     VectorCommandOp `json:"op"`
	Points []Point         `json:"points,omitempty"`
}

type Timeline struct {
	ID          ID                         `json:"id"`
	Name        string                     `json:"name,omitempty"`
	Tracks      []Track                    `json:"tracks,omitempty"`
	Transitions []Transition               `json:"transitions,omitempty"`
	Annotations []Annotation               `json:"annotations,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

type Track struct {
	ID          ID                         `json:"id"`
	Kind        TrackKind                  `json:"kind"`
	Name        string                     `json:"name,omitempty"`
	Clips       []Clip                     `json:"clips,omitempty"`
	AudioBusID  ID                         `json:"audio_bus_id,omitempty"`
	Annotations []Annotation               `json:"annotations,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

// Clip maps an exact edit interval. For temporal video/audio assets SourceRange participates in
// exact timeline-to-source mapping. Image, text and vector assets are static: every mapped source
// time returns the same immutable source, so SourceRange is retained only as stable edit/migration
// identity and never requests a temporal-retime capability or transition handle.
type Clip struct {
	ID            ID             `json:"id"`
	AssetID       ID             `json:"asset_id"`
	TimelineRange TimeRange      `json:"timeline_range"`
	SourceRange   TimeRange      `json:"source_range"`
	Disabled      bool           `json:"disabled,omitempty"`
	PropertySet   *PropertySet   `json:"property_set,omitempty"`
	KeyframeLanes []KeyframeLane `json:"keyframe_lanes,omitempty"`
	Annotations   []Annotation   `json:"annotations,omitempty"`
}

const (
	TransitionDissolveV1 = "cutly.transition.dissolve.v1"
	TransitionFadeV1     = "cutly.transition.fade.v1"
	TransitionWipeV1     = "cutly.transition.wipe.v1"
	PropertyTransformV1  = "cutly.transform.v1"
	PropertyOpacityV1    = "cutly.opacity.v1"
	PropertyCropV1       = "cutly.crop.v1"
	PropertyAudioGainV1  = "cutly.audio.gain.v1"
	PropertyAudioPanV1   = "cutly.audio.pan.v1"
	PropertyAudioRouteV1 = "cutly.audio.route.v1"
	// PropertyAudioChannelMatrixV1 maps one explicitly identified source-channel order to
	// stereo with exact coefficients. It is intentionally separate from pan: the matrix runs
	// first and pan then balances the resulting left/right pair.
	PropertyAudioChannelMatrixV1 = "cutly.audio.channel-matrix.stereo.v1"
	// PropertyAudioSamplePeakLimiterV1 is the zero-lookahead, stereo-linked sample-peak
	// limiter on the explicit master bus. It is not a true-peak limiter.
	PropertyAudioSamplePeakLimiterV1 = "cutly.audio.limiter.sample-peak-instant.v1"
)

type Transition struct {
	ID             ID           `json:"id"`
	Profile        string       `json:"profile"`
	OutgoingClipID ID           `json:"outgoing_clip_id"`
	IncomingClipID ID           `json:"incoming_clip_id"`
	PreDuration    RationalTime `json:"pre_duration"`
	PostDuration   RationalTime `json:"post_duration"`
}

type PropertySet struct {
	Transform          *TransformProperty          `json:"transform,omitempty"`
	Opacity            *OpacityProperty            `json:"opacity,omitempty"`
	Crop               *CropProperty               `json:"crop,omitempty"`
	AudioGain          *AudioGainProperty          `json:"audio_gain,omitempty"`
	AudioPan           *AudioPanProperty           `json:"audio_pan,omitempty"`
	AudioChannelMatrix *AudioChannelMatrixProperty `json:"audio_channel_matrix,omitempty"`
	Extensions         []ExtensionProperty         `json:"extensions,omitempty"`
}

type TransformProperty struct {
	Position        Point    `json:"position"`
	Scale           Point    `json:"scale"`
	RotationDegrees Rational `json:"rotation_degrees"`
	Anchor          Point    `json:"anchor"`
}

type OpacityProperty struct {
	Value Rational `json:"value"`
}

// Crop coordinates are normalized to the source surface and must remain within [0,1].
type CropProperty struct {
	Rect Rect `json:"rect"`
}

type AudioGainProperty struct {
	Linear Rational `json:"linear"`
}

type AudioPanProperty struct {
	Value Rational `json:"value"`
}

const AudioChannelMatrixMaxInputs = 32

// AudioChannelMatrixProperty binds its coefficient order to both the exact probed layout name
// and an explicit decoder input-channel order. Media preflight must match SourceLayout and the
// channel count to the selected ProbeManifest stream, then prove that the decoder exposes exactly
// SourceChannels in the authored order; it must not infer or reorder channels. The matrix emits
// stereo: left=sum(input[i]*LeftCoefficients[i]) and likewise for right. It performs no implicit
// normalization, clipping, or rounding; either stereo result may exceed nominal full scale and
// remains exact until an explicitly authored downstream stage handles it.
type AudioChannelMatrixProperty struct {
	SourceLayout      string     `json:"source_layout"`
	SourceChannels    []string   `json:"source_channels"`
	LeftCoefficients  []Rational `json:"left_coefficients"`
	RightCoefficients []Rational `json:"right_coefficients"`
}

// ExtensionProperty can round-trip an unrecognized namespace. AffectsRendering=true never grants
// execution permission by itself: RequiredCapability must be offered by a selected backend or
// PreflightDiagnostics rejects the document.
type ExtensionProperty struct {
	Namespace          string          `json:"namespace"`
	AffectsRendering   bool            `json:"affects_rendering"`
	RequiredCapability string          `json:"required_capability,omitempty"`
	Value              json.RawMessage `json:"value"`
}

type KeyframeValueType string

const (
	KeyframeValueScalar KeyframeValueType = "scalar"
	KeyframeValuePoint  KeyframeValueType = "point"
	KeyframeValueRect   KeyframeValueType = "rect"
	KeyframeValueColor  KeyframeValueType = "color"
)

type KeyframeTarget string

const (
	KeyframeTargetTransformPosition KeyframeTarget = "transform.position"
	KeyframeTargetTransformScale    KeyframeTarget = "transform.scale"
	KeyframeTargetTransformRotation KeyframeTarget = "transform.rotation_degrees"
	KeyframeTargetTransformAnchor   KeyframeTarget = "transform.anchor"
	KeyframeTargetOpacity           KeyframeTarget = "opacity.value"
	KeyframeTargetCrop              KeyframeTarget = "crop.rect"
	KeyframeTargetAudioGain         KeyframeTarget = "audio_gain.linear"
	KeyframeTargetAudioPan          KeyframeTarget = "audio_pan.value"
	KeyframeTargetTextFill          KeyframeTarget = "text.fill"
)

// KeyframeLane evaluation uses clip-local time. Before the first keyframe the first value is
// clamped; after the last keyframe the last value is clamped. At an exact keyframe offset that
// keyframe's value wins. Interpolation on keyframe i controls [offset_i, offset_i+1); hold returns
// value_i for that whole segment. A final offset equal to clip duration is only the right
// interpolation boundary because the clip's visible interval is half-open.
type KeyframeLane struct {
	ID        ID                `json:"id"`
	Target    KeyframeTarget    `json:"target"`
	ValueType KeyframeValueType `json:"value_type"`
	Keyframes []Keyframe        `json:"keyframes"`
}

type Interpolation string

const (
	InterpolationHold   Interpolation = "hold"
	InterpolationLinear Interpolation = "linear"
	InterpolationCubic  Interpolation = "cubic_bezier"
)

type Keyframe struct {
	// Offset is relative to Clip.TimelineRange.Start. Values in [0, clip.duration] are valid;
	// an endpoint exactly at duration is an interpolation boundary and is never sampled by the
	// clip's half-open visible interval.
	Offset        RationalTime  `json:"offset"`
	Value         KeyframeValue `json:"value"`
	Interpolation Interpolation `json:"interpolation,omitempty"`
	Easing        *CubicBezier  `json:"easing,omitempty"`
}

type KeyframeValue struct {
	Scalar *Rational    `json:"scalar,omitempty"`
	Point  *Point       `json:"point,omitempty"`
	Rect   *Rect        `json:"rect,omitempty"`
	Color  *ColorRGBA16 `json:"color,omitempty"`
}

type CubicBezier struct {
	X1 Rational `json:"x1"`
	Y1 Rational `json:"y1"`
	X2 Rational `json:"x2"`
	Y2 Rational `json:"y2"`
}

type AudioBusKind string

const (
	AudioBusMaster AudioBusKind = "master"
	AudioBusGroup  AudioBusKind = "group"
)

type AudioBus struct {
	ID          ID                 `json:"id"`
	Kind        AudioBusKind       `json:"kind"`
	Name        string             `json:"name,omitempty"`
	ParentBusID ID                 `json:"parent_bus_id,omitempty"`
	Properties  AudioBusProperties `json:"properties"`
}

type AudioBusProperties struct {
	GainLinear        Rational                        `json:"gain_linear"`
	Pan               Rational                        `json:"pan"`
	SamplePeakLimiter *AudioSamplePeakLimiterProperty `json:"sample_peak_limiter,omitempty"`
}

// AudioSamplePeakLimiterProperty is an instantaneous, stereo-linked sample-peak limiter applied
// after master gain and pan. Samples use nominal full-scale units but may exceed [-1,1] after
// summation/gain. For an exact stereo sample, peak=max(abs(L),abs(R)) and
// gain=min(1, CeilingLinear/peak), with silence using gain 1. The stage has no lookahead, state,
// attack/release, latency, true-peak estimation, or internal quantization. PCM quantization is a
// separate output-profile boundary.
type AudioSamplePeakLimiterProperty struct {
	CeilingLinear Rational `json:"ceiling_linear"`
}

// Annotation carries non-rendering intent at document/timeline/track/clip scope. Core evaluators
// never consume it; render semantics require a typed field or capability-gated extension.
type Annotation struct {
	ID        ID              `json:"id"`
	Namespace string          `json:"namespace"`
	Key       string          `json:"key,omitempty"`
	Range     *TimeRange      `json:"range,omitempty"`
	Value     json.RawMessage `json:"value"`
}
