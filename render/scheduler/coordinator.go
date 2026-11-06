package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// Store is the durable append-only boundary. Append must not return success
// until the event is durable. An Append error that does not wrap
// ErrCommitUnknown must be failure-atomic: the event must not be visible to any
// later Replay. If an implementation cannot prove that after an error, it must
// wrap ErrCommitUnknown; Coordinator will then stop serving so the same event
// sequence cannot be written twice. Replay must return complete events in
// append order and must fail closed on corruption. Replay implementations must
// apply their own byte/record admission before allocating; Coordinator
// additionally rejects results above MaxReplayEvents.
type Store interface {
	Append(context.Context, JobEvent) error
	Replay(context.Context) ([]JobEvent, error)
}

type Coordinator struct {
	mu             sync.RWMutex
	store          Store
	jobs           map[string]JobSnapshot
	stateJobs      map[renderapi.JobState]*jobIDHeap
	idempotencyJob map[string]string
	recoveryNeeded bool
}

func Open(ctx context.Context, store Store) (*Coordinator, error) {
	return openWithReplayLimit(ctx, store, MaxReplayEvents)
}

func openWithReplayLimit(ctx context.Context, store Store, maxReplayEvents int) (*Coordinator, error) {
	if ctx == nil || store == nil {
		return nil, fmt.Errorf("%w: context and store are required", ErrStore)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := store.Replay(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: replay: %w", ErrStore, err)
	}
	if err := validateReplayEventCount(len(events), maxReplayEvents); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	result := &Coordinator{
		store: store, jobs: make(map[string]JobSnapshot), idempotencyJob: make(map[string]string),
	}
	for index, event := range events {
		if index%256 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
		}
		if err := result.applyReplay(event); err != nil {
			return nil, fmt.Errorf("%w: replay event %d: %w", ErrStore, index, err)
		}
	}
	result.rebuildStateIndex()
	return result, nil
}

func validateReplayEventCount(count, maximum int) error {
	if maximum <= 0 || maximum > MaxReplayEvents {
		return fmt.Errorf("%w: configured maximum %d", ErrReplayLimit, maximum)
	}
	if count < 0 || count > maximum {
		return fmt.Errorf("%w: events %d exceed %d", ErrReplayLimit, count, maximum)
	}
	return nil
}

// Submit durably creates a queued job. Repeating the exact same RenderJob is
// idempotent and returns created=false. Reusing either the ID or idempotency key
// for different immutable content fails closed.
func (value *Coordinator) Submit(ctx context.Context, job renderapi.RenderJob) (JobSnapshot, bool, error) {
	verified, err := validateAndCloneRenderJob(job)
	if err != nil {
		return JobSnapshot{}, false, fmt.Errorf("%w: submit job: %v", ErrProtocolInvalid, err)
	}
	wantCanonical, err := canonicalProtocol(verified)
	if err != nil {
		return JobSnapshot{}, false, err
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return JobSnapshot{}, false, err
	}
	if err := value.readyLocked(); err != nil {
		return JobSnapshot{}, false, err
	}
	if existingID, found := value.idempotencyJob[verified.IdempotencyKey]; found {
		existing := value.jobs[existingID]
		existingCanonical, encodeErr := canonicalProtocol(existing.Job)
		if encodeErr != nil {
			return JobSnapshot{}, false, encodeErr
		}
		if existingID != verified.ID || !bytes.Equal(existingCanonical, wantCanonical) {
			return JobSnapshot{}, false, ErrIdempotencyConflict
		}
		return cloneSnapshot(existing), false, nil
	}
	if existing, found := value.jobs[verified.ID]; found {
		existingCanonical, encodeErr := canonicalProtocol(existing.Job)
		if encodeErr != nil {
			return JobSnapshot{}, false, encodeErr
		}
		if !bytes.Equal(existingCanonical, wantCanonical) {
			return JobSnapshot{}, false, ErrJobConflict
		}
		return cloneSnapshot(existing), false, nil
	}
	event := JobEvent{
		SchemaVersion: JobEventSchemaVersion, JobID: verified.ID, Sequence: 1,
		Kind: EventSubmitted, State: renderapi.JobQueued, Job: &verified,
	}
	if err := value.appendLocked(ctx, event); err != nil {
		return JobSnapshot{}, false, err
	}
	snapshot := snapshotFromSubmission(verified)
	value.jobs[verified.ID] = snapshot
	value.idempotencyJob[verified.IdempotencyKey] = verified.ID
	value.indexSnapshotLocked(snapshot)
	return cloneSnapshot(snapshot), true, nil
}

