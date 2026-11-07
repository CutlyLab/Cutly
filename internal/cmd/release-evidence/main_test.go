package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/internal/releaseevidence"
)

type fakeExecutor struct {
	calls int
	fail  string
}

func (e *fakeExecutor) Execute(ctx context.Context, c releaseevidence.Command, w, stderr io.Writer) releaseevidence.Result {
	e.calls++
	if e.fail == "cancel" {
		return releaseevidence.Result{ExitCode: -1, Err: context.Canceled}
	}
	if e.fail == "nonzero" {
		return releaseevidence.Result{ExitCode: 1, Err: errors.New("failed")}
	}
	emit := func(action, test string) {
		b, _ := json.Marshal(map[string]string{"Action": action, "Package": c.Package, "Test": test})
		_, _ = w.Write(append(b, '\n'))
	}
	emit("start", "")
	for _, n := range c.Expected {
		emit("run", n)
		emit("pass", n)
	}
	emit("pass", "")
	return releaseevidence.Result{}
}
func cliRunner(e *fakeExecutor) releaseevidence.Runner {
	r := releaseevidence.NewRunner()
	r.Executor = e
	r.Observe = func(context.Context, releaseevidence.Config, releaseevidence.Executor) (releaseevidence.Source, error) {
		return releaseevidence.Source{Tools: []releaseevidence.Tool{{Name: "go", Path: "/observed/go"}}}, nil
	}
	return r
}
func TestCLIExplicitOptInAndBounds(t *testing.T) {
	for _, args := range [][]string{nil, {"-output", "x"}, {"-profile", "smoke"}, {"-profile", "other", "-output", "x"}, {"-profile", "smoke", "-output", "x", "-command", "anything"}, {"-profile", "smoke", "-output", "x", "positional"}, {"-profile", "smoke", "-output", "x", "-budget", "0"}, {"-profile", "long", "-output", "x", "-budget", "1h"}, {"-profile", "smoke", "-output", "x", "-command-timeout", "11m"}, {"-profile", "smoke", "-output", "x", "-log-bytes", "9999999"}, {strings.Repeat("x", 4097)}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := &fakeExecutor{}
			var stderr bytes.Buffer
			if run(context.Background(), args, &stderr, cliRunner(e)) == 0 || e.calls != 0 {
				t.Fatalf("ran invalid invocation %v", args)
			}
		})
	}
}
func TestCLIReportAndExit(t *testing.T) {
	for _, mode := range []string{"", "nonzero", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			e := &fakeExecutor{fail: mode}
			path := filepath.Join(t.TempDir(), "report.json")
			var stderr bytes.Buffer
			code := run(context.Background(), []string{"--profile", "smoke", "--output", path}, &stderr, cliRunner(e))
			if (code == 0) != (mode == "") {
				t.Fatalf("exit%d: %s", code, &stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var report releaseevidence.Report
			if json.Unmarshal(data, &report) != nil {
				t.Fatal(string(data))
			}
			want := "passed"
			if mode == "nonzero" {
				want = "failed"
			}
			if mode == "cancel" {
				want = "canceled"
			}
			if report.Status != want {
				t.Fatalf("%+v", report)
			}
			calls := e.calls
			if run(context.Background(), []string{"-profile", "smoke", "-output", path}, &stderr, cliRunner(e)) == 0 || e.calls != calls {
				t.Fatal("no-replace violated")
			}
		})
	}
}
func TestCLICanceledBeforeExecutionPersists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &fakeExecutor{}
	path := filepath.Join(t.TempDir(), "interrupt.json")
	var stderr bytes.Buffer
	if run(ctx, []string{"-profile", "smoke", "-output", path}, &stderr, cliRunner(e)) == 0 {
		t.Fatal("cancellation succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"status": "canceled"`) {
		t.Fatalf("%s %v", data, err)
	}
}
func TestCLIRunnerDefaultsAreBounded(t *testing.T) {
	for _, profile := range []string{"smoke", "long"} {
		c := releaseevidence.Defaults(profile)
		if c.CommandTimeout > 10*time.Minute || c.Budget > 105*time.Minute {
			t.Fatal(c)
		}
	}
}
