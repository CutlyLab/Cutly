package ffmpegbackend

import (
	"context"
	"errors"
	"fmt"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	decode "github.com/CutlyLab/Cutly/media/decode"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

const (
	ChunkAssemblerIDV2             = "cutly.ffmpeg-chunk-assembler.document-v2.v2"
	ChunkAssemblerImplementationV2 = "cutly.ffmpeg-chunk-assembler.impl.document-v2.v2"
	ChunkAssemblerEncoderProfileV2 = "cutly.ffmpeg.rgba8-pcm16.h264-aac-mp4.document-v2.v2"
)

// ChunkAssemblerOptionsV2 is host authority, not tenant request data. The host
// owns this per-plan manifest, executable/dependency closure, output directory
// and admission budget. Serialize publication/recovery for a job. In particular
// ProviderBuild is the parent-measured provider worker executable digest, not
// the assembler implementation/SDK or FFmpeg decoder runtime identity.
// As with V1, immutable executable paths require the host worker sandbox.
type ChunkAssemblerOptionsV2 struct {
	Executable, OutputRoot, SDKBuildDigest, WorkerExecutableDigest, RuntimeDependencyFingerprint string
	MaxStderrBytes, MaxStagingBytes                                                              int64
	SupervisorExecutable, SupervisorExecutableDigest, SupervisorDependencyFingerprint            string
	Plan                                                                                         renderplanv2.Plan
	Manifest                                                                                     api.ExecutionManifestV2
}

type ChunkAssemblerV2 struct {
	io                           *Backend // only shared descriptor/runtime/filesystem helpers; never V1 requests
	plan                         renderplanv2.Plan
	manifest                     api.ExecutionManifestV2
	workerDigest                 string
	maxStagingBytes              int64
	supervisor, supervisorDigest string
	journalHook                  func(string) // test-only crash boundaries; nil in production
}

// ChunkAssemblerCapabilitiesV2 describes encoding of already-composited chunks,
// not an implementation of source evaluation. The host separately verifies the
// provider's ability to execute the selected semantic plan.
func ChunkAssemblerCapabilitiesV2(sdkDigest string) (api.BackendCapabilitiesV2, error) {
	build, err := backendImplementationFingerprint(ChunkAssemblerImplementationV2, sdkDigest)
	if err != nil {
		return api.BackendCapabilitiesV2{}, err
	}
	features := []videoedit.CapabilityID{documentv2.CapabilityTransformV1, documentv2.CapabilityOpacityV1, documentv2.CapabilityCropV1, documentv2.CapabilityTextV1, documentv2.CapabilityTextPinnedFontViewportPXV2, documentv2.CapabilityAudioGainV1, documentv2.CapabilityAudioPanV1, documentv2.CapabilityAudioRouteV1, documentv2.CapabilityAudioChannelMatrixV1, documentv2.CapabilityAudioSamplePeakLimiterV1, documentv2.CapabilityKeyframesV1, documentv2.CapabilityInterpolationHoldV1, documentv2.CapabilityInterpolationLinearV1, documentv2.CapabilityInterpolationCubicV1, documentv2.CapabilityVectorPathV1, documentv2.CapabilityVectorPathViewportPXV2, documentv2.CapabilityVectorTransitionScopeV1, documentv2.CapabilityTextTransitionScopeV1, videoedit.CapabilityVideoLinearRetime, videoedit.CapabilityAudioVarispeed, videoedit.CapabilityAudioTimeStretch}
	sort.Slice(features, func(i, j int) bool { return features[i] < features[j] })
	return api.BackendCapabilitiesV2{BackendID: ChunkAssemblerIDV2, BuildFingerprint: build,
		TrackKinds: []documentv2.TrackKind{documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector, documentv2.TrackKindVideo},
		AssetKinds: []documentv2.AssetKind{documentv2.AssetKindAudio, documentv2.AssetKindFont, documentv2.AssetKindImage, documentv2.AssetKindText, documentv2.AssetKindVector, documentv2.AssetKindVideo},
		Features:   features, MaxWidth: maxDimension, MaxHeight: maxDimension}, nil
}

// ChunkAssemblerRuntimeFingerprintV2 lets the host freeze the runtime identity
// before freezing its manifest. No untrusted manifest selects the executable.
func ChunkAssemblerRuntimeFingerprintV2(ctx context.Context, executable, dependencies string) (string, error) {
	if ctx == nil || !canonicalSHA256(dependencies) {
		return "", ErrConfiguration
	}
	path, err := resolveExecutable(executable)
	if err != nil {
		return "", err
	}
	return executableFingerprint(ctx, path, defaultMaxStderrBytes, dependencies, controlledRuntimeEnvironment())
}

func NewChunkAssemblerV2(ctx context.Context, o ChunkAssemblerOptionsV2) (_ *ChunkAssemblerV2, err error) {
	if ctx == nil || !platformSupportsSecureDescriptors() || !canonicalSHA256(o.WorkerExecutableDigest) || !canonicalSHA256(o.RuntimeDependencyFingerprint) || o.MaxStagingBytes <= 0 {
		return nil, ErrConfiguration
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Strict verification precedes cloning any host graph.
	verified, err := api.VerifyExecutionManifestV2AgainstPlan(ctx, o.Manifest, o.Plan)
	if err != nil {
		return nil, err
	}
	m, err := verified.Manifest()
	if err != nil {
		return nil, err
	}
	if o.WorkerExecutableDigest != m.ProviderBuild {
		return nil, fmt.Errorf("%w: measured worker digest must equal manifest provider", ErrConfiguration)
	}
	// Plan is cloned through its strict canonical codec, not a mutable caller alias.
	raw, err := renderplanv2.Canonical(o.Plan)
	if err != nil {
		return nil, err
	}
	p, err := renderplanv2.Decode(raw)
	if err != nil {
		return nil, err
	}
	caps, err := ChunkAssemblerCapabilitiesV2(o.SDKBuildDigest)
	supervised := o.SupervisorExecutable != "" || o.SupervisorExecutableDigest != "" || o.SupervisorDependencyFingerprint != ""
	if supervised && !filepath.IsAbs(o.SupervisorExecutable) {
		return nil, fmt.Errorf("%w: supervisor requires explicit absolute host path", ErrConfiguration)
	}
	if supervised {
		caps, err = ChunkAssemblerSupervisedCapabilitiesV2(o.SDKBuildDigest, o.SupervisorExecutableDigest, o.SupervisorDependencyFingerprint)
	}
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(caps, m.Backend) {
		return nil, fmt.Errorf("%w: assembler capabilities do not match host manifest", ErrConfiguration)
	}
	encoderProfile := ChunkAssemblerEncoderProfileV2
	if supervised {
		encoderProfile = ChunkAssemblerSupervisedEncoderProfileV2
	}
	if m.MediaRuntime.DecoderProfile != decode.AdapterSemanticProfileV1 || m.MediaRuntime.EncoderProfile != encoderProfile || m.MediaRuntime.ColorProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 {
		return nil, fmt.Errorf("%w: frozen V2 runtime profiles", ErrConfiguration)
	}
	out := m.Output
	if out.Format != "mp4" || out.PixelFormat != "rgba16" || out.ColorSpace != "linear-srgb" || (out.VideoCodec != "h264" && out.VideoCodec != "libx264") || out.AudioCodec != "aac" || out.Channels != 2 || out.Width > maxDimension || out.Height > maxDimension || out.Width%2 != 0 || out.Height%2 != 0 || out.SampleRate < 8000 || out.SampleRate > 96000 {
		return nil, fmt.Errorf("%w: unsupported V2 output", ErrConfiguration)
	}
	path, err := resolveExecutable(o.Executable)
	if err != nil {
		return nil, err
	}
	root, err := resolveOutputRoot(o.OutputRoot)
	if err != nil {
		return nil, err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = handle.Close()
		}
	}()
	limit := o.MaxStderrBytes
	if limit == 0 {
		limit = defaultMaxStderrBytes
	}
	if limit < 256 {
		return nil, ErrConfiguration
	}
	b := &Backend{executable: path, outputRoot: root, outputHandle: handle, maxStderrBytes: limit, runtimeEnvironment: controlledRuntimeEnvironment(), runtimeDependencyFingerprint: o.RuntimeDependencyFingerprint, runtimeFingerprint: m.MediaRuntime.FFmpegBuild, buildFingerprint: caps.BuildFingerprint}
	b.syncDirectory = func() error { return syncRootDirectory(handle) }
	if err = b.verifyRuntime(ctx); err != nil {
		return nil, err
	}
	assembler := &ChunkAssemblerV2{io: b, plan: p, manifest: m, workerDigest: o.WorkerExecutableDigest, maxStagingBytes: o.MaxStagingBytes}
	if supervised {
		assembler.supervisor, err = resolveExecutable(o.SupervisorExecutable)
		if err != nil {
			return nil, err
		}
		assembler.supervisorDigest = o.SupervisorExecutableDigest
		if err = assembler.verifySupervisorV2(ctx); err != nil {
			return nil, err
		}
	}
	success = true
	return assembler, nil
}
func (b *ChunkAssemblerV2) ID() string { return ChunkAssemblerIDV2 }
func (b *ChunkAssemblerV2) Close() error {
	if b == nil {
		return nil
	}
	return b.io.Close()
}
func (b *ChunkAssemblerV2) Capabilities(ctx context.Context) (api.BackendCapabilitiesV2, error) {
	if b == nil || ctx == nil {
		return api.BackendCapabilitiesV2{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return api.BackendCapabilitiesV2{}, err
	}
	m, err := api.VerifyExecutionManifestV2(b.manifest)
	if err != nil {
		return api.BackendCapabilitiesV2{}, err
	}
	v, err := m.Manifest()
	return v.Backend, err
}

// ChunkSourceV2 opens only exact admitted CAS descriptors. The assembler itself
// independently verifies payload length and SHA-256, including Close failures.
type ChunkSourceV2 interface {
	Open(context.Context, evidence.ChunkDescriptorV2) (io.ReadCloser, error)
}

func (b *ChunkAssemblerV2) admit(ctx context.Context, v evidence.VerifiedBindingV2) (evidence.BindingV2, error) {
	if b == nil || ctx == nil {
		return evidence.BindingV2{}, ErrConfiguration
	}
	r, err := v.Binding()
	if err != nil {
		return r, err
	}
	m, p := b.manifest, b.plan
	if r.DocumentHash != p.DocumentHash || r.DocumentHash != m.DocumentHash || r.PlanFingerprint != p.Fingerprint || r.ExecutionManifestFingerprint != m.Fingerprint || r.ProviderExecutableDigest != m.ProviderBuild || r.Target != p.Target || r.Output != m.Output || r.MediaRuntime != m.MediaRuntime || r.DeterministicSeed != m.DeterministicSeed {
		return r, fmt.Errorf("%w: host manifest binding", ErrPreflight)
	}
	start, se := r.TimelineRange.Start.Compare(p.RequestedRange.Start)
	end, ee := r.TimelineRange.End()
	limit, le := p.RequestedRange.End()
	stop, ce := end.Compare(limit)
	if se != nil || ee != nil || le != nil || ce != nil || start < 0 || stop > 0 {
		return r, ErrPreflight
	}
	var total int64
	for _, ds := range [][]evidence.ChunkDescriptorV2{r.Audio, r.Video, r.Video} {
		for _, d := range ds {
			if d.SizeBytes > b.maxStagingBytes-total {
				return r, fmt.Errorf("%w: staging byte budget", ErrPreflight)
			}
			total += d.SizeBytes
		}
	}
	if _, err = b.io.resolveArtifactPath(r.ArtifactURI); err != nil {
		return r, err
	}
	return r, errors.Join(b.io.verifyRuntime(ctx), b.verifySupervisorV2(ctx))
}

// removeOwnedV2 never unlinks a collision or a replacement belonging to another
// operation, including when a sink/fault callback panics during rollback.
func (b *ChunkAssemblerV2) removeOwnedV2(name string, file *os.File) error {
	named, err := b.io.outputHandle.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	own, err := file.Stat()
	if err != nil {
		return err
	}
	if named.Mode()&os.ModeSymlink != 0 || !os.SameFile(named, own) {
		return fmt.Errorf("%w: refusing to remove unowned %s", ErrPublication, filepath.Base(name))
	}
	return b.io.outputHandle.Remove(name)
}
