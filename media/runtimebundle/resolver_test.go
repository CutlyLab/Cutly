package runtimebundle

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
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

type bundleFixture struct {
	root       string
	manifest   Manifest
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	resolver   *Resolver
}

func newBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := []struct {
		path  string
		data  []byte
		mode  os.FileMode
		tools []string
	}{
		{path: "bin/ffmpeg", data: []byte("fixture ffmpeg\n"), mode: executableMode(), tools: []string{ToolFFmpeg}},
		{path: "bin/ffprobe", data: []byte("fixture ffprobe\n"), mode: executableMode(), tools: []string{ToolFFprobe}},
		{path: "lib/libmedia.bin", data: []byte("shared closure\n"), mode: 0o644, tools: []string{ToolFFmpeg, ToolFFprobe}},
	}
	entries := make([]FileEntry, 0, len(files))
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.data, file.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, file.mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(file.data)
		entries = append(entries, FileEntry{
			Path: file.path, SizeBytes: int64(len(file.data)), Mode: uint32(file.mode.Perm()),
			ContentHash: "sha256:" + hex.EncodeToString(sum[:]), Tools: file.tools,
		})
	}
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	manifest, err := SignManifest(Manifest{
		SchemaVersion: SchemaVersion, Profile: ManifestProfileV1,
		CanonicalJSONProfile: canonicaljson.Profile, Platform: runtime.GOOS, Architecture: runtime.GOARCH,
		FFmpegPath: "bin/ffmpeg", FFprobePath: "bin/ffprobe", Files: entries,
	}, "release-2026", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, manifest)
	resolver, err := New(Options{TrustedKeys: map[string]ed25519.PublicKey{"release-2026": publicKey}})
	if err != nil {
		t.Fatal(err)
	}
	return &bundleFixture{root: root, manifest: manifest, privateKey: privateKey, publicKey: publicKey, resolver: resolver}
}

