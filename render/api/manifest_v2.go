package renderapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/jsonadmission"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	ExecutionManifestV2SchemaVersion = 2
	ExecutionManifestV2Profile       = "cutly.execution-manifest.document-v2.v2"
	ExecutionManifestV2HashProfile   = "cutly.hash.sha256.execution-manifest-document-v2-canonical-json.v2"

	PipelineChunkedRestartableV1 = "cutly.pipeline.chunked-restartable.v1"
	PipelineStreamEncodeV1       = "cutly.pipeline.stream-encode.v1"
	TransportCASSpoolV1          = "cutly.transport.cas-spool.v1"
	TransportBoundedRingV1       = "cutly.transport.bounded-ring.v1"

	IntermediateVideoSurfaceRGBA16V1 = "cutly.surface.linear-bt709-premultiplied-rgba16.v1"
	IntermediateAudioBlockPCM16LEV1  = "cutly.intermediate-audio.pcm-s16le.interleaved.v1"

	MaxCanonicalExecutionManifestV2Bytes       = 64 << 20
	MaxProgramConfigurationV2Bytes             = 1 << 20
	MaxExecutionManifestV2Resources            = 16_384
	MaxExecutionManifestV2Features             = 16_384
	MaxMaterializedResourceBytesV2       int64 = 1 << 50
)

var (
	ErrExecutionManifestV2Invalid             = errors.New("render: execution manifest v2 is invalid")
	ErrExecutionManifestV2FingerprintMismatch = errors.New("render: execution manifest v2 fingerprint mismatch")
	ErrVerifiedExecutionManifestV2Required    = errors.New("render: verified execution manifest v2 is required")
)

type BackendCapabilitiesV2 struct {
	BackendID        string                   `json:"backend_id"`
	BuildFingerprint string                   `json:"build_fingerprint"`
	TrackKinds       []documentv2.TrackKind   `json:"track_kinds,omitempty"`
	AssetKinds       []documentv2.AssetKind   `json:"asset_kinds,omitempty"`
	Features         []videoedit.CapabilityID `json:"features,omitempty"`
	MaxWidth         int                      `json:"max_width"`
	MaxHeight        int                      `json:"max_height"`
}

type MaterializedResourceV2 struct {
	AssetID                documentv2.ID        `json:"asset_id"`
	Kind                   documentv2.AssetKind `json:"kind"`
	ContentHash            string               `json:"content_hash"`
	SizeBytes              int64                `json:"size_bytes"`
	LocatorScheme          LocatorScheme        `json:"locator_scheme"`
	Locator                string               `json:"locator"`
	MaterializationProfile string               `json:"materialization_profile"`
	InspectionFingerprint  string               `json:"inspection_fingerprint,omitempty"`
	ProbeFingerprint       string               `json:"probe_fingerprint,omitempty"`
}

type ProgramSelectionV2 struct {
	Enabled                   bool            `json:"enabled"`
	Profile                   string          `json:"profile,omitempty"`
	DescriptorFingerprint     string          `json:"descriptor_fingerprint,omitempty"`
	Configuration             json.RawMessage `json:"configuration,omitempty"`
	ConfigurationFingerprint  string          `json:"configuration_fingerprint,omitempty"`
	SourceManifestFingerprint string          `json:"source_manifest_fingerprint,omitempty"`
}

type IntermediateProfilesV2 struct {
	VideoSurfaceProfile string `json:"video_surface_profile"`
	AudioBlockProfile   string `json:"audio_block_profile"`
}

type OutputSpecV2 struct {
	ReferenceProfile    string                 `json:"reference_profile"`
	Format              string                 `json:"format"`
	Width               int                    `json:"width"`
	Height              int                    `json:"height"`
	PixelFormat         string                 `json:"pixel_format"`
	ColorSpace          string                 `json:"color_space"`
	VideoCodec          string                 `json:"video_codec"`
	AudioCodec          string                 `json:"audio_codec"`
	SampleRate          int64                  `json:"sample_rate"`
	Channels            int                    `json:"channels"`
	Intermediate        IntermediateProfilesV2 `json:"intermediate"`
	DecodedVideoProfile string                 `json:"decoded_video_profile"`
	DecodedAudioProfile string                 `json:"decoded_audio_profile"`
}

