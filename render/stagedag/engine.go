package stagedag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const MaxConcurrentTasks = 1024

// Scope contains only the immutable scalar identities a checkpoint store needs
// to locate a task checkpoint. It deliberately excludes the plan's task slice.
type Scope struct {
	JobID                        string
	PlanFingerprint              string
	ExecutionManifestFingerprint string
}

type CheckpointStore interface {
	// Load returns found=true only for a structurally verified stored value whose
	// referenced output is still present and integrity-checked. Corrupt or stale
	// checkpoint/artifact state must be quarantined and reported as an error or
	// a clean miss; the engine still verifies plan/task binding before reuse.
	// The returned checkpoint must be fully owned by the caller.
	Load(context.Context, Scope, Task) (checkpoint Checkpoint, found bool, err error)
	// Commit is idempotent for an identical checkpoint and no-replace for a
	// conflicting value. Implementations must be safe for concurrent Load and
	// Commit calls from one or more Engine runs.
	Commit(context.Context, Checkpoint) error
}

// Executor implementations must be safe for concurrent Execute calls. A
// successful call has already published and integrity-checked Output in the
// CAS or other immutable store described by the returned receipt.
type Executor interface {
	Execute(context.Context, Task, []Dependency) (StageOutput, error)
}

// AttemptExecutor is an additive extension for executors whose output receipt
// must bind the 1-based attempt that produced it. Engine prefers
// ExecuteAttempt when its Executor implements this interface; legacy
// executors continue to receive Execute calls with identical retry semantics.
// Implementations must be safe for concurrent calls to both methods.
type AttemptExecutor interface {
	Executor
	ExecuteAttempt(context.Context, Task, []Dependency, uint8) (StageOutput, error)
}

type ExecutorFunc func(context.Context, Task, []Dependency) (StageOutput, error)

func (fn ExecutorFunc) Execute(ctx context.Context, task Task, dependencies []Dependency) (StageOutput, error) {
	return fn(ctx, task, dependencies)
}

// Dependency is ordered exactly as Task.DependsOn. Executors never need to
// range over a Go map to derive a cache or output identity.
type Dependency struct {
	TaskID     string
	Checkpoint Checkpoint
}

// StageError classifies an executor failure for the task's explicit retry
// policy. Unknown errors are non-retryable.
type StageError struct {
	Class renderapi.FailureClass
	Err   error
}

func (value *StageError) Error() string {
	if value == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %v", value.Class, value.Err)
}

func (value *StageError) Unwrap() error {
	if value == nil {
		return nil
	}
	return value.Err
}

type Engine struct {
	Store    CheckpointStore
	Executor Executor
	Sink     renderapi.EventSink
	Limits   Limits
}

type RunResult struct {
	Checkpoints []Checkpoint
	CacheHits   int
	Executed    int
	Retries     int
}

type taskState struct {
	checked    bool
	running    bool
	done       bool
	checkpoint Checkpoint
}

type taskResult struct {
	index      int
	checkpoint Checkpoint
	attempts   uint8
	err        error
}

