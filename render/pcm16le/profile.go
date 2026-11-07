package pcm16le

import (
	"github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.audio.quantize.pcm-s16le-stereo-ties-even-no-dither.v1"

	Channels       = 2
	BytesPerSample = 2
	BytesPerFrame  = Channels * BytesPerSample

	DefaultMaxFrames      uint64 = 1_000_000
	DefaultMaxOutputBytes uint64 = DefaultMaxFrames * BytesPerFrame
	DefaultMaxExactBits          = 4096
	HardMaxFrames         uint64 = 1 << 30
	HardMaxOutputBytes    uint64 = 1 << 32
	HardMaxExactBits             = 1 << 20
)

type Limits struct {
	MaxFrames      uint64 `json:"max_frames"`
	MaxOutputBytes uint64 `json:"max_output_bytes"`
	MaxExactBits   int    `json:"max_exact_bits"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxFrames: DefaultMaxFrames, MaxOutputBytes: DefaultMaxOutputBytes,
		MaxExactBits: DefaultMaxExactBits,
	}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	InputSampleProfile       string `json:"input_sample_profile"`
	OutputSampleProfile      string `json:"output_sample_profile"`
	ChannelSemantics         string `json:"channel_semantics"`
	ClippingSemantics        string `json:"clipping_semantics"`
	ScalingSemantics         string `json:"scaling_semantics"`
	RoundingSemantics        string `json:"rounding_semantics"`
	ByteSemantics            string `json:"byte_semantics"`
	HashSemantics            string `json:"hash_semantics"`
	PlacementSemantics       string `json:"placement_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	InputSampleProfile:       audiograph.SampleDomainProfileV1,
	OutputSampleProfile:      renderapi.DecodedAudioProfilePCM16LEV1,
	ChannelSemantics:         "Every input frame is exact stereo in [left,right] order. Output is exactly two channels in interleaved [FL,FR] order; no channel inference, rematrixing or mono fold-down occurs.",
	ClippingSemantics:        "Independently clamp each exact input channel to the closed nominal-full-scale interval [-1,1] before integer conversion. This is the only saturation in the profile and is not a dynamics processor or true-peak limiter.",
	ScalingSemantics:         "Multiply each clamped exact rational by 32768. Negative full scale maps to -32768; positive full scale rounds to 32768 and is then saturated to the largest signed 16-bit value 32767.",
	RoundingSemantics:        "Round the complete exact scaled rational once to the nearest integer; an exact half chooses the even integer. Apply no float conversion, intermediate rounding, noise shaping or dither.",
	ByteSemantics:            "Serialize each signed sample as its two's-complement uint16 bit pattern in little-endian order, frame-major with left then right, with no header, padding or trailing bytes.",
	HashSemantics:            "The canonical hash is SHA-256 over only the complete interleaved PCM bytes. The containing result/evidence manifest must separately bind profile, sample rate, first composition sample and frame count.",
	PlacementSemantics:       "Sample rate must be positive. Negative first composition samples are valid, frame count is non-negative, and the half-open composition sample range [first,first+frame_count) must have an int64-representable exclusive end.",
	CancellationSemantics:    "Validate context before work; check cancellation at bounded intervals while validating and converting samples, while hashing output bytes and immediately before publication. Failure returns no Result or partial PCM block.",
	AllocationSemantics:      "Preflight frame count, composition range, platform length and exactly frame_count*4 output bytes before allocation. Quantization then validates each exact value while writing a private buffer; invalid input discards that buffer. Caller-owned exact samples are excluded. Arithmetic reuses one fixed-count big.Int scratch set bounded by max_exact_bits. Exact's public accessors also create bounded defensive numerator and denominator copies per channel; these are transient and never retained. Output owns one complete byte buffer; Bytes returns a copy and WriteCanonical passes only fixed 64 KiB defensive scratch chunks to external writers, so no accessor exposes the mutable backing storage.",
	DefaultLimits:            DefaultLimits(),
	ExcludedResponsibilities: "Source decode, sample-rate conversion, retime, channel mapping, gain, pan, bus mixing, limiter behavior, loudness, true-peak processing, WAV/container headers, codec encoding, encoder delay, priming and publication are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
