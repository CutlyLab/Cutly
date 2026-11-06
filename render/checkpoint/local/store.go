package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"unicode"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	locatorProfileV1            = "cutly.render.local-checkpoint-locator.v1"
	completeProfileV1           = "cutly.render.local-checkpoint-complete.v1"
	checkpointName              = "checkpoint.json"
	completeName                = "complete"
	DefaultMaxQuarantineEntries = 4096
	DefaultMaxScratchEntries    = 1024
	MaxQuarantineEntries        = 1 << 20
	MaxScratchEntries           = 1 << 16
	lockPollInterval            = 10 * time.Millisecond
)

var taskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,255}$`)

type Options struct {
	MaxQuarantineEntries int
	MaxScratchEntries    int
}

type Store struct {
	root                 string
	checkpointsRoot      string
	scratchRoot          string
	quarantineRoot       string
	locksRoot            string
	artifacts            *cas.Store
	maxQuarantineEntries int
	maxScratchEntries    int
	fault                func(string) error
}

type locator struct {
	SchemaVersion                int                  `json:"schema_version"`
	Profile                      string               `json:"profile"`
	JobID                        string               `json:"job_id"`
	PlanFingerprint              string               `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string               `json:"execution_manifest_fingerprint"`
	TaskID                       string               `json:"task_id"`
	Stage                        renderapi.Stage      `json:"stage"`
	TaskProfile                  string               `json:"task_profile"`
	CacheKey                     cachekey.Key         `json:"cache_key"`
	FrameRange                   *videoedit.TickRange `json:"frame_range,omitempty"`
	SampleRange                  *videoedit.TickRange `json:"sample_range,omitempty"`
}

var (
	processGatesOnce sync.Once
	processGates     [258]chan struct{}
)

