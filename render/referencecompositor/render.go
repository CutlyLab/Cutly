package referencecompositor

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// SourceRequest is the complete semantic request to the normalized-surface boundary. DocumentHash
// scopes otherwise document-local IDs when one provider serves multiple immutable Programs. A
// provider must sample exactly SourceTime and return a full-frame SurfaceProfile surface; the
// compositor never substitutes another source time or performs implicit resizing/color conversion.
type SourceRequest struct {
	DocumentHash   string                  `json:"document_hash"`
	TimelineID     documentv2.ID           `json:"timeline_id"`
	TrackID        documentv2.ID           `json:"track_id"`
	ClipID         documentv2.ID           `json:"clip_id"`
	AssetID        documentv2.ID           `json:"asset_id"`
	TrackIndex     int                     `json:"track_index"`
	ClipIndex      int                     `json:"clip_index"`
	TimelineTime   documentv2.RationalTime `json:"timeline_time"`
	LocalTime      documentv2.RationalTime `json:"local_time"`
	SourceTime     documentv2.RationalTime `json:"source_time"`
	Width          int                     `json:"width"`
	Height         int                     `json:"height"`
	SourceProfile  string                  `json:"source_profile"`
	SurfaceProfile string                  `json:"surface_profile"`
}

type SourceProvider interface {
	// Surface may be called concurrently by independent Program renders. Implementations either
	// need concurrent-read safety or must serialize access internally without changing results.
	Surface(context.Context, SourceRequest) (*cpucompositor.Surface, error)
}

type SourceProviderFunc func(context.Context, SourceRequest) (*cpucompositor.Surface, error)

func (function SourceProviderFunc) Surface(ctx context.Context, request SourceRequest) (*cpucompositor.Surface, error) {
	if function == nil {
		return nil, ErrProviderUnavailable
	}
	return function(ctx, request)
}

// Result binds a canonical pixel-byte hash to the geometry and semantic profiles that
// cpucompositor.Surface.CanonicalHash intentionally excludes. A containing evidence manifest must
// additionally bind document/timeline/range/runtime identity before treating it as render proof.
type Result struct {
	TimelineID        documentv2.ID           `json:"timeline_id"`
	TimelineTime      documentv2.RationalTime `json:"timeline_time"`
	Width             int                     `json:"width"`
	Height            int                     `json:"height"`
	DocumentHash      string                  `json:"document_hash"`
	CompositorProfile string                  `json:"compositor_profile"`
	SourceProfile     string                  `json:"source_profile"`
	SurfaceProfile    string                  `json:"surface_profile"`
	KernelProfile     string                  `json:"kernel_profile"`
	LayerOrder        string                  `json:"layer_order"`
	PixelHash         string                  `json:"pixel_hash"`
	Surface           *cpucompositor.Surface  `json:"-"`
}

type FrameResult struct {
	CompositionFrameIndex int64  `json:"composition_frame_index"`
	Result                Result `json:"result"`
}

