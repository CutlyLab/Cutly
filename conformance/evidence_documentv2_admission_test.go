package conformance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/render/backendstage"
	"github.com/CutlyLab/Cutly/render/evidence"
)

// Exercise every object/array recursively rather than sampling only envelope
// fields. In particular unknown fields, case aliases, duplicate keys, null and
// omitted zero-valued fields are never tolerated at a nested trust boundary.
func TestEvidenceDocumentV2StrictEveryNestedField(t *testing.T) {
	base := executionObjectV2(t, fixture(t, "evidence-document-v2-mixed.canonical.json"))
	var visit func(any, string)
	visit = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for key := range x {
				keys = append(keys, key)
			}
			for _, key := range keys {
				saved := x[key]
				for _, attack := range []string{"null", "missing", "case"} {
					t.Run(path+"/"+key+"/"+attack, func(t *testing.T) {
						switch attack {
						case "null":
							x[key] = nil
						case "missing":
							delete(x, key)
						case "case":
							delete(x, key)
							x[strings.ToUpper(key)] = saved
						}
						raw := executionCanonicalObjectV2(t, base)
						out, err := evidence.DecodeManifestV2(raw)
						if err == nil || !reflect.DeepEqual(out, evidence.ManifestV2{}) {
							t.Fatal("accepted ambiguous shape or returned partial value")
						}
						delete(x, strings.ToUpper(key))
						x[key] = saved
					})
				}
				visit(saved, path+"/"+key)
			}
			x["unknown_v2"] = true
			raw := executionCanonicalObjectV2(t, base)
			delete(x, "unknown_v2")
			if _, err := evidence.DecodeManifestV2(raw); err == nil {
				t.Fatalf("unknown field accepted at %s", path)
			}
		case []any:
			for i, child := range x {
				visit(child, path+"/item"+strings.Repeat("x", i))
			}
		}
	}
	visit(base, "$")
	raw := fixture(t, "evidence-document-v2-mixed.canonical.json")
	for _, key := range []string{"worker_executable_digest", "content_hash", "unit_bytes", "first", "count", "deterministic_seed", "profile"} {
		// Insert the same member immediately before its first occurrence; duplicate
		// detection must run even where the values are identical.
		token := []byte(`"` + key + `":`)
		at := bytes.Index(raw, token)
		if at < 0 {
			t.Fatal(key)
		}
		next := at + len(token)
		end := next
		depth := 0
		quoted := false
		for end < len(raw) {
			c := raw[end]
			if c == '"' && (end == next || raw[end-1] != '\\') {
				quoted = !quoted
			}
			if !quoted {
				if c == '{' || c == '[' {
					depth++
				}
				if c == '}' || c == ']' {
					if depth == 0 {
						break
					}
					depth--
				}
				if c == ',' && depth == 0 {
					break
				}
			}
			end++
		}
		member := append(append([]byte(nil), raw[at:end]...), ',')
		hostile := append(append(append([]byte(nil), raw[:at]...), member...), raw[at:]...)
		if _, err := evidence.DecodeManifestV2(hostile); err == nil {
			t.Fatalf("duplicate %s accepted", key)
		}
	}
	for _, raw := range [][]byte{append(append([]byte(nil), raw...), '\n'), []byte(`{}`), []byte(`null`), append(append([]byte(nil), raw...), []byte(`{}`)...)} {
		if _, err := evidence.DecodeManifestV2(raw); err == nil {
			t.Fatal("accepted noncanonical/incomplete JSON")
		}
	}
}

type evidencePanicContextV2 struct{}

func (evidencePanicContextV2) Deadline() (time.Time, bool) { return time.Time{}, false }
func (evidencePanicContextV2) Done() <-chan struct{}       { return nil }
func (evidencePanicContextV2) Err() error                  { panic("host context panic") }
func (evidencePanicContextV2) Value(any) any               { return nil }

type evidenceCancelContextV2 struct {
	calls int
	after int
}

func (c *evidenceCancelContextV2) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *evidenceCancelContextV2) Done() <-chan struct{}       { return nil }
func (c *evidenceCancelContextV2) Err() error {
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}
func (c *evidenceCancelContextV2) Value(any) any { return nil }

