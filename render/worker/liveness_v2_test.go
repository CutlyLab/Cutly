//go:build darwin || linux

package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParentLivenessV2Helper(t *testing.T) {
	mode := os.Getenv("CUTLY_LIVENESS_V2")
	if mode == "" {
		return
	}
	if mode == "child" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stop, err := WatchParentV2(ctx, cancel, os.NewFile(4, "liveness"))
		if err != nil {
			os.Exit(11)
		}
		defer stop()
		_ = os.WriteFile(os.Getenv("CUTLY_LIVENESS_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0600)
		// Deliberately ignore cancellation to require the watcher hard-kill guarantee.
		for {
			time.Sleep(time.Second)
		}
	}
	if mode == "parent" {
		self, _ := os.Executable()
		reader, writer, err := os.Pipe()
		if err != nil {
			os.Exit(12)
		}
		defer writer.Close()
		defer reader.Close()
		null, err := os.Open(os.DevNull)
		if err != nil {
			os.Exit(13)
		}
		defer null.Close()
		command := exec.Command(self, "-test.run=^TestParentLivenessV2Helper$")
		command.Env = []string{"CUTLY_LIVENESS_V2=child", "CUTLY_LIVENESS_MARKER=" + os.Getenv("CUTLY_LIVENESS_MARKER")}
		command.ExtraFiles = []*os.File{null, reader}
		configureProcess(command)
		if err = command.Start(); err != nil {
			os.Exit(14)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(15)
}
func TestParentSIGKILLV2ReapsWorker(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "child.pid")
	parent := exec.Command(self, "-test.run=^TestParentLivenessV2Helper$")
	parent.Env = []string{"CUTLY_LIVENESS_V2=parent", "CUTLY_LIVENESS_MARKER=" + marker}
	parent.Stdout = io.Discard
	parent.Stderr = io.Discard
	if err = parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	child := 0
	// Full-suite race/normal runs can start this nested test binary while other
	// process-heavy fault matrices are compiling or exiting. Startup is not the
	// two-second liveness SLO; only termination after the observed handshake is.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, e := os.ReadFile(marker); e == nil {
			candidate, parseErr := strconv.Atoi(string(raw))
			if parseErr == nil && candidate > 0 {
				child = candidate
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if child <= 0 {
		t.Fatal("child never reached liveness watcher")
	}
	defer syscall.Kill(-child, syscall.SIGKILL)
	start := time.Now()
	if err = parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	for time.Since(start) < 2*time.Second {
		err := syscall.Kill(child, 0)
		if err == syscall.ESRCH {
			return
		}
		// A briefly unreaped orphan zombie is terminal, never a surviving worker.
		command := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(child))
		raw, _ := command.Output()
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("worker %d survived parent SIGKILL for %s", child, time.Since(start)))
}
