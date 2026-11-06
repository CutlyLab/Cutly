package videoedit

import (
	"errors"
	"fmt"
	"slices"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

var (
	// ErrRenderPlanInvalid means a serialized plan violates the strict v3 wire contract.
	ErrRenderPlanInvalid = errors.New("videoedit: render plan is invalid")
	// ErrRenderPlanFingerprintMismatch means semantic plan content was changed after compilation.
	ErrRenderPlanFingerprintMismatch = errors.New("videoedit: render plan fingerprint mismatch")
)

// DecodeRenderPlan strictly decodes and verifies a v3 semantic plan. Execution paths must rebuild
// v1/v2 plans from the committed Document rather than guessing a migration for their implicit
// clock and backend-bound fields.
func DecodeRenderPlan(raw []byte) (RenderPlan, error) {
	var plan RenderPlan
	if err := wirejson.Decode(raw, &plan); err != nil {
		return RenderPlan{}, fmt.Errorf("%w: decode: %v", ErrRenderPlanInvalid, err)
	}
	if err := validateRenderPlanForEvaluation(plan); err != nil {
		if errors.Is(err, ErrRenderPlanSchemaUnsupported) || errors.Is(err, ErrRenderPlanSemanticProfileUnsupported) {
			return RenderPlan{}, err
		}
		return RenderPlan{}, fmt.Errorf("%w: semantics: %v", ErrRenderPlanInvalid, err)
	}
	if !requirementsCanonical(plan.RequiredCapabilities) {
		return RenderPlan{}, fmt.Errorf("%w: required capabilities must be sorted and unique", ErrRenderPlanInvalid)
	}
	if plan.Fingerprint == "" {
		return RenderPlan{}, fmt.Errorf("%w: fingerprint is required", ErrRenderPlanInvalid)
	}
	input := plan
	input.Fingerprint = ""
	want, err := canonicalHash(input)
	if err != nil {
		return RenderPlan{}, fmt.Errorf("%w: fingerprint input: %v", ErrRenderPlanInvalid, err)
	}
	if plan.Fingerprint != want {
		return RenderPlan{}, fmt.Errorf("%w: got %q, want %q", ErrRenderPlanFingerprintMismatch, plan.Fingerprint, want)
	}
	return plan, nil
}

func requirementsCanonical(value CapabilityRequirements) bool {
	tracks := make(map[TrackKind]struct{}, len(value.TrackKinds))
	assets := make(map[AssetKind]struct{}, len(value.AssetKinds))
	features := make(map[CapabilityID]struct{}, len(value.Features))
	for _, item := range value.TrackKinds {
		tracks[item] = struct{}{}
	}
	for _, item := range value.AssetKinds {
		assets[item] = struct{}{}
	}
	for _, item := range value.Features {
		features[item] = struct{}{}
	}
	want := normalizedRequirements(tracks, assets, features)
	return slices.Equal(value.TrackKinds, want.TrackKinds) &&
		slices.Equal(value.AssetKinds, want.AssetKinds) &&
		slices.Equal(value.Features, want.Features)
}