type ExecutionManifestV2 struct {
	SchemaVersion        int                      `json:"schema_version"`
	Profile              string                   `json:"profile"`
	CanonicalJSONProfile string                   `json:"canonical_json_profile"`
	HashProfile          string                   `json:"hash_profile"`
	PlanFingerprint      string                   `json:"plan_fingerprint"`
	SemanticProfile      string                   `json:"semantic_profile"`
	DocumentHash         string                   `json:"document_hash"`
	CompilerBuild        string                   `json:"compiler_build"`
	EvaluatorBuild       string                   `json:"evaluator_build"`
	ProviderBuild        string                   `json:"provider_build"`
	PipelineProfile      string                   `json:"pipeline_profile"`
	TransportProfile     string                   `json:"transport_profile"`
	Resources            []MaterializedResourceV2 `json:"resources,omitempty"`
	Backend              BackendCapabilitiesV2    `json:"backend"`
	MediaRuntime         MediaRuntime             `json:"media_runtime"`
	Output               OutputSpecV2             `json:"output"`
	Visual               ProgramSelectionV2       `json:"visual"`
	Audio                ProgramSelectionV2       `json:"audio"`
	DeterministicSeed    int64                    `json:"deterministic_seed"`
	Fingerprint          string                   `json:"fingerprint"`
}

// VerifiedExecutionManifestV2 is an immutable integrity handle that was both
// wire verified and checked against a document-bound semantic plan. It does
// not prove that declared locators, executable digests, probe receipts or
// program identities are authorized by the current host. S3 reconstruction
// must independently authorize and rehash them before execution.
type VerifiedExecutionManifestV2 struct {
	manifest ExecutionManifestV2
	verified bool
}

func (verified VerifiedExecutionManifestV2) Fingerprint() string {
	if !verified.verified {
		return ""
	}
	return verified.manifest.Fingerprint
}

func (verified VerifiedExecutionManifestV2) PlanFingerprint() string {
	if !verified.verified {
		return ""
	}
	return verified.manifest.PlanFingerprint
}

func (verified VerifiedExecutionManifestV2) Snapshot() (ExecutionManifestV2, error) {
	if !verified.verified {
		return ExecutionManifestV2{}, ErrVerifiedExecutionManifestV2Required
	}
	raw, err := CanonicalExecutionManifestV2(verified.manifest)
	if err != nil {
		return ExecutionManifestV2{}, err
	}
	return DecodeExecutionManifestV2(raw)
}

// FreezeExecutionManifestV2 canonicalizes and fingerprints a draft only after
// the semantic plan has been rebound to its authoritative Document. The result
// proves declaration integrity, not host execution authority.
func FreezeExecutionManifestV2(ctx context.Context, bound renderplanv2.DocumentBoundPlan, draft ExecutionManifestV2) (VerifiedExecutionManifestV2, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	if err := preflightExecutionManifestV2Draft(ctx, draft); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	plan, err := bound.Snapshot()
	if err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	draft = cloneExecutionManifestV2(draft)
	draft.SchemaVersion = ExecutionManifestV2SchemaVersion
	draft.Profile = ExecutionManifestV2Profile
	draft.CanonicalJSONProfile = canonicaljson.Profile
	draft.HashProfile = ExecutionManifestV2HashProfile
	draft.PlanFingerprint = plan.Fingerprint
	draft.SemanticProfile = plan.Profile
	draft.DocumentHash = plan.DocumentHash
	draft.Fingerprint = ""
	canonicalizeExecutionManifestV2(&draft)
	if err := canonicalizeProgramSelectionV2(&draft.Visual); err != nil {
		return VerifiedExecutionManifestV2{}, fmt.Errorf("%w: visual: %v", ErrExecutionManifestV2Invalid, err)
	}
	if err := canonicalizeProgramSelectionV2(&draft.Audio); err != nil {
		return VerifiedExecutionManifestV2{}, fmt.Errorf("%w: audio: %v", ErrExecutionManifestV2Invalid, err)
	}
	if err := ctx.Err(); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	if err := validateExecutionManifestV2(draft); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	if err := validateExecutionManifestV2AgainstPlan(plan, draft); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	fingerprint, err := executionManifestV2FingerprintContext(ctx, draft)
	if err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	draft.Fingerprint = fingerprint
	if err := ctx.Err(); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	return VerifiedExecutionManifestV2{manifest: draft, verified: true}, nil
}

