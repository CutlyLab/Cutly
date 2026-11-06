package audiograph

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const ProfileDescriptorSchemaVersion = 1

// ProfileDescriptor is the machine-readable executable boundary of the v1 reference AudioGraph.
// Its canonical fixture is part of conformance: changing any pixel/PCM-affecting statement requires
// a new versioned profile rather than silently changing this value.
type ProfileDescriptor struct {
	SchemaVersion             int                      `json:"schema_version"`
	Profile                   string                   `json:"profile"`
	EvaluatorProfile          string                   `json:"evaluator_profile"`
	SampleDomainProfile       string                   `json:"sample_domain_profile"`
	SourceContractVersion     int                      `json:"source_contract_version"`
	SourceProfile             string                   `json:"source_profile"`
	SourceSampleDomainProfile string                   `json:"source_sample_domain_profile"`
	ExplicitGraphCapabilities []videoedit.CapabilityID `json:"explicit_graph_capabilities"`
	OptionalStageCapabilities []videoedit.CapabilityID `json:"optional_stage_capabilities"`
	PipelineOrder             []string                 `json:"pipeline_order"`
	ActivationSemantics       string                   `json:"activation_semantics"`
	SourceProviderBoundary    string                   `json:"source_provider_boundary"`
	SourceSampleDomain        string                   `json:"source_sample_domain"`
	ChannelBindingSemantics   string                   `json:"channel_binding_semantics"`
	PanSemantics              string                   `json:"pan_semantics"`
	DefaultLimits             Limits                   `json:"default_limits"`
	SummationSemantics        string                   `json:"summation_semantics"`
	LimiterSemantics          string                   `json:"limiter_semantics"`
	RoundingSemantics         string                   `json:"rounding_semantics"`
	CancellationSemantics     string                   `json:"cancellation_semantics"`
	LimitSemantics            string                   `json:"limit_semantics"`
	FailureSemantics          string                   `json:"failure_semantics"`
	ExcludedResponsibilities  []string                 `json:"excluded_responsibilities"`
}

