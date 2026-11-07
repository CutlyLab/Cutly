package ffprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const helperExecutableName = "cutly-ffprobe-test-helper"

type ffprobeHelperConfig struct {
	Fixture        string `json:"fixture"`
	FrameFixture   string `json:"frame_fixture"`
	Capture        string `json:"capture,omitempty"`
	Ready          string `json:"ready,omitempty"`
	Release        string `json:"release,omitempty"`
	Observed       string `json:"observed,omitempty"`
	ReadDone       string `json:"read_done,omitempty"`
	Finish         string `json:"finish,omitempty"`
	EnvironmentLog string `json:"environment_log,omitempty"`
}

func TestMain(m *testing.M) {
	if strings.HasPrefix(filepath.Base(os.Args[0]), helperExecutableName) {
		os.Exit(runFFprobeHelper())
	}
	os.Exit(m.Run())
}

func runFFprobeHelper() int {
	arguments := os.Args[1:]
	configRaw, err := os.ReadFile(os.Args[0] + ".json")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read helper config: %v\n", err)
		return 3
	}
	var config ffprobeHelperConfig
	if err := json.Unmarshal(configRaw, &config); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "decode helper config: %v\n", err)
		return 3
	}
	if config.EnvironmentLog != "" {
		logFile, openErr := os.OpenFile(config.EnvironmentLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "open environment log: %v\n", openErr)
			return 3
		}
		encoded, _ := json.Marshal(os.Environ())
		_, writeErr := logFile.Write(append(encoded, '\n'))
		closeErr := logFile.Close()
		if writeErr != nil || closeErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, "write environment log")
			return 3
		}
	}
	if len(arguments) == 1 && arguments[0] == "-version" {
		_, _ = fmt.Fprintln(os.Stdout, "ffprobe version cutly-test")
		_, _ = fmt.Fprintln(os.Stdout, "libavformat 1.2.3")
		return 0
	}
	if len(arguments) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "missing probe arguments")
		return 2
	}
	input := arguments[len(arguments)-1]
	if config.Capture != "" {
		if err := os.WriteFile(config.Capture, []byte(input), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "capture input path: %v\n", err)
			return 4
		}
	}
	if config.Ready != "" {
		if err := os.WriteFile(config.Ready, []byte("ready"), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "signal ready: %v\n", err)
			return 5
		}
	}
	if config.Release != "" {
		if err := helperWaitForFile(config.Release); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "wait for read release: %v\n", err)
			return 6
		}
	}
	inherited := os.NewFile(3, "cutly-private-snapshot")
	if inherited == nil {
		_, _ = fmt.Fprintln(os.Stderr, "missing inherited descriptor 3")
		return 3
	}
	if _, err := inherited.Seek(0, 0); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rewind inherited input: %v\n", err)
		return 3
	}
	observed, err := io.ReadAll(inherited)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read inherited input: %v\n", err)
		return 3
	}
	if config.Observed != "" {
		if err := os.WriteFile(config.Observed, observed, 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "capture observed input: %v\n", err)
			return 7
		}
	}
	if config.ReadDone != "" {
		if err := os.WriteFile(config.ReadDone, []byte("read"), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "signal read completion: %v\n", err)
			return 8
		}
	}
	if config.Finish != "" {
		if err := helperWaitForFile(config.Finish); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "wait for finish: %v\n", err)
			return 9
		}
	}
	fixture := config.Fixture
	if slices.Contains(arguments, "-show_frames") {
		fixture = config.FrameFixture
	}
	if fixture == "" {
		_, _ = fmt.Fprintln(os.Stderr, "missing fixture path")
		return 10
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read fixture: %v\n", err)
		return 11
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		return 12
	}
	return 0
}

