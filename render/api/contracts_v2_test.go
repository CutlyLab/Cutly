package renderapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func planFixtureV2(t *testing.T, name string) renderplanv2.Plan {
	t.Helper()
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-plan-" + name + ".canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := renderplanv2.Decode(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func draftFixtureV2(p renderplanv2.Plan) ExecutionManifestV2 {
	t := p.Target
	m := ExecutionManifestV2{CompilerBuild: digest('1'), EvaluatorBuild: digest('2'), ProviderBuild: digest('3'), Resources: []MaterializedResourceV2{}, Backend: BackendCapabilitiesV2{BackendID: "document-v2-test", BuildFingerprint: digest('4'), TrackKinds: append([]documentv2.TrackKind{}, p.RequiredCapabilities.TrackKinds...), AssetKinds: append([]documentv2.AssetKind{}, p.RequiredCapabilities.AssetKinds...), Features: append([]videoedit.CapabilityID{}, p.RequiredCapabilities.Features...), MaxWidth: 4096, MaxHeight: 4096}, MediaRuntime: MediaRuntime{FFmpegBuild: digest('5'), DecoderProfile: "test.decoder.v1", EncoderProfile: "test.encoder.v1", ColorProfile: "test.color.v1"}, Output: OutputSpecV2{ReferenceProfile: OutputReferenceProfileV2, Format: "mp4", Width: uint32(t.Width), Height: uint32(t.Height), PixelFormat: t.PixelFormat, ColorSpace: t.ColorSpace, VideoCodec: "test-video", AudioCodec: "test-audio", FrameClock: t.FrameClock, SampleClock: t.SampleClock, SampleRate: t.SampleClock.SamplesPerSecond, Channels: uint32(t.Channels), Intermediate: IntermediateProfilesV2{VideoSurfaceProfileV2, AudioBlockProfileV2}, DecodedVideoProfile: DecodedVideoProfileRGBA8V1, DecodedAudioProfile: DecodedAudioProfilePCM16LEV1}}
	for _, r := range p.Resources {
		h := r.ContentHash
		if h == "" {
			h = digest('6')
		}
		a := MaterializedResourceV2{AssetID: r.AssetID, Kind: r.Kind, ContentHash: h, SizeBytes: 10, LocatorScheme: LocatorCAS, Locator: "cas://sha256/" + strings.TrimPrefix(h, "sha256:")}
		switch r.Kind {
		case documentv2.AssetKindVideo, documentv2.AssetKindAudio:
			a.MaterializationProfile = AVMaterializationProfileV2
			a.ProbeFingerprint = ptrV2(digest('7'))
		case documentv2.AssetKindImage:
			a.MaterializationProfile = ImageMaterializationProfileV2
			a.InspectionFingerprint = ptrV2(digest('8'))
		case documentv2.AssetKindFont:
			a.MaterializationProfile = FontMaterializationProfileV2
			a.InspectionFingerprint = ptrV2(digest('9'))
		}
		m.Resources = append(m.Resources, a)
	}
	return m
}
func manifestFixtureV2(t *testing.T, p renderplanv2.Plan) ExecutionManifestV2 {
	t.Helper()
	m, err := FreezeExecutionManifestV2(context.Background(), p, draftFixtureV2(p))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func requestFixtureV2(p renderplanv2.Plan, m ExecutionManifestV2) RenderRequestV2 {
	return RenderRequestV2{2, RenderRequestProfileV2, canonicaljson.Profile, RenderJob{SchemaVersion: 1, ID: "job-v2", IdempotencyKey: "test", PlanFingerprint: p.Fingerprint, ExecutionManifestFingerprint: m.Fingerprint, TimelineRange: p.RequestedRange, FrameRange: p.FrameRange, SampleRange: p.SampleRange, ArtifactURI: "file:///tmp/document-v2.mp4"}, p, m}
}
func cloneV2[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func resignV2(t *testing.T, m *ExecutionManifestV2) {
	t.Helper()
	for _, s := range []*ProgramSelectionV2{&m.Visual, &m.Audio} {
		if !s.Enabled {
			continue
		}
		if s.Descriptor != nil {
			h, err := hashV2(s.Descriptor)
			if err != nil {
				t.Fatal(err)
			}
			s.DescriptorFingerprint = ptrV2(h)
		}
		if s.Configuration != nil {
			h, err := hashV2(s.Configuration)
			if err != nil {
				t.Fatal(err)
			}
			s.ConfigurationFingerprint = ptrV2(h)
		}
		if s.SourceManifest != nil {
			s.SourceManifest.DescriptorFingerprint = *s.DescriptorFingerprint
			s.SourceManifest.ConfigurationFingerprint = *s.ConfigurationFingerprint
			h, err := hashV2(s.SourceManifest)
			if err != nil {
				t.Fatal(err)
			}
			s.SourceManifestFingerprint = ptrV2(h)
		}
	}
	h, err := manifestFingerprintV2(*m)
	if err != nil {
		t.Fatal(err)
	}
	m.Fingerprint = h
}
func TestV2RoundtripProfilesAndIdentity(t *testing.T) {
	for _, name := range []string{"gap", "mixed", "transition"} {
		t.Run(name, func(t *testing.T) {
			p := planFixtureV2(t, name)
			m := manifestFixtureV2(t, p)
			raw, err := CanonicalExecutionManifestV2(m)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeExecutionManifestV2(raw)
			if err != nil || !reflect.DeepEqual(m, decoded) {
				t.Fatalf("roundtrip %v", err)
			}
			if _, err := VerifyExecutionManifestV2AgainstPlan(context.Background(), decoded, p); err != nil {
				t.Fatal(err)
			}
			r := requestFixtureV2(p, m)
			rr, err := CanonicalRenderRequestV2(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = DecodeRenderRequestV2(rr); err != nil {
				t.Fatal(err)
			}
			if _, err = VerifyRenderRequestV2AgainstPlan(context.Background(), r, p); err != nil {
				t.Fatal(err)
			}
			if _, err = DecodeExecutionManifest(raw); err == nil {
				t.Fatal("legacy accepted v2")
			}
			if _, err = renderplanv2.Decode(raw); err == nil {
				t.Fatal("plan accepted manifest")
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "fingerprint")
			want, _ := hashV2(fields)
			if want != m.Fingerprint {
				t.Fatal("fingerprint member not excluded")
			}
			if name == "gap" && (*m.Visual.Profile != TransparentGapProgramProfileV2 || *m.Audio.Profile != SilenceProgramProfileV2) {
				t.Fatal("missing explicit gap/silence")
			}
		})
	}
}
func TestV2StrictManifestAndRequestCodecs(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	m := manifestFixtureV2(t, p)
	raw, _ := CanonicalExecutionManifestV2(m)
	mutations := map[string][]byte{"whitespace": append([]byte(" "), raw...), "newline": append(append([]byte{}, raw...), '\n'), "duplicate": bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1), "fraction": bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2.0`), 1), "exponent": bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2e0`), 1), "negative-zero": bytes.Replace(raw, []byte(`"deterministic_seed":0`), []byte(`"deterministic_seed":-0`), 1), "overflow": bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":4294967296`), 1)}
	for name, b := range mutations {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeExecutionManifestV2(b)
			if err == nil || !reflect.DeepEqual(got, ExecutionManifestV2{}) {
				t.Fatalf("accepted or partial %v", err)
			}
		})
	}
	for _, field := range []string{"schema_version", "profile", "canonical_json_profile", "resources", "deterministic_seed", "visual"} {
		for _, mode := range []string{"missing", "null", "unknown"} {
			t.Run(field+mode, func(t *testing.T) {
				var v map[string]json.RawMessage
				_ = json.Unmarshal(raw, &v)
				switch mode {
				case "missing":
					delete(v, field)
				case "null":
					v[field] = json.RawMessage("null")
				case "unknown":
					v["UNKNOWN_"+field] = v[field]
				}
				b, _ := encodeV2(v, MaxManifestBytesV2)
				got, err := DecodeExecutionManifestV2(b)
				if err == nil || !reflect.DeepEqual(got, ExecutionManifestV2{}) {
					t.Fatal("accepted or partial")
				}
			})
		}
	}
	rr, _ := CanonicalRenderRequestV2(requestFixtureV2(p, m))
	for _, bad := range [][]byte{append([]byte(" "), rr...), bytes.Replace(rr, []byte(`"job":`), []byte(`"job":null,"JOB":`), 1)} {
		got, err := DecodeRenderRequestV2(bad)
		if err == nil || !reflect.DeepEqual(got, RenderRequestV2{}) {
			t.Fatal("request accepted or partial")
		}
	}
}
func TestV2RefingerprintedNestedTamper(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	base := manifestFixtureV2(t, p)
	tests := map[string]func(*ExecutionManifestV2){
		"seed": func(m *ExecutionManifestV2) { m.DeterministicSeed = 1 }, "schema": func(m *ExecutionManifestV2) { m.SchemaVersion = 1 }, "profile": func(m *ExecutionManifestV2) { m.Profile = "wrong" }, "canonical": func(m *ExecutionManifestV2) { m.CanonicalJSONProfile = "wrong" }, "semantic": func(m *ExecutionManifestV2) { m.SemanticProfile = "wrong" }, "doc": func(m *ExecutionManifestV2) { m.DocumentHash = digest('a') }, "plan": func(m *ExecutionManifestV2) { m.PlanFingerprint = digest('a') },
		"size": func(m *ExecutionManifestV2) { m.Resources[0].SizeBytes = 0 }, "negative-size": func(m *ExecutionManifestV2) { m.Resources[0].SizeBytes = -1 }, "resource-kind": func(m *ExecutionManifestV2) { m.Resources[0].Kind = documentv2.AssetKindData }, "resource-id": func(m *ExecutionManifestV2) { m.Resources[0].AssetID = "substituted" }, "resource-hash": func(m *ExecutionManifestV2) { m.Resources[0].ContentHash = digest('a') }, "locator": func(m *ExecutionManifestV2) { m.Resources[0].Locator = "cas://sha256/other" }, "materialization": func(m *ExecutionManifestV2) { m.Resources[0].MaterializationProfile = "wrong" }, "extra-resource": func(m *ExecutionManifestV2) { m.Resources = append(m.Resources, m.Resources[0]) }, "missing-resource": func(m *ExecutionManifestV2) { m.Resources = m.Resources[1:] }, "resource-order": func(m *ExecutionManifestV2) { m.Resources[0], m.Resources[1] = m.Resources[1], m.Resources[0] },
		"backend-feature": func(m *ExecutionManifestV2) { m.Backend.Features = []videoedit.CapabilityID{} }, "backend-tracks": func(m *ExecutionManifestV2) { m.Backend.TrackKinds = []documentv2.TrackKind{} }, "backend-assets": func(m *ExecutionManifestV2) { m.Backend.AssetKinds = []documentv2.AssetKind{} }, "backend-order": func(m *ExecutionManifestV2) { m.Backend.Features = append(m.Backend.Features, m.Backend.Features[0]) }, "backend-width": func(m *ExecutionManifestV2) { m.Backend.MaxWidth = 1 }, "backend-build": func(m *ExecutionManifestV2) { m.Backend.BuildFingerprint = "bad" },
		"width": func(m *ExecutionManifestV2) { m.Output.Width++ }, "height": func(m *ExecutionManifestV2) { m.Output.Height++ }, "channels": func(m *ExecutionManifestV2) { m.Output.Channels++ }, "sample-rate": func(m *ExecutionManifestV2) { m.Output.SampleRate++ }, "frame-clock": func(m *ExecutionManifestV2) { m.Output.FrameClock.FramesPerSecond.Value++ }, "sample-clock": func(m *ExecutionManifestV2) { m.Output.SampleClock.SamplesPerSecond++ }, "pixel": func(m *ExecutionManifestV2) { m.Output.PixelFormat = "other" }, "color": func(m *ExecutionManifestV2) { m.Output.ColorSpace = "other" }, "intermediate": func(m *ExecutionManifestV2) { m.Output.Intermediate.VideoSurfaceProfile = DecodedVideoProfileRGBA8V1 }, "final": func(m *ExecutionManifestV2) { m.Output.DecodedVideoProfile = VideoSurfaceProfileV2 },
		"disabled-visual": func(m *ExecutionManifestV2) { m.Visual = ProgramSelectionV2{} }, "disabled-audio": func(m *ExecutionManifestV2) { m.Audio = ProgramSelectionV2{} }, "descriptor": func(m *ExecutionManifestV2) { m.Visual.Descriptor.Contribution = "invented" }, "source-clip": func(m *ExecutionManifestV2) { m.Visual.SourceManifest.Clips[0].Binding.ClipID = "other" }, "source-projection": func(m *ExecutionManifestV2) { m.Visual.SourceManifest.Clips[0].VisibleSourceRange.Duration.Value++ }, "source-resources": func(m *ExecutionManifestV2) { m.Visual.SourceManifest.Resources = []MaterializedResourceV2{} }, "source-tick": func(m *ExecutionManifestV2) { m.Visual.SourceManifest.TickRange.First++ }, "source-doc": func(m *ExecutionManifestV2) { m.Visual.SourceManifest.DocumentHash = digest('a') }, "source-bus": func(m *ExecutionManifestV2) {
			m.Audio.SourceManifest.AudioBusIDs = append(m.Audio.SourceManifest.AudioBusIDs, "invented")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := cloneV2(base)
			mutate(&m)
			resignV2(t, &m)
			v, err := VerifyExecutionManifestV2AgainstPlan(context.Background(), m, p)
			if err == nil || v.Valid() {
				t.Fatal("accepted or partial")
			}
			r := requestFixtureV2(p, m)
			if v, err := VerifyRenderRequestV2(r); err == nil || v.Valid() {
				t.Fatal("request accepted or partial")
			}
		})
	}
}
func TestV2RawConfigAndUnionPresence(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	base := manifestFixtureV2(t, p)
	for _, raw := range []string{" {}", "{} ", "null", "[]", "0", `{"x":1,"x":2}`, `{"x":0.5}`, `{"x":1e1}`, `{"unknown":1}`, strings.Repeat(" ", MaxConfigurationBytesV2+1) + "{}"} {
		t.Run(raw[:min(len(raw), 20)], func(t *testing.T) {
			m := cloneV2(base)
			m.Visual.Configuration = json.RawMessage(raw)
			if _, err := CanonicalExecutionManifestV2(m); err == nil {
				t.Fatal("canonical laundered config")
			}
			if _, err := VerifyExecutionManifestV2(m); err == nil {
				t.Fatal("verify laundered config")
			}
			if _, err := FreezeExecutionManifestV2(context.Background(), p, m); err == nil {
				t.Fatal("freeze laundered config")
			}
			if _, err := CanonicalRenderRequestV2(requestFixtureV2(p, m)); err == nil {
				t.Fatal("request laundered config")
			}
		})
	}
	for _, mutate := range []func(*ExecutionManifestV2){func(m *ExecutionManifestV2) { m.Visual.Profile = nil }, func(m *ExecutionManifestV2) { m.Visual.Descriptor = nil }, func(m *ExecutionManifestV2) { m.Visual.SourceManifest = nil }, func(m *ExecutionManifestV2) { m.Visual.Enabled = false }, func(m *ExecutionManifestV2) {
		m.Resources[0].InspectionFingerprint = ptrV2("")
		m.Resources[0].ProbeFingerprint = ptrV2("")
	}, func(m *ExecutionManifestV2) {
		m.Resources[0].InspectionFingerprint = nil
		m.Resources[0].ProbeFingerprint = nil
	}} {
		m := cloneV2(base)
		mutate(&m)
		if _, err := VerifyExecutionManifestV2(m); err == nil {
			t.Fatal("union accepted")
		}
	}
	raw, _ := CanonicalExecutionManifestV2(base)
	for _, field := range []string{"profile", "descriptor", "configuration", "configuration_fingerprint", "source_manifest", "source_manifest_fingerprint"} {
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		visual := v["visual"].(map[string]any)
		visual[field] = nil
		b, _ := encodeV2(v, MaxManifestBytesV2)
		if _, err := DecodeExecutionManifestV2(b); err == nil {
			t.Fatal("null enabled member accepted", field)
		}
	}
}
func TestV2RequestJobs(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	m := manifestFixtureV2(t, p)
	base := requestFixtureV2(p, m)
	for _, mutate := range []func(*RenderRequestV2){func(r *RenderRequestV2) { r.SchemaVersion = 1 }, func(r *RenderRequestV2) { r.Job.PlanFingerprint = digest('a') }, func(r *RenderRequestV2) { r.Job.ExecutionManifestFingerprint = digest('a') }, func(r *RenderRequestV2) { r.Job.FrameRange.First++ }, func(r *RenderRequestV2) { r.Job.SampleRange.Count++ }, func(r *RenderRequestV2) { r.Job.TimelineRange.Start.Value-- }, func(r *RenderRequestV2) { r.Job.ArtifactURI = "file:///tmp/../out.mp4" }, func(r *RenderRequestV2) { r.Job.ID = "" }, func(r *RenderRequestV2) { r.Plan.Visual[0].Binding.ClipID = "other" }} {
		r := cloneV2(base)
		mutate(&r)
		if v, err := VerifyRenderRequestV2(r); err == nil || v.Valid() {
			t.Fatal("invalid request accepted")
		}
	}
}
func TestV2OwnershipAndConcurrentReads(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	draft := draftFixtureV2(p)
	m, err := FreezeExecutionManifestV2(context.Background(), p, draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Resources[0].AssetID = "caller"
	if m.Resources[0].AssetID == "caller" {
		t.Fatal("freeze aliases draft")
	}
	v, err := VerifyExecutionManifestV2(m)
	if err != nil {
		t.Fatal(err)
	}
	r, err := VerifyRenderRequestV2(requestFixtureV2(p, m))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := v.Canonical()
	m.Resources[0].AssetID = "mutated"
	m.Visual.Configuration[0] = 'X'
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				b, e := v.Canonical()
				if e != nil || !bytes.Equal(b, before) {
					t.Error("changed handle")
				}
				b[0] = 'x'
				mm, _ := v.Manifest()
				mm.Resources[0].AssetID = "reader"
				rr, _ := r.Request()
				rr.Manifest.Resources[0].AssetID = "reader"
				rr.Plan.Visual[0].Binding.ClipID = "reader"
			}
		}()
	}
	wg.Wait()
	if v.Fingerprint() == "" {
		t.Fatal("lost fp")
	}
	if _, err := (VerifiedExecutionManifestV2{}).Manifest(); err == nil {
		t.Fatal("zero manifest handle")
	}
	if _, err := (VerifiedRenderRequestV2{}).Request(); err == nil {
		t.Fatal("zero request handle")
	}
}

