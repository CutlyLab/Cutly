package stagedag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

type memoryCheckpointStore struct {
	mu     sync.Mutex
	values map[string]Checkpoint
	loads  int
}

type testAttemptExecutor struct {
	legacy  func(context.Context, Task, []Dependency) (StageOutput, error)
	attempt func(context.Context, Task, []Dependency, uint8) (StageOutput, error)
}

func (executor *testAttemptExecutor) Execute(ctx context.Context, task Task, dependencies []Dependency) (StageOutput, error) {
	return executor.legacy(ctx, task, dependencies)
}

func (executor *testAttemptExecutor) ExecuteAttempt(
	ctx context.Context, task Task, dependencies []Dependency, attempt uint8,
) (StageOutput, error) {
	return executor.attempt(ctx, task, dependencies, attempt)
}

func newMemoryCheckpointStore() *memoryCheckpointStore {
	return &memoryCheckpointStore{values: make(map[string]Checkpoint)}
}

func (store *memoryCheckpointStore) Load(ctx context.Context, _ Scope, task Task) (Checkpoint, bool, error) {
	if err := ctx.Err(); err != nil {
		return Checkpoint{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loads++
	value, exists := store.values[task.ID]
	return cloneCheckpoint(value), exists, nil
}

func (store *memoryCheckpointStore) Commit(ctx context.Context, checkpoint Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, exists := store.values[checkpoint.TaskID]; exists {
		left, _ := CanonicalCheckpoint(existing)
		right, _ := CanonicalCheckpoint(checkpoint)
		if string(left) != string(right) {
			return errors.New("checkpoint collision")
		}
		return nil
	}
	store.values[checkpoint.TaskID] = cloneCheckpoint(checkpoint)
	return nil
}

func TestEngineRunsBudgetedDAGAndResumesFromCheckpoints(t *testing.T) {
	plan := testPlan()
	store := newMemoryCheckpointStore()
	var mu sync.Mutex
	running, maximumRunning := 0, 0
	dependencies := make(map[string][]string)
	executor := ExecutorFunc(func(ctx context.Context, task Task, inputs []Dependency) (StageOutput, error) {
		mu.Lock()
		running++
		if running > maximumRunning {
			maximumRunning = running
		}
		for _, dependency := range inputs {
			dependencies[task.ID] = append(dependencies[task.ID], dependency.TaskID)
		}
		mu.Unlock()
		select {
		case <-time.After(2 * time.Millisecond):
		case <-ctx.Done():
			return StageOutput{}, ctx.Err()
		}
		mu.Lock()
		running--
		mu.Unlock()
		return testOutput(t, task.ID), nil
	})
	var eventMu sync.Mutex
	var events []renderapi.ProgressEvent
	engine := Engine{
		Store: store, Executor: executor,
		Sink: renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
			eventMu.Lock()
			defer eventMu.Unlock()
			events = append(events, event)
			return nil
		}),
		Limits: Limits{MaxConcurrent: 2, MaxMemoryBytes: 8, MaxDiskBytes: 8},
	}
	result, err := engine.Run(context.Background(), plan)
	if err != nil || result.Executed != 4 || result.CacheHits != 0 || len(result.Checkpoints) != 4 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if maximumRunning != 2 {
		t.Fatalf("max running = %d", maximumRunning)
	}
	if got := fmt.Sprint(dependencies["d-mux"]); got != "[b-compose c-audio]" {
		t.Fatalf("mux dependency order = %s", got)
	}
	for index, checkpoint := range result.Checkpoints {
		if checkpoint.TaskID != plan.Tasks[index].ID {
			t.Fatalf("checkpoint order[%d] = %s", index, checkpoint.TaskID)
		}
	}

	resume := engine
	resume.Executor = ExecutorFunc(func(context.Context, Task, []Dependency) (StageOutput, error) {
		return StageOutput{}, errors.New("executor must not run on a full checkpoint hit")
	})
	resumed, err := resume.Run(context.Background(), plan)
	if err != nil || resumed.Executed != 0 || resumed.CacheHits != 4 {
		t.Fatalf("resumed = %+v, err = %v", resumed, err)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(events) != 8 || !events[len(events)-1].CacheHit {
		t.Fatalf("events = %+v", events)
	}
}

func TestEngineRetriesOnlyClassifiedFailures(t *testing.T) {
	plan := Plan{
		Profile: PlanProfileV1, JobID: "job-retry", PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		Tasks: []Task{testTask("retry", renderapi.StageDecode, nil, 1, 1, 3)},
	}
	bindTaskCacheKeys(t, &plan)
	store := newMemoryCheckpointStore()
	var attempts atomic.Int32
	engine := Engine{
		Store: store,
		Executor: ExecutorFunc(func(_ context.Context, task Task, _ []Dependency) (StageOutput, error) {
			if attempts.Add(1) == 1 {
				return StageOutput{}, &StageError{Class: renderapi.FailureTransient, Err: errors.New("temporary")}
			}
			return testOutput(t, task.ID), nil
		}),
		Limits: Limits{MaxConcurrent: 1, MaxMemoryBytes: 1, MaxDiskBytes: 1},
	}
	plan.Tasks[0].Retry.RetryTransient = true
	result, err := engine.Run(context.Background(), plan)
	if err != nil || result.Executed != 1 || result.Retries != 1 || attempts.Load() != 2 ||
		len(result.Checkpoints) != 1 || result.Checkpoints[0].AttemptCount != 2 {
		t.Fatalf("result = %+v attempts=%d err=%v", result, attempts.Load(), err)
	}

	plan.JobID = "job-no-retry"
	plan.Tasks[0].ID = "no-retry"
	bindTaskCacheKeys(t, &plan)
	attempts.Store(0)
	engine.Store = newMemoryCheckpointStore()
	engine.Executor = ExecutorFunc(func(context.Context, Task, []Dependency) (StageOutput, error) {
		attempts.Add(1)
		return StageOutput{}, errors.New("unclassified")
	})
	if _, err := engine.Run(context.Background(), plan); !errors.Is(err, ErrExecution) || attempts.Load() != 1 {
		t.Fatalf("unclassified err=%v attempts=%d", err, attempts.Load())
	}
}

func TestEngineAttemptExecutorBindsTheProducingAttempt(t *testing.T) {
	plan := Plan{
		Profile: PlanProfileV1, JobID: "job-attempt-aware", PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		Tasks: []Task{testTask("retry", renderapi.StageDecode, nil, 1, 1, 3)},
	}
	plan.Tasks[0].Retry.RetryTransient = true
	bindTaskCacheKeys(t, &plan)
	var legacyCalls int
	var attempts []uint8
	executor := &testAttemptExecutor{
		legacy: func(context.Context, Task, []Dependency) (StageOutput, error) {
			legacyCalls++
			return StageOutput{}, errors.New("legacy Execute must not be called")
		},
		attempt: func(_ context.Context, task Task, _ []Dependency, attempt uint8) (StageOutput, error) {
			attempts = append(attempts, attempt)
			if attempt == 1 {
				return StageOutput{}, &StageError{Class: renderapi.FailureTransient, Err: errors.New("temporary")}
			}
			receipt, err := canonicaljson.Marshal(map[string]any{"attempt": attempt, "task": task.ID})
			if err != nil {
				return StageOutput{}, err
			}
			return StageOutput{
				ContentHash: testKey("attempt-aware-output").Digest,
				SizeBytes:   int64(attempt),
				Receipt:     receipt,
			}, nil
		},
	}
	engine := Engine{
		Store: newMemoryCheckpointStore(), Executor: executor,
		Limits: Limits{MaxConcurrent: 1, MaxMemoryBytes: 1, MaxDiskBytes: 1},
	}
	result, err := engine.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(attempts); got != "[1 2]" || legacyCalls != 0 ||
		result.Executed != 1 || result.Retries != 1 || len(result.Checkpoints) != 1 {
		t.Fatalf("attempts=%s legacy=%d result=%+v", got, legacyCalls, result)
	}
	checkpoint := result.Checkpoints[0]
	wantReceipt := `{"attempt":2,"task":"retry"}`
	if checkpoint.AttemptCount != 2 || string(checkpoint.Output.Receipt) != wantReceipt || checkpoint.Output.SizeBytes != 2 {
		t.Fatalf("checkpoint = %+v, want receipt %s at attempt 2", checkpoint, wantReceipt)
	}
}

func TestEngineAttemptExecutorPreservesFailureClassification(t *testing.T) {
	tests := []struct {
		name      string
		execute   func(uint8) (StageOutput, error)
		wantError error
		contains  string
	}{
		{
			name: "canceled",
			execute: func(uint8) (StageOutput, error) {
				return StageOutput{}, &StageError{Class: renderapi.FailureTransient, Err: context.Canceled}
			},
			wantError: context.Canceled,
		},
		{
			name: "unclassified",
			execute: func(uint8) (StageOutput, error) {
				return StageOutput{}, errors.New("unclassified")
			},
			wantError: ErrExecution,
		},
		{
			name: "panic",
			execute: func(uint8) (StageOutput, error) {
				panic("attempt executor panic")
			},
			wantError: ErrExecution,
			contains:  "executor panic",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := Plan{
				Profile: PlanProfileV1, JobID: "job-attempt-" + test.name,
				PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
				Tasks: []Task{testTask("retry", renderapi.StageDecode, nil, 1, 1, 3)},
			}
			plan.Tasks[0].Retry.RetryTransient = true
			bindTaskCacheKeys(t, &plan)
			var attempts []uint8
			legacyCalls := 0
			executor := &testAttemptExecutor{
				legacy: func(context.Context, Task, []Dependency) (StageOutput, error) {
					legacyCalls++
					return StageOutput{}, errors.New("legacy Execute must not be called")
				},
				attempt: func(_ context.Context, _ Task, _ []Dependency, attempt uint8) (StageOutput, error) {
					attempts = append(attempts, attempt)
					return test.execute(attempt)
				},
			}
			engine := Engine{
				Store: newMemoryCheckpointStore(), Executor: executor,
				Limits: Limits{MaxConcurrent: 1, MaxMemoryBytes: 1, MaxDiskBytes: 1},
			}
			_, err := engine.Run(context.Background(), plan)
			if !errors.Is(err, test.wantError) || len(attempts) != 1 || attempts[0] != 1 || legacyCalls != 0 {
				t.Fatalf("err=%v attempts=%v legacy=%d", err, attempts, legacyCalls)
			}
			if test.contains != "" && !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error %q does not contain %q", err, test.contains)
			}
		})
	}
}

