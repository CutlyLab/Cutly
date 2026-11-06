package renderapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestValidateRenderRequestAcceptsFrozenFullAndChunkRanges(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := FreezeExecutionManifest(plan, executionTestDraft(plan))
	if err != nil {
		t.Fatal(err)
	}
	request := renderTestRequest(plan, manifest)
	if err := ValidateRenderRequest(request); err != nil {
		t.Fatal(err)
	}

	chunk := request
	chunk.Job.TimelineRange = tr(1, 1)
	chunk.Job.FrameRange, err = videoedit.CompileFrameTickRange(chunk.Job.TimelineRange, plan.Target.FrameClock)
	if err != nil {
		t.Fatal(err)
	}
	chunk.Job.SampleRange, err = videoedit.CompileSampleTickRange(chunk.Job.TimelineRange, plan.Target.SampleClock)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRenderRequest(chunk); err != nil {
		t.Fatalf("valid chunk: %v", err)
	}
}

func TestValidateRenderRequestRejectsIdentityAndRangeDrift(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := FreezeExecutionManifest(plan, executionTestDraft(plan))
	if err != nil {
		t.Fatal(err)
	}
	request := renderTestRequest(plan, manifest)

	tests := []struct {
		name   string
		mutate func(*RenderRequest)
	}{
		{name: "plan fingerprint", mutate: func(value *RenderRequest) { value.Job.PlanFingerprint = digest('3') }},
		{name: "execution fingerprint", mutate: func(value *RenderRequest) { value.Job.ExecutionManifestFingerprint = digest('4') }},
		{name: "frame before plan", mutate: func(value *RenderRequest) { value.Job.FrameRange.First-- }},
		{name: "sample after plan", mutate: func(value *RenderRequest) { value.Job.SampleRange.Count++ }},
		{name: "av time mismatch", mutate: func(value *RenderRequest) { value.Job.SampleRange.First++ }},
		{name: "timeline before plan on same tick grid", mutate: func(value *RenderRequest) {
			value.Job.TimelineRange.Start = videoedit.RationalTime{Value: -1, Rate: 96000}
		}},
		{name: "negative frame count", mutate: func(value *RenderRequest) { value.Job.FrameRange.Count = -1 }},
		{name: "negative sample count", mutate: func(value *RenderRequest) { value.Job.SampleRange.Count = -1 }},
		{name: "no work", mutate: func(value *RenderRequest) {
			value.Job.FrameRange.Count = 0
			value.Job.SampleRange.Count = 0
		}},
		{name: "missing idempotency", mutate: func(value *RenderRequest) { value.Job.IdempotencyKey = "" }},
		{name: "network artifact", mutate: func(value *RenderRequest) { value.Job.ArtifactURI = "https://example.invalid/output.mp4" }},
		{name: "unclean artifact", mutate: func(value *RenderRequest) { value.Job.ArtifactURI = "file:///controlled/../escape.mp4" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := request
			tt.mutate(&changed)
			if err := ValidateRenderRequest(changed); !errors.Is(err, ErrRenderRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeRenderJobIsStrict(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := FreezeExecutionManifest(plan, executionTestDraft(plan))
	if err != nil {
		t.Fatal(err)
	}
	job := renderTestRequest(plan, manifest).Job
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRenderJob(raw)
	if err != nil || decoded.ID != job.ID {
		t.Fatalf("decode = %+v, %v", decoded, err)
	}
	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeRenderJob(withUnknown); !errors.Is(err, ErrRenderJobInvalid) {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicateRoot := append([]byte(`{"schema_version":999,`), raw[1:]...)
	if _, err := DecodeRenderJob(duplicateRoot); !errors.Is(err, ErrRenderJobInvalid) {
		t.Fatalf("duplicate field error = %v", err)
	}
	caseAlias := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, err := DecodeRenderJob(caseAlias); !errors.Is(err, ErrRenderJobInvalid) {
		t.Fatalf("case alias error = %v", err)
	}
	var missingRequired map[string]json.RawMessage
	if err := json.Unmarshal(raw, &missingRequired); err != nil {
		t.Fatal(err)
	}
	var frameRange map[string]json.RawMessage
	if err := json.Unmarshal(missingRequired["frame_range"], &frameRange); err != nil {
		t.Fatal(err)
	}
	delete(frameRange, "first")
	missingRequired["frame_range"], _ = json.Marshal(frameRange)
	missingRaw, _ := json.Marshal(missingRequired)
	if _, err := DecodeRenderJob(missingRaw); !errors.Is(err, ErrRenderJobInvalid) {
		t.Fatalf("missing required zero field error = %v", err)
	}
	job.FrameRange.Count = -1
	raw, _ = json.Marshal(job)
	if _, err := DecodeRenderJob(raw); !errors.Is(err, ErrRenderJobInvalid) {
		t.Fatalf("negative range error = %v", err)
	}
}

func TestEventSinkFuncPropagatesContextAndErrors(t *testing.T) {
	want := errors.New("scheduler stopped")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	sink := EventSinkFunc(func(got context.Context, event ProgressEvent) error {
		called = true
		if got != ctx || event.JobID != "job" || got.Err() == nil {
			t.Fatalf("ctx/event not propagated: %+v %v", event, got.Err())
		}
		return want
	})
	if err := sink.Emit(ctx, ProgressEvent{JobID: "job"}); !errors.Is(err, want) || !called {
		t.Fatalf("emit error=%v called=%v", err, called)
	}
}

func TestEvidenceSidecarURIIsDerivedFromAuthorizedArtifact(t *testing.T) {
	got, err := EvidenceSidecarURI("file:///controlled/output%20name.mp4")
	if err != nil || got != "file:///controlled/output%20name.mp4.evidence.json" {
		t.Fatalf("sidecar = %q, %v", got, err)
	}
	for _, value := range []string{"https://example.invalid/output.mp4", "file://host/controlled/output.mp4", "file:///controlled/../escape.mp4"} {
		if _, err := EvidenceSidecarURI(value); !errors.Is(err, ErrRenderJobInvalid) {
			t.Fatalf("URI %q error = %v", value, err)
		}
	}
}

func TestValidateRenderRequestRechecksManifestPlanBinding(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := FreezeExecutionManifest(plan, executionTestDraft(plan))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*ExecutionManifest)
	}{
		{name: "output", mutate: func(value *ExecutionManifest) { value.Output.Width++ }},
		{name: "resource", mutate: func(value *ExecutionManifest) { value.Resources[0].Kind = videoedit.AssetKindVideo }},
		{name: "capabilities", mutate: func(value *ExecutionManifest) {
			value.Backend.TrackKinds = []videoedit.TrackKind{videoedit.TrackKindVideo}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := cloneExecutionManifest(manifest)
			tt.mutate(&changed)
			changed.Fingerprint, err = executionManifestFingerprint(changed)
			if err != nil {
				t.Fatal(err)
			}
			request := renderTestRequest(plan, changed)
			if err := ValidateRenderRequest(request); !errors.Is(err, ErrRenderRequestInvalid) {
				t.Fatalf("error = %v, want ErrRenderRequestInvalid", err)
			}
		})
	}
}

func renderTestRequest(plan videoedit.RenderPlan, manifest ExecutionManifest) RenderRequest {
	return RenderRequest{
		Plan:     plan,
		Manifest: manifest,
		Job: RenderJob{
			SchemaVersion:                RenderJobSchemaVersion,
			ID:                           "job-1",
			IdempotencyKey:               "render-r1",
			PlanFingerprint:              plan.Fingerprint,
			ExecutionManifestFingerprint: manifest.Fingerprint,
			TimelineRange:                plan.RequestedRange,
			FrameRange:                   plan.FrameRange,
			SampleRange:                  plan.SampleRange,
			ArtifactURI:                  "file:///controlled/output.mp4",
		},
	}
}
