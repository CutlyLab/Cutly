package cpuraster

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestProfileDescriptorCanonicalFixture(t *testing.T) {
	actual, err := json.Marshal(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read fixture: %v\nactual %s", err, actual)
	}
	if !bytes.Equal(actual, bytes.TrimSpace(want)) {
		t.Fatalf("profile mismatch\nactual %s\nwant   %s", actual, bytes.TrimSpace(want))
	}
}

func TestDescriptorIsValueCopy(t *testing.T) {
	first := Descriptor()
	first.Profile = "mutated"
	first.DefaultLimits.MaxWidth = 1
	second := Descriptor()
	if second.Profile != ProfileV1 || second.DefaultLimits != DefaultLimits() {
		t.Fatalf("descriptor mutated: %+v", second)
	}
}
