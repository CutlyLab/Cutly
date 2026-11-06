package sfntglyf

import (
	"math"
	"math/big"
)

func outlineSetMidpoint(exact *geometryExactContext, left, right geometryDyadic, path string) (geometryDyadic, error) {
	sum, err := exact.add(left, right, path+"/add")
	if err != nil {
		return geometryDyadic{}, err
	}
	return outlineSetHalf(exact, sum, path+"/half")
}

func outlineSetHalf(exact *geometryExactContext, input geometryDyadic, path string) (geometryDyadic, error) {
	if exact == nil {
		return geometryDyadic{}, outlineSetInvariant(path)
	}
	if err := exact.begin(path); err != nil {
		return geometryDyadic{}, err
	}
	if err := exact.validate(input, path); err != nil {
		return geometryDyadic{}, err
	}
	exact.observe(input)
	if input.shift == math.MaxUint32 {
		return geometryDyadic{}, outlineSetResource(path)
	}
	result, err := exact.normalize(new(big.Int).Set(input.number()), input.shift+1, path)
	if err != nil {
		return geometryDyadic{}, err
	}
	charge, ok := checkedGeometryLimbSum(input.portableLimbs(), result.portableLimbs())
	if !ok {
		return geometryDyadic{}, outlineSetResource(path)
	}
	if err := exact.chargeLimbs(charge, path); err != nil {
		return geometryDyadic{}, err
	}
	return result, nil
}

func outlineSetRoundQ26(exact *geometryExactContext, input geometryDyadic, path string) (Q26_6, error) {
	if exact == nil {
		return 0, outlineSetInvariant(path)
	}
	if err := exact.begin(path); err != nil {
		return 0, err
	}
	if err := exact.validate(input, path); err != nil {
		return 0, err
	}
	exact.observe(input)
	if input.isZero() {
		if err := exact.chargeLimbs(2, path); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if input.shift <= 6 {
		shift := uint32(6) - input.shift
		if uint64(shift) > uint64(^uint(0)) {
			return 0, outlineSetResource(path)
		}
		resultBits, ok := checkedAdd(uint64(input.numerator.BitLen()), uint64(shift))
		if !ok || resultBits > uint64(exact.maxBits) {
			return 0, outlineSetResource(path)
		}
		result := new(big.Int).Lsh(new(big.Int).Set(input.number()), uint(shift))
		exact.observe(geometryDyadic{numerator: result})
		charge, ok := checkedGeometryLimbSum(input.portableLimbs(), portableBigIntLimbs(result))
		if !ok {
			return 0, outlineSetResource(path)
		}
		if err := exact.chargeLimbs(charge, path); err != nil {
			return 0, err
		}
		if !result.IsInt64() {
			return 0, outlineSetCoordinateError(path)
		}
		return Q26_6(result.Int64()), nil
	}
	distance := input.shift - 6
	if uint64(distance) > uint64(^uint(0)) {
		return 0, outlineSetResource(path)
	}
	if uint64(distance)+1 > uint64(exact.maxBits) {
		return 0, outlineSetResource(path)
	}
	abs := new(big.Int).Abs(input.number())
	divisor := new(big.Int).Lsh(big.NewInt(1), uint(distance))
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(abs, divisor, remainder)
	half := new(big.Int).Rsh(new(big.Int).Set(divisor), 1)
	comparison := remainder.Cmp(half)
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}
	if input.numerator.Sign() < 0 {
		quotient.Neg(quotient)
	}
	exact.observe(geometryDyadic{numerator: quotient})
	charge, ok := checkedGeometryLimbSum(
		input.portableLimbs(), portableBigIntLimbs(abs), portableBigIntLimbs(divisor),
		portableBigIntLimbs(quotient), portableBigIntLimbs(remainder), portableBigIntLimbs(half),
	)
	if !ok {
		return 0, outlineSetResource(path)
	}
	if err := exact.chargeLimbs(charge, path); err != nil {
		return 0, err
	}
	if !quotient.IsInt64() {
		return 0, outlineSetCoordinateError(path)
	}
	return Q26_6(quotient.Int64()), nil
}

func outlineSetRoundPoint(exact *geometryExactContext, point exactGeometryPoint, path string) (OutlinePoint, error) {
	x, err := outlineSetRoundQ26(exact, point.x, path+"/x")
	if err != nil {
		return OutlinePoint{}, err
	}
	y, err := outlineSetRoundQ26(exact, point.y, path+"/y")
	if err != nil {
		return OutlinePoint{}, err
	}
	return OutlinePoint{X: x, Y: y}, nil
}

func outlineSetMidpointPoint(exact *geometryExactContext, left, right exactGeometryPoint, path string) (exactGeometryPoint, error) {
	x, err := outlineSetMidpoint(exact, left.x, right.x, path+"/x")
	if err != nil {
		return exactGeometryPoint{}, err
	}
	y, err := outlineSetMidpoint(exact, left.y, right.y, path+"/y")
	if err != nil {
		return exactGeometryPoint{}, err
	}
	return exactGeometryPoint{x: x, y: y, onCurve: true}, nil
}

func outlineSetCoordinateError(path string) error {
	return fontError(CodeOutlineSetCoordinate, path, ErrOutlineSetCoordinate)
}
