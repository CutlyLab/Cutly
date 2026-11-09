package reconstructv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

func bridgeGapPrograms(t *testing.T, env prepared.EnvironmentV2, config Config) (*referencevisual.Program, *audiograph.DomainProgram, *wavsource.Provider) {
	t.Helper()
	ctx := context.Background()
	scopes, err := selectScopes(ctx, env.Document, env.Plan)
	if err != nil {
		t.Fatal(err)
	}
	visual, ds, err := referencevisual.CompileDomainV2(ctx, env.Document, scopes.visual, nil, nil, config.Visual)
	if err != nil {
		t.Fatalf("visual %+v %v", ds, err)
	}
	wav, err := wavsource.Compile(ctx, env.Document, nil, config.WAV)
	if err != nil {
		t.Fatal(err)
	}
	audio, ds := audiograph.CompileDomain(ctx, env.Document, scopes.audio, audiograph.CompileOptions{Offered: fixedAudioOffered(), Bindings: wav.MediaBindings(), Limits: config.Audio})
	if audio == nil || documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("audio %+v", ds)
	}
	return visual, audio, wav
}

func TestBridgeGapNativeSemanticIdentityAndOwnedWire(t *testing.T) {
	config, env, source, calls := preflightFixture(t, true)
	visual, audio, wav := bridgeGapPrograms(t, env, config)
	ctx := context.Background()
	bridge, err := buildBridge(ctx, env, preflightDigest("8"), visual, audio, wav, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if bridge.Profile != BridgeProfileV1 || bridge.DocumentHash != env.Plan.DocumentHash || bridge.PlanFingerprint != env.Plan.Fingerprint || bridge.ExecutionManifestFingerprint != env.Manifest.Fingerprint || bridge.CompilerBuild != env.Manifest.CompilerBuild || bridge.EvaluatorBuild != env.Manifest.EvaluatorBuild || bridge.ProviderBuild != env.Manifest.ProviderBuild || bridge.Output != env.Manifest.Output || bridge.DeterministicSeed != env.Manifest.DeterministicSeed {
		t.Fatal("missing original identity or host pins")
	}
	if !bridge.NativeVisual.Enabled || !bridge.NativeAudio.Enabled || !bridge.SemanticVisual.Enabled || !bridge.SemanticAudio.Enabled || bridge.NativeVisual.SourceManifest.DomainScope == nil || bridge.NativeAudio.Receipt.DocumentHash != bridge.DocumentHash || bridge.NativeAudio.WAVManifest.DocumentHash != bridge.DocumentHash {
		t.Fatal("gap is enabled domain, not disabled selection")
	}
	if bridge.NativeVisual.ConfigHash == *env.Manifest.Visual.ConfigurationFingerprint || bridge.NativeAudio.Receipt.ConfigHash == *env.Manifest.Audio.ConfigurationFingerprint {
		t.Fatal("semantic and native configs falsely equated")
	}
	encoded, err := canonicalBridge(ctx, bridge)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte(`"Selection"`), []byte(`"ClipIDs"`), []byte(`"RequestedRange"`)} {
		if bytes.Contains(encoded, bad) {
			t.Fatalf("untagged evaluator scope escaped: %s", bad)
		}
	}
	for _, want := range [][]byte{[]byte(`"semantic_visual"`), []byte(`"native_visual"`), []byte(`"requested_range"`), []byte(`"compiler_build"`), []byte(`"receipt_projection_hash"`)} {
		if !bytes.Contains(encoded, want) {
			t.Fatalf("missing %s", want)
		}
	}
	// The fingerprint member is absent, not an empty string, in the hash preimage.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "fingerprint")
	digest, err := bridgeDigest(ctx, raw)
	if err != nil || digest != bridge.Fingerprint {
		t.Fatalf("self fingerprint not omitted: %s %s %v", digest, bridge.Fingerprint, err)
	}
	if got, err := bridgeDigest(ctx, bridge.NativeAudio.Receipt); err != nil || got != bridge.NativeAudio.ReceiptProjectionHash {
		t.Fatal("audio projection identity drift")
	}
	if bridge.NativeAudio.Receipt.OriginalReceiptHash != audio.Receipt().ReceiptHash {
		t.Fatal("native receipt identity lost")
	}
	// Deep-frozen result never retains caller manifest/config pointer or RawMessage headers.
	*env.Manifest.Visual.Profile = "caller-mutation"
	env.Manifest.Visual.Configuration[0] = 'x'
	env.Manifest.Backend.TrackKinds[0] = "caller-mutation"
	config.Visual.TextConfig.Limits.MaxBindings = 1
	again, err := canonicalBridge(ctx, bridge)
	if err != nil || !bytes.Equal(encoded, again) {
		t.Fatalf("input mutation reached bridge %v", err)
	}
	var owned Bridge
	if err := json.Unmarshal(encoded, &owned); err != nil {
		t.Fatal(err)
	}
	owned.NativeVisual.Config.TextConfig.Limits.MaxBindings = 1
	*owned.SemanticVisual.Profile = "getter-mutation"
	owned.NativeAudio.Receipt.Offered.TrackKinds[0] = "getter-mutation"
	if again, err := canonicalBridge(ctx, bridge); err != nil || !bytes.Equal(encoded, again) {
		t.Fatal("owned getter changed receipt")
	}
	requireNoPreflightIO(t, source, calls)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				if b, e := canonicalBridge(ctx, bridge); e != nil || !bytes.Equal(b, encoded) {
					t.Errorf("concurrent immutable bridge: %v", e)
				}
			}
		}()
	}
	wg.Wait()
}

