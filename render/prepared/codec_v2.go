package prepared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/internal/jsonadmission"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// recordV2 persists one complete Document v2 environment: schema/profile identity, the three
// binding fingerprints, and the canonical Document, plan and execution-manifest snapshots.
type recordV2 struct {
	SchemaVersion                int             `json:"schema_version"`
	Profile                      string          `json:"profile"`
	DocumentHash                 string          `json:"document_hash"`
	PlanFingerprint              string          `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string          `json:"execution_manifest_fingerprint"`
	Document                     json.RawMessage `json:"document"`
	Plan                         json.RawMessage `json:"plan"`
	ExecutionManifest            json.RawMessage `json:"execution_manifest"`
}

// freezeEnvironmentV2 runs the complete S3 admission on Put: canonicalize and strict-decode the
// Document, recompute its hash, rebind the plan through a fresh S1 Resolve requiring canonical
// plan equality, re-freeze the manifest through S2 requiring canonical manifest equality,
// cross-check every identity, and return fully owned values. No component is admitted before
// its combined size has been checked against the v2 record limit.
func freezeEnvironmentV2(
	ctx context.Context,
	document documentv2.Document,
	plan renderplanv2.Plan,
	manifest renderapi.ExecutionManifestV2,
	maximum int,
) (EnvironmentV2, Key, []byte, error) {
	// Typed-graph admission before any component canonicalization: a caller holding a
	// structurally valid but oversized in-memory document must not force full-size transient
	// allocations before the v2 record limit rejects it. The bound is the smaller of the v2
	// record limit and the Document v2 canonical ceiling.
	documentAdmission := uint64(maximum)
	if documentAdmission > renderplanv2.MaxCanonicalDocumentBytes {
		documentAdmission = renderplanv2.MaxCanonicalDocumentBytes
	}
	if err := jsonadmission.Check(ctx, document, documentAdmission, renderplanv2.MaxDocumentAdmissionValues); err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: document admission: %v", ErrLimit, err)
	}
	documentRaw, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: encode document: %v", ErrInvalidEnvironment, err)
	}
	frozenDocument, err := documentv2.Decode(documentRaw)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: document: %v", ErrInvalidEnvironment, err)
	}
	documentDigest := sha256.Sum256(documentRaw)
	documentHash := "sha256:" + hex.EncodeToString(documentDigest[:])

	planRaw, err := renderplanv2.CanonicalContext(ctx, plan)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: encode plan: %v", ErrInvalidEnvironment, err)
	}
	frozenPlan, err := renderplanv2.Decode(planRaw)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: plan: %v", ErrInvalidEnvironment, err)
	}
	bound, err := renderplanv2.VerifyAgainstDocument(ctx, frozenPlan, frozenDocument)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: plan is not the document-bound resolve: %v", ErrInvalidEnvironment, err)
	}

	verifiedManifest, err := renderapi.FreezeExecutionManifestV2(ctx, bound, manifest)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: bind manifest: %v", ErrInvalidEnvironment, err)
	}
	frozenManifest, err := verifiedManifest.Snapshot()
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: manifest snapshot: %v", ErrInvalidEnvironment, err)
	}
	manifestRaw, err := renderapi.CanonicalExecutionManifestV2(frozenManifest)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: encode manifest: %v", ErrInvalidEnvironment, err)
	}
	// The caller's manifest must already be the exact S2-frozen artifact for this plan. A draft,
	// or a manifest frozen for any other plan, re-freezes to different canonical bytes and is
	// rejected instead of being silently normalized.
	inputManifestRaw, err := renderapi.CanonicalExecutionManifestV2(manifest)
	if err != nil || !bytes.Equal(inputManifestRaw, manifestRaw) {
		return EnvironmentV2{}, Key{}, nil, errors.Join(
			ErrInvalidEnvironment,
			errors.New("manifest is not the exact frozen environment for the plan"),
			err,
		)
	}

	if err := crossCheckV2(frozenPlan, frozenManifest, documentHash); err != nil {
		return EnvironmentV2{}, Key{}, nil, err
	}
	// Admission before the combined record allocation: the three canonical components plus the
	// typed envelope must fit the v2 limit before the record is assembled.
	const envelopeAllowance = 4096
	if len(documentRaw) > maximum || len(planRaw) > maximum || len(manifestRaw) > maximum ||
		len(documentRaw)+len(planRaw)+len(manifestRaw)+envelopeAllowance > maximum {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: v2 record components exceed %d", ErrLimit, maximum)
	}
	value := recordV2{
		SchemaVersion: RecordSchemaVersionV2, Profile: RecordProfileV2,
		DocumentHash:                 documentHash,
		PlanFingerprint:              frozenPlan.Fingerprint,
		ExecutionManifestFingerprint: frozenManifest.Fingerprint,
		Document:                     documentRaw, Plan: planRaw, ExecutionManifest: manifestRaw,
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: encode v2 record: %v", ErrInvalidEnvironment, err)
	}
	if len(raw) == 0 || len(raw) > maximum {
		return EnvironmentV2{}, Key{}, nil, fmt.Errorf("%w: v2 record bytes %d exceed %d", ErrLimit, len(raw), maximum)
	}
	return EnvironmentV2{Document: frozenDocument, Plan: frozenPlan, Manifest: frozenManifest}, Key{
		PlanFingerprint: frozenPlan.Fingerprint, ExecutionManifestFingerprint: frozenManifest.Fingerprint,
	}, raw, nil
}

// decodeRecordV2 re-runs the complete S3 reverification on read: the record must already be
// canonical, every component strict-decodes, the plan must equal a fresh S1 Resolve of the
// stored Document, the manifest must equal a fresh S2 freeze, and all identities must bind.
func decodeRecordV2(ctx context.Context, raw []byte, maximum int) (EnvironmentV2, Key, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: v2 record bytes %d exceed [1,%d]", ErrCorrupt, len(raw), maximum)
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = maximum
	limits.MaxStringBytes = maximum
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil || !bytes.Equal(canonical, raw) {
		return EnvironmentV2{}, Key{}, errors.Join(ErrCorrupt, errors.New("v2 record is not canonical"), err)
	}
	var value recordV2
	if err := wirejson.Decode(raw, &value); err != nil {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: strict v2 record decode: %v", ErrCorrupt, err)
	}
	if value.SchemaVersion != RecordSchemaVersionV2 || value.Profile != RecordProfileV2 ||
		!cachekey.CanonicalSHA256(value.DocumentHash) || !cachekey.CanonicalSHA256(value.PlanFingerprint) ||
		!cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: v2 record identity", ErrCorrupt)
	}
	document, err := documentv2.Decode(value.Document)
	if err != nil {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: document: %v", ErrCorrupt, err)
	}
	plan, err := renderplanv2.Decode(value.Plan)
	if err != nil {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: plan: %v", ErrCorrupt, err)
	}
	manifest, err := renderapi.DecodeExecutionManifestV2(value.ExecutionManifest)
	if err != nil {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	bound, err := renderplanv2.VerifyAgainstDocument(ctx, plan, document)
	if err != nil {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: stored plan does not re-resolve from the stored document: %v", ErrCorrupt, err)
	}
	verified, err := renderapi.VerifyExecutionManifestV2(ctx, bound, manifest)
	if err != nil {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: stored manifest does not re-freeze for the stored plan: %v", ErrCorrupt, err)
	}
	if plan.Fingerprint != value.PlanFingerprint || manifest.Fingerprint != value.ExecutionManifestFingerprint ||
		verified.Fingerprint() != manifest.Fingerprint {
		return EnvironmentV2{}, Key{}, fmt.Errorf("%w: v2 record fingerprint binding", ErrCorrupt)
	}
	if err := crossCheckV2(plan, manifest, value.DocumentHash); err != nil {
		return EnvironmentV2{}, Key{}, err
	}
	want, err := canonicaljson.Marshal(value)
	if err != nil || !bytes.Equal(want, raw) {
		return EnvironmentV2{}, Key{}, errors.Join(ErrCorrupt, errors.New("v2 record encoding drift"), err)
	}
	return EnvironmentV2{Document: document, Plan: plan, Manifest: manifest}, Key{
		PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
	}, nil
}

// crossCheckV2 enforces the S3 identity lattice: plan/document hash equality and the manifest's
// plan-fingerprint binding.
func crossCheckV2(plan renderplanv2.Plan, manifest renderapi.ExecutionManifestV2, documentHash string) error {
	if !cachekey.CanonicalSHA256(plan.Fingerprint) || !cachekey.CanonicalSHA256(manifest.Fingerprint) {
		return fmt.Errorf("%w: plan or manifest fingerprint is not canonical", ErrInvalidEnvironment)
	}
	if plan.DocumentHash != documentHash || manifest.DocumentHash != documentHash || manifest.PlanFingerprint != plan.Fingerprint {
		return fmt.Errorf("%w: document hash %s does not bind plan %s and manifest %s", ErrInvalidEnvironment, documentHash, plan.DocumentHash, manifest.PlanFingerprint)
	}
	return nil
}
