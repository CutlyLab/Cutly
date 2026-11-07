package releaseevidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type executorFunc func(context.Context, Command, io.Writer, io.Writer) Result

func (f executorFunc) Execute(c context.Context, cmd Command, o, e io.Writer) Result {
	return f(c, cmd, o, e)
}
func event(w io.Writer, c Command, action, test, output string) {
	b, _ := json.Marshal(testEvent{action, c.Package, test, output})
	_, _ = w.Write(append(b, '\n'))
}
func passing(c Command, w io.Writer) {
	event(w, c, "start", "", "")
	for _, n := range c.Expected {
		event(w, c, "run", n, "")
		if n == rss {
			event(w, c, "output", n, "    m1_memory_acceptance_test.go:36: decoded evidence MaxRSS: 16MiB=12345 bytes, 256MiB=23456 bytes\n")
		}
		event(w, c, "pass", n, "")
	}
	if c.Benchmark {
		event(w, c, "run", benchmark, "")
		event(w, c, "output", benchmark, benchmark+"-8\t")
		event(w, c, "output", benchmark, "1\t 2000 ns/op\t 2148.70 MB/s\t 168208 B/op\t 63 allocs/op\n")
		event(w, c, "bench", benchmark, "")
	}
	event(w, c, "pass", "", "")
}
func fakeRunner(t *testing.T) Runner {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Runner{Executor: executorFunc(func(_ context.Context, c Command, w, e io.Writer) Result { passing(c, w); return Result{} }), Observe: func(context.Context, Config, Executor) (Source, error) {
		return Source{Revision: strings.Repeat("a", 40), Tools: []Tool{{Name: "go", Path: "/observed/go"}}}, nil
	}, Now: func() time.Time { return now }, Wait: func(ctx context.Context, d time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now = now.Add(d)
		return nil
	}}
}
func TestProfilesWithFakeExecutor(t *testing.T) {
	for _, profile := range []string{"smoke", "long"} {
		t.Run(profile, func(t *testing.T) {
			r := fakeRunner(t)
			report, err := r.Run(context.Background(), Defaults(profile))
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != "passed" {
				t.Fatalf("%+v", report)
			}
			if profile == "smoke" {
				if len(report.Commands) != 4 || report.Progress.FaultRoundsCompleted != 1 || report.Progress.RecoveryRoundsCompleted != 0 {
					t.Fatalf("smoke unexpectedly long: %+v", report.Progress)
				}
			} else {
				p := report.Progress
				if len(report.Commands) != 227 || p.FaultRoundsCompleted != 100 || p.RecoveryRoundsCompleted != 61 || p.ObservationCompleted < time.Hour || p.IntervalsCompleted != 60 || p.RealMediaRunsCompleted != 10 {
					t.Fatalf("incomplete long: commands%d %+v", len(report.Commands), p)
				}
				var metrics []Metric
				for _, inv := range report.Commands {
					metrics = append(metrics, inv.Metrics...)
				}
				if len(metrics) != 6 {
					t.Fatalf("metrics: %+v", metrics)
				}
			}
			for _, inv := range report.Commands {
				if inv.Command.Path != "/observed/go" {
					t.Fatal("not observed executable")
				}
				if strings.Contains(strings.Join(inv.Command.Args, " "), "RealSmallFilesystem") {
					t.Fatal("unsafe selector")
				}
			}
		})
	}
}
func TestRejectedExecutionEvidence(t *testing.T) {
	cases := map[string]executorFunc{
		"skip": func(_ context.Context, c Command, w, e io.Writer) Result {
			event(w, c, "start", "", "")
			event(w, c, "skip", c.Expected[0], "")
			return Result{}
		},
		"nested-skip": func(_ context.Context, c Command, w, e io.Writer) Result {
			event(w, c, "start", "", "")
			event(w, c, "skip", c.Expected[0]+"/nested", "")
			return Result{}
		},
		"missing": func(_ context.Context, c Command, w, e io.Writer) Result {
			event(w, c, "start", "", "")
			event(w, c, "pass", "", "")
			return Result{}
		},
		"fail": func(_ context.Context, c Command, w, e io.Writer) Result {
			event(w, c, "fail", c.Expected[0], "")
			return Result{}
		},
		"malformed": func(_ context.Context, c Command, w, e io.Writer) Result {
			fmt.Fprintln(w, "not json")
			return Result{}
		},
		"truncated": func(_ context.Context, c Command, w, e io.Writer) Result {
			fmt.Fprint(w, `{"Action":"start"`)
			return Result{}
		},
		"nonzero": func(_ context.Context, c Command, w, e io.Writer) Result { passing(c, w); return Result{ExitCode: 1} },
		"executor-error": func(_ context.Context, c Command, w, e io.Writer) Result {
			passing(c, w)
			return Result{Err: errors.New("wait failed")}
		},
		"cancel":  func(_ context.Context, c Command, w, e io.Writer) Result { return Result{-1, context.Canceled} },
		"timeout": func(_ context.Context, c Command, w, e io.Writer) Result { return Result{-1, context.DeadlineExceeded} },
		"stdout-cap": func(_ context.Context, c Command, w, e io.Writer) Result {
			_, err := io.Copy(w, strings.NewReader(strings.Repeat("x", (8<<20)+1)))
			return Result{-1, err}
		},
		"stderr-cap": func(_ context.Context, c Command, w, e io.Writer) Result {
			passing(c, w)
			_, err := io.Copy(e, strings.NewReader(strings.Repeat("x", (1<<20)+1)))
			return Result{-1, err}
		},
	}
	for name, ex := range cases {
		t.Run(name, func(t *testing.T) {
			r := fakeRunner(t)
			r.Executor = ex
			report, err := r.Run(context.Background(), Defaults("smoke"))
			if err == nil || report.Status == "passed" || report.Progress.FaultRoundsCompleted != 0 {
				t.Fatalf("accepted rejected evidence: %v %+v", err, report)
			}
		})
	}
}
func TestCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
			}
			r := fakeRunner(t)
			r.Executor = executorFunc(func(ctx context.Context, _ Command, _, _ io.Writer) Result {
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return Result{-1, ctx.Err()}
			})
			report, err := r.Run(ctx, Defaults("smoke"))
			want := "canceled"
			if deadline {
				want = "timeout"
			}
			if err == nil || report.Status != want {
				t.Fatalf("%v %+v", err, report)
			}
		})
	}
}
func TestFailurePersistedAndNoReplace(t *testing.T) {
	r := fakeRunner(t)
	calls := 0
	r.Observe = func(context.Context, Config, Executor) (Source, error) {
		calls++
		return Source{}, errors.New("missing ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "failure.json")
	report, err := r.RunToFile(context.Background(), Defaults("smoke"), path)
	if err == nil || report.Status != "failed" {
		t.Fatal("failure lost")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Report
	if json.Unmarshal(data, &saved) != nil || saved.Status != "failed" || !strings.Contains(saved.Error, "missing ffmpeg") {
		t.Fatalf("bad persisted report %s", data)
	}
	_, err = r.RunToFile(context.Background(), Defaults("smoke"), path)
	if err == nil || calls != 1 {
		t.Fatal("overwritten or executed despite existing output")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("overwritten")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err = os.Symlink(path, link); err == nil {
		if WriteExclusive(link, Report{}) == nil {
			t.Fatal("replaced symlink")
		}
	}
}
func TestIncompleteLongObservation(t *testing.T) {
	t.Run("short-wait", func(t *testing.T) {
		r := fakeRunner(t)
		r.Wait = func(context.Context, time.Duration) error { return nil }
		report, err := r.Run(context.Background(), Defaults("long"))
		if err == nil || report.Progress.RecoveryRoundsCompleted != 1 || report.Status == "passed" {
			t.Fatalf("%v %+v", err, report.Progress)
		}
	})
	t.Run("round-failure", func(t *testing.T) {
		r := fakeRunner(t)
		base := r.Executor
		calls := 0
		r.Executor = executorFunc(func(ctx context.Context, c Command, w, e io.Writer) Result {
			if strings.HasPrefix(c.Scope, "repeated fresh RC") {
				calls++
				if calls == 3 {
					return Result{1, errors.New("injected third observation")}
				}
			}
			return base.Execute(ctx, c, w, e)
		})
		report, err := r.Run(context.Background(), Defaults("long"))
		if err == nil || report.Progress.RecoveryRoundsCompleted != 2 || report.Status == "passed" {
			t.Fatalf("%v %+v", err, report.Progress)
		}
	})
}
func TestConfigurationBounds(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Profile = "" }, func(c *Config) { c.Profile = "anything" }, func(c *Config) { c.CommandTimeout = 0 }, func(c *Config) { c.CommandTimeout = 11 * time.Minute }, func(c *Config) { c.Budget = 106 * time.Minute }, func(c *Config) { c.Budget = 0 }, func(c *Config) { c.LogBytes = 1 }, func(c *Config) { c.LogBytes = 262145 }, func(c *Config) { c.Repo = "" }} {
		c := Defaults("smoke")
		mutate(&c)
		r := fakeRunner(t)
		if _, err := r.Run(context.Background(), c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := Defaults("long")
	c.Budget = time.Hour
	if c.validate() == nil {
		t.Fatal("short long budget accepted")
	}
}
func TestLogCaptureCapsAndReportSize(t *testing.T) {
	r := fakeRunner(t)
	base := r.Executor
	r.Executor = executorFunc(func(ctx context.Context, c Command, w, e io.Writer) Result {
		_, _ = io.WriteString(e, strings.Repeat("z", 300000))
		return base.Execute(ctx, c, w, e)
	})
	c := Defaults("long")
	c.LogBytes = 262144
	report, err := r.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	truncated := false
	for _, inv := range report.Commands {
		total += len(inv.Log) + len(inv.Stderr)
		truncated = truncated || inv.LogTruncated
	}
	if total > maxTotalLogBytes || !truncated {
		t.Fatalf("capture not bounded %d", total)
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	if len(data) > maxReportBytes {
		t.Fatalf("report too large %d", len(data))
	}
}
func TestEnvironmentRejectsInheritedOptIns(t *testing.T) {
	t.Setenv("CUTLY_REAL_SMALL_FS_TEST", "1")
	t.Setenv("CUTLY_M1_RSS_HELPER_MIB", "4096")
	t.Setenv("GOFLAGS", "-run=Anything")
	t.Setenv("GOWORK", "/untrusted")
	env := strings.Join(executionEnvironment(map[string]string{"CUTLY_M1_STREAM_BENCH_MIB": "4096"}), "\n")
	for _, bad := range []string{"CUTLY_REAL_SMALL_FS_TEST=1", "CUTLY_M1_RSS_HELPER_MIB=4096", "-run=Anything", "GOWORK=/untrusted"} {
		if strings.Contains(env, bad) {
			t.Fatalf("inherited %s", bad)
		}
	}
	if !strings.Contains(env, "GOWORK=off") || !strings.Contains(env, "CUTLY_M1_STREAM_BENCH_MIB=4096") {
		t.Fatal(env)
	}
}
