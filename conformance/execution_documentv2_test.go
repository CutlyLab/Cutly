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

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

const executionSchemaV2 = "cutly.document-v2-execution-manifest.v2.schema.json"
const requestSchemaV2 = "cutly.document-v2-render-request.v2.schema.json"

// These records freeze identity contracts only: synthetic digest/receipt claims
// are not evidence of inspected bytes, installed programs or an actual render.
func executionDraftV2(p renderplanv2.Plan) renderapi.ExecutionManifestV2 {
	hash := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	target := p.Target
	draft := renderapi.ExecutionManifestV2{
		CompilerBuild: hash("1"), EvaluatorBuild: hash("2"), ProviderBuild: hash("3"),
		Resources:    []renderapi.MaterializedResourceV2{},
		Backend:      renderapi.BackendCapabilitiesV2{BackendID: "conformance.document-v2", BuildFingerprint: hash("4"), TrackKinds: append([]documentv2.TrackKind{}, p.RequiredCapabilities.TrackKinds...), AssetKinds: append([]documentv2.AssetKind{}, p.RequiredCapabilities.AssetKinds...), Features: append([]videoedit.CapabilityID{}, p.RequiredCapabilities.Features...), MaxWidth: 4096, MaxHeight: 4096},
		MediaRuntime: renderapi.MediaRuntime{FFmpegBuild: hash("5"), DecoderProfile: "conformance.decoder.v1", EncoderProfile: "conformance.encoder.v1", ColorProfile: "conformance.color.v1", TextShaperBuild: hash("a")},
		Output:       renderapi.OutputSpecV2{ReferenceProfile: renderapi.OutputReferenceProfileV2, Format: "mp4", Width: uint32(target.Width), Height: uint32(target.Height), PixelFormat: target.PixelFormat, ColorSpace: target.ColorSpace, VideoCodec: "conformance-video", AudioCodec: "conformance-audio", FrameClock: target.FrameClock, SampleClock: target.SampleClock, SampleRate: target.SampleClock.SamplesPerSecond, Channels: uint32(target.Channels), Intermediate: renderapi.IntermediateProfilesV2{VideoSurfaceProfile: renderapi.VideoSurfaceProfileV2, AudioBlockProfile: renderapi.AudioBlockProfileV2}, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1},
	}
	// A backend advertises a supported superset, not the selected plan closure.
	draft.Backend.TrackKinds = []documentv2.TrackKind{documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector, documentv2.TrackKindVideo}
	draft.Backend.AssetKinds = []documentv2.AssetKind{documentv2.AssetKindAudio, documentv2.AssetKindData, documentv2.AssetKindFont, documentv2.AssetKindImage, documentv2.AssetKindText, documentv2.AssetKindVector, documentv2.AssetKindVideo}
	draft.Backend.Features = append(draft.Backend.Features, videoedit.CapabilityID("conformance.additional.v1"))
	for _, resource := range p.Resources {
		content := resource.ContentHash
		if content == "" {
			content = hash("6")
		}
		materialized := renderapi.MaterializedResourceV2{AssetID: resource.AssetID, Kind: resource.Kind, ContentHash: content, SizeBytes: 10, LocatorScheme: renderapi.LocatorCAS, Locator: "cas://sha256/" + strings.TrimPrefix(content, "sha256:")}
		receipt := hash("7")
		switch resource.Kind {
		case documentv2.AssetKindVideo, documentv2.AssetKindAudio:
			materialized.MaterializationProfile = renderapi.AVMaterializationProfileV2
			materialized.ProbeFingerprint = &receipt
		case documentv2.AssetKindImage:
			materialized.MaterializationProfile = renderapi.ImageMaterializationProfileV2
			materialized.InspectionFingerprint = &receipt
		case documentv2.AssetKindFont:
			materialized.MaterializationProfile = renderapi.FontMaterializationProfileV2
			materialized.InspectionFingerprint = &receipt
		}
		draft.Resources = append(draft.Resources, materialized)
	}
	return draft
}