func TestEvidenceDocumentV2AdmissionCancellationAndOwnership(t *testing.T) {
	m := evidenceFixtureV2(t, "mixed")
	for _, ctx := range []context.Context{nil, evidencePanicContextV2{}, &evidenceCancelContextV2{after: 1}, &evidenceCancelContextV2{after: 50}, &evidenceCancelContextV2{after: 200}} {
		out, err := evidence.VerifyManifestV2AgainstBinding(ctx, m, m.Assembly)
		if err == nil || out.Valid() {
			t.Fatal("context failure returned a handle")
		}
		created, e := evidence.NewManifestV2(ctx, m.Assembly, m.WorkerExecutableDigest, m.Artifact.ContentHash, m.Artifact.SizeBytes, m.DecodedVideo, m.DecodedAudio)
		if e == nil || !reflect.DeepEqual(created, evidence.ManifestV2{}) {
			t.Fatal("context failure returned draft")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evidence.VerifyManifestV2AgainstBinding(canceled, m, m.Assembly); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A foreign request must fail on identity before its huge graph is examined.
	bad := m
	bad.Assembly.DocumentHash = evidence.DigestDecodedBytes([]byte("foreign"))
	bad.DecodedVideo = &evidence.DecodedSequence{Chunks: make([]evidence.DecodedChunk, evidence.MaxEvidenceDescriptorsV2+1)}
	if _, err := evidence.VerifyManifestV2AgainstBinding(context.Background(), bad, m.Assembly); !errors.Is(err, evidence.ErrEvidenceInvalidV2) || errors.Is(err, evidence.ErrEvidenceLimitV2) {
		t.Fatal("identity not checked before graph", err)
	}
	for _, mutate := range []func(*evidence.ManifestV2){
		func(m *evidence.ManifestV2) {
			m.DecodedVideo.Chunks = make([]evidence.DecodedChunk, evidence.MaxEvidenceDescriptorsV2+1)
		},
		func(m *evidence.ManifestV2) {
			m.Assembly.Audio = make([]evidence.ChunkDescriptorV2, evidence.MaxEvidenceDescriptorsV2+1)
		},
		func(m *evidence.ManifestV2) {
			m.Assembly.Output.Format = strings.Repeat("x", evidence.MaxEvidenceStringBytesV2+1)
		},
		func(m *evidence.ManifestV2) { m.DecodedAudio.Domain = string([]byte{0xff}) },
	} {
		bad := evidenceFixtureV2(t, "mixed")
		mutate(&bad)
		out, err := evidence.VerifyManifestV2(bad)
		if err == nil || out.Valid() {
			t.Fatal("admission returned handle")
		}
	}
	oversized := make([]byte, evidence.MaxEvidenceBytesV2+1)
	if _, err := evidence.DecodeManifestV2(oversized); !errors.Is(err, evidence.ErrEvidenceLimitV2) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*evidence.ManifestV2){
		func(m *evidence.ManifestV2) { m.DecodedVideo.Range.First = math.MaxInt64 },
		func(m *evidence.ManifestV2) { m.DecodedVideo.UnitBytes = math.MaxInt64 },
		func(m *evidence.ManifestV2) { m.DecodedAudio.Chunks[0].Count = math.MaxInt64 },
		func(m *evidence.ManifestV2) { m.Assembly.Video[0].SizeBytes = math.MaxInt64 },
		func(m *evidence.ManifestV2) { m.Assembly.FrameRange.Count = math.MaxInt64 },
	} {
		bad := evidenceFixtureV2(t, "mixed")
		mutate(&bad)
		out, err := evidence.VerifyManifestV2(bad)
		if err == nil || out.Valid() {
			t.Fatal("overflow accepted")
		}
	}
	verified, err := evidence.VerifyManifestV2AgainstBinding(context.Background(), m, m.Assembly)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := verified.Canonical()
	raw[0] = '!'
	copy1, _ := verified.Manifest()
	copy1.Assembly.Audio[0].ContentHash = "changed"
	copy1.Assembly.Aggregate.Video.RootHash = "changed"
	copy1.DecodedVideo.Chunks[0].ContentHash = "changed"
	copy2, _ := verified.Manifest()
	if copy2.Assembly.Audio[0].ContentHash == "changed" || copy2.Assembly.Aggregate.Video.RootHash == "changed" || copy2.DecodedVideo.Chunks[0].ContentHash == "changed" {
		t.Fatal("getters alias internal state")
	}
	created, err := evidence.NewManifestV2(context.Background(), m.Assembly, m.WorkerExecutableDigest, m.Artifact.ContentHash, m.Artifact.SizeBytes, m.DecodedVideo, m.DecodedAudio)
	if err != nil {
		t.Fatal(err)
	}
	m.DecodedVideo.Chunks[0].ContentHash = "mutated input"
	m.Assembly.Video[0].ContentHash = "mutated input"
	m.Assembly.Aggregate.Audio.RootHash = "mutated input"
	if created.DecodedVideo.Chunks[0].ContentHash == "mutated input" || created.Assembly.Video[0].ContentHash == "mutated input" || created.Assembly.Aggregate.Audio.RootHash == "mutated input" {
		t.Fatal("constructor aliases input")
	}
	if (evidence.VerifiedManifestV2{}).Valid() {
		t.Fatal("zero handle valid")
	}
	if _, err := (evidence.VerifiedManifestV2{}).Manifest(); err == nil {
		t.Fatal("zero getter succeeded")
	}
}

func TestEvidenceDocumentV2EveryBindingIdentity(t *testing.T) {
	m := evidenceFixtureV2(t, "mixed")
	var bindingValueV2 evidence.BindingV2
	// Every scalar leaf of the binding is changed independently. Even when a
	// caller leaves a stale fingerprint, no against path may accept the drift.
	var visit func(reflect.Value, string)
	visit = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				visit(v.Elem(), path)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				visit(v.Field(i), path+"/"+v.Type().Field(i).Name)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				visit(v.Index(i), path+"/item")
			}
		case reflect.String:
			old := v.String()
			v.SetString(old + "x")
			if _, err := evidence.VerifyManifestV2AgainstBinding(context.Background(), m, bindingValueV2); err == nil {
				t.Fatal("accepted binding drift", path)
			}
			v.SetString(old)
		case reflect.Int, reflect.Int64:
			old := v.Int()
			v.SetInt(old + 1)
			if _, err := evidence.VerifyManifestV2AgainstBinding(context.Background(), m, bindingValueV2); err == nil {
				t.Fatal("accepted binding drift", path)
			}
			v.SetInt(old)
		case reflect.Uint32, reflect.Uint64:
			old := v.Uint()
			v.SetUint(old + 1)
			if _, err := evidence.VerifyManifestV2AgainstBinding(context.Background(), m, bindingValueV2); err == nil {
				t.Fatal("accepted binding drift", path)
			}
			v.SetUint(old)
		}
	}
	// owned clone, never shares slices with the evidence being compared
	raw, _ := evidence.CanonicalManifestV2(m)
	clone, err := evidence.DecodeManifestV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	bindingValueV2 = clone.Assembly
	visit(reflect.ValueOf(&bindingValueV2).Elem(), "assembly")
}

