package evaluator

import (
	"math/big"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

// compiledLane freezes the validation and exact-value conversion that standalone EvaluateLane
// must perform for hostile input. Program evaluation only binary-searches immutable offsets and
// evaluates the selected segment; no document structure is revalidated per frame.
type compiledLane struct {
	target    documentv2.KeyframeTarget
	valueType documentv2.KeyframeValueType
	keyframes []compiledKeyframe
}

type compiledKeyframe struct {
	offset        *big.Rat
	value         Value
	interpolation documentv2.Interpolation
	easing        *documentv2.CubicBezier
}

func compileLanes(lanes []documentv2.KeyframeLane) ([]compiledLane, error) {
	if len(lanes) == 0 {
		return nil, nil
	}
	result := make([]compiledLane, 0, len(lanes))
	for _, lane := range lanes {
		values, err := validateAndConvertLane(lane)
		if err != nil {
			return nil, err
		}
		compiled := compiledLane{
			target: lane.Target, valueType: lane.ValueType,
			keyframes: make([]compiledKeyframe, len(lane.Keyframes)),
		}
		for index, keyframe := range lane.Keyframes {
			offset, err := timeBig(keyframe.Offset)
			if err != nil {
				return nil, err
			}
			var easing *documentv2.CubicBezier
			if keyframe.Easing != nil {
				copy := *keyframe.Easing
				easing = &copy
			}
			compiled.keyframes[index] = compiledKeyframe{
				offset: offset, value: values[index],
				interpolation: keyframe.Interpolation, easing: easing,
			}
		}
		result = append(result, compiled)
	}
	return result, nil
}

func (lane compiledLane) evaluate(localTime documentv2.RationalTime) (Value, error) {
	at, err := timeBig(localTime)
	if err != nil {
		return Value{}, evaluationError(CodeInputInvalid, "local_time", ErrInputInvalid)
	}
	if len(lane.keyframes) == 0 {
		return Value{}, evaluationError(CodeKeyframeInvalid, "compiled_lane.keyframes", ErrKeyframeInvalid)
	}

	// Find the first keyframe at or after the requested local time. Exact endpoints select that
	// keyframe; an interior point evaluates the segment immediately to its left.
	right := sort.Search(len(lane.keyframes), func(index int) bool {
		return lane.keyframes[index].offset.Cmp(at) >= 0
	})
	if right == 0 {
		return cloneValue(lane.keyframes[0].value)
	}
	if right == len(lane.keyframes) {
		return cloneValue(lane.keyframes[len(lane.keyframes)-1].value)
	}
	if lane.keyframes[right].offset.Cmp(at) == 0 {
		return cloneValue(lane.keyframes[right].value)
	}

	left := lane.keyframes[right-1]
	if left.interpolation == documentv2.InterpolationHold {
		return cloneValue(left.value)
	}
	denominator := new(big.Rat).Sub(lane.keyframes[right].offset, left.offset)
	if denominator.Sign() <= 0 {
		return Value{}, evaluationError(CodeKeyframeInvalid, "compiled_lane.keyframes", ErrKeyframeInvalid)
	}
	progress := new(big.Rat).Quo(new(big.Rat).Sub(at, left.offset), denominator)
	weight := progress
	if left.interpolation == documentv2.InterpolationCubic {
		if left.easing == nil {
			return Value{}, evaluationError(CodeKeyframeInvalid, "compiled_lane.easing", ErrKeyframeInvalid)
		}
		weight = cubicBezierWeight(progress, *left.easing)
	}
	return interpolateValue(left.value, lane.keyframes[right].value, weight, lane.valueType)
}

func evaluateCompiledProperties(compiled *compiledClip, timelineTime documentv2.RationalTime) (Properties, error) {
	localTime, err := ClipLocalTime(compiled.timeClip(), timelineTime)
	if err != nil {
		return Properties{}, err
	}
	result := cloneStaticProperties(compiled.static)
	for _, lane := range compiled.lanes {
		value, laneErr := lane.evaluate(localTime)
		if laneErr != nil {
			return Properties{}, laneErr
		}
		if applyErr := applyLane(&result, compiled.trackKind, lane.target, value); applyErr != nil {
			return Properties{}, applyErr
		}
	}
	return result, nil
}

func cloneStaticProperties(value Properties) Properties {
	result := Properties{}
	if value.Transform != nil {
		copy := *value.Transform
		result.Transform = &copy
	}
	if value.Opacity != nil {
		copy := *value.Opacity
		result.Opacity = &copy
	}
	if value.Crop != nil {
		copy := *value.Crop
		result.Crop = &copy
	}
	if value.AudioGain != nil {
		copy := *value.AudioGain
		result.AudioGain = &copy
	}
	if value.AudioPan != nil {
		copy := *value.AudioPan
		result.AudioPan = &copy
	}
	if value.AudioChannelMatrix != nil {
		copy := *value.AudioChannelMatrix
		copy.SourceChannels = append([]string(nil), value.AudioChannelMatrix.SourceChannels...)
		copy.LeftCoefficients = append([]ExactRational(nil), value.AudioChannelMatrix.LeftCoefficients...)
		copy.RightCoefficients = append([]ExactRational(nil), value.AudioChannelMatrix.RightCoefficients...)
		result.AudioChannelMatrix = &copy
	}
	if value.TextFill != nil {
		copy := *value.TextFill
		result.TextFill = &copy
	}
	return result
}
