package cas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const (
	leaseSchemaVersion = 1
	leaseProfileV1     = "cutly.cache.reader-lease.v1"
	maxLeaseBytes      = 16 << 10
	maxLeaseOwnerBytes = 256
)

type leaseRecord struct {
	SchemaVersion     int          `json:"schema_version"`
	Profile           string       `json:"profile"`
	StageKey          cachekey.Key `json:"stage_key"`
	LeaseID           string       `json:"lease_id"`
	Owner             string       `json:"owner"`
	Generation        uint64       `json:"generation"`
	IssuedUnixNano    int64        `json:"issued_unix_nano"`
	HeartbeatUnixNano int64        `json:"heartbeat_unix_nano"`
	ExpiresUnixNano   int64        `json:"expires_unix_nano"`
}

// LeaseInfo is an immutable snapshot of a reader lease.
type LeaseInfo struct {
	ID              string
	Owner           string
	Generation      uint64
	ExpiresUnixNano int64
}

// Lease is safe for concurrent Heartbeat and Close calls.
type Lease struct {
	store      *Store
	key        cachekey.Key
	entryPath  string
	leasePath  string
	id         string
	owner      string
	mu         sync.Mutex
	generation uint64
	expires    int64
	closed     bool
}

func validateLeaseOwner(owner string) error {
	if owner == "" || len(owner) > maxLeaseOwnerBytes || strings.TrimSpace(owner) != owner || !utf8.ValidString(owner) {
		return fmt.Errorf("%w: lease owner", ErrInvalidRequest)
	}
	for _, character := range owner {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: lease owner contains control characters", ErrInvalidRequest)
		}
	}
	return nil
}

func validateLeaseRecord(value leaseRecord) error {
	if value.SchemaVersion != leaseSchemaVersion || value.Profile != leaseProfileV1 {
		return fmt.Errorf("%w: lease schema/profile", ErrLeaseLost)
	}
	if err := cachekey.ValidateKey(value.StageKey); err != nil {
		return fmt.Errorf("%w: lease stage key: %w", ErrLeaseLost, err)
	}
	if len(value.LeaseID) != 32 {
		return fmt.Errorf("%w: lease ID", ErrLeaseLost)
	}
	for _, character := range value.LeaseID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("%w: lease ID", ErrLeaseLost)
		}
	}
	if err := validateLeaseOwner(value.Owner); err != nil {
		return fmt.Errorf("%w: lease owner", ErrLeaseLost)
	}
	if value.Generation == 0 || value.IssuedUnixNano <= 0 || value.HeartbeatUnixNano < value.IssuedUnixNano || value.ExpiresUnixNano <= value.HeartbeatUnixNano {
		return fmt.Errorf("%w: lease times/generation", ErrLeaseLost)
	}
	if value.ExpiresUnixNano-value.HeartbeatUnixNano > MaxLeaseTTL.Nanoseconds() {
		return fmt.Errorf("%w: lease duration exceeds protocol maximum", ErrLeaseLost)
	}
	return nil
}

func canonicalLease(value leaseRecord) ([]byte, error) {
	if err := validateLeaseRecord(value); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxLeaseBytes {
		return nil, fmt.Errorf("%w: lease record too large", ErrLeaseLost)
	}
	return raw, nil
}

func decodeLease(raw []byte) (leaseRecord, error) {
	if len(raw) == 0 || len(raw) > maxLeaseBytes {
		return leaseRecord{}, fmt.Errorf("%w: lease record size", ErrLeaseLost)
	}
	var value leaseRecord
	if err := wirejson.Decode(raw, &value); err != nil {
		return leaseRecord{}, fmt.Errorf("%w: strict lease decode: %w", ErrLeaseLost, err)
	}
	canonical, err := canonicalLease(value)
	if err != nil {
		return leaseRecord{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return leaseRecord{}, fmt.Errorf("%w: lease record is not canonical", ErrLeaseLost)
	}
	return value, nil
}

func (store *Store) acquireLeaseLocked(ctx context.Context, value cachekey.Key, entryPath, owner string) (returnLease *Lease, returnErr error) {
	if err := validateLeaseOwner(owner); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	claimed, err := store.gcClaimActiveLocked(entryPath)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect GC claim: %w", ErrLeaseLost, err)
	}
	if claimed {
		return nil, fmt.Errorf("%w: GC has claimed entry", ErrLeaseLost)
	}
	leasesPath := filepath.Join(entryPath, leasesName)
	info, err := store.fs.Lstat(leasesPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: lease directory", ErrCorrupt), err)
	}
	id, err := store.nonce()
	if err != nil {
		return nil, fmt.Errorf("%w: lease nonce: %w", ErrLeaseLost, err)
	}
	now := store.clock.Now().UTC()
	record := leaseRecord{
		SchemaVersion: leaseSchemaVersion, Profile: leaseProfileV1, StageKey: value, LeaseID: id, Owner: owner,
		Generation: 1, IssuedUnixNano: now.UnixNano(), HeartbeatUnixNano: now.UnixNano(),
		ExpiresUnixNano: now.Add(store.leaseTTL).UnixNano(),
	}
	raw, err := canonicalLease(record)
	if err != nil {
		return nil, err
	}
	temporary := filepath.Join(leasesPath, ".partial-"+id)
	final := filepath.Join(leasesPath, id+".lease.json")
	if err := writeDurableFile(store.fs, temporary, raw, 0o600); err != nil {
		cleanupErr := store.fs.Remove(temporary)
		if isNotExist(cleanupErr) {
			cleanupErr = nil
		}
		return nil, errors.Join(fmt.Errorf("%w: write lease: %w", ErrLeaseLost, err), cleanupErr)
	}
	if err := store.fs.Rename(temporary, final); err != nil {
		cleanupErr := store.fs.Remove(temporary)
		if isNotExist(cleanupErr) {
			cleanupErr = nil
		}
		return nil, errors.Join(fmt.Errorf("%w: publish lease: %w", ErrLeaseLost, err), cleanupErr)
	}
	cleanup := true
	defer func() {
		if cleanup {
			cleanupErr := store.fs.Remove(final)
			if isNotExist(cleanupErr) {
				cleanupErr = nil
			}
			cleanupErr = errors.Join(cleanupErr, store.fs.SyncDir(leasesPath))
			if cleanupErr != nil {
				returnLease = nil
				returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean failed lease acquire: %w", ErrLeaseLost, cleanupErr))
			}
		}
	}()
	if err := store.fs.SyncDir(leasesPath); err != nil {
		return nil, fmt.Errorf("%w: sync lease: %w", ErrLeaseLost, err)
	}
	claimed, err = store.gcClaimActiveLocked(entryPath)
	if err != nil || claimed {
		return nil, errors.Join(fmt.Errorf("%w: GC raced lease acquire", ErrLeaseLost), err)
	}
	published, err := fileExists(store.fs, filepath.Join(entryPath, publishedName))
	if err != nil || !published {
		return nil, errors.Join(fmt.Errorf("%w: entry moved during lease acquire", ErrLeaseLost), err)
	}
	cleanup = false
	return &Lease{
		store: store, key: value, entryPath: entryPath, leasePath: final, id: id, owner: owner,
		generation: record.Generation, expires: record.ExpiresUnixNano,
	}, nil
}

