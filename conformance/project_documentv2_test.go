package conformance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	d "github.com/CutlyLab/Cutly/core/documentv2"
	tx "github.com/CutlyLab/Cutly/core/transactionv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	local "github.com/CutlyLab/Cutly/project/localv2"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

func projectRecordsV2(t *testing.T) (local.PlanRecord, local.CommitRecord) {
	t.Helper()
	doc := transactionBaseV2(t)
	proposal := transactionCreateV2(t, doc, "project-metadata", []tx.Operation{{ID: "metadata", Kind: tx.MetadataSet, Metadata: &tx.MetadataEdit{Scope: tx.Scope{Kind: tx.ScopeDocument, ID: doc.ID}, Key: "project_numbers", Value: json.RawMessage(transactionNumbersV2)}}})
	result, err := tx.Apply(context.Background(), doc, proposal)
	if err != nil || d.HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("apply: %v %+v", err, result.Diagnostics)
	}
	plan := local.PlanRecord{Version: 2, Transaction: proposal, Digest: result.TransactionDigest, BaseDocumentHash: proposal.ExpectedDocumentHash, CandidateDocumentHash: result.DocumentHash, Inverse: result.Inverse, Impact: result.Impact}
	raw, err := local.EncodePlanRecord(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	commit := local.CommitRecord{Version: 2, Sequence: 2, PlanHash: "sha256:" + hex.EncodeToString(sum[:]), TransactionID: proposal.ID, IdempotencyKey: proposal.IdempotencyKey, Digest: result.TransactionDigest, BaseRevision: doc.Revision, NewRevision: result.Document.Revision, BaseDocumentHash: proposal.ExpectedDocumentHash, NewDocumentHash: result.DocumentHash, Inverse: result.Inverse, Impact: result.Impact}
	return plan, commit
}