func TestEngineFailsClosedForBudgetCheckpointAndCancellation(t *testing.T) {
	plan := Plan{
		Profile: PlanProfileV1, JobID: "job-limits", PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		Tasks: []Task{testTask("large", renderapi.StageCompose, nil, 9, 1, 1)},
	}
	bindTaskCacheKeys(t, &plan)
	engine := Engine{
		Store: newMemoryCheckpointStore(),
		Executor: ExecutorFunc(func(context.Context, Task, []Dependency) (StageOutput, error) {
			return testOutput(t, "large"), nil
		}),
		Limits: Limits{MaxConcurrent: 1, MaxMemoryBytes: 8, MaxDiskBytes: 8},
	}
	if _, err := engine.Run(context.Background(), plan); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget error = %v", err)
	}

	plan.Tasks[0].Resources.MemoryBytes = 1
	badStore := newMemoryCheckpointStore()
	badStore.values["large"] = Checkpoint{Profile: CheckpointProfileV1, TaskID: "other"}
	engine.Store = badStore
	if _, err := engine.Run(context.Background(), plan); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("checkpoint error = %v", err)
	}

	engine.Store = newMemoryCheckpointStore()
	started := make(chan struct{})
	engine.Executor = ExecutorFunc(func(ctx context.Context, _ Task, _ []Dependency) (StageOutput, error) {
		close(started)
		<-ctx.Done()
		return StageOutput{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, plan)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestPlanAndCheckpointValidation(t *testing.T) {
	plan := testPlan()
	if err := ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	unsorted := plan
	unsorted.Tasks = append([]Task(nil), plan.Tasks...)
	unsorted.Tasks[0], unsorted.Tasks[1] = unsorted.Tasks[1], unsorted.Tasks[0]
	if err := ValidatePlan(unsorted); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("unsorted = %v", err)
	}
	cycle := plan
	cycle.Tasks = append([]Task(nil), plan.Tasks...)
	cycle.Tasks[0].DependsOn = []string{"d-mux"}
	if err := ValidatePlan(cycle); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("cycle = %v", err)
	}
	backward := Plan{
		Profile: PlanProfileV1, JobID: "job-backward", PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		Tasks: []Task{
			testTask("a-publish", renderapi.StagePublish, nil, 1, 1, 1),
			testTask("b-resolve", renderapi.StageResolve, []string{"a-publish"}, 1, 1, 1),
		},
	}
	bindTaskCacheKeys(t, &backward)
	if err := ValidatePlan(backward); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("backward stage edge = %v", err)
	}
	cacheMismatch := plan
	cacheMismatch.Tasks = append([]Task(nil), plan.Tasks...)
	cacheMismatch.Tasks[0] = cloneTask(cacheMismatch.Tasks[0])
	cacheMismatch.Tasks[0].CacheInput.PlanFingerprint = digest('c')
	if err := ValidatePlan(cacheMismatch); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("cache preimage mismatch = %v", err)
	}
	semanticMismatch := plan
	semanticMismatch.Tasks = append([]Task(nil), plan.Tasks...)
	semanticMismatch.Tasks[0] = cloneTask(semanticMismatch.Tasks[0])
	semanticMismatch.Tasks[0].CacheInput.PlanFingerprint = digest('c')
	semanticMismatch.Tasks[0].CacheKey = mustCacheKey(t, semanticMismatch.Tasks[0].CacheInput)
	if err := ValidatePlan(semanticMismatch); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("semantic plan binding mismatch = %v", err)
	}
	dependencyMismatch := plan
	dependencyMismatch.Tasks = cloneTasks(plan.Tasks)
	dependencyMismatch.Tasks[1].CacheInput.Inputs[0].ContentHash = digest('d')
	dependencyMismatch.Tasks[1].CacheKey = mustCacheKey(t, dependencyMismatch.Tasks[1].CacheInput)
	if err := ValidatePlan(dependencyMismatch); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("dependency cache binding mismatch = %v", err)
	}
	stageMismatch := plan
	stageMismatch.Tasks = cloneTasks(plan.Tasks)
	stageMismatch.Tasks[0].Stage = renderapi.StagePublish
	if err := ValidatePlan(stageMismatch); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("stage cache binding mismatch = %v", err)
	}
	output := testOutput(t, plan.Tasks[0].ID)
	output.Receipt = []byte(" {\"ok\":true}")
	if _, err := NewCheckpoint(plan, plan.Tasks[0], output, 1); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("noncanonical receipt = %v", err)
	}
	historicalPlan := plan
	historicalPlan.Tasks = cloneTasks(plan.Tasks)
	historicalPlan.Tasks[0].Retry.MaxAttempts = 3
	historicalTask := historicalPlan.Tasks[0]
	historical, err := NewCheckpoint(historicalPlan, historicalTask, testOutput(t, historicalTask.ID), 3)
	if err != nil {
		t.Fatal(err)
	}
	reducedPolicyPlan := historicalPlan
	reducedPolicyPlan.Tasks = cloneTasks(historicalPlan.Tasks)
	reducedPolicyPlan.Tasks[0].Retry.MaxAttempts = 1
	reducedPolicyTask := reducedPolicyPlan.Tasks[0]
	if err := ValidateCheckpoint(reducedPolicyPlan, reducedPolicyTask, historical); err != nil {
		t.Fatalf("historical attempt telemetry must not invalidate semantic output: %v", err)
	}
	if _, err := NewCheckpoint(reducedPolicyPlan, reducedPolicyTask, testOutput(t, reducedPolicyTask.ID), 2); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("new checkpoint above current retry policy = %v", err)
	}
	foreignTask := cloneTask(plan.Tasks[0])
	foreignTask.Resources.MemoryBytes++
	if _, err := NewCheckpoint(plan, foreignTask, testOutput(t, foreignTask.ID), 1); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("foreign task checkpoint = %v", err)
	}
}

