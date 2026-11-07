package cas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

// GCPolicy defines high/low logical-byte watermarks.
type GCPolicy struct {
	// GC starts only when valid entry charges exceed MaxBytes. Once started, it removes oldest
	// entries until charge is at most TargetBytes. TargetBytes may be zero and must not exceed
	// MaxBytes.
	MaxBytes    uint64
	TargetBytes uint64
}

// GCReport records deterministic deletion order and lease/integrity decisions.
type GCReport struct {
	BeforeBytes       uint64
	AfterBytes        uint64
	ScannedEntries    int
	DeletedDigests    []string
	SkippedActive     []string
	QuarantinedPaths  []string
	ExpiredLeaseCount int
}

type gcCandidate struct {
	entryPath  string
	entry      Entry
	generation string
}

type gcClaim struct {
	SchemaVersion   int          `json:"schema_version"`
	Profile         string       `json:"profile"`
	StageKey        cachekey.Key `json:"stage_key"`
	Nonce           string       `json:"nonce"`
	Phase           string       `json:"phase"`
	EntryGeneration string       `json:"entry_generation"`
	CreatedUnixNano int64        `json:"created_unix_nano"`
	ExpiresUnixNano int64        `json:"expires_unix_nano"`
}

const (
	gcClaimSchemaVersion = 1
	gcClaimProfileV1     = "cutly.cache.gc-claim.v1"
	maxGCClaimBytes      = 8 << 10
	gcClaimRecordName    = "claim.json"
	gcClaimPhaseAcquire  = "acquire"
	gcClaimPhaseCommit   = "commit"
)

// GC performs deterministic oldest-first quota collection. An entry is renamed out of the reader
// namespace only after a generation-bound exclusive claim is visible, a second lease scan proves
// that no active reader exists, and the claim has durably entered its non-expiring commit phase.
// Expired or malformed lease files never pin storage indefinitely; unknown claims do fail closed.
func (store *Store) GC(ctx context.Context, policy GCPolicy) (GCReport, error) {
	if store == nil || store.fs == nil || ctx == nil {
		return GCReport{}, fmt.Errorf("%w: nil store or context", ErrInvalidRequest)
	}
	if policy.MaxBytes == 0 || policy.TargetBytes > policy.MaxBytes {
		return GCReport{}, fmt.Errorf("%w: GC requires max>0 and target<=max", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return GCReport{}, err
	}
	store.namespaceMu.Lock()
	defer store.namespaceMu.Unlock()
	candidates, report, err := store.scanCandidatesLocked(ctx)
	if err != nil {
		return report, err
	}
	if report.BeforeBytes <= policy.MaxBytes {
		report.AfterBytes = report.BeforeBytes
		return report, nil
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].entry.CreatedUnixNano != candidates[right].entry.CreatedUnixNano {
			return candidates[left].entry.CreatedUnixNano < candidates[right].entry.CreatedUnixNano
		}
		if candidates[left].entry.Key.Digest != candidates[right].entry.Key.Digest {
			return candidates[left].entry.Key.Digest < candidates[right].entry.Key.Digest
		}
		return candidates[left].entryPath < candidates[right].entryPath
	})
	remaining := report.BeforeBytes
	for _, candidate := range candidates {
		if remaining <= policy.TargetBytes {
			break
		}
		if err := ctx.Err(); err != nil {
			report.AfterBytes = remaining
			return report, err
		}
		claimNonce, claimErr := store.createGCClaimLocked(candidate)
		if claimErr != nil {
			report.AfterBytes = remaining
			return report, claimErr
		}
		if claimNonce == "" {
			report.SkippedActive = append(report.SkippedActive, candidate.entry.Key.Digest)
			continue
		}
		active, expired, leaseErr := store.scanLeasesLocked(ctx, candidate.entryPath, candidate.entry.Key)
		report.ExpiredLeaseCount += expired
		if leaseErr != nil {
			cleanupErr := store.removeGCClaimIfOwnedLocked(candidate, claimNonce)
			report.AfterBytes = remaining
			return report, errors.Join(leaseErr, cleanupErr)
		}
		if active {
			if err := store.removeGCClaimIfOwnedLocked(candidate, claimNonce); err != nil {
				report.AfterBytes = remaining
				return report, err
			}
			report.SkippedActive = append(report.SkippedActive, candidate.entry.Key.Digest)
			continue
		}
		if err := ctx.Err(); err != nil {
			report.AfterBytes = remaining
			return report, errors.Join(err, store.removeGCClaimIfOwnedLocked(candidate, claimNonce))
		}
		if err := store.commitGCClaimLocked(candidate, claimNonce); err != nil {
			report.AfterBytes = remaining
			return report, err
		}
		if err := store.deleteClaimedEntryLocked(candidate, claimNonce); err != nil {
			report.AfterBytes = remaining
			return report, err
		}
		if candidate.entry.ChargeBytes > remaining {
			remaining = 0
		} else {
			remaining -= candidate.entry.ChargeBytes
		}
		report.DeletedDigests = append(report.DeletedDigests, candidate.entry.Key.Digest)
	}
	report.AfterBytes = remaining
	if remaining > policy.TargetBytes {
		return report, fmt.Errorf("%w: remaining=%d target=%d", ErrQuotaUnsatisfied, remaining, policy.TargetBytes)
	}
	return report, nil
}

