package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestCoordinatorSubmitIsDurableAndIdempotent(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-1", "request-1")
	first, created, err := coordinator.Submit(context.Background(), job)
	if err != nil || !created || first.State != renderapi.JobQueued || first.LastSequence != 1 {
		t.Fatalf("first submit = %+v created=%v err=%v", first, created, err)
	}
	second, created, err := coordinator.Submit(context.Background(), job)
	if err != nil || created || second != first {
		t.Fatalf("replay submit = %+v created=%v err=%v", second, created, err)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %d", len(store.events))
	}

	changed := job
	changed.PlanFingerprint = schedulerDigest('c')
	if _, _, err := coordinator.Submit(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	changed = job
	changed.IdempotencyKey = "request-2"
	if _, _, err := coordinator.Submit(context.Background(), changed); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("job conflict = %v", err)
	}
}

func TestCoordinatorConcurrentDuplicateSubmitAppendsOnce(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-submit-race", "request-submit-race")

	const workers = 32
	type result struct {
		snapshot JobSnapshot
		created  bool
		err      error
	}
	results := make(chan result, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, created, submitErr := coordinator.Submit(context.Background(), job)
			results <- result{snapshot: snapshot, created: created, err: submitErr}
		}()
	}
	wait.Wait()
	close(results)

	createdCount := 0
	for submitResult := range results {
		if submitResult.err != nil || submitResult.snapshot.State != renderapi.JobQueued || submitResult.snapshot.LastSequence != 1 {
			t.Fatalf("concurrent submit = %+v", submitResult)
		}
		if submitResult.created {
			createdCount++
		}
	}
	if createdCount != 1 || len(store.events) != 1 {
		t.Fatalf("concurrent submit created=%d events=%d", createdCount, len(store.events))
	}
}

func TestCoordinatorStartNextClaimsQueuedJobsOnceInDeterministicOrder(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"job-c", "job-a", "job-b"} {
		if _, _, err := coordinator.Submit(context.Background(), schedulerTestJob(id, "request-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	for index, want := range []string{"job-a", "job-b", "job-c"} {
		claimed, found, err := coordinator.StartNext(context.Background())
		if err != nil || !found || claimed.Job.ID != want || claimed.State != renderapi.JobRunning || claimed.LastSequence != 2 {
			t.Fatalf("claim %d = %+v found=%v err=%v", index, claimed, found, err)
		}
	}
	if claimed, found, err := coordinator.StartNext(context.Background()); err != nil || found || claimed != (JobSnapshot{}) {
		t.Fatalf("empty claim = %+v found=%v err=%v", claimed, found, err)
	}

	running, err := coordinator.Snapshots(renderapi.JobRunning)
	if err != nil || len(running) != 3 {
		t.Fatalf("running snapshots = %+v err=%v", running, err)
	}
	for index, want := range []string{"job-a", "job-b", "job-c"} {
		if running[index].Job.ID != want {
			t.Fatalf("snapshot order[%d] = %q, want %q", index, running[index].Job.ID, want)
		}
	}
	if _, err := coordinator.Snapshots(renderapi.JobState("hostile")); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("invalid state filter = %v", err)
	}
	if len(store.events) != 6 {
		t.Fatalf("events = %d", len(store.events))
	}
}

