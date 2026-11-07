package wavsource

import (
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/audiograph"
)

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.audio.source.verified-wav-pcm16-linear.v1"
	SelectionProfileV1    = "cutly.audio.select.pcm16-linear-source-time-terminal-hold.v1"
	RetimeProfileV1       = "cutly.audio.retime.linear-varispeed-pitch-linked.v1"
	ProbeImplementationV1 = "cutly.wavsource.strict-wav-probe.v1"

	DefaultMaxBindings                      = 256
	HardMaxBindings                         = 4096
	DefaultMaxClipScopes                    = 65536
	HardMaxClipScopes                       = 1 << 20
	DefaultMaxTracksPerTimeline             = 65536
	HardMaxTracksPerTimeline                = 1 << 20
	DefaultMaxDocumentNodes                 = 262144
	HardMaxDocumentNodes                    = 4 << 20
	DefaultMaxCanonicalDocumentBytes uint64 = 128 << 20
	HardMaxCanonicalDocumentBytes    uint64 = 1 << 30
	DefaultMaxTotalEncodedBytes      uint64 = 512 << 20
	HardMaxTotalEncodedBytes         uint64 = 2 << 30
	DefaultMaxRetainedBytes          uint64 = 512 << 20
	HardMaxRetainedBytes             uint64 = 2 << 30
	DefaultMaxExactBits                     = 4096
	HardMaxExactBits                        = 1 << 20
)

