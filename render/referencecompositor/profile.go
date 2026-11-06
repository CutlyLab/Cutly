package referencecompositor

import (
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	ProfileV1         = "cutly.reference-compositor.normalized-surface.v1"
	SourceProfileV1   = "cutly.source.normalized-full-frame-surface.v1"
	LayerOrderProfile = evaluator.VisualLayerOrderBackgroundToForegroundV1
)

// ProfileDescriptor freezes the narrow assembly contract. It is intentionally not the complete
// M2 compositor profile: every excluded stage must be implemented and versioned before a backend
// may advertise the corresponding Document v2 capability.
type ProfileDescriptor struct {
	Profile               string `json:"profile"`
	EvaluatorLayerOrder   string `json:"evaluator_layer_order"`
	SourceProfile         string `json:"source_profile"`
	SurfaceProfile        string `json:"surface_profile"`
	KernelProfile         string `json:"kernel_profile"`
	SourceSemantics       string `json:"source_semantics"`
	ClipSemantics         string `json:"clip_semantics"`
	TransitionSemantics   string `json:"transition_semantics"`
	CompositionSemantics  string `json:"composition_semantics"`
	GapSemantics          string `json:"gap_semantics"`
	CancellationSemantics string `json:"cancellation_semantics"`
	IdentitySemantics     string `json:"identity_semantics"`
	HashBindingSemantics  string `json:"hash_binding_semantics"`
	ExcludedStages        string `json:"excluded_stages"`
}

var profileV1 = ProfileDescriptor{
	Profile:               ProfileV1,
	EvaluatorLayerOrder:   LayerOrderProfile,
	SourceProfile:         SourceProfileV1,
	SurfaceProfile:        cpucompositor.SurfaceProfileV1,
	KernelProfile:         cpucompositor.KernelProfileV1,
	SourceSemantics:       "For each exact evaluator ClipEvaluation, SourceProvider receives the frozen document hash and complete document-scoped IDs, indexes, exact times, geometry, and source/surface profiles, then returns one immutable, full-frame surface at exactly SourceTime. The surface must already match the target width and height and the declared linear-light BT.709 premultiplied RGBA16 surface profile. No resize, transform, crop, color conversion, frame selection, edge hold, or fallback is implicit.",
	ClipSemantics:         "Validate evaluator defaults are identity transform and full crop, then multiply the complete premultiplied surface by the exact evaluated opacity using the CPU kernel. Any render extension or non-identity transform/crop is unsupported and must have failed capability preflight.",
	TransitionSemantics:   "An active cutly.transition.dissolve.v1 layer requests outgoing then incoming exact handle surfaces, applies each clip opacity, and dissolves them with the evaluator's exact complementary weights. The transition replaces the ordinary clip on its track.",
	CompositionSemantics:  "Composite evaluated layers in cutly.evaluator.visual-layer-order.background-to-foreground.v1 order with the CPU kernel's Porter-Duff source-over rule. Provider completion order and host map iteration never define z-order.",
	GapSemantics:          "A timeline time with no active visual layers returns one complete transparent target surface.",
	CancellationSemantics: "Check context cancellation before evaluation, before and after every source request, between layer/kernel stages, and after hashing before publication. A provider-returned context cancellation or deadline error is classified as cancellation even when the parent context has not yet observed it. CPU kernels are atomic per stage and do not expose a partial surface.",
	IdentitySemantics:     "This profile supports only evaluator-default identity transform and full-source crop. Authored transform/crop, text/vector rasterization, masks, and rendering extensions are rejected by capability preflight rather than ignored.",
	HashBindingSemantics:  "PixelHash covers canonical pixel bytes only. Width, height, compositor profile, source profile, surface profile, kernel profile, document hash, timeline ID, and exact timeline/frame identity must be bound by the containing result/evidence manifest.",
	ExcludedStages:        "Media materialization, decode, VFR frame selection, rotation/SAR normalization, YUV/RGB and transfer conversion, chroma reconstruction, ICC/HDR handling, transform/resampling, crop, masks, text/vector rasterization, output color conversion, dithering, encoding, muxing, publication, and scheduling are outside this profile.",
}

// Descriptor returns the immutable v1 descriptor by value.
func Descriptor() ProfileDescriptor { return profileV1 }
