package chunkplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const ReceiptSchemaVersion = 1

var receiptIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,255}$`)

// WorkerBinding records the exact worker invocation that produced bytes. It is
// provenance, not a signature or trusted-execution attestation.
type WorkerBinding struct {
	InvocationID     string `json:"invocation_id"`
	ExecutableDigest string `json:"executable_digest"`
}

// OutputIdentity is the immutable payload claim returned by a chunk executor.
// UnitBytes fixes the bytes per composition frame or sample frame.
type OutputIdentity struct {
	ContentHash string `json:"content_hash"`
	SizeBytes   int64  `json:"size_bytes"`
	UnitBytes   int64  `json:"unit_bytes"`
}

// Receipt is self-contained enough to validate the exact cache preimage and
// payload identity without trusting an enclosing checkpoint. The checkpoint is
// still required to bind the claim to a particular job and attempt lifecycle.
type Receipt struct {
	SchemaVersion                int                  `json:"schema_version"`
	Profile                      string               `json:"profile"`
	TaskID                       string               `json:"task_id"`
	Stage                        renderapi.Stage      `json:"stage"`
	StageProfile                 string               `json:"stage_profile"`
	PlanFingerprint              string               `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string               `json:"execution_manifest_fingerprint"`
	CacheInput                   cachekey.Input       `json:"cache_input"`
	CacheKey                     cachekey.Key         `json:"cache_key"`
	FrameRange                   *videoedit.TickRange `json:"frame_range,omitempty"`
	SampleRange                  *videoedit.TickRange `json:"sample_range,omitempty"`
	OutputProfile                string               `json:"output_profile"`
	Output                       OutputIdentity       `json:"output"`
	AttemptCount                 uint8                `json:"attempt_count"`
	Worker                       WorkerBinding        `json:"worker"`
	Fingerprint                  string               `json:"fingerprint"`
}

// NewReceipt binds a validated chunk task to its payload and worker claim.
// Failure and cancellation publish a strictly zero Receipt.
func NewReceipt(ctx context.Context, task stagedag.Task, output OutputIdentity, attempt uint8,
	worker WorkerBinding) (Receipt, error) {
	if ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil context", ErrReceipt)
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	profile, err := validateChunkTask(task)
	if err != nil {
		return Receipt{}, err
	}
	value := Receipt{
		SchemaVersion: ReceiptSchemaVersion, Profile: receiptProfile(profile),
		TaskID: task.ID, Stage: task.Stage, StageProfile: task.Profile,
		PlanFingerprint:              task.CacheInput.PlanFingerprint,
		ExecutionManifestFingerprint: task.CacheInput.ExecutionManifestFingerprint,
		CacheInput:                   cloneCacheInput(task.CacheInput), CacheKey: task.CacheKey,
		FrameRange: cloneRange(task.FrameRange), SampleRange: cloneRange(task.SampleRange),
		OutputProfile: task.CacheInput.OutputProfile, Output: output,
		AttemptCount: attempt, Worker: worker,
		Fingerprint: "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
	}
	if err := validateReceiptShape(value); err != nil {
		return Receipt{}, err
	}
	fingerprint, err := receiptFingerprint(value)
	if err != nil {
		return Receipt{}, err
	}
	value.Fingerprint = fingerprint
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := ValidateReceiptForTask(task, value); err != nil {
		return Receipt{}, err
	}
	return cloneReceipt(value), nil
}

// ValidateReceiptForTask proves that receipt describes task exactly.
func ValidateReceiptForTask(task stagedag.Task, receipt Receipt) error {
	profile, err := validateChunkTask(task)
	if err != nil {
		return err
	}
	if err := validateReceiptShape(receipt); err != nil {
		return err
	}
	if receipt.Profile != receiptProfile(profile) || receipt.TaskID != task.ID || receipt.Stage != task.Stage ||
		receipt.StageProfile != task.Profile || receipt.PlanFingerprint != task.CacheInput.PlanFingerprint ||
		receipt.ExecutionManifestFingerprint != task.CacheInput.ExecutionManifestFingerprint ||
		receipt.CacheKey != task.CacheKey || !equalRange(receipt.FrameRange, task.FrameRange) ||
		!equalRange(receipt.SampleRange, task.SampleRange) || receipt.OutputProfile != task.CacheInput.OutputProfile ||
		!equalCacheInput(receipt.CacheInput, task.CacheInput) {
		return fmt.Errorf("%w: receipt/task binding", ErrReceipt)
	}
	if receipt.AttemptCount > task.Retry.MaxAttempts {
		return fmt.Errorf("%w: receipt attempt exceeds task retry policy", ErrReceipt)
	}
	want, err := receiptFingerprint(receipt)
	if err != nil {
		return err
	}
	if receipt.Fingerprint != want {
		return fmt.Errorf("%w: fingerprint mismatch", ErrReceipt)
	}
	return nil
}

