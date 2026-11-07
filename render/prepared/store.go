package prepared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	recordName       = "environment.json"
	completeName     = "complete"
	completionV1     = "cutly.render.prepared-complete.v1"
	lockPollInterval = 10 * time.Millisecond
)

var canonicalHex = regexp.MustCompile(`^[0-9a-f]+$`)

// Store is a durable immutable registry rooted at one host-owned directory.
type Store struct {
	root              string
	entriesRoot       string
	scratchRoot       string
	locksRoot         string
	maxEntries        int
	maxScratchEntries int
	maxRecordBytes    int
	fault             func(string) error
	maxRecordBytesV2  int
	rootIdentityV2    os.FileInfo
	identitiesMuV2    sync.Mutex
	identitiesV2      map[string]os.FileInfo
}

var (
	processGatesOnce sync.Once
	processGates     [258]chan struct{}
)

// New opens or creates a registry at a clean absolute path.
func New(root string, options Options) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return nil, fmt.Errorf("%w: a clean absolute non-volume root is required", ErrConfiguration)
	}
	if !lockingSupported() || !publicationSupported() {
		return nil, ErrUnsupported
	}
	maximumEntries := options.MaxEntries
	if maximumEntries == 0 {
		maximumEntries = DefaultMaxEntries
	}
	maximumScratch := options.MaxScratchEntries
	if maximumScratch == 0 {
		maximumScratch = DefaultMaxScratchEntries
	}
	maximumRecord := options.MaxRecordBytes
	if maximumRecord == 0 {
		maximumRecord = DefaultMaxRecordBytes
	}
	if maximumEntries < 1 || maximumEntries > MaxEntries || maximumScratch < 1 || maximumScratch > MaxScratchEntries ||
		maximumRecord < 1024 || maximumRecord > MaxRecordBytes {
		return nil, fmt.Errorf("%w: invalid entry, scratch or record limit", ErrConfiguration)
	}
	maximumRecordV2 := options.MaxRecordBytesV2
	if maximumRecordV2 == 0 {
		maximumRecordV2 = MaxRecordBytesV2
	}
	if maximumRecordV2 < 1024 || maximumRecordV2 > MaxRecordBytesV2 {
		return nil, fmt.Errorf("%w: invalid v2 record limit", ErrConfiguration)
	}
	parent := filepath.Dir(root)
	if err := requireDirectory(parent); err != nil {
		return nil, fmt.Errorf("%w: root parent: %v", ErrConfiguration, err)
	}
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: create root: %v", ErrConfiguration, err)
	}
	if err := requireDirectory(root); err != nil {
		return nil, fmt.Errorf("%w: root: %v", ErrConfiguration, err)
	}
	store := &Store{
		root: root, entriesRoot: filepath.Join(root, "entries"), scratchRoot: filepath.Join(root, "scratch"),
		locksRoot: filepath.Join(root, "locks"), maxEntries: maximumEntries,
		maxScratchEntries: maximumScratch, maxRecordBytes: maximumRecord,
	}
	for _, directory := range []string{store.entriesRoot, store.scratchRoot, store.locksRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: create %s: %v", ErrConfiguration, filepath.Base(directory), err)
		}
		if err := requireDirectory(directory); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrConfiguration, filepath.Base(directory), err)
		}
	}
	if err := errors.Join(syncDirectory(root), syncDirectory(parent)); err != nil {
		return nil, fmt.Errorf("%w: sync root: %v", ErrConfiguration, err)
	}
	identityV2, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	store.maxRecordBytesV2 = maximumRecordV2
	store.rootIdentityV2 = identityV2
	store.identitiesV2 = make(map[string]os.FileInfo)
	return store, nil
}

