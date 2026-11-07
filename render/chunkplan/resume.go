package chunkplan

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	RejectionUnknownTask       = "unknown_task"
	RejectionDuplicateTask     = "duplicate_task"
	RejectionCheckpointInvalid = "checkpoint_invalid"
	RejectionReceiptInvalid    = "receipt_invalid"
	RejectionReceiptBinding    = "receipt_binding_invalid"
	RejectionArtifactMissing   = "artifact_missing"
)

// ArtifactAvailability must verify the exact CAS key and payload identity. A
// boolean false means absent or corrupt and therefore safely recomputable. An
// error means availability is unknown and aborts resume selection.
type ArtifactAvailability interface {
	Available(context.Context, cachekey.Key, string, int64) (bool, error)
}

type ArtifactAvailabilityFunc func(context.Context, cachekey.Key, string, int64) (bool, error)

func (function ArtifactAvailabilityFunc) Available(ctx context.Context, key cachekey.Key,
	contentHash string, sizeBytes int64) (bool, error) {
	return function(ctx, key, contentHash, sizeBytes)
}

type Rejection struct {
	TaskID string `json:"task_id"`
	Code   string `json:"code"`
}

// ResumeSelection owns its values. Ready and Missing follow canonical plan
// task order. Rejected is deterministic: unknown candidates retain candidate
// order, then known-task rejections follow plan order. Codes are stable and
// non-sensitive; this is diagnostic state, not evidence.
type ResumeSelection struct {
	Ready    []stagedag.Checkpoint `json:"ready"`
	Missing  []stagedag.Task       `json:"missing"`
	Rejected []Rejection           `json:"rejected"`
}

// SelectMissing freezes and validates the complete chunk plan once, indexes
// candidates once, and then validates every unique candidate in plan order.
// The algorithm is O(tasks + candidates + receipt bytes); it never re-freezes
// the plan per checkpoint. A malformed or missing candidate marks only its
// task missing; an availability callback failure or cancellation returns a
// zero selection because cache state is then unknown.
func SelectMissing(ctx context.Context, plan Plan, candidates []stagedag.Checkpoint,
	availability ArtifactAvailability) (selection ResumeSelection, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			selection = ResumeSelection{}
			returnErr = fmt.Errorf("%w: availability callback panicked\n%s", ErrResume, debug.Stack())
		}
		if returnErr != nil {
			selection = ResumeSelection{}
		}
	}()
	if ctx == nil || availability == nil {
		return ResumeSelection{}, fmt.Errorf("%w: context and availability are required", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return ResumeSelection{}, err
	}
	frozen, err := freezeChunkPlan(plan)
	if err != nil {
		return ResumeSelection{}, err
	}
	if len(candidates) > stagedag.MaxTasks {
		return ResumeSelection{}, fmt.Errorf("%w: candidates exceed %d", ErrLimit, stagedag.MaxTasks)
	}
	byID := make(map[string]int, len(frozen.Tasks))
	for index, task := range frozen.Tasks {
		byID[task.ID] = index
	}
	candidateByTask := make([]int, len(frozen.Tasks))
	for index := range candidateByTask {
		candidateByTask[index] = -1
	}
	rejected := make([]Rejection, 0)
	for index, candidate := range candidates {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				return ResumeSelection{}, err
			}
		}
		planIndex, known := byID[candidate.TaskID]
		if !known {
			taskID := candidate.TaskID
			if !receiptIDPattern.MatchString(taskID) {
				taskID = ""
			}
			rejected = append(rejected, Rejection{TaskID: taskID, Code: RejectionUnknownTask})
			continue
		}
		if candidateByTask[planIndex] != -1 {
			candidateByTask[planIndex] = -2
			continue
		}
		candidateByTask[planIndex] = index
	}
	ready := make([]stagedag.Checkpoint, 0, len(frozen.Tasks))
	missing := make([]stagedag.Task, 0, len(frozen.Tasks))
	for index, task := range frozen.Tasks {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				return ResumeSelection{}, err
			}
		}
		candidateIndex := candidateByTask[index]
		if candidateIndex == -1 {
			missing = append(missing, task)
			continue
		}
		if candidateIndex == -2 {
			rejected = append(rejected, Rejection{TaskID: task.ID, Code: RejectionDuplicateTask})
			missing = append(missing, task)
			continue
		}
		candidate, err := freezeCheckpoint(candidates[candidateIndex])
		if err != nil || validateCheckpointBinding(frozen.Plan, task, candidate) != nil {
			rejected = append(rejected, Rejection{TaskID: task.ID, Code: RejectionCheckpointInvalid})
			missing = append(missing, task)
			continue
		}
		receipt, err := DecodeReceipt(candidate.Output.Receipt)
		if err != nil {
			rejected = append(rejected, Rejection{TaskID: task.ID, Code: RejectionReceiptInvalid})
			missing = append(missing, task)
			continue
		}
		if err := ValidateReceiptForTask(task, receipt); err != nil ||
			receipt.Output.ContentHash != candidate.Output.ContentHash ||
			receipt.Output.SizeBytes != candidate.Output.SizeBytes || receipt.AttemptCount != candidate.AttemptCount {
			rejected = append(rejected, Rejection{TaskID: task.ID, Code: RejectionReceiptBinding})
			missing = append(missing, task)
			continue
		}
		available, err := availability.Available(ctx, task.CacheKey, candidate.Output.ContentHash, candidate.Output.SizeBytes)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ResumeSelection{}, ctxErr
			}
			return ResumeSelection{}, errors.Join(ErrResume, err)
		}
		if !available {
			rejected = append(rejected, Rejection{TaskID: task.ID, Code: RejectionArtifactMissing})
			missing = append(missing, task)
			continue
		}
		ready = append(ready, candidate)
	}
	if err := ctx.Err(); err != nil {
		return ResumeSelection{}, err
	}
	return ResumeSelection{Ready: ready, Missing: cloneTasks(missing), Rejected: append([]Rejection(nil), rejected...)}, nil
}