func CanonicalReceipt(value Receipt) ([]byte, error) {
	if err := validateReceiptShape(value); err != nil {
		return nil, err
	}
	want, err := receiptFingerprint(value)
	if err != nil {
		return nil, err
	}
	if value.Fingerprint != want {
		return nil, fmt.Errorf("%w: fingerprint mismatch", ErrReceipt)
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrReceipt, err)
	}
	if len(raw) > MaxReceiptBytes || len(raw) > stagedag.MaxReceiptBytes {
		return nil, fmt.Errorf("%w: canonical bytes exceed %d", ErrLimit, MaxReceiptBytes)
	}
	return raw, nil
}

func DecodeReceipt(raw []byte) (Receipt, error) {
	if len(raw) == 0 || len(raw) > MaxReceiptBytes {
		return Receipt{}, fmt.Errorf("%w: wire bytes %d", ErrReceipt, len(raw))
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = MaxReceiptBytes
	limits.MaxStringBytes = MaxReceiptBytes
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Receipt{}, fmt.Errorf("%w: receipt must be canonical JSON: %v", ErrReceipt, err)
	}
	var value Receipt
	if err := wirejson.Decode(raw, &value); err != nil {
		return Receipt{}, errors.Join(ErrReceipt, err)
	}
	if err := validateReceiptShape(value); err != nil {
		return Receipt{}, err
	}
	want, err := receiptFingerprint(value)
	if err != nil || value.Fingerprint != want {
		return Receipt{}, errors.Join(fmt.Errorf("%w: fingerprint mismatch", ErrReceipt), err)
	}
	return cloneReceipt(value), nil
}

// StageOutput encodes receipt in the exact canonical form required by stagedag.
func StageOutput(receipt Receipt) (stagedag.StageOutput, error) {
	raw, err := CanonicalReceipt(receipt)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	return stagedag.StageOutput{ContentHash: receipt.Output.ContentHash, SizeBytes: receipt.Output.SizeBytes, Receipt: raw}, nil
}

func validateReceiptShape(value Receipt) error {
	profile, ok := profileFromReceipt(value.Profile)
	if value.SchemaVersion != ReceiptSchemaVersion || !ok || !validChunkTaskID(profile, value.TaskID) ||
		!receiptIDPattern.MatchString(value.Worker.InvocationID) || !cachekey.CanonicalSHA256(value.Worker.ExecutableDigest) ||
		!cachekey.CanonicalSHA256(value.PlanFingerprint) || !cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) ||
		!cachekey.CanonicalSHA256(value.Output.ContentHash) || value.Output.SizeBytes <= 0 || value.Output.UnitBytes <= 0 ||
		value.AttemptCount == 0 || value.AttemptCount > stagedag.MaxAttempts || !cachekey.CanonicalSHA256(value.Fingerprint) {
		return fmt.Errorf("%w: receipt shape", ErrReceipt)
	}
	if err := cachekey.Verify(value.CacheInput, value.CacheKey); err != nil {
		return errors.Join(ErrReceipt, err)
	}
	want := profileFor(profile)
	if value.Stage != want.stage || value.StageProfile != want.stageProfile || value.OutputProfile != want.outputProfile ||
		value.CacheInput.Namespace != want.namespace || value.CacheInput.ArtifactKind != want.artifactKind ||
		value.CacheInput.StageProfile != want.stageProfile || value.CacheInput.OutputProfile != want.outputProfile ||
		value.CacheInput.PlanFingerprint != value.PlanFingerprint ||
		value.CacheInput.ExecutionManifestFingerprint != value.ExecutionManifestFingerprint {
		return fmt.Errorf("%w: profile/cache binding", ErrReceipt)
	}
	if err := validateReceiptRanges(profile, value); err != nil {
		return err
	}
	if len(value.CacheInput.Inputs) != 1 {
		return fmt.Errorf("%w: chunk cache input must contain only the stage binding", ErrReceipt)
	}
	stageBinding, err := stagedag.StageBindingInput(value.Stage)
	if err != nil || value.CacheInput.Inputs[0] != stageBinding {
		return fmt.Errorf("%w: stage binding", ErrReceipt)
	}
	count := value.FrameRange
	if profile == domainAudio {
		count = value.SampleRange
	}
	if count.Count > (1<<63-1)/value.Output.UnitBytes || count.Count*value.Output.UnitBytes != value.Output.SizeBytes {
		return fmt.Errorf("%w: payload size is not range count times unit bytes", ErrReceipt)
	}
	return nil
}

func validateReceiptRanges(profile chunkDomain, value Receipt) error {
	if profile == domainVideo {
		if value.FrameRange == nil || value.SampleRange != nil || value.CacheInput.FrameRange == nil || value.CacheInput.SampleRange != nil ||
			value.FrameRange.First != value.CacheInput.FrameRange.Start || value.FrameRange.Count != value.CacheInput.FrameRange.Count {
			return fmt.Errorf("%w: video range binding", ErrReceipt)
		}
	} else if value.SampleRange == nil || value.FrameRange != nil || value.CacheInput.SampleRange == nil || value.CacheInput.FrameRange != nil ||
		value.SampleRange.First != value.CacheInput.SampleRange.Start || value.SampleRange.Count != value.CacheInput.SampleRange.Count {
		return fmt.Errorf("%w: audio range binding", ErrReceipt)
	}
	rangeValue := value.FrameRange
	if profile == domainAudio {
		rangeValue = value.SampleRange
	}
	if err := rangeValue.Validate(); err != nil || rangeValue.First < 0 || rangeValue.Count <= 0 {
		return fmt.Errorf("%w: invalid range", ErrReceipt)
	}
	if rangeValue.Count > profileFor(profile).chunkSize {
		return fmt.Errorf("%w: range exceeds fixed chunk size", ErrReceipt)
	}
	return nil
}

