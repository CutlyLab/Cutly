package color

import (
	"bytes"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestProfileDescriptorCanonicalFixture(t *testing.T) {
	actual, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read profile fixture: %v\nactual %s", err, actual)
	}
	expected = bytes.TrimSpace(expected)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("profile fixture drift\nwant %s\n got %s", expected, actual)
	}
	var decoded ProfileDescriptor
	if err := wirejson.Decode(expected, &decoded); err != nil {
		t.Fatalf("strict profile fixture: %v", err)
	}
	if decoded != Descriptor() {
		t.Fatalf("profile fixture is incomplete: %+v", decoded)
	}
	if decoded.SchemaVersion != ProfileSchemaVersion || decoded.Profile != ProfileRGBA8SRGBV1 ||
		decoded.InputMetadata != MetadataRGBA8SRGBV1() || decoded.OutputSurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		decoded.LUTSHA256 != LUTSHA256V1 {
		t.Fatalf("profile identity is incomplete: %+v", decoded)
	}
}

func TestMetadataProfileIsFullyExplicit(t *testing.T) {
	got := MetadataRGBA8SRGBV1()
	if got.Profile == "" || got.PixelLayout == "" || got.RowOrder == "" || got.AlphaMode == "" ||
		got.Primaries == "" || got.WhitePoint == "" || got.Transfer == "" || got.Range == "" ||
		got.ChromaSubsampling == "" || got.ICCProfile != MetadataAbsent || got.HDRMetadata != MetadataAbsent {
		t.Fatalf("metadata profile contains an implicit field: %+v", got)
	}
}