func executionPlansV2(t *testing.T) map[string]renderplanv2.Plan {
	t.Helper()
	plans := make(map[string]renderplanv2.Plan)
	for _, name := range []string{"gap", "mixed", "transition"} {
		p, err := renderplanv2.Decode(fixture(t, "document-v2-render-plan-"+name+".canonical.json"))
		if err != nil {
			t.Fatal(err)
		}
		key := name
		if name == "gap" {
			key = "gap-silence"
		}
		plans[key] = p
	}
	resolve := func(name, source string, edit func(*documentv2.Document, *videoedit.TimeRange, *videoedit.RenderTarget)) {
		d, err := documentv2.Decode(fixture(t, "document-v2-render-plan-"+source+".document.json"))
		if err != nil {
			t.Fatal(err)
		}
		base := plans[source]
		if source == "gap" {
			base = plans["gap-silence"]
		}
		r, target := base.RequestedRange, base.Target
		edit(&d, &r, &target)
		p, ds := renderplanv2.Resolve(context.Background(), d, r, target)
		if documentv2.HasErrorDiagnostics(ds) {
			t.Fatalf("resolve %s: %+v", name, ds)
		}
		plans[name] = p
	}
	resolve("visual-silence", "mixed", func(d *documentv2.Document, _ *videoedit.TimeRange, _ *videoedit.RenderTarget) {
		tracks := []documentv2.Track{}
		for _, track := range d.Timelines[0].Tracks {
			if track.Kind != documentv2.TrackKindAudio {
				tracks = append(tracks, track)
			}
		}
		d.Timelines[0].Tracks = tracks
	})
	resolve("audio-gap", "mixed", func(d *documentv2.Document, _ *videoedit.TimeRange, _ *videoedit.RenderTarget) {
		tracks := []documentv2.Track{}
		for _, track := range d.Timelines[0].Tracks {
			if track.Kind == documentv2.TrackKindAudio {
				tracks = append(tracks, track)
			}
		}
		d.Timelines[0].Tracks = tracks
	})
	resolve("zero-frame", "mixed", func(_ *documentv2.Document, r *videoedit.TimeRange, _ *videoedit.RenderTarget) {
		*r = videoedit.TimeRange{Start: videoedit.RationalTime{Value: 1, Rate: 100}, Duration: videoedit.RationalTime{Value: 1, Rate: 100}}
	})
	resolve("zero-sample", "mixed", func(_ *documentv2.Document, r *videoedit.TimeRange, target *videoedit.RenderTarget) {
		*r = videoedit.TimeRange{Start: videoedit.RationalTime{Value: 1, Rate: 100}, Duration: videoedit.RationalTime{Value: 1, Rate: 10}}
		target.SampleClock.SamplesPerSecond = 1
	})
	resolve("fractional", "mixed", func(_ *documentv2.Document, _ *videoedit.TimeRange, target *videoedit.RenderTarget) {
		target.FrameClock.Origin = videoedit.RationalTime{Value: -1, Rate: 3}
		target.FrameClock.FramesPerSecond = videoedit.RationalTime{Value: 30000, Rate: 1001}
		target.SampleClock.Origin = videoedit.RationalTime{Value: -1, Rate: 7}
	})
	return plans
}

func executionRequestV2(t *testing.T, p renderplanv2.Plan, manifest renderapi.ExecutionManifestV2, fractional bool) renderapi.RenderRequestV2 {
	t.Helper()
	r := p.RequestedRange
	if fractional {
		r = videoedit.TimeRange{Start: videoedit.RationalTime{Value: 1, Rate: 3}, Duration: videoedit.RationalTime{Value: 7, Rate: 6}}
	}
	frames, err := videoedit.CompileFrameTickRange(r, p.Target.FrameClock)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := videoedit.CompileSampleTickRange(r, p.Target.SampleClock)
	if err != nil {
		t.Fatal(err)
	}
	return renderapi.RenderRequestV2{SchemaVersion: 2, Profile: renderapi.RenderRequestProfileV2, CanonicalJSONProfile: canonicaljson.Profile, Job: renderapi.RenderJob{SchemaVersion: 1, ID: "document-v2-conformance", IdempotencyKey: "document-v2-conformance", PlanFingerprint: p.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint, TimelineRange: r, FrameRange: frames, SampleRange: samples, ArtifactURI: "file:///controlled/document-v2.mp4"}, Plan: p, Manifest: manifest}
}

