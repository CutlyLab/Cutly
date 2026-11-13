package documentv2

import "github.com/CutlyLab/Cutly/core/videoedit"

const (
	EvaluatorDissolveV1 = "cutly.evaluator.transition-dissolve.v1"
	EvaluatorWipeV1     = "cutly.evaluator.transition-wipe.v1"
	EvaluatorFadeV1     = "cutly.evaluator.transition-fade-through-black.v1"
)

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

const transitionWindowSemantics = "For cut time C, evaluate the half-open window [C-pre_duration,C+post_duration). Temporal incoming video before C uses source head handle and temporal outgoing video at/after C uses source tail handle; a static image is held as its immutable frame on either side and needs no temporal handle. The transition never changes timeline duration or clip positions."
const transitionProgressSemantics = "For non-zero total window W=pre_duration+post_duration and timeline time t, progress=(t-(C-pre_duration))/W. At window start progress is 0; at its half-open end the transition has ended and incoming alone is visible."
const transitionHardCutSemantics = "pre_duration=0 and post_duration=0 is exactly a hard cut, requires no transition capability, and may be removed by v1 down-conversion."

var transitionDescriptorRegistry = []TransitionDescriptor{
	{
		Profile: TransitionDissolveV1, EvaluatorProfile: EvaluatorDissolveV1,
		RequiredCapability:    videoedit.CapabilityID(TransitionDissolveV1),
		WindowSemantics:       transitionWindowSemantics,
		ProgressSemantics:     transitionProgressSemantics,
		CompositingSemantics:  "In linear-light premultiplied-alpha space, outgoing weight is 1-progress and incoming weight is progress.",
		ZeroDurationSemantics: transitionHardCutSemantics,
		AgentDescription:      "Cross-dissolve adjacent video/image clips using explicit asymmetric windows; missing temporal-video handles fail preflight and are never synthesized by repeating a video edge frame.",
	},
}

// transitionDescriptorRegistryV2 extends the frozen v1 descriptor list. Keeping the base list
// byte-stable preserves SemanticDescriptors v1-v4 while newer callers can discover every
// transition accepted by Document v2 through TransitionDescriptorsV2.
var transitionDescriptorRegistryV2 = append(append([]TransitionDescriptor(nil), transitionDescriptorRegistry...),
	TransitionDescriptor{
		Profile: TransitionFadeV1, EvaluatorProfile: EvaluatorFadeV1,
		RequiredCapability:    videoedit.CapabilityID(TransitionFadeV1),
		WindowSemantics:       transitionWindowSemantics,
		ProgressSemantics:     transitionProgressSemantics,
		CompositingSemantics:  "In linear-light premultiplied-alpha space, progress 0..1/2 fades outgoing to an opaque black matte and progress 1/2..1 fades the matte into incoming; progress 1/2 is exactly opaque black.",
		ZeroDurationSemantics: transitionHardCutSemantics,
		AgentDescription:      "Fade adjacent video/image clips through a deterministic opaque black midpoint for scene and chapter boundaries.",
	},
	TransitionDescriptor{
		Profile: TransitionWipeV1, EvaluatorProfile: EvaluatorWipeV1,
		RequiredCapability:    videoedit.CapabilityID(TransitionWipeV1),
		WindowSemantics:       transitionWindowSemantics,
		ProgressSemantics:     transitionProgressSemantics,
		CompositingSemantics:  "A hard vertical boundary advances left-to-right by floor(width*progress) pixels; pixels left of the boundary are incoming and pixels at or right of it are outgoing.",
		ZeroDurationSemantics: transitionHardCutSemantics,
		AgentDescription:      "Reveal an incoming video/image clip from the left with an exact deterministic pixel boundary.",
	},
)

// TransitionDescriptors returns a defensive copy sorted by profile.
func TransitionDescriptors() []TransitionDescriptor {
	return append([]TransitionDescriptor(nil), transitionDescriptorRegistry...)
}

// TransitionDescriptorsV2 returns the complete transition registry, including the additive fade
// and wipe profiles, in canonical profile order.
func TransitionDescriptorsV2() []TransitionDescriptor {
	return append([]TransitionDescriptor(nil), transitionDescriptorRegistryV2...)
}

// LookupTransitionDescriptor returns the frozen contract for a transition profile.
func LookupTransitionDescriptor(profile string) (TransitionDescriptor, bool) {
	for _, descriptor := range transitionDescriptorRegistryV2 {
		if descriptor.Profile == profile {
			return descriptor, true
		}
	}
	return TransitionDescriptor{}, false
}