func (engine Engine) Run(ctx context.Context, plan Plan) (RunResult, error) {
	if ctx == nil || engine.Store == nil || engine.Executor == nil {
		return RunResult{}, fmt.Errorf("%w: context, checkpoint store and executor are required", ErrExecution)
	}
	if engine.Limits.MaxConcurrent <= 0 || engine.Limits.MaxConcurrent > MaxConcurrentTasks ||
		engine.Limits.MaxMemoryBytes == 0 || engine.Limits.MaxDiskBytes == 0 {
		return RunResult{}, fmt.Errorf("%w: invalid execution limits", ErrBudget)
	}
	frozen, err := freezePlan(plan)
	if err != nil {
		return RunResult{}, err
	}
	for _, task := range frozen.Tasks {
		if task.Resources.MemoryBytes > engine.Limits.MaxMemoryBytes || task.Resources.DiskBytes > engine.Limits.MaxDiskBytes {
			return RunResult{}, fmt.Errorf("%w: task %q requests memory=%d disk=%d", ErrBudget, task.ID, task.Resources.MemoryBytes, task.Resources.DiskBytes)
		}
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	states := make([]taskState, len(frozen.Tasks))
	scope := Scope{
		JobID: frozen.JobID, PlanFingerprint: frozen.PlanFingerprint,
		ExecutionManifestFingerprint: frozen.ExecutionManifestFingerprint,
	}
	byID := make(map[string]int, len(frozen.Tasks))
	for index, task := range frozen.Tasks {
		byID[task.ID] = index
	}
	remainingDependencies := make([]int, len(frozen.Tasks))
	dependents := make([][]int, len(frozen.Tasks))
	ready := make([]int, 0, len(frozen.Tasks))
	for index, task := range frozen.Tasks {
		remainingDependencies[index] = len(task.DependsOn)
		if len(task.DependsOn) == 0 {
			ready = append(ready, index)
		}
		for _, dependency := range task.DependsOn {
			dependencyIndex := byID[dependency]
			dependents[dependencyIndex] = append(dependents[dependencyIndex], index)
		}
	}
	results := make(chan taskResult, engine.Limits.MaxConcurrent)
	running := 0
	done := 0
	usedMemory := uint64(0)
	usedDisk := uint64(0)
	sequence := uint64(0)
	runResult := RunResult{Checkpoints: make([]Checkpoint, len(frozen.Tasks))}

	emit := func(task Task, cacheHit bool) error {
		if engine.Sink == nil {
			return nil
		}
		sequence++
		unit, completed, total := taskProgress(task)
		return safeEmit(engine.Sink, runCtx, renderapi.ProgressEvent{
			JobID: frozen.JobID, Sequence: sequence, Stage: task.Stage, State: renderapi.JobRunning,
			Unit: unit, Completed: completed, Total: total,
			FrameRange: cloneRange(task.FrameRange), SampleRange: cloneRange(task.SampleRange), CacheHit: cacheHit,
		})
	}
	unlock := func(index int, destination *[]int) error {
		for _, dependent := range dependents[index] {
			if remainingDependencies[dependent] <= 0 {
				return fmt.Errorf("%w: dependency counter underflow for task %q", ErrExecution, frozen.Tasks[dependent].ID)
			}
			remainingDependencies[dependent]--
			if remainingDependencies[dependent] == 0 {
				*destination = append(*destination, dependent)
			}
		}
		return nil
	}

	for done < len(frozen.Tasks) {
		if err := runCtx.Err(); err != nil {
			cancel()
			drainResults(results, running)
			return RunResult{}, err
		}
		madeProgress := false
		candidates := ready
		ready = nil
		for cursor := 0; cursor < len(candidates); cursor++ {
			index := candidates[cursor]
			task := frozen.Tasks[index]
			state := &states[index]
			if state.done || state.running || remainingDependencies[index] != 0 {
				cancel()
				drainResults(results, running)
				return RunResult{}, fmt.Errorf("%w: invalid ready state for task %q", ErrExecution, task.ID)
			}
			if running >= engine.Limits.MaxConcurrent {
				ready = append(ready, candidates[cursor:]...)
				break
			}
			if !state.checked {
				if err := runCtx.Err(); err != nil {
					cancel()
					drainResults(results, running)
					return RunResult{}, err
				}
				checkpoint, found, loadErr := safeLoad(engine.Store, runCtx, scope, cloneTask(task))
				if loadErr != nil {
					cancel()
					drainResults(results, running)
					if err := ctx.Err(); err != nil {
						return RunResult{}, err
					}
					return RunResult{}, fmt.Errorf("%w: load task %q: %w", ErrCheckpointStore, task.ID, loadErr)
				}
				state.checked = true
				if found {
					checkpoint = cloneCheckpoint(checkpoint)
					if err := validateCheckpointBinding(frozen, task, checkpoint); err != nil {
						cancel()
						drainResults(results, running)
						return RunResult{}, err
					}
					state.checkpoint = cloneCheckpoint(checkpoint)
					state.done = true
					done++
					runResult.CacheHits++
					runResult.Checkpoints[index] = cloneCheckpoint(checkpoint)
					if err := emit(task, true); err != nil {
						cancel()
						drainResults(results, running)
						return RunResult{}, err
					}
					if err := unlock(index, &candidates); err != nil {
						cancel()
						drainResults(results, running)
						return RunResult{}, err
					}
					madeProgress = true
					continue
				}
			}
			if running >= engine.Limits.MaxConcurrent ||
				task.Resources.MemoryBytes > engine.Limits.MaxMemoryBytes-usedMemory ||
				task.Resources.DiskBytes > engine.Limits.MaxDiskBytes-usedDisk {
				ready = append(ready, index)
				continue
			}
			dependencies := dependencyCheckpoints(task, states, byID)
			state.running = true
			running++
			usedMemory += task.Resources.MemoryBytes
			usedDisk += task.Resources.DiskBytes
			madeProgress = true
			go engine.executeTask(runCtx, frozen, index, task, dependencies, results)
		}
		if len(ready) == 0 {
			// No blocked candidate retained the next queue. Reuse the now
			// exhausted candidate storage so long dependency chains remain
			// O(tasks) in both inspections and queue allocations.
			ready = candidates[:0]
		}

		if done == len(frozen.Tasks) {
			break
		}
		if running == 0 {
			if madeProgress {
				continue
			}
			return RunResult{}, fmt.Errorf("%w: no runnable task fits the declared limits", ErrBudget)
		}

		var completed taskResult
		select {
		case completed = <-results:
		case <-ctx.Done():
			cancel()
			drainResults(results, running)
			return RunResult{}, ctx.Err()
		}
		task := frozen.Tasks[completed.index]
		state := &states[completed.index]
		state.running = false
		running--
		usedMemory -= task.Resources.MemoryBytes
		usedDisk -= task.Resources.DiskBytes
		if completed.err != nil {
			cancel()
			drainResults(results, running)
			return RunResult{}, fmt.Errorf("%w: task %q: %w", ErrExecution, task.ID, completed.err)
		}
		state.done = true
		state.checkpoint = cloneCheckpoint(completed.checkpoint)
		done++
		runResult.Executed++
		runResult.Retries += int(completed.attempts) - 1
		runResult.Checkpoints[completed.index] = cloneCheckpoint(completed.checkpoint)
		if err := emit(task, false); err != nil {
			cancel()
			drainResults(results, running)
			return RunResult{}, err
		}
		if err := unlock(completed.index, &ready); err != nil {
			cancel()
			drainResults(results, running)
			return RunResult{}, err
		}
	}
	return cloneRunResult(runResult), nil
}

func (engine Engine) executeTask(
	ctx context.Context,
	plan Plan,
	index int,
	task Task,
	dependencies []Dependency,
	results chan<- taskResult,
) {
	result := taskResult{index: index}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.err = fmt.Errorf("executor panic: %v\n%s", recovered, debug.Stack())
		}
		results <- result
	}()
	for attempt := uint8(1); attempt <= task.Retry.MaxAttempts; attempt++ {
		result.attempts = attempt
		if err := ctx.Err(); err != nil {
			result.err = err
			return
		}
		var output StageOutput
		var err error
		if executor, ok := engine.Executor.(AttemptExecutor); ok {
			output, err = executor.ExecuteAttempt(ctx, cloneTask(task), cloneDependencies(dependencies), attempt)
		} else {
			output, err = engine.Executor.Execute(ctx, cloneTask(task), cloneDependencies(dependencies))
		}
		if err != nil {
			if !retryable(task.Retry, err, attempt) {
				result.err = err
				return
			}
			continue
		}
		checkpoint, err := newCheckpointBound(plan, task, output, attempt)
		if err != nil {
			result.err = err
			return
		}
		if err := safeCommit(engine.Store, ctx, cloneCheckpoint(checkpoint)); err != nil {
			result.err = fmt.Errorf("%w: commit task %q: %w", ErrCheckpointStore, task.ID, err)
			return
		}
		result.checkpoint = checkpoint
		return
	}
	result.err = errors.New("retry loop exhausted")
}

