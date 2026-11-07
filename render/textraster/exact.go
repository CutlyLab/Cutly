package textraster

import (
	"math"
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

type exactContext struct{ maxBits uint32 }

func (exact exactContext) check(value *big.Int, path string) error {
	if value == nil || uint32(value.BitLen()) > exact.maxBits {
		return rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	return nil
}

func (exact exactContext) scaleQ(value int64, scale documentv2.Rational, unitsPerEm uint16, path string) (int64, error) {
	numerator := new(big.Int).Mul(big.NewInt(value), big.NewInt(scale.Numerator))
	denominator := new(big.Int).Mul(big.NewInt(scale.Denominator), new(big.Int).SetUint64(uint64(unitsPerEm)))
	if err := exact.check(numerator, path); err != nil {
		return 0, err
	}
	if err := exact.check(denominator, path); err != nil {
		return 0, err
	}
	return exact.roundEven(numerator, denominator, path)
}

func (exact exactContext) roundEven(numerator, denominator *big.Int, path string) (int64, error) {
	if numerator == nil || denominator == nil || denominator.Sign() <= 0 {
		return 0, rasterError(CodeInputInvalid, path, ErrInputInvalid)
	}
	negative := numerator.Sign() < 0
	absolute := new(big.Int).Abs(new(big.Int).Set(numerator))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(absolute, denominator, remainder)
	doubled := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	if err := exact.check(quotient, path); err != nil {
		return 0, err
	}
	if err := exact.check(doubled, path); err != nil {
		return 0, err
	}
	comparison := doubled.Cmp(denominator)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) != 0 {
		quotient.Add(quotient, big.NewInt(1))
		if err := exact.check(quotient, path); err != nil {
			return 0, err
		}
	}
	if negative {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return 0, rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	return quotient.Int64(), nil
}

func checkedAddInt64(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}

func checkedSubInt64(left, right int64) (int64, bool) {
	if right == math.MinInt64 {
		if left >= 0 {
			return 0, false
		}
		return left - right, true
	}
	return checkedAddInt64(left, -right)
}

type rationalCrossing struct {
	numerator   big.Int
	denominator big.Int // positive
}

func (value *rationalCrossing) rightOf(exact exactContext, sampleX int64, path string) (bool, error) {
	offset := new(big.Int).Mul(&value.denominator, big.NewInt(sampleX))
	if err := exact.check(offset, path); err != nil {
		return false, err
	}
	difference := new(big.Int).Sub(&value.numerator, offset)
	if err := exact.check(difference, path); err != nil {
		return false, err
	}
	return difference.Sign() > 0, nil
}

type algebraicCrossing struct {
	u            big.Int
	v            big.Int
	discriminant big.Int
	denominator  big.Int // positive, exactly 2*Ay^2
}

func (value *algebraicCrossing) rightOf(exact exactContext, sampleX int64, path string) (bool, error) {
	offset := new(big.Int).Mul(&value.denominator, big.NewInt(sampleX))
	if err := exact.check(offset, path); err != nil {
		return false, err
	}
	u := new(big.Int).Sub(&value.u, offset)
	if err := exact.check(u, path); err != nil {
		return false, err
	}
	sign, err := signSqrtExpression(exact, u, &value.v, &value.discriminant, path)
	return sign > 0, err
}

// signSqrtExpression returns sign(u+v*sqrt(d)) without approximating sqrt(d).
func signSqrtExpression(exact exactContext, u, v, d *big.Int, path string) (int, error) {
	if u == nil || v == nil || d == nil || d.Sign() < 0 {
		return 0, rasterError(CodeInvariantFailed, path, ErrInvariantFailed)
	}
	if err := exact.check(u, path); err != nil {
		return 0, err
	}
	if err := exact.check(v, path); err != nil {
		return 0, err
	}
	if err := exact.check(d, path); err != nil {
		return 0, err
	}
	if v.Sign() == 0 || d.Sign() == 0 {
		return u.Sign(), nil
	}
	if u.Sign() == 0 || u.Sign() == v.Sign() {
		return v.Sign(), nil
	}
	uSquared := new(big.Int).Mul(u, u)
	vSquaredD := new(big.Int).Mul(v, v)
	if err := exact.check(uSquared, path); err != nil {
		return 0, err
	}
	if err := exact.check(vSquaredD, path); err != nil {
		return 0, err
	}
	vSquaredD.Mul(vSquaredD, d)
	if err := exact.check(vSquaredD, path); err != nil {
		return 0, err
	}
	switch uSquared.Cmp(vSquaredD) {
	case 0:
		return 0, nil
	case 1:
		return u.Sign(), nil
	default:
		return v.Sign(), nil
	}
}

func thresholdForCrossing(sampleColumns int, rightOf func(int64) (bool, error)) (int, error) {
	low, high := 0, sampleColumns
	for low < high {
		middle := low + (high-low)/2
		right, err := rightOf(int64(middle)*8 + 4)
		if err != nil {
			return 0, err
		}
		if right {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low, nil
}
