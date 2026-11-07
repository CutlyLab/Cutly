//go:build darwin || linux

package releaseevidence

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_EVIDENCE_PROCESS_HELPER")
	if mode == "" {
		return
	}
	if mode == "parent" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
		cmd.Env = append(os.Environ(), "RELEASE_EVIDENCE_PROCESS_HELPER=grandchild")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = cmd.Wait()
		os.Exit(4)
	}
	// An inherited stdout pipe remains open unless the descendant is killed too.
	_, _ = io.WriteString(os.Stdout, "grandchild-ready\n")
	for {
		time.Sleep(time.Second)
	}
}

type readyWriter struct {
	cancel  context.CancelFunc
	pending string
}

func (w *readyWriter) Write(b []byte) (int, error) {
	w.pending += string(b)
	if strings.Contains(w.pending, "grandchild-ready") {
		w.cancel()
	}
	return len(b), nil
}
func TestOSExecutorCancelsDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	result := (OSExecutor{}).Execute(ctx, Command{Path: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$"}, Env: map[string]string{"RELEASE_EVIDENCE_PROCESS_HELPER": "parent"}}, &readyWriter{cancel: cancel}, io.Discard)
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("%+v", result)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("process tree did not stop promptly (%s); inherited descendant stdout likely remained open", elapsed)
	}
}
func TestOSExecutorDeadlineAndMissingExecutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := (OSExecutor{}).Execute(ctx, Command{Path: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$"}, Env: map[string]string{"RELEASE_EVIDENCE_PROCESS_HELPER": "grandchild"}}, io.Discard, io.Discard)
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("%+v", result)
	}
	result = (OSExecutor{}).Execute(context.Background(), Command{Path: "/nonexistent/release-evidence-tool"}, io.Discard, io.Discard)
	if result.Err == nil || result.ExitCode != -1 {
		t.Fatalf("%+v", result)
	}
}
