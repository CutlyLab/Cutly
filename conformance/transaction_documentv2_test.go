package conformance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	d "github.com/CutlyLab/Cutly/core/documentv2"
	tx "github.com/CutlyLab/Cutly/core/transactionv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

const transactionSchemaV2 = "cutly.document-v2-transaction.v2.schema.json"
const transactionNumbersV2 = `{"decimal":1.2500,"exponent":6.022e+23,"huge":9007199254740993,"negative_zero":-0,"small":1E-7}`

func transactionSchemasV2(t *testing.T) map[string]*jsonschema.Resolved {
	t.Helper()
	registry, names := map[string]*jsonschema.Schema{}, map[string]*jsonschema.Schema{}
	paths, err := filepath.Glob(filepath.Join(conformanceModuleRoot(t), "schemas", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		decodeStrict(t, raw, &schema)
		registry[schema.ID], names[filepath.Base(path)] = &schema, &schema
	}
	resolved := map[string]*jsonschema.Resolved{}
	for _, name := range []string{transactionSchemaV2, "cutly.transaction.v1.schema.json"} {
		schema, err := names[name].Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
			key := *uri
			key.Fragment = ""
			if schema := registry[key.String()]; schema != nil {
				return schema, nil
			}
			return nil, os.ErrNotExist
		}})
		if err != nil {
			t.Fatal(err)
		}
		resolved[name] = schema
	}
	return resolved
}

