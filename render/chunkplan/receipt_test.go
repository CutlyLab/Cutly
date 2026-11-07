package chunkplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestReceiptCanonicalRoundTripAndTaskBinding(t *testing.T) {
	plan := mustPlan(t, testJob("job-receipt", 1, 1))
	task := plan.Tasks[0]
	receipt := mustReceipt(t, task, 1)
	raw, err := CanonicalReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, mustCanonical(t, decoded)) || decoded.Fingerprint != receipt.Fingerprint {
		t.Fatal("receipt round trip drift")
	}
	if err := ValidateReceiptForTask(task, decoded); err != nil {
		t.Fatal(err)
	}
	output, err := StageOutput(decoded)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan.Plan, task, output, 1)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Output.ContentHash != receipt.Output.ContentHash || checkpoint.Output.SizeBytes != receipt.Output.SizeBytes {
		t.Fatalf("checkpoint output = %+v", checkpoint.Output)
	}

	// Receipt owns nested cache inputs/ranges.
	task.CacheInput.Inputs[0].ContentHash = digest("mutated")
	if decoded.CacheInput.Inputs[0].ContentHash == task.CacheInput.Inputs[0].ContentHash {
		t.Fatal("receipt aliases task cache inputs")
	}
}

func TestReceiptStrictWireAndTamperRejection(t *testing.T) {
	plan := mustPlan(t, testJob("job-wire", 1, 1))
	receipt := mustReceipt(t, plan.Tasks[0], 1)
	raw := mustCanonical(t, receipt)
	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":1}`)...)
	if _, err := DecodeReceipt(withUnknown); !errors.Is(err, ErrReceipt) {
		t.Fatalf("unknown field = %v", err)
	}
	duplicate := bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := DecodeReceipt(duplicate); !errors.Is(err, ErrReceipt) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := DecodeReceipt(append([]byte(" "), raw...)); !errors.Is(err, ErrReceipt) {
		t.Fatalf("noncanonical = %v", err)
	}
	tampered := cloneJSON(t, receipt)
	tampered.Output.ContentHash = digest("other")
	if _, err := CanonicalReceipt(tampered); !errors.Is(err, ErrReceipt) {
		t.Fatalf("tampered = %v", err)
	}
	wrongTask := plan.Tasks[0]
	wrongTask.ID = "audio-99999"
	if err := ValidateReceiptForTask(wrongTask, receipt); !errors.Is(err, ErrReceipt) {
		t.Fatalf("wrong task = %v", err)
	}
}

func TestHistoricalReceiptSurvivesRetryPolicyReductionButNewReceiptDoesNot(t *testing.T) {
	plan := mustPlan(t, testJob("job-receipt-policy-reduction", 1, 0))
	historicalTask := plan.Tasks[0]
	if historicalTask.Retry.MaxAttempts != 3 {
		t.Fatalf("historical policy = %+v", historicalTask.Retry)
	}
	historical := mustReceipt(t, historicalTask, 3)
	reducedTask := historicalTask
	reducedTask.Retry = stagedag.RetryPolicy{MaxAttempts: 1}
	if err := ValidateReceiptForTask(reducedTask, historical); err != nil {
		t.Fatalf("historical receipt rejected after policy reduction: %v", err)
	}
	if _, err := NewReceipt(context.Background(), reducedTask, historical.Output, 2, historical.Worker); !errors.Is(err, ErrReceipt) {
		t.Fatalf("new receipt above reduced policy = %v", err)
	}
}