func TestCoordinatorFirstSnapshotFiltersAndExcludesDeterministically(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"job-c", "job-a", "job-b"} {
		if _, _, err := coordinator.Submit(context.Background(), schedulerTestJob(id, "key-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	first, found, err := coordinator.FirstSnapshot(renderapi.JobQueued)
	if err != nil || !found || first.Job.ID != "job-a" {
		t.Fatalf("first queued found=%v snapshot=%+v err=%v", found, first, err)
	}
	second, found, err := coordinator.FirstSnapshot(renderapi.JobQueued, "job-a")
	if err != nil || !found || second.Job.ID != "job-b" {
		t.Fatalf("excluded queued found=%v snapshot=%+v err=%v", found, second, err)
	}
	if _, found, err := coordinator.FirstSnapshot(renderapi.JobRunning); err != nil || found {
		t.Fatalf("empty running found=%v err=%v", found, err)
	}
	if snapshot, changed, err := coordinator.RequestCancel(context.Background(), "job-b"); err != nil || !changed || snapshot.State != renderapi.JobCanceling {
		t.Fatalf("cancel changed=%v snapshot=%+v err=%v", changed, snapshot, err)
	}
	canceling, found, err := coordinator.FirstSnapshot(renderapi.JobCanceling)
	if err != nil || !found || canceling.Job.ID != "job-b" {
		t.Fatalf("first canceling found=%v snapshot=%+v err=%v", found, canceling, err)
	}
	started, found, err := coordinator.StartNext(context.Background())
	if err != nil || !found || started.Job.ID != "job-a" {
		t.Fatalf("start found=%v snapshot=%+v err=%v", found, started, err)
	}
	running, found, err := coordinator.FirstSnapshot(renderapi.JobRunning)
	if err != nil || !found || running.Job.ID != "job-a" {
		t.Fatalf("first running found=%v snapshot=%+v err=%v", found, running, err)
	}
	queued, found, err := coordinator.FirstSnapshot(renderapi.JobQueued)
	if err != nil || !found || queued.Job.ID != "job-c" {
		t.Fatalf("remaining queued found=%v snapshot=%+v err=%v", found, queued, err)
	}
	reopened, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	for state, want := range map[renderapi.JobState]string{
		renderapi.JobQueued: "job-c", renderapi.JobRunning: "job-a", renderapi.JobCanceling: "job-b",
	} {
		snapshot, found, err := reopened.FirstSnapshot(state)
		if err != nil || !found || snapshot.Job.ID != want {
			t.Fatalf("replayed %s found=%v snapshot=%+v err=%v", state, found, snapshot, err)
		}
	}
	if _, _, err := coordinator.FirstSnapshot(renderapi.JobState("unknown")); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("invalid state = %v", err)
	}
	tooMany := make([]string, MaxSnapshotExclusions+1)
	if _, _, err := coordinator.FirstSnapshot(renderapi.JobQueued, tooMany...); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("exclusion bound = %v", err)
	}
}

func TestCoordinatorConcurrentStartNextNeverDoubleClaimsAndReplays(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	const jobs = 32
	for index := 0; index < jobs; index++ {
		id := fmt.Sprintf("job-%02d", index)
		if _, _, err := coordinator.Submit(context.Background(), schedulerTestJob(id, "request-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan JobSnapshot, jobs)
	errorsSeen := make(chan error, jobs)
	var wait sync.WaitGroup
	for index := 0; index < jobs; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, found, claimErr := coordinator.StartNext(context.Background())
			if claimErr == nil && !found {
				claimErr = errors.New("queued job was not claimed")
			}
			results <- snapshot
			errorsSeen <- claimErr
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]struct{}, jobs)
	for snapshot := range results {
		if _, duplicate := seen[snapshot.Job.ID]; duplicate {
			t.Fatalf("job %q claimed twice", snapshot.Job.ID)
		}
		seen[snapshot.Job.ID] = struct{}{}
	}
	if len(seen) != jobs {
		t.Fatalf("unique claims = %d", len(seen))
	}

	reopened, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reopened.StartNext(context.Background()); err != nil || found {
		t.Fatalf("replayed queue claim found=%v err=%v", found, err)
	}
	running, err := reopened.Snapshots(renderapi.JobRunning)
	if err != nil || len(running) != jobs {
		t.Fatalf("replayed running = %d err=%v", len(running), err)
	}
}

func TestCoordinatorTransitionsTerminalOnceAndReplays(t *testing.T) {
	store := &memoryStore{}
	first, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-1", "request-1")
	if _, _, err := first.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	running, err := first.Start(context.Background(), job.ID)
	if err != nil || running.State != renderapi.JobRunning || running.LastSequence != 2 {
		t.Fatalf("running = %+v err=%v", running, err)
	}
	artifact := schedulerTestArtifact(job)
	complete, err := first.Complete(context.Background(), job.ID, artifact)
	if err != nil || complete.State != renderapi.JobComplete || complete.LastSequence != 3 || complete.Artifact == nil {
		t.Fatalf("complete = %+v err=%v", complete, err)
	}
	if _, err := first.Complete(context.Background(), job.ID, artifact); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second complete = %v", err)
	}
	if snapshot, changed, err := first.RequestCancel(context.Background(), job.ID); err != nil || changed || snapshot.State != renderapi.JobComplete {
		t.Fatalf("terminal cancel = %+v changed=%v err=%v", snapshot, changed, err)
	}
	if len(store.events) != 3 {
		t.Fatalf("terminal event count = %d", len(store.events))
	}

	restarted, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Snapshot(job.ID)
	if err != nil || !reflect.DeepEqual(replayed, complete) {
		t.Fatalf("replayed = %+v err=%v", replayed, err)
	}
	if duplicate, created, err := restarted.Submit(context.Background(), job); err != nil || created || !reflect.DeepEqual(duplicate, replayed) {
		t.Fatalf("restart duplicate = %+v created=%v err=%v", duplicate, created, err)
	}
}

