package visualcompositor

import (
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

type expectedResultSource struct {
	evaluation SourceEvaluation
	asset      frozenAsset
	trackKind  documentv2.TrackKind
}

// ValidateResult checks live runtime self-consistency against the frozen Program and required
// Surface. It re-evaluates timeline scope, cross-binds nested receipts and independently recomputes
// the final pixel hash. It does not prove that source pixels caused that Surface; offline provenance
// requires trusted provider replay, an artifact chain or a trusted-execution signature.
func (program *Program) ValidateResult(ctx context.Context, result Result) (err error) {
	defer program.recoverDomainPanic(&err)
	return program.validateResult(ctx, result, true)
}

// ValidateSerializedReceipt validates deterministic structure, frozen document/evaluator scope and
// all provider receipt bindings, but cannot verify final pixels when Surface is absent. A
// canonical-looking PixelHash in a serialized receipt is a claim, not proof of rendered content.
func (program *Program) ValidateSerializedReceipt(ctx context.Context, result Result) (err error) {
	defer program.recoverDomainPanic(&err)
	return program.validateResult(ctx, result, false)
}

func (program *Program) validateResult(ctx context.Context, result Result, requireSurface bool) error {
	if program == nil || program.evaluator == nil {
		return compositorError(CodeProgramUnavailable, "result/program", ErrProgramUnavailable)
	}
	if err := contextError(ctx, "result"); err != nil {
		return err
	}
	wantTextContract := 0
	if program.config.Source.StaticTextV2 {
		wantTextContract = textlayer.ContractVersion
	}
	if result.SchemaVersion != ResultSchemaVersion || result.Profile != program.profile() ||
		result.Width != program.config.Width || result.Height != program.config.Height ||
		result.DomainScopeHash != program.domainScopeHash || result.DocumentHash != program.documentHash || result.BuildFingerprint != program.config.BuildFingerprint ||
		result.NativeSourceContractVersion != rastercompositor.NativeSourceContractVersion ||
		result.ExpressionContractVersion != expressionlayer.ContractVersion ||
		result.ExpressionHandleContractVersion != expressionlayer.TransitionHandleContractVersion ||
		result.TextContractVersion != wantTextContract ||
		result.SurfaceProfile != cpucompositor.SurfaceProfileV1 || result.PixelHashProfile != PixelHashProfileV1 ||
		result.RasterProfile != cpuraster.ProfileV1 || result.KernelProfile != cpucompositor.KernelProfileV1 ||
		result.LayerOrder != LayerOrderProfile || result.TimelineID == "" || result.TimelineTime.Validate() != nil ||
		!canonicalSHA256(result.PixelHash) || result.Sources == nil {
		return compositorError(CodeSourceMismatch, "result/bindings", ErrSourceMismatch)
	}
	if requireSurface && result.Surface == nil {
		return compositorError(CodeSourceMismatch, "result/surface", ErrSourceMismatch)
	}
	if _, exists := program.timelines[result.TimelineID]; !exists {
		return compositorError(CodeSourceMismatch, "result/timeline_id", ErrSourceMismatch)
	}
	evaluated, err := program.evaluator.EvaluateTimeline(result.TimelineID, result.TimelineTime)
	if err != nil || evaluated.LayerOrder != LayerOrderProfile {
		return compositorError(CodeEvaluationFailed, "result/evaluation", errors.Join(ErrEvaluationFailed, err))
	}
	expected, err := program.expectedResultSources(evaluated)
	if err != nil {
		return err
	}
	if len(result.Sources) != len(expected) || len(result.Sources) > program.config.Limits.MaxSourceRequests {
		return compositorError(CodeSourceMismatch, "result/sources", ErrSourceMismatch)
	}
	for index := range expected {
		if err := contextError(ctx, fmt.Sprintf("result/sources/%d", index)); err != nil {
			return err
		}
		if err := program.validateResultSource(result.Sources[index], expected[index], fmt.Sprintf("result/sources/%d", index)); err != nil {
			return err
		}
	}
	if result.Surface != nil {
		if result.Surface.Width() != result.Width || result.Surface.Height() != result.Height {
			return compositorError(CodeSourceMismatch, "result/surface/geometry", ErrSourceMismatch)
		}
		actual, err := canonicalSurfaceHash(ctx, result.Surface, "result/surface/pixel_hash", CodeSourceMismatch, ErrSourceMismatch)
		if err != nil {
			return err
		}
		if actual != result.PixelHash {
			return compositorError(CodeSourceMismatch, "result/surface/pixel_hash", errors.Join(ErrSourceMismatch, err))
		}
	}
	return contextError(ctx, "result/publish")
}

func (program *Program) expectedResultSources(evaluated evaluator.TimelineEvaluation) ([]expectedResultSource, error) {
	count, err := sourceRequestCount(evaluated.Layers)
	if err != nil || count > program.config.Limits.MaxSourceRequests {
		return nil, compositorError(CodeSourceMismatch, "result/evaluation/layers", errors.Join(ErrSourceMismatch, err))
	}
	result := make([]expectedResultSource, 0, count)
	appendClip := func(clip evaluator.ClipEvaluation, transition *transitionSourceContext, path string) error {
		_, evaluation, asset, trackKind, err := program.expectedSourceEvaluation(clip, transition, path)
		if err != nil {
			return err
		}
		result = append(result, expectedResultSource{evaluation: evaluation, asset: asset, trackKind: trackKind})
		return nil
	}
	for layerIndex, layer := range evaluated.Layers {
		path := fmt.Sprintf("result/evaluation/layers/%d", layerIndex)
		switch layer.Kind {
		case evaluator.VisualLayerClip:
			if layer.Clip == nil || layer.Transition != nil {
				return nil, compositorError(CodeSourceMismatch, path, ErrSourceMismatch)
			}
			if err := appendClip(*layer.Clip, nil, path+"/clip"); err != nil {
				return nil, err
			}
		case evaluator.VisualLayerTransition:
			transition := layer.Transition
			if transition == nil || layer.Clip != nil || transition.Outgoing == nil || transition.Incoming == nil ||
				transition.Profile != documentv2.TransitionDissolveV1 || transition.Phase != documentv2.TransitionPhaseActive {
				return nil, compositorError(CodeSourceMismatch, path, ErrSourceMismatch)
			}
			frozen, exists := program.transitions[transitionScopeKey{timeline: transition.TimelineID, transition: transition.TransitionID}]
			if !exists || frozen.profile != transition.Profile || frozen.trackID != layer.TrackID || frozen.trackIndex != layer.TrackIndex ||
				frozen.outgoingClipID != transition.Outgoing.ClipID || frozen.incomingClipID != transition.Incoming.ClipID {
				return nil, compositorError(CodeSourceMismatch, path+"/transition_scope", ErrSourceMismatch)
			}
			if frozen.trackKind == documentv2.TrackKindText {
				return nil, compositorError(CodeSourceMismatch, path+"/text_transition", ErrSourceMismatch)
			}
			outgoing := &transitionSourceContext{id: transition.TransitionID, index: frozen.index, profile: transition.Profile, role: expressionlayer.TransitionHandleOutgoing}
			incoming := &transitionSourceContext{id: transition.TransitionID, index: frozen.index, profile: transition.Profile, role: expressionlayer.TransitionHandleIncoming}
			if err := appendClip(*transition.Outgoing, outgoing, path+"/outgoing"); err != nil {
				return nil, err
			}
			if err := appendClip(*transition.Incoming, incoming, path+"/incoming"); err != nil {
				return nil, err
			}
		default:
			return nil, compositorError(CodeSourceMismatch, path+"/kind", ErrSourceMismatch)
		}
	}
	return result, nil
}

func (program *Program) validateResultSource(receipt SourceReceipt, expected expectedResultSource, path string) error {
	if err := receipt.Validate(); err != nil || !sourceEvaluationEqual(receipt.Evaluation, expected.evaluation) {
		return compositorError(CodeSourceMismatch, path+"/evaluation", errors.Join(ErrSourceMismatch, err))
	}
	switch receipt.Kind {
	case SourceKindNative:
		if expected.trackKind != documentv2.TrackKindVideo || receipt.Native == nil {
			return compositorError(CodeSourceMismatch, path+"/route", ErrSourceMismatch)
		}
		return program.validateResultNative(*receipt.Native, expected.asset, path+"/native")
	case SourceKindExpression:
		if expected.trackKind != documentv2.TrackKindVector || receipt.Expression == nil {
			return compositorError(CodeSourceMismatch, path+"/route", ErrSourceMismatch)
		}
		return program.validateResultExpression(*receipt.Expression, expected.asset, receipt.Evaluation.Transition != nil, path+"/expression")
	case SourceKindText:
		if expected.trackKind != documentv2.TrackKindText || receipt.Text == nil || receipt.Evaluation.Transition != nil {
			return compositorError(CodeSourceMismatch, path+"/route", ErrSourceMismatch)
		}
		return program.validateResultText(*receipt.Text, expected.asset, path+"/text")
	default:
		return compositorError(CodeSourceMismatch, path+"/kind", ErrSourceMismatch)
	}
}

func (program *Program) validateResultText(receipt textlayer.FrameReceipt, asset frozenAsset, path string) error {
	return program.validateTextReceiptBindings(asset, receipt.Request, receipt, path)
}

func (program *Program) validateResultNative(receipt rastercompositor.SourceReceipt, asset frozenAsset, path string) error {
	profiles, ok := program.nativeProfiles(asset.kind)
	if !ok || receipt.ContractVersion != rastercompositor.NativeSourceContractVersion ||
		receipt.Request.ContractVersion != rastercompositor.NativeSourceContractVersion || receipt.Request.DocumentHash != program.documentHash ||
		receipt.AssetContentHash != asset.contentHash || receipt.Request.AssetContentHash != asset.contentHash ||
		receipt.SourceProfile != rastercompositor.SourceProfileV1 || receipt.Request.SourceProfile != rastercompositor.SourceProfileV1 ||
		receipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 || receipt.Request.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		receipt.DecodeProfile != profiles.DecodeProfile || receipt.Request.DecodeProfile != profiles.DecodeProfile ||
		receipt.SelectionProfile != profiles.SelectionProfile || receipt.Request.SelectionProfile != profiles.SelectionProfile ||
		receipt.ColorProfile != profiles.ColorProfile || receipt.Request.ColorProfile != profiles.ColorProfile ||
		receipt.NormalizationProfile != profiles.NormalizationProfile || receipt.Request.NormalizationProfile != profiles.NormalizationProfile ||
		receipt.Width <= 0 || receipt.Height <= 0 || !canonicalSHA256(receipt.PixelHash) {
		return compositorError(CodeSourceMismatch, path, ErrSourceMismatch)
	}
	return program.validateSourceShape(receipt.Width, receipt.Height, path)
}

func (program *Program) validateResultExpression(receipt ExpressionFrameReceipt, asset frozenAsset, transition bool, path string) error {
	if transition {
		request := receipt.TransitionRequest
		if receipt.VisibleRequest != nil || request == nil || receipt.ContractVersion != expressionlayer.TransitionHandleContractVersion ||
			request.ContractVersion != expressionlayer.TransitionHandleContractVersion || request.DocumentHash != program.documentHash ||
			request.SelectionProfile != expressionlayer.TransitionHandleSelectionProfileV2 {
			return compositorError(CodeSourceMismatch, path+"/transition_request", ErrSourceMismatch)
		}
		if err := validateExpressionProfiles(request.SourceProfile, request.ExpressionProfile, request.EvaluatorProfile, request.RasterProfile, request.PaintProfile, request.KernelProfile, request.SurfaceProfile); err != nil {
			return compositorError(CodeSourceMismatch, path+"/profiles", err)
		}
		if request.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 || request.AssetSemanticHash != asset.semanticHash || request.RasterViewport != asset.viewport {
			return compositorError(CodeSourceMismatch, path+"/source", ErrSourceMismatch)
		}
	} else {
		request := receipt.VisibleRequest
		if receipt.TransitionRequest != nil || request == nil || receipt.ContractVersion != expressionlayer.ContractVersion ||
			request.ContractVersion != expressionlayer.ContractVersion || request.DocumentHash != program.documentHash ||
			request.SelectionProfile != expressionlayer.SelectionProfileV1 {
			return compositorError(CodeSourceMismatch, path+"/visible_request", ErrSourceMismatch)
		}
		if err := validateExpressionProfiles(request.SourceProfile, request.ExpressionProfile, request.EvaluatorProfile, request.RasterProfile, request.PaintProfile, request.KernelProfile, request.SurfaceProfile); err != nil {
			return compositorError(CodeSourceMismatch, path+"/profiles", err)
		}
		if request.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 || request.AssetSemanticHash != asset.semanticHash || request.RasterViewport != asset.viewport {
			return compositorError(CodeSourceMismatch, path+"/source", ErrSourceMismatch)
		}
	}
	retained, retainedOK := checkedSurfaceBytes(asset.viewport.WidthPixels, asset.viewport.HeightPixels)
	samples, sampleOK := checkedMul(retained/cpucompositor.BytesPerPixel, 64)
	if sampleOK {
		samples, sampleOK = checkedMul(samples, asset.pathCount)
	}
	raster := receipt.RasterReceipt
	identity := receipt.SurfaceIdentity
	if !retainedOK || !sampleOK || receipt.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 ||
		receipt.AssetSemanticHash != asset.semanticHash || identity.WidthPixels != asset.viewport.WidthPixels ||
		identity.HeightPixels != asset.viewport.HeightPixels || identity.PixelHashProfile != expressionlayer.SurfaceHashProfileV1 ||
		identity.SurfaceProfile != cpucompositor.SurfaceProfileV1 || !canonicalSHA256(identity.PixelHash) ||
		raster.SchemaVersion != vectorraster.ReceiptSchemaVersion || raster.Profile != vectorraster.ReceiptProfileV1 ||
		raster.RasterProfile != vectorraster.ProfileV1 || raster.SourceProfile != documentv2.VectorProfilePathViewportPXV2 ||
		raster.SourceHashProfile != vectorraster.SourceHashProfileV1 || raster.SourceSemanticHash != asset.semanticHash ||
		raster.PaintProfile != color.ProfileRGBA16SRGBPaintV1 || raster.KernelProfile != cpucompositor.KernelProfileV1 ||
		raster.SurfaceProfile != cpucompositor.SurfaceProfileV1 || raster.WidthPixels != asset.viewport.WidthPixels ||
		raster.HeightPixels != asset.viewport.HeightPixels || raster.PixelHash != identity.PixelHash || raster.PathCount != asset.pathCount ||
		raster.CommandCount != asset.commandCount || raster.ConceptualEdgeCount != asset.conceptualEdgeCount ||
		raster.EffectiveEdgeCount > raster.ConceptualEdgeCount || raster.SampleCount != samples || raster.WorkUnits < raster.SampleCount ||
		raster.WorkUnits > vectorraster.HardMaxWorkUnits || raster.CanonicalSourceBytes != asset.canonicalSourceBytes ||
		raster.RetainedBytes != retained || raster.WorkingBytes < raster.RetainedBytes || raster.WorkingBytes > vectorraster.HardMaxWorkingBytes {
		return compositorError(CodeSourceMismatch, path+"/receipt", ErrSourceMismatch)
	}
	return nil
}

func validateExpressionProfiles(source, expression, evaluatorProfile, raster, paint, kernel, surface string) error {
	if source != expressionlayer.SourceProfileV1 || expression != documentv2.VectorProfilePathViewportPXV2 ||
		evaluatorProfile != documentv2.EvaluatorVectorPathViewportPXV2 || raster != vectorraster.ProfileV1 ||
		paint != color.ProfileRGBA16SRGBPaintV1 || kernel != cpucompositor.KernelProfileV1 || surface != cpucompositor.SurfaceProfileV1 {
		return ErrSourceMismatch
	}
	return nil
}

func sourceEvaluationEqual(left, right SourceEvaluation) bool {
	if (left.Clip == nil) != (right.Clip == nil) || (left.Transition == nil) != (right.Transition == nil) {
		return false
	}
	if left.Clip != nil && *left.Clip != *right.Clip {
		return false
	}
	return left.Transition == nil || *left.Transition == *right.Transition
}
