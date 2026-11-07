package releaseevidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObserveSnapshotBeforeTests(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"go", "ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("observed-tool-"+name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	calls := []string{}
	executor := executorFunc(func(_ context.Context, c Command, w, e io.Writer) Result {
		calls = append(calls, strings.Join(c.Args, " "))
		switch c.Args[0] {
		case "rev-parse":
			fmt.Fprintln(w, strings.Repeat("b", 40))
		case "status":
			fmt.Fprintln(w, "?? internal/releaseevidence/")
		default:
			fmt.Fprintln(w, "observed version 1.2.3")
		}
		return Result{}
	})
	s, err := Observe(context.Background(), Defaults("smoke"), executor)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 || !strings.HasPrefix(calls[0], "rev-parse") || !strings.HasPrefix(calls[1], "status") || !s.Dirty || s.SnapshotAt.IsZero() || s.Platform == "" || s.RunnerToolchain == "" {
		t.Fatalf("%+v calls%v", s, calls)
	}
	for _, tool := range s.Tools {
		if len(tool.SHA256) != 64 || !strings.Contains(tool.Version, "1.2.3") || !filepath.IsAbs(tool.Path) {
			t.Fatalf("%+v", tool)
		}
	}
}
func TestObservationFailures(t *testing.T) {
	t.Run("missing-tools", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		e := executorFunc(func(_ context.Context, c Command, w, err io.Writer) Result {
			if c.Args[0] == "rev-parse" {
				fmt.Fprintln(w, strings.Repeat("a", 40))
			}
			return Result{}
		})
		if _, err := Observe(context.Background(), Defaults("smoke"), e); err == nil {
			t.Fatal("missing dependency accepted")
		}
	})
	t.Run("git-failure", func(t *testing.T) {
		e := executorFunc(func(context.Context, Command, io.Writer, io.Writer) Result {
			return Result{1, errors.New("git failure")}
		})
		if _, err := Observe(context.Background(), Defaults("smoke"), e); err == nil {
			t.Fatal("git failure accepted")
		}
	})
	t.Run("oversize-output", func(t *testing.T) {
		e := executorFunc(func(_ context.Context, c Command, w, _ io.Writer) Result {
			_, _ = w.Write([]byte(strings.Repeat("x", 65537)))
			return Result{}
		})
		if _, err := Observe(context.Background(), Defaults("smoke"), e); err == nil {
			t.Fatal("oversize observation accepted")
		}
	})
	t.Run("hash-canceled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tool")
		_ = os.WriteFile(path, []byte("x"), 0700)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := hashFile(ctx, path); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("nonregular", func(t *testing.T) {
		if _, err := hashFile(context.Background(), t.TempDir()); err == nil {
			t.Fatal("directory accepted")
		}
	})
}
func TestObservationContextClassification(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			r := fakeRunner(t)
			r.Observe = Observe
			r.Executor = executorFunc(func(context.Context, Command, io.Writer, io.Writer) Result { return Result{-1, want} })
			report, err := r.Run(context.Background(), Defaults("smoke"))
			status := "canceled"
			if errors.Is(want, context.DeadlineExceeded) {
				status = "timeout"
			}
			if !errors.Is(err, want) || report.Status != status {
				t.Fatalf("error%v status%s", err, report.Status)
			}
		})
	}
	t.Run("actual-observation-cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := fakeRunner(t)
		r.Observe = Observe
		r.Executor = executorFunc(func(ctx context.Context, _ Command, _, _ io.Writer) Result {
			cancel()
			<-ctx.Done()
			return Result{-1, ctx.Err()}
		})
		report, err := r.Run(ctx, Defaults("smoke"))
		if !errors.Is(err, context.Canceled) || report.Status != "canceled" {
			t.Fatalf("%v %+v", err, report)
		}
	})
}
func TestReportSizeAndWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-big.json")
	if WriteExclusive(path, Report{Error: strings.Repeat("x", maxReportBytes)}) == nil {
		t.Fatal("oversize report accepted")
	}
	if WriteExclusive(filepath.Join(t.TempDir(), "missing", "report.json"), Report{}) == nil {
		t.Fatal("missing directory unexpectedly writable")
	}
}
func TestOverallBudgetBoundsObservation(t *testing.T) {
	r := fakeRunner(t)
	r.Observe = func(ctx context.Context, _ Config, _ Executor) (Source, error) {
		select {
		case <-ctx.Done():
			return Source{}, ctx.Err()
		case <-time.After(2 * time.Second):
			return Source{}, errors.New("budget not applied")
		}
	}
	c := Defaults("smoke")
	c.Budget = time.Second
	report, err := r.Run(context.Background(), c)
	if !errors.Is(err, context.DeadlineExceeded) || report.Status != "timeout" {
		t.Fatalf("%v %+v", err, report)
	}
}
