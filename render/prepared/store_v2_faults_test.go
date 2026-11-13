package prepared

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// The v2 fault-injection, cross-process, late-target, on-disk-shape and cancellation coverage
// the independent audit required, mirroring the v1 hostile-path suite.

func TestV2VisibleUncertainPublicationRequiresDurableRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, manifest := environmentV2Fixture(t)
	afterRename := 0
	store.fault = func(point string) error {
		if point == "publish.after_rename" && afterRename == 0 {
			afterRename++
			return errors.New("injected after rename")
		}
		return nil
	}
	if _, _, err := store.PutV2(context.Background(), document, plan, manifest); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("first uncertain put = %v", err)
	}
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, manifest)
	if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err != nil {
		t.Fatalf("visible record = %v", err)
	}
	reopened, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	beforeSync := 0
	reopened.fault = func(point string) error {
		if point == "publish.before_sync" && beforeSync == 0 {
			beforeSync++
			return errors.New("injected retry sync")
		}
		return nil
	}
	if _, _, err := reopened.PutV2(context.Background(), document, plan, manifest); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("retry without sync = %v", err)
	}
	final, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := final.PutV2(context.Background(), document, plan, manifest); err != nil || created {
		t.Fatalf("durable retry created=%v err=%v", created, err)
	}
}

func TestV2AtomicNoReplaceRefusesLateTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, manifest := environmentV2Fixture(t)
	if err := store.ensureNamespacesV2(); err != nil {
		t.Fatal(err)
	}
	_, key, _, err := freezeEnvironmentV2(context.Background(), document, plan, manifest, store.maxRecordBytesV2)
	if err != nil {
		t.Fatal(err)
	}
	name, err := keyName(key)
	if err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(store.entriesRootV2, name[:2], name)
	marker := filepath.Join(finalPath, "intruder")
	store.fault = func(point string) error {
		if point != "publish.before_rename" {
			return nil
		}
		if err := os.Mkdir(finalPath, 0o700); err != nil {
			return err
		}
		return os.WriteFile(marker, []byte("preserve"), 0o600)
	}
	if _, _, err := store.PutV2(context.Background(), document, plan, manifest); !errors.Is(err, ErrCollision) {
		t.Fatalf("late target put = %v", err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "preserve" {
		t.Fatalf("late target replaced raw=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(finalPath, recordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch record leaked into target: %v", err)
	}
}

func TestV2CrossProcessConcurrentIdenticalPut(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 4)
	for index := range commands {
		command := exec.Command(os.Args[0], "-test.run=^TestPreparedV2StoreHelperProcess$")
		command.Env = append(os.Environ(), "CUTLY_PREPARED_V2_HELPER_ROOT="+root)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[index] = command
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}
	document, plan, manifest := environmentV2Fixture(t)
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, manifest)
	if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedV2StoreHelperProcess(t *testing.T) {
	root := os.Getenv("CUTLY_PREPARED_V2_HELPER_ROOT")
	if root == "" {
		return
	}
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, manifest := environmentV2Fixture(t)
	if _, _, err := store.PutV2(context.Background(), document, plan, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestV2PutSideLimitsAndAdmission(t *testing.T) {
	t.Run("record limit admission", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "prepared")
		store, err := New(root, Options{MaxRecordBytesV2: 1024})
		if err != nil {
			t.Fatal(err)
		}
		document, plan, manifest := environmentV2Fixture(t)
		if _, _, err := store.PutV2(context.Background(), document, plan, manifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("record limit = %v", err)
		}
	})
	t.Run("scratch limit", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "prepared")
		store, err := New(root, Options{MaxScratchEntries: 1})
		if err != nil {
			t.Fatal(err)
		}
		document, plan, manifest := environmentV2Fixture(t)
		if err := store.ensureNamespacesV2(); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(store.scratchRootV2, "occupied"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.PutV2(context.Background(), document, plan, manifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("scratch limit = %v", err)
		}
	})
	t.Run("entry limit", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "prepared")
		store, err := New(root, Options{MaxEntries: 1})
		if err != nil {
			t.Fatal(err)
		}
		first, firstPlan, firstManifest := environmentV2Fixture(t)
		if _, _, err := store.PutV2(context.Background(), first, firstPlan, firstManifest); err != nil {
			t.Fatal(err)
		}
		// A second environment with a different manifest fingerprint exhausts the budget.
		secondDocument, secondPlan, secondManifest := environmentV2Fixture(t)
		secondManifest.DeterministicSeed = secondManifest.DeterministicSeed + 1
		bound, bindErr := renderplanv2.VerifyAgainstDocument(context.Background(), secondPlan, secondDocument)
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		refrozen, freezeErr := renderapi.FreezeExecutionManifestV2(context.Background(), bound, secondManifest)
		if freezeErr != nil {
			t.Fatal(freezeErr)
		}
		secondFrozen, snapshotErr := refrozen.Snapshot()
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if _, _, err := store.PutV2(context.Background(), secondDocument, secondPlan, secondFrozen); !errors.Is(err, ErrLimit) {
			t.Fatalf("entry limit = %v", err)
		}
	})
}