// Compare standalone native request and neutral binding acceptance after
// re-signing every scalar leaf mutation, so fingerprint rejection alone cannot
// conceal semantic divergence. Shared canonical vectors cover zero domains,
// fractional clocks and multiple ordered partitions.
func TestEvidenceDocumentV2AssemblyAcceptanceParity(t *testing.T) {
	check := func(b evidence.BindingV2) {
		t.Helper()
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, "fingerprint")
		unsigned := executionCanonicalObjectV2(t, fields)
		sum := sha256.Sum256(unsigned)
		b.Fingerprint = "sha256:" + hex.EncodeToString(sum[:])
		raw, err = json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		var native backendstage.ChunkAssemblyRequestV2
		if err := json.Unmarshal(raw, &native); err != nil {
			t.Fatal(err)
		}
		_, nativeErr := backendstage.VerifyChunkAssemblyRequestV2(native)
		_, neutralErr := evidence.VerifyBindingV2(context.Background(), b)
		if (nativeErr == nil) != (neutralErr == nil) {
			t.Fatalf("native/neutral acceptance divergence: native=%v neutral=%v", nativeErr, neutralErr)
		}
	}
	for _, name := range []string{"gap-silence", "mixed", "transition", "audio-gap", "visual-silence", "zero-frame", "zero-sample", "fractional"} {
		m := evidenceFixtureV2(t, name)
		check(m.Assembly)
		var visit func(reflect.Value)
		visit = func(v reflect.Value) {
			switch v.Kind() {
			case reflect.Pointer:
				if !v.IsNil() {
					visit(v.Elem())
				}
			case reflect.Struct:
				for i := 0; i < v.NumField(); i++ {
					visit(v.Field(i))
				}
			case reflect.Slice:
				for i := 0; i < v.Len(); i++ {
					visit(v.Index(i))
				}
			case reflect.String:
				old := v.String()
				v.SetString(old + "x")
				check(m.Assembly)
				v.SetString(old)
			case reflect.Int, reflect.Int64:
				old := v.Int()
				v.SetInt(old + 1)
				check(m.Assembly)
				v.SetInt(old)
			case reflect.Uint32, reflect.Uint64:
				old := v.Uint()
				v.SetUint(old + 1)
				check(m.Assembly)
				v.SetUint(old)
			}
		}
		visit(reflect.ValueOf(&m.Assembly).Elem())
	}
	for _, mutate := range []func(*evidence.BindingV2){
		func(b *evidence.BindingV2) { b.FrameRange.First = math.MaxInt64 },
		func(b *evidence.BindingV2) { b.SampleRange.Count = math.MaxInt64 },
		func(b *evidence.BindingV2) { b.Video[0].SizeBytes = math.MaxInt64 },
		func(b *evidence.BindingV2) { b.Video[0].UnitBytes = math.MaxInt64 },
		func(b *evidence.BindingV2) { b.Video[0].Range.Count = math.MaxInt64 },
		func(b *evidence.BindingV2) { b.Audio[0].CacheKey = b.Video[0].CacheKey },
		func(b *evidence.BindingV2) { b.Video = append(b.Video, b.Video[0]) },
		func(b *evidence.BindingV2) {
			if len(b.Audio) > 1 {
				b.Audio[0], b.Audio[1] = b.Audio[1], b.Audio[0]
			} else {
				b.Audio = append(b.Audio, b.Audio[0])
			}
		},
		func(b *evidence.BindingV2) { b.Audio = nil },
		func(b *evidence.BindingV2) { b.Aggregate.Video = nil },
	} {
		m := evidenceFixtureV2(t, "mixed")
		mutate(&m.Assembly)
		check(m.Assembly)
	}
}
