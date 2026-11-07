// Package ffmpegbackend implements Cutly's first no-Chrome hard-cut renderer.
//
// The v1 backend is deliberately narrow. It accepts one continuous video track and one
// continuous audio track, with cuts only, no clip properties and no retiming. Unsupported plans
// fail during preflight instead of being approximated by FFmpeg defaults.
//
// OutputRoot is a host-scheduler trust boundary, not a multi-tenant authorization
// mechanism. Give each mutually untrusted project its own host-owned root and serialize
// publication/recovery for a job. Evidence is an integrity record bound to a frozen request;
// it is not a signature or third-party attestation.
package ffmpegbackend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	BackendID                    = "cutly.ffmpeg-hardcut.v1"
	BackendImplementationProfile = "cutly.ffmpeg-hardcut.impl.v1"
	RuntimeEnvironmentProfile    = "cutly.ffmpeg.env.v1"

	CodeContextInvalid      = "BACKEND.FFMPEG.CONTEXT_INVALID"
	CodeIdentityMismatch    = "BACKEND.FFMPEG.IDENTITY_MISMATCH"
	CodeManifestUnsupported = "BACKEND.FFMPEG.MANIFEST_UNSUPPORTED"
	CodeTrackUnsupported    = "BACKEND.FFMPEG.TRACK_UNSUPPORTED"
	CodeTimelineUnsupported = "BACKEND.FFMPEG.TIMELINE_UNSUPPORTED"
	CodePropertyUnsupported = "BACKEND.FFMPEG.PROPERTY_UNSUPPORTED"
	CodeRetimeUnsupported   = "BACKEND.FFMPEG.RETIME_UNSUPPORTED"
	CodeResourceUnsupported = "BACKEND.FFMPEG.RESOURCE_UNSUPPORTED"
	CodeProbeUnsupported    = "BACKEND.FFMPEG.PROBE_UNSUPPORTED"
	CodeOutputUnsupported   = "BACKEND.FFMPEG.OUTPUT_UNSUPPORTED"
	CodeRenderFailed        = "BACKEND.FFMPEG.RENDER_FAILED"
	CodeContentMismatch     = "BACKEND.FFMPEG.CONTENT_MISMATCH"
	CodePublicationRejected = "BACKEND.FFMPEG.PUBLICATION_REJECTED"

	defaultMaxStderrBytes = int64(1024 * 1024)
	maxDimension          = 8192
)

var (
	ErrConfiguration = errors.New("ffmpeg backend: configuration is invalid")
	ErrPreflight     = errors.New("ffmpeg backend: preflight failed")
	ErrContent       = errors.New("ffmpeg backend: resource content mismatch")
	ErrCommand       = errors.New("ffmpeg backend: command failed")
	ErrPublication   = errors.New("ffmpeg backend: publication rejected")
)

type commandFileEndpoint struct {
	URL        string
	Descriptor string
}

// Options fixes the executable identity, publication root and memory bound used by a Backend.
// OutputRoot must already exist and may not be a symlink. V1 publishes direct children only.
// SDKBuildDigest identifies the Cutly compiler/evidence implementation. The caller's controlled
// runtime resolver must derive RuntimeDependencyFingerprint from an immutable or signed FFmpeg
// dependency closure (including dynamically loaded codec libraries); neither identity may be an
// unversioned label. This backend verifies the declared identity before every render, but portable
// Go does not provide fexecve, so preventing same-user executable-path replacement remains an
// explicit resolver/worker-sandbox responsibility.
type Options struct {
	Executable                   string
	OutputRoot                   string
	InputRoots                   []string
	MaxStderrBytes               int64
	ProbeResolver                ProbeResolver
	SDKBuildDigest               string
	RuntimeDependencyFingerprint string
}

// Backend is immutable after New and safe for concurrent Render calls.
type Backend struct {
	executable                   string
	buildFingerprint             string
	runtimeFingerprint           string
	outputRoot                   string
	outputHandle                 *os.Root
	inputRoots                   []authorizedRoot
	maxStderrBytes               int64
	probeResolver                ProbeResolver
	runtimeEnvironment           []string
	runtimeDependencyFingerprint string
	// syncDirectory is fixed by New. Keeping the durability boundary explicit lets fault tests
	// inject the exact errno returned by fsync without replacing filesystem or publication logic.
	syncDirectory func() error
	// recoveryHook is nil in production. Fault tests use it to replace a
	// published leaf at exact inspection boundaries without weakening the
	// public API or introducing package-global mutable state.
	recoveryHook func(string)
}

