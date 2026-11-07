package renderplanv2_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
)

func FuzzDecodeCanonicalPlan(f *testing.F) {
	for _, name := range []string{"gap", "mixed", "transition"} {
		raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-plan-" + name + ".canonical.json")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	for _, raw := range [][]byte{nil, []byte(`{}`), []byte(`{"schema_version":1,"schema_version":1}`), {0xff}} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		plan, err := renderplanv2.Decode(raw)
		if err != nil {
			if !reflect.DeepEqual(plan, renderplanv2.Plan{}) {
				t.Fatal("rejected input returned a partial plan")
			}
			return
		}
		encoded, err := renderplanv2.Canonical(plan)
		if err != nil || !bytes.Equal(encoded, raw) {
			t.Fatalf("accepted input is not its own canonical encoding: %v", err)
		}
		verified, err := renderplanv2.Verify(plan)
		if err != nil || !verified.Valid() {
			t.Fatalf("decoded plan cannot be verified: %v", err)
		}
		owned, err := verified.Canonical()
		if err != nil || !bytes.Equal(owned, raw) {
			t.Fatalf("verified bytes changed: %v", err)
		}
	})
}
