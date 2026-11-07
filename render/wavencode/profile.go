package wavencode

import (
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/pcm16le"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.encode.wav-pcm-s16le-stereo.v1"
	ArtifactHashProfile  = wavdecode.ContentHashProfileV1

	HeaderBytes = 44

	DefaultMaxDataBytes     uint64 = 512 << 20
	DefaultMaxArtifactBytes uint64 = DefaultMaxDataBytes + HeaderBytes
	HardMaxDataBytes        uint64 = (1 << 32) - 1 - 36
	HardMaxArtifactBytes    uint64 = (1 << 32) + 7
	HardMaxSampleRate       uint32 = wavdecode.HardMaxSampleRate
)

type Limits struct {
	MaxDataBytes     uint64 `json:"max_data_bytes"`
	MaxArtifactBytes uint64 `json:"max_artifact_bytes"`
}

func DefaultLimits() Limits {
	return Limits{MaxDataBytes: DefaultMaxDataBytes, MaxArtifactBytes: DefaultMaxArtifactBytes}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	InputQuantizationProfile string `json:"input_quantization_profile"`
	InputSampleProfile       string `json:"input_sample_profile"`
	RoundTripDecodeProfile   string `json:"round_trip_decode_profile"`
	RoundTripSemantics       string `json:"round_trip_semantics"`
	ContainerSemantics       string `json:"container_semantics"`
	FormatSemantics          string `json:"format_semantics"`
	PlacementSemantics       string `json:"placement_semantics"`
	HashProfile              string `json:"hash_profile"`
	HashSemantics            string `json:"hash_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	InputQuantizationProfile: pcm16le.ProfileV1,
	InputSampleProfile:       renderapi.DecodedAudioProfilePCM16LEV1,
	RoundTripDecodeProfile:   wavdecode.ProfileV1,
	RoundTripSemantics:       "The byte layout is in the decoder profile's structural subset. Round-trip Decode also requires the artifact and decoded PCM to fit wavdecode's independently configured encoded/working limits; a valid near-4-GiB RIFF artifact may therefore exceed that decoder's hard resource policy.",
	ContainerSemantics:       "Emit exactly one little-endian RIFF envelope with form WAVE, one 16-byte fmt chunk and one terminal data chunk. The artifact is exactly 44+data_bytes with no pad, JUNK, LIST, fact, bext, cue, metadata, unknown chunk or trailing byte. RIFF and data sizes are exact uint32 values.",
	FormatSemantics:          "fmt declares WAVE_FORMAT_PCM code 1, two channels, the bound positive integer sample rate no greater than the shared strict-decoder hard maximum, byte_rate=sample_rate*4, block_align=4 and bits_per_sample=16. data is the input frame-major interleaved [FL,FR] signed two's-complement PCM16LE byte stream unchanged.",
	PlacementSemantics:       "WAV carries no composition origin. Result separately preserves first_composition_sample, frame_count and sample_rate; an enclosing evidence manifest must bind them to the render request.",
	HashProfile:              ArtifactHashProfile,
	HashSemantics:            "ArtifactHash is SHA-256 over every encoded WAV byte including its exact header and PCM payload. SourcePCMHash is independently recomputed from the immutable input block before encoding; neither hash alone binds composition placement.",
	CancellationSemantics:    "Validate context and all PCM metadata/hash fields before allocation; check cancellation while copying PCM, while hashing the complete artifact and immediately before publication. Failure returns no Result or partial Artifact.",
	AllocationSemantics:      "Preflight RIFF uint32 sizes, platform length and configured data/artifact byte limits before allocating exactly one complete artifact buffer. Caller-owned PCM is excluded. The immutable input writes directly into the artifact payload without a second frame-sized copy. Bytes returns a caller-owned full copy; WriteTo uses a fixed-size defensive scratch buffer so writers never receive artifact-owned memory. Both occur after encoding and are outside the Encode working budget.",
	DefaultLimits:            DefaultLimits(),
	FailureSemantics:         "Any context, profile, placement, geometry, hash, size, allocation or writer mismatch returns a zero Result. Encoding never repairs metadata, truncates PCM or publishes a partial artifact.",
	ExcludedResponsibilities: "AudioGraph evaluation, resampling, retime, gain/mix/limiting, PCM quantization/dither, channel remapping, non-stereo or non-16-bit WAVE, RF64/Wave64, codec encoding, metadata, filesystem publication and signing are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
