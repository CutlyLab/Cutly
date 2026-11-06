package cas

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeartbeatKeepsReaderActiveAcrossQuotaGC(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, '8')
	payload := []byte("lease protected payload")
	entry := publishBytes(t, store, value, payload).Entry
	reader, err := store.Open(context.Background(), value, "long-render-reader")
	if err != nil {
		t.Fatal(err)
	}
	initial := reader.LeaseInfo()
	clock.Advance(8 * time.Second)
	if err := reader.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshed := reader.LeaseInfo()
	if refreshed.Generation != initial.Generation+1 || refreshed.ExpiresUnixNano <= initial.ExpiresUnixNano {
		t.Fatalf("heartbeat did not advance lease: before=%+v after=%+v", initial, refreshed)
	}
	clock.Advance(5 * time.Second) // Initial lease expired; heartbeat lease remains active.
	report, err := store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if !errors.Is(err, ErrQuotaUnsatisfied) || report.AfterBytes != entry.ChargeBytes ||
		len(report.SkippedActive) != 1 || report.SkippedActive[0] != value.Digest || len(report.DeletedDigests) != 0 {
		t.Fatalf("active-lease GC report=%+v error=%v", report, err)
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("leased read=%q error=%v", got, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if err != nil || len(report.DeletedDigests) != 1 || report.DeletedDigests[0] != value.Digest || report.AfterBytes != 0 {
		t.Fatalf("released GC report=%+v error=%v", report, err)
	}
}

func TestExpiredLeaseDoesNotPinEntry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, '9')
	payload := []byte("expired lease payload")
	publishBytes(t, store, value, payload)
	reader, err := store.Open(context.Background(), value, "abandoned-reader")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(11 * time.Second)
	if err := reader.Heartbeat(context.Background()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease was resurrected: %v", err)
	}
	report, err := store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if err != nil || report.ExpiredLeaseCount != 1 || len(report.DeletedDigests) != 1 || report.DeletedDigests[0] != value.Digest {
		t.Fatalf("expired GC report=%+v error=%v", report, err)
	}
	// The already-open descriptor remains readable on the supported local Unix profile, but the
	// lease cannot be renewed and no future lookup can observe the removed entry.
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	if readErr != nil || string(got) != string(payload) {
		t.Fatalf("open descriptor after GC=%q error=%v", got, readErr)
	}
	if err := reader.Heartbeat(context.Background()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("heartbeat after expiration/GC = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := store.Open(context.Background(), value, "future-reader"); next != nil || !errors.Is(err, ErrMiss) {
		t.Fatalf("post-GC lookup=(%v,%v)", next, err)
	}
}

func TestQuotaGCUsesDeterministicCreationThenDigestOrder(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	keys := []byte{'c', 'a', 'b'}
	entries := make([]Entry, len(keys))
	for index, marker := range keys {
		entries[index] = publishBytes(t, store, testKey(t, marker), []byte("same-size-payload")).Entry
		clock.Advance(time.Second)
	}
	total := entries[0].ChargeBytes + entries[1].ChargeBytes + entries[2].ChargeBytes
	report, err := store.GC(context.Background(), GCPolicy{MaxBytes: total - 1, TargetBytes: entries[2].ChargeBytes})
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted := []string{testKey(t, 'c').Digest, testKey(t, 'a').Digest}
	if len(report.DeletedDigests) != len(wantDeleted) {
		t.Fatalf("deleted=%v", report.DeletedDigests)
	}
	for index := range wantDeleted {
		if report.DeletedDigests[index] != wantDeleted[index] {
			t.Fatalf("deleted order=%v want=%v", report.DeletedDigests, wantDeleted)
		}
	}
	if report.BeforeBytes != total || report.AfterBytes != entries[2].ChargeBytes {
		t.Fatalf("quota bytes before=%d after=%d", report.BeforeBytes, report.AfterBytes)
	}
	for _, marker := range keys[:2] {
		if reader, err := store.Open(context.Background(), testKey(t, marker), "deleted-check"); reader != nil || !errors.Is(err, ErrMiss) {
			t.Fatalf("deleted key %c lookup=(%v,%v)", marker, reader, err)
		}
	}
	reader, err := store.Open(context.Background(), testKey(t, 'b'), "retained-check")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaBoundaryExactNAndNPlusOne(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, '2')
	entry := publishBytes(t, store, value, []byte("quota boundary")).Entry
	report, err := store.GC(context.Background(), GCPolicy{MaxBytes: entry.ChargeBytes, TargetBytes: 0})
	if err != nil || report.AfterBytes != entry.ChargeBytes || len(report.DeletedDigests) != 0 {
		t.Fatalf("exact-N report=%+v error=%v", report, err)
	}
	report, err = store.GC(context.Background(), GCPolicy{MaxBytes: entry.ChargeBytes - 1, TargetBytes: 0})
	if err != nil || report.AfterBytes != 0 || len(report.DeletedDigests) != 1 {
		t.Fatalf("N+1 report=%+v error=%v", report, err)
	}
}

