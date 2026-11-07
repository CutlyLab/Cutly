package cas

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestDiskPreflightExactNAndNMinusOne(t *testing.T) {
	available := PublicationOverheadBytes + 123
	filesystem := &faultFS{FileSystem: OSFileSystem{}, available: &available}
	store := newTestStore(t, filesystem, nil)
	budget, err := store.Preflight(context.Background(), 123)
	if err != nil || budget.RequiredBytes != available || budget.AvailableBytes != available {
		t.Fatalf("exact preflight=%+v error=%v", budget, err)
	}
	filesystem.mu.Lock()
	available--
	filesystem.mu.Unlock()
	budget, err = store.Preflight(context.Background(), 123)
	if !errors.Is(err, ErrInsufficientDisk) || !errors.Is(err, syscall.ENOSPC) || budget.AvailableBytes+1 != budget.RequiredBytes {
		t.Fatalf("N-1 preflight=%+v error=%v", budget, err)
	}
}

func TestDiskPreflightOverflowFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cas")
	store, err := New(root, Options{MinFreeBytes: ^uint64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Preflight(context.Background(), 1); !errors.Is(err, ErrInsufficientDisk) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestPublishFaultBoundariesNeverExposePartialEntry(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*faultFS)
		want      error
	}{
		{
			name: "payload write ENOSPC",
			configure: func(filesystem *faultFS) {
				filesystem.failPayloadWrite = syscall.ENOSPC
			},
			want: syscall.ENOSPC,
		},
		{
			name: "payload fsync",
			configure: func(filesystem *faultFS) {
				filesystem.failFileSyncMatch = func(path string) error {
					if filepath.Base(path) == payloadName {
						return syscall.EIO
					}
					return nil
				}
			},
			want: syscall.EIO,
		},
		{
			name: "completion sentinel fsync",
			configure: func(filesystem *faultFS) {
				filesystem.failFileSyncMatch = func(path string) error {
					if filepath.Base(path) == completeName {
						return syscall.EIO
					}
					return nil
				}
			},
			want: syscall.EIO,
		},
		{
			name: "scratch directory fsync",
			configure: func(filesystem *faultFS) {
				filesystem.failDirSyncMatch = func(path string) error {
					if strings.HasPrefix(filepath.Base(path), ".partial-") {
						return syscall.EIO
					}
					return nil
				}
			},
			want: syscall.EIO,
		},
		{
			name: "atomic directory rename",
			configure: func(filesystem *faultFS) {
				filesystem.failRenameMatch = func(_, newPath string) error {
					if canonicalHexName(filepath.Base(newPath), 64) {
						return syscall.EIO
					}
					return nil
				}
			},
			want: syscall.EIO,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filesystem := &faultFS{FileSystem: OSFileSystem{}}
			store := newTestStore(t, filesystem, nil)
			value := testKey(t, '5')
			test.configure(filesystem)
			payload := []byte("fault payload")
			hash, size := payloadIdentity(payload)
			result, err := store.Publish(context.Background(), PublishRequest{
				Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
			})
			if result != (PublishResult{}) || !errors.Is(err, test.want) {
				t.Fatalf("publish result=%+v error=%v want=%v", result, err, test.want)
			}
			filesystem.mu.Lock()
			filesystem.failPayloadWrite = nil
			filesystem.failFileSyncMatch = nil
			filesystem.failDirSyncMatch = nil
			filesystem.failRenameMatch = nil
			filesystem.mu.Unlock()
			if reader, lookupErr := store.Open(context.Background(), value, "fault-reader"); reader != nil || !errors.Is(lookupErr, ErrMiss) {
				t.Fatalf("failed publication became visible: (%v,%v)", reader, lookupErr)
			}
			assertScratchEmpty(t, store)
		})
	}
}

