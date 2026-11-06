package probe

import (
	"bytes"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestProbeManifestV1CanonicalFixture(t *testing.T) {
	manifest, diagnostics, err := Decode(fixture(t, "probe-manifest-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	actual, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.TrimSpace(fixture(t, "probe-manifest-v1.canonical.json"))
	if !bytes.Equal(actual, expected) {
		t.Fatalf("canonical fixture drift\nwant %s\n got %s", expected, actual)
	}
}
