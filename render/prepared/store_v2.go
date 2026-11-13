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
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const completionV2 = "cutly.render.prepared-complete.v2"

// ensureNamespacesV2 creates the v2 sibling namespaces on first use. Creation is lazy so a
// store that only ever sees v1 traffic keeps its exact v1 directory shape.
func (store *Store) ensureNamespacesV2() error {
	for _, directory := range []string{store.entriesRootV2, store.scratchRootV2, store.locksRootV2} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: create %s: %v", ErrConfiguration, filepath.Base(directory), err)
		}
		if err := requireDirectory(directory); err != nil {
			return fmt.Errorf("%w: namespace %s: %v", ErrCorrupt, filepath.Base(directory), err)
		}
	}
	return syncDirectory(store.root)
}

// PutV2 strictly freezes and durably publishes one Document v2 environment. The boolean is
// true only for the writer that created the immutable entry.
func (store *Store) PutV2(
	ctx context.Context,
	document documentv2.Document,
	plan renderplanv2.Plan,
	manifest renderapi.ExecutionManifestV2,
) (EnvironmentV2, bool, error) {
	if store == nil || ctx == nil {
		return EnvironmentV2{}, false, fmt.Errorf("%w: initialized store and context are required", ErrInvalidEnvironment)
	}
	if err := ctx.Err(); err != nil {
		return EnvironmentV2{}, false, err
	}
	if err := store.ensureNamespacesV2(); err != nil {
		return EnvironmentV2{}, false, err
	}
	environment, key, raw, err := freezeEnvironmentV2(ctx, document, plan, manifest, store.maxRecordBytesV2)
	if err != nil {
		return EnvironmentV2{}, false, err
	}
	name, err := keyName(key)
	if err != nil {
		return EnvironmentV2{}, false, err
	}
	created := false
	err = store.withLockV2(ctx, name, func() error {
		path, exists, err := store.entryPathV2(name, true)
		if err != nil {
			return err
		}
		if exists {
			existingRaw, err := store.readEntryV2(ctx, path)
			if err != nil {
				return err
			}
			_, existingKey, err := decodeRecordV2(ctx, existingRaw, store.maxRecordBytesV2)
			if err != nil {
				return err
			}
			if existingKey != key {
				return fmt.Errorf("%w: v2 entry path contains another environment", ErrCollision)
			}
			if !bytes.Equal(existingRaw, raw) {
				return fmt.Errorf("%w: v2 fingerprints identify different canonical bytes", ErrCollision)
			}
			return store.syncPublishedV2(path)
		}
		return store.admitEntryV2(ctx, func() error {
			if err := store.publishV2(ctx, path, name, raw); err != nil {
				return err
			}
			created = true
			return nil
		})
	})
	if err != nil {
		return EnvironmentV2{}, false, err
	}
	return environment, created, nil
}

// ResolveV2 verifies both fingerprints and returns newly owned decoded values after re-running
// the complete S3 reverification against the stored canonical record.
func (store *Store) ResolveV2(ctx context.Context, planFingerprint, manifestFingerprint string) (EnvironmentV2, error) {
	if store == nil || ctx == nil {
		return EnvironmentV2{}, fmt.Errorf("%w: initialized store and context are required", ErrInvalidEnvironment)
	}
	if err := ctx.Err(); err != nil {
		return EnvironmentV2{}, err
	}
	if err := store.ensureNamespacesV2(); err != nil {
		return EnvironmentV2{}, err
	}
	key := Key{PlanFingerprint: planFingerprint, ExecutionManifestFingerprint: manifestFingerprint}
	name, err := keyName(key)
	if err != nil {
		return EnvironmentV2{}, err
	}
	var result EnvironmentV2
	err = store.withLockV2(ctx, name, func() error {
		path, exists, err := store.entryPathV2(name, false)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		raw, err := store.readEntryV2(ctx, path)
		if err != nil {
			return err
		}
		environment, storedKey, err := decodeRecordV2(ctx, raw, store.maxRecordBytesV2)
		if err != nil {
			return err
		}
		if storedKey != key {
			return fmt.Errorf("%w: v2 entry path/key mismatch", ErrCollision)
		}
		result = environment
		return nil
	})
	if err != nil {
		return EnvironmentV2{}, err
	}
	return result, nil
}