func validateChunkTask(task stagedag.Task) (chunkDomain, error) {
	var profile chunkDomain
	switch {
	case task.Stage == renderapi.StageCompose && task.Profile == VideoStageProfileV1:
		profile = domainVideo
	case task.Stage == renderapi.StageAudio && task.Profile == AudioStageProfileV1:
		profile = domainAudio
	default:
		return 0, fmt.Errorf("%w: task is not a chunk profile", ErrReceipt)
	}
	if !receiptIDPattern.MatchString(task.ID) || task.DependsOn == nil || len(task.DependsOn) != 0 ||
		task.Retry.MaxAttempts == 0 || task.Retry.MaxAttempts > stagedag.MaxAttempts ||
		task.Resources.MemoryBytes == 0 || task.Resources.DiskBytes == 0 {
		return 0, fmt.Errorf("%w: task shape", ErrReceipt)
	}
	if !validChunkTaskID(profile, task.ID) {
		return 0, fmt.Errorf("%w: task ID does not match chunk domain", ErrReceipt)
	}
	value := Receipt{
		Profile: receiptProfile(profile), Stage: task.Stage, StageProfile: task.Profile,
		PlanFingerprint: task.CacheInput.PlanFingerprint, ExecutionManifestFingerprint: task.CacheInput.ExecutionManifestFingerprint,
		CacheInput: task.CacheInput, CacheKey: task.CacheKey, FrameRange: task.FrameRange, SampleRange: task.SampleRange,
		OutputProfile: task.CacheInput.OutputProfile,
	}
	if err := cachekey.Verify(task.CacheInput, task.CacheKey); err != nil {
		return 0, errors.Join(ErrReceipt, err)
	}
	want := profileFor(profile)
	if value.Stage != want.stage || value.StageProfile != want.stageProfile || value.OutputProfile != want.outputProfile ||
		value.CacheInput.Namespace != want.namespace || value.CacheInput.ArtifactKind != want.artifactKind ||
		value.CacheInput.StageProfile != want.stageProfile ||
		len(value.CacheInput.Inputs) != 1 {
		return 0, fmt.Errorf("%w: task cache profile", ErrReceipt)
	}
	stageBinding, err := stagedag.StageBindingInput(task.Stage)
	if err != nil || task.CacheInput.Inputs[0] != stageBinding {
		return 0, fmt.Errorf("%w: task stage binding", ErrReceipt)
	}
	if err := validateReceiptRanges(profile, value); err != nil {
		return 0, err
	}
	return profile, nil
}

func receiptProfile(profile chunkDomain) string {
	if profile == domainVideo {
		return VideoReceiptProfileV1
	}
	return AudioReceiptProfileV1
}

func profileFromReceipt(profile string) (chunkDomain, bool) {
	switch profile {
	case VideoReceiptProfileV1:
		return domainVideo, true
	case AudioReceiptProfileV1:
		return domainAudio, true
	default:
		return 0, false
	}
}

func validChunkTaskID(profile chunkDomain, value string) bool {
	prefix := "audio-"
	if profile == domainVideo {
		prefix = "video-"
	}
	if !receiptIDPattern.MatchString(value) || !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+5 {
		return false
	}
	for _, digit := range value[len(prefix):] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func receiptFingerprint(value Receipt) (string, error) {
	value = cloneReceipt(value)
	value.Fingerprint = ""
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return "", errors.Join(ErrReceipt, err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("cutly.chunk-receipt.v1\x00"))
	_, _ = hash.Write(raw)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func cloneReceipt(value Receipt) Receipt {
	value.CacheInput = cloneCacheInput(value.CacheInput)
	value.FrameRange = cloneRange(value.FrameRange)
	value.SampleRange = cloneRange(value.SampleRange)
	return value
}

func cloneCacheInput(value cachekey.Input) cachekey.Input {
	value.Inputs = append([]cachekey.NormalizedInput(nil), value.Inputs...)
	if value.FrameRange != nil {
		copy := *value.FrameRange
		value.FrameRange = &copy
	}
	if value.SampleRange != nil {
		copy := *value.SampleRange
		value.SampleRange = &copy
	}
	return value
}

func cloneRange(value *videoedit.TickRange) *videoedit.TickRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func equalRange(left, right *videoedit.TickRange) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalCacheInput(left, right cachekey.Input) bool {
	leftRaw, leftErr := canonicaljson.Marshal(left)
	rightRaw, rightErr := canonicaljson.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}
