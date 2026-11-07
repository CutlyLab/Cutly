package backendstage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
	checkpointlocal "github.com/CutlyLab/Cutly/render/checkpoint/local"
	"github.com/CutlyLab/Cutly/render/chunkplan"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestReferenceChunksExecuteCanonicalBytesAndResumeFromCheckpoints(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-reference", "reference.mp4")
	plan := mustPlan(t, fixture.service, job)
	assertReferencePlan(t, plan)
	store := newReferenceCheckpointStore(t, fixture)
	engine := fixture.engine(t, job, plan, store)

	result, err := engine.Run(context.Background(), plan)
	if err != nil || result.Executed != len(plan.Tasks) || result.CacheHits != 0 {
		t.Fatalf("first run = %+v, %v", result, err)
	}
	if fixture.providers.surfaceCalls.Load() != job.FrameRange.Count || fixture.providers.pcmCalls.Load() != 1 ||
		fixture.backend.assembleCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 0 || fixture.backend.preflightCalls.Load() != 0 {
		t.Fatalf("provider/assembler/render/preflight calls = %d/%d/%d/%d/%d", fixture.providers.surfaceCalls.Load(), fixture.providers.pcmCalls.Load(), fixture.backend.assembleCalls.Load(), fixture.backend.renderCalls.Load(), fixture.backend.preflightCalls.Load())
	}
	wantConsumed := result.Checkpoints[0].Output.SizeBytes + result.Checkpoints[1].Output.SizeBytes
	if fixture.backend.assembledBytes.Load() != wantConsumed {
		t.Fatalf("assembler consumed %d bytes, want exact chunk total %d", fixture.backend.assembledBytes.Load(), wantConsumed)
	}
	artifact, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result)
	if err != nil || artifact.URI != job.ArtifactURI {
		t.Fatalf("artifact = %+v, %v", artifact, err)
	}

	selection, err := fixture.service.SelectMissingChunks(context.Background(), job, result.Checkpoints[:len(plan.Tasks)-1])
	if err != nil || len(selection.Ready) != len(plan.Tasks)-1 || len(selection.Missing) != 0 || len(selection.Rejected) != 0 {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
	second, err := engine.Run(context.Background(), plan)
	if err != nil || second.Executed != 0 || second.CacheHits != len(plan.Tasks) {
		t.Fatalf("checkpoint resume = %+v, %v", second, err)
	}
	if fixture.providers.surfaceCalls.Load() != job.FrameRange.Count || fixture.providers.pcmCalls.Load() != 1 ||
		fixture.backend.assembleCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 0 {
		t.Fatal("cache-hit restart repeated media work")
	}
}

func TestReferenceChunksRetryExplicitTransientFailureWithAttemptBoundReceipt(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-reference-transient-retry", "reference-transient-retry.mp4")
	plan := mustPlan(t, fixture.service, job)
	originalPCM := fixture.service.reference.pcm
	var providerCalls atomic.Int32
	var firstSpool string
	fixture.service.reference.pcm = PCMProviderFunc(func(ctx context.Context, request ReferenceChunkRequest, sampleRange videoedit.TickRange) (*pcm16le.Block, error) {
		call := providerCalls.Add(1)
		entries, err := os.ReadDir(fixture.service.reference.scratchDirectory)
		if err != nil || len(entries) != 1 {
			return nil, fmt.Errorf("attempt %d scratch entries=%d: %w", call, len(entries), err)
		}
		if call == 1 {
			firstSpool = entries[0].Name()
			return nil, &stagedag.StageError{Class: renderapi.FailureTransient, Err: errors.New("temporary provider outage")}
		}
		if call != 2 || entries[0].Name() == firstSpool {
			return nil, fmt.Errorf("retry reused or leaked spool: first=%q current=%q call=%d", firstSpool, entries[0].Name(), call)
		}
		reader, openErr := fixture.store.Open(ctx, plan.Tasks[0].CacheKey, "transient-attempt-audit")
		if reader != nil {
			_ = reader.Close()
			return nil, errors.New("failed attempt exposed partial chunk CAS")
		}
		if !errors.Is(openErr, cas.ErrMiss) {
			return nil, fmt.Errorf("failed-attempt CAS lookup: %w", openErr)
		}
		return originalPCM.RenderPCM(ctx, request, sampleRange)
	})

	resolved := mustExecutor(t, fixture.service, job, plan)
	attemptExecutor, ok := resolved.(*executor)
	if !ok {
		t.Fatalf("executor type = %T", resolved)
	}
	var invocationCalls atomic.Int32
	attemptExecutor.newInvocation = func() (string, error) {
		return fmt.Sprintf("reference-attempt-%d", invocationCalls.Add(1)), nil
	}
	store := newReferenceCheckpointStore(t, fixture)
	engine := stagedag.Engine{
		Store: store, Executor: attemptExecutor,
		Limits: stagedag.Limits{MaxConcurrent: 1, MaxMemoryBytes: 8 << 20, MaxDiskBytes: 64 << 20},
	}
	result, err := engine.Run(context.Background(), plan)
	if err != nil || result.Executed != len(plan.Tasks) || result.CacheHits != 0 || result.Retries != 1 {
		t.Fatalf("retry result = %+v, %v", result, err)
	}
	audioCheckpoint := result.Checkpoints[0]
	audioReceipt, err := chunkplan.DecodeReceipt(audioCheckpoint.Output.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 2 || audioCheckpoint.AttemptCount != 2 || audioReceipt.AttemptCount != 2 ||
		audioReceipt.Worker.InvocationID != "reference-attempt-2" || invocationCalls.Load() != 3 {
		t.Fatalf("provider/invocation/checkpoint = %d/%d/%d/%q totalInvocations=%d",
			providerCalls.Load(), audioCheckpoint.AttemptCount, audioReceipt.AttemptCount,
			audioReceipt.Worker.InvocationID, invocationCalls.Load())
	}
	if entries, readErr := os.ReadDir(fixture.service.reference.scratchDirectory); readErr != nil || len(entries) != 0 {
		t.Fatalf("retry left spool entries=%d err=%v", len(entries), readErr)
	}
	selection, err := fixture.service.SelectMissingChunks(context.Background(), job, result.Checkpoints[:len(plan.Tasks)-1])
	if err != nil || len(selection.Ready) != len(plan.Tasks)-1 || len(selection.Missing) != 0 || len(selection.Rejected) != 0 {
		t.Fatalf("attempt-2 recovery selection = %+v, %v", selection, err)
	}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result); err != nil {
		t.Fatalf("attempt-2 artifact resolution: %v", err)
	}
	resumed, err := engine.Run(context.Background(), plan)
	if err != nil || resumed.Executed != 0 || resumed.CacheHits != len(plan.Tasks) || resumed.Retries != 0 ||
		resumed.Checkpoints[0].AttemptCount != 2 {
		t.Fatalf("attempt-2 checkpoint restart = %+v, %v", resumed, err)
	}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, resumed); err != nil {
		t.Fatalf("resumed attempt-2 artifact resolution: %v", err)
	}

	allExecutedWithoutRetry := result
	allExecutedWithoutRetry.Retries = 0
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, allExecutedWithoutRetry); !errors.Is(err, ErrArtifact) {
		t.Fatalf("all-executed retry counter forgery = %v", err)
	}
	finalOnlyWithRetry := result
	finalOnlyWithRetry.Executed, finalOnlyWithRetry.CacheHits, finalOnlyWithRetry.Retries = 1, len(plan.Tasks)-1, 1
	finalOnlyWithRetry.TaskOutcomes = append([]stagedag.TaskOutcome(nil), result.TaskOutcomes...)
	for index, task := range plan.Tasks {
		finalOnlyWithRetry.TaskOutcomes[index] = stagedag.TaskOutcome{TaskID: task.ID, CacheHit: true}
	}
	finalOnlyWithRetry.TaskOutcomes[len(plan.Tasks)-1] = stagedag.TaskOutcome{
		TaskID: plan.Tasks[len(plan.Tasks)-1].ID, AttemptsThisRun: 1,
	}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, finalOnlyWithRetry); !errors.Is(err, ErrArtifact) {
		t.Fatalf("final-only retry counter forgery = %v", err)
	}
	chunkOnlyRetry := result
	chunkOnlyRetry.Executed, chunkOnlyRetry.CacheHits, chunkOnlyRetry.Retries = 1, len(plan.Tasks)-1, 1
	chunkOnlyRetry.TaskOutcomes = append([]stagedag.TaskOutcome(nil), finalOnlyWithRetry.TaskOutcomes...)
	chunkOnlyRetry.TaskOutcomes[0] = stagedag.TaskOutcome{TaskID: plan.Tasks[0].ID, AttemptsThisRun: 2}
	chunkOnlyRetry.TaskOutcomes[len(plan.Tasks)-1] = stagedag.TaskOutcome{
		TaskID: plan.Tasks[len(plan.Tasks)-1].ID, CacheHit: true,
	}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, chunkOnlyRetry); err != nil {
		t.Fatalf("legitimate chunk-only retry telemetry rejected: %v", err)
	}

	// The enclosing checkpoint and canonical receipt must name the same
	// producing attempt on every recovery path.
	mismatched := append([]stagedag.Checkpoint(nil), result.Checkpoints...)
	mismatched[0].AttemptCount = 1
	selection, err = fixture.service.SelectMissingChunks(context.Background(), job, mismatched[:len(plan.Tasks)-1])
	if err != nil || len(selection.Missing) != 1 || len(selection.Rejected) != 1 || len(selection.Ready) != len(plan.Tasks)-2 {
		t.Fatalf("attempt-mismatch selection = %+v, %v", selection, err)
	}
	mismatchedResult := result
	mismatchedResult.Checkpoints = mismatched
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, mismatchedResult); !errors.Is(err, ErrArtifact) {
		t.Fatalf("attempt-mismatch artifact error = %v", err)
	}
}

