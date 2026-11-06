package documentv2

import (
	"errors"
	"math/big"
)

var ErrRationalNotCanonical = errors.New("documentv2: rational is not canonical")

func (value Rational) Validate() error {
	if value.Denominator <= 0 {
		return ErrRationalNotCanonical
	}
	if value.Numerator == 0 {
		if value.Denominator != 1 {
			return ErrRationalNotCanonical
		}
		return nil
	}
	a := new(big.Int).SetInt64(value.Numerator)
	a.Abs(a)
	b := new(big.Int).SetInt64(value.Denominator)
	if new(big.Int).GCD(nil, nil, a, b).Cmp(big.NewInt(1)) != 0 {
		return ErrRationalNotCanonical
	}
	return nil
}

func canonicalRational(numerator, denominator int64) (Rational, error) {
	if denominator <= 0 {
		return Rational{}, ErrRationalNotCanonical
	}
	if numerator == 0 {
		return Rational{Numerator: 0, Denominator: 1}, nil
	}
	n := new(big.Int).SetInt64(numerator)
	d := new(big.Int).SetInt64(denominator)
	gcd := new(big.Int).GCD(nil, nil, new(big.Int).Abs(new(big.Int).Set(n)), d)
	n.Quo(n, gcd)
	d.Quo(d, gcd)
	if !n.IsInt64() || !d.IsInt64() {
		return Rational{}, ErrRationalNotCanonical
	}
	return Rational{Numerator: n.Int64(), Denominator: d.Int64()}, nil
}

func rationalBig(value Rational) *big.Rat {
	// Validation is intentionally repeated at this low-level boundary. Validate and Decode are
	// public hostile-input surfaces, and no caller mistake may turn a malformed denominator into
	// big.Rat's divide-by-zero panic.
	if value.Validate() != nil {
		return new(big.Rat)
	}
	return new(big.Rat).SetFrac64(value.Numerator, value.Denominator)
}

func timeBig(value RationalTime) *big.Rat {
	if value.Validate() != nil {
		return new(big.Rat)
	}
	return new(big.Rat).SetFrac64(value.Value, value.Rate)
}

func rationalCompare(value Rational, numerator, denominator int64) int {
	return rationalBig(value).Cmp(new(big.Rat).SetFrac64(numerator, denominator))
}

func rationalAdd(left, right Rational) *big.Rat {
	return new(big.Rat).Add(rationalBig(left), rationalBig(right))
}
