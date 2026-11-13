package rendersession

import (
	"context"
	"fmt"

	"github.com/CutlyLab/Cutly/render/streamencode"
)

// EncodeFrames renders the requested frames with bounded parallelism and streams their
// straight-alpha RGBA8 output, in strict request order, into one resident streaming encoder —
// the cutly.pipeline.stream-encode.v1 profile over a render session: no per-frame PNG files,
// no per-frame encoder processes, and the ordered observer keeps the encoder input
// deterministic regardless of the worker count. The encoder is aborted on any failure.
func (session *Session) EncodeFrames(ctx context.Context, requests []FrameRequest, options ParallelOptions, encoder *streamencode.Encoder) (streamencode.Result, error) {
	if session == nil || session.visual == nil || session.scratch == nil {
		return streamencode.Result{}, fmt.Errorf("%w: session is closed", ErrSessionInvalid)
	}
	if encoder == nil {
		return streamencode.Result{}, fmt.Errorf("%w: encoder is required", ErrSessionInvalid)
	}
	renderErr := session.renderFrames(ctx, requests, options, func(_ int, result FrameResult) error {
		frame, err := result.Output.Frame.Bytes()
		if err != nil {
			return fmt.Errorf("%w: output frame bytes: %v", ErrRenderFailed, err)
		}
		if len(frame) != encoder.FrameBytes() {
			return fmt.Errorf("%w: frame %d is %d bytes, encoder expects %d", ErrStageMismatch, result.TimelineTime.Value, len(frame), encoder.FrameBytes())
		}
		return encoder.WriteFrame(ctx, frame)
	})
	if renderErr != nil {
		encoder.Abort()
		return streamencode.Result{}, renderErr
	}
	result, err := encoder.Finish(ctx)
	if err != nil {
		return streamencode.Result{}, err
	}
	if result.FramesWritten != int64(len(requests)) {
		return streamencode.Result{}, fmt.Errorf("%w: encoder consumed %d of %d frames", ErrStageMismatch, result.FramesWritten, len(requests))
	}
	return result, nil
}
