package ffmpeghost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/agent/renderexec"
	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/proberegistry"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/ffmpegbackend"
)

const testProjectID = " project "

var (
	_ Backend       = (*ffmpegbackend.Backend)(nil)
	_ FreshProber   = (*cutlyffprobe.Adapter)(nil)
	_ ProbeRegistry = (*proberegistry.Store)(nil)
)

type resolverFixture struct {
	plan     videoedit.RenderPlan
	manifest renderapi.ExecutionManifest
	probe    probe.ProbeManifest
	backend  *fakeBackend
	prober   *fakeProber
	registry *fakeRegistry
	resolver *Resolver
}

func newResolverFixture(t *testing.T) *resolverFixture {
	t.Helper()
	plan := testRenderPlan(t)
	observed := testProbeManifest()
	probeFingerprint, err := probe.Fingerprint(observed)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := videoedit.BackendCapabilities{
		BackendID: "cutly.ffmpeg-hardcut.v1", BuildFingerprint: testDigest('b'),
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindAudio},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindAudio},
		MaxWidth:   8192, MaxHeight: 8192,
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: testDigest('c'), EvaluatorBuild: testDigest('d'),
		Resources: []renderapi.MaterializedResource{{
			AssetID: "audio", Kind: videoedit.AssetKindAudio, ContentHash: testDigest('a'), SizeBytes: 10,
			LocatorScheme: renderapi.LocatorFile, Locator: "/allowed/audio.wav", ProbeFingerprint: probeFingerprint,
		}},
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: testDigest('e'), DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-mp4.v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.hardcut.bt709.v1", Format: "mp4",
			Width: plan.Target.Width, Height: plan.Target.Height, PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac", SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{id: capabilities.BackendID, capabilities: capabilities, runtime: testDigest('e')}
	prober := &fakeProber{manifest: observed}
	registry := &fakeRegistry{}
	resolver, err := New(Config{
		ProjectID: testProjectID, Backend: backend, Prober: prober, Registry: registry,
		ExpectedCompilerBuild: testDigest('c'), ExpectedEvaluatorBuild: testDigest('d'),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &resolverFixture{plan: plan, manifest: manifest, probe: observed, backend: backend, prober: prober, registry: registry, resolver: resolver}
}

func TestResolverFreshlyProbesAndRegistersBeforeReturningBackend(t *testing.T) {
	fixture := newResolverFixture(t)
	backend, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if backend != fixture.backend {
		t.Fatalf("backend = %T, want fixture backend", backend)
	}
	requests := fixture.prober.requestsSnapshot()
	if len(requests) != 1 || requests[0].AssetID != "audio" || requests[0].Path != "/allowed/audio.wav" || requests[0].ExpectedContentHash != testDigest('a') {
		t.Fatalf("probe requests = %+v", requests)
	}
	stored := fixture.registry.manifestsSnapshot()
	if len(stored) != 1 || !equalProbeManifests(stored[0], fixture.probe) {
		t.Fatalf("registered manifests = %+v", stored)
	}
}

func TestResolverRejectsScopeAndFrozenRuntimeMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverFixture)
		callID string
	}{
		{name: "wrong project", callID: "project"},
		{name: "compiler", mutate: func(f *resolverFixture) { f.resolver.expectedCompilerBuild = testDigest('f') }},
		{name: "evaluator", mutate: func(f *resolverFixture) { f.resolver.expectedEvaluatorBuild = testDigest('f') }},
		{name: "backend id", mutate: func(f *resolverFixture) { f.backend.id = "other-backend" }},
		{name: "backend capabilities", mutate: func(f *resolverFixture) { f.backend.capabilities.MaxWidth-- }},
		{name: "runtime", mutate: func(f *resolverFixture) { f.backend.runtime = testDigest('f') }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			if test.mutate != nil {
				test.mutate(fixture)
			}
			projectID := test.callID
			if projectID == "" {
				projectID = testProjectID
			}
			_, err := fixture.resolver.ResolveBackend(context.Background(), projectID, fixture.manifest)
			assertUnavailable(t, err)
			if len(fixture.prober.requestsSnapshot()) != 0 {
				t.Fatal("resource was probed after a runtime identity mismatch")
			}
		})
	}
}

