package videoedit

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrVerifiedRenderPlanRequired = errors.New("videoedit: verified render plan is required")

// VerifiedRenderPlan is an immutable, opaque execution handle. Verification deep-copies the plan
// through its strict wire decoder, so later mutation of caller-owned slices or property maps cannot
// change what evaluators consume.
type VerifiedRenderPlan struct {
	plan     RenderPlan
	verified bool
}

// VerifyRenderPlan performs strict semantic and fingerprint validation once and returns an opaque
// handle suitable for evaluating many frames or sample blocks.
func VerifyRenderPlan(plan RenderPlan) (VerifiedRenderPlan, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return VerifiedRenderPlan{}, fmt.Errorf("%w: encode: %v", ErrRenderPlanInvalid, err)
	}
	return DecodeVerifiedRenderPlan(raw)
}

// DecodeVerifiedRenderPlan combines strict wire decoding with the immutable evaluator handle.
func DecodeVerifiedRenderPlan(raw []byte) (VerifiedRenderPlan, error) {
	plan, err := DecodeRenderPlan(raw)
	if err != nil {
		return VerifiedRenderPlan{}, err
	}
	return VerifiedRenderPlan{plan: plan, verified: true}, nil
}

func (verified VerifiedRenderPlan) value() (RenderPlan, error) {
	if !verified.verified {
		return RenderPlan{}, ErrVerifiedRenderPlanRequired
	}
	return verified.plan, nil
}

// Fingerprint exposes only the immutable identity needed for cache and execution bindings.
func (verified VerifiedRenderPlan) Fingerprint() string {
	if !verified.verified {
		return ""
	}
	return verified.plan.Fingerprint
}
