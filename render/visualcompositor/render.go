package visualcompositor

import (
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func (program *Program) RenderAt(
	ctx context.Context,
	timelineID documentv2.ID,
	at documentv2.RationalTime,
	providers Providers,
) (Result, error) {
	resolved, err := program.validateRuntime(ctx, providers)
	if err != nil {
		return Result{}, err
	}
	evaluated, err := program.evaluator.EvaluateTimeline(timelineID, at)
	if err != nil {
		return Result{}, compositorError(CodeEvaluationFailed, "timeline", errors.Join(ErrEvaluationFailed, err))
	}
	return program.renderEvaluation(ctx, evaluated, resolved)
}

func (program *Program) RenderFrame(
	ctx context.Context,
	timelineID documentv2.ID,
	clock videoedit.FrameClock,
	compositionFrameIndex int64,
	providers Providers,
) (FrameResult, error) {
	resolved, err := program.validateRuntime(ctx, providers)
	if err != nil {
		return FrameResult{}, err
	}
	evaluated, err := program.evaluator.EvaluateVisualFrame(timelineID, clock, compositionFrameIndex)
	if err != nil {
		return FrameResult{}, compositorError(CodeEvaluationFailed, "frame", errors.Join(ErrEvaluationFailed, err))
	}
	result, err := program.renderEvaluation(ctx, evaluated.Timeline, resolved)
	if err != nil {
		return FrameResult{}, err
	}
	return FrameResult{CompositionFrameIndex: compositionFrameIndex, Result: result}, nil
}

func (program *Program) validateRuntime(ctx context.Context, providers Providers) (Providers, error) {
	if program == nil || program.evaluator == nil {
		return Providers{}, compositorError(CodeProgramUnavailable, "program", ErrProgramUnavailable)
	}
	if err := contextError(ctx, "context"); err != nil {
		return Providers{}, err
	}
	if program.needsNative && nilInterface(providers.Native) {
		return Providers{}, compositorError(CodeNativeProviderUnavailable, "providers/native", ErrNativeProviderUnavailable)
	}
	if program.needsExpression && nilInterface(providers.Expression) {
		return Providers{}, compositorError(CodeExpressionProviderUnavailable, "providers/expression", ErrExpressionProviderUnavailable)
	}
	if program.needsExpressionHandle && nilInterface(providers.ExpressionHandle) {
		if combined, ok := providers.Expression.(expressionlayer.TransitionHandleProvider); ok && !nilInterface(combined) {
			providers.ExpressionHandle = combined
		} else {
			return Providers{}, compositorError(CodeExpressionProviderUnavailable, "providers/expression_handle", ErrExpressionProviderUnavailable)
		}
	}
	return providers, nil
}

func (program *Program) renderEvaluation(
	ctx context.Context,
	evaluated evaluator.TimelineEvaluation,
	providers Providers,
) (Result, error) {
	if evaluated.LayerOrder != LayerOrderProfile {
		return Result{}, compositorError(CodeUnsupportedLayer, "timeline/layer_order", ErrUnsupportedLayer)
	}
	if len(evaluated.Layers) > program.config.Limits.MaxLayers {
		return Result{}, compositorError(CodeResourceLimit, "timeline/layers", ErrResourceLimit)
	}
	requestCount, err := sourceRequestCount(evaluated.Layers)
	if err != nil {
		return Result{}, compositorError(CodeUnsupportedLayer, "timeline/layers", errors.Join(ErrUnsupportedLayer, err))
	}
	if requestCount > program.config.Limits.MaxSourceRequests {
		return Result{}, compositorError(CodeResourceLimit, "timeline/source_requests", ErrResourceLimit)
	}
	receipts := make([]SourceReceipt, 0, requestCount)
	var output *cpucompositor.Surface
	previousTrackIndex := -1
	for index, layer := range evaluated.Layers {
		path := fmt.Sprintf("layers/%d", index)
		if err := contextError(ctx, path); err != nil {
			return Result{}, err
		}
		if layer.TrackIndex <= previousTrackIndex {
			return Result{}, compositorError(CodeUnsupportedLayer, path+"/track_index", ErrUnsupportedLayer)
		}
		previousTrackIndex = layer.TrackIndex
		var surface *cpucompositor.Surface
		var layerReceipts []SourceReceipt
		switch layer.Kind {
		case evaluator.VisualLayerClip:
			if layer.Clip == nil || layer.Transition != nil || layer.Clip.TrackID != layer.TrackID || layer.Clip.TrackIndex != layer.TrackIndex {
				return Result{}, compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
			}
			var receipt SourceReceipt
			surface, receipt, err = program.renderClip(ctx, *layer.Clip, providers, nil, path+"/clip")
			if err == nil {
				layerReceipts = []SourceReceipt{receipt}
			}
		case evaluator.VisualLayerTransition:
			if layer.Transition == nil || layer.Clip != nil || layer.Transition.Outgoing == nil || layer.Transition.Incoming == nil ||
				layer.Transition.Outgoing.TrackID != layer.TrackID || layer.Transition.Incoming.TrackID != layer.TrackID ||
				layer.Transition.Outgoing.TrackIndex != layer.TrackIndex || layer.Transition.Incoming.TrackIndex != layer.TrackIndex {
				return Result{}, compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
			}
			surface, layerReceipts, err = program.renderTransition(ctx, *layer.Transition, providers, path+"/transition")
		default:
			return Result{}, compositorError(CodeUnsupportedLayer, path+"/kind", ErrUnsupportedLayer)
		}
		if err != nil {
			return Result{}, err
		}
		for receiptIndex := range layerReceipts {
			if err := layerReceipts[receiptIndex].Validate(); err != nil {
				return Result{}, compositorError(CodeInvariantFailed, path+"/receipt", errors.Join(ErrInvariantFailed, err))
			}
		}
		receipts = append(receipts, layerReceipts...)
		if output == nil {
			output = surface
			continue
		}
		if err := contextError(ctx, path+"/composite"); err != nil {
			return Result{}, err
		}
		output, err = cpucompositor.CompositeOver(output, surface)
		if err != nil {
			if canceled := contextError(ctx, path+"/composite"); canceled != nil {
				return Result{}, canceled
			}
			return Result{}, compositorError(CodeKernelFailed, path+"/composite", errors.Join(ErrKernelFailed, err))
		}
		if err := contextError(ctx, path+"/composite"); err != nil {
			return Result{}, err
		}
	}
	if output == nil {
		if err := contextError(ctx, "composite/gap"); err != nil {
			return Result{}, err
		}
		output, err = cpucompositor.NewSurfaceWithLimits(program.config.Width, program.config.Height, program.config.Limits.Target)
		if err != nil {
			if canceled := contextError(ctx, "composite/gap"); canceled != nil {
				return Result{}, canceled
			}
			return Result{}, compositorError(CodeKernelFailed, "composite/gap", errors.Join(ErrKernelFailed, err))
		}
		if err := contextError(ctx, "composite/gap"); err != nil {
			return Result{}, err
		}
	}
	if err := contextError(ctx, "hash"); err != nil {
		return Result{}, err
	}
	pixelHash, err := output.CanonicalHash()
	if err != nil {
		return Result{}, compositorError(CodeKernelFailed, "hash", errors.Join(ErrKernelFailed, err))
	}
	if err := contextError(ctx, "publish"); err != nil {
		return Result{}, err
	}
	result := Result{
		SchemaVersion: ResultSchemaVersion, Profile: ProfileV1,
		TimelineID: evaluated.TimelineID, TimelineTime: evaluated.TimelineTime,
		Width: program.config.Width, Height: program.config.Height,
		DocumentHash: program.documentHash, BuildFingerprint: program.config.BuildFingerprint,
		NativeSourceContractVersion:     rastercompositor.NativeSourceContractVersion,
		ExpressionContractVersion:       expressionlayer.ContractVersion,
		ExpressionHandleContractVersion: expressionlayer.TransitionHandleContractVersion,
		SurfaceProfile:                  cpucompositor.SurfaceProfileV1, PixelHashProfile: PixelHashProfileV1,
		RasterProfile: cpuraster.ProfileV1, KernelProfile: cpucompositor.KernelProfileV1,
		LayerOrder: LayerOrderProfile, Sources: receipts, PixelHash: pixelHash, Surface: output,
	}
	if err := program.ValidateResult(ctx, result); err != nil {
		return Result{}, compositorError(CodeInvariantFailed, "result", errors.Join(ErrInvariantFailed, err))
	}
	return result, nil
}

func (program *Program) renderClip(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	providers Providers,
	transition *transitionSourceContext,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	if err := validateVisualProperties(clip.Properties, path+"/properties"); err != nil {
		return nil, SourceReceipt{}, err
	}
	source, receipt, err := program.sourceSurface(ctx, clip, providers, transition, path+"/source")
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	if err := contextError(ctx, path+"/raster"); err != nil {
		return nil, SourceReceipt{}, err
	}
	rasterized, err := cpuraster.Rasterize(ctx, source, program.config.Width, program.config.Height,
		*clip.Properties.Transform, *clip.Properties.Crop, program.config.Limits.Raster)
	if err != nil {
		if canceled := contextError(ctx, path+"/raster"); canceled != nil {
			return nil, SourceReceipt{}, canceled
		}
		if errors.Is(err, cpuraster.ErrCanceled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, SourceReceipt{}, compositorError(CodeCanceled, path+"/raster", errors.Join(ErrCanceled, err))
		}
		if errors.Is(err, cpuraster.ErrResourceLimit) {
			return nil, SourceReceipt{}, compositorError(CodeResourceLimit, path+"/raster", errors.Join(ErrResourceLimit, err))
		}
		return nil, SourceReceipt{}, compositorError(CodeRasterFailed, path+"/raster", errors.Join(ErrRasterFailed, err))
	}
	if err := contextError(ctx, path+"/opacity"); err != nil {
		return nil, SourceReceipt{}, err
	}
	result, err := cpucompositor.ApplyOpacity(rasterized, *clip.Properties.Opacity)
	if err != nil {
		if canceled := contextError(ctx, path+"/opacity"); canceled != nil {
			return nil, SourceReceipt{}, canceled
		}
		return nil, SourceReceipt{}, compositorError(CodeKernelFailed, path+"/opacity", errors.Join(ErrKernelFailed, err))
	}
	if err := contextError(ctx, path+"/opacity"); err != nil {
		return nil, SourceReceipt{}, err
	}
	return result, receipt, nil
}

func (program *Program) renderTransition(
	ctx context.Context,
	transition evaluator.TransitionEvaluation,
	providers Providers,
	path string,
) (*cpucompositor.Surface, []SourceReceipt, error) {
	if transition.Profile != documentv2.TransitionDissolveV1 || transition.Phase != documentv2.TransitionPhaseActive ||
		transition.Outgoing == nil || transition.Incoming == nil || transition.Progress == nil {
		return nil, nil, compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
	}
	frozen, exists := program.transitions[transitionScopeKey{timeline: transition.TimelineID, transition: transition.TransitionID}]
	if !exists || frozen.profile != transition.Profile || frozen.outgoingClipID != transition.Outgoing.ClipID ||
		frozen.incomingClipID != transition.Incoming.ClipID {
		return nil, nil, compositorError(CodeInvariantFailed, path+"/scope", ErrInvariantFailed)
	}
	outgoingContext := &transitionSourceContext{
		id: transition.TransitionID, index: frozen.index, profile: transition.Profile,
		role: expressionlayer.TransitionHandleOutgoing,
	}
	incomingContext := &transitionSourceContext{
		id: transition.TransitionID, index: frozen.index, profile: transition.Profile,
		role: expressionlayer.TransitionHandleIncoming,
	}
	outgoing, outgoingReceipt, err := program.renderClip(ctx, *transition.Outgoing, providers, outgoingContext, path+"/outgoing")
	if err != nil {
		return nil, nil, err
	}
	if err := contextError(ctx, path+"/incoming"); err != nil {
		return nil, nil, err
	}
	incoming, incomingReceipt, err := program.renderClip(ctx, *transition.Incoming, providers, incomingContext, path+"/incoming")
	if err != nil {
		return nil, nil, err
	}
	if err := contextError(ctx, path+"/dissolve"); err != nil {
		return nil, nil, err
	}
	result, err := cpucompositor.Dissolve(outgoing, incoming, transition.OutgoingWeight, transition.IncomingWeight)
	if err != nil {
		if canceled := contextError(ctx, path+"/dissolve"); canceled != nil {
			return nil, nil, canceled
		}
		return nil, nil, compositorError(CodeKernelFailed, path+"/dissolve", errors.Join(ErrKernelFailed, err))
	}
	if err := contextError(ctx, path+"/dissolve"); err != nil {
		return nil, nil, err
	}
	return result, []SourceReceipt{outgoingReceipt, incomingReceipt}, nil
}

func validateVisualProperties(properties evaluator.Properties, path string) error {
	if properties.Transform == nil || properties.Crop == nil || properties.Opacity == nil ||
		properties.AudioGain != nil || properties.AudioPan != nil || properties.AudioChannelMatrix != nil ||
		properties.TextFill != nil || len(properties.RenderingExtensions) != 0 {
		return compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
	}
	return nil
}
