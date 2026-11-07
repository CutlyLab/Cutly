package evaluator

import (
	"fmt"
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

const cubicQ32Denominator uint64 = 1 << 32

// Value is one exactly evaluated typed keyframe value. Scalar, point and rect components are
// arbitrary precision; colors remain the authored RGBA16 value because Document v2 permits only
// hold interpolation for color lanes.
type Value struct {
	Scalar *ExactRational          `json:"scalar,omitempty"`
	Point  *ExactPoint             `json:"point,omitempty"`
	Rect   *ExactRect              `json:"rect,omitempty"`
	Color  *documentv2.ColorRGBA16 `json:"color,omitempty"`
}

// EvaluateLane applies endpoint clamping and exact half-open segment semantics at clip-local
// time. The function is hostile-input safe even when called without Compile; malformed lanes
// return EVALUATOR.V2.KEYFRAME_INVALID instead of panicking or guessing.
func EvaluateLane(lane documentv2.KeyframeLane, localTime documentv2.RationalTime) (Value, error) {
	if err := localTime.Validate(); err != nil {
		return Value{}, evaluationError(CodeInputInvalid, "local_time", ErrInputInvalid)
	}
	values, err := validateAndConvertLane(lane)
	if err != nil {
		return Value{}, err
	}
	firstComparison, err := localTime.Compare(lane.Keyframes[0].Offset)
	if err != nil {
		return Value{}, evaluationError(CodeInputInvalid, "local_time", ErrInputInvalid)
	}
	if firstComparison <= 0 {
		return cloneValue(values[0])
	}
	lastIndex := len(lane.Keyframes) - 1
	lastComparison, err := localTime.Compare(lane.Keyframes[lastIndex].Offset)
	if err != nil {
		return Value{}, evaluationError(CodeInputInvalid, "local_time", ErrInputInvalid)
	}
	if lastComparison >= 0 {
		return cloneValue(values[lastIndex])
	}

	for index := 0; index < lastIndex; index++ {
		rightComparison, compareErr := localTime.Compare(lane.Keyframes[index+1].Offset)
		if compareErr != nil {
			return Value{}, evaluationError(CodeInputInvalid, "local_time", ErrInputInvalid)
		}
		if rightComparison >= 0 {
			continue // At an exact right endpoint, that next keyframe wins.
		}
		left := lane.Keyframes[index]
		if left.Interpolation == documentv2.InterpolationHold {
			return cloneValue(values[index])
		}
		progress, progressErr := segmentProgress(localTime, left.Offset, lane.Keyframes[index+1].Offset)
		if progressErr != nil {
			return Value{}, progressErr
		}
		weight := progress
		if left.Interpolation == documentv2.InterpolationCubic {
			weight = cubicBezierWeight(progress, *left.Easing)
		}
		return interpolateValue(values[index], values[index+1], weight, lane.ValueType)
	}
	return Value{}, evaluationError(CodeKeyframeInvalid, "lane.keyframes", ErrKeyframeInvalid)
}

func validateAndConvertLane(lane documentv2.KeyframeLane) ([]Value, error) {
	if len(lane.Keyframes) == 0 {
		return nil, evaluationError(CodeKeyframeInvalid, "lane.keyframes", ErrKeyframeInvalid)
	}
	expectedType, targetKnown := targetValueType(lane.Target)
	if !targetKnown || !knownValueType(lane.ValueType) || lane.ValueType != expectedType {
		return nil, evaluationError(CodeKeyframeInvalid, "lane.value_type", ErrKeyframeInvalid)
	}
	result := make([]Value, len(lane.Keyframes))
	for index, keyframe := range lane.Keyframes {
		path := fmt.Sprintf("lane.keyframes[%d]", index)
		if err := keyframe.Offset.Validate(); err != nil || keyframe.Offset.Value < 0 {
			return nil, evaluationError(CodeKeyframeInvalid, path+".offset", ErrKeyframeInvalid)
		}
		if index > 0 {
			comparison, err := keyframe.Offset.Compare(lane.Keyframes[index-1].Offset)
			if err != nil || comparison <= 0 {
				return nil, evaluationError(CodeKeyframeInvalid, path+".offset", ErrKeyframeInvalid)
			}
		}
		value, err := convertKeyframeValue(keyframe.Value, lane.ValueType)
		if err != nil {
			return nil, evaluationError(CodeKeyframeInvalid, path+".value", ErrKeyframeInvalid)
		}
		if !valueAllowedForTarget(lane.Target, value) {
			return nil, evaluationError(CodeKeyframeInvalid, path+".value", ErrKeyframeInvalid)
		}
		result[index] = value

		last := index == len(lane.Keyframes)-1
		if last {
			if keyframe.Interpolation != "" || keyframe.Easing != nil {
				return nil, evaluationError(CodeKeyframeInvalid, path, ErrKeyframeInvalid)
			}
			continue
		}
		if lane.ValueType == documentv2.KeyframeValueColor && keyframe.Interpolation != documentv2.InterpolationHold {
			return nil, evaluationError(CodeKeyframeInvalid, path+".interpolation", ErrKeyframeInvalid)
		}
		switch keyframe.Interpolation {
		case documentv2.InterpolationHold, documentv2.InterpolationLinear:
			if keyframe.Easing != nil {
				return nil, evaluationError(CodeKeyframeInvalid, path+".easing", ErrKeyframeInvalid)
			}
		case documentv2.InterpolationCubic:
			if keyframe.Easing == nil || !validBezier(*keyframe.Easing) {
				return nil, evaluationError(CodeKeyframeInvalid, path+".easing", ErrKeyframeInvalid)
			}
		default:
			return nil, evaluationError(CodeKeyframeInvalid, path+".interpolation", ErrKeyframeInvalid)
		}
	}
	return result, nil
}

func targetValueType(target documentv2.KeyframeTarget) (documentv2.KeyframeValueType, bool) {
	switch target {
	case documentv2.KeyframeTargetTransformPosition, documentv2.KeyframeTargetTransformScale,
		documentv2.KeyframeTargetTransformAnchor:
		return documentv2.KeyframeValuePoint, true
	case documentv2.KeyframeTargetTransformRotation, documentv2.KeyframeTargetOpacity,
		documentv2.KeyframeTargetAudioGain, documentv2.KeyframeTargetAudioPan:
		return documentv2.KeyframeValueScalar, true
	case documentv2.KeyframeTargetCrop:
		return documentv2.KeyframeValueRect, true
	case documentv2.KeyframeTargetTextFill:
		return documentv2.KeyframeValueColor, true
	default:
		return "", false
	}
}

func valueAllowedForTarget(target documentv2.KeyframeTarget, value Value) bool {
	zero, one := big.NewRat(0, 1), big.NewRat(1, 1)
	inUnitInterval := func(candidate ExactRational) bool {
		asBig, err := candidate.big()
		return err == nil && asBig.Cmp(zero) >= 0 && asBig.Cmp(one) <= 0
	}
	switch target {
	case documentv2.KeyframeTargetTransformAnchor:
		return value.Point != nil && inUnitInterval(value.Point.X) && inUnitInterval(value.Point.Y)
	case documentv2.KeyframeTargetOpacity:
		return value.Scalar != nil && inUnitInterval(*value.Scalar)
	case documentv2.KeyframeTargetCrop:
		if value.Rect == nil || !inUnitInterval(value.Rect.X) || !inUnitInterval(value.Rect.Y) ||
			!inUnitInterval(value.Rect.Width) || !inUnitInterval(value.Rect.Height) {
			return false
		}
		x, xErr := value.Rect.X.big()
		y, yErr := value.Rect.Y.big()
		width, widthErr := value.Rect.Width.big()
		height, heightErr := value.Rect.Height.big()
		return xErr == nil && yErr == nil && widthErr == nil && heightErr == nil &&
			width.Sign() > 0 && height.Sign() > 0 &&
			new(big.Rat).Add(x, width).Cmp(one) <= 0 && new(big.Rat).Add(y, height).Cmp(one) <= 0
	case documentv2.KeyframeTargetAudioGain:
		if value.Scalar == nil {
			return false
		}
		asBig, err := value.Scalar.big()
		return err == nil && asBig.Sign() >= 0
	case documentv2.KeyframeTargetAudioPan:
		if value.Scalar == nil {
			return false
		}
		asBig, err := value.Scalar.big()
		return err == nil && asBig.Cmp(big.NewRat(-1, 1)) >= 0 && asBig.Cmp(one) <= 0
	case documentv2.KeyframeTargetTransformPosition, documentv2.KeyframeTargetTransformScale:
		return value.Point != nil
	case documentv2.KeyframeTargetTransformRotation:
		return value.Scalar != nil
	case documentv2.KeyframeTargetTextFill:
		return value.Color != nil
	default:
		return false
	}
}

func knownValueType(value documentv2.KeyframeValueType) bool {
	switch value {
	case documentv2.KeyframeValueScalar, documentv2.KeyframeValuePoint,
		documentv2.KeyframeValueRect, documentv2.KeyframeValueColor:
		return true
	default:
		return false
	}
}

func convertKeyframeValue(value documentv2.KeyframeValue, kind documentv2.KeyframeValueType) (Value, error) {
	count := 0
	if value.Scalar != nil {
		count++
	}
	if value.Point != nil {
		count++
	}
	if value.Rect != nil {
		count++
	}
	if value.Color != nil {
		count++
	}
	if count != 1 {
		return Value{}, ErrKeyframeInvalid
	}
	switch kind {
	case documentv2.KeyframeValueScalar:
		if value.Scalar == nil {
			return Value{}, ErrKeyframeInvalid
		}
		exact, err := exactFromDocument(*value.Scalar)
		if err != nil {
			return Value{}, err
		}
		return Value{Scalar: &exact}, nil
	case documentv2.KeyframeValuePoint:
		if value.Point == nil {
			return Value{}, ErrKeyframeInvalid
		}
		exact, err := exactPoint(*value.Point)
		if err != nil {
			return Value{}, err
		}
		return Value{Point: &exact}, nil
	case documentv2.KeyframeValueRect:
		if value.Rect == nil {
			return Value{}, ErrKeyframeInvalid
		}
		exact, err := exactRect(*value.Rect)
		if err != nil {
			return Value{}, err
		}
		return Value{Rect: &exact}, nil
	case documentv2.KeyframeValueColor:
		if value.Color == nil {
			return Value{}, ErrKeyframeInvalid
		}
		copy := *value.Color
		return Value{Color: &copy}, nil
	default:
		return Value{}, ErrKeyframeInvalid
	}
}

func validBezier(value documentv2.CubicBezier) bool {
	parts := []documentv2.Rational{value.X1, value.Y1, value.X2, value.Y2}
	for _, part := range parts {
		if part.Validate() != nil {
			return false
		}
		asBig := new(big.Rat).SetFrac64(part.Numerator, part.Denominator)
		if asBig.Sign() < 0 || asBig.Cmp(big.NewRat(1, 1)) > 0 {
			return false
		}
	}
	x1 := new(big.Rat).SetFrac64(value.X1.Numerator, value.X1.Denominator)
	x2 := new(big.Rat).SetFrac64(value.X2.Numerator, value.X2.Denominator)
	return x1.Cmp(x2) <= 0
}

func segmentProgress(at, start, end documentv2.RationalTime) (*big.Rat, error) {
	atValue, err := timeBig(at)
	if err != nil {
		return nil, err
	}
	startValue, err := timeBig(start)
	if err != nil {
		return nil, err
	}
	endValue, err := timeBig(end)
	if err != nil || endValue.Cmp(startValue) <= 0 {
		return nil, evaluationError(CodeKeyframeInvalid, "lane.keyframes.offset", ErrKeyframeInvalid)
	}
	return new(big.Rat).Quo(new(big.Rat).Sub(atValue, startValue), new(big.Rat).Sub(endValue, startValue)), nil
}

// cubicBezierWeight implements the versioned Q0.32 lower-bound solver from Document v2's
// interpolation descriptor. The caller provides an interior progress value in (0,1).
func cubicBezierWeight(progress *big.Rat, easing documentv2.CubicBezier) *big.Rat {
	if progress.Sign() <= 0 {
		return new(big.Rat)
	}
	if progress.Cmp(big.NewRat(1, 1)) >= 0 {
		return new(big.Rat).SetInt64(1)
	}
	maximumInterior := cubicQ32Denominator - 1
	maximumU := q32Value(maximumInterior)
	if bezierCoordinate(maximumU, easing.X1, easing.X2).Cmp(progress) < 0 {
		return new(big.Rat).SetInt64(1)
	}

	low, high := uint64(0), maximumInterior
	for step := 0; step < 32; step++ {
		if low >= high {
			continue
		}
		middle := low + (high-low)/2
		if bezierCoordinate(q32Value(middle), easing.X1, easing.X2).Cmp(progress) >= 0 {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return bezierCoordinate(q32Value(low), easing.Y1, easing.Y2)
}

func q32Value(value uint64) *big.Rat {
	return new(big.Rat).SetFrac(new(big.Int).SetUint64(value), new(big.Int).SetUint64(cubicQ32Denominator))
}

func bezierCoordinate(u *big.Rat, first, second documentv2.Rational) *big.Rat {
	oneMinus := new(big.Rat).Sub(big.NewRat(1, 1), u)
	firstControl := new(big.Rat).SetFrac64(first.Numerator, first.Denominator)
	secondControl := new(big.Rat).SetFrac64(second.Numerator, second.Denominator)

	term1 := new(big.Rat).Mul(new(big.Rat).Mul(oneMinus, oneMinus), u)
	term1.Mul(term1, big.NewRat(3, 1)).Mul(term1, firstControl)
	term2 := new(big.Rat).Mul(oneMinus, new(big.Rat).Mul(u, u))
	term2.Mul(term2, big.NewRat(3, 1)).Mul(term2, secondControl)
	term3 := new(big.Rat).Mul(new(big.Rat).Mul(u, u), u)
	return new(big.Rat).Add(new(big.Rat).Add(term1, term2), term3)
}

func interpolateValue(left, right Value, weight *big.Rat, kind documentv2.KeyframeValueType) (Value, error) {
	switch kind {
	case documentv2.KeyframeValueScalar:
		if left.Scalar == nil || right.Scalar == nil {
			return Value{}, evaluationError(CodeKeyframeInvalid, "lane.value", ErrKeyframeInvalid)
		}
		value, err := lerpExact(*left.Scalar, *right.Scalar, weight)
		if err != nil {
			return Value{}, err
		}
		return Value{Scalar: &value}, nil
	case documentv2.KeyframeValuePoint:
		if left.Point == nil || right.Point == nil {
			return Value{}, evaluationError(CodeKeyframeInvalid, "lane.value", ErrKeyframeInvalid)
		}
		x, err := lerpExact(left.Point.X, right.Point.X, weight)
		if err != nil {
			return Value{}, err
		}
		y, err := lerpExact(left.Point.Y, right.Point.Y, weight)
		if err != nil {
			return Value{}, err
		}
		point := ExactPoint{X: x, Y: y}
		return Value{Point: &point}, nil
	case documentv2.KeyframeValueRect:
		if left.Rect == nil || right.Rect == nil {
			return Value{}, evaluationError(CodeKeyframeInvalid, "lane.value", ErrKeyframeInvalid)
		}
		x, err := lerpExact(left.Rect.X, right.Rect.X, weight)
		if err != nil {
			return Value{}, err
		}
		y, err := lerpExact(left.Rect.Y, right.Rect.Y, weight)
		if err != nil {
			return Value{}, err
		}
		width, err := lerpExact(left.Rect.Width, right.Rect.Width, weight)
		if err != nil {
			return Value{}, err
		}
		height, err := lerpExact(left.Rect.Height, right.Rect.Height, weight)
		if err != nil {
			return Value{}, err
		}
		rect := ExactRect{X: x, Y: y, Width: width, Height: height}
		return Value{Rect: &rect}, nil
	default:
		return Value{}, evaluationError(CodeKeyframeInvalid, "lane.value_type", ErrKeyframeInvalid)
	}
}

func lerpExact(left, right ExactRational, weight *big.Rat) (ExactRational, error) {
	leftBig, err := left.big()
	if err != nil {
		return ExactRational{}, err
	}
	rightBig, err := right.big()
	if err != nil {
		return ExactRational{}, err
	}
	delta := new(big.Rat).Sub(rightBig, leftBig)
	return exactFromBig(new(big.Rat).Add(leftBig, new(big.Rat).Mul(delta, weight))), nil
}

func cloneValue(value Value) (Value, error) {
	result := Value{}
	if value.Scalar != nil {
		copy, err := cloneExact(*value.Scalar)
		if err != nil {
			return Value{}, err
		}
		result.Scalar = &copy
	}
	if value.Point != nil {
		x, err := cloneExact(value.Point.X)
		if err != nil {
			return Value{}, err
		}
		y, err := cloneExact(value.Point.Y)
		if err != nil {
			return Value{}, err
		}
		copy := ExactPoint{X: x, Y: y}
		result.Point = &copy
	}
	if value.Rect != nil {
		x, err := cloneExact(value.Rect.X)
		if err != nil {
			return Value{}, err
		}
		y, err := cloneExact(value.Rect.Y)
		if err != nil {
			return Value{}, err
		}
		width, err := cloneExact(value.Rect.Width)
		if err != nil {
			return Value{}, err
		}
		height, err := cloneExact(value.Rect.Height)
		if err != nil {
			return Value{}, err
		}
		copy := ExactRect{
			X: x, Y: y, Width: width, Height: height,
		}
		result.Rect = &copy
	}
	if value.Color != nil {
		copy := *value.Color
		result.Color = &copy
	}
	return result, nil
}

func cloneExact(value ExactRational) (ExactRational, error) {
	result, err := value.big()
	if err != nil {
		return ExactRational{}, err
	}
	return exactFromBig(result), nil
}
