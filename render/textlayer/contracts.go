package textlayer

import (
	"context"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
)

const ContractVersion = 1

// FontBinding is an explicit authorization edge. Content-hash equality never authorizes an
// otherwise-unlisted FontAssetID, and Provider is borrowed only until Compile returns.
type FontBinding struct {
	DocumentHash string                  `json:"document_hash"`
	FontAssetID  documentv2.ID           `json:"font_asset_id"`
	Provider     *sfntglyf.ShapeProvider `json:"-"`
}

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

	FrozenTextBindingHashProfile string                    `json:"frozen_text_binding_hash_profile"`
	FrozenTextBindingHash        string                    `json:"frozen_text_binding_hash"`
	TextSemanticHashProfile      string                    `json:"text_semantic_hash_profile"`
	TextSemanticHash             string                    `json:"text_semantic_hash"`
	LayoutInputHashProfile       string                    `json:"layout_input_hash_profile"`
	LayoutInputHash              string                    `json:"layout_input_hash"`
	FontAssetID                  documentv2.ID             `json:"font_asset_id"`
	FontAssetIndex               int                       `json:"font_asset_index"`
	FontContentHashProfile       string                    `json:"font_content_hash_profile"`
	FontContentHash              string                    `json:"font_content_hash"`
	RasterViewport               documentv2.RasterViewport `json:"raster_viewport"`

	TimelineTime documentv2.RationalTime `json:"timeline_time"`
	LocalTime    documentv2.RationalTime `json:"local_time"`
	SourceTime   documentv2.RationalTime `json:"source_time"`

	SourceProfile    string `json:"source_profile"`
	SelectionProfile string `json:"selection_profile"`
	TextProfile      string `json:"text_profile"`
	EvaluatorProfile string `json:"evaluator_profile"`
	RasterProfile    string `json:"raster_profile"`
	PaintProfile     string `json:"paint_profile"`
	SurfaceProfile   string `json:"surface_profile"`
}

type SurfaceIdentity struct {
	WidthPixels      uint32 `json:"width_pixels"`
	HeightPixels     uint32 `json:"height_pixels"`
	PixelHashProfile string `json:"pixel_hash_profile"`
	PixelHash        string `json:"pixel_hash"`
	SurfaceProfile   string `json:"surface_profile"`
}

type FrameReceipt struct {
	ContractVersion int                        `json:"contract_version"`
	Request         Request                    `json:"request"`
	Assembly        textassembly.Receipt       `json:"assembly"`
	Raster          textassembly.RasterReceipt `json:"raster"`
	SurfaceIdentity SurfaceIdentity            `json:"surface_identity"`
}

type Frame struct {
	Receipt FrameReceipt           `json:"receipt"`
	Surface *cpucompositor.Surface `json:"-"`
}

type TextLayerProvider interface {
	Frame(context.Context, Request) (Frame, error)
}

type ProviderFunc func(context.Context, Request) (Frame, error)

func (function ProviderFunc) Frame(ctx context.Context, request Request) (Frame, error) {
	if function == nil {
		return Frame{}, ErrProviderInvalid
	}
	return function(ctx, request)
}

type AssetReceipt struct {
	AssetID                      documentv2.ID              `json:"asset_id"`
	AssetIndex                   int                        `json:"asset_index"`
	AssetKind                    documentv2.AssetKind       `json:"asset_kind"`
	FrozenTextBindingHashProfile string                     `json:"frozen_text_binding_hash_profile"`
	FrozenTextBindingHash        string                     `json:"frozen_text_binding_hash"`
	TextSemanticHashProfile      string                     `json:"text_semantic_hash_profile"`
	TextSemanticHash             string                     `json:"text_semantic_hash"`
	LayoutInputHashProfile       string                     `json:"layout_input_hash_profile"`
	LayoutInputHash              string                     `json:"layout_input_hash"`
	FontAssetID                  documentv2.ID              `json:"font_asset_id"`
	FontAssetIndex               int                        `json:"font_asset_index"`
	FontContentHashProfile       string                     `json:"font_content_hash_profile"`
	FontContentHash              string                     `json:"font_content_hash"`
	RasterViewport               documentv2.RasterViewport  `json:"raster_viewport"`
	SurfaceBytes                 uint64                     `json:"surface_bytes"`
	Assembly                     textassembly.Receipt       `json:"assembly"`
	Raster                       textassembly.RasterReceipt `json:"raster"`
	SurfaceIdentity              SurfaceIdentity            `json:"surface_identity"`
}

type Manifest struct {
	SchemaVersion        int            `json:"schema_version"`
	Profile              string         `json:"profile"`
	ContractVersion      int            `json:"contract_version"`
	DocumentHash         string         `json:"document_hash"`
	SourceProfile        string         `json:"source_profile"`
	SelectionProfile     string         `json:"selection_profile"`
	TextProfile          string         `json:"text_profile"`
	EvaluatorProfile     string         `json:"evaluator_profile"`
	RasterProfile        string         `json:"raster_profile"`
	PaintProfile         string         `json:"paint_profile"`
	SurfaceProfile       string         `json:"surface_profile"`
	PixelHashProfile     string         `json:"pixel_hash_profile"`
	RetainedSurfaceBytes uint64         `json:"retained_surface_bytes"`
	Assets               []AssetReceipt `json:"assets,omitempty"`
}