// Put strictly freezes and durably publishes an environment. The boolean is
// true only for the writer that created the immutable entry.
func (store *Store) Put(ctx context.Context, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) (Environment, bool, error) {
	if store == nil || ctx == nil {
		return Environment{}, false, fmt.Errorf("%w: initialized store and context are required", ErrInvalidEnvironment)
	}
	if err := ctx.Err(); err != nil {
		return Environment{}, false, err
	}
	if err := store.requireNamespaces(); err != nil {
		return Environment{}, false, err
	}
	environment, key, raw, err := freezeEnvironment(plan, manifest, store.maxRecordBytes)
	if err != nil {
		return Environment{}, false, err
	}
	name, err := keyName(key)
	if err != nil {
		return Environment{}, false, err
	}
	created := false
	err = store.withLock(ctx, name, func() error {
		path, exists, err := store.entryPath(name, true)
		if err != nil {
			return err
		}
		if exists {
			existingRaw, err := store.readEntry(ctx, path)
			if err != nil {
				return err
			}
			_, existingKey, err := decodeRecord(existingRaw, store.maxRecordBytes)
			if err != nil {
				return err
			}
			if existingKey != key {
				return fmt.Errorf("%w: entry path contains another environment", ErrCollision)
			}
			if !bytes.Equal(existingRaw, raw) {
				return fmt.Errorf("%w: fingerprints identify different canonical bytes", ErrCollision)
			}
			return store.syncPublished(path)
		}
		return store.admitEntry(ctx, func() error {
			if err := store.publish(ctx, path, name, raw); err != nil {
				return err
			}
			created = true
			return nil
		})
	})
	if err != nil {
		return Environment{}, false, err
	}
	return environment, created, nil
}

