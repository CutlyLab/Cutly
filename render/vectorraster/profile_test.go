package vectorraster

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestProfileIdentityAndDefensiveValue(t *testing.T) {
	descriptor := Descriptor()
	if descriptor.SchemaVersion != ProfileSchemaVersion || descriptor.Profile != ProfileV1 ||
		descriptor.InputProfile != documentv2.VectorProfilePathViewportPXV2 ||
		descriptor.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		descriptor.KernelProfile != cpucompositor.KernelProfileV1 ||
		descriptor.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		descriptor.SourceHashProfile != SourceHashProfileV1 {
		t.Fatalf("descriptor identities = %+v", descriptor)
	}
	descriptor.Profile = "mutated"
	descriptor.DefaultLimits.MaxWidth = 1
	second := Descriptor()
	if second.Profile != ProfileV1 || second.DefaultLimits.MaxWidth != DefaultMaxWidth {
		t.Fatalf("Descriptor returned shared mutable state: %+v", second)
	}
}

func TestProfileCanonicalFixture(t *testing.T) {
	canonical, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "profile-v1.canonical.json"))
	if err != nil {
		t.Fatalf("read profile fixture: %v\ncanonical: %s", err, canonical)
	}
	fixture = bytes.TrimSpace(fixture)
	if !bytes.Equal(canonical, fixture) {
		t.Fatalf("profile fixture drift\nwant %s\n got %s", fixture, canonical)
	}
}
