// Package ffprobe implements Cutly's first local-file FFprobe adapter.
//
// It never invokes a shell, never accepts network URLs, bounds captured output, fingerprints the
// executable, caller-supplied runtime identity, and semantic profiles, and probes a private
// content-addressed copy of each input. Protocol restrictions and private materialization are not
// an operating-system sandbox; callers probing untrusted containers must provide that boundary.
// The inherited-descriptor execution profiles are Unix-only; New fails closed elsewhere.
package ffprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

const (
	// Implementation is a stable product identifier, not a semantic-version or cache key.
	Implementation = "cutly.ffprobe"

	// AdapterSemanticProfileV1 versions invocation and materialization semantics.
	AdapterSemanticProfileV1 = "cutly.ffprobe.adapter.v1"
	// ParserSemanticProfileV1 versions FFprobe JSON-to-ProbeManifest semantics.
	ParserSemanticProfileV1 = "cutly.ffprobe.parser.v1"
	// AdapterSemanticProfileV2 adds a bounded, descriptor-bound video frame-index scan.
	AdapterSemanticProfileV2 = "cutly.ffprobe.adapter.v2"
	// ParserSemanticProfileV2 promotes frame-rate mode only from a complete exact PTS scan.
	ParserSemanticProfileV2 = "cutly.ffprobe.parser.v2"
	// CommandEnvironmentProfileV1 removes host loader/config injection and fixes locale/timezone.
	CommandEnvironmentProfileV1 = "cutly.ffprobe.environment.minimal.v1"

	defaultProbeSize      = int64(50 * 1024 * 1024)
	defaultAnalyzeMicros  = int64(10 * 1000 * 1000)
	defaultMaxOutputBytes = int64(32 * 1024 * 1024)
	defaultMaxStderrBytes = int64(1024 * 1024)
)

var (
	ErrExecutable     = errors.New("ffprobe: executable is invalid")
	ErrBuildIdentity  = errors.New("ffprobe: build identity is invalid")
	ErrInput          = errors.New("ffprobe: input is invalid")
	ErrContentChanged = errors.New("ffprobe: input content changed while probing")
	ErrContentHash    = errors.New("ffprobe: input content hash mismatch")
	ErrOutputLimit    = errors.New("ffprobe: output limit exceeded")
	ErrCommand        = errors.New("ffprobe: command failed")
	ErrParse          = errors.New("ffprobe: output parse failed")
	ErrCleanup        = errors.New("ffprobe: private input cleanup failed")
)

var (
	fingerprintPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*:[0-9a-f]{16,}$`)
	semanticProfilePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,255}$`)
)

// SemanticProfile explicitly identifies Cutly code semantics that affect manifests or probing.
// These values are separate from Implementation so a stable product name cannot accidentally be
// treated as a parser/build version.
type SemanticProfile struct {
	Adapter string `json:"adapter"`
	Parser  string `json:"parser"`
}

// BuildIdentity is supplied by the controlled runtime resolver that constructs an Adapter.
// RuntimeDependencyFingerprint must identify the resolved dynamic-library closure or a verified
// hermetic/static bundle. FFprobe's version text is not accepted as a substitute for this proof.
type BuildIdentity struct {
	SemanticProfile              SemanticProfile `json:"semantic_profile"`
	RuntimeDependencyFingerprint string          `json:"runtime_dependency_fingerprint"`
}

// DefaultBuildIdentity selects Cutly's current adapter/parser semantics while requiring callers to
// provide the runtime-dependency identity produced by their controlled resolver.
func DefaultBuildIdentity(runtimeDependencyFingerprint string) BuildIdentity {
	return BuildIdentity{
		SemanticProfile: SemanticProfile{
			Adapter: AdapterSemanticProfileV2,
			Parser:  ParserSemanticProfileV2,
		},
		RuntimeDependencyFingerprint: runtimeDependencyFingerprint,
	}
}