func TestNewSyncsRootParentAndRejectsUnsafeRootBoundaries(t *testing.T) {
	if _, err := New(string(filepath.Separator), Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("volume root error = %v", err)
	}
	missingParentRoot := filepath.Join(t.TempDir(), "missing", "cas")
	if _, err := New(missingParentRoot, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing parent error = %v", err)
	}

	parent := t.TempDir()
	root := filepath.Join(parent, "cas")
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	filesystem.failDirSyncMatch = func(path string) error {
		if path == parent {
			return syscall.EIO
		}
		return nil
	}
	if _, err := New(root, Options{FileSystem: filesystem}); !errors.Is(err, ErrConfiguration) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("parent sync error = %v", err)
	}
}

func TestPublishRejectsSymlinkShardWithoutWritingOutsideRoot(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, 'e')
	shard, _, _ := entryPaths(t, store, value)
	target := t.TempDir()
	if err := os.Symlink(target, shard); err != nil {
		t.Fatal(err)
	}
	payload := []byte("must stay inside root")
	hash, size := payloadIdentity(payload)
	result, err := store.Publish(context.Background(), PublishRequest{
		Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
	})
	if result != (PublishResult{}) || !errors.Is(err, ErrPublication) {
		t.Fatalf("symlink shard publish=(%+v,%v)", result, err)
	}
	if reader, openErr := store.Open(context.Background(), value, "symlink-shard-reader"); reader != nil || !errors.Is(openErr, ErrCorrupt) {
		t.Fatalf("symlink shard lookup=(%v,%v)", reader, openErr)
	}
	entries, readErr := os.ReadDir(target)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("publish escaped through shard symlink: entries=%v err=%v", entries, readErr)
	}
	assertScratchEmpty(t, store)
}

func TestVisibleButUnsyncedPublicationCanBeRecoveredByDuplicateRetry(t *testing.T) {
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	store := newTestStore(t, filesystem, nil)
	value := testKey(t, '6')
	_, entryPath, publishedPath := entryPaths(t, store, value)
	var failed atomic.Bool
	filesystem.failDirSyncMatch = func(path string) error {
		if path == entryPath && !failed.Load() {
			if _, err := os.Lstat(publishedPath); err == nil && failed.CompareAndSwap(false, true) {
				return syscall.EIO
			}
		}
		return nil
	}
	payload := []byte("durability retry")
	hash, size := payloadIdentity(payload)
	result, err := store.Publish(context.Background(), PublishRequest{
		Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
	})
	if result.Disposition != Published || !errors.Is(err, ErrDurabilityUncertain) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("visible-sync result=%+v error=%v", result, err)
	}
	filesystem.mu.Lock()
	filesystem.failDirSyncMatch = nil
	filesystem.mu.Unlock()
	result, err = store.Publish(context.Background(), PublishRequest{
		Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
	})
	if err != nil || result.Disposition != AlreadyPresent {
		t.Fatalf("recovery retry result=%+v error=%v", result, err)
	}
	if _, statErr := os.Lstat(filepath.Join(entryPath, reservationName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("duplicate recovery left publication reservation: %v", statErr)
	}
	reader, err := store.Open(context.Background(), value, "durability-reader")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); !bytes.Equal(got, payload) {
		t.Fatalf("recovered payload=%q", got)
	}
}

func TestPublishRecoversUnknownRenameOutcomeWithoutCleaningCommittedEntry(t *testing.T) {
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	store := newTestStore(t, filesystem, nil)
	value := testKey(t, 'd')
	var failed atomic.Bool
	filesystem.failRenameAfter = func(_, newPath string) error {
		if canonicalHexName(filepath.Base(newPath), 64) && failed.CompareAndSwap(false, true) {
			return syscall.EIO
		}
		return nil
	}
	payload := []byte("rename outcome unknown")
	hash, size := payloadIdentity(payload)
	result, err := store.Publish(context.Background(), PublishRequest{
		Key: value, Payload: bytes.NewReader(payload), ExpectedHash: hash, ExpectedSize: size,
	})
	if err != nil || result.Disposition != AlreadyPresent {
		t.Fatalf("unknown rename recovery result=%+v err=%v", result, err)
	}
	filesystem.mu.Lock()
	filesystem.failRenameAfter = nil
	filesystem.mu.Unlock()
	reader, err := store.Open(context.Background(), value, "unknown-rename-reader")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, reader); !bytes.Equal(got, payload) {
		t.Fatalf("unknown rename payload=%q", got)
	}
	assertScratchEmpty(t, store)
}

