package renderapi_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func seedsV2(f *testing.F, prefix string) {
	f.Helper()
	files, err := filepath.Glob("../../conformance/fixtures/" + prefix + "*.canonical.json")
	if err != nil || len(files) == 0 {
		f.Fatalf("missing v2 canonical seed corpus: %v", err)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	for _, raw := range [][]byte{nil, []byte(`{}`), []byte(`null`), []byte(`{"schema_version":2,"schema_version":2}`), {0xff}} {
		f.Add(raw)
	}
}
func FuzzDecodeCanonicalManifestV2(f *testing.F) {
	seedsV2(f, "document-v2-execution-")
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := renderapi.DecodeExecutionManifestV2(raw)
		if err != nil {
			if !reflect.DeepEqual(m, renderapi.ExecutionManifestV2{}) {
				t.Fatal("rejected input returned partial manifest")
			}
			return
		}
		canonical, err := renderapi.CanonicalExecutionManifestV2(m)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatalf("accepted noncanonical manifest: %v", err)
		}
		verified, err := renderapi.VerifyExecutionManifestV2(m)
		if err != nil || !verified.Valid() {
			t.Fatalf("decoded manifest unverifiable: %v", err)
		}
		owned, err := verified.Canonical()
		if err != nil || !bytes.Equal(raw, owned) {
			t.Fatalf("verified bytes changed: %v", err)
		}
	})
}
func FuzzDecodeCanonicalRequestV2(f *testing.F) {
	seedsV2(f, "document-v2-render-request-")
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, err := renderapi.DecodeRenderRequestV2(raw)
		if err != nil {
			if !reflect.DeepEqual(r, renderapi.RenderRequestV2{}) {
				t.Fatal("rejected input returned partial request")
			}
			return
		}
		canonical, err := renderapi.CanonicalRenderRequestV2(r)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatalf("accepted noncanonical request: %v", err)
		}
		verified, err := renderapi.VerifyRenderRequestV2(r)
		if err != nil || !verified.Valid() {
			t.Fatalf("decoded request unverifiable: %v", err)
		}
		owned, err := verified.Canonical()
		if err != nil || !bytes.Equal(raw, owned) {
			t.Fatalf("verified bytes changed: %v", err)
		}
	})
}