func (program *Program) RenderAt(
	ctx context.Context,
	timelineID documentv2.ID,
	at documentv2.RationalTime,
	provider SourceProvider,
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
	provider SourceProvider,
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

func (program *Program) validateRuntime(ctx context.Context, provider SourceProvider) error {
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

func nilProvider(provider SourceProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (program *Program) renderEvaluation(ctx context.Context, evaluated evaluator.TimelineEvaluation, provider SourceProvider) (Result, error) {
	if evaluated.LayerOrder != LayerOrderProfile {
		return Result{}, compositorError(CodeUnsupportedLayer, "timeline.layer_order", ErrUnsupportedLayer)
	}
	// Composite as layers arrive in evaluator order instead of retaining one full-frame surface
	// per track. At most the accumulator, current layer and one newly allocated kernel result are
	// referenced by this loop; total-track count therefore does not define retained frame memory.
	var output *cpucompositor.Surface
	previousTrackIndex := -1
	for index, layer := range evaluated.Layers {
		if err := contextError(ctx, fmt.Sprintf("layers/%d", index)); err != nil {
			return Result{}, err
		}
		if layer.TrackIndex <= previousTrackIndex {
			return Result{}, compositorError(CodeUnsupportedLayer, fmt.Sprintf("layers/%d/track_index", index), ErrUnsupportedLayer)
		}
		previousTrackIndex = layer.TrackIndex
		var surface *cpucompositor.Surface
		var err error
		switch layer.Kind {
		case evaluator.VisualLayerClip:
			if layer.Clip == nil || layer.Transition != nil || layer.Clip.TrackID != layer.TrackID || layer.Clip.TrackIndex != layer.TrackIndex {
				return Result{}, compositorError(CodeUnsupportedLayer, fmt.Sprintf("layers/%d", index), ErrUnsupportedLayer)
			}
			surface, err = program.renderClip(ctx, *layer.Clip, provider, fmt.Sprintf("layers/%d/clip", index))
		case evaluator.VisualLayerTransition:
			if layer.Transition == nil || layer.Clip != nil || layer.Transition.Outgoing == nil || layer.Transition.Incoming == nil ||
				layer.Transition.Outgoing.TrackID != layer.TrackID || layer.Transition.Incoming.TrackID != layer.TrackID ||
				layer.Transition.Outgoing.TrackIndex != layer.TrackIndex || layer.Transition.Incoming.TrackIndex != layer.TrackIndex {
				return Result{}, compositorError(CodeUnsupportedLayer, fmt.Sprintf("layers/%d", index), ErrUnsupportedLayer)
			}
			surface, err = program.renderTransition(ctx, *layer.Transition, provider, fmt.Sprintf("layers/%d/transition", index))
		default:
			return Result{}, compositorError(CodeUnsupportedLayer, fmt.Sprintf("layers/%d/kind", index), ErrUnsupportedLayer)
		}
		if err != nil {
			return Result{}, err
		}
		if output == nil {
			// renderClip/Transition always returned a compositor-owned allocation; assigning it as the
			// first accumulator cannot alias the provider's immutable source surface.
			output = surface
			continue
		}
		output, err = cpucompositor.CompositeOver(output, surface)
		if err != nil {
			return Result{}, compositorError(CodeKernelFailed, fmt.Sprintf("layers/%d/composite", index), errors.Join(ErrKernelFailed, err))
		}
	}
	if err := contextError(ctx, "composite"); err != nil {
		return Result{}, err
	}
	var err error
	if output == nil {
		output, err = cpucompositor.NewSurfaceWithLimits(program.config.Width, program.config.Height, program.config.Limits)
	}
	if err != nil {
		return Result{}, compositorError(CodeKernelFailed, "composite", errors.Join(ErrKernelFailed, err))
	}
	if err := contextError(ctx, "hash"); err != nil {
		return Result{}, err
	}
	pixelHash, err := output.CanonicalHash()
	if err != nil {
		return Result{}, compositorError(CodeKernelFailed, "hash", errors.Join(ErrKernelFailed, err))
	}
	if err := contextError(ctx, "hash"); err != nil {
		return Result{}, err
	}
	return Result{
		TimelineID: evaluated.TimelineID, TimelineTime: evaluated.TimelineTime,
		Width: program.config.Width, Height: program.config.Height,
		DocumentHash:      program.evaluator.DocumentHash(),
		CompositorProfile: ProfileV1, SourceProfile: SourceProfileV1,
		SurfaceProfile: cpucompositor.SurfaceProfileV1, KernelProfile: cpucompositor.KernelProfileV1,
		LayerOrder: LayerOrderProfile, PixelHash: pixelHash, Surface: output,
	}, nil
}

func (program *Program) renderTransition(
	ctx context.Context,
	transition evaluator.TransitionEvaluation,
	provider SourceProvider,
	path string,
) (*cpucompositor.Surface, error) {
	if transition.Profile != documentv2.TransitionDissolveV1 || transition.Phase != documentv2.TransitionPhaseActive ||
		transition.Outgoing == nil || transition.Incoming == nil || transition.Progress == nil {
		return nil, compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
	}
	outgoing, err := program.renderClip(ctx, *transition.Outgoing, provider, path+"/outgoing")
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, path+"/incoming"); err != nil {
		return nil, err
	}
	incoming, err := program.renderClip(ctx, *transition.Incoming, provider, path+"/incoming")
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, path+"/dissolve"); err != nil {
		return nil, err
	}
	result, err := cpucompositor.Dissolve(outgoing, incoming, transition.OutgoingWeight, transition.IncomingWeight)
	if err != nil {
		return nil, compositorError(CodeKernelFailed, path+"/dissolve", errors.Join(ErrKernelFailed, err))
	}
	return result, nil
}

func (program *Program) renderClip(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	provider SourceProvider,
	path string,
) (*cpucompositor.Surface, error) {
	if err := validateIdentityProperties(clip.Properties, path+"/properties"); err != nil {
		return nil, err
	}
	request := SourceRequest{
		DocumentHash: program.evaluator.DocumentHash(),
		TimelineID:   clip.TimelineID, TrackID: clip.TrackID, ClipID: clip.ClipID, AssetID: clip.AssetID,
		TrackIndex: clip.TrackIndex, ClipIndex: clip.ClipIndex,
		TimelineTime: clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		Width: program.config.Width, Height: program.config.Height,
		SourceProfile: SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
	if err := contextError(ctx, path+"/source"); err != nil {
		return nil, err
	}
	source, err := provider.Surface(ctx, request)
	if err != nil {
		if canceled := contextError(ctx, path+"/source"); canceled != nil {
			return nil, canceled
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, compositorError(CodeCanceled, path+"/source", errors.Join(ErrCanceled, err))
		}
		return nil, compositorError(CodeSourceFailed, path+"/source", errors.Join(ErrSourceFailed, err))
	}
	if err := contextError(ctx, path+"/source"); err != nil {
		return nil, err
	}
	if source == nil || source.Width() != program.config.Width || source.Height() != program.config.Height {
		return nil, compositorError(CodeSurfaceMismatch, path+"/source", ErrSurfaceMismatch)
	}
	if clip.Properties.Opacity == nil {
		return nil, compositorError(CodeUnsupportedLayer, path+"/properties/opacity", ErrUnsupportedLayer)
	}
	result, err := cpucompositor.ApplyOpacity(source, *clip.Properties.Opacity)
	if err != nil {
		return nil, compositorError(CodeKernelFailed, path+"/opacity", errors.Join(ErrKernelFailed, err))
	}
	return result, nil
}

func validateIdentityProperties(properties evaluator.Properties, path string) error {
	if properties.Transform == nil || properties.Crop == nil || properties.Opacity == nil ||
		properties.AudioGain != nil || properties.AudioPan != nil || properties.AudioChannelMatrix != nil ||
		properties.TextFill != nil || len(properties.RenderingExtensions) != 0 {
		return compositorError(CodeUnsupportedLayer, path, ErrUnsupportedLayer)
	}
	transform := properties.Transform
	if transform.Position.X.String() != "0/1" || transform.Position.Y.String() != "0/1" ||
		transform.Scale.X.String() != "1/1" || transform.Scale.Y.String() != "1/1" ||
		transform.RotationDegrees.String() != "0/1" ||
		transform.Anchor.X.String() != "1/2" || transform.Anchor.Y.String() != "1/2" {
		return compositorError(CodeUnsupportedLayer, path+"/transform", ErrUnsupportedLayer)
	}
	crop := properties.Crop
	if crop.X.String() != "0/1" || crop.Y.String() != "0/1" || crop.Width.String() != "1/1" || crop.Height.String() != "1/1" {
		return compositorError(CodeUnsupportedLayer, path+"/crop", ErrUnsupportedLayer)
	}
	return nil
}

func contextError(ctx context.Context, path string) error {
	if ctx == nil {
		return compositorError(CodeInvalidConfig, path, ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return compositorError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}
