package cas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func newTestStore(t *testing.T, filesystem FileSystem, clock *fakeClock) *Store {
	t.Helper()
	if filesystem == nil {
		filesystem = OSFileSystem{}
	}
	if clock == nil {
		clock = &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	}
	root := filepath.Join(t.TempDir(), "cas")
	store, err := New(root, Options{
		FileSystem: filesystem, Clock: clock, LeaseTTL: 10 * time.Second,
		ReservationTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testKey(t *testing.T, marker byte) cachekey.Key {
	t.Helper()
	value, err := cachekey.New(cachekey.Input{
		SchemaVersion:                cachekey.SchemaVersion,
		Profile:                      cachekey.ProfileV1,
		Namespace:                    "cutly.cache.render-chunk.v1",
		ArtifactKind:                 "cutly.cache-artifact.rgba-frame-group.v1",
		PlanFingerprint:              repeatedDigest(marker),
		ExecutionManifestFingerprint: repeatedDigest('e'),
		StageProfile:                 "cutly.compose.cpu-linear.v1",
		Inputs:                       []cachekey.NormalizedInput{{Role: "source", Ordinal: 0, ContentHash: repeatedDigest('f')}},
		FrameRange:                   &cachekey.IndexRange{Start: int64(marker), Count: 1},
		OutputProfile:                "cutly.surface.rgba16-linear-bt709.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func repeatedDigest(marker byte) string {
	if marker < '0' || (marker > '9' && marker < 'a') || marker > 'f' {
		marker = 'a'
	}
	return "sha256:" + strings.Repeat(string(marker), 64)
}

func payloadIdentity(content []byte) (string, int64) {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:]), int64(len(content))
}

func publishBytes(t *testing.T, store *Store, value cachekey.Key, content []byte) PublishResult {
	t.Helper()
	hash, size := payloadIdentity(content)
	result, err := store.Publish(context.Background(), PublishRequest{
		Key: value, Payload: bytes.NewReader(content), ExpectedHash: hash, ExpectedSize: size,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readAllAndClose(t *testing.T, reader *Reader) []byte {
	t.Helper()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return raw
}

func entryPaths(t *testing.T, store *Store, value cachekey.Key) (string, string, string) {
	t.Helper()
	shard, entry, published, err := store.keyPaths(value)
	if err != nil {
		t.Fatal(err)
	}
	return shard, entry, published
}

func assertScratchEmpty(t *testing.T, store *Store) {
	t.Helper()
	entries, err := os.ReadDir(store.scratchRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch leaked: %+v", entries)
	}
}

type faultFS struct {
	FileSystem
	mu                sync.Mutex
	available         *uint64
	failPayloadWrite  error
	failFileSyncMatch func(string) error
	failDirSyncMatch  func(string) error
	failRenameMatch   func(string, string) error
	failRenameAfter   func(string, string) error
	syncedDirectories []string
}

func (filesystem *faultFS) OpenFile(path string, flags int, permission fs.FileMode) (File, error) {
	file, err := filesystem.FileSystem.OpenFile(path, flags, permission)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, owner: filesystem, path: path}, nil
}

func (filesystem *faultFS) AvailableBytes(path string) (uint64, error) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	if filesystem.available != nil {
		return *filesystem.available, nil
	}
	return filesystem.FileSystem.AvailableBytes(path)
}

func (filesystem *faultFS) SyncDir(path string) error {
	filesystem.mu.Lock()
	hook := filesystem.failDirSyncMatch
	filesystem.syncedDirectories = append(filesystem.syncedDirectories, path)
	filesystem.mu.Unlock()
	if hook != nil {
		if err := hook(path); err != nil {
			return err
		}
	}
	return filesystem.FileSystem.SyncDir(path)
}

func (filesystem *faultFS) Rename(oldPath, newPath string) error {
	filesystem.mu.Lock()
	hook := filesystem.failRenameMatch
	after := filesystem.failRenameAfter
	filesystem.mu.Unlock()
	if hook != nil {
		if err := hook(oldPath, newPath); err != nil {
			return err
		}
	}
	if err := filesystem.FileSystem.Rename(oldPath, newPath); err != nil {
		return err
	}
	if after != nil {
		return after(oldPath, newPath)
	}
	return nil
}

type faultFile struct {
	File
	owner *faultFS
	path  string
}

func (file *faultFile) Write(content []byte) (int, error) {
	file.owner.mu.Lock()
	err := file.owner.failPayloadWrite
	file.owner.mu.Unlock()
	if err != nil && filepath.Base(file.path) == payloadName {
		return 0, err
	}
	return file.File.Write(content)
}

func (file *faultFile) Sync() error {
	file.owner.mu.Lock()
	hook := file.owner.failFileSyncMatch
	file.owner.mu.Unlock()
	if hook != nil {
		if err := hook(file.path); err != nil {
			return err
		}
	}
	return file.File.Sync()
}

var _ FileSystem = (*faultFS)(nil)
var _ File = (*faultFile)(nil)