func TestReferenceHistoricalAttemptRemainsAForcedCacheHitAfterPolicyReduction(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-reference-historical-attempt", "reference-historical-attempt.mp4")
	plan := mustPlan(t, fixture.service, job)
	executor := mustExecutor(t, fixture.service, job, plan)
	seed, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	seedReceipt, err := chunkplan.DecodeReceipt(seed.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	historicalPlan := plan
	historicalPlan.Tasks = append([]stagedag.Task(nil), plan.Tasks...)
	historicalPlan.Tasks[0].Retry = stagedag.RetryPolicy{MaxAttempts: 3, RetryTransient: true}
	historicalReceipt, err := chunkplan.NewReceipt(context.Background(), historicalPlan.Tasks[0], seedReceipt.Output, 3, chunkplan.WorkerBinding{
		InvocationID: "reference-historical-attempt", ExecutableDigest: fixture.service.reference.executableDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	historicalOutput, err := chunkplan.StageOutput(historicalReceipt)
	if err != nil {
		t.Fatal(err)
	}
	historicalCheckpoint, err := stagedag.NewCheckpoint(historicalPlan, historicalPlan.Tasks[0], historicalOutput, 3)
	if err != nil {
		t.Fatal(err)
	}
	store := newReferenceCheckpointStore(t, fixture)
	if err := store.Commit(context.Background(), historicalCheckpoint); err != nil {
		t.Fatal(err)
	}
	selection, err := fixture.service.SelectMissingChunks(context.Background(), job, []stagedag.Checkpoint{historicalCheckpoint})
	if err != nil || len(selection.Ready) != 1 || len(selection.Missing) != len(plan.Tasks)-2 ||
		len(selection.Rejected) != 0 || selection.Ready[0].AttemptCount != 3 {
		t.Fatalf("historical checkpoint selection = %+v, %v", selection, err)
	}
	result, err := fixture.engine(t, job, plan, store).Run(context.Background(), plan)
	if err != nil || result.CacheHits != 1 || result.Executed != len(plan.Tasks)-1 || result.Retries != 0 ||
		result.Checkpoints[0].AttemptCount != 3 ||
		result.TaskOutcomes[0] != (stagedag.TaskOutcome{TaskID: plan.Tasks[0].ID, CacheHit: true}) {
		t.Fatalf("historical checkpoint run = %+v, %v", result, err)
	}
	if fixture.providers.pcmCalls.Load() != 1 {
		t.Fatalf("historical cache hit reran PCM provider %d times", fixture.providers.pcmCalls.Load())
	}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result); err != nil {
		t.Fatalf("historical checkpoint artifact resolution: %v", err)
	}
	forgedExecution := result
	forgedExecution.Executed, forgedExecution.CacheHits, forgedExecution.Retries = len(plan.Tasks), 0, 2
	forgedExecution.TaskOutcomes = append([]stagedag.TaskOutcome(nil), result.TaskOutcomes...)
	forgedExecution.TaskOutcomes[0] = stagedag.TaskOutcome{TaskID: plan.Tasks[0].ID, AttemptsThisRun: 3}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, forgedExecution); !errors.Is(err, ErrArtifact) {
		t.Fatalf("historical attempt was accepted as current execution: %v", err)
	}
}

func TestReferenceVideoRetryDiscardsPartialFirstAttemptSpool(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-reference-partial-video-retry", "reference-partial-video-retry.mp4")
	plan := mustPlan(t, fixture.service, job)
	original := fixture.service.reference.surfaces
	firstFrame, secondFrame := plan.Tasks[1].FrameRange.First, plan.Tasks[1].FrameRange.First+1
	var failed atomic.Bool
	var firstSpool string
	fixture.service.reference.surfaces = SurfaceProviderFunc(func(ctx context.Context, request ReferenceChunkRequest, frame int64) (*cpucompositor.Surface, error) {
		if frame == secondFrame && failed.CompareAndSwap(false, true) {
			entries, err := os.ReadDir(fixture.service.reference.scratchDirectory)
			if err != nil || len(entries) != 1 {
				return nil, fmt.Errorf("partial attempt scratch entries=%d: %w", len(entries), err)
			}
			firstSpool = entries[0].Name()
			return nil, &stagedag.StageError{Class: renderapi.FailureTransient, Err: errors.New("temporary second-frame failure")}
		}
		if frame == firstFrame && failed.Load() {
			entries, err := os.ReadDir(fixture.service.reference.scratchDirectory)
			if err != nil || len(entries) != 1 || entries[0].Name() == firstSpool {
				return nil, fmt.Errorf("partial retry leaked/reused spool first=%q entries=%v: %w", firstSpool, entries, err)
			}
			reader, openErr := fixture.store.Open(ctx, plan.Tasks[1].CacheKey, "partial-video-attempt-audit")
			if reader != nil {
				_ = reader.Close()
				return nil, errors.New("partial video attempt exposed CAS")
			}
			if !errors.Is(openErr, cas.ErrMiss) {
				return nil, openErr
			}
		}
		return original.RenderSurface(ctx, request, frame)
	})
	result, err := fixture.engine(t, job, plan, newReferenceCheckpointStore(t, fixture)).Run(context.Background(), plan)
	if err != nil || result.Retries != 1 || result.Checkpoints[1].AttemptCount != 2 ||
		result.TaskOutcomes[1] != (stagedag.TaskOutcome{TaskID: plan.Tasks[1].ID, AttemptsThisRun: 2}) {
		t.Fatalf("partial video retry = %+v, %v", result, err)
	}
	if entries, readErr := os.ReadDir(fixture.service.reference.scratchDirectory); readErr != nil || len(entries) != 0 {
		t.Fatalf("partial video retry left spool entries=%d err=%v", len(entries), readErr)
	}
}

func TestReferenceChunkCleanupFailureSuppressesTransientRetryClass(t *testing.T) {
	transient := &stagedag.StageError{Class: renderapi.FailureTransient, Err: errors.New("temporary")}
	err := chunkCleanupFailure(wrapError(ErrExecution, "provider", transient), errors.New("remove spool"))
	var classified *stagedag.StageError
	if !errors.Is(err, ErrExecution) || errors.As(err, &classified) {
		t.Fatalf("cleanup failure retained retry classification: %v", err)
	}
	canceled := chunkCleanupFailure(&stagedag.StageError{Class: renderapi.FailureTransient, Err: context.Canceled}, errors.New("remove spool"))
	classified = nil
	if !errors.Is(canceled, context.Canceled) || errors.As(canceled, &classified) {
		t.Fatalf("cleanup failure lost cancellation or retained retry class: %v", canceled)
	}
}

func TestReferenceChunksDoNotRetryCancellationPanicOrUnclassifiedFailures(t *testing.T) {
	tests := []struct {
		name      string
		failure   error
		panicNow  bool
		wantCause error
	}{
		{name: "canceled transient", failure: &stagedag.StageError{Class: renderapi.FailureTransient, Err: context.Canceled}, wantCause: context.Canceled},
		{name: "deadline transient", failure: &stagedag.StageError{Class: renderapi.FailureTransient, Err: context.DeadlineExceeded}, wantCause: context.DeadlineExceeded},
		{name: "unclassified", failure: errors.New("unclassified provider failure")},
		{name: "permanent", failure: &stagedag.StageError{Class: renderapi.FailureNonRetryable, Err: errors.New("permanent provider failure")}},
		{name: "resource", failure: &stagedag.StageError{Class: renderapi.FailureResource, Err: errors.New("resource provider failure")}},
		{name: "panic", panicNow: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReferenceFixture(t)
			var calls atomic.Int32
			fixture.service.reference.surfaces = SurfaceProviderFunc(func(context.Context, ReferenceChunkRequest, int64) (*cpucompositor.Surface, error) {
				calls.Add(1)
				if test.panicNow {
					panic("provider panic")
				}
				return nil, test.failure
			})
			job := fixture.job("job-no-retry-"+strings.ReplaceAll(test.name, " ", "-"), "no-retry.mp4")
			plan := mustPlan(t, fixture.service, job)
			result, err := fixture.engine(t, job, plan, newReferenceCheckpointStore(t, fixture)).Run(context.Background(), plan)
			if err == nil || result.Executed != 0 || calls.Load() != 1 {
				t.Fatalf("failure result=%+v calls=%d err=%v", result, calls.Load(), err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("failure cause = %v, want %v", err, test.wantCause)
			}
			if fixture.backend.assembleCalls.Load() != 0 {
				t.Fatal("failed chunk reached final assembler")
			}
			reader, openErr := fixture.store.Open(context.Background(), plan.Tasks[1].CacheKey, "no-retry-audit")
			if reader != nil || !errors.Is(openErr, cas.ErrMiss) {
				if reader != nil {
					_ = reader.Close()
				}
				t.Fatalf("failed chunk CAS reader=%v err=%v", reader, openErr)
			}
			if entries, readErr := os.ReadDir(fixture.service.reference.scratchDirectory); readErr != nil || len(entries) != 0 {
				t.Fatalf("failed chunk left spool entries=%d err=%v", len(entries), readErr)
			}
		})
	}

	t.Run("final transient", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		fixture.backend.renderErr = &stagedag.StageError{Class: renderapi.FailureTransient, Err: errors.New("temporary encoder failure")}
		job := fixture.job("job-final-transient-no-retry", "final-transient-no-retry.mp4")
		plan := mustPlan(t, fixture.service, job)
		result, err := fixture.engine(t, job, plan, newReferenceCheckpointStore(t, fixture)).Run(context.Background(), plan)
		if err == nil || result.Executed != 0 || fixture.backend.assembleCalls.Load() != 1 {
			t.Fatalf("final transient result=%+v calls=%d err=%v", result, fixture.backend.assembleCalls.Load(), err)
		}
		reader, openErr := fixture.store.Open(context.Background(), plan.Tasks[len(plan.Tasks)-1].CacheKey, "final-transient-audit")
		if reader != nil || !errors.Is(openErr, cas.ErrMiss) {
			if reader != nil {
				_ = reader.Close()
			}
			t.Fatalf("failed final CAS reader=%v err=%v", reader, openErr)
		}
	})
}

