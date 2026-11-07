package cas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
)

func TestPublishDuplicateIsNoReplaceAndPartialScratchIsInvisible(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, 'a')
	partial, err := os.MkdirTemp(store.scratchRoot, ".partial-crash-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, payloadName), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reader, err := store.Open(context.Background(), value, "reader-before-publish"); reader != nil || !errors.Is(err, ErrMiss) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("partial lookup=(%v,%v), want clean miss", reader, err)
	}

	payload := []byte("immutable rendered frame group")
	first := publishBytes(t, store, value, payload)
	if first.Disposition != Published {
		t.Fatalf("first disposition = %q", first.Disposition)
	}
	second := publishBytes(t, store, value, payload)
	if second.Disposition != AlreadyPresent || second.Entry != first.Entry {
		t.Fatalf("duplicate = %+v, first=%+v", second, first)
	}
	reader, err := store.Open(context.Background(), value, "reader-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q", got)
	}
	_, _, published := entryPaths(t, store, value)
	for _, name := range []string{payloadName, manifestName, completeName} {
		if _, err := os.Lstat(filepath.Join(published, name)); err != nil {
			t.Fatalf("published %s: %v", name, err)
		}
	}
	// The manually abandoned scratch remains irrelevant; Store-owned scratch from both publishes
	// was removed or renamed.
	entries, err := os.ReadDir(store.scratchRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(partial) {
		t.Fatalf("scratch namespace = %+v, %v", entries, err)
	}
}

func TestSameStageKeyDifferentPayloadIsCollision(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, 'b')
	original := []byte("first")
	publishBytes(t, store, value, original)
	changed := []byte("second")
	hash, size := payloadIdentity(changed)
	if _, err := store.Publish(context.Background(), PublishRequest{Key: value, Payload: bytes.NewReader(changed), ExpectedHash: hash, ExpectedSize: size}); !errors.Is(err, ErrCollision) {
		t.Fatalf("collision error = %v", err)
	}
	reader, err := store.Open(context.Background(), value, "collision-check")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); !bytes.Equal(got, original) {
		t.Fatalf("collision replaced payload: %q", got)
	}
	assertScratchEmpty(t, store)
}

func TestCorruptEntryIsAtomicallyQuarantinedAndSignalsRecompute(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, 'c')
	payload := []byte("verified original")
	publishBytes(t, store, value, payload)
	_, entryPath, published := entryPaths(t, store, value)
	payloadPath := filepath.Join(published, payloadName)
	if err := os.Chmod(payloadPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, []byte("corrupt mutation"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(context.Background(), value, "corruption-reader")
	if reader != nil || !errors.Is(err, ErrMiss) || !errors.Is(err, ErrCorrupt) || !errors.Is(err, ErrRecomputeRequired) {
		t.Fatalf("corrupt lookup=(%v,%v)", reader, err)
	}
	var lookup *LookupError
	if !errors.As(err, &lookup) || lookup.QuarantinePath == "" {
		t.Fatalf("lookup error has no quarantine path: %#v", err)
	}
	if _, statErr := os.Lstat(entryPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("corrupt entry still reader-visible: %v", statErr)
	}
	if _, statErr := os.Lstat(lookup.QuarantinePath); statErr != nil {
		t.Fatalf("quarantine missing: %v", statErr)
	}
	leases, leaseErr := os.ReadDir(filepath.Join(lookup.QuarantinePath, leasesName))
	if leaseErr != nil || len(leases) != 0 {
		t.Fatalf("corrupt lookup leaked reader lease files=%v error=%v", leases, leaseErr)
	}
	// A recomputation can republish the same immutable key without touching the quarantined copy.
	result := publishBytes(t, store, value, payload)
	if result.Disposition != Published {
		t.Fatalf("recompute disposition = %q", result.Disposition)
	}
}

func TestConcurrentDuplicatePublishAndRead(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, 'd')
	payload := bytes.Repeat([]byte("concurrent"), 8192)
	hash, size := payloadIdentity(payload)
	const publishers = 24
	results := make(chan PublishResult, publishers)
	errorsOut := make(chan error, publishers)
	var wait sync.WaitGroup
	for index := 0; index < publishers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Publish(context.Background(), PublishRequest{
				Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
			})
			if err != nil {
				errorsOut <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		t.Errorf("concurrent publish: %v", err)
	}
	published := 0
	already := 0
	for result := range results {
		switch result.Disposition {
		case Published:
			published++
		case AlreadyPresent:
			already++
		default:
			t.Errorf("unexpected disposition %q", result.Disposition)
		}
	}
	if published != 1 || already != publishers-1 {
		t.Fatalf("published=%d already=%d", published, already)
	}
	reader, err := store.Open(context.Background(), value, "after-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("concurrent payload drift")
	}
	assertScratchEmpty(t, store)
}

