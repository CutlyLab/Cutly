package sfntglyf

import (
	"context"
	"math"
	"math/big"
)

// geometryDyadic is an immutable, normalized binary rational whose value is
// numerator / 2^shift. A zero value is represented by a zero numerator and
// shift zero. For every nonzero value with shift > 0, numerator is odd.
//
// The pointed-to big.Int must never be mutated. Every constructor and
// arithmetic operation below allocates a fresh numerator so values can be
// shared safely between sibling component expansions.
type geometryDyadic struct {
	numerator *big.Int
	shift     uint32
}

type geometryExactStats struct {
	OperationCount uint64
	LimbUnits      uint64
	MaxExactBits   uint32
}

type geometryExactWorkCharger interface {
	chargeExactOperation(path string) error
	chargeExactLimbs(units uint64, path string) error
}

// geometryExactContext owns deterministic operation and portable 64-bit-limb
// budgets. It is operation-local and is never retained by a published font.
type geometryExactContext struct {
	ctx          context.Context
	maxBits      uint32
	maxOps       uint64
	maxLimbUnits uint64
	work         geometryExactWorkCharger
	stats        geometryExactStats
}

func newGeometryExactContext(ctx context.Context, maxBits uint32, maxOps, maxLimbUnits uint64) *geometryExactContext {
	return newGeometryExactContextWithWork(ctx, maxBits, maxOps, maxLimbUnits, nil)
}

func newGeometryExactContextWithWork(ctx context.Context, maxBits uint32, maxOps, maxLimbUnits uint64, work geometryExactWorkCharger) *geometryExactContext {
	return &geometryExactContext{ctx: ctx, maxBits: maxBits, maxOps: maxOps, maxLimbUnits: maxLimbUnits, work: work}
}

func (value *geometryExactContext) Stats() geometryExactStats {
	if value == nil {
		return geometryExactStats{}
	}
	return value.stats
}

func (value geometryDyadic) isZero() bool {
	return value.numerator == nil || value.numerator.Sign() == 0
}

func (value geometryDyadic) exactBits() uint32 {
	if value.isZero() {
		return 0
	}
	bits := uint64(value.numerator.BitLen())
	denominatorBits := uint64(value.shift) + 1
	if denominatorBits > bits {
		bits = denominatorBits
	}
	if bits > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(bits)
}

func (value geometryDyadic) portableLimbs() uint64 {
	if value.isZero() {
		return 1
	}
	return uint64(value.numerator.BitLen()+63) / 64
}

func (value geometryDyadic) magnitudeBytes() []byte {
	if value.isZero() {
		return nil
	}
	abs := new(big.Int).Abs(value.numerator)
	return abs.Bytes()
}

func (value geometryDyadic) signTag() uint8 {
	if value.isZero() {
		return 0
	}
	if value.numerator.Sign() > 0 {
		return 1
	}
	return 2
}

func (value *geometryExactContext) integer(number int64, path string) (geometryDyadic, error) {
	if err := value.begin(path); err != nil {
		return geometryDyadic{}, err
	}
	result, err := value.normalize(new(big.Int).SetInt64(number), 0, path)
	if err != nil {
		return geometryDyadic{}, err
	}
	if err := value.chargeLimbs(result.portableLimbs(), path); err != nil {
		return geometryDyadic{}, err
	}
	return result, nil
}

func (value *geometryExactContext) f2dot14(number int16, path string) (geometryDyadic, error) {
	if err := value.begin(path); err != nil {
		return geometryDyadic{}, err
	}
	result, err := value.normalize(new(big.Int).SetInt64(int64(number)), 14, path)
	if err != nil {
		return geometryDyadic{}, err
	}
	if err := value.chargeLimbs(result.portableLimbs(), path); err != nil {
		return geometryDyadic{}, err
	}
	return result, nil
}

