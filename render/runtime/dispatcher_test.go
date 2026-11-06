package renderruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestDispatcherCompletesQueuedJobAndReportsNoMoreWork(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-complete")
	plan := testPlan(t, job)
	var executions atomic.Int32
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return plan, nil }),
		stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
			executions.Add(1)
			return testOutput(t), nil
		}),
		ArtifactResolverFunc(func(_ context.Context, actual renderapi.RenderJob, _ stagedag.Plan, result stagedag.RunResult) (renderapi.RenderArtifact, error) {
			if result.Executed != 1 || result.CacheHits != 0 {
				t.Fatalf("DAG result = %+v", result)
			}
			return testArtifact(actual), nil
		}), 1)
	if _, created, err := dispatcher.Submit(context.Background(), job); err != nil || !created {
		t.Fatalf("submit created=%v err=%v", created, err)
	}
	run, found, err := dispatcher.RunNext(context.Background())
	if err != nil || !found || run.Resumed || run.Snapshot.State != renderapi.JobComplete || run.DAG.Executed != 1 {
		t.Fatalf("run found=%v err=%v value=%+v", found, err, run)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions = %d", executions.Load())
	}
	if _, found, err := dispatcher.RunNext(context.Background()); err != nil || found {
		t.Fatalf("empty run found=%v err=%v", found, err)
	}
}

func TestDispatcherResumesJournaledRunningJob(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-resume")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if snapshot, found, err := coordinator.StartNext(context.Background()); err != nil || !found || snapshot.State != renderapi.JobRunning {
		t.Fatalf("start found=%v snapshot=%+v err=%v", found, snapshot, err)
	}
	dispatcher := successfulDispatcher(t, coordinator, job, 1)
	run, found, err := dispatcher.RunNext(context.Background())
	if err != nil || !found || !run.Resumed || run.Snapshot.State != renderapi.JobComplete {
		t.Fatalf("resume found=%v err=%v run=%+v", found, err, run)
	}
}

func TestDispatcherFreezesResolvedPlanBeforeExecution(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-freeze-plan")
	shared := testPlan(t, job)
	executorStarted := make(chan struct{})
	releaseExecutor := make(chan struct{})
	resolverPlanID := make(chan string, 1)
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return shared, nil }),
		stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
			close(executorStarted)
			<-releaseExecutor
			return testOutput(t), nil
		}),
		ArtifactResolverFunc(func(_ context.Context, actual renderapi.RenderJob, plan stagedag.Plan, _ stagedag.RunResult) (renderapi.RenderArtifact, error) {
			resolverPlanID <- plan.Tasks[0].ID
			return testArtifact(actual), nil
		}), 1)
	if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		run   Run
		found bool
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		run, found, err := dispatcher.RunNext(context.Background())
		finished <- outcome{run: run, found: found, err: err}
	}()
	awaitSignal(t, executorStarted)
	shared.Tasks[0].ID = "mutated-after-resolution"
	close(releaseExecutor)
	result := awaitOutcome(t, finished)
	if result.err != nil || !result.found || result.run.Snapshot.State != renderapi.JobComplete {
		t.Fatalf("run = %+v", result)
	}
	select {
	case taskID := <-resolverPlanID:
		if taskID != "publish" {
			t.Fatalf("artifact resolver observed mutable plan task %q", taskID)
		}
	default:
		t.Fatal("artifact resolver did not receive plan")
	}
}

func TestDispatcherCancellationInterruptsActiveStage(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-cancel-stage")
	started := make(chan struct{})
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return testPlan(t, job), nil }),
		stagedag.ExecutorFunc(func(ctx context.Context, _ stagedag.Task, _ []stagedag.Dependency) (stagedag.StageOutput, error) {
			close(started)
			<-ctx.Done()
			return stagedag.StageOutput{}, ctx.Err()
		}),
		ArtifactResolverFunc(func(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error) {
			t.Fatal("artifact resolver called after canceled stage")
			return renderapi.RenderArtifact{}, nil
		}), 1)
	if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		run   Run
		found bool
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		run, found, err := dispatcher.RunNext(context.Background())
		finished <- outcome{run: run, found: found, err: err}
	}()
	awaitSignal(t, started)
	if snapshot, changed, err := dispatcher.RequestCancel(context.Background(), job.ID); err != nil || !changed || snapshot.State != renderapi.JobCanceling {
		t.Fatalf("cancel changed=%v snapshot=%+v err=%v", changed, snapshot, err)
	}
	result := awaitOutcome(t, finished)
	if result.err != nil || !result.found || result.run.Snapshot.State != renderapi.JobCanceled {
		t.Fatalf("result = %+v", result)
	}
}