func TestReferenceChunksWorkerCrashRestartsOnlyMissingWork(t *testing.T) {
	fixture := newReferenceFixture(t)
	fixture.providers.failSurface.Store(1)
	job := fixture.job("job-worker-crash", "worker-crash.mp4")
	plan := mustPlan(t, fixture.service, job)
	store := newReferenceCheckpointStore(t, fixture)
	engine := fixture.engine(t, job, plan, store)

	if _, err := engine.Run(context.Background(), plan); err == nil || !errors.Is(err, errWorkerCrash) {
		t.Fatalf("worker crash error = %v", err)
	}
	if fixture.providers.pcmCalls.Load() != 1 || fixture.backend.assembleCalls.Load() != 0 || fixture.backend.renderCalls.Load() != 0 {
		t.Fatalf("work before crash = pcm %d assembler %d render %d", fixture.providers.pcmCalls.Load(), fixture.backend.assembleCalls.Load(), fixture.backend.renderCalls.Load())
	}
	if fixture.providers.surfaceCalls.Load() != 1 {
		t.Fatalf("unclassified worker failure retried %d times", fixture.providers.surfaceCalls.Load())
	}
	if reader, openErr := fixture.store.Open(context.Background(), plan.Tasks[1].CacheKey, "worker-crash-audit"); reader != nil || !errors.Is(openErr, cas.ErrMiss) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("failed video chunk became visible in CAS: reader=%v err=%v", reader, openErr)
	}
	restarted, err := engine.Run(context.Background(), plan)
	if err != nil || restarted.CacheHits != 1 || restarted.Executed != 2 {
		t.Fatalf("restart = %+v, %v", restarted, err)
	}
	if fixture.providers.pcmCalls.Load() != 1 || fixture.providers.surfaceCalls.Load() != job.FrameRange.Count+1 ||
		fixture.backend.assembleCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 0 {
		t.Fatalf("restart repeated completed chunk: surface=%d pcm=%d backend=%d",
			fixture.providers.surfaceCalls.Load(), fixture.providers.pcmCalls.Load(), fixture.backend.assembleCalls.Load())
	}
}

