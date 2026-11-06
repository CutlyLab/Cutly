package expressionlayer

import (
	"context"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

const ContractVersion = 1

// Request is the complete document-scoped selection and semantic-profile request. Every field is
// validated against the frozen Program; an expression hash alone never authorizes cross-document,
// cross-asset or cross-clip replay.
type Request struct {
	ContractVersion int    `json:"contract_version"`
	DocumentHash    string `json:"document_hash"`

	TimelineID    documentv2.ID        `json:"timeline_id"`
	TimelineIndex int                  `json:"timeline_index"`
	TrackID       documentv2.ID        `json:"track_id"`
	TrackIndex    int                  `json:"track_index"`
	TrackKind     documentv2.TrackKind `json:"track_kind"`
	ClipID        documentv2.ID        `json:"clip_id"`
	ClipIndex     int                  `json:"clip_index"`
	AssetID       documentv2.ID        `json:"asset_id"`
	AssetIndex    int                  `json:"asset_index"`
	AssetKind     documentv2.AssetKind `json:"asset_kind"`

	AssetSemanticHashProfile string                    `json:"asset_semantic_hash_profile"`
	AssetSemanticHash        string                    `json:"asset_semantic_hash"`
	RasterViewport           documentv2.RasterViewport `json:"raster_viewport"`

	TimelineTime documentv2.RationalTime `json:"timeline_time"`
	LocalTime    documentv2.RationalTime `json:"local_time"`
	SourceTime   documentv2.RationalTime `json:"source_time"`

	SourceProfile     string `json:"source_profile"`
	SelectionProfile  string `json:"selection_profile"`
	ExpressionProfile string `json:"expression_profile"`
	EvaluatorProfile  string `json:"evaluator_profile"`
	RasterProfile     string `json:"raster_profile"`
	PaintProfile      string `json:"paint_profile"`
	KernelProfile     string `json:"kernel_profile"`
	SurfaceProfile    string `json:"surface_profile"`
}

// SurfaceIdentity commits the immutable intrinsic surface. PixelHash covers only canonical pixel
// bytes; geometry and profiles remain separate fields that containing evidence must also bind.
type SurfaceIdentity struct {
	WidthPixels      uint32 `json:"width_pixels"`
	HeightPixels     uint32 `json:"height_pixels"`
	PixelHashProfile string `json:"pixel_hash_profile"`
	PixelHash        string `json:"pixel_hash"`
	SurfaceProfile   string `json:"surface_profile"`
}

// Frame is a provider-owned immutable Surface plus a complete request echo and derivation receipt.
// Surface is safe for concurrent reads and is intentionally excluded from serialization.
type Frame struct {
	ContractVersion          int                    `json:"contract_version"`
	Request                  Request                `json:"request"`
	AssetSemanticHashProfile string                 `json:"asset_semantic_hash_profile"`
	AssetSemanticHash        string                 `json:"asset_semantic_hash"`
	RasterReceipt            vectorraster.Receipt   `json:"raster_receipt"`
	SurfaceIdentity          SurfaceIdentity        `json:"surface_identity"`
	Surface                  *cpucompositor.Surface `json:"-"`
}

type ExpressionLayerProvider interface {
	Frame(context.Context, Request) (Frame, error)
}

type ProviderFunc func(context.Context, Request) (Frame, error)

func (function ProviderFunc) Frame(ctx context.Context, request Request) (Frame, error) {
	if function == nil {
		return Frame{}, ErrProviderInvalid
	}
	return function(ctx, request)
}

// AssetReceipt is the immutable, serializable derivation record for one distinct inline vector
// asset. It intentionally repeats the decisive profile and size fields from RasterReceipt so a
// manifest consumer can preflight without interpreting nested implementation state.
type AssetReceipt struct {
	AssetID                  documentv2.ID             `json:"asset_id"`
	AssetIndex               int                       `json:"asset_index"`
	AssetKind                documentv2.AssetKind      `json:"asset_kind"`
	AssetSemanticHashProfile string                    `json:"asset_semantic_hash_profile"`
	AssetSemanticHash        string                    `json:"asset_semantic_hash"`
	RasterViewport           documentv2.RasterViewport `json:"raster_viewport"`
	ExpressionProfile        string                    `json:"expression_profile"`
	EvaluatorProfile         string                    `json:"evaluator_profile"`
	SourceProfile            string                    `json:"source_profile"`
	SelectionProfile         string                    `json:"selection_profile"`
	RasterProfile            string                    `json:"raster_profile"`
	PaintProfile             string                    `json:"paint_profile"`
	KernelProfile            string                    `json:"kernel_profile"`
	SurfaceProfile           string                    `json:"surface_profile"`
	PathCount                uint64                    `json:"path_count"`
	CommandCount             uint64                    `json:"command_count"`
	ConceptualEdgeCount      uint64                    `json:"conceptual_edge_count"`
	EffectiveEdgeCount       uint64                    `json:"effective_edge_count"`
	SurfaceBytes             uint64                    `json:"surface_bytes"`
	SurfaceIdentity          SurfaceIdentity           `json:"surface_identity"`
	RasterReceipt            vectorraster.Receipt      `json:"raster_receipt"`
}

type Manifest struct {
	SchemaVersion            int            `json:"schema_version"`
	Profile                  string         `json:"profile"`
	ContractVersion          int            `json:"contract_version"`
	DocumentHash             string         `json:"document_hash"`
	SourceProfile            string         `json:"source_profile"`
	SelectionProfile         string         `json:"selection_profile"`
	AssetSemanticHashProfile string         `json:"asset_semantic_hash_profile"`
	ExpressionProfile        string         `json:"expression_profile"`
	EvaluatorProfile         string         `json:"evaluator_profile"`
	RasterProfile            string         `json:"raster_profile"`
	PaintProfile             string         `json:"paint_profile"`
	KernelProfile            string         `json:"kernel_profile"`
	SurfaceProfile           string         `json:"surface_profile"`
	PixelHashProfile         string         `json:"pixel_hash_profile"`
	Assets                   []AssetReceipt `json:"assets,omitempty"`
}