func (value *geometryExactContext) negate(input geometryDyadic, path string) (geometryDyadic, error) {
	if err := value.begin(path); err != nil {
		return geometryDyadic{}, err
	}
	if err := value.validate(input, path); err != nil {
		return geometryDyadic{}, err
	}
	result, err := value.normalize(new(big.Int).Neg(input.number()), input.shift, path)
	if err != nil {
		return geometryDyadic{}, err
	}
	if err := value.chargeLimbs(input.portableLimbs()+result.portableLimbs(), path); err != nil {
		return geometryDyadic{}, err
	}
	return result, nil
}

func (value *geometryExactContext) add(left, right geometryDyadic, path string) (geometryDyadic, error) {
	return value.addSub(left, right, false, path)
}

func (value *geometryExactContext) subtract(left, right geometryDyadic, path string) (geometryDyadic, error) {
	return value.addSub(left, right, true, path)
}

func (value *geometryExactContext) addSub(left, right geometryDyadic, subtract bool, path string) (geometryDyadic, error) {
	if err := value.begin(path); err != nil {
		return geometryDyadic{}, err
	}
	if err := value.validate(left, path); err != nil {
		return geometryDyadic{}, err
	}
	if err := value.validate(right, path); err != nil {
		return geometryDyadic{}, err
	}
	shift := left.shift
	if right.shift > shift {
		shift = right.shift
	}
	leftDelta := shift - left.shift
	rightDelta := shift - right.shift
	leftBits, ok := shiftedBitLen(left, leftDelta)
	if !ok {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	rightBits, ok := shiftedBitLen(right, rightDelta)
	if !ok {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	// An aligned operand is an explicit arbitrary-precision temporary. Refuse
	// obviously over-budget work before allocating it. One guard bit is
	// permitted because opposite-signed inputs may cancel during addition.
	guard := uint64(value.maxBits) + 1
	if leftBits > guard || rightBits > guard {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	leftAligned, err := shiftedCopy(left, leftDelta)
	if err != nil {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	rightAligned, err := shiftedCopy(right, rightDelta)
	if err != nil {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	resultNumerator := new(big.Int)
	if subtract {
		resultNumerator.Sub(leftAligned, rightAligned)
	} else {
		resultNumerator.Add(leftAligned, rightAligned)
	}
	result, err := value.normalize(resultNumerator, shift, path)
	if err != nil {
		return geometryDyadic{}, err
	}
	charge, ok := checkedGeometryLimbSum(
		left.portableLimbs(), right.portableLimbs(), portableBigIntLimbs(leftAligned),
		portableBigIntLimbs(rightAligned), result.portableLimbs(),
	)
	if !ok {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	if err := value.chargeLimbs(charge, path); err != nil {
		return geometryDyadic{}, err
	}
	return result, nil
}

func (value *geometryExactContext) multiply(left, right geometryDyadic, path string) (geometryDyadic, error) {
	if err := value.begin(path); err != nil {
		return geometryDyadic{}, err
	}
	if err := value.validate(left, path); err != nil {
		return geometryDyadic{}, err
	}
	if err := value.validate(right, path); err != nil {
		return geometryDyadic{}, err
	}
	shift64, ok := checkedAdd(uint64(left.shift), uint64(right.shift))
	if !ok || shift64 > math.MaxUint32 {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	if !left.isZero() && !right.isZero() {
		trailing, trailingOK := checkedAdd(uint64(left.numerator.TrailingZeroBits()), uint64(right.numerator.TrailingZeroBits()))
		if !trailingOK {
			return geometryDyadic{}, geometryExactResourceError(path)
		}
		cancel := trailing
		if cancel > shift64 {
			cancel = shift64
		}
		minimumProductBits := uint64(left.numerator.BitLen()+right.numerator.BitLen()) - 1
		minimumNormalizedBits := uint64(1)
		if minimumProductBits > cancel {
			minimumNormalizedBits = minimumProductBits - cancel
		}
		normalizedShift := shift64 - cancel
		minimumExactBits := minimumNormalizedBits
		if normalizedShift+1 > minimumExactBits {
			minimumExactBits = normalizedShift + 1
		}
		if minimumExactBits > uint64(value.maxBits) {
			return geometryDyadic{}, geometryExactResourceError(path)
		}
	}
	resultNumerator := new(big.Int).Mul(left.number(), right.number())
	result, err := value.normalize(resultNumerator, uint32(shift64), path)
	if err != nil {
		return geometryDyadic{}, err
	}
	charge, ok := checkedGeometryLimbSum(left.portableLimbs(), right.portableLimbs(), result.portableLimbs())
	if !ok {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	if err := value.chargeLimbs(charge, path); err != nil {
		return geometryDyadic{}, err
	}
	return result, nil
}

func (value *geometryExactContext) compare(left, right geometryDyadic, path string) (int, error) {
	if err := value.begin(path); err != nil {
		return 0, err
	}
	if err := value.validate(left, path); err != nil {
		return 0, err
	}
	if err := value.validate(right, path); err != nil {
		return 0, err
	}
	shift := left.shift
	if right.shift > shift {
		shift = right.shift
	}
	temporaryBits, ok := checkedMultiply(uint64(value.maxBits), 2)
	if !ok {
		return 0, geometryExactResourceError(path)
	}
	leftBits, ok := shiftedBitLen(left, shift-left.shift)
	if !ok {
		return 0, geometryExactResourceError(path)
	}
	rightBits, ok := shiftedBitLen(right, shift-right.shift)
	if !ok || leftBits > temporaryBits || rightBits > temporaryBits {
		return 0, geometryExactResourceError(path)
	}
	leftAligned, err := shiftedCopy(left, shift-left.shift)
	if err != nil {
		return 0, geometryExactResourceError(path)
	}
	rightAligned, err := shiftedCopy(right, shift-right.shift)
	if err != nil {
		return 0, geometryExactResourceError(path)
	}
	charge, ok := checkedGeometryLimbSum(
		left.portableLimbs(), right.portableLimbs(), portableBigIntLimbs(leftAligned), portableBigIntLimbs(rightAligned),
	)
	if !ok {
		return 0, geometryExactResourceError(path)
	}
	if err := value.chargeLimbs(charge, path); err != nil {
		return 0, err
	}
	return leftAligned.Cmp(rightAligned), nil
}

func (value *geometryExactContext) floor(input geometryDyadic, path string) (*big.Int, error) {
	return value.integerBound(input, false, path)
}

func (value *geometryExactContext) ceil(input geometryDyadic, path string) (*big.Int, error) {
	return value.integerBound(input, true, path)
}

func (value *geometryExactContext) integerBound(input geometryDyadic, ceil bool, path string) (*big.Int, error) {
	if err := value.begin(path); err != nil {
		return nil, err
	}
	if err := value.validate(input, path); err != nil {
		return nil, err
	}
	if input.isZero() {
		result := new(big.Int)
		if err := value.chargeLimbs(input.portableLimbs()+portableBigIntLimbs(result), path); err != nil {
			return nil, err
		}
		return result, nil
	}
	abs := new(big.Int).Abs(input.numerator)
	quotient := new(big.Int).Rsh(new(big.Int).Set(abs), uint(input.shift))
	if input.shift == 0 {
		if input.numerator.Sign() < 0 {
			quotient.Neg(quotient)
		}
	} else if input.numerator.Sign() > 0 {
		if ceil {
			quotient.Add(quotient, big.NewInt(1))
		}
	} else {
		if !ceil {
			quotient.Add(quotient, big.NewInt(1))
		}
		quotient.Neg(quotient)
	}
	charge, ok := checkedGeometryLimbSum(input.portableLimbs(), portableBigIntLimbs(abs), portableBigIntLimbs(quotient))
	if !ok {
		return nil, geometryExactResourceError(path)
	}
	if err := value.chargeLimbs(charge, path); err != nil {
		return nil, err
	}
	return quotient, nil
}

func (value *geometryExactContext) begin(path string) error {
	if value == nil || value.maxBits == 0 || value.maxOps == 0 || value.maxLimbUnits == 0 {
		return geometryExactResourceError(path)
	}
	if err := contextFailure(value.ctx, path); err != nil {
		return err
	}
	next, ok := checkedAdd(value.stats.OperationCount, 1)
	if !ok || next > value.maxOps {
		return geometryExactResourceError(path)
	}
	if value.work != nil {
		if err := value.work.chargeExactOperation(path); err != nil {
			return err
		}
	}
	value.stats.OperationCount = next
	return nil
}

func (value *geometryExactContext) validate(input geometryDyadic, path string) error {
	if input.numerator == nil {
		return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
	}
	if input.isZero() {
		if input.shift != 0 {
			return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
		}
		return nil
	}
	if input.shift > 0 && input.numerator.Bit(0) == 0 {
		return fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
	}
	bits := input.exactBits()
	if bits > value.maxBits {
		return geometryExactResourceError(path)
	}
	return nil
}

func (value *geometryExactContext) normalize(numerator *big.Int, shift uint32, path string) (geometryDyadic, error) {
	if numerator == nil {
		return geometryDyadic{}, fontError(CodeGeometryInvariant, path, ErrInvariantFailed)
	}
	owned := new(big.Int).Set(numerator)
	if owned.Sign() == 0 {
		result := geometryDyadic{numerator: new(big.Int)}
		value.observe(result)
		return result, nil
	}
	if shift > 0 {
		trailing := owned.TrailingZeroBits()
		if trailing > uint(shift) {
			trailing = uint(shift)
		}
		if trailing > 0 {
			owned.Rsh(owned, trailing)
			shift -= uint32(trailing)
		}
	}
	result := geometryDyadic{numerator: owned, shift: shift}
	if result.exactBits() > value.maxBits {
		return geometryDyadic{}, geometryExactResourceError(path)
	}
	value.observe(result)
	return result, nil
}

func (value *geometryExactContext) observe(result geometryDyadic) {
	bits := result.exactBits()
	if bits > value.stats.MaxExactBits {
		value.stats.MaxExactBits = bits
	}
}

func (value *geometryExactContext) chargeLimbs(units uint64, path string) error {
	next, ok := checkedAdd(value.stats.LimbUnits, units)
	if !ok || next > value.maxLimbUnits {
		return geometryExactResourceError(path)
	}
	if value.work != nil {
		if err := value.work.chargeExactLimbs(units, path); err != nil {
			return err
		}
	}
	value.stats.LimbUnits = next
	return nil
}

func (value geometryDyadic) number() *big.Int {
	if value.numerator == nil {
		return new(big.Int)
	}
	return value.numerator
}

func shiftedBitLen(value geometryDyadic, shift uint32) (uint64, bool) {
	if value.isZero() {
		return 0, true
	}
	bits, ok := checkedAdd(uint64(value.numerator.BitLen()), uint64(shift))
	return bits, ok
}

func shiftedCopy(value geometryDyadic, shift uint32) (*big.Int, error) {
	result := new(big.Int).Set(value.number())
	if shift == 0 || result.Sign() == 0 {
		return result, nil
	}
	if uint64(shift) > uint64(^uint(0)) {
		return nil, ErrResourceLimit
	}
	result.Lsh(result, uint(shift))
	return result, nil
}

func portableBigIntLimbs(value *big.Int) uint64 {
	if value == nil || value.Sign() == 0 {
		return 1
	}
	return uint64(value.BitLen()+63) / 64
}

func checkedGeometryLimbSum(values ...uint64) (uint64, bool) {
	var result uint64
	for _, value := range values {
		var ok bool
		result, ok = checkedAdd(result, value)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func geometryExactResourceError(path string) error {
	return fontError(CodeGeometryResourceLimit, path, ErrResourceLimit)
}