func TestCoordinatorConcurrentTerminalTransitionIsUnique(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-terminal-race", "request-terminal-race")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, transitionErr := coordinator.Complete(context.Background(), job.ID, schedulerTestArtifact(job))
		errorsSeen <- transitionErr
	}()
	go func() {
		defer wait.Done()
		_, transitionErr := coordinator.Fail(context.Background(), job.ID, renderapi.FailureIntegrity, "injected integrity failure")
		errorsSeen <- transitionErr
	}()
	wait.Wait()
	close(errorsSeen)

	successes, rejected := 0, 0
	for transitionErr := range errorsSeen {
		switch {
		case transitionErr == nil:
			successes++
		case errors.Is(transitionErr, ErrInvalidTransition):
			rejected++
		default:
			t.Fatalf("unexpected terminal race error = %v", transitionErr)
		}
	}
	if successes != 1 || rejected != 1 || len(store.events) != 3 {
		t.Fatalf("terminal race successes=%d rejected=%d events=%d", successes, rejected, len(store.events))
	}
	snapshot, err := coordinator.Snapshot(job.ID)
	if err != nil || snapshot.LastSequence != 3 || !terminalState(snapshot.State) {
		t.Fatalf("terminal race snapshot = %+v err=%v", snapshot, err)
	}
}

func TestCoordinatorRejectsArtifactNotBoundToJob(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-artifact-binding", "request-artifact-binding")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		edit func(*renderapi.RenderArtifact)
	}{
		{name: "artifact locator", edit: func(value *renderapi.RenderArtifact) { value.URI = "file:///controlled/other.mp4" }},
		{name: "execution identity", edit: func(value *renderapi.RenderArtifact) { value.ExecutionManifestFingerprint = schedulerDigest('c') }},
		{name: "evidence locator", edit: func(value *renderapi.RenderArtifact) { value.EvidenceURI = "file:///controlled/other.evidence.json" }},
		{name: "content identity", edit: func(value *renderapi.RenderArtifact) { value.ContentHash = "invalid" }},
		{name: "evidence identity", edit: func(value *renderapi.RenderArtifact) { value.EvidenceFingerprint = "invalid" }},
		{name: "empty artifact", edit: func(value *renderapi.RenderArtifact) { value.SizeBytes = 0 }},
		{name: "frame count", edit: func(value *renderapi.RenderArtifact) { value.FrameCount-- }},
		{name: "sample count", edit: func(value *renderapi.RenderArtifact) { value.SampleCount-- }},
		{name: "duration", edit: func(value *renderapi.RenderArtifact) { value.Duration.Value++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := schedulerTestArtifact(job)
			test.edit(&artifact)
			if _, err := coordinator.Complete(context.Background(), job.ID, artifact); !errors.Is(err, ErrProtocolInvalid) {
				t.Fatalf("complete error = %v", err)
			}
			snapshot, err := coordinator.Snapshot(job.ID)
			if err != nil || snapshot.State != renderapi.JobRunning || snapshot.LastSequence != 2 {
				t.Fatalf("snapshot after rejection = %+v err=%v", snapshot, err)
			}
		})
	}
}

func TestCoordinatorConcurrentCancelHasOneTransition(t *testing.T) {
	store := &memoryStore{}
	coordinator, _ := Open(context.Background(), store)
	job := schedulerTestJob("job-cancel", "request-cancel")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	var wait sync.WaitGroup
	changed := make(chan bool, workers)
	errorsSeen := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, didChange, err := coordinator.RequestCancel(context.Background(), job.ID)
			changed <- didChange
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(changed)
	close(errorsSeen)
	transitions := 0
	for value := range changed {
		if value {
			transitions++
		}
	}
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if transitions != 1 {
		t.Fatalf("cancel transitions = %d", transitions)
	}
	canceled, err := coordinator.MarkCanceled(context.Background(), job.ID)
	if err != nil || canceled.State != renderapi.JobCanceled || canceled.LastSequence != 4 {
		t.Fatalf("canceled = %+v err=%v", canceled, err)
	}
	if len(store.events) != 4 {
		t.Fatalf("events = %d", len(store.events))
	}
}

