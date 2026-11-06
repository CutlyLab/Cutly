package chunkplan

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestSelectMissingValidatesCheckpointReceiptAndCAS(t *testing.T) {
	plan := mustPlan(t, testJob("job-resume", 65, 65_537))
	checkpoints := make([]stagedag.Checkpoint, len(plan.Tasks))
	for index, task := range plan.Tasks {
		receipt := mustReceipt(t, task, 1)
		output, err := StageOutput(receipt)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := stagedag.NewCheckpoint(plan.Plan, task, output, 1)
		if err != nil {
			t.Fatal(err)
		}
		checkpoints[index] = checkpoint
	}
	available := make(map[cachekey.Key]bool, len(plan.Tasks))
	for _, task := range plan.Tasks {
		available[task.CacheKey] = true
	}
	check := ArtifactAvailabilityFunc(func(_ context.Context, key cachekey.Key, hash string, size int64) (bool, error) {
		if !available[key] || hash == "" || size <= 0 {
			return false, nil
		}
		return true, nil
	})
	selection, err := SelectMissing(context.Background(), plan, checkpoints, check)
	if err != nil || len(selection.Ready) != len(plan.Tasks) || len(selection.Missing) != 0 || len(selection.Rejected) != 0 {
		t.Fatalf("complete selection = %+v, %v", selection, err)
	}

	// One absent CAS object is isolated to one exact task.
	missingIndex := len(plan.Tasks) - 1
	available[plan.Tasks[missingIndex].CacheKey] = false
	selection, err = SelectMissing(context.Background(), plan, checkpoints, check)
	if err != nil || len(selection.Ready) != len(plan.Tasks)-1 || len(selection.Missing) != 1 ||
		selection.Missing[0].ID != plan.Tasks[missingIndex].ID ||
		!reflect.DeepEqual(selection.Rejected, []Rejection{{TaskID: plan.Tasks[missingIndex].ID, Code: RejectionArtifactMissing}}) {
		t.Fatalf("missing CAS selection = %+v, %v", selection, err)
	}

	// Caller mutation cannot alter the returned owned task.
	selection.Missing[0].CacheInput.Inputs[0].ContentHash = digest("mutated")
	if plan.Tasks[missingIndex].CacheInput.Inputs[0].ContentHash == selection.Missing[0].CacheInput.Inputs[0].ContentHash {
		t.Fatal("selection aliases plan task")
	}
}

func TestSelectMissingIsolatesInvalidDuplicateAndUnknownCandidates(t *testing.T) {
	plan := mustPlan(t, testJob("job-reject", 65, 0))
	valid := makeCheckpoint(t, plan, 0)
	badBinding := makeCheckpoint(t, plan, 1)
	badBinding.JobID = "other-job"
	unknown := valid
	unknown.TaskID = "foreign"
	candidates := []stagedag.Checkpoint{valid, valid, badBinding, unknown}
	selection, err := SelectMissing(context.Background(), plan, candidates,
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) { return true, nil }))
	if err != nil || len(selection.Ready) != 0 || len(selection.Missing) != 2 {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
	want := []Rejection{
		{TaskID: "foreign", Code: RejectionUnknownTask},
		{TaskID: plan.Tasks[0].ID, Code: RejectionDuplicateTask},
		{TaskID: plan.Tasks[1].ID, Code: RejectionCheckpointInvalid},
	}
	if !reflect.DeepEqual(selection.Rejected, want) {
		t.Fatalf("rejected = %+v, want %+v", selection.Rejected, want)
	}
}

func TestSelectMissingRejectsReceiptDriftAndFailsZeroOnUnknownAvailability(t *testing.T) {
	plan := mustPlan(t, testJob("job-drift", 1, 0))
	checkpoint := makeCheckpoint(t, plan, 0)
	checkpoint.Output.ContentHash = digest("different-payload")
	selection, err := SelectMissing(context.Background(), plan, []stagedag.Checkpoint{checkpoint},
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) { return true, nil }))
	if err != nil || len(selection.Missing) != 1 || len(selection.Rejected) != 1 ||
		selection.Rejected[0].Code != RejectionReceiptBinding {
		t.Fatalf("receipt drift = %+v, %v", selection, err)
	}

	checkpoint = makeCheckpoint(t, plan, 0)
	checkpoint.Output.Receipt = bytes.Replace(checkpoint.Output.Receipt,
		[]byte(`"fingerprint":"sha256:`), []byte(`"fingerprint":"sha256:f`), 1)
	selection, err = SelectMissing(context.Background(), plan, []stagedag.Checkpoint{checkpoint},
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) { return true, nil }))
	if err != nil || len(selection.Missing) != 1 || len(selection.Rejected) != 1 ||
		selection.Rejected[0].Code != RejectionReceiptInvalid {
		t.Fatalf("corrupt receipt = %+v, %v", selection, err)
	}

	checkpoint = makeCheckpoint(t, plan, 0)
	selection, err = SelectMissing(context.Background(), plan, []stagedag.Checkpoint{checkpoint},
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) {
			return false, errors.New("private CAS error")
		}))
	if !errors.Is(err, ErrResume) || len(selection.Ready) != 0 || len(selection.Missing) != 0 || len(selection.Rejected) != 0 {
		t.Fatalf("availability failure = %+v, %v", selection, err)
	}
	selection, err = SelectMissing(context.Background(), plan, []stagedag.Checkpoint{checkpoint},
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) { panic("secret") }))
	if !errors.Is(err, ErrResume) || len(selection.Ready) != 0 || len(selection.Missing) != 0 {
		t.Fatalf("availability panic = %+v, %v", selection, err)
	}
}

