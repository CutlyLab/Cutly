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
	"time"

	"github.com/CutlyLab/Cutly/internal/buildidentity"
	api "github.com/CutlyLab/Cutly/render/api"
)

// limitWriterV2 terminates an overflowing child immediately, including children
// which keep running after their stdout pipe closes.
type limitWriterV2 struct {
	writer *boundedWriter
	cancel context.CancelFunc
}

func (w *limitWriterV2) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		w.cancel()
	}
	return n, err
}

const MaxTerminationGraceV2 = 250 * time.Millisecond

type ConfigV2 struct {
	Process Config
	// LaunchConfig is a host-selected private regular descriptor inherited as fd 3.
	// It is never selected by a document locator or stdin request. The caller keeps
	// it open and immutable for the runner lifetime. Nil is for host child harnesses.
	LaunchConfig *os.File
	// SpoolDirectory is a host-selected private directory for descriptor-backed
	// chunk output. MaxPayloadBytes is an independent per-invocation disk bound.
	SpoolDirectory  string
	MaxPayloadBytes int64
}
type RunnerV2 struct {
	process        *Runner
	launch         *os.File
	spoolDirectory string
	maxPayload     int64
}

func measureAuthorizedV2(ctx context.Context, path string) (string, error) {
	p, err := buildidentity.ResolveProviderPolicy()
	if err != nil {
		return "", err
	}
	var identity buildidentity.ProviderExecutableIdentity
	if p.Distribution() == "development" {
		identity, err = p.MeasureLocalDevelopmentExecutable(ctx, path)
	} else {
		identity, err = p.MeasurePackagedExecutable(ctx, path)
	}
	if err != nil {
		return "", err
	}
	if !identity.Valid() {
		return "", ErrRunner
	}
	return identity.Digest(), nil
}

func measureBindingV2(ctx context.Context, path string) (buildidentity.ProviderExecutableIdentity, error) {
	identity, err := buildidentity.MeasureProviderExecutableBinding(ctx, path)
	if err != nil || !identity.Valid() {
		return buildidentity.ProviderExecutableIdentity{}, errors.Join(ErrRunner, err)
	}
	return identity, nil
}
func NewRunnerV2(config ConfigV2) (*RunnerV2, error) {
	if config.Process.TerminationGrace == 0 {
		config.Process.TerminationGrace = MaxTerminationGraceV2
	}
	if config.Process.TerminationGrace <= 0 || config.Process.TerminationGrace > MaxTerminationGraceV2 {
		return nil, fmt.Errorf("%w: v2 grace exceeds 250ms", ErrRunner)
	}
	if config.LaunchConfig != nil {
		if err := checkLaunchDescriptorV2(config.LaunchConfig); err != nil {
			return nil, err
		}
	}
	p, err := NewRunner(config.Process)
	if err != nil {
		return nil, err
	}
	digest, err := measureAuthorizedV2(context.Background(), p.executable)
	if err != nil {
		return nil, err
	}
	if digest != p.executableHash {
		return nil, ErrRunner
	}
	maximum := config.MaxPayloadBytes
	spool := config.SpoolDirectory
	if config.LaunchConfig == nil {
		if maximum == 0 {
			maximum = MaxStreamPayloadBytesV2
		}
		if spool == "" {
			spool = p.workingDirectory
		}
	}
	if maximum <= 0 || maximum > MaxStreamPayloadBytesV2 {
		return nil, fmt.Errorf("%w: invalid V2 payload spool bound", ErrRunner)
	}
	spool, err = verifyWorkingDirectory(spool)
	if err != nil {
		return nil, err
	}
	return &RunnerV2{process: p, launch: config.LaunchConfig, spoolDirectory: spool, maxPayload: maximum}, nil
}
func (r *RunnerV2) ExecutableDigest() string {
	if r == nil || r.process == nil {
		return ""
	}
	return r.process.executableHash
}

type StreamResultV2 struct {
	SchemaVersion int
	Profile       string
	InvocationID  string
	Attempt       uint32
	OK            bool
	Receipt       *ReceiptV2
	Payload       io.ReadSeekCloser
	FailureClass  api.FailureClass
	Message       string
}

