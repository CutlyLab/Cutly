package chunkplan

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestBuildPartitionsExactRangesAndFreezesPlan(t *testing.T) {
	job := testJob("job-one", 130, 131_073)
	config := testConfig()
	plan, err := Build(context.Background(), job, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 6 {
		t.Fatalf("tasks = %d, want 6", len(plan.Tasks))
	}
	want := []struct {
		id      string
		profile string
		rangeV  videoedit.TickRange
	}{
		{"audio-00000", AudioStageProfileV1, videoedit.TickRange{First: 0, Count: 65_536}},
		{"audio-00001", AudioStageProfileV1, videoedit.TickRange{First: 65_536, Count: 65_536}},
		{"audio-00002", AudioStageProfileV1, videoedit.TickRange{First: 131_072, Count: 1}},
		{"video-00000", VideoStageProfileV1, videoedit.TickRange{First: 0, Count: 64}},
		{"video-00001", VideoStageProfileV1, videoedit.TickRange{First: 64, Count: 64}},
		{"video-00002", VideoStageProfileV1, videoedit.TickRange{First: 128, Count: 2}},
	}
	for index, task := range plan.Tasks {
		if task.ID != want[index].id || task.Profile != want[index].profile || len(task.DependsOn) != 0 {
			t.Fatalf("task[%d] = %+v", index, task)
		}
		actualRange := task.FrameRange
		if task.Profile == AudioStageProfileV1 {
			actualRange = task.SampleRange
		}
		if actualRange == nil || *actualRange != want[index].rangeV {
			t.Fatalf("task[%d] range = %+v, want %+v", index, actualRange, want[index].rangeV)
		}
		if _, err := validateChunkTask(task); err != nil {
			t.Fatalf("task[%d] contract = %v", index, err)
		}
	}
	raw, err := stagedag.CanonicalPlan(plan.Plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := stagedag.DecodePlan(raw)
	if err != nil || !reflect.DeepEqual(plan.Plan, decoded) {
		t.Fatalf("frozen plan differs: %v", err)
	}

	// Returned plan owns every nested slice and range.
	job.ID = "mutated"
	config.Retry.MaxAttempts = 9
	if plan.JobID != "job-one" || plan.Tasks[0].Retry.MaxAttempts != 3 {
		t.Fatal("caller mutation changed plan")
	}
}

func TestEquivalentJobsShareChunkCacheKeys(t *testing.T) {
	leftJob := testJob("job-left", 65, 65_537)
	rightJob := leftJob
	rightJob.ID = "job-right"
	rightJob.IdempotencyKey = "request-right"
	rightJob.ArtifactURI = "file:///tmp/right.mp4"
	left, err := Build(context.Background(), leftJob, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	right, err := Build(context.Background(), rightJob, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if left.JobID == right.JobID || len(left.Tasks) != len(right.Tasks) {
		t.Fatalf("job/task identity = %q/%q %d/%d", left.JobID, right.JobID, len(left.Tasks), len(right.Tasks))
	}
	for index := range left.Tasks {
		if left.Tasks[index].CacheKey != right.Tasks[index].CacheKey ||
			!reflect.DeepEqual(left.Tasks[index].CacheInput, right.Tasks[index].CacheInput) {
			t.Fatalf("equivalent task %d did not reuse cache identity", index)
		}
	}
}

func TestBuildPreservesNonzeroCompositionOriginsAndIgnoresInactiveOrigin(t *testing.T) {
	job := testJob("job-origin", 65, 0)
	job.FrameRange.First = 100
	job.SampleRange.First = -99
	plan, err := Build(context.Background(), job, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 2 || *plan.Tasks[0].FrameRange != (videoedit.TickRange{First: 100, Count: 64}) ||
		*plan.Tasks[1].FrameRange != (videoedit.TickRange{First: 164, Count: 1}) {
		t.Fatalf("origin tasks = %+v", plan.Tasks)
	}
}

func TestBuildTaskLimitExactNAndNMinusOne(t *testing.T) {
	job := testJob("job-limit", 130, 131_073)
	config := testConfig()
	config.MaxTasks = 6
	if _, err := Build(context.Background(), job, config); err != nil {
		t.Fatalf("exact N failed: %v", err)
	}
	config.MaxTasks = 5
	plan, err := Build(context.Background(), job, config)
	if !errors.Is(err, ErrLimit) || len(plan.Tasks) != 0 {
		t.Fatalf("N-1 = %+v, %v", plan, err)
	}
}

func TestBuildFailsZeroForInvalidConfigurationJobAndCancellation(t *testing.T) {
	job := testJob("job-fail", 1, 1)
	if plan, err := Build(context.Background(), job, Config{}); !errors.Is(err, ErrConfiguration) || len(plan.Tasks) != 0 {
		t.Fatalf("unbounded default = %+v, %v", plan, err)
	}
	negative := job
	negative.FrameRange.First = -1
	if plan, err := Build(context.Background(), negative, testConfig()); !errors.Is(err, ErrJob) || len(plan.Tasks) != 0 {
		t.Fatalf("negative = %+v, %v", plan, err)
	}
	invalid := job
	invalid.PlanFingerprint = "latest"
	if plan, err := Build(context.Background(), invalid, testConfig()); !errors.Is(err, ErrJob) || len(plan.Tasks) != 0 {
		t.Fatalf("invalid = %+v, %v", plan, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if plan, err := Build(canceled, job, testConfig()); !errors.Is(err, context.Canceled) || len(plan.Tasks) != 0 {
		t.Fatalf("canceled = %+v, %v", plan, err)
	}
	if plan, err := Build(nil, job, testConfig()); !errors.Is(err, ErrConfiguration) || len(plan.Tasks) != 0 {
		t.Fatalf("nil context = %+v, %v", plan, err)
	}
}

func TestBuildRejectsOversizedProgrammaticJobBeforeFreeze(t *testing.T) {
	base := testJob("job-admission", 1, 0)
	tests := []struct {
		name   string
		mutate func(*renderapi.RenderJob)
	}{
		{name: "job ID", mutate: func(job *renderapi.RenderJob) {
			job.ID = strings.Repeat("j", maxJobIDBytes+1)
		}},
		{name: "idempotency key", mutate: func(job *renderapi.RenderJob) {
			job.IdempotencyKey = strings.Repeat("i", maxIdempotencyKeyBytes+1)
		}},
		{name: "artifact URI", mutate: func(job *renderapi.RenderJob) {
			job.ArtifactURI = strings.Repeat("a", maxArtifactURIBytes+1)
		}},
		{name: "invalid UTF-8", mutate: func(job *renderapi.RenderJob) {
			job.IdempotencyKey = string([]byte{0xff})
		}},
		{name: "range overflow", mutate: func(job *renderapi.RenderJob) {
			job.FrameRange = videoedit.TickRange{First: int64(1<<63 - 1), Count: 2}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := base
			test.mutate(&job)
			plan, err := Build(context.Background(), job, testConfig())
			if !errors.Is(err, ErrJob) || !reflect.DeepEqual(plan, Plan{}) {
				t.Fatalf("plan = %+v, error = %v", plan, err)
			}
		})
	}
}

func testJob(id string, frames, samples int64) renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion, ID: id, IdempotencyKey: "request-" + id,
		PlanFingerprint: digest("plan"), ExecutionManifestFingerprint: digest("manifest"),
		TimelineRange: videoedit.TimeRange{
			Start:    videoedit.RationalTime{Value: 0, Rate: 1},
			Duration: videoedit.RationalTime{Value: 1, Rate: 1},
		},
		FrameRange:  videoedit.TickRange{First: 0, Count: frames},
		SampleRange: videoedit.TickRange{First: 0, Count: samples},
		ArtifactURI: "file:///tmp/" + id + ".mp4",
	}
}

func testConfig() Config {
	result := ConfigForResources(
		stagedag.ResourceEstimate{MemoryBytes: 1024, DiskBytes: 2048},
		stagedag.ResourceEstimate{MemoryBytes: 512, DiskBytes: 1024},
	)
	result.Retry.MaxAttempts = 3
	return result
}

func cloneJSON[T any](t *testing.T, value T) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned T
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}
