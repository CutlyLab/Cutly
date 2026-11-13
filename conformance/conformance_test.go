package conformance_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestSchemaDocumentsAreValidJSON(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"cutly.document.v1.schema.json",
		"cutly.document.v2.schema.json",
		"cutly.transaction.v1.schema.json",
		"cutly.render-plan.v2.schema.json",
		"cutly.render-plan.v3.schema.json",
		"cutly.document-v2-render-plan.v1.schema.json",
		"cutly.probe-manifest.v1.schema.json",
		"cutly.execution-manifest.v1.schema.json",
		"cutly.execution-manifest.document-v2.v2.schema.json",
		"cutly.render-job.v1.schema.json",
		"cutly.render-request.document-v2.v2.schema.json",
		"cutly.exact-frame-request.v1.schema.json",
		"cutly.evidence-manifest.v1.schema.json",
		"cutly.agent-request.v1.schema.json",
		"cutly.agent-result.v1.schema.json",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var schema map[string]any
			decodeStrict(t, schemaFile(t, name), &schema)
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("$schema = %v", schema["$schema"])
			}
			if id, ok := schema["$id"].(string); !ok || id == "" {
				t.Fatalf("schema has no stable $id: %v", schema["$id"])
			}
		})
	}
}

func TestDocumentV2RenderPlanFixtures(t *testing.T) {
	raw := bytes.TrimSpace(fixture(t, "document-v2-render-plan-v1.valid.json"))
	plan, err := renderplanv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != renderplanv2.SchemaVersion || plan.Profile != renderplanv2.ProfileV1 {
		t.Fatalf("plan version/profile = %d/%q", plan.SchemaVersion, plan.Profile)
	}
	canonical, err := renderplanv2.Canonical(plan)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, "document-v2-render-plan-v1.canonical.json"))
	if !bytes.Equal(canonical, expected) || !bytes.Equal(raw, expected) {
		t.Fatalf("Document v2 RenderPlan canonical fixture drift\nwant %s\n got %s", expected, canonical)
	}
	if _, err := renderplanv2.Decode(bytes.TrimSpace(fixture(t, "document-v2-render-plan-v1.invalid.json"))); !errors.Is(err, renderplanv2.ErrFingerprintMismatch) {
		t.Fatalf("invalid Document v2 plan error = %v, want ErrFingerprintMismatch", err)
	}
	gap, err := renderplanv2.Decode(bytes.TrimSpace(fixture(t, "document-v2-render-plan-v1-gap.canonical.json")))
	if err != nil {
		t.Fatal(err)
	}
	if len(gap.Visual) != 0 || len(gap.Audio) != 0 || len(gap.Transitions) != 0 || len(gap.Resources) != 0 ||
		len(gap.RequiredCapabilities.TrackKinds) != 0 || len(gap.RequiredCapabilities.AssetKinds) != 0 || len(gap.RequiredCapabilities.Features) != 0 {
		t.Fatalf("gap plan has an executable closure: %+v", gap)
	}
}

func TestDocumentV2Fixtures(t *testing.T) {
	document, err := documentv2.Decode(fixture(t, "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != documentv2.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", document.SchemaVersion, documentv2.SchemaVersion)
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, "document-v2.canonical.json"))
	if !bytes.Equal(canonical, expected) {
		t.Fatalf("canonical fixture drift\nwant %s\n got %s", expected, canonical)
	}
	if _, err := documentv2.Decode(fixture(t, "document-v2.invalid.json")); !errors.Is(err, documentv2.ErrDocumentInvalid) {
		t.Fatalf("invalid v2 document error = %v, want ErrDocumentInvalid", err)
	}
}