func (r *StreamResultV2) Close() error {
	if r == nil || r.Payload == nil {
		return nil
	}
	err := r.Payload.Close()
	r.Payload = nil
	return err
}

// Run retains the bounded inline compatibility surface. Production execution
// with a launch descriptor uses RunStream internally and only materializes
// payloads that fit MaxPayloadBytesV2.
func (r *RunnerV2) Run(ctx context.Context, request RequestV2) (out ResultV2, report Report, err error) {
	if r == nil || r.launch == nil {
		return r.runInline(ctx, request)
	}
	stream, report, err := r.RunStream(ctx, request)
	if err != nil {
		return ResultV2{}, report, err
	}
	if stream == nil {
		return ResultV2{}, report, ErrRunner
	}
	if !stream.OK {
		closeErr := stream.Close()
		if closeErr != nil {
			return ResultV2{}, report, closeErr
		}
		return ResultV2{SchemaVersion: stream.SchemaVersion, Profile: stream.Profile, InvocationID: stream.InvocationID, Attempt: stream.Attempt, FailureClass: stream.FailureClass, Message: stream.Message}, report, nil
	}
	if stream.Receipt == nil || stream.Receipt.SizeBytes > MaxPayloadBytesV2 {
		_ = stream.Close()
		return ResultV2{}, report, &ProcessError{Class: api.FailureIntegrity, Report: report, Cause: ErrOutputLimit}
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream.Payload, MaxPayloadBytesV2+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || int64(len(payload)) != stream.Receipt.SizeBytes {
		return ResultV2{}, report, &ProcessError{Class: api.FailureIntegrity, Report: report, Cause: errors.Join(ErrProtocol, readErr, closeErr)}
	}
	receipt := *stream.Receipt
	out = ResultV2{SchemaVersion: stream.SchemaVersion, Profile: stream.Profile, InvocationID: stream.InvocationID, Attempt: stream.Attempt, OK: true, Receipt: &receipt, Payload: payload}
	if err = ValidateResultV2(out); err != nil {
		return ResultV2{}, report, &ProcessError{Class: api.FailureIntegrity, Report: report, Cause: err}
	}
	return out, report, nil
}

func verifyPayloadFileV2(ctx context.Context, file *os.File, expected int64) (string, error) {
	if ctx == nil || file == nil || expected <= 0 {
		return "", ErrProtocol
	}
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != expected {
		return "", errors.Join(ErrProtocol, err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	buffer := make([]byte, 1<<20)
	var read int64
	for read < expected {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		want := int64(len(buffer))
		if expected-read < want {
			want = expected - read
		}
		n, readErr := io.ReadFull(file, buffer[:want])
		if n > 0 {
			_, _ = h.Write(buffer[:n])
			read += int64(n)
		}
		if readErr != nil {
			return "", readErr
		}
	}
	var extra [1]byte
	if n, readErr := file.Read(extra[:]); n != 0 || readErr != io.EOF {
		return "", errors.Join(ErrProtocol, readErr)
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.Join(ErrProtocol, err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// RunStream launches the production child with fd3 host policy, fd4 parent
// liveness and fd5 anonymous payload spool. Stdout is bounded metadata only.
func (r *RunnerV2) RunStream(ctx context.Context, request RequestV2) (out *StreamResultV2, report Report, err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
		if err != nil && out != nil {
			err = errors.Join(err, out.Close())
			out = nil
		}
	}()
	if r == nil || r.process == nil || r.launch == nil || ctx == nil {
		return nil, report, ErrRunner
	}
	if err = ctx.Err(); err != nil {
		return nil, report, err
	}
	p := r.process
	if request.ProviderExecutableDigest != p.executableHash || request.Environment.Manifest.ProviderBuild != p.executableHash {
		return nil, report, fmt.Errorf("%w: foreign v2 provider", ErrProtocol)
	}
	measured, err := measureAuthorizedV2(ctx, p.executable)
	if err != nil || measured != p.executableHash {
		return nil, report, errors.Join(ErrRunner, err)
	}
	if err = checkLaunchDescriptorV2(r.launch); err != nil {
		return nil, report, err
	}
	expected, err := chunkSizeV2(request)
	if err != nil || expected > r.maxPayload {
		return nil, report, errors.Join(ErrOutputLimit, err)
	}
	raw, err := CanonicalRequestV2(ctx, request)
	if err != nil {
		return nil, report, err
	}
	payload, err := os.CreateTemp(r.spoolDirectory, ".cutly-worker-v2-")
	if err != nil {
		return nil, report, err
	}
	payloadOwned := true
	defer func() {
		if payloadOwned {
			err = errors.Join(err, payload.Close())
		}
	}()
	if err = payload.Chmod(0o600); err != nil {
		return nil, report, err
	}
	name := payload.Name()
	if err = os.Remove(name); err != nil {
		return nil, report, err
	}

	runCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	stdout := &boundedWriter{maximum: MaxStreamResultBytesV2, fail: true}
	stderr := &boundedWriter{maximum: p.maxStderrBytes}
	command := exec.Command(p.executable, p.arguments...)
	command.Dir = p.workingDirectory
	command.Env = append([]string{}, p.environment...)
	command.Stdin = bytes.NewReader(raw)
	command.Stdout = &limitWriterV2{writer: stdout, cancel: cancel}
	command.Stderr = stderr
	command.WaitDelay = p.terminationGrace
	aliveRead, aliveWrite, err := os.Pipe()
	if err != nil {
		return nil, report, err
	}
	defer aliveRead.Close()
	defer aliveWrite.Close()
	command.ExtraFiles = []*os.File{r.launch, aliveRead, payload}
	configureProcess(command)
	report = Report{Profile: RunnerProfileV2, ExecutableHash: measured, ExitCode: -1, DescendantIsolation: p.descendantIsolation}
	fail := func(class api.FailureClass, cause error) (*StreamResultV2, Report, error) {
		return nil, report, &ProcessError{Class: class, Report: report, Cause: cause}
	}
	if err = command.Start(); err != nil {
		return fail(api.FailureResource, err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-runCtx.Done():
		terminateErr := terminateProcess(command.Process)
		timer := time.NewTimer(p.terminationGrace)
		select {
		case waitErr = <-wait:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			terminateErr = errors.Join(terminateErr, killProcess(command.Process))
			waitErr = <-wait
		}
		waitErr = errors.Join(runCtx.Err(), terminateErr, waitErr)
	}
	waitErr = errors.Join(waitErr, cleanupDescendants(command.Process, p.terminationGrace))
	report.Canceled = ctx.Err() != nil
	report.TimedOut = ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded)
	report.ExitCode = processStateExitCode(command.ProcessState)
	report.Signal = exitSignal(waitErr)
	report.Stderr, report.StderrTruncated = stderr.text()
	if stdout.exceededValue() && ctx.Err() == nil {
		return fail(api.FailureIntegrity, ErrOutputLimit)
	}
	if runCtx.Err() != nil {
		return fail(api.FailureCancellation, errors.Join(runCtx.Err(), waitErr))
	}
	if waitErr != nil {
		return fail(api.FailureTransient, waitErr)
	}
	wire, err := decodeStreamResultV2(stdout.bytes())
	if err != nil || wire.InvocationID != request.InvocationID || wire.Attempt != request.Attempt {
		return fail(api.FailureIntegrity, errors.Join(ErrProtocol, err))
	}
	out = &StreamResultV2{SchemaVersion: wire.SchemaVersion, Profile: wire.Profile, InvocationID: wire.InvocationID, Attempt: wire.Attempt, OK: wire.OK, Receipt: wire.Receipt, FailureClass: wire.FailureClass, Message: wire.Message}
	if !wire.OK {
		return out, report, nil
	}
	contentHash, err := verifyPayloadFileV2(ctx, payload, expected)
	if err != nil {
		return fail(api.FailureIntegrity, err)
	}
	want := receiptMetadataV2(request, contentHash, expected)
	if wire.Receipt == nil || !EqualReceiptV2(*wire.Receipt, want) {
		return fail(api.FailureIntegrity, ErrProtocol)
	}
	out.Payload = payload
	payloadOwned = false
	return out, report, nil
}

func (r *RunnerV2) runInline(ctx context.Context, request RequestV2) (out ResultV2, report Report, err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
		if err != nil {
			out = ResultV2{}
		}
	}()
	if r == nil || r.process == nil || ctx == nil {
		return out, report, ErrRunner
	}
	if err = ctx.Err(); err != nil {
		return out, report, err
	}
	p := r.process
	// No document traversal, chunk allocation, or launch precedes this equality.
	if request.ProviderExecutableDigest != p.executableHash || request.Environment.Manifest.ProviderBuild != p.executableHash {
		return out, report, fmt.Errorf("%w: foreign v2 provider", ErrProtocol)
	}
	measured, e := measureAuthorizedV2(ctx, p.executable)
	if e != nil {
		return out, report, e
	}
	if measured != p.executableHash {
		return out, report, ErrRunner
	}
	if r.launch != nil {
		if err = checkLaunchDescriptorV2(r.launch); err != nil {
			return out, report, err
		}
	}
	raw, e := CanonicalRequestV2(ctx, request)
	if e != nil {
		return out, report, e
	}
	runCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	stdout := &boundedWriter{maximum: MaxResultBytesV2, fail: true}
	stderr := &boundedWriter{maximum: p.maxStderrBytes}
	command := exec.Command(p.executable, p.arguments...)
	command.Dir = p.workingDirectory
	command.Env = append([]string{}, p.environment...)
	command.Stdin = bytes.NewReader(raw)
	command.Stdout = &limitWriterV2{writer: stdout, cancel: cancel}
	command.Stderr = stderr
	command.WaitDelay = p.terminationGrace
	if r.launch != nil {
		// fd4 remains live only while this parent owns the invocation. SIGKILL
		// closes the writer too, independently of request stdin consumption.
		aliveRead, aliveWrite, e := os.Pipe()
		if e != nil {
			return out, report, e
		}
		defer aliveRead.Close()
		defer aliveWrite.Close()
		command.ExtraFiles = []*os.File{r.launch, aliveRead}
	}
	configureProcess(command)
	report = Report{Profile: RunnerProfileV2, ExecutableHash: measured, ExitCode: -1, DescendantIsolation: p.descendantIsolation}
	fail := func(class api.FailureClass, cause error) (ResultV2, Report, error) {
		return ResultV2{}, report, &ProcessError{Class: class, Report: report, Cause: cause}
	}
	if e = command.Start(); e != nil {
		return fail(api.FailureResource, e)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-runCtx.Done():
		terminateErr := terminateProcess(command.Process)
		timer := time.NewTimer(p.terminationGrace)
		select {
		case waitErr = <-wait:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			terminateErr = errors.Join(terminateErr, killProcess(command.Process))
			waitErr = <-wait
		}
		waitErr = errors.Join(runCtx.Err(), terminateErr, waitErr)
	}
	waitErr = errors.Join(waitErr, cleanupDescendants(command.Process, p.terminationGrace))
	report.Canceled = ctx.Err() != nil
	report.TimedOut = ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded)
	report.ExitCode = processStateExitCode(command.ProcessState)
	report.Signal = exitSignal(waitErr)
	report.Stderr, report.StderrTruncated = stderr.text()
	if stdout.exceededValue() && ctx.Err() == nil {
		return fail(api.FailureIntegrity, ErrOutputLimit)
	}
	if runCtx.Err() != nil {
		return fail(api.FailureCancellation, errors.Join(runCtx.Err(), waitErr))
	}
	if waitErr != nil {
		return fail(api.FailureTransient, waitErr)
	}
	out, e = DecodeResultV2(stdout.bytes())
	if e != nil {
		return fail(api.FailureIntegrity, e)
	}
	if out.InvocationID != request.InvocationID || out.Attempt != request.Attempt {
		return fail(api.FailureIntegrity, ErrProtocol)
	}
	if out.OK {
		size, e := chunkSizeV2(request)
		want := receiptV2(request, out.Payload)
		if e != nil || size != int64(len(out.Payload)) || out.Receipt == nil || *out.Receipt != want {
			return fail(api.FailureIntegrity, ErrProtocol)
		}
	}
	return out, report, nil
}