func TestResolverRejectsUnauthorizedAndNonFileLocatorsWithoutLeakingPath(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.backend.authorizeErr = errors.New("secret host failure: /allowed/audio.wav")
	_, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
	assertUnavailable(t, err)
	if strings.Contains(err.Error(), "/allowed/audio.wav") || strings.Contains(err.Error(), "secret host failure") {
		t.Fatalf("host detail leaked: %v", err)
	}

	fixture = newResolverFixture(t)
	draft := fixture.manifest
	draft.Resources[0].LocatorScheme = renderapi.LocatorCAS
	draft.Resources[0].Locator = "cas://sha256/" + strings.Repeat("a", 64)
	fixture.manifest = refreezeManifest(t, fixture.plan, draft)
	_, err = fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
	assertUnavailable(t, err)
	if fixture.backend.authorizeCalls != 0 || len(fixture.prober.requestsSnapshot()) != 0 {
		t.Fatal("non-file resource reached locator authorization or probing")
	}
}

func TestResolverRejectsFreshProbeFailuresAndStaleEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverFixture)
	}{
		{name: "probe error", mutate: func(f *resolverFixture) { f.prober.err = errors.New("private probe error") }},
		{name: "probe diagnostic", mutate: func(f *resolverFixture) {
			f.prober.diagnostics = []probe.Diagnostic{{Code: "TEST", Severity: probe.SeverityError, Message: "private diagnostic"}}
		}},
		{name: "asset identity", mutate: func(f *resolverFixture) { f.prober.manifest.Resource.AssetID = "other" }},
		{name: "content identity", mutate: func(f *resolverFixture) { f.prober.manifest.Resource.ContentHash = testDigest('f') }},
		{name: "size identity", mutate: func(f *resolverFixture) { f.prober.manifest.Resource.SizeBytes++ }},
		{name: "stale fingerprint", mutate: func(f *resolverFixture) { f.prober.manifest.Streams[0].Codec = "flac" }},
		{name: "invalid manifest", mutate: func(f *resolverFixture) { f.prober.manifest.Streams = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			test.mutate(fixture)
			_, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
			assertUnavailable(t, err)
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("callback detail leaked: %v", err)
			}
			if len(fixture.registry.manifestsSnapshot()) != 0 {
				t.Fatal("rejected probe evidence was registered")
			}
		})
	}
}

func TestResolverVerifiesRegistryResult(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverFixture)
	}{
		{name: "error", mutate: func(f *resolverFixture) { f.registry.err = errors.New("private registry path") }},
		{name: "fingerprint", mutate: func(f *resolverFixture) { f.registry.returnFingerprint = testDigest('f') }},
		{name: "manifest", mutate: func(f *resolverFixture) {
			changed, err := cloneProbeManifest(f.probe)
			if err != nil {
				panic(err)
			}
			changed.Streams[0].Codec = "flac"
			f.registry.returnManifest = &changed
			f.registry.returnFingerprint, err = probe.Fingerprint(f.probe)
			if err != nil {
				panic(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			test.mutate(fixture)
			_, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
			assertUnavailable(t, err)
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("registry detail leaked: %v", err)
			}
		})
	}
}

func TestResolverContainsCallbackPanics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverFixture)
	}{
		{name: "backend capabilities", mutate: func(f *resolverFixture) { f.backend.panicAt = "capabilities" }},
		{name: "backend id", mutate: func(f *resolverFixture) { f.backend.panicAt = "id" }},
		{name: "backend runtime", mutate: func(f *resolverFixture) { f.backend.panicAt = "runtime" }},
		{name: "backend authorization", mutate: func(f *resolverFixture) { f.backend.panicAt = "authorize" }},
		{name: "prober", mutate: func(f *resolverFixture) { f.prober.panic = true }},
		{name: "registry", mutate: func(f *resolverFixture) { f.registry.panic = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverFixture(t)
			test.mutate(fixture)
			_, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
			assertUnavailable(t, err)
			if !strings.Contains(err.Error(), "panicked") || strings.Contains(err.Error(), "secret panic") {
				t.Fatalf("panic error = %v", err)
			}
		})
	}
}

func TestResolverPreservesCancellation(t *testing.T) {
	fixture := newResolverFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.resolver.ResolveBackend(canceled, testProjectID, fixture.manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}
	if len(fixture.prober.requestsSnapshot()) != 0 {
		t.Fatal("pre-canceled resolver touched probe adapter")
	}

	fixture = newResolverFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.prober.onProbe = func() { cancel() }
	fixture.prober.err = errors.New("callback cancellation")
	if _, err := fixture.resolver.ResolveBackend(ctx, testProjectID, fixture.manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight canceled error = %v", err)
	}
}

