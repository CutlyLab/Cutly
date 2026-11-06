package prepared

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

type record struct {
	SchemaVersion                int             `json:"schema_version"`
	Profile                      string          `json:"profile"`
	PlanFingerprint              string          `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string          `json:"execution_manifest_fingerprint"`
	Plan                         json.RawMessage `json:"plan"`
	ExecutionManifest            json.RawMessage `json:"execution_manifest"`
}

func freezeEnvironment(plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest, maximum int) (Environment, Key, []byte, error) {
	planRaw, err := canonicaljson.Marshal(plan)
	if err != nil {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: encode plan: %v", ErrInvalidEnvironment, err)
	}
	frozenPlan, err := videoedit.DecodeRenderPlan(planRaw)
	if err != nil {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: plan: %v", ErrInvalidEnvironment, err)
	}
	manifestRaw, err := canonicaljson.Marshal(manifest)
	if err != nil {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: encode manifest: %v", ErrInvalidEnvironment, err)
	}
	frozenManifest, err := renderapi.DecodeExecutionManifest(manifestRaw)
	if err != nil {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: manifest: %v", ErrInvalidEnvironment, err)
	}
	refrozen, err := renderapi.FreezeExecutionManifest(frozenPlan, frozenManifest)
	if err != nil {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: bind manifest: %v", ErrInvalidEnvironment, err)
	}
	refrozenRaw, err := canonicaljson.Marshal(refrozen)
	if err != nil || !bytes.Equal(manifestRaw, refrozenRaw) {
		return Environment{}, Key{}, nil, errors.Join(
			ErrInvalidEnvironment,
			errors.New("manifest is not the exact frozen environment for the plan"),
			err,
		)
	}
	value := record{
		SchemaVersion: RecordSchemaVersion, Profile: RecordProfileV1,
		PlanFingerprint: frozenPlan.Fingerprint, ExecutionManifestFingerprint: frozenManifest.Fingerprint,
		Plan: planRaw, ExecutionManifest: manifestRaw,
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: encode record: %v", ErrInvalidEnvironment, err)
	}
	if len(raw) == 0 || len(raw) > maximum {
		return Environment{}, Key{}, nil, fmt.Errorf("%w: record bytes %d exceed %d", ErrLimit, len(raw), maximum)
	}
	return Environment{Plan: frozenPlan, Manifest: frozenManifest}, Key{
		PlanFingerprint: frozenPlan.Fingerprint, ExecutionManifestFingerprint: frozenManifest.Fingerprint,
	}, raw, nil
}

func decodeRecord(raw []byte, maximum int) (Environment, Key, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return Environment{}, Key{}, fmt.Errorf("%w: record bytes %d exceed [1,%d]", ErrCorrupt, len(raw), maximum)
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = maximum
	limits.MaxStringBytes = maximum
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Environment{}, Key{}, errors.Join(ErrCorrupt, errors.New("record is not canonical"), err)
	}
	var value record
	if err := wirejson.Decode(raw, &value); err != nil {
		return Environment{}, Key{}, fmt.Errorf("%w: strict record decode: %v", ErrCorrupt, err)
	}
	if value.SchemaVersion != RecordSchemaVersion || value.Profile != RecordProfileV1 ||
		!cachekey.CanonicalSHA256(value.PlanFingerprint) || !cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) {
		return Environment{}, Key{}, fmt.Errorf("%w: record identity", ErrCorrupt)
	}
	plan, err := videoedit.DecodeRenderPlan(value.Plan)
	if err != nil {
		return Environment{}, Key{}, fmt.Errorf("%w: plan: %v", ErrCorrupt, err)
	}
	manifest, err := renderapi.DecodeExecutionManifest(value.ExecutionManifest)
	if err != nil {
		return Environment{}, Key{}, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	if plan.Fingerprint != value.PlanFingerprint || manifest.Fingerprint != value.ExecutionManifestFingerprint ||
		manifest.PlanFingerprint != plan.Fingerprint {
		return Environment{}, Key{}, fmt.Errorf("%w: record fingerprint binding", ErrCorrupt)
	}
	refrozen, err := renderapi.FreezeExecutionManifest(plan, manifest)
	if err != nil {
		return Environment{}, Key{}, fmt.Errorf("%w: manifest binding: %v", ErrCorrupt, err)
	}
	refrozenRaw, err := canonicaljson.Marshal(refrozen)
	if err != nil || !bytes.Equal(refrozenRaw, value.ExecutionManifest) {
		return Environment{}, Key{}, errors.Join(ErrCorrupt, errors.New("manifest is not the exact frozen environment for the plan"), err)
	}
	want, err := canonicaljson.Marshal(value)
	if err != nil || !bytes.Equal(want, raw) {
		return Environment{}, Key{}, errors.Join(ErrCorrupt, errors.New("record encoding drift"), err)
	}
	return Environment{Plan: plan, Manifest: manifest}, Key{
		PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
	}, nil
}
