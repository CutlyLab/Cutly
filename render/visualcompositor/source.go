package visualcompositor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

type transitionSourceContext struct {
	id      documentv2.ID
	index   int
	profile string
	role    expressionlayer.TransitionHandleRole
}

func (program *Program) sourceSurface(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	providers Providers,
	transition *transitionSourceContext,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	binding, evaluation, asset, trackKind, err := program.expectedSourceEvaluation(clip, transition, path)
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	switch trackKind {
	case documentv2.TrackKindVideo:
		if asset.kind != documentv2.AssetKindVideo && asset.kind != documentv2.AssetKindImage {
			return nil, SourceReceipt{}, compositorError(CodeUnsupportedLayer, path+"/asset_kind", ErrUnsupportedLayer)
		}
		return program.nativeSource(ctx, clip, binding, evaluation, asset, providers.Native, path+"/native")
	case documentv2.TrackKindVector:
		if asset.kind != documentv2.AssetKindVector || !canonicalSHA256(asset.semanticHash) {
			return nil, SourceReceipt{}, compositorError(CodeUnsupportedLayer, path+"/asset_kind", ErrUnsupportedLayer)
		}
		if transition == nil {
			return program.expressionSource(ctx, clip, binding, evaluation, asset, providers.Expression, path+"/expression")
		}
		return program.expressionTransitionSource(ctx, clip, binding, evaluation, asset, *transition, providers.ExpressionHandle, path+"/expression_handle")
	case documentv2.TrackKindText:
		if transition != nil {
			return nil, SourceReceipt{}, compositorError(CodeUnsupportedLayer, path+"/text_transition", ErrUnsupportedLayer)
		}
		if asset.kind != documentv2.AssetKindText || asset.textProfile != documentv2.TextProfilePinnedFontViewportPXV2 {
			return nil, SourceReceipt{}, compositorError(CodeUnsupportedLayer, path+"/asset_kind", ErrUnsupportedLayer)
		}
		return program.textSource(ctx, clip, binding, evaluation, asset, providers.Text, path+"/text")
	default:
		return nil, SourceReceipt{}, compositorError(CodeUnsupportedLayer, path+"/track_kind", ErrUnsupportedLayer)
	}
}

