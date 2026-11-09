package reconstructv2

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

const MixedConfigProfileV2 = "cutly.reconstruct.document-v2.native-mixed-config.v2"

// MixedVideoConfig is trusted host policy, not a runtime selected by a manifest.
// MediaRuntime explicitly maps an already measured decoder/probe pair into the
// host authority's application-specific runtime labels. It does not claim that
// those labels are measured version reports or signed runtimebundle closures.
// Construct adapters using a verified runtimebundle resolution in production;
// their constructors measure executable bytes, reports, profiles and options.
// Scratch must remain host-owned for the factory lifetime.
type MixedVideoConfig struct {
	Probe            *ffprobe.Adapter
	Decoder          *videodecode.Adapter
	MediaRuntime     renderapi.MediaRuntime
	Scan             ffprobe.VideoScanOptions
	Scratch          string
	MaxDiskBytes     uint64
	MaxScanBytes     uint64
	MaxIndexedFrames uint64
}

// MixedConfig is additive. Static is the unchanged S3 host configuration. Visual
// is separately versioned and its Static field must equal Static.Visual exactly.
// Nil Video explicitly disables temporal video while retaining transitions/buses.
type MixedConfig struct {
	Static Config
	Visual referencevisual.ConfigV3
	Video  *MixedVideoConfig
}

func DefaultMixedConfig(config Config) MixedConfig {
	visual := referencevisual.DefaultConfigV3(config.Visual.Width, config.Visual.Height)
	visual.Static = ownFactoryConfig(config).Visual
	return MixedConfig{Static: config, Visual: visual}
}

type MixedFactory struct {
	static     *Factory
	visual     referencevisual.ConfigV3
	video      *MixedVideoConfig
	configHash string
}

func NewMixed(ctx context.Context, config MixedConfig) (result *MixedFactory, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = nil
		}
	}()
	if ctx == nil {
		return nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	static, err := New(ctx, config.Static)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(config.Visual.Static, static.config.Visual) || !positiveWithin(config.Visual, referencevisual.DefaultConfigV3(config.Visual.Static.Width, config.Visual.Static.Height)) {
		return nil, ErrConfig
	}
	// Copy nested host configuration before retaining it.
	config.Visual.Static = ownFactoryConfig(config.Static).Visual
	d, v, _ := preflightGap()
	if _, diagnostics, e := referencevisual.PreflightDomainV3(ctx, d, v, config.Visual); e != nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, errors.Join(ErrConfig, e)
	}
	f := &MixedFactory{static: static, visual: config.Visual}
	var runtimeIdentity any
	if config.Video != nil {
		value := *config.Video
		f.video = &value
		if value.Probe == nil || value.Decoder == nil || !bridgeSHA(value.Decoder.Fingerprint()) || value.Scan.MaxOutputBytes <= 0 || value.Scan.MaxOutputBytes > ffprobe.HardMaxVideoScanBytes || value.Scan.MaxFrames <= 0 || value.Scan.MaxFrames > videodecode.DefaultMaxIndexedFrames || value.MaxDiskBytes == 0 || value.MaxDiskBytes > 16<<30 || value.MaxScanBytes == 0 || value.MaxScanBytes > 2<<30 || value.MaxIndexedFrames == 0 || value.MaxIndexedFrames > 16*uint64(videodecode.DefaultMaxIndexedFrames) || value.Scratch == "" {
			return nil, ErrConfig
		}
		if value.MediaRuntime.FFmpegBuild == "" || value.MediaRuntime.DecoderProfile == "" || value.MediaRuntime.ColorProfile == "" {
			return nil, ErrConfig
		}
		if !filepath.IsAbs(value.Scratch) || filepath.Clean(value.Scratch) != value.Scratch {
			return nil, ErrConfig
		}
		resolved, e := filepath.EvalSymlinks(value.Scratch)
		if e != nil || resolved != value.Scratch {
			return nil, ErrConfig
		}
		info, e := os.Lstat(value.Scratch)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return nil, ErrConfig
		}
		identity := value.Probe.Identity()
		p := value.Probe.Options()
		options := value.Decoder.Options()
		if !bridgeSHA(identity.BuildFingerprint) || !bridgeSHA(identity.OptionsFingerprint) || value.Scan.MaxOutputBytes < 1024 || value.Scan.MaxFrames > options.MaxIndexedFrames || p.MaxOutputBytes <= 0 || p.MaxOutputBytes > ffprobe.HardMaxVideoScanBytes || p.MaxStderrBytes <= 0 || p.MaxStderrBytes > 16<<20 || p.ProbeSizeBytes <= 0 || p.ProbeSizeBytes > 1<<30 {
			return nil, ErrConfig
		}
		runtimeIdentity = f.runtimeIdentity()
	}
	f.configHash, err = bridgeDigest(ctx, struct {
		Profile          string
		StaticConfigHash string
		Visual           referencevisual.ConfigV3
		Video            any
	}{MixedConfigProfileV2, static.configHash, f.visual, runtimeIdentity})
	if err != nil {
		return nil, err
	}
	return f, ctx.Err()
}

