package conformance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

func resourceInspectionSchemaV2(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(conformanceModuleRoot(t), "schemas", "cutly.resource-inspection.document-v2.v2.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func resourceInspectionFixtureV2(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", "document-v2-resource-inspection-"+name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// These observations use the native packages' existing pinned encoded bytes, not
// identities invented by this test or receipts mistaken for content authority.
func resourceInspectionNativeV2(t *testing.T, kind string) *resourceidentityv2.Inspected {
	t.Helper()
	root := conformanceModuleRoot(t)
	var encoded []byte
	var assetKind documentv2.AssetKind
	if kind == "png" {
		raw, err := os.ReadFile(filepath.Join(root, "media/pngdecode/testdata/decode-v1.vectors.canonical.json"))
		if err != nil {
			t.Fatal(err)
		}
		var vectors struct {
			Vectors []struct {
				Name    string `json:"name"`
				Encoded string `json:"encoded_png_base64"`
			} `json:"vectors"`
		}
		if err := json.Unmarshal(raw, &vectors); err != nil || len(vectors.Vectors) != 1 || vectors.Vectors[0].Name != "opaque-and-alpha" {
			t.Fatalf("pinned PNG vector inventory drift: %v", err)
		}
		encoded, err = base64.StdEncoding.Strict().DecodeString(vectors.Vectors[0].Encoded)
		if err != nil {
			t.Fatal(err)
		}
		assetKind = documentv2.AssetKindImage
	} else {
		raw, err := os.ReadFile(filepath.Join(root, "font/sfntglyf/testdata/minimal-font-v1.hex"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err = hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		assetKind = documentv2.AssetKindFont
	}
	binding := resourceidentityv2.Binding{
		AssetID: documentv2.ID("resource-inspection-" + kind), Kind: assetKind,
		ContentHash: fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), SizeBytes: int64(len(encoded)),
	}
	var actual *resourceidentityv2.Inspected
	var err error
	if kind == "png" {
		actual, err = resourceidentityv2.InspectPNG(context.Background(), binding, encoded, pngdecode.Limits{})
	} else {
		actual, err = resourceidentityv2.InspectFont(context.Background(), binding, encoded, sfntglyf.Limits{})
	}
	if err != nil {
		t.Fatal(err)
	}
	return actual
}

func TestResourceInspectionDocumentV2FixedCorpus(t *testing.T) {
	ctx := context.Background()
	schema := resourceInspectionSchemaV2(t)
	for _, kind := range []string{"png", "font"} {
		t.Run(kind, func(t *testing.T) {
			canonical := resourceInspectionFixtureV2(t, kind+".canonical.json")
			wantHash := strings.TrimSpace(string(resourceInspectionFixtureV2(t, kind+".sha256")))
			if got := fmt.Sprintf("%x", sha256.Sum256(canonical)); got != wantHash {
				t.Fatalf("fixed canonical SHA256 drift: %s != %s", got, wantHash)
			}
			for _, suffix := range []string{"valid.json", "canonical.json"} {
				raw := resourceInspectionFixtureV2(t, kind+"."+suffix)
				if err := schema.Validate(decodeSchemaInstance(t, raw)); err != nil {
					t.Fatal(err)
				}
				verified, err := resourceidentityv2.Decode(ctx, raw)
				if err != nil {
					t.Fatal(err)
				}
				got, err := verified.CanonicalJSON()
				if err != nil || !bytes.Equal(got, canonical) {
					t.Fatalf("fixed canonical roundtrip drift: %v", err)
				}
				receipt, err := verified.Receipt()
				if err != nil {
					t.Fatal(err)
				}
				// Independently hash the exact explicit projection: the self member
				// must be absent, not replaced by an empty string.
				body := decodeExactJSON(t, canonical).(map[string]any)
				delete(body, "fingerprint")
				bodyJSON, err := canonicaljson.Marshal(body)
				if err != nil || receipt.Fingerprint != fmt.Sprintf("sha256:%x", sha256.Sum256(bodyJSON)) {
					t.Fatalf("fingerprint projection drift: %v", err)
				}
				actual := resourceInspectionNativeV2(t, kind)
				observed, err := actual.Receipt()
				if err != nil || !reflect.DeepEqual(observed, receipt) {
					t.Fatalf("actual native observation differs from fixed corpus: %v", err)
				}
				if err := resourceidentityv2.Matches(ctx, receipt.Resource, receipt, actual); err != nil {
					t.Fatal(err)
				}
				if kind == "font" && (receipt.Font.OpaqueLayoutTables == nil || len(receipt.Font.OpaqueLayoutTables) != 0) {
					t.Fatal("empty opaque_layout_tables must be required []")
				}
			}
		})
	}
	for _, name := range []string{"both-payloads", "no-payload", "null-png", "wrong-kind", "wrong-inspector", "live-surface", "null-layout-tables", "missing-zero"} {
		t.Run(name, func(t *testing.T) {
			raw := resourceInspectionFixtureV2(t, name+".invalid.json")
			if err := schema.Validate(decodeSchemaInstance(t, raw)); err == nil {
				t.Fatal("schema accepted invalid fixed corpus")
			}
			verified, err := resourceidentityv2.Decode(ctx, raw)
			if err == nil || !reflect.DeepEqual(verified, resourceidentityv2.Verified{}) {
				t.Fatalf("invalid corpus must fail zero: %v", err)
			}
		})
	}
}

func TestResourceInspectionDocumentV2SchemaParity(t *testing.T) {
	ctx := context.Background()
	schema := resourceInspectionSchemaV2(t)
	for _, kind := range []string{"png", "font"} {
		raw := resourceInspectionFixtureV2(t, kind+".canonical.json")
		// Every required nested scalar, including zeros, and every object is
		// closed. Removing or adding a field is not an alternate wire spelling.
		var paths [][]string
		paths = append(paths, nil, []string{"resource"}, []string{"inspector"}, []string{kind})
		if kind == "font" {
			paths = append(paths, []string{"font", "tables", "0"})
		}
		for _, path := range paths {
			base := decodeExactJSON(t, raw).(map[string]any)
			object := resourceInspectionObjectV2(base, path)
			for key := range object {
				key := key
				t.Run(kind+"/missing/"+strings.Join(append(append([]string{}, path...), key), "/"), func(t *testing.T) {
					candidate := decodeExactJSON(t, raw).(map[string]any)
					delete(resourceInspectionObjectV2(candidate, path), key)
					assertResourceInspectionRejectedV2(t, schema, candidate)
				})
			}
			for _, field := range []string{"__unknown", "Font", "Surface", "limits", "encoded_bytes_live"} {
				candidate := decodeExactJSON(t, raw).(map[string]any)
				resourceInspectionObjectV2(candidate, path)[field] = true
				assertResourceInspectionRejectedV2(t, schema, candidate)
			}
		}
	}
	for _, test := range []struct {
		name, kind string
		path       []string
		field      string
		value      any
	}{
		{"zero width", "png", []string{"png"}, "width", 0},
		{"uint32 width overflow", "png", []string{"png"}, "width", uint64(1) << 32},
		{"intent overflow", "png", []string{"png"}, "srgb_rendering_intent", 4},
		{"fractional height", "png", []string{"png"}, "height", 1.5},
		{"nonpositive size", "png", []string{"resource"}, "size_bytes", 0},
		{"signed size overflow", "png", []string{"resource"}, "size_bytes", uint64(1) << 63},
		{"audio not new union", "png", []string{"resource"}, "kind", "audio"},
		{"video unsupported", "png", []string{"resource"}, "kind", "video"},
		{"profile drift", "png", []string{"png"}, "decode_profile", "cutly.decode.png.v0"},
		{"font glyph overflow", "font", []string{"font"}, "num_glyphs", 65536},
		{"font units overflow", "font", []string{"font"}, "units_per_em", 16385},
		{"ascender below int16", "font", []string{"font"}, "hhea_ascender", -32769},
		{"ascender above int16", "font", []string{"font"}, "hhea_ascender", 32768},
		{"negative unsigned", "font", []string{"font"}, "cmap_format12_groups", -1},
		{"uint64 overflow", "font", []string{"font"}, "work_units", json.Number("18446744073709551616")},
		{"table checksum overflow", "font", []string{"font", "tables", "0"}, "checksum", uint64(1) << 32},
		{"table offset misaligned", "font", []string{"font", "tables", "0"}, "offset", 125},
		{"null tables", "font", []string{"font"}, "tables", nil},
		{"null layout", "font", []string{"font"}, "opaque_layout_tables", nil},
		{"duplicate layout", "font", []string{"font"}, "opaque_layout_tables", []string{"GSUB", "GSUB"}},
		{"unsupported layout", "font", []string{"font"}, "opaque_layout_tables", []string{"kern"}},
		{"unauthorized outlines", "font", []string{"font"}, "glyf_table_status", "validated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := decodeExactJSON(t, resourceInspectionFixtureV2(t, test.kind+".canonical.json")).(map[string]any)
			resourceInspectionObjectV2(candidate, test.path)[test.field] = test.value
			assertResourceInspectionRejectedV2(t, schema, candidate)
		})
	}
	// Valid integer extrema must survive strict decoding exactly. These re-signed
	// claims are NOT new observations: Matches must reject their changed facts.
	for _, ascender := range []int16{-32768, 32767} {
		actual := resourceInspectionNativeV2(t, "font")
		r, err := actual.Receipt()
		if err != nil {
			t.Fatal(err)
		}
		r.Font.HheaAscender = ascender
		r.Font.Tables[0].Checksum = ^uint32(0)
		r.Fingerprint = ""
		r.Fingerprint, err = resourceidentityv2.Fingerprint(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(decodeSchemaInstance(t, raw)); err != nil {
			t.Fatal(err)
		}
		verified, err := resourceidentityv2.Decode(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := verified.Receipt()
		if err != nil || got.Font.HheaAscender != ascender || got.Font.Tables[0].Checksum != ^uint32(0) {
			t.Fatalf("integer extrema lost: %v", err)
		}
		if err := resourceidentityv2.Matches(ctx, r.Resource, r, actual); err == nil {
			t.Fatal("re-signed changed claims are not native observations")
		}
	}
}

// Schema validates shape, not relational arithmetic or hash authenticity. These
// cases deliberately remain inside schema bounds, then exercise the typed gate
// with an absent self fingerprint so a stale digest cannot mask the invariant.
func TestResourceInspectionDocumentV2SemanticCounters(t *testing.T) {
	ctx := context.Background()
	schema := resourceInspectionSchemaV2(t)
	for name, mutate := range map[string]func(*resourceidentityv2.Receipt){
		"work charge":          func(r *resourceidentityv2.Receipt) { r.Font.WorkUnits++ },
		"working charge":       func(r *resourceidentityv2.Receipt) { r.Font.WorkingBytes++ },
		"retained charge":      func(r *resourceidentityv2.Receipt) { r.Font.RetainedBytes++ },
		"table count":          func(r *resourceidentityv2.Receipt) { r.Font.TableCount++ },
		"mapping cardinality":  func(r *resourceidentityv2.Receipt) { r.Font.CmapMappingCount = r.Font.CmapMappingSlots + 1 },
		"metric cardinality":   func(r *resourceidentityv2.Receipt) { r.Font.NumberOfHMetrics = uint16(r.Font.NumGlyphs + 1) },
		"encoded size binding": func(r *resourceidentityv2.Receipt) { r.Resource.SizeBytes += 4 },
		"content binding":      func(r *resourceidentityv2.Receipt) { r.Resource.ContentHash = "sha256:" + strings.Repeat("0", 64) },
		"table order": func(r *resourceidentityv2.Receipt) {
			r.Font.Tables[0], r.Font.Tables[1] = r.Font.Tables[1], r.Font.Tables[0]
		},
		"opaque table set": func(r *resourceidentityv2.Receipt) { r.Font.OpaqueLayoutTables = []string{"GSUB"} },
	} {
		t.Run(name, func(t *testing.T) {
			verified, err := resourceidentityv2.Decode(ctx, resourceInspectionFixtureV2(t, "font.canonical.json"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := verified.Receipt()
			if err != nil {
				t.Fatal(err)
			}
			mutate(&r)
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(decodeSchemaInstance(t, raw)); err != nil {
				t.Fatalf("test must isolate a relational codec requirement: %v", err)
			}
			r.Fingerprint = ""
			if _, err := resourceidentityv2.Fingerprint(ctx, r); err == nil {
				t.Fatal("inconsistent deterministic font facts accepted for signing")
			}
		})
	}
}

func resourceInspectionObjectV2(root map[string]any, path []string) map[string]any {
	var current any = root
	for _, part := range path {
		if part == "0" {
			current = current.([]any)[0]
		} else {
			current = current.(map[string]any)[part]
		}
	}
	return current.(map[string]any)
}

func assertResourceInspectionRejectedV2(t *testing.T, schema *jsonschema.Resolved, candidate map[string]any) {
	t.Helper()
	raw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(decodeSchemaInstance(t, raw)); err == nil {
		t.Fatal("schema accepted invalid shape/domain")
	}
	verified, err := resourceidentityv2.Decode(context.Background(), raw)
	if err == nil || !reflect.DeepEqual(verified, resourceidentityv2.Verified{}) {
		t.Fatalf("strict decode must fail zero: %v", err)
	}
}

func TestResourceInspectionDocumentV2StrictDecodeAndOwnership(t *testing.T) {
	ctx := context.Background()
	png := resourceInspectionFixtureV2(t, "png.canonical.json")
	font := resourceInspectionFixtureV2(t, "font.canonical.json")
	for name, raw := range map[string][]byte{
		"duplicate root":   bytes.Replace(png, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1),
		"case alias":       bytes.Replace(png, []byte(`"width":2`), []byte(`"Width":2`), 1),
		"decimal integer":  bytes.Replace(png, []byte(`"width":2`), []byte(`"width":2.0`), 1),
		"exponent integer": bytes.Replace(png, []byte(`"width":2`), []byte(`"width":2e0`), 1),
		"missing zero":     bytes.Replace(font, []byte(`"cmap_format12_groups":0,`), nil, 1),
		"null optional":    bytes.Replace(png, []byte(`"png":`), []byte(`"font":null,"png":`), 1),
		"duplicate nested": bytes.Replace(font, []byte(`"checksum":0`), []byte(`"checksum":0,"checksum":0`), 1),
		"trailing JSON":    append(append([]byte{}, png...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			verified, err := resourceidentityv2.Decode(ctx, raw)
			if err == nil || !reflect.DeepEqual(verified, resourceidentityv2.Verified{}) {
				t.Fatalf("strict hostile spelling did not fail zero: %v", err)
			}
		})
	}
	for _, kind := range []string{"png", "font"} {
		canonical := resourceInspectionFixtureV2(t, kind+".canonical.json")
		input := append([]byte{}, canonical...)
		verified, err := resourceidentityv2.Decode(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		for i := range input {
			input[i] = 'x'
		}
		r, err := verified.Receipt()
		if err != nil {
			t.Fatal(err)
		}
		owned, err := resourceidentityv2.Verify(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if r.PNG != nil {
			r.PNG.Width++
		} else {
			r.Font.Tables[0].Tag = "xxxx"
			r.Font.OpaqueLayoutTables = append(r.Font.OpaqueLayoutTables, "GSUB")
		}
		for _, value := range []resourceidentityv2.Verified{verified, owned} {
			got, err := value.CanonicalJSON()
			if err != nil || !bytes.Equal(got, canonical) {
				t.Fatalf("borrowed mutation changed owned identity: %v", err)
			}
			got[0] = 'x'
			again, err := value.CanonicalJSON()
			if err != nil || !bytes.Equal(again, canonical) {
				t.Fatalf("canonical accessor leaked storage: %v", err)
			}
			fresh, err := value.Receipt()
			if err != nil {
				t.Fatal(err)
			}
			freshJSON, err := canonicaljson.Marshal(fresh)
			if err != nil || !bytes.Equal(freshJSON, canonical) {
				t.Fatalf("receipt accessor leaked nested storage: %v", err)
			}
		}
	}
}
