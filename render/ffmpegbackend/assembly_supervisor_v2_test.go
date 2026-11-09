//go:build darwin || linux

package ffmpegbackend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

func buildSupervisorV2(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(t.TempDir(), "cutly-ffmpeg-supervisor-v2")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/cutly-ffmpeg-supervisor-v2")
	cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	if data, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build supervisor: %v %s", e, data)
	}
	return out
}
func superviseFixtureV2(t *testing.T, f *assemblyFixtureNativeV2, helper string) {
	t.Helper()
	ctx := context.Background()
	hash, _, e := hashRegularFile(ctx, helper)
	if e != nil {
		t.Fatal(e)
	}
	f.o.SupervisorExecutable = helper
	f.o.SupervisorExecutableDigest = hash
	f.o.SupervisorDependencyFingerprint = digestV2([]byte("local test helper closure"))
	m := f.o.Manifest
	m.Backend, e = ChunkAssemblerSupervisedCapabilitiesV2(f.o.SDKBuildDigest, hash, f.o.SupervisorDependencyFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	m.MediaRuntime.EncoderProfile = ChunkAssemblerSupervisedEncoderProfileV2
	m, e = api.FreezeExecutionManifestV2(ctx, f.o.Plan, m)
	if e != nil {
		t.Fatal(e)
	}
	f.o.Manifest = m
	f.r.ExecutionManifestFingerprint = m.Fingerprint
	f.r.MediaRuntime = m.MediaRuntime
	signBindingV2(t, &f.r)
	f.v, e = evidence.VerifyBindingV2(ctx, f.r)
	if e != nil {
		t.Fatal(e)
	}
	f.b, e = NewChunkAssemblerV2(ctx, f.o)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.b.Close() })
}
func TestChunkAssemblerV2SupervisedRealEncodeAndAuthority(t *testing.T) {
	helper := buildSupervisorV2(t)
	f := assemblyNativeV2(t, 48001)
	superviseFixtureV2(t, f, helper)
	if _, e := f.b.AssembleChunks(context.Background(), f.v, f.source(), nil); e != nil {
		t.Fatal(e)
	}
	if _, found, e := f.b.OpenChunkAssembly(context.Background(), f.v); e != nil || !found {
		t.Fatalf("supervised reopen: %v %v", found, e)
	}
	for _, mode := range []string{"missing-path", "wrong-hash", "wrong-closure", "wrong-profile"} {
		t.Run(mode, func(t *testing.T) {
			o := f.o
			switch mode {
			case "missing-path":
				o.SupervisorExecutable = ""
			case "wrong-hash":
				o.SupervisorExecutableDigest = digestV2([]byte("wrong"))
			case "wrong-closure":
				o.SupervisorDependencyFingerprint = digestV2([]byte("wrong"))
			case "wrong-profile":
				o.Manifest.MediaRuntime.EncoderProfile = ChunkAssemblerEncoderProfileV2
			}
			b, e := NewChunkAssemblerV2(context.Background(), o)
			if e == nil {
				b.Close()
				t.Fatal("accepted supervisor authority drift")
			}
		})
	}
	if e := os.Rename(helper, helper+".measured"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(helper, []byte("replaced"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.b.InspectChunkAssembly(context.Background(), f.v); e == nil {
		t.Fatal("accepted replaced supervisor")
	}
}

func TestChunkAssemblerV2SupervisorHostChild(t *testing.T) {
	helper := os.Getenv("CUTLY_SUPERVISOR_HOST_TEST")
	if helper == "" {
		return
	}
	ffmpeg := os.Getenv("CUTLY_SUPERVISOR_RUNTIME_TEST")
	hash, _, e := hashRegularFile(context.Background(), helper)
	if e != nil {
		t.Fatal(e)
	}
	b := &ChunkAssemblerV2{io: &Backend{executable: ffmpeg}, supervisor: helper, supervisorDigest: hash}
	original := newCommandContext(context.Background(), ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc=size=16x16:rate=1", "-t", "600", "-f", "null", "-")
	original.Env = controlledRuntimeEnvironment()
	cmd, cleanup, e := b.superviseCommandV2(context.Background(), original)
	if e != nil {
		t.Fatal(e)
	}
	defer cleanup()
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("CUTLY_SUPERVISOR_PID_TEST"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); e != nil {
		t.Fatal(e)
	}
	if e = cmd.Wait(); e != nil {
		t.Fatal(e)
	}
}
func processRowsV2(t *testing.T) [][]string {
	t.Helper()
	data, e := exec.Command("ps", "-axo", "pid=,ppid=,stat=").Output()
	if e != nil {
		t.Fatal(e)
	}
	var rows [][]string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 {
			rows = append(rows, fields)
		}
	}
	return rows
}
func TestChunkAssemblerV2SupervisorActualParentSIGKILL(t *testing.T) {
	helper := buildSupervisorV2(t)
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("FFmpeg unavailable")
	}
	pidFile := filepath.Join(t.TempDir(), "supervisor.pid")
	host := exec.Command(os.Args[0], "-test.run=^TestChunkAssemblerV2SupervisorHostChild$", "-test.count=1")
	host.Env = append(os.Environ(), "CUTLY_SUPERVISOR_HOST_TEST="+helper, "CUTLY_SUPERVISOR_RUNTIME_TEST="+ffmpeg, "CUTLY_SUPERVISOR_PID_TEST="+pidFile)
	if e = host.Start(); e != nil {
		t.Fatal(e)
	}
	defer host.Process.Kill()
	var supervisor, encoder int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidFile)
		supervisor, _ = strconv.Atoi(string(raw))
		for _, row := range processRowsV2(t) {
			if supervisor > 0 && row[1] == strconv.Itoa(supervisor) && !strings.HasPrefix(row[2], "Z") {
				encoder, _ = strconv.Atoi(row[0])
			}
		}
		if encoder > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if encoder == 0 {
		t.Fatal("live FFmpeg child was not observed")
	}
	defer syscall.Kill(-supervisor, syscall.SIGKILL)
	// Prove this is live long-running FFmpeg, not natural completion at the gate.
	time.Sleep(100 * time.Millisecond)
	if e = syscall.Kill(encoder, 0); e != nil {
		t.Fatal("FFmpeg exited before parent kill")
	}
	if e = host.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = host.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		live := false
		for _, row := range processRowsV2(t) {
			if (row[0] == strconv.Itoa(supervisor) || row[0] == strconv.Itoa(encoder)) && !strings.HasPrefix(row[2], "Z") {
				live = true
			}
		}
		if !live {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("parent SIGKILL left live supervisor %d or encoder %d", supervisor, encoder))
}