func (value *Coordinator) Start(ctx context.Context, jobID string) (JobSnapshot, error) {
	return value.transition(ctx, jobID, EventStarted, "", "", nil)
}

// StartNext atomically selects the lexicographically first queued job and
// durably transitions it to running. Selection and transition share the
// Coordinator lock, so multiple worker goroutines in one owning process cannot
// claim the same job. A deployment must still run one Coordinator owner per
// journal root; cross-process worker leases are a separate distributed profile.
func (value *Coordinator) StartNext(ctx context.Context) (JobSnapshot, bool, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return JobSnapshot{}, false, err
	}
	if err := value.readyLocked(); err != nil {
		return JobSnapshot{}, false, err
	}
	snapshot, found := value.firstSnapshotLocked(renderapi.JobQueued, nil)
	if !found {
		return JobSnapshot{}, false, nil
	}
	event := nextEvent(snapshot, EventStarted)
	if err := value.appendLocked(ctx, event); err != nil {
		return JobSnapshot{}, false, err
	}
	snapshot = applyEvent(snapshot, event)
	value.jobs[snapshot.Job.ID] = snapshot
	value.indexSnapshotLocked(snapshot)
	return cloneSnapshot(snapshot), true, nil
}

// RequestCancel is idempotent for canceling and terminal jobs. The first
// request durably moves queued/running work to canceling.
func (value *Coordinator) RequestCancel(ctx context.Context, jobID string) (JobSnapshot, bool, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return JobSnapshot{}, false, err
	}
	if err := value.readyLocked(); err != nil {
		return JobSnapshot{}, false, err
	}
	snapshot, found := value.jobs[jobID]
	if !found {
		return JobSnapshot{}, false, ErrJobNotFound
	}
	if snapshot.State == renderapi.JobCanceling || terminalState(snapshot.State) {
		return cloneSnapshot(snapshot), false, nil
	}
	if snapshot.State != renderapi.JobQueued && snapshot.State != renderapi.JobRunning {
		return JobSnapshot{}, false, transitionError(snapshot.State, renderapi.JobCanceling)
	}
	event := nextEvent(snapshot, EventCancelRequested)
	if err := value.appendLocked(ctx, event); err != nil {
		return JobSnapshot{}, false, err
	}
	snapshot = applyEvent(snapshot, event)
	value.jobs[jobID] = snapshot
	value.indexSnapshotLocked(snapshot)
	return cloneSnapshot(snapshot), true, nil
}

func (value *Coordinator) MarkCanceled(ctx context.Context, jobID string) (JobSnapshot, error) {
	return value.transition(ctx, jobID, EventCanceled, "", "", nil)
}

func (value *Coordinator) Fail(ctx context.Context, jobID string, class renderapi.FailureClass, message string) (JobSnapshot, error) {
	if !validFailureClass(class) || stringsTrimmedEmpty(message) {
		return JobSnapshot{}, fmt.Errorf("%w: failure details are required", ErrProtocolInvalid)
	}
	return value.transition(ctx, jobID, EventFailed, class, message, nil)
}

func (value *Coordinator) Complete(ctx context.Context, jobID string, artifact renderapi.RenderArtifact) (JobSnapshot, error) {
	return value.transition(ctx, jobID, EventCompleted, "", "", &artifact)
}