func (store *Store) publishV2(ctx context.Context, finalPath, name string, raw []byte) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	scratch, err := store.createScratchV2(ctx, name)
	if err != nil {
		return err
	}
	owned := true
	defer func() {
		if !owned {
			return
		}
		returnErr = errors.Join(returnErr, os.RemoveAll(scratch), syncDirectory(store.scratchRootV2))
	}()
	if err := writeDurableFile(filepath.Join(scratch, recordName), raw); err != nil {
		return err
	}
	if err := writeDurableFile(filepath.Join(scratch, completeName), completionV2Sentinel(raw)); err != nil {
		return err
	}
	if err := errors.Join(syncDirectory(scratch), syncDirectory(store.scratchRootV2)); err != nil {
		return fmt.Errorf("prepared: sync v2 scratch: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.fault != nil {
		if err := store.fault("publish.before_rename"); err != nil {
			return err
		}
	}
	// The durable rename is the point of no return: a context canceled at any earlier point of
	// the publication sequence, including between the scratch sync and this rename, must not
	// publish.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := renameNoReplace(scratch, finalPath); err != nil {
		if _, pathErr := os.Lstat(finalPath); pathErr == nil || errors.Is(err, fs.ErrExist) {
			return errors.Join(ErrCollision, fmt.Errorf("prepared: atomic no-replace v2 publish: %w", err))
		}
		return fmt.Errorf("prepared: atomic no-replace v2 publish: %w", err)
	}
	owned = false
	if store.fault != nil {
		if err := store.fault("publish.after_rename"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	return store.syncPublishedV2(finalPath)
}

func (store *Store) syncPublishedV2(finalPath string) error {
	if store.fault != nil {
		if err := store.fault("publish.before_sync"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	if err := errors.Join(syncDirectory(finalPath), syncDirectory(filepath.Dir(finalPath)), syncDirectory(store.scratchRootV2)); err != nil {
		return fmt.Errorf("%w: %v", ErrDurabilityUncertain, err)
	}
	return nil
}

func (store *Store) readEntryV2(ctx context.Context, path string) ([]byte, error) {
	if err := requireDirectory(path); err != nil {
		return nil, fmt.Errorf("%w: v2 entry directory: %v", ErrCorrupt, err)
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open v2 entry: %v", ErrCorrupt, err)
	}
	entries, readErr := directory.ReadDir(3)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return nil, fmt.Errorf("%w: read v2 entry: %v", ErrCorrupt, err)
	}
	if len(entries) != 2 || !hasEntry(entries, recordName) || !hasEntry(entries, completeName) {
		return nil, fmt.Errorf("%w: v2 entry directory shape", ErrCorrupt)
	}
	raw, err := readRegularFile(ctx, filepath.Join(path, recordName), store.maxRecordBytesV2)
	if err != nil {
		return nil, err
	}
	complete, err := readRegularFile(ctx, filepath.Join(path, completeName), 256)
	if err != nil || !bytes.Equal(complete, completionV2Sentinel(raw)) {
		return nil, errors.Join(fmt.Errorf("%w: v2 completion sentinel", ErrCorrupt), err)
	}
	return raw, nil
}

func (store *Store) entryPathV2(name string, createShard bool) (string, bool, error) {
	if !validHex(name, sha256.Size*2) {
		return "", false, fmt.Errorf("%w: v2 entry name", ErrInvalidEnvironment)
	}
	if err := requireDirectory(store.entriesRootV2); err != nil {
		return "", false, fmt.Errorf("%w: v2 entries namespace: %v", ErrCorrupt, err)
	}
	shard := filepath.Join(store.entriesRootV2, name[:2])
	if createShard {
		if err := os.Mkdir(shard, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", false, err
		}
		if err := requireDirectory(shard); err != nil {
			return "", false, fmt.Errorf("%w: v2 entry shard: %v", ErrCorrupt, err)
		}
		if err := syncDirectory(store.entriesRootV2); err != nil {
			return "", false, err
		}
	} else if err := requireDirectory(shard); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(shard, name), false, nil
		}
		return "", false, fmt.Errorf("%w: v2 entry shard: %v", ErrCorrupt, err)
	}
	path := filepath.Join(shard, name)
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := requireRegularDirectory(path); err != nil {
		return "", false, fmt.Errorf("%w: v2 entry path: %v", ErrCorrupt, err)
	}
	return path, true, nil
}

