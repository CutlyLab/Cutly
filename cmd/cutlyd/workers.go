package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	renderruntime "github.com/CutlyLab/Cutly/render/runtime"
)

const (
	defaultWorkerPollInterval = 250 * time.Millisecond
	maximumWorkerPollInterval = time.Minute
)

type nextJobRunner interface {
	// RunNext must return when the context is canceled. workerPool.Close waits
	// for that ownership handoff before allowing durable stores to close.
	RunNext(context.Context) (renderruntime.Run, bool, error)
}

// workerPool owns the in-process dispatcher consumers for one cutlyd runtime.
// Every worker probes the durable queue immediately on startup, so replayed
// running/canceling/queued work does not depend on a new HTTP submission.
type workerPool struct {
	cancel context.CancelFunc
	wake   chan struct{}
	errors chan error
	done   chan struct{}
	once   sync.Once
}

func startWorkerPool(parent context.Context, runner nextJobRunner, workers int, pollInterval time.Duration) (*workerPool, error) {
	if parent == nil || nilLikeNextJobRunner(runner) || workers < 1 || workers > renderruntime.MaxConcurrentJobs {
		return nil, errors.New("cutlyd: worker pool requires a context, runner and bounded worker count")
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if pollInterval == 0 {
		pollInterval = defaultWorkerPollInterval
	}
	if pollInterval < time.Millisecond || pollInterval > maximumWorkerPollInterval {
		return nil, fmt.Errorf("cutlyd: worker poll interval must be within [1ms,%s]", maximumWorkerPollInterval)
	}
	ctx, cancel := context.WithCancel(parent)
	pool := &workerPool{
		cancel: cancel,
		wake:   make(chan struct{}, workers),
		errors: make(chan error, workers),
		done:   make(chan struct{}),
	}
	var group sync.WaitGroup
	group.Add(workers)
	for index := 0; index < workers; index++ {
		go func(worker int) {
			defer group.Done()
			pool.run(ctx, runner, worker, pollInterval)
		}(index)
	}
	go func() {
		group.Wait()
		close(pool.errors)
		close(pool.done)
	}()
	return pool, nil
}

func nilLikeNextJobRunner(runner nextJobRunner) bool {
	if runner == nil {
		return true
	}
	value := reflect.ValueOf(runner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Notify wakes every potentially idle worker without blocking the submitter.
// The bounded channel deliberately coalesces bursts; an awakened worker drains
// durable work until RunNext reports an empty queue.
func (pool *workerPool) Notify() {
	if pool == nil {
		return
	}
	for remaining := cap(pool.wake); remaining > 0; remaining-- {
		select {
		case pool.wake <- struct{}{}:
		default:
			return
		}
	}
}

func (pool *workerPool) Errors() <-chan error {
	if pool == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return pool.errors
}

// Close cancels in-flight RunNext calls and waits until every worker releases
// its job ownership. It is safe to call concurrently and repeatedly.
func (pool *workerPool) Close() {
	if pool == nil {
		return
	}
	pool.once.Do(pool.cancel)
	<-pool.done
}

func (pool *workerPool) run(ctx context.Context, runner nextJobRunner, worker int, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	shouldDrain := true
	for {
		if shouldDrain {
			_, found, err := runner.RunNext(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, renderruntime.ErrCapacity) {
					shouldDrain = false
					continue
				}
				select {
				case pool.errors <- fmt.Errorf("cutlyd: render worker %d: %w", worker, err):
				case <-ctx.Done():
				}
				return
			}
			if found {
				continue
			}
			shouldDrain = false
		}
		select {
		case <-ctx.Done():
			return
		case <-pool.wake:
			shouldDrain = true
		case <-ticker.C:
			shouldDrain = true
		}
	}
}