func (program *Program) textSource(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	binding ClipBinding,
	evaluation SourceEvaluation,
	asset frozenAsset,
	provider textlayer.TextLayerProvider,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	if nilInterface(provider) {
		return nil, SourceReceipt{}, compositorError(CodeTextProviderUnavailable, path, ErrTextProviderUnavailable)
	}
	request := textlayer.Request{
		ContractVersion: textlayer.ContractVersion, DocumentHash: program.documentHash,
		TimelineID: binding.TimelineID, TimelineIndex: binding.TimelineIndex,
		TrackID: binding.TrackID, TrackIndex: binding.TrackIndex, TrackKind: binding.TrackKind,
		ClipID: binding.ClipID, ClipIndex: binding.ClipIndex,
		AssetID: binding.AssetID, AssetIndex: binding.AssetIndex, AssetKind: binding.AssetKind,
		FrozenTextBindingHashProfile: evaluator.FrozenTextSourceBindingHashProfileV1,
		FrozenTextBindingHash:        asset.textBindingHash,
		TextSemanticHashProfile:      documentv2.TextSemanticHashProfileV1, TextSemanticHash: asset.textSemanticHash,
		LayoutInputHashProfile: documentv2.TextLayoutInputHashProfileV1, LayoutInputHash: asset.textLayoutInputHash,
		FontAssetID: asset.textFontAssetID, FontAssetIndex: asset.textFontAssetIndex,
		FontContentHashProfile: sfntglyf.ContentHashProfileV1, FontContentHash: asset.textFontContentHash,
		RasterViewport: asset.viewport,
		TimelineTime:   clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		SourceProfile: textlayer.SourceProfileV1, SelectionProfile: textlayer.SelectionProfileV1,
		TextProfile:      documentv2.TextProfilePinnedFontViewportPXV2,
		EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
		RasterProfile:    documentv2.TextRasterProfileSupersample8x8V1,
		PaintProfile:     color.ProfileRGBA16SRGBPaintV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	frame, err := callTextProvider(ctx, provider, request, path)
	if err != nil {
		return nil, SourceReceipt{}, providerFailure(ctx, path, err)
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	receipt, err := program.validateTextFrame(ctx, asset, request, frame, path+"/response")
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	result := SourceReceipt{Kind: SourceKindText, Evaluation: evaluation, Text: &receipt}
	if err := result.Validate(); err != nil {
		return nil, SourceReceipt{}, compositorError(CodeInvariantFailed, path+"/receipt", errors.Join(ErrInvariantFailed, err))
	}
	return frame.Surface, result, nil
}

func callTextProvider(
	ctx context.Context,
	provider textlayer.TextLayerProvider,
	request textlayer.Request,
	path string,
) (frame textlayer.Frame, err error) {
	defer func() {
		if recover() != nil {
			frame = textlayer.Frame{}
			err = compositorError(CodeSourceFailed, path+"/panic", ErrSourceFailed)
		}
	}()
	return provider.Frame(ctx, request)
}

func (program *Program) expectedSourceEvaluation(
	clip evaluator.ClipEvaluation,
	transition *transitionSourceContext,
	path string,
) (ClipBinding, SourceEvaluation, frozenAsset, documentv2.TrackKind, error) {
	scope, exists := program.clips[clipScopeKey{timeline: clip.TimelineID, track: clip.TrackID, clip: clip.ClipID}]
	timelineIndex, timelineExists := program.timelines[clip.TimelineID]
	asset, assetExists := program.assets[clip.AssetID]
	if !exists || !timelineExists || !assetExists || timelineIndex != scope.timelineIndex ||
		clip.TrackIndex != scope.trackIndex || clip.ClipIndex != scope.clipIndex || scope.assetID != clip.AssetID {
		return ClipBinding{}, SourceEvaluation{}, frozenAsset{}, "", compositorError(CodeSourceInvalid, path+"/scope", ErrSourceInvalid)
	}
	binding := ClipBinding{
		TimelineID: clip.TimelineID, TimelineIndex: timelineIndex,
		TrackID: clip.TrackID, TrackIndex: clip.TrackIndex, TrackKind: scope.trackKind,
		ClipID: clip.ClipID, ClipIndex: clip.ClipIndex,
		AssetID: clip.AssetID, AssetIndex: asset.index, AssetKind: asset.kind,
		TimelineTime: clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		Visible: clip.Visible,
	}
	evaluation := SourceEvaluation{Clip: &binding, Transition: nil}
	if transition != nil {
		evaluation = SourceEvaluation{Clip: nil, Transition: &TransitionBinding{
			TransitionID: transition.id, TransitionIndex: transition.index,
			TransitionProfile: transition.profile, Role: transition.role, Clip: binding,
		}}
	}
	return binding, evaluation, asset, scope.trackKind, nil
}

func (program *Program) nativeSource(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	binding ClipBinding,
	evaluation SourceEvaluation,
	asset frozenAsset,
	provider rastercompositor.NativeSourceProvider,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	profiles, ok := program.nativeProfiles(asset.kind)
	if !ok || nilInterface(provider) {
		return nil, SourceReceipt{}, compositorError(CodeNativeProviderUnavailable, path, ErrNativeProviderUnavailable)
	}
	request := rastercompositor.NativeSourceRequest{
		ContractVersion: rastercompositor.NativeSourceContractVersion,
		DocumentHash:    program.documentHash,
		TimelineID:      binding.TimelineID, TimelineIndex: binding.TimelineIndex,
		TrackID: binding.TrackID, TrackIndex: binding.TrackIndex,
		ClipID: binding.ClipID, ClipIndex: binding.ClipIndex,
		AssetID: binding.AssetID, AssetIndex: binding.AssetIndex, AssetKind: binding.AssetKind,
		AssetContentHash: asset.contentHash,
		TimelineTime:     clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		SourceProfile: rastercompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		DecodeProfile: profiles.DecodeProfile, SelectionProfile: profiles.SelectionProfile,
		ColorProfile: profiles.ColorProfile, NormalizationProfile: profiles.NormalizationProfile,
	}
	if err := validateNativeRequest(request); err != nil {
		return nil, SourceReceipt{}, compositorError(CodeInvariantFailed, path+"/request", errors.Join(ErrInvariantFailed, err))
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	frame, err := provider.Frame(ctx, request)
	if err != nil {
		return nil, SourceReceipt{}, providerFailure(ctx, path, err)
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	receipt, err := program.validateNativeFrame(ctx, request, frame, path+"/response")
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	result := SourceReceipt{Kind: SourceKindNative, Evaluation: evaluation, Native: &receipt, Expression: nil}
	if err := result.Validate(); err != nil {
		return nil, SourceReceipt{}, compositorError(CodeInvariantFailed, path+"/receipt", errors.Join(ErrInvariantFailed, err))
	}
	return frame.Surface, result, nil
}

func (program *Program) expressionSource(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	binding ClipBinding,
	evaluation SourceEvaluation,
	asset frozenAsset,
	provider expressionlayer.ExpressionLayerProvider,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	if nilInterface(provider) {
		return nil, SourceReceipt{}, compositorError(CodeExpressionProviderUnavailable, path, ErrExpressionProviderUnavailable)
	}
	request := expressionlayer.Request{
		ContractVersion: expressionlayer.ContractVersion, DocumentHash: program.documentHash,
		TimelineID: binding.TimelineID, TimelineIndex: binding.TimelineIndex,
		TrackID: binding.TrackID, TrackIndex: binding.TrackIndex, TrackKind: binding.TrackKind,
		ClipID: binding.ClipID, ClipIndex: binding.ClipIndex,
		AssetID: binding.AssetID, AssetIndex: binding.AssetIndex, AssetKind: binding.AssetKind,
		AssetSemanticHashProfile: vectorraster.SourceHashProfileV1,
		AssetSemanticHash:        asset.semanticHash, RasterViewport: asset.viewport,
		TimelineTime: clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		SourceProfile: expressionlayer.SourceProfileV1, SelectionProfile: expressionlayer.SelectionProfileV1,
		ExpressionProfile: documentv2.VectorProfilePathViewportPXV2,
		EvaluatorProfile:  documentv2.EvaluatorVectorPathViewportPXV2,
		RasterProfile:     vectorraster.ProfileV1, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	frame, err := provider.Frame(ctx, request)
	if err != nil {
		return nil, SourceReceipt{}, providerFailure(ctx, path, err)
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	frameReceipt, err := program.validateExpressionFrame(ctx, asset, request, frame, path+"/response")
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	result := SourceReceipt{Kind: SourceKindExpression, Evaluation: evaluation, Native: nil, Expression: &frameReceipt}
	if err := result.Validate(); err != nil {
		return nil, SourceReceipt{}, compositorError(CodeInvariantFailed, path+"/receipt", errors.Join(ErrInvariantFailed, err))
	}
	return frame.Surface, result, nil
}

func (program *Program) expressionTransitionSource(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	binding ClipBinding,
	evaluation SourceEvaluation,
	asset frozenAsset,
	transition transitionSourceContext,
	provider expressionlayer.TransitionHandleProvider,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	if nilInterface(provider) {
		return nil, SourceReceipt{}, compositorError(CodeExpressionProviderUnavailable, path, ErrExpressionProviderUnavailable)
	}
	frozen, exists := program.transitions[transitionScopeKey{timeline: clip.TimelineID, transition: transition.id}]
	wantClip := frozen.outgoingClipID
	if transition.role == expressionlayer.TransitionHandleIncoming {
		wantClip = frozen.incomingClipID
	}
	if !exists || transition.index != frozen.index || transition.profile != frozen.profile ||
		binding.TrackID != frozen.trackID || binding.TrackIndex != frozen.trackIndex || binding.ClipID != wantClip {
		return nil, SourceReceipt{}, compositorError(CodeInvariantFailed, path+"/transition_scope", ErrInvariantFailed)
	}
	request := expressionlayer.TransitionHandleRequest{
		ContractVersion: expressionlayer.TransitionHandleContractVersion, DocumentHash: program.documentHash,
		TimelineID: binding.TimelineID, TimelineIndex: binding.TimelineIndex,
		TrackID: binding.TrackID, TrackIndex: binding.TrackIndex, TrackKind: binding.TrackKind,
		TransitionID: transition.id, TransitionIndex: transition.index,
		TransitionProfile: transition.profile, Role: transition.role,
		ClipID: binding.ClipID, ClipIndex: binding.ClipIndex,
		AssetID: binding.AssetID, AssetIndex: binding.AssetIndex, AssetKind: binding.AssetKind,
		AssetSemanticHashProfile: vectorraster.SourceHashProfileV1,
		AssetSemanticHash:        asset.semanticHash, RasterViewport: asset.viewport,
		TimelineTime: clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		SourceProfile:     expressionlayer.SourceProfileV1,
		SelectionProfile:  expressionlayer.TransitionHandleSelectionProfileV2,
		ExpressionProfile: documentv2.VectorProfilePathViewportPXV2,
		EvaluatorProfile:  documentv2.EvaluatorVectorPathViewportPXV2,
		RasterProfile:     vectorraster.ProfileV1, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	frame, err := provider.TransitionHandle(ctx, request)
	if err != nil {
		return nil, SourceReceipt{}, providerFailure(ctx, path, err)
	}
	if err := contextError(ctx, path); err != nil {
		return nil, SourceReceipt{}, err
	}
	frameReceipt, err := program.validateExpressionTransitionFrame(ctx, asset, request, frame, path+"/response")
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	result := SourceReceipt{Kind: SourceKindExpression, Evaluation: evaluation, Native: nil, Expression: &frameReceipt}
	if err := result.Validate(); err != nil {
		return nil, SourceReceipt{}, compositorError(CodeInvariantFailed, path+"/receipt", errors.Join(ErrInvariantFailed, err))
	}
	return frame.Surface, result, nil
}

func (program *Program) validateNativeFrame(
	ctx context.Context,
	request rastercompositor.NativeSourceRequest,
	frame rastercompositor.NativeSourceFrame,
	path string,
) (rastercompositor.SourceReceipt, error) {
	if frame.ContractVersion != rastercompositor.NativeSourceContractVersion || frame.Request != request {
		return rastercompositor.SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/request", ErrSourceMismatch)
	}
	if frame.Surface == nil || frame.Width <= 0 || frame.Height <= 0 || frame.Width != frame.Surface.Width() || frame.Height != frame.Surface.Height() {
		return rastercompositor.SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/surface", ErrSourceInvalid)
	}
	if frame.AssetContentHash != request.AssetContentHash || frame.SourceProfile != request.SourceProfile ||
		frame.SurfaceProfile != request.SurfaceProfile || frame.DecodeProfile != request.DecodeProfile ||
		frame.SelectionProfile != request.SelectionProfile || frame.ColorProfile != request.ColorProfile ||
		frame.NormalizationProfile != request.NormalizationProfile || !canonicalSHA256(frame.PixelHash) {
		return rastercompositor.SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/bindings", ErrSourceMismatch)
	}
	if err := program.validateSourceShape(frame.Width, frame.Height, path); err != nil {
		return rastercompositor.SourceReceipt{}, err
	}
	if err := contextError(ctx, path+"/hash"); err != nil {
		return rastercompositor.SourceReceipt{}, err
	}
	actualHash, err := canonicalSurfaceHash(ctx, frame.Surface, path+"/hash", CodeSourceInvalid, ErrSourceInvalid)
	if err != nil {
		return rastercompositor.SourceReceipt{}, err
	}
	if err := contextError(ctx, path+"/hash"); err != nil {
		return rastercompositor.SourceReceipt{}, err
	}
	if actualHash != frame.PixelHash {
		return rastercompositor.SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/pixel_hash", ErrSourceMismatch)
	}
	return rastercompositor.SourceReceipt{
		ContractVersion: frame.ContractVersion, Request: frame.Request,
		Width: frame.Width, Height: frame.Height, AssetContentHash: frame.AssetContentHash,
		PixelHash: frame.PixelHash, DecodeProfile: frame.DecodeProfile,
		SelectionProfile: frame.SelectionProfile, ColorProfile: frame.ColorProfile,
		NormalizationProfile: frame.NormalizationProfile, SourceProfile: frame.SourceProfile,
		SurfaceProfile: frame.SurfaceProfile,
	}, nil
}

func (program *Program) validateExpressionFrame(
	ctx context.Context,
	asset frozenAsset,
	request expressionlayer.Request,
	frame expressionlayer.Frame,
	path string,
) (ExpressionFrameReceipt, error) {
	if frame.ContractVersion != expressionlayer.ContractVersion || frame.Request != request {
		return ExpressionFrameReceipt{}, compositorError(CodeSourceMismatch, path+"/request", ErrSourceMismatch)
	}
	receipt, err := program.validateExpressionCommon(ctx, asset, frame.AssetSemanticHashProfile, frame.AssetSemanticHash,
		frame.RasterReceipt, frame.SurfaceIdentity, frame.Surface, path)
	if err != nil {
		return ExpressionFrameReceipt{}, err
	}
	requestCopy := request
	receipt.ContractVersion = frame.ContractVersion
	receipt.VisibleRequest = &requestCopy
	return receipt, nil
}

func (program *Program) validateExpressionTransitionFrame(
	ctx context.Context,
	asset frozenAsset,
	request expressionlayer.TransitionHandleRequest,
	frame expressionlayer.TransitionHandleFrame,
	path string,
) (ExpressionFrameReceipt, error) {
	if frame.ContractVersion != expressionlayer.TransitionHandleContractVersion || frame.Request != request {
		return ExpressionFrameReceipt{}, compositorError(CodeSourceMismatch, path+"/request", ErrSourceMismatch)
	}
	receipt, err := program.validateExpressionCommon(ctx, asset, frame.AssetSemanticHashProfile, frame.AssetSemanticHash,
		frame.RasterReceipt, frame.SurfaceIdentity, frame.Surface, path)
	if err != nil {
		return ExpressionFrameReceipt{}, err
	}
	requestCopy := request
	receipt.ContractVersion = frame.ContractVersion
	receipt.TransitionRequest = &requestCopy
	return receipt, nil
}

func (program *Program) validateExpressionCommon(
	ctx context.Context,
	asset frozenAsset,
	semanticProfile, semanticHash string,
	raster vectorraster.Receipt,
	identity expressionlayer.SurfaceIdentity,
	surface *cpucompositor.Surface,
	path string,
) (ExpressionFrameReceipt, error) {
	if surface == nil || semanticProfile != vectorraster.SourceHashProfileV1 || semanticHash != asset.semanticHash ||
		!canonicalSHA256(semanticHash) || identity.WidthPixels != asset.viewport.WidthPixels ||
		identity.HeightPixels != asset.viewport.HeightPixels || identity.PixelHashProfile != expressionlayer.SurfaceHashProfileV1 ||
		identity.SurfaceProfile != cpucompositor.SurfaceProfileV1 || !canonicalSHA256(identity.PixelHash) {
		return ExpressionFrameReceipt{}, compositorError(CodeSourceMismatch, path+"/identity", ErrSourceMismatch)
	}
	if surface.Width() != int(asset.viewport.WidthPixels) || surface.Height() != int(asset.viewport.HeightPixels) {
		return ExpressionFrameReceipt{}, compositorError(CodeSourceMismatch, path+"/geometry", ErrSourceMismatch)
	}
	if err := program.validateSourceShape(surface.Width(), surface.Height(), path); err != nil {
		return ExpressionFrameReceipt{}, err
	}
	retained, ok := checkedSurfaceBytes(asset.viewport.WidthPixels, asset.viewport.HeightPixels)
	samples, sampleOK := checkedMul(retained/cpucompositor.BytesPerPixel, 64)
	if sampleOK {
		samples, sampleOK = checkedMul(samples, asset.pathCount)
	}
	if !ok || !sampleOK || raster.SchemaVersion != vectorraster.ReceiptSchemaVersion ||
		raster.Profile != vectorraster.ReceiptProfileV1 || raster.RasterProfile != vectorraster.ProfileV1 ||
		raster.SourceProfile != documentv2.VectorProfilePathViewportPXV2 || raster.SourceHashProfile != vectorraster.SourceHashProfileV1 ||
		raster.SourceSemanticHash != asset.semanticHash || raster.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		raster.KernelProfile != cpucompositor.KernelProfileV1 || raster.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		raster.WidthPixels != asset.viewport.WidthPixels || raster.HeightPixels != asset.viewport.HeightPixels ||
		raster.PixelHash != identity.PixelHash || raster.PathCount != asset.pathCount ||
		raster.CommandCount != asset.commandCount || raster.ConceptualEdgeCount != asset.conceptualEdgeCount ||
		raster.EffectiveEdgeCount > raster.ConceptualEdgeCount || raster.SampleCount != samples ||
		raster.WorkUnits < raster.SampleCount || raster.WorkUnits > vectorraster.HardMaxWorkUnits ||
		raster.CanonicalSourceBytes != asset.canonicalSourceBytes || raster.RetainedBytes != retained ||
		raster.WorkingBytes < raster.RetainedBytes || raster.WorkingBytes > vectorraster.HardMaxWorkingBytes {
		return ExpressionFrameReceipt{}, compositorError(CodeSourceMismatch, path+"/raster_receipt", ErrSourceMismatch)
	}
	if err := contextError(ctx, path+"/hash"); err != nil {
		return ExpressionFrameReceipt{}, err
	}
	actualHash, err := canonicalSurfaceHash(ctx, surface, path+"/hash", CodeSourceInvalid, ErrSourceInvalid)
	if err != nil {
		return ExpressionFrameReceipt{}, err
	}
	if err := contextError(ctx, path+"/hash"); err != nil {
		return ExpressionFrameReceipt{}, err
	}
	if actualHash != identity.PixelHash {
		return ExpressionFrameReceipt{}, compositorError(CodeSourceMismatch, path+"/pixel_hash", ErrSourceMismatch)
	}
	return ExpressionFrameReceipt{
		AssetSemanticHashProfile: semanticProfile, AssetSemanticHash: semanticHash,
		RasterReceipt: raster, SurfaceIdentity: identity,
	}, nil
}

func (program *Program) validateTextFrame(
	ctx context.Context,
	asset frozenAsset,
	request textlayer.Request,
	frame textlayer.Frame,
	path string,
) (textlayer.FrameReceipt, error) {
	receipt := frame.Receipt
	if frame.Surface == nil {
		return textlayer.FrameReceipt{}, compositorError(CodeSourceMismatch, path+"/surface", ErrSourceMismatch)
	}
	if err := program.validateTextReceiptBindings(asset, request, receipt, path); err != nil {
		return textlayer.FrameReceipt{}, err
	}
	if frame.Surface.Width() != int(asset.viewport.WidthPixels) ||
		frame.Surface.Height() != int(asset.viewport.HeightPixels) {
		return textlayer.FrameReceipt{}, compositorError(CodeSourceMismatch, path+"/identity", ErrSourceMismatch)
	}
	if err := program.validateSourceShape(frame.Surface.Width(), frame.Surface.Height(), path); err != nil {
		return textlayer.FrameReceipt{}, err
	}
	actualHash, err := canonicalSurfaceHash(ctx, frame.Surface, path+"/hash", CodeSourceInvalid, ErrSourceInvalid)
	if err != nil {
		return textlayer.FrameReceipt{}, err
	}
	if actualHash != receipt.SurfaceIdentity.PixelHash {
		return textlayer.FrameReceipt{}, compositorError(CodeSourceMismatch, path+"/pixel_hash", ErrSourceMismatch)
	}
	return receipt, nil
}

func (program *Program) validateSourceShape(width, height int, path string) error {
	if width <= 0 || height <= 0 || width > program.config.Limits.Raster.MaxWidth || height > program.config.Limits.Raster.MaxHeight {
		return compositorError(CodeSourceLimit, path+"/dimensions", ErrSourceLimit)
	}
	w, h := uint64(width), uint64(height)
	if h != 0 && w > math.MaxUint64/h {
		return compositorError(CodeSourceLimit, path+"/pixel_count", ErrSourceLimit)
	}
	pixels := w * h
	if pixels > math.MaxUint64/cpucompositor.BytesPerPixel || pixels*cpucompositor.BytesPerPixel > program.config.Limits.MaxSourceBytes {
		return compositorError(CodeSourceLimit, path+"/bytes", ErrSourceLimit)
	}
	return nil
}

func (program *Program) nativeProfiles(kind documentv2.AssetKind) (rastercompositor.AssetSourceProfiles, bool) {
	switch kind {
	case documentv2.AssetKindVideo:
		return program.config.NativeProfiles.Video, program.config.Source.Video
	case documentv2.AssetKindImage:
		return program.config.NativeProfiles.Image, program.config.Source.Image
	default:
		return rastercompositor.AssetSourceProfiles{}, false
	}
}

func validateNativeRequest(value rastercompositor.NativeSourceRequest) error {
	if value.ContractVersion != rastercompositor.NativeSourceContractVersion || !canonicalSHA256(value.DocumentHash) ||
		value.TimelineID == "" || value.TrackID == "" || value.ClipID == "" || value.AssetID == "" ||
		value.TimelineIndex < 0 || value.TrackIndex < 0 || value.ClipIndex < 0 || value.AssetIndex < 0 ||
		(value.AssetKind != documentv2.AssetKindVideo && value.AssetKind != documentv2.AssetKindImage) ||
		!canonicalSHA256(value.AssetContentHash) || value.TimelineTime.Validate() != nil ||
		value.LocalTime.Validate() != nil || value.SourceTime.Validate() != nil ||
		value.SourceProfile != rastercompositor.SourceProfileV1 || value.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		!canonicalProfileToken(value.DecodeProfile) || !canonicalProfileToken(value.SelectionProfile) ||
		!canonicalProfileToken(value.ColorProfile) || !canonicalProfileToken(value.NormalizationProfile) {
		return ErrSourceInvalid
	}
	return nil
}

func providerFailure(ctx context.Context, path string, err error) error {
	if canceled := contextError(ctx, path); canceled != nil {
		return canceled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, expressionlayer.ErrCanceled) || errors.Is(err, textlayer.ErrCanceled) {
		return compositorError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return compositorError(CodeSourceFailed, path, errors.Join(ErrSourceFailed, err))
}

func canonicalSurfaceHash(
	ctx context.Context,
	surface *cpucompositor.Surface,
	path string,
	code ErrorCode,
	cause error,
) (string, error) {
	actual, err := surface.CanonicalHashContext(ctx)
	if err == nil {
		return actual, nil
	}
	if canceled := contextError(ctx, path); canceled != nil {
		return "", canceled
	}
	return "", compositorError(code, path, errors.Join(cause, err))
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func canonicalProfileToken(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for index, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) || character > unicode.MaxASCII {
			return false
		}
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' || character == ':') {
			return false
		}
		if index == 0 && !(character >= 'a' && character <= 'z') {
			return false
		}
	}
	version := strings.LastIndex(value, ".v")
	if version <= 0 || version+2 >= len(value) {
		return false
	}
	for _, character := range value[version+2:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func checkedSurfaceBytes(width, height uint32) (uint64, bool) {
	pixels, ok := checkedMul(uint64(width), uint64(height))
	if !ok {
		return 0, false
	}
	return checkedMul(pixels, cpucompositor.BytesPerPixel)
}

func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}

func sourceRequestCount(layers []evaluator.VisualLayer) (int, error) {
	count := 0
	for index, layer := range layers {
		switch layer.Kind {
		case evaluator.VisualLayerClip:
			if layer.Clip == nil || layer.Transition != nil {
				return 0, fmt.Errorf("layer %d malformed", index)
			}
			count++
		case evaluator.VisualLayerTransition:
			if layer.Transition == nil || layer.Clip != nil || layer.Transition.Outgoing == nil || layer.Transition.Incoming == nil {
				return 0, fmt.Errorf("layer %d malformed", index)
			}
			count += 2
		default:
			return 0, fmt.Errorf("layer %d kind %q", index, layer.Kind)
		}
	}
	return count, nil
}
