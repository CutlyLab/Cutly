package expressionlayer

import (
	"context"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

const TransitionHandleContractVersion = 2

type TransitionHandleRole string

const (
	TransitionHandleOutgoing TransitionHandleRole = "outgoing"
	TransitionHandleIncoming TransitionHandleRole = "incoming"
)

// TransitionHandleRequest is an additive authorization contract for an actual evaluator handle.
// It never relaxes the visible-only Frame(Request) v1 contract.
type TransitionHandleRequest struct {
	ContractVersion int    `json:"contract_version"`
	DocumentHash    string `json:"document_hash"`

	TimelineID        documentv2.ID        `json:"timeline_id"`
	TimelineIndex     int                  `json:"timeline_index"`
	TrackID           documentv2.ID        `json:"track_id"`
	TrackIndex        int                  `json:"track_index"`
	TrackKind         documentv2.TrackKind `json:"track_kind"`
	TransitionID      documentv2.ID        `json:"transition_id"`
	TransitionIndex   int                  `json:"transition_index"`
	TransitionProfile string               `json:"transition_profile"`
	Role              TransitionHandleRole `json:"role"`
	ClipID            documentv2.ID        `json:"clip_id"`
	ClipIndex         int                  `json:"clip_index"`
	AssetID           documentv2.ID        `json:"asset_id"`
	AssetIndex        int                  `json:"asset_index"`
	AssetKind         documentv2.AssetKind `json:"asset_kind"`

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

// TransitionHandleFrame is a provider-owned immutable intrinsic surface and complete v2 echo.
type TransitionHandleFrame struct {
	ContractVersion          int                     `json:"contract_version"`
	Request                  TransitionHandleRequest `json:"request"`
	AssetSemanticHashProfile string                  `json:"asset_semantic_hash_profile"`
	AssetSemanticHash        string                  `json:"asset_semantic_hash"`
	RasterReceipt            vectorraster.Receipt    `json:"raster_receipt"`
	SurfaceIdentity          SurfaceIdentity         `json:"surface_identity"`
	Surface                  *cpucompositor.Surface  `json:"-"`
}

type TransitionHandleProvider interface {
	TransitionHandle(context.Context, TransitionHandleRequest) (TransitionHandleFrame, error)
}

type TransitionHandleProviderFunc func(context.Context, TransitionHandleRequest) (TransitionHandleFrame, error)

func (function TransitionHandleProviderFunc) TransitionHandle(ctx context.Context, request TransitionHandleRequest) (TransitionHandleFrame, error) {
	if function == nil {
		return TransitionHandleFrame{}, ErrProviderInvalid
	}
	return function(ctx, request)
}

var _ TransitionHandleProvider = (*Provider)(nil)

func (provider *Provider) TransitionHandle(ctx context.Context, request TransitionHandleRequest) (TransitionHandleFrame, error) {
	if provider == nil {
		return TransitionHandleFrame{}, providerError(CodeProviderInvalid, "provider", ErrProviderInvalid)
	}
	if ctx == nil {
		return TransitionHandleFrame{}, providerError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "request"); err != nil {
		return TransitionHandleFrame{}, err
	}
	allowed, err := provider.validateTransitionHandleRequest(request)
	if err != nil {
		return TransitionHandleFrame{}, err
	}
	clip, exists := provider.clips[allowed.clipKey]
	if !exists {
		return TransitionHandleFrame{}, providerError(CodeInvariantFailed, "request/clip_scope", ErrInvariantFailed)
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists || entry.surface == nil || entry.receipt.AssetID != clip.asset.id {
		return TransitionHandleFrame{}, providerError(CodeAssetUnavailable, "request/asset_id", ErrAssetUnavailable)
	}
	if err := contextFailure(ctx, "response"); err != nil {
		return TransitionHandleFrame{}, err
	}
	return TransitionHandleFrame{
		ContractVersion: TransitionHandleContractVersion, Request: request,
		AssetSemanticHashProfile: entry.receipt.AssetSemanticHashProfile,
		AssetSemanticHash:        entry.receipt.AssetSemanticHash,
		RasterReceipt:            entry.receipt.RasterReceipt, SurfaceIdentity: entry.receipt.SurfaceIdentity,
		Surface: entry.surface,
	}, nil
}

func (provider *Provider) validateTransitionHandleRequest(request TransitionHandleRequest) (allowedTransitionHandle, error) {
	if request.ContractVersion != TransitionHandleContractVersion || request.DocumentHash != provider.documentHash {
		return allowedTransitionHandle{}, providerError(CodeRequestMismatch, "request/document", ErrRequestMismatch)
	}
	if request.TimelineIndex < 0 || request.TrackIndex < 0 || request.TransitionIndex < 0 || request.ClipIndex < 0 || request.AssetIndex < 0 ||
		request.TrackKind != documentv2.TrackKindVector || request.AssetKind != documentv2.AssetKindVector ||
		(request.Role != TransitionHandleOutgoing && request.Role != TransitionHandleIncoming) {
		return allowedTransitionHandle{}, providerError(CodeRequestInvalid, "request/scope", ErrRequestInvalid)
	}
	if request.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 || !canonicalSHA256(request.AssetSemanticHash) ||
		request.RasterViewport.WidthPixels == 0 || request.RasterViewport.HeightPixels == 0 {
		return allowedTransitionHandle{}, providerError(CodeRequestInvalid, "request/expression", ErrRequestInvalid)
	}
	if request.SourceProfile != SourceProfileV1 || request.SelectionProfile != TransitionHandleSelectionProfileV2 ||
		request.ExpressionProfile != documentv2.VectorProfilePathViewportPXV2 ||
		request.EvaluatorProfile != documentv2.EvaluatorVectorPathViewportPXV2 ||
		request.RasterProfile != vectorraster.ProfileV1 || request.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		request.KernelProfile != cpucompositor.KernelProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return allowedTransitionHandle{}, providerError(CodeRequestMismatch, "request/profiles", ErrRequestMismatch)
	}
	if request.TimelineTime.Validate() != nil || request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil {
		return allowedTransitionHandle{}, providerError(CodeRequestInvalid, "request/times", ErrRequestInvalid)
	}
	allowed, exists := provider.transitions[transitionScopeKey{
		timeline: request.TimelineID, transition: request.TransitionID, role: request.Role,
	}]
	if !exists {
		return allowedTransitionHandle{}, providerError(CodeRequestMismatch, "request/transition_scope", ErrRequestMismatch)
	}
	clip, exists := provider.clips[allowed.clipKey]
	if !exists {
		return allowedTransitionHandle{}, providerError(CodeInvariantFailed, "request/clip_scope", ErrInvariantFailed)
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists {
		return allowedTransitionHandle{}, providerError(CodeAssetUnavailable, "request/asset_id", ErrAssetUnavailable)
	}
	if request.TimelineIndex != allowed.timelineIndex || request.TrackID != allowed.trackID ||
		request.TrackIndex != allowed.trackIndex || request.TrackKind != clip.trackKind ||
		request.TransitionIndex != allowed.transitionIndex || request.TransitionProfile != allowed.transition.Profile ||
		request.ClipID != clip.clip.ID || request.ClipIndex != clip.clipIndex ||
		request.AssetID != clip.asset.id || request.AssetIndex != clip.asset.index || request.AssetKind != clip.asset.kind ||
		request.AssetSemanticHash != entry.receipt.AssetSemanticHash || request.RasterViewport != entry.receipt.RasterViewport {
		return allowedTransitionHandle{}, providerError(CodeRequestMismatch, "request/scope", ErrRequestMismatch)
	}
	cut, err := transitionCut(allowed, clip.clip)
	if err != nil {
		return allowedTransitionHandle{}, providerError(CodeInvariantFailed, "request/transition", errors.Join(ErrInvariantFailed, err))
	}
	sample, err := documentv2.SampleTransitionWindow(cut, allowed.transition, request.TimelineTime)
	if err != nil || sample.Phase != documentv2.TransitionPhaseActive {
		return allowedTransitionHandle{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	expectedLocal, err := evaluator.ClipLocalTime(clip.clip, request.TimelineTime)
	if err != nil {
		return allowedTransitionHandle{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	expectedSource, err := evaluator.SourceTime(clip.clip, request.TimelineTime)
	if err != nil {
		return allowedTransitionHandle{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	if !equivalentTime(request.LocalTime, expectedLocal) || !equivalentTime(request.SourceTime, expectedSource) {
		return allowedTransitionHandle{}, providerError(CodeRequestMismatch, "request/times", ErrRequestMismatch)
	}
	return allowed, nil
}

func transitionCut(value allowedTransitionHandle, clip documentv2.Clip) (documentv2.RationalTime, error) {
	if value.role == TransitionHandleOutgoing {
		return clip.TimelineRange.End()
	}
	return clip.TimelineRange.Start, nil
}