func TestDispatcherCancellationInterruptsArtifactResolution(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-cancel-artifact")
	resolverStarted := make(chan struct{})
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return testPlan(t, job), nil }),
		stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
			return testOutput(t), nil
		}),
		ArtifactResolverFunc(func(ctx context.Context, _ renderapi.RenderJob, _ stagedag.Plan, _ stagedag.RunResult) (renderapi.RenderArtifact, error) {
			close(resolverStarted)
			<-ctx.Done()
			return renderapi.RenderArtifact{}, ctx.Err()
		}), 1)
	if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		run   Run
		found bool
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		run, found, err := dispatcher.RunNext(context.Background())
		finished <- outcome{run: run, found: found, err: err}
	}()
	awaitSignal(t, resolverStarted)
	cancelDone := make(chan error, 1)
	go func() {
		snapshot, changed, err := dispatcher.RequestCancel(context.Background(), job.ID)
		if err == nil && (!changed || snapshot.State != renderapi.JobCanceling) {
			err = errors.New("cancel did not transition active artifact resolution")
		}
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel blocked behind artifact resolver")
	}
	result := awaitOutcome(t, finished)
	if result.err != nil || !result.found || result.run.Snapshot.State != renderapi.JobCanceled {
		t.Fatalf("result = %+v", result)
	}
}

func TestDispatcherClosesJournaledQueuedCancellationWithoutExecution(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-cancel-queued")
	var planCalls atomic.Int32
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) {
			planCalls.Add(1)
			return stagedag.Plan{}, errors.New("must not resolve")
		}),
		stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
			t.Fatal("must not execute")
			return stagedag.StageOutput{}, nil
		}),
		ArtifactResolverFunc(func(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error) {
			t.Fatal("must not resolve artifact")
			return renderapi.RenderArtifact{}, nil
		}), 1)
	if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := dispatcher.RequestCancel(context.Background(), job.ID); err != nil || !changed {
		t.Fatalf("cancel changed=%v err=%v", changed, err)
	}
	run, found, err := dispatcher.RunNext(context.Background())
	if err != nil || !found || !run.Resumed || run.Snapshot.State != renderapi.JobCanceled || planCalls.Load() != 0 {
		t.Fatalf("close cancellation found=%v err=%v run=%+v calls=%d", found, err, run, planCalls.Load())
	}
}

func TestDispatcherPersistsFailureClassesAndArtifactIntegrityFailure(t *testing.T) {
	tests := []struct {
		name      string
		executor  stagedag.Executor
		resolver  ArtifactResolver
		wantClass renderapi.FailureClass
	}{
		{
			name: "stage resource",
			executor: stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
				return stagedag.StageOutput{}, &stagedag.StageError{Class: renderapi.FailureResource, Err: errors.New("memory exhausted")}
			}),
			resolver: ArtifactResolverFunc(func(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error) {
				return renderapi.RenderArtifact{}, errors.New("must not resolve")
			}),
			wantClass: renderapi.FailureResource,
		},
		{
			name: "invalid artifact",
			executor: stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
				return testOutput(t), nil
			}),
			resolver: ArtifactResolverFunc(func(_ context.Context, job renderapi.RenderJob, _ stagedag.Plan, _ stagedag.RunResult) (renderapi.RenderArtifact, error) {
				artifact := testArtifact(job)
				artifact.ContentHash = ""
				return artifact, nil
			}),
			wantClass: renderapi.FailureIntegrity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := testCoordinator(t)
			job := testJob("job-" + strings.ReplaceAll(test.name, " ", "-"))
			dispatcher := testDispatcher(t, coordinator,
				PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return testPlan(t, job), nil }),
				test.executor, test.resolver, 1)
			if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			run, found, err := dispatcher.RunNext(context.Background())
			if err != nil || !found || run.Snapshot.State != renderapi.JobFailed || run.Snapshot.FailureClass != test.wantClass {
				t.Fatalf("failure found=%v err=%v run=%+v", found, err, run)
			}
		})
	}
}

func TestDispatcherRejectsPlanJobBindingDrift(t *testing.T) {
	coordinator := testCoordinator(t)
	job := testJob("job-plan-drift")
	plan := testPlan(t, job)
	plan.JobID = "different-job"
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return plan, nil }),
		stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
			t.Fatal("invalid plan executed")
			return stagedag.StageOutput{}, nil
		}),
		ArtifactResolverFunc(func(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error) {
			return renderapi.RenderArtifact{}, errors.New("must not resolve")
		}), 1)
	if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	run, found, err := dispatcher.RunNext(context.Background())
	if err != nil || !found || run.Snapshot.State != renderapi.JobFailed || run.Snapshot.FailureClass != renderapi.FailureInputRepair ||
		!strings.Contains(run.Snapshot.FailureMessage, ErrPlanBinding.Error()) {
		t.Fatalf("binding failure found=%v err=%v run=%+v", found, err, run)
	}
}