// VerifyExecutionManifestV2 round-trips an existing manifest through the
// strict decoder and then rechecks its complete binding to the plan.
func VerifyExecutionManifestV2(ctx context.Context, bound renderplanv2.DocumentBoundPlan, manifest ExecutionManifestV2) (VerifiedExecutionManifestV2, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	plan, err := bound.Snapshot()
	if err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	raw, err := CanonicalExecutionManifestV2Context(ctx, manifest)
	if err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	decoded, err := DecodeExecutionManifestV2(raw)
	if err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	if err := validateExecutionManifestV2AgainstPlan(plan, decoded); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	if err := ctx.Err(); err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	return VerifiedExecutionManifestV2{manifest: decoded, verified: true}, nil
}

func CanonicalExecutionManifestV2(manifest ExecutionManifestV2) ([]byte, error) {
	return CanonicalExecutionManifestV2Context(context.Background(), manifest)
}

func CanonicalExecutionManifestV2Context(ctx context.Context, manifest ExecutionManifestV2) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := preflightExecutionManifestV2Draft(ctx, manifest); err != nil {
		return nil, err
	}
	if err := validateExecutionManifestV2(manifest); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return canonicaljson.Marshal(manifest)
}

// DecodeExecutionManifestV2 verifies the standalone canonical wire value. It
// does not authorize execution. VerifyExecutionManifestV2 additionally proves
// declaration consistency with a DocumentBoundPlan, while S3 must still
// reconstruct host-authorized resources and executable identities.
func DecodeExecutionManifestV2(raw []byte) (ExecutionManifestV2, error) {
	if len(raw) == 0 || len(raw) > MaxCanonicalExecutionManifestV2Bytes {
		return ExecutionManifestV2{}, fmt.Errorf("%w: byte length %d", ErrExecutionManifestV2Invalid, len(raw))
	}
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{
		MaxInputBytes: MaxCanonicalExecutionManifestV2Bytes,
		MaxDepth:      96, MaxValues: 1 << 20,
		MaxStringBytes:   MaxCanonicalExecutionManifestV2Bytes,
		MaxIntegerDigits: 20,
	})
	if err != nil {
		return ExecutionManifestV2{}, fmt.Errorf("%w: canonical JSON: %v", ErrExecutionManifestV2Invalid, err)
	}
	if !bytes.Equal(raw, canonical) {
		return ExecutionManifestV2{}, fmt.Errorf("%w: noncanonical encoding", ErrExecutionManifestV2Invalid)
	}
	var manifest ExecutionManifestV2
	if err := wirejson.Decode(raw, &manifest); err != nil {
		return ExecutionManifestV2{}, fmt.Errorf("%w: decode: %v", ErrExecutionManifestV2Invalid, err)
	}
	if err := validateExecutionManifestV2(manifest); err != nil {
		return ExecutionManifestV2{}, err
	}
	canonicalManifest := cloneExecutionManifestV2(manifest)
	canonicalManifest.Fingerprint = ""
	canonicalizeExecutionManifestV2(&canonicalManifest)
	if err := canonicalizeProgramSelectionV2(&canonicalManifest.Visual); err != nil {
		return ExecutionManifestV2{}, fmt.Errorf("%w: visual: %v", ErrExecutionManifestV2Invalid, err)
	}
	if err := canonicalizeProgramSelectionV2(&canonicalManifest.Audio); err != nil {
		return ExecutionManifestV2{}, fmt.Errorf("%w: audio: %v", ErrExecutionManifestV2Invalid, err)
	}
	want, err := executionManifestV2Fingerprint(canonicalManifest)
	if err != nil {
		return ExecutionManifestV2{}, err
	}
	if manifest.Fingerprint != want {
		return ExecutionManifestV2{}, fmt.Errorf("%w: got %q, want %q", ErrExecutionManifestV2FingerprintMismatch, manifest.Fingerprint, want)
	}
	canonicalManifest.Fingerprint = manifest.Fingerprint
	canonicalRaw, err := canonicaljson.Marshal(canonicalManifest)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return ExecutionManifestV2{}, fmt.Errorf("%w: set, resource or configuration fields are not canonical", ErrExecutionManifestV2Invalid)
	}
	return manifest, nil
}

