package localv2

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Separate processes prove the lock is not a goroutine-only gate; abrupt exit
// at publication points leaves the journal for another process to recover.
func TestSubprocessHelper(t *testing.T) {
	mode := os.Getenv("CUTLY_LOCALV2_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("CUTLY_LOCALV2_DIR")
	s, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "lock":
		err = s.locked(context.Background(), false, func(x *session) error {
			if _, err := os.Stdout.WriteString("locked\n"); err != nil {
				return err
			}
			_, err := bufio.NewReader(os.Stdin).ReadString('\n')
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	case "crash":
		point := os.Getenv("CUTLY_LOCALV2_POINT")
		s.fault = func(p string) error {
			if boundary(p) == point {
				os.Exit(73)
			}
			return nil
		}
		if _, err := s.Apply(context.Background(), "p"); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash point not reached")
	}
}

func child(t *testing.T, s *Service, mode, point string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessHelper$")
	cmd.Env = append(os.Environ(), "CUTLY_LOCALV2_HELPER="+mode, "CUTLY_LOCALV2_DIR="+s.dir, "CUTLY_LOCALV2_POINT="+point)
	return cmd
}

func TestSubprocessExclusiveLock(t *testing.T) {
	s := createTest(t)
	cmd := child(t, s, "lock", "")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(out).ReadString('\n')
		if err == nil && line != "locked\n" {
			err = errors.New(line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not acquire lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.Status(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock failed to exclude other process", err)
	}
	if _, err := in.Write([]byte("release\n")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Status(context.Background()); err != nil {
		t.Fatal("lock not released", err)
	}
}

func TestSubprocessCrashRecovery(t *testing.T) {
	points := []string{"before_publish:.cutly/v2/commits/<hash>.json", "after_publish:.cutly/v2/commits/<hash>.json", "after_publish:.cutly/v2/states/<hash>.json", "before_publish:" + intentName, "after_publish:" + intentName, "after_sync:" + intentName, "after_publish:" + DocumentName, "after_sync:" + DocumentName, "after_publish:" + stateName, "after_sync:" + stateName, "after_remove_intent", "after_remove_sync"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			s := createTest(t)
			if _, err := s.Plan(context.Background(), metadataTransaction(t, s, "p", "x", `0.25`)); err != nil {
				t.Fatal(err)
			}
			cmd := child(t, s, "crash", point)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("child %v %s", err, out)
			}
			reopened, err := New(s.dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			status, err := reopened.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			committed := point == "after_publish:"+intentName || point == "after_sync:"+intentName || point == "after_publish:"+DocumentName || point == "after_sync:"+DocumentName || point == "after_publish:"+stateName || point == "after_sync:"+stateName || point == "after_remove_intent" || point == "after_remove_sync"
			if committed && status.Revision != 1 || !committed && status.Revision != 0 {
				t.Fatalf("%#v", status)
			}
			replay, err := reopened.Apply(context.Background(), "p")
			if err != nil || !replay.OK || replay.Replayed != committed {
				t.Fatalf("%#v %v", replay, err)
			}
			files, err := os.ReadDir(filepath.Join(s.dir, ".cutly/v2/commits"))
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, f := range files {
				if len(f.Name()) == 69 {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%d immutable commits", count)
			}
		})
	}
}
