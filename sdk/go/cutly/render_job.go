package cutly

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

// RenderJobSnapshotV1 is the strict scheduler receipt returned by render,
// status, and cancel actions.
type RenderJobSnapshotV1 = scheduler.JobSnapshot

// BuildRenderJobV1 creates the whole-plan public render job. The ranges and
// immutable fingerprints are copied from the verified plan and manifest so a
// caller cannot accidentally submit a job that addresses different work.
func BuildRenderJobV1(
	jobID, idempotencyKey string,
	plan RenderPlanV3,
	manifest ExecutionManifestV1,
	artifactURI string,
) (RenderJobV1, error) {
	planRaw, err := canonicaljson.Marshal(plan)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: encode render plan: %w", err)
	}
	verifiedPlan, err := videoedit.DecodeRenderPlan(planRaw)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: verify render plan: %w", err)
	}

	manifestRaw, err := canonicaljson.Marshal(manifest)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: encode execution manifest: %w", err)
	}
	verifiedManifest, err := renderapi.DecodeExecutionManifest(manifestRaw)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: verify execution manifest: %w", err)
	}
	refrozenManifest, err := renderapi.FreezeExecutionManifest(verifiedPlan, verifiedManifest)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: bind execution manifest to plan: %w", err)
	}
	refrozenRaw, err := canonicaljson.Marshal(refrozenManifest)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: encode refrozen execution manifest: %w", err)
	}
	if !bytes.Equal(manifestRaw, refrozenRaw) {
		return RenderJobV1{}, errors.New("cutly sdk: execution manifest is not the exact frozen value for the plan")
	}

	draft := renderapi.RenderJob{
		SchemaVersion:                renderapi.RenderJobSchemaVersion,
		ID:                           jobID,
		IdempotencyKey:               idempotencyKey,
		PlanFingerprint:              verifiedPlan.Fingerprint,
		ExecutionManifestFingerprint: verifiedManifest.Fingerprint,
		TimelineRange:                verifiedPlan.RequestedRange,
		FrameRange:                   verifiedPlan.FrameRange,
		SampleRange:                  verifiedPlan.SampleRange,
		ArtifactURI:                  artifactURI,
	}
	jobRaw, err := canonicaljson.Marshal(draft)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: encode render job: %w", err)
	}
	job, err := renderapi.DecodeRenderJob(jobRaw)
	if err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: build render job: %w", err)
	}
	if err := renderapi.ValidateRenderRequest(renderapi.RenderRequest{
		Job: job, Plan: verifiedPlan, Manifest: verifiedManifest,
	}); err != nil {
		return RenderJobV1{}, fmt.Errorf("cutly sdk: validate render job binding: %w", err)
	}
	return job, nil
}

// DecodeRenderJobSnapshotReceipt strictly decodes the canonical receipt
// shared by render, status, and cancel. It rejects semantically valid but
// non-canonical encodings so callers can safely retain or fingerprint bytes.
func DecodeRenderJobSnapshotReceipt(raw json.RawMessage) (RenderJobSnapshotV1, error) {
	if len(raw) == 0 || len(raw) > scheduler.MaxProtocolBytes {
		return RenderJobSnapshotV1{}, fmt.Errorf("%w: scheduler receipt encoded length %d", ErrReceiptInvalid, len(raw))
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return RenderJobSnapshotV1{}, fmt.Errorf("%w: scheduler receipt is not canonical: %v", ErrReceiptInvalid, err)
	}
	snapshot, err := scheduler.DecodeJobSnapshot(raw)
	if err != nil {
		return RenderJobSnapshotV1{}, fmt.Errorf("%w: scheduler receipt: %v", ErrReceiptInvalid, err)
	}
	return snapshot, nil
}

// ValidateRenderJobSnapshotReceipt binds a decoded scheduler receipt to the
// exact job submitted by the caller. DecodeRenderJobSnapshotReceipt remains a
// useful low-level shape boundary; callers that act on a status or terminal
// artifact must additionally use this helper so a valid receipt for another
// job cannot be accepted through a correlated Agent response.
func ValidateRenderJobSnapshotReceipt(snapshot RenderJobSnapshotV1, expected RenderJobV1) error {
	expectedRaw, err := canonicaljson.Marshal(expected)
	if err != nil {
		return fmt.Errorf("%w: encode expected render job: %v", ErrReceiptInvalid, err)
	}
	verifiedExpected, err := renderapi.DecodeRenderJob(expectedRaw)
	if err != nil || !reflect.DeepEqual(verifiedExpected, expected) {
		return fmt.Errorf("%w: expected render job: %v", ErrReceiptInvalid, err)
	}

	snapshotRaw, err := canonicaljson.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("%w: encode scheduler receipt: %v", ErrReceiptInvalid, err)
	}
	verifiedSnapshot, err := scheduler.DecodeJobSnapshot(snapshotRaw)
	if err != nil || !reflect.DeepEqual(verifiedSnapshot, snapshot) {
		return fmt.Errorf("%w: scheduler receipt: %v", ErrReceiptInvalid, err)
	}
	if !reflect.DeepEqual(verifiedSnapshot.Job, verifiedExpected) {
		return fmt.Errorf("%w: scheduler receipt job does not match the submitted job", ErrReceiptInvalid)
	}

	if verifiedSnapshot.State == renderapi.JobComplete {
		artifact := verifiedSnapshot.Artifact
		if artifact == nil {
			return fmt.Errorf("%w: complete scheduler receipt has no artifact", ErrReceiptInvalid)
		}
		evidenceURI, evidenceErr := renderapi.EvidenceSidecarURI(verifiedExpected.ArtifactURI)
		durationComparison, durationErr := artifact.Duration.Compare(verifiedExpected.TimelineRange.Duration)
		if evidenceErr != nil || durationErr != nil || durationComparison != 0 ||
			artifact.URI != verifiedExpected.ArtifactURI ||
			artifact.ExecutionManifestFingerprint != verifiedExpected.ExecutionManifestFingerprint ||
			artifact.EvidenceURI != evidenceURI ||
			artifact.FrameCount != verifiedExpected.FrameRange.Count ||
			artifact.SampleCount != verifiedExpected.SampleRange.Count {
			return fmt.Errorf("%w: terminal artifact does not match the submitted job", ErrReceiptInvalid)
		}
	}
	return nil
}
