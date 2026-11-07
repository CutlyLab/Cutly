package cpuraster

import (
	"fmt"
	"math/big"
)

// piDecimalV1 has 101 significant decimal digits (the leading 3 plus 100 fractional digits).
// Parsing it as a rational is exact; the decimal itself, rather than an implementation's binary
// floating-point pi, is the normative TrigProfileV1 constant.
const piDecimalV1 = "3.1415926535897932384626433832795028841971693993751058209749445923078164062862089986280348253421170679"

var (
	ratZero        = new(big.Rat)
	ratOne         = new(big.Rat).SetInt64(1)
	ratHalf        = new(big.Rat).SetFrac64(1, 2)
	ratNinety      = new(big.Rat).SetInt64(90)
	ratMinusNinety = new(big.Rat).SetInt64(-90)
	ratOneEighty   = new(big.Rat).SetInt64(180)
)

func sinCosQ48(degrees *big.Rat) (*big.Rat, *big.Rat, error) {
	if degrees == nil {
		return nil, nil, rasterError(CodeGeometryInvalid, "transform.rotation_degrees", ErrGeometryInvalid)
	}
	reduced := reduceDegrees(degrees)
	cosineSign := int64(1)
	switch {
	case reduced.Cmp(ratNinety) > 0:
		reduced.Sub(new(big.Rat).Set(ratOneEighty), reduced)
		cosineSign = -1
	case reduced.Cmp(ratMinusNinety) < 0:
		reduced.Sub(new(big.Rat).Neg(new(big.Rat).Set(ratOneEighty)), reduced)
		cosineSign = -1
	}
	// Exact quadrant angles have exact Q48 results. Returning them directly is semantically
	// identical to the frozen Taylor/quantization algorithm and removes hundreds of arbitrary-
	// precision allocations from the overwhelmingly common 0° identity path.
	if reduced.Sign() == 0 {
		return new(big.Rat), new(big.Rat).SetInt64(cosineSign), nil
	}
	if reduced.Cmp(ratNinety) == 0 {
		return new(big.Rat).SetInt64(1), new(big.Rat), nil
	}
	if reduced.Cmp(ratMinusNinety) == 0 {
		return new(big.Rat).SetInt64(-1), new(big.Rat), nil
	}
	pi, ok := new(big.Rat).SetString(piDecimalV1)
	if !ok {
		return nil, nil, rasterError(CodeGeometryInvalid, "trig.pi", ErrGeometryInvalid)
	}
	x := new(big.Rat).Mul(reduced, pi)
	x.Quo(x, ratOneEighty)
	xSquared := new(big.Rat).Mul(new(big.Rat).Set(x), x)

	sineTerm := new(big.Rat).Set(x)
	sine := new(big.Rat).Set(sineTerm)
	cosineTerm := new(big.Rat).Set(ratOne)
	cosine := new(big.Rat).Set(cosineTerm)
	for term := int64(1); term < TaylorTerms; term++ {
		// sin term_k = term_(k-1) * -x^2 / ((2k)*(2k+1))
		sineDenominator := new(big.Rat).SetInt64((2 * term) * (2*term + 1))
		sineTerm.Mul(sineTerm, xSquared)
		sineTerm.Quo(sineTerm, sineDenominator)
		sineTerm.Neg(sineTerm)
		sine.Add(sine, sineTerm)

		// cos term_k = term_(k-1) * -x^2 / ((2k-1)*(2k))
		cosineDenominator := new(big.Rat).SetInt64((2*term - 1) * (2 * term))
		cosineTerm.Mul(cosineTerm, xSquared)
		cosineTerm.Quo(cosineTerm, cosineDenominator)
		cosineTerm.Neg(cosineTerm)
		cosine.Add(cosine, cosineTerm)
	}
	if cosineSign < 0 {
		cosine.Neg(cosine)
	}
	sineQ, err := quantizeSignedQ(sine, TrigFractionBits)
	if err != nil {
		return nil, nil, rasterError(CodeGeometryInvalid, "trig.sin", err)
	}
	cosineQ, err := quantizeSignedQ(cosine, TrigFractionBits)
	if err != nil {
		return nil, nil, rasterError(CodeGeometryInvalid, "trig.cos", err)
	}
	denominator := int64(1) << TrigFractionBits
	return new(big.Rat).SetFrac64(sineQ, denominator), new(big.Rat).SetFrac64(cosineQ, denominator), nil
}

func reduceDegrees(value *big.Rat) *big.Rat {
	denominator := new(big.Int).Set(value.Denom())
	period := new(big.Int).Mul(big.NewInt(360), denominator)
	remainder := new(big.Int).Mod(new(big.Int).Set(value.Num()), period)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(period) >= 0 {
		remainder.Sub(remainder, period)
	}
	return new(big.Rat).SetFrac(remainder, denominator)
}

func quantizeSignedQ(value *big.Rat, fractionBits uint) (int64, error) {
	if value == nil || value.Denom().Sign() <= 0 {
		return 0, ErrGeometryInvalid
	}
	numerator := new(big.Int).Set(value.Num())
	sign := numerator.Sign()
	numerator.Abs(numerator)
	numerator.Lsh(numerator, fractionBits)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, value.Denom(), remainder)
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := twiceRemainder.Cmp(value.Denom())
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if sign < 0 {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("%w: Q%d result does not fit int64", ErrGeometryInvalid, fractionBits)
	}
	return quotient.Int64(), nil
}
