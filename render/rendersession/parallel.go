package rendersession

import (
	"context"
	"fmt"
	"sync"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

// MaxParallelWorkers is the hard ceiling on concurrent frame renders inside one session. The
// host picks the worker count within it; a render session never spawns unbounded parallelism.
const MaxParallelWorkers = 32

// ParallelOptions bounds one parallel frame pass. Workers 1 renders serially through the same
// code path; values above MaxParallelWorkers are rejected.
type ParallelOptions struct {
	Workers int
}

// FrameRequest selects one frame of a parallel pass.
type FrameRequest struct {
	TimelineID documentv2.ID
	At         documentv2.RationalTime
}

// RenderFrames renders the requested frames with bounded parallelism and returns the results
// in request order. Every result is byte-identical to a serial RenderFrame of the same
// request; deep validation applies deterministically by request index. Any frame failure
// cancels the pass and no partial slice is returned. Peak retained memory is the full result
// slice; streaming consumers should use RenderFramesFunc.
func (session *Session) RenderFrames(ctx context.Context, requests []FrameRequest, options ParallelOptions) ([]FrameResult, error) {
	results := make([]FrameResult, len(requests))
	err := session.renderFrames(ctx, requests, options, func(index int, result FrameResult) error {
		results[index] = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// RenderFramesFunc renders the requested frames with bounded parallelism and invokes the
// observer in strict request order as frames complete. The observer may publish durably and
// drop the result, keeping peak memory bounded by the worker and reorder window rather than
// the full sequence. Returning an error from the observer cancels the pass.
func (session *Session) RenderFramesFunc(ctx context.Context, requests []FrameRequest, options ParallelOptions, observer func(index int, result FrameResult) error) error {
	return session.renderFrames(ctx, requests, options, observer)
}

func (session *Session) renderFrames(ctx context.Context, requests []FrameRequest, options ParallelOptions, observer func(index int, result FrameResult) error) error {
	if session == nil || session.visual == nil || session.scratch == nil {
		return fmt.Errorf("%w: session is closed", ErrSessionInvalid)
	}
	if observer == nil {
		return fmt.Errorf("%w: observer is required", ErrSessionInvalid)
	}
	if options.Workers <= 0 {
		options.Workers = 1
	}
	if options.Workers > MaxParallelWorkers {
		return fmt.Errorf("%w: workers %d exceed the bound %d", ErrSessionInvalid, options.Workers, MaxParallelWorkers)
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrSessionInvalid)
	}
	if len(requests) == 0 {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Bounded dispatch: at most Workers requests are in flight (rendering or waiting in the
	// reorder buffer), which is the backpressure boundary — the dispatcher blocks until the
	// ordered consumer advances.
	dispatch := make(chan int, options.Workers)
	results := make([]chan FrameResult, len(requests))
	for index := range results {
		results[index] = make(chan FrameResult, 1)
	}

	var failureMu sync.Mutex
	var failure error
	fail := func(err error) {
		failureMu.Lock()
		if failure == nil {
			failure = err
			cancel()
		}
		failureMu.Unlock()
	}
	firstFailure := func() error {
		failureMu.Lock()
		defer failureMu.Unlock()
		return failure
	}

	var pipeline sync.WaitGroup
	pipeline.Add(1 + options.Workers)
	go func() {
		defer pipeline.Done()
		defer close(dispatch)
		for index := range requests {
			select {
			case dispatch <- index:
			case <-runCtx.Done():
				return
			}
		}
	}()
	for worker := 0; worker < options.Workers; worker++ {
		go func() {
			defer pipeline.Done()
			for index := range dispatch {
				result, err := session.renderOne(runCtx, index, requests[index])
				if err != nil {
					fail(err)
					continue
				}
				// Buffered size one: the write never blocks, so a failing ordered consumer
				// cannot strand a worker.
				results[index] <- result
			}
		}()
	}

	ordered := func() error {
		for index := range requests {
			var result FrameResult
			select {
			case result = <-results[index]:
			case <-runCtx.Done():
				if err := firstFailure(); err != nil {
					return err
				}
				return runCtx.Err()
			}
			if err := observer(index, result); err != nil {
				fail(err)
				return err
			}
		}
		return nil
	}()
	pipeline.Wait()
	return ordered
}

// renderOne renders one request with the session's production profile; deep validation is
// selected deterministically by request index, matching the serial serving order.
func (session *Session) renderOne(ctx context.Context, index int, request FrameRequest) (FrameResult, error) {
	visual, err := session.RenderSurface(ctx, request.TimelineID, request.At)
	if err != nil {
		return FrameResult{}, err
	}
	result, err := session.finishFrame(ctx, request.TimelineID, request.At, visual)
	if err != nil {
		return FrameResult{}, err
	}
	session.served.Add(1)
	if session.deepEvery > 0 && (index == 0 || (index+1)%session.deepEvery == 0) {
		if err := session.deepValidate(ctx, request.TimelineID, request.At, result); err != nil {
			return FrameResult{}, err
		}
		session.deepChecks.Add(1)
	}
	return result, nil
}
