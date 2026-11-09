package reconstructv2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/preparev2"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

func mixedDocument(t *testing.T) documentv2.Document {
	d := factoryMixedDocument(factoryFixtureBytes(t))
	picture := &d.Timelines[0].Tracks[1]
	a := picture.Clips[1]
	a.TimelineRange.Duration = factoryTime(1, 2)
	a.SourceRange.Duration = factoryTime(1, 2)
	b := a
	b.ID = "incoming-image"
	b.TimelineRange.Start = factoryTime(1, 2)
	picture.Clips = []documentv2.Clip{picture.Clips[0], a, b}
	d.Timelines[0].Transitions = []documentv2.Transition{{ID: "image-dissolve", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: a.ID, IncomingClipID: b.ID, PreDuration: factoryTime(1, 4), PostDuration: factoryTime(1, 4)}}
	gain := factoryRat(1, 2)
	pan := factoryRat(0, 1)
	ceiling := factoryRat(3, 4)
	d.AudioBuses = []documentv2.AudioBus{{ID: "group", Kind: documentv2.AudioBusGroup, ParentBusID: "master", Properties: documentv2.AudioBusProperties{GainLinear: gain, Pan: pan}}, {ID: "master", Kind: documentv2.AudioBusMaster, Properties: documentv2.AudioBusProperties{GainLinear: gain, Pan: pan, SamplePeakLimiter: &documentv2.AudioSamplePeakLimiterProperty{CeilingLinear: ceiling}}}}
	d.Timelines[0].Tracks[2].AudioBusID = "group"
	return d
}

func mixedHost(t *testing.T, root string, d documentv2.Document) (MixedConfig, *factoryHostSource) {
	return mixedHostWithVerifier(t, root, d, nil)
}
func mixedHostWithVerifier(t *testing.T, root string, d documentv2.Document, verifier preparev2.ResourceVerifier) (MixedConfig, *factoryHostSource) {
	config, source := factoryHost(t, root, d)
	if verifier == nil {
		verifier = source.verifyActual
	}
	visual := referencevisual.DefaultConfigV3(16, 16)
	offered, err := referencevisual.CapabilitiesV3(visual)
	if err != nil {
		t.Fatal(err)
	}
	audio := fixedMixedAudioOffered()
	features := append(append([]videoedit.CapabilityID{}, offered.Features...), audio.Features...)
	unique := map[videoedit.CapabilityID]bool{}
	features2 := []videoedit.CapabilityID{}
	for _, f := range features {
		if !unique[f] {
			unique[f] = true
			features2 = append(features2, f)
		}
	}
	sort.Slice(features2, func(i, j int) bool { return features2[i] < features2[j] })
	authority, err := preparev2.New(context.Background(), preparev2.Config{ProviderExecutable: filepath.Join(root, "measured-provider"), AllowLocalDevelopment: true, CompilerBuild: factoryDigest([]byte("mixed compiler")), EvaluatorBuild: factoryDigest([]byte("mixed evaluator")), Backend: renderapi.BackendCapabilitiesV2{BackendID: "mixed-host-test", BuildFingerprint: factoryDigest([]byte("mixed backend")), TrackKinds: []documentv2.TrackKind{documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector, documentv2.TrackKindVideo}, AssetKinds: []documentv2.AssetKind{documentv2.AssetKindAudio, documentv2.AssetKindFont, documentv2.AssetKindImage, documentv2.AssetKindText, documentv2.AssetKindVector, documentv2.AssetKindVideo}, Features: features2, MaxWidth: 16, MaxHeight: 16}, MediaRuntime: renderapi.MediaRuntime{FFmpegBuild: factoryDigest([]byte("host media build")), DecoderProfile: "host.native.decoder.v1", EncoderProfile: "host.native.encoder.v1", ColorProfile: "host.native.color.v1"}, VerifyResource: verifier})
	if err != nil {
		t.Fatal(err)
	}
	config.Authority = authority
	return DefaultMixedConfig(config), source
}

func mixedRender(t *testing.T, b *MixedBundle, env prepared.EnvironmentV2) factoryRenderIdentity {
	t.Helper()
	ctx := context.Background()
	bridge, err := b.Bridge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := videoedit.CanonicalJSON(bridge)
	if err != nil {
		t.Fatal(err)
	}
	identity := factoryRenderIdentity{Key: b.Key(), Bridge: raw}
	end, _ := env.Plan.FrameRange.EndExclusive()
	for i := end - 1; i >= env.Plan.FrameRange.First; i-- {
		surface, e := b.RenderSurfaceFrame(ctx, i)
		if e != nil {
			t.Fatal(e)
		}
		frame, e := b.RenderFrame(ctx, i)
		if e != nil {
			t.Fatal(e)
		}
		if surface.Result.PixelHash != frame.Result.Visual.PixelHash {
			t.Fatal("surface/frame mismatch")
		}
		identity.SurfaceHashes = append(identity.SurfaceHashes, surface.Result.PixelHash)
		identity.PNGHashes = append(identity.PNGHashes, frame.Result.PNG.ArtifactHash)
	}
	full, e := b.RenderPCM(ctx, env.Plan.SampleRange.First, env.Plan.SampleRange.Count)
	if e != nil {
		t.Fatal(e)
	}
	identity.PCMHash = full.PCMHash
	all, e := full.PCM.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	var split []byte
	for first := env.Plan.SampleRange.First; first < env.Plan.SampleRange.First+env.Plan.SampleRange.Count; first++ {
		r, e := b.RenderPCM(ctx, first, 1)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := r.PCM.Bytes()
		if e != nil {
			t.Fatal(e)
		}
		split = append(split, raw...)
	}
	if !reflect.DeepEqual(all, split) {
		t.Fatal("arbitrary PCM partition drift")
	}
	return identity
}

