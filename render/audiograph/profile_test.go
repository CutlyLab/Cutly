package audiograph

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestProfileDescriptorCanonicalFixtureAndDefensiveCopies(t *testing.T) {
	raw, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "profile-v1.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), raw) {
		t.Fatalf("profile fixture drift:\nwant=%s\n got=%s", want, raw)
	}

	mutated := Descriptor()
	mutated.ExplicitGraphCapabilities[0] = "mutated.v1"
	mutated.OptionalStageCapabilities[0] = "mutated.v1"
	mutated.PipelineOrder[0] = "mutated"
	mutated.ExcludedResponsibilities[0] = "mutated"
	again, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again) {
		t.Fatal("caller mutation changed registry-owned profile descriptor")
	}
}