func validateExecutionManifestV2(manifest ExecutionManifestV2) error {
	if manifest.SchemaVersion != ExecutionManifestV2SchemaVersion || manifest.Profile != ExecutionManifestV2Profile ||
		manifest.CanonicalJSONProfile != canonicaljson.Profile || manifest.HashProfile != ExecutionManifestV2HashProfile ||
		manifest.SemanticProfile != renderplanv2.ProfileV1 {
		return fmt.Errorf("%w: unsupported version/profile", ErrExecutionManifestV2Invalid)
	}
	for _, item := range []struct{ name, value string }{
		{"plan_fingerprint", manifest.PlanFingerprint}, {"document_hash", manifest.DocumentHash},
		{"compiler_build", manifest.CompilerBuild}, {"evaluator_build", manifest.EvaluatorBuild},
		{"provider_build", manifest.ProviderBuild}, {"backend.build_fingerprint", manifest.Backend.BuildFingerprint},
		{"media_runtime.ffmpeg_build", manifest.MediaRuntime.FFmpegBuild},
	} {
		if !validDigest(item.value) {
			return fmt.Errorf("%w: %s must be a canonical sha256 digest", ErrExecutionManifestV2Invalid, item.name)
		}
	}
	if manifest.Fingerprint != "" && !validDigest(manifest.Fingerprint) {
		return fmt.Errorf("%w: fingerprint must be a canonical sha256 digest", ErrExecutionManifestV2Invalid)
	}
	if len(manifest.Resources) > MaxExecutionManifestV2Resources || len(manifest.Backend.TrackKinds) > 4 ||
		len(manifest.Backend.AssetKinds) > 7 || len(manifest.Backend.Features) > MaxExecutionManifestV2Features {
		return fmt.Errorf("%w: manifest collection limit exceeded", ErrExecutionManifestV2Invalid)
	}
	if manifest.PipelineProfile != PipelineChunkedRestartableV1 && manifest.PipelineProfile != PipelineStreamEncodeV1 {
		return fmt.Errorf("%w: unsupported pipeline profile %q", ErrExecutionManifestV2Invalid, manifest.PipelineProfile)
	}
	if (manifest.PipelineProfile == PipelineChunkedRestartableV1 && manifest.TransportProfile != TransportCASSpoolV1) ||
		(manifest.PipelineProfile == PipelineStreamEncodeV1 && manifest.TransportProfile != TransportBoundedRingV1) {
		return fmt.Errorf("%w: transport %q is not valid for pipeline %q", ErrExecutionManifestV2Invalid, manifest.TransportProfile, manifest.PipelineProfile)
	}
	if err := validateBackendCapabilitiesV2(manifest.Backend); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.MediaRuntime.DecoderProfile) == "" || strings.TrimSpace(manifest.MediaRuntime.EncoderProfile) == "" || strings.TrimSpace(manifest.MediaRuntime.ColorProfile) == "" {
		return fmt.Errorf("%w: decoder, encoder and color profiles are required", ErrExecutionManifestV2Invalid)
	}
	if manifest.MediaRuntime.TextShaperBuild != "" && !validDigest(manifest.MediaRuntime.TextShaperBuild) {
		return fmt.Errorf("%w: text shaper build must be a canonical sha256 digest", ErrExecutionManifestV2Invalid)
	}
	if err := validateOutputSpecV2(manifest.Output); err != nil {
		return err
	}
	if err := validateProgramSelectionV2("visual", manifest.Visual); err != nil {
		return err
	}
	if err := validateProgramSelectionV2("audio", manifest.Audio); err != nil {
		return err
	}
	var totalResourceBytes int64
	for index, resource := range manifest.Resources {
		if err := validateMaterializedResourceV2(resource); err != nil {
			return fmt.Errorf("%w: resource %d: %v", ErrExecutionManifestV2Invalid, index, err)
		}
		if index > 0 && manifest.Resources[index-1].AssetID >= resource.AssetID {
			return fmt.Errorf("%w: resources are not sorted unique", ErrExecutionManifestV2Invalid)
		}
		if resource.SizeBytes > MaxMaterializedResourceBytesV2-totalResourceBytes {
			return fmt.Errorf("%w: materialized resource bytes exceed %d", ErrExecutionManifestV2Invalid, MaxMaterializedResourceBytesV2)
		}
		totalResourceBytes += resource.SizeBytes
	}
	return nil
}