func TestBridgeActualWAVSourceEvidence(t *testing.T) {
	ctx := context.Background()
	config, env, source, calls := preflightFixture(t, true)
	encoded := factoryFixtureBytes(t)["audio"]
	span := documentv2.TimeRange{Start: documentv2.RationalTime{Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}}
	doc := env.Document
	doc.Assets = []documentv2.Asset{{ID: "actual-wav", Kind: documentv2.AssetKindAudio, ContentHash: factoryDigest(encoded), Duration: &span.Duration}}
	doc.Timelines[0].Tracks = []documentv2.Track{{ID: "actual-audio", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "actual-clip", AssetID: "actual-wav", TimelineRange: span, SourceRange: span}}}}
	target := env.Plan.Target
	target.SampleClock.SamplesPerSecond = 4
	plan := resolveScopePlan(t, doc, span, target)
	env = preflightEnvironment(t, config.Authority, doc, plan)
	calls.Store(0)
	scopes, err := selectScopes(ctx, doc, plan)
	if err != nil {
		t.Fatal(err)
	}
	visual, ds, err := referencevisual.CompileDomainV2(ctx, doc, scopes.visual, nil, nil, config.Visual)
	if err != nil {
		t.Fatalf("visual %+v %v", ds, err)
	}
	wav, err := wavsource.Compile(ctx, doc, []wavsource.Binding{{DocumentHash: plan.DocumentHash, AssetID: "actual-wav", EncodedWAV: encoded}}, config.WAV)
	if err != nil {
		t.Fatal(err)
	}
	audio, ds := audiograph.CompileDomain(ctx, doc, scopes.audio, audiograph.CompileOptions{Offered: fixedAudioOffered(), Bindings: wav.MediaBindings(), Limits: config.Audio})
	if audio == nil {
		t.Fatal(ds)
	}
	b, err := buildBridge(ctx, env, preflightDigest("8"), visual, audio, wav, config.PCM)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.NativeAudio.Receipt.Sources) != 1 || len(b.NativeAudio.WAVManifest.Assets) != 1 || len(b.ValidatedResources) != 1 {
		t.Fatal("actual source evidence missing")
	}
	a, w := b.NativeAudio.Receipt.Sources[0], b.NativeAudio.WAVManifest.Assets[0]
	if a.AssetID != "actual-wav" || a.ResourceContentHash != factoryDigest(encoded) || a.ManifestFingerprint != w.ManifestFingerprint || a.SampleRate != int64(w.SampleRate) || !reflect.DeepEqual(a.DecoderChannels, w.DecoderChannels) {
		t.Fatal("actual decode identity not cross-bound")
	}
	raw, err := canonicalBridge(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	*env.Manifest.Resources[0].ProbeFingerprint = preflightDigest("9")
	if again, err := canonicalBridge(ctx, b); err != nil || !bytes.Equal(raw, again) {
		t.Fatal("resource pointer retained")
	}
	b.NativeAudio.Receipt.Sources[0].DecoderChannels[0] = "caller-mutation"
	var copy Bridge
	if err = json.Unmarshal(raw, &copy); err != nil {
		t.Fatal(err)
	}
	if copy.NativeAudio.Receipt.Sources[0].DecoderChannels[0] == "caller-mutation" || *copy.ValidatedResources[0].ProbeFingerprint == preflightDigest("9") {
		t.Fatal("source evidence not owned")
	}
	requireNoPreflightIO(t, source, calls)
}