func TestConcurrentReaderLeasesBlockGCWithoutTOCTOUDelete(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, '7')
	publishBytes(t, store, value, []byte("multi-reader"))
	const count = 16
	readers := make([]*Reader, count)
	var wait sync.WaitGroup
	errorsOut := make(chan error, count)
	for index := range readers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			reader, err := store.Open(context.Background(), value, "reader-group")
			if err == nil {
				readers[index] = reader
			}
			errorsOut <- err
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if !errors.Is(err, ErrQuotaUnsatisfied) || len(report.SkippedActive) != 1 || len(report.DeletedDigests) != 0 {
		t.Fatalf("leased GC report=%+v error=%v", report, err)
	}
	for _, reader := range readers {
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	report, err = store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if err != nil || len(report.DeletedDigests) != 1 {
		t.Fatalf("post-release GC report=%+v error=%v", report, err)
	}
}

func TestCrossStoreGCClaimClosesLeaseAcquireTOCTOU(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	storeA := newTestStore(t, filesystem, clock)
	storeB, err := New(storeA.root, Options{
		FileSystem: filesystem, Clock: clock, LeaseTTL: 10 * time.Second, ReservationTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := testKey(t, '3')
	publishBytes(t, storeA, value, []byte("claim race"))
	claimVisible := make(chan struct{})
	allowGC := make(chan struct{})
	var blocked atomic.Bool
	filesystem.failDirSyncMatch = func(path string) error {
		if filepath.Base(path) == gcClaimName && blocked.CompareAndSwap(false, true) {
			close(claimVisible)
			<-allowGC
		}
		return nil
	}
	result := make(chan struct {
		report GCReport
		err    error
	}, 1)
	go func() {
		report, gcErr := storeB.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
		result <- struct {
			report GCReport
			err    error
		}{report: report, err: gcErr}
	}()
	<-claimVisible
	reader, openErr := storeA.Open(context.Background(), value, "racing-reader")
	if reader != nil || !errors.Is(openErr, ErrMiss) || !errors.Is(openErr, ErrLeaseLost) {
		t.Fatalf("lookup after GC claim=(%v,%v)", reader, openErr)
	}
	close(allowGC)
	gcResult := <-result
	filesystem.mu.Lock()
	filesystem.failDirSyncMatch = nil
	filesystem.mu.Unlock()
	if gcResult.err != nil || len(gcResult.report.DeletedDigests) != 1 || gcResult.report.DeletedDigests[0] != value.Digest {
		t.Fatalf("claimed GC report=%+v error=%v", gcResult.report, gcResult.err)
	}
	_, entryPath, _ := entryPaths(t, storeA, value)
	if _, err := filesystem.Lstat(filepath.Join(entryPath, leasesName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("racing reader leaked lease namespace: %v", err)
	}
}

func TestCrossStoreGCClaimRemainsExclusivePastNominalExpiry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	storeA := newTestStore(t, filesystem, clock)
	storeB, err := New(storeA.root, Options{
		FileSystem: filesystem, Clock: clock, LeaseTTL: 10 * time.Second, ReservationTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := testKey(t, '4')
	publishBytes(t, storeA, value, []byte("expired claim race"))
	claimVisible := make(chan struct{})
	allowGC := make(chan struct{})
	var blocked atomic.Bool
	filesystem.failDirSyncMatch = func(path string) error {
		if filepath.Base(path) == gcClaimName && blocked.CompareAndSwap(false, true) {
			close(claimVisible)
			<-allowGC
		}
		return nil
	}
	result := make(chan struct {
		report GCReport
		err    error
	}, 1)
	go func() {
		report, gcErr := storeB.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
		result <- struct {
			report GCReport
			err    error
		}{report: report, err: gcErr}
	}()
	<-claimVisible
	clock.Advance(6 * time.Second)
	reader, openErr := storeA.Open(context.Background(), value, "post-expiry-reader")
	if reader != nil || !errors.Is(openErr, ErrMiss) || !errors.Is(openErr, ErrLeaseLost) {
		t.Fatalf("expired acquisition claim admitted reader=(%v,%v)", reader, openErr)
	}
	close(allowGC)
	gcResult := <-result
	filesystem.mu.Lock()
	filesystem.failDirSyncMatch = nil
	filesystem.mu.Unlock()
	if gcResult.err != nil || len(gcResult.report.DeletedDigests) != 1 || gcResult.report.DeletedDigests[0] != value.Digest {
		t.Fatalf("expired-claim GC report=%+v error=%v", gcResult.report, gcResult.err)
	}
	if reader, err := storeA.Open(context.Background(), value, "post-gc-reader"); reader != nil || !errors.Is(err, ErrMiss) {
		t.Fatalf("deleted lookup=(%v,%v)", reader, err)
	}
}

func TestDurableGCCommitPhaseClosesExpiryToRenameWindow(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	store := newTestStore(t, filesystem, clock)
	value := testKey(t, '5')
	publishBytes(t, store, value, []byte("claim commit margin"))
	readerStore, err := New(store.root, Options{
		FileSystem: filesystem, Clock: clock, LeaseTTL: 10 * time.Second, ReservationTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	trashPrepared := make(chan struct{})
	allowDeleteCheck := make(chan struct{})
	var blocked atomic.Bool
	filesystem.failDirSyncMatch = func(path string) error {
		if path == store.trashRoot && blocked.CompareAndSwap(false, true) {
			close(trashPrepared)
			<-allowDeleteCheck
		}
		return nil
	}
	result := make(chan struct {
		report GCReport
		err    error
	}, 1)
	go func() {
		report, gcErr := store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
		result <- struct {
			report GCReport
			err    error
		}{report: report, err: gcErr}
	}()
	<-trashPrepared
	clock.Advance(store.reservationTTL + time.Second)
	reader, openErr := readerStore.Open(context.Background(), value, "commit-phase-reader")
	if reader != nil || !errors.Is(openErr, ErrMiss) || !errors.Is(openErr, ErrLeaseLost) {
		t.Fatalf("commit-phase claim admitted reader=(%v,%v)", reader, openErr)
	}
	close(allowDeleteCheck)
	gcResult := <-result
	filesystem.mu.Lock()
	filesystem.failDirSyncMatch = nil
	filesystem.mu.Unlock()
	if gcResult.err != nil || len(gcResult.report.DeletedDigests) != 1 || gcResult.report.DeletedDigests[0] != value.Digest {
		t.Fatalf("durable commit claim report=%+v error=%v", gcResult.report, gcResult.err)
	}
	if reader, err := readerStore.Open(context.Background(), value, "post-commit-gc-reader"); reader != nil || !errors.Is(err, ErrMiss) {
		t.Fatalf("post-GC lookup=(%v,%v)", reader, err)
	}
}

func TestCommittedGCClaimCrashRemainsFailClosedAfterReopenAndExpiry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, '6')
	publishBytes(t, store, value, []byte("commit crash"))

	store.namespaceMu.Lock()
	candidates, _, err := store.scanCandidatesLocked(context.Background())
	if err != nil || len(candidates) != 1 {
		store.namespaceMu.Unlock()
		t.Fatalf("scan candidates=%d err=%v", len(candidates), err)
	}
	nonce, err := store.createGCClaimLocked(candidates[0])
	if err == nil {
		err = store.commitGCClaimLocked(candidates[0], nonce)
	}
	store.namespaceMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(store.reservationTTL + time.Hour)

	reopened, err := New(store.root, Options{
		Clock: clock, LeaseTTL: store.leaseTTL, ReservationTTL: store.reservationTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reader, openErr := reopened.Open(context.Background(), value, "after-commit-crash"); reader != nil || !errors.Is(openErr, ErrMiss) || !errors.Is(openErr, ErrLeaseLost) {
		t.Fatalf("committed crash claim admitted reader=(%v,%v)", reader, openErr)
	}
	report, gcErr := reopened.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if !errors.Is(gcErr, ErrQuotaUnsatisfied) || len(report.DeletedDigests) != 0 || len(report.SkippedActive) != 1 {
		t.Fatalf("committed crash GC report=%+v err=%v", report, gcErr)
	}
	_, entryPath, _ := entryPaths(t, reopened, value)
	if _, err := reopened.fs.Lstat(entryPath); err != nil {
		t.Fatalf("committed crash claim did not fail closed: %v", err)
	}
}

func TestLegacyGCClaimIsNeverAgeReclaimed(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, 'a')
	publishBytes(t, store, value, []byte("legacy claim"))
	_, entryPath, _ := entryPaths(t, store, value)
	claimPath := filepath.Join(entryPath, gcClaimName)
	if err := store.fs.Mkdir(claimPath, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := gcClaim{
		SchemaVersion: gcClaimSchemaVersion, Profile: gcClaimProfileV1, StageKey: value,
		Nonce: repeatedNonce('a'), CreatedUnixNano: clock.Now().UnixNano(),
		ExpiresUnixNano: clock.Now().Add(store.reservationTTL).UnixNano(),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDurableFile(store.fs, filepath.Join(claimPath, gcClaimRecordName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(store.fs.SyncDir(claimPath), store.fs.SyncDir(entryPath)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(store.reservationTTL + time.Hour)
	if reader, openErr := store.Open(context.Background(), value, "legacy-claim-reader"); reader != nil || !errors.Is(openErr, ErrMiss) || !errors.Is(openErr, ErrLeaseLost) {
		t.Fatalf("legacy claim admitted reader=(%v,%v)", reader, openErr)
	}
	report, gcErr := store.GC(context.Background(), GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if !errors.Is(gcErr, ErrPublicationBusy) || len(report.DeletedDigests) != 0 {
		t.Fatalf("legacy claim GC report=%+v err=%v", report, gcErr)
	}
	if _, err := store.fs.Lstat(entryPath); err != nil {
		t.Fatalf("legacy claim entry was reclaimed: %v", err)
	}
}

func TestGCCandidateGenerationRejectsABAReplacement(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, 'b')
	publishBytes(t, store, value, []byte("old generation"))

	store.namespaceMu.Lock()
	candidates, _, err := store.scanCandidatesLocked(context.Background())
	store.namespaceMu.Unlock()
	if err != nil || len(candidates) != 1 {
		t.Fatalf("scan candidates=%d err=%v", len(candidates), err)
	}
	old := candidates[0]
	store.namespaceMu.Lock()
	_, err = store.quarantineLocked(old.entryPath, value.Digest)
	store.namespaceMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Nanosecond)
	newPayload := []byte("new generation")
	publishBytes(t, store, value, newPayload)

	store.namespaceMu.Lock()
	nonce, claimErr := store.createGCClaimLocked(old)
	store.namespaceMu.Unlock()
	if nonce != "" || !errors.Is(claimErr, ErrPublicationBusy) {
		t.Fatalf("stale candidate claim nonce=%q err=%v", nonce, claimErr)
	}
	reader, err := store.Open(context.Background(), value, "aba-reader")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); string(got) != string(newPayload) {
		t.Fatalf("ABA replacement payload=%q", got)
	}
}

func repeatedNonce(marker byte) string {
	return strings.Repeat(string(marker), 32)
}