func validateBackendCapabilitiesV2(backend BackendCapabilitiesV2) error {
	if strings.TrimSpace(backend.BackendID) == "" || !validDigest(backend.BuildFingerprint) || backend.MaxWidth <= 0 || backend.MaxHeight <= 0 ||
		backend.MaxWidth > math.MaxInt32 || backend.MaxHeight > math.MaxInt32 {
		return fmt.Errorf("%w: backend identity, build and positive limits are required", ErrExecutionManifestV2Invalid)
	}
	for _, kind := range backend.TrackKinds {
		switch kind {
		case documentv2.TrackKindVideo, documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector:
		default:
			return fmt.Errorf("%w: unsupported backend track kind %q", ErrExecutionManifestV2Invalid, kind)
		}
	}
	for _, kind := range backend.AssetKinds {
		if !validBackendAssetKindV2(kind) {
			return fmt.Errorf("%w: unsupported backend asset kind %q", ErrExecutionManifestV2Invalid, kind)
		}
	}
	for _, capability := range backend.Features {
		if !capabilityIDPattern.MatchString(string(capability)) {
			return fmt.Errorf("%w: backend capability %q must be a versioned ID", ErrExecutionManifestV2Invalid, capability)
		}
	}
	if !sortedUniqueV2(backend.TrackKinds) || !sortedUniqueV2(backend.AssetKinds) || !sortedUniqueV2(backend.Features) {
		return fmt.Errorf("%w: backend capability sets must be sorted unique", ErrExecutionManifestV2Invalid)
	}
	return nil
}

func validateOutputSpecV2(output OutputSpecV2) error {
	if output.Width <= 0 || output.Height <= 0 || output.SampleRate <= 0 || output.Channels <= 0 ||
		output.Width > math.MaxInt32 || output.Height > math.MaxInt32 || output.Channels > math.MaxInt32 {
		return fmt.Errorf("%w: output dimensions, sample rate and channels must be positive", ErrExecutionManifestV2Invalid)
	}
	for _, value := range []string{output.ReferenceProfile, output.Format, output.PixelFormat, output.ColorSpace, output.VideoCodec, output.AudioCodec} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: output profiles, format, codecs and color fields are required", ErrExecutionManifestV2Invalid)
		}
	}
	if output.Intermediate.VideoSurfaceProfile != IntermediateVideoSurfaceRGBA16V1 || output.Intermediate.AudioBlockProfile != IntermediateAudioBlockPCM16LEV1 {
		return fmt.Errorf("%w: unsupported intermediate profiles %q / %q", ErrExecutionManifestV2Invalid, output.Intermediate.VideoSurfaceProfile, output.Intermediate.AudioBlockProfile)
	}
	if output.DecodedVideoProfile != DecodedVideoProfileRGBA8V1 || output.DecodedAudioProfile != DecodedAudioProfilePCM16LEV1 {
		return fmt.Errorf("%w: unsupported decoded evidence profiles %q / %q", ErrExecutionManifestV2Invalid, output.DecodedVideoProfile, output.DecodedAudioProfile)
	}
	return nil
}

