package renderapi

import (
	"context"
	"encoding/json"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	ExecutionManifestSchemaVersionV2 uint32 = 2
	ExecutionManifestProfileV2              = "cutly.execution-manifest.document-v2.v2"
	RenderRequestSchemaVersionV2     uint32 = 2
	RenderRequestProfileV2                  = "cutly.render-request.document-v2.v2"
	ProgramSourceManifestProfileV2          = "cutly.program-source-manifest.document-v2.v2"
	VisualProgramProfileV2                  = "cutly.program.document-v2.plan-visual.v2"
	AudioProgramProfileV2                   = "cutly.program.document-v2.plan-audio.v2"
	TransparentGapProgramProfileV2          = "cutly.program.document-v2.transparent-gap.v2"
	SilenceProgramProfileV2                 = "cutly.program.document-v2.silence.v2"
	VisualConfigurationProfileV2            = "cutly.program-config.document-v2.visual.v2"
	AudioConfigurationProfileV2             = "cutly.program-config.document-v2.audio.v2"
	VideoSurfaceProfileV2                   = "cutly.surface.linear-bt709-premultiplied-rgba16.v1"
	AudioBlockProfileV2                     = DecodedAudioProfilePCM16LEV1
	OutputReferenceProfileV2                = "cutly.output.document-v2.v2"
	AVMaterializationProfileV2              = "cutly.materialized.document-v2.probed-av.v2"
	ImageMaterializationProfileV2           = "cutly.materialized.document-v2.inspected-image.v2"
	FontMaterializationProfileV2            = "cutly.materialized.document-v2.inspected-font.v2"
)

type BackendCapabilitiesV2 struct {
	BackendID        string                   `json:"backend_id"`
	BuildFingerprint string                   `json:"build_fingerprint"`
	TrackKinds       []documentv2.TrackKind   `json:"track_kinds"`
	AssetKinds       []documentv2.AssetKind   `json:"asset_kinds"`
	Features         []videoedit.CapabilityID `json:"features"`
	MaxWidth         uint32                   `json:"max_width"`
	MaxHeight        uint32                   `json:"max_height"`
}

// MaterializedResourceV2 is an identity claim, not proof of bytes or locator authority.
// ProbeFingerprint and InspectionFingerprint form an exclusive, kind-dependent union.
type MaterializedResourceV2 struct {
	AssetID                documentv2.ID        `json:"asset_id"`
	Kind                   documentv2.AssetKind `json:"kind"`
	ContentHash            string               `json:"content_hash"`
	SizeBytes              int64                `json:"size_bytes"`
	LocatorScheme          LocatorScheme        `json:"locator_scheme"`
	Locator                string               `json:"locator"`
	MaterializationProfile string               `json:"materialization_profile"`
	InspectionFingerprint  *string              `json:"inspection_fingerprint,omitempty"`
	ProbeFingerprint       *string              `json:"probe_fingerprint,omitempty"`
}

type ProgramDomainV2 string

const (
	ProgramDomainVisualV2 ProgramDomainV2 = "visual"
	ProgramDomainAudioV2  ProgramDomainV2 = "audio"
)

// Descriptor fields are fixed by ProgramDescriptorForV2, not caller-defined hashes.
type ProgramDescriptorV2 struct {
	SchemaVersion         uint32          `json:"schema_version"`
	Profile               string          `json:"profile"`
	Domain                ProgramDomainV2 `json:"domain"`
	ConfigurationProfile  string          `json:"configuration_profile"`
	SourceManifestProfile string          `json:"source_manifest_profile"`
	OutputProfile         string          `json:"output_profile"`
	Contribution          string          `json:"contribution"`
}

type VisualProgramConfigurationV2 struct {
	SchemaVersion  uint32               `json:"schema_version"`
	Profile        string               `json:"profile"`
	Width          uint32               `json:"width"`
	Height         uint32               `json:"height"`
	FrameClock     videoedit.FrameClock `json:"frame_clock"`
	FrameRange     videoedit.TickRange  `json:"frame_range"`
	SurfaceProfile string               `json:"surface_profile"`
}

type AudioProgramConfigurationV2 struct {
	SchemaVersion uint32                `json:"schema_version"`
	Profile       string                `json:"profile"`
	SampleClock   videoedit.SampleClock `json:"sample_clock"`
	SampleRange   videoedit.TickRange   `json:"sample_range"`
	Channels      uint32                `json:"channels"`
	BlockProfile  string                `json:"block_profile"`
}

