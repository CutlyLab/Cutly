package conformance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

// TestSchemasCompileAndValidateInstances is the actual Draft 2020-12 publication gate. Merely
// decoding schema files as JSON does not resolve $ref targets or exercise validation keywords.
func TestSchemasCompileAndValidateInstances(t *testing.T) {
	moduleRoot := conformanceModuleRoot(t)
	schemaDir := filepath.Join(moduleRoot, "schemas")
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}

	byID := make(map[string]*jsonschema.Schema)
	byName := make(map[string]*jsonschema.Schema)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(schemaDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := wirejson.Validate(raw); err != nil {
			t.Fatalf("schema %s is not unambiguous strict JSON: %v", entry.Name(), err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("decode schema %s: %v", entry.Name(), err)
		}
		if schema.ID == "" {
			t.Fatalf("schema %s has no $id", entry.Name())
		}
		if _, duplicate := byID[schema.ID]; duplicate {
			t.Fatalf("duplicate schema $id %q", schema.ID)
		}
		byID[schema.ID] = &schema
		byName[entry.Name()] = &schema
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	loader := func(uri *url.URL) (*jsonschema.Schema, error) {
		withoutFragment := *uri
		withoutFragment.Fragment = ""
		if schema := byID[withoutFragment.String()]; schema != nil {
			return schema, nil
		}
		return nil, fmt.Errorf("offline schema registry has no %s", withoutFragment.String())
	}
	resolved := make(map[string]*jsonschema.Resolved, len(names))
	for _, name := range names {
		compiled, err := byName[name].Resolve(&jsonschema.ResolveOptions{Loader: loader, ValidateDefaults: true})
		if err != nil {
			t.Fatalf("compile Draft 2020-12 schema %s: %v", name, err)
		}
		resolved[name] = compiled
	}

	validInstances := map[string]string{
		"cutly.agent-request.document-v2.v2.schema.json":        "conformance/fixtures/agent-request-document-v2.valid.json",
		"cutly.agent-result.document-v2.v2.schema.json":         "conformance/fixtures/agent-result-document-v2.valid.json",
		"cutly.agent-request.v1.schema.json":                    "agent/protocol/testdata/agent-request-v1.canonical.json",
		"cutly.agent-result.v1.schema.json":                     "agent/protocol/testdata/agent-result-v1.canonical.json",
		"cutly.document.v1.schema.json":                         "conformance/fixtures/document-v1.valid.json",
		"cutly.document.v2.schema.json":                         "conformance/fixtures/document-v2.valid.json",
		"cutly.transaction.v1.schema.json":                      "conformance/fixtures/transaction-v1.valid.json",
		"cutly.document-v2-transaction.v2.schema.json":          "conformance/fixtures/document-v2-transaction-numbers.valid.json",
		"cutly.project-plan.document-v2.v2.schema.json":         "conformance/fixtures/document-v2-project-plan.valid.json",
		"cutly.project-commit.document-v2.v2.schema.json":       "conformance/fixtures/document-v2-project-commit.valid.json",
		"cutly.project-history.document-v2.v2.schema.json":      "conformance/fixtures/document-v2-project-history.valid.json",
		"cutly.render-plan.v2.schema.json":                      "conformance/fixtures/render-plan-v2.valid.json",
		"cutly.render-plan.v3.schema.json":                      "conformance/fixtures/render-plan-v3.valid.json",
		"cutly.document-v2-render-plan.v1.schema.json":          "conformance/fixtures/document-v2-render-plan-gap.valid.json",
		"cutly.document-v2-execution-manifest.v2.schema.json":   "conformance/fixtures/document-v2-execution-gap-silence.valid.json",
		"cutly.document-v2-render-request.v2.schema.json":       "conformance/fixtures/document-v2-render-request-fractional.valid.json",
		"cutly.prepared-environment.document-v2.v2.schema.json": "conformance/fixtures/document-v2-prepared-numbers.valid.json",
		"cutly.resource-inspection.document-v2.v2.schema.json":  "conformance/fixtures/document-v2-resource-inspection-png.valid.json",
		"cutly.probe-manifest.v1.schema.json":                   "media/probe/testdata/probe-manifest-v1.valid.json",
		"cutly.media-runtime-bundle.v1.schema.json":             "conformance/fixtures/media-runtime-bundle-v1.valid.json",
		"cutly.execution-manifest.v1.schema.json":               "render/api/testdata/execution-manifest-v1.valid.json",
		"cutly.render-job.v1.schema.json":                       "render/api/testdata/render-job-v1.valid.json",
		"cutly.render-job-history.v1.schema.json":               "render/scheduler/testdata/job-history-page-v1.canonical.json",
		"cutly.exact-frame-request.v1.schema.json":              "render/api/testdata/exact-frame-request-v1.valid.json",
		"cutly.evidence-manifest.v1.schema.json":                "render/evidence/testdata/evidence-manifest-v1.valid.json",
	}
	validInstances[assemblySchemaV2] = "conformance/fixtures/document-v2-chunk-assembly-mixed.valid.json"
	validInstances["cutly.evidence-manifest.document-v2.v2.schema.json"] = "conformance/fixtures/evidence-document-v2-mixed.valid.json"
	if len(validInstances) != len(resolved) {
		t.Fatalf("schema fixture inventory mismatch: %d schemas, %d valid instances", len(resolved), len(validInstances))
	}
	for _, name := range names {
		relative, ok := validInstances[name]
		if !ok {
			t.Fatalf("schema %s has no valid instance fixture", name)
		}
		raw, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		instance := decodeSchemaInstance(t, raw)
		if err := resolved[name].Validate(instance); err != nil {
			t.Fatalf("valid instance %s rejected by %s: %v", relative, name, err)
		}
		if name == "cutly.document.v2.schema.json" {
			testDocumentV2OptionalZeroParity(t, resolved[name], raw)
		}

		if name == "cutly.project-history.document-v2.v2.schema.json" {
			// History is an array of closed commit records, not an object envelope.
			// The project-specific test exercises its record-level hostile corpus.
			if err := resolved[name].Validate(map[string]any{"__cutly_unknown_field": true}); err == nil {
				t.Fatal("project history schema accepted an object envelope")
			}
			continue
		}

		object, ok := instance.(map[string]any)
		if !ok {
			t.Fatalf("root instance %s is not an object", relative)
		}
		object["__cutly_unknown_field"] = true
		if err := resolved[name].Validate(object); err == nil {
			t.Fatalf("schema %s accepted an unknown root property", name)
		}

		critical := decodeSchemaInstance(t, raw).(map[string]any)
		mutateCriticalSchemaInvalid(t, name, critical)
		if err := resolved[name].Validate(critical); err == nil {
			t.Fatalf("schema %s accepted its critical invalid contract case", name)
		}
	}

	for _, schemaCase := range []struct {
		name    string
		fixture string
		profile func(map[string]any) map[string]any
	}{
		{
			name: "cutly.document.v2.schema.json", fixture: "conformance/fixtures/document-v2.valid.json",
			profile: func(root map[string]any) map[string]any {
				return root["timelines"].([]any)[0].(map[string]any)["transitions"].([]any)[0].(map[string]any)
			},
		},
		{
			name: "cutly.document-v2-render-plan.v1.schema.json", fixture: "conformance/fixtures/document-v2-render-plan-transition.valid.json",
			profile: func(root map[string]any) map[string]any {
				return root["transitions"].([]any)[0].(map[string]any)
			},
		},
	} {
		raw, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(schemaCase.fixture)))
		if err != nil {
			t.Fatal(err)
		}
		for _, profile := range []string{documentv2.TransitionDissolveV1, documentv2.TransitionFadeV1, documentv2.TransitionWipeV1} {
			candidate := decodeSchemaInstance(t, raw).(map[string]any)
			schemaCase.profile(candidate)["profile"] = profile
			if err := resolved[schemaCase.name].Validate(candidate); err != nil {
				t.Fatalf("schema %s rejected transition profile %q: %v", schemaCase.name, profile, err)
			}
		}
		candidate := decodeSchemaInstance(t, raw).(map[string]any)
		schemaCase.profile(candidate)["profile"] = "cutly.transition.unknown.v1"
		if err := resolved[schemaCase.name].Validate(candidate); err == nil {
			t.Fatalf("schema %s accepted unknown transition profile", schemaCase.name)
		}
	}

	prepareRequest := decodeSchemaInstance(t, []byte(`{"protocol_version":1,"request_id":"prepare-1","tool":"cutly_render","action":"prepare","project_id":"project","payload":{"plan":{},"resources":[]}}`))
	if err := resolved["cutly.agent-request.v1.schema.json"].Validate(prepareRequest); err != nil {
		t.Fatalf("render prepare request rejected by published Agent schema: %v", err)
	}
	prepareResult := decodeSchemaInstance(t, []byte(`{"protocol_version":1,"ok":true,"request_id":"prepare-1","tool":"cutly_render","action":"prepare","state":"prepared","project_id":"project","changed":false,"replayed":false,"render_required":true,"diagnostics":[],"receipt":{},"next_actions":[{"tool":"cutly_render","action":"preflight","project_id":"project"}]}`))
	if err := resolved["cutly.agent-result.v1.schema.json"].Validate(prepareResult); err != nil {
		t.Fatalf("render prepare result rejected by published Agent schema: %v", err)
	}
	planRequest := decodeSchemaInstance(t, []byte(`{"protocol_version":1,"request_id":"plan-1","tool":"cutly_render","action":"plan","project_id":"project","payload":{"range":{},"target":{}}}`))
	if err := resolved["cutly.agent-request.v1.schema.json"].Validate(planRequest); err != nil {
		t.Fatalf("render plan request rejected by published Agent schema: %v", err)
	}
	planResult := decodeSchemaInstance(t, []byte(`{"protocol_version":1,"ok":true,"request_id":"plan-1","tool":"cutly_render","action":"plan","state":"planned","project_id":"project","changed":false,"replayed":false,"render_required":true,"diagnostics":[],"receipt":{},"next_actions":[{"tool":"cutly_render","action":"prepare","project_id":"project"}]}`))
	if err := resolved["cutly.agent-result.v1.schema.json"].Validate(planResult); err != nil {
		t.Fatalf("render plan result rejected by published Agent schema: %v", err)
	}

	audioProcessingRaw, err := os.ReadFile(filepath.Join(moduleRoot, "conformance", "fixtures", "document-v2-audio-processing.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved["cutly.document.v2.schema.json"].Validate(decodeSchemaInstance(t, audioProcessingRaw)); err != nil {
		t.Fatalf("Document v2 audio-processing fixture rejected by published schema: %v", err)
	}
	audioSchema := resolved["cutly.document.v2.schema.json"]
	audioClone := func() map[string]any {
		return decodeSchemaInstance(t, audioProcessingRaw).(map[string]any)
	}
	audioParts := func(document map[string]any) (map[string]any, map[string]any) {
		matrix := document["timelines"].([]any)[0].(map[string]any)["tracks"].([]any)[0].(map[string]any)["clips"].([]any)[0].(map[string]any)["property_set"].(map[string]any)["audio_channel_matrix"].(map[string]any)
		limiter := document["audio_buses"].([]any)[0].(map[string]any)["properties"].(map[string]any)["sample_peak_limiter"].(map[string]any)
		return matrix, limiter
	}

	unknownMatrixField := audioClone()
	matrix, _ := audioParts(unknownMatrixField)
	matrix["normalize"] = true
	if err := audioSchema.Validate(unknownMatrixField); err == nil {
		t.Fatal("Document v2 schema accepted an implicit channel-matrix normalization field")
	}

	unknownLimiterField := audioClone()
	_, limiter := audioParts(unknownLimiterField)
	limiter["true_peak"] = true
	if err := audioSchema.Validate(unknownLimiterField); err == nil {
		t.Fatal("Document v2 schema accepted an undeclared true-peak limiter field")
	}

	oversizedMatrix := audioClone()
	matrix, _ = audioParts(oversizedMatrix)
	channels := make([]any, documentv2.AudioChannelMatrixMaxInputs+1)
	for index := range channels {
		channels[index] = fmt.Sprintf("C%d", index)
	}
	matrix["source_channels"] = channels
	if err := audioSchema.Validate(oversizedMatrix); err == nil {
		t.Fatal("Document v2 schema accepted an oversized channel matrix")
	}

	vectorViewportRaw, err := os.ReadFile(filepath.Join(moduleRoot, "conformance", "fixtures", "document-v2-vector-viewport.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	documentV2Schema := resolved["cutly.document.v2.schema.json"]
	if err := documentV2Schema.Validate(decodeSchemaInstance(t, vectorViewportRaw)); err != nil {
		t.Fatalf("Document v2 viewport-vector fixture rejected by published schema: %v", err)
	}
	invalidVectorViewportRaw, err := os.ReadFile(filepath.Join(moduleRoot, "conformance", "fixtures", "document-v2-vector-viewport.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := documentV2Schema.Validate(decodeSchemaInstance(t, invalidVectorViewportRaw)); err == nil {
		t.Fatal("Document v2 schema accepted viewport-vector fixture without its required viewport")
	}
	vectorClone := func() map[string]any {
		return decodeSchemaInstance(t, vectorViewportRaw).(map[string]any)
	}
	vectorSource := func(document map[string]any) map[string]any {
		return document["assets"].([]any)[0].(map[string]any)["vector"].(map[string]any)
	}
	assertVectorRejected := func(name string, mutate func(map[string]any)) {
		t.Run("Document v2 viewport-vector schema "+name, func(t *testing.T) {
			candidate := vectorClone()
			mutate(vectorSource(candidate))
			if err := documentV2Schema.Validate(candidate); err == nil {
				t.Fatalf("published schema accepted %s", name)
			}
		})
	}
	assertVectorRejected("v1 profile with viewport", func(vector map[string]any) {
		vector["profile"] = documentv2.VectorProfilePathV1
	})
	assertVectorRejected("unknown viewport field", func(vector map[string]any) {
		vector["viewport"].(map[string]any)["dpi"] = int64(72)
	})
	assertVectorRejected("zero width", func(vector map[string]any) {
		vector["viewport"].(map[string]any)["width_pixels"] = int64(0)
	})
	assertVectorRejected("width above hard bound", func(vector map[string]any) {
		vector["viewport"].(map[string]any)["width_pixels"] = uint64(documentv2.RasterViewportMaxDimension) + 1
	})
	assertVectorRejected("width outside uint32", func(vector map[string]any) {
		vector["viewport"].(map[string]any)["width_pixels"] = uint64(1) << 32
	})
	assertVectorRejected("stroke", func(vector map[string]any) {
		vector["paths"].([]any)[0].(map[string]any)["stroke"] = map[string]any{}
	})
	assertVectorRejected("cubic command", func(vector map[string]any) {
		vector["paths"].([]any)[0].(map[string]any)["commands"].([]any)[1].(map[string]any)["op"] = "cubic_to"
	})

	textV2Raw, err := os.ReadFile(filepath.Join(moduleRoot, "conformance", "fixtures", "document-v2-text-pinned-font-viewport.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := documentV2Schema.Validate(decodeSchemaInstance(t, textV2Raw)); err != nil {
		t.Fatalf("Document v2 pinned-font text fixture rejected by published schema: %v", err)
	}
	if _, err := documentv2.Decode(textV2Raw); err != nil {
		t.Fatalf("Document v2 pinned-font text fixture rejected by decoder: %v", err)
	}
	invalidTextV2Raw, err := os.ReadFile(filepath.Join(moduleRoot, "conformance", "fixtures", "document-v2-text-pinned-font-viewport.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := documentV2Schema.Validate(decodeSchemaInstance(t, invalidTextV2Raw)); err == nil {
		t.Fatal("Document v2 schema accepted pinned-font text without its required viewport")
	}
	if _, err := documentv2.Decode(invalidTextV2Raw); err == nil {
		t.Fatal("Document v2 decoder accepted pinned-font text without its required viewport")
	}
	textClone := func() map[string]any {
		return decodeSchemaInstance(t, textV2Raw).(map[string]any)
	}
	textSource := func(document map[string]any) map[string]any {
		return document["assets"].([]any)[1].(map[string]any)["text"].(map[string]any)
	}
	assertTextRejected := func(name string, mutate func(map[string]any)) {
		t.Run("Document v2 pinned-font text schema "+name, func(t *testing.T) {
			candidate := textClone()
			mutate(textSource(candidate))
			if err := documentV2Schema.Validate(candidate); err == nil {
				t.Fatalf("published schema accepted %s", name)
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := documentv2.Decode(raw); err == nil {
				t.Fatalf("decoder accepted %s", name)
			}
		})
	}
	assertTextRejected("missing profile", func(text map[string]any) { delete(text, "profile") })
	assertTextRejected("explicit legacy alias", func(text map[string]any) { text["profile"] = string(documentv2.CapabilityTextV1) })
	assertTextRejected("missing line height", func(text map[string]any) { delete(text, "line_height") })
	assertTextRejected("missing script", func(text map[string]any) { delete(text, "script") })
	assertTextRejected("unsupported script", func(text map[string]any) { text["script"] = "Cyrl" })
	assertTextRejected("unknown member", func(text map[string]any) { text["wrap"] = true })

	legacyTextRaw, err := os.ReadFile(filepath.Join(moduleRoot, "conformance", "fixtures", "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacyTextWithV2Member := decodeSchemaInstance(t, legacyTextRaw).(map[string]any)
	legacyTextSource := legacyTextWithV2Member["assets"].([]any)[4].(map[string]any)["text"].(map[string]any)
	legacyTextSource["script"] = documentv2.TextScriptLatin
	if err := documentV2Schema.Validate(legacyTextWithV2Member); err == nil {
		t.Fatal("Document v2 schema accepted a Text v2 member in legacy text spelling")
	}
	legacyRaw, err := json.Marshal(legacyTextWithV2Member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := documentv2.Decode(legacyRaw); err == nil {
		t.Fatal("Document v2 decoder accepted a Text v2 member in legacy text spelling")
	}
}

func testDocumentV2OptionalZeroParity(t *testing.T, schema *jsonschema.Resolved, validRaw []byte) {
	t.Helper()
	clone := func() map[string]any {
		value, ok := decodeExactJSON(t, validRaw).(map[string]any)
		if !ok {
			t.Fatal("Document v2 fixture is not an object")
		}
		return value
	}
	assertParity := func(name string, value map[string]any, wantValid bool) {
		t.Run("Document v2 schema decode parity "+name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			schemaErr := schema.Validate(decodeSchemaInstance(t, raw))
			document, decodeErr := documentv2.Decode(raw)
			if (schemaErr == nil) != wantValid || (decodeErr == nil) != wantValid {
				t.Fatalf("wantValid=%t schemaErr=%v decodeErr=%v", wantValid, schemaErr, decodeErr)
			}
			if !wantValid {
				return
			}
			canonical, err := documentv2.CanonicalJSON(document)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{
				`"content_hash":""`, `"uri":""`, `"parent_bus_id":""`,
				`"audio_bus_id":""`, `"required_capability":""`, `"interpolation":""`,
				`"profile":""`, `"line_height":null`, `"viewport":null`, `"script":""`,
			} {
				if bytes.Contains(canonical, []byte(forbidden)) {
					t.Fatalf("canonical output retained optional zero %s: %s", forbidden, canonical)
				}
			}
		})
	}

	aliases := clone()
	assets := aliases["assets"].([]any)
	assets[0].(map[string]any)["content_hash"] = "" // URI remains a valid locator.
	assets[4].(map[string]any)["uri"] = ""
	assets[4].(map[string]any)["content_hash"] = ""
	legacyText := assets[4].(map[string]any)["text"].(map[string]any)
	legacyText["profile"] = ""
	legacyText["script"] = ""
	assets[5].(map[string]any)["uri"] = ""
	assets[5].(map[string]any)["content_hash"] = ""
	aliases["audio_buses"].([]any)[0].(map[string]any)["parent_bus_id"] = ""
	tracks := aliases["timelines"].([]any)[0].(map[string]any)["tracks"].([]any)
	videoClip := tracks[0].(map[string]any)["clips"].([]any)[0].(map[string]any)
	videoClip["property_set"].(map[string]any)["extensions"].([]any)[0].(map[string]any)["required_capability"] = ""
	videoClip["keyframe_lanes"].([]any)[0].(map[string]any)["keyframes"].([]any)[1].(map[string]any)["interpolation"] = ""
	assertParity("accepted aliases", aliases, true)

	legacyNullLineHeight := clone()
	legacyNullLineHeight["assets"].([]any)[4].(map[string]any)["text"].(map[string]any)["line_height"] = nil
	assertParity("legacy null line height", legacyNullLineHeight, false)

	legacyNullViewport := clone()
	legacyNullViewport["assets"].([]any)[4].(map[string]any)["text"].(map[string]any)["viewport"] = nil
	assertParity("legacy null viewport", legacyNullViewport, false)

	implicitGraph := clone()
	delete(implicitGraph, "audio_buses")
	implicitTracks := implicitGraph["timelines"].([]any)[0].(map[string]any)["tracks"].([]any)
	implicitTracks[1].(map[string]any)["audio_bus_id"] = ""
	assertParity("empty implicit audio route", implicitGraph, true)

	missingLocator := clone()
	missingAsset := missingLocator["assets"].([]any)[0].(map[string]any)
	missingAsset["uri"] = ""
	missingAsset["content_hash"] = ""
	assertParity("media empty locator", missingLocator, false)

	blankLocator := clone()
	blankAsset := blankLocator["assets"].([]any)[0].(map[string]any)
	blankAsset["uri"] = "   "
	blankAsset["content_hash"] = ""
	assertParity("media blank locator", blankLocator, false)

	inlineLocator := clone()
	inlineLocator["assets"].([]any)[4].(map[string]any)["uri"] = "host-font://forbidden"
	assertParity("inline nonempty locator", inlineLocator, false)

	fontWithoutPin := clone()
	fontWithoutPin["assets"].([]any)[3].(map[string]any)["content_hash"] = ""
	assertParity("font empty content hash", fontWithoutPin, false)

	renderingWithoutCapability := clone()
	renderingTracks := renderingWithoutCapability["timelines"].([]any)[0].(map[string]any)["tracks"].([]any)
	renderingExtension := renderingTracks[0].(map[string]any)["clips"].([]any)[0].(map[string]any)["property_set"].(map[string]any)["extensions"].([]any)[0].(map[string]any)
	renderingExtension["affects_rendering"] = true
	renderingExtension["required_capability"] = ""
	assertParity("rendering empty capability", renderingWithoutCapability, false)

	nonRenderingCapability := clone()
	nonRenderingTracks := nonRenderingCapability["timelines"].([]any)[0].(map[string]any)["tracks"].([]any)
	nonRenderingExtension := nonRenderingTracks[0].(map[string]any)["clips"].([]any)[0].(map[string]any)["property_set"].(map[string]any)["extensions"].([]any)[0].(map[string]any)
	nonRenderingExtension["required_capability"] = "vendor.note.v1"
	assertParity("non-rendering nonempty capability", nonRenderingCapability, false)

	blankID := clone()
	blankID["id"] = "   "
	assertParity("blank stable ID", blankID, false)

	blankMetadataKey := clone()
	blankMetadataKey["metadata"].(map[string]any)["   "] = true
	assertParity("blank metadata key", blankMetadataKey, false)

	blankAnnotationNamespace := clone()
	blankAnnotationNamespace["annotations"].([]any)[0].(map[string]any)["namespace"] = "   "
	assertParity("blank annotation namespace", blankAnnotationNamespace, false)
}

func mutateCriticalSchemaInvalid(t *testing.T, name string, value map[string]any) {
	t.Helper()
	switch name {
	case "cutly.document.v1.schema.json":
		value["assets"].([]any)[0].(map[string]any)["content_hash"] = "sha256:invalid"
	case "cutly.document.v2.schema.json":
		value["assets"].([]any)[4].(map[string]any)["text"].(map[string]any)["fill"].(map[string]any)["r"] = 0.5
	case "cutly.transaction.v1.schema.json", "cutly.document-v2-transaction.v2.schema.json", "cutly.project-plan.document-v2.v2.schema.json", "cutly.project-commit.document-v2.v2.schema.json":
		value["schema_version"] = int64(999)
	case "cutly.render-plan.v2.schema.json":
		value["schema_version"] = int64(3)
	case "cutly.render-plan.v3.schema.json":
		value["resources"].([]any)[0].(map[string]any)["content_hash"] = "sha256:invalid"
	case "cutly.document-v2-render-plan.v1.schema.json":
		value["schema_version"] = int64(999)
	case "cutly.document-v2-execution-manifest.v2.schema.json":
		value["visual"].(map[string]any)["enabled"] = false
	case "cutly.chunk-assembly-request.document-v2.v2.schema.json":
		value["output"].(map[string]any)["intermediate"].(map[string]any)["video_surface_profile"] = "rgba8"
	case "cutly.document-v2-render-request.v2.schema.json":
		value["execution_manifest"].(map[string]any)["output"].(map[string]any)["frame_clock"] = nil
	case "cutly.prepared-environment.document-v2.v2.schema.json":
		value["document"] = nil
	case "cutly.resource-inspection.document-v2.v2.schema.json":
		value["png"] = nil
	case "cutly.probe-manifest.v1.schema.json":
		value["resource"].(map[string]any)["content_hash"] = "sha256:invalid"
	case "cutly.media-runtime-bundle.v1.schema.json":
		value["signature"] = "not-an-ed25519-signature"
	case "cutly.execution-manifest.v1.schema.json":
		value["resources"].([]any)[0].(map[string]any)["locator"] = "cas://sha256/not-a-digest"
	case "cutly.render-job.v1.schema.json":
		value["artifact_uri"] = "https://example.invalid/output.mp4"
	case "cutly.render-job-history.v1.schema.json":
		value["events"].([]any)[0].(map[string]any)["state"] = "complete"
	case "cutly.exact-frame-request.v1.schema.json":
		value["artifact_uri"] = "https://example.invalid/frame.png"
	case "cutly.evidence-manifest.document-v2.v2.schema.json":
		value["schema_version"] = int64(999)
	case "cutly.evidence-manifest.v1.schema.json":
		value["decoded_video"].(map[string]any)["chunks"].([]any)[0].(map[string]any)["count"] = int64(0)
	case "cutly.agent-request.v1.schema.json":
		value["action"] = "unknown"
	case "cutly.agent-result.v1.schema.json":
		value["action"] = "unknown"
	case "cutly.agent-request.document-v2.v2.schema.json":
		value["payload_profile"] = "cutly.agent-payload.edit.apply.document-v2.v2"
	case "cutly.agent-result.document-v2.v2.schema.json":
		value["schema_version"] = int64(1)
	default:
		t.Fatalf("no critical invalid case for schema %s", name)
	}
}

func conformanceModuleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate conformance package")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), ".."))
}

func decodeSchemaInstance(t *testing.T, raw []byte) any {
	t.Helper()
	return normalizeSchemaInstanceNumbers(t, decodeExactJSON(t, raw))
}

func decodeExactJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// google/jsonschema-go v0.4.2 classifies json.Number by its underlying string kind during the
// `type` keyword check. Convert integer tokens to the smallest exact native integer accepted by
// the validator; unlike the old float64 path this preserves every int64/uint64 fixture value.
func normalizeSchemaInstanceNumbers(t *testing.T, value any) any {
	t.Helper()
	switch typed := value.(type) {
	case json.Number:
		raw := typed.String()
		if !strings.ContainsAny(raw, ".eE") {
			integer, ok := new(big.Int).SetString(raw, 10)
			if !ok {
				t.Fatalf("invalid JSON integer %q", raw)
			}
			if integer.IsInt64() {
				return integer.Int64()
			}
			if integer.Sign() >= 0 && integer.BitLen() <= 64 {
				return integer.Uint64()
			}
			// An integer outside both Cutly wire domains is deliberately left unsupported so
			// schema validation rejects it instead of rounding it into range.
			return typed
		}
		floating, err := typed.Float64()
		if err != nil {
			t.Fatalf("JSON number %q cannot be represented for schema validation: %v", raw, err)
		}
		return floating
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalizeSchemaInstanceNumbers(t, child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = normalizeSchemaInstanceNumbers(t, child)
		}
		return typed
	default:
		return value
	}
}

// TestPublishedIntegerSchemasDeclareWireBounds prevents JSON Schema from advertising a wider
// numeric domain than the concrete Go wire types. The bounds are decoded as json.Number and
// checked with big.Int so this gate itself cannot round int64/uint64 limits through float64.
func TestPublishedIntegerSchemasDeclareWireBounds(t *testing.T) {
	moduleRoot := conformanceModuleRoot(t)
	entries, err := os.ReadDir(filepath.Join(moduleRoot, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(moduleRoot, "schemas", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		root := decodeExactJSON(t, raw)
		walkIntegerSchemas(t, entry.Name(), "$", root)
	}

	document := readSchemaObject(t, filepath.Join(moduleRoot, "schemas", "cutly.document.v1.schema.json"))
	revision := schemaPath(t, document, "$defs", "revision")
	assertExactBound(t, revision, "minimum", "0")
	assertExactBound(t, revision, "maximum", "18446744073709551615")
	rationalValue := schemaPath(t, document, "$defs", "rationalTime", "properties", "value")
	assertExactBound(t, rationalValue, "minimum", "-9223372036854775808")
	assertExactBound(t, rationalValue, "maximum", "9223372036854775807")

	documentV2 := readSchemaObject(t, filepath.Join(moduleRoot, "schemas", "cutly.document.v2.schema.json"))
	revisionV2 := schemaPath(t, documentV2, "$defs", "revision")
	assertExactBound(t, revisionV2, "minimum", "0")
	assertExactBound(t, revisionV2, "maximum", "18446744073709551615")
	rationalNumeratorV2 := schemaPath(t, documentV2, "$defs", "rational", "properties", "numerator")
	if reference, _ := rationalNumeratorV2["$ref"].(string); reference != "#/$defs/int64" {
		t.Fatalf("Document v2 rational numerator ref = %q", reference)
	}
	colorChannelV2 := schemaPath(t, documentV2, "$defs", "uint16")
	assertExactBound(t, colorChannelV2, "minimum", "0")
	assertExactBound(t, colorChannelV2, "maximum", "65535")
	rasterWidthV2 := schemaPath(t, documentV2, "$defs", "rasterViewport", "properties", "width_pixels")
	assertExactBound(t, rasterWidthV2, "minimum", "1")
	assertExactBound(t, rasterWidthV2, "maximum", "65536")
	rasterHeightV2 := schemaPath(t, documentV2, "$defs", "rasterViewport", "properties", "height_pixels")
	assertExactBound(t, rasterHeightV2, "minimum", "1")
	assertExactBound(t, rasterHeightV2, "maximum", "65536")

	execution := readSchemaObject(t, filepath.Join(moduleRoot, "schemas", "cutly.execution-manifest.v1.schema.json"))
	seed := schemaPath(t, execution, "properties", "deterministic_seed")
	assertExactBound(t, seed, "minimum", "-9223372036854775808")
	assertExactBound(t, seed, "maximum", "9223372036854775807")
	width := schemaPath(t, execution, "$defs", "outputSpec", "properties", "width")
	assertExactBound(t, width, "maximum", "2147483647")
}

// TestCutlyWireIntegerBoundaryCorpus covers the lexical/range rules that JSON Schema deliberately
// cannot express: JSON Schema considers 1 and 1.0 the same mathematical integer, while Cutly's
// typed wire profile accepts only a token that encoding/json can decode into the declared Go
// integer. This also closes the one-unit precision gap in schema engines that store maximum as a
// float64 internally.
func TestCutlyWireIntegerBoundaryCorpus(t *testing.T) {
	type signedEnvelope struct {
		Value int64 `json:"value"`
	}
	type unsignedEnvelope struct {
		Value uint64 `json:"value"`
	}

	for _, test := range []struct {
		name    string
		raw     string
		target  any
		wantErr bool
	}{
		{name: "max int64", raw: `{"value":9223372036854775807}`, target: &signedEnvelope{}},
		{name: "min int64", raw: `{"value":-9223372036854775808}`, target: &signedEnvelope{}},
		{name: "above int64", raw: `{"value":9223372036854775808}`, target: &signedEnvelope{}, wantErr: true},
		{name: "below int64", raw: `{"value":-9223372036854775809}`, target: &signedEnvelope{}, wantErr: true},
		{name: "decimal integer spelling", raw: `{"value":1.0}`, target: &signedEnvelope{}, wantErr: true},
		{name: "exponent integer spelling", raw: `{"value":1e0}`, target: &signedEnvelope{}, wantErr: true},
		{name: "max uint64", raw: `{"value":18446744073709551615}`, target: &unsignedEnvelope{}},
		{name: "above uint64", raw: `{"value":18446744073709551616}`, target: &unsignedEnvelope{}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := wirejson.Decode([]byte(test.raw), test.target)
			if (err != nil) != test.wantErr {
				t.Fatalf("Decode(%s) error = %v, wantErr=%t", test.raw, err, test.wantErr)
			}
		})
	}
}

func walkIntegerSchemas(t *testing.T, schemaName, path string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if kind, _ := typed["type"].(string); kind == "integer" {
			minimum, hasMinimum := typed["minimum"].(json.Number)
			maximum, hasMaximum := typed["maximum"].(json.Number)
			if !hasMinimum || !hasMaximum {
				t.Errorf("%s %s integer has no exact minimum/maximum", schemaName, path)
			} else {
				min := parseIntegerToken(t, schemaName+" "+path+" minimum", minimum)
				max := parseIntegerToken(t, schemaName+" "+path+" maximum", maximum)
				if min.Cmp(max) > 0 {
					t.Errorf("%s %s minimum %s exceeds maximum %s", schemaName, path, min, max)
				}
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkIntegerSchemas(t, schemaName, path+"/"+key, typed[key])
		}
	case []any:
		for index, child := range typed {
			walkIntegerSchemas(t, schemaName, fmt.Sprintf("%s/%d", path, index), child)
		}
	}
}

func parseIntegerToken(t *testing.T, label string, value json.Number) *big.Int {
	t.Helper()
	raw := value.String()
	if strings.ContainsAny(raw, ".eE") {
		t.Fatalf("%s is not an integer token: %q", label, raw)
	}
	result, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		t.Fatalf("%s is not a base-10 integer: %q", label, raw)
	}
	return result
}

func readSchemaObject(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := decodeExactJSON(t, raw).(map[string]any)
	if !ok {
		t.Fatalf("schema %s is not an object", path)
	}
	return value
}

func schemaPath(t *testing.T, value map[string]any, path ...string) map[string]any {
	t.Helper()
	current := value
	for _, component := range path {
		next, ok := current[component].(map[string]any)
		if !ok {
			t.Fatalf("schema path %q has no object %q", strings.Join(path, "/"), component)
		}
		current = next
	}
	return current
}

func assertExactBound(t *testing.T, schema map[string]any, keyword, want string) {
	t.Helper()
	value, ok := schema[keyword].(json.Number)
	if !ok || value.String() != want {
		t.Fatalf("%s = %v, want exact integer %s", keyword, schema[keyword], want)
	}
}