func TestReferenceChunkReceiptInvocationIDsArePerExecution(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-invocation-id", "invocation-id.mp4")
	plan := mustPlan(t, fixture.service, job)
	executor := mustExecutor(t, fixture.service, job, plan)
	first, err := executor.Execute(context.Background(), plan.Tasks[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(context.Background(), plan.Tasks[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	firstReceipt, err := chunkplan.DecodeReceipt(first.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := chunkplan.DecodeReceipt(second.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash || firstReceipt.AttemptCount != 1 || secondReceipt.AttemptCount != 1 ||
		firstReceipt.Worker.InvocationID == secondReceipt.Worker.InvocationID {
		t.Fatalf("content/invocations = %s %s / %s %s", first.ContentHash, second.ContentHash, firstReceipt.Worker.InvocationID, secondReceipt.Worker.InvocationID)
	}
}

func TestReferenceFinalFailsZeroBeforeBackendWhenChunkCASIsCorrupt(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-corrupt-before-final", "corrupt-before-final.mp4")
	plan := mustPlan(t, fixture.service, job)
	executor := mustExecutor(t, fixture.service, job, plan)
	dependencies := make([]stagedag.Dependency, 0, len(plan.Tasks)-1)
	for _, task := range plan.Tasks[:len(plan.Tasks)-1] {
		output, err := executor.Execute(context.Background(), task, nil)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := stagedag.NewCheckpoint(plan, task, output, 1)
		if err != nil {
			t.Fatal(err)
		}
		dependencies = append(dependencies, stagedag.Dependency{TaskID: task.ID, Checkpoint: checkpoint})
	}
	corruptPayloadByHash(t, filepath.Join(fixture.root, "cas"), dependencies[1].Checkpoint.Output.ContentHash)
	output, err := executor.Execute(context.Background(), plan.Tasks[len(plan.Tasks)-1], dependencies)
	if err == nil || output.ContentHash != "" || output.SizeBytes != 0 || output.Receipt != nil {
		t.Fatalf("corrupt dependency output=%+v err=%v", output, err)
	}
	if fixture.backend.assembleCalls.Load() != 0 || fixture.backend.renderCalls.Load() != 0 || fixture.backend.inspectCalls.Load() != 0 {
		t.Fatal("final backend was touched before complete aggregate/CAS verification")
	}
}

func TestReferenceChunkExecutableDigestDriftIsNeverReadyOrAggregated(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-worker-digest-drift", "worker-digest-drift.mp4")
	plan := mustPlan(t, fixture.service, job)
	result, err := fixture.engine(t, job, plan, newReferenceCheckpointStore(t, fixture)).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original, err := chunkplan.DecodeReceipt(result.Checkpoints[0].Output.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	driftedReceipt, err := chunkplan.NewReceipt(context.Background(), plan.Tasks[0], original.Output, 1, chunkplan.WorkerBinding{
		InvocationID: original.Worker.InvocationID, ExecutableDigest: digest('7'),
	})
	if err != nil {
		t.Fatal(err)
	}
	driftedOutput, err := chunkplan.StageOutput(driftedReceipt)
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := stagedag.NewCheckpoint(plan, plan.Tasks[0], driftedOutput, 1)
	if err != nil {
		t.Fatal(err)
	}
	candidates := append([]stagedag.Checkpoint(nil), result.Checkpoints[:len(plan.Tasks)-1]...)
	candidates[0] = drifted
	selection, err := fixture.service.SelectMissingChunks(context.Background(), job, candidates)
	if err != nil || len(selection.Ready) != len(candidates)-1 || len(selection.Missing) != 1 ||
		selection.Missing[0].ID != plan.Tasks[0].ID || len(selection.Rejected) == 0 {
		t.Fatalf("digest-drift selection = %+v, %v", selection, err)
	}

	dependencies := make([]stagedag.Dependency, len(candidates))
	for index, checkpoint := range candidates {
		dependencies[index] = stagedag.Dependency{TaskID: plan.Tasks[index].ID, Checkpoint: checkpoint}
	}
	before := fixture.backend.assembleCalls.Load()
	output, err := mustExecutor(t, fixture.service, job, plan).Execute(context.Background(), plan.Tasks[len(plan.Tasks)-1], dependencies)
	if err == nil || output.ContentHash != "" || output.SizeBytes != 0 || output.Receipt != nil {
		t.Fatalf("digest-drift final output=%+v err=%v", output, err)
	}
	if fixture.backend.assembleCalls.Load() != before {
		t.Fatal("digest-drifted dependency reached the assembler")
	}

	driftedResult := result
	driftedResult.Checkpoints = append([]stagedag.Checkpoint(nil), result.Checkpoints...)
	driftedResult.Checkpoints[0] = drifted
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, driftedResult); err == nil {
		t.Fatal("artifact resolution aggregated a foreign executable digest")
	}
}

func TestReferenceSelectMissingRejectsHostileCandidateShapesBeforeCAS(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-hostile-selection", "hostile-selection.mp4")
	plan := mustPlan(t, fixture.service, job)
	executor := mustExecutor(t, fixture.service, job, plan)
	output, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan, plan.Tasks[0], output, 1)
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingContentStore{contentStore: fixture.service.cas}
	fixture.service.cas = counting

	t.Run("candidate count exceeds exact plan", func(t *testing.T) {
		candidates := []stagedag.Checkpoint{checkpoint, checkpoint, checkpoint}
		candidates[2].Output.Receipt = make([]byte, 2<<20)
		if _, err := fixture.service.SelectMissingChunks(context.Background(), job, candidates); !errors.Is(err, ErrArtifact) {
			t.Fatalf("oversized candidate error = %v", err)
		}
		if counting.openCalls.Load() != 0 {
			t.Fatalf("oversized candidate count queried CAS %d times", counting.openCalls.Load())
		}
	})

	t.Run("receipt bytes exceed admitted validation state", func(t *testing.T) {
		counting.openCalls.Store(0)
		candidates := []stagedag.Checkpoint{checkpoint, checkpoint}
		for index := range candidates {
			candidates[index].Output.Receipt = make([]byte, 2<<20)
		}
		if _, err := fixture.service.SelectMissingChunks(context.Background(), job, candidates); !errors.Is(err, ErrArtifact) {
			t.Fatalf("oversized receipt error = %v", err)
		}
		if counting.openCalls.Load() != 0 {
			t.Fatalf("oversized receipts queried CAS %d times", counting.openCalls.Load())
		}
	})

	t.Run("service memory below final validation allowance", func(t *testing.T) {
		counting.openCalls.Store(0)
		previous := fixture.service.memoryBytes
		fixture.service.memoryBytes = 1
		defer func() { fixture.service.memoryBytes = previous }()
		if _, err := fixture.service.SelectMissingChunks(context.Background(), job, []stagedag.Checkpoint{checkpoint}); !errors.Is(err, ErrArtifact) {
			t.Fatalf("low-memory selection error = %v", err)
		}
		if counting.openCalls.Load() != 0 {
			t.Fatalf("low-memory selection queried CAS %d times", counting.openCalls.Load())
		}
	})

	t.Run("duplicates are rejected without availability lookup", func(t *testing.T) {
		counting.openCalls.Store(0)
		selection, err := fixture.service.SelectMissingChunks(context.Background(), job, []stagedag.Checkpoint{checkpoint, checkpoint})
		if err != nil || len(selection.Ready) != 0 || len(selection.Missing) != len(plan.Tasks)-1 || len(selection.Rejected) != 1 {
			t.Fatalf("duplicate selection=%+v err=%v", selection, err)
		}
		if counting.openCalls.Load() != 0 {
			t.Fatalf("duplicate candidates queried CAS %d times", counting.openCalls.Load())
		}
	})

	t.Run("unknown tasks are rejected without availability lookup", func(t *testing.T) {
		counting.openCalls.Store(0)
		candidates := []stagedag.Checkpoint{checkpoint, checkpoint}
		candidates[0].TaskID = "unknown-a"
		candidates[1].TaskID = "unknown-b"
		selection, err := fixture.service.SelectMissingChunks(context.Background(), job, candidates)
		if err != nil || len(selection.Ready) != 0 || len(selection.Missing) != len(plan.Tasks)-1 || len(selection.Rejected) != 2 {
			t.Fatalf("unknown selection=%+v err=%v", selection, err)
		}
		if counting.openCalls.Load() != 0 {
			t.Fatalf("unknown candidates queried CAS %d times", counting.openCalls.Load())
		}
	})
}

func TestReferenceAssemblerSelectionNeverCallsLegacyManifestResolver(t *testing.T) {
	fixture := newReferenceFixture(t)
	var resolverCalls atomic.Int32
	fixture.service.backends = BackendResolverFunc(func(context.Context, renderapi.ExecutionManifest) (Backend, error) {
		resolverCalls.Add(1)
		return nil, errors.New("legacy resolver must be unreachable in reference mode")
	})
	job := fixture.job("job-locator-free-assembler", "locator-free-assembler.mp4")
	plan := mustPlan(t, fixture.service, job)
	result, err := fixture.engine(t, job, plan, newReferenceCheckpointStore(t, fixture)).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result); err != nil {
		t.Fatal(err)
	}
	if resolverCalls.Load() != 0 {
		t.Fatalf("legacy resolver calls = %d", resolverCalls.Load())
	}
	fixture.backend.mu.Lock()
	request := cloneChunkAssemblyRequest(*fixture.backend.publications[job.ArtifactURI].assembly)
	fixture.backend.mu.Unlock()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"resources"`, `"fonts"`, `"locator"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("locator-bearing field %s crossed assembler boundary: %s", forbidden, raw)
		}
	}
}

func TestReferenceArtifactRejectsExternalAggregateDrift(t *testing.T) {
	fixture := newReferenceFixture(t)
	job := fixture.job("job-aggregate-drift", "aggregate-drift.mp4")
	plan := mustPlan(t, fixture.service, job)
	store := newReferenceCheckpointStore(t, fixture)
	result, err := fixture.engine(t, job, plan, store).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.backend.mu.Lock()
	publication := fixture.backend.publications[job.ArtifactURI]
	publication.assembly.Aggregate.Video.RootHash = digest('9')
	fixture.backend.publications[job.ArtifactURI] = publication
	fixture.backend.mu.Unlock()
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result); err == nil {
		t.Fatal("aggregate-drifted external publication was accepted")
	}
}

func TestReferenceChunksOneMissingChunkAndEncoderCrashRecovery(t *testing.T) {
	t.Run("one missing chunk", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		job := fixture.job("job-one-missing", "one-missing.mp4")
		plan := mustPlan(t, fixture.service, job)
		store := newReferenceCheckpointStore(t, fixture)
		executor := mustExecutor(t, fixture.service, job, plan)
		output, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := stagedag.NewCheckpoint(plan, plan.Tasks[0], output, 1)
		if err != nil || store.Commit(context.Background(), checkpoint) != nil {
			t.Fatalf("seed checkpoint = %v", err)
		}
		selection, err := fixture.service.SelectMissingChunks(context.Background(), job, []stagedag.Checkpoint{checkpoint})
		if err != nil || len(selection.Ready) != 1 || len(selection.Missing) != 1 || selection.Missing[0].ID != "video-00000" {
			t.Fatalf("missing selection = %+v, %v", selection, err)
		}
		result, err := fixture.engine(t, job, plan, store).Run(context.Background(), plan)
		if err != nil || result.CacheHits != 1 || result.Executed != 2 {
			t.Fatalf("missing-only run = %+v, %v", result, err)
		}
		if fixture.providers.pcmCalls.Load() != 1 || fixture.providers.surfaceCalls.Load() != job.FrameRange.Count {
			t.Fatal("missing-only run recomputed the ready audio chunk")
		}
	})

	t.Run("whole encoder restarts", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		fixture.backend.failBeforePublication.Store(1)
		job := fixture.job("job-encoder-crash", "encoder-crash.mp4")
		plan := mustPlan(t, fixture.service, job)
		store := newReferenceCheckpointStore(t, fixture)
		engine := fixture.engine(t, job, plan, store)
		if _, err := engine.Run(context.Background(), plan); err == nil {
			t.Fatal("encoder crash unexpectedly succeeded")
		}
		if fixture.backend.assembleCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 0 {
			t.Fatalf("first assembler/render calls = %d/%d", fixture.backend.assembleCalls.Load(), fixture.backend.renderCalls.Load())
		}
		surfaceCalls, pcmCalls := fixture.providers.surfaceCalls.Load(), fixture.providers.pcmCalls.Load()
		result, err := engine.Run(context.Background(), plan)
		if err != nil || result.CacheHits != 2 || result.Executed != 1 {
			t.Fatalf("encoder restart = %+v, %v", result, err)
		}
		if fixture.backend.assembleCalls.Load() != 2 || fixture.backend.renderCalls.Load() != 0 || fixture.providers.surfaceCalls.Load() != surfaceCalls || fixture.providers.pcmCalls.Load() != pcmCalls {
			t.Fatal("encoder restart did not reuse all pre-codec chunks or did not restart the complete encoder")
		}
	})
}

