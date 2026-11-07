package textraster

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestDescriptorCanonicalFixtureAndHash(t *testing.T) {
	fixture, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.TrimSuffix(fixture, []byte("\n"))
	canonical := DescriptorCanonicalJSON()
	if !bytes.Equal(canonical, fixture) {
		t.Fatalf("descriptor fixture drift\ngot:  %s\nwant: %s", canonical, fixture)
	}
	var decoded ProfileDescriptor
	if err := json.Unmarshal(canonical, &decoded); err != nil || decoded != Descriptor() {
		t.Fatalf("descriptor decode=%+v err=%v", decoded, err)
	}
	if got, want := DescriptorHash(), "sha256:c9b1459b629b30588d5bf0bace61465bfa5679dbe7fd520638564f2ff107bc21"; got != want {
		t.Fatalf("descriptor hash=%s want=%s", got, want)
	}
	canonical[0] ^= 0xff
	if bytes.Equal(canonical, DescriptorCanonicalJSON()) {
		t.Fatal("DescriptorCanonicalJSON returned aliased storage")
	}
}