func validateProgramSelectionV2(name string, selection ProgramSelectionV2) error {
	if !selection.Enabled {
		if selection.Profile != "" || selection.DescriptorFingerprint != "" || len(selection.Configuration) != 0 || selection.ConfigurationFingerprint != "" || selection.SourceManifestFingerprint != "" {
			return fmt.Errorf("%w: disabled %s selection must omit all identity and configuration fields", ErrExecutionManifestV2Invalid, name)
		}
		return nil
	}
	if !capabilityIDPattern.MatchString(selection.Profile) || !validDigest(selection.DescriptorFingerprint) ||
		!validDigest(selection.ConfigurationFingerprint) || !validDigest(selection.SourceManifestFingerprint) || len(selection.Configuration) == 0 {
		return fmt.Errorf("%w: enabled %s selection is incomplete", ErrExecutionManifestV2Invalid, name)
	}
	canonical, err := canonicalProgramConfigurationV2(selection.Configuration)
	if err != nil || !bytes.Equal(canonical, selection.Configuration) {
		return fmt.Errorf("%w: %s configuration is not canonical: %v", ErrExecutionManifestV2Invalid, name, err)
	}
	want := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(want[:])
	if selection.ConfigurationFingerprint != wantDigest {
		return fmt.Errorf("%w: %s configuration fingerprint mismatch", ErrExecutionManifestV2Invalid, name)
	}
	return nil
}

func validateMaterializedResourceV2(resource MaterializedResourceV2) error {
	if strings.TrimSpace(string(resource.AssetID)) == "" || !validAssetKindV2(resource.Kind) || !validDigest(resource.ContentHash) || resource.SizeBytes <= 0 ||
		!capabilityIDPattern.MatchString(resource.MaterializationProfile) || strings.TrimSpace(resource.Locator) == "" {
		return errors.New("resource is incomplete")
	}
	if resource.SizeBytes > MaxMaterializedResourceBytesV2 {
		return fmt.Errorf("resource exceeds %d bytes", MaxMaterializedResourceBytesV2)
	}
	if err := validateMaterializedLocatorV2(resource.LocatorScheme, resource.Locator, resource.ContentHash); err != nil {
		return fmt.Errorf("locator: %v", err)
	}
	switch resource.Kind {
	case documentv2.AssetKindVideo, documentv2.AssetKindAudio:
		if !validDigest(resource.ProbeFingerprint) || resource.InspectionFingerprint != "" {
			return errors.New("temporal media requires only a probe fingerprint")
		}
	case documentv2.AssetKindImage, documentv2.AssetKindFont, documentv2.AssetKindData:
		if !validDigest(resource.InspectionFingerprint) || resource.ProbeFingerprint != "" {
			return errors.New("static resources require only an inspection fingerprint")
		}
	default:
		return fmt.Errorf("unsupported resource kind %q", resource.Kind)
	}
	return nil
}

