package reconstructv2

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/preparev2"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

type preflightSource struct{ authorize, open atomic.Int64 }

func (s *preflightSource) Authorize(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
	s.authorize.Add(1)
	return resourceidentityv2.Binding{}, errors.New("must not authorize")
}
func (s *preflightSource) Open(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error) {
	s.open.Add(1)
	return nil, errors.New("must not open")
}
func preflightDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func preflightFixture(t *testing.T, gap bool) (Config, prepared.EnvironmentV2, *preflightSource, *atomic.Int64) {
	t.Helper()
	document, plan := scopeFixture(t)
	if gap {
		document.Timelines[0].Tracks = nil
		plan = resolveScopePlan(t, document, plan.RequestedRange, plan.Target)
	}
	source := &preflightSource{}
	calls := &atomic.Int64{}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "provider")
	if err = os.WriteFile(executable, []byte("host reconstruction preflight executable\n"), 0700); err != nil {
		t.Fatal(err)
	}
	authority, err := preparev2.New(context.Background(), preparev2.Config{ProviderExecutable: executable, AllowLocalDevelopment: true, CompilerBuild: preflightDigest("1"), EvaluatorBuild: preflightDigest("2"), Backend: renderapi.BackendCapabilitiesV2{BackendID: "preflight-host", BuildFingerprint: preflightDigest("3"), TrackKinds: []documentv2.TrackKind{"audio", "text", "vector", "video"}, AssetKinds: []documentv2.AssetKind{"audio", "font", "image", "text", "vector"}, Features: []videoedit.CapabilityID{videoedit.CapabilityAudioVarispeed, documentv2.CapabilityTextPinnedFontViewportPXV2, documentv2.CapabilityVectorPathViewportPXV2, "test.native.unsupported.v1"}, MaxWidth: 4096, MaxHeight: 4096}, MediaRuntime: renderapi.MediaRuntime{FFmpegBuild: preflightDigest("4"), DecoderProfile: "host.decoder.v1", EncoderProfile: "host.encoder.v1", ColorProfile: "host.color.v1"}, VerifyResource: func(_ context.Context, _ string, claim renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		calls.Add(1)
		return claim, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(8, 8, authority, source)
	env := preflightEnvironment(t, authority, document, plan)
	calls.Store(0)
	return config, env, source, calls
}
func preflightEnvironment(t *testing.T, authority *preparev2.Authority, document documentv2.Document, plan renderplanv2.Plan) prepared.EnvironmentV2 {
	t.Helper()
	target := plan.Target
	draft := renderapi.ExecutionManifestV2{Resources: []renderapi.MaterializedResourceV2{}, Output: renderapi.OutputSpecV2{ReferenceProfile: renderapi.OutputReferenceProfileV2, Format: "mp4", Width: uint32(target.Width), Height: uint32(target.Height), PixelFormat: target.PixelFormat, ColorSpace: target.ColorSpace, VideoCodec: "h264", AudioCodec: "aac", FrameClock: target.FrameClock, SampleClock: target.SampleClock, SampleRate: target.SampleClock.SamplesPerSecond, Channels: uint32(target.Channels), Intermediate: renderapi.IntermediateProfilesV2{VideoSurfaceProfile: renderapi.VideoSurfaceProfileV2, AudioBlockProfile: renderapi.AudioBlockProfileV2}, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1}}
	for _, ref := range plan.Resources {
		fingerprint := preflightDigest("5")
		claim := renderapi.MaterializedResourceV2{AssetID: ref.AssetID, Kind: ref.Kind, ContentHash: ref.ContentHash, SizeBytes: 64, LocatorScheme: renderapi.LocatorCAS, Locator: "cas://sha256/" + strings.TrimPrefix(ref.ContentHash, "sha256:"), MaterializationProfile: renderapi.AVMaterializationProfileV2}
		switch ref.Kind {
		case documentv2.AssetKindImage:
			claim.MaterializationProfile = renderapi.ImageMaterializationProfileV2
			claim.InspectionFingerprint = &fingerprint
		case documentv2.AssetKindFont:
			claim.MaterializationProfile = renderapi.FontMaterializationProfileV2
			claim.InspectionFingerprint = &fingerprint
		default:
			claim.ProbeFingerprint = &fingerprint
		}
		draft.Resources = append(draft.Resources, claim)
	}
	manifest, err := authority.Freeze(context.Background(), document, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	env, err := prepared.NewEnvironmentV2(context.Background(), document, plan, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return env
}
func requireNoPreflightIO(t *testing.T, source *preflightSource, calls *atomic.Int64) {
	t.Helper()
	if calls.Load() != 0 || source.authorize.Load() != 0 || source.open.Load() != 0 {
		t.Fatalf("preflight resource callback: authority=%d authorize=%d open=%d", calls.Load(), source.authorize.Load(), source.open.Load())
	}
}

func TestFactoryPreflightVerifiedGapAndMixedNoIO(t *testing.T) {
	for _, gap := range []bool{true, false} {
		t.Run(map[bool]string{true: "gap", false: "mixed"}[gap], func(t *testing.T) {
			config, env, source, calls := preflightFixture(t, gap)
			factory, err := New(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			requireNoPreflightIO(t, source, calls)
			got, err := factory.preflight(context.Background(), env)
			if err != nil || got == nil || !got.verified.Valid() {
				t.Fatalf("preflight: %v", err)
			}
			if got.configHash == "" || got.configHash != factory.configHash || got.environment.Plan.DocumentHash != env.Plan.DocumentHash || got.scopes.visual.RequestedRange != env.Plan.RequestedRange || got.environment.Manifest.Output.FrameClock != env.Plan.Target.FrameClock {
				t.Fatal("admission identity drift")
			}
			if got.charge.workingBytes == 0 || got.charge.retainedBytes == 0 || (!gap && got.charge.resourceBytes != 128) {
				t.Fatal("missing aggregate reservation")
			}
			requireNoPreflightIO(t, source, calls)
		})
	}
}

func TestFactoryConstructorExplicitOwnedConfiguration(t *testing.T) {
	config, _, source, calls := preflightFixture(t, true)
	factory, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	before := factory.configHash
	config.Visual.TextConfig.Limits.MaxBindings = 1
	if factory.config.Visual.TextConfig.Limits.MaxBindings == 1 {
		t.Fatal("TextConfig pointer retained")
	}
	another := factory.config
	another.Visual = ownFactoryConfig(factory.config).Visual
	another.Visual.BuildFingerprint = preflightDigest("6")
	changed, err := New(context.Background(), another)
	if err != nil {
		t.Fatal(err)
	}
	if changed.configHash == before {
		t.Fatal("native host identity omitted config")
	}
	for _, name := range []string{"nil-authority", "nil-source", "typed-nil", "nil-text", "zero-audio", "zero-inspection", "negative", "over-default", "overflow", "aggregate", "zero-wav", "nested-text-minimum"} {
		t.Run(name, func(t *testing.T) {
			c := ownFactoryConfig(factory.config)
			switch name {
			case "nil-authority":
				c.Authority = nil
			case "nil-source":
				c.Resources = nil
			case "typed-nil":
				c.Resources = (*preflightSource)(nil)
			case "nil-text":
				c.Visual.TextConfig = nil
			case "zero-audio":
				c.Audio.MaxBlockSamples = 0
			case "zero-inspection":
				c.FontInspection.MaxWorkingBytes = 0
			case "negative":
				c.Visual.Width = -1
			case "over-default":
				c.PNGInspection.MaxEncodedBytes = pngdecode.HardMaxEncodedBytes
			case "overflow":
				c.Limits.MaxWorkingBytes = ^uint64(0)
			case "aggregate":
				c.Limits.MaxWorkingBytes = 1
			case "nested-text-minimum":
				c.Visual.TextConfig.Limits.FrozenText.MaxWorkingBytes = 1
			case "zero-wav":
				c.WAV.Limits.WAV.MaxEncodedBytes = 0
			}
			p, e := New(context.Background(), c)
			if e == nil || p != nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	requireNoPreflightIO(t, source, calls)
}

func TestFactoryPreflightRejectsBeforeResourceCallbacks(t *testing.T) {
	for _, name := range []string{"tampered", "pins", "dimensions", "stereo", "pixel", "resource-size", "total-resource", "unsupported-subset", "zero-ticks", "unsupported-properties", "audio-retime", "source-count", "wav-document", "sample-rate"} {
		t.Run(name, func(t *testing.T) {
			config, env, source, calls := preflightFixture(t, false)
			switch name {
			case "tampered":
				env.Document.Revision++
			case "pins":
				env.Manifest.CompilerBuild = preflightDigest("8")
				m, e := renderapi.FreezeExecutionManifestV2(context.Background(), env.Plan, env.Manifest)
				if e != nil {
					t.Fatal(e)
				}
				env, e = prepared.NewEnvironmentV2(context.Background(), env.Document, env.Plan, m)
				if e != nil {
					t.Fatal(e)
				}
			case "dimensions":
				config.Visual.Width = 9
			case "stereo":
				target := env.Plan.Target
				target.Channels = 1
				p := resolveScopePlan(t, env.Document, env.Plan.RequestedRange, target)
				env = preflightEnvironment(t, config.Authority, env.Document, p)
			case "pixel":
				target := env.Plan.Target
				target.PixelFormat = "rgba8"
				p := resolveScopePlan(t, env.Document, env.Plan.RequestedRange, target)
				env = preflightEnvironment(t, config.Authority, env.Document, p)
			case "resource-size":
				config.Limits.MaxResourceBytes = 32
			case "total-resource":
				config.Limits.MaxResourceBytes = 64
				config.Limits.MaxTotalResourceBytes = 100
			case "unsupported-subset":
				d := env.Document
				c := d.Timelines[0].Tracks[2].Clips[1]
				c.ID = "later"
				c.TimelineRange.Start.Value = 3
				d.Timelines[0].Tracks[2].Clips = append(d.Timelines[0].Tracks[2].Clips, c)
				p := resolveScopePlan(t, d, env.Plan.RequestedRange, env.Plan.Target)
				env = preflightEnvironment(t, config.Authority, d, p)
			case "audio-retime":
				d := env.Document
				d.Timelines[0].Tracks[1].Clips[0].SourceRange.Duration.Value = 1
				p := resolveScopePlan(t, d, env.Plan.RequestedRange, env.Plan.Target)
				env = preflightEnvironment(t, config.Authority, d, p)
			case "source-count":
				d := env.Document
				track := d.Timelines[0].Tracks[2]
				track.ID = "second-picture"
				track.Clips = append([]documentv2.Clip(nil), track.Clips[1:]...)
				track.Clips[0].ID = "second-picture-clip"
				d.Timelines[0].Tracks = append(d.Timelines[0].Tracks, track)
				p := resolveScopePlan(t, d, env.Plan.RequestedRange, env.Plan.Target)
				env = preflightEnvironment(t, config.Authority, d, p)
				config.Visual.NativeLimits.MaxClipScopes = 1
			case "wav-document":
				config.WAV.Limits.MaxDocumentNodes = 10
			case "sample-rate":
				target := env.Plan.Target
				target.SampleClock.SamplesPerSecond = 384000
				p := resolveScopePlan(t, env.Document, env.Plan.RequestedRange, target)
				env = preflightEnvironment(t, config.Authority, env.Document, p)
			case "zero-ticks":
				env.Plan.FrameRange.Count = 0
			case "unsupported-properties":
				d := env.Document
				d.Timelines[0].Tracks[2].Clips[1].PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{Namespace: "test.native.unsupported.v1", AffectsRendering: true, RequiredCapability: "test.native.unsupported.v1", Value: []byte(`true`)}}}
				p := resolveScopePlan(t, d, env.Plan.RequestedRange, env.Plan.Target)
				env = preflightEnvironment(t, config.Authority, d, p)
			}
			calls.Store(0)
			factory, err := New(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := factory.preflight(context.Background(), env)
			if err == nil || got != nil {
				t.Fatalf("rejected input published: %v", err)
			}
			requireNoPreflightIO(t, source, calls)
		})
	}
}

func TestFactoryPreflightAggregateCheckedArithmeticAndCounts(t *testing.T) {
	config, env, source, calls := preflightFixture(t, false)
	factory, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	base, err := factory.aggregate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"working", "retained", "count", "sum-overflow", "mul-overflow", "unknown-kind", "wav-encoded"} {
		t.Run(name, func(t *testing.T) {
			f := *factory
			f.config = ownFactoryConfig(factory.config)
			e := env
			e.Manifest.Resources = append([]renderapi.MaterializedResourceV2(nil), env.Manifest.Resources...)
			switch name {
			case "working":
				f.config.Limits.MaxWorkingBytes = base.workingBytes - 1
			case "retained":
				f.config.Limits.MaxRetainedBytes = base.retainedBytes - 1
			case "count":
				f.config.Limits.MaxResources = 1
			case "sum-overflow":
				f.config.Visual.PipelineLimits.MaxCompileWorkingBytes = ^uint64(0)
			case "mul-overflow":
				f.config.Audio.MaxBlockSamples = 1 << 62
			case "unknown-kind":
				e.Manifest.Resources[0].Kind = documentv2.AssetKindVideo
			case "wav-encoded":
				f.config.WAV.Limits.MaxTotalEncodedBytes = 1
			}
			if _, err := f.aggregate(context.Background(), e); err == nil {
				t.Fatal("aggregate accepted")
			}
		})
	}
	requireNoPreflightIO(t, source, calls)
}

func TestFactoryPreflightNativeKnownSourceLimitsBeforeIO(t *testing.T) {
	for _, name := range []string{"valid", "audio-bindings", "wav-retained", "wav-raw-bytes", "vector-commands", "vector-edges", "vector-viewport", "text-surfaces", "text-layout-width", "text-raster-pixels", "text-frozen"} {
		t.Run(name, func(t *testing.T) {
			config, base, source, calls := preflightFixture(t, false)
			document := factoryMixedDocument(map[documentv2.ID][]byte{"image": []byte("image"), "audio": []byte("audio"), "font": []byte("font")})
			if name == "audio-bindings" {
				audio := document.Assets[2]
				audio.ID = "audio-second"
				document.Assets = append(document.Assets, audio)
				clip := document.Timelines[0].Tracks[2].Clips[0]
				clip.ID, clip.AssetID = "audio-second-clip", audio.ID
				document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{ID: "second-sound", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{clip}})
				config.Audio.MaxBindings = 1
			}
			plan := resolveScopePlan(t, document, base.Plan.RequestedRange, base.Plan.Target)
			env := preflightEnvironment(t, config.Authority, document, plan)
			switch name {
			case "wav-retained":
				config.WAV.Limits.MaxRetainedBytes = 1
			case "wav-raw-bytes":
				canonical, err := documentv2.CanonicalJSON(document)
				if err != nil {
					t.Fatal(err)
				}
				// Canonical bytes omit optional fields; the source's raw conservative walk
				// must reject this narrower policy even though canonical JSON fits.
				config.WAV.Limits.MaxCanonicalDocumentBytes = uint64(len(canonical))
			case "vector-commands":
				config.Visual.ExpressionLimits.MaxTotalCommands = 1
			case "vector-edges":
				config.Visual.ExpressionLimits.Vector.MaxEdges = 1
			case "vector-viewport":
				config.Visual.ExpressionLimits.Vector.MaxWidth = 1
			case "text-surfaces":
				config.Visual.TextConfig.Limits.MaxTotalStaticSurfaceBytes = 8
			case "text-layout-width":
				config.Visual.TextConfig.Limits.Assembly.Layout.MaxWidth = 1
			case "text-raster-pixels":
				config.Visual.TextConfig.Limits.Raster.Raster.MaxPixels = 1
			case "text-frozen":
				config.Visual.TextConfig.Limits.FrozenText.MaxCanonicalSourceBytes = 1
			}
			calls.Store(0)
			factory, err := New(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := factory.preflight(context.Background(), env)
			if name == "valid" {
				if err != nil || got == nil {
					t.Fatalf("valid mixed admission: %v", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("document-known source limit published admission")
			}
			requireNoPreflightIO(t, source, calls)
		})
	}
}

func TestFactoryPreflightTextScalarAndRasterNecessaryBounds(t *testing.T) {
	const pixels, width = uint64(16 * 16), uint64(16)
	const canonical = 9*pixels + (16 << 10)
	const working = (64 << 10) + 9*pixels + 72*width + 8
	const retained = 8*pixels + (8 << 10)
	const work = 80*pixels + canonical
	for _, name := range []string{"scalar-total", "scalar-line", "scalar-multiline-total", "accept-unicode-lines", "accept-inner-bounds", "inner-canonical", "inner-working", "inner-retained", "inner-work", "outer-working", "outer-retained", "outer-work"} {
		t.Run(name, func(t *testing.T) {
			config, base, source, calls := preflightFixture(t, false)
			document := factoryMixedDocument(map[documentv2.ID][]byte{"image": []byte("image"), "audio": []byte("audio"), "font": []byte("font")})
			limits := &config.Visual.TextConfig.Limits
			switch name {
			case "scalar-total":
				document.Assets[4].Text.Text = "AA"
				limits.Assembly.Shape.MaxScalars = 1
			case "scalar-line":
				document.Assets[4].Text.Text = "AA"
				limits.Assembly.Shape.MaxScalarsPerLine = 1
			case "scalar-multiline-total":
				document.Assets[4].Text.Text = "A\nA"
				limits.Assembly.Shape.MaxScalars = 1
			case "accept-unicode-lines":
				document.Assets[4].Text.Text = "é\nA"
				limits.Assembly.Shape.MaxScalars = 2
				limits.Assembly.Shape.MaxScalarsPerLine = 1
			case "accept-inner-bounds":
				limits.Raster.Raster.MaxCanonicalBinaryBytes = canonical
				limits.Raster.Raster.MaxWorkingBytes = working
				limits.Raster.Raster.MaxRetainedBytes = retained
				limits.Raster.Raster.MaxWorkUnits = work
			case "inner-canonical":
				limits.Raster.Raster.MaxCanonicalBinaryBytes = canonical - 1
			case "inner-working":
				limits.Raster.Raster.MaxWorkingBytes = working - 1
			case "inner-retained":
				limits.Raster.Raster.MaxRetainedBytes = retained - 1
			case "inner-work":
				limits.Raster.Raster.MaxWorkUnits = work - 1
			case "outer-working":
				limits.Raster.MaxWorkingBytes = working - 1
			case "outer-retained":
				limits.Raster.MaxRetainedBytes = retained - 1
			case "outer-work":
				limits.Raster.MaxWorkUnits = work - 1
			}
			plan := resolveScopePlan(t, document, base.Plan.RequestedRange, base.Plan.Target)
			env := preflightEnvironment(t, config.Authority, document, plan)
			calls.Store(0)
			factory, err := New(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := factory.preflight(context.Background(), env)
			if strings.HasPrefix(name, "accept-") {
				// Necessary no-IO bounds are not sufficient for later font-dependent geometry.
				if err != nil || got == nil {
					t.Fatalf("necessary bound rejected: %v", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("known impossible text policy admitted")
			}
			requireNoPreflightIO(t, source, calls)
		})
	}
}

type preflightPanicContext struct{ context.Context }

func (preflightPanicContext) Err() error { panic(preflightBadString{}) }

type preflightBadString struct{}

func (preflightBadString) String() string { panic("must not format panic") }

func TestFactoryPreflightContextZeroPanicAndConcurrency(t *testing.T) {
	config, env, source, calls := preflightFixture(t, true)
	factory, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, preflightPanicContext{context.Background()}} {
		if p, e := factory.preflight(ctx, env); p != nil || e == nil {
			t.Fatal("context preflight did not fail zero")
		}
		if p, e := New(ctx, config); p != nil || e == nil {
			t.Fatal("context constructor did not fail zero")
		}
	}
	for _, f := range []*Factory{nil, {}} {
		if p, e := f.preflight(context.Background(), env); p != nil || e == nil {
			t.Fatal("zero factory")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := factory.preflight(context.Background(), env)
			if e != nil {
				t.Error(e)
				return
			}
			got.environment.Document.ID = "changed"
			got.scopes.visual.Selection.ClipIDs = append(got.scopes.visual.Selection.ClipIDs, "local")
		}()
	}
	wg.Wait()
	if string(env.Document.ID) != "scope-document" {
		t.Fatal("input alias")
	}
	requireNoPreflightIO(t, source, calls)
}
