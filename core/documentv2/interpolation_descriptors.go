package documentv2

import "github.com/CutlyLab/Cutly/core/videoedit"

// InterpolationNumericProfileV1 is implied by every Document v2 keyframe lane. It deliberately
// does not inherit CSS, browser or backend floating-point easing behavior.
const InterpolationNumericProfileV1 = "cutly.evaluator.interpolation-numeric.v1"

type InterpolationDescriptor struct {
	Interpolation      Interpolation          `json:"interpolation"`
	Evaluator          string                 `json:"evaluator_profile"`
	RequiredCapability videoedit.CapabilityID `json:"required_capability"`
	ValueTypes         []KeyframeValueType    `json:"value_types"`
	ValueSemantics     string                 `json:"value_semantics"`
}

var interpolationDescriptorRegistry = []InterpolationDescriptor{
	{
		Interpolation: InterpolationCubic, Evaluator: InterpolationNumericProfileV1,
		RequiredCapability: CapabilityInterpolationCubicV1,
		ValueTypes:         []KeyframeValueType{KeyframeValuePoint, KeyframeValueRect, KeyframeValueScalar},
		ValueSemantics:     "Normalize segment time p exactly. For 0<p<1, first test the largest inner Q0.32 point k=2^32-1; if x(k/2^32)<p use u=1, otherwise use exactly 32 integer lower-bound steps on k in [0,2^32-1] to choose the smallest x(k/2^32)>=p. Evaluate x/y with exact rationals, then apply exact component-wise a+(b-a)*y. p<=0 and p>=1 return exact endpoints. No CSS or floating-point solver is permitted.",
	},
	{
		Interpolation: InterpolationHold, Evaluator: InterpolationNumericProfileV1,
		RequiredCapability: CapabilityInterpolationHoldV1,
		ValueTypes:         []KeyframeValueType{KeyframeValueColor, KeyframeValuePoint, KeyframeValueRect, KeyframeValueScalar},
		ValueSemantics:     "Return the left keyframe value throughout its half-open segment; at the right keyframe offset that keyframe value wins. This is the only Document v2 interpolation for color.",
	},
	{
		Interpolation: InterpolationLinear, Evaluator: InterpolationNumericProfileV1,
		RequiredCapability: CapabilityInterpolationLinearV1,
		ValueTypes:         []KeyframeValueType{KeyframeValuePoint, KeyframeValueRect, KeyframeValueScalar},
		ValueSemantics:     "Normalize segment time p exactly and evaluate every rational component as a+(b-a)*p with arbitrary-precision rational intermediates. No intermediate rounding is permitted.",
	},
}

// InterpolationDescriptors returns the canonical interpolation-name-sorted evaluator contract.
func InterpolationDescriptors() []InterpolationDescriptor {
	result := make([]InterpolationDescriptor, len(interpolationDescriptorRegistry))
	for index, descriptor := range interpolationDescriptorRegistry {
		result[index] = descriptor
		result[index].ValueTypes = append([]KeyframeValueType(nil), descriptor.ValueTypes...)
	}
	return result
}

// LookupInterpolationDescriptor returns a defensive copy of one interpolation contract.
func LookupInterpolationDescriptor(interpolation Interpolation) (InterpolationDescriptor, bool) {
	for _, descriptor := range interpolationDescriptorRegistry {
		if descriptor.Interpolation == interpolation {
			result := descriptor
			result.ValueTypes = append([]KeyframeValueType(nil), descriptor.ValueTypes...)
			return result, true
		}
	}
	return InterpolationDescriptor{}, false
}
