package renderapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestBindRenderRequestV2RequiresDocumentAndReturnsOwnedHandle(t *testing.T) {
	document, plan, boundPlan := executionV2Plan(t)
	request := executionV2Request(t, plan, boundPlan)
	bound, err := BindRenderRequestV2(context.Background(), document, request)
	if err != nil {
		t.Fatal(err)
	}
	if bound.JobID() != request.Job.ID || bound.PlanFingerprint() != plan.Fingerprint || bound.ManifestFingerprint() != request.Manifest.Fingerprint {
		t.Fatalf("bound identity=%q/%q/%q", bound.JobID(), bound.PlanFingerprint(), bound.ManifestFingerprint())
	}
	snapshot, err := bound.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Plan.Resources[0].AssetID = "mutated"
	snapshot.Manifest.Resources[0].AssetID = "mutated"
	snapshot.Manifest.Visual.Configuration[0] = '['
	second, err := bound.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if second.Plan.Resources[0].AssetID == "mutated" || second.Manifest.Resources[0].AssetID == "mutated" || bytes.HasPrefix(second.Manifest.Visual.Configuration, []byte{'['}) {
		t.Fatal("document-bound request retained caller mutation")
	}
	if _, err := (DocumentBoundRenderRequestV2{}).Snapshot(); !errors.Is(err, ErrDocumentBoundRenderRequestV2Required) {
		t.Fatalf("zero bound request error = %v", err)
	}

	request.Plan.Resources[0].AssetID = "caller-mutated"
	request.Manifest.Resources[0].AssetID = "caller-mutated"
	third, err := bound.Snapshot()
	if err != nil || third.Plan.Resources[0].AssetID == "caller-mutated" || third.Manifest.Resources[0].AssetID == "caller-mutated" {
		t.Fatalf("bound handle changed after caller mutation: err=%v", err)
	}
}

func TestRenderRequestV2SupportsExactSubranges(t *testing.T) {
	document, plan, boundPlan := executionV2Plan(t)
	request := executionV2Request(t, plan, boundPlan)
	request.Job.TimelineRange = documentv2.TimeRange{
		Start:    videoedit.RationalTime{Value: 5, Rate: 1},
		Duration: videoedit.RationalTime{Value: 1, Rate: 1},
	}
	request.Job.FrameRange, _ = videoedit.CompileFrameTickRange(request.Job.TimelineRange, plan.Target.FrameClock)
	request.Job.SampleRange, _ = videoedit.CompileSampleTickRange(request.Job.TimelineRange, plan.Target.SampleClock)
	if _, err := BindRenderRequestV2(context.Background(), document, request); err != nil {
		t.Fatal(err)
	}
}