// Document canonicalization sorts objects while preserving RawMessage number
// tokens. The integer-only protocol canonicalizer is deliberately not used here.
func transactionCanonicalObjectV2(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func transactionBaseV2(t *testing.T) d.Document {
	t.Helper()
	doc, err := d.Decode(fixture(t, "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc.Metadata["numbers"] = json.RawMessage(transactionNumbersV2)
	return doc
}

func transactionCreateV2(t *testing.T, doc d.Document, id string, operations []tx.Operation) tx.Transaction {
	t.Helper()
	transaction, err := tx.Create(doc, tx.Transaction{ID: id, IdempotencyKey: id + "-key", Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}

func transactionOperationsV2(doc d.Document) []tx.Operation {
	position := 0
	asset, timeline := doc.Assets[0], doc.Timelines[0]
	track, clip := timeline.Tracks[0], timeline.Tracks[0].Clips[0]
	bus, lane, transition := doc.AudioBuses[0], clip.KeyframeLanes[0], timeline.Transitions[0]
	annotation := doc.Annotations[0]
	scope := tx.Scope{Kind: tx.ScopeDocument, ID: doc.ID}
	var operations []tx.Operation
	add := func(op tx.Operation) { op.ID = string(op.Kind); operations = append(operations, op) }
	for _, kind := range []tx.Kind{tx.AssetAdd, tx.AssetRemove, tx.AssetReplace} {
		edit := &tx.AssetEdit{AssetID: asset.ID}
		if kind != tx.AssetRemove {
			edit.Position, edit.Value = &position, &asset
		}
		add(tx.Operation{Kind: kind, Asset: edit})
	}
	for _, kind := range []tx.Kind{tx.TimelineAdd, tx.TimelineRemove, tx.TimelineReplace} {
		edit := &tx.TimelineEdit{TimelineID: timeline.ID}
		if kind != tx.TimelineRemove {
			edit.Position, edit.Value = &position, &timeline
		}
		add(tx.Operation{Kind: kind, Timeline: edit})
	}
	for _, kind := range []tx.Kind{tx.TrackAdd, tx.TrackRemove, tx.TrackReplace} {
		edit := &tx.TrackEdit{TimelineID: timeline.ID, TrackID: track.ID}
		if kind != tx.TrackRemove {
			edit.Position, edit.Value = &position, &track
		}
		add(tx.Operation{Kind: kind, Track: edit})
	}
	for _, kind := range []tx.Kind{tx.ClipAdd, tx.ClipRemove, tx.ClipReplace, tx.ClipDelete} {
		edit := &tx.ClipEdit{TrackID: track.ID, ClipID: clip.ID}
		if kind == tx.ClipAdd || kind == tx.ClipReplace {
			edit.Position, edit.Value = &position, &clip
		}
		add(tx.Operation{Kind: kind, Clip: edit})
	}
	for _, kind := range []tx.Kind{tx.BusAdd, tx.BusRemove, tx.BusReplace} {
		edit := &tx.BusEdit{BusID: bus.ID}
		if kind != tx.BusRemove {
			edit.Position, edit.Value = &position, &bus
		}
		add(tx.Operation{Kind: kind, Bus: edit})
	}
	right := clip
	right.ID = "split-right"
	add(tx.Operation{Kind: tx.ClipMove, Move: &tx.MoveEdit{ClipID: clip.ID, At: d.RationalTime{Value: 1, Rate: 3}}})
	add(tx.Operation{Kind: tx.ClipTrim, Trim: &tx.TrimEdit{ClipID: clip.ID, TimelineRange: clip.TimelineRange, SourceRange: clip.SourceRange}})
	add(tx.Operation{Kind: tx.ClipSplit, Split: &tx.SplitEdit{ClipID: clip.ID, At: d.RationalTime{Value: 1, Rate: 1}, Left: clip, Right: right}})
	add(tx.Operation{Kind: tx.ClipRetrack, Retrack: &tx.RetrackEdit{ClipID: clip.ID, TrackID: track.ID, Position: 0}})
	add(tx.Operation{Kind: tx.ClipSetDisabled, Disabled: &tx.DisabledEdit{ClipID: clip.ID, Disabled: false}})
	add(tx.Operation{Kind: tx.ClipSetAsset, AssetReference: &tx.AssetReferenceEdit{ClipID: clip.ID, AssetID: clip.AssetID}})
	add(tx.Operation{Kind: tx.PropertySet, Properties: &tx.PropertiesEdit{ClipID: clip.ID, Value: clip.PropertySet}})
	add(tx.Operation{Kind: tx.LaneSet, Lane: &tx.LaneEdit{ClipID: clip.ID, LaneID: lane.ID, Position: &position, Value: &lane}})
	add(tx.Operation{Kind: tx.LaneRemove, Lane: &tx.LaneEdit{ClipID: clip.ID, LaneID: lane.ID}})
	add(tx.Operation{Kind: tx.TransitionSet, Transition: &tx.TransitionEdit{TimelineID: timeline.ID, TransitionID: transition.ID, Position: &position, Value: &transition}})
	add(tx.Operation{Kind: tx.TransitionRemove, Transition: &tx.TransitionEdit{TimelineID: timeline.ID, TransitionID: transition.ID}})
	add(tx.Operation{Kind: tx.TrackRoute, Route: &tx.RouteEdit{TrackID: "track-audio", BusID: "bus-music"}})
	add(tx.Operation{Kind: tx.MetadataSet, Metadata: &tx.MetadataEdit{Scope: scope, Key: "numbers", Value: json.RawMessage(transactionNumbersV2)}})
	add(tx.Operation{Kind: tx.MetadataRemove, Metadata: &tx.MetadataEdit{Scope: scope, Key: "fixture"}})
	add(tx.Operation{Kind: tx.AnnotationSet, Annotation: &tx.AnnotationEdit{Scope: scope, AnnotationID: annotation.ID, Position: &position, Value: &annotation}})
	add(tx.Operation{Kind: tx.AnnotationRemove, Annotation: &tx.AnnotationEdit{Scope: scope, AnnotationID: annotation.ID}})
	return operations
}

func transactionNumberEditsV2(doc d.Document) []tx.Operation {
	position := 1
	scope := tx.Scope{Kind: tx.ScopeDocument, ID: doc.ID}
	annotation := d.Annotation{ID: "annotation-numbers", Namespace: "cutly.intent.v1", Value: json.RawMessage(transactionNumbersV2)}
	properties := *doc.Timelines[0].Tracks[0].Clips[0].PropertySet
	properties.Extensions = []d.ExtensionProperty{{Namespace: "cutly.note.v1", Value: json.RawMessage(transactionNumbersV2)}}
	asset := doc.Assets[0]
	asset.Metadata = map[string]json.RawMessage{"numbers": json.RawMessage(transactionNumbersV2)}
	zero := 0
	return []tx.Operation{
		{ID: "set-numbers", Kind: tx.MetadataSet, Metadata: &tx.MetadataEdit{Scope: scope, Key: "numbers", Value: json.RawMessage(transactionNumbersV2)}},
		{ID: "set-null", Kind: tx.MetadataSet, Metadata: &tx.MetadataEdit{Scope: scope, Key: "null", Value: json.RawMessage(`null`)}},
		{ID: "annotate", Kind: tx.AnnotationSet, Annotation: &tx.AnnotationEdit{Scope: scope, AnnotationID: annotation.ID, Position: &position, Value: &annotation}},
		{ID: "properties", Kind: tx.PropertySet, Properties: &tx.PropertiesEdit{ClipID: "clip-video-a", Value: &properties}},
		{ID: "asset-metadata", Kind: tx.AssetReplace, Asset: &tx.AssetEdit{AssetID: asset.ID, Position: &zero, Value: &asset}},
	}
}

// Opt-in authoring is confined to this stage's new prefix, never legacy fixtures.
// Normal tests only compare rebuilt bytes to fixed published bytes and hashes.
func publishTransactionFixtureV2(t *testing.T, name string, raw []byte) {
	t.Helper()
	if os.Getenv("CUTLY_UPDATE_TRANSACTIONV2_FIXTURES") != "1" {
		return
	}
	if !strings.HasPrefix(name, "document-v2-transaction-") {
		t.Fatal("out-of-scope fixture")
	}
	if err := os.WriteFile(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", name), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func assertTransactionVectorV2(t *testing.T, schema *jsonschema.Resolved, name string, transaction tx.Transaction) {
	t.Helper()
	raw, err := tx.CanonicalJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest, err := tx.Digest(transaction)
	if err != nil {
		t.Fatal(err)
	}
	for suffix, data := range map[string][]byte{
		".canonical.json": raw, ".valid.json": append(pretty.Bytes(), '\n'),
		".sha256": []byte(hex.EncodeToString(sum[:]) + "\n"), ".digest": []byte(digest + "\n"),
	} {
		publishTransactionFixtureV2(t, name+suffix, data)
		if !bytes.Equal(fixture(t, name+suffix), data) {
			t.Fatalf("fixed vector drift: %s%s", name, suffix)
		}
	}
	if fixed := transactionFixtureHashesV2[name]; fixed != hex.EncodeToString(sum[:]) {
		t.Fatalf("fixed canonical SHA-256 changed for %s: %x", name, sum)
	}
	for _, data := range [][]byte{raw, pretty.Bytes()} {
		if err := schema.Validate(decodeSchemaInstance(t, data)); err != nil {
			t.Fatalf("schema rejected %s: %v", name, err)
		}
	}
	decoded, err := tx.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.HasErrorDiagnostics(tx.Validate(decoded)) {
		t.Fatal("decoded vector failed validation")
	}
	roundtrip, err := tx.CanonicalJSON(decoded)
	if err != nil || !bytes.Equal(raw, roundtrip) {
		t.Fatalf("canonical roundtrip: %v", err)
	}
	if _, err := tx.Decode(pretty.Bytes()); !errors.Is(err, tx.ErrInvalid) {
		t.Fatalf("noncanonical accepted: %v", err)
	}
	unsigned := decoded
	unsigned.Digest = ""
	unsignedRaw, err := tx.CanonicalJSON(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	unsignedSum := sha256.Sum256(unsignedRaw)
	if digest != "sha256:"+hex.EncodeToString(unsignedSum[:]) {
		t.Fatal("digest does not exclude only digest member")
	}
}

var transactionFixtureHashesV2 = map[string]string{
	"document-v2-transaction-numbers":         "57c9588e8c501c18d6d5a638f2c1628295f4b9c57fd32d1efbddf2f534597f8b",
	"document-v2-transaction-all-operations":  "9399da387e77cb1721fb47813a58c03d1da766850562f525ad813e338fa8ff1c",
	"document-v2-transaction-numbers-inverse": "07bfc73c06f318e83301f404fba205441914d5e250dadc2f6aaee2b788864557",
}

func TestDocumentV2TransactionPublishedFixtures(t *testing.T) {
	schema := transactionSchemasV2(t)[transactionSchemaV2]
	doc := transactionBaseV2(t)
	base, err := d.CanonicalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	publishTransactionFixtureV2(t, "document-v2-transaction-base.document.json", base)
	if !bytes.Equal(base, fixture(t, "document-v2-transaction-base.document.json")) {
		t.Fatal("base document drift")
	}
	numbers := transactionCreateV2(t, doc, "transaction-numbers", transactionNumberEditsV2(doc))
	all := transactionCreateV2(t, doc, "transaction-all-operations", transactionOperationsV2(doc))
	// The union fixture is codec/schema coverage, not a claim that contradictory
	// add/remove/replace proposals apply to one base. Applicability belongs to Apply.
	for i := range all.Operations {
		all.Operations[i].Expect = &tx.Precondition{Hash: "sha256:" + strings.Repeat("a", 64)}
	}
	all.Digest = ""
	all, err = tx.Create(doc, all)
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionVectorV2(t, schema, "document-v2-transaction-all-operations", all)
	assertTransactionVectorV2(t, schema, "document-v2-transaction-numbers", numbers)
	result, err := tx.Apply(context.Background(), doc, numbers)
	if err != nil || d.HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("apply: %v %+v", err, result.Diagnostics)
	}
	if result.Document.Revision != doc.Revision+1 || result.TransactionDigest != numbers.Digest {
		t.Fatal("apply identity mismatch")
	}
	assertTransactionVectorV2(t, schema, "document-v2-transaction-numbers-inverse", result.Inverse)
	for _, op := range result.Inverse.Operations {
		if op.Expect == nil {
			t.Fatal("inverse missing enforced postimage")
		}
	}
	restored, err := tx.Apply(context.Background(), result.Document, result.Inverse)
	if err != nil || d.HasErrorDiagnostics(restored.Diagnostics) {
		t.Fatalf("inverse: %v %+v", err, restored.Diagnostics)
	}
	restored.Document.Revision = doc.Revision
	restoredRaw, err := d.CanonicalJSON(restored.Document)
	if err != nil || !bytes.Equal(restoredRaw, base) {
		t.Fatalf("inverse failed exact restoration: %v", err)
	}
	after, err := d.CanonicalJSON(result.Document)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{after, fixture(t, "document-v2-transaction-numbers.canonical.json")} {
		if !bytes.Contains(raw, []byte(transactionNumbersV2)) {
			t.Fatal("fraction/exponent/large integer/negative-zero tokens lost")
		}
		if _, err := canonicaljson.Canonicalize(raw); err == nil {
			t.Fatal("numeric vector accidentally fits integer-only canonicalizer")
		}
	}
	inputAfter, err := d.CanonicalJSON(doc)
	if err != nil || !bytes.Equal(inputAfter, base) {
		t.Fatal("Apply mutated input document")
	}
	changed := numbers
	changed.Digest = ""
	changed.Operations = append([]tx.Operation(nil), numbers.Operations...)
	metadata := *changed.Operations[0].Metadata
	metadata.Value = bytes.ReplaceAll(metadata.Value, []byte("1.2500"), []byte("1.25"))
	changed.Operations[0].Metadata = &metadata
	changedDigest, err := tx.Digest(changed)
	if err != nil || changedDigest == numbers.Digest {
		t.Fatalf("lexically distinct numeric intent lost digest identity: %v", err)
	}
}

func transactionObjectV2(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	return decodeExactJSON(t, raw).(map[string]any)
}

func TestDocumentV2TransactionOperationSchemaCoverage(t *testing.T) {
	schema := transactionSchemasV2(t)[transactionSchemaV2]
	root := readSchemaObject(t, filepath.Join(conformanceModuleRoot(t), "schemas", transactionSchemaV2))
	defs := root["$defs"].(map[string]any)
	branches := defs["operation"].(map[string]any)["oneOf"].([]any)
	allRaw := fixture(t, "document-v2-transaction-all-operations.canonical.json")
	all := transactionObjectV2(t, allRaw)
	operations := all["operations"].([]any)
	// Core confirmed the stable model has 32 kinds, not the initial count of 33.
	if len(branches) != 32 || len(operations) != len(branches) {
		t.Fatalf("union inventory mismatch: %d branches %d operations", len(branches), len(operations))
	}
	seen := map[string]bool{}
	for _, branch := range branches {
		ref := branch.(map[string]any)["$ref"].(string)
		definition := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		kind := definition["properties"].(map[string]any)["op"].(map[string]any)["const"].(string)
		if seen[kind] {
			t.Fatalf("duplicate kind %s", kind)
		}
		seen[kind] = true
	}
	for _, value := range operations {
		op := value.(map[string]any)
		kind := op["op"].(string)
		if !seen[kind] {
			t.Fatalf("operation missing branch: %s", kind)
		}
		delete(seen, kind)
		t.Run(kind, func(t *testing.T) {
			candidate := func() map[string]any {
				m := transactionObjectV2(t, allRaw)
				delete(m, "digest")
				m["operations"] = []any{transactionObjectV2(t, transactionCanonicalObjectV2(t, op))}
				return m
			}
			assert := func(m map[string]any, valid bool) {
				t.Helper()
				raw := transactionCanonicalObjectV2(t, m)
				schemaErr := schema.Validate(decodeSchemaInstance(t, raw))
				_, decodeErr := tx.Decode(raw)
				if (schemaErr == nil) != valid || (decodeErr == nil) != valid {
					t.Fatalf("valid=%t schema=%v decoder=%v", valid, schemaErr, decodeErr)
				}
			}
			assert(candidate(), true)
			payload := ""
			for key := range op {
				if key != "id" && key != "op" && key != "expect" {
					payload = key
				}
			}
			for _, field := range []string{"id", "op", payload} {
				m := candidate()
				delete(m["operations"].([]any)[0].(map[string]any), field)
				assert(m, false)
				m = candidate()
				m["operations"].([]any)[0].(map[string]any)[field] = nil
				assert(m, false)
			}
			for _, field := range []string{"unknown", "Expect", "asset_reference", "route"} {
				if field == payload {
					continue
				}
				m := candidate()
				m["operations"].([]any)[0].(map[string]any)[field] = map[string]any{}
				assert(m, false)
			}
			for _, expect := range []any{nil, map[string]any{}, map[string]any{"hash": nil}, map[string]any{"hash": "sha256:bad"}, map[string]any{"hash": op["expect"].(map[string]any)["hash"], "ignored": true}} {
				m := candidate()
				m["operations"].([]any)[0].(map[string]any)["expect"] = expect
				assert(m, false)
			}
			m := candidate()
			delete(m["operations"].([]any)[0].(map[string]any), "expect")
			assert(m, true)
			for field := range op[payload].(map[string]any) {
				m := candidate()
				p := m["operations"].([]any)[0].(map[string]any)[payload].(map[string]any)
				p[field] = nil
				// Metadata value is open JSON, where explicit null is meaningful.
				assert(m, kind == "metadata_set" && field == "value")
				m = candidate()
				p = m["operations"].([]any)[0].(map[string]any)[payload].(map[string]any)
				delete(p, field)
				assert(m, kind == "property_set" && field == "value")
			}
			m = candidate()
			m["operations"].([]any)[0].(map[string]any)[payload].(map[string]any)["unknown"] = true
			assert(m, false)
			p := op[payload].(map[string]any)
			if _, hasPosition := p["position"]; hasPosition {
				// Positions use the codec's portable int32 domain, not the
				// inherited Document int64 rational/revision domains. These
				// canonical proposals test codec shape, not Apply index existence.
				for _, boundary := range []struct {
					position json.Number
					valid    bool
				}{
					{json.Number("-1"), false},
					{json.Number("2147483647"), true},
					{json.Number("2147483648"), false},
				} {
					m = candidate()
					m["operations"].([]any)[0].(map[string]any)[payload].(map[string]any)["position"] = boundary.position
					assert(m, boundary.valid)
				}
			} else if strings.HasSuffix(kind, "remove") || kind == "clip_delete" {
				for _, field := range []string{"position", "value"} {
					m = candidate()
					m["operations"].([]any)[0].(map[string]any)[payload].(map[string]any)[field] = 0
					assert(m, false)
				}
			}
		})
	}
	if len(seen) != 0 {
		t.Fatalf("unexercised union branches: %v", seen)
	}
	// Required zero-valued bool/position survive canonicalization; omitted property
	// value clears, and an empty routing bus explicitly clears a route.
	m := transactionObjectV2(t, allRaw)
	delete(m, "digest")
	m["operations"] = []any{
		map[string]any{"id": "clear-property", "op": "property_set", "properties": map[string]any{"clip_id": "clip-video-a"}},
		map[string]any{"id": "clear-route", "op": "track_route", "route": map[string]any{"track_id": "track-audio", "bus_id": ""}},
	}
	raw := transactionCanonicalObjectV2(t, m)
	if err := schema.Validate(decodeSchemaInstance(t, raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Decode(raw); err != nil {
		t.Fatal(err)
	}
}

type transactionAttackV2 struct {
	name          string
	schemaInvalid bool
	mutate        func(map[string]any)
	lexical       func([]byte) []byte
}

func TestDocumentV2TransactionHostileFixtures(t *testing.T) {
	schema := transactionSchemasV2(t)[transactionSchemaV2]
	attacks := []transactionAttackV2{
		{name: "missing-profile", schemaInvalid: true, mutate: func(m map[string]any) { delete(m, "profile") }},
		{name: "legacy-version", schemaInvalid: true, mutate: func(m map[string]any) { m["schema_version"] = 1 }},
		{name: "wrong-profile", schemaInvalid: true, mutate: func(m map[string]any) { m["profile"] = canonicaljson.Profile }},
		{name: "unknown-root", schemaInvalid: true, mutate: func(m map[string]any) { m["authorized"] = true }},
		{name: "case-folded-key", schemaInvalid: true, mutate: func(m map[string]any) { m["ID"] = m["id"]; delete(m, "id") }},
		{name: "null-operations", schemaInvalid: true, mutate: func(m map[string]any) { m["operations"] = nil }},
		{name: "empty-operations", schemaInvalid: true, mutate: func(m map[string]any) { m["operations"] = []any{} }},
		{name: "null-operation", schemaInvalid: true, mutate: func(m map[string]any) { m["operations"] = []any{nil} }},
		{name: "unknown-kind", schemaInvalid: true, mutate: func(m map[string]any) { m["operations"].([]any)[0].(map[string]any)["op"] = "insert" }},
		{name: "missing-value", schemaInvalid: true, mutate: func(m map[string]any) {
			delete(m["operations"].([]any)[0].(map[string]any)["metadata"].(map[string]any), "value")
		}},
		{name: "null-scope", schemaInvalid: true, mutate: func(m map[string]any) {
			m["operations"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["scope"] = nil
		}},
		{name: "null-property-value", schemaInvalid: true, mutate: func(m map[string]any) {
			m["operations"].([]any)[3].(map[string]any)["properties"].(map[string]any)["value"] = nil
		}},
		{name: "unknown-property", schemaInvalid: true, mutate: func(m map[string]any) {
			m["operations"].([]any)[3].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)["blur"] = true
		}},
		{name: "null-expect", schemaInvalid: true, mutate: func(m map[string]any) { m["operations"].([]any)[0].(map[string]any)["expect"] = nil }},
		{name: "missing-expect-hash", schemaInvalid: true, mutate: func(m map[string]any) { m["operations"].([]any)[0].(map[string]any)["expect"] = map[string]any{} }},
		{name: "second-payload", schemaInvalid: true, mutate: func(m map[string]any) {
			m["operations"].([]any)[0].(map[string]any)["route"] = map[string]any{"track_id": "track-audio", "bus_id": "bus-music"}
		}},
		{name: "missing-position", schemaInvalid: true, mutate: func(m map[string]any) {
			delete(m["operations"].([]any)[4].(map[string]any)["asset"].(map[string]any), "position")
		}},
		{name: "null-position", schemaInvalid: true, mutate: func(m map[string]any) {
			m["operations"].([]any)[4].(map[string]any)["asset"].(map[string]any)["position"] = nil
		}},
		{name: "negative-position", schemaInvalid: true, mutate: func(m map[string]any) {
			m["operations"].([]any)[4].(map[string]any)["asset"].(map[string]any)["position"] = -1
		}},
		{name: "target-value-id-mismatch", mutate: func(m map[string]any) {
			m["operations"].([]any)[4].(map[string]any)["asset"].(map[string]any)["value"].(map[string]any)["id"] = "different-asset"
		}},
		{name: "padded-id", schemaInvalid: true, mutate: func(m map[string]any) { m["id"] = " padded " }},
		{name: "bad-hash", schemaInvalid: true, mutate: func(m map[string]any) { m["expected_document_hash"] = "sha256:BAD" }},
		{name: "negative-revision", schemaInvalid: true, mutate: func(m map[string]any) { m["expected_revision"] = json.Number("-1") }},
		{name: "duplicate-op-id", mutate: func(m map[string]any) {
			ops := m["operations"].([]any)
			ops[1].(map[string]any)["id"] = ops[0].(map[string]any)["id"]
		}},
		{name: "stale-digest", mutate: func(m map[string]any) { m["digest"] = "sha256:" + strings.Repeat("0", 64) }},
		{name: "revision-decimal", mutate: func(m map[string]any) { m["expected_revision"] = json.Number("8.0") }},
		{name: "revision-exponent", mutate: func(m map[string]any) { m["expected_revision"] = json.Number("8e0") }},
		{name: "revision-overflow", mutate: func(m map[string]any) { m["expected_revision"] = json.Number("18446744073709551616") }},
		{name: "whitespace", lexical: func(raw []byte) []byte { return append(raw, '\n') }},
		{name: "trailing-value", lexical: func(raw []byte) []byte { return append(raw, []byte(`{}`)...) }},
		{name: "duplicate-root", lexical: func(raw []byte) []byte { return append([]byte(`{"id":"duplicate",`), raw[1:]...) }},
		{name: "duplicate-open-json", lexical: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"decimal":1.2500`), []byte(`"decimal":1.2500,"decimal":2`), 1)
		}},
		{name: "unpaired-surrogate", lexical: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"transaction-numbers"`), []byte(`"\ud800"`), 1)
		}},
		{name: "invalid-utf8", lexical: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"transaction-numbers"`), []byte{'"', 0xff, '"'}, 1)
		}},
		{name: "noncanonical-escape", lexical: func(raw []byte) []byte {
			escaped := append([]byte{'"', '\\'}, []byte(`u0074ransaction-numbers"`)...)
			return bytes.Replace(raw, []byte(`"transaction-numbers"`), escaped, 1)
		}},
		{name: "reordered-root", lexical: func(raw []byte) []byte {
			v := []byte(`"id":"transaction-numbers",`)
			raw = bytes.Replace(raw, v, nil, 1)
			return append(append([]byte{'{'}, v...), raw[1:]...)
		}},
	}
	expected := map[string]bool{}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			m := transactionObjectV2(t, fixture(t, "document-v2-transaction-numbers.canonical.json"))
			delete(m, "digest")
			if attack.mutate != nil {
				attack.mutate(m)
			}
			raw := transactionCanonicalObjectV2(t, m)
			if attack.lexical != nil {
				raw = attack.lexical(raw)
			}
			name := "document-v2-transaction-" + attack.name + ".invalid.json"
			expected[name] = true
			publishTransactionFixtureV2(t, name, raw)
			if !bytes.Equal(raw, fixture(t, name)) {
				t.Fatal("hostile fixture drift")
			}
			got, err := tx.Decode(raw)
			if !errors.Is(err, tx.ErrInvalid) || !reflect.DeepEqual(got, tx.Transaction{}) {
				t.Fatalf("accepted hostile record or leaked partial value: %v", err)
			}
			// Schema sees parsed values, not duplicate keys, Unicode spelling,
			// canonical layout or integer token spelling. The engine also rounds
			// uint64 bounds; adjacent overflow is a strict codec assertion only.
			if attack.schemaInvalid {
				if err := schema.Validate(decodeSchemaInstance(t, raw)); err == nil {
					t.Fatal("schema accepted structural attack")
				}
			}
		})
	}
	paths, err := filepath.Glob(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", "document-v2-transaction-*.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !expected[filepath.Base(path)] {
			t.Fatalf("unregistered hostile fixture %s", path)
		}
		delete(expected, filepath.Base(path))
	}
	if len(expected) != 0 {
		t.Fatalf("missing hostile fixtures: %v", expected)
	}
}

func TestDocumentV2TransactionVersionIsolation(t *testing.T) {
	schemas := transactionSchemasV2(t)
	legacy := fixture(t, "transaction-v1.canonical.json")
	modern := fixture(t, "document-v2-transaction-numbers.canonical.json")
	if _, err := tx.Decode(legacy); !errors.Is(err, tx.ErrInvalid) {
		t.Fatalf("v2 accepted v1: %v", err)
	}
	var old videoedit.Transaction
	if err := wirejson.Decode(modern, &old); err == nil {
		t.Fatal("strict v1 typed boundary accepted v2")
	}
	if err := schemas[transactionSchemaV2].Validate(decodeSchemaInstance(t, legacy)); err == nil {
		t.Fatal("v2 schema accepted v1")
	}
	if err := schemas["cutly.transaction.v1.schema.json"].Validate(decodeSchemaInstance(t, modern)); err == nil {
		t.Fatal("v1 schema accepted v2")
	}
}

func TestDocumentV2TransactionApplyBindingAndExpect(t *testing.T) {
	doc := transactionBaseV2(t)
	base, err := d.CanonicalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, code string
		mutate     func(*tx.Transaction)
	}{
		{"document", tx.CodeDocumentConflict, func(proposal *tx.Transaction) { proposal.DocumentID = "different-document" }},
		{"revision", tx.CodeRevisionConflict, func(proposal *tx.Transaction) { proposal.ExpectedRevision++ }},
		{"hash", tx.CodeHashConflict, func(proposal *tx.Transaction) { proposal.ExpectedDocumentHash = "sha256:" + strings.Repeat("0", 64) }},
		{"expect", tx.CodePrecondition, func(proposal *tx.Transaction) {
			proposal.Operations[0].Expect = &tx.Precondition{Hash: "sha256:" + strings.Repeat("0", 64)}
		}},
		{"replace-position", tx.CodeConflict, func(proposal *tx.Transaction) { wrong := 1; proposal.Operations[4].Asset.Position = &wrong }},
	} {
		t.Run(test.name, func(t *testing.T) {
			proposal := transactionCreateV2(t, doc, "binding-"+test.name, transactionNumberEditsV2(doc))
			test.mutate(&proposal)
			proposal.Digest = ""
			// These records are structurally valid and self-consistent. Re-sign
			// before Apply so a stale digest cannot mask binding/expect checks.
			proposal.Digest, err = tx.Digest(proposal)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := tx.CanonicalJSON(proposal)
			if err != nil {
				t.Fatal(err)
			}
			if err := transactionSchemasV2(t)[transactionSchemaV2].Validate(decodeSchemaInstance(t, raw)); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Decode(raw); err != nil {
				t.Fatal(err)
			}
			result, err := tx.Apply(context.Background(), doc, proposal)
			if err != nil || len(result.Diagnostics) == 0 || result.Diagnostics[0].Code != test.code {
				t.Fatalf("want %s, got %v %+v", test.code, err, result.Diagnostics)
			}
			result.Diagnostics = nil
			if !reflect.DeepEqual(result, tx.Result{}) {
				t.Fatal("failure leaked candidate/inverse/hash/impact")
			}
			after, err := d.CanonicalJSON(doc)
			if err != nil || !bytes.Equal(after, base) {
				t.Fatal("rejected proposal mutated input")
			}
		})
	}
}

