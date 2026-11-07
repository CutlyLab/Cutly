package releaseevidence

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Runner's hooks exist to test hour-long profiles without executing media or
// waiting an hour. The CLI always uses OSExecutor, Observe and the real clock.
type Runner struct {
	Executor Executor
	Observe  func(context.Context, Config, Executor) (Source, error)
	Now      func() time.Time
	Wait     func(context.Context, time.Duration) error
}

func NewRunner() Runner {
	return Runner{Executor: OSExecutor{}, Observe: Observe, Now: time.Now, Wait: func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}}
}
func (r Runner) Run(ctx context.Context, c Config) (report Report, err error) {
	c, err = absoluteConfig(c)
	if err != nil {
		return report, err
	}
	if r.Executor == nil || r.Observe == nil || r.Now == nil || r.Wait == nil {
		return report, fmt.Errorf("incomplete runner dependencies")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Budget)
	defer cancel()
	report = Report{Schema: "cutly.rc-release-evidence.v1", Profile: c.Profile, CommandTimeout: c.CommandTimeout, Budget: c.Budget, LogBytesPerStream: c.LogBytes, Scope: "restricted existing RC tests; not broad v2 acceptance", Status: "failed", Started: r.Now().UTC(), Limitations: []string{
		"Observed local tool paths, versions and SHA256 only; not an approved or signed runtime bundle; tools may change after observation.",
		"RC repeated-job recovery observation starts fresh go-test jobs; not a single-daemon continuous soak or leak/liveness proof.",
		"Synthetic streaming/RSS measures the evidence reader subprocess, not real-media throughput or daemon resident memory.",
		"Injected publication ENOSPC only; no mounts or real small-filesystem exhaustion; no S2 or S3-S6 acceptance.",
		"Source status is a pre-test snapshot, not a content attestation; output reserved inside repo may itself mark source dirty.",
	}}
	p := &report.Progress
	p.FaultRoundsRequested = 1
	p.RealMediaRunsRequested = 1
	if c.Profile == "long" {
		p.FaultRoundsRequested = 100
		p.RealMediaRunsRequested = 10
		p.RecoveryRoundsRequested = 61
		p.ObservationRequested = time.Hour
		p.IntervalRequested = time.Minute
	}
	defer func() {
		report.Finished = r.Now().UTC()
		if err != nil {
			report.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				report.Status = "canceled"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				report.Status = "timeout"
			}
		} else {
			report.Status = "passed"
		}
	}()
	report.Source, err = r.Observe(ctx, c, r.Executor)
	if err != nil {
		return report, err
	}
	// Bind invocations to the observed go path. Test subprocess ffmpeg resolution
	// remains PATH based and is explicitly only an observation, not a pinned bundle.
	goPath := ""
	for _, tool := range report.Source.Tools {
		if tool.Name == "go" {
			goPath = tool.Path
		}
	}
	if goPath == "" {
		return report, fmt.Errorf("missing observed go executable")
	}
	remainingLogs := maxTotalLogBytes
	invoke := func(cmd Command) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd.Path = goPath
		result, runErr := r.invoke(ctx, c, cmd, &remainingLogs)
		report.Commands = append(report.Commands, result)
		return runErr
	}
	for i := 0; i < p.FaultRoundsRequested; i++ {
		if err = invoke(testCommand(c, backend, "injected RC faults; no real small filesystem", faults...)); err != nil {
			return report, err
		}
		p.FaultRoundsCompleted++
	}
	if err = invoke(testCommand(c, backend, "RC recovery", recovery...)); err != nil {
		return report, err
	}
	if err = invoke(testCommand(c, daemon, "real media RC recovery", daemonRecovery)); err != nil {
		return report, err
	}
	if c.Profile == "smoke" {
		if err = invoke(testCommand(c, daemon, "real media public SDK RC", "TestPublicGoSDKHTTPRenderEndToEnd")); err != nil {
			return report, err
		}
		p.RealMediaRunsCompleted = 1
		return report, nil
	}
	if err = invoke(benchmarkCommand(c)); err != nil {
		return report, err
	}
	if err = invoke(testCommand(c, backend, "synthetic streaming reader subprocess RSS", rss)); err != nil {
		return report, err
	}
	if err = invoke(testCommand(c, backend, "existing ten-run real-media decoded repeatability", repeatability)); err != nil {
		return report, err
	}
	p.RealMediaRunsCompleted = 10
	start := r.Now()
	for i := 0; i < p.RecoveryRoundsRequested; i++ {
		if i > 0 {
			before := r.Now()
			if err = r.Wait(ctx, p.IntervalRequested); err != nil {
				return report, err
			}
			if r.Now().Sub(before) < p.IntervalRequested {
				return report, fmt.Errorf("incomplete recovery interval")
			}
			p.IntervalsCompleted++
			p.IntervalCompleted += r.Now().Sub(before)
		}
		if err = invoke(testCommand(c, backend, "repeated fresh RC recovery job", recovery...)); err != nil {
			return report, err
		}
		if err = invoke(testCommand(c, daemon, "repeated fresh real-media RC recovery job", daemonRecovery)); err != nil {
			return report, err
		}
		p.RecoveryRoundsCompleted++
		p.ObservationCompleted = r.Now().Sub(start)
	}
	if p.RecoveryRoundsCompleted != p.RecoveryRoundsRequested || p.IntervalsCompleted != 60 || p.ObservationCompleted < p.ObservationRequested {
		return report, fmt.Errorf("incomplete recovery observation duration or rounds")
	}
	return report, nil
}
func (r Runner) invoke(ctx context.Context, c Config, cmd Command, remaining *int) (Invocation, error) {
	started := r.Now()
	child, cancel := context.WithTimeout(ctx, c.CommandTimeout)
	defer cancel()
	inv := Invocation{Command: cmd, Status: "failed", ExitCode: -1}
	limit := min(c.LogBytes, *remaining/2)
	stdout := &capture{limit: limit, max: 8 << 20, cancel: cancel}
	stderr := &capture{limit: limit, max: 1 << 20, cancel: cancel}
	parser := newParser(cmd)
	result := r.Executor.Execute(child, cmd, checkedWriter{parser, stdout, cancel}, stderr)
	inv.Duration = r.Now().Sub(started)
	inv.ExitCode = result.ExitCode
	inv.Log = string(stdout.data)
	inv.Stderr = string(stderr.data)
	inv.LogTruncated = stdout.truncated || stderr.truncated
	*remaining -= len(stdout.data) + len(stderr.data)
	err := parser.finish()
	capErr := errors.Join(stdout.capError("stdout"), stderr.capError("stderr"))
	if ctx.Err() != nil {
		// A genuine caller cancellation/deadline takes precedence over cleanup.
		err = ctx.Err()
	} else if capErr != nil {
		err = capErr
	} else if parser.err != nil {
		// Malformed evidence also cancels the child internally; keep its cause.
		err = parser.err
	} else if errors.Is(child.Err(), context.DeadlineExceeded) {
		err = child.Err()
	} else if result.Err != nil && (err == nil || errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded)) {
		err = result.Err
	}
	if result.ExitCode != 0 && err == nil {
		err = fmt.Errorf("nonzero command exit %d", result.ExitCode)
	}
	if err != nil {
		inv.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			inv.Status = "canceled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			inv.Status = "timeout"
		}
		return inv, err
	}
	inv.Status = "passed"
	inv.Metrics = parser.metrics
	return inv, nil
}

// RunToFile reserves output before execution and persists unsuccessful evidence
// too. Context cancellation still permits best-effort report serialization.
func (r Runner) RunToFile(ctx context.Context, c Config, path string) (Report, error) {
	if _, err := absoluteConfig(c); err != nil {
		return Report{}, err
	}
	f, err := reserve(path)
	if err != nil {
		return Report{}, err
	}
	report, runErr := r.Run(ctx, c)
	writeErr := writeReport(f, report)
	return report, errors.Join(runErr, writeErr)
}
