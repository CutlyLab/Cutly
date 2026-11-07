package ffmpegbackend

import (
	"context"
	"errors"
	"syscall"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// TestM1ENOSPCAtPublicationRollsBack injects the real ENOSPC errno at the directory durability
// boundary after the evidence link exists but before the media commit marker is published. The
// production rollback path must remove that sidecar and leave no visible or temporary artifact.
func TestM1ENOSPCAtPublicationRollsBack(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	realSync := fixture.backend.syncDirectory
	syncCalls := 0
	fixture.backend.syncDirectory = func() error {
		syncCalls++
		if syncCalls == 1 {
			return syscall.ENOSPC
		}
		return realSync()
	}
	var events []renderapi.ProgressEvent
	_, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if !errors.Is(err, ErrPublication) || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("ENOSPC publication error = %v", err)
	}
	if syncCalls < 2 {
		t.Fatalf("directory sync calls = %d, want failure plus rollback sync", syncCalls)
	}
	if len(events) == 0 || events[len(events)-1].State != renderapi.JobFailed {
		t.Fatalf("terminal events = %+v", events)
	}
	assertOutputRootEmpty(t, fixture.outputRoot)
}