func TestResolveVerifiesSignedCompleteBundle(t *testing.T) {
	fixture := newBundleFixture(t)
	resolution, err := fixture.resolver.Resolve(context.Background(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.BundleRoot != fixture.root || resolution.BundleFingerprint != fixture.manifest.BundleFingerprint ||
		resolution.FFmpeg.Path != filepath.Join(fixture.root, "bin", "ffmpeg") ||
		resolution.FFprobe.Path != filepath.Join(fixture.root, "bin", "ffprobe") ||
		resolution.FFmpeg.BinaryContentHash != fixture.manifest.Files[0].ContentHash ||
		resolution.FFprobe.BinaryContentHash != fixture.manifest.Files[1].ContentHash ||
		!canonicalSHA256(resolution.FFmpeg.ClosureFingerprint) ||
		!canonicalSHA256(resolution.FFprobe.ClosureFingerprint) ||
		resolution.FFmpeg.ClosureFingerprint == resolution.FFprobe.ClosureFingerprint {
		t.Fatalf("resolution = %+v", resolution)
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		if resolution.FFmpeg.ClosureFingerprint != "sha256:fc019311095f80f3fe528460564b686771af696954e72e4b5eca045c07c98477" ||
			resolution.FFprobe.ClosureFingerprint != "sha256:508f3f6a4e6a6169c93023673bbf4a7040ec4732a067f72c59991dd28390966c" {
			t.Fatalf("closure fingerprint drift: ffmpeg=%s ffprobe=%s", resolution.FFmpeg.ClosureFingerprint, resolution.FFprobe.ClosureFingerprint)
		}
	}
	resolution.Manifest.Files[0].Tools[0] = ToolFFprobe
	second, err := fixture.resolver.Resolve(context.Background(), fixture.root)
	if err != nil || !reflect.DeepEqual(second.Manifest, fixture.manifest) {
		t.Fatalf("resolver retained caller mutation: %+v, %v", second.Manifest, err)
	}
	canonical, err := CanonicalManifest(fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeManifest(canonical)
	if err != nil || !reflect.DeepEqual(decoded, fixture.manifest) {
		t.Fatalf("canonical round trip = %+v, %v", decoded, err)
	}
}

func TestResolverSupportsConcurrentVerification(t *testing.T) {
	fixture := newBundleFixture(t)
	const workers = 16
	results := make(chan Resolution, workers)
	errorsCh := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			resolution, err := fixture.resolver.Resolve(context.Background(), fixture.root)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- resolution
		}()
	}
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	for result := range results {
		if result.BundleFingerprint != fixture.manifest.BundleFingerprint {
			t.Fatalf("concurrent fingerprint = %s", result.BundleFingerprint)
		}
	}
}

func TestResolveAuthenticatesBeforeTouchingClosure(t *testing.T) {
	fixture := newBundleFixture(t)
	bad := fixture.manifest
	bad.Signature = strings.Repeat("0", ed25519.SignatureSize*2)
	writeManifest(t, fixture.root, bad)
	if err := os.Remove(filepath.Join(fixture.root, "bin", "ffmpeg")); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.resolver.Resolve(context.Background(), fixture.root)
	if !errors.Is(err, ErrSignature) || errors.Is(err, ErrClosure) || errors.Is(err, ErrContent) {
		t.Fatalf("error = %v, want signature precedence", err)
	}
}

func TestResolveRejectsTrustFingerprintSignatureAndPlatform(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bundleFixture)
		want   error
	}{
		{name: "unknown key", want: ErrTrust, mutate: func(f *bundleFixture) {
			resolver, err := New(Options{TrustedKeys: map[string]ed25519.PublicKey{"other": f.publicKey}})
			if err != nil {
				t.Fatal(err)
			}
			f.resolver = resolver
		}},
		{name: "payload fingerprint", want: ErrSignature, mutate: func(f *bundleFixture) {
			f.manifest.BundleFingerprint = digest([]byte("wrong"))
			writeManifest(t, f.root, f.manifest)
		}},
		{name: "signature", want: ErrSignature, mutate: func(f *bundleFixture) {
			f.manifest.Signature = strings.Repeat("0", 128)
			writeManifest(t, f.root, f.manifest)
		}},
		{name: "signed payload mutation", want: ErrSignature, mutate: func(f *bundleFixture) {
			f.manifest.Files = cloneFiles(f.manifest.Files)
			f.manifest.Files[2].ContentHash = digest([]byte("other dependency"))
			writeManifest(t, f.root, f.manifest)
		}},
		{name: "platform", want: ErrPlatform, mutate: func(f *bundleFixture) {
			resolver, err := New(Options{Platform: "not-" + runtime.GOOS, Architecture: runtime.GOARCH, TrustedKeys: map[string]ed25519.PublicKey{"release-2026": f.publicKey}})
			if err != nil {
				t.Fatal(err)
			}
			f.resolver = resolver
		}},
		{name: "architecture", want: ErrPlatform, mutate: func(f *bundleFixture) {
			resolver, err := New(Options{Platform: runtime.GOOS, Architecture: "not-" + runtime.GOARCH, TrustedKeys: map[string]ed25519.PublicKey{"release-2026": f.publicKey}})
			if err != nil {
				t.Fatal(err)
			}
			f.resolver = resolver
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			test.mutate(fixture)
			resolution, err := fixture.resolver.Resolve(context.Background(), fixture.root)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(resolution, Resolution{}) {
				t.Fatalf("resolution=%+v error=%v want=%v", resolution, err, test.want)
			}
		})
	}
}

func TestResolveRejectsClosureAndContentDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bundleFixture)
		want   error
	}{
		{name: "missing", want: ErrClosure, mutate: func(f *bundleFixture) { _ = os.Remove(filepath.Join(f.root, "bin", "ffprobe")) }},
		{name: "extra file", want: ErrClosure, mutate: func(f *bundleFixture) { _ = os.WriteFile(filepath.Join(f.root, "extra"), []byte("x"), 0o644) }},
		{name: "extra directory", want: ErrClosure, mutate: func(f *bundleFixture) { _ = os.Mkdir(filepath.Join(f.root, "extra"), 0o755) }},
		{name: "bytes", want: ErrContent, mutate: func(f *bundleFixture) {
			_ = os.WriteFile(filepath.Join(f.root, "bin", "ffmpeg"), []byte("fixture ffmpeh\n"), 0o755)
		}},
		{name: "dependency bytes", want: ErrContent, mutate: func(f *bundleFixture) {
			_ = os.WriteFile(filepath.Join(f.root, "lib", "libmedia.bin"), []byte("shared closurf\n"), 0o644)
		}},
		{name: "mode", want: ErrContent, mutate: func(f *bundleFixture) { _ = os.Chmod(filepath.Join(f.root, "lib", "libmedia.bin"), 0o600) }},
		{name: "file symlink", want: ErrClosure, mutate: func(f *bundleFixture) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink requires platform privileges")
			}
			path := filepath.Join(f.root, "bin", "ffprobe")
			_ = os.Remove(path)
			_ = os.Symlink("ffmpeg", path)
		}},
		{name: "directory symlink", want: ErrClosure, mutate: func(f *bundleFixture) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink requires platform privileges")
			}
			_ = os.Rename(filepath.Join(f.root, "lib"), filepath.Join(f.root, "real-lib"))
			_ = os.Symlink("real-lib", filepath.Join(f.root, "lib"))
		}},
		{name: "special file", want: ErrClosure, mutate: func(f *bundleFixture) {
			if runtime.GOOS == "windows" {
				t.Skip("named pipe test is Unix-only")
			}
			_ = os.Remove(filepath.Join(f.root, "lib", "libmedia.bin"))
			if err := makeNamedPipe(filepath.Join(f.root, "lib", "libmedia.bin")); err != nil {
				t.Skip(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			test.mutate(fixture)
			resolution, err := fixture.resolver.Resolve(context.Background(), fixture.root)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(resolution, Resolution{}) {
				t.Fatalf("resolution=%+v error=%v want=%v", resolution, err, test.want)
			}
		})
	}
}

func TestResolveRejectsRootAndManifestLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires platform privileges")
	}
	fixture := newBundleFixture(t)
	link := filepath.Join(t.TempDir(), "bundle-link")
	if err := os.Symlink(fixture.root, link); err != nil {
		t.Skip(err)
	}
	if _, err := fixture.resolver.Resolve(context.Background(), link); !errors.Is(err, ErrClosure) {
		t.Fatalf("linked root error = %v", err)
	}

	fixture = newBundleFixture(t)
	manifestPath := filepath.Join(fixture.root, ManifestFileName)
	if err := os.Rename(manifestPath, manifestPath+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(manifestPath)+".real", manifestPath); err != nil {
		t.Skip(err)
	}
	if _, err := fixture.resolver.Resolve(context.Background(), fixture.root); !errors.Is(err, ErrClosure) {
		t.Fatalf("linked manifest error = %v", err)
	}
}

func TestResolveRejectsLinkedAncestorAndWritableDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink policy")
	}
	t.Run("linked ancestor", func(t *testing.T) {
		fixture := newBundleFixture(t)
		linkParent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkParent, "bundle-parent")
		if err := os.Symlink(filepath.Dir(fixture.root), link); err != nil {
			t.Skip(err)
		}
		linkedRoot := filepath.Join(link, filepath.Base(fixture.root))
		if _, err := fixture.resolver.Resolve(context.Background(), linkedRoot); !errors.Is(err, ErrClosure) {
			t.Fatalf("linked ancestor error = %v", err)
		}
	})

	t.Run("writable root", func(t *testing.T) {
		fixture := newBundleFixture(t)
		if err := os.Chmod(fixture.root, 0o770); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.resolver.Resolve(context.Background(), fixture.root); !errors.Is(err, ErrClosure) {
			t.Fatalf("writable root error = %v", err)
		}
	})

	t.Run("writable child directory", func(t *testing.T) {
		fixture := newBundleFixture(t)
		if err := os.Chmod(filepath.Join(fixture.root, "bin"), 0o775); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.resolver.Resolve(context.Background(), fixture.root); !errors.Is(err, ErrClosure) {
			t.Fatalf("writable child error = %v", err)
		}
	})
}

