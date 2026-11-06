package renderruntime

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	DefaultMaxConcurrentJobs = 1
	MaxConcurrentJobs        = 1024
	jobGateCount             = 257
	maxFailureMessageBytes   = scheduler.MaxFailureMessageBytes
)

var (
	ErrConfiguration = errors.New("render runtime: invalid configuration")
	ErrCapacity      = errors.New("render runtime: all job slots are occupied")
	ErrPlanBinding   = errors.New("render runtime: stage plan is not bound to the scheduler job")
	ErrExecutor      = errors.New("render runtime: stage executor is unavailable")
)

// PlanSource loads the immutable stage plan selected by a scheduler job. The
// Dispatcher revalidates the returned plan and its job/range bindings.
type PlanSource interface {
	ResolveStagePlan(context.Context, renderapi.RenderJob) (stagedag.Plan, error)
}

type PlanSourceFunc func(context.Context, renderapi.RenderJob) (stagedag.Plan, error)

func (function PlanSourceFunc) ResolveStagePlan(ctx context.Context, job renderapi.RenderJob) (stagedag.Plan, error) {
	return function(ctx, job)
}

// ExecutorSource creates an immutable, job-scoped executor after the plan has
// been frozen and rebound to the scheduler job. The source receives an owned
// plan copy and may be called concurrently for different jobs; implementations
// must be concurrency-safe and honor context cancellation. This is required by
// publish stages whose target belongs to RenderJob; cacheable stages may use
// Config's shared Executor instead. Exactly one executor mode must be
// configured.
type ExecutorSource interface {
	ResolveExecutor(context.Context, renderapi.RenderJob, stagedag.Plan) (stagedag.Executor, error)
}

type ExecutorSourceFunc func(context.Context, renderapi.RenderJob, stagedag.Plan) (stagedag.Executor, error)

func (function ExecutorSourceFunc) ResolveExecutor(ctx context.Context, job renderapi.RenderJob, plan stagedag.Plan) (stagedag.Executor, error) {
	return function(ctx, job, plan)
}

// ArtifactResolver derives and verifies the final scheduler artifact from
// already-committed stage checkpoints. Publish belongs in the StagePublish
// task; this callback must be bounded, idempotent and read-only.
type ArtifactResolver interface {
	ResolveArtifact(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error)
}

type ArtifactResolverFunc func(context.Context, renderapi.RenderJob, stagedag.Plan, stagedag.RunResult) (renderapi.RenderArtifact, error)

func (function ArtifactResolverFunc) ResolveArtifact(ctx context.Context, job renderapi.RenderJob, plan stagedag.Plan, result stagedag.RunResult) (renderapi.RenderArtifact, error) {
	return function(ctx, job, plan, result)
}

// FailureError lets a PlanSource, Executor or ArtifactResolver select one
// stable scheduler failure class while preserving the original error chain.
type FailureError struct {
	Class renderapi.FailureClass
	Err   error
}

func (value *FailureError) Error() string {
	if value == nil {
		return "render runtime: <nil failure>"
	}
	return fmt.Sprintf("render runtime: %s: %v", value.Class, value.Err)
}

func (value *FailureError) Unwrap() error {
	if value == nil {
		return nil
	}
	return value.Err
}

type Config struct {
	Coordinator *scheduler.Coordinator
	Plans       PlanSource
	Checkpoints stagedag.CheckpointStore
	Executor    stagedag.Executor
	Executors   ExecutorSource
	Artifacts   ArtifactResolver
	StageLimits stagedag.Limits
	Progress    renderapi.EventSink
	// StageLimits applies independently to every active job. The embedding host
	// must size MaxConcurrentJobs so their product fits its aggregate memory and
	// disk budget; Dispatcher does not overcommit a hidden global pool.
	MaxConcurrentJobs int
}

type Dispatcher struct {
	coordinator *scheduler.Coordinator
	plans       PlanSource
	checkpoints stagedag.CheckpointStore
	executor    stagedag.Executor
	executors   ExecutorSource
	artifacts   ArtifactResolver
	stageLimits stagedag.Limits
	progress    renderapi.EventSink
	maximumJobs int

	mu       sync.Mutex
	active   map[string]*activeRun
	jobGates [jobGateCount]sync.Mutex
}

type activeRun struct {
	cancel          context.CancelFunc
	cancelRequested bool
}

type Run struct {
	Snapshot scheduler.JobSnapshot
	DAG      stagedag.RunResult
	Resumed  bool
}

