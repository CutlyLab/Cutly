package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	ownerHelperMarker = "CUTLYD_OWNER_LOCK_HELPER"
	ownerHelperRoot   = "CUTLYD_OWNER_LOCK_ROOT"
)

func TestRenderStateOwnerLockIsExclusiveAndReusable(t *testing.T) {
	root := t.TempDir()
	owner, err := acquireRenderStateOwner(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRenderStateOwner(root); !errors.Is(err, ErrRenderStateAlreadyOwned) {
		t.Fatalf("second owner error = %v", err)
	}
	var closeWait sync.WaitGroup
	closeErrors := make(chan error, 16)
	for index := 0; index < cap(closeErrors); index++ {
		closeWait.Add(1)
		go func() {
			defer closeWait.Done()
			closeErrors <- owner.Close()
		}()
	}
	closeWait.Wait()
	close(closeErrors)
	for err := range closeErrors {
		if err != nil {
			t.Fatalf("concurrent close = %v", err)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
	reopened, err := acquireRenderStateOwner(root)
	if err != nil {
		t.Fatalf("reacquire = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	var nilOwner *renderStateOwnerLock
	if err := nilOwner.Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
}

func TestRenderStateOwnerLockIsCrossProcess(t *testing.T) {
	root := t.TempDir()
	owner, err := acquireRenderStateOwner(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRenderStateOwnerLockHelperProcess$")
	command.Env = append(os.Environ(), ownerHelperMarker+"=1", ownerHelperRoot+"="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper error = %v, output = %s", err, output)
	}
}

func TestRenderStateOwnerLockHelperProcess(t *testing.T) {
	if os.Getenv(ownerHelperMarker) != "1" {
		return
	}
	if _, err := acquireRenderStateOwner(os.Getenv(ownerHelperRoot)); !errors.Is(err, ErrRenderStateAlreadyOwned) {
		t.Fatalf("cross-process owner error = %v", err)
	}
}

func TestRenderStateOwnerLockConcurrentAcquisition(t *testing.T) {
	root := t.TempDir()
	const contenders = 32
	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			owner, err := acquireRenderStateOwner(root)
			results <- err
			if err == nil {
				<-release
				if closeErr := owner.Close(); closeErr != nil {
					results <- closeErr
				}
			}
		}()
	}
	close(start)
	winners := 0
	for index := 0; index < contenders; index++ {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrRenderStateAlreadyOwned) {
			t.Fatalf("contender error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("owners = %d, want 1", winners)
	}
	close(release)
	wait.Wait()
}

func TestRenderStateOwnerLockRejectsInvalidPathsAndLinks(t *testing.T) {
	for _, path := range []string{"", ".", string(filepath.Separator), filepath.Join(t.TempDir(), "missing")} {
		if _, err := acquireRenderStateOwner(path); !errors.Is(err, ErrRenderStateOwnerInvalid) {
			t.Fatalf("path %q error = %v", path, err)
		}
	}
	dirtyRoot := t.TempDir()
	dirty := dirtyRoot + string(filepath.Separator) + "."
	if _, err := acquireRenderStateOwner(dirty); !errors.Is(err, ErrRenderStateOwnerInvalid) {
		t.Fatalf("dirty path error = %v", err)
	}

	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := acquireRenderStateOwner(linkedRoot); !errors.Is(err, ErrRenderStateOwnerInvalid) {
		t.Fatalf("linked root error = %v", err)
	}

	lockLinkRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(lockLinkRoot, "target"), filepath.Join(lockLinkRoot, renderStateOwnerLockName)); err != nil {
		t.Skipf("lock symlink unavailable: %v", err)
	}
	if _, err := acquireRenderStateOwner(lockLinkRoot); !errors.Is(err, ErrRenderStateOwnerInvalid) {
		t.Fatalf("linked lock error = %v", err)
	}
}

func TestRenderStateOwnerLockRejectsNonRegularLockFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, renderStateOwnerLockName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRenderStateOwner(root); !errors.Is(err, ErrRenderStateOwnerInvalid) {
		t.Fatalf("directory lock error = %v", err)
	}
}