func TestVerifiedFileIdentityRejectsSameMetadataPathReplacement(t *testing.T) {
	fixture := newBundleFixture(t)
	root, err := os.OpenRoot(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, manifestIdentity, err := readRegularFile(context.Background(), root, ManifestFileName, DefaultMaxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	verified := map[string]os.FileInfo{ManifestFileName: manifestIdentity}
	for _, file := range fixture.manifest.Files {
		identity, verifyErr := verifyFile(context.Background(), root, file)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		verified[file.Path] = identity
	}
	expected := fixture.manifest.Files[0]
	absolute := filepath.Join(fixture.root, filepath.FromSlash(expected.Path))
	raw, err := os.ReadFile(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(absolute); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, raw, os.FileMode(expected.Mode)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, os.FileMode(expected.Mode)); err != nil {
		t.Fatal(err)
	}
	if err := verifyCompleteClosure(
		context.Background(), root, ManifestFileName, fixture.manifest, DefaultMaxClosureNodes, verified,
	); !errors.Is(err, ErrContent) {
		t.Fatalf("replacement error = %v", err)
	}
}

func TestResolveRejectsSpecialUnixFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode policy")
	}
	fixture := newBundleFixture(t)
	path := filepath.Join(fixture.root, "bin", "ffmpeg")
	if err := os.Chmod(path, executableMode()|os.ModeSetuid); err != nil {
		t.Skip(err)
	}
	information, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if information.Mode()&os.ModeSetuid == 0 {
		t.Skip("filesystem did not retain setuid mode")
	}
	if _, err := fixture.resolver.Resolve(context.Background(), fixture.root); !errors.Is(err, ErrContent) {
		t.Fatalf("special mode error = %v", err)
	}
}

func TestManifestSemanticValidationAndLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   error
	}{
		{name: "absolute", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Path = "/bin/ffmpeg" }},
		{name: "parent", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Path = "bin/../ffmpeg" }},
		{name: "backslash", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Path = `bin\ffmpeg` }},
		{name: "nonportable uppercase", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Path = "bin/FFmpeg" }},
		{name: "nonportable unicode", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Path = "bin/媒体" }},
		{name: "unsorted files", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0], m.Files[1] = m.Files[1], m.Files[0] }},
		{name: "duplicate tool", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Tools = []string{ToolFFmpeg, ToolFFmpeg} }},
		{name: "unknown tool", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Tools = []string{"player"} }},
		{name: "empty file", want: ErrManifest, mutate: func(m *Manifest) { m.Files[2].SizeBytes = 0 }},
		{name: "missing executable membership", want: ErrManifest, mutate: func(m *Manifest) { m.Files[0].Tools = []string{ToolFFprobe} }},
		{name: "non executable", want: ErrManifest, mutate: func(m *Manifest) {
			if m.Platform != "windows" {
				m.Files[0].Mode = 0o644
			} else {
				m.KeyID = ""
			}
		}},
		{name: "group writable file", want: ErrManifest, mutate: func(m *Manifest) {
			if m.Platform != "windows" {
				m.Files[2].Mode = 0o664
			} else {
				m.KeyID = ""
			}
		}},
		{name: "total limit", want: ErrLimit, mutate: func(m *Manifest) { m.Files[0].SizeBytes = DefaultMaxTotalFileBytes + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			candidate := fixture.manifest
			candidate.Files = cloneFiles(candidate.Files)
			test.mutate(&candidate)
			if err := validateManifestShape(candidate, DefaultMaxFiles, DefaultMaxTotalFileBytes); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStrictDecodeSigningAndCancellation(t *testing.T) {
	fixture := newBundleFixture(t)
	raw, err := CanonicalManifest(fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := DecodeManifest(duplicate); !errors.Is(err, ErrManifest) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := DecodeManifest(append([]byte(" \n"), raw...)); !errors.Is(err, ErrManifest) {
		t.Fatalf("noncanonical error = %v", err)
	}
	if _, err := SignManifest(fixture.manifest, "release-2026", fixture.privateKey); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("resign error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if resolution, err := fixture.resolver.Resolve(canceled, fixture.root); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(resolution, Resolution{}) {
		t.Fatalf("canceled resolution=%+v error=%v", resolution, err)
	}
}

func TestNewCopiesTrustedKeysAndRejectsInvalidConfiguration(t *testing.T) {
	fixture := newBundleFixture(t)
	keys := map[string]ed25519.PublicKey{"release-2026": append(ed25519.PublicKey(nil), fixture.publicKey...)}
	resolver, err := New(Options{TrustedKeys: keys})
	if err != nil {
		t.Fatal(err)
	}
	keys["release-2026"][0] ^= 0xff
	delete(keys, "release-2026")
	if _, err := resolver.Resolve(context.Background(), fixture.root); err != nil {
		t.Fatalf("resolver did not own trust store: %v", err)
	}
	for _, options := range []Options{
		{},
		{TrustedKeys: map[string]ed25519.PublicKey{"bad key": fixture.publicKey}},
		{TrustedKeys: map[string]ed25519.PublicKey{"good": []byte("short")}},
		{TrustedKeys: map[string]ed25519.PublicKey{"good": fixture.publicKey}, MaxFiles: 1},
		{TrustedKeys: map[string]ed25519.PublicKey{"good": fixture.publicKey}, MaxManifestBytes: DefaultMaxManifestBytes + 1},
		{TrustedKeys: map[string]ed25519.PublicKey{"good": fixture.publicKey}, MaxClosureNodes: 2},
	} {
		if resolver, err := New(options); !errors.Is(err, ErrConfiguration) || resolver != nil {
			t.Fatalf("New(%+v) = %v, %v", options, resolver, err)
		}
	}
	limited, err := New(Options{
		TrustedKeys:     map[string]ed25519.PublicKey{"release-2026": fixture.publicKey},
		MaxClosureNodes: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := limited.Resolve(context.Background(), fixture.root); !errors.Is(err, ErrLimit) || !reflect.DeepEqual(result, Resolution{}) {
		t.Fatalf("limited resolution=%+v error=%v", result, err)
	}
}

func TestManifestJSONFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/media-runtime-bundle-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeManifest(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(raw)) {
		t.Fatalf("fixture drift\nwant %s\n got %s", bytes.TrimSpace(raw), canonical)
	}
	if manifest.BundleFingerprint != "sha256:6c6481c35c97e45b2c2b64fefc39310e6950159dc58ca8bacc95f02702a2d7a8" {
		t.Fatalf("fixture fingerprint = %s", manifest.BundleFingerprint)
	}
	wantFingerprint, err := payloadFingerprint(manifest)
	if err != nil || wantFingerprint != manifest.BundleFingerprint {
		t.Fatalf("fixture payload fingerprint = %s, %v", wantFingerprint, err)
	}
	preimage, err := signaturePreimage(manifest)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	signature, err := hex.DecodeString(manifest.Signature)
	if err != nil || !ed25519.Verify(privateKey.Public().(ed25519.PublicKey), preimage, signature) {
		t.Fatalf("fixture signature invalid: %v", err)
	}
}

func writeManifest(t *testing.T, root string, manifest Manifest) {
	t.Helper()
	raw, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func executableMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o666
	}
	return 0o755
}