func TestQuarantineReservationNeverReplacesExistingQuarantine(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, '0')
	publishBytes(t, store, value, []byte("quarantine collision"))
	_, entryPath, _ := entryPaths(t, store, value)
	firstNonce := strings.Repeat("1", 32)
	secondNonce := strings.Repeat("2", 32)
	existingSlot := filepath.Join(store.quarantineRoot,
		strings.TrimPrefix(value.Digest, "sha256:")+"."+fmt.Sprintf("%d", clock.Now().UnixNano())+"."+firstNonce)
	if err := os.Mkdir(existingSlot, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existingSlot, "keep")
	if err := os.WriteFile(marker, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonces := []string{firstNonce, secondNonce}
	store.nonce = func() (string, error) {
		result := nonces[0]
		nonces = nonces[1:]
		return result, nil
	}
	store.namespaceMu.Lock()
	quarantined, err := store.quarantineLocked(entryPath, value.Digest)
	store.namespaceMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if quarantined == filepath.Join(existingSlot, "entry") {
		t.Fatal("quarantine reused an existing slot")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "do not replace" {
		t.Fatalf("existing quarantine changed: %q %v", raw, err)
	}
	if _, err := os.Lstat(quarantined); err != nil {
		t.Fatalf("new quarantine missing: %v", err)
	}
}

func TestLegacyPublishReservationRemainsFailClosedUntilManualRecovery(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	store := newTestStore(t, nil, clock)
	value := testKey(t, '1')
	shard, entryPath, publishedPath := entryPaths(t, store, value)
	if err := os.MkdirAll(entryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	active := reservation{
		SchemaVersion: reservationSchemaVersion, Profile: reservationProfileV1, StageKey: value,
		Nonce: strings.Repeat("a", 32), CreatedUnixNano: clock.Now().UnixNano(),
		ExpiresUnixNano: clock.Now().Add(store.reservationTTL).UnixNano(),
	}
	raw, err := json.Marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryPath, reservationName), raw, 0o400); err != nil {
		t.Fatal(err)
	}
	if reader, err := store.Open(context.Background(), value, "reservation-reader"); reader != nil || !errors.Is(err, ErrMiss) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("active reservation lookup=(%v,%v)", reader, err)
	}
	payload := []byte("recovered reservation")
	hash, size := payloadIdentity(payload)
	if result, err := store.Publish(context.Background(), PublishRequest{Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size}); result != (PublishResult{}) || !errors.Is(err, ErrPublicationBusy) {
		t.Fatalf("active reservation publish=(%+v,%v)", result, err)
	}
	assertScratchEmpty(t, store)
	if _, err := os.Lstat(publishedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active reservation exposed published directory: %v", err)
	}
	clock.Advance(store.reservationTTL + time.Nanosecond)
	if result, err := store.Publish(context.Background(), PublishRequest{Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size}); result != (PublishResult{}) || !errors.Is(err, ErrPublicationBusy) {
		t.Fatalf("expired legacy reservation was reclaimed=(%+v,%v)", result, err)
	}
	if _, err := os.Lstat(filepath.Join(entryPath, reservationName)); err != nil {
		t.Fatalf("legacy reservation owner was removed: %v", err)
	}
	if err := os.RemoveAll(entryPath); err != nil {
		t.Fatal(err)
	}
	if err := store.fs.SyncDir(shard); err != nil {
		t.Fatal(err)
	}
	result, err := store.Publish(context.Background(), PublishRequest{Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size})
	if err != nil || result.Disposition != Published {
		t.Fatalf("manual legacy reservation recovery=(%+v,%v)", result, err)
	}
	if _, err := os.Lstat(filepath.Join(shard, filepath.Base(entryPath), publishedName)); err != nil {
		t.Fatalf("recovered publication missing: %v", err)
	}
}