// Options are part of the probe cache identity. Zero values select conservative defaults.
type Options struct {
	ProbeSizeBytes        int64 `json:"probe_size_bytes"`
	AnalyzeDurationMicros int64 `json:"analyze_duration_micros"`
	MaxOutputBytes        int64 `json:"max_output_bytes"`
	MaxStderrBytes        int64 `json:"max_stderr_bytes"`
}

// Request binds a local file to a stable Document asset. ExpectedContentHash is optional; when
// present it must equal the adapter's SHA-256 of the file bytes.
type Request struct {
	AssetID             videoedit.ID
	Path                string
	ExpectedContentHash string
}

// Adapter is immutable after construction and safe for concurrent Probe calls.
type Adapter struct {
	executable         string
	binaryFingerprint  string
	buildFingerprint   string
	options            Options
	optionsFingerprint string
}

// New resolves and verifies one concrete FFprobe executable. Its content hash, bounded version
// report, explicit Cutly semantic profiles, and caller-resolved runtime dependency identity jointly
// become cache identity. New does not discover or claim to sandbox dynamic dependencies, and the
// controlled runtime remains responsible for pinning executable/dependency paths against replacement.
func New(ctx context.Context, executable string, identity BuildIdentity, options Options) (*Adapter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrExecutable)
	}
	if err := validateBuildIdentity(identity); err != nil {
		return nil, err
	}
	if err := validateInheritedInputPlatform(); err != nil {
		return nil, err
	}
	resolved, err := resolveExecutable(executable)
	if err != nil {
		return nil, err
	}
	options, err = normalizedOptions(options)
	if err != nil {
		return nil, err
	}
	binaryFingerprint, _, err := hashRegularFile(ctx, resolved)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: hash %q: %v", ErrExecutable, resolved, err)
	}

	stdout := newBoundedBuffer(64 * 1024)
	stderr := newBoundedBuffer(64 * 1024)
	command := exec.CommandContext(ctx, resolved, "-version")
	command.Env = controlledCommandEnvironment()
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %v: %s", ErrExecutable, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded || stderr.exceeded || !bytes.Contains(stdout.Bytes(), []byte("ffprobe version")) {
		return nil, fmt.Errorf("%w: executable did not return a bounded FFprobe version", ErrExecutable)
	}
	buildFingerprint, optionsFingerprint, err := adapterFingerprints(
		binaryFingerprint,
		strings.TrimSpace(stdout.String()),
		identity,
		options,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: fingerprints: %v", ErrExecutable, err)
	}
	return &Adapter{
		executable:         resolved,
		binaryFingerprint:  binaryFingerprint,
		buildFingerprint:   buildFingerprint,
		options:            options,
		optionsFingerprint: optionsFingerprint,
	}, nil
}

