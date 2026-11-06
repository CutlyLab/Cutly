package documentv2

import (
	"testing"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestDissolveBoundaryConformanceVectors(t *testing.T) {
	type vectorSample struct {
		At       RationalTime    `json:"at"`
		Phase    TransitionPhase `json:"phase"`
		Progress *Rational       `json:"progress,omitempty"`
	}
	type vectors struct {
		Profile      string         `json:"profile"`
		Cut          RationalTime   `json:"cut"`
		PreDuration  RationalTime   `json:"pre_duration"`
		PostDuration RationalTime   `json:"post_duration"`
		Samples      []vectorSample `json:"samples"`
	}
	var fixture vectors
	if err := wirejson.Decode(readFixture(t, "transition-dissolve-v1.vectors.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	transition := Transition{Profile: fixture.Profile, PreDuration: fixture.PreDuration, PostDuration: fixture.PostDuration}
	for index, expected := range fixture.Samples {
		actual, err := SampleTransitionWindow(fixture.Cut, transition, expected.At)
		if err != nil {
			t.Fatalf("sample %d: %v", index, err)
		}
		if actual.Phase != expected.Phase || !equalOptionalRational(actual.Progress, expected.Progress) {
			t.Fatalf("sample %d = %+v, want %+v", index, actual, expected)
		}
	}
}

func TestZeroWidthTransitionSamplesAsHardCut(t *testing.T) {
	transition := Transition{PreDuration: RationalTime{Value: 0, Rate: 1}, PostDuration: RationalTime{Value: 0, Rate: 1}}
	before, err := SampleTransitionWindow(RationalTime{Value: 5, Rate: 1}, transition, RationalTime{Value: 499, Rate: 100})
	if err != nil || before.Phase != TransitionPhaseOutgoing {
		t.Fatalf("before cut = %+v err=%v", before, err)
	}
	atCut, err := SampleTransitionWindow(RationalTime{Value: 5, Rate: 1}, transition, RationalTime{Value: 5, Rate: 1})
	if err != nil || atCut.Phase != TransitionPhaseIncoming {
		t.Fatalf("at cut = %+v err=%v", atCut, err)
	}
}

func equalOptionalRational(left, right *Rational) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