func (store *Store) admitEntryV2(ctx context.Context, publish func() error) error {
	return store.withLockV2(ctx, "entries-v2-global", func() error {
		count := 0
		shards, err := readDirectoryBounded(store.entriesRootV2, 257)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !shard.IsDir() || !validHex(shard.Name(), 2) {
				return fmt.Errorf("%w: invalid v2 entry shard %q", ErrCorrupt, shard.Name())
			}
			children, err := readDirectoryBounded(filepath.Join(store.entriesRootV2, shard.Name()), store.maxEntries-count+1)
			if err != nil {
				return err
			}
			for _, child := range children {
				if !child.IsDir() || !validHex(child.Name(), sha256.Size*2) {
					return fmt.Errorf("%w: invalid v2 entry name %q", ErrCorrupt, child.Name())
				}
				count++
				if count >= store.maxEntries {
					return fmt.Errorf("%w: v2 entries reached %d", ErrLimit, store.maxEntries)
				}
			}
		}
		return publish()
	})
}

func (store *Store) createScratchV2(ctx context.Context, name string) (string, error) {
	var scratch string
	err := store.withLockV2(ctx, "scratch-v2-global", func() error {
		if err := requireDirectory(store.scratchRootV2); err != nil {
			return fmt.Errorf("%w: v2 scratch namespace: %v", ErrCorrupt, err)
		}
		entries, err := readDirectoryBounded(store.scratchRootV2, store.maxScratchEntries+1)
		if err != nil {
			return err
		}
		if len(entries) >= store.maxScratchEntries {
			return fmt.Errorf("%w: v2 scratch entries reached %d", ErrLimit, store.maxScratchEntries)
		}
		scratch, err = os.MkdirTemp(store.scratchRootV2, ".partial-"+name[:12]+"-")
		return err
	})
	return scratch, err
}

func (store *Store) withLockV2(ctx context.Context, name string, action func() error) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireDirectory(store.locksRootV2); err != nil {
		return fmt.Errorf("%w: v2 lock namespace: %v", ErrCorrupt, err)
	}
	path, err := store.lockPathV2(name)
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

func (store *Store) lockPathV2(name string) (string, error) {
	switch name {
	case "entries-v2-global", "scratch-v2-global":
		return filepath.Join(store.locksRootV2, name+".lock"), nil
	default:
		if !validHex(name, sha256.Size*2) {
			return "", fmt.Errorf("%w: v2 lock identity", ErrInvalidEnvironment)
		}
		return filepath.Join(store.locksRootV2, "entry-"+name[:3]+".lock"), nil
	}
}

func completionV2Sentinel(raw []byte) []byte {
	digest := sha256.Sum256(raw)
	return []byte(completionV2 + "\nsha256:" + hex.EncodeToString(digest[:]) + "\n")
}

// requireRegularDirectory rejects symlinked entry paths: the published entry must be a real
// directory owned by this namespace, mirroring the substitution checks v1 performs through
// requireDirectory on the final path.
func requireRegularDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: not a directory", ErrCorrupt)
	}
	return nil
}