// Probe materializes one local regular file into a private content-addressed snapshot, inspects
// that snapshot, and converts FFprobe JSON into ProbeManifest v1. The caller's path is never
// reopened after materialization.
func (adapter *Adapter) Probe(ctx context.Context, request Request) (
	manifest probe.ProbeManifest,
	diagnostics []probe.Diagnostic,
	returnErr error,
) {
	if adapter == nil || adapter.executable == "" {
		return probe.ProbeManifest{}, nil, fmt.Errorf("%w: uninitialized adapter", ErrExecutable)
	}
	if ctx == nil {
		return probe.ProbeManifest{}, nil, fmt.Errorf("%w: nil context", ErrInput)
	}
	if strings.TrimSpace(string(request.AssetID)) == "" {
		return probe.ProbeManifest{}, nil, fmt.Errorf("%w: asset ID is required", ErrInput)
	}
	if err := ctx.Err(); err != nil {
		return probe.ProbeManifest{}, nil, err
	}
	currentBuild, _, err := hashRegularFile(ctx, adapter.executable)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return probe.ProbeManifest{}, nil, ctxErr
		}
		return probe.ProbeManifest{}, nil, fmt.Errorf("%w: verify executable: %v", ErrExecutable, err)
	}
	if currentBuild != adapter.binaryFingerprint {
		return probe.ProbeManifest{}, nil, fmt.Errorf("%w: executable changed after adapter initialization", ErrExecutable)
	}

	materialized, err := materializeLocalInput(ctx, request.Path, request.ExpectedContentHash)
	if err != nil {
		return probe.ProbeManifest{}, nil, err
	}
	defer func() {
		if cleanupErr := materialized.Close(); cleanupErr != nil {
			if returnErr == nil {
				manifest = probe.ProbeManifest{}
				diagnostics = nil
			}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: %v", ErrCleanup, cleanupErr))
		}
	}()

	baseOutput, err := adapter.runProbeCommand(ctx, materialized, probeArguments)
	if err != nil {
		return probe.ProbeManifest{}, nil, err
	}

	manifest, err = parseOutput(baseOutput, parseIdentity{
		assetID:            request.AssetID,
		contentHash:        materialized.contentHash,
		sizeBytes:          materialized.sizeBytes,
		buildFingerprint:   adapter.buildFingerprint,
		optionsFingerprint: adapter.optionsFingerprint,
	})
	if err != nil {
		return probe.ProbeManifest{}, nil, err
	}
	if manifestHasVideo(manifest) {
		frameOutput, scanErr := adapter.runProbeCommand(ctx, materialized, frameScanArguments)
		if scanErr != nil {
			return probe.ProbeManifest{}, nil, scanErr
		}
		if scanErr := applyFrameScan(&manifest, frameOutput); scanErr != nil {
			return probe.ProbeManifest{}, nil, scanErr
		}
	}

	afterHash, afterSize, err := materialized.verify(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return probe.ProbeManifest{}, nil, ctxErr
		}
		return probe.ProbeManifest{}, nil, fmt.Errorf("%w: verify private input: %v", ErrInput, err)
	}
	if afterHash != materialized.contentHash || afterSize != materialized.sizeBytes {
		return probe.ProbeManifest{}, nil, fmt.Errorf(
			"%w: private snapshot before=%s/%d after=%s/%d",
			ErrContentChanged,
			materialized.contentHash,
			materialized.sizeBytes,
			afterHash,
			afterSize,
		)
	}

	diagnostics = probe.Validate(manifest)
	return manifest, diagnostics, nil
}

type argumentBuilder func(Options, string) []string

func (adapter *Adapter) runProbeCommand(ctx context.Context, input *materializedInput, arguments argumentBuilder) ([]byte, error) {
	return adapter.runProbeCommandChecked(ctx, input, arguments, false)
}

