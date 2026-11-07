package documentv2

import "github.com/CutlyLab/Cutly/core/videoedit"

const (
	AudioGraphProfileV1   = "cutly.audio.graph.v1"
	EvaluatorAudioGraphV1 = "cutly.evaluator.audio-graph.v1"
)

// AudioGraphDescriptor freezes the executable graph contract in one discoverable entry. The
// individual gain, pan and route fields remain discoverable through PropertyDescriptors; this
// descriptor defines how those fields compose into a graph.
type AudioGraphDescriptor struct {
	Profile                     string                   `json:"profile"`
	EvaluatorProfile            string                   `json:"evaluator_profile"`
	ExplicitGraphCapabilities   []videoedit.CapabilityID `json:"explicit_graph_capabilities"`
	ActivationSemantics         string                   `json:"activation_semantics"`
	TopologySemantics           string                   `json:"topology_semantics"`
	RoutingSemantics            string                   `json:"routing_semantics"`
	SummationSemantics          string                   `json:"summation_semantics"`
	EvaluationOrder             string                   `json:"evaluation_order"`
	PanSemantics                string                   `json:"pan_semantics"`
	OptionalProcessingSemantics string                   `json:"optional_processing_semantics"`
	AgentDescription            string                   `json:"agent_description"`
}

var audioGraphDescriptorRegistry = []AudioGraphDescriptor{{
	Profile: AudioGraphProfileV1, EvaluatorProfile: EvaluatorAudioGraphV1,
	ExplicitGraphCapabilities: []videoedit.CapabilityID{
		CapabilityAudioGainV1, CapabilityAudioPanV1, CapabilityAudioRouteV1,
	},
	ActivationSemantics:         "When Document.AudioBuses is empty, use the legacy implicit direct-master graph: every audio track has empty audio_bus_id and no route/gain/pan graph capability is required. Any non-empty AudioBuses array activates the explicit graph and requires all listed capabilities, even when authored gain/pan values are identity.",
	TopologySemantics:           "An explicit graph contains exactly one master bus. Every group bus has exactly one existing parent and every parent chain terminates at the master; cycles, orphan groups, implicit buses and sends are forbidden.",
	RoutingSemantics:            "Every track has at most one route. In an explicit graph each audio track routes to exactly one existing bus; non-audio tracks cannot route. Empty audio_bus_id is forbidden once the explicit graph is active.",
	SummationSemantics:          "Rendering selects exactly one timeline. At each bus, sum its direct track signals in that selected timeline's track-array order, followed by child-bus signals in Document.AudioBuses order. The declared order is the stable tie-breaker; host map iteration, graph traversal order and backend optimization cannot reorder the semantic mix.",
	EvaluationOrder:             "For each clip evaluate source samples, clip gain, clip pan and its track route. For each bus evaluate the stable input sum, bus gain, bus pan and then its single parent edge. The master emits after its own gain and pan. This profile adds no hidden normalization, limiter, saturation or fallback route.",
	PanSemantics:                "For exact pan p in [-1,1], left gain=min(1,1-p) and right gain=min(1,1+p). Mono duplicates to L/R before gains; stereo balances channels independently with no crossfeed; other layouts fail preflight unless an explicit channel-matrix capability first produces stereo. Equal-power and surround pan require a different versioned profile.",
	OptionalProcessingSemantics: "The base graph capabilities never imply extra processing. An authored cutly.audio.channel-matrix.stereo.v1 stage runs after source decode/retime and before clip gain/pan. An authored cutly.audio.limiter.sample-peak-instant.v1 stage is valid only on the master and runs after master gain/pan. Their independent capabilities, evaluator profiles, media preflight, state/latency and rounding boundaries are defined by their PropertyDescriptors; omission means the stages do not exist.",
	AgentDescription:            "Deterministic single-parent audio mix graph with explicit route, gain and linear-balance pan semantics; use another profile for sends, effects, surround or equal-power pan.",
}}

// AudioGraphDescriptors returns a profile-sorted defensive copy.
func AudioGraphDescriptors() []AudioGraphDescriptor {
	result := make([]AudioGraphDescriptor, len(audioGraphDescriptorRegistry))
	for index, descriptor := range audioGraphDescriptorRegistry {
		result[index] = cloneAudioGraphDescriptor(descriptor)
	}
	return result
}

// LookupAudioGraphDescriptor returns a defensive copy of the requested graph contract.
func LookupAudioGraphDescriptor(profile string) (AudioGraphDescriptor, bool) {
	for _, descriptor := range audioGraphDescriptorRegistry {
		if descriptor.Profile == profile {
			return cloneAudioGraphDescriptor(descriptor), true
		}
	}
	return AudioGraphDescriptor{}, false
}

func cloneAudioGraphDescriptor(value AudioGraphDescriptor) AudioGraphDescriptor {
	result := value
	result.ExplicitGraphCapabilities = append([]videoedit.CapabilityID(nil), value.ExplicitGraphCapabilities...)
	return result
}
