package cpucompositor

import (
	"bytes"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestProfileDescriptorCanonicalFixture(t *testing.T) {
	actual, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	expected = bytes.TrimSpace(expected)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("profile fixture drift\nwant %s\n got %s", expected, actual)
	}
	var decoded ProfileDescriptor
	if err := wirejson.Decode(expected, &decoded); err != nil {
		t.Fatalf("strict profile fixture: %v", err)
	}
	if decoded != Descriptor() || decoded.Profile != KernelProfileV1 || decoded.SurfaceProfile != SurfaceProfileV1 {
		t.Fatalf("profile fixture is incomplete: %+v", decoded)
	}
}
