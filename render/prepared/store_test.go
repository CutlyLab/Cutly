package prepared

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestPutResolveReopenIdempotenceAndOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plan, manifest := testEnvironment(t, 2)
	environment, created, err := store.Put(context.Background(), plan, manifest)
	if err != nil || !created || environment.Plan.Fingerprint != plan.Fingerprint || environment.Manifest.Fingerprint != manifest.Fingerprint {
		t.Fatalf("put created=%v environment=%+v err=%v", created, environment, err)
	}
	environment.Plan.Video[0].ClipID = "mutated"
	environment.Manifest.Resources[0].Locator = "/mutated"
	resolved, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint)
	if err != nil || resolved.Plan.Video[0].ClipID == "mutated" || resolved.Manifest.Resources[0].Locator == "/mutated" {
		t.Fatalf("resolve ownership environment=%+v err=%v", resolved, err)
	}
	resolved.Plan.Video[0].ClipID = "mutated-again"
	again, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint)
	if err != nil || again.Plan.Video[0].ClipID == "mutated-again" {
		t.Fatalf("second resolve aliases first: %+v err=%v", again, err)
	}
	if _, created, err := store.Put(context.Background(), plan, manifest); err != nil || created {
		t.Fatalf("idempotent put created=%v err=%v", created, err)
	}
	reopened, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := reopened.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); err != nil || value.Manifest.Fingerprint != manifest.Fingerprint {
		t.Fatalf("reopen resolve=%+v err=%v", value, err)
	}
	if _, err := store.Resolve(context.Background(), digest('9'), digest('8')); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing resolve = %v", err)
	}
}

func TestCrossStoreConcurrentIdenticalPut(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	first, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plan, manifest := testEnvironment(t, 2)
	stores := []*Store{first, second}
	start := make(chan struct{})
	results := make(chan error, 32)
	var wait sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, _, err := stores[index%2].Put(context.Background(), plan, manifest)
			results <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent put = %v", err)
		}
	}
	if _, err := second.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestCrossProcessConcurrentIdenticalPut(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 4)
	for index := range commands {
		command := exec.Command(os.Args[0], "-test.run=^TestPreparedStoreHelperProcess$")
		command.Env = append(os.Environ(), "CUTLY_PREPARED_HELPER_ROOT="+root)
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
	plan, manifest := testEnvironment(t, 2)
	if _, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedStoreHelperProcess(t *testing.T) {
	root := os.Getenv("CUTLY_PREPARED_HELPER_ROOT")
	if root == "" {
		return
	}
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plan, manifest := testEnvironment(t, 2)
	if _, _, err := store.Put(context.Background(), plan, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestVisibleUncertainPublicationRequiresDurableRetry(t *testing.T) {
	store, plan, manifest := testFixture(t, Options{})
	afterRename := 0
	store.fault = func(point string) error {
		if point == "publish.after_rename" && afterRename == 0 {
			afterRename++
			return errors.New("injected after rename")
		}
		return nil
	}
	if _, _, err := store.Put(context.Background(), plan, manifest); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("first uncertain put = %v", err)
	}
	if _, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); err != nil {
		t.Fatalf("visible record = %v", err)
	}
	reopened, err := New(store.root, Options{})
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
	if _, _, err := reopened.Put(context.Background(), plan, manifest); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("retry without sync = %v", err)
	}
	final, err := New(store.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := final.Put(context.Background(), plan, manifest); err != nil || created {
		t.Fatalf("durable retry created=%v err=%v", created, err)
	}
}

func TestAtomicNoReplaceRefusesLateTarget(t *testing.T) {
	store, plan, manifest := testFixture(t, Options{})
	_, key, _, err := freezeEnvironment(plan, manifest, store.maxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	name, err := keyName(key)
	if err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(store.entriesRoot, name[:2], name)
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
	if _, _, err := store.Put(context.Background(), plan, manifest); !errors.Is(err, ErrCollision) {
		t.Fatalf("late target put = %v", err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "preserve" {
		t.Fatalf("late target replaced raw=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(finalPath, recordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch record leaked into target: %v", err)
	}
}

func TestStrictCorruptAndSymlinkRecordsFailClosed(t *testing.T) {
	t.Run("unknown member", func(t *testing.T) {
		store, plan, manifest := testFixture(t, Options{})
		if _, _, err := store.Put(context.Background(), plan, manifest); err != nil {
			t.Fatal(err)
		}
		path := testEntryPath(t, store, plan, manifest)
		recordPath := filepath.Join(path, recordName)
		raw, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		object["unknown"] = json.RawMessage(`true`)
		changed, err := canonicaljson.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{recordName: changed, completeName: completion(changed)} {
			file := filepath.Join(path, name)
			if err := os.Chmod(file, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("unknown record = %v", err)
		}
	})

	for name, mutate := range map[string]func([]byte) []byte{
		"missing member": func(raw []byte) []byte {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			delete(object, "profile")
			changed, err := canonicaljson.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			return changed
		},
		"duplicate member": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `{"execution_manifest":`, `{"profile":"duplicate","execution_manifest":`, 1))
		},
		"noncanonical": func(raw []byte) []byte {
			return append([]byte(" \n"), raw...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, plan, manifest := testFixture(t, Options{})
			if _, _, err := store.Put(context.Background(), plan, manifest); err != nil {
				t.Fatal(err)
			}
			path := testEntryPath(t, store, plan, manifest)
			recordPath := filepath.Join(path, recordName)
			raw, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatal(err)
			}
			changed := mutate(raw)
			for fileName, content := range map[string][]byte{recordName: changed, completeName: completion(changed)} {
				file := filepath.Join(path, fileName)
				if err := os.Chmod(file, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("strict record = %v", err)
			}
		})
	}

	t.Run("record symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privilege is not guaranteed")
		}
		store, plan, manifest := testFixture(t, Options{})
		if _, _, err := store.Put(context.Background(), plan, manifest); err != nil {
			t.Fatal(err)
		}
		path := testEntryPath(t, store, plan, manifest)
		recordPath := filepath.Join(path, recordName)
		if err := os.Remove(recordPath); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "external")
		if err := os.WriteFile(external, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, recordPath); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Resolve(context.Background(), plan.Fingerprint, manifest.Fingerprint); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("symlink record = %v", err)
		}
		if raw, err := os.ReadFile(external); err != nil || string(raw) != "safe" {
			t.Fatalf("external changed raw=%q err=%v", raw, err)
		}
	})
}

