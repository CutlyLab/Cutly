package stagedag

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestStrictPlanAndCheckpointCodecs(t *testing.T) {
	plan := testPlan()
	planRaw, err := CanonicalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	decodedPlan, err := DecodePlan(append([]byte(" \n"), planRaw...))
	if err != nil || decodedPlan.JobID != plan.JobID || len(decodedPlan.Tasks) != len(plan.Tasks) {
		t.Fatalf("decoded plan = %+v, err = %v", decodedPlan, err)
	}
	unknown := append([]byte(`{"unknown":0,`), planRaw[1:]...)
	if _, err := DecodePlan(unknown); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("unknown plan member = %v", err)
	}
	duplicate := append([]byte(`{"profile":"cutly.render.stage-dag.v1",`), planRaw[1:]...)
	if _, err := DecodePlan(duplicate); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("duplicate plan member = %v", err)
	}
	deep := []byte(`{"unknown":` + strings.Repeat("[", 1000) + "0" + strings.Repeat("]", 1000) + "}")
	if _, err := DecodePlan(deep); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("deep plan = %v", err)
	}

	checkpoint, err := NewCheckpoint(plan, plan.Tasks[0], testOutput(t, plan.Tasks[0].ID), 1)
	if err != nil {
		t.Fatal(err)
	}
	checkpointRaw, err := CanonicalCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	decodedCheckpoint, err := DecodeCheckpoint(checkpointRaw)
	if err != nil || decodedCheckpoint.TaskID != checkpoint.TaskID {
		t.Fatalf("decoded checkpoint = %+v, err = %v", decodedCheckpoint, err)
	}
	if _, err := DecodeCheckpoint(append(checkpointRaw, '\n')); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("noncanonical checkpoint = %v", err)
	}
	checkpointDuplicate := append([]byte(`{"profile":"cutly.render.stage-checkpoint.v1",`), checkpointRaw[1:]...)
	if _, err := DecodeCheckpoint(checkpointDuplicate); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("duplicate checkpoint = %v", err)
	}
}

func TestTypedPlanRejectsInvalidUnicodeIdentity(t *testing.T) {
	plan := testPlan()
	plan.JobID = string([]byte{0xff})
	if err := ValidatePlan(plan); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("invalid unicode identity = %v", err)
	}
	for _, identity := range []string{" leading", "trailing\n", "log\ninjection", "internal space", "x\u2028y", "x\u202ey", "\u00a0"} {
		plan = testPlan()
		plan.JobID = identity
		if err := ValidatePlan(plan); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("noncanonical identity %q = %v", identity, err)
		}
	}
}

type checkpointStoreFuncs struct {
	load   func(context.Context, Scope, Task) (Checkpoint, bool, error)
	commit func(context.Context, Checkpoint) error
}

func (store checkpointStoreFuncs) Load(ctx context.Context, scope Scope, task Task) (Checkpoint, bool, error) {
	return store.load(ctx, scope, task)
}

func (store checkpointStoreFuncs) Commit(ctx context.Context, checkpoint Checkpoint) error {
	return store.commit(ctx, checkpoint)
}

func TestEngineContainsCollaboratorPanicsAndOwnsValues(t *testing.T) {
	plan := testPlan()
	limits := Limits{MaxConcurrent: 1, MaxMemoryBytes: 8, MaxDiskBytes: 8}
	loadPanic := checkpointStoreFuncs{
		load:   func(context.Context, Scope, Task) (Checkpoint, bool, error) { panic("load") },
		commit: func(context.Context, Checkpoint) error { return nil },
	}
	engine := Engine{
		Store: loadPanic, Executor: ExecutorFunc(func(context.Context, Task, []Dependency) (StageOutput, error) {
			return StageOutput{}, errors.New("must not execute")
		}),
		Limits: limits,
	}
	if _, err := engine.Run(context.Background(), plan); !errors.Is(err, ErrCheckpointStore) {
		t.Fatalf("load panic = %v", err)
	}

	one := Plan{
		Profile: PlanProfileV1, JobID: "owned", PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		Tasks: []Task{testTask("owned", "compose", nil, 1, 1, 1)},
	}
	bindTaskCacheKeys(t, &one)
	wantStageInput := one.Tasks[0].CacheInput.Inputs[0]
	ownershipStore := checkpointStoreFuncs{
		load: func(_ context.Context, _ Scope, task Task) (Checkpoint, bool, error) {
			task.CacheInput.Inputs[0].ContentHash = digest('f')
			task.DependsOn = append(task.DependsOn, "mutated")
			return Checkpoint{}, false, nil
		},
		commit: func(_ context.Context, checkpoint Checkpoint) error {
			checkpoint.Output.Receipt[0] = '['
			return nil
		},
	}
	engine = Engine{
		Store: ownershipStore,
		Executor: ExecutorFunc(func(_ context.Context, task Task, _ []Dependency) (StageOutput, error) {
			if task.CacheInput.Inputs[0] != wantStageInput || len(task.DependsOn) != 0 {
				return StageOutput{}, errors.New("store mutation escaped")
			}
			task.CacheInput.Inputs[0].ContentHash = digest('e')
			return testOutput(t, task.ID), nil
		}),
		Limits: limits,
	}
	result, err := engine.Run(context.Background(), one)
	if err != nil || len(result.Checkpoints) != 1 {
		t.Fatalf("owned run = %+v, err = %v", result, err)
	}
	if err := ValidateCheckpoint(one, one.Tasks[0], result.Checkpoints[0]); err != nil {
		t.Fatalf("commit mutation escaped: %v", err)
	}
	if !bytes.Equal(result.Checkpoints[0].Output.Receipt, []byte(`{"task":"owned"}`)) {
		t.Fatalf("receipt mutated: %s", result.Checkpoints[0].Output.Receipt)
	}

	commitPanic := checkpointStoreFuncs{
		load:   func(context.Context, Scope, Task) (Checkpoint, bool, error) { return Checkpoint{}, false, nil },
		commit: func(context.Context, Checkpoint) error { panic("commit") },
	}
	engine.Store = commitPanic
	if _, err := engine.Run(context.Background(), one); !errors.Is(err, ErrCheckpointStore) || !errors.Is(err, ErrExecution) {
		t.Fatalf("commit panic = %v", err)
	}

	engine.Store = newMemoryCheckpointStore()
	engine.Sink = eventSinkPanic{}
	if result, err := engine.Run(context.Background(), one); err == nil || len(result.Checkpoints) != 0 {
		t.Fatalf("sink panic result = %+v, err = %v", result, err)
	}
}

type eventSinkPanic struct{}

func (eventSinkPanic) Emit(context.Context, renderapi.ProgressEvent) error { panic("sink") }

func FuzzDecodePlan(f *testing.F) {
	seed, err := CanonicalPlan(testPlan())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = DecodePlan(raw)
	})
}

func FuzzDecodeCheckpoint(f *testing.F) {
	plan := testPlan()
	receipt := []byte(`{"task":"a-decode"}`)
	checkpoint, err := NewCheckpoint(plan, plan.Tasks[0], StageOutput{
		ContentHash: testKey("fuzz-output").Digest, SizeBytes: 1, Receipt: receipt,
	}, 1)
	if err != nil {
		f.Fatal(err)
	}
	seed, err := CanonicalCheckpoint(checkpoint)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = DecodeCheckpoint(raw)
	})
}
