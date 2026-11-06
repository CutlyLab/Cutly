package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const renderStateOwnerLockName = ".cutlyd-owner.lock"

var (
	ErrRenderStateOwnerInvalid     = errors.New("cutlyd: render state owner path is invalid")
	ErrRenderStateAlreadyOwned     = errors.New("cutlyd: render state is already owned")
	ErrRenderStateOwnerUnsupported = errors.New("cutlyd: render state ownership is unsupported")
)

// renderStateOwnerLock owns one already initialized render-state directory for
// the lifetime of a cutlyd process. The persistent lock file is only an inode
// on which the OS holds an advisory/exclusive handle; its existence is never
// interpreted as ownership.
type renderStateOwnerLock struct {
	mu     sync.Mutex
	handle *renderStateOwnerHandle
}

// acquireRenderStateOwner is non-blocking. stateDir must already exist, be a
// clean absolute non-volume-root path, and be a real directory rather than a
// symlink/reparse point. The host creates this directory before initializing
// any durable render component, then retains the returned owner until all
// workers and stores have closed.
func acquireRenderStateOwner(stateDir string) (*renderStateOwnerLock, error) {
	if stateDir == "" || strings.ContainsRune(stateDir, 0) || !filepath.IsAbs(stateDir) ||
		filepath.Clean(stateDir) != stateDir || filepath.Dir(stateDir) == stateDir {
		return nil, ErrRenderStateOwnerInvalid
	}
	information, err := os.Lstat(stateDir)
	if err != nil {
		return nil, errors.Join(ErrRenderStateOwnerInvalid, err)
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return nil, ErrRenderStateOwnerInvalid
	}
	handle, err := acquireRenderStateOwnerHandle(stateDir)
	if err != nil {
		return nil, fmt.Errorf("cutlyd: acquire render state owner: %w", err)
	}
	if handle == nil {
		return nil, ErrRenderStateOwnerUnsupported
	}
	return &renderStateOwnerLock{handle: handle}, nil
}

// Close releases process ownership. It is safe to call concurrently, more
// than once, or on a nil receiver.
func (owner *renderStateOwnerLock) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.handle == nil {
		return nil
	}
	handle := owner.handle
	owner.handle = nil
	return handle.close()
}