var _ renderapi.RenderBackend = (*Backend)(nil)

// New resolves and fingerprints one concrete FFmpeg binary and a non-symlink output directory.
func New(ctx context.Context, options Options) (*Backend, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !platformSupportsSecureDescriptors() {
		return nil, fmt.Errorf("%w: platform lacks the inherited descriptor contract required by hard-cut v1", ErrConfiguration)
	}
	executable, err := resolveExecutable(options.Executable)
	if err != nil {
		return nil, err
	}
	outputRoot, err := resolveOutputRoot(options.OutputRoot)
	if err != nil {
		return nil, err
	}
	outputHandle, err := os.OpenRoot(outputRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: open output root: %v", ErrConfiguration, err)
	}
	var inputRoots []authorizedRoot
	success := false
	defer func() {
		if success {
			return
		}
		_ = outputHandle.Close()
		for _, root := range inputRoots {
			_ = root.handle.Close()
		}
	}()
	limit := options.MaxStderrBytes
	if limit == 0 {
		limit = defaultMaxStderrBytes
	}
	if limit < 256 {
		return nil, fmt.Errorf("%w: max stderr bytes must be at least 256", ErrConfiguration)
	}
	if options.ProbeResolver == nil {
		return nil, fmt.Errorf("%w: probe resolver is required for fail-closed media semantics", ErrConfiguration)
	}
	if !canonicalSHA256(options.SDKBuildDigest) {
		return nil, fmt.Errorf("%w: SDK build digest must be canonical sha256", ErrConfiguration)
	}
	if !canonicalSHA256(options.RuntimeDependencyFingerprint) {
		return nil, fmt.Errorf("%w: runtime dependency fingerprint must be canonical sha256", ErrConfiguration)
	}
	inputRoots, err = resolveInputRoots(options.InputRoots)
	if err != nil {
		return nil, err
	}
	runtimeEnvironment := controlledRuntimeEnvironment()
	runtimeFingerprint, err := executableFingerprint(ctx, executable, limit, options.RuntimeDependencyFingerprint, runtimeEnvironment)
	if err != nil {
		return nil, err
	}
	buildFingerprint, err := backendImplementationFingerprint(BackendImplementationProfile, options.SDKBuildDigest)
	if err != nil {
		return nil, err
	}
	backend := &Backend{
		executable:                   executable,
		buildFingerprint:             buildFingerprint,
		runtimeFingerprint:           runtimeFingerprint,
		outputRoot:                   outputRoot,
		outputHandle:                 outputHandle,
		inputRoots:                   inputRoots,
		maxStderrBytes:               limit,
		probeResolver:                options.ProbeResolver,
		runtimeEnvironment:           runtimeEnvironment,
		runtimeDependencyFingerprint: options.RuntimeDependencyFingerprint,
		syncDirectory:                func() error { return syncRootDirectory(outputHandle) },
	}
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return nil, err
	}
	success = true
	return backend, nil
}

func (backend *Backend) ID() string { return BackendID }

// Close releases the directory handles used for input and publication confinement. Callers must
// not race Close with Capabilities, Preflight, Render, InspectPublished, or OpenPublished.
func (backend *Backend) Close() error {
	if backend == nil {
		return nil
	}
	var closeErrors []error
	if backend.outputHandle != nil {
		closeErrors = append(closeErrors, backend.outputHandle.Close())
	}
	for _, root := range backend.inputRoots {
		if root.handle != nil {
			closeErrors = append(closeErrors, root.handle.Close())
		}
	}
	return errors.Join(closeErrors...)
}

// FFmpegRuntimeFingerprint returns the runtime identity declared by the controlled resolver and
// checked against the current executable bytes/version. It is intentionally distinct from
// Capabilities().BuildFingerprint, which identifies Cutly's backend/compiler implementation.
func (backend *Backend) FFmpegRuntimeFingerprint(ctx context.Context) (string, error) {
	if err := backend.verifyRuntime(ctx); err != nil {
		return "", err
	}
	return backend.runtimeFingerprint, nil
}

