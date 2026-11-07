package releaseevidence

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Result struct {
	ExitCode int
	Err      error
}
type Executor interface {
	Execute(context.Context, Command, io.Writer, io.Writer) Result
}

// OSExecutor never invokes a shell and uses a separate, killable process group.
type OSExecutor struct{}

func (OSExecutor) Execute(ctx context.Context, c Command, stdout, stderr io.Writer) Result {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = executionEnvironment(c.Env)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	if err := configureProcessTree(cmd); err != nil {
		return Result{-1, err}
	}
	err := cmd.Start()
	if err != nil {
		return Result{-1, err}
	}
	err = cmd.Wait()
	cleanupProcessTree(cmd)
	code := cmd.ProcessState.ExitCode()
	if ctx.Err() != nil {
		return Result{code, ctx.Err()}
	}
	return Result{code, err}
}
func executionEnvironment(overrides map[string]string) []string {
	// Do not inherit Go flags/overlays/toolchain downloads or RC helper opt-ins.
	// ffmpeg and its children still use the same observed PATH resolution.
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CUTLY_") || strings.HasPrefix(key, "GO") || key == "GODEBUG" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "GOENV=off")
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

type capture struct {
	data      []byte
	limit     int
	total     int
	truncated bool
	max       int
	cancel    context.CancelFunc
}

func (w *capture) Write(b []byte) (int, error) {
	w.total += len(b)
	if w.total > w.max {
		if w.cancel != nil {
			w.cancel()
		}
		return 0, fmt.Errorf("command stream exceeds %d bytes", w.max)
	}
	n := w.limit - len(w.data)
	if n > len(b) {
		n = len(b)
	}
	if n > 0 {
		w.data = append(w.data, b[:n]...)
	}
	if n < len(b) {
		w.truncated = true
	}
	return len(b), nil
}

type checkedWriter struct {
	parser *eventParser
	log    *capture
	cancel context.CancelFunc
}

func (w checkedWriter) Write(b []byte) (int, error) {
	if _, err := w.log.Write(b); err != nil {
		w.cancel()
		return 0, err
	}
	n, err := w.parser.Write(b)
	if err != nil {
		w.cancel()
	}
	return n, err
}
