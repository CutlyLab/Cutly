package vectorraster

import (
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

type exactContext struct {
	maxBits uint32
}

func (exact exactContext) fromModel(value documentv2.Rational, path string) (big.Rat, error) {
	var result big.Rat
	result.SetFrac64(value.Numerator, value.Denominator)
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) integer(value uint64, path string) (big.Rat, error) {
	var result big.Rat
	result.SetInt(new(big.Int).SetUint64(value))
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) add(left, right *big.Rat, path string) (big.Rat, error) {
	var result big.Rat
	result.Add(left, right)
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) sub(left, right *big.Rat, path string) (big.Rat, error) {
	var result big.Rat
	result.Sub(left, right)
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) mul(left, right *big.Rat, path string) (big.Rat, error) {
	var result big.Rat
	result.Mul(left, right)
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) quo(left, right *big.Rat, path string) (big.Rat, error) {
	if right.Sign() == 0 {
		return big.Rat{}, rasterError(CodeSourceInvalid, path, ErrSourceInvalid)
	}
	var result big.Rat
	result.Quo(left, right)
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) half(value *big.Rat, path string) (big.Rat, error) {
	var result big.Rat
	result.Quo(value, big.NewRat(2, 1))
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

func (exact exactContext) check(value *big.Rat, path string) error {
	if value == nil || value.Denom().Sign() <= 0 ||
		uint32(value.Num().BitLen()) > exact.maxBits || uint32(value.Denom().BitLen()) > exact.maxBits {
		return rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	return nil
}

// compareSample compares value with sample (2*index+1)/16 while bounding the complete integer
// cross-products, not only the reduced input rational.
func (exact exactContext) compareSample(value *big.Rat, index int, path string) (int, error) {
	if index < 0 {
		return 0, rasterError(CodeSourceInvalid, path, ErrSourceInvalid)
	}
	odd := uint64(index)*2 + 1
	left := new(big.Int).Lsh(new(big.Int).Set(value.Num()), 4)
	right := new(big.Int).Mul(new(big.Int).Set(value.Denom()), new(big.Int).SetUint64(odd))
	if uint32(left.BitLen()) > exact.maxBits || uint32(right.BitLen()) > exact.maxBits {
		return 0, rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	return left.Cmp(right), nil
}

func (exact exactContext) sample(index int, path string) (big.Rat, error) {
	if index < 0 || uint64(index) > (^uint64(0)-1)/2 {
		return big.Rat{}, rasterError(CodeResourceLimit, path, ErrResourceLimit)
	}
	var result big.Rat
	result.SetFrac(new(big.Int).SetUint64(uint64(index)*2+1), big.NewInt(16))
	if err := exact.check(&result, path); err != nil {
		return big.Rat{}, err
	}
	return result, nil
}

// sampleLowerBound returns the first index k in [0,count] whose center (2*k+1)/16 is
// greater than or equal to value. It is the exact integer solution of
// 2*denominator*k >= 16*numerator-denominator.
func (exact exactContext) sampleLowerBound(value *big.Rat, count int, path string) (int, error) {
	if count < 0 {
		return 0, rasterError(CodeSourceInvalid, path, ErrSourceInvalid)
	}
	threshold := new(big.Int).Lsh(new(big.Int).Set(value.Num()), 4)
	threshold.Sub(threshold, value.Denom())
	divisor := new(big.Int).Lsh(new(big.Int).Set(value.Denom()), 1)
	if uint32(threshold.BitLen()) > exact.maxBits || uint32(divisor.BitLen()) > exact.maxBits {
		return 0, rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	if threshold.Sign() <= 0 {
		return 0, nil
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(threshold, divisor, remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Int64() >= int64(count) {
		return count, nil
	}
	return int(quotient.Int64()), nil
}

// greatestSampleBelow returns the greatest index k in [0,count) whose center is strictly less
// than value. Subtracting one before integer division freezes the strict crossing-x rule.
func (exact exactContext) greatestSampleBelow(value *big.Rat, count int, path string) (int, error) {
	if count < 0 {
		return 0, rasterError(CodeSourceInvalid, path, ErrSourceInvalid)
	}
	threshold := new(big.Int).Lsh(new(big.Int).Set(value.Num()), 4)
	threshold.Sub(threshold, value.Denom())
	divisor := new(big.Int).Lsh(new(big.Int).Set(value.Denom()), 1)
	if uint32(threshold.BitLen()) > exact.maxBits || uint32(divisor.BitLen()) > exact.maxBits {
		return 0, rasterError(CodeExactLimit, path, ErrExactLimit)
	}
	if threshold.Sign() <= 0 || count == 0 {
		return -1, nil
	}
	threshold.Sub(threshold, big.NewInt(1))
	quotient := new(big.Int).Quo(threshold, divisor)
	if !quotient.IsInt64() || quotient.Int64() >= int64(count) {
		return count - 1, nil
	}
	return int(quotient.Int64()), nil
}
