//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package proberegistry

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestNewFailsClosedOnUnsupportedPlatform(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	if _, err := New(root, Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("New error=%v, want %v", err, ErrUnsupported)
	}
}