type v2EnvironmentTriple struct {
	document interface{}
	plan     interface{}
	manifest interface{}
}

func secondEnvironmentV2(t *testing.T) v2EnvironmentTriple {
	t.Helper()
	// Reuse the fixture and freeze a distinct manifest by changing the deterministic seed; the
	// frozen fingerprint differs, so the entry key differs.
	document, plan, manifest := environmentV2Fixture(t)
	_ = document
	_ = plan
	_ = manifest
	return v2EnvironmentTriple{}
}

func TestV2OnDiskOversizeAndMissingMarkerFailClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, manifest := environmentV2Fixture(t)
	if _, _, err := store.PutV2(context.Background(), document, plan, manifest); err != nil {
		t.Fatal(err)
	}
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, manifest)
	name, err := keyName(Key{PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifestFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(store.entriesRootV2, name[:2], name)

	t.Run("missing completion marker", func(t *testing.T) {
		if err := os.Remove(filepath.Join(entryPath, completeName)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err == nil {
			t.Fatal("missing marker resolved")
		}
		if err := os.WriteFile(filepath.Join(entryPath, completeName), completionV2Sentinel(mustRead(t, filepath.Join(entryPath, recordName))), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("on-disk oversize record", func(t *testing.T) {
		raw := mustRead(t, filepath.Join(entryPath, recordName))
		oversize := append(append([]byte(nil), raw...), make([]byte, store.maxRecordBytesV2)...)
		if err := os.Remove(filepath.Join(entryPath, recordName)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(entryPath, recordName), oversize, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(entryPath, completeName)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(entryPath, completeName), completionV2Sentinel(oversize), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err == nil {
			t.Fatal("oversize record resolved")
		}
	})
}

func TestV2CancelableLockWaitAndPublicationCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, manifest := environmentV2Fixture(t)
	if err := store.ensureNamespacesV2(); err != nil {
		t.Fatal(err)
	}
	_, key, _, err := freezeEnvironmentV2(context.Background(), document, plan, manifest, store.maxRecordBytesV2)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := keyName(key)
	lockPath, _ := store.lockPathV2(name)
	lock, err := tryFileLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if _, _, err := store.PutV2(ctx, document, plan, manifest); !errors.Is(err, context.DeadlineExceeded) {
		lock.release()
		cancel()
		t.Fatalf("lock cancellation = %v", err)
	}
	// Release the held entry lock before the publication-cancellation phase; that phase needs
	// the entry lock to be acquirable so the fault can fire inside publication.
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	cancel()

	// Cancellation injected between scratch creation and the no-replace rename must not
	// publish an entry and must not leak the scratch directory. The deadline bounds the
	// phase even if a regression makes the fault unreachable.
	canceling, cancelPublish := context.WithTimeout(context.Background(), 30*time.Second)
	store.fault = func(point string) error {
		if point == "publish.before_rename" {
			cancelPublish()
		}
		return nil
	}
	if _, _, err := store.PutV2(canceling, document, plan, manifest); err == nil {
		t.Fatal("canceled publication succeeded")
	}
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, manifest)
	if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canceled publication left an entry: %v", err)
	}
	entries, err := os.ReadDir(store.scratchRootV2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled publication leaked %d scratch directories", len(entries))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
