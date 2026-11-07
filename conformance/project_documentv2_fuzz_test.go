package conformance_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	local "github.com/CutlyLab/Cutly/project/localv2"
)

func FuzzDocumentV2ProjectRecords(f *testing.F) {
	for _, kind := range []string{"plan", "commit"} {
		raw, err := os.ReadFile("fixtures/document-v2-project-" + kind + ".canonical.json")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(kind == "plan", raw)
		for _, name := range []string{"wrong-version", "null-timelines", "unknown-impact"} {
			raw, err := os.ReadFile("fixtures/document-v2-project-" + kind + "-" + name + ".invalid.json")
			if err != nil {
				f.Fatal(err)
			}
			f.Add(kind == "plan", raw)
		}
	}
	f.Add(true, []byte(`{}`))
	f.Add(false, []byte(`null`))
	f.Fuzz(func(t *testing.T, plan bool, raw []byte) {
		// Keep mutation throughput useful; deterministic tests cover profile ceilings.
		if len(raw) > 1<<20 {
			t.Skip()
		}
		original := append([]byte(nil), raw...)
		var canonical []byte
		if plan {
			record, err := local.DecodePlanRecord(raw)
			if err != nil {
				if !reflect.DeepEqual(record, local.PlanRecord{}) {
					t.Fatal("failed decode retained plan")
				}
				return
			}
			canonical, err = local.EncodePlanRecord(record)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			record, err := local.DecodeCommitRecord(raw)
			if err != nil {
				if !reflect.DeepEqual(record, local.CommitRecord{}) {
					t.Fatal("failed decode retained commit")
				}
				return
			}
			canonical, err = local.EncodeCommitRecord(record)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(raw, original) || !bytes.Equal(canonical, raw) {
			t.Fatal("decode/encode changed canonical input")
		}
	})
}
