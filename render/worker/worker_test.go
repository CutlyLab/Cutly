package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestProtocolStrictRoundTripAndOwnership(t *testing.T) {
	request := testRequest(t)
	raw, err := CanonicalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoded.Task.CacheInput.Inputs[0].Role = "mutated"
	again, err := DecodeRequest(raw)
	if err != nil || again.Task.CacheInput.Inputs[0].Role != stagedag.StageBindingRole {
		t.Fatalf("owned request = %+v, %v", again, err)
	}
	result := successResult(t, request.InvocationID)
	resultRaw, err := CanonicalResult(result)
	if err != nil {
		t.Fatal(err)
	}
	decodedResult, err := DecodeResult(resultRaw)
	if err != nil || !decodedResult.OK {
		t.Fatalf("result = %+v, %v", decodedResult, err)
	}
	decodedResult.Output.Receipt[0] = 'x'
	if result.Output.Receipt[0] == 'x' {
		t.Fatal("decoded result aliases source")
	}
	for _, invalid := range [][]byte{
		append([]byte(" "), raw...),
		[]byte(`{"schema_version":1,"profile":"cutly.render.worker-request.v1","invocation_id":"x","invocation_id":"y","task":{},"dependencies":[]}`),
	} {
		if _, err := DecodeRequest(invalid); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
}

func TestProtocolRejectsTaskBindingDrift(t *testing.T) {
	base := testRequest(t)
	invalid := []Request{
		func() Request { value := cloneRequest(base); value.Task.DependsOn = nil; return value }(),
		func() Request { value := cloneRequest(base); value.Task.Retry.MaxAttempts = 0; return value }(),
		func() Request {
			value := cloneRequest(base)
			value.Task.FrameRange = &videoedit.TickRange{First: 0, Count: 1}
			return value
		}(),
		func() Request {
			value := cloneRequest(base)
			value.Task.DependsOn = []string{"missing"}
			return value
		}(),
	}
	for index, value := range invalid {
		if _, err := CanonicalRequest(value); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid request %d accepted: %v", index, err)
		}
	}
}

func TestProtocolBindsDependencyCheckpointToTask(t *testing.T) {
	request := testDependentRequest(t)
	if _, err := CanonicalRequest(request); err != nil {
		t.Fatalf("valid dependent request: %v", err)
	}
	for name, mutate := range map[string]func(*Request){
		"plan": func(value *Request) {
			value.Dependencies[0].Checkpoint.PlanFingerprint = digest('c')
		},
		"stage": func(value *Request) {
			value.Dependencies[0].Checkpoint.Stage = renderapi.StagePublish
		},
		"cache key": func(value *Request) {
			value.Dependencies[0].Checkpoint.CacheKey = value.Task.CacheKey
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := cloneRequest(request)
			mutate(&value)
			if _, err := CanonicalRequest(value); !errors.Is(err, ErrProtocol) {
				t.Fatalf("dependency drift accepted: %v", err)
			}
		})
	}
}