func executionSchemasV2(t *testing.T) map[string]*jsonschema.Resolved {
	t.Helper()
	registry := map[string]*jsonschema.Schema{}
	names := map[string]*jsonschema.Schema{}
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
		registry[schema.ID] = &schema
		names[filepath.Base(path)] = &schema
	}
	resolved := map[string]*jsonschema.Resolved{}
	for _, name := range []string{executionSchemaV2, requestSchemaV2} {
		schema, err := names[name].Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
			key := *uri
			key.Fragment = ""
			s := registry[key.String()]
			if s == nil {
				return nil, os.ErrNotExist
			}
			return s, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		resolved[name] = schema
	}
	return resolved
}

// This opt-in writer touches only this stage's new fixtures, never the legacy
// corpus. Normal test runs compare independently rebuilt bytes and fixed hashes.
func publishExecutionFixtureV2(t *testing.T, prefix string, raw []byte) {
	t.Helper()
	if os.Getenv("CUTLY_UPDATE_EXECUTIONV2_FIXTURES") != "1" {
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	base := filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", prefix)
	for suffix, data := range map[string][]byte{".canonical.json": raw, ".valid.json": append(pretty.Bytes(), '\n'), ".sha256": []byte(hex.EncodeToString(sum[:]) + "\n")} {
		if err := os.WriteFile(base+suffix, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDocumentV2ExecutionPublishedFixtures(t *testing.T) {
	schemas := executionSchemasV2(t)
	for name, plan := range executionPlansV2(t) {
		manifest, err := renderapi.FreezeExecutionManifestV2(context.Background(), plan, executionDraftV2(plan))
		if err != nil {
			t.Fatalf("freeze %s: %v", name, err)
		}
		if name == "zero-frame" && (len(plan.Visual) == 0 || len(plan.Resources) == 0) {
			t.Fatal("zero-frame case lost authored visual clips/resources")
		}
		if name == "zero-sample" && (len(plan.Audio) == 0 || len(plan.Resources) == 0) {
			t.Fatal("zero-sample case lost authored audio clips/resources")
		}
		if name == "zero-frame" && (plan.FrameRange.Count != 0 || manifest.Visual.Enabled || !manifest.Audio.Enabled) {
			t.Fatal("zero-frame fixture is not genuinely disabled")
		}
		if name == "zero-sample" && (plan.SampleRange.Count != 0 || manifest.Audio.Enabled || !manifest.Visual.Enabled) {
			t.Fatal("zero-sample fixture is not genuinely disabled")
		}
		mraw, err := renderapi.CanonicalExecutionManifestV2(manifest)
		if err != nil {
			t.Fatal(err)
		}
		request := executionRequestV2(t, plan, manifest, name == "fractional")
		rraw, err := renderapi.CanonicalRenderRequestV2(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range []struct {
			prefix, schema string
			raw            []byte
		}{
			{"document-v2-execution-" + name, executionSchemaV2, mraw},
			{"document-v2-render-request-" + name, requestSchemaV2, rraw},
		} {
			t.Run(record.prefix, func(t *testing.T) {
				publishExecutionFixtureV2(t, record.prefix, record.raw)
				valid := fixture(t, record.prefix+".valid.json")
				canonical := fixture(t, record.prefix+".canonical.json")
				if !bytes.Equal(record.raw, canonical) {
					t.Fatal("builder/canonical fixture drift")
				}
				normalized, err := canonicaljson.Canonicalize(valid)
				if err != nil || !bytes.Equal(normalized, canonical) {
					t.Fatalf("valid/canonical mismatch: %v", err)
				}
				sum := sha256.Sum256(canonical)
				actual := hex.EncodeToString(sum[:])
				if actual != strings.TrimSpace(string(fixture(t, record.prefix+".sha256"))) {
					t.Fatal("SHA-256 sidecar mismatch")
				}
				if fixed := executionFixtureHashesV2[record.prefix]; fixed == "" || actual != fixed {
					t.Fatal("fixed protocol SHA-256 changed")
				}
				for _, raw := range [][]byte{valid, canonical} {
					if err := schemas[record.schema].Validate(decodeSchemaInstance(t, raw)); err != nil {
						t.Fatalf("schema rejected published fixture: %v", err)
					}
				}
				if record.schema == executionSchemaV2 {
					m, err := renderapi.DecodeExecutionManifestV2(canonical)
					if err != nil {
						t.Fatal(err)
					}
					encoded, err := renderapi.CanonicalExecutionManifestV2(m)
					if err != nil || !bytes.Equal(encoded, canonical) {
						t.Fatalf("codec drift: %v", err)
					}
					verified, err := renderapi.VerifyExecutionManifestV2AgainstPlan(context.Background(), m, plan)
					if err != nil || !verified.Valid() {
						t.Fatalf("plan-bound verification: %v", err)
					}
					for _, raw := range [][]byte{valid, append(append([]byte(nil), canonical...), '\n')} {
						if _, err := renderapi.DecodeExecutionManifestV2(raw); err == nil {
							t.Fatal("accepted noncanonical manifest")
						}
					}
					if _, err := renderapi.DecodeExecutionManifest(canonical); err == nil {
						t.Fatal("legacy manifest decoder accepted v2")
					}
					if _, err := renderapi.DecodeRenderRequestV2(canonical); err == nil {
						t.Fatal("request decoder accepted manifest")
					}
				} else {
					r, err := renderapi.DecodeRenderRequestV2(canonical)
					if err != nil {
						t.Fatal(err)
					}
					encoded, err := renderapi.CanonicalRenderRequestV2(r)
					if err != nil || !bytes.Equal(encoded, canonical) {
						t.Fatalf("codec drift: %v", err)
					}
					verified, err := renderapi.VerifyRenderRequestV2AgainstPlan(context.Background(), r, plan)
					if err != nil || !verified.Valid() {
						t.Fatalf("independent plan verification: %v", err)
					}
					for _, raw := range [][]byte{valid, append(append([]byte(nil), canonical...), '\n')} {
						if _, err := renderapi.DecodeRenderRequestV2(raw); err == nil {
							t.Fatal("accepted noncanonical request")
						}
					}
					if _, err := renderapi.DecodeExecutionManifestV2(canonical); err == nil {
						t.Fatal("manifest decoder accepted request")
					}
					if _, err := renderplanv2.Decode(canonical); err == nil {
						t.Fatal("plan decoder accepted request")
					}
				}
			})
		}
	}
	for _, legacy := range []string{"execution-manifest-v1.canonical.json", "render-job-v1.canonical.json"} {
		raw, err := os.ReadFile(filepath.Join(conformanceModuleRoot(t), "render", "api", "testdata", legacy))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := renderapi.DecodeExecutionManifestV2(raw); err == nil {
			t.Fatalf("v2 manifest decoder accepted %s", legacy)
		}
		if _, err := renderapi.DecodeRenderRequestV2(raw); err == nil {
			t.Fatalf("v2 request decoder accepted %s", legacy)
		}
	}
}

var executionFixtureHashesV2 = map[string]string{
	"document-v2-execution-audio-gap":           "5c5e42fe1847bd7a252bb47b2dbde403121fbe85fba1377eea454c4322135ead",
	"document-v2-execution-fractional":          "d78d5e3cc2cdb7d904ad859ff03658fc1916f220b707ac75f9eea7f90da4841e",
	"document-v2-execution-gap-silence":         "6f248229da31e02f699c5e8c002bc9551c585eb951077882f397c26fc0e5d658",
	"document-v2-execution-mixed":               "4383f7f65867d46457f8793cf7fa0a18a3dc4b91f3f94ae6f9cb845947c169cc",
	"document-v2-execution-transition":          "0b709b8c0f9c44b893cea4215e409221055ecb6dd0a0a9cc4ee3376c3539a084",
	"document-v2-execution-visual-silence":      "17b28217bd1cc83dde26be10720ec7fb782c8dea0906af71396cd9bbc6cba759",
	"document-v2-execution-zero-frame":          "5d23d601ae1559898f7f18fa92feff675e23b7d0e5699ac0d4ec1db952d2a804",
	"document-v2-execution-zero-sample":         "0aaff3999dfae5fc06711afc064cfbc17aa46c174ece963937eb2685e0efe0ae",
	"document-v2-render-request-audio-gap":      "63039eb6fb98d068364398df5f9892bffaedcdc23ef16e7ff20989d75b62858a",
	"document-v2-render-request-fractional":     "c481e64be283c6f50449f9d39e89edab3a39bea8a38eed38a962c267ed8f9765",
	"document-v2-render-request-gap-silence":    "edf57ac2c6eee929a2468acdaa3bb96ec02c3fe6ca0d4fa65b04de652117a6eb",
	"document-v2-render-request-mixed":          "960d9a1169c9a80fef85be03a2bbd89f18276e9541a900ccfde7805166f5b460",
	"document-v2-render-request-transition":     "f13f7d852e803910e770ee7fcb6c1016541022b38de4c7d54f815bcf47129305",
	"document-v2-render-request-visual-silence": "dd80da4de60afc1673dad7eca1a2d2753901220152e88538b5966e24f3c7c871",
	"document-v2-render-request-zero-frame":     "a68949d110fabc02a68bf3a5dc2f85cdf8641316f6042c9863eb77829b6b2f08",
	"document-v2-render-request-zero-sample":    "abf81dbe227848c9c3ae465fb109fbaa92f77ae32c70baf5428037c668260e3a",
}

func executionObjectV2(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	return decodeExactJSON(t, raw).(map[string]any)
}
func executionCanonicalObjectV2(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonicaljson.Canonicalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func executionHashObjectV2(t *testing.T, value any) string {
	t.Helper()
	sum := sha256.Sum256(executionCanonicalObjectV2(t, value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Re-sign nested identities and then the manifest, so semantic cross-binding
// attacks cannot pass merely because a test accidentally used a stale digest.
func executionResignV2(t *testing.T, m map[string]any) {
	t.Helper()
	for _, domain := range []string{"visual", "audio"} {
		selection := m[domain].(map[string]any)
		if selection["enabled"] != true {
			continue
		}
		selection["descriptor_fingerprint"] = executionHashObjectV2(t, selection["descriptor"])
		selection["configuration_fingerprint"] = executionHashObjectV2(t, selection["configuration"])
		source := selection["source_manifest"].(map[string]any)
		source["descriptor_fingerprint"] = selection["descriptor_fingerprint"]
		source["configuration_fingerprint"] = selection["configuration_fingerprint"]
		selection["source_manifest_fingerprint"] = executionHashObjectV2(t, source)
	}
	delete(m, "fingerprint")
	m["fingerprint"] = executionHashObjectV2(t, m)
}

func TestDocumentV2ExecutionHostileFixtures(t *testing.T) {
	schemas := executionSchemasV2(t)
	type attack struct {
		name, base    string
		schemaInvalid bool
		mutate        func(map[string]any)
	}
	manifestAttacks := []attack{
		{"missing-resources", "gap-silence", true, func(m map[string]any) { delete(m, "resources") }},
		{"null-resources", "gap-silence", true, func(m map[string]any) { m["resources"] = nil }},
		{"unknown-root", "gap-silence", true, func(m map[string]any) { m["backend_authorized"] = true }},
		{"disabled-payload", "gap-silence", true, func(m map[string]any) { m["visual"].(map[string]any)["enabled"] = false }},
		{"enabled-missing-descriptor", "gap-silence", true, func(m map[string]any) { delete(m["visual"].(map[string]any), "descriptor") }},
		{"enabled-null-source", "gap-silence", true, func(m map[string]any) { m["visual"].(map[string]any)["source_manifest"] = nil }},
		{"disabled-null-profile", "zero-frame", true, func(m map[string]any) { m["visual"].(map[string]any)["profile"] = nil }},
		{"resource-both-receipts", "mixed", true, func(m map[string]any) {
			m["resources"].([]any)[0].(map[string]any)["inspection_fingerprint"] = m["document_hash"]
		}},
		{"resource-missing-receipt", "mixed", true, func(m map[string]any) { delete(m["resources"].([]any)[0].(map[string]any), "probe_fingerprint") }},
		{"resource-null-receipt", "mixed", true, func(m map[string]any) { m["resources"].([]any)[0].(map[string]any)["probe_fingerprint"] = nil }},
		{"font-probe-receipt", "mixed", true, func(m map[string]any) {
			r := m["resources"].([]any)[1].(map[string]any)
			r["probe_fingerprint"] = r["inspection_fingerprint"]
			delete(r, "inspection_fingerprint")
		}},
		{"resource-wrong-profile", "mixed", true, func(m map[string]any) {
			m["resources"].([]any)[0].(map[string]any)["materialization_profile"] = renderapi.FontMaterializationProfileV2
		}},
		{"output-clock-null", "gap-silence", true, func(m map[string]any) { m["output"].(map[string]any)["frame_clock"] = nil }},
		{"output-unknown-intermediate", "gap-silence", true, func(m map[string]any) {
			m["output"].(map[string]any)["intermediate"].(map[string]any)["video_surface_profile"] = "rgba8"
		}},
		{"config-unknown-field", "gap-silence", true, func(m map[string]any) {
			m["visual"].(map[string]any)["configuration"].(map[string]any)["implicit_color_conversion"] = true
		}},
		{"source-null-clips", "gap-silence", true, func(m map[string]any) {
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["clips"] = nil
		}},
		{"descriptor-domain", "gap-silence", true, func(m map[string]any) {
			m["visual"].(map[string]any)["descriptor"].(map[string]any)["domain"] = "audio"
		}},
		{"fingerprint", "gap-silence", false, func(m map[string]any) { m["fingerprint"] = m["document_hash"] }},
		{"resigned-config-clock", "gap-silence", false, func(m map[string]any) {
			m["visual"].(map[string]any)["configuration"].(map[string]any)["frame_clock"].(map[string]any)["origin"].(map[string]any)["value"] = json.Number("-2")
			executionResignV2(t, m)
		}},
		{"resigned-source-document", "gap-silence", false, func(m map[string]any) {
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["document_hash"] = m["compiler_build"]
			executionResignV2(t, m)
		}},
		{"resigned-source-resource-omission", "mixed", false, func(m map[string]any) {
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["resources"] = []any{}
			executionResignV2(t, m)
		}},
	}
	manifestAttacks = append(manifestAttacks,
		attack{"source-missing-requested-range", "mixed", true, func(m map[string]any) {
			delete(m["visual"].(map[string]any)["source_manifest"].(map[string]any), "requested_range")
		}},
		attack{"source-null-requested-range", "mixed", true, func(m map[string]any) {
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["requested_range"] = nil
		}},
		attack{"resource-int64-overflow", "mixed", false, func(m map[string]any) {
			m["resources"].([]any)[0].(map[string]any)["size_bytes"] = json.Number("9223372036854775808")
		}},
		attack{"resigned-source-projection", "mixed", false, func(m map[string]any) {
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["clips"].([]any)[0].(map[string]any)["visible_source_range"].(map[string]any)["start"].(map[string]any)["value"] = json.Number("999")
			executionResignV2(t, m)
		}},
	)
	requestAttacks := []attack{
		{"missing-plan", "fractional", true, func(r map[string]any) { delete(r, "plan") }},
		{"null-manifest", "fractional", true, func(r map[string]any) { r["execution_manifest"] = nil }},
		{"profile", "fractional", true, func(r map[string]any) { r["profile"] = renderapi.ExecutionManifestProfileV2 }},
		{"job-manifest-binding", "fractional", false, func(r map[string]any) {
			r["job"].(map[string]any)["execution_manifest_fingerprint"] = r["plan"].(map[string]any)["fingerprint"]
		}},
		{"job-frame-ticks", "fractional", false, func(r map[string]any) {
			r["job"].(map[string]any)["frame_range"].(map[string]any)["first"] = json.Number("0")
		}},
		{"job-sample-ticks", "fractional", false, func(r map[string]any) {
			r["job"].(map[string]any)["sample_range"].(map[string]any)["first"] = json.Number("0")
		}},
		{"job-outside-plan", "fractional", false, func(r map[string]any) {
			r["job"].(map[string]any)["timeline_range"].(map[string]any)["start"].(map[string]any)["value"] = json.Number("100")
		}},
		{"resigned-disabled-domain", "mixed", false, func(r map[string]any) {
			m := r["execution_manifest"].(map[string]any)
			m["visual"] = map[string]any{"enabled": false}
			executionResignV2(t, m)
			r["job"].(map[string]any)["execution_manifest_fingerprint"] = m["fingerprint"]
		}},
		{"resigned-resource-substitution", "transition", false, func(r map[string]any) {
			m := r["execution_manifest"].(map[string]any)
			m["resources"].([]any)[0].(map[string]any)["asset_id"] = "different-asset"
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["resources"].([]any)[0].(map[string]any)["asset_id"] = "different-asset"
			executionResignV2(t, m)
			r["job"].(map[string]any)["execution_manifest_fingerprint"] = m["fingerprint"]
		}},
		{"resigned-plan-binding", "fractional", false, func(r map[string]any) {
			m := r["execution_manifest"].(map[string]any)
			m["plan_fingerprint"] = m["compiler_build"]
			for _, domain := range []string{"visual", "audio"} {
				m[domain].(map[string]any)["source_manifest"].(map[string]any)["plan_fingerprint"] = m["plan_fingerprint"]
			}
			executionResignV2(t, m)
			r["job"].(map[string]any)["execution_manifest_fingerprint"] = m["fingerprint"]
		}},
	}
	expected := map[string]bool{}
	for _, group := range []struct {
		prefix, schema string
		attacks        []attack
	}{{"document-v2-execution-", executionSchemaV2, manifestAttacks}, {"document-v2-render-request-", requestSchemaV2, requestAttacks}} {
		for _, test := range group.attacks {
			name := group.prefix + test.name + ".invalid.json"
			expected[name] = true
			t.Run(name, func(t *testing.T) {
				base := fixture(t, group.prefix+test.base+".canonical.json")
				object := executionObjectV2(t, base)
				test.mutate(object)
				raw := executionCanonicalObjectV2(t, object)
				if os.Getenv("CUTLY_UPDATE_EXECUTIONV2_FIXTURES") == "1" {
					if err := os.WriteFile(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", name), raw, 0644); err != nil {
						t.Fatal(err)
					}
				}
				published := fixture(t, name)
				if !bytes.Equal(raw, published) {
					t.Fatal("hostile fixture drift")
				}
				canonical, err := canonicaljson.Canonicalize(published)
				if err != nil || !bytes.Equal(canonical, published) {
					t.Fatalf("hostile record is not complete canonical JSON: %v", err)
				}
				schemaErr := schemas[group.schema].Validate(decodeSchemaInstance(t, published))
				// The schema declares the exact int64 maximum, but this engine stores
				// numeric bounds as float64. Only the wire codec can distinguish
				// maxInt64 from maxInt64+1; do not require that engine bug to persist.
				if test.name != "resource-int64-overflow" && (schemaErr != nil) != test.schemaInvalid {
					t.Fatalf("schema rejection mismatch, want invalid=%t: %v", test.schemaInvalid, schemaErr)
				}
				if group.schema == executionSchemaV2 {
					got, err := renderapi.DecodeExecutionManifestV2(published)
					if err == nil || !reflect.DeepEqual(got, renderapi.ExecutionManifestV2{}) {
						t.Fatal("hostile manifest accepted or returned partial value")
					}
				} else {
					if test.name == "resigned-plan-binding" {
						// These manifests are independently self-consistent. Only the
						// embedded/independent plan binding may reject the request.
						manifestRaw := executionCanonicalObjectV2(t, object["execution_manifest"])
						if _, err := renderapi.DecodeExecutionManifestV2(manifestRaw); err != nil {
							t.Fatalf("cross-binding attack did not reach plan verification: %v", err)
						}
					}
					got, err := renderapi.DecodeRenderRequestV2(published)
					if err == nil || !reflect.DeepEqual(got, renderapi.RenderRequestV2{}) {
						t.Fatal("hostile request accepted or returned partial value")
					}
				}
			})
		}
	}
	for _, glob := range []string{"document-v2-execution-*.invalid.json", "document-v2-render-request-*.invalid.json"} {
		paths, err := filepath.Glob(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", glob))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if !expected[filepath.Base(path)] {
				t.Fatalf("unregistered hostile fixture %s", path)
			}
			delete(expected, filepath.Base(path))
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing hostile fixtures: %v", expected)
	}
}

func TestDocumentV2ExecutionSchemaBoundaries(t *testing.T) {
	schemas := executionSchemasV2(t)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"width zero", func(m map[string]any) { m["output"].(map[string]any)["width"] = int64(0) }},
		{"width hard limit", func(m map[string]any) { m["output"].(map[string]any)["width"] = uint64(2147483648) }},
		{"negative resource size", func(m map[string]any) { m["resources"].([]any)[0].(map[string]any)["size_bytes"] = int64(-1) }},
		// More than one float64 ULP above maxInt64: the published schema still
		// declares the exact bound, while the adjacent lexical case is frozen
		// separately and always rejected by the strict codec.
		{"resource size overflow", func(m map[string]any) {
			m["resources"].([]any)[0].(map[string]any)["size_bytes"] = uint64(9223372036854777856)
		}},
		{"nonzero seed", func(m map[string]any) { m["deterministic_seed"] = int64(1) }},
		{"unsupported track", func(m map[string]any) { m["backend"].(map[string]any)["track_kinds"] = []any{"subtitle"} }},
		{"unsupported asset", func(m map[string]any) { m["backend"].(map[string]any)["asset_kinds"] = []any{"movie"} }},
		{"zero enabled ticks", func(m map[string]any) {
			m["visual"].(map[string]any)["source_manifest"].(map[string]any)["tick_range"].(map[string]any)["count"] = int64(0)
		}},
		{"null runtime", func(m map[string]any) { m["media_runtime"] = nil }},
		{"null backend features", func(m map[string]any) { m["backend"].(map[string]any)["features"] = nil }},
		{"duplicate capability", func(m map[string]any) { m["backend"].(map[string]any)["track_kinds"] = []any{"video", "video"} }},
		{"path traversal", func(m map[string]any) {
			r := m["resources"].([]any)[0].(map[string]any)
			r["locator_scheme"] = "file"
			r["locator"] = "/controlled/../escape"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := decodeSchemaInstance(t, fixture(t, "document-v2-execution-mixed.valid.json")).(map[string]any)
			test.mutate(m)
			if err := schemas[executionSchemaV2].Validate(m); err == nil {
				t.Fatal("schema accepted invalid boundary")
			}
		})
	}
}
