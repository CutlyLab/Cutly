package ffmpeghost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestPrepareManifestBuildsHostOwnedFrozenManifest(t *testing.T) {
	fixture := newResolverFixture(t)
	request := prepareRequest(fixture.plan)
	request.DeterministicSeed = 73
	planBefore, _ := json.Marshal(request.Plan)
	probeBefore, _ := json.Marshal(fixture.probe)

	manifest, err := fixture.resolver.PrepareManifest(context.Background(), testProjectID, request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := renderapi.DecodeExecutionManifest(raw)
	if err != nil || decoded.Fingerprint != manifest.Fingerprint {
		t.Fatalf("prepared manifest is not canonically frozen: %v", err)
	}
	wantProbeFingerprint, err := probe.Fingerprint(fixture.probe)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.PlanFingerprint != fixture.plan.Fingerprint || manifest.CompilerBuild != testDigest('c') ||
		manifest.EvaluatorBuild != testDigest('d') || manifest.DeterministicSeed != 73 ||
		!reflect.DeepEqual(manifest.Backend, videoedit.CanonicalBackendCapabilities(fixture.backend.capabilities)) ||
		manifest.MediaRuntime.FFmpegBuild != testDigest('e') || manifest.MediaRuntime.DecoderProfile != hardcutDecoderProfile ||
		manifest.MediaRuntime.EncoderProfile != hardcutEncoderProfile || manifest.MediaRuntime.ColorProfile != hardcutColorProfile {
		t.Fatalf("host identities were not frozen: %+v", manifest)
	}
	if manifest.Output.ReferenceProfile != hardcutReferenceProfile || manifest.Output.Format != "mp4" ||
		manifest.Output.VideoCodec != "h264" || manifest.Output.AudioCodec != "aac" ||
		manifest.Output.Width != fixture.plan.Target.Width || manifest.Output.Height != fixture.plan.Target.Height ||
		manifest.Output.SampleRate != fixture.plan.Target.SampleClock.SamplesPerSecond ||
		manifest.Output.Channels != fixture.plan.Target.Channels ||
		manifest.Output.DecodedVideoProfile != renderapi.DecodedVideoProfileRGBA8V1 ||
		manifest.Output.DecodedAudioProfile != renderapi.DecodedAudioProfilePCM16LEV1 {
		t.Fatalf("output was not derived from the fixed hard-cut profile: %+v", manifest.Output)
	}
	if len(manifest.Resources) != 1 || manifest.Resources[0].AssetID != "audio" ||
		manifest.Resources[0].LocatorScheme != renderapi.LocatorFile ||
		manifest.Resources[0].Locator != "/allowed/audio.wav" ||
		manifest.Resources[0].ContentHash != testDigest('a') || manifest.Resources[0].SizeBytes != 10 ||
		manifest.Resources[0].ProbeFingerprint != wantProbeFingerprint {
		t.Fatalf("materialized resources = %+v", manifest.Resources)
	}
	requests := fixture.prober.requestsSnapshot()
	if len(requests) != 1 || requests[0].AssetID != "audio" || requests[0].Path != "/allowed/audio.wav" ||
		requests[0].ExpectedContentHash != testDigest('a') {
		t.Fatalf("fresh probe requests = %+v", requests)
	}
	if len(fixture.registry.manifestsSnapshot()) != 0 {
		t.Fatal("prepare registered evidence reserved for ResolveBackend")
	}
	planAfter, _ := json.Marshal(request.Plan)
	probeAfter, _ := json.Marshal(fixture.probe)
	if !reflect.DeepEqual(planBefore, planAfter) || !reflect.DeepEqual(probeBefore, probeAfter) {
		t.Fatal("prepare mutated caller or callback-owned values")
	}
}

func TestPrepareManifestRequiresExactProjectAndResourceMapping(t *testing.T) {
	tests := []struct {
		name      string
		projectID string
		resources []PrepareResource
	}{
		{name: "wrong project", projectID: "other", resources: prepareRequestResources()},
		{name: "missing", projectID: testProjectID, resources: []PrepareResource{}},
		{name: "duplicate", projectID: testProjectID, resources: []PrepareResource{{AssetID: "audio", Locator: "/allowed/audio.wav"}, {AssetID: "audio", Locator: "/allowed/other.wav"}}},
		{name: "unknown", projectID: testProjectID, resources: []PrepareResource{{AssetID: "other", Locator: "/allowed/audio.wav"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			_, err := fixture.resolver.PrepareManifest(context.Background(), test.projectID, PrepareRequest{
				Plan: fixture.plan, Resources: test.resources,
			})
			assertUnavailable(t, err)
			if fixture.backend.capabilityCalls != 0 || len(fixture.prober.requestsSnapshot()) != 0 {
				t.Fatal("invalid scope/mapping reached host observation callbacks")
			}
		})
	}
}

func TestPrepareManifestRequiresCanonicalPlannedContentHash(t *testing.T) {
	fixture := newResolverFixture(t)
	plan := prepareRenderPlan(t, "", "yuv420p", "bt709", 2)
	_, err := fixture.resolver.PrepareManifest(context.Background(), testProjectID, PrepareRequest{
		Plan: plan, Resources: prepareRequestResources(),
	})
	assertUnavailable(t, err)
	if fixture.backend.capabilityCalls != 0 || len(fixture.prober.requestsSnapshot()) != 0 {
		t.Fatal("hashless plan reached host observation callbacks")
	}
}

func TestPrepareManifestRejectsUnauthorizedOrNonFileLocatorWithoutDisclosure(t *testing.T) {
	tests := []struct {
		name    string
		locator string
		setErr  bool
	}{
		{name: "relative", locator: "private/audio.wav"},
		{name: "file URL", locator: "file:///private/audio.wav"},
		{name: "unclean", locator: "/allowed/../private/audio.wav"},
		{name: "unauthorized", locator: "/private/secret.wav", setErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			if test.setErr {
				fixture.backend.authorizeErr = errors.New("secret authorization detail: " + test.locator)
			}
			_, err := fixture.resolver.PrepareManifest(context.Background(), testProjectID, PrepareRequest{
				Plan: fixture.plan, Resources: []PrepareResource{{AssetID: "audio", Locator: test.locator}},
			})
			assertUnavailable(t, err)
			if strings.Contains(err.Error(), test.locator) || strings.Contains(err.Error(), "secret authorization") {
				t.Fatalf("sensitive locator detail leaked: %v", err)
			}
			if len(fixture.prober.requestsSnapshot()) != 0 {
				t.Fatal("rejected locator reached fresh probe")
			}
		})
	}
}

func TestPrepareManifestRejectsFreshProbeIdentityKindAndDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverFixture)
	}{
		{name: "callback error", mutate: func(f *resolverFixture) { f.prober.err = errors.New("private probe path") }},
		{name: "error diagnostic", mutate: func(f *resolverFixture) {
			f.prober.diagnostics = []probe.Diagnostic{{Code: "TEST", Severity: probe.SeverityError, Message: "private diagnostic"}}
		}},
		{name: "asset", mutate: func(f *resolverFixture) { f.prober.manifest.Resource.AssetID = "other" }},
		{name: "hash", mutate: func(f *resolverFixture) { f.prober.manifest.Resource.ContentHash = testDigest('f') }},
		{name: "size", mutate: func(f *resolverFixture) { f.prober.manifest.Resource.SizeBytes = 0 }},
		{name: "kind", mutate: func(f *resolverFixture) { f.prober.manifest = videoProbeManifest() }},
		{name: "invalid probe", mutate: func(f *resolverFixture) { f.prober.manifest.Streams = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			test.mutate(fixture)
			_, err := fixture.resolver.PrepareManifest(context.Background(), testProjectID, prepareRequest(fixture.plan))
			assertUnavailable(t, err)
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("probe detail leaked: %v", err)
			}
		})
	}
}

