package color

import (
	"bytes"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestPaintProfileDescriptorCanonicalFixture(t *testing.T) {
	actual, err := videoedit.CanonicalJSON(PaintDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/paint-profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read paint profile fixture: %v\nactual %s", err, actual)
	}
	expected = bytes.TrimSpace(expected)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("paint profile fixture drift\nwant %s\n got %s", expected, actual)
	}
	var decoded PaintProfileDescriptor
	if err := wirejson.Decode(expected, &decoded); err != nil {
		t.Fatalf("strict paint profile fixture: %v", err)
	}
	if decoded != PaintDescriptor() {
		t.Fatalf("paint profile fixture is incomplete: %+v", decoded)
	}
	if decoded.SchemaVersion != PaintProfileSchemaVersion || decoded.Profile != ProfileRGBA16SRGBPaintV1 ||
		decoded.OutputSurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		decoded.TransferTableSHA256 != PaintTransferTableSHA256V1 {
		t.Fatalf("paint profile identity is incomplete: %+v", decoded)
	}
}
