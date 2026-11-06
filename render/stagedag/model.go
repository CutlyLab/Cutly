package stagedag

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	PlanProfileV1         = "cutly.render.stage-dag.v1"
	CheckpointProfileV1   = "cutly.render.stage-checkpoint.v1"
	StageBindingProfileV1 = "cutly.render.stage-binding.v1"
	DependencyBindingRole = "dependency"
	StageBindingRole      = "stage"
	MaxTasks              = 1 << 16
	MaxDependencies       = 1 << 12
	MaxDependencyEdges    = 1 << 20
	MaxPlanInputs         = 1 << 18
	MaxAttempts           = 16
	MaxPlanBytes          = 64 << 20
	MaxReceiptBytes       = 1 << 20
)

var (
	ErrPlanInvalid       = errors.New("stagedag: plan is invalid")
	ErrCheckpointInvalid = errors.New("stagedag: checkpoint is invalid")
	ErrBudget            = errors.New("stagedag: task exceeds execution budget")
	ErrExecution         = errors.New("stagedag: stage execution failed")
	ErrCheckpointStore   = errors.New("stagedag: checkpoint store failed")
	taskIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,255}$`)
	profilePattern       = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9][0-9]*$`)
)

type ResourceEstimate struct {
	MemoryBytes uint64 `json:"memory_bytes"`
	DiskBytes   uint64 `json:"disk_bytes"`
}

type RetryPolicy struct {
	MaxAttempts    uint8 `json:"max_attempts"`
	RetryTransient bool  `json:"retry_transient"`
	RetryResource  bool  `json:"retry_resource"`
}

type Task struct {
	ID          string               `json:"id"`
	Stage       renderapi.Stage      `json:"stage"`
	Profile     string               `json:"profile"`
	DependsOn   []string             `json:"depends_on"`
	CacheInput  cachekey.Input       `json:"cache_input"`
	CacheKey    cachekey.Key         `json:"cache_key"`
	FrameRange  *videoedit.TickRange `json:"frame_range,omitempty"`
	SampleRange *videoedit.TickRange `json:"sample_range,omitempty"`
	Resources   ResourceEstimate     `json:"resources"`
	Retry       RetryPolicy          `json:"retry"`
}

type Plan struct {
	Profile                      string `json:"profile"`
	JobID                        string `json:"job_id"`
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
	Tasks                        []Task `json:"tasks"`
}

type StageOutput struct {
	ContentHash string          `json:"content_hash"`
	SizeBytes   int64           `json:"size_bytes"`
	Receipt     json.RawMessage `json:"receipt"`
}

