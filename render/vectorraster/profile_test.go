package vectorraster

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestProfileV2IdentityAndFixedCubicContract(t *testing.T) {
	descriptor := DescriptorV2()
	if descriptor.SchemaVersion != ProfileSchemaVersion || descriptor.Profile != ProfileV2 ||
		descriptor.InputProfile != documentv2.VectorProfilePathViewportPXV3 ||
		descriptor.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		descriptor.KernelProfile != cpucompositor.KernelProfileV1 ||
		descriptor.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		descriptor.SourceHashProfile != SourceHashProfileV1 ||
		!strings.Contains(descriptor.CurveFlattening, "t=i/128") ||
		!strings.Contains(descriptor.ResourceSemantics, "exactly 128 conceptual edges") {
		t.Fatalf("v2 descriptor = %+v", descriptor)
	}
	descriptor.Profile = "mutated"
	descriptor.DefaultLimits.MaxWidth = 1
	second := DescriptorV2()
	if second.Profile != ProfileV2 || second.DefaultLimits.MaxWidth != DefaultMaxWidth {
		t.Fatalf("DescriptorV2 returned shared mutable state: %+v", second)
	}
}

func TestProfileCanonicalFixture(t *testing.T) {
	for _, test := range []struct {
		name       string
		descriptor ProfileDescriptor
		fixture    string
	}{
		{name: "v1", descriptor: Descriptor(), fixture: "profile-v1.canonical.json"},
		{name: "v2", descriptor: DescriptorV2(), fixture: "profile-v2.canonical.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := videoedit.CanonicalJSON(test.descriptor)
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile(filepath.Join("testdata", test.fixture))
			if err != nil {
				t.Fatalf("read profile fixture: %v\ncanonical: %s", err, canonical)
			}
			fixture = bytes.TrimSpace(fixture)
			if !bytes.Equal(canonical, fixture) {
				t.Fatalf("profile fixture drift\nwant %s\n got %s", fixture, canonical)
			}
		})
	}
}