func New(config Config) (*Dispatcher, error) {
	maximum := config.MaxConcurrentJobs
	if maximum == 0 {
		maximum = DefaultMaxConcurrentJobs
	}
	if config.Coordinator == nil || config.Plans == nil || config.Checkpoints == nil ||
		(config.Executor == nil) == (config.Executors == nil) || config.Artifacts == nil || maximum < 1 || maximum > MaxConcurrentJobs ||
		config.StageLimits.MaxConcurrent <= 0 || config.StageLimits.MaxConcurrent > stagedag.MaxConcurrentTasks ||
		config.StageLimits.MaxMemoryBytes == 0 || config.StageLimits.MaxDiskBytes == 0 {
		return nil, ErrConfiguration
	}
	return &Dispatcher{
		coordinator: config.Coordinator, plans: config.Plans, checkpoints: config.Checkpoints,
		executor: config.Executor, executors: config.Executors, artifacts: config.Artifacts, stageLimits: config.StageLimits,
		progress: config.Progress, maximumJobs: maximum, active: make(map[string]*activeRun),
	}, nil
}

// Submit, Snapshot and RequestCancel make Dispatcher directly usable by the
// public agent/renderqueue service. Cancellation is coupled to the active
// stage context instead of merely changing durable scheduler state.
func (dispatcher *Dispatcher) Submit(ctx context.Context, job renderapi.RenderJob) (scheduler.JobSnapshot, bool, error) {
	if dispatcher == nil || dispatcher.coordinator == nil {
		return scheduler.JobSnapshot{}, false, ErrConfiguration
	}
	return dispatcher.coordinator.Submit(ctx, job)
}

func (dispatcher *Dispatcher) Snapshot(jobID string) (scheduler.JobSnapshot, error) {
	if dispatcher == nil || dispatcher.coordinator == nil {
		return scheduler.JobSnapshot{}, ErrConfiguration
	}
	return dispatcher.coordinator.Snapshot(jobID)
}

func (dispatcher *Dispatcher) History(jobID string, afterSequence uint64, limit int) (scheduler.JobEventPage, error) {
	if dispatcher == nil || dispatcher.coordinator == nil {
		return scheduler.JobEventPage{}, ErrConfiguration
	}
	return dispatcher.coordinator.History(jobID, afterSequence, limit)
}

func (dispatcher *Dispatcher) Snapshots(states ...renderapi.JobState) ([]scheduler.JobSnapshot, error) {
	if dispatcher == nil || dispatcher.coordinator == nil {
		return nil, ErrConfiguration
	}
	return dispatcher.coordinator.Snapshots(states...)
}

func (dispatcher *Dispatcher) RequestCancel(ctx context.Context, jobID string) (scheduler.JobSnapshot, bool, error) {
	if dispatcher == nil || dispatcher.coordinator == nil {
		return scheduler.JobSnapshot{}, false, ErrConfiguration
	}
	gate := dispatcher.jobGate(jobID)
	gate.Lock()
	defer gate.Unlock()
	snapshot, changed, err := dispatcher.coordinator.RequestCancel(ctx, jobID)
	if err != nil {
		return scheduler.JobSnapshot{}, false, err
	}
	if snapshot.State == renderapi.JobCanceling {
		dispatcher.mu.Lock()
		if active := dispatcher.active[jobID]; active != nil {
			active.cancelRequested = true
			active.cancel()
		}
		dispatcher.mu.Unlock()
	}
	return snapshot, changed, nil
}