func TestRunnerSuccessFailureIntegrityAndTimeout(t *testing.T) {
	request := testRequest(t)

	t.Run("success", func(t *testing.T) {
		runner := testRunner(t, "success", 10*time.Second)
		result, report, err := runner.Run(context.Background(), request)
		if err != nil || !result.OK || report.ExitCode != 0 || report.ExecutableHash == "" {
			t.Fatalf("run = %+v, report=%+v, err=%v", result, report, err)
		}
		output, err := runner.Execute(context.Background(), request.Task, request.Dependencies)
		if err != nil || output.ContentHash != result.Output.ContentHash {
			t.Fatalf("execute = %+v, %v", output, err)
		}
	})

	t.Run("business failure", func(t *testing.T) {
		runner := testRunner(t, "failure", 10*time.Second)
		result, _, err := runner.Run(context.Background(), request)
		if err != nil || result.OK || result.FailureClass != renderapi.FailureNonRetryable {
			t.Fatalf("run = %+v, %v", result, err)
		}
		_, err = runner.Execute(context.Background(), request.Task, request.Dependencies)
		var stage *stagedag.StageError
		if !errors.As(err, &stage) || stage.Class != renderapi.FailureNonRetryable {
			t.Fatalf("execute failure = %v", err)
		}
	})

	for _, mode := range []string{"malformed", "mismatch", "exit", "flood", "panic", "stderr-flood"} {
		t.Run(mode, func(t *testing.T) {
			runner := testRunner(t, mode, 10*time.Second)
			_, report, err := runner.Run(context.Background(), request)
			var process *ProcessError
			if !errors.As(err, &process) || report.ExecutableHash == "" {
				t.Fatalf("process error = %v report=%+v", err, report)
			}
			if mode == "malformed" || mode == "mismatch" || mode == "flood" {
				if process.Class != renderapi.FailureIntegrity {
					t.Fatalf("class = %s", process.Class)
				}
			} else if mode == "panic" {
				if process.Class != renderapi.FailureTransient || !strings.Contains(report.Stderr, "synthetic worker panic") {
					t.Fatalf("panic report = %+v class=%s", report, process.Class)
				}
			} else if report.ExitCode != 7 {
				t.Fatalf("exit report = %+v", report)
			}
			if mode == "stderr-flood" && (!report.StderrTruncated || len(report.Stderr) != DefaultMaxStderrBytes) {
				t.Fatalf("stderr bound report = %+v len=%d", report, len(report.Stderr))
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		runner := testRunner(t, "hang", 40*time.Millisecond)
		_, report, err := runner.Run(context.Background(), request)
		var process *ProcessError
		if !errors.As(err, &process) || process.Class != renderapi.FailureCancellation || !report.TimedOut || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout = %v report=%+v", err, report)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		runner := testRunner(t, "hang", time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		_, report, err := runner.Run(ctx, request)
		var process *ProcessError
		if !errors.As(err, &process) || process.Class != renderapi.FailureCancellation || !report.Canceled || report.TimedOut || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v report=%+v", err, report)
		}
	})
}

func TestRunnerRejectsUnsafeConfiguration(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("symlink executable", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "worker-link")
		if err := os.Symlink(executable, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink privilege unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if _, err := NewRunner(Config{Executable: link, WorkingDirectory: t.TempDir()}); !errors.Is(err, ErrRunner) {
			t.Fatalf("symlink executable = %v", err)
		}
	})
	t.Run("duplicate environment", func(t *testing.T) {
		if _, err := NewRunner(Config{
			Executable: executable, WorkingDirectory: t.TempDir(), Environment: []string{"A=1", "A=2"},
		}); !errors.Is(err, ErrRunner) {
			t.Fatalf("duplicate environment = %v", err)
		}
	})
}