func TestReferenceFinalResidualPublicationCannotBypassChunkConsumption(t *testing.T) {
	fixture := newReferenceFixture(t)
	fixture.backend.publishWithoutSource.Store(1)
	fixture.backend.failAfterSource.Store(1)
	job := fixture.job("job-residual-publication", "residual-publication.mp4")
	plan := mustPlan(t, fixture.service, job)
	store := newReferenceCheckpointStore(t, fixture)
	engine := fixture.engine(t, job, plan, store)

	if _, err := engine.Run(context.Background(), plan); err == nil {
		t.Fatal("assembler publication without reading ChunkSource unexpectedly produced a final checkpoint")
	}
	if fixture.backend.assembleCalls.Load() != 1 || fixture.backend.assembledBytes.Load() != 0 {
		t.Fatalf("first assembly calls/bytes = %d/%d", fixture.backend.assembleCalls.Load(), fixture.backend.assembledBytes.Load())
	}
	if _, err := engine.Run(context.Background(), plan); err == nil {
		t.Fatal("assembler error after complete source consumption accepted the stale publication")
	}
	if fixture.backend.assembleCalls.Load() != 2 || fixture.backend.assembledBytes.Load() == 0 {
		t.Fatalf("residual publication skipped restarted assembly: calls=%d", fixture.backend.assembleCalls.Load())
	}
	if fixture.backend.chunkInspectCalls.Load() != 0 || fixture.backend.chunkOpenCalls.Load() != 0 {
		t.Fatal("failed assembly inspected or opened a stale publication")
	}
	reader, openErr := fixture.store.Open(context.Background(), plan.Tasks[len(plan.Tasks)-1].CacheKey, "residual-final-audit")
	if reader != nil || !errors.Is(openErr, cas.ErrMiss) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("failed assembly published final CAS state: reader=%v err=%v", reader, openErr)
	}

	result, err := engine.Run(context.Background(), plan)
	if err != nil || result.CacheHits != len(plan.Tasks)-1 || result.Executed != 1 {
		t.Fatalf("clean restart = %+v, %v", result, err)
	}
	if fixture.backend.assembleCalls.Load() != 3 {
		t.Fatalf("clean restart assembly calls=%d", fixture.backend.assembleCalls.Load())
	}
	wantConsumed := 2 * (result.Checkpoints[0].Output.SizeBytes + result.Checkpoints[1].Output.SizeBytes)
	if fixture.backend.assembledBytes.Load() != wantConsumed {
		t.Fatalf("two complete attempts consumed %d chunk bytes, want %d", fixture.backend.assembledBytes.Load(), wantConsumed)
	}
}

