package wavdecode

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
		t.Fatalf("read profile fixture: %v\nactual %s", err, actual)
	}
	want = bytes.TrimSpace(want)
	if !bytes.Equal(actual, want) {
		t.Fatalf("profile fixture mismatch\nactual %s\nwant   %s", actual, want)
	}
}

func TestDescriptorReturnsValueCopy(t *testing.T) {
	first := Descriptor()
	first.Profile = "mutated"
	first.DefaultLimits.MaxFrames = 1
	second := Descriptor()
	if second.Profile != ProfileV1 || second.DefaultLimits != DefaultLimits() {
		t.Fatalf("descriptor was mutated: %+v", second)
	}
}
