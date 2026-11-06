package stagedag

import (
	"bytes"
	"fmt"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const MaxCheckpointBytes = MaxReceiptBytes + 64<<10

func planCanonicalLimits(maxInputBytes int) canonicaljson.Limits {
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = maxInputBytes
	limits.MaxDepth = 32
	limits.MaxValues = 4 << 20
	limits.MaxStringBytes = maxInputBytes
	limits.MaxIntegerDigits = 32
	return limits
}

// DecodePlan is the strict wire boundary for a stage plan. It rejects
// duplicate, missing, case-aliased and unknown members before semantic
// validation, and returns fully owned task state.
func DecodePlan(raw []byte) (Plan, error) {
	if len(raw) == 0 || len(raw) > MaxPlanBytes {
		return Plan{}, fmt.Errorf("%w: wire bytes %d exceed [1,%d]", ErrPlanInvalid, len(raw), MaxPlanBytes)
	}
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, planCanonicalLimits(MaxPlanBytes))
	if err != nil {
		return Plan{}, fmt.Errorf("%w: canonical admission: %v", ErrPlanInvalid, err)
	}
	var plan Plan
	if err := wirejson.Decode(canonical, &plan); err != nil {
		return Plan{}, fmt.Errorf("%w: decode: %v", ErrPlanInvalid, err)
	}
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// DecodeCheckpoint strictly decodes and structurally validates a checkpoint.
// Callers must still use ValidateCheckpoint with the requested plan and task
// before reuse.
func DecodeCheckpoint(raw []byte) (Checkpoint, error) {
	if len(raw) == 0 || len(raw) > MaxCheckpointBytes {
		return Checkpoint{}, fmt.Errorf("%w: wire bytes %d exceed [1,%d]", ErrCheckpointInvalid, len(raw), MaxCheckpointBytes)
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = MaxCheckpointBytes
	limits.MaxDepth = 256
	limits.MaxValues = 1 << 20
	limits.MaxStringBytes = MaxCheckpointBytes
	limits.MaxIntegerDigits = 32
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("%w: canonical admission: %v", ErrCheckpointInvalid, err)
	}
	if !bytes.Equal(canonical, raw) {
		return Checkpoint{}, fmt.Errorf("%w: stored checkpoint must use canonical JSON", ErrCheckpointInvalid)
	}
	var checkpoint Checkpoint
	if err := wirejson.Decode(canonical, &checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: decode: %v", ErrCheckpointInvalid, err)
	}
	if err := validateCheckpointShape(checkpoint); err != nil {
		return Checkpoint{}, err
	}
	canonical, err = CanonicalCheckpoint(checkpoint)
	if err != nil {
		return Checkpoint{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return Checkpoint{}, fmt.Errorf("%w: stored checkpoint must use canonical JSON", ErrCheckpointInvalid)
	}
	return checkpoint, nil
}