// RunNext claims and runs at most one job. Journaled canceling work is closed
// first, then an unowned running job is resumed, then StartNext claims a queued
// job. found=false means there is currently no runnable work.
func (dispatcher *Dispatcher) RunNext(ctx context.Context) (run Run, found bool, returnErr error) {
	if dispatcher == nil || dispatcher.coordinator == nil || ctx == nil {
		return Run{}, false, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Run{}, false, err
	}
	claim, err := dispatcher.claim(ctx)
	if err != nil || claim == nil {
		return Run{}, false, err
	}
	found = true
	defer dispatcher.release(claim.snapshot.Job.ID)
	if claim.cancelOnly {
		snapshot, err := dispatcher.coordinator.MarkCanceled(ctx, claim.snapshot.Job.ID)
		if err != nil {
			return Run{}, true, err
		}
		return Run{Snapshot: snapshot, Resumed: true}, true, nil
	}

	plan, err := safeResolvePlan(claim.ctx, dispatcher.plans, claim.snapshot.Job)
	if err == nil {
		plan, err = freezeResolvedPlan(plan)
	}
	if err == nil {
		err = validatePlanJobBinding(plan, claim.snapshot.Job)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Run{}, true, ctxErr
		}
		snapshot, finishErr := dispatcher.finishFailure(ctx, claim.snapshot.Job.ID, classifyFailure(err, renderapi.FailureInputRepair), err)
		return Run{Snapshot: snapshot, Resumed: claim.resumed}, true, finishErr
	}
	executor := dispatcher.executor
	if dispatcher.executors != nil {
		// Never expose the plan that the Engine and ArtifactResolver will use to
		// an embedding callback. A source may retain or mutate its owned copy;
		// neither action can change execution or introduce a race at freeze time.
		executorPlan, cloneErr := freezeResolvedPlan(plan)
		if cloneErr != nil {
			err = cloneErr
		} else {
			executor, err = safeResolveExecutor(claim.ctx, dispatcher.executors, claim.snapshot.Job, executorPlan)
		}
		if err == nil && executor == nil {
			err = ErrExecutor
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && !dispatcher.cancelWasRequested(claim.snapshot.Job.ID) {
				return Run{Snapshot: claim.snapshot, Resumed: claim.resumed}, true, ctxErr
			}
			snapshot, finishErr := dispatcher.finishFailure(ctx, claim.snapshot.Job.ID, classifyFailure(err, renderapi.FailureNonRetryable), err)
			return Run{Snapshot: snapshot, Resumed: claim.resumed}, true, finishErr
		}
	}

	engine := stagedag.Engine{
		Store: dispatcher.checkpoints, Executor: executor,
		Sink: dispatcher.progress, Limits: dispatcher.stageLimits,
	}
	dagResult, err := engine.Run(claim.ctx, plan)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !dispatcher.cancelWasRequested(claim.snapshot.Job.ID) {
			// A host shutdown leaves the durable running job resumable. Do not
			// turn a local owner lifetime into a terminal render failure.
			return Run{Snapshot: claim.snapshot, Resumed: claim.resumed}, true, ctxErr
		}
		snapshot, finishErr := dispatcher.finishExecutionError(ctx, claim.snapshot.Job.ID, err)
		return Run{Snapshot: snapshot, DAG: dagResult, Resumed: claim.resumed}, true, finishErr
	}

	// Artifact resolution is intentionally outside the per-job transition gate:
	// RequestCancel must be able to cancel a bounded resolver instead of waiting
	// for it. The gate and state recheck below serialize the final transition.
	artifact, err := safeResolveArtifact(claim.ctx, dispatcher.artifacts, claim.snapshot.Job, plan, dagResult)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !dispatcher.cancelWasRequested(claim.snapshot.Job.ID) {
			return Run{Snapshot: claim.snapshot, DAG: dagResult, Resumed: claim.resumed}, true, ctxErr
		}
		snapshot, finishErr := dispatcher.finishFailure(ctx, claim.snapshot.Job.ID, classifyFailure(err, renderapi.FailureIntegrity), err)
		return Run{Snapshot: snapshot, DAG: dagResult, Resumed: claim.resumed}, true, finishErr
	}

	gate := dispatcher.jobGate(claim.snapshot.Job.ID)
	gate.Lock()
	defer gate.Unlock()
	current, err := dispatcher.coordinator.Snapshot(claim.snapshot.Job.ID)
	if err != nil {
		return Run{}, true, err
	}
	if current.State == renderapi.JobCanceling {
		snapshot, err := dispatcher.coordinator.MarkCanceled(ctx, current.Job.ID)
		return Run{Snapshot: snapshot, DAG: dagResult, Resumed: claim.resumed}, true, err
	}
	if current.State != renderapi.JobRunning {
		return Run{}, true, fmt.Errorf("render runtime: job %q left running state before completion: %s", current.Job.ID, current.State)
	}
	snapshot, err := dispatcher.coordinator.Complete(ctx, current.Job.ID, artifact)
	if err != nil {
		if errors.Is(err, scheduler.ErrProtocolInvalid) {
			failed, finishErr := dispatcher.failLocked(ctx, current.Job.ID, renderapi.FailureIntegrity, err)
			return Run{Snapshot: failed, DAG: dagResult, Resumed: claim.resumed}, true, finishErr
		}
		return Run{}, true, err
	}
	return Run{Snapshot: snapshot, DAG: dagResult, Resumed: claim.resumed}, true, nil
}

