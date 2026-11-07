//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package main

type renderStateOwnerHandle struct{}

func acquireRenderStateOwnerHandle(string) (*renderStateOwnerHandle, error) {
	return nil, ErrRenderStateOwnerUnsupported
}

func (*renderStateOwnerHandle) close() error { return nil }
