//go:build darwin || linux

package releaseevidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
func TestObserveExecutableFIFOHelper(t *testing.T) {
	if os.Getenv("RELEASE_EVIDENCE_FIFO_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := NewRunner()
	r.Executor = executorFunc(func(_ context.Context, c Command, w, _ io.Writer) Result {
		if c.Args[0] == "rev-parse" {
			fmt.Fprintln(w, strings.Repeat("a", 40))
		}
		return Result{}
	})
	config := Defaults("smoke")
	config.Budget = time.Second
	report, err := r.Run(ctx, config)
	if err == nil || report.Status != "failed" || !strings.Contains(report.Error, "not a regular file") {
		t.Fatalf("FIFO observation: %v %+v", err, report)
	}
}
func TestObserveExecutableFIFOIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go")
	if err := syscall.Mkfifo(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	// Run the regression in a killable child. A reintroduced blocking open fails
	// within 3s without leaving a blocked goroutine in this test process.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestObserveExecutableFIFOHelper$")
	cmd.Env = append(executionEnvironment(nil), "RELEASE_EVIDENCE_FIFO_HELPER=1", "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FIFO observation was not bounded/rejected: %v %s", err, output)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := hashFile(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("open must honor preexisting cancellation: %v", err)
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