type jobClaim struct {
	snapshot   scheduler.JobSnapshot
	ctx        context.Context
	cancelOnly bool
	resumed    bool
}

func (dispatcher *Dispatcher) claim(ctx context.Context) (*jobClaim, error) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()

	if len(dispatcher.active) >= dispatcher.maximumJobs {
		return nil, ErrCapacity
	}
	excluded := make([]string, 0, len(dispatcher.active))
	for jobID := range dispatcher.active {
		excluded = append(excluded, jobID)
	}
	canceling, found, err := dispatcher.coordinator.FirstSnapshot(renderapi.JobCanceling, excluded...)
	if err != nil {
		return nil, err
	}
	if found {
		runCtx, cancel := context.WithCancel(ctx)
		dispatcher.active[canceling.Job.ID] = &activeRun{cancel: cancel, cancelRequested: true}
		return &jobClaim{snapshot: canceling, ctx: runCtx, cancelOnly: true, resumed: true}, nil
	}
	running, found, err := dispatcher.coordinator.FirstSnapshot(renderapi.JobRunning, excluded...)
	if err != nil {
		return nil, err
	}
	if found {
		runCtx, cancel := context.WithCancel(ctx)
		dispatcher.active[running.Job.ID] = &activeRun{cancel: cancel}
		return &jobClaim{snapshot: running, ctx: runCtx, resumed: true}, nil
	}
	snapshot, found, err := dispatcher.coordinator.StartNext(ctx)
	if err != nil || !found {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	dispatcher.active[snapshot.Job.ID] = &activeRun{cancel: cancel}
	return &jobClaim{snapshot: snapshot, ctx: runCtx}, nil
}

func (dispatcher *Dispatcher) release(jobID string) {
	dispatcher.mu.Lock()
	if active := dispatcher.active[jobID]; active != nil {
		active.cancel()
		delete(dispatcher.active, jobID)
	}
	dispatcher.mu.Unlock()
}

func (dispatcher *Dispatcher) cancelWasRequested(jobID string) bool {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	active := dispatcher.active[jobID]
	return active != nil && active.cancelRequested
}

func (dispatcher *Dispatcher) finishExecutionError(ctx context.Context, jobID string, executionErr error) (scheduler.JobSnapshot, error) {
	gate := dispatcher.jobGate(jobID)
	gate.Lock()
	defer gate.Unlock()
	current, err := dispatcher.coordinator.Snapshot(jobID)
	if err != nil {
		return scheduler.JobSnapshot{}, err
	}
	if current.State == renderapi.JobCanceling || dispatcher.cancelWasRequested(jobID) {
		return dispatcher.coordinator.MarkCanceled(ctx, jobID)
	}
	return dispatcher.failLocked(ctx, jobID, classifyFailure(executionErr, renderapi.FailureNonRetryable), executionErr)
}

func (dispatcher *Dispatcher) finishFailure(ctx context.Context, jobID string, class renderapi.FailureClass, failure error) (scheduler.JobSnapshot, error) {
	gate := dispatcher.jobGate(jobID)
	gate.Lock()
	defer gate.Unlock()
	current, err := dispatcher.coordinator.Snapshot(jobID)
	if err != nil {
		return scheduler.JobSnapshot{}, err
	}
	if current.State == renderapi.JobCanceling || dispatcher.cancelWasRequested(jobID) {
		return dispatcher.coordinator.MarkCanceled(ctx, jobID)
	}
	return dispatcher.failLocked(ctx, jobID, class, failure)
}

func (dispatcher *Dispatcher) failLocked(ctx context.Context, jobID string, class renderapi.FailureClass, failure error) (scheduler.JobSnapshot, error) {
	return dispatcher.coordinator.Fail(ctx, jobID, class, failureMessage(failure))
}

func (dispatcher *Dispatcher) jobGate(jobID string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(jobID))
	return &dispatcher.jobGates[int(hash.Sum32()%jobGateCount)]
}

