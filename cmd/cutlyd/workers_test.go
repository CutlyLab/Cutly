package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	renderruntime "github.com/CutlyLab/Cutly/render/runtime"
)

func TestWorkerPoolDrainsDurableWorkOnStartupAndWake(t *testing.T) {
	runner := &fakeNextJobRunner{calls: make(chan struct{}, 32)}
	runner.remaining.Store(2)
	pool, err := startWorkerPool(context.Background(), runner, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	waitForRunCalls(t, runner.calls, 4) // two jobs plus one empty observation per worker
	if remaining := runner.remaining.Load(); remaining != 0 {
		t.Fatalf("startup work remaining = %d", remaining)
	}

	runner.remaining.Store(3)
	pool.Notify()
	waitUntil(t, func() bool { return runner.remaining.Load() == 0 })
}

func TestWorkerPoolPollsAfterCapacityAndReportsFatalErrors(t *testing.T) {
	capacity := &fakeNextJobRunner{
		steps: []runnerStep{{err: renderruntime.ErrCapacity}, {found: true}, {}},
		calls: make(chan struct{}, 8),
	}
	pool, err := startWorkerPool(context.Background(), capacity, 1, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCalls(t, capacity.calls, 3)
	pool.Close()
	for err := range pool.Errors() {
		t.Fatalf("capacity was reported as fatal: %v", err)
	}

	want := errors.New("journal failed")
	fatal := &fakeNextJobRunner{steps: []runnerStep{{err: want}}, calls: make(chan struct{}, 2)}
	pool, err = startWorkerPool(context.Background(), fatal, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-pool.Errors():
		if !errors.Is(got, want) {
			t.Fatalf("worker error = %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fatal worker error was not reported")
	}
	pool.Close()
}

func TestWorkerPoolCloseCancelsAndIsConcurrentSafe(t *testing.T) {
	runner := &blockingNextJobRunner{started: make(chan struct{}), canceled: make(chan struct{})}
	pool, err := startWorkerPool(context.Background(), runner, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			pool.Close()
		}()
	}
	group.Wait()
	select {
	case <-runner.canceled:
	default:
		t.Fatal("RunNext context was not canceled")
	}
}

func TestWorkerPoolRejectsInvalidConfiguration(t *testing.T) {
	runner := &fakeNextJobRunner{}
	var typedNil *nilNextJobRunner
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		ctx      context.Context
		runner   nextJobRunner
		workers  int
		interval time.Duration
	}{
		{ctx: nil, runner: runner, workers: 1},
		{ctx: context.Background(), runner: nil, workers: 1},
		{ctx: context.Background(), runner: typedNil, workers: 1},
		{ctx: context.Background(), runner: runner, workers: 0},
		{ctx: context.Background(), runner: runner, workers: renderruntime.MaxConcurrentJobs + 1},
		{ctx: context.Background(), runner: runner, workers: 1, interval: time.Microsecond},
		{ctx: context.Background(), runner: runner, workers: 1, interval: maximumWorkerPollInterval + time.Second},
	} {
		if pool, err := startWorkerPool(test.ctx, test.runner, test.workers, test.interval); err == nil || pool != nil {
			t.Fatalf("invalid config accepted: %+v", test)
		}
	}
	if pool, err := startWorkerPool(canceled, runner, 1, time.Second); !errors.Is(err, context.Canceled) || pool != nil {
		t.Fatalf("canceled context accepted: pool=%v err=%v", pool, err)
	}
	var nilPool *workerPool
	nilPool.Notify()
	nilPool.Close()
	if _, open := <-nilPool.Errors(); open {
		t.Fatal("nil pool returned an open error channel")
	}
}

type nilNextJobRunner struct{}

func (*nilNextJobRunner) RunNext(context.Context) (renderruntime.Run, bool, error) {
	panic("typed nil runner must never be called")
}

type runnerStep struct {
	found bool
	err   error
}

type fakeNextJobRunner struct {
	mu        sync.Mutex
	steps     []runnerStep
	remaining atomic.Int64
	calls     chan struct{}
}

func (runner *fakeNextJobRunner) RunNext(context.Context) (renderruntime.Run, bool, error) {
	if runner.calls != nil {
		runner.calls <- struct{}{}
	}
	runner.mu.Lock()
	if len(runner.steps) != 0 {
		step := runner.steps[0]
		runner.steps = runner.steps[1:]
		runner.mu.Unlock()
		return renderruntime.Run{}, step.found, step.err
	}
	runner.mu.Unlock()
	for {
		remaining := runner.remaining.Load()
		if remaining <= 0 {
			return renderruntime.Run{}, false, nil
		}
		if runner.remaining.CompareAndSwap(remaining, remaining-1) {
			return renderruntime.Run{}, true, nil
		}
	}
}

type blockingNextJobRunner struct {
	once     sync.Once
	started  chan struct{}
	canceled chan struct{}
}

func (runner *blockingNextJobRunner) RunNext(ctx context.Context) (renderruntime.Run, bool, error) {
	runner.once.Do(func() { close(runner.started) })
	<-ctx.Done()
	select {
	case <-runner.canceled:
	default:
		close(runner.canceled)
	}
	return renderruntime.Run{}, false, ctx.Err()
}

func waitForRunCalls(t *testing.T, calls <-chan struct{}, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatalf("received %d/%d RunNext calls", index, count)
		}
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition was not satisfied")
		case <-ticker.C:
		}
	}
}