func fixedMixedAudioOffered() videoedit.BackendCapabilities {
	offered := fixedAudioOffered()
	offered.BackendID = "cutly.reconstruct.native-wav-buses.v2"
	offered.Features = append(offered.Features, documentv2.CapabilityAudioRouteV1, documentv2.CapabilityAudioSamplePeakLimiterV1)
	return offered
}

// runtimeIdentity contains only owned observations/configuration, never paths,
// mutable pointers, fake version strings or inferred closure fingerprints.
type MixedRuntimeBinding struct {
	MediaRuntime       renderapi.MediaRuntime    `json:"media_runtime"`
	Probe              probe.ProbeIdentity       `json:"probe"`
	ProbeOptions       ffprobe.Options           `json:"probe_options"`
	DecoderFingerprint string                    `json:"decoder_fingerprint"`
	DecoderBuild       videodecode.BuildIdentity `json:"decoder_build"`
	DecoderOptions     videodecode.Options       `json:"decoder_options"`
	Scan               ffprobe.VideoScanOptions  `json:"scan"`
	MaxDiskBytes       uint64                    `json:"max_disk_bytes"`
	MaxScanBytes       uint64                    `json:"max_scan_bytes"`
	MaxIndexedFrames   uint64                    `json:"max_indexed_frames"`
}

func (f *MixedFactory) runtimeIdentity() *MixedRuntimeBinding {
	if f.video == nil {
		return nil
	}
	v := f.video
	return &MixedRuntimeBinding{MediaRuntime: v.MediaRuntime, Probe: v.Probe.Identity(), ProbeOptions: v.Probe.Options(), DecoderFingerprint: v.Decoder.Fingerprint(), DecoderBuild: v.Decoder.BuildIdentity(), DecoderOptions: v.Decoder.Options(), Scan: v.Scan, MaxDiskBytes: v.MaxDiskBytes, MaxScanBytes: v.MaxScanBytes, MaxIndexedFrames: v.MaxIndexedFrames}
}

