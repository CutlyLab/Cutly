package proberegistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/CutlyLab/Cutly/media/probe"
)

const (
	lockPollInterval   = 10 * time.Millisecond
	directoryReadBatch = 128
)

var canonicalHex = regexp.MustCompile(`^[0-9a-f]+$`)

// Store is a durable immutable registry rooted at one host-owned directory.
// It is safe for concurrent use by cooperating processes.
type Store struct {
	root                 string
	entriesRoot          string
	scratchRoot          string
	quarantineRoot       string
	locksRoot            string
	maxEntries           int
	maxScratchEntries    int
	maxQuarantineEntries int
	maxRecordBytes       int
	fault                func(string) error
}

var (
	processGatesOnce sync.Once
	processGates     [259]chan struct{}
)

// New opens or creates a registry at a clean absolute, non-volume path.
func New(root string, options Options) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return nil, fmt.Errorf("%w: a clean absolute non-volume root is required", ErrConfiguration)
	}
	if !lockingSupported() || !filesystemSupported() {
		return nil, ErrUnsupported
	}
	maxEntries := defaulted(options.MaxEntries, DefaultMaxEntries)
	maxScratch := defaulted(options.MaxScratchEntries, DefaultMaxScratchEntries)
	maxQuarantine := defaulted(options.MaxQuarantineEntries, DefaultMaxQuarantineEntries)
	maxRecord := defaulted(options.MaxRecordBytes, DefaultMaxRecordBytes)
	if maxEntries < 1 || maxEntries > MaxEntries || maxScratch < 1 || maxScratch > MaxScratchEntries ||
		maxQuarantine < 1 || maxQuarantine > MaxQuarantineEntries || maxRecord < MinRecordBytes || maxRecord > MaxRecordBytes {
		return nil, fmt.Errorf("%w: invalid entry, scratch, quarantine or record limit", ErrConfiguration)
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
		quarantineRoot: filepath.Join(root, "quarantine"), locksRoot: filepath.Join(root, "locks"),
		maxEntries: maxEntries, maxScratchEntries: maxScratch,
		maxQuarantineEntries: maxQuarantine, maxRecordBytes: maxRecord,
	}
	for _, directory := range []string{store.entriesRoot, store.scratchRoot, store.quarantineRoot, store.locksRoot} {
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
	return store, nil
}

// Put strictly freezes and durably publishes manifest. The returned manifest
// is independently owned, fingerprint is its canonical SHA-256 identity, and
// created is true only for the process that made the immutable entry visible.
func (store *Store) Put(ctx context.Context, manifest probe.ProbeManifest) (probe.ProbeManifest, string, bool, error) {
	if store == nil || ctx == nil {
		return probe.ProbeManifest{}, "", false, fmt.Errorf("%w: initialized store and context are required", ErrInvalidManifest)
	}
	if err := ctx.Err(); err != nil {
		return probe.ProbeManifest{}, "", false, err
	}
	if err := store.requireNamespaces(); err != nil {
		return probe.ProbeManifest{}, "", false, err
	}
	frozen, fingerprint, raw, err := freezeManifest(manifest, store.maxRecordBytes)
	if err != nil {
		return probe.ProbeManifest{}, "", false, err
	}
	name, err := fingerprintName(fingerprint)
	if err != nil {
		return probe.ProbeManifest{}, "", false, err
	}
	created := false
	err = store.withLock(ctx, name, func() error {
		if err := store.cleanupOwnScratch(ctx, name); err != nil {
			return err
		}
		path, exists, err := store.entryPath(name, true)
		if err != nil {
			return err
		}
		if exists {
			existingRaw, err := store.readEntry(ctx, path)
			if err != nil {
				return store.failCorrupt(ctx, path, name, err)
			}
			if _, err := decodeManifest(existingRaw, fingerprint, store.maxRecordBytes); err != nil {
				return store.failCorrupt(ctx, path, name, err)
			}
			if !bytes.Equal(existingRaw, raw) {
				return fmt.Errorf("%w: fingerprint identifies different canonical bytes", ErrCollision)
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
		return probe.ProbeManifest{}, "", false, err
	}
	return frozen, fingerprint, created, nil
}

// ResolveProbe verifies the path identity and canonical bytes and returns a
// newly owned manifest. It implements ffmpegbackend.ProbeResolver.
func (store *Store) ResolveProbe(ctx context.Context, fingerprint string) (probe.ProbeManifest, error) {
	if store == nil || ctx == nil {
		return probe.ProbeManifest{}, fmt.Errorf("%w: initialized store and context are required", ErrInvalidManifest)
	}
	if err := ctx.Err(); err != nil {
		return probe.ProbeManifest{}, err
	}
	if err := store.requireNamespaces(); err != nil {
		return probe.ProbeManifest{}, err
	}
	name, err := fingerprintName(fingerprint)
	if err != nil {
		return probe.ProbeManifest{}, err
	}
	var result probe.ProbeManifest
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
			return store.failCorrupt(ctx, path, name, err)
		}
		result, err = decodeManifest(raw, fingerprint, store.maxRecordBytes)
		if err != nil {
			return store.failCorrupt(ctx, path, name, err)
		}
		return nil
	})
	if err != nil {
		return probe.ProbeManifest{}, err
	}
	return result, nil
}

