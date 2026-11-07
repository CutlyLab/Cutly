package renderapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestExactFrameRequestStrictDecodeAndBinding(t *testing.T) {
	request := exactFrameTestRequest(t)
	raw, err := json.Marshal(request.Frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExactFrameRequest(raw)
	if err != nil || !reflect.DeepEqual(decoded, request.Frame) {
		t.Fatalf("decode = %+v, %v", decoded, err)
	}
	if err := ValidateExactFrameRenderRequest(request); err != nil {
		t.Fatal(err)
	}

	attacks := [][]byte{
		append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...),
		append([]byte(`{"schema_version":999,`), raw[1:]...),
		bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1),
		bytes.Replace(raw, []byte(`"composition_frame_index":0`), []byte(`"composition_frame_index":0.0`), 1),
	}
	for index, attack := range attacks {
		if _, err := DecodeExactFrameRequest(attack); !errors.Is(err, ErrExactFrameRequestInvalid) {
			t.Fatalf("attack %d error = %v", index, err)
		}
	}

	var missing map[string]json.RawMessage
	if err := json.Unmarshal(raw, &missing); err != nil {
		t.Fatal(err)
	}
	delete(missing, "composition_frame_index")
	missingRaw, _ := json.Marshal(missing)
	if _, err := DecodeExactFrameRequest(missingRaw); !errors.Is(err, ErrExactFrameRequestInvalid) {
		t.Fatalf("missing required zero field error = %v", err)
	}
}

func TestValidateExactFrameRenderRequestRejectsDrift(t *testing.T) {
	base := exactFrameTestRequest(t)
	tests := []struct {
		name   string
		mutate func(*ExactFrameRenderRequest)
	}{
		{name: "outside frame", mutate: func(value *ExactFrameRenderRequest) {
			value.Frame.CompositionFrameIndex = value.Plan.FrameRange.First + value.Plan.FrameRange.Count
		}},
		{name: "plan identity", mutate: func(value *ExactFrameRenderRequest) { value.Frame.PlanFingerprint = digest('9') }},
		{name: "execution identity", mutate: func(value *ExactFrameRenderRequest) { value.Frame.ExecutionManifestFingerprint = digest('8') }},
		{name: "mp4 output", mutate: func(value *ExactFrameRenderRequest) { value.Frame.ArtifactURI = "file:///controlled/frame.mp4" }},
		{name: "network output", mutate: func(value *ExactFrameRenderRequest) { value.Frame.ArtifactURI = "https://example.invalid/frame.png" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			tt.mutate(&changed)
			if err := ValidateExactFrameRenderRequest(changed); !errors.Is(err, ErrExactFrameRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateExactFrameArtifactBindsEvaluatorAndProfiles(t *testing.T) {
	request := exactFrameTestRequest(t)
	evaluation, err := videoedit.EvaluateFrame(request.Plan, request.Frame.CompositionFrameIndex)
	if err != nil {
		t.Fatal(err)
	}
	evaluationFingerprint, err := FrameEvaluationFingerprint(evaluation)
	if err != nil {
		t.Fatal(err)
	}
	artifact := ExactFrameArtifact{
		URI: request.Frame.ArtifactURI, ContentHash: digest('1'), SizeBytes: 1024, DecodedContentHash: digest('2'),
		PlanFingerprint: request.Plan.Fingerprint, ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		BackendID: request.Manifest.Backend.BackendID, BackendBuildFingerprint: request.Manifest.Backend.BuildFingerprint,
		ReferenceProfile: request.Manifest.Output.ReferenceProfile, PNGEncodingProfile: ExactFramePNGProfileV1,
		DecodedVideoProfile: request.Manifest.Output.DecodedVideoProfile,
		Width:               request.Manifest.Output.Width, Height: request.Manifest.Output.Height,
		CompositionFrameIndex: request.Frame.CompositionFrameIndex, TimelineTime: evaluation.TimelineTime,
		EvaluationFingerprint: evaluationFingerprint, Evaluation: evaluation,
	}
	if err := ValidateExactFrameArtifact(artifact, request); err != nil {
		t.Fatal(err)
	}

	mutations := []func(*ExactFrameArtifact){
		func(value *ExactFrameArtifact) { value.DecodedContentHash = "" },
		func(value *ExactFrameArtifact) { value.PNGEncodingProfile = "other.v1" },
		func(value *ExactFrameArtifact) { value.CompositionFrameIndex++ },
		func(value *ExactFrameArtifact) { value.Evaluation.Video[0].SourceTime.Value++ },
		func(value *ExactFrameArtifact) { value.EvaluationFingerprint = digest('3') },
	}
	for index, mutate := range mutations {
		changed := artifact
		changed.Evaluation.Video = append([]videoedit.EvaluatedClip(nil), artifact.Evaluation.Video...)
		mutate(&changed)
		if err := ValidateExactFrameArtifact(changed, request); !errors.Is(err, ErrExactFrameArtifactInvalid) {
			t.Fatalf("mutation %d error = %v", index, err)
		}
	}
}

func exactFrameTestRequest(t *testing.T) ExactFrameRenderRequest {
	t.Helper()
	plan := executionTestPlan(t)
	manifest, err := FreezeExecutionManifest(plan, executionTestDraft(plan))
	if err != nil {
		t.Fatal(err)
	}
	return ExactFrameRenderRequest{
		Plan: plan, Manifest: manifest,
		Frame: ExactFrameRequest{
			SchemaVersion: ExactFrameRequestSchemaVersion, ID: "frame-1", IdempotencyKey: "frame-1-once",
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
			CompositionFrameIndex: plan.FrameRange.First, ArtifactURI: "file:///controlled/frame.png",
		},
	}
}
