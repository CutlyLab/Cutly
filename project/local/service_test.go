package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestLifecyclePlanApplyHistoryRevertAndOwnership(t *testing.T) {
	service := createFixtureService(t, Options{})
	ctx := context.Background()
	before, err := service.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transaction := moveTransaction("tx-move", "key-move", before.Document.Revision, 2)
	planned, err := service.Plan(ctx, transaction)
	if err != nil || !planned.OK || planned.State != "planned" || planned.Changed {
		t.Fatalf("plan = %+v, err = %v", planned, err)
	}
	afterPlan, err := service.Inspect(ctx)
	if err != nil || afterPlan.DocumentHash != before.DocumentHash || afterPlan.Document.Revision != before.Document.Revision || len(afterPlan.PendingPlans) != 1 {
		t.Fatalf("after plan = %+v, err = %v", afterPlan, err)
	}

	committed, err := service.Apply(ctx, transaction.ID, before.Document.Revision)
	if err != nil || !committed.OK || !committed.Changed || !committed.RenderRequired || committed.Commit == nil || *committed.NewRevision != before.Document.Revision+1 {
		t.Fatalf("apply = %+v, err = %v", committed, err)
	}
	replayed, err := service.Apply(ctx, transaction.ID, before.Document.Revision)
	if err != nil || !replayed.OK || !replayed.Replayed || replayed.Changed || replayed.RenderRequired {
		t.Fatalf("apply replay = %+v, err = %v", replayed, err)
	}
	history, err := service.History(ctx, 0, 10)
	if err != nil || len(history) != 1 || history[0].Transaction.ID != transaction.ID {
		t.Fatalf("history = %+v, err = %v", history, err)
	}

	reverted, err := service.Revert(ctx, transaction.ID, *committed.NewRevision)
	if err != nil || !reverted.OK || reverted.RevertsTransactionID != transaction.ID || reverted.Commit == nil {
		t.Fatalf("revert = %+v, err = %v", reverted, err)
	}
	revertReplay, err := service.Revert(ctx, transaction.ID, *committed.NewRevision)
	if err != nil || !revertReplay.OK || !revertReplay.Replayed {
		t.Fatalf("revert replay = %+v, err = %v", revertReplay, err)
	}
	afterRevert, err := service.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantDocument := before.Document
	gotDocument := afterRevert.Document
	wantDocument.Revision = 0
	gotDocument.Revision = 0
	wantHash, _ := videoedit.DocumentHash(wantDocument)
	gotHash, _ := videoedit.DocumentHash(gotDocument)
	if wantHash != gotHash || afterRevert.Document.Revision != before.Document.Revision+2 {
		t.Fatalf("revert content/revision mismatch: %s != %s, revision=%d", wantHash, gotHash, afterRevert.Document.Revision)
	}

	// Returned values must not alias retained service state.
	afterRevert.Document.Timelines[0].Tracks[0].Clips[0].ID = "mutated"
	history[0].Transaction.Operations[0].ID = "mutated"
	again, err := service.Inspect(ctx)
	if err != nil || again.Document.Timelines[0].Tracks[0].Clips[0].ID != "clip-1" {
		t.Fatalf("inspect aliases caller state: %+v, %v", again, err)
	}
	againHistory, err := service.History(ctx, 0, 10)
	if err != nil || againHistory[0].Transaction.Operations[0].ID == "mutated" {
		t.Fatalf("history aliases caller state: %+v, %v", againHistory, err)
	}
}