// Capabilities declares only the semantics v1 actually implements. In particular, no retime
// capability is advertised.
func (backend *Backend) Capabilities(ctx context.Context) (videoedit.BackendCapabilities, error) {
	if backend == nil || backend.executable == "" || backend.buildFingerprint == "" {
		return videoedit.BackendCapabilities{}, fmt.Errorf("%w: uninitialized backend", ErrConfiguration)
	}
	if ctx == nil {
		return videoedit.BackendCapabilities{}, fmt.Errorf("%w: nil context", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return videoedit.BackendCapabilities{}, err
	}
	return videoedit.BackendCapabilities{
		BackendID:        BackendID,
		BuildFingerprint: backend.buildFingerprint,
		TrackKinds:       []videoedit.TrackKind{videoedit.TrackKindAudio, videoedit.TrackKindVideo},
		AssetKinds:       []videoedit.AssetKind{videoedit.AssetKindAudio, videoedit.AssetKindVideo},
		MaxWidth:         maxDimension,
		MaxHeight:        maxDimension,
	}, nil
}

// Preflight checks both generic capabilities and the intentionally narrow hard-cut profile.
func (backend *Backend) Preflight(ctx context.Context, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) []videoedit.Diagnostic {
	var diagnostics []videoedit.Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, videoedit.Diagnostic{Code: code, Severity: videoedit.SeverityError, Path: path, Message: message})
	}
	if ctx == nil {
		add(CodeContextInvalid, "", "context is required")
		return diagnostics
	}
	if err := ctx.Err(); err != nil {
		add(CodeContextInvalid, "", err.Error())
		return diagnostics
	}
	if raw, err := json.Marshal(plan); err != nil {
		add(CodeManifestUnsupported, "/plan", err.Error())
		return diagnostics
	} else if _, err := videoedit.DecodeRenderPlan(raw); err != nil {
		add(CodeManifestUnsupported, "/plan", err.Error())
		return diagnostics
	}
	if raw, err := json.Marshal(manifest); err != nil {
		add(CodeManifestUnsupported, "/execution_manifest", err.Error())
		return diagnostics
	} else if _, err := renderapi.DecodeExecutionManifest(raw); err != nil {
		add(CodeManifestUnsupported, "/execution_manifest", err.Error())
		return diagnostics
	}
	capabilities, err := backend.Capabilities(ctx)
	if err != nil {
		add(CodeIdentityMismatch, "/backend", err.Error())
		return diagnostics
	}
	capabilities = videoedit.CanonicalBackendCapabilities(capabilities)
	diagnostics = append(diagnostics, videoedit.CheckCapabilities(plan, capabilities)...)
	if !reflect.DeepEqual(videoedit.CanonicalBackendCapabilities(manifest.Backend), capabilities) {
		add(CodeIdentityMismatch, "/backend", "execution manifest does not select this exact backend build")
	}
	if manifest.MediaRuntime.FFmpegBuild != backend.runtimeFingerprint {
		add(CodeIdentityMismatch, "/media_runtime/ffmpeg_build", "execution manifest FFmpeg build does not match the running executable")
	}
	if manifest.PlanFingerprint != plan.Fingerprint || manifest.SemanticProfile != plan.SemanticProfile {
		add(CodeIdentityMismatch, "/plan_fingerprint", "execution manifest is not bound to this semantic plan")
	}
	if manifest.MediaRuntime.DecoderProfile != "ffmpeg-hardcut-v1" ||
		manifest.MediaRuntime.EncoderProfile != "libx264-aac-mp4.v1" ||
		manifest.MediaRuntime.ColorProfile != "cutly.bt709.v1" || manifest.MediaRuntime.TextShaperBuild != "" {
		add(CodeManifestUnsupported, "/media_runtime", "hard-cut v1 requires the pinned ffmpeg-hardcut-v1/bt709 runtime profile")
	}
	if len(manifest.Fonts) != 0 || len(manifest.Plugins) != 0 || manifest.BrowserFingerprint != "" {
		add(CodeManifestUnsupported, "", "hard-cut v1 does not load fonts, plugins, or a browser runtime")
	}
	validateOutput(plan, manifest.Output, add)
	validateClockAlignment(plan, add)
	validateResourceBinding(plan.Resources, manifest.Resources, add)
	resources := make(map[videoedit.ID]renderapi.MaterializedResource, len(manifest.Resources))
	for index, resource := range manifest.Resources {
		resources[resource.AssetID] = resource
		if resource.LocatorScheme != renderapi.LocatorFile {
			add(CodeResourceUnsupported, fmt.Sprintf("/resources/%d/locator", index), "hard-cut v1 requires an absolute local file locator")
		} else if _, err := backend.authorizeInput(resource.Locator); err != nil {
			add(CodeResourceUnsupported, fmt.Sprintf("/resources/%d/locator", index), err.Error())
		}
	}
	validateTrack("video", plan.Video, plan.RequestedRange, resources, add)
	validateTrack("audio", plan.Audio, plan.RequestedRange, resources, add)
	_, probeDiagnostics := backend.resolveSourceProfiles(ctx, plan, manifest)
	diagnostics = append(diagnostics, probeDiagnostics...)
	return diagnostics
}

