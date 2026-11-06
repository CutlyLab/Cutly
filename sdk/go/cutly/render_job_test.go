package cutly

import (
	"bytes"
	"errors"
	"testing"

	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

func TestBuildRenderJobV1BindsVerifiedPlanAndManifest(t *testing.T) {
	plan, manifest, _ := renderPrepareSDKFixture(t)
	job, err := BuildRenderJobV1("job-1", "job-1-once", plan, manifest, "file:///tmp/cutly-job-1.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if job.SchemaVersion != renderapi.RenderJobSchemaVersion || job.ID != "job-1" ||
		job.IdempotencyKey != "job-1-once" || job.PlanFingerprint != plan.Fingerprint ||
		job.ExecutionManifestFingerprint != manifest.Fingerprint || job.TimelineRange != plan.RequestedRange ||
		job.FrameRange != plan.FrameRange || job.SampleRange != plan.SampleRange ||
		job.ArtifactURI != "file:///tmp/cutly-job-1.mp4" {
		t.Fatalf("job is not bound to plan/manifest: %+v", job)
	}
	if err := renderapi.ValidateRenderRequest(renderapi.RenderRequest{Job: job, Plan: plan, Manifest: manifest}); err != nil {
		t.Fatalf("built job is not executable: %v", err)
	}

	tamperedRange := job
	tamperedRange.FrameRange.Count++
	if err := renderapi.ValidateRenderRequest(renderapi.RenderRequest{
		Job: tamperedRange, Plan: plan, Manifest: manifest,
	}); err == nil {
		t.Fatal("tampered job frame range was accepted")
	}
}

func TestBuildRenderJobV1RejectsUnfrozenOrInvalidInputs(t *testing.T) {
	plan, manifest, _ := renderPrepareSDKFixture(t)
	tests := []struct {
		name        string
		plan        RenderPlanV3
		manifest    ExecutionManifestV1
		artifactURI string
	}{
		{name: "plan fingerprint", plan: func() RenderPlanV3 {
			value := plan
			value.Fingerprint = digestString('0')
			return value
		}(), manifest: manifest, artifactURI: "file:///tmp/out.mp4"},
		{name: "manifest fingerprint", plan: plan, manifest: func() ExecutionManifestV1 {
			value := manifest
			value.Fingerprint = digestString('0')
			return value
		}(), artifactURI: "file:///tmp/out.mp4"},
		{name: "manifest plan binding", plan: plan, manifest: func() ExecutionManifestV1 {
			value := manifest
			value.PlanFingerprint = digestString('0')
			return value
		}(), artifactURI: "file:///tmp/out.mp4"},
		{name: "artifact URI", plan: plan, manifest: manifest, artifactURI: "https://example.com/out.mp4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildRenderJobV1("job", "once", test.plan, test.manifest, test.artifactURI); err == nil {
				t.Fatal("invalid render job input was accepted")
			}
		})
	}
}

func TestDecodeRenderJobSnapshotReceipt(t *testing.T) {
	plan, manifest, _ := renderPrepareSDKFixture(t)
	job, err := BuildRenderJobV1("job-status", "job-status-once", plan, manifest, "file:///tmp/cutly-status.mp4")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicaljson.Marshal(scheduler.JobSnapshot{
		SchemaVersion: scheduler.JobSnapshotSchemaVersion,
		Job:           job,
		State:         renderapi.JobQueued,
		LastSequence:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeRenderJobSnapshotReceipt(raw)
	if err != nil || snapshot.Job.ID != job.ID || snapshot.State != renderapi.JobQueued {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}

	noncanonical := append([]byte{' '}, raw...)
	if _, err := DecodeRenderJobSnapshotReceipt(noncanonical); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("non-canonical snapshot error = %v", err)
	}
	unknown := bytes.Replace(raw, []byte(`"job":`), []byte(`"extra":0,"job":`), 1)
	if _, err := DecodeRenderJobSnapshotReceipt(unknown); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("unknown-field snapshot error = %v", err)
	}
}