func TestCoordinatorStoreFailureIsAtomic(t *testing.T) {
	store := &memoryStore{}
	coordinator, _ := Open(context.Background(), store)
	job := schedulerTestJob("job-atomic", "request-atomic")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	store.appendErr = errors.New("injected append failure")
	if _, found, err := coordinator.StartNext(context.Background()); !errors.Is(err, ErrStore) || found {
		t.Fatalf("start-next found=%v error=%v", found, err)
	}
	snapshot, err := coordinator.Snapshot(job.ID)
	if err != nil || snapshot.State != renderapi.JobQueued || snapshot.LastSequence != 1 {
		t.Fatalf("snapshot after failed append = %+v err=%v", snapshot, err)
	}
	store.appendErr = nil
	if snapshot, found, err := coordinator.StartNext(context.Background()); err != nil || !found || snapshot.LastSequence != 2 {
		t.Fatalf("retry found=%v snapshot=%+v err=%v", found, snapshot, err)
	}
}

func TestCoordinatorCommitUnknownRequiresReopen(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-unknown-submit", "request-unknown-submit")
	store.appendCommittedErr = fmt.Errorf("%w: injected after durable append", ErrCommitUnknown)
	if _, _, err := coordinator.Submit(context.Background(), job); !errors.Is(err, ErrStore) || !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("unknown submit error = %v", err)
	}
	if len(store.events) != 1 || store.events[0].Sequence != 1 {
		t.Fatalf("unknown submit events = %+v", store.events)
	}
	if _, _, err := coordinator.Submit(context.Background(), job); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("retry on poisoned coordinator = %v", err)
	}
	if _, err := coordinator.Snapshot(job.ID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("snapshot on poisoned coordinator = %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("poisoned coordinator rewrote sequence: %+v", store.events)
	}

	store.appendCommittedErr = nil
	reopened, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(job.ID)
	if err != nil || snapshot.State != renderapi.JobQueued || snapshot.LastSequence != 1 {
		t.Fatalf("reopened snapshot = %+v err=%v", snapshot, err)
	}
	indexed, found, err := reopened.FirstSnapshot(renderapi.JobQueued)
	if err != nil || !found || indexed.Job.ID != job.ID {
		t.Fatalf("reopened queued index found=%v snapshot=%+v err=%v", found, indexed, err)
	}
	if _, created, err := reopened.Submit(context.Background(), job); err != nil || created {
		t.Fatalf("reopened duplicate created=%v err=%v", created, err)
	}
	if len(store.events) != 1 {
		t.Fatalf("reopened duplicate events = %+v", store.events)
	}
}