func TestPrepareManifestRejectsLiveRuntimeFailuresAndPanics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverFixture)
	}{
		{name: "capabilities error", mutate: func(f *resolverFixture) { f.backend.capabilitiesErr = errors.New("private caps") }},
		{name: "capabilities panic", mutate: func(f *resolverFixture) { f.backend.panicAt = "capabilities" }},
		{name: "backend ID panic", mutate: func(f *resolverFixture) { f.backend.panicAt = "id" }},
		{name: "backend ID mismatch", mutate: func(f *resolverFixture) { f.backend.id = "other" }},
		{name: "runtime error", mutate: func(f *resolverFixture) { f.backend.runtimeErr = errors.New("private runtime") }},
		{name: "runtime panic", mutate: func(f *resolverFixture) { f.backend.panicAt = "runtime" }},
		{name: "runtime invalid", mutate: func(f *resolverFixture) { f.backend.runtime = "latest" }},
		{name: "authorize panic", mutate: func(f *resolverFixture) { f.backend.panicAt = "authorize" }},
		{name: "probe panic", mutate: func(f *resolverFixture) { f.prober.panic = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			test.mutate(fixture)
			_, err := fixture.resolver.PrepareManifest(context.Background(), testProjectID, prepareRequest(fixture.plan))
			assertUnavailable(t, err)
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret panic") {
				t.Fatalf("host callback detail leaked: %v", err)
			}
		})
	}
}

