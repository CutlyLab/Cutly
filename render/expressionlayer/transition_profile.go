package expressionlayer

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

const (
	TransitionHandleProfileSchemaVersion = 1
	TransitionHandleProfileV2            = "cutly.provider.expression-layer.vector-transition-handle.v2"
	TransitionHandleSelectionProfileV2   = "cutly.selection.static-expression-transition-handle.v2"
)

type TransitionHandleProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	ContractVersion          int    `json:"contract_version"`
	BaseVisibleProvider      string `json:"base_visible_provider"`
	ScopeAmendmentProfile    string `json:"scope_amendment_profile"`
	SourceProfile            string `json:"source_profile"`
	SelectionProfile         string `json:"selection_profile"`
	ExpressionProfile        string `json:"expression_profile"`
	EvaluatorProfile         string `json:"evaluator_profile"`
	TransitionProfile        string `json:"transition_profile"`
	TransitionEvaluator      string `json:"transition_evaluator_profile"`
	RasterProfile            string `json:"raster_profile"`
	PaintProfile             string `json:"paint_profile"`
	KernelProfile            string `json:"kernel_profile"`
	SurfaceProfile           string `json:"surface_profile"`
	SurfaceHashProfile       string `json:"surface_hash_profile"`
	RequestSemantics         string `json:"request_semantics"`
	TimeSemantics            string `json:"time_semantics"`
	ResponseSemantics        string `json:"response_semantics"`
	ResourceSemantics        string `json:"resource_semantics"`
	OwnershipSemantics       string `json:"ownership_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var transitionHandleDescriptorV2 = TransitionHandleProfileDescriptor{
	SchemaVersion:            TransitionHandleProfileSchemaVersion,
	Profile:                  TransitionHandleProfileV2,
	ContractVersion:          TransitionHandleContractVersion,
	BaseVisibleProvider:      ProfileV1,
	ScopeAmendmentProfile:    documentv2.VectorTransitionScopeProfileV1,
	SourceProfile:            SourceProfileV1,
	SelectionProfile:         TransitionHandleSelectionProfileV2,
	ExpressionProfile:        documentv2.VectorProfilePathViewportPXV2,
	EvaluatorProfile:         documentv2.EvaluatorVectorPathViewportPXV2,
	TransitionProfile:        documentv2.TransitionDissolveV1,
	TransitionEvaluator:      documentv2.EvaluatorDissolveV1,
	RasterProfile:            vectorraster.ProfileV1,
	PaintProfile:             color.ProfileRGBA16SRGBPaintV1,
	KernelProfile:            cpucompositor.KernelProfileV1,
	SurfaceProfile:           cpucompositor.SurfaceProfileV1,
	SurfaceHashProfile:       SurfaceHashProfileV1,
	RequestSemantics:         "Require the exact frozen document hash and complete timeline/track/transition/clip/asset IDs and indexes, vector track/asset kinds, transition profile, outgoing or incoming role, semantic source hash, intrinsic viewport, actual evaluator timeline/local/source handle time and the complete expression/raster/paint/kernel/surface profile chain.",
	TimeSemantics:            "Accept only actual samples in the transition's half-open active [cut-pre_duration,cut+post_duration) window. The role must select the authored outgoing or incoming clip, and local/source times must equal exact evaluator mappings at that same TimelineTime even when the clip is outside its ordinary visible interval. Visible-time clamping and edge-frame substitution are forbidden.",
	ResponseSemantics:        "Echo every v2 request field exactly and return the same cached immutable intrinsic Surface, canonical semantic source hash, complete raster receipt and independently bindable surface identity used by the visible-only provider.",
	ResourceSemantics:        "Compile retains transition-handle scopes only for non-zero active windows, checks cancellation while indexing them and caps the two role scopes per transition at twice MaxClipScopes. A compact clip-key index and compact handle records reference the already bounded authoritative clip scopes; zero-width hard cuts retain no handle scope.",
	OwnershipSemantics:       "Compile freezes transition scopes together with clip scopes. Provider calls are concurrent immutable reads; returned Surfaces remain provider-owned and receipts contain no caller-owned mutable storage.",
	FailureSemantics:         "Unknown transition/role/scope, inactive time, wrong clip or asset, mismatched local/source time, profile/hash/viewport drift, cancellation or unavailable cached surface fails closed with a zero frame. The visible-only v1 Frame contract is never widened.",
	ExcludedResponsibilities: "Transition weights, crop/transform/opacity evaluation, dissolve/composition, text, legacy vector v1, temporal media decode, encoding, persistence and publication remain outside this provider add-on.",
}

func TransitionHandleDescriptor() TransitionHandleProfileDescriptor {
	return transitionHandleDescriptorV2
}
