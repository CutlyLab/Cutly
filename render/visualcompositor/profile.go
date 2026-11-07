package visualcompositor

import (
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.reference-compositor.native-expression-affine-surface.v1"
	ProfileV2            = "cutly.reference-compositor.native-expression-text-affine-surface.v2"
	LayerOrderProfile    = evaluator.VisualLayerOrderBackgroundToForegroundV1
	PixelHashProfileV1   = "cutly.hash.sha256.surface-rgba16be-pixels.v1"
)

type ProfileDescriptor struct {
	SchemaVersion               int                             `json:"schema_version"`
	Profile                     string                          `json:"profile"`
	EvaluatorLayerOrder         string                          `json:"evaluator_layer_order"`
	NativeBaseProfile           string                          `json:"native_base_profile"`
	NativeSourceContractVersion int                             `json:"native_source_contract_version"`
	ExpressionProviderProfile   string                          `json:"expression_provider_profile"`
	ExpressionContractVersion   int                             `json:"expression_contract_version"`
	ExpressionHandleProfile     string                          `json:"expression_handle_profile"`
	ExpressionHandleContract    int                             `json:"expression_handle_contract_version"`
	TextProviderProfile         string                          `json:"text_provider_profile,omitempty"`
	TextContractVersion         int                             `json:"text_contract_version,omitempty"`
	SurfaceProfile              string                          `json:"surface_profile"`
	PixelHashProfile            string                          `json:"pixel_hash_profile"`
	RasterProfile               string                          `json:"raster_profile"`
	KernelProfile               string                          `json:"kernel_profile"`
	CapabilitySemantics         string                          `json:"capability_semantics"`
	DispatchSemantics           string                          `json:"dispatch_semantics"`
	SourceValidation            string                          `json:"source_validation"`
	ClipSemantics               string                          `json:"clip_semantics"`
	TransitionSemantics         string                          `json:"transition_semantics"`
	CompositionSemantics        string                          `json:"composition_semantics"`
	ReceiptSemantics            string                          `json:"receipt_semantics"`
	GapSemantics                string                          `json:"gap_semantics"`
	CancellationSemantics       string                          `json:"cancellation_semantics"`
	ResourceSemantics           string                          `json:"resource_semantics"`
	OwnershipSemantics          string                          `json:"ownership_semantics"`
	FailureSemantics            string                          `json:"failure_semantics"`
	ExcludedResponsibilities    string                          `json:"excluded_responsibilities"`
	DefaultLimits               Limits                          `json:"default_limits"`
	DefaultNativeProfiles       rastercompositor.SourceProfiles `json:"default_native_profiles"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:               ProfileSchemaVersion,
	Profile:                     ProfileV1,
	EvaluatorLayerOrder:         LayerOrderProfile,
	NativeBaseProfile:           rastercompositor.ProfileV1,
	NativeSourceContractVersion: rastercompositor.NativeSourceContractVersion,
	ExpressionProviderProfile:   expressionlayer.ProfileV1,
	ExpressionContractVersion:   expressionlayer.ContractVersion,
	ExpressionHandleProfile:     expressionlayer.TransitionHandleProfileV2,
	ExpressionHandleContract:    expressionlayer.TransitionHandleContractVersion,
	SurfaceProfile:              cpucompositor.SurfaceProfileV1,
	PixelHashProfile:            PixelHashProfileV1,
	RasterProfile:               cpuraster.ProfileV1,
	KernelProfile:               cpucompositor.KernelProfileV1,
	CapabilitySemantics:         "This v1 Compile consumes a complete visual-only Document v2 and advertises only explicitly enabled video, image and cutly.vector.path-viewport-px.v2 source kinds plus the exact visual properties, keyframes, interpolation, dissolve and optional linear-video-retime semantics implemented here. Non-zero vector dissolves additionally advertise the scoped cutly.transition-scope.vector-path-viewport-px.v1 capability. Enabled text, audio, legacy vector v1 and rendering extensions fail capability negotiation; they are never advertised or silently ignored.",
	DispatchSemantics:           "Freeze the canonical Document v2 asset, timeline, track, clip and transition indexes. For every evaluator ClipEvaluation, TrackKindVideo with AssetKindVideo/Image dispatches only to the native provider; TrackKindVector with AssetKindVector/path-viewport-px.v2 dispatches only to the expression provider. No provider chooses its own scope or z-order.",
	SourceValidation:            "Native responses must echo the complete rastercompositor request and match frozen content hash, allowlisted profiles, geometry and independently recomputed surface hash. Expression responses must echo the complete visible or transition-handle request and match frozen semantic source hash, viewport, profile chain, raster receipt counts/geometry and independently recomputed surface hash. Provider claims never replace these checks.",
	ClipSemantics:               "Apply evaluator crop and transform through cutly.raster.cpu-affine-bilinear-linear-rgba16.v1 to either native or intrinsic vector surface, then evaluator opacity through the CPU kernel. Provider surfaces remain immutable and are never modified.",
	TransitionSemantics:         "For an active dissolve, render outgoing then incoming evaluator handles with their exact actual times and properties. Native media uses its native request. Vector expressions use the additive transition-handle v2 provider contract with exact transition ID/index/profile and role; visible-time clamping is forbidden. Dissolve with the evaluator's exact complementary weights.",
	CompositionSemantics:        "Process evaluator layers strictly in background-to-foreground track-array order and source-over each completed target layer. Native/vector interleaving, provider completion order and map iteration cannot change z-order.",
	ReceiptSemantics:            "Result sources are ordered by evaluator layer and outgoing-before-incoming transition role. Each has a strict outer clip-versus-transition evaluation union with both JSON keys present; a transition branch carries transition ID/index/profile/role plus its exact ClipBinding, so visible handles cannot masquerade as ordinary clips. The provider receipt is a second native-versus-expression union, and expression receipts use a third visible-request-versus-transition-request union. Every linked scope/time agrees exactly. Strict live validation checks runtime self-consistency by requiring Surface and recomputing its final canonical pixel hash; it does not prove source-pixel causality. Serialized structural validation treats PixelHash as an unauthenticated claim. Offline provenance requires trusted provider replay, an artifact chain or trusted-execution signature.",
	GapSemantics:                "A timeline time with no active supported visual layer returns one complete transparent target Surface and an explicit empty source array.",
	CancellationSemantics:       "Check non-nil context during document preflight and freezing, before and after every provider call and source hash, throughout CPU raster, between atomic opacity/dissolve/composite stages and before publication. Any failure returns a zero Result and no partial Surface.",
	ResourceSemantics:           "Bound document graph/canonical bytes and tracks before compile; bound active layers/source requests, every provider surface, target/raster dimensions, exact geometry bits and the fixed six-target-frame working peak. Full target layers are processed incrementally; receipts scale only to MaxSourceRequests.",
	OwnershipSemantics:          "Compile deep-freezes canonical document-derived dispatch state. Programs support arbitrary seek order and concurrent immutable reads. Provider surfaces are borrowed only for validation/rasterization, target surfaces own their pixels, capabilities are defensive copies and serializable receipts contain no mutable provider storage.",
	FailureSemantics:            "Unsupported capability, wrong provider route, scope/index/kind/profile/hash/time/geometry/receipt mismatch, unavailable required provider, overflow, cancellation or kernel failure fails closed. There is no provider fallback, lower-quality vector path, partial source set or success with a missing layer.",
	ExcludedResponsibilities:    "Mixed A/V documents are outside this v1 because evaluator capability negotiation currently gates the complete document; a future visual-scope preflight must preserve the original DocumentHash while ignoring only non-visual requirements. Media resource authorization/decode trust, VFR/PTS/rotation/SAR/YUV/HDR rules, vector strokes/curves, text shaping, audio, masks, output color conversion, encoding, muxing, persistence, scheduling, signatures and publication also remain outside this compositor.",
	DefaultLimits:               DefaultLimits(),
	DefaultNativeProfiles:       rastercompositor.DefaultSourceProfiles(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

var descriptorV2 = func() ProfileDescriptor {
	value := descriptorV1
	value.Profile = ProfileV2
	value.TextProviderProfile = textlayer.ProfileV1
	value.TextContractVersion = textlayer.ContractVersion
	value.CapabilitySemantics = "This additive v2 Compile preserves every v1 native/vector capability and may additionally advertise only cutly.text.pinned-font-viewport-px.v2 when StaticTextV2 is enabled. Enabled Text is restricted to static frozen fill plus transform, crop and opacity; text.fill lanes, non-zero Text transitions, legacy Text and rendering extensions fail closed."
	value.DispatchSemantics = "Freeze the canonical Document v2 asset, timeline, track, clip and transition indexes. Video/image dispatch only to native, viewport-vector only to expression, and pinned static Text v2 only to the text-layer provider. No provider chooses scope, time or z-order."
	value.SourceValidation = "Apply all v1 validation and, for Text, require the complete textlayer request echo, frozen binding/semantic/layout/font identities, provider-authenticated assembly and raster receipts, exact viewport/static paint lineage and an independently recomputed immutable surface hash."
	value.ClipSemantics = "Apply evaluator crop and transform through cutly.raster.cpu-affine-bilinear-linear-rgba16.v1 to native, intrinsic vector or intrinsic Text surfaces, then evaluator opacity. TextFill must exist and equal the frozen asset's static fill exactly; it is evidence only and is never reapplied after text rasterization."
	value.TransitionSemantics = "Native and vector dissolve semantics remain v1. Any non-zero transition involving a Text track is rejected at compile and a Text transition reaching runtime fails as an invariant; zero-width records remain inert hard cuts."
	value.CompositionSemantics = "Process evaluator layers strictly in background-to-foreground track-array order and source-over each completed target layer. Native/vector/Text interleaving, provider completion order and map iteration cannot change z-order."
	value.ReceiptSemantics = "V2 preserves the v1 ordered evaluation union and adds an optional Text branch containing the exact textlayer FrameReceipt. Text is visible-clip-only; every scope/time/profile/identity agrees with the frozen Program. Serialized receipts remain structural claims, while live validation recomputes final pixels."
	value.ExcludedResponsibilities = "Dynamic text paint, rich text, fallback/system fonts, bidi inference, Text transitions, media decode trust, audio, masks, output color conversion, encoding, muxing, persistence, scheduling, signatures and publication remain outside this compositor."
	return value
}()

func DescriptorV2() ProfileDescriptor { return descriptorV2 }

func profileForConfig(config Config) string {
	if config.Source.StaticTextV2 {
		return ProfileV2
	}
	return ProfileV1
}