func (store *Store) scanCandidatesLocked(ctx context.Context) ([]gcCandidate, GCReport, error) {
	var candidates []gcCandidate
	report := GCReport{}
	shards, err := store.fs.ReadDir(store.entriesRoot)
	if err != nil {
		return nil, report, fmt.Errorf("%w: read entry shards: %w", ErrPublication, err)
	}
	sort.Slice(shards, func(left, right int) bool { return shards[left].Name() < shards[right].Name() })
	for _, shard := range shards {
		if err := ctx.Err(); err != nil {
			return nil, report, err
		}
		if !shard.IsDir() || !canonicalHexName(shard.Name(), 2) {
			continue
		}
		shardPath := filepath.Join(store.entriesRoot, shard.Name())
		entries, err := store.fs.ReadDir(shardPath)
		if err != nil {
			return nil, report, fmt.Errorf("%w: read shard %s: %w", ErrPublication, shard.Name(), err)
		}
		sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
		for _, child := range entries {
			if err := ctx.Err(); err != nil {
				return nil, report, err
			}
			if !canonicalHexName(child.Name(), 64) || child.Name()[:2] != shard.Name() {
				continue
			}
			report.ScannedEntries++
			entryPath := filepath.Join(shardPath, child.Name())
			digest := "sha256:" + child.Name()
			if !child.IsDir() {
				path, quarantineErr := store.quarantineLocked(entryPath, digest)
				if path != "" {
					report.QuarantinedPaths = append(report.QuarantinedPaths, path)
				}
				if quarantineErr != nil {
					return nil, report, quarantineErr
				}
				continue
			}
			publishedPath := filepath.Join(entryPath, publishedName)
			if _, err := store.fs.Lstat(publishedPath); err != nil {
				if isNotExist(err) {
					continue
				}
				return nil, report, err
			}
			manifestRaw, manifestErr := readSmallFile(store.fs, filepath.Join(publishedPath, manifestName), maxManifestBytes)
			var manifest entryManifest
			if manifestErr == nil {
				manifest, manifestErr = decodeEntryManifest(manifestRaw)
			}
			if manifestErr == nil {
				pathName, pathErr := stageKeyPathName(manifest.StageKey)
				if pathErr != nil || pathName != child.Name() {
					manifestErr = errors.Join(fmt.Errorf("%w: entry path identity mismatch", ErrCorrupt), pathErr)
				}
			}
			if manifestErr == nil {
				file, verified, verifiedRaw, sentinelRaw, verifyErr := store.verifyDirectory(ctx, manifest.StageKey, publishedPath)
				if file != nil {
					verifyErr = errors.Join(verifyErr, file.Close())
				}
				if verifyErr == nil {
					entry := publicEntry(verified, len(verifiedRaw), len(sentinelRaw))
					candidates = append(candidates, gcCandidate{
						entryPath: entryPath, entry: entry, generation: entryGeneration(verifiedRaw),
					})
					var ok bool
					report.BeforeBytes, ok = checkedAddUint64(report.BeforeBytes, entry.ChargeBytes)
					if !ok {
						return nil, report, fmt.Errorf("%w: quota charge overflow", ErrInvalidRequest)
					}
					continue
				}
				manifestErr = verifyErr
			}
			if errors.Is(manifestErr, context.Canceled) || errors.Is(manifestErr, context.DeadlineExceeded) {
				return nil, report, manifestErr
			}
			path, quarantineErr := store.quarantineLocked(entryPath, digest)
			if path != "" {
				report.QuarantinedPaths = append(report.QuarantinedPaths, path)
			}
			if quarantineErr != nil {
				return nil, report, errors.Join(manifestErr, quarantineErr)
			}
		}
	}
	return candidates, report, nil
}