func TestBoundedStderrRemainsBoundedAfterUTF8Repair(t *testing.T) {
	writer := &boundedWriter{maximum: 4}
	if _, err := writer.Write([]byte{0xff, 0xff, 0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	value, truncated := writer.text()
	if !truncated || len(value) > 4 || !utf8.ValidString(value) {
		t.Fatalf("stderr = %q bytes=%d truncated=%v", value, len(value), truncated)
	}
}

func TestWorkerHelperProcess(t *testing.T) {
	mode := os.Getenv("CUTLY_WORKER_HELPER")
	if mode == "" {
		return
	}
	if mode == "descendant" {
		heartbeat := os.Getenv("CUTLY_WORKER_DESCENDANT_HEARTBEAT")
		for sequence := 0; ; sequence++ {
			if err := os.WriteFile(heartbeat, []byte(fmt.Sprintf("%d", sequence)), 0o600); err != nil {
				os.Exit(99)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, MaxRequestBytes+1))
	if err != nil {
		os.Exit(91)
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		os.Exit(92)
	}
	switch mode {
	case "success":
		writeHelperResult(successResult(t, request.InvocationID))
		os.Exit(0)
	case "failure":
		writeHelperResult(Result{
			SchemaVersion: SchemaVersion, Profile: ResultProfileV1, InvocationID: request.InvocationID,
			FailureClass: renderapi.FailureNonRetryable, Message: "worker rejected input",
		})
		os.Exit(0)
	case "malformed":
		_, _ = io.WriteString(os.Stdout, "{}")
	case "mismatch":
		writeHelperResult(successResult(t, "run-wrong"))
		os.Exit(0)
	case "exit":
		_, _ = io.WriteString(os.Stderr, "synthetic crash")
		os.Exit(7)
	case "flood":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", MaxResultBytes+1024))
	case "hang":
		time.Sleep(time.Hour)
	case "orphan-success":
		heartbeat := os.Getenv("CUTLY_WORKER_DESCENDANT_HEARTBEAT")
		if heartbeat == "" {
			os.Exit(96)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestWorkerHelperProcess$")
		child.Env = []string{
			"CUTLY_WORKER_HELPER=descendant",
			"CUTLY_WORKER_DESCENDANT_HEARTBEAT=" + heartbeat,
		}
		if err := child.Start(); err != nil {
			os.Exit(97)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(heartbeat); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(98)
			}
			time.Sleep(5 * time.Millisecond)
		}
		writeHelperResult(successResult(t, request.InvocationID))
		os.Exit(0)
	case "panic":
		panic("synthetic worker panic")
	case "stderr-flood":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("e", DefaultMaxStderrBytes+1024))
		os.Exit(7)
	default:
		os.Exit(93)
	}
}

func writeHelperResult(result Result) {
	raw, err := CanonicalResult(result)
	if err != nil {
		os.Exit(94)
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		os.Exit(95)
	}
}

func testRunner(t *testing.T, mode string, timeout time.Duration) *Runner {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Config{
		Executable: executable, Arguments: []string{"-test.run=^TestWorkerHelperProcess$"},
		Environment: []string{"CUTLY_WORKER_HELPER=" + mode}, WorkingDirectory: t.TempDir(),
		Timeout: timeout, TerminationGrace: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func testRequest(t *testing.T) Request {
	t.Helper()
	stage, err := stagedag.StageBindingInput(renderapi.StageCompose)
	if err != nil {
		t.Fatal(err)
	}
	input := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: "cutly.cache.test.v1", ArtifactKind: "cutly.chunk.test.v1",
		PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('b'),
		StageProfile: "cutly.stage.test.v1", Inputs: []cachekey.NormalizedInput{stage},
		OutputProfile: "cutly.output.test.v1",
	}
	key, err := cachekey.New(input)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		SchemaVersion: SchemaVersion, Profile: RequestProfileV1, InvocationID: "run-test",
		Task: stagedag.Task{
			ID: "compose", Stage: renderapi.StageCompose, Profile: input.StageProfile,
			DependsOn: []string{}, CacheInput: input, CacheKey: key,
			Resources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1},
			Retry:     stagedag.RetryPolicy{MaxAttempts: 1},
		},
		Dependencies: []stagedag.Dependency{},
	}
}

func testDependentRequest(t *testing.T) Request {
	t.Helper()
	planFingerprint := digest('a')
	manifestFingerprint := digest('b')
	composeStage, err := stagedag.StageBindingInput(renderapi.StageCompose)
	if err != nil {
		t.Fatal(err)
	}
	composeInput := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: "cutly.cache.test.v1", ArtifactKind: "cutly.chunk.test.v1",
		PlanFingerprint: planFingerprint, ExecutionManifestFingerprint: manifestFingerprint,
		StageProfile: "cutly.stage.compose.v1", Inputs: []cachekey.NormalizedInput{composeStage},
		OutputProfile: "cutly.output.test.v1",
	}
	composeKey, err := cachekey.New(composeInput)
	if err != nil {
		t.Fatal(err)
	}
	dependencyBinding, err := stagedag.DependencyBindingInput(0, composeKey)
	if err != nil {
		t.Fatal(err)
	}
	encodeStage, err := stagedag.StageBindingInput(renderapi.StageEncode)
	if err != nil {
		t.Fatal(err)
	}
	encodeInput := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: "cutly.cache.test.v1", ArtifactKind: "cutly.chunk.test.v1",
		PlanFingerprint: planFingerprint, ExecutionManifestFingerprint: manifestFingerprint,
		StageProfile:  "cutly.stage.encode.v1",
		Inputs:        []cachekey.NormalizedInput{dependencyBinding, encodeStage},
		OutputProfile: "cutly.output.test.v1",
	}
	encodeKey, err := cachekey.New(encodeInput)
	if err != nil {
		t.Fatal(err)
	}
	compose := stagedag.Task{
		ID: "compose", Stage: renderapi.StageCompose, Profile: composeInput.StageProfile,
		DependsOn: []string{}, CacheInput: composeInput, CacheKey: composeKey,
		Resources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1},
		Retry:     stagedag.RetryPolicy{MaxAttempts: 1},
	}
	encode := stagedag.Task{
		ID: "encode", Stage: renderapi.StageEncode, Profile: encodeInput.StageProfile,
		DependsOn: []string{"compose"}, CacheInput: encodeInput, CacheKey: encodeKey,
		Resources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1},
		Retry:     stagedag.RetryPolicy{MaxAttempts: 1},
	}
	plan := stagedag.Plan{
		Profile: stagedag.PlanProfileV1, JobID: "job-1", PlanFingerprint: planFingerprint,
		ExecutionManifestFingerprint: manifestFingerprint, Tasks: []stagedag.Task{compose, encode},
	}
	if err := stagedag.ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan, compose, successResult(t, "unused").OutputValue(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		SchemaVersion: SchemaVersion, Profile: RequestProfileV1, InvocationID: "run-dependent",
		Task: encode, Dependencies: []stagedag.Dependency{{TaskID: compose.ID, Checkpoint: checkpoint}},
	}
}

func successResult(t *testing.T, invocationID string) Result {
	t.Helper()
	receipt, err := canonicaljson.Marshal(map[string]any{"profile": "cutly.worker-test-output.v1"})
	if err != nil {
		t.Fatal(err)
	}
	return Result{
		SchemaVersion: SchemaVersion, Profile: ResultProfileV1, InvocationID: invocationID, OK: true,
		Output: &stagedag.StageOutput{ContentHash: shaDigest("worker-output"), SizeBytes: 42, Receipt: receipt},
	}
}

func shaDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func digest(fill byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = fill
	}
	return "sha256:" + string(value)
}