func TestAcyclicValidatorUsesBoundedIterativeTraversal(t *testing.T) {
	const count = 10_000
	tasks := make([]Task, count)
	byID := make(map[string]int, count)
	for index := range tasks {
		id := fmt.Sprintf("task-%05d", index)
		tasks[index].ID = id
		byID[id] = index
		if index > 0 {
			tasks[index].DependsOn = []string{tasks[index-1].ID}
		}
	}
	if err := validateAcyclic(tasks, byID); err != nil {
		t.Fatal(err)
	}
	tasks[0].DependsOn = []string{tasks[count-1].ID}
	if err := validateAcyclic(tasks, byID); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("cycle = %v", err)
	}
}

func testPlan() Plan {
	plan := Plan{
		Profile: PlanProfileV1, JobID: "job-dag", PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		Tasks: []Task{
			testTask("a-decode", renderapi.StageDecode, []string{}, 4, 4, 1),
			testTask("b-compose", renderapi.StageCompose, []string{"a-decode"}, 4, 4, 1),
			testTask("c-audio", renderapi.StageAudio, []string{}, 4, 4, 1),
			testTask("d-mux", renderapi.StageMux, []string{"b-compose", "c-audio"}, 4, 4, 1),
		},
	}
	bindTaskCacheKeys(nil, &plan)
	return plan
}

