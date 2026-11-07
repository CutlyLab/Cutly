package videoedit

import (
	"errors"
	"fmt"
	"math/big"
)

var (
	// ErrInvalidRate means a time value has no usable time base. Stored values must always have a
	// positive rate; NewRationalTime and Normalize accept a negative denominator and move its sign
	// to Value before returning the canonical representation.
	ErrInvalidRate = errors.New("videoedit: time rate must be non-zero")
	// ErrTimeOverflow means an exact rational result cannot be represented by int64 Value/Rate.
	ErrTimeOverflow = errors.New("videoedit: rational time overflow")
	// ErrInexactRescale means the requested time base cannot represent the value as an integer
	// number of ticks. Refusing to round here prevents small edits from accumulating A/V drift.
	ErrInexactRescale = errors.New("videoedit: inexact time rescale")
	// ErrNegativeDuration means a TimeRange extends backwards.
	ErrNegativeDuration = errors.New("videoedit: duration must not be negative")
)

// RationalTime is an exact number of seconds expressed as Value/Rate. Rate is also the time base:
// a frame in 30000/1001 video is {Value: 1001, Rate: 30000}; an audio sample at 48 kHz is
// {Value: 1, Rate: 48000}.
//
// Arithmetic returns a normalized fraction. Rescale intentionally preserves the requested Rate so
// callers can address a concrete frame or sample clock, even when that representation is reducible.
type RationalTime struct {
	Value int64 `json:"value"`
	Rate  int64 `json:"rate"`
}

// NewRationalTime constructs the canonical reduced representation of value/rate.
func NewRationalTime(value, rate int64) (RationalTime, error) {
	return RationalTime{Value: value, Rate: rate}.Normalize()
}

// Validate checks whether t is safe to use as a stored time value. A valid value need not be
// reduced because a deliberately selected media time base (for example 48000 audio ticks/second)
// is meaningful to callers.
func (t RationalTime) Validate() error {
	if t.Rate <= 0 {
		return fmt.Errorf("%w: got %d", ErrInvalidRate, t.Rate)
	}
	return nil
}

// Normalize returns an equivalent reduced fraction with a positive Rate. Zero has the sole
// canonical representation 0/1.
func (t RationalTime) Normalize() (RationalTime, error) {
	if t.Rate == 0 {
		return RationalTime{}, fmt.Errorf("%w: got 0", ErrInvalidRate)
	}
	r := new(big.Rat).SetFrac(big.NewInt(t.Value), big.NewInt(t.Rate))
	return rationalFromBig(r)
}

// Add returns t+other exactly, or ErrTimeOverflow when the reduced result does not fit int64.
func (t RationalTime) Add(other RationalTime) (RationalTime, error) {
	a, b, err := checkedRationals(t, other)
	if err != nil {
		return RationalTime{}, err
	}
	return rationalFromBig(new(big.Rat).Add(a, b))
}

// Sub returns t-other exactly, or ErrTimeOverflow when the reduced result does not fit int64.
func (t RationalTime) Sub(other RationalTime) (RationalTime, error) {
	a, b, err := checkedRationals(t, other)
	if err != nil {
		return RationalTime{}, err
	}
	return rationalFromBig(new(big.Rat).Sub(a, b))
}

// Compare returns -1, 0, or 1 according to whether t is less than, equal to, or greater than
// other. The comparison uses arbitrary precision intermediates and therefore cannot overflow.
func (t RationalTime) Compare(other RationalTime) (int, error) {
	a, b, err := checkedRationals(t, other)
	if err != nil {
		return 0, err
	}
	return a.Cmp(b), nil
}

// Rescale expresses t as an integer number of ticks at targetRate while preserving its exact
// value. It returns ErrInexactRescale instead of rounding. For example, one 30000/1001 frame is
// not an integer number of 48 kHz samples, while five such frames are exactly 8008 samples.
func (t RationalTime) Rescale(targetRate int64) (RationalTime, error) {
	if err := t.Validate(); err != nil {
		return RationalTime{}, err
	}
	if targetRate <= 0 {
		return RationalTime{}, fmt.Errorf("%w: got %d", ErrInvalidRate, targetRate)
	}

	scaled := new(big.Int).Mul(big.NewInt(t.Value), big.NewInt(targetRate))
	value, remainder := new(big.Int), new(big.Int)
	value.QuoRem(scaled, big.NewInt(t.Rate), remainder)
	if remainder.Sign() != 0 {
		return RationalTime{}, fmt.Errorf("%w: %d/%d at rate %d", ErrInexactRescale, t.Value, t.Rate, targetRate)
	}
	if !value.IsInt64() {
		return RationalTime{}, ErrTimeOverflow
	}
	return RationalTime{Value: value.Int64(), Rate: targetRate}, nil
}

// TimeRange is a half-open interval [Start, Start+Duration). Start may be negative for preroll,
// but Duration must not be negative.
type TimeRange struct {
	Start    RationalTime `json:"start"`
	Duration RationalTime `json:"duration"`
}

// Validate checks both time bases, rejects a backwards range, and requires a representable end.
func (r TimeRange) Validate() error {
	if err := r.Start.Validate(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := r.Duration.Validate(); err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	if r.Duration.Value < 0 {
		return ErrNegativeDuration
	}
	if _, err := r.Start.Add(r.Duration); err != nil {
		return fmt.Errorf("end: %w", err)
	}
	return nil
}

// End returns the exclusive end of r.
func (r TimeRange) End() (RationalTime, error) {
	if err := r.Validate(); err != nil {
		return RationalTime{}, err
	}
	return r.Start.Add(r.Duration)
}

// scaleTimeByRatio multiplies a time value by the dimensionless ratio numerator/denominator.
// It is intentionally package-private until playback-rate capabilities are part of the public
// document schema. Render-plan compilation and evaluation share it so visible source ranges and
// per-frame source positions cannot disagree for linearly retimed clips.
func scaleTimeByRatio(value, numerator, denominator RationalTime) (RationalTime, error) {
	for _, candidate := range []struct {
		name  string
		value RationalTime
	}{
		{name: "value", value: value},
		{name: "numerator", value: numerator},
		{name: "denominator", value: denominator},
	} {
		if err := candidate.value.Validate(); err != nil {
			return RationalTime{}, fmt.Errorf("%s: %w", candidate.name, err)
		}
	}
	if denominator.Value == 0 {
		return RationalTime{}, errors.New("videoedit: scale denominator must not be zero")
	}

	result := new(big.Rat).SetFrac64(value.Value, value.Rate)
	result.Mul(result, new(big.Rat).SetFrac64(numerator.Value, numerator.Rate))
	result.Quo(result, new(big.Rat).SetFrac64(denominator.Value, denominator.Rate))
	return rationalFromBig(result)
}

func checkedRationals(a, b RationalTime) (*big.Rat, *big.Rat, error) {
	if err := a.Validate(); err != nil {
		return nil, nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, nil, err
	}
	return new(big.Rat).SetFrac64(a.Value, a.Rate), new(big.Rat).SetFrac64(b.Value, b.Rate), nil
}

func rationalFromBig(r *big.Rat) (RationalTime, error) {
	num, den := r.Num(), r.Denom()
	if !num.IsInt64() || !den.IsInt64() {
		return RationalTime{}, ErrTimeOverflow
	}
	return RationalTime{Value: num.Int64(), Rate: den.Int64()}, nil
}
