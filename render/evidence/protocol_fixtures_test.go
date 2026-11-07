package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestEvidenceManifestV1SchemaIsValidJSON(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate evidence schema")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "schemas", "cutly.evidence-manifest.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["$id"] != "https://schemas.cutly.dev/evidence-manifest/v1" {
		t.Fatalf("schema identity = %v / %v", schema["$schema"], schema["$id"])
	}
}

func TestEvidenceManifestV1ProtocolFixtures(t *testing.T) {
	manifest, err := Decode(evidenceFixture(t, "evidence-manifest-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.DecodedVideo == nil || manifest.DecodedAudio == nil {
		t.Fatalf("manifest = %+v", manifest)
	}
	actual, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(evidenceFixture(t, "evidence-manifest-v1.canonical.json"))
	if !bytes.Equal(actual, expected) {
		t.Fatalf("canonical fixture drift\nwant %s\n got %s", expected, actual)
	}
	if _, err := Decode(evidenceFixture(t, "evidence-manifest-v1.invalid.json")); !errors.Is(err, ErrEvidenceFingerprintMismatch) {
		t.Fatalf("invalid fixture error = %v", err)
	}
}

func evidenceFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate evidence fixtures")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
