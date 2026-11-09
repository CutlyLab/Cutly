package conformance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

func preparedSchemaV2(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(conformanceModuleRoot(t), "schemas", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]*jsonschema.Schema{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		registry[schema.ID] = &schema
	}
	root := registry["https://cutly.dev/schemas/cutly.prepared-environment.document-v2.v2.schema.json"]
	if root == nil {
		t.Fatal("missing prepared schema")
	}
	resolved, err := root.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		copy := *uri
		copy.Fragment = ""
		if schema := registry[copy.String()]; schema != nil {
			return schema, nil
		}
		return nil, fmt.Errorf("offline schema missing %s", copy.String())
	}})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestPreparedDocumentV2FixedCorpus(t *testing.T) {
	schema := preparedSchemaV2(t)
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile("fixtures/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	ctx := context.Background()
	canonical := read("document-v2-prepared-gap.canonical.json")
	if err := schema.Validate(decodeSchemaInstance(t, canonical)); err != nil {
		t.Fatal(err)
	}
	wantHash := strings.TrimSpace(string(read("document-v2-prepared-gap.sha256")))
	if got := fmt.Sprintf("%x", sha256.Sum256(canonical)); got != wantHash {
		t.Fatalf("fixed canonical hash drift: %s != %s", got, wantHash)
	}
	for _, suffix := range []string{"valid.json", "canonical.json"} {
		environment, err := prepared.DecodeEnvironmentV2(ctx, read("document-v2-prepared-gap."+suffix))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := prepared.CanonicalEnvironmentV2(ctx, environment)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatalf("prepared canonical drift: %v", err)
		}
	}
	doc, err := documentv2.Decode(read("document-v2-render-plan-gap.document.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := renderplanv2.Decode(bytes.TrimSpace(read("document-v2-render-plan-gap.canonical.json")))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderapi.DecodeExecutionManifestV2(bytes.TrimSpace(read("document-v2-execution-gap-silence.canonical.json")))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := prepared.NewEnvironmentV2(ctx, doc, plan, manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := prepared.CanonicalEnvironmentV2(ctx, environment)
	if err != nil || !bytes.Equal(raw, canonical) {
		t.Fatalf("independent builder differs from fixed record: %v", err)
	}
	numbers := read("document-v2-prepared-numbers.canonical.json")
	if err := schema.Validate(decodeSchemaInstance(t, numbers)); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(numbers)); got != strings.TrimSpace(string(read("document-v2-prepared-numbers.sha256"))) {
		t.Fatal("number-preserving fixture hash drift")
	}
	for _, suffix := range []string{"valid.json", "canonical.json"} {
		got, err := prepared.DecodeEnvironmentV2(ctx, read("document-v2-prepared-numbers."+suffix))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := prepared.CanonicalEnvironmentV2(ctx, got)
		if err != nil || !bytes.Equal(raw, numbers) {
			t.Fatalf("number-preserving fixture roundtrip: %v", err)
		}
		for _, lexeme := range []string{"1.2300", "1e+009", "9007199254740993", "1234567890123456789012345678901234567890", "-0"} {
			if !bytes.Contains(got.Document.Metadata["numbers"], []byte(lexeme)) {
				t.Fatalf("metadata lexeme lost: %s", lexeme)
			}
		}
	}
	for _, name := range []string{"wrong-version", "unknown-root", "null-document"} {
		t.Run(name, func(t *testing.T) {
			raw := read("document-v2-prepared-" + name + ".invalid.json")
			if err := schema.Validate(decodeSchemaInstance(t, raw)); err == nil {
				t.Fatal("schema accepted invalid corpus")
			}
			got, err := prepared.DecodeEnvironmentV2(ctx, raw)
			if err == nil || !reflect.DeepEqual(got, prepared.EnvironmentV2{}) {
				t.Fatalf("invalid record did not fail zero: %v", err)
			}
		})
	}
}