func TestDocumentV2VectorViewportFixtures(t *testing.T) {
	document, err := documentv2.Decode(fixture(t, "document-v2-vector-viewport.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, "document-v2-vector-viewport.canonical.json"))
	if !bytes.Equal(canonical, expected) {
		t.Fatalf("viewport-vector canonical fixture drift\nwant %s\n got %s", expected, canonical)
	}
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	const wantDocumentHash = "sha256:590e2dcc94d87cf072e7d148523751c8fbe7dd6e4caf0cc1d7dcfcb1e2413d06"
	if documentHash != wantDocumentHash {
		t.Fatalf("viewport-vector document hash = %q, want %q", documentHash, wantDocumentHash)
	}
	if _, err := documentv2.Decode(fixture(t, "document-v2-vector-viewport.invalid.json")); !errors.Is(err, documentv2.ErrDocumentInvalid) {
		t.Fatalf("invalid viewport-vector document error = %v, want ErrDocumentInvalid", err)
	}
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("viewport-vector requirements: %+v", diagnostics)
	}
	if !slices.Equal(required.TrackKinds, []documentv2.TrackKind{documentv2.TrackKindVector}) ||
		!slices.Equal(required.AssetKinds, []documentv2.AssetKind{documentv2.AssetKindVector}) ||
		!slices.Equal(required.Features, []videoedit.CapabilityID{documentv2.CapabilityVectorPathViewportPXV2}) {
		t.Fatalf("viewport-vector requirements = %+v", required)
	}
}

func TestDocumentV2PinnedFontViewportTextFixtures(t *testing.T) {
	document, err := documentv2.Decode(fixture(t, "document-v2-text-pinned-font-viewport.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, "document-v2-text-pinned-font-viewport.canonical.json"))
	if !bytes.Equal(canonical, expected) {
		t.Fatalf("pinned-font text canonical fixture drift\nwant %s\n got %s", expected, canonical)
	}
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	const wantDocumentHash = "sha256:12c272cee65e2f0a4d2d6a4104ee17b84fba1568960c9655a0502d0d28ebbb6f"
	if documentHash != wantDocumentHash {
		t.Fatalf("pinned-font text document hash = %q, want %q", documentHash, wantDocumentHash)
	}
	if _, err := documentv2.Decode(fixture(t, "document-v2-text-pinned-font-viewport.invalid.json")); !errors.Is(err, documentv2.ErrDocumentInvalid) {
		t.Fatalf("invalid pinned-font text document error = %v, want ErrDocumentInvalid", err)
	}
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("pinned-font text requirements: %+v", diagnostics)
	}
	if !slices.Equal(required.TrackKinds, []documentv2.TrackKind{documentv2.TrackKindText}) ||
		!slices.Equal(required.AssetKinds, []documentv2.AssetKind{documentv2.AssetKindFont, documentv2.AssetKindText}) ||
		!slices.Equal(required.Features, []videoedit.CapabilityID{documentv2.CapabilityTextPinnedFontViewportPXV2}) {
		t.Fatalf("pinned-font text requirements = %+v", required)
	}
}

func TestDocumentV2AudioProcessingFixtures(t *testing.T) {
	document, err := documentv2.Decode(fixture(t, "document-v2-audio-processing.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, "document-v2-audio-processing.canonical.json"))
	if !bytes.Equal(canonical, expected) {
		t.Fatalf("audio-processing canonical fixture drift\nwant %s\n got %s", expected, canonical)
	}
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("audio-processing requirements: %+v", diagnostics)
	}
	wantFeatures := []videoedit.CapabilityID{
		documentv2.CapabilityAudioChannelMatrixV1,
		documentv2.CapabilityAudioGainV1,
		documentv2.CapabilityAudioSamplePeakLimiterV1,
		documentv2.CapabilityAudioPanV1,
		documentv2.CapabilityAudioRouteV1,
	}
	if !slices.Equal(required.TrackKinds, []documentv2.TrackKind{documentv2.TrackKindAudio}) ||
		!slices.Equal(required.AssetKinds, []documentv2.AssetKind{documentv2.AssetKindAudio}) ||
		!slices.Equal(required.Features, wantFeatures) {
		t.Fatalf("audio-processing requirements = %+v, want features %q", required, wantFeatures)
	}
	if documentv2.IsV1Compatible(document) {
		t.Fatal("typed audio-processing fixture unexpectedly passed the v1 compatibility gate")
	}
}

func TestDocumentV1Fixtures(t *testing.T) {
	var document videoedit.Document
	decodeStrict(t, fixture(t, "document-v1.valid.json"), &document)
	if diagnostics := videoedit.ValidateDocument(document); videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("valid document diagnostics: %+v", diagnostics)
	}
	if document.SchemaVersion != videoedit.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", document.SchemaVersion, videoedit.SchemaVersion)
	}
	assertCanonicalFixture(t, document, "document-v1.canonical.json")

	var invalid videoedit.Document
	decodeStrict(t, fixture(t, "document-v1.invalid.json"), &invalid)
	if diagnostics := videoedit.ValidateDocument(invalid); !videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatal("invalid document produced no error diagnostics")
	}
}