// Resolve verifies both fingerprints and returns newly owned decoded values.
func (store *Store) Resolve(ctx context.Context, planFingerprint, manifestFingerprint string) (Environment, error) {
	if store == nil || ctx == nil {
		return Environment{}, fmt.Errorf("%w: initialized store and context are required", ErrInvalidEnvironment)
	}
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if err := store.requireNamespaces(); err != nil {
		return Environment{}, err
	}
	key := Key{PlanFingerprint: planFingerprint, ExecutionManifestFingerprint: manifestFingerprint}
	name, err := keyName(key)
	if err != nil {
		return Environment{}, err
	}
	var result Environment
	err = store.withLock(ctx, name, func() error {
		path, exists, err := store.entryPath(name, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		raw, err := store.readEntry(ctx, path)
		if err != nil {
			return err
		}
		environment, storedKey, err := decodeRecord(raw, store.maxRecordBytes)
		if err != nil {
			return err
		}
		if storedKey != key {
			return fmt.Errorf("%w: entry path/key mismatch", ErrCollision)
		}
		result = environment
		return nil
	})
	if err != nil {
		return Environment{}, err
	}
	return result, nil
}

func (store *Store) publish(ctx context.Context, finalPath, name string, raw []byte) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	scratch, err := store.createScratch(ctx, name)
	if err != nil {
		return err
	}
	owned := true
	defer func() {
		if !owned {
			return
		}
		returnErr = errors.Join(returnErr, os.RemoveAll(scratch), syncDirectory(store.scratchRoot))
	}()
	if err := writeDurableFile(filepath.Join(scratch, recordName), raw); err != nil {
		return err
	}
	if err := writeDurableFile(filepath.Join(scratch, completeName), completion(raw)); err != nil {
		return err
	}
	if err := errors.Join(syncDirectory(scratch), syncDirectory(store.scratchRoot)); err != nil {
		return fmt.Errorf("prepared: sync scratch: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.fault != nil {
		if err := store.fault("publish.before_rename"); err != nil {
			return err
		}
	}
	if err := renameNoReplace(scratch, finalPath); err != nil {
		if _, pathErr := os.Lstat(finalPath); pathErr == nil || errors.Is(err, fs.ErrExist) {
			return errors.Join(ErrCollision, fmt.Errorf("prepared: atomic no-replace publish: %w", err))
		}
		return fmt.Errorf("prepared: atomic no-replace publish: %w", err)
	}
	owned = false
	if store.fault != nil {
		if err := store.fault("publish.after_rename"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	return store.syncPublished(finalPath)
}

func (store *Store) syncPublished(finalPath string) error {
	if store.fault != nil {
		if err := store.fault("publish.before_sync"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	if err := errors.Join(syncDirectory(finalPath), syncDirectory(filepath.Dir(finalPath)), syncDirectory(store.scratchRoot)); err != nil {
		return fmt.Errorf("%w: %v", ErrDurabilityUncertain, err)
	}
	return nil
}

func (store *Store) readEntry(ctx context.Context, path string) ([]byte, error) {
	if err := requireDirectory(path); err != nil {
		return nil, fmt.Errorf("%w: entry directory: %v", ErrCorrupt, err)
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open entry: %v", ErrCorrupt, err)
	}
	entries, readErr := directory.ReadDir(3)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return nil, fmt.Errorf("%w: read entry: %v", ErrCorrupt, err)
	}
	if len(entries) != 2 || !hasEntry(entries, recordName) || !hasEntry(entries, completeName) {
		return nil, fmt.Errorf("%w: entry directory shape", ErrCorrupt)
	}
	raw, err := readRegularFile(ctx, filepath.Join(path, recordName), store.maxRecordBytes)
	if err != nil {
		return nil, err
	}
	complete, err := readRegularFile(ctx, filepath.Join(path, completeName), 256)
	if err != nil || !bytes.Equal(complete, completion(raw)) {
		return nil, errors.Join(fmt.Errorf("%w: completion sentinel", ErrCorrupt), err)
	}
	return raw, nil
}

func (store *Store) entryPath(name string, createShard bool) (string, bool, error) {
	if !validHex(name, sha256.Size*2) {
		return "", false, fmt.Errorf("%w: entry name", ErrInvalidEnvironment)
	}
	if err := requireDirectory(store.entriesRoot); err != nil {
		return "", false, fmt.Errorf("%w: entries namespace: %v", ErrCorrupt, err)
	}
	shard := filepath.Join(store.entriesRoot, name[:2])
	if createShard {
		if err := os.Mkdir(shard, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", false, err
		}
		if err := requireDirectory(shard); err != nil {
			return "", false, fmt.Errorf("%w: entry shard: %v", ErrCorrupt, err)
		}
		if err := syncDirectory(store.entriesRoot); err != nil {
			return "", false, err
		}
	} else if err := requireDirectory(shard); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(shard, name), false, nil
		}
		return "", false, fmt.Errorf("%w: entry shard: %v", ErrCorrupt, err)
	}
	path := filepath.Join(shard, name)
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

func (store *Store) admitEntry(ctx context.Context, publish func() error) error {
	return store.withLock(ctx, "entries-global", func() error {
		count := 0
		shards, err := readDirectoryBounded(store.entriesRoot, 257)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !shard.IsDir() || !validHex(shard.Name(), 2) {
				return fmt.Errorf("%w: invalid entry shard %q", ErrCorrupt, shard.Name())
			}
			children, err := readDirectoryBounded(filepath.Join(store.entriesRoot, shard.Name()), store.maxEntries-count+1)
			if err != nil {
				return err
			}
			for _, child := range children {
				if !child.IsDir() || !validHex(child.Name(), sha256.Size*2) {
					return fmt.Errorf("%w: invalid entry name %q", ErrCorrupt, child.Name())
				}
				count++
				if count >= store.maxEntries {
					return fmt.Errorf("%w: entries reached %d", ErrLimit, store.maxEntries)
				}
			}
		}
		return publish()
	})
}

func (store *Store) createScratch(ctx context.Context, name string) (string, error) {
	var scratch string
	err := store.withLock(ctx, "scratch-global", func() error {
		if err := requireDirectory(store.scratchRoot); err != nil {
			return fmt.Errorf("%w: scratch namespace: %v", ErrCorrupt, err)
		}
		entries, err := readDirectoryBounded(store.scratchRoot, store.maxScratchEntries+1)
		if err != nil {
			return err
		}
		if len(entries) >= store.maxScratchEntries {
			return fmt.Errorf("%w: scratch entries reached %d", ErrLimit, store.maxScratchEntries)
		}
		scratch, err = os.MkdirTemp(store.scratchRoot, ".partial-"+name[:12]+"-")
		return err
	})
	return scratch, err
}

func (store *Store) withLock(ctx context.Context, name string, action func() error) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireDirectory(store.locksRoot); err != nil {
		return fmt.Errorf("%w: lock namespace: %v", ErrCorrupt, err)
	}
	path, err := store.lockPath(name)
	if err != nil {
		return err
	}
	gate := processGate(path)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
	}
	defer func() { gate <- struct{}{} }()
	var lock *fileLock
	for {
		acquired, err := tryFileLock(path)
		if err == nil {
			lock = acquired
			break
		}
		if !errors.Is(err, ErrBusy) {
			return err
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer func() { returnErr = errors.Join(returnErr, lock.release()) }()
	return action()
}

func (store *Store) lockPath(name string) (string, error) {
	switch name {
	case "entries-global", "scratch-global":
		return filepath.Join(store.locksRoot, name+".lock"), nil
	default:
		if !validHex(name, sha256.Size*2) {
			return "", fmt.Errorf("%w: lock identity", ErrInvalidEnvironment)
		}
		return filepath.Join(store.locksRoot, "entry-"+name[:3]+".lock"), nil
	}
}

func processGate(path string) chan struct{} {
	processGatesOnce.Do(func() {
		for index := range processGates {
			processGates[index] = make(chan struct{}, 1)
			processGates[index] <- struct{}{}
		}
	})
	digest := sha256.Sum256([]byte(path))
	switch filepath.Base(path) {
	case "entries-global.lock":
		return processGates[256]
	case "scratch-global.lock":
		return processGates[257]
	default:
		return processGates[int(digest[0])]
	}
}

func (store *Store) requireNamespaces() error {
	for _, directory := range []string{store.root, store.entriesRoot, store.scratchRoot, store.locksRoot} {
		if err := requireDirectory(directory); err != nil {
			return fmt.Errorf("%w: namespace %s: %v", ErrCorrupt, filepath.Base(directory), err)
		}
	}
	return nil
}

func keyName(key Key) (string, error) {
	if !cachekey.CanonicalSHA256(key.PlanFingerprint) || !cachekey.CanonicalSHA256(key.ExecutionManifestFingerprint) {
		return "", fmt.Errorf("%w: canonical fingerprints are required", ErrInvalidEnvironment)
	}
	digest := sha256.Sum256([]byte(key.PlanFingerprint + "\x00" + key.ExecutionManifestFingerprint))
	return hex.EncodeToString(digest[:]), nil
}

func completion(raw []byte) []byte {
	digest := sha256.Sum256(raw)
	return []byte(completionV1 + "\nsha256:" + hex.EncodeToString(digest[:]) + "\n")
}

func writeDurableFile(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(raw)
	if writeErr == nil && written != len(raw) {
		writeErr = io.ErrShortWrite
	}
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	return errors.Join(writeErr, syncErr, file.Close())
}

func readRegularFile(ctx context.Context, path string, maximum int) ([]byte, error) {
	file, err := openRegularNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open record: %v", ErrCorrupt, err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || opened.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: opened record is not regular", ErrCorrupt), statErr, file.Close())
	}
	if opened.Size() < 0 || opened.Size() > int64(maximum) {
		return nil, errors.Join(fmt.Errorf("%w: file exceeds %d bytes", ErrLimit, maximum), file.Close())
	}
	var output bytes.Buffer
	output.Grow(int(opened.Size()))
	buffer := make([]byte, 128<<10)
	remaining := int64(maximum) + 1
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
		want := int64(len(buffer))
		if want > remaining {
			want = remaining
		}
		count, readErr := file.Read(buffer[:want])
		if count > 0 {
			_, _ = output.Write(buffer[:count])
			remaining -= int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, errors.Join(readErr, file.Close())
		}
	}
	closeErr := file.Close()
	if output.Len() > maximum {
		return nil, errors.Join(fmt.Errorf("%w: file exceeds %d bytes", ErrLimit, maximum), closeErr)
	}
	return output.Bytes(), closeErr
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a non-symlink directory")
	}
	return nil
}

func hasEntry(entries []os.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

func validHex(value string, length int) bool {
	return len(value) == length && canonicalHex.MatchString(value)
}

func readDirectoryBounded(path string, maximum int) ([]os.DirEntry, error) {
	if maximum <= 0 {
		return nil, fmt.Errorf("%w: invalid directory admission bound", ErrLimit)
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(maximum)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	return entries, errors.Join(readErr, directory.Close())
}

var _ interface {
	RecordPrepared(context.Context, string, videoedit.RenderPlan, renderapi.ExecutionManifest) error
} = (*Store)(nil)
