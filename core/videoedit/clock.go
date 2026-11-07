package videoedit

import (
	"errors"
	"fmt"
	"math/big"
)

var (
	// ErrInvalidFrameClock means a frame clock has an invalid origin or non-positive frame rate.
	ErrInvalidFrameClock = errors.New("videoedit: frame clock is invalid")
	// ErrInvalidSampleClock means a sample clock has an invalid origin or non-positive sample rate.
	ErrInvalidSampleClock = errors.New("videoedit: sample clock is invalid")
	// ErrInvalidTickRange means a compiled index range has a negative count or an end that cannot
	// be represented by the signed int64 wire contract.
	ErrInvalidTickRange = errors.New("videoedit: tick range is invalid")
)

// FrameClock fixes the composition-wide video grid. FramesPerSecond is an exact rate such as
// 30000/1001. Requested render ranges and worker chunks select indices from this clock; they never
// establish a new origin.
type FrameClock struct {
	Origin          RationalTime `json:"origin"`
	FramesPerSecond RationalTime `json:"frames_per_second"`
}

// Validate rejects clocks that cannot produce a strictly increasing frame grid.
func (clock FrameClock) Validate() error {
	if err := clock.Origin.Validate(); err != nil {
		return fmt.Errorf("%w: origin: %v", ErrInvalidFrameClock, err)
	}
	if err := clock.FramesPerSecond.Validate(); err != nil {
		return fmt.Errorf("%w: frames_per_second: %v", ErrInvalidFrameClock, err)
	}
	if clock.FramesPerSecond.Value <= 0 {
		return fmt.Errorf("%w: frames_per_second must be positive, got %d/%d", ErrInvalidFrameClock, clock.FramesPerSecond.Value, clock.FramesPerSecond.Rate)
	}
	return nil
}

// SampleClock fixes the composition-wide audio grid.
type SampleClock struct {
	Origin           RationalTime `json:"origin"`
	SamplesPerSecond int64        `json:"samples_per_second"`
}

// Validate rejects clocks that cannot produce a strictly increasing sample grid.
func (clock SampleClock) Validate() error {
	if err := clock.Origin.Validate(); err != nil {
		return fmt.Errorf("%w: origin: %v", ErrInvalidSampleClock, err)
	}
	if clock.SamplesPerSecond <= 0 {
		return fmt.Errorf("%w: samples_per_second must be positive, got %d", ErrInvalidSampleClock, clock.SamplesPerSecond)
	}
	return nil
}

// TickRange is a half-open range of composition-wide frame or sample indices.
type TickRange struct {
	First int64 `json:"first"`
	Count int64 `json:"count"`
}

// Validate checks the non-negative count and verifies that the exclusive end fits int64.
func (value TickRange) Validate() error {
	if value.Count < 0 {
		return fmt.Errorf("%w: count must not be negative, got %d", ErrInvalidTickRange, value.Count)
	}
	end := new(big.Int).Add(big.NewInt(value.First), big.NewInt(value.Count))
	if !end.IsInt64() {
		return fmt.Errorf("%w: exclusive end overflows int64", ErrInvalidTickRange)
	}
	return nil
}

// EndExclusive returns First+Count after overflow validation.
func (value TickRange) EndExclusive() (int64, error) {
	if err := value.Validate(); err != nil {
		return 0, err
	}
	return value.First + value.Count, nil
}

// Contains reports whether index lies in the half-open range.
func (value TickRange) Contains(index int64) (bool, error) {
	end, err := value.EndExclusive()
	if err != nil {
		return false, err
	}
	return index >= value.First && index < end, nil
}

// CompileFrameTickRange selects all composition frame indices whose tick starts lie in requested.
func CompileFrameTickRange(requested TimeRange, clock FrameClock) (TickRange, error) {
	if err := clock.Validate(); err != nil {
		return TickRange{}, err
	}
	rate := new(big.Rat).SetFrac64(clock.FramesPerSecond.Value, clock.FramesPerSecond.Rate)
	return compileTickRange(requested, clock.Origin, rate)
}

