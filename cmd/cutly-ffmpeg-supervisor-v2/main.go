//go:build darwin || linux

// cutly-ffmpeg-supervisor-v2 is a native, single-child process-group supervisor.
// The host measures this executable and its dependency closure independently of
// FFmpeg. It accepts only inherited input/output descriptors and an inherited
// parent-liveness reader; no shell, network, plugins or request JSON is loaded.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() { os.Exit(run()) }
func run() int {
	flags := flag.NewFlagSet("cutly-ffmpeg-supervisor-v2", flag.ContinueOnError)
	runtimePath := flags.String("runtime", "", "host-owned absolute FFmpeg path")
	runtimeHash := flags.String("runtime-sha256", "", "measured FFmpeg binary hash")
	inherited := flags.Int("files", 0, "number of runtime inherited descriptors")
	timeout := flags.Int("max-seconds", 3600, "hard process lifetime bound")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if *inherited < 0 || *inherited > 16 || *timeout <= 0 || *timeout > 86400 || len(flags.Args()) == 0 || len(*runtimePath) == 0 || (*runtimePath)[0] != '/' {
		fmt.Fprintln(os.Stderr, "invalid supervisor invocation")
		return 2
	}
	// The caller starts us as the leader of a dedicated process group. Never kill
	// an inherited unrelated group even if this executable is invoked incorrectly.
	group, err := syscall.Getpgid(os.Getpid())
	if err != nil || group != os.Getpid() {
		fmt.Fprintln(os.Stderr, "supervisor requires isolated process group")
		return 2
	}
	life := os.NewFile(uintptr(3+*inherited), "parent-liveness")
	if life == nil {
		return 2
	}
	// ExtraFiles arrives without close-on-exec. Explicitly exclude liveness
	// from the runtime child even when descriptor numbers overlap its mappings.
	syscall.CloseOnExec(int(life.Fd()))
	done := make(chan struct{})
	defer func() { close(done); _ = life.Close() }()
	kill := func() { _ = syscall.Kill(-group, syscall.SIGKILL) }
	go func() {
		var one [1]byte
		_, _ = life.Read(one[:])
		select {
		case <-done:
			return
		default:
			kill()
		}
	}()
	timer := time.AfterFunc(time.Duration(*timeout)*time.Second, kill)
	defer timer.Stop()
	file, err := os.Open(*runtimePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return 2
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	err = errors.Join(err, file.Close())
	if err != nil || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != *runtimeHash {
		fmt.Fprintln(os.Stderr, "supervisor FFmpeg measured identity differs")
		return 2
	}
	cmd := exec.Command(*runtimePath, flags.Args()...)
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Preserve the existing fd: numbers without forwarding the liveness reader.
	for i := 0; i < *inherited; i++ {
		f := os.NewFile(uintptr(3+i), "runtime-fd-"+strconv.Itoa(i))
		if f == nil {
			return 2
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, f)
	}
	err = cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if n := exit.ExitCode(); n >= 0 {
			return n
		}
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
