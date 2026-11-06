//go:build darwin || linux

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestRunnerRejectsAndCleansSuccessfulWorkerDescendants(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(t.TempDir(), "descendant-heartbeat")
	runner, err := NewRunner(Config{
		Executable: executable,
		Arguments:  []string{"-test.run=^TestWorkerHelperProcess$"},
		Environment: []string{
			"CUTLY_WORKER_HELPER=orphan-success",
			"CUTLY_WORKER_DESCENDANT_HEARTBEAT=" + heartbeat,
		},
		WorkingDirectory: t.TempDir(),
		Timeout:          10 * time.Second,
		TerminationGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := runner.Run(context.Background(), testRequest(t))
	var process *ProcessError
	if !errors.As(err, &process) || process.Class != renderapi.FailureTransient ||
		!errors.Is(err, ErrDescendants) || !report.DescendantIsolation {
		t.Fatalf("descendant result err=%v report=%+v", err, report)
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("descendant still wrote after cleanup: before=%q after=%q", before, after)
	}
}
