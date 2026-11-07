package conformance_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

// The pretty schema examples and the canonical wire records describe the same
// value, but only the latter are accepted at the strict executable boundary.
func TestDocumentV2RenderPlanPublishedFixtures(t *testing.T) {
	root := conformanceModuleRoot(t)
	rawSchema, err := os.ReadFile(filepath.Join(root, "schemas", "cutly.document-v2-render-plan.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	decodeStrict(t, rawSchema, &schema)
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gap", "mixed", "transition"} {
		t.Run(name, func(t *testing.T) {
			prefix := "document-v2-render-plan-" + name
			valid := fixture(t, prefix+".valid.json")
			canonical := fixture(t, prefix+".canonical.json")
			for _, raw := range [][]byte{valid, canonical} {
				if err := resolved.Validate(decodeSchemaInstance(t, raw)); err != nil {
					t.Fatalf("published schema rejected fixture: %v", err)
				}
			}
			normalized, err := canonicaljson.Canonicalize(valid)
			if err != nil || !bytes.Equal(normalized, canonical) {
				t.Fatalf("valid/canonical fixture disagreement: %v", err)
			}
			plan, err := renderplanv2.Decode(canonical)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := renderplanv2.Canonical(plan)
			if err != nil || !bytes.Equal(encoded, canonical) {
				t.Fatalf("canonical encoder drift: %v", err)
			}
			if _, err := renderplanv2.Decode(append(append([]byte(nil), canonical...), '\n')); err == nil {
				t.Fatal("strict decoder accepted noncanonical trailing whitespace")
			}
			if _, err := videoedit.DecodeRenderPlan(canonical); err == nil {
				t.Fatal("legacy decoder accepted Document v2 semantic plan")
			}
		})
	}
	for _, name := range []string{"render-plan-v2.valid.json", "render-plan-v3.canonical.json", "document-v2.canonical.json"} {
		if _, err := renderplanv2.Decode(fixture(t, name)); err == nil {
			t.Fatalf("Document v2 plan decoder accepted %s", name)
		}
	}
}
