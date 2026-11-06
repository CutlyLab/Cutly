package cas

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
)

const (
	PublicationOverheadBytes = uint64(64 << 10)
	DefaultLeaseTTL          = 30 * time.Second
	DefaultReservationTTL    = 2 * time.Minute
	MinLeaseTTL              = time.Second
	MinReservationTTL        = time.Second
	MaxLeaseTTL              = 24 * time.Hour
	MaxReservationTTL        = 24 * time.Hour
)

// Clock makes lease expiry, deterministic GC ordering, and claim timestamps testable. Ownership
// records are never reclaimed solely because this clock passes their nominal expiry.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function into Clock.
type ClockFunc func() time.Time

func (function ClockFunc) Now() time.Time { return function() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Options controls only local persistence policy. FileSystem and Clock default to their OS
// implementations. MinFreeBytes is retained after the conservative publication overhead.
type Options struct {
	FileSystem     FileSystem
	Clock          Clock
	LeaseTTL       time.Duration
	ReservationTTL time.Duration
	MinFreeBytes   uint64
}

// Store is safe for concurrent use. namespaceMu serializes short namespace transitions and full
// read verification within one process; lease/GC claim files provide the cross-process handshake.
type Store struct {
	root           string
	entriesRoot    string
	scratchRoot    string
	quarantineRoot string
	trashRoot      string
	fs             FileSystem
	clock          Clock
	leaseTTL       time.Duration
	reservationTTL time.Duration
	minFreeBytes   uint64
	nonce          func() (string, error)
	namespaceMu    sync.Mutex
}

// New initializes a local CAS root, requires its parent to exist, and rejects a symlink root.
func New(root string, options Options) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return nil, fmt.Errorf("%w: root must be a clean absolute non-volume-root path", ErrConfiguration)
	}
	filesystem := options.FileSystem
	if filesystem == nil {
		filesystem = OSFileSystem{}
	}
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	leaseTTL := options.LeaseTTL
	if leaseTTL == 0 {
		leaseTTL = DefaultLeaseTTL
	}
	reservationTTL := options.ReservationTTL
	if reservationTTL == 0 {
		reservationTTL = DefaultReservationTTL
	}
	if leaseTTL < MinLeaseTTL || leaseTTL > MaxLeaseTTL || reservationTTL < MinReservationTTL || reservationTTL > MaxReservationTTL {
		return nil, fmt.Errorf("%w: lease and reservation TTLs must be within [1s,24h]", ErrConfiguration)
	}
	parent := filepath.Dir(root)
	parentInfo, err := filesystem.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: root parent must be an existing non-symlink directory", ErrConfiguration), err)
	}
	if err := filesystem.Mkdir(root, 0o700); err != nil && !isExist(err) {
		return nil, fmt.Errorf("%w: create root: %w", ErrConfiguration, err)
	}
	rootInfo, err := filesystem.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: root must be a non-symlink directory: %w", ErrConfiguration, err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: root must be a non-symlink directory", ErrConfiguration)
	}
	store := &Store{
		root: root, entriesRoot: filepath.Join(root, "entries"), scratchRoot: filepath.Join(root, "scratch"),
		quarantineRoot: filepath.Join(root, "quarantine"), trashRoot: filepath.Join(root, "trash"),
		fs: filesystem, clock: clock, leaseTTL: leaseTTL, reservationTTL: reservationTTL,
		minFreeBytes: options.MinFreeBytes, nonce: randomNonce,
	}
	for _, directory := range []string{store.entriesRoot, store.scratchRoot, store.quarantineRoot, store.trashRoot} {
		if err := filesystem.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("%w: initialize %s: %w", ErrConfiguration, filepath.Base(directory), err)
		}
		info, statErr := filesystem.Lstat(directory)
		if statErr != nil {
			return nil, fmt.Errorf("%w: inspect %s: %w", ErrConfiguration, filepath.Base(directory), statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s is not a non-symlink directory", ErrConfiguration, filepath.Base(directory))
		}
	}
	if err := errors.Join(filesystem.SyncDir(root), filesystem.SyncDir(parent)); err != nil {
		return nil, fmt.Errorf("%w: sync root and parent: %w", ErrConfiguration, err)
	}
	return store, nil
}

// DiskBudget is the exact result of a preflight check.
type DiskBudget struct {
	AvailableBytes uint64
	RequiredBytes  uint64
	PayloadBytes   uint64
	OverheadBytes  uint64
	ReserveBytes   uint64
}

// Preflight rejects work before scratch creation when the advertised free space cannot hold the
// complete payload, conservative metadata overhead, and configured reserve.
func (store *Store) Preflight(ctx context.Context, payloadBytes int64) (DiskBudget, error) {
	if store == nil || store.fs == nil || ctx == nil {
		return DiskBudget{}, fmt.Errorf("%w: nil store or context", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return DiskBudget{}, err
	}
	if payloadBytes < 0 {
		return DiskBudget{}, fmt.Errorf("%w: negative payload size", ErrInvalidRequest)
	}
	payload := uint64(payloadBytes)
	required, ok := checkedAddUint64(payload, PublicationOverheadBytes)
	if ok {
		required, ok = checkedAddUint64(required, store.minFreeBytes)
	}
	if !ok {
		return DiskBudget{}, &DiskError{Required: ^uint64(0), Cause: errors.New("required bytes overflow")}
	}
	available, err := store.fs.AvailableBytes(store.root)
	if err != nil {
		return DiskBudget{}, fmt.Errorf("%w: inspect available bytes: %w", ErrPublication, err)
	}
	budget := DiskBudget{
		AvailableBytes: available, RequiredBytes: required, PayloadBytes: payload,
		OverheadBytes: PublicationOverheadBytes, ReserveBytes: store.minFreeBytes,
	}
	if available < required {
		return budget, &DiskError{Available: available, Required: required, Cause: errors.New("preflight threshold not met")}
	}
	return budget, nil
}

func (store *Store) keyPaths(value cachekey.Key) (shard, entry, published string, err error) {
	pathName, pathErr := stageKeyPathName(value)
	if pathErr != nil {
		return "", "", "", fmt.Errorf("%w: %w", ErrInvalidRequest, pathErr)
	}
	shard = filepath.Join(store.entriesRoot, pathName[:2])
	entry = filepath.Join(shard, pathName)
	published = filepath.Join(entry, publishedName)
	return shard, entry, published, nil
}

func stageKeyPathName(value cachekey.Key) (string, error) {
	if err := cachekey.ValidateKey(value); err != nil {
		return "", err
	}
	identity := struct {
		Profile  string       `json:"profile"`
		StageKey cachekey.Key `json:"stage_key"`
	}{Profile: "cutly.cache.local-key-path.v1", StageKey: value}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (store *Store) validateDirectory(path string) error {
	info, err := store.fs.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a non-symlink directory", ErrCorrupt, path)
	}
	return nil
}

func randomNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func checkedAddUint64(left, right uint64) (uint64, bool) {
	if left > ^uint64(0)-right {
		return 0, false
	}
	return left + right, true
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	return ctx.Err()
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func isExist(err error) bool { return errors.Is(err, fs.ErrExist) }
