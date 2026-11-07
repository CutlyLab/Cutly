package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	SchemaVersion    = 1
	RequestProfileV1 = "cutly.render.worker-request.v1"
	ResultProfileV1  = "cutly.render.worker-result.v1"
	MaxRequestBytes  = 16 << 20
	MaxResultBytes   = 2 << 20
	MaxMessageBytes  = 64 << 10
	MaxDependencies  = stagedag.MaxDependencies
)

var (
	ErrProtocol = errors.New("worker: invalid protocol value")
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,255}$`)
)

type Request struct {
	SchemaVersion int                   `json:"schema_version"`
	Profile       string                `json:"profile"`
	InvocationID  string                `json:"invocation_id"`
	Task          stagedag.Task         `json:"task"`
	Dependencies  []stagedag.Dependency `json:"dependencies"`
}

type Result struct {
	SchemaVersion int                    `json:"schema_version"`
	Profile       string                 `json:"profile"`
	InvocationID  string                 `json:"invocation_id"`
	OK            bool                   `json:"ok"`
	Output        *stagedag.StageOutput  `json:"output,omitempty"`
	FailureClass  renderapi.FailureClass `json:"failure_class,omitempty"`
	Message       string                 `json:"message,omitempty"`
}

func CanonicalRequest(value Request) ([]byte, error) {
	if err := ValidateRequest(value); err != nil {
		return nil, err
	}
	if err := requestSizeBound(value); err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: request encoding: %v", ErrProtocol, err)
	}
	if len(raw) > MaxRequestBytes {
		return nil, fmt.Errorf("%w: request encoding exceeds %d bytes", ErrProtocol, MaxRequestBytes)
	}
	return raw, nil
}

func DecodeRequest(raw []byte) (Request, error) {
	if len(raw) == 0 || len(raw) > MaxRequestBytes {
		return Request{}, fmt.Errorf("%w: request bytes %d", ErrProtocol, len(raw))
	}
	canonical, err := canonicalize(raw, MaxRequestBytes)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Request{}, fmt.Errorf("%w: request is not canonical: %v", ErrProtocol, err)
	}
	var value Request
	if err := wirejson.Decode(raw, &value); err != nil {
		return Request{}, fmt.Errorf("%w: request decode: %v", ErrProtocol, err)
	}
	if err := ValidateRequest(value); err != nil {
		return Request{}, err
	}
	return cloneRequest(value), nil
}

func CanonicalResult(value Result) ([]byte, error) {
	if err := ValidateResult(value); err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: result encoding: %v", ErrProtocol, err)
	}
	if len(raw) > MaxResultBytes {
		return nil, fmt.Errorf("%w: result encoding exceeds %d bytes", ErrProtocol, MaxResultBytes)
	}
	return raw, nil
}

func DecodeResult(raw []byte) (Result, error) {
	if len(raw) == 0 || len(raw) > MaxResultBytes {
		return Result{}, fmt.Errorf("%w: result bytes %d", ErrProtocol, len(raw))
	}
	canonical, err := canonicalize(raw, MaxResultBytes)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Result{}, fmt.Errorf("%w: result is not canonical: %v", ErrProtocol, err)
	}
	var value Result
	if err := wirejson.Decode(raw, &value); err != nil {
		return Result{}, fmt.Errorf("%w: result decode: %v", ErrProtocol, err)
	}
	if err := ValidateResult(value); err != nil {
		return Result{}, err
	}
	return cloneResult(value), nil
}

func ValidateRequest(value Request) error {
	if value.SchemaVersion != SchemaVersion || value.Profile != RequestProfileV1 || !idPattern.MatchString(value.InvocationID) ||
		!idPattern.MatchString(value.Task.ID) || value.Task.DependsOn == nil || len(value.Task.DependsOn) > MaxDependencies ||
		len(value.Dependencies) > MaxDependencies || value.Dependencies == nil ||
		value.Task.Retry.MaxAttempts == 0 || value.Task.Retry.MaxAttempts > stagedag.MaxAttempts {
		return fmt.Errorf("%w: request header/task/dependency shape", ErrProtocol)
	}
	if err := cachekey.Verify(value.Task.CacheInput, value.Task.CacheKey); err != nil {
		return fmt.Errorf("%w: task cache key: %v", ErrProtocol, err)
	}
	if value.Task.CacheInput.StageProfile != value.Task.Profile ||
		!cacheRangeEqual(value.Task.CacheInput.FrameRange, value.Task.FrameRange) ||
		!cacheRangeEqual(value.Task.CacheInput.SampleRange, value.Task.SampleRange) ||
		!validRange(value.Task.FrameRange) || !validRange(value.Task.SampleRange) {
		return fmt.Errorf("%w: task profile/range cache binding", ErrProtocol)
	}
	wantStage, err := stagedag.StageBindingInput(value.Task.Stage)
	if err != nil {
		return fmt.Errorf("%w: task stage", ErrProtocol)
	}
	stageCount := 0
	for _, input := range value.Task.CacheInput.Inputs {
		if input.Role == stagedag.StageBindingRole {
			stageCount++
			if input != wantStage {
				return fmt.Errorf("%w: stage binding", ErrProtocol)
			}
		}
	}
	dependencyInputCount := 0
	for _, input := range value.Task.CacheInput.Inputs {
		if input.Role == stagedag.DependencyBindingRole {
			dependencyInputCount++
		}
	}
	if stageCount != 1 || len(value.Task.DependsOn) != len(value.Dependencies) || dependencyInputCount != len(value.Dependencies) {
		return fmt.Errorf("%w: task dependency/stage binding", ErrProtocol)
	}
	dependencyJobID := ""
	for index, dependency := range value.Dependencies {
		dependencyID := value.Task.DependsOn[index]
		if !idPattern.MatchString(dependencyID) || dependencyID == value.Task.ID ||
			(index > 0 && dependencyID <= value.Task.DependsOn[index-1]) ||
			dependency.TaskID != dependencyID || dependency.Checkpoint.TaskID != dependency.TaskID {
			return fmt.Errorf("%w: dependency %d identity/order", ErrProtocol, index)
		}
		raw, err := stagedag.CanonicalCheckpoint(dependency.Checkpoint)
		if err != nil {
			return fmt.Errorf("%w: dependency %d checkpoint: %v", ErrProtocol, index, err)
		}
		if _, err := stagedag.DecodeCheckpoint(raw); err != nil {
			return fmt.Errorf("%w: dependency %d checkpoint: %v", ErrProtocol, index, err)
		}
		if dependency.Checkpoint.PlanFingerprint != value.Task.CacheInput.PlanFingerprint ||
			dependency.Checkpoint.ExecutionManifestFingerprint != value.Task.CacheInput.ExecutionManifestFingerprint ||
			stageRank(dependency.Checkpoint.Stage) > stageRank(value.Task.Stage) {
			return fmt.Errorf("%w: dependency %d plan/manifest/stage binding", ErrProtocol, index)
		}
		if dependencyJobID == "" {
			dependencyJobID = dependency.Checkpoint.JobID
		} else if dependency.Checkpoint.JobID != dependencyJobID {
			return fmt.Errorf("%w: dependencies span jobs", ErrProtocol)
		}
		input, err := stagedag.DependencyBindingInput(index, dependency.Checkpoint.CacheKey)
		if err != nil || !containsInput(value.Task.CacheInput.Inputs, input) {
			return fmt.Errorf("%w: dependency %d cache binding", ErrProtocol, index)
		}
	}
	return nil
}

func requestSizeBound(value Request) error {
	taskRaw, err := canonicaljson.Marshal(value.Task)
	if err != nil {
		return fmt.Errorf("%w: task size: %v", ErrProtocol, err)
	}
	// The fixed allowance covers the enclosing object, field names, commas and
	// scalar header fields. Each dependency is measured in its exact canonical
	// representation before the complete request is allocated.
	total := len(taskRaw) + len(value.Profile) + len(value.InvocationID) + 1024
	if total > MaxRequestBytes {
		return fmt.Errorf("%w: request encoding exceeds %d bytes", ErrProtocol, MaxRequestBytes)
	}
	for index, dependency := range value.Dependencies {
		raw, err := canonicaljson.Marshal(dependency)
		if err != nil {
			return fmt.Errorf("%w: dependency %d size: %v", ErrProtocol, index, err)
		}
		if len(raw) > MaxRequestBytes-total {
			return fmt.Errorf("%w: request encoding exceeds %d bytes", ErrProtocol, MaxRequestBytes)
		}
		total += len(raw)
	}
	if total > MaxRequestBytes {
		return fmt.Errorf("%w: request encoding exceeds %d bytes", ErrProtocol, MaxRequestBytes)
	}
	return nil
}

func ValidateResult(value Result) error {
	if value.SchemaVersion != SchemaVersion || value.Profile != ResultProfileV1 || !idPattern.MatchString(value.InvocationID) {
		return fmt.Errorf("%w: result header", ErrProtocol)
	}
	if value.OK {
		if value.Output == nil || value.FailureClass != "" || value.Message != "" {
			return fmt.Errorf("%w: successful result shape", ErrProtocol)
		}
		return validateOutput(*value.Output)
	}
	if value.Output != nil || !validFailureClass(value.FailureClass) || !validText(value.Message) {
		return fmt.Errorf("%w: failed result shape", ErrProtocol)
	}
	return nil
}

func validateOutput(output stagedag.StageOutput) error {
	if !cachekey.CanonicalSHA256(output.ContentHash) || output.SizeBytes < 0 || len(output.Receipt) == 0 || len(output.Receipt) > stagedag.MaxReceiptBytes {
		return fmt.Errorf("%w: output identity", ErrProtocol)
	}
	canonical, err := canonicalize(output.Receipt, stagedag.MaxReceiptBytes)
	if err != nil || len(canonical) == 0 || !bytes.Equal(canonical, output.Receipt) || canonical[0] != '{' {
		return fmt.Errorf("%w: output receipt", ErrProtocol)
	}
	return nil
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

func canonicalize(raw []byte, maximum int) ([]byte, error) {
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = maximum
	limits.MaxStringBytes = maximum
	limits.MaxDepth = 64
	limits.MaxIntegerDigits = 32
	return canonicaljson.CanonicalizeWithLimits(raw, limits)
}

func containsInput(values []cachekey.NormalizedInput, wanted cachekey.NormalizedInput) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cacheRangeEqual(cacheRange *cachekey.IndexRange, taskRange *videoedit.TickRange) bool {
	return cacheRange == nil && taskRange == nil ||
		cacheRange != nil && taskRange != nil && cacheRange.Start == taskRange.First && cacheRange.Count == taskRange.Count
}

func validRange(value *videoedit.TickRange) bool {
	if value == nil {
		return true
	}
	return value.First >= 0 && value.Count > 0 && value.Validate() == nil
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

func cloneRequest(value Request) Request {
	dependencies := value.Dependencies
	value.Task.DependsOn = appendNonNil(value.Task.DependsOn)
	value.Task.CacheInput.Inputs = appendInputs(value.Task.CacheInput.Inputs)
	value.Task.CacheInput.FrameRange = cloneCacheRange(value.Task.CacheInput.FrameRange)
	value.Task.CacheInput.SampleRange = cloneCacheRange(value.Task.CacheInput.SampleRange)
	value.Task.FrameRange = cloneRange(value.Task.FrameRange)
	value.Task.SampleRange = cloneRange(value.Task.SampleRange)
	value.Dependencies = make([]stagedag.Dependency, len(dependencies))
	for index, dependency := range dependencies {
		value.Dependencies[index] = dependency
		value.Dependencies[index].Checkpoint = cloneCheckpoint(dependency.Checkpoint)
	}
	return value
}

func cloneResult(value Result) Result {
	if value.Output != nil {
		output := *value.Output
		output.Receipt = append(json.RawMessage(nil), output.Receipt...)
		value.Output = &output
	}
	return value
}

func cloneCheckpoint(value stagedag.Checkpoint) stagedag.Checkpoint {
	value.FrameRange = cloneRange(value.FrameRange)
	value.SampleRange = cloneRange(value.SampleRange)
	value.Output.Receipt = append(json.RawMessage(nil), value.Output.Receipt...)
	return value
}

func cloneRange(value *videoedit.TickRange) *videoedit.TickRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneCacheRange(value *cachekey.IndexRange) *cachekey.IndexRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func appendNonNil(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func appendInputs(values []cachekey.NormalizedInput) []cachekey.NormalizedInput {
	if values == nil {
		return nil
	}
	return append([]cachekey.NormalizedInput{}, values...)
}

func validText(value string) bool {
	if value == "" || len(value) > MaxMessageBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	return true
}
