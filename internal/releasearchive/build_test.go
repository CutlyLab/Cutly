package releasearchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) Options {
	t.Helper()
	repo := t.TempDir()
	files := map[string]string{
		"go.mod":                       "module " + modulePath + "\n\ngo 1.25.0\n",
		"sdk/go/cutly/doc.go":          "package cutly\nconst Version = \"1.2.3-rc.1\"\n",
		"agent/protocol/protocol.go":   "package agentprotocol\nconst ProtocolVersion = 1\n",
		"agent/protocolv2/protocol.go": "package protocolv2\nconst SchemaVersion = 2\n",
		"LICENSE":                      "project license\n", "cmd/cutlyd/README.md": "daemon docs\n", "go.sum": "module checksum\n",
		"deps/THIRD_PARTY_NOTICES.md": "third party notices\n", "deps/sbom.spdx.json": "{\"sbom\":true}\n",
		"deps/dependency-lock.json": "{\"lock\":true}\n", "deps/licenses/one.LICENSE": "one license\n", "deps/licenses/two.PATENTS": "two patents\n",
	}
	for name, data := range files {
		file := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return Options{Repo: repo, Output: t.TempDir(), Target: "linux/amd64"}
}

func fakeTools(t *testing.T) tools {
	t.Helper()
	return tools{
		build: func(_ context.Context, _ string, target target, name, output, _ string) error {
			return os.WriteFile(output, []byte(name+" "+target.os+"/"+target.arch), 0755)
		},
		info: func(r io.ReaderAt) (*debug.BuildInfo, error) {
			data := make([]byte, 100)
			n, err := r.ReadAt(data, 0)
			if err != nil && err != io.EOF {
				return nil, err
			}
			fields := strings.Fields(string(data[:n]))
			if len(fields) != 2 {
				return nil, fmt.Errorf("bad fake binary")
			}
			pair := strings.Split(fields[1], "/")
			if len(pair) != 2 {
				return nil, fmt.Errorf("bad fake target")
			}
			return buildInfo(target{pair[0], pair[1]}, fields[0]), nil
		},
		smoke: func(_ context.Context, _ string, _ []string, _ string) error { return nil },
	}
}

func buildInfo(target target, name string) *debug.BuildInfo {
	return &debug.BuildInfo{Path: modulePath + "/cmd/" + name, Main: debug.Module{Path: modulePath, Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "GOOS", Value: target.os}, {Key: "GOARCH", Value: target.arch}, {Key: "CGO_ENABLED", Value: "0"}, {Key: "-trimpath", Value: "true"},
	}}
}

func TestBuildAllReleaseTargets(t *testing.T) {
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "freebsd/amd64"} {
		t.Run(target, func(t *testing.T) {
			opts := fixture(t)
			opts.Target = target
			opts.ExpectedVersion = "1.2.3-rc.1"
			result, err := build(context.Background(), opts, fakeTools(t))
			if err != nil {
				t.Fatal(err)
			}
			ext := ".tar.gz"
			if strings.HasPrefix(target, "windows/") {
				ext = ".zip"
			}
			want := "cutly-1.2.3-rc.1-" + strings.ReplaceAll(target, "/", "-") + ext
			if filepath.Base(result.Archive) != want || result.Version != "1.2.3-rc.1" || result.Target != target {
				t.Fatalf("unexpected result %+v", result)
			}
			p, err := prepare(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			suffix := ""
			if strings.HasPrefix(target, "windows/") {
				suffix = ".exe"
			}
			for _, name := range commandNames(p.target) {
				p.files[name+suffix] = member{mode: 0755, size: -1}
			}
			if err := validate(context.Background(), result.Archive, p, fakeTools(t).info); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != want {
				t.Fatalf("staging leaked: %v", entries)
			}
			original, err := os.ReadFile(result.Archive)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := build(context.Background(), opts, fakeTools(t)); err == nil {
				t.Fatal("existing archive overwritten")
			}
			after, err := os.ReadFile(result.Archive)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Fatal("existing archive changed")
			}
		})
	}
}