type panicContextV2 struct{ context.Context }

func (panicContextV2) Err() error { panic("host context panic") }
func TestV2CancellationPanicAndLimits(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	m := manifestFixtureV2(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx, panicContextV2{context.Background()}} {
		if out, err := FreezeExecutionManifestV2(c, p, m); err == nil || !reflect.DeepEqual(out, ExecutionManifestV2{}) {
			t.Fatal("freeze not fail-zero")
		}
		if out, err := VerifyExecutionManifestV2AgainstPlan(c, m, p); err == nil || out.Valid() {
			t.Fatal("verify not fail-zero")
		}
		if out, err := VerifyRenderRequestV2AgainstPlan(c, requestFixtureV2(p, m), p); err == nil || out.Valid() {
			t.Fatal("request not fail-zero")
		}
	}
	oversized := cloneV2(m)
	oversized.Resources = make([]MaterializedResourceV2, MaxCollectionV2+1)
	if _, err := CanonicalExecutionManifestV2(oversized); !errors.Is(err, ErrLimitV2) {
		t.Fatalf("collection limit %v", err)
	}
	oversized = cloneV2(m)
	oversized.Backend.BackendID = strings.Repeat("x", MaxStringBytesV2+1)
	if _, err := FreezeExecutionManifestV2(context.Background(), p, oversized); !errors.Is(err, ErrLimitV2) {
		t.Fatalf("string limit %v", err)
	}
	if _, err := DecodeExecutionManifestV2(bytes.Repeat([]byte(" "), MaxManifestBytesV2+1)); err == nil {
		t.Fatal("raw byte bound")
	}
}

