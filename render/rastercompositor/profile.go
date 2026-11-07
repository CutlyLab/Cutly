package rastercompositor

import (
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.reference-compositor.native-affine-surface.v1"
	SourceProfileV1      = "cutly.source.normalized-native-surface-frame.v1"
	LayerOrderProfile    = evaluator.VisualLayerOrderBackgroundToForegroundV1
)

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	EvaluatorLayerOrder      string `json:"evaluator_layer_order"`
	SourceProfile            string `json:"source_profile"`
	SourceContractVersion    int    `json:"source_contract_version"`
	SurfaceProfile           string `json:"surface_profile"`
	RasterProfile            string `json:"raster_profile"`
	KernelProfile            string `json:"kernel_profile"`
	SourceSemantics          string `json:"source_semantics"`
	SourceValidation         string `json:"source_validation"`
	ClipSemantics            string `json:"clip_semantics"`
	TransitionSemantics      string `json:"transition_semantics"`
	CompositionSemantics     string `json:"composition_semantics"`
	GapSemantics             string `json:"gap_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	MemorySemantics          string `json:"memory_semantics"`
	DocumentLimitSemantics   string `json:"document_limit_semantics"`
	HashBindingSemantics     string `json:"hash_binding_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var profileV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	EvaluatorLayerOrder:      LayerOrderProfile,
	SourceProfile:            SourceProfileV1,
	SourceContractVersion:    NativeSourceContractVersion,
	SurfaceProfile:           cpucompositor.SurfaceProfileV1,
	RasterProfile:            cpuraster.ProfileV1,
	KernelProfile:            cpucompositor.KernelProfileV1,
	SourceSemantics:          "For each exact evaluator ClipEvaluation, request one normalized native-size surface at exactly SourceTime using the frozen document hash, complete document-scoped IDs/indexes, frozen asset kind/content hash, exact timeline/local/source times and configuration-allowlisted decode/selection/color/normalization profiles. Static images and temporal video have distinct exact selection profiles; no edge hold, fallback time, decode or color conversion is inferred here.",
	SourceValidation:         "The versioned response must echo the complete request, repeat the frozen canonical asset content hash, native geometry, canonical pixel hash and the exact allowlisted decode/selection/color/normalization/source/surface profiles. The compositor validates every binding, geometry and resource limit and independently recomputes the actual immutable Surface canonical hash before rasterization. This proves the returned pixels, not that they were honestly derived from the asset bytes; resource authorization, a trusted provider/runtime receipt and containing evidence remain required.",
	ClipSemantics:            "Apply evaluated crop and transform to the native normalized surface with the frozen CPU raster profile, then multiply the complete premultiplied target surface by evaluated opacity. No property is inferred from provider state.",
	TransitionSemantics:      "For an active cutly.transition.dissolve.v1 layer, independently request, validate, rasterize and apply opacity to outgoing and incoming handles, then dissolve the two target surfaces using the evaluator's exact complementary weights. The transition replaces the ordinary clip on its track.",
	CompositionSemantics:     "Composite completed target layers in cutly.evaluator.visual-layer-order.background-to-foreground.v1 order with CPU Porter-Duff source-over. Provider completion order and map iteration never define z-order.",
	GapSemantics:             "A timeline time with no active visual layer returns one complete transparent target surface.",
	CancellationSemantics:    "Check cancellation before evaluation, before and after each provider call, before/after source hashing, throughout CPU rasterization, between kernel stages and after final hashing. Provider-returned context cancellation/deadline errors are classified as cancellation even before the parent context observes them. Atomic kernel/hash stages do not publish partial output.",
	MemorySemantics:          "Layer surfaces are processed incrementally, so retained full target surfaces do not scale with track count. The configured target working budget reserves six target-frame byte equivalents for the transition/raster/opacity/composite peak; provider-owned native surfaces are separately capped per response and source receipts are bounded by MaxSourceRequests.",
	DocumentLimitSemantics:   "Before canonical freezing, bound every collection element/non-nil pointer in the complete typed Document, a conservative JSON-size upper bound, and tracks per timeline; after canonicalization, bound the exact byte count before decoding or evaluator compilation. These are per-Program host limits, not pixel semantics. Caller storage and fixed canonicalizer overhead remain outside the budget.",
	HashBindingSemantics:     "PixelHash commits only the final profile-defined pixel bytes; width, height and every other Result field remain separate unsigned mutable data. Result carries target geometry, document/timeline/time, source/raster/surface/kernel/layer-order profiles and ordered validated source receipts alongside that hash. A containing canonical evidence manifest must cryptographically bind those fields plus runtime identity, frozen plan, request/frame identity, publication and trusted worker receipt before this record is treated as attestation.",
	ExcludedResponsibilities: "Resource resolution, media decode, VFR/PTS selection rules, rotation/SAR normalization before the authored transform, YUV/RGB conversion, chroma reconstruction, ICC/HDR/tone mapping, masks, text/vector rasterization, output color conversion, dithering, encoding, muxing, publication and scheduling are outside this profile.",
}

// Descriptor returns the immutable profile descriptor by value.
func Descriptor() ProfileDescriptor { return profileV1 }