// A complete semantic scan cannot publish evidence after a decoder reports an
// error, even when FFprobe exits zero. Legacy Probe retains its existing policy.
func (adapter *Adapter) runProbeCommandChecked(ctx context.Context, input *materializedInput, arguments argumentBuilder, rejectDiagnostics bool) ([]byte, error) {
	inputArgument, extraFiles, err := inheritedInputBinding(input)
	if err != nil {
		return nil, err
	}
	stdout := newBoundedBuffer(adapter.options.MaxOutputBytes)
	stderr := newBoundedBuffer(adapter.options.MaxStderrBytes)
	command := exec.CommandContext(ctx, adapter.executable, arguments(adapter.options, inputArgument)...)
	command.Env = controlledCommandEnvironment()
	command.ExtraFiles = extraFiles
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("%w: stdout=%t stderr=%t", ErrOutputLimit, stdout.exceeded, stderr.exceeded)
	}
	if runErr != nil || (rejectDiagnostics && stderr.buffer.Len() != 0) {
		return nil, fmt.Errorf("%w: %v: %s", ErrCommand, runErr, strings.TrimSpace(stderr.String()))
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func probeArguments(options Options, path string) []string {
	return []string{
		"-v", "error",
		"-protocol_whitelist", "fd",
		"-fd", "3",
		"-probesize", strconv.FormatInt(options.ProbeSizeBytes, 10),
		"-analyzeduration", strconv.FormatInt(options.AnalyzeDurationMicros, 10),
		"-show_format",
		"-show_streams",
		"-print_format", "json",
		path,
	}
}

func frameScanArguments(options Options, path string) []string {
	return []string{
		"-v", "error",
		"-protocol_whitelist", "fd",
		"-fd", "3",
		"-probesize", strconv.FormatInt(options.ProbeSizeBytes, 10),
		"-analyzeduration", strconv.FormatInt(options.AnalyzeDurationMicros, 10),
		"-select_streams", "v",
		"-show_frames",
		"-show_entries", "frame=stream_index,best_effort_timestamp,pts,pkt_duration,key_frame",
		"-print_format", "json",
		path,
	}
}

func controlledCommandEnvironment() []string {
	return []string{
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
}

func normalizedOptions(options Options) (Options, error) {
	if options.ProbeSizeBytes == 0 {
		options.ProbeSizeBytes = defaultProbeSize
	}
	if options.AnalyzeDurationMicros == 0 {
		options.AnalyzeDurationMicros = defaultAnalyzeMicros
	}
	if options.MaxOutputBytes == 0 {
		options.MaxOutputBytes = defaultMaxOutputBytes
	}
	if options.MaxStderrBytes == 0 {
		options.MaxStderrBytes = defaultMaxStderrBytes
	}
	if options.ProbeSizeBytes < 32 || options.AnalyzeDurationMicros < 0 || options.MaxOutputBytes < 1024 || options.MaxStderrBytes < 256 {
		return Options{}, fmt.Errorf("%w: invalid resource limits %+v", ErrExecutable, options)
	}
	return options, nil
}

func validateBuildIdentity(identity BuildIdentity) error {
	for _, value := range []struct {
		name  string
		value string
	}{
		{name: "adapter semantic profile", value: identity.SemanticProfile.Adapter},
		{name: "parser semantic profile", value: identity.SemanticProfile.Parser},
	} {
		if !semanticProfilePattern.MatchString(value.value) {
			return fmt.Errorf("%w: %s must be a canonical lowercase profile identifier", ErrBuildIdentity, value.name)
		}
	}
	if !fingerprintPattern.MatchString(identity.RuntimeDependencyFingerprint) {
		return fmt.Errorf(
			"%w: runtime dependency fingerprint must be an algorithm-prefixed lowercase hex digest",
			ErrBuildIdentity,
		)
	}
	return nil
}

func adapterFingerprints(
	binaryFingerprint string,
	versionReport string,
	identity BuildIdentity,
	options Options,
) (string, string, error) {
	if err := validateBuildIdentity(identity); err != nil {
		return "", "", err
	}
	buildFingerprint, err := fingerprintJSON(struct {
		Implementation     string        `json:"implementation"`
		Binary             string        `json:"binary"`
		Version            string        `json:"version"`
		Identity           BuildIdentity `json:"identity"`
		EnvironmentProfile string        `json:"environment_profile"`
		Environment        []string      `json:"environment"`
	}{
		Implementation:     Implementation,
		Binary:             binaryFingerprint,
		Version:            versionReport,
		Identity:           identity,
		EnvironmentProfile: CommandEnvironmentProfileV1,
		Environment:        controlledCommandEnvironment(),
	})
	if err != nil {
		return "", "", err
	}
	optionsFingerprint, err := fingerprintJSON(struct {
		Implementation     string          `json:"implementation"`
		SemanticProfile    SemanticProfile `json:"semantic_profile"`
		ProbeArguments     []string        `json:"probe_arguments"`
		FrameScanArguments []string        `json:"frame_scan_arguments"`
		Options            Options         `json:"options"`
		EnvironmentProfile string          `json:"environment_profile"`
		Environment        []string        `json:"environment"`
	}{
		Implementation:     Implementation,
		SemanticProfile:    identity.SemanticProfile,
		ProbeArguments:     probeArguments(options, "<private-content-addressed-input>"),
		FrameScanArguments: frameScanArguments(options, "<private-content-addressed-input>"),
		Options:            options,
		EnvironmentProfile: CommandEnvironmentProfileV1,
		Environment:        controlledCommandEnvironment(),
	})
	if err != nil {
		return "", "", err
	}
	return buildFingerprint, optionsFingerprint, nil
}

func resolveExecutable(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "ffprobe"
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExecutable, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: absolute path: %v", ErrExecutable, err)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		path = evaluated
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%w: %q must be an executable regular file", ErrExecutable, path)
	}
	return path, nil
}

func secureLocalPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: path is required", ErrInput)
	}
	if strings.HasPrefix(filepath.VolumeName(value), `\\`) {
		return "", fmt.Errorf("%w: UNC and device-network paths are forbidden", ErrInput)
	}
	if parsed, err := url.Parse(value); err != nil || (parsed.Scheme != "" && filepath.VolumeName(value) == "") {
		return "", fmt.Errorf("%w: URL/protocol inputs are forbidden", ErrInput)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w: absolute path: %v", ErrInput, err)
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInput, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: stat %q: %v", ErrInput, path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q must be a non-symlink regular file", ErrInput, path)
	}
	return path, nil
}