func TestDiscardAndPlanReplayArePersistent(t *testing.T) {
	service := createFixtureService(t, Options{})
	transaction := moveTransaction("tx-discard", "key-discard", 3, 2)
	first, err := service.Plan(context.Background(), transaction)
	if err != nil || !first.OK {
		t.Fatalf("plan = %+v, %v", first, err)
	}
	replay, err := service.Plan(context.Background(), transaction)
	if err != nil || !replay.OK || !replay.Replayed {
		t.Fatalf("plan replay = %+v, %v", replay, err)
	}
	discarded, err := service.Discard(context.Background(), transaction.ID)
	if err != nil || !discarded.OK || !discarded.Changed {
		t.Fatalf("discard = %+v, %v", discarded, err)
	}
	discardReplay, err := service.Discard(context.Background(), transaction.ID)
	if err != nil || !discardReplay.OK || !discardReplay.Replayed {
		t.Fatalf("discard replay = %+v, %v", discardReplay, err)
	}
	apply, err := service.Apply(context.Background(), transaction.ID, 3)
	if err != nil || apply.OK || apply.Diagnostics[0].Code != CodePlanDiscarded {
		t.Fatalf("apply discarded = %+v, %v", apply, err)
	}

	reopened, err := New(service.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := reopened.Status(context.Background())
	if err != nil || status.DiscardedPlans != 1 || status.Revision != 3 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestCommitIntentRecoversAfterDocumentPublicationFault(t *testing.T) {
	service := createFixtureService(t, Options{})
	transaction := moveTransaction("tx-crash", "key-crash", 3, 2)
	if planned, err := service.Plan(context.Background(), transaction); err != nil || !planned.OK {
		t.Fatalf("plan = %+v, %v", planned, err)
	}
	fired := false
	service.fault = func(point string) error {
		if !fired && point == "atomic.after_rename:"+DocumentName {
			fired = true
			return errors.New("simulated power boundary")
		}
		return nil
	}
	if _, err := service.Apply(context.Background(), transaction.ID, 3); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("apply error = %v", err)
	}
	service.fault = nil
	reopened, err := New(service.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Inspect(context.Background())
	if err != nil || snapshot.Document.Revision != 4 || snapshot.Document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Start.Value != 2 {
		t.Fatalf("recovered snapshot = %+v, %v", snapshot, err)
	}
	history, err := reopened.History(context.Background(), 0, 10)
	if err != nil || len(history) != 1 || history[0].Transaction.ID != transaction.ID {
		t.Fatalf("recovered history = %+v, %v", history, err)
	}
	retry, err := reopened.Apply(context.Background(), transaction.ID, 3)
	if err != nil || !retry.Replayed || !retry.OK {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	if _, err := os.Stat(filepath.Join(service.dir, filepath.FromSlash(intentName))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery intent remains: %v", err)
	}
}

func TestCommitRecoveryAcrossEveryPublishedBoundary(t *testing.T) {
	points := []string{
		"atomic.after_rename:" + intentName,
		"atomic.after_rename:" + DocumentName,
		"atomic.after_rename:" + stateName,
		"intent.after_remove",
	}
	for _, point := range points {
		t.Run(strings.ReplaceAll(point, "/", "_"), func(t *testing.T) {
			service := createFixtureService(t, Options{})
			transaction := moveTransaction("tx-boundary", "key-boundary", 3, 2)
			if result, err := service.Plan(context.Background(), transaction); err != nil || !result.OK {
				t.Fatalf("plan = %+v, %v", result, err)
			}
			fired := false
			service.fault = func(actual string) error {
				if !fired && actual == point {
					fired = true
					return errors.New("injected boundary failure")
				}
				return nil
			}
			if _, err := service.Apply(context.Background(), transaction.ID, 3); !errors.Is(err, ErrCommitUnknown) {
				t.Fatalf("Apply error at %s = %v", point, err)
			}
			service.fault = nil
			reopened, err := New(service.dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			retry, err := reopened.Apply(context.Background(), transaction.ID, 3)
			if err != nil || !retry.OK || !retry.Replayed || *retry.NewRevision != 4 {
				t.Fatalf("retry at %s = %+v, %v", point, retry, err)
			}
			status, err := reopened.Status(context.Background())
			if err != nil || status.Revision != 4 || status.HistoryCount != 1 {
				t.Fatalf("status at %s = %+v, %v", point, status, err)
			}
		})
	}
}

func TestPlanUnknownOutcomeCanBeRetriedExactly(t *testing.T) {
	service := createFixtureService(t, Options{})
	transaction := moveTransaction("tx-plan-unknown", "key-plan-unknown", 3, 2)
	fired := false
	service.fault = func(point string) error {
		if !fired && point == "atomic.after_rename:"+stateName {
			fired = true
			return errors.New("directory sync failed")
		}
		return nil
	}
	if _, err := service.Plan(context.Background(), transaction); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("plan error = %v", err)
	}
	service.fault = nil
	retry, err := service.Plan(context.Background(), transaction)
	if err != nil || !retry.OK || !retry.Replayed || retry.State != "planned" {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
}

func TestConcurrentApplyCommitsExactlyOneBaseRevision(t *testing.T) {
	service := createFixtureService(t, Options{})
	first := moveTransaction("tx-a", "key-a", 3, 1)
	second := moveTransaction("tx-b", "key-b", 3, 2)
	for _, transaction := range []videoedit.Transaction{first, second} {
		if result, err := service.Plan(context.Background(), transaction); err != nil || !result.OK {
			t.Fatalf("plan = %+v, %v", result, err)
		}
	}
	other, err := New(service.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan MutationResult, 2)
	errorsCh := make(chan error, 2)
	var wait sync.WaitGroup
	for index, candidate := range []struct {
		service *Service
		id      string
	}{{service, first.ID}, {other, second.ID}} {
		_ = index
		wait.Add(1)
		go func(candidate struct {
			service *Service
			id      string
		}) {
			defer wait.Done()
			<-start
			result, err := candidate.service.Apply(context.Background(), candidate.id, 3)
			results <- result
			errorsCh <- err
		}(candidate)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	successes := 0
	for result := range results {
		if result.OK {
			successes++
		} else if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != CodeRevisionConflict {
			t.Fatalf("unexpected rejected result: %+v", result)
		}
	}
	if successes != 1 {
		t.Fatalf("successful commits = %d", successes)
	}
	status, err := service.Status(context.Background())
	if err != nil || status.Revision != 4 || status.HistoryCount != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestLockWaitHonorsContext(t *testing.T) {
	service := createFixtureService(t, Options{})
	lock, err := tryFileLock(filepath.Join(service.dir, filepath.FromSlash(lockName)))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := service.Status(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Status error = %v", err)
	}
}

func TestLimitsUseExactNAndNPlusOneBoundaries(t *testing.T) {
	limits := DefaultLimits
	limits.MaxOperations = 1
	limits.MaxPlans = 1
	limits.MaxHistory = 1
	service := createFixtureService(t, Options{Limits: limits})
	valid := moveTransaction("tx-one", "key-one", 3, 1)
	if result, err := service.Plan(context.Background(), valid); err != nil || !result.OK {
		t.Fatalf("N plan = %+v, %v", result, err)
	}
	tooManyOperations := valid
	tooManyOperations.ID = "tx-two-ops"
	tooManyOperations.IdempotencyKey = "key-two-ops"
	tooManyOperations.Operations = append(tooManyOperations.Operations, videoedit.Operation{
		ID: "move-again", Kind: videoedit.OperationMove, ClipID: "clip-1", At: timePointer(2),
	})
	if result, err := service.Plan(context.Background(), tooManyOperations); err != nil || result.OK || result.Diagnostics[0].Code != CodeResourceLimit {
		t.Fatalf("N+1 operation plan = %+v, %v", result, err)
	}
	secondPlan := moveTransaction("tx-second-plan", "key-second-plan", 3, 2)
	if result, err := service.Plan(context.Background(), secondPlan); err != nil || result.OK || result.Diagnostics[0].Code != CodeResourceLimit {
		t.Fatalf("N+1 plan = %+v, %v", result, err)
	}
	if result, err := service.Apply(context.Background(), valid.ID, 3); err != nil || !result.OK {
		t.Fatalf("N history apply = %+v, %v", result, err)
	}
	if _, err := service.History(context.Background(), 0, MaxHistoryPage+1); !errors.Is(err, ErrLimit) {
		t.Fatalf("history page error = %v", err)
	}
}

func TestStrictCanonicalAndSymlinkFilesFailClosed(t *testing.T) {
	service := createFixtureService(t, Options{})
	documentPath := filepath.Join(service.dir, DocumentName)
	raw, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(documentPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Inspect(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("noncanonical document error = %v", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	for _, target := range []string{DocumentName, stateName} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			service := createFixtureService(t, Options{})
			path := filepath.Join(service.dir, filepath.FromSlash(target))
			backup := path + ".real"
			if err := os.Rename(path, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(backup, path); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Inspect(context.Background()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("symlink error = %v", err)
			}
		})
	}
}

func TestInitializationMarkerDetectsLostState(t *testing.T) {
	service := createFixtureService(t, Options{})
	if err := os.Remove(filepath.Join(service.dir, filepath.FromSlash(stateName))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Inspect(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lost state error = %v", err)
	}
}

func TestStateFingerprintDetectsCanonicalTampering(t *testing.T) {
	service := createFixtureService(t, Options{})
	path := filepath.Join(service.dir, filepath.FromSlash(stateName))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state diskState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.Sequence++
	tampered, err := videoedit.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Inspect(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered state error = %v", err)
	}
}

func TestDurableIntentCanRecoverAMissingStateFile(t *testing.T) {
	service := createFixtureService(t, Options{})
	transaction := moveTransaction("tx-missing-state", "key-missing-state", 3, 2)
	if result, err := service.Plan(context.Background(), transaction); err != nil || !result.OK {
		t.Fatalf("plan = %+v, %v", result, err)
	}
	fired := false
	service.fault = func(point string) error {
		if !fired && point == "atomic.after_rename:"+DocumentName {
			fired = true
			return errors.New("stop after document publication")
		}
		return nil
	}
	if _, err := service.Apply(context.Background(), transaction.ID, 3); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("apply error = %v", err)
	}
	service.fault = nil
	if err := os.Remove(filepath.Join(service.dir, filepath.FromSlash(stateName))); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(service.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := reopened.Status(context.Background())
	if err != nil || status.Revision != 4 || status.HistoryCount != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestCreateIsIdempotentOnlyForExactDocument(t *testing.T) {
	dir := t.TempDir()
	document := fixtureDocument()
	first, err := Create(context.Background(), dir, document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Create(context.Background(), dir, document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.dir != second.dir {
		t.Fatalf("dirs differ: %q %q", first.dir, second.dir)
	}
	different := document
	different.ID = "other"
	if _, err := Create(context.Background(), dir, different, Options{}); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("different Create error = %v", err)
	}
}

func TestCurrentV1RawNumberSpellingRemainsSupported(t *testing.T) {
	document := fixtureDocument()
	document.Metadata = map[string]json.RawMessage{"legacy_fraction": json.RawMessage(`1.5`)}
	service, err := Create(context.Background(), t.TempDir(), document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	transaction := moveTransaction("tx-fraction", "key-fraction", document.Revision, 2)
	plan, err := service.Plan(context.Background(), transaction)
	if err != nil || !plan.OK {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	commit, err := service.Apply(context.Background(), transaction.ID, document.Revision)
	if err != nil || !commit.OK {
		t.Fatalf("commit = %+v, %v", commit, err)
	}
	snapshot, err := service.Inspect(context.Background())
	if err != nil || string(snapshot.Document.Metadata["legacy_fraction"]) != "1.5" {
		t.Fatalf("snapshot metadata = %s, %v", snapshot.Document.Metadata["legacy_fraction"], err)
	}
}

func createFixtureService(t *testing.T, options Options) *Service {
	t.Helper()
	service, err := Create(context.Background(), t.TempDir(), fixtureDocument(), options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func fixtureDocument() videoedit.Document {
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	ten := videoedit.RationalTime{Value: 10, Rate: 1}
	twenty := videoedit.RationalTime{Value: 20, Rate: 1}
	return videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion,
		ID:            "doc-1",
		Revision:      3,
		Assets: []videoedit.Asset{{
			ID: "asset-1", Kind: videoedit.AssetKindVideo, URI: "footage.mp4", Duration: &twenty,
		}},
		Timelines: []videoedit.Timeline{{
			ID: "timeline-1",
			Tracks: []videoedit.Track{{
				ID: "video-1", Kind: videoedit.TrackKindVideo,
				Clips: []videoedit.Clip{{
					ID: "clip-1", AssetID: "asset-1",
					TimelineRange: videoedit.TimeRange{Start: zero, Duration: ten},
					SourceRange:   videoedit.TimeRange{Start: zero, Duration: ten},
				}},
			}},
		}},
	}
}

func moveTransaction(id, key string, revision videoedit.Revision, at int64) videoedit.Transaction {
	return videoedit.Transaction{
		ID: id, IdempotencyKey: key, ExpectedRevision: revision,
		Operations: []videoedit.Operation{{
			ID: "move-" + id, Kind: videoedit.OperationMove, ClipID: "clip-1", At: timePointer(at),
		}},
	}
}

func timePointer(value int64) *videoedit.RationalTime {
	time := videoedit.RationalTime{Value: value, Rate: 1}
	return &time
}
