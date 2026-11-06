package pngsource

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestProfileCanonicalFixture(t *testing.T) {
	actual, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read fixture: %v\nactual %s", err, actual)
	}
	var decoded ProfileDescriptor
	if err := wirejson.Decode(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, Descriptor()) || !bytes.Equal(bytes.TrimSpace(raw), actual) {
		t.Fatalf("profile fixture mismatch\nactual %s\nwant   %s", actual, bytes.TrimSpace(raw))
	}
	copyValue := Descriptor()
	copyValue.DefaultLimits.MaxBindings = 1
	if Descriptor().DefaultLimits.MaxBindings != DefaultMaxBindings {
		t.Fatal("Descriptor returned shared mutable state")
	}
}

func TestManifestCanonicalFixture(t *testing.T) {
	encoded, err := base64.StdEncoding.Strict().DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAAXNSR0ICQMB9xQAAABZJREFUeJwACQD2/wP/AAD/AUAgAQMADhwCZN2J4wgAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	document := oneImageDocument(contentHash(encoded))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig(2, 1))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := videoedit.CanonicalJSON(provider.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/manifest-v1.canonical.json")
	if err != nil {
		t.Fatalf("read fixture: %v\nactual %s", err, actual)
	}
	var decoded Manifest
	if err := wirejson.Decode(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, provider.Manifest()) || !bytes.Equal(bytes.TrimSpace(raw), actual) {
		t.Fatalf("manifest fixture mismatch\nactual %s\nwant   %s", actual, bytes.TrimSpace(raw))
	}
	if len(decoded.Assets) != 1 {
		t.Fatalf("manifest assets = %d", len(decoded.Assets))
	}
}