func freezeChunkPlan(plan Plan) (Plan, error) {
	frozenJob, err := freezeJob(plan.Job)
	if err != nil {
		return Plan{}, errors.Join(ErrResume, err)
	}
	frozenStage, err := freezeStagePlan(plan.Plan)
	if err != nil {
		return Plan{}, err
	}
	if err := validatePlanContract(frozenJob, frozenStage); err != nil {
		return Plan{}, err
	}
	return Plan{Plan: frozenStage, Job: frozenJob}, nil
}

func freezeCheckpoint(checkpoint stagedag.Checkpoint) (stagedag.Checkpoint, error) {
	raw, err := stagedag.CanonicalCheckpoint(checkpoint)
	if err != nil {
		return stagedag.Checkpoint{}, err
	}
	return stagedag.DecodeCheckpoint(raw)
}

// validateCheckpointBinding reproduces the stagedag ownership binding against
// an already frozen plan/task. Shape and ownership were established once by
// freezeCheckpoint; unlike stagedag.ValidateCheckpoint this helper does not
// freeze the complete plan again for every candidate.
func validateCheckpointBinding(plan stagedag.Plan, task stagedag.Task, checkpoint stagedag.Checkpoint) error {
	if checkpoint.JobID != plan.JobID || checkpoint.PlanFingerprint != plan.PlanFingerprint ||
		checkpoint.ExecutionManifestFingerprint != plan.ExecutionManifestFingerprint ||
		checkpoint.TaskID != task.ID || checkpoint.Stage != task.Stage || checkpoint.TaskProfile != task.Profile ||
		checkpoint.CacheKey != task.CacheKey || !equalRange(checkpoint.FrameRange, task.FrameRange) ||
		!equalRange(checkpoint.SampleRange, task.SampleRange) {
		return fmt.Errorf("%w: checkpoint/task binding", ErrResume)
	}
	return nil
}

func freezeStagePlan(plan stagedag.Plan) (stagedag.Plan, error) {
	raw, err := stagedag.CanonicalPlan(plan)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrResume, err)
	}
	frozen, err := stagedag.DecodePlan(raw)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrResume, err)
	}
	return frozen, nil
}

func cloneTasks(values []stagedag.Task) []stagedag.Task {
	if values == nil {
		return nil
	}
	result := make([]stagedag.Task, len(values))
	for index, value := range values {
		result[index] = value
		result[index].DependsOn = append([]string(nil), value.DependsOn...)
		result[index].CacheInput = cloneCacheInput(value.CacheInput)
		result[index].FrameRange = cloneRange(value.FrameRange)
		result[index].SampleRange = cloneRange(value.SampleRange)
	}
	return result
}