func TestDocumentV2TransactionDecoderBudgets(t *testing.T) {
	m := transactionObjectV2(t, fixture(t, "document-v2-transaction-numbers.canonical.json"))
	delete(m, "digest")
	base := transactionCanonicalObjectV2(t, m)
	for name, raw := range map[string][]byte{
		"bytes":    bytes.Repeat([]byte(" "), tx.MaxTransactionBytes+1),
		"depth":    bytes.Replace(base, []byte(transactionNumbersV2), []byte(strings.Repeat("[", tx.MaxDepth+1)+"0"+strings.Repeat("]", tx.MaxDepth+1)), 1),
		"string":   bytes.Replace(base, []byte(`"transaction-numbers"`), []byte(`"`+strings.Repeat("x", tx.MaxStringBytes+1)+`"`), 1),
		"id-bytes": bytes.Replace(base, []byte(`"transaction-numbers"`), []byte(`"`+strings.Repeat("界", 86)+`"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := tx.Decode(raw); !errors.Is(err, tx.ErrInvalid) || !reflect.DeepEqual(got, tx.Transaction{}) {
				t.Fatalf("budget bypass: %v", err)
			}
		})
	}
	operations := make([]any, tx.MaxOperations+1)
	for i := range operations {
		operations[i] = map[string]any{"id": fmt.Sprintf("op-%d", i), "op": "metadata_set", "metadata": map[string]any{"scope": map[string]any{"kind": "document", "id": "fixture-document-v2"}, "key": "k", "value": nil}}
	}
	m["operations"] = operations
	raw := transactionCanonicalObjectV2(t, m)
	if _, err := tx.Decode(raw); !errors.Is(err, tx.ErrInvalid) {
		t.Fatalf("operation budget bypass: %v", err)
	}
	if err := transactionSchemasV2(t)[transactionSchemaV2].Validate(decodeSchemaInstance(t, raw)); err == nil {
		t.Fatal("schema accepted excessive operation count")
	}
}
