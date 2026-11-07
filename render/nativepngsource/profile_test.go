package nativepngsource

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestCanonicalProfileAndManifestFixtures(t *testing.T) {
	profileCanonical, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	profileRaw, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("%v\ncanonical profile=%s", err, profileCanonical)
	}
	var profileFixture ProfileDescriptor
	if err := wirejson.Decode(profileRaw, &profileFixture); err != nil {
		t.Fatal(err)
	}
	profileFixtureCanonical, err := videoedit.CanonicalJSON(profileFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profileFixture, Descriptor()) || !bytes.Equal(bytes.TrimSpace(profileRaw), profileFixtureCanonical) ||
		!bytes.Equal(profileCanonical, profileFixtureCanonical) {
		t.Fatalf("profile fixture differs\nwant=%s\ngot=%s", profileCanonical, bytes.TrimSpace(profileRaw))
	}

	encoded := decodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAAXNSR0ICQMB9xQAAABZJREFUeJwACQD2/wP/AAD/AUAgAQMADhwCZN2J4wgAAAAASUVORK5CYII=")
	document := oneImageDocument(contentHash(encoded))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	manifestCanonical, err := videoedit.CanonicalJSON(provider.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile("testdata/manifest-v1.canonical.json")
	if err != nil {
		t.Fatalf("%v\ncanonical manifest=%s", err, manifestCanonical)
	}
	var manifestFixture Manifest
	if err := wirejson.Decode(manifestRaw, &manifestFixture); err != nil {
		t.Fatal(err)
	}
	manifestFixtureCanonical, err := videoedit.CanonicalJSON(manifestFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifestFixture, provider.Manifest()) || !bytes.Equal(bytes.TrimSpace(manifestRaw), manifestFixtureCanonical) ||
		!bytes.Equal(manifestCanonical, manifestFixtureCanonical) {
		t.Fatalf("manifest fixture differs\nwant=%s\ngot=%s", manifestCanonical, bytes.TrimSpace(manifestRaw))
	}
}