func (store *Store) createGCClaimLocked(candidate gcCandidate) (claimNonce string, returnErr error) {
	claimPath := filepath.Join(candidate.entryPath, gcClaimName)
	if active, err := store.gcClaimActiveLocked(candidate.entryPath); err != nil || active {
		return "", err
	}
	current, err := store.gcCandidateCurrentLocked(candidate)
	if err != nil || !current {
		return "", errors.Join(fmt.Errorf("%w: GC candidate changed before claim", ErrPublicationBusy), err)
	}
	if err := store.fs.Mkdir(claimPath, 0o700); err != nil {
		if isExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("%w: reserve GC claim: %w", ErrPublication, err)
	}
	claimOwned := true
	defer func() {
		if claimOwned {
			cleanupErr := errors.Join(store.fs.RemoveAll(claimPath), store.fs.SyncDir(candidate.entryPath))
			if cleanupErr != nil {
				claimNonce = ""
				returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean failed GC claim: %w", ErrPublication, cleanupErr))
			}
		}
	}()
	nonce, err := store.nonce()
	if err != nil {
		return "", err
	}
	now := store.clock.Now().UTC()
	value := gcClaim{
		SchemaVersion: gcClaimSchemaVersion, Profile: gcClaimProfileV1, StageKey: candidate.entry.Key,
		Nonce: nonce, Phase: gcClaimPhaseAcquire, EntryGeneration: candidate.generation,
		CreatedUnixNano: now.UnixNano(), ExpiresUnixNano: now.Add(store.reservationTTL).UnixNano(),
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if err := writeDurableFile(store.fs, filepath.Join(claimPath, gcClaimRecordName), raw, 0o600); err != nil {
		return "", fmt.Errorf("%w: publish GC claim: %w", ErrPublication, err)
	}
	if err := errors.Join(store.fs.SyncDir(claimPath), store.fs.SyncDir(candidate.entryPath)); err != nil {
		return "", fmt.Errorf("%w: sync GC claim: %w", ErrPublication, err)
	}
	current, err = store.gcCandidateCurrentLocked(candidate)
	if err != nil || !current {
		return "", errors.Join(fmt.Errorf("%w: GC candidate changed while claiming", ErrPublicationBusy), err)
	}
	claimOwned = false
	return nonce, nil
}

func (store *Store) gcClaimActiveLocked(entryPath string) (bool, error) {
	claimPath := filepath.Join(entryPath, gcClaimName)
	claimInfo, err := store.fs.Lstat(claimPath)
	if isNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !claimInfo.IsDir() || claimInfo.Mode()&os.ModeSymlink != 0 {
		return true, fmt.Errorf("%w: malformed GC claim node is fail-closed", ErrCorrupt)
	}
	if _, err := store.readGCClaimLocked(entryPath); err != nil {
		// A claim directory is the atomic exclusion boundary. Unknown, legacy, or
		// partially durable owners are never reclaimed by age in v1.
		return true, fmt.Errorf("%w: unreadable GC claim is fail-closed: %w", ErrPublicationBusy, err)
	}
	return true, nil
}

func (store *Store) readGCClaimLocked(entryPath string) (gcClaim, error) {
	raw, err := readSmallFile(store.fs, filepath.Join(entryPath, gcClaimName, gcClaimRecordName), maxGCClaimBytes)
	if err != nil {
		return gcClaim{}, err
	}
	var value gcClaim
	if err := wirejson.Decode(raw, &value); err != nil {
		return gcClaim{}, err
	}
	canonical, err := json.Marshal(value)
	pathName, pathErr := stageKeyPathName(value.StageKey)
	if err != nil || pathErr != nil || !bytes.Equal(raw, canonical) || value.SchemaVersion != gcClaimSchemaVersion ||
		value.Profile != gcClaimProfileV1 || cachekey.ValidateKey(value.StageKey) != nil || !canonicalHexName(value.Nonce, 32) ||
		(value.Phase != gcClaimPhaseAcquire && value.Phase != gcClaimPhaseCommit) || !cachekey.CanonicalSHA256(value.EntryGeneration) ||
		value.CreatedUnixNano <= 0 || value.ExpiresUnixNano <= value.CreatedUnixNano ||
		value.ExpiresUnixNano-value.CreatedUnixNano > MaxReservationTTL.Nanoseconds() ||
		pathName != filepath.Base(entryPath) {
		return gcClaim{}, errors.Join(fmt.Errorf("%w: invalid GC claim", ErrCorrupt), err, pathErr)
	}
	return value, nil
}

func (store *Store) gcClaimOwnedForCommitLocked(candidate gcCandidate, nonce string) (bool, error) {
	value, err := store.readGCClaimLocked(candidate.entryPath)
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if value.StageKey != candidate.entry.Key || value.Nonce != nonce || value.Phase != gcClaimPhaseCommit || value.EntryGeneration != candidate.generation {
		return false, nil
	}
	return store.gcCandidateCurrentLocked(candidate)
}

func (store *Store) commitGCClaimLocked(candidate gcCandidate, nonce string) error {
	value, err := store.readGCClaimLocked(candidate.entryPath)
	if err != nil {
		return err
	}
	if value.StageKey != candidate.entry.Key || value.Nonce != nonce || value.Phase != gcClaimPhaseAcquire || value.EntryGeneration != candidate.generation {
		return fmt.Errorf("%w: GC acquisition claim changed before commit", ErrPublicationBusy)
	}
	current, err := store.gcCandidateCurrentLocked(candidate)
	if err != nil || !current {
		return errors.Join(fmt.Errorf("%w: GC candidate changed before commit", ErrPublicationBusy), err)
	}
	value.Phase = gcClaimPhaseCommit
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	claimPath := filepath.Join(candidate.entryPath, gcClaimName)
	temporary := filepath.Join(claimPath, ".commit-"+nonce)
	if err := store.fs.Remove(temporary); err != nil && !isNotExist(err) {
		return err
	}
	if err := writeDurableFile(store.fs, temporary, raw, 0o600); err != nil {
		return fmt.Errorf("%w: write GC commit phase: %w", ErrPublication, err)
	}
	if err := store.fs.Rename(temporary, filepath.Join(claimPath, gcClaimRecordName)); err != nil {
		cleanupErr := store.fs.Remove(temporary)
		if isNotExist(cleanupErr) {
			cleanupErr = nil
		}
		return errors.Join(fmt.Errorf("%w: publish GC commit phase: %w", ErrPublication, err), cleanupErr)
	}
	if err := errors.Join(store.fs.SyncDir(claimPath), store.fs.SyncDir(candidate.entryPath)); err != nil {
		return fmt.Errorf("%w: sync GC commit phase: %w", ErrDurabilityUncertain, err)
	}
	owned, err := store.gcClaimOwnedForCommitLocked(candidate, nonce)
	if err != nil || !owned {
		return errors.Join(fmt.Errorf("%w: GC commit phase did not bind current entry", ErrPublicationBusy), err)
	}
	return nil
}

func entryGeneration(manifestRaw []byte) string {
	digest := sha256.Sum256(manifestRaw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (store *Store) gcCandidateCurrentLocked(candidate gcCandidate) (bool, error) {
	publishedPath := filepath.Join(candidate.entryPath, publishedName)
	manifestRaw, err := readSmallFile(store.fs, filepath.Join(publishedPath, manifestName), maxManifestBytes)
	if err != nil {
		return false, err
	}
	sentinelRaw, err := readSmallFile(store.fs, filepath.Join(publishedPath, completeName), 512)
	if err != nil {
		return false, err
	}
	if err := validateCompleteSentinel(sentinelRaw, manifestRaw); err != nil {
		return false, err
	}
	manifest, err := decodeEntryManifest(manifestRaw)
	if err != nil {
		return false, err
	}
	if manifest.StageKey != candidate.entry.Key || manifest.PayloadHash != candidate.entry.PayloadHash ||
		manifest.PayloadSize != candidate.entry.PayloadSize || manifest.CreatedUnixNano != candidate.entry.CreatedUnixNano {
		return false, nil
	}
	return entryGeneration(manifestRaw) == candidate.generation, nil
}

func (store *Store) removeGCClaimIfOwnedLocked(candidate gcCandidate, nonce string) error {
	value, err := store.readGCClaimLocked(candidate.entryPath)
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	if value.StageKey != candidate.entry.Key || value.Nonce != nonce || value.Phase != gcClaimPhaseAcquire || value.EntryGeneration != candidate.generation {
		return nil
	}
	return store.removeGCClaimLocked(candidate.entryPath)
}

func (store *Store) removeGCClaimLocked(entryPath string) error {
	err := store.fs.RemoveAll(filepath.Join(entryPath, gcClaimName))
	if err != nil && !isNotExist(err) {
		return err
	}
	if err := store.fs.SyncDir(entryPath); err != nil {
		return err
	}
	return nil
}

func (store *Store) scanLeasesLocked(ctx context.Context, entryPath string, value cachekey.Key) (active bool, expired int, returnErr error) {
	leasesPath := filepath.Join(entryPath, leasesName)
	entries, err := store.fs.ReadDir(leasesPath)
	if err != nil {
		return false, 0, fmt.Errorf("%w: scan leases: %w", ErrPublication, err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	now := store.clock.Now().UTC().UnixNano()
	removed := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return active, expired, err
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".partial-") {
			if err := store.fs.Remove(filepath.Join(leasesPath, name)); err == nil || isNotExist(err) {
				removed = true
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".lease.json") {
			continue
		}
		path := filepath.Join(leasesPath, name)
		raw, readErr := readSmallFile(store.fs, path, maxLeaseBytes)
		var record leaseRecord
		if readErr == nil {
			record, readErr = decodeLease(raw)
		}
		if readErr == nil && record.StageKey == value && record.ExpiresUnixNano > now {
			active = true
			continue
		}
		if err := store.fs.Remove(path); err != nil && !isNotExist(err) {
			return active, expired, err
		}
		removed = true
		expired++
	}
	if removed {
		returnErr = store.fs.SyncDir(leasesPath)
	}
	return active, expired, returnErr
}

func (store *Store) deleteClaimedEntryLocked(candidate gcCandidate, claimNonce string) error {
	var slot string
	for attempt := 0; attempt < 4; attempt++ {
		nonce, err := store.nonce()
		if err != nil {
			return err
		}
		slot = filepath.Join(store.trashRoot, strings.TrimPrefix(candidate.entry.Key.Digest, "sha256:")+"."+nonce)
		if err := store.fs.Mkdir(slot, 0o700); err == nil {
			break
		} else if !isExist(err) {
			return err
		}
		slot = ""
	}
	if slot == "" {
		return fmt.Errorf("%w: could not reserve unique GC trash slot", ErrPublication)
	}
	target := filepath.Join(slot, "entry")
	if err := store.fs.SyncDir(store.trashRoot); err != nil {
		cleanupErr := errors.Join(store.fs.RemoveAll(slot), store.fs.SyncDir(store.trashRoot))
		return errors.Join(fmt.Errorf("%w: sync GC trash reservation: %w", ErrPublication, err), cleanupErr)
	}
	owned, ownershipErr := store.gcClaimOwnedForCommitLocked(candidate, claimNonce)
	if ownershipErr != nil || !owned {
		cleanupErr := errors.Join(store.fs.RemoveAll(slot), store.fs.SyncDir(store.trashRoot))
		return errors.Join(fmt.Errorf("%w: GC claim expired or changed before commit", ErrPublicationBusy), ownershipErr, cleanupErr)
	}
	if err := store.fs.Rename(candidate.entryPath, target); err != nil {
		cleanupErr := errors.Join(store.fs.RemoveAll(slot), store.fs.SyncDir(store.trashRoot))
		return errors.Join(fmt.Errorf("%w: atomically remove GC entry: %w", ErrPublication, err), cleanupErr)
	}
	if err := errors.Join(store.fs.SyncDir(filepath.Dir(candidate.entryPath)), store.fs.SyncDir(slot), store.fs.SyncDir(store.trashRoot)); err != nil {
		return fmt.Errorf("%w: sync GC rename: %w", ErrDurabilityUncertain, err)
	}
	if err := store.fs.RemoveAll(slot); err != nil {
		return fmt.Errorf("%w: remove GC trash: %w", ErrPublication, err)
	}
	if err := store.fs.SyncDir(store.trashRoot); err != nil {
		return fmt.Errorf("%w: sync GC trash: %w", ErrDurabilityUncertain, err)
	}
	return nil
}

func canonicalHexName(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