func TestNewReceiptFailureIsZeroAndBoundsOutput(t *testing.T) {
	plan := mustPlan(t, testJob("job-failure", 1, 1))
	task := plan.Tasks[0]
	unitBytes := int64(4)
	rangeValue := task.SampleRange
	if task.FrameRange != nil {
		rangeValue = task.FrameRange
	}
	valid := OutputIdentity{ContentHash: digest("payload"), SizeBytes: rangeValue.Count * unitBytes, UnitBytes: unitBytes}
	worker := WorkerBinding{InvocationID: "worker-run", ExecutableDigest: digest("worker")}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if receipt, err := NewReceipt(canceled, task, valid, 1, worker); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(receipt, Receipt{}) {
		t.Fatalf("canceled = %+v, %v", receipt, err)
	}
	invalid := valid
	invalid.SizeBytes++
	if receipt, err := NewReceipt(context.Background(), task, invalid, 1, worker); !errors.Is(err, ErrReceipt) || !reflect.DeepEqual(receipt, Receipt{}) {
		t.Fatalf("size = %+v, %v", receipt, err)
	}
	if receipt, err := NewReceipt(context.Background(), task, valid, task.Retry.MaxAttempts+1, worker); !errors.Is(err, ErrReceipt) || !reflect.DeepEqual(receipt, Receipt{}) {
		t.Fatalf("attempt = %+v, %v", receipt, err)
	}
	if receipt, err := NewReceipt(nil, task, valid, 1, worker); !errors.Is(err, ErrReceipt) || !reflect.DeepEqual(receipt, Receipt{}) {
		t.Fatalf("nil context = %+v, %v", receipt, err)
	}
}

func TestNewReceiptRejectsTaskLargerThanProtocolChunk(t *testing.T) {
	config := testConfig()
	tests := []struct {
		name      string
		domain    chunkDomain
		oversized renderapi.RenderJob
		valid     renderapi.RenderJob
		resources stagedag.ResourceEstimate
	}{
		{
			name: "video", domain: domainVideo,
			oversized: testJob("job-oversized-video", FrameGroupCount+1, 0),
			valid:     testJob("job-valid-video", FrameGroupCount, 0), resources: config.VideoResources,
		},
		{
			name: "audio", domain: domainAudio,
			oversized: testJob("job-oversized-audio", 0, SampleBlockCount+1),
			valid:     testJob("job-valid-audio", 0, SampleBlockCount), resources: config.AudioResources,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			count := profileFor(test.domain).chunkSize + 1
			task, err := makeTask(test.oversized, test.domain, 0,
				videoedit.TickRange{First: 0, Count: count}, test.resources, config.Retry)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := NewReceipt(context.Background(), task, OutputIdentity{
				ContentHash: digest("oversized-" + test.name), SizeBytes: count * 4, UnitBytes: 4,
			}, 1, WorkerBinding{InvocationID: "run-oversized-" + test.name, ExecutableDigest: digest("worker")})
			if !errors.Is(err, ErrReceipt) || !reflect.DeepEqual(receipt, Receipt{}) {
				t.Fatalf("receipt = %+v, error = %v", receipt, err)
			}

			validPlan := mustPlan(t, test.valid)
			forged := mustReceipt(t, validPlan.Tasks[0], 1)
			forged.CacheInput = cloneCacheInput(task.CacheInput)
			forged.CacheKey = task.CacheKey
			forged.FrameRange = cloneRange(task.FrameRange)
			forged.SampleRange = cloneRange(task.SampleRange)
			forged.Output.SizeBytes = count * forged.Output.UnitBytes
			forged.Fingerprint, err = receiptFingerprint(forged)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonicaljson.Marshal(forged)
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := DecodeReceipt(raw); !errors.Is(err, ErrReceipt) || !reflect.DeepEqual(decoded, Receipt{}) {
				t.Fatalf("decoded = %+v, error = %v", decoded, err)
			}
		})
	}
}

func mustPlan(t *testing.T, job renderapi.RenderJob) Plan {
	t.Helper()
	plan, err := Build(context.Background(), job, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func mustReceipt(t *testing.T, task stagedag.Task, attempt uint8) Receipt {
	t.Helper()
	rangeValue := task.FrameRange
	unitBytes := int64(32)
	if rangeValue == nil {
		rangeValue = task.SampleRange
		unitBytes = 4
	}
	receipt, err := NewReceipt(context.Background(), task, OutputIdentity{
		ContentHash: digest("payload:" + task.ID), SizeBytes: rangeValue.Count * unitBytes, UnitBytes: unitBytes,
	}, attempt, WorkerBinding{InvocationID: "run-" + task.ID, ExecutableDigest: digest("worker")})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func mustCanonical(t *testing.T, value Receipt) []byte {
	t.Helper()
	raw, err := CanonicalReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
