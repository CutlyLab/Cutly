// Package releasearchive builds and validates local command release archives.
// It does not publish releases, start services, or exercise media runtimes.
package releasearchive

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const modulePath = "github.com/CutlyLab/Cutly"
const maxFileBytes = 256 << 20

// Options requires an existing repository and output directory. Target is one
// of the six release workflow GOOS/GOARCH pairs. Existing archives are never
// overwritten. ExpectedVersion, when set, must equal the repository SDK constant.
type Options struct {
	Repo, Target, Output, ExpectedVersion string
	MediaRuntimeBundleFingerprint         string
	MediaRuntimeTrustedKeys               string
	Smoke                                 bool
}

// Result identifies a fully validated archive. Cross-target build information
// proves compile identity, not runtime behavior; Main.Version may be (devel).
type Result struct{ Archive, Version, Target string }

type target struct{ os, arch string }
type member struct {
	source string
	mode   os.FileMode
	size   int64
	digest [32]byte
}
type plan struct {
	opts                   Options
	target                 target
	version, root, archive string
	files                  map[string]member
	dirs                   map[string]bool
	packaged               bool
}
type tools struct {
	build func(context.Context, string, target, string, string, string) error
	info  func(io.ReaderAt) (*debug.BuildInfo, error)
	smoke func(context.Context, string, []string, string) error
}

// Build runs bounded, CGO-disabled Go builds from opts.Repo, validates the
// completed archive against its input bytes and binary build information, then
// atomically installs its exact name in opts.Output without overwriting a file.
// Failed attempts remove their staging directory and publish no final archive.
func Build(ctx context.Context, opts Options) (Result, error) {
	return build(ctx, opts, tools{build: buildCommand, info: buildinfo.Read, smoke: smokeCommand})
}