func TestResolverDefensivelyOwnsCallbackValues(t *testing.T) {
	fixture := newResolverFixture(t)
	manifestBefore, _ := json.Marshal(fixture.manifest)
	probeBefore, _ := json.Marshal(fixture.probe)
	capabilitiesBefore := videoedit.CanonicalBackendCapabilities(fixture.backend.capabilities)
	fixture.registry.put = func(input probe.ProbeManifest) (probe.ProbeManifest, string, error) {
		input.Streams[0].Codec = "mutated-by-registry"
		fingerprint, err := probe.Fingerprint(fixture.probe)
		return fixture.probe, fingerprint, err
	}
	if _, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest); err != nil {
		t.Fatal(err)
	}
	manifestAfter, _ := json.Marshal(fixture.manifest)
	probeAfter, _ := json.Marshal(fixture.probe)
	if !reflect.DeepEqual(manifestBefore, manifestAfter) || !reflect.DeepEqual(probeBefore, probeAfter) ||
		!reflect.DeepEqual(capabilitiesBefore, videoedit.CanonicalBackendCapabilities(fixture.backend.capabilities)) {
		t.Fatal("resolver allowed callback mutation to escape into caller-owned values")
	}
}

func TestNewRejectsInvalidAndTypedNilDependencies(t *testing.T) {
	fixture := newResolverFixture(t)
	base := Config{
		ProjectID: testProjectID, Backend: fixture.backend, Prober: fixture.prober, Registry: fixture.registry,
		ExpectedCompilerBuild: testDigest('c'), ExpectedEvaluatorBuild: testDigest('d'),
	}
	var nilBackend *fakeBackend
	var nilProber *fakeProber
	var nilRegistry *fakeRegistry
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "blank project", mutate: func(c *Config) { c.ProjectID = " \t" }},
		{name: "invalid utf8 project", mutate: func(c *Config) { c.ProjectID = string([]byte{0xff}) }},
		{name: "typed nil backend", mutate: func(c *Config) { c.Backend = nilBackend }},
		{name: "typed nil prober", mutate: func(c *Config) { c.Prober = nilProber }},
		{name: "typed nil registry", mutate: func(c *Config) { c.Registry = nilRegistry }},
		{name: "compiler", mutate: func(c *Config) { c.ExpectedCompilerBuild = "latest" }},
		{name: "evaluator", mutate: func(c *Config) { c.ExpectedEvaluatorBuild = testDigest('A') }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("New error = %v", err)
			}
		})
	}
}

func TestResolverRejectsTamperedManifestBeforeHostCallbacks(t *testing.T) {
	fixture := newResolverFixture(t)
	fixture.manifest.Resources[0].SizeBytes++
	_, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
	assertUnavailable(t, err)
	if fixture.backend.capabilityCalls != 0 || len(fixture.prober.requestsSnapshot()) != 0 {
		t.Fatal("tampered manifest reached host callbacks")
	}
}

func TestResolverSupportsConcurrentResolution(t *testing.T) {
	fixture := newResolverFixture(t)
	const calls = 16
	var wait sync.WaitGroup
	errorsSeen := make(chan error, calls)
	for index := 0; index < calls; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := fixture.resolver.ResolveBackend(context.Background(), testProjectID, fixture.manifest)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.prober.requestsSnapshot()) != calls || len(fixture.registry.manifestsSnapshot()) != calls {
		t.Fatalf("probes/registrations = %d/%d", len(fixture.prober.requestsSnapshot()), len(fixture.registry.manifestsSnapshot()))
	}
}

type fakeBackend struct {
	mu              sync.Mutex
	id              string
	capabilities    videoedit.BackendCapabilities
	runtime         string
	authorizeErr    error
	capabilitiesErr error
	runtimeErr      error
	panicAt         string
	authorizeCalls  int
	capabilityCalls int
}

func (backend *fakeBackend) ID() string {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.panicAt == "id" {
		panic("secret panic id")
	}
	return backend.id
}

func (backend *fakeBackend) Capabilities(context.Context) (videoedit.BackendCapabilities, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.capabilityCalls++
	if backend.panicAt == "capabilities" {
		panic("secret panic capabilities")
	}
	return backend.capabilities, backend.capabilitiesErr
}

func (backend *fakeBackend) FFmpegRuntimeFingerprint(context.Context) (string, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.panicAt == "runtime" {
		panic("secret panic runtime")
	}
	return backend.runtime, backend.runtimeErr
}

func (backend *fakeBackend) ValidateInputLocator(string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.authorizeCalls++
	if backend.panicAt == "authorize" {
		panic("secret panic authorize")
	}
	return backend.authorizeErr
}

func (*fakeBackend) Preflight(context.Context, videoedit.RenderPlan, renderapi.ExecutionManifest) []videoedit.Diagnostic {
	return nil
}

func (*fakeBackend) Render(context.Context, renderapi.RenderRequest, renderapi.EventSink) (renderapi.RenderArtifact, error) {
	return renderapi.RenderArtifact{}, nil
}