func TestPathKeyCollisionFailsClosed(t *testing.T) {
	store, firstPlan, firstManifest := testFixture(t, Options{})
	if _, _, err := store.Put(context.Background(), firstPlan, firstManifest); err != nil {
		t.Fatal(err)
	}
	firstPath := testEntryPath(t, store, firstPlan, firstManifest)
	firstRaw, err := os.ReadFile(filepath.Join(firstPath, recordName))
	if err != nil {
		t.Fatal(err)
	}

	secondPlan, secondManifest := testEnvironment(t, 3)
	_, secondKey, _, err := freezeEnvironment(secondPlan, secondManifest, store.maxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	secondName, err := keyName(secondKey)
	if err != nil {
		t.Fatal(err)
	}
	secondShard := filepath.Join(store.entriesRoot, secondName[:2])
	if err := os.Mkdir(secondShard, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	secondPath := filepath.Join(secondShard, secondName)
	if err := os.Mkdir(secondPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeDurableFile(filepath.Join(secondPath, recordName), firstRaw); err != nil {
		t.Fatal(err)
	}
	if err := writeDurableFile(filepath.Join(secondPath, completeName), completion(firstRaw)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Resolve(context.Background(), secondPlan.Fingerprint, secondManifest.Fingerprint); !errors.Is(err, ErrCollision) {
		t.Fatalf("resolve collision = %v", err)
	}
	if _, _, err := store.Put(context.Background(), secondPlan, secondManifest); !errors.Is(err, ErrCollision) {
		t.Fatalf("put collision = %v", err)
	}
}

func TestLimitsAndCancelableLockWait(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		store, firstPlan, firstManifest := testFixture(t, Options{MaxEntries: 1})
		if _, _, err := store.Put(context.Background(), firstPlan, firstManifest); err != nil {
			t.Fatal(err)
		}
		secondPlan, secondManifest := testEnvironment(t, 3)
		if _, _, err := store.Put(context.Background(), secondPlan, secondManifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("entry limit = %v", err)
		}
	})
	t.Run("scratch", func(t *testing.T) {
		store, plan, manifest := testFixture(t, Options{MaxScratchEntries: 1})
		if err := os.Mkdir(filepath.Join(store.scratchRoot, "occupied"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Put(context.Background(), plan, manifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("scratch limit = %v", err)
		}
	})
	t.Run("record", func(t *testing.T) {
		store, plan, manifest := testFixture(t, Options{MaxRecordBytes: 1024})
		if _, _, err := store.Put(context.Background(), plan, manifest); !errors.Is(err, ErrLimit) {
			t.Fatalf("record limit = %v", err)
		}
	})
	t.Run("lock cancellation", func(t *testing.T) {
		store, plan, manifest := testFixture(t, Options{})
		_, key, _, err := freezeEnvironment(plan, manifest, store.maxRecordBytes)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := keyName(key)
		lockPath, _ := store.lockPath(name)
		lock, err := tryFileLock(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.release()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, _, err := store.Put(ctx, plan, manifest); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock cancellation = %v", err)
		}
	})
}

func TestInvalidEnvironmentAndOptionsRejected(t *testing.T) {
	parent := t.TempDir()
	if _, err := New(filepath.Join(parent, "prepared"), Options{MaxEntries: -1}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid options = %v", err)
	}
	store, plan, manifest := testFixture(t, Options{})
	manifest.PlanFingerprint = digest('9')
	if _, _, err := store.Put(context.Background(), plan, manifest); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("drifted environment = %v", err)
	}
	if _, err := store.Resolve(context.Background(), "not-a-digest", digest('1')); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("invalid key = %v", err)
	}
}

func testFixture(t *testing.T, options Options) (*Store, videoedit.RenderPlan, renderapi.ExecutionManifest) {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "prepared"), options)
	if err != nil {
		t.Fatal(err)
	}
	plan, manifest := testEnvironment(t, 2)
	return store, plan, manifest
}