// hard-cut v1 rebases the resolved media interval to PTS zero before FFmpeg's fps/aresample
// filters run. Therefore the first selected composition frame and sample must start exactly at
// RequestedRange.Start. A phase-shifted composition clock is valid semantic input, but accepting
// it here would render bytes for RequestedRange.Start while the evaluator names a later tick.
// A future compositor can preserve that leading fractional interval explicitly; v1 fails closed.
func validateClockAlignment(plan videoedit.RenderPlan, add func(string, string, string)) {
	frameStart, frameErr := videoedit.FrameTimeAt(plan.Target.FrameClock, plan.FrameRange.First)
	if comparison, compareErr := frameStart.Compare(plan.RequestedRange.Start); frameErr != nil || compareErr != nil || comparison != 0 {
		add(CodeTimelineUnsupported, "/frame_range/first", "hard-cut v1 requires the first composition frame tick to equal the requested range start")
	}
	sampleStart, sampleErr := videoedit.SampleTimeAt(plan.Target.SampleClock, plan.SampleRange.First)
	if comparison, compareErr := sampleStart.Compare(plan.RequestedRange.Start); sampleErr != nil || compareErr != nil || comparison != 0 {
		add(CodeTimelineUnsupported, "/sample_range/first", "hard-cut v1 requires the first composition sample tick to equal the requested range start")
	}
}

func validateOutput(plan videoedit.RenderPlan, output renderapi.OutputSpec, add func(string, string, string)) {
	if plan.Target.Width > maxDimension || plan.Target.Height > maxDimension {
		add(CodeOutputUnsupported, "/target", fmt.Sprintf("dimensions exceed %dx%d", maxDimension, maxDimension))
	}
	if output.ReferenceProfile != "cutly.hardcut.bt709.v1" || output.Format != "mp4" || output.PixelFormat != "yuv420p" || output.ColorSpace != "bt709" ||
		output.DecodedVideoProfile != renderapi.DecodedVideoProfileRGBA8V1 || output.DecodedAudioProfile != renderapi.DecodedAudioProfilePCM16LEV1 {
		add(CodeOutputUnsupported, "/output", "hard-cut v1 requires mp4/yuv420p/bt709 output and the pinned RGBA8/PCM16 evidence profiles")
	}
	if output.VideoCodec != "h264" && output.VideoCodec != "libx264" {
		add(CodeOutputUnsupported, "/output/video_codec", "hard-cut v1 requires h264 or libx264")
	}
	if output.AudioCodec != "aac" {
		add(CodeOutputUnsupported, "/output/audio_codec", "hard-cut v1 requires AAC audio")
	}
	if output.Channels != 1 && output.Channels != 2 {
		add(CodeOutputUnsupported, "/output/channels", "hard-cut v1 supports mono or stereo output")
	}
	if output.Width != plan.Target.Width || output.Height != plan.Target.Height ||
		output.PixelFormat != plan.Target.PixelFormat || output.ColorSpace != plan.Target.ColorSpace ||
		output.SampleRate != plan.Target.SampleClock.SamplesPerSecond || output.Channels != plan.Target.Channels {
		add(CodeIdentityMismatch, "/output", "execution output does not match the semantic plan target")
	}
}