func validateExecutionManifestV2AgainstPlan(plan renderplanv2.Plan, manifest ExecutionManifestV2) error {
	if manifest.PlanFingerprint != plan.Fingerprint || manifest.SemanticProfile != plan.Profile || manifest.DocumentHash != plan.DocumentHash {
		return fmt.Errorf("%w: plan identity mismatch", ErrExecutionManifestV2Invalid)
	}
	if manifest.Output.Width != plan.Target.Width || manifest.Output.Height != plan.Target.Height ||
		manifest.Output.SampleRate != plan.Target.SampleClock.SamplesPerSecond || manifest.Output.Channels != plan.Target.Channels ||
		manifest.Output.PixelFormat != plan.Target.PixelFormat || manifest.Output.ColorSpace != plan.Target.ColorSpace {
		return fmt.Errorf("%w: output spec does not match plan target", ErrExecutionManifestV2Invalid)
	}
	if manifest.Backend.MaxWidth < plan.Target.Width || manifest.Backend.MaxHeight < plan.Target.Height {
		return fmt.Errorf("%w: backend limits do not cover target", ErrExecutionManifestV2Invalid)
	}
	if missingRequirementsV2(plan.RequiredCapabilities.TrackKinds, manifest.Backend.TrackKinds) ||
		missingRequirementsV2(plan.RequiredCapabilities.AssetKinds, manifest.Backend.AssetKinds) ||
		missingRequirementsV2(plan.RequiredCapabilities.Features, manifest.Backend.Features) {
		return fmt.Errorf("%w: backend capability gap", ErrExecutionManifestV2Invalid)
	}
	if len(manifest.Resources) != len(plan.Resources) {
		return fmt.Errorf("%w: materialized resource count %d does not match plan count %d", ErrExecutionManifestV2Invalid, len(manifest.Resources), len(plan.Resources))
	}
	for index, planned := range plan.Resources {
		materialized := manifest.Resources[index]
		if materialized.AssetID != planned.AssetID || materialized.Kind != planned.Kind || (planned.ContentHash != "" && materialized.ContentHash != planned.ContentHash) {
			return fmt.Errorf("%w: resource %d does not match planned asset", ErrExecutionManifestV2Invalid, index)
		}
	}
	if plan.FrameRange.Count > 0 && !manifest.Visual.Enabled {
		return fmt.Errorf("%w: nonempty frame range requires an explicit visual or transparent-gap program", ErrExecutionManifestV2Invalid)
	}
	if plan.SampleRange.Count > 0 && !manifest.Audio.Enabled {
		return fmt.Errorf("%w: nonempty sample range requires an explicit audio or silence program", ErrExecutionManifestV2Invalid)
	}
	return nil
}

func canonicalizeExecutionManifestV2(manifest *ExecutionManifestV2) {
	sort.Slice(manifest.Resources, func(i, j int) bool { return manifest.Resources[i].AssetID < manifest.Resources[j].AssetID })
	sort.Slice(manifest.Backend.TrackKinds, func(i, j int) bool { return manifest.Backend.TrackKinds[i] < manifest.Backend.TrackKinds[j] })
	manifest.Backend.TrackKinds = compactSortedV2(manifest.Backend.TrackKinds)
	sort.Slice(manifest.Backend.AssetKinds, func(i, j int) bool { return manifest.Backend.AssetKinds[i] < manifest.Backend.AssetKinds[j] })
	manifest.Backend.AssetKinds = compactSortedV2(manifest.Backend.AssetKinds)
	sort.Slice(manifest.Backend.Features, func(i, j int) bool { return manifest.Backend.Features[i] < manifest.Backend.Features[j] })
	manifest.Backend.Features = compactSortedV2(manifest.Backend.Features)
}

func preflightExecutionManifestV2Draft(ctx context.Context, manifest ExecutionManifestV2) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(manifest.Resources) > MaxExecutionManifestV2Resources || len(manifest.Backend.TrackKinds) > 4 ||
		len(manifest.Backend.AssetKinds) > 7 || len(manifest.Backend.Features) > MaxExecutionManifestV2Features ||
		len(manifest.Visual.Configuration) > MaxProgramConfigurationV2Bytes || len(manifest.Audio.Configuration) > MaxProgramConfigurationV2Bytes {
		return fmt.Errorf("%w: draft collection or configuration limit exceeded", ErrExecutionManifestV2Invalid)
	}
	for index := range manifest.Resources {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	if err := jsonadmission.Check(ctx, manifest, MaxCanonicalExecutionManifestV2Bytes, 1<<20); err != nil {
		return fmt.Errorf("%w: canonical admission: %v", ErrExecutionManifestV2Invalid, err)
	}
	return ctx.Err()
}