func retryable(policy RetryPolicy, err error, attempt uint8) bool {
	if attempt >= policy.MaxAttempts || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified *StageError
	if !errors.As(err, &classified) || classified == nil {
		return false
	}
	switch classified.Class {
	case renderapi.FailureTransient:
		return policy.RetryTransient
	case renderapi.FailureResource:
		return policy.RetryResource
	default:
		return false
	}
}

func safeLoad(store CheckpointStore, ctx context.Context, scope Scope, task Task) (
	checkpoint Checkpoint,
	found bool,
	err error,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			checkpoint = Checkpoint{}
			found = false
			err = fmt.Errorf("checkpoint load panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return store.Load(ctx, scope, task)
}

func safeCommit(store CheckpointStore, ctx context.Context, checkpoint Checkpoint) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("checkpoint commit panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return store.Commit(ctx, checkpoint)
}

func safeEmit(sink renderapi.EventSink, ctx context.Context, event renderapi.ProgressEvent) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("progress sink panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return sink.Emit(ctx, event)
}

func freezePlan(plan Plan) (Plan, error) {
	canonical, err := CanonicalPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	var frozen Plan
	if err := json.Unmarshal(canonical, &frozen); err != nil {
		return Plan{}, fmt.Errorf("%w: freeze: %v", ErrPlanInvalid, err)
	}
	return frozen, nil
}

func dependencyCheckpoints(task Task, states []taskState, byID map[string]int) []Dependency {
	result := make([]Dependency, len(task.DependsOn))
	for index, dependency := range task.DependsOn {
		result[index] = Dependency{TaskID: dependency, Checkpoint: cloneCheckpoint(states[byID[dependency]].checkpoint)}
	}
	return result
}

func drainResults(results <-chan taskResult, count int) {
	for index := 0; index < count; index++ {
		<-results
	}
}

func taskProgress(task Task) (renderapi.ProgressUnit, int64, int64) {
	if task.FrameRange != nil {
		return renderapi.ProgressFrames, task.FrameRange.Count, task.FrameRange.Count
	}
	if task.SampleRange != nil {
		return renderapi.ProgressSamples, task.SampleRange.Count, task.SampleRange.Count
	}
	return renderapi.ProgressItems, 1, 1
}

func cloneTask(value Task) Task {
	value.DependsOn = cloneStrings(value.DependsOn)
	value.CacheInput.Inputs = cloneNormalizedInputs(value.CacheInput.Inputs)
	value.CacheInput.FrameRange = cloneCacheRange(value.CacheInput.FrameRange)
	value.CacheInput.SampleRange = cloneCacheRange(value.CacheInput.SampleRange)
	value.FrameRange = cloneRange(value.FrameRange)
	value.SampleRange = cloneRange(value.SampleRange)
	return value
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func cloneNormalizedInputs(values []cachekey.NormalizedInput) []cachekey.NormalizedInput {
	if values == nil {
		return nil
	}
	result := make([]cachekey.NormalizedInput, len(values))
	copy(result, values)
	return result
}

func cloneCacheRange(value *cachekey.IndexRange) *cachekey.IndexRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneCheckpoint(value Checkpoint) Checkpoint {
	value.FrameRange = cloneRange(value.FrameRange)
	value.SampleRange = cloneRange(value.SampleRange)
	value.Output = cloneOutput(value.Output)
	return value
}

func cloneDependencies(values []Dependency) []Dependency {
	result := make([]Dependency, len(values))
	for index, value := range values {
		result[index] = Dependency{TaskID: value.TaskID, Checkpoint: cloneCheckpoint(value.Checkpoint)}
	}
	return result
}

func cloneRunResult(value RunResult) RunResult {
	result := value
	result.Checkpoints = make([]Checkpoint, len(value.Checkpoints))
	for index, checkpoint := range value.Checkpoints {
		result.Checkpoints[index] = cloneCheckpoint(checkpoint)
	}
	return result
}
