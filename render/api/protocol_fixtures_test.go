package renderapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestExecutionManifestV1ProtocolFixtures(t *testing.T) {
	raw := renderProtocolFixture(t, "execution-manifest-v1.valid.json")
	manifest, err := DecodeExecutionManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != ExecutionManifestSchemaVersion || len(manifest.Resources) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.Resources[0].LocatorScheme != LocatorCAS || manifest.Resources[1].LocatorScheme != LocatorFile {
		t.Fatalf("locator boundary = %+v", manifest.Resources)
	}
	assertRenderProtocolCanonical(t, manifest, "execution-manifest-v1.canonical.json")

	if _, err := DecodeExecutionManifest(renderProtocolFixture(t, "execution-manifest-v1.invalid.json")); !errors.Is(err, ErrExecutionManifestFingerprintMismatch) {
		t.Fatalf("tampered manifest error = %v, want ErrExecutionManifestFingerprintMismatch", err)
	}
}

func TestRenderJobV1ProtocolFixtures(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := DecodeExecutionManifest(renderProtocolFixture(t, "execution-manifest-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}

	var job RenderJob
	decodeRenderProtocolStrict(t, renderProtocolFixture(t, "render-job-v1.valid.json"), &job)
	if err := ValidateRenderRequest(RenderRequest{Job: job, Plan: plan, Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	assertRenderProtocolCanonical(t, job, "render-job-v1.canonical.json")

	var invalid RenderJob
	decodeRenderProtocolStrict(t, renderProtocolFixture(t, "render-job-v1.invalid.json"), &invalid)
	if err := ValidateRenderRequest(RenderRequest{Job: invalid, Plan: plan, Manifest: manifest}); !errors.Is(err, ErrRenderRequestInvalid) {
		t.Fatalf("invalid job error = %v, want ErrRenderRequestInvalid", err)
	}
}

func TestExactFrameRequestV1ProtocolFixtures(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := DecodeExecutionManifest(renderProtocolFixture(t, "execution-manifest-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := DecodeExactFrameRequest(renderProtocolFixture(t, "exact-frame-request-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := ExactFrameRenderRequest{Frame: frame, Plan: plan, Manifest: manifest}
	if err := ValidateExactFrameRenderRequest(request); err != nil {
		t.Fatal(err)
	}
	assertRenderProtocolCanonical(t, frame, "exact-frame-request-v1.canonical.json")

	if _, err := DecodeExactFrameRequest(renderProtocolFixture(t, "exact-frame-request-v1.invalid.json")); !errors.Is(err, ErrExactFrameRequestInvalid) {
		t.Fatalf("invalid exact frame error = %v", err)
	}
}

func assertRenderProtocolCanonical(t *testing.T, value any, expectedName string) {
	t.Helper()
	actual, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(renderProtocolFixture(t, expectedName))
	if !bytes.Equal(actual, expected) {
		t.Fatalf("canonical fixture drift\nwant %s\n got %s", expected, actual)
	}
}

func decodeRenderProtocolStrict(t *testing.T, raw []byte, destination any) {
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

func renderProtocolFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate render protocol fixtures")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
