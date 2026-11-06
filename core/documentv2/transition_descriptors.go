package documentv2

import "github.com/CutlyLab/Cutly/core/videoedit"

const EvaluatorDissolveV1 = "cutly.evaluator.transition-dissolve.v1"

// TransitionDescriptor makes the transition profile discoverable without requiring an agent or
// backend to reverse-engineer validator code. RequiredCapability applies only when at least one
// window duration is non-zero; the zero-duration rule is an exact hard cut.
type TransitionDescriptor struct {
	Profile               string                 `json:"profile"`
	EvaluatorProfile      string                 `json:"evaluator_profile"`
	RequiredCapability    videoedit.CapabilityID `json:"required_capability"`
	WindowSemantics       string                 `json:"window_semantics"`
	ProgressSemantics     string                 `json:"progress_semantics"`
	CompositingSemantics  string                 `json:"compositing_semantics"`
	ZeroDurationSemantics string                 `json:"zero_duration_semantics"`
	AgentDescription      string                 `json:"agent_description"`
}

var transitionDescriptorRegistry = []TransitionDescriptor{{
	Profile: TransitionDissolveV1, EvaluatorProfile: EvaluatorDissolveV1,
	RequiredCapability:    videoedit.CapabilityID(TransitionDissolveV1),
	WindowSemantics:       "For cut time C, evaluate the half-open window [C-pre_duration,C+post_duration). Temporal incoming video before C uses source head handle and temporal outgoing video at/after C uses source tail handle; a static image is held as its immutable frame on either side and needs no temporal handle. The transition never changes timeline duration or clip positions.",
	ProgressSemantics:     "For non-zero total window W=pre_duration+post_duration and timeline time t, progress=(t-(C-pre_duration))/W. At window start progress is 0; at its half-open end the transition has ended and incoming alone is visible.",
	CompositingSemantics:  "In linear-light premultiplied-alpha space, outgoing weight is 1-progress and incoming weight is progress.",
	ZeroDurationSemantics: "pre_duration=0 and post_duration=0 is exactly a hard cut, requires no transition capability, and may be removed by v1 down-conversion.",
	AgentDescription:      "Cross-dissolve adjacent video/image clips using explicit asymmetric windows; missing temporal-video handles fail preflight and are never synthesized by repeating a video edge frame.",
}}

// TransitionDescriptors returns a defensive copy sorted by profile.
func TransitionDescriptors() []TransitionDescriptor {
	return append([]TransitionDescriptor(nil), transitionDescriptorRegistry...)
}

// LookupTransitionDescriptor returns the frozen contract for a transition profile.
func LookupTransitionDescriptor(profile string) (TransitionDescriptor, bool) {
	for _, descriptor := range transitionDescriptorRegistry {
		if descriptor.Profile == profile {
			return descriptor, true
		}
	}
	return TransitionDescriptor{}, false
}
