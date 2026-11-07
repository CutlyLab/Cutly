package ffmpegbackend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestPublishedRecoveryOpensExactVerifiedMedia(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	if artifact, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); err != nil || found || artifact != (renderapi.RenderArtifact{}) {
		t.Fatalf("missing artifact = %+v, found=%v, err=%v", artifact, found, err)
	}
	want, err := fixture.backend.Render(context.Background(), fixture.request, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("inspect got=%+v found=%v err=%v, want=%+v", got, found, err, want)
	}
	published, found, err := fixture.backend.OpenPublished(context.Background(), fixture.request)
	if err != nil || !found {
		t.Fatalf("open found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(published.Artifact(), want) {
		t.Fatalf("open artifact=%+v, want=%+v", published.Artifact(), want)
	}
	raw, err := io.ReadAll(published)
	if err != nil || string(raw) != "fake mp4" {
		t.Fatalf("media bytes=%q err=%v", raw, err)
	}
	if _, err := published.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := published.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := published.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after close = %v", err)
	}
}

func TestPublishedRecoveryReturnsAnonymousImmutableSnapshot(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
		t.Fatal(err)
	}
	published, found, err := fixture.backend.OpenPublished(context.Background(), fixture.request)
	if err != nil || !found {
		t.Fatalf("open found=%v err=%v", found, err)
	}
	entries, err := os.ReadDir(fixture.outputRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".cutly-recovery-") {
			t.Fatalf("recovery snapshot remained path-addressable: %q", entry.Name())
		}
	}
	// Mutating the original published inode after OpenPublished must not alter
	// the already-verified bytes handed to CAS.
	if err := os.WriteFile(filepath.Join(fixture.outputRoot, "result.mp4"), []byte("evil mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(published)
	if err != nil || string(raw) != "fake mp4" {
		t.Fatalf("snapshot bytes=%q err=%v", raw, err)
	}
	if err := published.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); found || !errors.Is(err, ErrPublication) {
		t.Fatalf("mutated original found=%v err=%v", found, err)
	}
}

func TestPublishedMediaCopiesShareConcurrentCloseState(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
		t.Fatal(err)
	}
	published, found, err := fixture.backend.OpenPublished(context.Background(), fixture.request)
	if err != nil || !found {
		t.Fatalf("open found=%v err=%v", found, err)
	}
	copyValue := *published
	start := make(chan struct{})
	results := make(chan error, 3)
	go func() {
		<-start
		_, err := published.Read(make([]byte, 4))
		results <- err
	}()
	go func() {
		<-start
		_, err := copyValue.Seek(0, io.SeekStart)
		results <- err
	}()
	go func() {
		<-start
		results <- copyValue.Close()
	}()
	close(start)
	for index := 0; index < 3; index++ {
		if err := <-results; err != nil && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("concurrent operation error = %v", err)
		}
	}
	if err := published.Close(); err != nil {
		t.Fatalf("idempotent close = %v", err)
	}
	if _, err := published.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after shared close = %v", err)
	}
}

func TestPublishedRecoveryRejectsPartialPairs(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
	}{
		{name: "media only", filename: "result.mp4"},
		{name: "evidence only", filename: "result.mp4.evidence.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackendFixture(t, fakeSuccessScript)
			if err := os.WriteFile(filepath.Join(fixture.outputRoot, test.filename), []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); found || !errors.Is(err, ErrPublication) {
				t.Fatalf("partial found=%v err=%v", found, err)
			}
		})
	}
}

func TestPublishedRecoveryRejectsStaticAndRacingSymlinks(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		fixture := newBackendFixture(t, fakeSuccessScript)
		if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
			t.Fatal(err)
		}
		media := filepath.Join(fixture.outputRoot, "result.mp4")
		backup := filepath.Join(fixture.outputRoot, "backup.mp4")
		if err := os.Rename(media, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("backup.mp4", media); err != nil {
			t.Fatal(err)
		}
		if _, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); found || !errors.Is(err, ErrPublication) {
			t.Fatalf("static symlink found=%v err=%v", found, err)
		}
	})

	t.Run("swap after lstat", func(t *testing.T) {
		fixture := newBackendFixture(t, fakeSuccessScript)
		if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
			t.Fatal(err)
		}
		media := filepath.Join(fixture.outputRoot, "result.mp4")
		backup := filepath.Join(fixture.outputRoot, "backup.mp4")
		var swapped atomic.Bool
		fixture.backend.recoveryHook = func(stage string) {
			if stage != "media.after_lstat" || !swapped.CompareAndSwap(false, true) {
				return
			}
			if err := os.Rename(media, backup); err != nil {
				t.Errorf("rename at hook: %v", err)
				return
			}
			if err := os.Symlink("backup.mp4", media); err != nil {
				t.Errorf("symlink at hook: %v", err)
			}
		}
		if _, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); found || !errors.Is(err, ErrPublication) {
			t.Fatalf("racing symlink found=%v err=%v", found, err)
		}
		if !swapped.Load() {
			t.Fatal("race hook did not run")
		}
	})
}

func TestPublishedRecoveryRejectsMutatedMediaAndOversizeEvidence(t *testing.T) {
	t.Run("mutated media", func(t *testing.T) {
		fixture := newBackendFixture(t, fakeSuccessScript)
		if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
			t.Fatal(err)
		}
		media := filepath.Join(fixture.outputRoot, "result.mp4")
		file, err := os.OpenFile(media, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString("tampered")
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			t.Fatal(err)
		}
		if _, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); found || !errors.Is(err, ErrPublication) {
			t.Fatalf("mutated media found=%v err=%v", found, err)
		}
	})

	t.Run("oversize evidence", func(t *testing.T) {
		fixture := newBackendFixture(t, fakeSuccessScript)
		if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
			t.Fatal(err)
		}
		evidencePath := filepath.Join(fixture.outputRoot, "result.mp4.evidence.json")
		if err := os.WriteFile(evidencePath, bytes.Repeat([]byte{'x'}, int(MaxPublishedEvidenceBytes)+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request); found || !errors.Is(err, ErrPublication) {
			t.Fatalf("oversize evidence found=%v err=%v", found, err)
		}
	})
}

func TestPublishedRecoveryBindsJobAndIsConcurrentSafe(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	want, err := fixture.backend.Render(context.Background(), fixture.request, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed := fixture.request
	changed.Job.ID = "another-job"
	changed.Job.IdempotencyKey = "another-job-once"
	if _, found, err := fixture.backend.InspectPublished(context.Background(), changed); found || !errors.Is(err, ErrPublication) {
		t.Fatalf("foreign job found=%v err=%v", found, err)
	}

	const readers = 16
	var wait sync.WaitGroup
	errorsSeen := make(chan error, readers)
	for index := 0; index < readers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			artifact, found, err := fixture.backend.InspectPublished(context.Background(), fixture.request)
			if err != nil {
				errorsSeen <- err
				return
			}
			if !found || !reflect.DeepEqual(artifact, want) {
				errorsSeen <- errors.New("concurrent inspection returned another artifact")
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func TestPublishedRecoveryHonorsCanceledContext(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found, err := fixture.backend.InspectPublished(ctx, fixture.request); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled found=%v err=%v", found, err)
	}
}
