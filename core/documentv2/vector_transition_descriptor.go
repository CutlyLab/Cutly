package documentv2

const VectorTransitionScopeProfileV1 = "cutly.transition-scope.vector-path-viewport-px.v1"

// VectorTransitionScopeDescriptor is an additive scope amendment for the existing dissolve
// profile. Keeping it separate preserves the canonical cutly.transition.dissolve.v1 descriptor
// while making the narrower viewport-vector contract independently discoverable and hashable.
type VectorTransitionScopeDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	RequiredCapability       string `json:"required_capability"`
	BaseTransitionProfile    string `json:"base_transition_profile"`
	TrackKind                string `json:"track_kind"`
	AssetKind                string `json:"asset_kind"`
	VectorProfile            string `json:"vector_profile"`
	VectorEvaluatorProfile   string `json:"vector_evaluator_profile"`
	TransitionEvaluator      string `json:"transition_evaluator_profile"`
	ScopeSemantics           string `json:"scope_semantics"`
	SourceSemantics          string `json:"source_semantics"`
	HandleSemantics          string `json:"handle_semantics"`
	CapabilitySemantics      string `json:"capability_semantics"`
	ProviderSemantics        string `json:"provider_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var vectorTransitionScopeV1 = VectorTransitionScopeDescriptor{
	SchemaVersion:            1,
	Profile:                  VectorTransitionScopeProfileV1,
	RequiredCapability:       VectorTransitionScopeProfileV1,
	BaseTransitionProfile:    TransitionDissolveV1,
	TrackKind:                string(TrackKindVector),
	AssetKind:                string(AssetKindVector),
	VectorProfile:            VectorProfilePathViewportPXV2,
	VectorEvaluatorProfile:   EvaluatorVectorPathViewportPXV2,
	TransitionEvaluator:      EvaluatorDissolveV1,
	ScopeSemantics:           "The existing dissolve profile may connect adjacent enabled clips on one TrackKindVector only when both clips reference AssetKindVector sources using cutly.vector.path-viewport-px.v2. Text tracks, legacy vector v1 and mixed track/source kinds remain invalid.",
	SourceSemantics:          "Both vector expressions are immutable intrinsic raster sources. At every active transition time the evaluator still computes each clip's exact timeline/local/source handle time and clip properties; only the source pixels are time-invariant.",
	HandleSemantics:          "The active half-open dissolve window and complementary weights remain exactly cutly.evaluator.transition-dissolve.v1. Vector sources need no temporal media head or tail bytes, but a source provider must explicitly authorize the actual transition ID, role and out-of-visible handle time; clamping or substituting a visible clip time is forbidden.",
	CapabilitySemantics:      "A non-zero vector dissolve requires cutly.transition-scope.vector-path-viewport-px.v1, cutly.vector.path-viewport-px.v2 and cutly.transition.dissolve.v1 in addition to every authored property/keyframe capability. The scoped token proves the backend implements their composition rather than merely supporting static vectors and video-only dissolves separately.",
	ProviderSemantics:        "A transition-aware expression provider binds the complete document/timeline/track/transition/clip/asset scope, outgoing or incoming role, actual evaluator timeline/local/source time, intrinsic viewport, semantic source hash and full raster profile chain. The ordinary visible-clip provider contract remains unchanged.",
	FailureSemantics:         "Unsupported vector profiles, cross-track or non-adjacent clips, disabled clips, mixed asset kinds, wrong transition role/scope/time, missing provider capability or any receipt mismatch fail closed. There is no edge-frame substitution or visible-time clamp.",
	ExcludedResponsibilities: "Text transitions, vector strokes or curves, SVG/CSS, motion interpolation inside the vector source, masks, non-dissolve transitions, provider/runtime attestation, encoding and publication remain outside this amendment.",
}

// VectorTransitionScope returns the immutable additive descriptor by value.
func VectorTransitionScope() VectorTransitionScopeDescriptor { return vectorTransitionScopeV1 }

// VectorTransitionScopeDescriptors returns a defensive discovery snapshot in registry order.
func VectorTransitionScopeDescriptors() []VectorTransitionScopeDescriptor {
	return []VectorTransitionScopeDescriptor{vectorTransitionScopeV1}
}