func TestMixedFactoryActualPNGFontWAVTransitionBusAndRestart(t *testing.T) {
	ctx := context.Background()
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	d := mixedDocument(t)
	config, source := mixedHost(t, root, d)
	env := factoryEnvironment(t, config.Static, source, d)
	f, err := NewMixed(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	source.requireZero(t)
	b, err := f.Reconstruct(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	identity := mixedRender(t, b, env)
	bridge, err := b.Bridge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bridge.Profile == BridgeProfileV1 || bridge.SchemaVersion != 2 || bridge.AudioReceipt.Profile != "cutly.audio.domain-compile.v2" || bridge.VisualManifest.SchemaVersion != 3 || len(bridge.VisualManifest.DomainScope.Scope.Selection.TransitionIDs) != 1 || bridge.Key.DocumentHash != env.Plan.DocumentHash {
		t.Fatal("version/identity drift")
	}
	bridge.VisualManifest.Static.NativePNG.Assets[0].AssetID = "mutated"
	again, err := b.Bridge(ctx)
	if err != nil || again.VisualManifest.Static.NativePNG.Assets[0].AssetID == "mutated" {
		t.Fatal("borrowed bridge")
	}
	if old, e := New(ctx, config.Static); e == nil {
		if result, e := old.Reconstruct(ctx, env); e == nil || result != nil {
			t.Fatal("S3 widened")
		}
	}
	wire, err := prepared.CanonicalEnvironmentV2(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "environment.json")
	if err = os.WriteFile(path, wire, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMixedRestartChild$")
	command.Env = append(os.Environ(), "CUTLY_MIXED_RESTART_ENV="+path, "CUTLY_MIXED_RESTART_ROOT="+root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	var got factoryRenderIdentity
	outputPath := filepath.Join(root, "child-output.json")
	output, err = os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, identity) {
		t.Fatal("fresh process identity differs")
	}
}

func TestMixedRestartChild(t *testing.T) {
	path := os.Getenv("CUTLY_MIXED_RESTART_ENV")
	if path == "" {
		t.Skip("subprocess helper")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env, err := prepared.DecodeEnvironmentV2(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("CUTLY_MIXED_RESTART_ROOT")
	config, _ := mixedHost(t, root, env.Document)
	f, err := NewMixed(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Reconstruct(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got := mixedRender(t, b, env)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "child-output.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMixedFactoryFailureZeroBudgetBeforeIOAndLifecycle(t *testing.T) {
	ctx := context.Background()
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	d := mixedDocument(t)
	config, source := mixedHost(t, root, d)
	env := factoryEnvironment(t, config.Static, source, d)
	f, err := NewMixed(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	f.static.config.Limits.MaxWorkingBytes = 1
	if b, e := f.Reconstruct(ctx, env); e == nil || b != nil {
		t.Fatal("budget failure published handle")
	}
	source.requireZero(t)
	f, err = NewMixed(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if b, e := f.Reconstruct(canceled, env); !errors.Is(e, context.Canceled) || b != nil {
		t.Fatal("cancel failure-zero")
	}
	source.requireZero(t)
	b, err := f.Reconstruct(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := b.RenderSurfaceFrame(ctx, env.Plan.FrameRange.First); e != nil {
				t.Error(e)
			}
			if _, e := b.RenderPCM(ctx, env.Plan.SampleRange.First, 1); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	release, e := b.lease(ctx)
	if e != nil {
		t.Fatal(e)
	}
	closed := make(chan struct{})
	go func() { _ = b.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close did not wait for lifecycle lease")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close hung")
	}
	if b.Valid() || b.Key() != (prepared.KeyV2{}) {
		t.Fatal("closed remains valid")
	}
	if r, e := b.RenderSurfaceFrame(ctx, env.Plan.FrameRange.First); e == nil || !reflect.DeepEqual(r, referencevisual.SurfaceFrameResult{}) {
		t.Fatal("closed render not failure zero")
	}
	if e := b.Close(); e != nil {
		t.Fatal(e)
	}
	var zero *MixedBundle
	if r, e := zero.RenderFrame(ctx, 0); e == nil || !reflect.DeepEqual(r, referencevisual.FrameResultV3{}) {
		t.Fatal("nil handle")
	}
}

func TestMixedScopeExactTransitionClosureAndNoPruning(t *testing.T) {
	ctx := context.Background()
	d := mixedDocument(t)
	target := videoedit.RenderTarget{TimelineID: "timeline", Width: 16, Height: 16, FrameClock: videoedit.FrameClock{Origin: factoryTime(0, 1), FramesPerSecond: factoryTime(8, 1)}, SampleClock: videoedit.SampleClock{Origin: factoryTime(0, 1), SamplesPerSecond: 4}, Channels: 2, PixelFormat: "rgba16", ColorSpace: "linear-srgb"}
	full := documentv2.TimeRange{Start: factoryTime(0, 1), Duration: factoryTime(1, 1)}
	p, ds := renderplanv2.Resolve(ctx, d, full, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	scopes, e := selectMixedScopes(ctx, d, p)
	if e != nil {
		t.Fatal(e)
	}
	if len(scopes.visual.Selection.ClipIDs) != 4 || len(scopes.visual.Selection.TransitionIDs) != 1 || len(scopes.audio.Selection.ClipIDs) != 1 {
		t.Fatal("wrong closure")
	}
	if p.Visual[0].Binding.TrackIndex != 1 || p.Visual[0].Binding.ClipIndex != 1 || p.Audio[0].Binding.TrackIndex != 2 {
		t.Fatal("original indexes changed")
	}
	p.Transitions = append(p.Transitions, p.Transitions[0])
	if result, e := selectMixedScopes(ctx, d, p); e == nil || !reflect.DeepEqual(result, reconstructionScopes{}) {
		t.Fatal("duplicate transition admitted")
	}
	narrow := documentv2.TimeRange{Start: factoryTime(0, 1), Duration: factoryTime(1, 8)}
	p, ds = renderplanv2.Resolve(ctx, d, narrow, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	if result, e := selectMixedScopes(ctx, d, p); e == nil || !reflect.DeepEqual(result, reconstructionScopes{}) {
		t.Fatal("partial document scope silently pruned")
	}
}

func TestMixedFactoryGapsDisabledDomainsAndFaults(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"gap", "visual", "audio"} {
		t.Run(kind, func(t *testing.T) {
			d := factoryMixedDocument(factoryFixtureBytes(t))
			switch kind {
			case "gap":
				d.Assets = nil
				d.Timelines[0].Tracks = nil
			case "visual":
				d.Timelines[0].Tracks[2].Clips = nil
			case "audio":
				d.Timelines[0].Tracks = []documentv2.Track{d.Timelines[0].Tracks[2]}
			}
			root, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			cfg, source := mixedHost(t, root, d)
			env := factoryEnvironment(t, cfg.Static, source, d)
			factory, e := NewMixed(ctx, cfg)
			if e != nil {
				t.Fatal(e)
			}
			bundle, e := factory.Reconstruct(ctx, env)
			if e != nil {
				t.Fatal(e)
			}
			defer bundle.Close()
			bridge, e := bundle.Bridge(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if bridge.SemanticVisual.Enabled != env.Manifest.Visual.Enabled || bridge.SemanticAudio.Enabled != env.Manifest.Audio.Enabled {
				t.Fatal("domain mismatch")
			}
			if !env.Manifest.Visual.Enabled {
				if r, e := bundle.RenderSurfaceFrame(ctx, env.Plan.FrameRange.First); e == nil || !reflect.DeepEqual(r, referencevisual.SurfaceFrameResult{}) {
					t.Fatal("disabled visual rendered")
				}
			}
			if !env.Manifest.Audio.Enabled {
				if _, e := bundle.RenderPCM(ctx, env.Plan.SampleRange.First, 1); e == nil {
					t.Fatal("disabled audio rendered")
				}
			}
		})
	}
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	d := mixedDocument(t)
	cfg, source := mixedHost(t, root, d)
	env := factoryEnvironment(t, cfg.Static, source, d)
	factory, e := NewMixed(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	source.onOpen = func(documentv2.ID) { panic(factoryPanicValue{}) }
	if b, e := factory.Reconstruct(ctx, env); !errors.Is(e, ErrPanic) || b != nil {
		t.Fatal("panic not failure-zero")
	}
	source.onOpen = nil
	source.drift = "image"
	if b, e := factory.Reconstruct(ctx, env); e == nil || b != nil {
		t.Fatal("byte drift accepted")
	}
	source.drift = ""
	source.onOpen = func(documentv2.ID) {}
	ctx2, cancel := context.WithCancel(ctx)
	source.onOpen = func(documentv2.ID) { cancel() }
	if b, e := factory.Reconstruct(ctx2, env); !errors.Is(e, context.Canceled) || b != nil {
		t.Fatal("late cancel not failure-zero")
	}
}