func TestReferenceChunkSourceHeartbeatsLongReadsAndFailsZeroOnLeaseLoss(t *testing.T) {
	t.Run("long read survives short TTL and GC", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		store, err := cas.New(filepath.Join(fixture.root, "heartbeat-cas"), cas.Options{LeaseTTL: cas.MinLeaseTTL})
		if err != nil {
			t.Fatal(err)
		}
		fixture.store, fixture.service.cas = store, store
		job := fixture.job("job-heartbeat-gc", "heartbeat-gc.mp4")
		plan := mustPlan(t, fixture.service, job)
		executor := mustExecutor(t, fixture.service, job, plan)
		output, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := chunkplan.DecodeReceipt(output.Receipt)
		if err != nil {
			t.Fatal(err)
		}
		descriptor := descriptorFor(plan.Tasks[0], receipt)
		stageContext := context.Background()
		source := newReferenceChunkSource(stageContext, fixture.service, []ChunkDescriptor{descriptor})
		childContext, cancelChild := context.WithCancel(context.Background())
		reader, err := source.Open(childContext, descriptor)
		if err != nil {
			t.Fatal(err)
		}
		cancelChild()
		var first [1]byte
		if count, err := reader.Read(first[:]); count != 1 || err != nil {
			t.Fatalf("first read = %d, %v", count, err)
		}
		// Holding the wrapper's read lock models one slow/blocking underlying
		// file Read. Heartbeat must not need that lock.
		verified := reader.(*verifiedChunkReader)
		verified.mu.Lock()
		time.Sleep(cas.MinLeaseTTL + 2*referenceChunkHeartbeatInterval)
		report, err := store.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
		verified.mu.Unlock()
		if !errors.Is(err, cas.ErrQuotaUnsatisfied) || len(report.SkippedActive) != 1 || len(report.DeletedDigests) != 0 {
			t.Fatalf("leased GC report=%+v err=%v", report, err)
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if err := source.VerifyComplete(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("heartbeat failure invalidates final", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		var clockNanos atomic.Int64
		clockNanos.Store(time.Now().UnixNano())
		clock := cas.ClockFunc(func() time.Time { return time.Unix(0, clockNanos.Load()) })
		store, err := cas.New(filepath.Join(fixture.root, "heartbeat-loss-cas"), cas.Options{
			Clock: clock, LeaseTTL: cas.MinLeaseTTL,
		})
		if err != nil {
			t.Fatal(err)
		}
		fixture.store, fixture.service.cas = store, store
		fixture.backend.pauseChunkRead.Store(true)
		job := fixture.job("job-heartbeat-loss", "heartbeat-loss.mp4")
		plan := mustPlan(t, fixture.service, job)
		executor := mustExecutor(t, fixture.service, job, plan)
		dependencies := make([]stagedag.Dependency, 0, len(plan.Tasks)-1)
		for _, task := range plan.Tasks[:len(plan.Tasks)-1] {
			output, err := executor.Execute(context.Background(), task, nil)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := stagedag.NewCheckpoint(plan, task, output, 1)
			if err != nil {
				t.Fatal(err)
			}
			dependencies = append(dependencies, stagedag.Dependency{TaskID: task.ID, Checkpoint: checkpoint})
		}
		type finalResult struct {
			output stagedag.StageOutput
			err    error
		}
		finished := make(chan finalResult, 1)
		go func() {
			output, err := executor.Execute(context.Background(), plan.Tasks[len(plan.Tasks)-1], dependencies)
			finished <- finalResult{output: output, err: err}
		}()
		select {
		case <-fixture.backend.chunkReadPaused:
		case <-time.After(5 * time.Second):
			t.Fatal("assembler did not pause with an open chunk lease")
		}
		clockNanos.Add((2 * cas.MinLeaseTTL).Nanoseconds())
		time.Sleep(3 * referenceChunkHeartbeatInterval)
		close(fixture.backend.resumeChunkRead)
		select {
		case result := <-finished:
			if result.err == nil || result.output.ContentHash != "" || result.output.SizeBytes != 0 || result.output.Receipt != nil {
				t.Fatalf("lease-loss final output=%+v err=%v", result.output, result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("lease-loss final did not stop")
		}
		reader, openErr := store.Open(context.Background(), plan.Tasks[len(plan.Tasks)-1].CacheKey, "heartbeat-loss-audit")
		if reader != nil || !errors.Is(openErr, cas.ErrMiss) {
			if reader != nil {
				_ = reader.Close()
			}
			t.Fatalf("lease-loss final became visible in CAS: reader=%v err=%v", reader, openErr)
		}
	})
}

func TestReferenceChunksCorruptCASIsRecomputedAndCancellationResumes(t *testing.T) {
	t.Run("corrupt CAS", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		job := fixture.job("job-corrupt-chunk", "corrupt-chunk.mp4")
		plan := mustPlan(t, fixture.service, job)
		store := newReferenceCheckpointStore(t, fixture)
		engine := fixture.engine(t, job, plan, store)
		first, err := engine.Run(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		corruptPayloadByHash(t, filepath.Join(fixture.root, "cas"), first.Checkpoints[1].Output.ContentHash)
		before := fixture.providers.surfaceCalls.Load()
		restarted, err := engine.Run(context.Background(), plan)
		if err != nil || restarted.Executed != 1 || restarted.CacheHits != 2 {
			t.Fatalf("corrupt restart = %+v, %v", restarted, err)
		}
		if fixture.providers.surfaceCalls.Load() != before+job.FrameRange.Count || fixture.providers.pcmCalls.Load() != 1 || fixture.backend.assembleCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 0 {
			t.Fatal("corrupt CAS did not isolate recomputation to the damaged video chunk")
		}
		if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, restarted); err != nil {
			t.Fatalf("artifact after repair: %v", err)
		}
	})

	t.Run("cancel and restart", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		fixture.providers.blockSurface.Store(true)
		job := fixture.job("job-cancel-chunk", "cancel-chunk.mp4")
		plan := mustPlan(t, fixture.service, job)
		store := newReferenceCheckpointStore(t, fixture)
		engine := fixture.engine(t, job, plan, store)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			_, err := engine.Run(ctx, plan)
			finished <- err
		}()
		select {
		case <-fixture.providers.surfaceStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("surface provider did not start")
		}
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled chunk did not stop")
		}
		fixture.providers.blockSurface.Store(false)
		restarted, err := engine.Run(context.Background(), plan)
		if err != nil || restarted.CacheHits != 1 || restarted.Executed != 2 {
			t.Fatalf("cancel restart = %+v, %v", restarted, err)
		}
		if fixture.providers.pcmCalls.Load() != 1 {
			t.Fatal("cancel restart recomputed committed audio")
		}
	})
}