func helperWaitForFile(path string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBuildIdentityFailsClosed(t *testing.T) {
	if _, err := New(context.Background(), "unused", BuildIdentity{}, Options{}); !errors.Is(err, ErrBuildIdentity) {
		t.Fatalf("empty identity error = %v", err)
	}
	identity := DefaultBuildIdentity("")
	if _, err := New(context.Background(), "unused", identity, Options{}); !errors.Is(err, ErrBuildIdentity) {
		t.Fatalf("empty runtime dependency error = %v", err)
	}
	identity.RuntimeDependencyFingerprint = "ffprobe-version-text"
	if _, err := New(context.Background(), "unused", identity, Options{}); !errors.Is(err, ErrBuildIdentity) {
		t.Fatalf("unstructured runtime dependency error = %v", err)
	}
}

func TestSemanticProfilesChangeManifestFingerprints(t *testing.T) {
	helper := installFFprobeHelper(t)
	writeFFprobeHelperConfig(t, helper, ffprobeHelperConfig{})
	input := filepath.Join(canonicalTempDir(t), "profile-input.bin")
	if err := os.WriteFile(input, []byte("semantic-profile-input"), 0o600); err != nil {
		t.Fatal(err)
	}

	baseIdentity := DefaultBuildIdentity(testDigest("runtime-closure-a"))
	baseAdapter, err := New(context.Background(), helper, baseIdentity, Options{})
	if err != nil {
		t.Fatal(err)
	}
	baseManifest, _, err := baseAdapter.Probe(context.Background(), Request{AssetID: "profile", Path: input})
	if err != nil {
		t.Fatal(err)
	}
	changedProfiles := []struct {
		name     string
		identity BuildIdentity
	}{
		{
			name: "adapter",
			identity: BuildIdentity{
				SemanticProfile: SemanticProfile{
					Adapter: "cutly.ffprobe.adapter.v2-test",
					Parser:  baseIdentity.SemanticProfile.Parser,
				},
				RuntimeDependencyFingerprint: baseIdentity.RuntimeDependencyFingerprint,
			},
		},
		{
			name: "parser",
			identity: BuildIdentity{
				SemanticProfile: SemanticProfile{
					Adapter: baseIdentity.SemanticProfile.Adapter,
					Parser:  "cutly.ffprobe.parser.v2-test",
				},
				RuntimeDependencyFingerprint: baseIdentity.RuntimeDependencyFingerprint,
			},
		},
	}
	for _, test := range changedProfiles {
		t.Run(test.name, func(t *testing.T) {
			changedAdapter, newErr := New(context.Background(), helper, test.identity, Options{})
			if newErr != nil {
				t.Fatal(newErr)
			}
			changedManifest, _, probeErr := changedAdapter.Probe(context.Background(), Request{AssetID: "profile", Path: input})
			if probeErr != nil {
				t.Fatal(probeErr)
			}
			if baseManifest.Probe.BuildFingerprint == changedManifest.Probe.BuildFingerprint {
				t.Fatalf("%s semantic profile did not change manifest build fingerprint", test.name)
			}
			if baseManifest.Probe.OptionsFingerprint == changedManifest.Probe.OptionsFingerprint {
				t.Fatalf("%s semantic profile did not change manifest options fingerprint", test.name)
			}
		})
	}

	options, err := normalizedOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	runtimeChanged := baseIdentity
	runtimeChanged.RuntimeDependencyFingerprint = testDigest("runtime-closure-b")
	baseBuild, baseOptions, err := adapterFingerprints(testDigest("binary"), "ffprobe version test", baseIdentity, options)
	if err != nil {
		t.Fatal(err)
	}
	changedBuild, changedOptions, err := adapterFingerprints(testDigest("binary"), "ffprobe version test", runtimeChanged, options)
	if err != nil {
		t.Fatal(err)
	}
	if baseBuild == changedBuild {
		t.Fatal("runtime dependency identity did not change build fingerprint")
	}
	if baseOptions != changedOptions {
		t.Fatal("runtime dependency identity unexpectedly changed pure options fingerprint")
	}
}

func TestMaterializationPublishesContentAddressThenUnlinksWorkspace(t *testing.T) {
	content := []byte("content-addressed-private-snapshot")
	input := filepath.Join(canonicalTempDir(t), "materialize.bin")
	if err := os.WriteFile(input, content, 0o600); err != nil {
		t.Fatal(err)
	}
	materialized, err := materializeLocalInput(context.Background(), input, testDigestBytes(content))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := materialized.Close(); err != nil {
			t.Error(err)
		}
	}()
	wantName := "sha256-" + strings.TrimPrefix(testDigestBytes(content), "sha256:")
	if materialized.contentAddressedName != wantName {
		t.Fatalf("content-addressed name = %q, want %q", materialized.contentAddressedName, wantName)
	}
	assertRemoved(t, materialized.workspaceDirectory)
	info, err := materialized.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("retained snapshot mode = %v, want read-only", info.Mode())
	}
	hash, size, err := materialized.verify(context.Background())
	if err != nil || hash != testDigestBytes(content) || size != int64(len(content)) {
		t.Fatalf("private snapshot = %q/%d, err=%v", hash, size, err)
	}
}