// validateMaterializedLocatorV2 validates only the portable wire spelling. A
// file URI remains an untrusted declaration until S3 resolves it beneath a
// host-owned root with no-follow semantics and rehashes the opened bytes.
func validateMaterializedLocatorV2(scheme LocatorScheme, locator, contentHash string) error {
	if strings.ContainsRune(locator, 0) {
		return errors.New("NUL bytes are forbidden")
	}
	switch scheme {
	case LocatorCAS:
		want := "cas://sha256/" + strings.TrimPrefix(contentHash, "sha256:")
		if locator != want {
			return fmt.Errorf("CAS locator must equal content identity %q", want)
		}
	case LocatorFile:
		parsed, err := url.ParseRequestURI(locator)
		if err != nil {
			return fmt.Errorf("file locator URI: %v", err)
		}
		if parsed.Scheme != "file" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			!path.IsAbs(parsed.Path) || path.Clean(parsed.Path) != parsed.Path || parsed.Path == "/" {
			return errors.New("file locator must be a hostless clean non-root file URI")
		}
	case "":
		return errors.New("locator scheme is required")
	default:
		return fmt.Errorf("unsupported scheme %q", scheme)
	}
	return nil
}

func canonicalizeProgramSelectionV2(selection *ProgramSelectionV2) error {
	if !selection.Enabled {
		return nil
	}
	canonical, err := canonicalProgramConfigurationV2(selection.Configuration)
	if err != nil {
		return err
	}
	selection.Configuration = canonical
	digest := sha256.Sum256(canonical)
	selection.ConfigurationFingerprint = "sha256:" + hex.EncodeToString(digest[:])
	return nil
}

func canonicalProgramConfigurationV2(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxProgramConfigurationV2Bytes {
		return nil, fmt.Errorf("configuration byte length %d", len(raw))
	}
	return canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{
		MaxInputBytes: MaxProgramConfigurationV2Bytes,
		MaxDepth:      64, MaxValues: 1 << 16,
		MaxStringBytes:   MaxProgramConfigurationV2Bytes,
		MaxIntegerDigits: 20,
	})
}

func cloneExecutionManifestV2(manifest ExecutionManifestV2) ExecutionManifestV2 {
	clone := manifest
	clone.Resources = append([]MaterializedResourceV2(nil), manifest.Resources...)
	clone.Backend.TrackKinds = append([]documentv2.TrackKind(nil), manifest.Backend.TrackKinds...)
	clone.Backend.AssetKinds = append([]documentv2.AssetKind(nil), manifest.Backend.AssetKinds...)
	clone.Backend.Features = append([]videoedit.CapabilityID(nil), manifest.Backend.Features...)
	clone.Visual.Configuration = append(json.RawMessage(nil), manifest.Visual.Configuration...)
	clone.Audio.Configuration = append(json.RawMessage(nil), manifest.Audio.Configuration...)
	return clone
}

func executionManifestV2Fingerprint(manifest ExecutionManifestV2) (string, error) {
	return executionManifestV2FingerprintContext(context.Background(), manifest)
}

func executionManifestV2FingerprintContext(ctx context.Context, manifest ExecutionManifestV2) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manifest.Fingerprint = ""
	if err := preflightExecutionManifestV2Draft(ctx, manifest); err != nil {
		return "", err
	}
	raw, err := canonicaljson.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("%w: canonical JSON: %v", ErrExecutionManifestV2Invalid, err)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validAssetKindV2(kind documentv2.AssetKind) bool {
	switch kind {
	case documentv2.AssetKindVideo, documentv2.AssetKindAudio, documentv2.AssetKindImage, documentv2.AssetKindFont, documentv2.AssetKindData:
		return true
	default:
		return false
	}
}

func validBackendAssetKindV2(kind documentv2.AssetKind) bool {
	return validAssetKindV2(kind) || kind == documentv2.AssetKindText || kind == documentv2.AssetKindVector
}

func sortedUniqueV2[S ~[]E, E ~string](values S) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func compactSortedV2[S ~[]E, E comparable](values S) S {
	if len(values) < 2 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] == values[write-1] {
			continue
		}
		values[write] = values[read]
		write++
	}
	return values[:write]
}

func missingRequirementsV2[S ~[]E, E ~string](required, offered S) bool {
	index := 0
	for _, requirement := range required {
		for index < len(offered) && offered[index] < requirement {
			index++
		}
		if index == len(offered) || offered[index] != requirement {
			return true
		}
	}
	return false
}