func TestV2ZeroTickAuthoredDomainsAndURIOnly(t *testing.T) {
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-plan-mixed.document.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	original := planFixtureV2(t, "mixed")
	for _, both := range []bool{false, true} {
		target := original.Target
		if both {
			target.SampleClock.SamplesPerSecond = 1
		}
		requested := documentv2.TimeRange{Start: documentv2.RationalTime{Value: 1, Rate: 1000}, Duration: documentv2.RationalTime{Value: 1, Rate: 1000}}
		p, ds := renderplanv2.Resolve(context.Background(), d, requested, target)
		if documentv2.HasErrorDiagnostics(ds) {
			t.Fatal(ds)
		}
		if p.FrameRange.Count != 0 || len(p.Visual) == 0 || len(p.Resources) == 0 {
			t.Fatal("fixture lacks authored zero-tick visual")
		}
		m := manifestFixtureV2(t, p)
		if m.Visual.Enabled || m.Visual.SourceManifest != nil {
			t.Fatal("zero tick visual must be exactly disabled")
		}
		if both && (p.SampleRange.Count != 0 || len(p.Audio) == 0 || m.Audio.Enabled) {
			t.Fatal("both grids zero case")
		}
		if _, err := VerifyExecutionManifestV2(m); err != nil {
			t.Fatal(err)
		}
		if both {
			if _, err := VerifyRenderRequestV2(requestFixtureV2(p, m)); err == nil {
				t.Fatal("job with no ticks accepted")
			}
		}
	}
	// URI-only plans accept a freshly bound nonempty content identity, never empty
	// materialized content, and never substitution for a pinned plan hash.
	for i := range d.Assets {
		if d.Assets[i].Kind != documentv2.AssetKindFont {
			d.Assets[i].ContentHash = ""
		}
	}
	p, ds := renderplanv2.Resolve(context.Background(), d, original.RequestedRange, original.Target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	m := manifestFixtureV2(t, p)
	if _, err := VerifyExecutionManifestV2AgainstPlan(context.Background(), m, p); err != nil {
		t.Fatal(err)
	}
	draft := draftFixtureV2(p)
	draft.Resources[0].ContentHash = ""
	if _, err := FreezeExecutionManifestV2(context.Background(), p, draft); err == nil {
		t.Fatal("empty materialized hash accepted")
	}
}

func TestV2StandaloneRefingerprintedSourceConsistency(t *testing.T) {
	p := planFixtureV2(t, "mixed")
	base := manifestFixtureV2(t, p)
	cases := map[string]func(*ExecutionManifestV2){
		"projection": func(m *ExecutionManifestV2) { m.Visual.SourceManifest.Clips[0].VisibleSourceRange.Duration.Value++ },
		"domain": func(m *ExecutionManifestV2) {
			m.Audio.SourceManifest.Clips[0].Binding.TrackKind = documentv2.TrackKindVideo
		},
		"missing-materialization": func(m *ExecutionManifestV2) {
			id := m.Visual.SourceManifest.Clips[0].Binding.AssetID
			out := []MaterializedResourceV2{}
			for _, r := range m.Resources {
				if r.AssetID != id {
					out = append(out, r)
				}
			}
			m.Resources = out
			m.Visual.SourceManifest.Resources = domainResourcesV2(m.Visual.SourceManifest.Clips, m.Visual.SourceManifest.Transitions, m.Resources)
		},
		"disabled-visual": func(m *ExecutionManifestV2) { m.Visual = ProgramSelectionV2{} },
		"disabled-audio":  func(m *ExecutionManifestV2) { m.Audio = ProgramSelectionV2{} },
		"clip-order": func(m *ExecutionManifestV2) {
			s := m.Visual.SourceManifest
			s.Clips[0], s.Clips[1] = s.Clips[1], s.Clips[0]
		},
		"source-range": func(m *ExecutionManifestV2) { m.Audio.SourceManifest.RequestedRange.Duration.Value++ },
		"cross-domain-clip-id": func(m *ExecutionManifestV2) {
			m.Audio.SourceManifest.Clips[0].Binding.ClipID = m.Visual.SourceManifest.Clips[0].Binding.ClipID
		},
		"cross-domain-track-slot": func(m *ExecutionManifestV2) {
			m.Audio.SourceManifest.Clips[0].Binding.TrackIndex = m.Visual.SourceManifest.Clips[0].Binding.TrackIndex
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := cloneV2(base)
			mutate(&m)
			resignV2(t, &m)
			if v, err := VerifyExecutionManifestV2(m); err == nil || v.Valid() {
				t.Fatal("standalone accepted inferable inconsistency")
			}
		})
	}
	// Freeze must own the caller plan, not just its draft resources/configuration.
	m := manifestFixtureV2(t, p)
	before := m.Visual.SourceManifest.Clips[0].Binding.ClipID
	p.Visual[0].Binding.ClipID = "caller"
	if m.Visual.SourceManifest.Clips[0].Binding.ClipID != before {
		t.Fatal("freeze aliased plan")
	}
	transition := planFixtureV2(t, "transition")
	tm := manifestFixtureV2(t, transition)
	tm.Visual.SourceManifest.Transitions[0].Outgoing.SampledSourceRange.Duration.Value++
	resignV2(t, &tm)
	if _, err := VerifyExecutionManifestV2(tm); err == nil {
		t.Fatal("standalone accepted transition tamper")
	}
}