func rejectSymlinkComponents(path string) error {
	path = filepath.Clean(path)
	var components []string
	for current := path; ; current = filepath.Dir(current) {
		components = append(components, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for left, right := 0, len(components)-1; left < right; left, right = left+1, right-1 {
		components[left], components[right] = components[right], components[left]
	}
	for index, component := range components {
		info, err := os.Lstat(component)
		if err != nil {
			return fmt.Errorf("stat path component %q: %v", component, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component %q is forbidden", component)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("parent path component %q is not a directory", component)
		}
	}
	return nil
}

func openLocalRegularFile(value string) (*os.File, os.FileInfo, string, error) {
	path, err := secureLocalPath(value)
	if err != nil {
		return nil, nil, "", err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: stat %q: %v", ErrInput, path, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: open %q: %v", ErrInput, path, err)
	}
	fail := func(cause error) (*os.File, os.FileInfo, string, error) {
		_ = file.Close()
		return nil, nil, "", cause
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("%w: inspect opened input: %v", ErrInput, err))
	}
	if !openedInfo.Mode().IsRegular() {
		return fail(fmt.Errorf("%w: opened input is not a regular file", ErrInput))
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		return fail(fmt.Errorf("%w: input path changed while opening: %v", ErrContentChanged, err))
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return fail(fmt.Errorf("%w: input path changed while opening: %v", ErrContentChanged, err))
	}
	if !currentInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) || !os.SameFile(currentInfo, openedInfo) {
		return fail(fmt.Errorf("%w: input path resolved to a different file while opening", ErrContentChanged))
	}
	return file, openedInfo, path, nil
}

type materializedInput struct {
	file                 *os.File
	contentAddressedName string
	workspaceDirectory   string
	contentHash          string
	sizeBytes            int64
	closed               bool
}

type materializationWorkspace struct {
	directory     string
	directoryInfo os.FileInfo
	root          *os.Root
	tracked       map[string]bool
	rootClosed    bool
	closed        bool
}