func build(ctx context.Context, opts Options, tool tools) (Result, error) {
	p, err := prepare(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Lstat(p.archive); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = fmt.Errorf("archive already exists: %s", p.archive)
		}
		return Result{}, err
	}
	stage, err := os.MkdirTemp(p.opts.Output, ".cutly-archive-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)
	suffix := ""
	if p.target.os == "windows" {
		suffix = ".exe"
	}
	buildOrder := []string{"cutly-worker-v2", "cutly", "cutlyd"}
	if p.target.os == "darwin" || p.target.os == "linux" {
		buildOrder = append(buildOrder, "cutly-ffmpeg-supervisor-v2")
	}
	workerDigest := ""
	for _, name := range buildOrder {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		output := filepath.Join(stage, name+suffix)
		ldflags := "-s -w"
		if name == "cutlyd" && p.packaged {
			ldflags += " -X github.com/CutlyLab/Cutly/internal/buildidentity.providerDistributionMode=packaged"
			ldflags += " -X github.com/CutlyLab/Cutly/internal/buildidentity.stampedProviderExecutableDigest=" + workerDigest
			ldflags += " -X github.com/CutlyLab/Cutly/internal/buildidentity.mediaRuntimeDistributionMode=packaged"
			ldflags += " -X github.com/CutlyLab/Cutly/internal/buildidentity.stampedMediaRuntimeTrustedKeys=" + p.opts.MediaRuntimeTrustedKeys
			ldflags += " -X github.com/CutlyLab/Cutly/internal/buildidentity.stampedMediaRuntimeBundleFingerprint=" + p.opts.MediaRuntimeBundleFingerprint
		}
		if err := tool.build(ctx, p.opts.Repo, p.target, name, output, ldflags); err != nil {
			return Result{}, fmt.Errorf("build %s: %w", name, err)
		}
		if err := os.Chmod(output, 0755); err != nil {
			return Result{}, err
		}
		m, err := sourceMember(output, 0755)
		if err != nil {
			return Result{}, err
		}
		p.files[name+suffix] = m
		if name == "cutly-worker-v2" {
			workerDigest = "sha256:" + hex.EncodeToString(m.digest[:])
		}
	}
	tempArchive := filepath.Join(stage, filepath.Base(p.archive))
	if err := writeArchive(ctx, tempArchive, p); err != nil {
		return Result{}, err
	}
	if err := validate(ctx, tempArchive, p, tool.info); err != nil {
		return Result{}, fmt.Errorf("validate archive: %w", err)
	}
	if p.opts.Smoke {
		protocol, err := literalConstant(filepath.Join(p.opts.Repo, "agent", "protocol", "protocol.go"), "ProtocolVersion", token.INT)
		if err != nil {
			return Result{}, err
		}
		protocolV2, err := literalConstant(filepath.Join(p.opts.Repo, "agent", "protocolv2", "protocol.go"), "SchemaVersion", token.INT)
		if err != nil {
			return Result{}, err
		}
		for _, command := range []struct {
			name string
			args []string
			want string
		}{
			{"cutly", []string{"version"}, fmt.Sprintf("cutly %s (Document v2 Agent schema %s; legacy Agent protocol v%s)\n", p.version, protocolV2, protocol)},
			{"cutlyd", []string{"-version"}, "cutlyd " + p.version + "\n"},
		} {
			// Execute only the exact bytes that were validated inside the archive.
			if err := tool.smoke(ctx, filepath.Join(stage, command.name+suffix), command.args, command.want); err != nil {
				return Result{}, fmt.Errorf("smoke %s: %w", command.name, err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// Link provides an atomic no-replace publish on the same filesystem. There is
	// deliberately no rename/copy fallback that could overwrite or expose a partial.
	if err := os.Link(tempArchive, p.archive); err != nil {
		return Result{}, fmt.Errorf("publish archive without overwrite: %w", err)
	}
	return Result{Archive: p.archive, Version: p.version, Target: p.opts.Target}, nil
}

// Validate checks an existing archive against repository legal/dependency bytes,
// its exact member set and binary compile identity. Build additionally compares
// archived binary bytes with the fresh build outputs. Validate does not execute
// binaries, and does not claim Main.Version is the SDK version.
func Validate(ctx context.Context, archive string, opts Options) error {
	p, err := prepare(ctx, opts)
	if err != nil {
		return err
	}
	if filepath.Base(archive) != filepath.Base(p.archive) {
		return fmt.Errorf("unexpected archive name")
	}
	suffix := ""
	if p.target.os == "windows" {
		suffix = ".exe"
	}
	for _, name := range commandNames(p.target) {
		p.files[name+suffix] = member{mode: 0755, size: -1}
	}
	return validate(ctx, archive, p, buildinfo.Read)
}

func commandNames(t target) []string {
	commands := []string{"cutly", "cutlyd", "cutly-worker-v2"}
	if t.os == "darwin" || t.os == "linux" {
		commands = append(commands, "cutly-ffmpeg-supervisor-v2")
	}
	return commands
}

func prepare(ctx context.Context, opts Options) (plan, error) {
	if err := ctx.Err(); err != nil {
		return plan{}, err
	}
	if opts.Repo == "" || opts.Output == "" {
		return plan{}, fmt.Errorf("repo and output directories are required")
	}
	var t target
	switch opts.Target {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "freebsd/amd64":
		pair := strings.Split(opts.Target, "/")
		t = target{pair[0], pair[1]}
	default:
		return plan{}, fmt.Errorf("unsupported release target %q", opts.Target)
	}
	if opts.Smoke && (t.os != runtime.GOOS || t.arch != runtime.GOARCH) {
		return plan{}, fmt.Errorf("smoke requires host target %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	var err error
	opts.Repo, err = filepath.Abs(opts.Repo)
	if err != nil {
		return plan{}, err
	}
	opts.Output, err = filepath.Abs(opts.Output)
	if err != nil {
		return plan{}, err
	}
	for _, dir := range []string{opts.Repo, opts.Output} {
		info, err := os.Stat(dir)
		if err != nil {
			return plan{}, err
		}
		if !info.IsDir() {
			return plan{}, fmt.Errorf("not a directory: %s", dir)
		}
	}
	mod, err := os.ReadFile(filepath.Join(opts.Repo, "go.mod"))
	if err != nil {
		return plan{}, err
	}
	fields := strings.Fields(string(mod))
	if len(fields) < 2 || fields[0] != "module" || fields[1] != modulePath {
		return plan{}, fmt.Errorf("unexpected repository module")
	}
	// Read the version from the same repository that is built, never from the
	// helper's compiled-in SDK, so --repo cannot silently cross version identities.
	version, err := literalConstant(filepath.Join(opts.Repo, "sdk", "go", "cutly", "doc.go"), "Version", token.STRING)
	if err != nil {
		return plan{}, err
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`).MatchString(version) {
		return plan{}, fmt.Errorf("unsafe SDK version %q", version)
	}
	if opts.ExpectedVersion != "" && opts.ExpectedVersion != version {
		return plan{}, fmt.Errorf("expected version %q differs from repository SDK version %q", opts.ExpectedVersion, version)
	}
	packaged := opts.MediaRuntimeBundleFingerprint != "" || opts.MediaRuntimeTrustedKeys != ""
	if packaged && (!canonicalSHA256(opts.MediaRuntimeBundleFingerprint) || !canonicalTrustedKeys(opts.MediaRuntimeTrustedKeys)) {
		return plan{}, fmt.Errorf("packaged release requires canonical media runtime fingerprint and trusted keys")
	}
	root := "cutly-" + version + "-" + t.os + "-" + t.arch
	extension := ".tar.gz"
	if t.os == "windows" {
		extension = ".zip"
	}
	p := plan{opts: opts, target: t, version: version, root: root, archive: filepath.Join(opts.Output, root+extension), files: map[string]member{}, dirs: map[string]bool{"": true, "deps": true, "deps/licenses": true}, packaged: packaged}
	for dest, src := range map[string]string{
		"LICENSE": "LICENSE", "CUTLYD.md": "cmd/cutlyd/README.md", "go.sum": "go.sum",
		"deps/THIRD_PARTY_NOTICES.md": "deps/THIRD_PARTY_NOTICES.md", "deps/sbom.spdx.json": "deps/sbom.spdx.json", "deps/dependency-lock.json": "deps/dependency-lock.json",
	} {
		m, err := sourceMember(filepath.Join(opts.Repo, filepath.FromSlash(src)), 0644)
		if err != nil {
			return plan{}, err
		}
		p.files[dest] = m
	}
	licenses := filepath.Join(opts.Repo, "deps", "licenses")
	entries, err := os.ReadDir(licenses)
	if err != nil {
		return plan{}, err
	}
	if len(entries) == 0 {
		return plan{}, fmt.Errorf("no dependency license files")
	}
	for _, entry := range entries {
		m, err := sourceMember(filepath.Join(licenses, entry.Name()), 0644)
		if err != nil {
			return plan{}, err
		}
		p.files["deps/licenses/"+entry.Name()] = m
	}
	return p, nil
}

func canonicalSHA256(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[7:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func canonicalTrustedKeys(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	prior := ""
	for _, record := range strings.Split(value, ",") {
		key, encoded, ok := strings.Cut(record, "=")
		if !ok || key == "" || key <= prior || len(encoded) != 64 {
			return false
		}
		for index, r := range key {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || index > 0 && (r == '.' || r == '_' || r == '-')) {
				return false
			}
		}
		for _, r := range encoded {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
		prior = key
	}
	return true
}

func literalConstant(file, name string, kind token.Token) (string, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		return "", err
	}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, ident := range value.Names {
				if ident.Name != name {
					continue
				}
				if i >= len(value.Values) {
					return "", fmt.Errorf("%s must be an explicit literal constant", name)
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != kind {
					return "", fmt.Errorf("%s must be a literal", name)
				}
				if kind == token.STRING {
					return strconv.Unquote(literal.Value)
				}
				return literal.Value, nil
			}
		}
	}
	return "", fmt.Errorf("missing %s constant in %s", name, file)
}

func sourceMember(file string, mode os.FileMode) (member, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return member{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return member{}, fmt.Errorf("source must be a bounded regular file: %s", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return member{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return member{}, err
	}
	if n != info.Size() {
		return member{}, fmt.Errorf("source size changed: %s", file)
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return member{source: file, mode: mode, size: n, digest: digest}, nil
}

func command(ctx context.Context, dir string, env []string, timeout time.Duration, program string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, 2*time.Second
	output := &boundedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if ctx.Err() != nil {
		return output.String(), ctx.Err()
	}
	if err != nil {
		return output.String(), fmt.Errorf("%w: %s", err, output.String())
	}
	if output.overflow {
		return output.String(), fmt.Errorf("command output exceeded 64 KiB")
	}
	return output.String(), nil
}

type boundedOutput struct {
	data     []byte
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	space := (64 << 10) - len(b.data)
	if len(p) > space {
		p = p[:space]
		b.overflow = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedOutput) String() string { return string(b.data) }

func buildCommand(ctx context.Context, repo string, t target, name, output, ldflags string) error {
	overrides := map[string]string{"GOOS": t.os, "GOARCH": t.arch, "CGO_ENABLED": "0", "GOFLAGS": "", "GOWORK": "off", "GOENV": "off", "GO111MODULE": "on", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off"}
	env := []string{}
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if _, exists := overrides[key]; !exists {
			env = append(env, setting)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	_, err := command(ctx, repo, env, 5*time.Minute, "go", "build", "-mod=readonly", "-trimpath", "-ldflags="+ldflags, "-o", output, "./cmd/"+name)
	return err
}

func smokeCommand(ctx context.Context, file string, args []string, want string) error {
	got, err := command(ctx, filepath.Dir(file), os.Environ(), 10*time.Second, file, args...)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("version output %q, want %q", got, want)
	}
	return nil
}
