package cas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

// PublishDisposition distinguishes the writer that won the no-replace commit from a verified
// duplicate that reused exactly the same payload.
type PublishDisposition string

const (
	// Published means this call committed the key slot.
	Published PublishDisposition = "published"
	// AlreadyPresent means a complete existing entry was reverified and matched the request.
	AlreadyPresent PublishDisposition = "already_present"
)

// PublishRequest supplies both a stream and its caller-expected immutable identity. Publish fails
// if the stream differs, even if no entry exists yet.
type PublishRequest struct {
	Key          cachekey.Key
	Payload      io.Reader
	ExpectedHash string
	ExpectedSize int64
}

// PublishResult describes the complete visible entry. A durability-uncertain error may accompany
// a Published result after the atomic visibility boundary; retrying the same request resolves it.
type PublishResult struct {
	Disposition PublishDisposition
	Entry       Entry
}

type reservation struct {
	SchemaVersion   int          `json:"schema_version"`
	Profile         string       `json:"profile"`
	StageKey        cachekey.Key `json:"stage_key"`
	Nonce           string       `json:"nonce"`
	CreatedUnixNano int64        `json:"created_unix_nano"`
	ExpiresUnixNano int64        `json:"expires_unix_nano"`
}

func decodeReservation(raw []byte) (reservation, error) {
	if len(raw) == 0 || len(raw) > maxReservationBytes {
		return reservation{}, fmt.Errorf("%w: reservation size", ErrCorrupt)
	}
	var value reservation
	if err := wirejson.Decode(raw, &value); err != nil {
		return reservation{}, fmt.Errorf("%w: strict reservation decode: %w", ErrCorrupt, err)
	}
	if value.SchemaVersion != reservationSchemaVersion || value.Profile != reservationProfileV1 ||
		cachekey.ValidateKey(value.StageKey) != nil || !canonicalHexName(value.Nonce, 32) ||
		value.CreatedUnixNano <= 0 || value.ExpiresUnixNano <= value.CreatedUnixNano ||
		value.ExpiresUnixNano-value.CreatedUnixNano > MaxReservationTTL.Nanoseconds() {
		return reservation{}, fmt.Errorf("%w: reservation fields", ErrCorrupt)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return reservation{}, fmt.Errorf("%w: canonical reservation: %w", ErrCorrupt, err)
	}
	if !bytes.Equal(raw, canonical) {
		return reservation{}, fmt.Errorf("%w: reservation is not canonical", ErrCorrupt)
	}
	return value, nil
}

const (
	reservationSchemaVersion = 1
	reservationProfileV1     = "cutly.cache.local-publish-reservation.v1"
	maxReservationBytes      = 8 << 10
)

