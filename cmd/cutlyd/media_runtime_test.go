package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/media/runtimebundle"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

type daemonRuntimeBundleFixture struct {
	root       string
	manifest   runtimebundle.Manifest
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	resolver   *runtimebundle.Resolver
}

func newDaemonRuntimeBundleFixture(t *testing.T) *daemonRuntimeBundleFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := []struct {
		path  string
		raw   []byte
		mode  os.FileMode
		tools []string
	}{
		{path: "bin/ffmpeg", raw: []byte("fixture ffmpeg\n"), mode: daemonExecutableMode(), tools: []string{runtimebundle.ToolFFmpeg}},
		{path: "bin/ffprobe", raw: []byte("fixture ffprobe\n"), mode: daemonExecutableMode(), tools: []string{runtimebundle.ToolFFprobe}},
		{path: "lib/media.bin", raw: []byte("shared closure\n"), mode: 0o644, tools: []string{runtimebundle.ToolFFmpeg, runtimebundle.ToolFFprobe}},
	}
	entries := make([]runtimebundle.FileEntry, 0, len(files))
	for _, file := range files {
		absolute := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, file.raw, file.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(absolute, file.mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(file.raw)
		entries = append(entries, runtimebundle.FileEntry{
			Path: file.path, SizeBytes: int64(len(file.raw)), Mode: uint32(file.mode.Perm()),
			ContentHash: "sha256:" + hex.EncodeToString(sum[:]), Tools: file.tools,
		})
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x67}, ed25519.SeedSize))
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	manifest, err := runtimebundle.SignManifest(runtimebundle.Manifest{
		SchemaVersion: runtimebundle.SchemaVersion, Profile: runtimebundle.ManifestProfileV1,
		CanonicalJSONProfile: canonicaljson.Profile, Platform: runtime.GOOS, Architecture: runtime.GOARCH,
		FFmpegPath: "bin/ffmpeg", FFprobePath: "bin/ffprobe", Files: entries,
	}, "test-release", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runtimebundle.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, runtimebundle.ManifestFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver, err := runtimebundle.New(runtimebundle.Options{
		TrustedKeys: map[string]ed25519.PublicKey{"test-release": publicKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &daemonRuntimeBundleFixture{
		root: root, manifest: manifest, privateKey: privateKey, publicKey: publicKey, resolver: resolver,
	}
}

func (fixture *daemonRuntimeBundleFixture) renderConfig() renderConfig {
	return renderConfig{
		mediaRuntimeBundleRoot: fixture.root, mediaRuntimeResolver: fixture.resolver,
		expectedMediaRuntimeBundleFingerprint: fixture.manifest.BundleFingerprint,
	}
}

func TestResolveMediaRuntimeUsesOnlySignedPinnedResolution(t *testing.T) {
	fixture := newDaemonRuntimeBundleFixture(t)
	resolved, err := resolveMediaRuntime(context.Background(), fixture.renderConfig())
	if err != nil {
		t.Fatal(err)
	}
	want, err := fixture.resolver.Resolve(context.Background(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ffmpegExecutable != want.FFmpeg.Path || resolved.ffprobeExecutable != want.FFprobe.Path ||
		resolved.ffmpegRuntimeDependencyFingerprint != want.FFmpeg.ClosureFingerprint ||
		resolved.ffprobeRuntimeDependencyFingerprint != want.FFprobe.ClosureFingerprint ||
		resolved.resolution.BundleFingerprint != fixture.manifest.BundleFingerprint {
		t.Fatalf("resolved = %+v, want = %+v", resolved, want)
	}
	if err := resolved.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResolvedMediaRuntimeRevalidationRejectsMutation(t *testing.T) {
	fixture := newDaemonRuntimeBundleFixture(t)
	resolved, err := resolveMediaRuntime(context.Background(), fixture.renderConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.root, "lib", "media.bin")
	if err := os.WriteFile(path, []byte("changed closure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Revalidate(context.Background()); !errors.Is(err, ErrMediaRuntimeReplaced) {
		t.Fatalf("revalidation error = %v", err)
	}
}

func TestResolveMediaRuntimeRejectsPinTrustContentAndMixedConfiguration(t *testing.T) {
	fixture := newDaemonRuntimeBundleFixture(t)
	t.Run("pin", func(t *testing.T) {
		configuration := fixture.renderConfig()
		configuration.expectedMediaRuntimeBundleFingerprint = testSHA256('f')
		if value, err := resolveMediaRuntime(context.Background(), configuration); !errors.Is(err, ErrMediaRuntimePin) || !reflect.DeepEqual(value, resolvedMediaRuntime{}) {
			t.Fatalf("resolved=%+v error=%v", value, err)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x68}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
		resolver, err := runtimebundle.New(runtimebundle.Options{TrustedKeys: map[string]ed25519.PublicKey{"other": otherKey}})
		if err != nil {
			t.Fatal(err)
		}
		configuration := fixture.renderConfig()
		configuration.mediaRuntimeResolver = resolver
		if _, err := resolveMediaRuntime(context.Background(), configuration); !errors.Is(err, runtimebundle.ErrTrust) {
			t.Fatalf("unknown-key error = %v", err)
		}
	})
	t.Run("content", func(t *testing.T) {
		path := filepath.Join(fixture.root, "lib", "media.bin")
		if err := os.WriteFile(path, []byte("tampered bytes!\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveMediaRuntime(context.Background(), fixture.renderConfig()); !errors.Is(err, runtimebundle.ErrContent) {
			t.Fatalf("tamper error = %v", err)
		}
	})
	t.Run("mixed", func(t *testing.T) {
		configuration := fixture.renderConfig()
		configuration.ffmpegExecutable = "/tmp/other-ffmpeg"
		if _, err := resolveMediaRuntime(context.Background(), configuration); !errors.Is(err, ErrMediaRuntimePolicy) {
			t.Fatalf("mixed error = %v", err)
		}
	})
}

func TestDevelopmentMediaRuntimeRequiresExplicitUnsafeGate(t *testing.T) {
	configuration := renderConfig{
		ffmpegExecutable: "ffmpeg", ffprobeExecutable: "ffprobe",
		ffmpegRuntimeDependencyFingerprint:  testSHA256('a'),
		ffprobeRuntimeDependencyFingerprint: testSHA256('b'),
	}
	if _, err := resolveMediaRuntime(context.Background(), configuration); !errors.Is(err, ErrMediaRuntimePolicy) {
		t.Fatalf("ungated error = %v", err)
	}
	configuration.unsafeAllowUnverifiedMediaRuntime = true
	resolved, err := resolveMediaRuntime(context.Background(), configuration)
	if err != nil || resolved.ffmpegExecutable != "ffmpeg" || resolved.ffprobeExecutable != "ffprobe" {
		t.Fatalf("development resolved=%+v error=%v", resolved, err)
	}
}

func TestMediaRuntimeRootMustBeDisjointFromMutableRoots(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"bundle": filepath.Join(root, "bundle"), "project": filepath.Join(root, "project"),
		"state": filepath.Join(root, "state"), "output": filepath.Join(root, "output"),
		"input": filepath.Join(root, "input"),
	}
	for _, path := range paths {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateMediaRuntimeRootSeparation(
		paths["bundle"], paths["project"], paths["state"], paths["output"], []string{paths["input"]},
	); err != nil {
		t.Fatal(err)
	}
	for name, overlap := range map[string]struct {
		project, state, output string
		inputs                 []string
	}{
		"project": {project: paths["bundle"], state: paths["state"], output: paths["output"], inputs: []string{paths["input"]}},
		"state":   {project: paths["project"], state: paths["bundle"], output: paths["output"], inputs: []string{paths["input"]}},
		"output":  {project: paths["project"], state: paths["state"], output: paths["bundle"], inputs: []string{paths["input"]}},
		"input":   {project: paths["project"], state: paths["state"], output: paths["output"], inputs: []string{paths["bundle"]}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMediaRuntimeRootSeparation(
				paths["bundle"], overlap.project, overlap.state, overlap.output, overlap.inputs,
			); err == nil {
				t.Fatal("overlapping bundle root accepted")
			}
		})
	}
}

func TestInvalidSignedRuntimeFailsBeforeDurableStateMutation(t *testing.T) {
	fixture := newDaemonRuntimeBundleFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration := config{
		projectID: "project", projectDir: filepath.Join(root, "project"),
		render: renderConfig{
			enabled: true, stateDir: filepath.Join(root, "state"), outputDir: filepath.Join(root, "output"),
			inputRoots: stringList{filepath.Join(root, "input")}, mediaRuntimeBundleRoot: fixture.root,
			mediaRuntimeResolver: fixture.resolver, expectedMediaRuntimeBundleFingerprint: fixture.manifest.BundleFingerprint,
		},
	}
	for _, directory := range []string{
		configuration.projectDir, configuration.render.stateDir, configuration.render.outputDir, configuration.render.inputRoots[0],
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "lib", "media.bin"), []byte("tampered bytes!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	application, err := buildApplication(context.Background(), configuration)
	if application != nil || !errors.Is(err, runtimebundle.ErrContent) {
		if application != nil {
			_ = application.Close()
		}
		t.Fatalf("application=%+v error=%v", application, err)
	}
	entries, err := os.ReadDir(configuration.render.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("runtime failure mutated durable state: %s", strings.Join(names, ","))
	}
}

func TestPackagedMediaRuntimeLDFlagsDaemonSmoke(t *testing.T) {
	if os.Getenv("CUTLY_TEST_EXPECT_PACKAGED_MEDIA_RUNTIME") != "1" {
		t.Skip("set CUTLY_TEST_EXPECT_PACKAGED_MEDIA_RUNTIME=1 and pass packaged media-runtime linker stamps")
	}
	root := t.TempDir()
	digest := testSHA256('a')
	signed := []string{
		"--project-id", "project", "--project-dir", root,
		"--render-state-dir", filepath.Join(root, "state"),
		"--render-output-dir", filepath.Join(root, "output"),
		"--render-input-root", filepath.Join(root, "input"),
		"--media-runtime-bundle-root", filepath.Join(root, "bundle"),
		"--sdk-build-digest", digest, "--compiler-build", digest, "--evaluator-build", digest,
	}
	configuration, err := parseConfig(signed)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.render.mediaRuntimeResolver == nil ||
		configuration.render.expectedMediaRuntimeBundleFingerprint != digest {
		t.Fatalf("packaged render configuration = %+v", configuration.render)
	}
	direct := []string{
		"--project-id", "project", "--project-dir", root,
		"--render-state-dir", filepath.Join(root, "state"),
		"--render-output-dir", filepath.Join(root, "output"),
		"--render-input-root", filepath.Join(root, "input"),
		"--unsafe-allow-unverified-media-runtime",
		"--sdk-build-digest", digest, "--compiler-build", digest, "--evaluator-build", digest,
		"--ffmpeg-runtime-dependency-fingerprint", digest,
		"--ffprobe-runtime-dependency-fingerprint", digest,
	}
	if _, err := parseConfig(direct); !errors.Is(err, ErrMediaRuntimePolicy) {
		t.Fatalf("packaged direct runtime error = %v", err)
	}
}

func daemonExecutableMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o666
	}
	return 0o755
}
