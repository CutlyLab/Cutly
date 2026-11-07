package rastercompositor

import (
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
)

// Result carries the final pixel hash beside the semantic profiles and ordered source claims
// validated by this compositor. PixelHash commits only pixel bytes; Result is unsigned mutable
// render evidence material until a containing canonical evidence manifest binds every field.
type Result struct {
	TimelineID            documentv2.ID           `json:"timeline_id"`
	TimelineTime          documentv2.RationalTime `json:"timeline_time"`
	Width                 int                     `json:"width"`
	Height                int                     `json:"height"`
	DocumentHash          string                  `json:"document_hash"`
	BuildFingerprint      string                  `json:"build_fingerprint,omitempty"`
	CompositorProfile     string                  `json:"compositor_profile"`
	SourceContractVersion int                     `json:"source_contract_version"`
	SourceProfile         string                  `json:"source_profile"`
	SurfaceProfile        string                  `json:"surface_profile"`
	RasterProfile         string                  `json:"raster_profile"`
	KernelProfile         string                  `json:"kernel_profile"`
	LayerOrder            string                  `json:"layer_order"`
	Sources               []SourceReceipt         `json:"sources,omitempty"`
	PixelHash             string                  `json:"pixel_hash"`
	Surface               *cpucompositor.Surface  `json:"-"`
}

type FrameResult struct {
	CompositionFrameIndex int64  `json:"composition_frame_index"`
	Result                Result `json:"result"`
}

func (program *Program) RenderAt(
	ctx context.Context,
	timelineID documentv2.ID,
	at documentv2.RationalTime,
	provider NativeSourceProvider,
) (Result, error) {
	if err := program.validateRuntime(ctx, provider); err != nil {
		return Result{}, err
	}
	evaluated, err := program.evaluator.EvaluateTimeline(timelineID, at)
	if err != nil {
		return Result{}, compositorError(CodeEvaluationFailed, "timeline", errors.Join(ErrEvaluationFailed, err))
	}
	return program.renderEvaluation(ctx, evaluated, provider)
}

func (program *Program) RenderFrame(
	ctx context.Context,
	timelineID documentv2.ID,
	clock videoedit.FrameClock,
	compositionFrameIndex int64,
	provider NativeSourceProvider,
) (FrameResult, error) {
	if err := program.validateRuntime(ctx, provider); err != nil {
		return FrameResult{}, err
	}
	evaluated, err := program.evaluator.EvaluateVisualFrame(timelineID, clock, compositionFrameIndex)
	if err != nil {
		return FrameResult{}, compositorError(CodeEvaluationFailed, "frame", errors.Join(ErrEvaluationFailed, err))
	}
	result, err := program.renderEvaluation(ctx, evaluated.Timeline, provider)
	if err != nil {
		return FrameResult{}, err
	}
	return FrameResult{CompositionFrameIndex: compositionFrameIndex, Result: result}, nil
}

func (program *Program) validateRuntime(ctx context.Context, provider NativeSourceProvider) error {
	if program == nil || program.evaluator == nil {
		return compositorError(CodeProgramUnavailable, "program", ErrProgramUnavailable)
	}
	if ctx == nil {
		return compositorError(CodeInvalidConfig, "context", ErrInvalidConfig)
	}
	if err := contextError(ctx, "context"); err != nil {
		return err
	}
	if nilProvider(provider) {
		return compositorError(CodeProviderUnavailable, "provider", ErrProviderUnavailable)
	}
	return nil
}