func (value *Coordinator) Snapshot(jobID string) (JobSnapshot, error) {
	value.mu.RLock()
	defer value.mu.RUnlock()
	if err := value.readyLocked(); err != nil {
		return JobSnapshot{}, err
	}
	snapshot, found := value.jobs[jobID]
	if !found {
		return JobSnapshot{}, ErrJobNotFound
	}
	return cloneSnapshot(snapshot), nil
}

// Snapshots returns an owned, job-ID-sorted point-in-time view. With no state
// arguments it returns all jobs; otherwise it returns the union of the
// requested states. The result is bounded by the Coordinator replay ceiling.
func (value *Coordinator) Snapshots(states ...renderapi.JobState) ([]JobSnapshot, error) {
	filter := make(map[renderapi.JobState]struct{}, len(states))
	for _, state := range states {
		if !validJobState(state) {
			return nil, fmt.Errorf("%w: snapshot filter state %q", ErrProtocolInvalid, state)
		}
		filter[state] = struct{}{}
	}
	value.mu.RLock()
	defer value.mu.RUnlock()
	if err := value.readyLocked(); err != nil {
		return nil, err
	}
	capacity := len(value.jobs)
	if len(filter) != 0 {
		// A filtered query must allocate in proportion to its matches, not to
		// every queued job. This keeps an empty running/canceling query bounded
		// even when the durable queue is near MaxReplayEvents.
		capacity = 0
	}
	result := make([]JobSnapshot, 0, capacity)
	for _, snapshot := range value.jobs {
		if len(filter) != 0 {
			if _, included := filter[snapshot.State]; !included {
				continue
			}
		}
		result = append(result, cloneSnapshot(snapshot))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Job.ID < result[right].Job.ID })
	return result, nil
}

// FirstSnapshot returns the lexicographically first job in state whose ID is
// not excluded. It uses a replay-rebuilt state heap without cloning or sorting
// every match, so local runtimes can resume work with bounded result memory.
// Exclusions are bounded because callers normally pass only their active jobs.
func (value *Coordinator) FirstSnapshot(state renderapi.JobState, excludedIDs ...string) (JobSnapshot, bool, error) {
	if !validJobState(state) || len(excludedIDs) > MaxSnapshotExclusions {
		return JobSnapshot{}, false, fmt.Errorf("%w: invalid first-snapshot query", ErrProtocolInvalid)
	}
	excluded := make(map[string]struct{}, len(excludedIDs))
	for _, jobID := range excludedIDs {
		if !validBoundedString(jobID, MaxJobIDBytes) || stringsTrimmedEmpty(jobID) {
			return JobSnapshot{}, false, fmt.Errorf("%w: invalid excluded job ID", ErrProtocolInvalid)
		}
		excluded[jobID] = struct{}{}
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if err := value.readyLocked(); err != nil {
		return JobSnapshot{}, false, err
	}
	selected, found := value.firstSnapshotLocked(state, excluded)
	if !found {
		return JobSnapshot{}, false, nil
	}
	return cloneSnapshot(selected), true, nil
}

func (value *Coordinator) transition(ctx context.Context, jobID string, kind JobEventKind,
	failureClass renderapi.FailureClass, failureMessage string, artifact *renderapi.RenderArtifact) (JobSnapshot, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return JobSnapshot{}, err
	}
	if err := value.readyLocked(); err != nil {
		return JobSnapshot{}, err
	}
	snapshot, found := value.jobs[jobID]
	if !found {
		return JobSnapshot{}, ErrJobNotFound
	}
	wantState, valid := eventState(kind)
	if !valid || !legalTransition(snapshot.State, wantState) {
		return JobSnapshot{}, transitionError(snapshot.State, wantState)
	}
	if artifact != nil {
		if err := validateArtifact(snapshot.Job, *artifact); err != nil {
			return JobSnapshot{}, err
		}
	}
	event := nextEvent(snapshot, kind)
	event.FailureClass = failureClass
	event.FailureMessage = failureMessage
	if artifact != nil {
		copyValue := *artifact
		event.Artifact = &copyValue
	}
	if err := ValidateJobEvent(event); err != nil {
		return JobSnapshot{}, err
	}
	if err := value.appendLocked(ctx, event); err != nil {
		return JobSnapshot{}, err
	}
	snapshot = applyEvent(snapshot, event)
	value.jobs[jobID] = snapshot
	value.indexSnapshotLocked(snapshot)
	return cloneSnapshot(snapshot), nil
}