func TestDispatcherCapacityAndHostShutdownRemainResumable(t *testing.T) {
	coordinator := testCoordinator(t)
	first := testJob("job-capacity-a")
	second := testJob("job-capacity-b")
	started := make(chan struct{})
	dispatcher := testDispatcher(t, coordinator,
		PlanSourceFunc(func(_ context.Context, job renderapi.RenderJob) (stagedag.Plan, error) { return testPlan(t, job), nil }),
		stagedag.ExecutorFunc(func(ctx context.Context, _ stagedag.Task, _ []stagedag.Dependency) (stagedag.StageOutput, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
			return stagedag.StageOutput{}, ctx.Err()
		}),
		ArtifactResolverFunc(func(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error) {
			return renderapi.RenderArtifact{}, errors.New("must not resolve")
		}), 1)
	for _, job := range []renderapi.RenderJob{first, second} {
		if _, _, err := dispatcher.Submit(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	hostCtx, stopHost := context.WithCancel(context.Background())
	type outcome struct {
		run   Run
		found bool
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		run, found, err := dispatcher.RunNext(hostCtx)
		finished <- outcome{run: run, found: found, err: err}
	}()
	awaitSignal(t, started)
	if _, found, err := dispatcher.RunNext(context.Background()); !errors.Is(err, ErrCapacity) || found {
		t.Fatalf("capacity found=%v err=%v", found, err)
	}
	stopHost()
	result := awaitOutcome(t, finished)
	if !errors.Is(result.err, context.Canceled) || !result.found || result.run.Snapshot.State != renderapi.JobRunning {
		t.Fatalf("host shutdown result = %+v", result)
	}
	snapshot, err := dispatcher.Snapshot(first.ID)
	if err != nil || snapshot.State != renderapi.JobRunning {
		t.Fatalf("resumable snapshot=%+v err=%v", snapshot, err)
	}
}

func TestFailureMessageRemainsSchedulerValidAfterBounding(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		want  string
		bound bool
	}{
		{name: "nil", want: "render job failed"},
		{name: "whitespace", err: errors.New(" \t\n "), want: "render job failed"},
		{name: "trim before truncate", err: errors.New(strings.Repeat(" ", maxFailureMessageBytes) + "x"), want: "x"},
		{name: "invalid UTF-8", err: errors.New("x\xffy"), want: "x\uFFFDy"},
		{name: "bounded", err: errors.New(strings.Repeat("x", maxFailureMessageBytes+1024)), bound: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := failureMessage(test.err)
			if !utf8.ValidString(message) || strings.TrimSpace(message) == "" || len(message) > maxFailureMessageBytes {
				t.Fatalf("invalid bounded message bytes=%d value=%q", len(message), message)
			}
			if test.bound {
				if len(message) != maxFailureMessageBytes {
					t.Fatalf("bounded bytes=%d", len(message))
				}
			} else if message != test.want {
				t.Fatalf("message=%q want=%q", message, test.want)
			}
		})
	}
}

type memorySchedulerStore struct {
	mu     sync.Mutex
	events []scheduler.JobEvent
}

func (store *memorySchedulerStore) Append(_ context.Context, event scheduler.JobEvent) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.events = append(store.events, event)
	return nil
}

func (store *memorySchedulerStore) Replay(context.Context) ([]scheduler.JobEvent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]scheduler.JobEvent(nil), store.events...), nil
}

type memoryCheckpointStore struct {
	mu     sync.Mutex
	values map[string]stagedag.Checkpoint
}

func (store *memoryCheckpointStore) Load(_ context.Context, scope stagedag.Scope, task stagedag.Task) (stagedag.Checkpoint, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	checkpoint, found := store.values[scope.JobID+"\x00"+task.ID]
	return cloneTestCheckpoint(checkpoint), found, nil
}

func (store *memoryCheckpointStore) Commit(_ context.Context, checkpoint stagedag.Checkpoint) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[checkpoint.JobID+"\x00"+checkpoint.TaskID] = cloneTestCheckpoint(checkpoint)
	return nil
}

func cloneTestCheckpoint(value stagedag.Checkpoint) stagedag.Checkpoint {
	value.Output.Receipt = append([]byte(nil), value.Output.Receipt...)
	if value.FrameRange != nil {
		copyValue := *value.FrameRange
		value.FrameRange = &copyValue
	}
	if value.SampleRange != nil {
		copyValue := *value.SampleRange
		value.SampleRange = &copyValue
	}
	return value
}