func TestReferenceChunksFailClosedAndEnforceResourceAdmission(t *testing.T) {
	base := newServiceFixture(t, nil)
	scratch := filepath.Join(base.root, "reference-scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	config := ReferenceChunkConfig{
		ProviderExecutableDigest: digest('7'), ScratchDirectory: scratch,
		VideoResources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1},
		AudioResources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1},
	}
	if _, err := New(Config{
		Environments: base.service.environments, Backends: base.service.backends, CAS: base.store,
		MemoryBytes: 64 << 10, DiskBytes: 1 << 20, ReferenceChunks: &config,
	}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing providers error = %v", err)
	}

	t.Run("provider identity is manifest-bound", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		fixture.service.reference.executableDigest = digest('7')
		if _, err := fixture.service.ResolveStagePlan(context.Background(), fixture.job("job-provider-drift", "provider-drift.mp4")); !errors.Is(err, ErrEnvironment) {
			t.Fatalf("provider identity drift error = %v", err)
		}
	})

	t.Run("final validation memory is admitted at plan time", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		fixture.service.memoryBytes = 1
		if _, err := fixture.service.ResolveStagePlan(context.Background(), fixture.job("job-final-budget", "final-budget.mp4")); !errors.Is(err, ErrPlan) {
			t.Fatalf("final validation budget error = %v", err)
		}
		if _, ok := referenceFinalValidationMemory(ReferenceChunkMaxTasks + 1); ok {
			t.Fatal("over-limit final dependency count produced a memory budget")
		}
	})

	t.Run("host-owned assembler is mandatory", func(t *testing.T) {
		fixture := newReferenceFixture(t)
		fixture.service.assembler = nil
		job := fixture.job("job-no-assembler", "no-assembler.mp4")
		plan := mustPlan(t, fixture.service, job)
		if _, err := fixture.service.ResolveExecutor(context.Background(), job, plan); !errors.Is(err, ErrBackend) {
			t.Fatalf("missing assembler error = %v", err)
		}
	})

	fixture := newReferenceFixture(t)
	fixture.service.reference.videoResources.MemoryBytes = 1
	fixture.service.reference.videoResources.DiskBytes = 1
	job := fixture.job("job-resource-gate", "resource-gate.mp4")
	plan := mustPlan(t, fixture.service, job)
	executor := mustExecutor(t, fixture.service, job, plan)
	if _, err := executor.Execute(context.Background(), plan.Tasks[1], nil); !errors.Is(err, ErrExecution) {
		t.Fatalf("resource gate error = %v", err)
	}
	if fixture.providers.surfaceCalls.Load() != 0 {
		t.Fatal("resource gate invoked provider before rejecting the task")
	}
}

func TestReferenceScratchRejectsSymlinkedAndUnsafePaths(t *testing.T) {
	resolvedRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("symlinked ancestor", func(t *testing.T) {
		realParent := filepath.Join(resolvedRoot, "real-parent")
		realScratch := filepath.Join(realParent, "scratch")
		if err := os.MkdirAll(realScratch, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(resolvedRoot, "linked-parent")
		if err := os.Symlink(realParent, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if err := validateReferenceScratchDirectory(filepath.Join(link, "scratch")); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("symlinked ancestor error = %v", err)
		}
	})

	if runtime.GOOS == "windows" {
		return
	}
	t.Run("public leaf", func(t *testing.T) {
		path := filepath.Join(resolvedRoot, "public-leaf")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := validateReferenceScratchDirectory(path); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("public leaf error = %v", err)
		}
	})

	t.Run("writable non-sticky ancestor", func(t *testing.T) {
		parent := filepath.Join(resolvedRoot, "unsafe-parent")
		path := filepath.Join(parent, "scratch")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(parent, 0o700) // allow t.TempDir cleanup on restrictive platforms
		if err := validateReferenceScratchDirectory(path); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("unsafe ancestor error = %v", err)
		}
	})
}