func (program *Program) renderEvaluation(
	ctx context.Context,
	evaluated evaluator.TimelineEvaluation,
	provider NativeSourceProvider,
) (Result, error) {
	if evaluated.LayerOrder != LayerOrderProfile {
		return Result{}, compositorError(CodeUnsupportedLayer, "timeline.layer_order", ErrUnsupportedLayer)
	}
	if len(evaluated.Layers) > program.config.Limits.MaxLayers {
		return Result{}, compositorError(CodeResourceLimit, "timeline.layers", ErrResourceLimit)
	}
	requestCount, err := sourceRequestCount(evaluated.Layers)
	if err != nil {
		return Result{}, compositorError(CodeUnsupportedLayer, "timeline.layers", errors.Join(ErrUnsupportedLayer, err))
	}
	if requestCount > program.config.Limits.MaxSourceRequests {
		return Result{}, compositorError(CodeResourceLimit, "timeline.source_requests", ErrResourceLimit)
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
			surface, receipt, err = program.renderClip(ctx, *layer.Clip, provider, path+"/clip")
			if err == nil {
				layerReceipts = []SourceReceipt{receipt}
			}
		case evaluator.VisualLayerTransition:
			if layer.Transition == nil || layer.Clip != nil || layer.Transition.Outgoing == nil || layer.Transition.Incoming == nil ||
				layer.Transition.Outgoing.TrackID != layer.TrackID || layer.Transition.Incoming.TrackID != layer.TrackID ||
				layer.Transition.Outgoing.TrackIndex != layer.TrackIndex || layer.Transition.Incoming.TrackIndex != layer.TrackIndex {
				return Result{}, compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
			}
			surface, layerReceipts, err = program.renderTransition(ctx, *layer.Transition, provider, path+"/transition")
		default:
			return Result{}, compositorError(CodeUnsupportedLayer, path+"/kind", ErrUnsupportedLayer)
		}
		if err != nil {
			return Result{}, err
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
	if err := contextError(ctx, "composite"); err != nil {
		return Result{}, err
	}
	if output == nil {
		output, err = cpucompositor.NewSurfaceWithLimits(program.config.Width, program.config.Height, program.config.Limits.Target)
		if err != nil {
			return Result{}, compositorError(CodeKernelFailed, "composite", errors.Join(ErrKernelFailed, err))
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
	return Result{
		TimelineID: evaluated.TimelineID, TimelineTime: evaluated.TimelineTime,
		Width: program.config.Width, Height: program.config.Height,
		DocumentHash: program.evaluator.DocumentHash(), BuildFingerprint: program.config.BuildFingerprint,
		CompositorProfile: ProfileV1, SourceContractVersion: NativeSourceContractVersion,
		SourceProfile:  SourceProfileV1,
		SurfaceProfile: cpucompositor.SurfaceProfileV1, RasterProfile: cpuraster.ProfileV1,
		KernelProfile: cpucompositor.KernelProfileV1, LayerOrder: LayerOrderProfile,
		Sources: receipts, PixelHash: pixelHash, Surface: output,
	}, nil
}

func (program *Program) renderClip(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	provider NativeSourceProvider,
	path string,
) (*cpucompositor.Surface, SourceReceipt, error) {
	if err := validateVisualProperties(clip.Properties, path+"/properties"); err != nil {
		return nil, SourceReceipt{}, err
	}
	frame, receipt, err := program.sourceFrame(ctx, clip, provider, path+"/source")
	if err != nil {
		return nil, SourceReceipt{}, err
	}
	if err := contextError(ctx, path+"/raster"); err != nil {
		return nil, SourceReceipt{}, err
	}
	rasterized, err := cpuraster.Rasterize(
		ctx, frame.Surface, program.config.Width, program.config.Height,
		*clip.Properties.Transform, *clip.Properties.Crop, program.config.Limits.Raster,
	)
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
	provider NativeSourceProvider,
	path string,
) (*cpucompositor.Surface, []SourceReceipt, error) {
	if transition.Profile != documentv2.TransitionDissolveV1 || transition.Phase != documentv2.TransitionPhaseActive ||
		transition.Outgoing == nil || transition.Incoming == nil || transition.Progress == nil {
		return nil, nil, compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
	}
	outgoing, outgoingReceipt, err := program.renderClip(ctx, *transition.Outgoing, provider, path+"/outgoing")
	if err != nil {
		return nil, nil, err
	}
	if err := contextError(ctx, path+"/incoming"); err != nil {
		return nil, nil, err
	}
	incoming, incomingReceipt, err := program.renderClip(ctx, *transition.Incoming, provider, path+"/incoming")
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