type Checkpoint struct {
	Profile                      string               `json:"profile"`
	JobID                        string               `json:"job_id"`
	PlanFingerprint              string               `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string               `json:"execution_manifest_fingerprint"`
	TaskID                       string               `json:"task_id"`
	Stage                        renderapi.Stage      `json:"stage"`
	TaskProfile                  string               `json:"task_profile"`
	CacheKey                     cachekey.Key         `json:"cache_key"`
	FrameRange                   *videoedit.TickRange `json:"frame_range,omitempty"`
	SampleRange                  *videoedit.TickRange `json:"sample_range,omitempty"`
	Output                       StageOutput          `json:"output"`
	AttemptCount                 uint8                `json:"attempt_count"`
}

type Limits struct {
	MaxConcurrent  int
	MaxMemoryBytes uint64
	MaxDiskBytes   uint64
}

func ValidatePlan(plan Plan) error {
	if plan.Profile != PlanProfileV1 || !validIdentity(plan.JobID) ||
		!cachekey.CanonicalSHA256(plan.PlanFingerprint) ||
		!cachekey.CanonicalSHA256(plan.ExecutionManifestFingerprint) ||
		len(plan.Tasks) == 0 || len(plan.Tasks) > MaxTasks {
		return fmt.Errorf("%w: profile, identities and non-empty bounded tasks are required", ErrPlanInvalid)
	}
	byID := make(map[string]int, len(plan.Tasks))
	totalDependencies := 0
	totalInputs := 0
	for index, task := range plan.Tasks {
		if !taskIDPattern.MatchString(task.ID) || !validStage(task.Stage) || !validProfile(task.Profile) ||
			task.DependsOn == nil || len(task.DependsOn) > MaxDependencies ||
			task.Retry.MaxAttempts == 0 || task.Retry.MaxAttempts > MaxAttempts {
			return fmt.Errorf("%w: task %d shape", ErrPlanInvalid, index)
		}
		if err := validateTaskCacheBinding(plan, task); err != nil {
			return fmt.Errorf("%w: task %q cache binding: %v", ErrPlanInvalid, task.ID, err)
		}
		if len(task.CacheInput.Inputs) > MaxPlanInputs-totalInputs {
			return fmt.Errorf("%w: normalized inputs exceed %d", ErrPlanInvalid, MaxPlanInputs)
		}
		totalInputs += len(task.CacheInput.Inputs)
		if _, duplicate := byID[task.ID]; duplicate {
			return fmt.Errorf("%w: duplicate task ID %q", ErrPlanInvalid, task.ID)
		}
		if index > 0 && task.ID <= plan.Tasks[index-1].ID {
			return fmt.Errorf("%w: tasks must be strictly sorted by ID", ErrPlanInvalid)
		}
		byID[task.ID] = index
		totalDependencies += len(task.DependsOn)
		if totalDependencies > MaxDependencyEdges {
			return fmt.Errorf("%w: dependency edges exceed %d", ErrPlanInvalid, MaxDependencyEdges)
		}
		if err := validateOptionalRange(task.FrameRange); err != nil {
			return fmt.Errorf("%w: task %q frame range: %v", ErrPlanInvalid, task.ID, err)
		}
		if err := validateOptionalRange(task.SampleRange); err != nil {
			return fmt.Errorf("%w: task %q sample range: %v", ErrPlanInvalid, task.ID, err)
		}
		for dependencyIndex, dependency := range task.DependsOn {
			if !taskIDPattern.MatchString(dependency) || dependency == task.ID ||
				(dependencyIndex > 0 && dependency <= task.DependsOn[dependencyIndex-1]) {
				return fmt.Errorf("%w: task %q dependencies must be strictly sorted unique non-self IDs", ErrPlanInvalid, task.ID)
			}
		}
	}
	for _, task := range plan.Tasks {
		for dependencyIndex, dependency := range task.DependsOn {
			dependencyTaskIndex, exists := byID[dependency]
			if !exists {
				return fmt.Errorf("%w: task %q missing dependency %q", ErrPlanInvalid, task.ID, dependency)
			}
			if stageRank(plan.Tasks[dependencyTaskIndex].Stage) > stageRank(task.Stage) {
				return fmt.Errorf("%w: task %q stage %q depends on later stage %q", ErrPlanInvalid, task.ID, task.Stage, plan.Tasks[dependencyTaskIndex].Stage)
			}
			if err := validateDependencyCacheBinding(task, dependencyIndex, plan.Tasks[dependencyTaskIndex]); err != nil {
				return fmt.Errorf("%w: task %q dependency %q cache binding: %v", ErrPlanInvalid, task.ID, dependency, err)
			}
		}
		if dependencyInputCount(task.CacheInput.Inputs) != len(task.DependsOn) {
			return fmt.Errorf("%w: task %q dependency cache inputs do not match dependencies", ErrPlanInvalid, task.ID)
		}
	}
	if err := validateAcyclic(plan.Tasks, byID); err != nil {
		return err
	}
	return nil
}

func CanonicalPlan(plan Plan) ([]byte, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrPlanInvalid, err)
	}
	// encoding/json may HTML-escape the bounded JobID before canonicalization.
	// The additive allowance prevents that representation detail from rejecting
	// a plan whose final canonical form still fits MaxPlanBytes.
	const marshalAllowance = 64 << 10
	if len(raw) > MaxPlanBytes+marshalAllowance {
		return nil, fmt.Errorf("%w: marshaled plan bytes %d exceed admission bound", ErrPlanInvalid, len(raw))
	}
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, planCanonicalLimits(MaxPlanBytes+marshalAllowance))
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize: %v", ErrPlanInvalid, err)
	}
	if len(canonical) > MaxPlanBytes {
		return nil, fmt.Errorf("%w: canonical plan bytes %d exceed %d", ErrPlanInvalid, len(canonical), MaxPlanBytes)
	}
	return canonical, nil
}

func ValidateCheckpoint(plan Plan, task Task, checkpoint Checkpoint) error {
	frozen, err := freezePlan(plan)
	if err != nil {
		return err
	}
	boundTask, err := exactPlanTask(frozen, task)
	if err != nil {
		return err
	}
	return validateCheckpointBinding(frozen, boundTask, checkpoint)
}

func validateCheckpointBinding(plan Plan, task Task, checkpoint Checkpoint) error {
	if err := validateCheckpointShape(checkpoint); err != nil {
		return err
	}
	if checkpoint.JobID != plan.JobID ||
		checkpoint.PlanFingerprint != plan.PlanFingerprint ||
		checkpoint.ExecutionManifestFingerprint != plan.ExecutionManifestFingerprint ||
		checkpoint.TaskID != task.ID || checkpoint.Stage != task.Stage || checkpoint.TaskProfile != task.Profile ||
		checkpoint.CacheKey != task.CacheKey || !equalRange(checkpoint.FrameRange, task.FrameRange) ||
		!equalRange(checkpoint.SampleRange, task.SampleRange) {
		return fmt.Errorf("%w: checkpoint/task binding", ErrCheckpointInvalid)
	}
	return nil
}

func NewCheckpoint(plan Plan, task Task, output StageOutput, attempts uint8) (Checkpoint, error) {
	frozen, err := freezePlan(plan)
	if err != nil {
		return Checkpoint{}, err
	}
	boundTask, err := exactPlanTask(frozen, task)
	if err != nil {
		return Checkpoint{}, err
	}
	return newCheckpointBound(frozen, boundTask, output, attempts)
}

func newCheckpointBound(plan Plan, task Task, output StageOutput, attempts uint8) (Checkpoint, error) {
	if attempts == 0 || attempts > task.Retry.MaxAttempts {
		return Checkpoint{}, fmt.Errorf("%w: attempt count exceeds the executing task policy", ErrCheckpointInvalid)
	}
	checkpoint := Checkpoint{
		Profile: CheckpointProfileV1, JobID: plan.JobID,
		PlanFingerprint: plan.PlanFingerprint, ExecutionManifestFingerprint: plan.ExecutionManifestFingerprint,
		TaskID: task.ID, Stage: task.Stage, TaskProfile: task.Profile, CacheKey: task.CacheKey,
		FrameRange: cloneRange(task.FrameRange), SampleRange: cloneRange(task.SampleRange),
		Output: cloneOutput(output), AttemptCount: attempts,
	}
	if err := validateCheckpointBinding(plan, task, checkpoint); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func exactPlanTask(plan Plan, task Task) (Task, error) {
	for _, candidate := range plan.Tasks {
		if candidate.ID != task.ID {
			continue
		}
		if !reflect.DeepEqual(candidate, task) {
			return Task{}, fmt.Errorf("%w: task %q is not the exact task in the plan", ErrPlanInvalid, task.ID)
		}
		return candidate, nil
	}
	return Task{}, fmt.Errorf("%w: task %q is not present in the plan", ErrPlanInvalid, task.ID)
}

func CanonicalCheckpoint(checkpoint Checkpoint) ([]byte, error) {
	if err := validateCheckpointShape(checkpoint); err != nil {
		return nil, err
	}
	return canonicaljson.Marshal(checkpoint)
}

func validateOutput(output StageOutput) error {
	if !cachekey.CanonicalSHA256(output.ContentHash) || output.SizeBytes < 0 || len(output.Receipt) == 0 || len(output.Receipt) > MaxReceiptBytes {
		return fmt.Errorf("%w: output identity/receipt", ErrCheckpointInvalid)
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = MaxReceiptBytes
	limits.MaxStringBytes = MaxReceiptBytes
	canonical, err := canonicaljson.CanonicalizeWithLimits(output.Receipt, limits)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' || !bytes.Equal(canonical, output.Receipt) {
		return fmt.Errorf("%w: output receipt must be a canonical-profile object", ErrCheckpointInvalid)
	}
	return nil
}

func validateAcyclic(tasks []Task, byID map[string]int) error {
	indegree := make([]int, len(tasks))
	dependents := make([][]int, len(tasks))
	for index, task := range tasks {
		indegree[index] = len(task.DependsOn)
		for _, dependency := range task.DependsOn {
			dependencyIndex := byID[dependency]
			dependents[dependencyIndex] = append(dependents[dependencyIndex], index)
		}
	}
	queue := make([]int, 0, len(tasks))
	for index, count := range indegree {
		if count == 0 {
			queue = append(queue, index)
		}
	}
	visited := 0
	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
		visited++
		for _, dependent := range dependents[index] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if visited != len(tasks) {
		return fmt.Errorf("%w: dependency cycle", ErrPlanInvalid)
	}
	return nil
}

func validStage(stage renderapi.Stage) bool {
	return stageRank(stage) >= 0
}

func stageRank(stage renderapi.Stage) int {
	switch stage {
	case renderapi.StageResolve:
		return 0
	case renderapi.StageMaterialize:
		return 1
	case renderapi.StageProbe:
		return 2
	case renderapi.StageDecode:
		return 3
	case renderapi.StageCompose, renderapi.StageAudio:
		return 4
	case renderapi.StageEncode:
		return 5
	case renderapi.StageMux:
		return 6
	case renderapi.StageVerify:
		return 7
	case renderapi.StagePublish:
		return 8
	default:
		return -1
	}
}

func validIdentity(value string) bool {
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	return true
}

func validProfile(value string) bool {
	return len(value) > 0 && len(value) <= cachekey.MaxTokenBytes && profilePattern.MatchString(value)
}

func validateOptionalRange(value *videoedit.TickRange) error {
	if value == nil {
		return nil
	}
	if err := value.Validate(); err != nil || value.First < 0 || value.Count <= 0 {
		return errors.New("range must be nonnegative, nonempty and nonoverflowing")
	}
	return nil
}

func equalRange(left, right *videoedit.TickRange) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneRange(value *videoedit.TickRange) *videoedit.TickRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneOutput(value StageOutput) StageOutput {
	value.Receipt = append(json.RawMessage(nil), value.Receipt...)
	return value
}

func validateTaskCacheBinding(plan Plan, task Task) error {
	if err := cachekey.Verify(task.CacheInput, task.CacheKey); err != nil {
		return err
	}
	if task.CacheInput.PlanFingerprint != plan.PlanFingerprint ||
		task.CacheInput.ExecutionManifestFingerprint != plan.ExecutionManifestFingerprint ||
		task.CacheInput.StageProfile != task.Profile ||
		!equalCacheRange(task.CacheInput.FrameRange, task.FrameRange) ||
		!equalCacheRange(task.CacheInput.SampleRange, task.SampleRange) {
		return errors.New("cache input is not bound to the plan, manifest, profile and task ranges")
	}
	if err := validateStageCacheBinding(task); err != nil {
		return err
	}
	return nil
}

// StageBindingInput returns the reserved normalized input that binds an
// executor-dispatch stage into a stage cache key. Callers must include exactly
// one such input, in canonical role order, when constructing Task.CacheInput.
func StageBindingInput(stage renderapi.Stage) (cachekey.NormalizedInput, error) {
	if !validStage(stage) {
		return cachekey.NormalizedInput{}, errors.New("invalid render stage")
	}
	hash := sha256.New()
	hash.Write([]byte(StageBindingProfileV1))
	hash.Write([]byte{0})
	hash.Write([]byte(stage))
	return cachekey.NormalizedInput{
		Role: StageBindingRole, Ordinal: 0,
		ContentHash: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func validateStageCacheBinding(task Task) error {
	want, err := StageBindingInput(task.Stage)
	if err != nil {
		return err
	}
	count := 0
	for _, input := range task.CacheInput.Inputs {
		if input.Role != StageBindingRole {
			continue
		}
		count++
		if input != want {
			return errors.New("stage input does not match executor-dispatch stage")
		}
	}
	if count != 1 {
		return errors.New("cache input must contain exactly one stage binding")
	}
	return nil
}

// DependencyBindingInput returns the reserved normalized input for one entry
// in Task.DependsOn. The ordinal is the dependency's index in that strictly
// sorted slice; the dependency's complete cache key is already domain-separated
// by its namespace, artifact kind and canonical preimage.
func DependencyBindingInput(ordinal int, dependency cachekey.Key) (cachekey.NormalizedInput, error) {
	if ordinal < 0 || ordinal >= MaxDependencies {
		return cachekey.NormalizedInput{}, errors.New("dependency ordinal is out of range")
	}
	if err := cachekey.ValidateKey(dependency); err != nil {
		return cachekey.NormalizedInput{}, err
	}
	return cachekey.NormalizedInput{
		Role: DependencyBindingRole, Ordinal: uint32(ordinal), ContentHash: dependency.Digest,
	}, nil
}

func validateDependencyCacheBinding(task Task, ordinal int, dependency Task) error {
	for _, input := range task.CacheInput.Inputs {
		if input.Role != DependencyBindingRole {
			continue
		}
		if input.Ordinal == uint32(ordinal) {
			if input.ContentHash != dependency.CacheKey.Digest {
				return errors.New("dependency input hash does not match dependency cache key")
			}
			return nil
		}
	}
	return errors.New("dependency input is missing")
}

func dependencyInputCount(inputs []cachekey.NormalizedInput) int {
	count := 0
	for _, input := range inputs {
		if input.Role == DependencyBindingRole {
			count++
		}
	}
	return count
}

func equalCacheRange(cacheRange *cachekey.IndexRange, taskRange *videoedit.TickRange) bool {
	return cacheRange == nil && taskRange == nil ||
		cacheRange != nil && taskRange != nil && cacheRange.Start == taskRange.First && cacheRange.Count == taskRange.Count
}

func validateCheckpointShape(checkpoint Checkpoint) error {
	if checkpoint.Profile != CheckpointProfileV1 || !validIdentity(checkpoint.JobID) ||
		!cachekey.CanonicalSHA256(checkpoint.PlanFingerprint) ||
		!cachekey.CanonicalSHA256(checkpoint.ExecutionManifestFingerprint) ||
		!taskIDPattern.MatchString(checkpoint.TaskID) || !validStage(checkpoint.Stage) ||
		!validProfile(checkpoint.TaskProfile) || cachekey.ValidateKey(checkpoint.CacheKey) != nil ||
		checkpoint.AttemptCount == 0 || checkpoint.AttemptCount > MaxAttempts {
		return fmt.Errorf("%w: checkpoint shape", ErrCheckpointInvalid)
	}
	if err := validateOptionalRange(checkpoint.FrameRange); err != nil {
		return fmt.Errorf("%w: checkpoint frame range", ErrCheckpointInvalid)
	}
	if err := validateOptionalRange(checkpoint.SampleRange); err != nil {
		return fmt.Errorf("%w: checkpoint sample range", ErrCheckpointInvalid)
	}
	return validateOutput(checkpoint.Output)
}