func materializeLocalInput(ctx context.Context, value, expectedContentHash string) (
	result *materializedInput,
	returnErr error,
) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, sourceInfo, _, err := openLocalRegularFile(value)
	if err != nil {
		return nil, err
	}
	defer source.Close()

	workspace, err := makePrivateMaterializationWorkspace()
	if err != nil {
		return nil, fmt.Errorf("%w: create private materialization directory: %v", ErrInput, err)
	}
	var writableSnapshot *os.File
	var retainedSnapshot *os.File
	completed := false
	defer func() {
		if completed {
			return
		}
		if retainedSnapshot != nil {
			_ = retainedSnapshot.Close()
		}
		if writableSnapshot != nil {
			_ = writableSnapshot.Close()
		}
		// Close is deliberately retried here when an explicit cleanup attempt below failed.
		if cleanupErr := workspace.Close(); cleanupErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: %v", ErrCleanup, cleanupErr))
		}
	}()

	const stagingName = ".materializing"
	writableSnapshot, err = workspace.root.OpenFile(stagingName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: create private snapshot: %v", ErrInput, err)
	}
	workspace.track(stagingName)
	contentHash, sizeBytes, err := copyAndHash(ctx, writableSnapshot, source)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: materialize input: %v", ErrInput, err)
	}
	afterSourceInfo, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: inspect source after materialization: %v", ErrInput, err)
	}
	if sizeBytes != sourceInfo.Size() || sizeBytes != afterSourceInfo.Size() ||
		!sourceInfo.ModTime().Equal(afterSourceInfo.ModTime()) {
		return nil, fmt.Errorf("%w: source metadata changed while materializing", ErrContentChanged)
	}
	if sizeBytes <= 0 {
		return nil, fmt.Errorf("%w: input must not be empty", ErrInput)
	}
	if expectedContentHash != "" && expectedContentHash != contentHash {
		return nil, fmt.Errorf("%w: got %s, want %s", ErrContentHash, contentHash, expectedContentHash)
	}
	if err := writableSnapshot.Sync(); err != nil {
		return nil, fmt.Errorf("%w: sync private snapshot: %v", ErrInput, err)
	}
	snapshotInfo, err := writableSnapshot.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: inspect private snapshot: %v", ErrInput, err)
	}

	digest := strings.TrimPrefix(contentHash, "sha256:")
	finalName := "sha256-" + digest
	if _, err := workspace.root.Lstat(finalName); !os.IsNotExist(err) {
		if err == nil {
			return nil, fmt.Errorf("%w: private content-addressed output already exists", ErrInput)
		}
		return nil, fmt.Errorf("%w: inspect private output: %v", ErrInput, err)
	}
	// Link is deliberately no-replace. The directory is new and private, but this also refuses a
	// raced symlink or file at the content-addressed output name.
	if err := workspace.root.Link(stagingName, finalName); err != nil {
		return nil, fmt.Errorf("%w: publish private snapshot: %v", ErrInput, err)
	}
	workspace.track(finalName)
	if err := workspace.remove(stagingName); err != nil {
		return nil, fmt.Errorf("%w: remove private staging name: %v", ErrInput, err)
	}
	if err := writableSnapshot.Chmod(0o400); err != nil {
		return nil, fmt.Errorf("%w: make private snapshot read-only: %v", ErrInput, err)
	}
	retainedSnapshot, err = workspace.root.Open(finalName)
	if err != nil {
		return nil, fmt.Errorf("%w: retain read-only private snapshot: %v", ErrInput, err)
	}
	retainedInfo, err := retainedSnapshot.Stat()
	if err != nil || !retainedInfo.Mode().IsRegular() || !os.SameFile(snapshotInfo, retainedInfo) {
		return nil, fmt.Errorf("%w: private content-addressed output changed during publication", ErrContentChanged)
	}
	if err := writableSnapshot.Close(); err != nil {
		return nil, fmt.Errorf("%w: close writable snapshot handle: %v", ErrInput, err)
	}
	writableSnapshot = nil
	if err := workspace.remove(finalName); err != nil {
		return nil, fmt.Errorf("%w: unlink retained private snapshot: %v", ErrCleanup, err)
	}

	result = &materializedInput{
		file:                 retainedSnapshot,
		contentAddressedName: finalName,
		workspaceDirectory:   workspace.directory,
		contentHash:          contentHash,
		sizeBytes:            sizeBytes,
	}
	verifiedHash, verifiedSize, err := result.verify(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: verify private snapshot: %v", ErrInput, err)
	}
	if verifiedHash != contentHash || verifiedSize != sizeBytes {
		return nil, fmt.Errorf("%w: private snapshot did not match copied bytes", ErrContentChanged)
	}
	// Remove every pathname before running FFprobe. Only the retained read-only descriptor remains,
	// and workspace cleanup is descriptor-relative. A cleanup failure aborts probing and the defer
	// retries it; ENOENT is treated as evidence of replacement, never as success.
	if err := workspace.Close(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCleanup, err)
	}
	completed = true
	return result, nil
}