func TestFullStageKeyPathIdentitySeparatesSameDigestVariants(t *testing.T) {
	store := newTestStore(t, nil, nil)
	base := testKey(t, '1')
	namespaceVariant := base
	namespaceVariant.Namespace = "cutly.cache.render-chunk-alt.v1"
	kindVariant := base
	kindVariant.ArtifactKind = "cutly.cache-artifact.rgba-frame-group-alt.v1"
	keys := []cachekey.Key{base, namespaceVariant, kindVariant}
	payloads := [][]byte{[]byte("base"), []byte("namespace"), []byte("kind")}
	paths := make(map[string]struct{}, len(keys))
	for index, value := range keys {
		_, entryPath, _ := entryPaths(t, store, value)
		if _, duplicate := paths[entryPath]; duplicate {
			t.Fatalf("full StageKey identity aliased entry path %q", entryPath)
		}
		paths[entryPath] = struct{}{}
		if result := publishBytes(t, store, value, payloads[index]); result.Disposition != Published {
			t.Fatalf("variant %d disposition=%q", index, result.Disposition)
		}
	}
	for index, value := range keys {
		reader, err := store.Open(context.Background(), value, fmt.Sprintf("variant-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		if got := readAllAndClose(t, reader); !bytes.Equal(got, payloads[index]) {
			t.Fatalf("variant %d payload=%q", index, got)
		}
	}
	if entries, err := os.ReadDir(store.quarantineRoot); err != nil || len(entries) != 0 {
		t.Fatalf("StageKey variants triggered quarantine: entries=%v err=%v", entries, err)
	}
}

func TestCrossStoreConcurrentWholeEntryPublishConvergesWithoutReplacement(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	storeA := newTestStore(t, filesystem, clock)
	storeB, err := New(storeA.root, Options{
		FileSystem: filesystem, Clock: clock, LeaseTTL: 10 * time.Second, ReservationTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := testKey(t, '2')
	payload := bytes.Repeat([]byte("cross-store"), 1024)
	hash, size := payloadIdentity(payload)
	var arrivals atomic.Int32
	ready := make(chan struct{})
	filesystem.failRenameMatch = func(_, newPath string) error {
		if !canonicalHexName(filepath.Base(newPath), 64) {
			return nil
		}
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		<-ready
		return nil
	}
	type outcome struct {
		result PublishResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, store := range []*Store{storeA, storeB} {
		go func(store *Store) {
			result, publishErr := store.Publish(context.Background(), PublishRequest{
				Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
			})
			outcomes <- outcome{result: result, err: publishErr}
		}(store)
	}
	published, already := 0, 0
	for range 2 {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("cross-store publish: result=%+v err=%v", outcome.result, outcome.err)
		}
		switch outcome.result.Disposition {
		case Published:
			published++
		case AlreadyPresent:
			already++
		default:
			t.Fatalf("unexpected disposition %q", outcome.result.Disposition)
		}
	}
	filesystem.mu.Lock()
	filesystem.failRenameMatch = nil
	filesystem.mu.Unlock()
	if published != 1 || already != 1 {
		t.Fatalf("published=%d already=%d", published, already)
	}
	reader, err := storeA.Open(context.Background(), value, "cross-store-reader")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); !bytes.Equal(got, payload) {
		t.Fatalf("cross-store payload=%q", got)
	}
	assertScratchEmpty(t, storeA)
}

func TestCrossStoreConcurrentDifferentPayloadNeverReplacesWinner(t *testing.T) {
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
	payloads := [][]byte{[]byte("first contender"), []byte("second contender")}
	var arrivals atomic.Int32
	ready := make(chan struct{})
	filesystem.failRenameMatch = func(_, newPath string) error {
		if !canonicalHexName(filepath.Base(newPath), 64) {
			return nil
		}
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		<-ready
		return nil
	}
	type outcome struct {
		index  int
		result PublishResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for index, store := range []*Store{storeA, storeB} {
		go func(index int, store *Store) {
			hash, size := payloadIdentity(payloads[index])
			result, publishErr := store.Publish(context.Background(), PublishRequest{
				Key: value, Payload: bytes.NewReader(payloads[index]), ExpectedHash: hash, ExpectedSize: size,
			})
			outcomes <- outcome{index: index, result: result, err: publishErr}
		}(index, store)
	}
	winner := -1
	collisions := 0
	for range 2 {
		outcome := <-outcomes
		if outcome.err == nil && outcome.result.Disposition == Published {
			winner = outcome.index
			continue
		}
		if outcome.result == (PublishResult{}) && errors.Is(outcome.err, ErrCollision) {
			collisions++
			continue
		}
		t.Fatalf("unexpected contender %d outcome=(%+v,%v)", outcome.index, outcome.result, outcome.err)
	}
	filesystem.mu.Lock()
	filesystem.failRenameMatch = nil
	filesystem.mu.Unlock()
	if winner < 0 || collisions != 1 {
		t.Fatalf("winner=%d collisions=%d", winner, collisions)
	}
	reader, err := storeA.Open(context.Background(), value, "collision-winner-reader")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); !bytes.Equal(got, payloads[winner]) {
		t.Fatalf("winner was replaced: got=%q want=%q", got, payloads[winner])
	}
	assertScratchEmpty(t, storeA)
}