func TestBuildFailureCleanup(t *testing.T) {
	for _, failure := range []string{"build", "metadata", "smoke", "cancel", "publish-race"} {
		t.Run(failure, func(t *testing.T) {
			opts := fixture(t)
			opts.Target = runtime.GOOS + "/" + runtime.GOARCH
			tool := fakeTools(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "build":
				tool.build = func(context.Context, string, target, string, string, string) error { return errors.New("build failed") }
			case "metadata":
				tool.info = func(io.ReaderAt) (*debug.BuildInfo, error) { return buildInfo(target{"wrong", "amd64"}, "cutly"), nil }
			case "smoke":
				opts.Smoke = true
				tool.smoke = func(context.Context, string, []string, string) error { return errors.New("smoke failed") }
			case "cancel":
				original := tool.build
				tool.build = func(c context.Context, r string, target target, n, o, flags string) error {
					err := original(c, r, target, n, o, flags)
					cancel()
					return err
				}
			case "publish-race":
				original := tool.info
				tool.info = func(r io.ReaderAt) (*debug.BuildInfo, error) {
					p, err := prepare(context.Background(), opts)
					if err != nil {
						return nil, err
					}
					if err := os.WriteFile(p.archive, []byte("concurrent owner"), 0644); err != nil {
						return nil, err
					}
					return original(r)
				}
			}
			if result, err := build(ctx, opts, tool); err == nil || result != (Result{}) {
				t.Fatalf("failure returned success: %+v %v", result, err)
			}
			entries, err := os.ReadDir(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "publish-race" {
				if len(entries) != 1 {
					t.Fatalf("unexpected entries %v", entries)
				}
				data, err := os.ReadFile(filepath.Join(opts.Output, entries[0].Name()))
				if err != nil || string(data) != "concurrent owner" {
					t.Fatalf("concurrent file overwritten: %q %v", data, err)
				}
			} else if len(entries) != 0 {
				t.Fatalf("partial output: %v", entries)
			}
		})
	}
}

func TestPrepareRejectsInvalidInputs(t *testing.T) {
	for _, test := range []string{"missing-repo", "missing-output", "target", "version", "module", "unsafe-version", "symlink", "license-directory", "empty-licenses", "missing-checksum", "cross-smoke"} {
		t.Run(test, func(t *testing.T) {
			opts := fixture(t)
			switch test {
			case "missing-repo":
				opts.Repo = ""
			case "missing-output":
				opts.Output = filepath.Join(opts.Output, "missing")
			case "target":
				opts.Target = "windows/arm64"
			case "version":
				opts.ExpectedVersion = "9.9.9"
			case "module":
				if err := os.WriteFile(filepath.Join(opts.Repo, "go.mod"), []byte("module other\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "unsafe-version":
				if err := os.WriteFile(filepath.Join(opts.Repo, "sdk/go/cutly/doc.go"), []byte("package cutly\nconst Version=\"../bad\"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				license := filepath.Join(opts.Repo, "LICENSE")
				if err := os.Remove(license); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(opts.Repo, "go.sum"), license); err != nil {
					t.Fatal(err)
				}
			case "license-directory":
				if err := os.Mkdir(filepath.Join(opts.Repo, "deps/licenses/nested"), 0755); err != nil {
					t.Fatal(err)
				}
			case "empty-licenses":
				if err := os.RemoveAll(filepath.Join(opts.Repo, "deps/licenses")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(opts.Repo, "deps/licenses"), 0755); err != nil {
					t.Fatal(err)
				}
			case "missing-checksum":
				if err := os.Remove(filepath.Join(opts.Repo, "go.sum")); err != nil {
					t.Fatal(err)
				}
			case "cross-smoke":
				opts.Smoke = true
				opts.Target = "windows/amd64"
				if runtime.GOOS == "windows" {
					opts.Target = "linux/amd64"
				}
			}
			if _, err := prepare(context.Background(), opts); err == nil {
				t.Fatal("accepted invalid inputs")
			}
		})
	}
}

func TestSmokeExactCommands(t *testing.T) {
	opts := fixture(t)
	opts.Target = runtime.GOOS + "/" + runtime.GOARCH
	opts.Smoke = true
	tool := fakeTools(t)
	var calls []string
	tool.smoke = func(_ context.Context, file string, args []string, want string) error {
		calls = append(calls, filepath.Base(file)+" "+strings.Join(args, " ")+" => "+want)
		return nil
	}
	if _, err := build(context.Background(), opts, tool); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	want := []string{"cutly" + suffix + " version => cutly 1.2.3-rc.1 (Document v2 Agent schema 2; legacy Agent protocol v1)\n", "cutlyd" + suffix + " -version => cutlyd 1.2.3-rc.1\n"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("smoke commands %q want %q", calls, want)
	}
}

func TestPackagedBuildStampsDaemonWithWorkerAndRuntimePolicy(t *testing.T) {
	opts := fixture(t)
	opts.MediaRuntimeBundleFingerprint = "sha256:" + strings.Repeat("a", 64)
	opts.MediaRuntimeTrustedKeys = "release-2026=" + strings.Repeat("b", 64)
	tool := fakeTools(t)
	original := tool.build
	var daemonFlags string
	tool.build = func(ctx context.Context, repo string, target target, name, output, flags string) error {
		if name == "cutlyd" {
			daemonFlags = flags
		}
		return original(ctx, repo, target, name, output, flags)
	}
	if _, err := build(context.Background(), opts, tool); err != nil {
		t.Fatal(err)
	}
	worker := []byte("cutly-worker-v2 linux/amd64")
	wantWorker := fmt.Sprintf("sha256:%x", sha256.Sum256(worker))
	for _, want := range []string{
		"providerDistributionMode=packaged",
		"stampedProviderExecutableDigest=" + wantWorker,
		"mediaRuntimeDistributionMode=packaged",
		"stampedMediaRuntimeBundleFingerprint=" + opts.MediaRuntimeBundleFingerprint,
		"stampedMediaRuntimeTrustedKeys=" + opts.MediaRuntimeTrustedKeys,
	} {
		if !strings.Contains(daemonFlags, want) {
			t.Fatalf("daemon ldflags missing %q: %s", want, daemonFlags)
		}
	}
}

func TestBuildInfoValidation(t *testing.T) {
	target := target{"freebsd", "amd64"}
	for _, test := range []string{"valid-devel", "path", "module", "goos", "goarch", "cgo", "trimpath", "missing", "duplicate"} {
		t.Run(test, func(t *testing.T) {
			bi := buildInfo(target, "cutlyd")
			switch test {
			case "path":
				bi.Path = modulePath + "/cmd/cutly"
			case "module":
				bi.Main.Path = "other"
			case "goos":
				bi.Settings[0].Value = "linux"
			case "goarch":
				bi.Settings[1].Value = "arm64"
			case "cgo":
				bi.Settings[2].Value = "1"
			case "trimpath":
				bi.Settings[3].Value = "false"
			case "missing":
				bi.Settings = bi.Settings[:3]
			case "duplicate":
				bi.Settings = append(bi.Settings, bi.Settings[0])
			}
			err := validateBuildInfo(bi, target, "cutlyd")
			if (err == nil) != (test == "valid-devel") {
				t.Fatalf("validation = %v", err)
			}
		})
	}
}

type archiveEntry struct {
	name, data string
	mode       os.FileMode
	kind       byte
}

func maliciousArchive(t *testing.T, p plan, entries []archiveEntry) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), filepath.Base(p.archive))
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if p.target.os == "windows" {
		w := zip.NewWriter(f)
		for _, e := range entries {
			h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			h.SetMode(e.mode)
			dst, err := w.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(dst, e.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(f)
		w := tar.NewWriter(gz)
		for _, e := range entries {
			kind := e.kind
			if kind == 0 {
				kind = tar.TypeReg
			}
			if e.mode.IsDir() {
				kind = tar.TypeDir
			}
			h := &tar.Header{Name: e.name, Size: int64(len(e.data)), Mode: int64(e.mode.Perm()), Typeflag: kind}
			if kind == tar.TypeSymlink || kind == tar.TypeLink {
				h.Linkname = "outside"
				h.Size = 0
			}
			if err := w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size != 0 {
				if _, err := io.WriteString(w, e.data); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestValidatorRejectsMalformedArchives(t *testing.T) {
	for _, target := range []string{"linux/amd64", "windows/amd64"} {
		t.Run(target, func(t *testing.T) {
			opts := fixture(t)
			opts.Target = target
			p, err := prepare(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []string{"absolute", "traversal", "dot", "backslash", "drive", "other-root", "duplicate", "symlink", "hardlink", "extra", "changed-source", "directory-mode", "file-mode", "missing", "corrupt"} {
				t.Run(test, func(t *testing.T) {
					root := archiveEntry{name: p.root + "/", mode: 0755 | os.ModeDir}
					legal := archiveEntry{name: p.root + "/LICENSE", data: "project license\n", mode: 0644}
					entries := []archiveEntry{root, legal}
					switch test {
					case "absolute":
						entries[1].name = "/LICENSE"
					case "traversal":
						entries[1].name = p.root + "/../LICENSE"
					case "dot":
						entries[1].name = p.root + "/./LICENSE"
					case "backslash":
						entries[1].name = p.root + "/deps\\LICENSE"
					case "drive":
						entries[1].name = "C:/LICENSE"
					case "other-root":
						entries[1].name = "other/LICENSE"
					case "duplicate":
						entries = append(entries, legal)
					case "symlink":
						entries[1].mode = 0777 | os.ModeSymlink
						entries[1].kind = tar.TypeSymlink
					case "hardlink":
						entries[1].kind = tar.TypeLink
						if p.target.os == "windows" {
							entries[1].mode = 0777 | os.ModeSymlink
						}
					case "extra":
						entries[1].name = p.root + "/extra"
					case "changed-source":
						entries[1].data = "changed license\n"
					case "directory-mode":
						entries[0].mode = 0700 | os.ModeDir
					case "file-mode":
						entries[1].mode = 0755
					case "missing":
						entries = entries[:1]
					}
					archive := maliciousArchive(t, p, entries)
					if test == "corrupt" {
						data, err := os.ReadFile(archive)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(archive, data[:len(data)/2], 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := validate(context.Background(), archive, p, fakeTools(t).info); err == nil {
						t.Fatal("accepted malformed archive")
					}
				})
			}
		})
	}
}

func TestSourceChangesDetected(t *testing.T) {
	opts := fixture(t)
	tool := fakeTools(t)
	original := tool.build
	tool.build = func(ctx context.Context, repo string, target target, name, output, flags string) error {
		if err := os.WriteFile(filepath.Join(repo, "LICENSE"), []byte("changed license\n"), 0644); err != nil {
			return err
		}
		return original(ctx, repo, target, name, output, flags)
	}
	if _, err := build(context.Background(), opts, tool); err == nil {
		t.Fatal("accepted changed source")
	}
	entries, err := os.ReadDir(opts.Output)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial archive %v %v", entries, err)
	}
}

func TestBoundedCommand(t *testing.T) {
	// Re-execute this test binary as a controlled subprocess without shell tools.
	if os.Getenv("CUTLY_ARCHIVE_TEST_CHILD") == "1" {
		if os.Getenv("CUTLY_ARCHIVE_TEST_MODE") == "wait" {
			time.Sleep(time.Minute)
		}
		fmt.Print(strings.Repeat("x", 70<<10))
		os.Exit(0)
	}
	env := append(os.Environ(), "CUTLY_ARCHIVE_TEST_CHILD=1")
	if _, err := command(context.Background(), t.TempDir(), env, 5*time.Second, os.Args[0], "-test.run=^TestBoundedCommand$"); err == nil {
		t.Fatal("unbounded output accepted")
	}
	env = append(env, "CUTLY_ARCHIVE_TEST_MODE=wait")
	start := time.Now()
	if _, err := command(context.Background(), t.TempDir(), env, 100*time.Millisecond, os.Args[0], "-test.run=^TestBoundedCommand$"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("subprocess timeout was not bounded")
	}
}

func TestIntegrationHostAndWindows(t *testing.T) {
	if os.Getenv("CUTLY_RELEASE_ARCHIVE_INTEGRATION") != "1" {
		t.Skip("set CUTLY_RELEASE_ARCHIVE_INTEGRATION=1 for real host and Windows builds")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{runtime.GOOS + "/" + runtime.GOARCH, "windows/amd64"} {
		t.Run(target, func(t *testing.T) {
			opts := Options{Repo: repo, Output: t.TempDir(), Target: target, Smoke: target == runtime.GOOS+"/"+runtime.GOARCH}
			result, err := Build(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(context.Background(), result.Archive, opts); err != nil {
				t.Fatal(err)
			}
			t.Logf("validated %s; runtime smoke=%v (cross-target runtime not exercised)", result.Archive, opts.Smoke)
		})
	}
}