func (*fakeBackend) RenderFrame(context.Context, renderapi.ExactFrameRenderRequest, renderapi.EventSink) (renderapi.ExactFrameArtifact, error) {
	return renderapi.ExactFrameArtifact{}, nil
}

type fakeProber struct {
	mu          sync.Mutex
	manifest    probe.ProbeManifest
	diagnostics []probe.Diagnostic
	err         error
	panic       bool
	onProbe     func()
	requests    []cutlyffprobe.Request
}

func (prober *fakeProber) Probe(_ context.Context, request cutlyffprobe.Request) (probe.ProbeManifest, []probe.Diagnostic, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.panic {
		panic("secret panic prober")
	}
	prober.requests = append(prober.requests, request)
	if prober.onProbe != nil {
		prober.onProbe()
	}
	return prober.manifest, append([]probe.Diagnostic(nil), prober.diagnostics...), prober.err
}

func (prober *fakeProber) requestsSnapshot() []cutlyffprobe.Request {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return append([]cutlyffprobe.Request(nil), prober.requests...)
}

type fakeRegistry struct {
	mu                sync.Mutex
	err               error
	panic             bool
	returnFingerprint string
	returnManifest    *probe.ProbeManifest
	put               func(probe.ProbeManifest) (probe.ProbeManifest, string, error)
	manifests         []probe.ProbeManifest
}

func (registry *fakeRegistry) Put(_ context.Context, manifest probe.ProbeManifest) (probe.ProbeManifest, string, bool, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.panic {
		panic("secret panic registry")
	}
	registry.manifests = append(registry.manifests, manifest)
	if registry.put != nil {
		stored, fingerprint, err := registry.put(manifest)
		return stored, fingerprint, true, err
	}
	if registry.err != nil {
		return probe.ProbeManifest{}, "", false, registry.err
	}
	stored := manifest
	if registry.returnManifest != nil {
		stored = *registry.returnManifest
	}
	fingerprint, err := probe.Fingerprint(stored)
	if registry.returnFingerprint != "" {
		fingerprint = registry.returnFingerprint
	}
	return stored, fingerprint, true, err
}

func (registry *fakeRegistry) manifestsSnapshot() []probe.ProbeManifest {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return append([]probe.ProbeManifest(nil), registry.manifests...)
}

func testRenderPlan(t *testing.T) videoedit.RenderPlan {
	t.Helper()
	one := videoedit.RationalTime{Value: 1, Rate: 1}
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{{ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.wav", ContentHash: testDigest('a'), Duration: &one}},
		Timelines: []videoedit.Timeline{{ID: "timeline", Tracks: []videoedit.Track{{
			ID: "audio-track", Kind: videoedit.TrackKindAudio,
			Clips: []videoedit.Clip{{ID: "audio-clip", AssetID: "audio", TimelineRange: videoedit.TimeRange{Start: zero, Duration: one}, SourceRange: videoedit.TimeRange{Start: zero, Duration: one}}},
		}}}},
	}
	plan, diagnostics := videoedit.Resolve(document, videoedit.TimeRange{Start: zero, Duration: one}, videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock:  videoedit.FrameClock{Origin: zero, FramesPerSecond: videoedit.RationalTime{Value: 30, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: zero, SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("plan diagnostics = %+v", diagnostics)
	}
	return plan
}

func testProbeManifest() probe.ProbeManifest {
	duration := int64(48000)
	return probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "audio", ContentHash: testDigest('a'), SizeBytes: 10},
		Probe:         probe.ProbeIdentity{Implementation: "ffprobe-json-v1", BuildFingerprint: testDigest('1'), OptionsFingerprint: testDigest('2')},
		Container:     probe.ContainerInfo{FormatName: "wav", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, BitRate: 128000, Seekable: true},
		Duration:      videoedit.RationalTime{Value: 1, Rate: 1},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindAudio, Codec: "pcm_s16le", TimeBase: videoedit.RationalTime{Value: 1, Rate: 48000}, StartPTS: 0, DurationPTS: &duration,
			Audio: &probe.AudioStream{SampleRate: 48000, Channels: 2, ChannelLayout: "stereo", SampleFormat: "s16", BitsPerSample: 16},
		}},
	}
}

func refreezeManifest(t *testing.T, plan videoedit.RenderPlan, draft renderapi.ExecutionManifest) renderapi.ExecutionManifest {
	t.Helper()
	draft.Fingerprint = ""
	frozen, err := renderapi.FreezeExecutionManifest(plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, renderexec.ErrBackendUnavailable) {
		t.Fatalf("error = %v, want ErrBackendUnavailable", err)
	}
}

func testDigest(value byte) string {
	return fmt.Sprintf("sha256:%s", strings.Repeat(string(value), 64))
}