type Limits struct {
	MaxBindings               int              `json:"max_bindings"`
	MaxClipScopes             int              `json:"max_clip_scopes"`
	MaxTracksPerTimeline      int              `json:"max_tracks_per_timeline"`
	MaxDocumentNodes          int              `json:"max_document_nodes"`
	MaxCanonicalDocumentBytes uint64           `json:"max_canonical_document_bytes"`
	MaxTotalEncodedBytes      uint64           `json:"max_total_encoded_bytes"`
	MaxRetainedBytes          uint64           `json:"max_retained_bytes"`
	MaxExactBits              int              `json:"max_exact_bits"`
	WAV                       wavdecode.Limits `json:"wav"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxBindings: DefaultMaxBindings, MaxClipScopes: DefaultMaxClipScopes,
		MaxTracksPerTimeline:      DefaultMaxTracksPerTimeline,
		MaxDocumentNodes:          DefaultMaxDocumentNodes,
		MaxCanonicalDocumentBytes: DefaultMaxCanonicalDocumentBytes,
		MaxTotalEncodedBytes:      DefaultMaxTotalEncodedBytes,
		MaxRetainedBytes:          DefaultMaxRetainedBytes,
		MaxExactBits:              DefaultMaxExactBits, WAV: wavdecode.DefaultLimits(),
	}
}

type Config struct {
	Limits Limits `json:"limits"`
}

func DefaultConfig() Config { return Config{Limits: DefaultLimits()} }

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	AudioGraphSourceProfile  string `json:"audio_graph_source_profile"`
	SourceSampleDomain       string `json:"source_sample_domain"`
	DecodeProfile            string `json:"decode_profile"`
	SelectionProfile         string `json:"selection_profile"`
	RetimeProfile            string `json:"retime_profile"`
	ProbeImplementation      string `json:"probe_implementation"`
	DocumentSemantics        string `json:"document_semantics"`
	BindingSemantics         string `json:"binding_semantics"`
	ProbeSemantics           string `json:"probe_semantics"`
	RequestSemantics         string `json:"request_semantics"`
	SamplingSemantics        string `json:"sampling_semantics"`
	TerminalSemantics        string `json:"terminal_semantics"`
	ChannelSemantics         string `json:"channel_semantics"`
	RetimeSemantics          string `json:"retime_semantics"`
	EvidenceSemantics        string `json:"evidence_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion: ProfileSchemaVersion, Profile: ProfileV1,
	AudioGraphSourceProfile: audiograph.SourceProfileV1,
	SourceSampleDomain:      audiograph.SourceSampleDomainProfileV1,
	DecodeProfile:           wavdecode.ProfileV1, SelectionProfile: SelectionProfileV1,
	RetimeProfile: RetimeProfileV1, ProbeImplementation: ProbeImplementationV1,
	DocumentSemantics:        "Canonical-validate, deep-freeze and hash one complete Document v2. Every enabled clip on an audio track must reference a content-addressed audio asset whose declared duration exactly equals the decoded WAV frame_count/sample_rate; video assets on audio tracks are outside this profile.",
	BindingSemantics:         "Require exactly one caller binding for every distinct referenced audio asset and no other binding. Every binding declares the exact frozen document hash. Each Document content_hash must equal SHA-256 of the complete strict WAV bytes; URI values are never opened or fetched.",
	ProbeSemantics:           "Create one validated ProbeManifest from the strict decoder result: WAVE start zero, stream index zero, pcm_s16le, time base 1/sample_rate, exact frame duration, mono/FC or stereo/FL,FR, and zero encoder delay/padding. Probe BuildFingerprint is the deterministic canonical digest of the source and decoder semantic descriptors, not a binary, toolchain or supply-chain identity; OptionsFingerprint binds the explicit versioned probe options. Neither fingerprint depends on host media-tool defaults.",
	RequestSemantics:         "Accept only the frozen document/scope IDs and indexes, exact independently recomputed timeline/local/source mapping, asset kind/hash/size, manifest fingerprint, stream zero, sample rate/layout/channel order, AudioGraph source/domain profiles and the three binding-frozen decode/selection/retime profiles. The response owns a complete request echo and exact ordered channel samples.",
	SamplingSemantics:        "Let p=SourceTimeSeconds*sample_rate as an exact non-negative rational. At integer p return frame p. Otherwise linearly interpolate each channel between floor(p) and floor(p)+1 with exact rational weights, divide signed PCM16 words by 32768, and perform no rounding, clipping or float conversion.",
	TerminalSemantics:        "Valid source time is half-open [0,frame_count/sample_rate). When floor(p) is the final frame and p is fractional, hold the final frame through the remaining terminal sample cell. Source time at or beyond duration, negative time, empty decoded media, loop, zero padding and extrapolation are rejected.",
	ChannelSemantics:         "Strict mono emits one exact channel in [FC] order; strict stereo emits two in [FL,FR] order. No inference, rematrix, reorder, normalization or gain occurs before AudioGraph.",
	RetimeSemantics:          "The evaluator owns timeline-to-source mapping. Sampling that exact SourceTime with this linear kernel implements varispeed: speed and pitch change together. Pitch-preserving time stretch is explicitly outside this profile.",
	EvidenceSemantics:        "The AssetID-sorted manifest binds document hash, encoded/sample hashes and their serialization profiles, decoded and graph sample domains, geometry in sample units, deterministic ProbeManifest identity/fingerprint and every decode/selection/retime/source profile. Provider echoes and descriptor digests are validated semantic claims, not signatures or proof against a malicious runtime; outer evidence must bind trusted runtime/worker identity.",
	CancellationSemantics:    "Check context before and throughout document preflight and scope indexing, before and after document freezing, throughout canonical document hashing, before each binding decode, throughout strict WAV decode, before/after request validation and exact sampling, and before publishing a complete provider or source response. Failure returns no partial provider or SourceSample.",
	AllocationSemantics:      "Before canonicalization bound the complete typed Document nodes, conservative JSON bytes and tracks per timeline. While indexing enabled audio scopes, reject a new distinct referenced asset before it can exceed MaxBindings. Before decode bound binding count, clip scopes, aggregate caller-provided encoded bytes and the accepted strict-WAV retained PCM total; each decoder also enforces its own work budget. Provider sampling allocates only bounded exact/channel response state.",
	DefaultLimits:            DefaultLimits(),
	ExcludedResponsibilities: "Filesystem/network resource resolution, video-container audio extraction, alternate WAV chunks or sample formats, sinc/polyphase resampling, pitch-preserving time stretch, channel matrix/mix/gain/pan/buses/limiter, PCM output quantization, container encoding, publication, scheduler-wide concurrency and trusted execution attestation are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

func MediaProfiles() (decode, selection, retime string) {
	return wavdecode.ProfileV1, SelectionProfileV1, RetimeProfileV1
}
