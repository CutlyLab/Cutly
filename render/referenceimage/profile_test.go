package referenceimage

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestProfileDescriptorCanonicalFixture(t *testing.T) {
	encoded, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("%v\ncanonical=%s", err, encoded)
	}
	wanted = bytes.TrimSpace(wanted)
	if !bytes.Equal(encoded, wanted) {
		t.Fatalf("profile fixture drift\nwant=%s\ngot=%s", wanted, encoded)
	}
	var decoded ProfileDescriptor
	if err := wirejson.Decode(wanted, &decoded); err != nil {
		t.Fatalf("strict profile fixture: %v", err)
	}
	if !reflect.DeepEqual(decoded, Descriptor()) {
		t.Fatalf("profile fixture is incomplete: %+v", decoded)
	}
	defaults := DefaultConfig(2, 3)
	if defaults.Width != 2 || defaults.Height != 3 || defaults.SourceLimits != Descriptor().DefaultSourceLimits ||
		defaults.CompositorLimits != Descriptor().DefaultCompositorLimits || defaults.OutputLimits != Descriptor().DefaultOutputLimits ||
		defaults.PNGLimits != Descriptor().DefaultPNGLimits || defaults.BuildFingerprint != "" {
		t.Fatalf("default config=%+v descriptor=%+v", defaults, Descriptor())
	}
}