func TestRenderRequestV2RejectsIdentityRangeAndDocumentDrift(t *testing.T) {
	document, plan, boundPlan := executionV2Plan(t)
	tests := []struct {
		name   string
		mutate func(*RenderRequestV2)
	}{
		{name: "plan fingerprint", mutate: func(value *RenderRequestV2) { value.Job.PlanFingerprint = digest('0') }},
		{name: "manifest fingerprint", mutate: func(value *RenderRequestV2) { value.Job.ExecutionManifestFingerprint = digest('0') }},
		{name: "frame drift", mutate: func(value *RenderRequestV2) { value.Job.FrameRange.First++ }},
		{name: "sample drift", mutate: func(value *RenderRequestV2) { value.Job.SampleRange.Count-- }},
		{name: "timeline outside", mutate: func(value *RenderRequestV2) {
			value.Job.TimelineRange.Start = videoedit.RationalTime{Value: 3, Rate: 1}
		}},
		{name: "network publication", mutate: func(value *RenderRequestV2) { value.Job.ArtifactURI = "https://example.invalid/out.mp4" }},
		{name: "resource omission", mutate: func(value *RenderRequestV2) {
			value.Manifest.Resources = value.Manifest.Resources[1:]
			value.Manifest.Fingerprint, _ = executionManifestV2Fingerprint(value.Manifest)
			value.Job.ExecutionManifestFingerprint = value.Manifest.Fingerprint
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := executionV2Request(t, plan, boundPlan)
			test.mutate(&request)
			if _, err := BindRenderRequestV2(context.Background(), document, request); !errors.Is(err, ErrRenderRequestV2Invalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	request := executionV2Request(t, plan, boundPlan)
	document.Revision++
	if _, err := BindRenderRequestV2(context.Background(), document, request); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("document mismatch error = %v", err)
	}
}

func TestDecodeRenderRequestV2RejectsNoncanonicalAndUnknownWire(t *testing.T) {
	_, plan, boundPlan := executionV2Plan(t)
	request := executionV2Request(t, plan, boundPlan)
	raw, err := CanonicalRenderRequestV2(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRenderRequestV2(raw)
	if err != nil || decoded.Job.ID != request.Job.ID {
		t.Fatalf("decoded job=%q err=%v", decoded.Job.ID, err)
	}
	if _, err := DecodeRenderRequestV2(append([]byte("\n"), raw...)); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("noncanonical error = %v", err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeRenderRequestV2(unknown); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("unknown error = %v", err)
	}
	duplicate := bytes.Replace(raw, []byte(`"job":`), []byte(`"job":{},"job":`), 1)
	if _, err := DecodeRenderRequestV2(duplicate); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := DecodeRenderRequestV2(bytes.Repeat([]byte{' '}, MaxCanonicalRenderRequestV2Bytes+1)); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("oversized error = %v", err)
	}

	tampered := request
	tampered.Plan = request.Plan
	tampered.Plan.Visual = append([]renderplanv2.PlannedClip(nil), request.Plan.Visual...)
	tampered.Plan.Visual[0].AssetID = "asset-video-b"
	tamperedRaw, _ := canonicaljson.Marshal(tampered)
	if _, err := DecodeRenderRequestV2(tamperedRaw); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("nested plan tamper error = %v", err)
	}
}

func TestBindRenderRequestV2CancellationAndConcurrentReads(t *testing.T) {
	document, plan, boundPlan := executionV2Plan(t)
	request := executionV2Request(t, plan, boundPlan)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BindRenderRequestV2(cancelled, document, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	bound, err := BindRenderRequestV2(context.Background(), document, request)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				snapshot, snapshotErr := bound.Snapshot()
				if snapshotErr != nil || snapshot.Job.ID != request.Job.ID || bound.PlanFingerprint() != plan.Fingerprint {
					t.Errorf("snapshot job=%q err=%v", snapshot.Job.ID, snapshotErr)
					return
				}
				snapshot.Manifest.Visual.Configuration[0] = '['
			}
		}()
	}
	group.Wait()
}

func TestCanonicalRenderRequestV2PreflightAdmission(t *testing.T) {
	_, plan, boundPlan := executionV2Plan(t)
	request := executionV2Request(t, plan, boundPlan)
	request.Manifest.Resources = make([]MaterializedResourceV2, MaxExecutionManifestV2Resources+1)
	if _, err := CanonicalRenderRequestV2(request); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("oversized struct request error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CanonicalRenderRequestV2Context(cancelled, executionV2Request(t, plan, boundPlan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled canonicalization error = %v", err)
	}
}

func TestRenderRequestV2ProtocolFixtures(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("testdata", "render-request-document-v2-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeRenderRequestV2(bytes.TrimSpace(valid))
	if err != nil {
		t.Fatal(err)
	}
	if request.Plan.Fingerprint != "sha256:3c2a1618fc37dbe00aca03447deeece0ef2aff8b2af86f2bd6d5c902afe8a8f1" ||
		request.Manifest.Fingerprint != "sha256:36f02a89e082e9c779067d7b2305f32bad9c7b5cd48a6605112cbe309aaed7dd" {
		t.Fatalf("fixture identities = %q / %q", request.Plan.Fingerprint, request.Manifest.Fingerprint)
	}
	canonical, err := CanonicalRenderRequestV2(request)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "render-request-document-v2-v2.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(want)) || !bytes.Equal(bytes.TrimSpace(valid), bytes.TrimSpace(want)) {
		t.Fatal("render request v2 canonical fixture drift")
	}
	invalid, err := os.ReadFile(filepath.Join("testdata", "render-request-document-v2-v2.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRenderRequestV2(bytes.TrimSpace(invalid)); !errors.Is(err, ErrRenderRequestV2Invalid) {
		t.Fatalf("invalid fixture error = %v", err)
	}
}

func executionV2Request(t *testing.T, plan renderplanv2.Plan, boundPlan renderplanv2.DocumentBoundPlan) RenderRequestV2 {
	t.Helper()
	verified, err := FreezeExecutionManifestV2(context.Background(), boundPlan, executionV2Draft(plan))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verified.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return RenderRequestV2{
		Job: RenderJob{
			SchemaVersion: RenderJobSchemaVersion,
			ID:            "job-document-v2", IdempotencyKey: "job-document-v2-key",
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
			TimelineRange: plan.RequestedRange, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange,
			ArtifactURI: "file:///controlled/output.mp4",
		},
		Plan: plan, Manifest: manifest,
	}
}
