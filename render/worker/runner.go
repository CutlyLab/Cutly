package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	RunnerProfileV1           = "cutly.render.local-process-worker.v1"
	DefaultTimeout            = 5 * time.Minute
	DefaultTerminationGrace   = 2 * time.Second
	DefaultMaxStderrBytes     = 64 << 10
	DefaultMaxExecutableBytes = int64(512 << 20)
	MaxArguments              = 256
	MaxEnvironment            = 256
	MaxArgumentBytes          = 1 << 20
)

var (
	ErrRunner       = errors.New("worker: runner failure")
	ErrOutputLimit  = errors.New("worker: output limit exceeded")
	ErrUnsupported  = errors.New("worker: process isolation is unsupported on this platform")
	ErrDescendants  = errors.New("worker: descendants survived direct worker exit")
	environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Config struct {
	Executable         string
	Arguments          []string
	Environment        []string
	WorkingDirectory   string
	Timeout            time.Duration
	TerminationGrace   time.Duration
	MaxStderrBytes     int
	MaxExecutableBytes int64
}

type Runner struct {
	executable          string
	arguments           []string
	environment         []string
	workingDirectory    string
	timeout             time.Duration
	terminationGrace    time.Duration
	maxStderrBytes      int
	executableHash      string
	descendantIsolation bool
}

type Report struct {
	Profile             string `json:"profile"`
	ExecutableHash      string `json:"executable_hash"`
	ExitCode            int    `json:"exit_code"`
	Signal              string `json:"signal,omitempty"`
	Stderr              string `json:"stderr,omitempty"`
	StderrTruncated     bool   `json:"stderr_truncated"`
	TimedOut            bool   `json:"timed_out"`
	Canceled            bool   `json:"canceled"`
	DescendantIsolation bool   `json:"descendant_isolation"`
}

type ProcessError struct {
	Class  renderapi.FailureClass
	Report Report
	Cause  error
}

func (value *ProcessError) Error() string {
	if value == nil {
		return "worker: process failure"
	}
	return fmt.Sprintf("worker: process failure class=%s exit=%d signal=%s: %v", value.Class, value.Report.ExitCode, value.Report.Signal, value.Cause)
}

func (value *ProcessError) Unwrap() error {
	if value == nil {
		return nil
	}
	return value.Cause
}

func NewRunner(config Config) (*Runner, error) {
	if !processIsolationSupported() {
		return nil, ErrUnsupported
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	grace := config.TerminationGrace
	if grace == 0 {
		grace = DefaultTerminationGrace
	}
	maximumStderr := config.MaxStderrBytes
	if maximumStderr == 0 {
		maximumStderr = DefaultMaxStderrBytes
	}
	maximumExecutable := config.MaxExecutableBytes
	if maximumExecutable == 0 {
		maximumExecutable = DefaultMaxExecutableBytes
	}
	if timeout <= 0 || grace <= 0 || maximumStderr <= 0 || maximumStderr > MaxResultBytes || maximumExecutable <= 0 {
		return nil, fmt.Errorf("%w: invalid runner limits", ErrRunner)
	}
	executable, executableHash, err := verifyExecutable(config.Executable, maximumExecutable)
	if err != nil {
		return nil, err
	}
	working, err := verifyWorkingDirectory(config.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	arguments, err := validateArguments(config.Arguments)
	if err != nil {
		return nil, err
	}
	environment, err := validateEnvironment(config.Environment)
	if err != nil {
		return nil, err
	}
	return &Runner{
		executable: executable, executableHash: executableHash, arguments: arguments,
		environment: environment, workingDirectory: working, timeout: timeout,
		terminationGrace: grace, maxStderrBytes: maximumStderr, descendantIsolation: descendantIsolation(),
	}, nil
}

func (runner *Runner) Execute(ctx context.Context, task stagedag.Task, dependencies []stagedag.Dependency) (stagedag.StageOutput, error) {
	if runner == nil || ctx == nil {
		return stagedag.StageOutput{}, fmt.Errorf("%w: initialized runner and context are required", ErrRunner)
	}
	if err := ctx.Err(); err != nil {
		return stagedag.StageOutput{}, err
	}
	invocationID, err := invocationIdentity(task, dependencies)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	result, _, err := runner.Run(ctx, Request{
		SchemaVersion: SchemaVersion, Profile: RequestProfileV1, InvocationID: invocationID,
		Task: task, Dependencies: dependencies,
	})
	if err != nil {
		var process *ProcessError
		if errors.As(err, &process) {
			return stagedag.StageOutput{}, &stagedag.StageError{Class: process.Class, Err: err}
		}
		return stagedag.StageOutput{}, err
	}
	if !result.OK {
		return stagedag.StageOutput{}, &stagedag.StageError{Class: result.FailureClass, Err: errors.New(result.Message)}
	}
	return cloneResult(result).OutputValue(), nil
}

func (value Result) OutputValue() stagedag.StageOutput {
	if value.Output == nil {
		return stagedag.StageOutput{}
	}
	output := *value.Output
	output.Receipt = append([]byte(nil), output.Receipt...)
	return output
}

func (runner *Runner) Run(ctx context.Context, request Request) (Result, Report, error) {
	if runner == nil || runner.executable == "" || ctx == nil {
		return Result{}, Report{}, fmt.Errorf("%w: initialized runner and context are required", ErrRunner)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, Report{}, err
	}
	requestRaw, err := CanonicalRequest(request)
	if err != nil {
		return Result{}, Report{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, runner.timeout)
	defer cancel()
	stdout := &boundedWriter{maximum: MaxResultBytes, fail: true}
	stderr := &boundedWriter{maximum: runner.maxStderrBytes}
	command := exec.Command(runner.executable, runner.arguments...)
	command.Dir = runner.workingDirectory
	command.Env = append([]string{}, runner.environment...)
	command.Stdin = bytes.NewReader(requestRaw)
	command.Stdout = stdout
	command.Stderr = stderr
	// Bound Wait's pipe-drain phase if a descendant retains inherited stdout
	// or stderr after the direct worker exits.
	command.WaitDelay = runner.terminationGrace
	configureProcess(command)
	report := Report{
		Profile: RunnerProfileV1, ExecutableHash: runner.executableHash,
		ExitCode: -1, DescendantIsolation: runner.descendantIsolation,
	}
	if err := command.Start(); err != nil {
		report.Stderr, report.StderrTruncated = stderr.text()
		return Result{}, report, &ProcessError{Class: renderapi.FailureResource, Report: report, Cause: err}
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-runCtx.Done():
		report.Canceled = ctx.Err() != nil
		report.TimedOut = ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded)
		terminateErr := terminateProcess(command.Process)
		timer := time.NewTimer(runner.terminationGrace)
		select {
		case waitErr = <-wait:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			killErr := killProcess(command.Process)
			waitErr = <-wait
			terminateErr = errors.Join(terminateErr, killErr)
		}
		waitErr = errors.Join(runCtx.Err(), terminateErr, waitErr)
	}
	// A direct worker can exit after spawning a background encoder with closed
	// stdio. WaitDelay cannot observe that descendant. Reap the process group on
	// every exit path; a nominal success with live descendants is retried rather
	// than accepting an output that may still be mutating.
	waitErr = errors.Join(waitErr, cleanupDescendants(command.Process, runner.terminationGrace))
	if runErr := runCtx.Err(); runErr != nil {
		waitErr = errors.Join(runErr, waitErr)
	}
	// Cleanup itself is part of the bounded invocation. If its grace interval
	// crosses the caller deadline, reflect that in the report even when the
	// direct worker had already exited before the original select observed it.
	report.Canceled = report.Canceled || ctx.Err() != nil
	report.TimedOut = report.TimedOut || ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded)
	report.ExitCode = processStateExitCode(command.ProcessState)
	report.Signal = exitSignal(waitErr)
	report.Stderr, report.StderrTruncated = stderr.text()
	if runCtx.Err() != nil {
		return Result{}, report, &ProcessError{Class: renderapi.FailureCancellation, Report: report, Cause: waitErr}
	}
	if stdout.exceededValue() {
		return Result{}, report, &ProcessError{Class: renderapi.FailureIntegrity, Report: report, Cause: ErrOutputLimit}
	}
	if waitErr != nil {
		return Result{}, report, &ProcessError{Class: renderapi.FailureTransient, Report: report, Cause: waitErr}
	}
	result, err := DecodeResult(stdout.bytes())
	if err != nil {
		return Result{}, report, &ProcessError{Class: renderapi.FailureIntegrity, Report: report, Cause: err}
	}
	if result.InvocationID != request.InvocationID {
		return Result{}, report, &ProcessError{Class: renderapi.FailureIntegrity, Report: report, Cause: errors.New("worker result correlation mismatch")}
	}
	return result, report, nil
}

func invocationIdentity(task stagedag.Task, dependencies []stagedag.Dependency) (string, error) {
	raw, err := CanonicalRequest(Request{
		SchemaVersion: SchemaVersion,
		Profile:       RequestProfileV1,
		InvocationID:  "run-identity-preimage",
		Task:          task,
		Dependencies:  dependencies,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "run-" + hex.EncodeToString(digest[:16]), nil
}

type boundedWriter struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	maximum  int
	fail     bool
	exceeded bool
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	remaining := writer.maximum - writer.buffer.Len()
	if remaining < 0 {
		remaining = 0
	}
	written := len(value)
	if len(value) > remaining {
		writer.exceeded = true
		value = value[:remaining]
	}
	_, _ = writer.buffer.Write(value)
	if writer.exceeded && writer.fail {
		return len(value), ErrOutputLimit
	}
	return written, nil
}

func (writer *boundedWriter) bytes() []byte {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return append([]byte(nil), writer.buffer.Bytes()...)
}

func (writer *boundedWriter) text() (string, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	original := writer.buffer.String()
	value := strings.ToValidUTF8(original, "�")
	truncated := writer.exceeded || value != original
	if len(value) > writer.maximum {
		value = strings.ToValidUTF8(value[:writer.maximum], "")
		truncated = true
	}
	return value, truncated
}

func (writer *boundedWriter) exceededValue() bool {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.exceeded
}

func verifyExecutable(path string, maximum int64) (string, string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", fmt.Errorf("%w: executable must be a clean absolute path", ErrRunner)
	}
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() || named.Mode()&os.ModeSymlink != 0 || !executableModeAllowed(named) || named.Size() < 0 || named.Size() > maximum {
		return "", "", errors.Join(fmt.Errorf("%w: executable must be a bounded executable regular file", ErrRunner), err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(named, opened) {
		return "", "", errors.Join(fmt.Errorf("%w: executable changed while opening", ErrRunner), statErr, file.Close())
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if written != named.Size() || written > maximum || copyErr != nil || closeErr != nil {
		return "", "", errors.Join(fmt.Errorf("%w: executable changed or exceeded its limit", ErrRunner), copyErr, closeErr)
	}
	return path, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyWorkingDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%w: working directory must be a clean absolute path", ErrRunner)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(fmt.Errorf("%w: working directory must be a non-symlink directory", ErrRunner), err)
	}
	return path, nil
}

func validateArguments(values []string) ([]string, error) {
	if len(values) > MaxArguments {
		return nil, fmt.Errorf("%w: too many arguments", ErrRunner)
	}
	return validateStrings(values)
}

func validateEnvironment(values []string) ([]string, error) {
	if len(values) > MaxEnvironment {
		return nil, fmt.Errorf("%w: too many environment entries", ErrRunner)
	}
	result, err := validateStrings(values)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(result))
	for _, value := range result {
		name, _, found := strings.Cut(value, "=")
		if !found || !environmentName.MatchString(name) {
			return nil, fmt.Errorf("%w: invalid environment entry", ErrRunner)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate environment entry %q", ErrRunner, name)
		}
		seen[name] = struct{}{}
	}
	return result, nil
}

func validateStrings(values []string) ([]string, error) {
	result := make([]string, len(values))
	total := 0
	for index, value := range values {
		if strings.IndexByte(value, 0) >= 0 || !utf8.ValidString(value) {
			return nil, fmt.Errorf("%w: invalid string at %d", ErrRunner, index)
		}
		total += len(value)
		if total > MaxArgumentBytes {
			return nil, fmt.Errorf("%w: argument/environment bytes exceed %d", ErrRunner, MaxArgumentBytes)
		}
		result[index] = value
	}
	return result, nil
}

func processStateExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
}

var _ stagedag.Executor = (*Runner)(nil)