func makePrivateMaterializationWorkspace() (*materializationWorkspace, error) {
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return nil, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlinkComponents(base); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(base, ".cutly-ffprobe-input-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.Remove(directory)
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(directory)
		return nil, errors.New("private materialization path is not a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		_ = os.Remove(directory)
		return nil, err
	}
	return &materializationWorkspace{
		directory:     directory,
		directoryInfo: info,
		root:          root,
		tracked:       make(map[string]bool),
	}, nil
}

func (workspace *materializationWorkspace) track(name string) {
	workspace.tracked[name] = false
}

func (workspace *materializationWorkspace) remove(name string) error {
	removed, tracked := workspace.tracked[name]
	if !tracked {
		return fmt.Errorf("untracked private materialization entry %q", name)
	}
	if removed {
		return nil
	}
	if workspace.rootClosed || workspace.root == nil {
		return errors.New("private materialization root is closed")
	}
	if err := workspace.root.Remove(name); err != nil {
		return err
	}
	workspace.tracked[name] = true
	return nil
}

func (workspace *materializationWorkspace) Close() error {
	if workspace == nil || workspace.closed {
		return nil
	}
	var removalErrors []error
	for name := range workspace.tracked {
		if err := workspace.remove(name); err != nil {
			// Missing tracked entries are not accepted: a pathname may have been moved elsewhere.
			removalErrors = append(removalErrors, fmt.Errorf("remove %q: %w", name, err))
		}
	}
	if len(removalErrors) > 0 {
		return errors.Join(removalErrors...)
	}
	if !workspace.rootClosed {
		if err := workspace.root.Close(); err != nil {
			return err
		}
		workspace.rootClosed = true
	}
	current, err := os.Lstat(workspace.directory)
	if err != nil {
		return fmt.Errorf("private materialization directory disappeared before cleanup: %w", err)
	}
	if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || workspace.directoryInfo == nil ||
		!os.SameFile(current, workspace.directoryInfo) {
		return errors.New("refusing to remove a replaced private materialization directory")
	}
	// os.Remove is intentionally non-recursive: even a raced replacement cannot cause a tree to be
	// deleted. All media entries were already unlinked relative to the held os.Root descriptor.
	if err := os.Remove(workspace.directory); err != nil {
		return err
	}
	workspace.closed = true
	return nil
}

func (input *materializedInput) verify(ctx context.Context) (string, int64, error) {
	if input == nil || input.file == nil || input.closed {
		return "", 0, errors.New("private snapshot is closed")
	}
	return hashOpenRegularFile(ctx, input.file)
}

func (input *materializedInput) Close() error {
	if input == nil || input.closed {
		return nil
	}
	if input.file == nil {
		input.closed = true
		return nil
	}
	if err := input.file.Close(); err != nil {
		return err
	}
	input.closed = true
	return nil
}

func copyAndHash(ctx context.Context, destination, source *os.File) (string, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written := 0
			for written < count {
				writeCount, writeErr := destination.Write(buffer[written:count])
				written += writeCount
				if writeErr != nil {
					return "", 0, writeErr
				}
				if writeCount == 0 {
					return "", 0, io.ErrShortWrite
				}
			}
			_, _ = hash.Write(buffer[:count])
			size += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func hashRegularFile(ctx context.Context, path string) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	return hashOpenRegularFile(ctx, file)
}

func hashOpenRegularFile(ctx context.Context, file *os.File) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	before, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
			size += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || size != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", 0, fmt.Errorf("%w: regular file metadata changed while hashing", ErrContentChanged)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func fingerprintJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func newBoundedBuffer(limit int64) *boundedBuffer { return &boundedBuffer{limit: limit} }

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining <= 0 {
		buffer.exceeded = true
		return original, nil
	}
	if int64(len(value)) > remaining {
		value = value[:int(remaining)]
		buffer.exceeded = true
	}
	_, _ = buffer.buffer.Write(value)
	return original, nil
}

func (buffer *boundedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }
