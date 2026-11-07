//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows

package proberegistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

func TestConfigurationAndInvalidRequestsFailClosed(t *testing.T) {
	dirtyRoot := t.TempDir() + string(filepath.Separator) + "x" + string(filepath.Separator) + ".." + string(filepath.Separator) + "registry"
	for _, root := range []string{"", "relative", string(filepath.Separator), dirtyRoot} {
		if _, err := New(root, Options{}); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("root %q error=%v", root, err)
		}
	}
	validRoot := filepath.Join(t.TempDir(), "registry")
	for _, options := range []Options{
		{MaxEntries: -1}, {MaxScratchEntries: -1}, {MaxQuarantineEntries: -1}, {MaxRecordBytes: MinRecordBytes - 1},
		{MaxEntries: MaxEntries + 1}, {MaxScratchEntries: MaxScratchEntries + 1},
		{MaxQuarantineEntries: MaxQuarantineEntries + 1}, {MaxRecordBytes: MaxRecordBytes + 1},
	} {
		if _, err := New(validRoot, options); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("options=%+v error=%v", options, err)
		}
	}
	store := mustStore(t, validRoot, Options{})
	var nilStore *Store
	if _, _, _, err := nilStore.Put(context.Background(), testManifest(0)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("nil put=%v", err)
	}
	if _, err := nilStore.ResolveProbe(context.Background(), digest('a')); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("nil resolve=%v", err)
	}
	if _, _, _, err := store.Put(nil, testManifest(0)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("nil context put=%v", err)
	}
	if _, err := store.ResolveProbe(context.Background(), "sha512:"+strings.Repeat("a", 128)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("noncanonical fingerprint=%v", err)
	}
	invalid := testManifest(0)
	invalid.Streams = nil
	if _, _, _, err := store.Put(context.Background(), invalid); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("invalid manifest=%v", err)
	}
	invalid = testManifest(0)
	invalid.Container.FormatName = string([]byte{0xff})
	if _, _, _, err := store.Put(context.Background(), invalid); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("invalid UTF-8 manifest=%v", err)
	}
}

func TestPutResolveReopenIdempotenceAndOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "probes")
	store := mustStore(t, root, Options{})
	manifest := testManifest(1)
	frozen, fingerprint, created, err := store.Put(context.Background(), manifest)
	if err != nil || !created {
		t.Fatalf("put created=%v fingerprint=%q err=%v", created, fingerprint, err)
	}
	wantFingerprint, err := probe.Fingerprint(manifest)
	if err != nil || fingerprint != wantFingerprint {
		t.Fatalf("fingerprint=%q want=%q err=%v", fingerprint, wantFingerprint, err)
	}
	frozen.Streams[0].Codec = "mutated"
	*frozen.Streams[0].DurationPTS = 999
	resolved, err := store.ResolveProbe(context.Background(), fingerprint)
	if err != nil || resolved.Streams[0].Codec == "mutated" || *resolved.Streams[0].DurationPTS == 999 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	resolved.Streams[0].Codec = "mutated-again"
	again, err := store.ResolveProbe(context.Background(), fingerprint)
	if err != nil || again.Streams[0].Codec == "mutated-again" {
		t.Fatalf("resolve aliases previous result: %+v err=%v", again, err)
	}
	if _, gotFingerprint, created, err := store.Put(context.Background(), manifest); err != nil || created || gotFingerprint != fingerprint {
		t.Fatalf("idempotent put created=%v fingerprint=%q err=%v", created, gotFingerprint, err)
	}
	reopened := mustStore(t, root, Options{})
	if got, err := reopened.ResolveProbe(context.Background(), fingerprint); err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("reopen resolve=%+v err=%v", got, err)
	}
	if _, err := store.ResolveProbe(context.Background(), digest('9')); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing resolve=%v", err)
	}
	if entries, err := os.ReadDir(store.scratchRoot); err != nil || len(entries) != 0 {
		t.Fatalf("scratch entries=%d err=%v", len(entries), err)
	}
}

func TestCanonicalFingerprintProfileEscapes(t *testing.T) {
	store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
	manifest := testManifest(27)
	manifest.Probe.Implementation = "ffprobe<>&\u2028\u2029"

	wantRaw, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	frozen, fingerprint, created, err := store.Put(context.Background(), manifest)
	if err != nil || !created || fingerprint != wantFingerprint || !reflect.DeepEqual(frozen, manifest) {
		t.Fatalf("put created=%v fingerprint=%q want=%q frozen=%+v err=%v", created, fingerprint, wantFingerprint, frozen, err)
	}
	raw, err := os.ReadFile(testEntryPath(t, store, fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(wantRaw) {
		t.Fatalf("stored canonical bytes differ\n got: %s\nwant: %s", raw, wantRaw)
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`, `\u2028`, `\u2029`} {
		if !strings.Contains(string(raw), escaped) {
			t.Fatalf("stored bytes do not use official escape %q: %s", escaped, raw)
		}
	}
	resolved, err := store.ResolveProbe(context.Background(), fingerprint)
	if err != nil || !reflect.DeepEqual(resolved, manifest) {
		t.Fatalf("resolve=%+v err=%v", resolved, err)
	}
}

func TestCrossStoreAndCrossProcessConcurrentPut(t *testing.T) {
	t.Run("stores", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "probes")
		stores := []*Store{mustStore(t, root, Options{}), mustStore(t, root, Options{})}
		manifest := testManifest(2)
		start := make(chan struct{})
		errorsChannel := make(chan error, 32)
		var wait sync.WaitGroup
		for index := 0; index < cap(errorsChannel); index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				<-start
				_, _, _, err := stores[index%len(stores)].Put(context.Background(), manifest)
				errorsChannel <- err
			}(index)
		}
		close(start)
		wait.Wait()
		close(errorsChannel)
		for err := range errorsChannel {
			if err != nil {
				t.Fatalf("concurrent put=%v", err)
			}
		}
		fingerprint, _ := probe.Fingerprint(manifest)
		if _, err := stores[1].ResolveProbe(context.Background(), fingerprint); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("processes", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "probes")
		store := mustStore(t, root, Options{})
		commands := make([]*exec.Cmd, 4)
		for index := range commands {
			command := exec.Command(os.Args[0], "-test.run=^TestProbeRegistryHelperProcess$")
			command.Env = append(os.Environ(), "CUTLY_PROBE_REGISTRY_HELPER_ROOT="+root)
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
		fingerprint, _ := probe.Fingerprint(testManifest(7))
		if _, err := store.ResolveProbe(context.Background(), fingerprint); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProbeRegistryHelperProcess(t *testing.T) {
	root := os.Getenv("CUTLY_PROBE_REGISTRY_HELPER_ROOT")
	if root == "" {
		return
	}
	store := mustStore(t, root, Options{})
	if _, _, _, err := store.Put(context.Background(), testManifest(7)); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptCollisionAndSymlinkEntriesAreQuarantined(t *testing.T) {
	t.Run("partial", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		manifest := testManifest(3)
		_, fingerprint, _, err := store.Put(context.Background(), manifest)
		if err != nil {
			t.Fatal(err)
		}
		path := testEntryPath(t, store, fingerprint)
		if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveProbe(context.Background(), fingerprint); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corrupt resolve=%v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("corrupt entry not quarantined: %v", err)
		}
		if entries, err := os.ReadDir(store.quarantineRoot); err != nil || len(entries) != 1 {
			t.Fatalf("quarantine entries=%d err=%v", len(entries), err)
		}
		if _, got, created, err := store.Put(context.Background(), manifest); err != nil || !created || got != fingerprint {
			t.Fatalf("repair put created=%v fingerprint=%q err=%v", created, got, err)
		}
	})

	t.Run("collision", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		first := testManifest(4)
		_, fingerprint, _, err := store.Put(context.Background(), first)
		if err != nil {
			t.Fatal(err)
		}
		_, _, otherRaw, err := freezeManifest(testManifest(5), store.maxRecordBytes)
		if err != nil {
			t.Fatal(err)
		}
		path := testEntryPath(t, store, fingerprint)
		if err := os.WriteFile(path, otherRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveProbe(context.Background(), fingerprint); !errors.Is(err, ErrCollision) {
			t.Fatalf("collision resolve=%v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		manifest := testManifest(6)
		_, fingerprint, _, err := store.Put(context.Background(), manifest)
		if err != nil {
			t.Fatal(err)
		}
		path := testEntryPath(t, store, fingerprint)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("not a manifest"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.ResolveProbe(context.Background(), fingerprint); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("symlink resolve=%v", err)
		}
		if raw, err := os.ReadFile(target); err != nil || string(raw) != "not a manifest" {
			t.Fatalf("symlink target changed raw=%q err=%v", raw, err)
		}
	})
}

func TestLimitsCancellationAndNamespaceReplacement(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{MaxEntries: 1})
		if _, _, _, err := store.Put(context.Background(), testManifest(10)); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.Put(context.Background(), testManifest(11)); !errors.Is(err, ErrLimit) {
			t.Fatalf("entry limit=%v", err)
		}
	})

	t.Run("scratch", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{MaxScratchEntries: 1})
		name, _ := fingerprintName(digest('8'))
		path := filepath.Join(store.scratchRoot, ".partial-"+name+"-leftover")
		if err := os.WriteFile(path, []byte("leftover"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.Put(context.Background(), testManifest(12)); !errors.Is(err, ErrLimit) {
			t.Fatalf("scratch limit=%v", err)
		}
	})

	t.Run("own scratch recovery", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{MaxScratchEntries: 1})
		manifest := testManifest(16)
		fingerprint, _ := probe.Fingerprint(manifest)
		name, _ := fingerprintName(fingerprint)
		leftover := filepath.Join(store.scratchRoot, ".partial-"+name+"-crashed")
		if err := os.WriteFile(leftover, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, created, err := store.Put(context.Background(), manifest); err != nil || !created {
			t.Fatalf("scratch recovery created=%v err=%v", created, err)
		}
		if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("own scratch survived recovery: %v", err)
		}
	})

	t.Run("record and stream", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{MaxRecordBytes: MinRecordBytes})
		manifest := testManifest(13)
		manifest.Streams[0].Video.HDR = &probe.HDRMetadata{Format: "hdr10", MasteringDisplay: strings.Repeat("x", MinRecordBytes*2)}
		if _, _, _, err := store.Put(context.Background(), manifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("record limit=%v", err)
		}
		manifest = testManifest(13)
		manifest.Streams = make([]probe.Stream, MaxStreams+1)
		if _, _, _, err := store.Put(context.Background(), manifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("stream limit=%v", err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, _, err := store.Put(ctx, testManifest(14)); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled put=%v", err)
		}
		if _, err := store.ResolveProbe(ctx, digest('1')); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled resolve=%v", err)
		}
	})

	t.Run("lock wait", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		name, _ := fingerprintName(digest('2'))
		path, _ := store.lockPath(name)
		lock, err := tryFileLock(path)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.release()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, err := store.ResolveProbe(ctx, digest('2')); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock cancellation=%v", err)
		}
	})

	t.Run("namespace symlink", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		outside := t.TempDir()
		if err := os.Remove(store.scratchRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, store.scratchRoot); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, _, _, err := store.Put(context.Background(), testManifest(15)); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("namespace replacement=%v", err)
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		name, _ := fingerprintName(digest('3'))
		lockPath, _ := store.lockPath(name)
		target := filepath.Join(t.TempDir(), "lock-target")
		if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, lockPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.ResolveProbe(context.Background(), digest('3')); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("lock symlink=%v", err)
		}
		if raw, err := os.ReadFile(target); err != nil || string(raw) != "preserve" {
			t.Fatalf("lock target changed raw=%q err=%v", raw, err)
		}
	})
}

func TestDirectoryScansCheckCancellationIncrementally(t *testing.T) {
	directory := t.TempDir()
	for index := 0; index < directoryReadBatch+8; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("entry-%04d", index)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := newCancelAfterChecksContext(12)
	if _, err := readDirectoryBounded(ctx, directory, directoryReadBatch+8); !errors.Is(err, context.Canceled) {
		t.Fatalf("incremental directory cancellation=%v", err)
	}

	store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
	for index := 0; index < 8; index++ {
		name := fmt.Sprintf(".partial-%064x-%02d", index+1, index)
		if err := os.WriteFile(filepath.Join(store.scratchRoot, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// readDirectoryBounded performs eleven checks for eight entries; cancel on a
	// later check so scanScratch's per-entry validation loop is exercised.
	ctx = newCancelAfterChecksContext(14)
	if _, err := store.scanScratch(ctx, store.maxScratchEntries+1); !errors.Is(err, context.Canceled) {
		t.Fatalf("scratch validation cancellation=%v", err)
	}
}

func TestQuarantineBoundAndPublicationFaults(t *testing.T) {
	t.Run("quarantine bound", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{MaxQuarantineEntries: 1})
		fingerprints := make([]string, 2)
		for index := range fingerprints {
			_, fingerprints[index], _, _ = store.Put(context.Background(), testManifest(20+index))
			if err := os.WriteFile(testEntryPath(t, store, fingerprints[index]), []byte("broken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.ResolveProbe(context.Background(), fingerprints[0]); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("first corrupt=%v", err)
		}
		if _, err := store.ResolveProbe(context.Background(), fingerprints[1]); !errors.Is(err, ErrCorrupt) || !errors.Is(err, ErrLimit) {
			t.Fatalf("bounded quarantine=%v", err)
		}
		if _, err := os.Lstat(testEntryPath(t, store, fingerprints[1])); err != nil {
			t.Fatalf("entry moved despite full quarantine: %v", err)
		}
	})

	t.Run("late target", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		manifest := testManifest(22)
		fingerprint, _ := probe.Fingerprint(manifest)
		name, _ := fingerprintName(fingerprint)
		path, _, err := store.entryPath(name, true)
		if err != nil {
			t.Fatal(err)
		}
		store.fault = func(point string) error {
			if point == "publish.before_link" {
				return os.WriteFile(path, []byte("intruder"), 0o600)
			}
			return nil
		}
		if _, _, _, err := store.Put(context.Background(), manifest); !errors.Is(err, ErrCollision) {
			t.Fatalf("late target=%v", err)
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "intruder" {
			t.Fatalf("late target overwritten raw=%q err=%v", raw, err)
		}
	})

	t.Run("scratch path replacement", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		manifest := testManifest(24)
		var held string
		store.fault = func(point string) error {
			if point != "publish.before_link" {
				return nil
			}
			entries, err := os.ReadDir(store.scratchRoot)
			if err != nil || len(entries) != 1 {
				return errors.Join(errors.New("unexpected scratch shape"), err)
			}
			path := filepath.Join(store.scratchRoot, entries[0].Name())
			held = path + ".held"
			if err := os.Rename(path, held); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("replacement"), 0o600)
		}
		if _, _, _, err := store.Put(context.Background(), manifest); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("scratch replacement=%v", err)
		}
		if held == "" {
			t.Fatal("fault did not replace scratch")
		}
		if err := os.Remove(held); err != nil {
			t.Fatal(err)
		}
		fingerprint, _ := probe.Fingerprint(manifest)
		name, _ := fingerprintName(fingerprint)
		path, exists, err := store.entryPath(name, false)
		if err != nil || exists {
			t.Fatalf("mismatched publication remains path=%s exists=%v err=%v", path, exists, err)
		}
	})

	t.Run("visible uncertain retry", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "probes"), Options{})
		manifest := testManifest(23)
		triggered := false
		store.fault = func(point string) error {
			if point == "publish.after_link" && !triggered {
				triggered = true
				return errors.New("injected")
			}
			return nil
		}
		fingerprint, _ := probe.Fingerprint(manifest)
		_, _, _, err := store.Put(context.Background(), manifest)
		if !errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("uncertain put=%v", err)
		}
		if _, err := store.ResolveProbe(context.Background(), fingerprint); err != nil {
			t.Fatalf("visible entry=%v", err)
		}
		store.fault = nil
		if _, _, created, err := store.Put(context.Background(), manifest); err != nil || created {
			t.Fatalf("durable retry created=%v err=%v", created, err)
		}
	})
}

func TestConcurrentScratchRemovalDoesNotReportCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "probes")
	publisher := mustStore(t, root, Options{})
	scanner := mustStore(t, root, Options{})
	first, second := manifestsWithIndependentProcessGates(t, publisher)

	removeReady := make(chan struct{})
	allowRemove := make(chan struct{})
	scanReady := make(chan struct{})
	allowScan := make(chan struct{})
	var removeOnce, scanOnce sync.Once
	publisher.fault = func(point string) error {
		if point == "publish.before_scratch_remove" {
			removeOnce.Do(func() {
				close(removeReady)
				<-allowRemove
			})
		}
		return nil
	}
	scanner.fault = func(point string) error {
		if point == "scan.scratch.before_validate" {
			scanOnce.Do(func() {
				close(scanReady)
				<-allowScan
			})
		}
		return nil
	}

	publisherResult := make(chan error, 1)
	go func() {
		_, _, _, err := publisher.Put(context.Background(), first)
		publisherResult <- err
	}()
	waitSignal(t, removeReady, "publisher scratch removal")

	scannerResult := make(chan error, 1)
	go func() {
		_, _, _, err := scanner.Put(context.Background(), second)
		scannerResult <- err
	}()
	waitSignal(t, scanReady, "scratch scan validation")
	close(allowRemove)
	publisherErr := waitResult(t, publisherResult, "publisher put")
	close(allowScan)
	if publisherErr != nil {
		t.Fatal(publisherErr)
	}
	if err := waitResult(t, scannerResult, "scanner put"); err != nil {
		t.Fatalf("concurrent scratch disappearance was treated as corruption: %v", err)
	}
	if entries, err := os.ReadDir(publisher.scratchRoot); err != nil || len(entries) != 0 {
		t.Fatalf("scratch entries=%d err=%v", len(entries), err)
	}
}

func TestSmallerRecordLimitDoesNotQuarantineValidEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "probes")
	large := mustStore(t, root, Options{MaxRecordBytes: MinRecordBytes * 4})
	manifest := testManifest(29)
	manifest.Streams[0].Video.HDR = &probe.HDRMetadata{
		Format: "hdr10", MasteringDisplay: strings.Repeat("x", MinRecordBytes),
	}
	_, fingerprint, _, err := large.Put(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := testEntryPath(t, large, fingerprint)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= MinRecordBytes {
		t.Fatalf("record size=%d", info.Size())
	}

	small := mustStore(t, root, Options{MaxRecordBytes: MinRecordBytes})
	if _, err := small.ResolveProbe(context.Background(), fingerprint); !errors.Is(err, ErrLimit) {
		t.Fatalf("small-limit resolve=%v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("valid large record was moved: %v", err)
	}
	if entries, err := os.ReadDir(small.quarantineRoot); err != nil || len(entries) != 0 {
		t.Fatalf("quarantine entries=%d err=%v", len(entries), err)
	}
	if _, err := large.ResolveProbe(context.Background(), fingerprint); err != nil {
		t.Fatalf("large-limit resolve after small reader=%v", err)
	}
}

func mustStore(t *testing.T, root string, options Options) *Store {
	t.Helper()
	store, err := New(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testEntryPath(t *testing.T, store *Store, fingerprint string) string {
	t.Helper()
	name, err := fingerprintName(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	path, exists, err := store.entryPath(name, false)
	if err != nil || !exists {
		t.Fatalf("entry path exists=%v err=%v", exists, err)
	}
	return path
}

func testManifest(seed int) probe.ProbeManifest {
	durationPTS := int64(30)
	firstPTS := int64(0)
	lastPTS := int64(0)
	return probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource: probe.ResourceIdentity{
			AssetID: videoedit.ID("asset-" + string(rune('a'+seed%26))), ContentHash: digest(byte('a' + seed%6)), SizeBytes: int64(1024 + seed),
		},
		Probe: probe.ProbeIdentity{
			Implementation: "cutly.test-probe.v1", BuildFingerprint: digest('b'), OptionsFingerprint: digest('c'),
		},
		Container: probe.ContainerInfo{FormatName: "mp4", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration:  videoedit.RationalTime{Value: 1, Rate: 1},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "h264", TimeBase: videoedit.RationalTime{Value: 1, Rate: 30},
			StartPTS: 0, DurationPTS: &durationPTS,
			Video: &probe.VideoStream{
				Width: 16, Height: 16, PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateCFR,
				AverageFrameRate: &videoedit.RationalTime{Value: 30, Rate: 1},
				Keyframes:        probe.KeyframeSummary{Complete: true, Count: 1, FirstPTS: &firstPTS, LastPTS: &lastPTS, IndexHash: digest('d')},
				Color:            probe.ColorMetadata{Range: probe.ColorRangeLimited},
			},
		}},
	}
}

func digest(value byte) string { return "sha256:" + strings.Repeat(string(value), 64) }

type cancelAfterChecksContext struct {
	context.Context
	mu       sync.Mutex
	checks   int
	cancelAt int
	done     chan struct{}
	canceled bool
}

func newCancelAfterChecksContext(cancelAt int) *cancelAfterChecksContext {
	return &cancelAfterChecksContext{Context: context.Background(), cancelAt: cancelAt, done: make(chan struct{})}
}

func (ctx *cancelAfterChecksContext) Done() <-chan struct{} { return ctx.done }

func (ctx *cancelAfterChecksContext) Err() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.checks++
	if !ctx.canceled && ctx.checks >= ctx.cancelAt {
		ctx.canceled = true
		close(ctx.done)
	}
	if ctx.canceled {
		return context.Canceled
	}
	return nil
}

func manifestsWithIndependentProcessGates(t *testing.T, store *Store) (probe.ProbeManifest, probe.ProbeManifest) {
	t.Helper()
	first := testManifest(0)
	firstFingerprint, err := probe.Fingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	firstName, _ := fingerprintName(firstFingerprint)
	firstPath, _ := store.lockPath(firstName)
	firstGate := processGate(firstPath)
	for seed := 1; seed < 2048; seed++ {
		candidate := testManifest(seed)
		fingerprint, err := probe.Fingerprint(candidate)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := fingerprintName(fingerprint)
		path, _ := store.lockPath(name)
		if name[:2] != firstName[:2] && processGate(path) != firstGate {
			return first, candidate
		}
	}
	t.Fatal("could not find independently gated manifests")
	return probe.ProbeManifest{}, probe.ProbeManifest{}
}

func waitSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func waitResult(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		return nil
	}
}