func (value *Coordinator) appendLocked(ctx context.Context, event JobEvent) error {
	if err := value.store.Append(ctx, cloneEvent(event)); err != nil {
		if errors.Is(err, ErrCommitUnknown) {
			value.recoveryNeeded = true
			return errors.Join(
				fmt.Errorf("%w: append: %w", ErrStore, err),
				ErrRecoveryRequired,
			)
		}
		return fmt.Errorf("%w: append: %w", ErrStore, err)
	}
	return nil
}

func (value *Coordinator) readyLocked() error {
	if value.recoveryNeeded {
		return ErrRecoveryRequired
	}
	return nil
}

func (value *Coordinator) applyReplay(event JobEvent) error {
	if err := ValidateJobEvent(event); err != nil {
		return err
	}
	if event.Kind == EventSubmitted {
		if _, exists := value.jobs[event.JobID]; exists {
			return ErrJobConflict
		}
		if _, exists := value.idempotencyJob[event.Job.IdempotencyKey]; exists {
			return ErrIdempotencyConflict
		}
		snapshot := snapshotFromSubmission(*event.Job)
		value.jobs[event.JobID] = snapshot
		value.idempotencyJob[event.Job.IdempotencyKey] = event.JobID
		return nil
	}
	snapshot, found := value.jobs[event.JobID]
	if !found || event.Sequence != snapshot.LastSequence+1 || !legalTransition(snapshot.State, event.State) {
		return fmt.Errorf("%w: replay %q sequence/state", ErrInvalidTransition, event.JobID)
	}
	if event.Artifact != nil {
		if err := validateArtifact(snapshot.Job, *event.Artifact); err != nil {
			return err
		}
	}
	value.jobs[event.JobID] = applyEvent(snapshot, event)
	return nil
}

func snapshotFromSubmission(job renderapi.RenderJob) JobSnapshot {
	return JobSnapshot{
		SchemaVersion: JobSnapshotSchemaVersion, Job: job,
		State: renderapi.JobQueued, LastSequence: 1,
	}
}

func nextEvent(snapshot JobSnapshot, kind JobEventKind) JobEvent {
	state, _ := eventState(kind)
	return JobEvent{
		SchemaVersion: JobEventSchemaVersion, JobID: snapshot.Job.ID,
		Sequence: snapshot.LastSequence + 1, Kind: kind, State: state,
	}
}

func applyEvent(snapshot JobSnapshot, event JobEvent) JobSnapshot {
	snapshot.State = event.State
	snapshot.LastSequence = event.Sequence
	snapshot.FailureClass = event.FailureClass
	snapshot.FailureMessage = event.FailureMessage
	if event.Artifact == nil {
		snapshot.Artifact = nil
	} else {
		artifact := *event.Artifact
		snapshot.Artifact = &artifact
	}
	return snapshot
}

func legalTransition(from, to renderapi.JobState) bool {
	switch from {
	case renderapi.JobQueued:
		return to == renderapi.JobRunning || to == renderapi.JobCanceling || to == renderapi.JobFailed
	case renderapi.JobRunning:
		return to == renderapi.JobCanceling || to == renderapi.JobFailed || to == renderapi.JobComplete
	case renderapi.JobCanceling:
		return to == renderapi.JobCanceled || to == renderapi.JobFailed
	default:
		return false
	}
}

func terminalState(state renderapi.JobState) bool {
	return state == renderapi.JobCanceled || state == renderapi.JobFailed || state == renderapi.JobComplete
}

func transitionError(from, to renderapi.JobState) error {
	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scheduler: nil context")
	}
	return ctx.Err()
}

func stringsTrimmedEmpty(value string) bool {
	for _, character := range value {
		if character != ' ' && character != '\t' && character != '\r' && character != '\n' {
			return false
		}
	}
	return true
}
