//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderStateDirectoryLockSurvivesLockPathRemoval(t *testing.T) {
	root := t.TempDir()
	owner, err := acquireRenderStateOwner(root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, renderStateOwnerLockName)
	if err := os.Remove(lockPath); err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	if second, err := acquireRenderStateOwner(root); !errors.Is(err, ErrRenderStateAlreadyOwned) || second != nil {
		_ = owner.Close()
		t.Fatalf("owner after lock-path removal = %v, %v", second, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := acquireRenderStateOwner(root)
	if err != nil {
		t.Fatalf("reacquire after release = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