func validateResourceBinding(planned []videoedit.ResourceRef, materialized []renderapi.MaterializedResource, add func(string, string, string)) {
	if len(planned) != len(materialized) {
		add(CodeIdentityMismatch, "/resources", "materialized resource count does not match the semantic plan")
	}
	plannedByID := make(map[videoedit.ID]videoedit.ResourceRef, len(planned))
	for _, resource := range planned {
		plannedByID[resource.AssetID] = resource
		if resource.ContentHash == "" {
			add(CodeResourceUnsupported, "/resources", fmt.Sprintf("planned resource %q has no immutable content hash", resource.AssetID))
		}
	}
	for index, resource := range materialized {
		expected, exists := plannedByID[resource.AssetID]
		if !exists || expected.Kind != resource.Kind || expected.ContentHash != resource.ContentHash {
			add(CodeIdentityMismatch, fmt.Sprintf("/resources/%d", index), "materialized resource does not match the semantic plan")
		}
	}
}

func validateTrack(kind string, clips []videoedit.PlannedClip, requested videoedit.TimeRange, resources map[videoedit.ID]renderapi.MaterializedResource, add func(string, string, string)) {
	path := "/" + kind
	if len(clips) == 0 {
		add(CodeTrackUnsupported, path, "hard-cut v1 requires exactly one continuous "+kind+" track")
		return
	}
	trackID := clips[0].TrackID
	trackIndex := clips[0].TrackIndex
	cursor := requested.Start
	for index, clip := range clips {
		clipPath := fmt.Sprintf("%s/%d", path, index)
		if clip.TrackID != trackID || clip.TrackIndex != trackIndex {
			add(CodeTrackUnsupported, clipPath+"/track_id", "hard-cut v1 supports only one "+kind+" track")
		}
		if len(clip.Properties) != 0 {
			add(CodePropertyUnsupported, clipPath+"/properties", "clip properties are not implemented by hard-cut v1")
		}
		if comparison, err := clip.VisibleTimelineRange.Start.Compare(cursor); err != nil || comparison != 0 {
			add(CodeTimelineUnsupported, clipPath+"/visible_timeline_range", "clips must cover the requested range continuously without gaps or overlaps")
		}
		if clip.VisibleTimelineRange.Duration.Value <= 0 {
			add(CodeTimelineUnsupported, clipPath+"/visible_timeline_range/duration", "visible clip duration must be positive")
		}
		if comparison, err := clip.VisibleSourceRange.Duration.Compare(clip.VisibleTimelineRange.Duration); err != nil || comparison != 0 {
			add(CodeRetimeUnsupported, clipPath+"/visible_source_range", "hard-cut v1 does not implement retiming")
		}
		if comparison, err := clip.VisibleSourceRange.Start.Compare(videoedit.RationalTime{Value: 0, Rate: 1}); err != nil || comparison < 0 {
			add(CodeTimelineUnsupported, clipPath+"/visible_source_range/start", "negative source time is not supported")
		}
		resource, exists := resources[clip.AssetID]
		if !exists {
			add(CodeResourceUnsupported, clipPath+"/asset_id", "clip resource is not materialized")
		} else if kind == "video" && resource.Kind != videoedit.AssetKindVideo {
			add(CodeResourceUnsupported, clipPath+"/asset_id", "video track requires a video resource")
		} else if kind == "audio" && resource.Kind != videoedit.AssetKindAudio && resource.Kind != videoedit.AssetKindVideo {
			add(CodeResourceUnsupported, clipPath+"/asset_id", "audio track requires an audio or video resource")
		}
		end, err := clip.VisibleTimelineRange.End()
		if err != nil {
			add(CodeTimelineUnsupported, clipPath+"/visible_timeline_range", err.Error())
		} else {
			cursor = end
		}
	}
	requestedEnd, err := requested.End()
	if err != nil {
		add(CodeTimelineUnsupported, "/requested_range", err.Error())
		return
	}
	if comparison, compareErr := cursor.Compare(requestedEnd); compareErr != nil || comparison != 0 {
		add(CodeTimelineUnsupported, path, "clips must cover the complete requested range")
	}
}