// Info returns a synchronized snapshot.
func (lease *Lease) Info() LeaseInfo {
	if lease == nil {
		return LeaseInfo{}
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return LeaseInfo{ID: lease.id, Owner: lease.owner, Generation: lease.generation, ExpiresUnixNano: lease.expires}
}

// Heartbeat atomically replaces the lease record and extends it by Store's configured TTL.
func (lease *Lease) Heartbeat(ctx context.Context) error {
	if lease == nil || lease.store == nil || ctx == nil {
		return fmt.Errorf("%w: nil lease or context", ErrLeaseLost)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return ErrLeaseLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store := lease.store
	store.namespaceMu.Lock()
	defer store.namespaceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	claimed, err := store.gcClaimActiveLocked(lease.entryPath)
	if err != nil || claimed {
		return errors.Join(ErrLeaseLost, err)
	}
	raw, err := readSmallFile(store.fs, lease.leasePath, maxLeaseBytes)
	if err != nil {
		return errors.Join(ErrLeaseLost, err)
	}
	current, err := decodeLease(raw)
	if err != nil || current.StageKey != lease.key || current.LeaseID != lease.id || current.Owner != lease.owner || current.Generation != lease.generation {
		return errors.Join(ErrLeaseLost, err)
	}
	now := store.clock.Now().UTC()
	if current.ExpiresUnixNano <= now.UnixNano() {
		return ErrLeaseLost
	}
	current.Generation++
	current.HeartbeatUnixNano = now.UnixNano()
	current.ExpiresUnixNano = now.Add(store.leaseTTL).UnixNano()
	updated, err := canonicalLease(current)
	if err != nil {
		return err
	}
	leasesPath := filepath.Dir(lease.leasePath)
	temporary := filepath.Join(leasesPath, ".partial-"+lease.id+"-heartbeat")
	if err := store.fs.Remove(temporary); err != nil && !isNotExist(err) {
		return errors.Join(ErrLeaseLost, err)
	}
	if err := writeDurableFile(store.fs, temporary, updated, 0o600); err != nil {
		cleanupErr := store.fs.Remove(temporary)
		if isNotExist(cleanupErr) {
			cleanupErr = nil
		}
		return errors.Join(ErrLeaseLost, err, cleanupErr)
	}
	if err := store.fs.Rename(temporary, lease.leasePath); err != nil {
		cleanupErr := store.fs.Remove(temporary)
		if isNotExist(cleanupErr) {
			cleanupErr = nil
		}
		return errors.Join(ErrLeaseLost, err, cleanupErr)
	}
	if err := store.fs.SyncDir(leasesPath); err != nil {
		return errors.Join(ErrLeaseLost, err)
	}
	claimed, err = store.gcClaimActiveLocked(lease.entryPath)
	if err != nil || claimed {
		return errors.Join(ErrLeaseLost, err)
	}
	lease.generation = current.Generation
	lease.expires = current.ExpiresUnixNano
	return nil
}

// Close releases the lease. It is idempotent.
func (lease *Lease) Close() error {
	if lease == nil || lease.store == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return nil
	}
	store := lease.store
	store.namespaceMu.Lock()
	defer store.namespaceMu.Unlock()
	err := lease.releaseLocked()
	lease.closed = true
	return err
}

func (lease *Lease) releaseLocked() error {
	if lease == nil || lease.store == nil {
		return nil
	}
	err := lease.store.fs.Remove(lease.leasePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	leasesPath := filepath.Dir(lease.leasePath)
	if syncErr := lease.store.fs.SyncDir(leasesPath); syncErr != nil && !errors.Is(syncErr, fs.ErrNotExist) {
		return syncErr
	}
	return nil
}