var profileDescriptorV1 = ProfileDescriptor{
	SchemaVersion:             ProfileDescriptorSchemaVersion,
	Profile:                   ProfileV1,
	EvaluatorProfile:          EvaluatorProfileV1,
	SampleDomainProfile:       SampleDomainProfileV1,
	SourceContractVersion:     SourceContractVersion,
	SourceProfile:             SourceProfileV1,
	SourceSampleDomainProfile: SourceSampleDomainProfileV1,
	ExplicitGraphCapabilities: []videoedit.CapabilityID{
		documentv2.CapabilityAudioGainV1,
		documentv2.CapabilityAudioPanV1,
		documentv2.CapabilityAudioRouteV1,
	},
	OptionalStageCapabilities: []videoedit.CapabilityID{
		documentv2.CapabilityAudioChannelMatrixV1,
		documentv2.CapabilityAudioSamplePeakLimiterV1,
	},
	PipelineOrder: []string{
		"source_provider",
		"explicit_channel_bind_or_matrix",
		"clip_gain",
		"clip_linear_balance_pan",
		"track_route",
		"bus_direct_track_sum",
		"bus_child_sum",
		"bus_gain",
		"bus_linear_balance_pan",
		"master_sample_peak_limiter_optional",
		"exact_stereo_output",
	},
	ActivationSemantics:     "An empty Document.AudioBuses array is the legacy implicit direct-master graph and requires no route or bus gain/pan capability. Any non-empty array activates the explicit single-parent graph and requires gain, pan, and route capabilities even for identity values.",
	SourceProviderBoundary:  "For every active clip, issue one versioned request binding the frozen canonical Document hash; complete timeline/track/clip/asset IDs and indexes; asset kind; exact timeline/local/source times; selected immutable ProbeManifest fingerprint, resource hash/size and stream; decoder channel tokens; and binding-frozen decode, selection and retime profiles. The provider must exactly echo the request plus the ordered-channel source domain. The graph validates every field against a provider-isolated request copy and retains a defensive SourceReceipt before DSP. A receipt records validated claims and exact returned channels; it is not a signature or proof that an untrusted provider actually followed them.",
	SourceSampleDomain:      "Provider input is cutly.audio.sample.exact-rational-ordered-channels.v1 in the exact DecoderChannels order and may contain one, two or an explicitly matrix-bound larger channel set. Graph output is cutly.audio.sample.exact-rational-stereo.v1. All values are bounded arbitrary-precision rationals in nominal full-scale units; values outside [-1,1] are valid and no implicit clamp, normalization, saturation or fallback is applied.",
	ChannelBindingSemantics: "Without a matrix, only exact mono layout with decoder order [FC] or exact stereo layout with [FL,FR] is accepted; mono duplicates to stereo. An authored matrix must exactly match ProbeManifest channel_layout, channel count, and decoder token order, and emits exact stereo sums before clip gain and pan.",
	PanSemantics:            "For exact pan p in [-1,1], left gain is min(1,1-p) and right gain is min(1,1+p). Stereo channels are balanced independently with no crossfeed; the same law applies after each bus gain.",
	DefaultLimits:           DefaultLimits(),
	SummationSemantics:      "For each bus, direct active track signals are summed in selected-timeline track-array order, then child-bus outputs are summed in Document.AudioBuses order. Exact arithmetic does not authorize an implementation to reorder the evidence trace.",
	LimiterSemantics:        "Only an explicitly authored master cutly.audio.limiter.sample-peak-instant.v1 stage runs, after master gain and pan. It is exact, stereo-linked, instantaneous, stateless, zero-lookahead, zero-latency sample-peak limiting; omission means no limiter.",
	RoundingSemantics:       "Matrix, gain, pan, summation, and limiter arithmetic perform no rounding or PCM quantization. Output remains exact rational stereo until a separately versioned output profile.",
	CancellationSemantics:   "Every public evaluation requires context.Context. Cancellation is checked before and after each provider call, between block samples, and during bus summation; cancellation returns AUDIOGRAPH.V1.CANCELED and no partial Evaluation or BlockEvaluation.",
	LimitSemantics:          "Before canonicalization or evaluator compilation, conservatively bound the complete typed Document graph by nodes/encoded bytes, all tracks/clips, and each ProbeManifest by streams/nodes/encoded bytes; exact canonical Document/manifest bytes are checked again before decode. Compile also bounds bindings, timelines, audio tracks and buses. Runtime bounds block sample count, platform allocation size, decoder channels, and numerator-plus-denominator bit length at source and every DSP/summation stage boundary; excess returns a stable limit error.",
	FailureSemantics:        "Compile returns no Program after any schema, capability, media-binding, source-profile, extension-consumer, or limit error. Runtime rejects provider request/domain/profile/resource/stream/scope mismatches and returns no partial success after provider, sample, time, cancellation, or DSP failure. Block output omits per-sample receipts to keep memory bounded but every sample passed the same strict response validation path.",
	ExcludedResponsibilities: []string{
		"media_decode",
		"sample_rate_conversion_or_retime_algorithm",
		"encoder_delay_aac_priming_and_padding",
		"pcm_quantization_clipping_and_dither",
		"codec_encode_and_container_mux",
		"true_peak_loudness_and_dynamics_processing",
		"provider_runtime_attestation_and_resource_authorization",
	},
}

// Descriptor returns a defensive copy of the v1 profile descriptor.
func Descriptor() ProfileDescriptor {
	result := profileDescriptorV1
	result.ExplicitGraphCapabilities = append([]videoedit.CapabilityID(nil), profileDescriptorV1.ExplicitGraphCapabilities...)
	result.OptionalStageCapabilities = append([]videoedit.CapabilityID(nil), profileDescriptorV1.OptionalStageCapabilities...)
	result.PipelineOrder = append([]string(nil), profileDescriptorV1.PipelineOrder...)
	result.ExcludedResponsibilities = append([]string(nil), profileDescriptorV1.ExcludedResponsibilities...)
	return result
}