func TestBridgeRejectsWrongNativeDocumentScopeAndTampering(t *testing.T) {
	ctx := context.Background()
	config, env, _, _ := preflightFixture(t, true)
	v, a, w := bridgeGapPrograms(t, env, config)
	base, err := buildBridge(ctx, env, preflightDigest("8"), v, a, w, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"document", "scope", "audio-document", "audio-scope", "wav-document", "nil-visual", "nil-audio", "nil-wav", "config-hash", "pcm"} {
		t.Run(mode, func(t *testing.T) {
			vv, aa, ww := v, a, w
			cc := pcm16le.DefaultLimits()
			hash := preflightDigest("8")
			if mode == "document" || mode == "scope" {
				other := env.Document
				if mode == "document" {
					other.ID = "different-document"
				}
				scopes, e := selectScopes(ctx, env.Document, env.Plan)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "scope" {
					scopes.visual.RequestedRange.Duration.Value++
				}
				vv, _, e = referencevisual.CompileDomainV2(ctx, other, scopes.visual, nil, nil, config.Visual)
				if e != nil {
					t.Fatal(e)
				}
			}
			if mode == "audio-document" || mode == "audio-scope" || mode == "wav-document" {
				other := env.Document
				if mode != "audio-scope" {
					other.ID = "other-native-document"
				}
				otherWAV, e := wavsource.Compile(ctx, other, nil, config.WAV)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "wav-document" {
					ww = otherWAV
				} else {
					scopes, e := selectScopes(ctx, env.Document, env.Plan)
					if e != nil {
						t.Fatal(e)
					}
					if mode == "audio-scope" {
						scopes.audio.RequestedRange.Duration.Value++
					}
					var ds []documentv2.Diagnostic
					aa, ds = audiograph.CompileDomain(ctx, other, scopes.audio, audiograph.CompileOptions{Offered: fixedAudioOffered(), Bindings: otherWAV.MediaBindings(), Limits: config.Audio})
					if aa == nil {
						t.Fatal(ds)
					}
				}
			}
			switch mode {
			case "nil-visual":
				vv = nil
			case "nil-audio":
				aa = nil
			case "nil-wav":
				ww = nil
			case "config-hash":
				hash = "not a digest"
			case "pcm":
				cc.MaxFrames = 0
			}
			if got, err := buildBridge(ctx, env, hash, vv, aa, ww, cc); err == nil || !reflect.DeepEqual(got, Bridge{}) {
				t.Fatalf("wrong native identity accepted %+v %v", got, err)
			}
		})
	}
	for _, mode := range []string{"seed", "semantic", "native", "resources", "fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			encoded, _ := canonicalBridge(ctx, base)
			var changed Bridge
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "seed":
				changed.DeterministicSeed++
			case "semantic":
				*changed.SemanticAudio.ConfigurationFingerprint = preflightDigest("9")
			case "native":
				changed.NativeVisual.Config.Width++
			case "resources":
				changed.ValidatedResources = append(changed.ValidatedResources, env.Manifest.Resources...)
				changed.EvidenceSemantics = "forged"
			case "fingerprint":
				changed.Fingerprint = preflightDigest("9")
			}
			if b, err := canonicalBridge(ctx, changed); err == nil || b != nil {
				t.Fatal("tampered bridge accepted")
			}
		})
	}
}

type bridgePanicContext struct{ context.Context }

func (bridgePanicContext) Err() error { panic(struct{}{}) }

func TestBridgeExplicitDisabledDomainAndCancellation(t *testing.T) {
	config, env, _, _ := preflightFixture(t, true)
	// Tiny positive range between ticks disables the semantic domains, unlike an enabled gap.
	span := documentv2.TimeRange{Start: documentv2.RationalTime{Value: 1, Rate: 96000}, Duration: documentv2.RationalTime{Value: 1, Rate: 192000}}
	plan := resolveScopePlan(t, env.Document, span, env.Plan.Target)
	env = preflightEnvironment(t, config.Authority, env.Document, plan)
	v, a, w := bridgeGapPrograms(t, env, config)
	ctx := context.Background()
	b, err := buildBridge(ctx, env, preflightDigest("8"), v, a, w, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if b.SemanticVisual.Enabled || b.SemanticAudio.Enabled || b.NativeVisual.Enabled || b.NativeAudio.Enabled || b.SemanticVisual.Profile != nil || b.SemanticAudio.Profile != nil {
		t.Fatalf("disabled domain broadened %+v", b)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := buildBridge(canceled, env, preflightDigest("8"), v, a, w, pcm16le.DefaultLimits()); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Bridge{}) {
		t.Fatalf("cancel %+v %v", got, err)
	}
	if got, err := canonicalBridge(canceled, b); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal("cancellation published bytes")
	}
	if got, err := buildBridge(nil, env, preflightDigest("8"), v, a, w, pcm16le.DefaultLimits()); !errors.Is(err, ErrContext) || !reflect.DeepEqual(got, Bridge{}) {
		t.Fatal("nil context")
	}
	panicCtx := bridgePanicContext{context.Background()}
	if got, err := buildBridge(panicCtx, env, preflightDigest("8"), v, a, w, pcm16le.DefaultLimits()); !errors.Is(err, ErrPanic) || !reflect.DeepEqual(got, Bridge{}) {
		t.Fatalf("panic published bridge %+v %v", got, err)
	}
	if got, err := canonicalBridge(panicCtx, b); !errors.Is(err, ErrPanic) || got != nil {
		t.Fatal("panic published bytes")
	}
}
