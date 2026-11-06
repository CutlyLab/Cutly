package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

type fixture struct {
	root       string
	artifacts  *cas.Store
	store      *Store
	plan       stagedag.Plan
	task       stagedag.Task
	scope      stagedag.Scope
	checkpoint stagedag.Checkpoint
	payload    []byte
}

func TestCommitLoadIdempotenceCollisionAndOwnership(t *testing.T) {
	value := newFixture(t, true)
	if checkpoint, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || found || checkpoint.Profile != "" {
		t.Fatalf("initial load = %+v/%v, %v", checkpoint, found, err)
	}
	originalReceipt := append([]byte(nil), value.checkpoint.Output.Receipt...)
	if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
		t.Fatal(err)
	}
	value.checkpoint.Output.Receipt[0] = 'x'
	loaded, found, err := value.store.Load(context.Background(), value.scope, value.task)
	if err != nil || !found || string(loaded.Output.Receipt) != string(originalReceipt) {
		t.Fatalf("load = %+v/%v, %v", loaded, found, err)
	}
	loaded.Output.Receipt[0] = 'x'
	again, found, err := value.store.Load(context.Background(), value.scope, value.task)
	if err != nil || !found || string(again.Output.Receipt) != string(originalReceipt) {
		t.Fatalf("owned reload = %+v/%v, %v", again, found, err)
	}
	value.checkpoint.Output.Receipt = originalReceipt
	if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
		t.Fatalf("idempotent commit = %v", err)
	}
	second, err := stagedag.NewCheckpoint(value.plan, value.task, value.checkpoint.Output, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := value.store.Commit(context.Background(), second); !errors.Is(err, ErrCollision) {
		t.Fatalf("collision = %v", err)
	}
}

func TestCrossStoreConcurrentIdenticalCommit(t *testing.T) {
	value := newFixture(t, true)
	other, err := New(filepath.Join(value.root, "checkpoints"), value.artifacts, Options{})
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{value.store, other}
	start := make(chan struct{})
	errorsCh := make(chan error, 32)
	var wait sync.WaitGroup
	for index := 0; index < cap(errorsCh); index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			errorsCh <- stores[index%len(stores)].Commit(context.Background(), value.checkpoint)
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent commit = %v", err)
		}
	}
	if _, found, err := other.Load(context.Background(), value.scope, value.task); err != nil || !found {
		t.Fatalf("cross-store load found=%v err=%v", found, err)
	}
}

func TestCrossProcessConcurrentIdenticalCommit(t *testing.T) {
	value := newFixture(t, true)
	raw, err := stagedag.CanonicalCheckpoint(value.checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	children := make([]child, 6)
	for index := range children {
		command := exec.CommandContext(ctx, executable, "-test.run=^TestCheckpointHelperProcess$")
		command.Env = append(os.Environ(),
			"CUTLY_CHECKPOINT_HELPER_ROOT="+value.root,
			"CUTLY_CHECKPOINT_HELPER_VALUE="+base64.RawStdEncoding.EncodeToString(raw),
		)
		command.Stdout = &children[index].output
		command.Stderr = &children[index].output
		children[index].command = command
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for index := range children {
		if err := children[index].command.Wait(); err != nil {
			t.Fatalf("child %d: %v: %s", index, err, children[index].output.String())
		}
	}
	if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || !found {
		t.Fatalf("cross-process load found=%v err=%v", found, err)
	}
}

func TestCheckpointHelperProcess(t *testing.T) {
	root := os.Getenv("CUTLY_CHECKPOINT_HELPER_ROOT")
	encoded := os.Getenv("CUTLY_CHECKPOINT_HELPER_VALUE")
	if root == "" || encoded == "" {
		return
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	checkpoint, err := stagedag.DecodeCheckpoint(raw)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(91)
	}
	artifacts, err := cas.New(filepath.Join(root, "cas"), cas.Options{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(92)
	}
	store, err := New(filepath.Join(root, "checkpoints"), artifacts, Options{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(93)
	}
	if err := store.Commit(context.Background(), checkpoint); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(94)
	}
	os.Exit(0)
}

func TestVisibleUnknownCommitRecoversByRetry(t *testing.T) {
	value := newFixture(t, true)
	afterRenameFailures := 0
	beforeSyncFailures := 0
	value.store.fault = func(point string) error {
		if point == "publish.after_rename" && afterRenameFailures == 0 {
			afterRenameFailures++
			return errors.New("injected directory sync failure")
		}
		if point == "publish.before_sync" && beforeSyncFailures == 0 {
			beforeSyncFailures++
			return errors.New("injected retry sync failure")
		}
		return nil
	}
	if err := value.store.Commit(context.Background(), value.checkpoint); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("unknown commit = %v", err)
	}
	if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || !found {
		t.Fatalf("visible checkpoint found=%v err=%v", found, err)
	}
	if err := value.store.Commit(context.Background(), value.checkpoint); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("retry without durable sync = %v", err)
	}
	if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if afterRenameFailures != 1 || beforeSyncFailures != 1 {
		t.Fatalf("fault calls after-rename=%d before-sync=%d", afterRenameFailures, beforeSyncFailures)
	}
}

func TestCommitAcquiresCASLeaseAfterLocatorLockWait(t *testing.T) {
	value := newFixtureWithCASOptions(t, true, cas.Options{LeaseTTL: time.Second}, Options{})
	locatorValue, err := locatorFromTask(value.scope, value.task)
	if err != nil {
		t.Fatal(err)
	}
	name, err := locatorName(locatorValue)
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := value.store.lockPath(name)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := tryFileLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = lock.release()
		}
	}()

	commitResult := make(chan error, 1)
	go func() { commitResult <- value.store.Commit(context.Background(), value.checkpoint) }()
	gate := processGate(lockPath)
	deadline := time.Now().Add(time.Second)
	for {
		select {
		case <-gate:
			gate <- struct{}{}
			if time.Now().After(deadline) {
				t.Fatal("commit did not reach locator lock wait")
			}
			time.Sleep(5 * time.Millisecond)
		default:
			goto waiting
		}
	}

