//go:build darwin

package ffmpegbackend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// TestM1RealSmallFilesystemExhaustionRollsBack is an opt-in system acceptance test. Unlike the
// deterministic syscall injection test, it writes through the real encoder descriptor until a
// writable HFS+ disk image reports ENOSPC, then proves the backend removed every staging and
// publication name. The image and mount point live under t.TempDir; no host volume is modified.
//
// Run on macOS with:
//
//	CUTLY_REAL_SMALL_FS_TEST=1 go test -run TestM1RealSmallFilesystemExhaustionRollsBack ./render/ffmpegbackend
func TestM1RealSmallFilesystemExhaustionRollsBack(t *testing.T) {
	if os.Getenv("CUTLY_REAL_SMALL_FS_TEST") != "1" {
		t.Skip("set CUTLY_REAL_SMALL_FS_TEST=1 on macOS to run the real small-filesystem ENOSPC acceptance test")
	}
	if _, err := exec.LookPath("hdiutil"); err != nil {
		t.Skip("hdiutil is unavailable; a writable temporary disk image is required")
	}

	mount := mountSmallTestVolume(t)
	outputRoot := filepath.Join(mount, "output")
	if err := os.Mkdir(outputRoot, 0o700); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("temporary disk image is not writable in this test environment: %v", err)
		}
		t.Fatalf("create mounted output root: %v", err)
	}

	seed := newBackendFixture(t, fakeFillFilesystemScript)
	inputRootSet := make(map[string]struct{})
	for _, resource := range seed.request.Manifest.Resources {
		inputRootSet[filepath.Dir(resource.Locator)] = struct{}{}
	}
	inputRoots := make([]string, 0, len(inputRootSet))
	for root := range inputRootSet {
		inputRoots = append(inputRoots, root)
	}
	sort.Strings(inputRoots)

	backend, err := New(context.Background(), Options{
		Executable: seed.backend.executable, OutputRoot: outputRoot, InputRoots: inputRoots,
		MaxStderrBytes: 4096, ProbeResolver: seed.backend.probeResolver,
		SDKBuildDigest: digestByte('a'), RuntimeDependencyFingerprint: digestByte('f'),
	})
	if err != nil {
		t.Fatalf("construct mounted backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	request := seed.request
	request.Job.ID = "real-small-fs"
	request.Job.IdempotencyKey = "real-small-fs-once"
	request.Job.ArtifactURI = fileURI(filepath.Join(backend.outputRoot, "result.mp4"))
	var events []renderapi.ProgressEvent
	renderContext, cancelRender := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRender()
	_, err = backend.Render(renderContext, request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if !errors.Is(err, ErrCommand) || !strings.Contains(strings.ToLower(err.Error()), "no space") {
		t.Fatalf("real small-filesystem error = %v, want encoder ENOSPC", err)
	}
	if len(events) == 0 || events[len(events)-1].State != renderapi.JobFailed {
		t.Fatalf("terminal events = %+v", events)
	}
	assertOutputRootEmpty(t, backend.outputRoot)
}

func mountSmallTestVolume(t *testing.T) string {
	t.Helper()
	image := filepath.Join(t.TempDir(), "cutly-smallfs.dmg")
	mount := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(arguments ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "hdiutil", arguments...).CombinedOutput()
		if err != nil {
			return errors.Join(err, errors.New(strings.TrimSpace(string(output))))
		}
		return nil
	}
	if err := run("create", "-quiet", "-size", "16m", "-fs", "HFS+", "-volname", "CutlySmallFSTest", image); err != nil {
		t.Skipf("cannot create isolated small disk image: %v", err)
	}
	if err := run("attach", "-quiet", "-nobrowse", "-mountpoint", mount, image); err != nil {
		t.Skipf("cannot attach isolated small disk image: %v", err)
	}
	t.Cleanup(func() {
		if err := run("detach", "-quiet", mount); err != nil {
			t.Logf("normal detach of small test volume failed, retrying with -force: %v", err)
			if forceErr := run("detach", "-force", "-quiet", mount); forceErr != nil {
				t.Errorf("detach isolated small test volume after forced retry: %v", forceErr)
			}
		}
	})
	return mount
}

const fakeFillFilesystemScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*) dd if=/dev/zero bs=16384 count=60 2>/dev/null; exit 0 ;;
  *" -f s16le "*) dd if=/dev/zero bs=384000 count=1 2>/dev/null; exit 0 ;;
esac
descriptor=""
previous=""
for last do
  if [ "$previous" = "-fd" ]; then descriptor="$last"; fi
  previous="$last"
done
case "$last" in
  fd:*)
    case "$descriptor" in
      ''|*[!0-9]*) echo "invalid inherited descriptor" >&2; exit 64 ;;
    esac
    eval "dd if=/dev/zero bs=1048576 count=64 >&$descriptor"
    ;;
  *) dd if=/dev/zero bs=1048576 count=64 of="$last" ;;
esac
`