func resolveExecutable(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "ffmpeg"
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("%w: find FFmpeg: %v", ErrConfiguration, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: executable path: %v", ErrConfiguration, err)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		path = evaluated
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%w: %q is not an executable regular file", ErrConfiguration, path)
	}
	return path, nil
}

func resolveOutputRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: output root is required", ErrConfiguration)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w: output root: %v", ErrConfiguration, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: output root must be an existing non-symlink directory", ErrConfiguration)
	}
	evaluated, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve output root: %v", ErrConfiguration, err)
	}
	info, err = os.Lstat(evaluated)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: resolved output root must be a non-symlink directory", ErrConfiguration)
	}
	return filepath.Clean(evaluated), nil
}

func executableFingerprint(ctx context.Context, executable string, limit int64, dependencyFingerprint string, environment []string) (string, error) {
	binaryHash, _, err := hashRegularFile(ctx, executable)
	if err != nil {
		return "", fmt.Errorf("%w: hash executable: %w", ErrConfiguration, err)
	}
	stdout := newBoundedBuffer(64 * 1024)
	stderr := newBoundedBuffer(limit)
	command := newCommandContext(ctx, executable, "-version")
	command.Env = append([]string(nil), environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("%w: FFmpeg version: %v: %s", ErrConfiguration, err, stderr.String())
	}
	if stdout.exceeded || stderr.exceeded || !bytes.Contains(stdout.Bytes(), []byte("ffmpeg version")) {
		return "", fmt.Errorf("%w: executable did not return a bounded FFmpeg version", ErrConfiguration)
	}
	return runtimeIdentityFingerprint(
		binaryHash,
		strings.TrimSpace(stdout.String()),
		dependencyFingerprint,
		RuntimeEnvironmentProfile,
		environment,
	)
}

func runtimeIdentityFingerprint(binaryHash, version, dependencyFingerprint, environmentProfile string, environment []string) (string, error) {
	canonical, err := videoedit.CanonicalJSON(struct {
		Binary                string   `json:"binary"`
		Version               string   `json:"version"`
		DependencyFingerprint string   `json:"dependency_fingerprint"`
		EnvironmentProfile    string   `json:"environment_profile"`
		Environment           []string `json:"environment"`
	}{
		Binary: binaryHash, Version: version, DependencyFingerprint: dependencyFingerprint,
		EnvironmentProfile: environmentProfile, Environment: append([]string(nil), environment...),
	})
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint: %v", ErrConfiguration, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func controlledRuntimeEnvironment() []string {
	return []string{"LANG=C", "LC_ALL=C", "TZ=UTC", "PATH=/usr/bin:/bin"}
}

func backendImplementationFingerprint(profile, sdkBuildDigest string) (string, error) {
	if strings.TrimSpace(profile) == "" || !canonicalSHA256(sdkBuildDigest) {
		return "", fmt.Errorf("%w: backend implementation identity requires a profile and canonical SDK build digest", ErrConfiguration)
	}
	canonical, err := videoedit.CanonicalJSON(struct {
		Profile        string `json:"profile"`
		SDKBuildDigest string `json:"sdk_build_digest"`
	}{Profile: profile, SDKBuildDigest: sdkBuildDigest})
	if err != nil {
		return "", fmt.Errorf("%w: backend implementation fingerprint: %v", ErrConfiguration, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
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
		value = value[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.buffer.Write(value)
	return original, nil
}

func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }
func (buffer *boundedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }

func hashRegularFile(ctx context.Context, path string) (string, int64, error) {
	if ctx == nil {
		return "", 0, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	return hashOpenFile(ctx, file)
}

func hashOpenFile(ctx context.Context, file *os.File) (string, int64, error) {
	if ctx == nil {
		return "", 0, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if file == nil {
		return "", 0, errors.New("nil file")
	}
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
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
	if !after.Mode().IsRegular() || !os.SameFile(before, after) ||
		before.Size() != after.Size() || size != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("regular file metadata changed while hashing")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}