// Publish computes a complete entry wrapper in scratch, revalidates every byte, writes the
// completion sentinel last, and atomically moves the non-empty wrapper to a missing canonical key
// path. Concurrent identical publishers converge on AlreadyPresent. A different payload for the
// same key is a hard collision and never replaces the published bytes.
func (store *Store) Publish(ctx context.Context, request PublishRequest) (result PublishResult, returnErr error) {
	if store == nil || store.fs == nil || ctx == nil || request.Payload == nil {
		return PublishResult{}, fmt.Errorf("%w: nil store, context, or payload", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return PublishResult{}, err
	}
	if err := cachekey.ValidateKey(request.Key); err != nil {
		return PublishResult{}, fmt.Errorf("%w: stage key: %w", ErrInvalidRequest, err)
	}
	if !cachekey.CanonicalSHA256(request.ExpectedHash) || request.ExpectedSize < 0 {
		return PublishResult{}, fmt.Errorf("%w: expected payload identity", ErrInvalidRequest)
	}
	if _, err := store.Preflight(ctx, request.ExpectedSize); err != nil {
		return PublishResult{}, err
	}
	nonce, err := store.nonce()
	if err != nil {
		return PublishResult{}, fmt.Errorf("%w: create scratch nonce: %w", ErrPublication, err)
	}
	hexDigest := request.Key.Digest[len("sha256:"):]
	scratch, err := store.fs.MkdirTemp(store.scratchRoot, ".partial-"+hexDigest[:12]+"-"+nonce[:8]+"-")
	if err != nil {
		return PublishResult{}, fmt.Errorf("%w: create scratch: %w", ErrPublication, err)
	}
	scratchOwned := true
	defer func() {
		if !scratchOwned {
			return
		}
		if cleanupErr := errors.Join(store.fs.RemoveAll(scratch), store.fs.SyncDir(store.scratchRoot)); cleanupErr != nil {
			result = PublishResult{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean scratch: %w", ErrPublication, cleanupErr))
		}
	}()

	scratchPublished := filepath.Join(scratch, publishedName)
	scratchLeases := filepath.Join(scratch, leasesName)
	if err := store.fs.Mkdir(scratchPublished, 0o700); err != nil {
		return PublishResult{}, fmt.Errorf("%w: create scratch publication: %w", ErrPublication, err)
	}
	if err := store.fs.Mkdir(scratchLeases, 0o700); err != nil {
		return PublishResult{}, fmt.Errorf("%w: create scratch lease namespace: %w", ErrPublication, err)
	}
	payloadPath := filepath.Join(scratchPublished, payloadName)
	payload, err := store.fs.OpenFile(payloadPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return PublishResult{}, fmt.Errorf("%w: create payload: %w", ErrPublication, err)
	}
	actualHash, actualSize, copyErr := copyAndHash(ctx, payload, request.Payload, request.ExpectedSize)
	var syncErr error
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	if copyErr == nil {
		syncErr = payload.Sync()
	}
	closeErr := payload.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return PublishResult{}, fmt.Errorf("%w: write payload: %w", ErrPublication, err)
	}
	if actualHash != request.ExpectedHash || actualSize != request.ExpectedSize {
		return PublishResult{}, fmt.Errorf("%w: payload got %s/%d, want %s/%d", ErrInvalidRequest, actualHash, actualSize, request.ExpectedHash, request.ExpectedSize)
	}
	if err := store.fs.Chmod(payloadPath, 0o400); err != nil {
		return PublishResult{}, fmt.Errorf("%w: protect payload: %w", ErrPublication, err)
	}
	now := store.clock.Now().UTC()
	if now.IsZero() || now.UnixNano() <= 0 {
		return PublishResult{}, fmt.Errorf("%w: clock returned invalid publication time", ErrConfiguration)
	}
	manifest, err := newEntryManifest(request.Key, actualHash, actualSize, now.UnixNano())
	if err != nil {
		return PublishResult{}, err
	}
	manifestRaw, err := canonicalEntryManifest(manifest)
	if err != nil {
		return PublishResult{}, err
	}
	if err := writeDurableFile(store.fs, filepath.Join(scratchPublished, manifestName), manifestRaw, 0o400); err != nil {
		return PublishResult{}, fmt.Errorf("%w: write manifest: %w", ErrPublication, err)
	}
	sentinelRaw := completeSentinel(manifestRaw)
	if err := writeDurableFile(store.fs, filepath.Join(scratchPublished, completeName), sentinelRaw, 0o400); err != nil {
		return PublishResult{}, fmt.Errorf("%w: write completion sentinel: %w", ErrPublication, err)
	}
	if err := errors.Join(
		store.fs.SyncDir(scratchPublished),
		store.fs.SyncDir(scratchLeases),
		store.fs.SyncDir(scratch),
		store.fs.SyncDir(store.scratchRoot),
	); err != nil {
		return PublishResult{}, fmt.Errorf("%w: sync complete scratch entry: %w", ErrPublication, err)
	}
	verifiedFile, verifiedManifest, verifiedManifestRaw, verifiedSentinelRaw, err := store.verifyDirectory(ctx, request.Key, scratchPublished)
	if verifiedFile != nil {
		err = errors.Join(err, verifiedFile.Close())
	}
	if err != nil {
		return PublishResult{}, fmt.Errorf("%w: verify scratch: %w", ErrPublication, err)
	}
	if verifiedManifest.PayloadHash != actualHash || verifiedManifest.PayloadSize != actualSize ||
		!bytes.Equal(verifiedManifestRaw, manifestRaw) || !bytes.Equal(verifiedSentinelRaw, sentinelRaw) {
		return PublishResult{}, fmt.Errorf("%w: scratch identity drift", ErrPublication)
	}

	store.namespaceMu.Lock()
	defer store.namespaceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return PublishResult{}, err
	}
	result, moved, err := store.commitScratchLocked(ctx, scratch, manifest, manifestRaw, sentinelRaw)
	if moved {
		scratchOwned = false
	}
	return result, err
}

func (store *Store) commitScratchLocked(
	ctx context.Context,
	scratch string,
	manifest entryManifest,
	manifestRaw, sentinelRaw []byte,
) (result PublishResult, moved bool, returnErr error) {
	shard, entryPath, publishedPath, err := store.keyPaths(manifest.StageKey)
	if err != nil {
		return PublishResult{}, false, err
	}
	if err := store.fs.Mkdir(shard, 0o700); err != nil && !isExist(err) {
		return PublishResult{}, false, fmt.Errorf("%w: create key shard: %w", ErrPublication, err)
	}
	shardInfo, err := store.fs.Lstat(shard)
	if err != nil || !shardInfo.IsDir() || shardInfo.Mode()&os.ModeSymlink != 0 {
		return PublishResult{}, false, errors.Join(fmt.Errorf("%w: key shard is not a non-symlink directory", ErrPublication), err)
	}
	if err := store.fs.SyncDir(store.entriesRoot); err != nil {
		return PublishResult{}, false, fmt.Errorf("%w: sync key-shard parent: %w", ErrPublication, err)
	}
	entry := publicEntry(manifest, len(manifestRaw), len(sentinelRaw))
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return PublishResult{}, false, err
		}
		_, pathErr := store.fs.Lstat(entryPath)
		if pathErr == nil {
			existing, stateErr := store.inspectExistingLocked(ctx, manifest.StageKey, entryPath, publishedPath)
			if stateErr == nil {
				if existing.PayloadHash != manifest.PayloadHash || existing.PayloadSize != manifest.PayloadSize {
					return PublishResult{}, false, fmt.Errorf("%w: %s already binds %s/%d, attempted %s/%d", ErrCollision,
						manifest.StageKey.Digest, existing.PayloadHash, existing.PayloadSize, manifest.PayloadHash, manifest.PayloadSize)
				}
				if syncErr := errors.Join(store.fs.SyncDir(publishedPath), store.fs.SyncDir(entryPath), store.fs.SyncDir(shard)); syncErr != nil {
					return PublishResult{Disposition: AlreadyPresent, Entry: existing}, false,
						fmt.Errorf("%w: resync duplicate entry: %w", ErrDurabilityUncertain, syncErr)
				}
				return PublishResult{Disposition: AlreadyPresent, Entry: existing}, false, nil
			}
			if errors.Is(stateErr, ErrPublicationBusy) {
				return PublishResult{}, false, stateErr
			}
			if !errors.Is(stateErr, ErrCorrupt) && !errors.Is(stateErr, ErrMiss) {
				return PublishResult{}, false, stateErr
			}
			if _, quarantineErr := store.quarantineLocked(entryPath, manifest.StageKey.Digest); quarantineErr != nil && !isNotExist(quarantineErr) {
				return PublishResult{}, false, errors.Join(stateErr, quarantineErr)
			}
			continue
		}
		if !isNotExist(pathErr) {
			return PublishResult{}, false, fmt.Errorf("%w: inspect key slot: %w", ErrPublication, pathErr)
		}
		if err := store.fs.Rename(scratch, entryPath); err != nil {
			if _, inspectErr := store.fs.Lstat(entryPath); inspectErr == nil {
				continue
			}
			return PublishResult{}, false, fmt.Errorf("%w: atomic no-replace entry publish: %w", ErrPublication, err)
		}
		if err := errors.Join(store.fs.SyncDir(entryPath), store.fs.SyncDir(shard), store.fs.SyncDir(store.scratchRoot)); err != nil {
			return PublishResult{Disposition: Published, Entry: entry}, true,
				fmt.Errorf("%w: sync visible publication: %w", ErrDurabilityUncertain, err)
		}
		return PublishResult{Disposition: Published, Entry: entry}, true, nil
	}
	return PublishResult{}, false, fmt.Errorf("%w: key slot changed repeatedly", ErrPublicationBusy)
}

