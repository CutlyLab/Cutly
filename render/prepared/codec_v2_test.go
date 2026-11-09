package prepared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func environmentFixtureV2(t *testing.T, name string) EnvironmentV2 {
	t.Helper()
	requestName := name
	if name == "gap" {
		requestName = "gap-silence"
	}
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-request-" + requestName + ".canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := renderapi.DecodeRenderRequestV2(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../conformance/fixtures/document-v2-render-plan-" + name + ".document.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return EnvironmentV2{d, r.Plan, r.Manifest}
}
func rawEnvironmentV2(t *testing.T, e EnvironmentV2) []byte {
	t.Helper()
	raw, err := CanonicalEnvironmentV2(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func assertRejectedEnvironmentV2(t *testing.T, raw []byte) {
	t.Helper()
	e, err := DecodeEnvironmentV2(context.Background(), raw)
	if err == nil || !reflect.DeepEqual(e, EnvironmentV2{}) {
		t.Fatalf("accepted or nonzero: %v", err)
	}
}
func recordFromEnvironmentV2(t *testing.T, raw []byte) recordV2 {
	t.Helper()
	var r recordV2
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func encodeTestRecordV2(t *testing.T, r recordV2) []byte {
	t.Helper()
	raw, err := encodeRecordEnvironmentV2(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func hashTestEnvironmentV2(raw []byte) string {
	s := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(s[:])
}
func resignPlanEnvironmentV2(t *testing.T, p *renderplanv2.Plan) {
	t.Helper()
	b, _ := json.Marshal(p)
	var f map[string]json.RawMessage
	_ = json.Unmarshal(b, &f)
	delete(f, "fingerprint")
	b, err := canonicaljson.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	p.Fingerprint = hashTestEnvironmentV2(b)
}

func TestEnvironmentV2Roundtrip(t *testing.T) {
	for _, name := range []string{"gap", "mixed", "transition"} {
		t.Run(name, func(t *testing.T) {
			e := environmentFixtureV2(t, name)
			raw := rawEnvironmentV2(t, e)
			got, err := DecodeEnvironmentV2(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, rawEnvironmentV2(t, got)) {
				t.Fatal("roundtrip drift")
			}
			owned, err := NewEnvironmentV2(context.Background(), e.Document, e.Plan, e.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, rawEnvironmentV2(t, owned)) {
				t.Fatal("constructor drift")
			}
			r := recordFromEnvironmentV2(t, raw)
			d, _ := documentv2.CanonicalJSON(e.Document)
			p, _ := renderplanv2.Canonical(e.Plan)
			m, _ := renderapi.CanonicalExecutionManifestV2(e.Manifest)
			if !bytes.Equal(d, r.Document) || !bytes.Equal(p, r.Plan) || !bytes.Equal(m, r.ExecutionManifest) {
				t.Fatal("nested profile changed")
			}
			if _, _, err := decodeRecord(raw, DefaultMaxRecordBytes); err == nil {
				t.Fatal("v1 accepted v2")
			}
		})
	}
}

func TestEnvironmentV2NumberPreservingMetadata(t *testing.T) {
	e := environmentFixtureV2(t, "mixed")
	e.Document.Metadata = map[string]json.RawMessage{"numbers": json.RawMessage(`{"decimal":1.2300,"exponent":1e+009,"huge":1234567890123456789012345678901234567890,"negative":-0,"unicode":"<>&😀"}`)}
	p, ds := renderplanv2.Resolve(context.Background(), e.Document, e.Plan.RequestedRange, e.Plan.Target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	draft := e.Manifest
	draft.Visual, draft.Audio = renderapi.ProgramSelectionV2{}, renderapi.ProgramSelectionV2{}
	m, err := renderapi.FreezeExecutionManifestV2(context.Background(), p, draft)
	if err != nil {
		t.Fatal(err)
	}
	e.Plan, e.Manifest = p, m
	raw := rawEnvironmentV2(t, e)
	r := recordFromEnvironmentV2(t, raw)
	want, err := documentv2.CanonicalJSON(e.Document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, r.Document) {
		t.Fatal("document profile drift")
	}
	for _, s := range []string{"1.2300", "1e+009", "1234567890123456789012345678901234567890", "-0"} {
		if !bytes.Contains(raw, []byte(s)) {
			t.Fatalf("lost %s", s)
		}
	}
	got, err := DecodeEnvironmentV2(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, rawEnvironmentV2(t, got)) {
		t.Fatal("number spelling changed")
	}
}

func TestEnvironmentV2StrictHostileWire(t *testing.T) {
	raw := rawEnvironmentV2(t, environmentFixtureV2(t, "mixed"))
	cases := map[string][]byte{
		"empty": nil, "trailing": append(append([]byte{}, raw...), '\n'), "truncated": raw[:len(raw)-1], "multiple": append(append([]byte{}, raw...), raw...),
		"unknown":           bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2,"unknown":0`), 1),
		"duplicate":         bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1),
		"case":              bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"Schema_version":2`), 1),
		"null":              bytes.Replace(raw, []byte(`"document":{`), []byte(`"document":null,"unused":{`), 1),
		"overflow":          bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":4294967296`), 1),
		"version":           bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":1`), 1),
		"missing":           bytes.Replace(raw, []byte(`,"schema_version":2`), nil, 1),
		"profile":           bytes.Replace(raw, []byte(RecordProfileV2), []byte(RecordProfileV1), 1),
		"canonical-profile": bytes.Replace(raw, []byte(RecordCanonicalJSONProfileV2), []byte(canonicaljson.Profile), 1),
		"utf8":              bytes.Replace(raw, []byte(RecordProfileV2), []byte{0xff}, 1),
		"surrogate-high":    bytes.Replace(raw, []byte(RecordProfileV2), []byte(`\ud800`), 1),
		"surrogate-low":     bytes.Replace(raw, []byte(RecordProfileV2), []byte(`\udc00`), 1),
		"nested-unknown":    bytes.Replace(raw, []byte(`"document":{`), []byte(`"document":{"unknown":0,`), 1),
		"nested-case":       bytes.Replace(raw, []byte(`"document":{`), []byte(`"document":{"ID":"case",`), 1),
		"nested-duplicate":  bytes.Replace(raw, []byte(`"plan":{`), []byte(`"plan":{"schema_version":1,`), 1),
		"nested-null":       bytes.Replace(raw, []byte(`"visual":[`), []byte(`"visual":null,"unused":[`), 1),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) { assertRejectedEnvironmentV2(t, b) })
	}
	// Isolate type/null failures without an extra unknown member masking them.
	for _, part := range []string{"document", "plan", "manifest"} {
		r := recordFromEnvironmentV2(t, raw)
		switch part {
		case "document":
			r.Document = json.RawMessage(`null`)
		case "plan":
			r.Plan = json.RawMessage(`null`)
		case "manifest":
			r.ExecutionManifest = json.RawMessage(`null`)
		}
		assertRejectedEnvironmentV2(t, encodeTestRecordV2(t, r))
	}
	// Empty optional metadata is syntactically legal and semantically valid, but
	// omitted by typed encoding. Rebind every identity so only canonical typed
	// byte equality can reject this alias.
	gap := environmentFixtureV2(t, "gap")
	r := recordFromEnvironmentV2(t, rawEnvironmentV2(t, gap))
	r.Document = bytes.Replace(r.Document, []byte(`{`), []byte(`{"metadata":{},`), 1)
	r.DocumentHash = hashTestEnvironmentV2(r.Document)
	gap.Plan.DocumentHash = r.DocumentHash
	resignPlanEnvironmentV2(t, &gap.Plan)
	gap.Manifest.Visual, gap.Manifest.Audio = renderapi.ProgramSelectionV2{}, renderapi.ProgramSelectionV2{}
	var err error
	gap.Manifest, err = renderapi.FreezeExecutionManifestV2(context.Background(), gap.Plan, gap.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.Plan, err = renderplanv2.Canonical(gap.Plan)
	if err != nil {
		t.Fatal(err)
	}
	r.ExecutionManifest, err = renderapi.CanonicalExecutionManifestV2(gap.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.PlanFingerprint, r.ExecutionManifestFingerprint = gap.Plan.Fingerprint, gap.Manifest.Fingerprint
	assertRejectedEnvironmentV2(t, encodeTestRecordV2(t, r))
	for _, field := range []string{"document_hash", "plan_fingerprint", "execution_manifest_fingerprint"} {
		t.Run(field, func(t *testing.T) {
			r := recordFromEnvironmentV2(t, raw)
			switch field {
			case "document_hash":
				r.DocumentHash = hashTestEnvironmentV2(nil)
			case "plan_fingerprint":
				r.PlanFingerprint = hashTestEnvironmentV2(nil)
			default:
				r.ExecutionManifestFingerprint = hashTestEnvironmentV2(nil)
			}
			assertRejectedEnvironmentV2(t, encodeTestRecordV2(t, r))
		})
	}
}

func TestEnvironmentV2ResignedMismatchAndNoPinRepair(t *testing.T) {
	e := environmentFixtureV2(t, "mixed")
	// A metadata edit changes the complete document identity even though render
	// selections do not change. Resign every affected identity, not just headers.
	e.Document.Metadata = map[string]json.RawMessage{"edit": json.RawMessage(`1.0`)}
	h, err := documentv2.Hash(e.Document)
	if err != nil {
		t.Fatal(err)
	}
	e.Plan.DocumentHash = h
	// Lie about the authored revision, then sign a standalone valid Plan/Manifest.
	e.Plan.Revision++
	resignPlanEnvironmentV2(t, &e.Plan)
	if _, err = renderplanv2.Verify(e.Plan); err != nil {
		t.Fatal("standalone resigned plan", err)
	}
	e.Manifest.Visual, e.Manifest.Audio = renderapi.ProgramSelectionV2{}, renderapi.ProgramSelectionV2{}
	e.Manifest, err = renderapi.FreezeExecutionManifestV2(context.Background(), e.Plan, e.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = renderapi.VerifyExecutionManifestV2AgainstPlan(context.Background(), e.Manifest, e.Plan); err != nil {
		t.Fatal(err)
	}
	if out, err := NewEnvironmentV2(context.Background(), e.Document, e.Plan, e.Manifest); err == nil || !reflect.DeepEqual(out, EnvironmentV2{}) {
		t.Fatal("accepted resigned revision lie")
	}
	docRaw, _ := documentv2.CanonicalJSON(e.Document)
	planRaw, _ := renderplanv2.Canonical(e.Plan)
	manifestRaw, _ := renderapi.CanonicalExecutionManifestV2(e.Manifest)
	assertRejectedEnvironmentV2(t, encodeTestRecordV2(t, recordV2{RecordCanonicalJSONProfileV2, docRaw, h, manifestRaw, e.Manifest.Fingerprint, planRaw, e.Plan.Fingerprint, RecordProfileV2, RecordSchemaVersionV2}))
	good := environmentFixtureV2(t, "mixed")
	good.Manifest.PlanFingerprint = hashTestEnvironmentV2(nil)
	before, _ := json.Marshal(good)
	if b, err := CanonicalEnvironmentV2(context.Background(), good); err == nil || b != nil {
		t.Fatal("repaired stale pin")
	}
	after, _ := json.Marshal(good)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated supplied pins")
	}
	// Independently valid plan and manifest from different fixtures cannot mix.
	other := environmentFixtureV2(t, "transition")
	good = environmentFixtureV2(t, "mixed")
	good.Manifest = other.Manifest
	if v, err := VerifyEnvironmentV2(context.Background(), good); err == nil || v.Valid() {
		t.Fatal("accepted cross-environment manifest")
	}
}

func TestEnvironmentV2OwnershipAndConcurrentReads(t *testing.T) {
	e := environmentFixtureV2(t, "mixed")
	owned, err := NewEnvironmentV2(context.Background(), e.Document, e.Plan, e.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyEnvironmentV2(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	want, err := v.Canonical(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Key() != (KeyV2{e.Plan.DocumentHash, e.Plan.Fingerprint, e.Manifest.Fingerprint}) {
		t.Fatal("key mismatch")
	}
	e.Document.ID = "changed"
	e.Plan.Resources[0].ContentHash = "changed"
	e.Manifest.Resources[0].Locator = "changed"
	if !bytes.Equal(want, rawEnvironmentV2(t, owned)) {
		t.Fatal("constructor retained caller data")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				b, err := v.Canonical(context.Background())
				if err != nil || !bytes.Equal(b, want) {
					t.Error("handle drift", err)
					return
				}
				b[0] = '!'
				x, err := v.Environment(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				x.Document.ID = "mutated"
				x.Plan.Resources[0].ContentHash = "mutated"
				x.Manifest.Resources[0].Locator = "mutated"
			}
		}()
	}
	wg.Wait()
	b, _ := v.Canonical(context.Background())
	if !bytes.Equal(b, want) {
		t.Fatal("mutable handle")
	}
	input := append([]byte{}, want...)
	decoded, err := DecodeEnvironmentV2(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for i := range input {
		input[i] = '!'
	}
	if !bytes.Equal(want, rawEnvironmentV2(t, decoded)) {
		t.Fatal("decoder retained input")
	}
}

type contextFaultEnvironmentV2 struct {
	context.Context
	calls      int
	at         int
	panicValue bool
}

func (c *contextFaultEnvironmentV2) Err() error {
	c.calls++
	if c.calls >= c.at {
		if c.panicValue {
			panic(panicStringEnvironmentV2{})
		}
		return context.Canceled
	}
	return nil
}

type panicStringEnvironmentV2 struct{}

func (panicStringEnvironmentV2) String() string { panic("must not format panic") }
func TestEnvironmentV2CancellationPanicFailureZero(t *testing.T) {
	e := environmentFixtureV2(t, "mixed")
	raw := rawEnvironmentV2(t, e)
	v, err := VerifyEnvironmentV2(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{1, 20, 100, 500} {
		for _, panics := range []bool{false, true} {
			name := fmtTestEnvironmentV2(at, panics)
			t.Run(name, func(t *testing.T) {
				ctx := func() context.Context {
					return &contextFaultEnvironmentV2{Context: context.Background(), at: at, panicValue: panics}
				}
				if b, err := CanonicalEnvironmentV2(ctx(), e); err == nil || b != nil {
					t.Fatal("canonical failure not zero")
				}
				if x, err := NewEnvironmentV2(ctx(), e.Document, e.Plan, e.Manifest); err == nil || !reflect.DeepEqual(x, EnvironmentV2{}) {
					t.Fatal("constructor failure not zero")
				}
				if x, err := DecodeEnvironmentV2(ctx(), raw); err == nil || !reflect.DeepEqual(x, EnvironmentV2{}) {
					t.Fatal("decode failure not zero")
				}
				if x, err := VerifyEnvironmentV2(ctx(), e); err == nil || x.Valid() || x.Key() != (KeyV2{}) {
					t.Fatal("verify failure not zero")
				}
			})
		}
	}
	// Exercise the final cancellation check, after all validation/allocation work.
	for _, op := range []struct {
		name string
		run  func(context.Context) bool
	}{
		{"canonical", func(ctx context.Context) bool {
			b, err := CanonicalEnvironmentV2(ctx, e)
			return err != nil && b == nil
		}},
		{"constructor", func(ctx context.Context) bool {
			x, err := NewEnvironmentV2(ctx, e.Document, e.Plan, e.Manifest)
			return err != nil && reflect.DeepEqual(x, EnvironmentV2{})
		}},
		{"decode", func(ctx context.Context) bool {
			x, err := DecodeEnvironmentV2(ctx, raw)
			return err != nil && reflect.DeepEqual(x, EnvironmentV2{})
		}},
		{"verify", func(ctx context.Context) bool { x, err := VerifyEnvironmentV2(ctx, e); return err != nil && !x.Valid() }},
		{"bytes-getter", func(ctx context.Context) bool { b, err := v.Canonical(ctx); return err != nil && b == nil }},
		{"snapshot-getter", func(ctx context.Context) bool {
			x, err := v.Environment(ctx)
			return err != nil && reflect.DeepEqual(x, EnvironmentV2{})
		}},
	} {
		t.Run("late-"+op.name, func(t *testing.T) {
			baseline := &contextFaultEnvironmentV2{Context: context.Background(), at: int(^uint(0) >> 1)}
			if op.run(baseline) {
				t.Fatal("baseline failure")
			}
			for _, p := range []bool{false, true} {
				if !op.run(&contextFaultEnvironmentV2{Context: context.Background(), at: baseline.calls, panicValue: p}) {
					t.Fatal("late failure not zero")
				}
			}
		})
	}
	for _, ctx := range []context.Context{nil, &contextFaultEnvironmentV2{Context: context.Background(), at: 1, panicValue: true}} {
		if b, err := v.Canonical(ctx); err == nil || b != nil {
			t.Fatal("canonical getter failure")
		}
		if x, err := v.Environment(ctx); err == nil || !reflect.DeepEqual(x, EnvironmentV2{}) {
			t.Fatal("environment getter failure")
		}
	}
	var zero VerifiedEnvironmentV2
	if zero.Valid() || zero.Key() != (KeyV2{}) {
		t.Fatal("valid zero")
	}
	if b, err := zero.Canonical(context.Background()); err == nil || b != nil {
		t.Fatal("zero canonical")
	}
	if x, err := zero.Environment(context.Background()); err == nil || !reflect.DeepEqual(x, EnvironmentV2{}) {
		t.Fatal("zero environment")
	}
	if _, err := CanonicalEnvironmentV2(nil, e); err == nil {
		t.Fatal("nil context")
	}
}

// RawMessage limits apply to the complete encoded leaf, not each string or
// array element. Every real Document raw field is covered through typed paths.
func TestEnvironmentV2RawLeafPreflight(t *testing.T) {
	ctx := context.Background()
	largeArray := `[` + strings.Repeat(`"`+strings.Repeat("x", (1<<20)-4)+`",`, 19) + `"` + strings.Repeat("x", (1<<20)-4) + `"]`
	object := `{"a":"` + strings.Repeat("x", 600000) + `","b":"` + strings.Repeat("x", 600000) + `"}`
	for shape, payload := range map[string]string{"array": largeArray, "object": object, "string": `"` + strings.Repeat("x", 1<<20) + `"`, "nested": `{"metadata":{"large":` + object + `}}`} {
		for name, wrap := range map[string]func(string) string{
			"document-metadata": func(p string) string { return `{"metadata":{"large":` + p + `}}` },
			"asset-metadata":    func(p string) string { return `{"assets":[{"metadata":{"large":` + p + `}}]}` },
			"timeline-metadata": func(p string) string { return `{"timelines":[{"metadata":{"large":` + p + `}}]}` },
			"track-metadata":    func(p string) string { return `{"timelines":[{"tracks":[{"metadata":{"large":` + p + `}}]}]}` },
			"clip-annotation": func(p string) string {
				return `{"timelines":[{"tracks":[{"clips":[{"annotations":[{"value":` + p + `}]}]}]}]}`
			},
			"clip-extension": func(p string) string {
				return `{"timelines":[{"tracks":[{"clips":[{"property_set":{"extensions":[{"value":` + p + `}]}}]}]}]}`
			},
		} {
			t.Run(shape+"/"+name, func(t *testing.T) {
				doc := []byte(wrap(payload))
				if err := scanEnvironmentV2(ctx, doc, MaxDocumentBytesV2, renderplanv2.MaxNodes, renderplanv2.MaxDepth); !errors.Is(err, ErrLimit) {
					t.Fatal("raw leaf not rejected by standalone preflight", err)
				}
				record := []byte(`{"document":` + string(doc) + `}`)
				if err := scanEnvironmentV2(ctx, record, MaxRecordBytesV2, renderplanv2.MaxNodes*3+32, renderplanv2.MaxDepth+2); !errors.Is(err, ErrLimit) {
					t.Fatal("raw leaf not rejected by record preflight", err)
				}
				assertRejectedEnvironmentV2(t, record)
			})
		}
	}
	for _, domain := range []string{"visual", "audio"} {
		raw := []byte(`{"execution_manifest":{"` + domain + `":{"configuration":{"a":"` + strings.Repeat("x", 9000) + `","b":"` + strings.Repeat("x", 9000) + `"}}}}`)
		if err := scanEnvironmentV2(ctx, raw, MaxRecordBytesV2, renderplanv2.MaxNodes*3+32, renderplanv2.MaxDepth+2); !errors.Is(err, ErrLimit) {
			t.Fatal("S2 configuration16KiB bound", err)
		}
	}
	// Exact span endpoints and escaped delimiters/backslashes remain legal; open
	// numeric lexemes are inspected as bytes, never converted through floats.
	for _, raw := range []string{`12345`, `"a\\\"b"`, `[1,2]`, `{"a":1}`, `1e+999999999999999999`, `1.23000000000000000001`} {
		if err := rawSpanEnvironmentV2(ctx, []byte(raw+`,`), 0, len(raw)); err != nil {
			t.Fatal("exact raw cap", raw, err)
		}
		if err := rawSpanEnvironmentV2(ctx, []byte(raw+`,`), 0, len(raw)-1); !errors.Is(err, ErrLimit) {
			t.Fatal("raw cap+1", raw, err)
		}
	}
	legal := []byte(`{"metadata":{"n":{"a":1.23000000000000000001,"b":1e+999999999999999999,"c":"😀","d":"\\ud800"}}}`)
	if err := scanEnvironmentV2(ctx, legal, MaxDocumentBytesV2, renderplanv2.MaxNodes, renderplanv2.MaxDepth); err != nil {
		t.Fatal("legal raw numeric/Unicode profile rejected", err)
	}
	for _, name := range []string{"unknown", "Document"} {
		if err := scanEnvironmentV2(ctx, []byte(`{"`+name+`":`+largeArray+`}`), MaxRecordBytesV2, renderplanv2.MaxNodes*3+32, renderplanv2.MaxDepth+2); !errors.Is(err, ErrInvalidEnvironmentV2) {
			t.Fatal("unknown root value was scanned", err)
		}
	}
}

func fmtTestEnvironmentV2(at int, p bool) string {
	b, _ := json.Marshal([]any{at, p})
	return string(b)
}

func TestEnvironmentV2Limits(t *testing.T) {
	if MaxDocumentBytesV2 != 128<<20 || MaxPlanBytesV2 != 8<<20 || MaxManifestBytesV2 != 16<<20 || MaxRecordBytesV2 != 160<<20 {
		t.Fatal("bounds drift")
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name                  string
		raw                   []byte
		maximum, nodes, depth int
	}{
		{"bytes", []byte(`[]`), 1, 10, 10}, {"nodes", []byte(`[1,2]`), 10, 2, 10}, {"depth", []byte(`[[[]]]`), 10, 10, 1},
		{"collection", []byte(`[` + strings.Repeat(`0,`, renderplanv2.MaxCollection) + `0]`), 1 << 20, 100000, 64},
		{"string", []byte(`"` + strings.Repeat("x", renderplanv2.MaxStringBytes+1) + `"`), 2 << 20, 10, 64},
		{"number", []byte(strings.Repeat("1", renderplanv2.MaxStringBytes+1)), 2 << 20, 10, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := scanEnvironmentV2(ctx, tc.raw, tc.maximum, tc.nodes, tc.depth); !errors.Is(err, ErrLimit) {
				t.Fatalf("want limit, got %v", err)
			}
		})
	}
	// Per-subtree ceilings must reject in the first record scan, not only after
	// allocating an envelope or typed shape.
	for _, tc := range []struct {
		key   string
		count int
	}{{"plan", 9}, {"execution_manifest", 17}} {
		// Leading whitespace counts toward the embedded record span, without
		// relying on an already-invalid array-for-struct shape.
		b := []byte(`{"` + tc.key + `":` + strings.Repeat(" ", tc.count*(renderplanv2.MaxStringBytes+1)) + `{}}`)
		if err := scanEnvironmentV2(ctx, b, MaxRecordBytesV2, renderplanv2.MaxNodes*3+32, renderplanv2.MaxDepth+2); !errors.Is(err, ErrLimit) {
			t.Fatal("subtree byte cap", tc.key, err)
		}
	}
	if err := scanEnvironmentV2(ctx, []byte(`[[0]]`), 7, 3, 2); err != nil {
		t.Fatal("inclusive limits", err)
	}
	e := environmentFixtureV2(t, "gap")
	for _, metadata := range []json.RawMessage{json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`), json.RawMessage(`{"k":1,"k":2}`), json.RawMessage(`"\ud800"`), {0xff}} {
		e.Document.Metadata = map[string]json.RawMessage{"hostile": metadata}
		if b, err := CanonicalEnvironmentV2(ctx, e); err == nil || b != nil {
			t.Fatal("typed admission bypass")
		}
	}
	// Large bytes are bounded before any whole-shape decoding, not marshalled.
	large := make([]byte, MaxRecordBytesV2+1)
	assertRejectedEnvironmentV2(t, large)
	// S1's real 1MiB RawMessage cap precludes claiming arbitrary >64MiB metadata.
	// Keep S1 unchanged; reject before marshal even though the v2 record cap is larger.
	e.Document.Metadata = map[string]json.RawMessage{"large": make([]byte, renderplanv2.MaxStringBytes+1)}
	if b, err := CanonicalEnvironmentV2(ctx, e); !errors.Is(err, ErrLimit) || b != nil {
		t.Fatal("S1 bound not preserved", err)
	}
}
