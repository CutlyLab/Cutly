//go:build windows

package ffprobe

import (
	"context"
	"errors"
	"testing"
)

func TestWindowsSecureProfileFailsClosedAndRejectsUNC(t *testing.T) {
	if _, err := New(
		context.Background(),
		"unused",
		DefaultBuildIdentity("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Options{},
	); !errors.Is(err, ErrExecutable) {
		t.Fatalf("unsupported inherited-handle profile error = %v", err)
	}
	if _, err := secureLocalPath(`\\server\share\video.mp4`); !errors.Is(err, ErrInput) {
		t.Fatalf("UNC error = %v", err)
	}
}
