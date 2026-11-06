package textlayout

import (
	"math"
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

type exactContext struct{ maxBits uint32 }

func (exact exactContext) checkInt(value *big.Int, path string) error {
	if value == nil || uint32(value.BitLen()) > exact.maxBits {
		return layoutError(CodeExactLimit, path, ErrExactLimit)
	}
	return nil
}

func (exact exactContext) scaleQ(value Q26_6, scale documentv2.Rational, unitsPerEm uint32, path string) (Q26_6, error) {
	numerator := new(big.Int).SetInt64(int64(value))
	numerator.Mul(numerator, big.NewInt(scale.Numerator))
	denominator := new(big.Int).Mul(big.NewInt(scale.Denominator), new(big.Int).SetUint64(uint64(unitsPerEm)))
	if err := exact.checkInt(numerator, path); err != nil {
		return 0, err
	}
	if err := exact.checkInt(denominator, path); err != nil {
		return 0, err
	}
	result, err := exact.roundFraction(numerator, denominator, path)
	return Q26_6(result), err
}

func (exact exactContext) baseline(
	ascender int32,
	fontSize, lineHeight documentv2.Rational,
	unitsPerEm uint32,
	lineIndex uint64,
	path string,
) (Q26_6, error) {
	// 64 * (ascender*fontSize/unitsPerEm + lineIndex*lineHeight), evaluated as
	// one exact fraction so rounded line baselines are never accumulated.
	fontTerm := big.NewInt(int64(ascender))
	if err := exact.checkInt(fontTerm, path+"/font_term"); err != nil {
		return 0, err
	}
	fontTerm.Mul(fontTerm, big.NewInt(fontSize.Numerator))
	if err := exact.checkInt(fontTerm, path+"/font_term"); err != nil {
		return 0, err
	}
	fontTerm.Mul(fontTerm, big.NewInt(lineHeight.Denominator))
	if err := exact.checkInt(fontTerm, path+"/font_term"); err != nil {
		return 0, err
	}
	lineTerm := new(big.Int).SetUint64(lineIndex)
	if err := exact.checkInt(lineTerm, path+"/line_term"); err != nil {
		return 0, err
	}
	lineTerm.Mul(lineTerm, big.NewInt(lineHeight.Numerator))
	if err := exact.checkInt(lineTerm, path+"/line_term"); err != nil {
		return 0, err
	}
	lineTerm.Mul(lineTerm, big.NewInt(fontSize.Denominator))
	if err := exact.checkInt(lineTerm, path+"/line_term"); err != nil {
		return 0, err
	}
	lineTerm.Mul(lineTerm, new(big.Int).SetUint64(uint64(unitsPerEm)))
	if err := exact.checkInt(lineTerm, path+"/line_term"); err != nil {
		return 0, err
	}
	numerator := new(big.Int).Add(fontTerm, lineTerm)
	if err := exact.checkInt(numerator, path); err != nil {
		return 0, err
	}
	numerator.Lsh(numerator, 6)
	if err := exact.checkInt(numerator, path); err != nil {
		return 0, err
	}
	denominator := big.NewInt(fontSize.Denominator)
	if err := exact.checkInt(denominator, path); err != nil {
		return 0, err
	}
	denominator.Mul(denominator, new(big.Int).SetUint64(uint64(unitsPerEm)))
	if err := exact.checkInt(denominator, path); err != nil {
		return 0, err
	}
	denominator.Mul(denominator, big.NewInt(lineHeight.Denominator))
	if err := exact.checkInt(denominator, path); err != nil {
		return 0, err
	}
	result, err := exact.roundFraction(numerator, denominator, path)
	return Q26_6(result), err
}

func (exact exactContext) halfEven(value int64, path string) (int64, error) {
	return exact.roundFraction(big.NewInt(value), big.NewInt(2), path)
}

func (exact exactContext) roundFraction(numerator, denominator *big.Int, path string) (int64, error) {
	if numerator == nil || denominator == nil || denominator.Sign() <= 0 {
		return 0, layoutError(CodeInputInvalid, path, ErrInputInvalid)
	}
	if err := exact.checkInt(numerator, path); err != nil {
		return 0, err
	}
	if err := exact.checkInt(denominator, path); err != nil {
		return 0, err
	}
	negative := numerator.Sign() < 0
	absolute := new(big.Int).Abs(new(big.Int).Set(numerator))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(absolute, denominator, remainder)
	doubled := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	if err := exact.checkInt(quotient, path); err != nil {
		return 0, err
	}
	if err := exact.checkInt(doubled, path); err != nil {
		return 0, err
	}
	comparison := doubled.Cmp(denominator)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative {
		quotient.Neg(quotient)
	}
	if err := exact.checkInt(quotient, path); err != nil {
		return 0, err
	}
	if !quotient.IsInt64() {
		return 0, layoutError(CodeExactLimit, path, ErrExactLimit)
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