func TestReferenceChunkExecutionRevalidatesScratchDrift(t *testing.T) {
	tests := []struct {
		name  string
		drift func(*testing.T, string) func()
	}{
		{
			name: "leaf permission drift",
			drift: func(t *testing.T, path string) func() {
				if runtime.GOOS == "windows" {
					t.Skip("Unix permission contract does not apply on Windows")
				}
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return func() { _ = os.Chmod(path, 0o700) }
			},
		},
		{
			name: "directory replacement drift",
			drift: func(t *testing.T, path string) func() {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("not-a-directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {
					_ = os.Remove(path)
					_ = os.Mkdir(path, 0o700)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReferenceFixture(t)
			job := fixture.job("job-scratch-drift", "scratch-drift.mp4")
			plan := mustPlan(t, fixture.service, job)
			executor := mustExecutor(t, fixture.service, job, plan)
			restore := test.drift(t, fixture.service.reference.scratchDirectory)
			defer restore()
			output, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
			if !errors.Is(err, ErrExecution) || output.ContentHash != "" || output.SizeBytes != 0 || output.Receipt != nil {
				t.Fatalf("scratch drift output=%+v err=%v", output, err)
			}
			if fixture.providers.pcmCalls.Load() != 0 || fixture.providers.surfaceCalls.Load() != 0 {
				t.Fatal("scratch drift reached a media provider")
			}
			reader, openErr := fixture.store.Open(context.Background(), plan.Tasks[0].CacheKey, "scratch-drift-audit")
			if reader != nil || !errors.Is(openErr, cas.ErrMiss) {
				if reader != nil {
					_ = reader.Close()
				}
				t.Fatalf("scratch drift published chunk CAS state: reader=%v err=%v", reader, openErr)
			}
		})
	}
}

var errWorkerCrash = errors.New("reference worker crashed")

type referenceProviders struct {
	surfaceCalls   atomic.Int64
	pcmCalls       atomic.Int64
	failSurface    atomic.Int32
	blockSurface   atomic.Bool
	surfaceStarted chan struct{}
	startedOnce    sync.Once
}

func newReferenceProviders() *referenceProviders {
	return &referenceProviders{surfaceStarted: make(chan struct{})}
}

func (providers *referenceProviders) RenderSurface(ctx context.Context, request ReferenceChunkRequest, _ int64) (*cpucompositor.Surface, error) {
	providers.surfaceCalls.Add(1)
	if providers.failSurface.CompareAndSwap(1, 0) {
		return nil, errWorkerCrash
	}
	if providers.blockSurface.Load() {
		providers.startedOnce.Do(func() { close(providers.surfaceStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return cpucompositor.NewSurface(request.Target.Width, request.Target.Height)
}

func (providers *referenceProviders) RenderPCM(ctx context.Context, request ReferenceChunkRequest, sampleRange videoedit.TickRange) (*pcm16le.Block, error) {
	providers.pcmCalls.Add(1)
	samples := make([]audiograph.StereoSample, sampleRange.Count)
	for index := range samples {
		samples[index] = audiograph.StereoSample{Left: audiograph.Zero(), Right: audiograph.Zero()}
	}
	result, err := pcm16le.Quantize(ctx, request.Target.SampleClock.SamplesPerSecond, sampleRange.First, samples, pcm16le.DefaultLimits())
	if err != nil {
		return nil, err
	}
	return result.PCM, nil
}

type referenceFixture struct {
	serviceFixture
	providers *referenceProviders
}

type countingContentStore struct {
	contentStore
	openCalls atomic.Int32
}

func (store *countingContentStore) Open(ctx context.Context, key cachekey.Key, owner string) (*cas.Reader, error) {
	store.openCalls.Add(1)
	return store.contentStore.Open(ctx, key, owner)
}

func newReferenceFixture(t *testing.T) referenceFixture {
	t.Helper()
	base := newServiceFixture(t, nil)
	providers := newReferenceProviders()
	resolvedRoot, err := filepath.EvalSymlinks(base.root)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(resolvedRoot, "reference-scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		Environments: base.service.environments, ReferenceAssembler: base.backend, CAS: base.store,
		MemoryBytes: 8 << 20, DiskBytes: 1 << 20,
		ReferenceChunks: &ReferenceChunkConfig{
			Surfaces: providers, PCM: providers, ProviderExecutableDigest: digest('b'), ScratchDirectory: scratch,
			VideoResources: stagedag.ResourceEstimate{MemoryBytes: 2 << 20, DiskBytes: 64 << 20},
			AudioResources: stagedag.ResourceEstimate{MemoryBytes: 1 << 20, DiskBytes: 2 << 20},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	base.service = service
	return referenceFixture{serviceFixture: base, providers: providers}
}

func (fixture referenceFixture) engine(t *testing.T, job renderapi.RenderJob, plan stagedag.Plan, store stagedag.CheckpointStore) stagedag.Engine {
	t.Helper()
	return stagedag.Engine{
		Store: store, Executor: mustExecutor(t, fixture.service, job, plan),
		Limits: stagedag.Limits{MaxConcurrent: 1, MaxMemoryBytes: 8 << 20, MaxDiskBytes: 64 << 20},
	}
}

func newReferenceCheckpointStore(t *testing.T, fixture referenceFixture) *checkpointlocal.Store {
	t.Helper()
	store, err := checkpointlocal.New(filepath.Join(fixture.root, "checkpoints"), fixture.store, checkpointlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertReferencePlan(t *testing.T, plan stagedag.Plan) {
	t.Helper()
	if len(plan.Tasks) != 3 || plan.Tasks[0].Profile != chunkplan.AudioStageProfileV1 ||
		plan.Tasks[1].Profile != chunkplan.VideoStageProfileV1 ||
		plan.Tasks[2].Profile != ReferenceChunkPublishStageProfileV1 ||
		plan.Tasks[0].Retry.MaxAttempts != 2 || !plan.Tasks[0].Retry.RetryTransient || plan.Tasks[0].Retry.RetryResource ||
		plan.Tasks[1].Retry.MaxAttempts != 2 || !plan.Tasks[1].Retry.RetryTransient || plan.Tasks[1].Retry.RetryResource ||
		len(plan.Tasks[2].DependsOn) != 2 || plan.Tasks[2].Retry.MaxAttempts != 1 ||
		plan.Tasks[2].Retry.RetryTransient || plan.Tasks[2].Retry.RetryResource {
		t.Fatalf("reference plan = %+v", plan)
	}
}

func corruptPayloadByHash(t *testing.T, root, wantedHash string) {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != "payload" {
			return walkErr
		}
		value, err := os.ReadFile(path)
		if err != nil || hashBytes(value) != wantedHash {
			return err
		}
		if len(value) == 0 {
			return errors.New("test payload is empty")
		}
		value[0] ^= 0xff
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(path, value, 0o600); err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("CAS payload %s not found", wantedHash)
	}
}