waiting:
	// This expires the old, incorrect pre-lock lease. The fixed implementation
	// has not acquired a lease yet, so cross-store GC removes the unpinned CAS
	// entry and Commit must fail closed after it finally gets the locator lock.
	time.Sleep(time.Second + 100*time.Millisecond)
	collector, err := cas.New(filepath.Join(value.root, "cas"), cas.Options{LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	report, err := collector.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if err != nil || len(report.DeletedDigests) != 1 {
		t.Fatalf("GC report=%+v err=%v", report, err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	released = true
	select {
	case err := <-commitResult:
		if !errors.Is(err, ErrArtifactUnavailable) || !errors.Is(err, cas.ErrMiss) {
			t.Fatalf("commit after GC = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("commit did not finish after locator lock release")
	}
	if _, exists, err := value.store.entryPath(name, false); err != nil || exists {
		t.Fatalf("checkpoint published after artifact GC: exists=%v err=%v", exists, err)
	}
}

func TestCorruptAndStaleCheckpointsQuarantineToMiss(t *testing.T) {
	t.Run("checkpoint", func(t *testing.T) {
		value := newFixture(t, true)
		if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
			t.Fatal(err)
		}
		path := checkpointPath(t, value.store, value.checkpoint)
		file := filepath.Join(path, checkpointName)
		if err := os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(`{"corrupt":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || found {
			t.Fatalf("corrupt load found=%v err=%v", found, err)
		}
		entries, _ := os.ReadDir(value.store.quarantineRoot)
		if len(entries) != 1 {
			t.Fatalf("quarantine entries = %d", len(entries))
		}
	})

	t.Run("artifact", func(t *testing.T) {
		value := newFixture(t, true)
		if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
			t.Fatal(err)
		}
		payload := findNamedFile(t, filepath.Join(value.root, "cas"), "payload")
		if err := os.Chmod(payload, 0o600); err != nil {
			t.Fatal(err)
		}
		corrupt := make([]byte, len(value.payload))
		copy(corrupt, value.payload)
		corrupt[0] ^= 0xff
		if err := os.WriteFile(payload, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || found {
			t.Fatalf("stale load found=%v err=%v", found, err)
		}
	})

	t.Run("artifact identity", func(t *testing.T) {
		value := newFixture(t, true)
		if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
			t.Fatal(err)
		}
		path := checkpointPath(t, value.store, value.checkpoint)
		stale := cloneCheckpoint(value.checkpoint)
		stale.Output.ContentHash = digest('c')
		raw, err := stagedag.CanonicalCheckpoint(stale)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{checkpointName: raw, completeName: completion(raw)} {
			file := filepath.Join(path, name)
			if err := os.Chmod(file, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || found {
			t.Fatalf("identity mismatch found=%v err=%v", found, err)
		}
		entries, err := os.ReadDir(value.store.quarantineRoot)
		if err != nil || len(entries) != 1 {
			t.Fatalf("quarantine entries=%d err=%v", len(entries), err)
		}
	})
}

func TestCommitRequiresVerifiedCASArtifact(t *testing.T) {
	value := newFixture(t, false)
	if err := value.store.Commit(context.Background(), value.checkpoint); !errors.Is(err, ErrArtifactUnavailable) || !errors.Is(err, cas.ErrMiss) {
		t.Fatalf("missing artifact = %v", err)
	}
	if entries, _ := os.ReadDir(value.store.checkpointsRoot); len(entries) != 0 {
		t.Fatalf("checkpoint published without artifact: %v", entries)
	}
}

func TestLockWaitIsCancelable(t *testing.T) {
	value := newFixture(t, true)
	locatorValue, err := locatorFromTask(value.scope, value.task)
	if err != nil {
		t.Fatal(err)
	}
	name, err := locatorName(locatorValue)
	if err != nil {
		t.Fatal(err)
	}
	path, err := value.store.lockPath(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("process gate", func(t *testing.T) {
		gate := processGate(path)
		<-gate
		defer func() { gate <- struct{}{} }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, _, err := value.store.Load(ctx, value.scope, value.task); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("gate cancellation = %v", err)
		}
	})
	t.Run("OS lock", func(t *testing.T) {
		lock, err := tryFileLock(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := lock.release(); err != nil {
				t.Error(err)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, _, err := value.store.Load(ctx, value.scope, value.task); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("OS lock cancellation = %v", err)
		}
	})
}

func TestSymlinkEntryIsQuarantinedWithoutFollowing(t *testing.T) {
	value := newFixture(t, true)
	locatorValue, _ := locatorFromTask(value.scope, value.task)
	name, _ := locatorName(locatorValue)
	path, _, err := value.store.entryPath(name, true)
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, path)
	if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || found {
		t.Fatalf("symlink load found=%v err=%v", found, err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "safe" {
		t.Fatalf("external marker = %q, %v", raw, err)
	}
}

func TestQuarantineCardinalityIsBounded(t *testing.T) {
	value := newFixtureWithOptions(t, true, Options{MaxQuarantineEntries: 1})
	if err := value.store.Commit(context.Background(), value.checkpoint); err != nil {
		t.Fatal(err)
	}
	path := checkpointPath(t, value.store, value.checkpoint)
	if err := os.Chmod(filepath.Join(path, completeName), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, completeName), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := value.store.Load(context.Background(), value.scope, value.task); err != nil || found {
		t.Fatalf("first quarantine found=%v err=%v", found, err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := value.store.Load(context.Background(), value.scope, value.task); !errors.Is(err, ErrLimit) {
		t.Fatalf("quarantine limit = %v", err)
	}
}

func TestConcurrentQuarantineAdmissionIsBounded(t *testing.T) {
	value := newFixtureWithOptions(t, true, Options{MaxQuarantineEntries: 1})
	scopes := []stagedag.Scope{value.scope, value.scope}
	scopes[1].JobID = "job-2"
	for _, scope := range scopes {
		locatorValue, err := locatorFromTask(scope, value.task)
		if err != nil {
			t.Fatal(err)
		}
		name, err := locatorName(locatorValue)
		if err != nil {
			t.Fatal(err)
		}
		path, exists, err := value.store.entryPath(name, true)
		if err != nil || exists {
			t.Fatalf("prepare corrupt locator exists=%v err=%v", exists, err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsCh := make(chan error, len(scopes))
	for _, scope := range scopes {
		go func(scope stagedag.Scope) {
			<-start
			_, _, err := value.store.Load(context.Background(), scope, value.task)
			errorsCh <- err
		}(scope)
	}
	close(start)
	limited := 0
	for range scopes {
		if err := <-errorsCh; err != nil {
			if !errors.Is(err, ErrLimit) {
				t.Fatalf("unexpected quarantine error: %v", err)
			}
			limited++
		}
	}
	if limited != 1 {
		t.Fatalf("limited operations = %d, want 1", limited)
	}
	entries, err := os.ReadDir(value.store.quarantineRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries = %d, %v", len(entries), err)
	}
}

func TestScratchCardinalityIsBounded(t *testing.T) {
	value := newFixtureWithOptions(t, true, Options{MaxScratchEntries: 1})
	if err := os.Mkdir(filepath.Join(value.store.scratchRoot, "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := value.store.Commit(context.Background(), value.checkpoint); !errors.Is(err, ErrLimit) {
		t.Fatalf("scratch limit = %v", err)
	}
}

func TestNamespaceAndShardSymlinksFailClosed(t *testing.T) {
	t.Run("namespace", func(t *testing.T) {
		value := newFixture(t, true)
		external := t.TempDir()
		marker := filepath.Join(external, "marker")
		if err := os.WriteFile(marker, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(value.store.scratchRoot); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, external, value.store.scratchRoot)
		if err := value.store.Commit(context.Background(), value.checkpoint); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("namespace symlink = %v", err)
		}
		if raw, err := os.ReadFile(marker); err != nil || string(raw) != "safe" {
			t.Fatalf("external marker = %q, %v", raw, err)
		}
	})

	t.Run("shard", func(t *testing.T) {
		value := newFixture(t, true)
		locatorValue, err := locatorFromTask(value.scope, value.task)
		if err != nil {
			t.Fatal(err)
		}
		name, err := locatorName(locatorValue)
		if err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		marker := filepath.Join(external, "marker")
		if err := os.WriteFile(marker, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, external, filepath.Join(value.store.checkpointsRoot, name[:2]))
		if _, _, err := value.store.Load(context.Background(), value.scope, value.task); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("shard symlink = %v", err)
		}
		if raw, err := os.ReadFile(marker); err != nil || string(raw) != "safe" {
			t.Fatalf("external marker = %q, %v", raw, err)
		}
	})
}

func newFixture(t *testing.T, publish bool) fixture {
	return newFixtureWithOptions(t, publish, Options{})
}

func newFixtureWithOptions(t *testing.T, publish bool, options Options) fixture {
	return newFixtureWithCASOptions(t, publish, cas.Options{}, options)
}

func newFixtureWithCASOptions(t *testing.T, publish bool, casOptions cas.Options, options Options) fixture {
	t.Helper()
	root := t.TempDir()
	artifacts, err := cas.New(filepath.Join(root, "cas"), casOptions)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(filepath.Join(root, "checkpoints"), artifacts, options)
	if err != nil {
		t.Fatal(err)
	}
	planFingerprint := digest('a')
	manifestFingerprint := digest('b')
	stageBinding, err := stagedag.StageBindingInput(renderapi.StageCompose)
	if err != nil {
		t.Fatal(err)
	}
	input := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: "cutly.cache.test.v1", ArtifactKind: "cutly.chunk.test.v1",
		PlanFingerprint: planFingerprint, ExecutionManifestFingerprint: manifestFingerprint,
		StageProfile: "cutly.stage.test.v1", Inputs: []cachekey.NormalizedInput{stageBinding},
		OutputProfile: "cutly.output.test.v1",
	}
	key, err := cachekey.New(input)
	if err != nil {
		t.Fatal(err)
	}
	task := stagedag.Task{
		ID: "compose-0001", Stage: renderapi.StageCompose, Profile: input.StageProfile,
		DependsOn: []string{}, CacheInput: input, CacheKey: key,
		Resources: stagedag.ResourceEstimate{MemoryBytes: 1, DiskBytes: 1},
		Retry:     stagedag.RetryPolicy{MaxAttempts: 2},
	}
	plan := stagedag.Plan{
		Profile: stagedag.PlanProfileV1, JobID: "job-1", PlanFingerprint: planFingerprint,
		ExecutionManifestFingerprint: manifestFingerprint, Tasks: []stagedag.Task{task},
	}
	if err := stagedag.ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified reference surface bytes")
	payloadHash := sha256.Sum256(payload)
	contentHash := "sha256:" + hex.EncodeToString(payloadHash[:])
	if publish {
		if _, err := artifacts.Publish(context.Background(), cas.PublishRequest{
			Key: key, Payload: bytes.NewReader(payload), ExpectedHash: contentHash, ExpectedSize: int64(len(payload)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := canonicaljson.Marshal(map[string]any{"profile": "cutly.test-output.v1"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan, task, stagedag.StageOutput{
		ContentHash: contentHash, SizeBytes: int64(len(payload)), Receipt: receipt,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		root: root, artifacts: artifacts, store: store, plan: plan, task: task,
		scope:      stagedag.Scope{JobID: plan.JobID, PlanFingerprint: planFingerprint, ExecutionManifestFingerprint: manifestFingerprint},
		checkpoint: checkpoint, payload: payload,
	}
}

func checkpointPath(t *testing.T, store *Store, checkpoint stagedag.Checkpoint) string {
	t.Helper()
	name, err := locatorName(locatorFromCheckpoint(checkpoint))
	if err != nil {
		t.Fatal(err)
	}
	path, exists, err := store.entryPath(name, false)
	if err != nil || !exists {
		t.Fatalf("checkpoint path exists=%v err=%v", exists, err)
	}
	return path
}

func findNamedFile(t *testing.T, root, name string) string {
	t.Helper()
	var result string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == name {
			result = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil || result == "" {
		t.Fatalf("find %s: %v", name, err)
	}
	return result
}

func digest(fill byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = fill
	}
	return "sha256:" + string(value)
}

func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
}
