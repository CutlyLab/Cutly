package color

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestOutputProfileDescriptorCanonicalFixture(t *testing.T) {
	actual, err := videoedit.CanonicalJSON(OutputDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/output-profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read output profile fixture: %v\nactual %s", err, actual)
	}
	expected = bytes.TrimSpace(expected)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("output profile fixture drift\nwant %s\n got %s", expected, actual)
	}
	var decoded OutputProfileDescriptor
	if err := wirejson.Decode(expected, &decoded); err != nil {
		t.Fatalf("strict output profile fixture: %v", err)
	}
	if decoded != OutputDescriptor() {
		t.Fatalf("output profile fixture is incomplete: %+v", decoded)
	}
	if decoded.SchemaVersion != OutputProfileSchemaVersion ||
		decoded.Profile != ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		decoded.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		decoded.OutputMetadata != MetadataRGBA8SRGBOutputV1() ||
		decoded.ForwardLUTSHA256 != LUTSHA256V1 ||
		decoded.OutputLUTSHA256 != OutputTransferLUTSHA256V1 ||
		decoded.RawHashProfile != OutputRGBAHashProfileV1 ||
		decoded.HardMaxDimension != HardMaxOutputDimension {
		t.Fatalf("output profile identity is incomplete: %+v", decoded)
	}
}

func TestOutputTransferLookupSHA256(t *testing.T) {
	sum := sha256.Sum256(linear16ToSRGB8Lookup[:])
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != OutputTransferLUTSHA256V1 {
		t.Fatalf("output transfer LUT hash = %q, want %q", got, OutputTransferLUTSHA256V1)
	}
}