func TestCoordinatorCommitUnknownTransitionNeverRewritesSequence(t *testing.T) {
	store := &memoryStore{}
	coordinator, _ := Open(context.Background(), store)
	job := schedulerTestJob("job-unknown-transition", "request-unknown-transition")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	store.appendCommittedErr = fmt.Errorf("%w: injected after durable append", ErrCommitUnknown)
	if _, err := coordinator.Start(context.Background(), job.ID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("unknown transition = %v", err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("transition retry = %v", err)
	}
	if len(store.events) != 2 || store.events[0].Sequence != 1 || store.events[1].Sequence != 2 {
		t.Fatalf("unknown transition events = %+v", store.events)
	}
	store.appendCommittedErr = nil
	reopened, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(job.ID)
	if err != nil || snapshot.State != renderapi.JobRunning || snapshot.LastSequence != 2 {
		t.Fatalf("reopened transition = %+v err=%v", snapshot, err)
	}
	indexed, found, err := reopened.FirstSnapshot(renderapi.JobRunning)
	if err != nil || !found || indexed.Job.ID != job.ID {
		t.Fatalf("reopened running index found=%v snapshot=%+v err=%v", found, indexed, err)
	}
}

func TestCoordinatorRejectsIllegalReplay(t *testing.T) {
	job := schedulerTestJob("job-replay", "request-replay")
	store := &memoryStore{events: []JobEvent{
		{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: EventSubmitted, State: renderapi.JobQueued, Job: &job},
		{SchemaVersion: 1, JobID: job.ID, Sequence: 3, Kind: EventStarted, State: renderapi.JobRunning},
	}}
	if _, err := Open(context.Background(), store); !errors.Is(err, ErrStore) {
		t.Fatalf("replay error = %v", err)
	}
	store.events[1].Sequence = 2
	store.events[1].Kind = EventCompleted
	store.events[1].State = renderapi.JobComplete
	store.events[1].Artifact = pointerArtifact(schedulerTestArtifact(job))
	if _, err := Open(context.Background(), store); !errors.Is(err, ErrStore) {
		t.Fatalf("illegal queued->complete replay = %v", err)
	}
}

func TestCoordinatorReplayAdmissionLimitAtNAndNPlusOne(t *testing.T) {
	if err := validateReplayEventCount(MaxReplayEvents, MaxReplayEvents); err != nil {
		t.Fatalf("hard N replay admission = %v", err)
	}
	if err := validateReplayEventCount(MaxReplayEvents+1, MaxReplayEvents); !errors.Is(err, ErrReplayLimit) {
		t.Fatalf("hard N+1 replay admission = %v", err)
	}
	job := schedulerTestJob("job-replay-limit", "request-replay-limit")
	store := &memoryStore{events: []JobEvent{
		{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: EventSubmitted, State: renderapi.JobQueued, Job: &job},
		{SchemaVersion: 1, JobID: job.ID, Sequence: 2, Kind: EventStarted, State: renderapi.JobRunning},
	}}
	if _, err := openWithReplayLimit(context.Background(), store, len(store.events)); err != nil {
		t.Fatalf("N replay = %v", err)
	}
	if _, err := openWithReplayLimit(context.Background(), store, len(store.events)-1); !errors.Is(err, ErrReplayLimit) || !errors.Is(err, ErrStore) {
		t.Fatalf("N+1 replay = %v", err)
	}
}

func TestCoordinatorFailureTransition(t *testing.T) {
	store := &memoryStore{}
	coordinator, _ := Open(context.Background(), store)
	job := schedulerTestJob("job-fail", "request-fail")
	_, _, _ = coordinator.Submit(context.Background(), job)
	failed, err := coordinator.Fail(context.Background(), job.ID, renderapi.FailureIntegrity, "decoded root mismatch")
	if err != nil || failed.State != renderapi.JobFailed || failed.FailureClass != renderapi.FailureIntegrity {
		t.Fatalf("failed = %+v err=%v", failed, err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal start = %v", err)
	}
}

type memoryStore struct {
	mu                 sync.Mutex
	events             []JobEvent
	appendErr          error
	appendCommittedErr error
	replayErr          error
}

func (value *memoryStore) Append(_ context.Context, event JobEvent) error {
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.appendErr != nil {
		return value.appendErr
	}
	value.events = append(value.events, cloneEvent(event))
	return value.appendCommittedErr
}

func (value *memoryStore) Replay(_ context.Context) ([]JobEvent, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.replayErr != nil {
		return nil, value.replayErr
	}
	result := make([]JobEvent, len(value.events))
	for index := range value.events {
		result[index] = cloneEvent(value.events[index])
	}
	return result, nil
}

func schedulerTestJob(id, key string) renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion,
		ID:            id, IdempotencyKey: key,
		PlanFingerprint: schedulerDigest('a'), ExecutionManifestFingerprint: schedulerDigest('b'),
		TimelineRange: videoedit.TimeRange{
			Start:    videoedit.RationalTime{Value: 0, Rate: 1},
			Duration: videoedit.RationalTime{Value: 1, Rate: 1},
		},
		FrameRange:  videoedit.TickRange{First: 0, Count: 30},
		SampleRange: videoedit.TickRange{First: 0, Count: 48_000},
		ArtifactURI: "file:///controlled/" + id + ".mp4",
	}
}

func schedulerTestArtifact(job renderapi.RenderJob) renderapi.RenderArtifact {
	return renderapi.RenderArtifact{
		URI: job.ArtifactURI, ContentHash: schedulerDigest('c'), SizeBytes: 1024,
		ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		EvidenceURI:                  job.ArtifactURI + ".evidence.json", EvidenceFingerprint: schedulerDigest('d'),
		Duration: videoedit.RationalTime{Value: 1, Rate: 1}, FrameCount: 30, SampleCount: 48_000,
	}
}

func schedulerDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func pointerArtifact(value renderapi.RenderArtifact) *renderapi.RenderArtifact { return &value }

func marshalProtocol(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
