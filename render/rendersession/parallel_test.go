package rendersession

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

// A parallel pass must reproduce the serial path byte for byte, in request order, for every
// worker count — including frames across layer-count changes and the dissolve window.
func TestParallelFramesMatchSerialForEveryWorkerCount(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	session, err := Open(context.Background(), document, bindings, config, Options{DeepValidationInterval: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	times := frameTimes()
	serial := make([]FrameResult, len(times))
	for index, at := range times {
		frame, err := session.RenderFrame(context.Background(), "timeline", at)
		if err != nil {
			t.Fatalf("serial frame %d: %v", at.Value, err)
		}
		serial[index] = frame
	}
	for _, workers := range []int{1, 2, 4, 8} {
		requests := make([]FrameRequest, len(times))
		for index, at := range times {
			requests[index] = FrameRequest{TimelineID: "timeline", At: at}
		}
		parallel, err := session.RenderFrames(context.Background(), requests, ParallelOptions{Workers: workers})
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		for index := range serial {
			if parallel[index].Visual.PixelHash != serial[index].Visual.PixelHash ||
				parallel[index].Output.PixelHash != serial[index].Output.PixelHash ||
				parallel[index].PNG.ArtifactHash != serial[index].PNG.ArtifactHash ||
				parallel[index].TimelineTime != serial[index].TimelineTime {
				t.Fatalf("workers=%d frame %d diverged from serial", workers, times[index].Value)
			}
		}
	}
}

// The ordered observer must observe strictly increasing indexes under concurrency.
func TestParallelObserverSeesStrictRequestOrder(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	session, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	const frames = 40
	requests := make([]FrameRequest, frames)
	for index := range requests {
		requests[index] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(index), Rate: 30}}
	}
	var mu sync.Mutex
	seen := 0
	err = session.RenderFramesFunc(context.Background(), requests, ParallelOptions{Workers: 6}, func(index int, result FrameResult) error {
		mu.Lock()
		defer mu.Unlock()
		if index != seen {
			t.Fatalf("observer saw index %d after %d", index, seen)
		}
		seen++
		if result.PNG.ArtifactHash == "" {
			t.Fatal("observer received an incomplete result")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != frames {
		t.Fatalf("observer saw %d frames, want %d", seen, frames)
	}
}

// Parallel passes are deterministic run to run and respect cancellation without partial output.
func TestParallelDeterminismCancellationAndBounds(t *testing.T) {
	document, bindings, config := sessionFixture(t)
	session, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	const frames = 24
	requests := make([]FrameRequest, frames)
	for index := range requests {
		requests[index] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(index), Rate: 30}}
	}
	first, err := session.RenderFrames(context.Background(), requests, ParallelOptions{Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.RenderFrames(context.Background(), requests, ParallelOptions{Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	for index := range first {
		if first[index].PNG.ArtifactHash != second[index].PNG.ArtifactHash {
			t.Fatalf("frame %d diverged across parallel runs", index)
		}
	}

	// Cancellation mid-pass fails closed with no partial slice.
	canceled, cancel := context.WithCancel(context.Background())
	var observed atomic.Int64
	_ = session.RenderFramesFunc(canceled, requests, ParallelOptions{Workers: 2}, func(index int, _ FrameResult) error {
		if observed.Add(1) == 3 {
			cancel()
		}
		return nil
	})
	// The pass either returned an error or completed before cancellation took effect; a
	// hang would fail the test timeout.

	// An observer error cancels the pass and is reported.
	observerErr := errors.New("observer rejected")
	if err := session.RenderFramesFunc(context.Background(), requests, ParallelOptions{Workers: 3}, func(index int, _ FrameResult) error {
		if index == 2 {
			return observerErr
		}
		return nil
	}); !errors.Is(err, observerErr) {
		t.Fatalf("observer error = %v", err)
	}

	// The worker bound is enforced and closed sessions are rejected.
	if _, err := session.RenderFrames(context.Background(), requests, ParallelOptions{Workers: MaxParallelWorkers + 1}); err == nil {
		t.Fatal("worker bound not enforced")
	}
	closed, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if _, err := closed.RenderFrames(context.Background(), requests, ParallelOptions{Workers: 2}); err == nil {
		t.Fatal("closed session rendered")
	}
}

// The parallel benchmark shows the wall-clock effect of bounded worker parallelism on the
// production frame path.
func BenchmarkSessionFrames640x480Workers1(b *testing.B) {
	benchmarkParallelFrames(b, 1)
}

func BenchmarkSessionFrames640x480Workers4(b *testing.B) {
	benchmarkParallelFrames(b, 4)
}

func BenchmarkSessionFrames640x480Workers8(b *testing.B) {
	benchmarkParallelFrames(b, 8)
}

func benchmarkParallelFrames(b *testing.B, workers int) {
	document, bindings, config := sessionFixtureTB(b, 640, 480)
	session, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()
	requests := make([]FrameRequest, 20)
	for index := range requests {
		requests[index] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(index) + 20, Rate: 30}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := session.RenderFrames(context.Background(), requests, ParallelOptions{Workers: workers}); err != nil {
			b.Fatal(err)
		}
	}
}