func TestTransactionV1Fixtures(t *testing.T) {
	var document videoedit.Document
	decodeStrict(t, fixture(t, "document-v1.valid.json"), &document)

	var transaction videoedit.Transaction
	decodeStrict(t, fixture(t, "transaction-v1.valid.json"), &transaction)
	result, err := videoedit.Apply(document, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if videoedit.HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("valid transaction diagnostics: %+v", result.Diagnostics)
	}
	if result.Document.Revision != document.Revision+1 {
		t.Fatalf("revision = %d, want %d", result.Document.Revision, document.Revision+1)
	}
	assertCanonicalFixture(t, transaction, "transaction-v1.canonical.json")

	var invalid videoedit.Transaction
	decodeStrict(t, fixture(t, "transaction-v1.invalid.json"), &invalid)
	invalidResult, err := videoedit.Apply(document, invalid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !videoedit.HasErrorDiagnostics(invalidResult.Diagnostics) {
		t.Fatal("invalid transaction produced no error diagnostics")
	}
}

func TestRenderPlanV3Fixtures(t *testing.T) {
	raw := fixture(t, "render-plan-v3.valid.json")
	plan, err := videoedit.DecodeRenderPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != videoedit.RenderPlanSchemaVersion || plan.SemanticProfile != videoedit.RenderSemanticProfile {
		t.Fatalf("plan version/profile = %d/%q", plan.SchemaVersion, plan.SemanticProfile)
	}
	if bytes.Contains(raw, []byte(`"uri"`)) {
		t.Fatal("semantic RenderPlan fixture contains an execution locator")
	}
	evaluation, err := videoedit.EvaluateFrame(plan, plan.FrameRange.First)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Video) != 1 || evaluation.Video[0].ClipID != "clip-video" {
		t.Fatalf("frame evaluation = %+v", evaluation)
	}
	if evaluation.Video[0].SourceTime != (videoedit.RationalTime{Value: 1, Rate: 1}) {
		t.Fatalf("source time = %+v", evaluation.Video[0].SourceTime)
	}
	assertCanonicalFixture(t, plan, "render-plan-v3.canonical.json")

	if _, err := videoedit.DecodeRenderPlan(fixture(t, "render-plan-v3.invalid.json")); !errors.Is(err, videoedit.ErrRenderPlanFingerprintMismatch) {
		t.Fatalf("tampered v3 error = %v, want ErrRenderPlanFingerprintMismatch", err)
	}
}

func TestRenderPlanV2FixtureIsCanonicalLegacyEvidenceButRejected(t *testing.T) {
	legacyRaw := fixture(t, "render-plan-v2.valid.json")
	var legacy any
	decodeStrict(t, legacyRaw, &legacy)
	assertCanonicalFixture(t, legacy, "render-plan-v2.canonical.json")

	if _, err := videoedit.DecodeRenderPlan(legacyRaw); err == nil {
		t.Fatal("legacy RenderPlan v2 was accepted by the v3 decoder")
	} else if !errors.Is(err, videoedit.ErrRenderPlanInvalid) && !errors.Is(err, videoedit.ErrRenderPlanSchemaUnsupported) {
		t.Fatalf("legacy RenderPlan v2 error = %v", err)
	}
}

func assertCanonicalFixture(t *testing.T, value any, expectedName string) {
	t.Helper()
	actual, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, expectedName))
	if !bytes.Equal(actual, expected) {
		t.Fatalf("canonical fixture drift\nwant %s\n got %s", expected, actual)
	}
}

func decodeStrict(t *testing.T, raw []byte, destination any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			t.Fatal("fixture contains multiple JSON values")
		}
		t.Fatalf("fixture has trailing JSON: %v", err)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	return readTestFile(t, "fixtures", name)
}

func schemaFile(t *testing.T, name string) []byte {
	t.Helper()
	return readTestFile(t, "..", "schemas", name)
}

func readTestFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate conformance test directory")
	}
	path := filepath.Join(append([]string{filepath.Dir(source)}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		t.Fatalf("empty fixture %s", path)
	}
	return raw
}
