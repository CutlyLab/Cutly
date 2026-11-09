package reconstructv2

import (
	"context"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

// lease holds an RW lifecycle lease until a render's final publication check.
// sync.RWMutex blocks new readers once Close is waiting for its exclusive lease.
func (b *MixedBundle) lease(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, ErrContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrResource
	}
	b.mu.RLock()
	if !b.validLocked() {
		b.mu.RUnlock()
		return nil, ErrResource
	}
	return b.mu.RUnlock, nil
}
func (b *MixedBundle) RenderSurfaceFrame(ctx context.Context, index int64) (result referencevisual.SurfaceFrameResult, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = referencevisual.SurfaceFrameResult{}
		}
	}()
	release, err := b.lease(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if !b.visualEnabled {
		return result, ErrUnsupported
	}
	inside, e := b.frames.Contains(index)
	if e != nil || !inside {
		return result, ErrUnsupported
	}
	result, err = b.visual.RenderSurfaceFrame(ctx, b.timeline, b.frameClock, index)
	if err != nil {
		return referencevisual.SurfaceFrameResult{}, err
	}
	return result, ctx.Err()
}
func (b *MixedBundle) RenderFrame(ctx context.Context, index int64) (result referencevisual.FrameResultV3, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = referencevisual.FrameResultV3{}
		}
	}()
	release, err := b.lease(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if !b.visualEnabled {
		return result, ErrUnsupported
	}
	inside, e := b.frames.Contains(index)
	if e != nil || !inside {
		return result, ErrUnsupported
	}
	result, err = b.visual.RenderFrame(ctx, b.timeline, b.frameClock, index)
	if err != nil {
		return referencevisual.FrameResultV3{}, err
	}
	return result, ctx.Err()
}
func (b *MixedBundle) RenderPCM(ctx context.Context, first, count int64) (result pcm16le.Result, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = pcm16le.Result{}
		}
	}()
	release, err := b.lease(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if !b.audioEnabled {
		return result, ErrUnsupported
	}
	if count < 0 || count > b.maxBlockSamples || uint64(count) > b.pcmLimits.MaxFrames || uint64(count) > b.pcmLimits.MaxOutputBytes/pcm16le.BytesPerFrame {
		return result, ErrLimit
	}
	requested := videoedit.TickRange{First: first, Count: count}
	end, e := requested.EndExclusive()
	if e != nil {
		return result, ErrUnsupported
	}
	inside, e := b.samples.Contains(first)
	if e != nil || !inside {
		return result, ErrUnsupported
	}
	limit, e := b.samples.EndExclusive()
	if e != nil || end > limit {
		return result, ErrUnsupported
	}
	block, e := b.audio.EvaluateBlock(ctx, b.timeline, b.sampleClock, first, count, b.wav)
	if e != nil {
		return result, e
	}
	if block.DocumentHash != b.key.DocumentHash || block.TimelineID != b.timeline || block.SampleClock != b.sampleClock || block.CompositionSampleRange != requested || int64(len(block.Samples)) != count {
		return result, ErrResource
	}
	result, err = pcm16le.Quantize(ctx, b.sampleClock.SamplesPerSecond, first, block.Samples, b.pcmLimits)
	if err != nil {
		return pcm16le.Result{}, err
	}
	return result, ctx.Err()
}