func testEntryPath(t *testing.T, store *Store, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) string {
	t.Helper()
	_, key, _, err := freezeEnvironment(plan, manifest, store.maxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	name, err := keyName(key)
	if err != nil {
		t.Fatal(err)
	}
	path, exists, err := store.entryPath(name, false)
	if err != nil || !exists {
		t.Fatalf("entry path exists=%v err=%v", exists, err)
	}
	return path
}

func testEnvironment(t *testing.T, seconds int64) (videoedit.RenderPlan, renderapi.ExecutionManifest) {
	t.Helper()
	duration := rational(10, 1)
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{{ID: "video", Kind: videoedit.AssetKindVideo, URI: "video.mp4", ContentHash: digest('a'), Duration: &duration}},
		Timelines: []videoedit.Timeline{{ID: "timeline", Tracks: []videoedit.Track{{
			ID: "video-track", Kind: videoedit.TrackKindVideo,
			Clips: []videoedit.Clip{{ID: "clip", AssetID: "video", TimelineRange: timeRange(0, seconds), SourceRange: timeRange(0, seconds)}},
		}}}},
	}
	plan, diagnostics := videoedit.Resolve(document, timeRange(0, seconds), videoedit.RenderTarget{
		TimelineID: "timeline", Width: 1920, Height: 1080,
		FrameClock:  videoedit.FrameClock{Origin: rational(0, 1), FramesPerSecond: rational(30, 1)},
		SampleClock: videoedit.SampleClock{Origin: rational(0, 1), SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	capabilities := videoedit.BackendCapabilities{
		BackendID: "native-cpu", BuildFingerprint: digest('1'),
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo}, AssetKinds: []videoedit.AssetKind{videoedit.AssetKindVideo},
		MaxWidth: 3840, MaxHeight: 2160,
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digest('c'), EvaluatorBuild: digest('d'),
		Resources: []renderapi.MaterializedResource{{
			AssetID: "video", Kind: videoedit.AssetKindVideo, ContentHash: digest('a'), SizeBytes: 100,
			LocatorScheme: renderapi.LocatorFile, Locator: "/controlled/video.mp4", ProbeFingerprint: digest('e'),
		}},
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: digest('2'), DecoderProfile: "ffmpeg-sw-v1", EncoderProfile: "libx264-aac-v1", ColorProfile: "cutly.bt709-linear.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.reference.hardcut.v1", Format: "mp4", Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace, VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan, manifest
}

func rational(value, rate int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: rate}
}

func timeRange(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{Start: rational(start, 1), Duration: rational(duration, 1)}
}

func digest(value byte) string { return "sha256:" + strings.Repeat(string(value), 64) }
