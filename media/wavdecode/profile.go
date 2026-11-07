package wavdecode

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.decode.wav-pcm-s16le.v1"
	SampleDomainProfileV1 = "cutly.audio.sample.signed-pcm16-interleaved.v1"
	ContentHashProfileV1  = "cutly.hash.sha256.encoded-wav-bytes.v1"
	SampleHashProfileV1   = "cutly.hash.sha256.pcm-s16be-interleaved.v1"
	// NominalFullScaleDenominator is the exact divisor used when a decoded int16 enters the
	// AudioGraph rational sample domain.
	NominalFullScaleDenominator int64 = 32_768

	DefaultMaxEncodedBytes uint64 = 512 << 20
	DefaultMaxWorkingBytes uint64 = 512 << 20
	HardMaxEncodedBytes    uint64 = 1 << 30
	HardMaxWorkingBytes    uint64 = 1 << 30
	HardMaxSampleRate      uint32 = 768_000
	HardMaxFrames          uint64 = 1 << 40
)

// ChannelOrder is the complete semantic decoder order for a supported interleaved frame.
type ChannelOrder string

const (
	ChannelOrderMonoFC     ChannelOrder = "FC"
	ChannelOrderStereoFLFR ChannelOrder = "FL,FR"
)

// Limits is host allocation/input policy, not a sample semantic. Zero fields select the
// corresponding default. MaxWorkingBytes covers the one owned []int16 sample buffer; caller-owned
// encoded bytes and fixed parser/hash state are excluded.
type Limits struct {
	MaxSampleRate   uint32 `json:"max_sample_rate"`
	MaxFrames       uint64 `json:"max_frames"`
	MaxEncodedBytes uint64 `json:"max_encoded_bytes"`
	MaxWorkingBytes uint64 `json:"max_working_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxSampleRate:   192_000,
		MaxFrames:       2_764_800_000, // Four hours at the default maximum sample rate.
		MaxEncodedBytes: DefaultMaxEncodedBytes,
		MaxWorkingBytes: DefaultMaxWorkingBytes,
	}
}

// ProfileDescriptor freezes the complete accepted container/sample subset and every
// sample-affecting handoff. A semantic change requires a new profile identifier.
type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	ContainerSubset          string `json:"container_subset"`
	ChunkSequence            string `json:"chunk_sequence"`
	FormatSubset             string `json:"format_subset"`
	ChannelSemantics         string `json:"channel_semantics"`
	SampleDecode             string `json:"sample_decode"`
	SampleDomainProfile      string `json:"sample_domain_profile"`
	ContentHashProfile       string `json:"content_hash_profile"`
	SampleHashProfile        string `json:"sample_hash_profile"`
	SampleHashSemantics      string `json:"sample_hash_semantics"`
	EncodedInputSemantics    string `json:"encoded_input_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	ContainerSubset:          "Little-endian RIFF with form type WAVE and a declared RIFF size exactly equal to the complete input length minus eight bytes. RF64, RIFX, Wave64 and every non-RIFF envelope are rejected.",
	ChunkSequence:            "Exactly one 16-byte fmt chunk followed immediately by exactly one terminal data chunk. No JUNK, LIST, fact, bext, cue, padding chunk, metadata, duplicate, unknown chunk, hidden chunk or trailing byte is accepted. Every chunk length and required RIFF even-byte padding boundary is checked; accepted fmt and data sizes are even and therefore carry no pad byte.",
	FormatSubset:             "WAVE_FORMAT_PCM code 1 only; one or two channels; positive integer sample rate; signed 16-bit little-endian samples; block_align must equal channels*2; byte_rate must equal sample_rate*block_align; data length must be an exact whole number of frames. WAVE_FORMAT_IEEE_FLOAT and WAVE_FORMAT_EXTENSIBLE are rejected.",
	ChannelSemantics:         "One channel is bound to decoder order [FC] and layout mono. Two channels are bound to decoder order [FL,FR] and layout stereo. No channel mask, inference, reorder, downmix or upmix is performed.",
	SampleDecode:             "Decode every data word as a two's-complement signed little-endian int16 in frame-major interleaved channel order. The exact nominal-full-scale rational value for downstream audio evaluation is int16/32768, so -32768 is -1 and 32767 is 32767/32768. Decoding performs no clipping, normalization, gain, dither or rounding.",
	SampleDomainProfile:      SampleDomainProfileV1,
	ContentHashProfile:       ContentHashProfileV1,
	SampleHashProfile:        SampleHashProfileV1,
	SampleHashSemantics:      "Serialize decoded samples only, in frame-major interleaved channel order, as two-byte two's-complement signed words with the most significant byte first and no header; SHA-256 the complete byte stream and prefix lowercase hex with sha256:. Sample rate, channel count and channel order are bound separately by Result.",
	EncodedInputSemantics:    "Decode borrows one complete encoded byte slice only for the call. The caller must keep it immutable until return; immutable input may be shared by concurrent calls. Decode retains no alias, and Result binds every encoded byte with SHA-256.",
	CancellationSemantics:    "Check context before validation, during complete-input hashing, before allocation, at bounded intervals while decoding and canonical sample hashing, and immediately before publication. Cancellation returns no partial Result or PCM buffer.",
	AllocationSemantics:      "Before allocation require data_chunk_bytes <= MaxWorkingBytes, frame_count <= MaxFrames, sample count representable as int, and the complete input within MaxEncodedBytes. The sole frame-sized allocation is the final owned []int16 sample buffer; caller-owned encoded bytes and fixed parser/hash state are excluded.",
	DefaultLimits:            DefaultLimits(),
	FailureSemantics:         "Every structural, profile, arithmetic, limit, allocation or cancellation failure returns a zero Result. There is no permissive repair, chunk skipping, alternate decoder or partial sample publication.",
	ExcludedResponsibilities: "Remote fetch, trust/asset authorization, RF64, metadata, more than two channels, channel masks, unsigned PCM, 8/24/32-bit PCM, float PCM, compressed codecs, sample-rate conversion, retime, encoder delay, loudness, mixing, scheduler-wide concurrent memory admission, output quantization, encoding and muxing are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
