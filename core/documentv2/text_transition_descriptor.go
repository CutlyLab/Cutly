package documentv2

const TextTransitionScopeProfileV1 = "cutly.transition-scope.text-pinned-font-viewport-px.v1"

// TextTransitionScopeDescriptor is an additive scope amendment for the existing dissolve
// profile. It prevents a backend that separately supports static Text v2 and video/image dissolve
// from claiming their composition without implementing the exact Text transition contract.
type TextTransitionScopeDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	RequiredCapability       string `json:"required_capability"`
	BaseTransitionProfile    string `json:"base_transition_profile"`
	TrackKind                string `json:"track_kind"`
	AssetKind                string `json:"asset_kind"`
	TextProfile              string `json:"text_profile"`
	TextEvaluatorProfile     string `json:"text_evaluator_profile"`
	TransitionEvaluator      string `json:"transition_evaluator_profile"`
	ScopeSemantics           string `json:"scope_semantics"`
	SourceSemantics          string `json:"source_semantics"`
	HandleSemantics          string `json:"handle_semantics"`
	CapabilitySemantics      string `json:"capability_semantics"`
	ProviderSemantics        string `json:"provider_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var textTransitionScopeV1 = TextTransitionScopeDescriptor{
	SchemaVersion:            1,
	Profile:                  TextTransitionScopeProfileV1,
	RequiredCapability:       TextTransitionScopeProfileV1,
	BaseTransitionProfile:    TransitionDissolveV1,
	TrackKind:                string(TrackKindText),
	AssetKind:                string(AssetKindText),
	TextProfile:              TextProfilePinnedFontViewportPXV2,
	TextEvaluatorProfile:     EvaluatorTextPinnedFontViewportPXV2,
	TransitionEvaluator:      EvaluatorDissolveV1,
	ScopeSemantics:           "The existing dissolve profile may connect adjacent enabled clips on one TrackKindText only when both clips reference AssetKindText sources using cutly.text.pinned-font-viewport-px.v2. Legacy Text v1, mixed tracks and mixed source kinds remain invalid.",
	SourceSemantics:          "Each Text source is an immutable intrinsic raster expression for its frozen shaping and layout identity. The evaluator still resolves every authored clip property and text.fill keyframe at each exact outgoing or incoming handle time.",
	HandleSemantics:          "The active half-open window and complementary weights remain exactly cutly.evaluator.transition-dissolve.v1. Text source bytes need no temporal media handles, but a transition-capable renderer must authorize the exact transition ID, role and evaluator handle time; visible-time clamping is forbidden.",
	CapabilitySemantics:      "A non-zero pinned Text v2 dissolve requires cutly.transition-scope.text-pinned-font-viewport-px.v1, cutly.text.pinned-font-viewport-px.v2 and cutly.transition.dissolve.v1 plus every authored property/keyframe capability. A zero-width hard cut requires no transition or scoped-transition capability.",
	ProviderSemantics:        "A transition-aware Text provider binds the complete document/timeline/track/transition/clip/asset/font scope, outgoing or incoming role, actual evaluator timeline/local/source time, evaluated paint, intrinsic viewport and complete authenticated shaping/raster receipt chain. An ordinary visible-only static Text provider does not satisfy this scope.",
	FailureSemantics:         "Unsupported Text profiles, non-adjacent or cross-track clips, disabled clips, wrong transition role/scope/time/paint, absent scoped capability, missing font authorization or any receipt mismatch fail closed without a partial surface.",
	ExcludedResponsibilities: "This amendment does not make a visible-only static Text provider transition-capable. Rich text, fallback fonts, mixed-script inference, non-dissolve transitions, masks, provider/runtime attestation, encoding and publication remain outside this scope.",
}

// TextTransitionScope returns the immutable additive descriptor by value.
func TextTransitionScope() TextTransitionScopeDescriptor { return textTransitionScopeV1 }

// TextTransitionScopeDescriptors returns a defensive discovery snapshot in registry order.
func TextTransitionScopeDescriptors() []TextTransitionScopeDescriptor {
	return []TextTransitionScopeDescriptor{textTransitionScopeV1}
}