func TestPublishSyncsScratchEntryShardAndShardParent(t *testing.T) {
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	store := newTestStore(t, filesystem, nil)
	value := testKey(t, '2')
	publishBytes(t, store, value, []byte("fsync topology"))
	shard, entryPath, _ := entryPaths(t, store, value)
	filesystem.mu.Lock()
	synced := append([]string(nil), filesystem.syncedDirectories...)
	filesystem.mu.Unlock()
	wanted := map[string]bool{store.entriesRoot: false, store.scratchRoot: false, shard: false, entryPath: false}
	scratchSynced := false
	for _, path := range synced {
		if _, exists := wanted[path]; exists {
			wanted[path] = true
		}
		if filepath.Dir(path) == store.scratchRoot && strings.HasPrefix(filepath.Base(path), ".partial-") {
			scratchSynced = true
		}
	}
	if !scratchSynced {
		t.Fatalf("complete scratch directory was not fsynced: %v", synced)
	}
	for path, found := range wanted {
		if !found {
			t.Fatalf("publication parent %s was not fsynced: %v", path, synced)
		}
	}
}

func TestFailedLeaseAcquireRemovesPublishedLeaseFile(t *testing.T) {
	filesystem := &faultFS{FileSystem: OSFileSystem{}}
	store := newTestStore(t, filesystem, nil)
	value := testKey(t, '1')
	publishBytes(t, store, value, []byte("lease cleanup"))
	_, entryPath, _ := entryPaths(t, store, value)
	leasesPath := filepath.Join(entryPath, leasesName)
	var failed atomic.Bool
	filesystem.failDirSyncMatch = func(path string) error {
		if path == leasesPath && failed.CompareAndSwap(false, true) {
			return syscall.EIO
		}
		return nil
	}
	reader, err := store.Open(context.Background(), value, "lease-fsync-fault")
	if reader != nil || !errors.Is(err, ErrMiss) || !errors.Is(err, ErrLeaseLost) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("lease fault lookup=(%v,%v)", reader, err)
	}
	filesystem.mu.Lock()
	filesystem.failDirSyncMatch = nil
	filesystem.mu.Unlock()
	entries, readErr := os.ReadDir(leasesPath)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed lease leaked files=%v error=%v", entries, readErr)
	}
}

type cancelAfterFirstRead struct {
	content []byte
	cancel  context.CancelFunc
	done    bool
}

func (reader *cancelAfterFirstRead) Read(destination []byte) (int, error) {
	if len(reader.content) == 0 {
		return 0, io.EOF
	}
	count := copy(destination, reader.content)
	reader.content = reader.content[count:]
	if !reader.done {
		reader.done = true
		reader.cancel()
	}
	return count, nil
}

func TestPublishCancellationCleansScratchAndLeaseNamespace(t *testing.T) {
	store := newTestStore(t, nil, nil)
	value := testKey(t, '4')
	payload := bytes.Repeat([]byte("x"), 2*ioBufferBytes)
	hash, size := payloadIdentity(payload)
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterFirstRead{content: payload, cancel: cancel}
	result, err := store.Publish(ctx, PublishRequest{Key: value, Payload: reader, ExpectedHash: hash, ExpectedSize: size})
	if result != (PublishResult{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result=%+v error=%v", result, err)
	}
	assertScratchEmpty(t, store)
	if opened, lookupErr := store.Open(context.Background(), value, "after-cancel"); opened != nil || !errors.Is(lookupErr, ErrMiss) {
		t.Fatalf("canceled publish lookup=(%v,%v)", opened, lookupErr)
	}
}
