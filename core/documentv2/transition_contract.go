package documentv2

import (
	"errors"
	"math/big"
)

var ErrTransitionWindowInvalid = errors.New("documentv2: invalid transition window sample")

type TransitionPhase string

const (
	TransitionPhaseOutgoing TransitionPhase = "outgoing_only"
	TransitionPhaseActive   TransitionPhase = "transition"
	TransitionPhaseIncoming TransitionPhase = "incoming_only"
)

type TransitionWindowSample struct {
	Phase    TransitionPhase
	Progress *Rational
}

// SampleTransitionWindow is the shared exact boundary contract for preview/export evaluators.
// The active interval is [cut-pre,cut+post); progress is exact and the end is incoming-only. A
// zero-width transition is the schema spelling of a hard cut.
func SampleTransitionWindow(cut RationalTime, transition Transition, at RationalTime) (TransitionWindowSample, error) {
	if cut.Validate() != nil || at.Validate() != nil || transition.PreDuration.Validate() != nil || transition.PostDuration.Validate() != nil ||
		transition.PreDuration.Value < 0 || transition.PostDuration.Value < 0 {
		return TransitionWindowSample{}, ErrTransitionWindowInvalid
	}
	pre := timeBig(transition.PreDuration)
	post := timeBig(transition.PostDuration)
	total := new(big.Rat).Add(pre, post)
	cutValue := timeBig(cut)
	atValue := timeBig(at)
	if total.Sign() == 0 {
		if atValue.Cmp(cutValue) < 0 {
			return TransitionWindowSample{Phase: TransitionPhaseOutgoing}, nil
		}
		return TransitionWindowSample{Phase: TransitionPhaseIncoming}, nil
	}
	start := new(big.Rat).Sub(cutValue, pre)
	end := new(big.Rat).Add(cutValue, post)
	if atValue.Cmp(start) < 0 {
		return TransitionWindowSample{Phase: TransitionPhaseOutgoing}, nil
	}
	if atValue.Cmp(end) >= 0 {
		return TransitionWindowSample{Phase: TransitionPhaseIncoming}, nil
	}
	progressBig := new(big.Rat).Quo(new(big.Rat).Sub(atValue, start), total)
	progress, err := rationalFromBigValue(progressBig)
	if err != nil {
		return TransitionWindowSample{}, err
	}
	return TransitionWindowSample{Phase: TransitionPhaseActive, Progress: &progress}, nil
}

func rationalFromBigValue(value *big.Rat) (Rational, error) {
	if value == nil || !value.Num().IsInt64() || !value.Denom().IsInt64() {
		return Rational{}, ErrTransitionWindowInvalid
	}
	result := Rational{Numerator: value.Num().Int64(), Denominator: value.Denom().Int64()}
	if result.Validate() != nil {
		return Rational{}, ErrTransitionWindowInvalid
	}
	return result, nil
}