// ProgramSourceManifestV2 binds ordered plan records and exactly the domain's
// sorted materialized resources. It deliberately has no self fingerprint member.
// Its fingerprint is stored by ProgramSelectionV2, avoiding an identity cycle.
// This is a source identity contract, not an implementation or execution receipt.
type ProgramSourceManifestV2 struct {
	SchemaVersion            uint32                           `json:"schema_version"`
	Profile                  string                           `json:"profile"`
	CanonicalJSONProfile     string                           `json:"canonical_json_profile"`
	DocumentHash             string                           `json:"document_hash"`
	PlanFingerprint          string                           `json:"plan_fingerprint"`
	Domain                   ProgramDomainV2                  `json:"domain"`
	ProgramProfile           string                           `json:"program_profile"`
	DescriptorFingerprint    string                           `json:"descriptor_fingerprint"`
	ConfigurationFingerprint string                           `json:"configuration_fingerprint"`
	RequestedRange           documentv2.TimeRange             `json:"requested_range"`
	TickRange                videoedit.TickRange              `json:"tick_range"`
	Clips                    []renderplanv2.PlannedClip       `json:"clips"`
	Transitions              []renderplanv2.PlannedTransition `json:"transitions"`
	AudioBusIDs              []documentv2.ID                  `json:"audio_bus_ids"`
	Resources                []MaterializedResourceV2         `json:"resources"`
}

// Disabled selections encode exactly {"enabled":false}. All enabled fields are
// mandatory; pointers preserve conditional presence and reject null/empty aliases.
type ProgramSelectionV2 struct {
	Enabled                   bool                     `json:"enabled"`
	Profile                   *string                  `json:"profile,omitempty"`
	Descriptor                *ProgramDescriptorV2     `json:"descriptor,omitempty"`
	DescriptorFingerprint     *string                  `json:"descriptor_fingerprint,omitempty"`
	Configuration             json.RawMessage          `json:"configuration,omitempty"`
	ConfigurationFingerprint  *string                  `json:"configuration_fingerprint,omitempty"`
	SourceManifest            *ProgramSourceManifestV2 `json:"source_manifest,omitempty"`
	SourceManifestFingerprint *string                  `json:"source_manifest_fingerprint,omitempty"`
}

type IntermediateProfilesV2 struct {
	VideoSurfaceProfile string `json:"video_surface_profile"`
	AudioBlockProfile   string `json:"audio_block_profile"`
}

type OutputSpecV2 struct {
	ReferenceProfile    string                 `json:"reference_profile"`
	Format              string                 `json:"format"`
	Width               uint32                 `json:"width"`
	Height              uint32                 `json:"height"`
	PixelFormat         string                 `json:"pixel_format"`
	ColorSpace          string                 `json:"color_space"`
	VideoCodec          string                 `json:"video_codec"`
	AudioCodec          string                 `json:"audio_codec"`
	FrameClock          videoedit.FrameClock   `json:"frame_clock"`
	SampleClock         videoedit.SampleClock  `json:"sample_clock"`
	SampleRate          int64                  `json:"sample_rate"`
	Channels            uint32                 `json:"channels"`
	Intermediate        IntermediateProfilesV2 `json:"intermediate"`
	DecodedVideoProfile string                 `json:"decoded_video_profile"`
	DecodedAudioProfile string                 `json:"decoded_audio_profile"`
}

type ExecutionManifestV2 struct {
	SchemaVersion        uint32                   `json:"schema_version"`
	Profile              string                   `json:"profile"`
	CanonicalJSONProfile string                   `json:"canonical_json_profile"`
	PlanFingerprint      string                   `json:"plan_fingerprint"`
	SemanticProfile      string                   `json:"semantic_profile"`
	DocumentHash         string                   `json:"document_hash"`
	CompilerBuild        string                   `json:"compiler_build"`
	EvaluatorBuild       string                   `json:"evaluator_build"`
	ProviderBuild        string                   `json:"provider_build"`
	Resources            []MaterializedResourceV2 `json:"resources"`
	Backend              BackendCapabilitiesV2    `json:"backend"`
	MediaRuntime         MediaRuntime             `json:"media_runtime"`
	Output               OutputSpecV2             `json:"output"`
	Visual               ProgramSelectionV2       `json:"visual"`
	Audio                ProgramSelectionV2       `json:"audio"`
	DeterministicSeed    int64                    `json:"deterministic_seed"`
	Fingerprint          string                   `json:"fingerprint"`
}

type RenderRequestV2 struct {
	SchemaVersion        uint32              `json:"schema_version"`
	Profile              string              `json:"profile"`
	CanonicalJSONProfile string              `json:"canonical_json_profile"`
	Job                  RenderJob           `json:"job"`
	Plan                 renderplanv2.Plan   `json:"plan"`
	Manifest             ExecutionManifestV2 `json:"execution_manifest"`
}

// RenderBackendV2 receives owned verified identities, not authorization. Hosts must
// separately authorize locators, executable builds and final publication paths.
type RenderBackendV2 interface {
	ID() string
	Capabilities(context.Context) (BackendCapabilitiesV2, error)
	Preflight(context.Context, renderplanv2.VerifiedPlan, VerifiedExecutionManifestV2) []videoedit.Diagnostic
	Render(context.Context, VerifiedRenderRequestV2, EventSink) (RenderArtifact, error)
}