func (store *Store) publish(ctx context.Context, finalPath, name string, raw []byte) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	scratch, file, err := store.createScratch(ctx, name)
	if err != nil {
		return err
	}
	owned := true
	fileOpen := true
	defer func() {
		if fileOpen {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if !owned {
			return
		}
		returnErr = errors.Join(returnErr, removeFileIfPresent(scratch), syncDirectory(store.scratchRoot))
	}()
	if err := writeDurableFile(file, raw); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.fault != nil {
		if err := store.fault("publish.before_link"); err != nil {
			return err
		}
	}
	if err := os.Link(scratch, finalPath); err != nil {
		if _, pathErr := os.Lstat(finalPath); pathErr == nil || errors.Is(err, fs.ErrExist) {
			return errors.Join(ErrCollision, fmt.Errorf("probe registry: atomic no-replace link: %w", err))
		}
		return fmt.Errorf("probe registry: atomic no-replace link: %w", err)
	}
	if err := verifyPublishedLink(file, finalPath); err != nil {
		return errors.Join(err, removeFileIfPresent(finalPath), syncDirectory(filepath.Dir(finalPath)))
	}
	if store.fault != nil {
		if err := store.fault("publish.after_link"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	if err := file.Close(); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	fileOpen = false
	if err := store.syncPublished(finalPath); err != nil {
		return err
	}
	if store.fault != nil {
		if err := store.fault("publish.before_scratch_remove"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	if err := removeFileIfPresent(scratch); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	owned = false
	if err := syncDirectory(store.scratchRoot); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	return nil
}

func (store *Store) syncPublished(finalPath string) error {
	if store.fault != nil {
		if err := store.fault("publish.before_sync"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	file, err := openRegularNoFollow(finalPath)
	if err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	fileErr := errors.Join(file.Sync(), file.Close())
	if err := errors.Join(fileErr, syncDirectory(filepath.Dir(finalPath))); err != nil {
		return fmt.Errorf("%w: %v", ErrDurabilityUncertain, err)
	}
	return nil
}

func (store *Store) readEntry(ctx context.Context, path string) ([]byte, error) {
	file, err := openRegularNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open record: %v", ErrCorrupt, err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || opened.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: opened record is not regular", ErrCorrupt), statErr, file.Close())
	}
	if opened.Size() < 0 || opened.Size() > int64(store.maxRecordBytes) {
		return nil, errors.Join(fmt.Errorf("%w: file exceeds %d bytes", ErrLimit, store.maxRecordBytes), file.Close())
	}
	var output bytes.Buffer
	output.Grow(int(opened.Size()))
	buffer := make([]byte, 128<<10)
	remaining := int64(store.maxRecordBytes) + 1
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
	if output.Len() > store.maxRecordBytes {
		return nil, errors.Join(fmt.Errorf("%w: file exceeds %d bytes", ErrLimit, store.maxRecordBytes), closeErr)
	}
	return output.Bytes(), closeErr
}

func (store *Store) entryPath(name string, createShard bool) (string, bool, error) {
	if !validHex(name, sha256.Size*2) {
		return "", false, fmt.Errorf("%w: entry name", ErrInvalidManifest)
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
		shards, err := readDirectoryBounded(ctx, store.entriesRoot, 257)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if err := ctx.Err(); err != nil {
				return err
			}
			shardPath := filepath.Join(store.entriesRoot, shard.Name())
			if !shard.IsDir() || !validHex(shard.Name(), 2) || requireDirectory(shardPath) != nil {
				return fmt.Errorf("%w: invalid entry shard %q", ErrCorrupt, shard.Name())
			}
			if count >= store.maxEntries {
				return fmt.Errorf("%w: entries reached %d", ErrLimit, store.maxEntries)
			}
			children, err := readDirectoryBounded(ctx, shardPath, store.maxEntries-count+1)
			if err != nil {
				return err
			}
			for _, child := range children {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !child.Type().IsRegular() || !validHex(child.Name(), sha256.Size*2) {
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

func (store *Store) createScratch(ctx context.Context, name string) (string, *os.File, error) {
	var path string
	var opened *os.File
	err := store.withLock(ctx, "scratch-global", func() error {
		entries, err := store.scanScratch(ctx, store.maxScratchEntries+1)
		if err != nil {
			return err
		}
		if len(entries) >= store.maxScratchEntries {
			return fmt.Errorf("%w: scratch entries reached %d", ErrLimit, store.maxScratchEntries)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := os.CreateTemp(store.scratchRoot, ".partial-"+name+"-")
		if err != nil {
			return err
		}
		path = file.Name()
		opened = file
		return nil
	})
	if err != nil && opened != nil {
		err = errors.Join(err, opened.Close(), removeFileIfPresent(path))
		opened = nil
	}
	return path, opened, err
}

func (store *Store) cleanupOwnScratch(ctx context.Context, name string) error {
	return store.withLock(ctx, "scratch-global", func() (returnErr error) {
		entries, err := store.scanScratch(ctx, store.maxScratchEntries+1)
		if err != nil {
			return err
		}
		prefix := ".partial-" + name + "-"
		changed := false
		defer func() {
			if changed {
				returnErr = errors.Join(returnErr, syncDirectory(store.scratchRoot))
			}
		}()
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if strings.HasPrefix(entry.Name(), prefix) {
				if err := os.Remove(filepath.Join(store.scratchRoot, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				changed = true
			}
		}
		return nil
	})
}

func (store *Store) scanScratch(ctx context.Context, maximum int) ([]os.DirEntry, error) {
	if err := requireDirectory(store.scratchRoot); err != nil {
		return nil, fmt.Errorf("%w: scratch namespace: %v", ErrCorrupt, err)
	}
	entries, err := readDirectoryBounded(ctx, store.scratchRoot, maximum)
	if err != nil {
		return nil, err
	}
	validated := entries[:0]
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(store.scratchRoot, entry.Name())
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), ".partial-") {
			return nil, fmt.Errorf("%w: invalid scratch entry %q", ErrCorrupt, entry.Name())
		}
		if store.fault != nil {
			if err := store.fault("scan.scratch.before_validate"); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := requireRegularNoFollow(path); err != nil {
			// Completed publishers remove their scratch link without taking the
			// admission lock. A directory entry may therefore disappear between
			// ReadDir and this descriptor check; disappearance only decreases use.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("%w: invalid scratch entry %q", ErrCorrupt, entry.Name())
		}
		validated = append(validated, entry)
	}
	return validated, nil
}

func (store *Store) failCorrupt(ctx context.Context, path, name string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	if !errors.Is(cause, ErrCorrupt) && !errors.Is(cause, ErrCollision) {
		return cause
	}
	return errors.Join(cause, store.quarantine(ctx, path, name))
}

func (store *Store) quarantine(ctx context.Context, path, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.withLock(ctx, "quarantine-global", func() (returnErr error) {
		entries, err := readDirectoryBounded(ctx, store.quarantineRoot, store.maxQuarantineEntries+1)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(store.quarantineRoot, entry.Name())
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".corrupt-") || requireDirectory(path) != nil {
				return fmt.Errorf("%w: invalid quarantine entry %q", ErrCorrupt, entry.Name())
			}
		}
		if len(entries) >= store.maxQuarantineEntries {
			return fmt.Errorf("%w: quarantine entries reached %d", ErrLimit, store.maxQuarantineEntries)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		destination, err := os.MkdirTemp(store.quarantineRoot, ".corrupt-"+name[:12]+"-")
		if err != nil {
			return err
		}
		owned := true
		defer func() {
			if owned {
				returnErr = errors.Join(returnErr, os.RemoveAll(destination), syncDirectory(store.quarantineRoot))
			}
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(path, filepath.Join(destination, "record")); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		owned = false
		return errors.Join(syncDirectory(destination), syncDirectory(filepath.Dir(path)), syncDirectory(store.quarantineRoot))
	})
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
	case "entries-global", "scratch-global", "quarantine-global":
		return filepath.Join(store.locksRoot, name+".lock"), nil
	default:
		if !validHex(name, sha256.Size*2) {
			return "", fmt.Errorf("%w: lock identity", ErrInvalidManifest)
		}
		return filepath.Join(store.locksRoot, "entry-"+name[:2]+".lock"), nil
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
	case "quarantine-global.lock":
		return processGates[258]
	default:
		return processGates[int(digest[0])]
	}
}

func (store *Store) requireNamespaces() error {
	for _, directory := range []string{store.root, store.entriesRoot, store.scratchRoot, store.quarantineRoot, store.locksRoot} {
		if err := requireDirectory(directory); err != nil {
			return fmt.Errorf("%w: namespace %s: %v", ErrCorrupt, filepath.Base(directory), err)
		}
	}
	return nil
}

func writeDurableFile(file *os.File, raw []byte) error {
	if file == nil {
		return errors.New("probe registry: nil scratch file")
	}
	written, writeErr := file.Write(raw)
	if writeErr == nil && written != len(raw) {
		writeErr = io.ErrShortWrite
	}
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	return errors.Join(writeErr, syncErr)
}

func verifyPublishedLink(source *os.File, finalPath string) (returnErr error) {
	if source == nil {
		return errors.Join(ErrCorrupt, errors.New("probe registry: nil publication source"))
	}
	sourceInfo, err := source.Stat()
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.Join(ErrCorrupt, errors.New("probe registry: publication source is not regular"), err)
	}
	final, err := openRegularNoFollow(finalPath)
	if err != nil {
		return errors.Join(ErrCorrupt, fmt.Errorf("probe registry: open published link: %w", err))
	}
	defer func() { returnErr = errors.Join(returnErr, final.Close()) }()
	finalInfo, err := final.Stat()
	if err != nil || !finalInfo.Mode().IsRegular() || finalInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(sourceInfo, finalInfo) {
		return errors.Join(ErrCorrupt, errors.New("probe registry: published link identity mismatch"), err)
	}
	return nil
}

func requireRegularNoFollow(path string) (returnErr error) {
	file, err := openRegularNoFollow(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("path is not a non-reparse regular file"), err)
	}
	return nil
}

func removeFileIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func readDirectoryBounded(ctx context.Context, path string, maximum int) (entries []os.DirEntry, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil directory scan context", ErrInvalidManifest)
	}
	if maximum <= 0 {
		return nil, fmt.Errorf("%w: invalid directory admission bound", ErrLimit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	for len(entries) < maximum {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count := directoryReadBatch
		if remaining := maximum - len(entries); count > remaining {
			count = remaining
		}
		batch, readErr := directory.ReadDir(count)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, entry := range batch {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
		if errors.Is(readErr, io.EOF) {
			return entries, nil
		}
		if readErr != nil {
			return nil, readErr
		}
		if len(batch) == 0 {
			return nil, io.ErrNoProgress
		}
	}
	return entries, nil
}

func validHex(value string, length int) bool {
	return len(value) == length && canonicalHex.MatchString(value)
}

func defaulted(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

var _ interface {
	ResolveProbe(context.Context, string) (probe.ProbeManifest, error)
} = (*Store)(nil)