func testCoordinator(t *testing.T) *scheduler.Coordinator {
	t.Helper()
	coordinator, err := scheduler.Open(context.Background(), &memorySchedulerStore{})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func testDispatcher(t *testing.T, coordinator *scheduler.Coordinator, plans PlanSource, executor stagedag.Executor, artifacts ArtifactResolver, maximum int) *Dispatcher {
	t.Helper()
	dispatcher, err := New(Config{
		Coordinator: coordinator, Plans: plans, Checkpoints: &memoryCheckpointStore{values: make(map[string]stagedag.Checkpoint)},
		Executor: executor, Artifacts: artifacts, MaxConcurrentJobs: maximum,
		StageLimits: stagedag.Limits{MaxConcurrent: 1, MaxMemoryBytes: 8, MaxDiskBytes: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func successfulDispatcher(t *testing.T, coordinator *scheduler.Coordinator, job renderapi.RenderJob, maximum int) *Dispatcher {
	t.Helper()
	return testDispatcher(t, coordinator,
		PlanSourceFunc(func(context.Context, renderapi.RenderJob) (stagedag.Plan, error) { return testPlan(t, job), nil }),
		stagedag.ExecutorFunc(func(context.Context, stagedag.Task, []stagedag.Dependency) (stagedag.StageOutput, error) {
			return testOutput(t), nil
		}),
		ArtifactResolverFunc(func(_ context.Context, actual renderapi.RenderJob, _ stagedag.Plan, _ stagedag.RunResult) (renderapi.RenderArtifact, error) {
			return testArtifact(actual), nil
		}), maximum)
}

func testJob(id string) renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion,
		ID:            id, IdempotencyKey: "idempotency-" + id,
		PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		TimelineRange: videoedit.TimeRange{
			Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 1, Rate: 1},
		},
		FrameRange: videoedit.TickRange{First: 0, Count: 30}, SampleRange: videoedit.TickRange{First: 0, Count: 48_000},
		ArtifactURI: "file:///controlled/" + id + ".mp4",
	}
}

func testPlan(t *testing.T, job renderapi.RenderJob) stagedag.Plan {
	t.Helper()
	stageInput, err := stagedag.StageBindingInput(renderapi.StagePublish)
	if err != nil {
		t.Fatal(err)
	}
	frameRange := job.FrameRange
	sampleRange := job.SampleRange
	input := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: "cutly.cache.runtime.v1", ArtifactKind: "cutly.artifact.runtime.v1",
		PlanFingerprint: job.PlanFingerprint, ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		StageProfile: "cutly.stage.publish.v1", Inputs: []cachekey.NormalizedInput{stageInput},
		FrameRange:    &cachekey.IndexRange{Start: frameRange.First, Count: frameRange.Count},
		SampleRange:   &cachekey.IndexRange{Start: sampleRange.First, Count: sampleRange.Count},
		OutputProfile: "cutly.output.runtime.v1",
	}
	key, err := cachekey.New(input)
	if err != nil {
		t.Fatal(err)
	}
	plan := stagedag.Plan{
		Profile: stagedag.PlanProfileV1, JobID: job.ID, PlanFingerprint: job.PlanFingerprint,
		ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		Tasks: []stagedag.Task{{
			ID: "publish", Stage: renderapi.StagePublish, Profile: input.StageProfile,
			DependsOn: []string{}, CacheInput: input, CacheKey: key,
			FrameRange: &frameRange, SampleRange: &sampleRange,
			Resources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1}, Retry: stagedag.RetryPolicy{MaxAttempts: 1},
		}},
	}
	if err := stagedag.ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func testOutput(t *testing.T) stagedag.StageOutput {
	t.Helper()
	receipt, err := canonicaljson.Marshal(map[string]any{"profile": "cutly.runtime-test-output.v1"})
	if err != nil {
		t.Fatal(err)
	}
	return stagedag.StageOutput{ContentHash: digest('c'), SizeBytes: 42, Receipt: receipt}
}

func testArtifact(job renderapi.RenderJob) renderapi.RenderArtifact {
	return renderapi.RenderArtifact{
		URI: job.ArtifactURI, ContentHash: digest('d'), SizeBytes: 1024,
		ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		EvidenceURI:                  job.ArtifactURI + ".evidence.json", EvidenceFingerprint: digest('e'),
		Duration: job.TimelineRange.Duration, FrameCount: job.FrameRange.Count, SampleCount: job.SampleRange.Count,
	}
}

func digest(character byte) string { return "sha256:" + strings.Repeat(string(character), 64) }

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test signal")
	}
}

func awaitOutcome[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dispatcher")
		var zero T
		return zero
	}
}