// CompileSampleTickRange selects all composition sample indices whose tick starts lie in requested.
func CompileSampleTickRange(requested TimeRange, clock SampleClock) (TickRange, error) {
	if err := clock.Validate(); err != nil {
		return TickRange{}, err
	}
	rate := new(big.Rat).SetInt64(clock.SamplesPerSecond)
	return compileTickRange(requested, clock.Origin, rate)
}

// FrameTimeAt returns the exact timeline time of a composition frame index.
func FrameTimeAt(clock FrameClock, compositionFrameIndex int64) (RationalTime, error) {
	if err := clock.Validate(); err != nil {
		return RationalTime{}, err
	}
	offset := new(big.Rat).Mul(
		new(big.Rat).SetInt64(compositionFrameIndex),
		new(big.Rat).SetFrac(big.NewInt(clock.FramesPerSecond.Rate), big.NewInt(clock.FramesPerSecond.Value)),
	)
	origin := new(big.Rat).SetFrac64(clock.Origin.Value, clock.Origin.Rate)
	return rationalFromBig(new(big.Rat).Add(origin, offset))
}

// SampleTimeAt returns the exact timeline time of a composition sample index.
func SampleTimeAt(clock SampleClock, compositionSampleIndex int64) (RationalTime, error) {
	if err := clock.Validate(); err != nil {
		return RationalTime{}, err
	}
	offset := new(big.Rat).SetFrac(big.NewInt(compositionSampleIndex), big.NewInt(clock.SamplesPerSecond))
	origin := new(big.Rat).SetFrac64(clock.Origin.Value, clock.Origin.Rate)
	return rationalFromBig(new(big.Rat).Add(origin, offset))
}

func compileTickRange(requested TimeRange, origin RationalTime, ticksPerSecond *big.Rat) (TickRange, error) {
	if err := requested.Validate(); err != nil {
		return TickRange{}, fmt.Errorf("compile tick range: requested range: %w", err)
	}
	if err := origin.Validate(); err != nil {
		return TickRange{}, fmt.Errorf("compile tick range: origin: %w", err)
	}
	if ticksPerSecond == nil || ticksPerSecond.Sign() <= 0 {
		return TickRange{}, errors.New("compile tick range: ticks per second must be positive")
	}

	end, err := requested.End()
	if err != nil {
		return TickRange{}, fmt.Errorf("compile tick range: requested end: %w", err)
	}
	firstValue := tickPosition(requested.Start, origin, ticksPerSecond)
	endValue := tickPosition(end, origin, ticksPerSecond)
	first, err := ceilBigRatToInt64(firstValue)
	if err != nil {
		return TickRange{}, fmt.Errorf("compile tick range first index: %w", err)
	}
	endExclusive, err := ceilBigRatToInt64(endValue)
	if err != nil {
		return TickRange{}, fmt.Errorf("compile tick range exclusive end: %w", err)
	}

	count := new(big.Int).Sub(big.NewInt(endExclusive), big.NewInt(first))
	if count.Sign() < 0 || !count.IsInt64() {
		return TickRange{}, fmt.Errorf("%w: compiled count is not representable", ErrInvalidTickRange)
	}
	result := TickRange{First: first, Count: count.Int64()}
	if err := result.Validate(); err != nil {
		return TickRange{}, err
	}
	return result, nil
}

func tickPosition(value, origin RationalTime, ticksPerSecond *big.Rat) *big.Rat {
	position := new(big.Rat).Sub(
		new(big.Rat).SetFrac64(value.Value, value.Rate),
		new(big.Rat).SetFrac64(origin.Value, origin.Rate),
	)
	return position.Mul(position, ticksPerSecond)
}

func ceilBigRatToInt64(value *big.Rat) (int64, error) {
	if value == nil {
		return 0, errors.New("videoedit: cannot ceil a nil rational")
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, ErrTimeOverflow
	}
	return quotient.Int64(), nil
}
