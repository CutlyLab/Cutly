package reconstructv2

import (
	"context"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

func (bundle *Bundle) ready(ctx context.Context) error {
	if ctx == nil {
		return ErrContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !bundle.Valid() {
		return ErrResource
	}
	return nil
}

// RenderSurfaceFrame uses only the persisted composition clock and admitted tick
// range. The result is canonical linear RGBA16, before output color/PNG conversion.
func (bundle *Bundle) RenderSurfaceFrame(ctx context.Context, index int64) (result referencevisual.SurfaceFrameResult, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = referencevisual.SurfaceFrameResult{}
		}
	}()
	if err = bundle.ready(ctx); err != nil {
		return result, err
	}
	if !bundle.visualEnabled {
		return result, ErrUnsupported
	}
	inside, e := bundle.frames.Contains(index)
	if e != nil || !inside {
		return result, ErrUnsupported
	}
	result, err = bundle.visual.RenderSurfaceFrame(ctx, bundle.timeline, bundle.frameClock, index)
	if err != nil {
		return referencevisual.SurfaceFrameResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return referencevisual.SurfaceFrameResult{}, err
	}
	return result, nil
}

// RenderFrame adds the native output color and PNG stages to the same scoped frame.
func (bundle *Bundle) RenderFrame(ctx context.Context, index int64) (result referencevisual.FrameResult, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = referencevisual.FrameResult{}
		}
	}()
	if err = bundle.ready(ctx); err != nil {
		return result, err
	}
	if !bundle.visualEnabled {
		return result, ErrUnsupported
	}
	inside, e := bundle.frames.Contains(index)
	if e != nil || !inside {
		return result, ErrUnsupported
	}
	result, err = bundle.visual.RenderFrame(ctx, bundle.timeline, bundle.frameClock, index)
	if err != nil {
		return referencevisual.FrameResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return referencevisual.FrameResult{}, err
	}
	return result, nil
}

// RenderPCM returns owned stereo PCM16LE using the persisted sample clock. Bounds
// are checked before exact graph evaluation or PCM allocation. Zero blocks obey
// the audio domain's strict half-open start rule, not an end-of-range sentinel.
func (bundle *Bundle) RenderPCM(ctx context.Context, first, count int64) (result pcm16le.Result, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = pcm16le.Result{}
		}
	}()
	if err = bundle.ready(ctx); err != nil {
		return result, err
	}
	if !bundle.audioEnabled {
		return result, ErrUnsupported
	}
	if count < 0 || count > bundle.maxBlockSamples || uint64(count) > bundle.pcmLimits.MaxFrames || uint64(count) > bundle.pcmLimits.MaxOutputBytes/pcm16le.BytesPerFrame {
		return result, ErrLimit
	}
	requested := videoedit.TickRange{First: first, Count: count}
	end, e := requested.EndExclusive()
	if e != nil {
		return result, ErrUnsupported
	}
	inside, e := bundle.samples.Contains(first)
	if e != nil || !inside {
		return result, ErrUnsupported
	}
	limit, e := bundle.samples.EndExclusive()
	if e != nil || end > limit {
		return result, ErrUnsupported
	}
	block, e := bundle.audio.EvaluateBlock(ctx, bundle.timeline, bundle.sampleClock, first, count, bundle.wav)
	if e != nil {
		return result, e
	}
	if block.DocumentHash != bundle.key.DocumentHash || block.TimelineID != bundle.timeline || block.SampleClock != bundle.sampleClock || block.CompositionSampleRange != requested || int64(len(block.Samples)) != count {
		return result, ErrResource
	}
	result, err = pcm16le.Quantize(ctx, bundle.sampleClock.SamplesPerSecond, first, block.Samples, bundle.pcmLimits)
	if err != nil {
		return pcm16le.Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return pcm16le.Result{}, err
	}
	return result, nil
}