func New(root string, artifacts *cas.Store, options Options) (*Store, error) {
	if artifacts == nil || root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return nil, fmt.Errorf("%w: CAS and a clean absolute non-volume root are required", ErrConfiguration)
	}
	if !lockingSupported() {
		return nil, ErrUnsupported
	}
	maximum := options.MaxQuarantineEntries
	if maximum == 0 {
		maximum = DefaultMaxQuarantineEntries
	}
	if maximum < 1 || maximum > MaxQuarantineEntries {
		return nil, fmt.Errorf("%w: max quarantine entries must be in [1,%d]", ErrConfiguration, MaxQuarantineEntries)
	}
	maximumScratch := options.MaxScratchEntries
	if maximumScratch == 0 {
		maximumScratch = DefaultMaxScratchEntries
	}
	if maximumScratch < 1 || maximumScratch > MaxScratchEntries {
		return nil, fmt.Errorf("%w: max scratch entries must be in [1,%d]", ErrConfiguration, MaxScratchEntries)
	}
	parent := filepath.Dir(root)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: root parent must be an existing non-symlink directory", ErrConfiguration), err)
	}
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: create root: %v", ErrConfiguration, err)
	}
	if err := requireDirectory(root); err != nil {
		return nil, fmt.Errorf("%w: root: %v", ErrConfiguration, err)
	}
	store := &Store{
		root: root, checkpointsRoot: filepath.Join(root, "checkpoints"),
		scratchRoot: filepath.Join(root, "scratch"), quarantineRoot: filepath.Join(root, "quarantine"),
		locksRoot: filepath.Join(root, "locks"), artifacts: artifacts,
		maxQuarantineEntries: maximum, maxScratchEntries: maximumScratch,
	}
	for _, directory := range []string{store.checkpointsRoot, store.scratchRoot, store.quarantineRoot, store.locksRoot} {
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

func (store *Store) Load(ctx context.Context, scope stagedag.Scope, task stagedag.Task) (stagedag.Checkpoint, bool, error) {
	if store == nil || store.artifacts == nil || ctx == nil {
		return stagedag.Checkpoint{}, false, fmt.Errorf("%w: initialized store and context are required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	if err := store.requireNamespaces(); err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	value, err := locatorFromTask(scope, task)
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	name, err := locatorName(value)
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	var result stagedag.Checkpoint
	found := false
	err = store.withLock(ctx, name, func() error {
		path, exists, err := store.entryPath(name, false)
		if err != nil || !exists {
			return err
		}
		checkpoint, raw, err := readCheckpoint(path)
		if err != nil {
			if quarantineErr := store.quarantine(ctx, path, name); quarantineErr != nil {
				return errors.Join(err, quarantineErr)
			}
			return nil
		}
		if !checkpointMatches(value, checkpoint) {
			return fmt.Errorf("%w: locator path contains a different valid checkpoint", ErrCollision)
		}
		canonical, err := stagedag.CanonicalCheckpoint(checkpoint)
		if err != nil || !bytes.Equal(canonical, raw) {
			if quarantineErr := store.quarantine(ctx, path, name); quarantineErr != nil {
				return errors.Join(err, quarantineErr)
			}
			return nil
		}
		reader, err := store.openArtifact(ctx, checkpoint, name)
		if err != nil {
			if artifactRecomputable(err) {
				if quarantineErr := store.quarantine(ctx, path, name); quarantineErr != nil {
					return errors.Join(err, quarantineErr)
				}
				return nil
			}
			return err
		}
		closeErr := reader.Close()
		if closeErr != nil {
			return fmt.Errorf("%w: release CAS verification lease: %v", ErrArtifactUnavailable, closeErr)
		}
		result = cloneCheckpoint(checkpoint)
		found = true
		return nil
	})
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	return result, found, nil
}

func (store *Store) Commit(ctx context.Context, checkpoint stagedag.Checkpoint) (returnErr error) {
	if store == nil || store.artifacts == nil || ctx == nil {
		return fmt.Errorf("%w: initialized store and context are required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.requireNamespaces(); err != nil {
		return err
	}
	raw, err := stagedag.CanonicalCheckpoint(checkpoint)
	if err != nil {
		return fmt.Errorf("%w: checkpoint: %v", ErrInvalidRequest, err)
	}
	frozen, err := stagedag.DecodeCheckpoint(raw)
	if err != nil {
		return fmt.Errorf("%w: freeze checkpoint: %v", ErrInvalidRequest, err)
	}
	value := locatorFromCheckpoint(frozen)
	name, err := locatorName(value)
	if err != nil {
		return err
	}
	returnErr = store.withLock(ctx, name, func() (actionErr error) {
		// Acquire the CAS lease only after the potentially unbounded locator-lock
		// wait. A lease acquired before the wait could expire and let GC remove
		// the artifact before the checkpoint becomes visible.
		reader, err := store.openArtifact(ctx, frozen, name)
		if err != nil {
			return err
		}
		defer func() { actionErr = errors.Join(actionErr, reader.Close()) }()
		path, exists, err := store.entryPath(name, true)
		if err != nil {
			return err
		}
		if exists {
			existing, existingRaw, readErr := readCheckpoint(path)
			if readErr == nil {
				if !checkpointMatches(value, existing) {
					return fmt.Errorf("%w: locator path contains a different valid checkpoint", ErrCollision)
				}
				if bytes.Equal(existingRaw, raw) {
					// A previous attempt may have renamed successfully and failed
					// before syncing either rename parent. Re-establish durability on
					// every exact idempotent retry instead of reporting a false success.
					if err := store.syncPublished(path); err != nil {
						return err
					}
					return store.refreshArtifactLease(ctx, reader)
				}
				return fmt.Errorf("%w: task locator already has a different valid checkpoint", ErrCollision)
			}
			if quarantineErr := store.quarantine(ctx, path, name); quarantineErr != nil {
				return errors.Join(readErr, quarantineErr)
			}
		}
		return store.publish(ctx, path, name, raw, func() error {
			return store.refreshArtifactLease(ctx, reader)
		})
	})
	return returnErr
}

func (store *Store) refreshArtifactLease(ctx context.Context, reader *cas.Reader) error {
	if err := reader.Heartbeat(ctx); err != nil {
		return errors.Join(ErrArtifactUnavailable, err)
	}
	return nil
}

func (store *Store) openArtifact(ctx context.Context, checkpoint stagedag.Checkpoint, name string) (*cas.Reader, error) {
	reader, err := store.artifacts.Open(ctx, checkpoint.CacheKey, "checkpoint-"+name[:24])
	if err != nil {
		if artifactRecomputable(err) {
			return nil, errors.Join(ErrArtifactUnavailable, err)
		}
		return nil, fmt.Errorf("%w: verify CAS artifact: %w", ErrArtifactUnavailable, err)
	}
	if reader.Entry.PayloadHash != checkpoint.Output.ContentHash || reader.Entry.PayloadSize != checkpoint.Output.SizeBytes {
		closeErr := reader.Close()
		return nil, errors.Join(
			fmt.Errorf("%w: %w: CAS artifact got %s/%d, checkpoint claims %s/%d", ErrArtifactUnavailable, ErrArtifactMismatch,
				reader.Entry.PayloadHash, reader.Entry.PayloadSize, checkpoint.Output.ContentHash, checkpoint.Output.SizeBytes),
			closeErr,
		)
	}
	return reader, nil
}

func artifactRecomputable(err error) bool {
	return errors.Is(err, ErrArtifactMismatch) || errors.Is(err, cas.ErrMiss) ||
		errors.Is(err, cas.ErrCorrupt) || errors.Is(err, cas.ErrRecomputeRequired)
}

func (store *Store) publish(ctx context.Context, finalPath, name string, raw []byte, refreshLease func() error) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	scratch, err := store.createScratch(ctx, name)
	if err != nil {
		return fmt.Errorf("checkpoint: create scratch: %w", err)
	}
	scratchOwned := true
	defer func() {
		if !scratchOwned {
			return
		}
		cleanupErr := errors.Join(os.RemoveAll(scratch), syncDirectory(store.scratchRoot))
		if cleanupErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("checkpoint: clean scratch: %w", cleanupErr))
		}
	}()
	if err := writeDurableFile(filepath.Join(scratch, checkpointName), raw); err != nil {
		return err
	}
	if err := writeDurableFile(filepath.Join(scratch, completeName), completion(raw)); err != nil {
		return err
	}
	if err := errors.Join(syncDirectory(scratch), syncDirectory(store.scratchRoot)); err != nil {
		return fmt.Errorf("checkpoint: sync scratch: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Scratch construction and fsync can outlive the CAS lease TTL on a slow
	// disk. Refresh immediately before the visibility transition, and once more
	// before reporting success, so Commit never blesses a checkpoint after GC
	// has claimed its artifact.
	if err := refreshLease(); err != nil {
		return err
	}
	if err := os.Rename(scratch, finalPath); err != nil {
		if _, existingRaw, readErr := readCheckpoint(finalPath); readErr == nil {
			if bytes.Equal(existingRaw, raw) {
				if err := store.syncPublished(finalPath); err != nil {
					return err
				}
				return refreshLease()
			}
			return fmt.Errorf("%w: concurrent checkpoint differs", ErrCollision)
		}
		return fmt.Errorf("checkpoint: atomic no-replace publish: %w", err)
	}
	scratchOwned = false
	if store.fault != nil {
		if err := store.fault("publish.after_rename"); err != nil {
			return errors.Join(ErrDurabilityUncertain, err)
		}
	}
	if err := store.syncPublished(finalPath); err != nil {
		return err
	}
	return refreshLease()
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

func (store *Store) entryPath(name string, createShard bool) (string, bool, error) {
	if err := requireDirectory(store.checkpointsRoot); err != nil {
		return "", false, fmt.Errorf("%w: checkpoint namespace: %v", ErrCorrupt, err)
	}
	shard := filepath.Join(store.checkpointsRoot, name[:2])
	if createShard {
		if err := os.Mkdir(shard, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", false, err
		}
		if err := requireDirectory(shard); err != nil {
			return "", false, fmt.Errorf("%w: checkpoint shard: %v", ErrCorrupt, err)
		}
		if err := syncDirectory(store.checkpointsRoot); err != nil {
			return "", false, err
		}
	} else if err := requireDirectory(shard); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(shard, name), false, nil
		}
		return "", false, fmt.Errorf("%w: checkpoint shard: %v", ErrCorrupt, err)
	}
	path := filepath.Join(shard, name)
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	// The per-locator lock makes it safe for the caller to quarantine a
	// non-directory or symlink entry by renaming the name itself without
	// following it.
	return path, true, nil
}

func (store *Store) quarantine(ctx context.Context, path, name string) error {
	// Admission and rename share a process/OS lock so concurrent corrupt
	// locators cannot race past the cardinality bound.
	return store.withLock(ctx, "quarantine-global", func() error {
		if err := store.admitQuarantine(); err != nil {
			return err
		}
		nonce, err := randomNonce()
		if err != nil {
			return err
		}
		target := filepath.Join(store.quarantineRoot, name+"."+nonce)
		if err := os.Rename(path, target); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("checkpoint: quarantine: %w", err)
		}
		return errors.Join(syncDirectory(filepath.Dir(path)), syncDirectory(store.quarantineRoot))
	})
}

func (store *Store) admitQuarantine() error {
	if err := requireDirectory(store.quarantineRoot); err != nil {
		return fmt.Errorf("%w: quarantine namespace: %v", ErrCorrupt, err)
	}
	directory, err := os.Open(store.quarantineRoot)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(store.maxQuarantineEntries + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(entries) >= store.maxQuarantineEntries {
		return fmt.Errorf("%w: quarantine contains %d entries", ErrLimit, len(entries))
	}
	return nil
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
	returnErr = action()
	return returnErr
}

func processGate(path string) chan struct{} {
	processGatesOnce.Do(func() {
		for index := range processGates {
			processGates[index] = make(chan struct{}, 1)
			processGates[index] <- struct{}{}
		}
	})
	digest := sha256.Sum256([]byte(path))
	if filepath.Base(path) == "quarantine-global.lock" {
		return processGates[256]
	}
	if filepath.Base(path) == "scratch-global.lock" {
		return processGates[257]
	}
	return processGates[int(digest[0])]
}

func (store *Store) lockPath(name string) (string, error) {
	switch name {
	case "quarantine-global", "scratch-global":
		return filepath.Join(store.locksRoot, name+".lock"), nil
	default:
		if len(name) != sha256.Size*2 {
			return "", fmt.Errorf("%w: invalid locator lock identity", ErrInvalidRequest)
		}
		if _, err := hex.DecodeString(name); err != nil {
			return "", fmt.Errorf("%w: invalid locator lock identity", ErrInvalidRequest)
		}
		// Fixed lock striping bounds persistent lock cardinality even when
		// callers probe an unbounded set of valid-but-missing locators.
		return filepath.Join(store.locksRoot, "locator-"+name[:3]+".lock"), nil
	}
}

func (store *Store) createScratch(ctx context.Context, name string) (string, error) {
	var scratch string
	err := store.withLock(ctx, "scratch-global", func() error {
		if err := requireDirectory(store.scratchRoot); err != nil {
			return fmt.Errorf("%w: scratch namespace: %v", ErrCorrupt, err)
		}
		directory, err := os.Open(store.scratchRoot)
		if err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(store.maxScratchEntries + 1)
		if errors.Is(readErr, io.EOF) {
			readErr = nil
		}
		closeErr := directory.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if len(entries) >= store.maxScratchEntries {
			return fmt.Errorf("%w: scratch contains %d entries", ErrLimit, len(entries))
		}
		scratch, err = os.MkdirTemp(store.scratchRoot, ".partial-"+name[:12]+"-")
		return err
	})
	return scratch, err
}

func (store *Store) requireNamespaces() error {
	for _, directory := range []string{store.root, store.checkpointsRoot, store.scratchRoot, store.quarantineRoot, store.locksRoot} {
		if err := requireDirectory(directory); err != nil {
			return fmt.Errorf("%w: namespace %s: %v", ErrCorrupt, filepath.Base(directory), err)
		}
	}
	return nil
}

func locatorFromTask(scope stagedag.Scope, task stagedag.Task) (locator, error) {
	if !validIdentity(scope.JobID) || !cachekey.CanonicalSHA256(scope.PlanFingerprint) ||
		!cachekey.CanonicalSHA256(scope.ExecutionManifestFingerprint) || !taskIDPattern.MatchString(task.ID) {
		return locator{}, fmt.Errorf("%w: scope/task identity", ErrInvalidRequest)
	}
	if err := cachekey.Verify(task.CacheInput, task.CacheKey); err != nil {
		return locator{}, fmt.Errorf("%w: cache key: %v", ErrInvalidRequest, err)
	}
	if task.CacheInput.PlanFingerprint != scope.PlanFingerprint ||
		task.CacheInput.ExecutionManifestFingerprint != scope.ExecutionManifestFingerprint ||
		task.CacheInput.StageProfile != task.Profile || !cacheRangesEqual(task.CacheInput.FrameRange, task.FrameRange) ||
		!cacheRangesEqual(task.CacheInput.SampleRange, task.SampleRange) {
		return locator{}, fmt.Errorf("%w: task cache input is not bound to scope", ErrInvalidRequest)
	}
	wantStage, err := stagedag.StageBindingInput(task.Stage)
	if err != nil {
		return locator{}, fmt.Errorf("%w: stage: %v", ErrInvalidRequest, err)
	}
	stageBindings := 0
	for _, input := range task.CacheInput.Inputs {
		if input.Role == stagedag.StageBindingRole {
			stageBindings++
			if input != wantStage {
				return locator{}, fmt.Errorf("%w: stage cache binding", ErrInvalidRequest)
			}
		}
	}
	if stageBindings != 1 {
		return locator{}, fmt.Errorf("%w: exactly one stage cache binding is required", ErrInvalidRequest)
	}
	return locator{
		SchemaVersion: 1, Profile: locatorProfileV1, JobID: scope.JobID,
		PlanFingerprint: scope.PlanFingerprint, ExecutionManifestFingerprint: scope.ExecutionManifestFingerprint,
		TaskID: task.ID, Stage: task.Stage, TaskProfile: task.Profile, CacheKey: task.CacheKey,
		FrameRange: cloneRange(task.FrameRange), SampleRange: cloneRange(task.SampleRange),
	}, nil
}

func locatorFromCheckpoint(checkpoint stagedag.Checkpoint) locator {
	return locator{
		SchemaVersion: 1, Profile: locatorProfileV1, JobID: checkpoint.JobID,
		PlanFingerprint: checkpoint.PlanFingerprint, ExecutionManifestFingerprint: checkpoint.ExecutionManifestFingerprint,
		TaskID: checkpoint.TaskID, Stage: checkpoint.Stage, TaskProfile: checkpoint.TaskProfile,
		CacheKey: checkpoint.CacheKey, FrameRange: cloneRange(checkpoint.FrameRange), SampleRange: cloneRange(checkpoint.SampleRange),
	}
}

func locatorName(value locator) (string, error) {
	if value.SchemaVersion != 1 || value.Profile != locatorProfileV1 || !validIdentity(value.JobID) ||
		!cachekey.CanonicalSHA256(value.PlanFingerprint) || !cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) ||
		!taskIDPattern.MatchString(value.TaskID) || cachekey.ValidateKey(value.CacheKey) != nil {
		return "", fmt.Errorf("%w: checkpoint locator", ErrInvalidRequest)
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: checkpoint locator: %v", ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func checkpointMatches(value locator, checkpoint stagedag.Checkpoint) bool {
	actual := locatorFromCheckpoint(checkpoint)
	return value.SchemaVersion == actual.SchemaVersion && value.Profile == actual.Profile && value.JobID == actual.JobID &&
		value.PlanFingerprint == actual.PlanFingerprint && value.ExecutionManifestFingerprint == actual.ExecutionManifestFingerprint &&
		value.TaskID == actual.TaskID && value.Stage == actual.Stage && value.TaskProfile == actual.TaskProfile &&
		value.CacheKey == actual.CacheKey && rangesEqual(value.FrameRange, actual.FrameRange) && rangesEqual(value.SampleRange, actual.SampleRange)
}

func validIdentity(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	return true
}

func cacheRangesEqual(left *cachekey.IndexRange, right *videoedit.TickRange) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Start == right.First && left.Count == right.Count
}

func rangesEqual(left, right *videoedit.TickRange) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneRange(value *videoedit.TickRange) *videoedit.TickRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneCheckpoint(value stagedag.Checkpoint) stagedag.Checkpoint {
	value.FrameRange = cloneRange(value.FrameRange)
	value.SampleRange = cloneRange(value.SampleRange)
	value.Output.Receipt = append([]byte(nil), value.Output.Receipt...)
	return value
}

func completion(raw []byte) []byte {
	digest := sha256.Sum256(raw)
	return []byte(completeProfileV1 + "\nsha256:" + hex.EncodeToString(digest[:]) + "\n")
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

func readCheckpoint(path string) (stagedag.Checkpoint, []byte, error) {
	if err := requireDirectory(path); err != nil {
		return stagedag.Checkpoint{}, nil, err
	}
	directory, err := os.Open(path)
	if err != nil {
		return stagedag.Checkpoint{}, nil, err
	}
	entries, readErr := directory.ReadDir(3)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return stagedag.Checkpoint{}, nil, err
	}
	if len(entries) != 2 || !hasEntry(entries, checkpointName) || !hasEntry(entries, completeName) {
		return stagedag.Checkpoint{}, nil, fmt.Errorf("%w: checkpoint directory shape", ErrCorrupt)
	}
	raw, err := readRegularFile(filepath.Join(path, checkpointName), stagedag.MaxCheckpointBytes)
	if err != nil {
		return stagedag.Checkpoint{}, nil, err
	}
	complete, err := readRegularFile(filepath.Join(path, completeName), 256)
	if err != nil || !bytes.Equal(complete, completion(raw)) {
		return stagedag.Checkpoint{}, nil, errors.Join(fmt.Errorf("%w: completion sentinel", ErrCorrupt), err)
	}
	checkpoint, err := stagedag.DecodeCheckpoint(raw)
	if err != nil {
		return stagedag.Checkpoint{}, nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return checkpoint, raw, nil
}

func hasEntry(entries []os.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

func readRegularFile(path string, maximum int) ([]byte, error) {
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() || named.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: named file is not regular", ErrCorrupt), err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(named, opened) {
		return nil, errors.Join(fmt.Errorf("%w: file changed while opening", ErrCorrupt), statErr, file.Close())
	}
	if opened.Size() < 0 || opened.Size() > int64(maximum) {
		return nil, errors.Join(fmt.Errorf("%w: file exceeds %d bytes", ErrLimit, maximum), file.Close())
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	closeErr := file.Close()
	if len(raw) > maximum {
		return nil, errors.Join(fmt.Errorf("%w: file exceeds %d bytes", ErrLimit, maximum), readErr, closeErr)
	}
	return raw, errors.Join(readErr, closeErr)
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is not a non-symlink directory")
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

func errorsJoin(values ...error) error { return errors.Join(values...) }

var _ stagedag.CheckpointStore = (*Store)(nil)