func TestSelectMissingCancellationAndConfigurationAreFailureZero(t *testing.T) {
	plan := mustPlan(t, testJob("job-cancel", 1, 0))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	selection, err := SelectMissing(canceled, plan, nil,
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) { return true, nil }))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(selection, ResumeSelection{}) {
		t.Fatalf("canceled = %+v, %v", selection, err)
	}
	if selection, err := SelectMissing(nil, plan, nil, nil); !errors.Is(err, ErrConfiguration) || !reflect.DeepEqual(selection, ResumeSelection{}) {
		t.Fatalf("nil = %+v, %v", selection, err)
	}
}

func TestSelectMissingRejectsTruncatedAndRepartitionedPlans(t *testing.T) {
	availability := ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) {
		return true, nil
	})
	base := mustPlan(t, testJob("job-plan-contract", 128, 0))

	truncated := base
	truncated.Tasks = cloneTasks(base.Tasks[:1])
	selection, err := SelectMissing(context.Background(), truncated, nil, availability)
	if !errors.Is(err, ErrResume) || !reflect.DeepEqual(selection, ResumeSelection{}) {
		t.Fatalf("truncated plan = %+v, %v", selection, err)
	}

	repartitioned := base
	left, err := makeTask(base.Job, domainVideo, 0, videoRange(0, 63),
		base.Tasks[0].Resources, base.Tasks[0].Retry)
	if err != nil {
		t.Fatal(err)
	}
	right, err := makeTask(base.Job, domainVideo, 1, videoRange(63, 65),
		base.Tasks[1].Resources, base.Tasks[1].Retry)
	if err != nil {
		t.Fatal(err)
	}
	repartitioned.Tasks = []stagedag.Task{left, right}
	selection, err = SelectMissing(context.Background(), repartitioned, nil, availability)
	if !errors.Is(err, ErrResume) || !reflect.DeepEqual(selection, ResumeSelection{}) {
		t.Fatalf("repartitioned plan = %+v, %v", selection, err)
	}
}

func TestSelectMissingMaxTaskScanIsLinear(t *testing.T) {
	// Keep the full 65,536 candidate admission bound while using enough valid
	// tasks to expose the old quadratic path without making -race CI spend two
	// minutes on one regression. Re-freezing this roughly 10 MiB plan for each
	// known candidate would still process about 80 GiB of plan bytes.
	const taskCount = 8_192
	job := testJob("job-linear", FrameGroupCount*taskCount, 0)
	plan, err := Build(context.Background(), job, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != taskCount {
		t.Fatalf("tasks = %d, want %d", len(plan.Tasks), taskCount)
	}
	candidates := make([]stagedag.Checkpoint, stagedag.MaxTasks)
	for index, task := range plan.Tasks {
		// A known ID with an otherwise malformed checkpoint forces one binding
		// decision per task without allocating 8,192 receipt payloads. The old
		// implementation re-froze the full plan for every one of these entries.
		candidates[index].TaskID = task.ID
	}
	selection, err := SelectMissing(context.Background(), plan, candidates,
		ArtifactAvailabilityFunc(func(context.Context, cachekey.Key, string, int64) (bool, error) {
			t.Fatal("availability must not run for malformed checkpoints")
			return false, nil
		}))
	if err != nil || len(selection.Ready) != 0 || len(selection.Missing) != taskCount ||
		len(selection.Rejected) != stagedag.MaxTasks {
		t.Fatalf("selection sizes ready=%d missing=%d rejected=%d, error=%v",
			len(selection.Ready), len(selection.Missing), len(selection.Rejected), err)
	}
}

func videoRange(first, count int64) videoedit.TickRange {
	return videoedit.TickRange{First: first, Count: count}
}

func makeCheckpoint(t *testing.T, plan Plan, index int) stagedag.Checkpoint {
	t.Helper()
	receipt := mustReceipt(t, plan.Tasks[index], 1)
	output, err := StageOutput(receipt)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan.Plan, plan.Tasks[index], output, 1)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