func projectSchemaV2(t *testing.T, kind string) *jsonschema.Resolved {
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
		var s jsonschema.Schema
		decodeStrict(t, raw, &s)
		registry[s.ID] = &s
	}
	id := "https://schemas.cutly.dev/cutly.project-" + kind + ".document-v2.v2.schema.json"
	schema, err := registry[id].Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		copy := *uri
		copy.Fragment = ""
		if s := registry[copy.String()]; s != nil {
			return s, nil
		}
		return nil, os.ErrNotExist
	}})
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestDocumentV2ProjectPublishedFixtures(t *testing.T) {
	plan, commit := projectRecordsV2(t)
	hashes := map[string]string{
		"plan":    "d8d3bf96f8eea87e79e11cbc617e89a85ff7e7d23fcfa4fb6f45607b0c1f1b4f",
		"commit":  "d82b1233687d48692e6e7228a31da53815063c37a29d29c5a7588764cffbaa84",
		"history": "3931e6c9ef2fdf1744ed37f2d743b53feadfe1bb000aae6d68ad6fa113f4e29b",
	}
	for _, tc := range []struct {
		kind  string
		value any
	}{{"plan", plan}, {"commit", commit}, {"history", []local.CommitRecord{commit}}} {
		t.Run(tc.kind, func(t *testing.T) {
			raw, err := videoedit.CanonicalJSON(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			name := "document-v2-project-" + tc.kind
			if !bytes.Equal(raw, fixture(t, name+".canonical.json")) {
				t.Fatal("fixed canonical fixture drift")
			}
			sum := sha256.Sum256(raw)
			want := strings.TrimSpace(string(fixture(t, name+".sha256")))
			if hex.EncodeToString(sum[:]) != want || want != hashes[tc.kind] {
				t.Fatal("fixed SHA256 sidecar mismatch")
			}
			var pretty any
			dec := json.NewDecoder(bytes.NewReader(fixture(t, name+".valid.json")))
			dec.UseNumber()
			if err := dec.Decode(&pretty); err != nil {
				t.Fatal(err)
			}
			canonical, err := videoedit.CanonicalJSON(pretty)
			if err != nil || !bytes.Equal(canonical, raw) {
				t.Fatal("pretty/canonical mismatch")
			}
			schema := projectSchemaV2(t, tc.kind)
			if err := schema.Validate(decodeSchemaInstance(t, raw)); err != nil {
				t.Fatal(err)
			}
			switch tc.kind {
			case "plan":
				got, err := local.DecodePlanRecord(raw)
				if err != nil {
					t.Fatal(err)
				}
				again, err := local.EncodePlanRecord(got)
				if err != nil || !bytes.Equal(again, raw) {
					t.Fatal("plan roundtrip")
				}
				if !bytes.Contains(raw, []byte(transactionNumbersV2)) {
					t.Fatal("number tokens changed")
				}
			case "commit":
				got, err := local.DecodeCommitRecord(raw)
				if err != nil {
					t.Fatal(err)
				}
				again, err := local.EncodeCommitRecord(got)
				if err != nil || !bytes.Equal(again, raw) {
					t.Fatal("commit roundtrip")
				}
			}
		})
	}
}

func TestDocumentV2ProjectRecordRejections(t *testing.T) {
	plan, commit := projectRecordsV2(t)
	for _, kind := range []string{"plan", "commit"} {
		t.Run(kind, func(t *testing.T) {
			var value any = plan
			if kind == "commit" {
				value = commit
			}
			raw := transactionCanonicalObjectV2(t, value)
			decode := func(raw []byte) error {
				if kind == "plan" {
					got, err := local.DecodePlanRecord(raw)
					if err != nil && !reflect.DeepEqual(got, local.PlanRecord{}) {
						t.Fatal("plan failure leaked value")
					}
					return err
				}
				got, err := local.DecodeCommitRecord(raw)
				if err != nil && !reflect.DeepEqual(got, local.CommitRecord{}) {
					t.Fatal("commit failure leaked value")
				}
				return err
			}
			for _, name := range []string{"wrong-version", "null-timelines", "unknown-impact"} {
				bad := fixture(t, "document-v2-project-"+kind+"-"+name+".invalid.json")
				if decode(bad) == nil {
					t.Fatalf("published invalid fixture %s accepted", name)
				}
				if projectSchemaV2(t, kind).Validate(decodeSchemaInstance(t, bad)) == nil {
					t.Fatalf("schema accepted %s", name)
				}
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			for key := range object {
				t.Run("missing-"+key, func(t *testing.T) {
					copy := map[string]json.RawMessage{}
					for k, v := range object {
						if k != key {
							copy[k] = v
						}
					}
					bad := transactionCanonicalObjectV2(t, copy)
					if decode(bad) == nil {
						t.Fatal("missing field accepted")
					}
					if projectSchemaV2(t, kind).Validate(decodeSchemaInstance(t, bad)) == nil {
						t.Fatal("schema accepted missing field")
					}
				})
			}
			for _, bad := range [][]byte{append([]byte(" "), raw...), append(append([]byte{}, raw...), '\n'), bytes.Replace(raw, []byte(`"version":2`), []byte(`"version":1`), 1), bytes.Replace(raw, []byte(`"version":2`), []byte(`"version":2,"version":2`), 1), bytes.Replace(raw, []byte(`"impact":{`), []byte(`"impact":{"unknown":true,`), 1), bytes.Replace(raw, []byte(`"timeline_ids":[]`), []byte(`"timeline_ids":null`), 1)} {
				if decode(bad) == nil {
					t.Fatalf("malformed %s accepted", kind)
				}
			}
			object["unknown"] = json.RawMessage(`true`)
			if decode(transactionCanonicalObjectV2(t, object)) == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
	// Internally valid individual records still cannot substitute for another record kind.
	p, _ := local.EncodePlanRecord(plan)
	c, _ := local.EncodeCommitRecord(commit)
	if _, err := local.DecodePlanRecord(c); err == nil {
		t.Fatal("commit accepted as plan")
	}
	if _, err := local.DecodeCommitRecord(p); err == nil {
		t.Fatal("plan accepted as commit")
	}
	plan.Inverse.ExpectedDocumentHash = plan.BaseDocumentHash
	plan.Inverse.Digest = ""
	if _, err := local.EncodePlanRecord(plan); err == nil {
		t.Fatal("contradictory inverse binding accepted")
	}
	commit.NewRevision = commit.BaseRevision
	if _, err := local.EncodeCommitRecord(commit); err == nil {
		t.Fatal("nonincrementing revision accepted")
	}
}

func TestDocumentV2ProjectHistorySchema(t *testing.T) {
	schema := projectSchemaV2(t, "history")
	for _, value := range []any{nil, map[string]any{}, []any{nil}, make([]any, local.MaxHistoryPage+1)} {
		if err := schema.Validate(value); err == nil {
			t.Fatal("invalid history page accepted")
		}
	}
	if err := schema.Validate([]any{}); err != nil {
		t.Fatal(err)
	}
}