func (f *MixedFactory) preflight(ctx context.Context, input prepared.EnvironmentV2) (result *admittedEnvironment, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = nil
		}
	}()
	if ctx == nil {
		return nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil || f.static == nil || !bridgeSHA(f.configHash) {
		return nil, ErrConfig
	}
	verified, err := prepared.VerifyEnvironmentV2(ctx, input)
	if err != nil {
		return nil, err
	}
	env, err := verified.Environment(ctx)
	if err != nil {
		return nil, err
	}
	scopes, err := selectMixedScopes(ctx, env.Document, env.Plan)
	if err != nil {
		return nil, err
	}
	if err = f.static.config.Authority.CheckEnvironmentPins(ctx, env.Manifest); err != nil {
		return nil, err
	}
	if err = f.static.checkOutput(env); err != nil {
		return nil, err
	}
	if f.video != nil && env.Manifest.MediaRuntime != f.video.MediaRuntime {
		return nil, ErrUnsupported
	}
	// This resource-list projection is used only to reuse static admission arithmetic;
	// the original verified Document/plan/environment are never rewritten or compiled.
	admission := env
	admission.Manifest.Resources = nil
	var disk, scan, frames, videoWorking uint64
	add := func(total *uint64, n uint64) bool {
		if *total > math.MaxUint64-n {
			return false
		}
		*total += n
		return true
	}
	for _, r := range env.Manifest.Resources {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if r.Kind != documentv2.AssetKindVideo {
			admission.Manifest.Resources = append(admission.Manifest.Resources, r)
			continue
		}
		if f.video == nil || env.Manifest.MediaRuntime != f.video.MediaRuntime {
			return nil, ErrUnsupported
		}
		if r.SizeBytes <= 0 || uint64(r.SizeBytes) > f.static.config.Limits.MaxResourceBytes || r.ProbeFingerprint == nil || !bridgeSHA(*r.ProbeFingerprint) || r.MaterializationProfile != renderapi.AVMaterializationProfileV2 || r.InspectionFingerprint != nil {
			return nil, ErrResource
		}
		v := f.video
		options := v.Decoder.Options()
		p := v.Probe.Options()
		if r.SizeBytes > options.MaxInputBytes || uint64(r.SizeBytes) > math.MaxUint64/2 || !add(&disk, 2*uint64(r.SizeBytes)) || !add(&scan, uint64(v.Scan.MaxOutputBytes)) || !add(&frames, uint64(v.Scan.MaxFrames)) {
			return nil, ErrLimit
		}
		// Full scan JSON parsing + records + decoder frame/stderr buffers. This is a
		// conservative Go logical envelope, not an OS codec process RSS guarantee.
		per := uint64(v.Scan.MaxOutputBytes)*16 + uint64(v.Scan.MaxFrames)*2048 + uint64(options.MaxFrameBytes) + uint64(options.MaxStderrBytes) + uint64(p.MaxOutputBytes)*4
		if !add(&videoWorking, per) {
			return nil, ErrLimit
		}
	}
	if f.video != nil && (disk > f.video.MaxDiskBytes || scan > f.video.MaxScanBytes || frames > f.video.MaxIndexedFrames) {
		return nil, ErrLimit
	}
	charge, err := f.static.aggregate(ctx, admission)
	if err != nil {
		return nil, err
	}
	if !add(&charge.workingBytes, videoWorking) || !add(&charge.retainedBytes, videoWorking) || charge.workingBytes > f.static.config.Limits.MaxWorkingBytes || charge.retainedBytes > f.static.config.Limits.MaxRetainedBytes {
		return nil, ErrLimit
	}
	total := charge.resourceBytes
	for _, r := range env.Manifest.Resources {
		if r.Kind == documentv2.AssetKindVideo && !add(&total, uint64(r.SizeBytes)) {
			return nil, ErrLimit
		}
	}
	if total > f.static.config.Limits.MaxTotalResourceBytes || uint64(len(env.Manifest.Resources)) > f.static.config.Limits.MaxResources {
		return nil, ErrLimit
	}
	if _, d, e := referencevisual.PreflightDomainV3(ctx, env.Document, scopes.visual, f.visual); e != nil || documentv2.HasErrorDiagnostics(d) {
		return nil, errors.Join(ErrUnsupported, e)
	}
	if receipt, d := audiograph.PreflightDomainV2(ctx, env.Document, scopes.audio, fixedMixedAudioOffered(), f.static.config.Audio); documentv2.HasErrorDiagnostics(d) || receipt.DocumentHash != env.Plan.DocumentHash {
		return nil, ErrUnsupported
	}
	admission.Plan.Visual = nil
	allVisual := append([]renderplanv2.PlannedClip(nil), env.Plan.Visual...)
	seenClips := map[documentv2.ID]bool{}
	for _, c := range allVisual {
		seenClips[c.Binding.ClipID] = true
	}
	for _, tr := range env.Plan.Transitions {
		for _, b := range []renderplanv2.ClipBinding{tr.Outgoing.Binding, tr.Incoming.Binding} {
			if !seenClips[b.ClipID] {
				seenClips[b.ClipID] = true
				allVisual = append(allVisual, renderplanv2.PlannedClip{Binding: b})
			}
		}
	}
	var videos int
	videoAssets := map[documentv2.ID]bool{}
	for _, c := range allVisual {
		if c.Binding.AssetKind == documentv2.AssetKindVideo {
			videos++
			videoAssets[c.Binding.AssetID] = true
		} else {
			admission.Plan.Visual = append(admission.Plan.Visual, c)
		}
	}
	for _, tr := range env.Plan.Transitions {
		for _, b := range []struct {
			id   documentv2.ID
			kind documentv2.AssetKind
		}{{tr.Outgoing.Binding.AssetID, tr.Outgoing.Binding.AssetKind}, {tr.Incoming.Binding.AssetID, tr.Incoming.Binding.AssetKind}} {
			if b.kind == documentv2.AssetKindVideo {
				videoAssets[b.id] = true
			}
		}
	}
	if videos > f.visual.VideoLimits.MaxClipScopes || len(videoAssets) > f.visual.VideoLimits.MaxBindings {
		return nil, ErrLimit
	}
	if err = f.static.admitSourceCounts(ctx, admission); err != nil {
		return nil, err
	}
	// Source compilers still inspect the complete original Document.
	if err = f.static.admitWAVDocument(ctx, env); err != nil {
		return nil, err
	}
	return &admittedEnvironment{verified: verified, environment: env, scopes: scopes, charge: charge, configHash: f.configHash}, ctx.Err()
}