func TestPrepareManifestDerivesOnlySupportedHardcutOutput(t *testing.T) {
	tests := []struct {
		name     string
		pixel    string
		color    string
		channels int
	}{
		{name: "pixel", pixel: "rgba", color: "bt709", channels: 2},
		{name: "color", pixel: "yuv420p", color: "display-p3", channels: 2},
		{name: "channels", pixel: "yuv420p", color: "bt709", channels: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			plan := prepareRenderPlan(t, testDigest('a'), test.pixel, test.color, test.channels)
			_, err := fixture.resolver.PrepareManifest(context.Background(), testProjectID, PrepareRequest{
				Plan: plan, Resources: prepareRequestResources(),
			})
			assertUnavailable(t, err)
			if fixture.backend.capabilityCalls != 0 || len(fixture.prober.requestsSnapshot()) != 0 {
				t.Fatal("unsupported output target reached host callbacks")
			}
		})
	}
}

func TestPrepareManifestPreservesCancellation(t *testing.T) {
	fixture := newResolverFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.resolver.PrepareManifest(canceled, testProjectID, prepareRequest(fixture.plan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}
	if fixture.backend.capabilityCalls != 0 {
		t.Fatal("pre-canceled prepare touched backend")
	}

	fixture = newResolverFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.prober.onProbe = cancel
	fixture.prober.err = errors.New("callback cancellation")
	if _, err := fixture.resolver.PrepareManifest(ctx, testProjectID, prepareRequest(fixture.plan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight canceled error = %v", err)
	}
}

func TestPrepareManifestNilResolverFailsClosed(t *testing.T) {
	var resolver *Resolver
	_, err := resolver.PrepareManifest(context.Background(), testProjectID, PrepareRequest{})
	assertUnavailable(t, err)
}

func prepareRequest(plan videoedit.RenderPlan) PrepareRequest {
	return PrepareRequest{Plan: plan, Resources: prepareRequestResources()}
}

func prepareRequestResources() []PrepareResource {
	return []PrepareResource{{AssetID: "audio", Locator: "/allowed/audio.wav"}}
}

func prepareRenderPlan(t *testing.T, contentHash, pixelFormat, colorSpace string, channels int) videoedit.RenderPlan {
	t.Helper()
	one := videoedit.RationalTime{Value: 1, Rate: 1}
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{{
			ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.wav", ContentHash: contentHash, Duration: &one,
		}},
		Timelines: []videoedit.Timeline{{ID: "timeline", Tracks: []videoedit.Track{{
			ID: "audio-track", Kind: videoedit.TrackKindAudio,
			Clips: []videoedit.Clip{{
				ID: "audio-clip", AssetID: "audio",
				TimelineRange: videoedit.TimeRange{Start: zero, Duration: one},
				SourceRange:   videoedit.TimeRange{Start: zero, Duration: one},
			}},
		}}}},
	}
	plan, diagnostics := videoedit.Resolve(document, videoedit.TimeRange{Start: zero, Duration: one}, videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock: videoedit.FrameClock{
			Origin: zero, FramesPerSecond: videoedit.RationalTime{Value: 30, Rate: 1},
		},
		SampleClock: videoedit.SampleClock{Origin: zero, SamplesPerSecond: 48000},
		Channels:    channels, PixelFormat: pixelFormat, ColorSpace: colorSpace,
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("plan diagnostics = %+v", diagnostics)
	}
	return plan
}

func videoProbeManifest() probe.ProbeManifest {
	durationPTS := int64(30)
	average := videoedit.RationalTime{Value: 30, Rate: 1}
	return probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource: probe.ResourceIdentity{
			AssetID: "audio", ContentHash: testDigest('a'), SizeBytes: 10,
		},
		Probe: probe.ProbeIdentity{
			Implementation: "ffprobe-json-v1", BuildFingerprint: testDigest('1'), OptionsFingerprint: testDigest('2'),
		},
		Container: probe.ContainerInfo{
			FormatName: "mov,mp4,m4a,3gp,3g2,mj2", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true,
		},
		Duration: videoedit.RationalTime{Value: 1, Rate: 1},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "h264",
			TimeBase: videoedit.RationalTime{Value: 1, Rate: 30}, DurationPTS: &durationPTS,
			Video: &probe.VideoStream{
				Width: 64, Height: 64, PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateCFR,
				AverageFrameRate: &average, Color: probe.ColorMetadata{Range: probe.ColorRangeLimited},
			},
		}},
	}
}
