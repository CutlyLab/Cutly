package expressionlayer

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.provider.expression-layer.vector-viewport-px.v1"
	ManifestSchemaVersion = 1

	SourceProfileV1      = "cutly.source.expression-layer-intrinsic-surface.v1"
	SelectionProfileV1   = "cutly.selection.static-expression-visible-time.v1"
	SurfaceHashProfileV1 = "cutly.hash.sha256.surface-rgba16be-pixels.v1"
)

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	ContractVersion          int    `json:"contract_version"`
	DocumentSemantics        string `json:"document_semantics"`
	ScopeSemantics           string `json:"scope_semantics"`
	SemanticHashProfile      string `json:"semantic_hash_profile"`
	SemanticHashSemantics    string `json:"semantic_hash_semantics"`
	SourceProfile            string `json:"source_profile"`
	SelectionProfile         string `json:"selection_profile"`
	ExpressionProfile        string `json:"expression_profile"`
	EvaluatorProfile         string `json:"evaluator_profile"`
	RasterProfile            string `json:"raster_profile"`
	PaintProfile             string `json:"paint_profile"`
	KernelProfile            string `json:"kernel_profile"`
	SurfaceProfile           string `json:"surface_profile"`
	SurfaceHashProfile       string `json:"surface_hash_profile"`
	RequestSemantics         string `json:"request_semantics"`
	ResponseSemantics        string `json:"response_semantics"`
	StaticTimeSemantics      string `json:"static_time_semantics"`
	ManifestSemantics        string `json:"manifest_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	OwnershipSemantics       string `json:"ownership_semantics"`
	EvidenceSemantics        string `json:"evidence_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
	DefaultLimits            Limits `json:"default_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	ContractVersion:          ContractVersion,
	DocumentSemantics:        "Borrow one complete immutable Document v2 object graph only for Compile, conservatively bound it, canonical-validate it, then deep-freeze and hash that exact representation. Inline vector expressions require no external resource binding and no URI is opened.",
	ScopeSemantics:           "Index only enabled TrackKindVector clips referencing AssetKindVector. Every enabled vector expression must use cutly.vector.path-viewport-px.v2; unsupported expression profiles fail Compile rather than disappearing. Disabled clips and non-vector tracks create no request scope.",
	SemanticHashProfile:      vectorraster.SourceHashProfileV1,
	SemanticHashSemantics:    "Use the exact canonical VectorSource hash returned by the raster stage. It commits viewBox, viewport, ordered paths, commands, fill rules and RGBA16 paints, but not asset ID/index, metadata or clip properties. DocumentHash plus the complete request scope independently prevents cross-document or cross-asset replay.",
	SourceProfile:            SourceProfileV1,
	SelectionProfile:         SelectionProfileV1,
	ExpressionProfile:        documentv2.VectorProfilePathViewportPXV2,
	EvaluatorProfile:         documentv2.EvaluatorVectorPathViewportPXV2,
	RasterProfile:            vectorraster.ProfileV1,
	PaintProfile:             color.ProfileRGBA16SRGBPaintV1,
	KernelProfile:            cpucompositor.KernelProfileV1,
	SurfaceProfile:           cpucompositor.SurfaceProfileV1,
	SurfaceHashProfile:       SurfaceHashProfileV1,
	RequestSemantics:         "Require the exact frozen document hash; timeline/track/clip/asset IDs and array indexes; track/asset kinds; semantic hash; intrinsic viewport; timeline/local/source times; and every expression/raster/paint/kernel/surface profile. No partial ID lookup, profile fallback or hash-only replay is accepted.",
	ResponseSemantics:        "Echo every Request field exactly and return the cached provider-owned immutable intrinsic Surface, its canonical pixel identity and the complete raster receipt. Callers must validate the full echo and all profile/geometry fields, not PixelHash alone.",
	StaticTimeSemantics:      "TimelineTime must be inside the frozen clip half-open visible interval, and LocalTime/SourceTime must equal the exact evaluator mappings. Time is scope evidence only: a static inline expression returns the same cached pixels at every valid time. Transition handles outside the clip interval are unsupported.",
	ManifestSemantics:        "Return an AssetID-sorted defensive manifest binding every distinct enabled expression asset to its document index, semantic hash, viewport, profile chain, path/command/edge counts, retained bytes, raster receipt and surface identity.",
	AllocationSemantics:      "Before canonicalization bound the complete typed document graph and a conservative encoded size. Before rasterization bound clip scopes, distinct expression assets, total paths/commands/edges and the overflow-checked sum of all intrinsic width*height*8 retained bytes. Each vector raster also obeys explicit per-asset limits. No partial Provider is published.",
	CancellationSemantics:    "Check a non-nil context before and throughout document preflight, freezing, scope indexing and every sequential raster; immediately before Provider publication; and before request lookup and response publication. A canceled operation returns no Provider or Frame.",
	OwnershipSemantics:       "Compile borrows the caller Document only until return and requires no concurrent mutation. Successful compilation retains only the canonical deep-frozen document-derived scope data, immutable Surfaces and value-only receipts. Provider, Frame and Manifest support concurrent reads; Manifest returns a defensive slice copy.",
	EvidenceSemantics:        "Manifest and Frame receipts prove deterministic in-process derivation and actual canonical pixels, not worker identity, signature, resource authorization, publication or third-party attestation. Containing evidence must bind the complete serializable records and trusted runtime identity.",
	FailureSemantics:         "Invalid context/config/document/profile/scope/time/hash/profile echo, raster failure, resource overflow, cancellation or stage invariant fails closed with nil Provider or a zero Frame. There is no fallback profile, partial asset set or lower-quality raster path.",
	ExcludedResponsibilities: "External media resolution, video/image decode, text shaping, vector v1 strokes/curves, clip crop/transform/opacity, transitions, target composition, output transfer conversion, encoding, persistence, scheduling and publication are outside this provider. rastercompositor v1 is unchanged.",
	DefaultLimits:            DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