func (store *Store) inspectExistingLocked(ctx context.Context, value cachekey.Key, entryPath, publishedPath string) (Entry, error) {
	if err := store.validateDirectory(entryPath); err != nil {
		return Entry{}, err
	}
	if _, err := store.fs.Lstat(publishedPath); err == nil {
		file, manifest, manifestRaw, sentinelRaw, verifyErr := store.verifyDirectory(ctx, value, publishedPath)
		if file != nil {
			verifyErr = errors.Join(verifyErr, file.Close())
		}
		if verifyErr != nil {
			return Entry{}, verifyErr
		}
		return publicEntry(manifest, len(manifestRaw), len(sentinelRaw)), nil
	} else if !isNotExist(err) {
		return Entry{}, fmt.Errorf("%w: inspect published directory: %w", ErrCorrupt, err)
	}
	reservationPath := filepath.Join(entryPath, reservationName)
	if raw, reservationErr := readSmallFile(store.fs, reservationPath, maxReservationBytes); reservationErr == nil {
		if valueOnDisk, decodeErr := decodeReservation(raw); decodeErr == nil && valueOnDisk.StageKey == value {
			return Entry{}, fmt.Errorf("%w: legacy reservation %s has unknown owner", ErrPublicationBusy, valueOnDisk.Nonce)
		}
		return Entry{}, fmt.Errorf("%w: malformed legacy reservation has unknown owner", ErrPublicationBusy)
	} else if !isNotExist(reservationErr) {
		return Entry{}, errors.Join(fmt.Errorf("%w: unreadable legacy reservation has unknown owner", ErrPublicationBusy), reservationErr)
	}
	return Entry{}, fmt.Errorf("%w: incomplete key slot has unknown owner", ErrPublicationBusy)
}