func TestWorkspaceCleanupRejectsMissingPathAndCanRetry(t *testing.T) {
	workspace, err := makePrivateMaterializationWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if workspace.closed {
			return
		}
		if _, err := os.Lstat(workspace.directory); os.IsNotExist(err) {
			_ = os.Rename(workspace.directory+"-moved", workspace.directory)
		}
		_ = workspace.Close()
	}()
	const trackedName = "tracked"
	if err := workspace.root.WriteFile(trackedName, []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace.track(trackedName)
	moved := workspace.directory + "-moved"
	if err := os.Rename(workspace.directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err == nil {
		t.Fatal("missing private directory path was accepted as successful cleanup")
	}
	if workspace.closed {
		t.Fatal("failed cleanup was marked closed")
	}
	if err := os.Rename(moved, workspace.directory); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	assertRemoved(t, workspace.directory)
}

func TestCommandsUseFrozenMinimalEnvironment(t *testing.T) {
	helper := installFFprobeHelper(t)
	control := canonicalTempDir(t)
	environmentLog := filepath.Join(control, "environment.jsonl")
	writeFFprobeHelperConfig(t, helper, ffprobeHelperConfig{EnvironmentLog: environmentLog})
	t.Setenv("LD_PRELOAD", "/forbidden/cutly-test.so")
	t.Setenv("LD_LIBRARY_PATH", "/forbidden/cutly-test-libs")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/forbidden/cutly-test.dylib")
	t.Setenv("DYLD_LIBRARY_PATH", "/forbidden/cutly-test-dylibs")
	t.Setenv("FFREPORT", "file=/forbidden/cutly-ffreport.log")
	t.Setenv("LANG", "host-locale")

	input := filepath.Join(canonicalTempDir(t), "environment.bin")
	if err := os.WriteFile(input, []byte("environment-input"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(
		context.Background(),
		helper,
		DefaultBuildIdentity(testDigest("environment-test-runtime")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Probe(context.Background(), Request{AssetID: "environment", Path: input}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(environmentLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("environment invocation count = %d, want build, probe and frame scan", len(lines))
	}
	want := controlledCommandEnvironment()
	for index, line := range lines {
		var got []string
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("invocation %d environment = %q, want %q", index, got, want)
		}
	}
}

func TestProbeUsesPrivateSnapshotWhenOriginalPathIsReplaced(t *testing.T) {
	helper := installFFprobeHelper(t)
	control := canonicalTempDir(t)
	capture := filepath.Join(control, "capture")
	ready := filepath.Join(control, "ready")
	release := filepath.Join(control, "allow-read")
	observed := filepath.Join(control, "observed")
	readDone := filepath.Join(control, "read-done")
	finish := filepath.Join(control, "finish")
	writeFFprobeHelperConfig(t, helper, ffprobeHelperConfig{
		Capture:  capture,
		Ready:    ready,
		Release:  release,
		Observed: observed,
		ReadDone: readDone,
		Finish:   finish,
	})

	original := []byte("immutable-original-input")
	input := filepath.Join(canonicalTempDir(t), "replace-me.bin")
	if err := os.WriteFile(input, original, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(
		context.Background(),
		helper,
		DefaultBuildIdentity(testDigest("replacement-test-runtime")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type probeResult struct {
		contentHash string
		err         error
	}
	resultChannel := make(chan probeResult, 1)
	go func() {
		manifest, _, probeErr := adapter.Probe(ctx, Request{
			AssetID:             "replace",
			Path:                input,
			ExpectedContentHash: testDigestBytes(original),
		})
		resultChannel <- probeResult{contentHash: manifest.Resource.ContentHash, err: probeErr}
	}()
	if err := waitForFile(ctx, ready); err != nil {
		t.Fatal(err)
	}
	privateRaw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	inputArgument := strings.TrimSpace(string(privateRaw))
	replacement := filepath.Join(filepath.Dir(input), "replacement.bin")
	if err := os.WriteFile(replacement, []byte("attacker-replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(input, replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForFile(ctx, readDone); err != nil {
		t.Fatal(err)
	}
	observedBytes, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(observedBytes) != string(original) {
		t.Fatalf("helper observed replacement bytes %q", observedBytes)
	}
	restored := filepath.Join(filepath.Dir(input), "restored-original.bin")
	if err := os.WriteFile(restored, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(input, restored); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finish, []byte("finish"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-resultChannel
	if result.err != nil {
		t.Fatal(result.err)
	}
	wantHash := testDigestBytes(original)
	if result.contentHash != wantHash {
		t.Fatalf("content hash = %q, want %q", result.contentHash, wantHash)
	}
	if inputArgument != "fd:" {
		t.Fatalf("ffprobe input = %q, want inherited fd protocol", inputArgument)
	}
}

func TestCanceledProbeCleansPrivateSnapshot(t *testing.T) {
	helper := installFFprobeHelper(t)
	control := canonicalTempDir(t)
	capture := filepath.Join(control, "capture")
	ready := filepath.Join(control, "ready")
	writeFFprobeHelperConfig(t, helper, ffprobeHelperConfig{
		Capture: capture,
		Ready:   ready,
		Release: filepath.Join(control, "never-release"),
	})
	input := filepath.Join(canonicalTempDir(t), "cancel.bin")
	if err := os.WriteFile(input, []byte("cancel-input"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(
		context.Background(),
		helper,
		DefaultBuildIdentity(testDigest("cancel-test-runtime")),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan error, 1)
	go func() {
		_, _, probeErr := adapter.Probe(ctx, Request{AssetID: "cancel", Path: input})
		resultChannel <- probeErr
	}()
	waitCtx, stopWaiting := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopWaiting()
	if err := waitForFile(waitCtx, ready); err != nil {
		cancel()
		t.Fatal(err)
	}
	privateRaw, err := os.ReadFile(capture)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	inputArgument := strings.TrimSpace(string(privateRaw))
	cancel()
	if err := <-resultChannel; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe error = %v", err)
	}
	if inputArgument != "fd:" {
		t.Fatalf("ffprobe input = %q, want inherited fd protocol", inputArgument)
	}
}

func writeFFprobeHelperConfig(t *testing.T, helper string, config ffprobeHelperConfig) {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("testdata", "ffprobe-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	config.Fixture = fixture
	if config.FrameFixture == "" {
		frameFixture, frameErr := filepath.Abs(filepath.Join("testdata", "ffprobe-frames-empty.json"))
		if frameErr != nil {
			t.Fatal(frameErr)
		}
		config.FrameFixture = frameFixture
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper+".json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func installFFprobeHelper(t *testing.T) string {
	t.Helper()
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	name := helperExecutableName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	destinationPath := filepath.Join(canonicalTempDir(t), name)
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destinationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	return destinationPath
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func waitForFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func replaceFile(destination, replacement string) error {
	if err := os.Rename(replacement, destination); err == nil {
		return nil
	}
	if err := os.Remove(destination); err != nil {
		return err
	}
	return os.Rename(replacement, destination)
}

func assertRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("private materialization path still exists: %q, err=%v", path, err)
	}
}

func testDigest(value string) string { return testDigestBytes([]byte(value)) }

func testDigestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