func validatePlanJobBinding(plan stagedag.Plan, job renderapi.RenderJob) error {
	if err := stagedag.ValidatePlan(plan); err != nil {
		return errors.Join(ErrPlanBinding, err)
	}
	if plan.JobID != job.ID || plan.PlanFingerprint != job.PlanFingerprint ||
		plan.ExecutionManifestFingerprint != job.ExecutionManifestFingerprint {
		return ErrPlanBinding
	}
	for _, task := range plan.Tasks {
		if !tickRangeContained(task.FrameRange, job.FrameRange) || !tickRangeContained(task.SampleRange, job.SampleRange) {
			return fmt.Errorf("%w: task %q range exceeds job", ErrPlanBinding, task.ID)
		}
	}
	return nil
}

func tickRangeContained(value *videoedit.TickRange, parent videoedit.TickRange) bool {
	if value == nil {
		return true
	}
	valueEnd, valueOK := tickEnd(*value)
	parentEnd, parentOK := tickEnd(parent)
	return valueOK && parentOK && value.First >= parent.First && valueEnd <= parentEnd
}

func tickEnd(value videoedit.TickRange) (int64, bool) {
	if value.First < 0 || value.Count < 0 || value.First > math.MaxInt64-value.Count {
		return 0, false
	}
	return value.First + value.Count, true
}

func classifyFailure(err error, fallback renderapi.FailureClass) renderapi.FailureClass {
	var explicit *FailureError
	if errors.As(err, &explicit) && explicit != nil && validFailureClass(explicit.Class) {
		return explicit.Class
	}
	var stage *stagedag.StageError
	if errors.As(err, &stage) && stage != nil && validFailureClass(stage.Class) {
		return stage.Class
	}
	switch {
	case errors.Is(err, stagedag.ErrBudget):
		return renderapi.FailureResource
	case errors.Is(err, stagedag.ErrCheckpointInvalid), errors.Is(err, stagedag.ErrCheckpointStore):
		return renderapi.FailureIntegrity
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return renderapi.FailureCancellation
	default:
		return fallback
	}
}

func validFailureClass(value renderapi.FailureClass) bool {
	switch value {
	case renderapi.FailureNonRetryable, renderapi.FailureInputRepair, renderapi.FailureTransient,
		renderapi.FailureResource, renderapi.FailureIntegrity, renderapi.FailureCancellation:
		return true
	default:
		return false
	}
}

func failureMessage(err error) string {
	message := "render job failed"
	if err != nil {
		// Trim before bounding. Otherwise an error with a bounded prefix made
		// entirely of whitespace can become an invalid scheduler failure after
		// truncation and leave the durable job stuck in running forever.
		candidate := strings.TrimSpace(err.Error())
		if candidate != "" {
			if len(candidate) > maxFailureMessageBytes {
				candidate = candidate[:maxFailureMessageBytes]
			}
			message = strings.ToValidUTF8(candidate, "\uFFFD")
		}
	}
	if len(message) <= maxFailureMessageBytes {
		return message
	}
	message = message[:maxFailureMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	if strings.TrimSpace(message) == "" {
		return "render job failed"
	}
	return message
}

func safeResolvePlan(ctx context.Context, source PlanSource, job renderapi.RenderJob) (_ stagedag.Plan, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("render runtime: plan source panic: %v", recovered)
		}
	}()
	return source.ResolveStagePlan(ctx, job)
}

func freezeResolvedPlan(plan stagedag.Plan) (stagedag.Plan, error) {
	canonical, err := stagedag.CanonicalPlan(plan)
	if err != nil {
		return stagedag.Plan{}, err
	}
	frozen, err := stagedag.DecodePlan(canonical)
	if err != nil {
		return stagedag.Plan{}, err
	}
	return frozen, nil
}

func safeResolveExecutor(ctx context.Context, source ExecutorSource, job renderapi.RenderJob, plan stagedag.Plan) (_ stagedag.Executor, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("render runtime: executor source panic: %v", recovered)
		}
	}()
	return source.ResolveExecutor(ctx, job, plan)
}

func safeResolveArtifact(ctx context.Context, resolver ArtifactResolver, job renderapi.RenderJob, plan stagedag.Plan, result stagedag.RunResult) (_ renderapi.RenderArtifact, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("render runtime: artifact resolver panic: %v", recovered)
		}
	}()
	return resolver.ResolveArtifact(ctx, job, plan, result)
}

var _ interface {
	Submit(context.Context, renderapi.RenderJob) (scheduler.JobSnapshot, bool, error)
	Snapshot(string) (scheduler.JobSnapshot, error)
	RequestCancel(context.Context, string) (scheduler.JobSnapshot, bool, error)
} = (*Dispatcher)(nil)