func testTask(id string, stage renderapi.Stage, dependencies []string, memory, disk uint64, attempts uint8) Task {
	if dependencies == nil {
		dependencies = []string{}
	}
	return Task{
		ID: id, Stage: stage, Profile: "cutly.stage.test.v1", DependsOn: dependencies,
		Resources: ResourceEstimate{MemoryBytes: memory, DiskBytes: disk},
		Retry:     RetryPolicy{MaxAttempts: attempts},
	}
}

func bindTaskCacheKeys(t *testing.T, plan *Plan) {
	if t != nil {
		t.Helper()
	}
	byID := make(map[string]cachekey.Key, len(plan.Tasks))
	for index := range plan.Tasks {
		task := &plan.Tasks[index]
		inputs := make([]cachekey.NormalizedInput, len(task.DependsOn)+1)
		for dependencyIndex, dependency := range task.DependsOn {
			key, exists := byID[dependency]
			if !exists {
				panic("test cache-key helper requires dependencies before dependents")
			}
			input, err := DependencyBindingInput(dependencyIndex, key)
			if err != nil {
				if t != nil {
					t.Fatal(err)
				}
				panic(err)
			}
			inputs[dependencyIndex] = input
		}
		stageInput, err := StageBindingInput(task.Stage)
		if err != nil {
			if t != nil {
				t.Fatal(err)
			}
			panic(err)
		}
		inputs[len(inputs)-1] = stageInput
		task.CacheInput = cachekey.Input{
			SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
			Namespace: "cutly.cache.test.v1", ArtifactKind: "cutly.chunk.test.v1",
			PlanFingerprint: plan.PlanFingerprint, ExecutionManifestFingerprint: plan.ExecutionManifestFingerprint,
			StageProfile: task.Profile, Inputs: inputs,
			FrameRange: cacheRange(task.FrameRange), SampleRange: cacheRange(task.SampleRange),
			OutputProfile: "cutly.output.test.v1",
		}
		key, err := cachekey.New(task.CacheInput)
		if err != nil {
			if t != nil {
				t.Fatal(err)
			}
			panic(err)
		}
		task.CacheKey = key
		byID[task.ID] = key
	}
}

func mustCacheKey(t *testing.T, input cachekey.Input) cachekey.Key {
	t.Helper()
	value, err := cachekey.New(input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func cacheRange(value *videoedit.TickRange) *cachekey.IndexRange {
	if value == nil {
		return nil
	}
	return &cachekey.IndexRange{Start: value.First, Count: value.Count}
}

func cloneTasks(values []Task) []Task {
	result := make([]Task, len(values))
	for index, value := range values {
		result[index] = cloneTask(value)
	}
	return result
}

func testKey(id string) cachekey.Key {
	sum := sha256.Sum256([]byte(id))
	return cachekey.Key{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: "cutly.cache.test.v1", ArtifactKind: "cutly.chunk.test.v1",
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}
}

func testOutput(t *testing.T, id string) StageOutput {
	t.Helper()
	receipt, err := canonicaljson.Marshal(map[string]any{"task": id})
	if err != nil {
		t.Fatal(err)
	}
	return StageOutput{ContentHash: testKey("output:" + id).Digest, SizeBytes: int64(len(id)), Receipt: receipt}
}

func digest(fill byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = fill
	}
	return "sha256:" + string(value)
}