func (store *Store) verifyDirectory(
	ctx context.Context,
	value cachekey.Key,
	directory string,
) (File, entryManifest, []byte, []byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, entryManifest{}, nil, nil, err
	}
	directoryInfo, err := store.fs.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return nil, entryManifest{}, nil, nil, errors.Join(fmt.Errorf("%w: published entry is not a non-symlink directory", ErrCorrupt), err)
	}
	sentinelRaw, err := readSmallFile(store.fs, filepath.Join(directory, completeName), 512)
	if err != nil {
		return nil, entryManifest{}, nil, nil, fmt.Errorf("%w: completion sentinel: %w", ErrCorrupt, err)
	}
	manifestRaw, err := readSmallFile(store.fs, filepath.Join(directory, manifestName), maxManifestBytes)
	if err != nil {
		return nil, entryManifest{}, nil, nil, fmt.Errorf("%w: manifest: %w", ErrCorrupt, err)
	}
	if err := validateCompleteSentinel(sentinelRaw, manifestRaw); err != nil {
		return nil, entryManifest{}, nil, nil, err
	}
	manifest, err := decodeEntryManifest(manifestRaw)
	if err != nil {
		return nil, entryManifest{}, nil, nil, err
	}
	if manifest.StageKey != value {
		return nil, entryManifest{}, nil, nil, fmt.Errorf("%w: manifest stage key mismatch", ErrCorrupt)
	}
	payloadPath := filepath.Join(directory, payloadName)
	namedInfo, err := store.fs.Lstat(payloadPath)
	if err != nil || !namedInfo.Mode().IsRegular() || namedInfo.Mode()&os.ModeSymlink != 0 {
		return nil, entryManifest{}, nil, nil, errors.Join(fmt.Errorf("%w: payload name is not a regular file", ErrCorrupt), err)
	}
	payload, err := store.fs.Open(payloadPath)
	if err != nil {
		return nil, entryManifest{}, nil, nil, fmt.Errorf("%w: open payload: %w", ErrCorrupt, err)
	}
	openedInfo, err := payload.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(namedInfo, openedInfo) {
		closeErr := payload.Close()
		return nil, entryManifest{}, nil, nil, errors.Join(fmt.Errorf("%w: payload changed while opening", ErrCorrupt), err, closeErr)
	}
	if openedInfo.Size() != manifest.PayloadSize {
		closeErr := payload.Close()
		return nil, entryManifest{}, nil, nil, errors.Join(
			fmt.Errorf("%w: payload stat size %d, manifest %d", ErrCorrupt, openedInfo.Size(), manifest.PayloadSize), closeErr)
	}
	hash, size, err := hashFile(ctx, payload, manifest.PayloadSize)
	if err != nil || hash != manifest.PayloadHash || size != manifest.PayloadSize {
		closeErr := payload.Close()
		return nil, entryManifest{}, nil, nil, errors.Join(fmt.Errorf("%w: payload got %s/%d, manifest %s/%d", ErrCorrupt,
			hash, size, manifest.PayloadHash, manifest.PayloadSize), err, closeErr)
	}
	return payload, manifest, manifestRaw, sentinelRaw, nil
}

func (store *Store) quarantineLocked(entryPath, digest string) (string, error) {
	name := digest[len("sha256:"):]
	var slot string
	for attempt := 0; attempt < 4; attempt++ {
		nonce, err := store.nonce()
		if err != nil {
			return "", fmt.Errorf("%w: quarantine nonce: %w", ErrPublication, err)
		}
		slot = filepath.Join(store.quarantineRoot, name+"."+fmt.Sprintf("%d", store.clock.Now().UTC().UnixNano())+"."+nonce)
		if err := store.fs.Mkdir(slot, 0o700); err == nil {
			break
		} else if !isExist(err) {
			return "", err
		}
		slot = ""
	}
	if slot == "" {
		return "", fmt.Errorf("%w: could not reserve unique quarantine slot", ErrPublication)
	}
	target := filepath.Join(slot, "entry")
	if err := store.fs.SyncDir(store.quarantineRoot); err != nil {
		cleanupErr := errors.Join(store.fs.RemoveAll(slot), store.fs.SyncDir(store.quarantineRoot))
		return "", errors.Join(fmt.Errorf("%w: sync quarantine reservation: %w", ErrPublication, err), cleanupErr)
	}
	if err := store.fs.Rename(entryPath, target); err != nil {
		cleanupErr := errors.Join(store.fs.RemoveAll(slot), store.fs.SyncDir(store.quarantineRoot))
		return "", errors.Join(err, cleanupErr)
	}
	shard := filepath.Dir(entryPath)
	if err := errors.Join(store.fs.SyncDir(shard), store.fs.SyncDir(slot), store.fs.SyncDir(store.quarantineRoot)); err != nil {
		return target, fmt.Errorf("%w: sync quarantine: %w", ErrDurabilityUncertain, err)
	}
	return target, nil
}

func fileExists(filesystem FileSystem, path string) (bool, error) {
	_, err := filesystem.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}
