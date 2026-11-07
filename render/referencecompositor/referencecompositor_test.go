package referencecompositor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestRenderAtCompositesStableBackgroundToForeground(t *testing.T) {
	document := twoTrackDocument()
	program := compileProgram(t, document, DefaultConfig(1, 1))
	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red":  {R: 65535, A: 65535},
		"asset-blue": {B: 32768, A: 32768},
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(2, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, result.Surface, cpucompositor.Pixel{R: 32767, B: 32768, A: 65535})
	if result.CompositorProfile != ProfileV1 || result.SourceProfile != SourceProfileV1 ||
		result.SurfaceProfile != cpucompositor.SurfaceProfileV1 || result.KernelProfile != cpucompositor.KernelProfileV1 ||
		result.LayerOrder != LayerOrderProfile || result.Width != 1 || result.Height != 1 || result.PixelHash == "" {
		t.Fatalf("incomplete result identity: %+v", result)
	}
	requests := provider.Requests()
	wantRed := SourceRequest{
		DocumentHash: program.DocumentHash(),
		TimelineID:   "timeline", TrackID: "track-red", ClipID: "clip-red", AssetID: "asset-red",
		TrackIndex: 0, ClipIndex: 0, TimelineTime: rt(2, 1), LocalTime: rt(2, 1), SourceTime: rt(2, 1),
		Width: 1, Height: 1, SourceProfile: SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
	wantBlue := wantRed
	wantBlue.TrackID, wantBlue.ClipID, wantBlue.AssetID, wantBlue.TrackIndex = "track-blue", "clip-blue", "asset-blue", 1
	if len(requests) != 2 || requests[0] != wantRed || requests[1] != wantBlue {
		t.Fatalf("provider order = %+v", requests)
	}
}

func TestRenderAtConsumesExactOpacityKeyframe(t *testing.T) {
	document := oneTrackDocument()
	zero, one := ratio(0, 1), ratio(1, 1)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Duration = rt(2, 1)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(2, 1)
	document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{{
		ID: "opacity-lane", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &zero}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: &one}},
		},
	}}
	program := compileProgram(t, document, DefaultConfig(1, 1))
	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red": {R: 65535, G: 65535, B: 65535, A: 65535},
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, result.Surface, cpucompositor.Pixel{R: 32768, G: 32768, B: 32768, A: 32768})
	request := provider.Requests()[0]
	if request.TimelineTime != rt(1, 1) || request.LocalTime != rt(1, 1) || request.SourceTime != rt(1, 1) ||
		request.SourceProfile != SourceProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		t.Fatalf("source request = %+v", request)
	}
}

func TestAsymmetricTransitionUsesExactHandlesAndFrameParity(t *testing.T) {
	document := transitionDocument()
	program := compileProgram(t, document, DefaultConfig(1, 1))
	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red":  {R: 65535, A: 65535},
		"asset-blue": {B: 65535, A: 65535},
	})
	start, err := program.RenderAt(context.Background(), "timeline", rt(4, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, start.Surface, cpucompositor.Pixel{R: 65535, A: 65535})
	atCut, err := program.RenderAt(context.Background(), "timeline", rt(5, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, atCut.Surface, cpucompositor.Pixel{R: 43690, B: 21845, A: 65535})
	frame, err := program.RenderFrame(context.Background(), "timeline", videoedit.FrameClock{
		Origin: rt(0, 1), FramesPerSecond: rt(1, 1),
	}, 5, provider)
	if err != nil {
		t.Fatal(err)
	}
	if frame.CompositionFrameIndex != 5 || frame.Result.TimelineTime != rt(5, 1) || frame.Result.PixelHash != atCut.PixelHash {
		t.Fatalf("frame parity = %+v, RenderAt=%+v", frame, atCut)
	}
	end, err := program.RenderAt(context.Background(), "timeline", rt(7, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, end.Surface, cpucompositor.Pixel{B: 65535, A: 65535})

	requests := provider.Requests()
	// Calls 0/1 are window start; 2/3 are RenderAt(cut); 4/5 are RenderFrame(cut); final is normal incoming.
	if len(requests) != 7 || requests[0].ClipID != "clip-red" || requests[0].SourceTime != rt(5, 1) ||
		requests[1].ClipID != "clip-blue" || requests[1].SourceTime != rt(0, 1) ||
		requests[2].SourceTime != rt(6, 1) || requests[3].SourceTime != rt(1, 1) ||
		requests[6].ClipID != "clip-blue" || requests[6].SourceTime != rt(3, 1) {
		t.Fatalf("transition handle requests = %+v", requests)
	}
}

func TestTransitionAppliesEachClipOpacityBeforeDissolve(t *testing.T) {
	document := transitionDocument()
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		Opacity: &documentv2.OpacityProperty{Value: ratio(1, 2)},
	}
	document.Timelines[0].Tracks[0].Clips[1].PropertySet = &documentv2.PropertySet{
		Opacity: &documentv2.OpacityProperty{Value: ratio(1, 3)},
	}
	program := compileProgram(t, document, DefaultConfig(1, 1))
	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red":  {R: 65535, A: 65535},
		"asset-blue": {B: 65535, A: 65535},
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(5, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	// At the asymmetric cut, dissolve weights are 2/3 and 1/3. Each source was first
	// quantized through its own exact opacity kernel (1/2 and 1/3 respectively).
	assertPixel(t, result.Surface, cpucompositor.Pixel{R: 21845, B: 7282, A: 29127})
}

func TestGapReturnsTransparentSurfaceWithoutSourceRequest(t *testing.T) {
	program := compileProgram(t, oneTrackDocument(), DefaultConfig(2, 1))
	called := false
	provider := SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	for _, at := range []documentv2.RationalTime{rt(10, 1), rt(20, 1)} {
		result, err := program.RenderAt(context.Background(), "timeline", at, provider)
		if err != nil {
			t.Fatal(err)
		}
		if called {
			t.Fatalf("gap at %v requested a source surface", at)
		}
		for x := 0; x < 2; x++ {
			pixel, err := result.Surface.Pixel(x, 0)
			if err != nil || pixel != (cpucompositor.Pixel{}) {
				t.Fatalf("gap at %v pixel %d = %+v err=%v", at, x, pixel, err)
			}
		}
	}
}

func TestCompileRejectsEveryUnconsumedVisualSemantic(t *testing.T) {
	tests := []struct {
		name     string
		document documentv2.Document
		config   Config
	}{
		{name: "transform", document: withTransform(oneTrackDocument()), config: DefaultConfig(1, 1)},
		{name: "crop", document: withCrop(oneTrackDocument()), config: DefaultConfig(1, 1)},
		{name: "rendering extension", document: withRenderingExtension(oneTrackDocument()), config: DefaultConfig(1, 1)},
		{name: "text", document: textDocument(), config: DefaultConfig(1, 1)},
		{name: "vector", document: vectorDocument(), config: DefaultConfig(1, 1)},
		{name: "unsupported retime", document: retimedDocument(), config: DefaultConfig(1, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program, diagnostics := Compile(test.document, test.config)
			if program != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("compile = program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}

	config := DefaultConfig(1, 1)
	config.Source.LinearVideoRetime = true
	program, diagnostics := Compile(retimedDocument(), config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("explicit retime source capability rejected: program=%v diagnostics=%+v", program, diagnostics)
	}
	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red": {R: 65535, A: 65535},
	})
	if _, err := program.RenderAt(context.Background(), "timeline", rt(4, 1), provider); err != nil {
		t.Fatal(err)
	}
	request := provider.Requests()[0]
	if request.TimelineTime != rt(4, 1) || request.LocalTime != rt(4, 1) || request.SourceTime != rt(2, 1) {
		t.Fatalf("retimed source request = %+v", request)
	}
}

func TestProviderFailuresCancellationAndAliasSafety(t *testing.T) {
	program := compileProgram(t, oneTrackDocument(), DefaultConfig(1, 1))
	if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), nil); result.Surface != nil || !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("nil provider result=%+v err=%v", result, err)
	}
	var typedNil *recordingProvider
	if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), typedNil); result.Surface != nil || !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("typed nil provider result=%+v err=%v", result, err)
	}
	var typedNilFunc SourceProviderFunc
	if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), typedNilFunc); result.Surface != nil || !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("typed nil function provider result=%+v err=%v", result, err)
	}
	if result, err := program.RenderAt(context.Background(), "missing", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
		return nil, nil
	})); result.Surface != nil || !errors.Is(err, ErrEvaluationFailed) {
		t.Fatalf("evaluation failure result=%+v err=%v", result, err)
	}
	providerErr := errors.New("decoder unavailable")
	if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
		return nil, providerErr
	})); result.Surface != nil || !errors.Is(err, ErrSourceFailed) || !errors.Is(err, providerErr) || !hasErrorIdentity(err, CodeSourceFailed, "layers/0/clip/source") {
		t.Fatalf("provider error result=%+v err=%v", result, err)
	}
	for _, providerErr := range []error{context.Canceled, context.DeadlineExceeded} {
		if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
			return nil, providerErr
		})); result.Surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, providerErr) || errors.Is(err, ErrSourceFailed) ||
			!hasErrorIdentity(err, CodeCanceled, "layers/0/clip/source") {
			t.Fatalf("provider cancellation %v result=%+v err=%v", providerErr, result, err)
		}
	}
	mismatch, err := cpucompositor.NewSurface(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
		return mismatch, nil
	})); result.Surface != nil || !errors.Is(err, ErrSurfaceMismatch) {
		t.Fatalf("mismatch result=%+v err=%v", result, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if result, err := program.RenderAt(ctx, "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
		called = true
		return nil, nil
	})); result.Surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled result=%+v err=%v called=%t", result, err, called)
	}

	source := mustSurface(t, 1, 1, cpucompositor.Pixel{R: 100, G: 200, B: 300, A: 400})
	sourceHash, _ := source.CanonicalHash()
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (*cpucompositor.Surface, error) {
		return source, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	afterHash, _ := source.CanonicalHash()
	if result.Surface == source || sourceHash != afterHash {
		t.Fatalf("provider surface aliased/mutated: result=%p source=%p %s != %s", result.Surface, source, sourceHash, afterHash)
	}
}

func TestSeekOrderAndConcurrentReadersAreStable(t *testing.T) {
	program := compileProgram(t, transitionDocument(), DefaultConfig(1, 1))
	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red":  {R: 65535, A: 65535},
		"asset-blue": {B: 65535, A: 65535},
	})
	times := []documentv2.RationalTime{rt(7, 1), rt(4, 1), rt(5, 1), rt(0, 1), rt(6, 1), rt(20, 1)}
	baseline := make(map[documentv2.RationalTime]string, len(times))
	for _, at := range times {
		result, err := program.RenderAt(context.Background(), "timeline", at, provider)
		if err != nil {
			t.Fatal(err)
		}
		baseline[at] = result.PixelHash
	}

	const workers = 24
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers*len(times))
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := range times {
				at := times[(worker+len(times)-index-1)%len(times)]
				result, err := program.RenderAt(context.Background(), "timeline", at, provider)
				if err != nil {
					errorsOut <- err
					continue
				}
				if result.PixelHash != baseline[at] {
					errorsOut <- fmt.Errorf("at %v hash = %s, want %s", at, result.PixelHash, baseline[at])
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func TestCapabilitiesAreNarrowAndDefensive(t *testing.T) {
	config := DefaultConfig(1920, 1080)
	capabilities, err := Capabilities(config)
	if err != nil {
		t.Fatal(err)
	}
	features := make(map[videoedit.CapabilityID]bool)
	for _, feature := range capabilities.Features {
		features[feature] = true
	}
	for _, absent := range []videoedit.CapabilityID{
		documentv2.CapabilityTransformV1, documentv2.CapabilityCropV1,
		documentv2.CapabilityTextV1, documentv2.CapabilityVectorPathV1,
	} {
		if features[absent] {
			t.Fatalf("unexpected capability %q in %+v", absent, capabilities.Features)
		}
	}
	if capabilities.MaxWidth != 1920 || capabilities.MaxHeight != 1080 || !features[documentv2.CapabilityOpacityV1] ||
		!features[videoedit.CapabilityID(documentv2.TransitionDissolveV1)] {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	capabilities.Features[0] = "mutated.v1"
	again, err := Capabilities(config)
	if err != nil || again.Features[0] == "mutated.v1" {
		t.Fatalf("capabilities alias: %+v err=%v", again, err)
	}
}

func TestCompileFreezesDocumentConfigAndScopesSourceRequests(t *testing.T) {
	document := oneTrackDocument()
	config := DefaultConfig(1, 1)
	config.BuildFingerprint = "build-a"
	program := compileProgram(t, document, config)
	frozenHash := program.DocumentHash()

	// Mutating caller-owned inputs after Compile must not change the compiled program.
	document.Assets[0].ID = "mutated-asset"
	document.Assets[0].URI = "mutated.mp4"
	document.Timelines[0].Tracks[0].ID = "mutated-track"
	document.Timelines[0].Tracks[0].Clips[0].AssetID = "mutated-asset"
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Start = rt(9, 1)
	config.Width = 2
	config.Source.Video = false
	config.BuildFingerprint = "build-b"

	provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red": {R: 65535, A: 65535},
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.Requests()[0]
	if result.DocumentHash != frozenHash || program.DocumentHash() != frozenHash || request.DocumentHash != frozenHash ||
		request.TrackID != "track-red" || request.AssetID != "asset-red" || request.SourceTime != rt(0, 1) ||
		request.Width != 1 || program.Config().Width != 1 || program.Config().BuildFingerprint != "build-a" {
		t.Fatalf("compiled snapshot changed: result=%+v request=%+v config=%+v", result, request, program.Config())
	}

	// Document-local IDs may collide across programs; the frozen document hash keeps requests
	// unambiguous for one provider serving both programs concurrently.
	other := oneTrackDocument()
	other.Revision = 2
	other.Assets[0].URI = "other.mp4"
	otherProgram := compileProgram(t, other, DefaultConfig(1, 1))
	if otherProgram.DocumentHash() == frozenHash {
		t.Fatal("different frozen documents have the same document hash")
	}
	otherProvider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
		"asset-red": {R: 65535, A: 65535},
	})
	if _, err := otherProgram.RenderAt(context.Background(), "timeline", rt(0, 1), otherProvider); err != nil {
		t.Fatal(err)
	}
	if got := otherProvider.Requests()[0].DocumentHash; got != otherProgram.DocumentHash() || got == request.DocumentHash {
		t.Fatalf("source request document scope = %q, want %q and not %q", got, otherProgram.DocumentHash(), request.DocumentHash)
	}
}

func TestConfigFailsBeforeSurfaceAllocation(t *testing.T) {
	config := DefaultConfig(2, 1)
	config.Limits = cpucompositor.Limits{MaxWidth: 1, MaxHeight: 1, MaxBytes: 8}
	if program, diagnostics := Compile(oneTrackDocument(), config); program != nil || len(diagnostics) != 1 || diagnostics[0].Code != CodeConfigInvalid {
		t.Fatalf("invalid target compile = program=%v diagnostics=%+v", program, diagnostics)
	}
	config = DefaultConfig(1, 1)
	config.Source.Video = false
	config.Source.LinearVideoRetime = true
	if _, err := Capabilities(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("retime without video error = %v", err)
	}
	maxInt := int(^uint(0) >> 1)
	config = Config{
		Width: maxInt, Height: 2,
		Limits: cpucompositor.Limits{MaxWidth: maxInt, MaxHeight: 2, MaxBytes: cpucompositor.HardMaxSurfaceBytes},
	}
	if _, err := Capabilities(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("overflow-sized target error = %v", err)
	}
}

type compositorVectorCase struct {
	Name          string                  `json:"name"`
	Scenario      string                  `json:"scenario"`
	At            documentv2.RationalTime `json:"at"`
	ExpectedPixel cpucompositor.Pixel     `json:"expected_pixel"`
	ExpectedHash  string                  `json:"expected_hash"`
}

type compositorVectors struct {
	Profile string                 `json:"profile"`
	Width   int                    `json:"width"`
	Height  int                    `json:"height"`
	Cases   []compositorVectorCase `json:"cases"`
}

func TestCanonicalCompositorVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/compositor-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture compositorVectors
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) || fixture.Profile != ProfileV1 || fixture.Width != 1 || fixture.Height != 1 || len(fixture.Cases) == 0 {
		t.Fatalf("invalid/non-canonical vectors: fixture=%+v\nwant=%s\n got=%s", fixture, canonical, bytes.TrimSpace(raw))
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			var document documentv2.Document
			switch vector.Scenario {
			case "gap":
				document = oneTrackDocument()
			case "z_order":
				document = twoTrackDocument()
			case "transition":
				document = transitionDocument()
			default:
				t.Fatalf("unknown vector scenario %q", vector.Scenario)
			}
			program := compileProgram(t, document, DefaultConfig(fixture.Width, fixture.Height))
			provider := newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
				"asset-red":  {R: 65535, A: 65535},
				"asset-blue": {B: 32768, A: 32768},
			})
			if vector.Scenario == "transition" {
				provider = newRecordingProvider(t, map[documentv2.ID]cpucompositor.Pixel{
					"asset-red": {R: 65535, A: 65535}, "asset-blue": {B: 65535, A: 65535},
				})
			}
			result, err := program.RenderAt(context.Background(), "timeline", vector.At, provider)
			if err != nil {
				t.Fatal(err)
			}
			assertPixel(t, result.Surface, vector.ExpectedPixel)
			if result.PixelHash != vector.ExpectedHash {
				t.Fatalf("pixel hash = %s, want %s", result.PixelHash, vector.ExpectedHash)
			}
		})
	}
}

func TestProfileCanonicalFixture(t *testing.T) {
	canonical, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture ProfileDescriptor
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatalf("%v\ncanonical descriptor=%s", err, canonical)
	}
	if !reflect.DeepEqual(fixture, Descriptor()) {
		t.Fatalf("fixture descriptor differs\nfixture=%+v\ncode=%+v", fixture, Descriptor())
	}
	fixtureCanonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), fixtureCanonical) || !bytes.Equal(canonical, fixtureCanonical) {
		t.Fatalf("fixture not canonical\nwant=%s\n got=%s", canonical, bytes.TrimSpace(raw))
	}
	encoded, err := json.Marshal(Descriptor())
	if err != nil || len(encoded) == 0 {
		t.Fatalf("descriptor marshal: %s err=%v", encoded, err)
	}
}

type recordingProvider struct {
	mu       sync.Mutex
	surfaces map[documentv2.ID]*cpucompositor.Surface
	requests []SourceRequest
}

func newRecordingProvider(t *testing.T, pixels map[documentv2.ID]cpucompositor.Pixel) *recordingProvider {
	t.Helper()
	surfaces := make(map[documentv2.ID]*cpucompositor.Surface, len(pixels))
	for assetID, pixel := range pixels {
		surfaces[assetID] = mustSurface(t, 1, 1, pixel)
	}
	return &recordingProvider{surfaces: surfaces}
}

func (provider *recordingProvider) Surface(_ context.Context, request SourceRequest) (*cpucompositor.Surface, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	surface := provider.surfaces[request.AssetID]
	if surface == nil {
		return nil, fmt.Errorf("asset %q missing", request.AssetID)
	}
	return surface, nil
}

func (provider *recordingProvider) Requests() []SourceRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]SourceRequest(nil), provider.requests...)
}

func compileProgram(t *testing.T, document documentv2.Document, config Config) *Program {
	t.Helper()
	program, diagnostics := Compile(document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile: program=%v diagnostics=%+v", program, diagnostics)
	}
	return program
}

func twoTrackDocument() documentv2.Document {
	document := oneTrackDocument()
	duration := rt(10, 1)
	document.Assets = append(document.Assets, documentv2.Asset{
		ID: "asset-blue", Kind: documentv2.AssetKindVideo, URI: "blue.mp4", Duration: &duration,
	})
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{
		ID: "track-blue", Kind: documentv2.TrackKindVideo,
		Clips: []documentv2.Clip{{
			ID: "clip-blue", AssetID: "asset-blue",
			TimelineRange: timeRange(0, 10), SourceRange: timeRange(0, 10),
		}},
	})
	return document
}

func oneTrackDocument() documentv2.Document {
	duration := rt(10, 1)
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset-red", Kind: documentv2.AssetKindVideo, URI: "red.mp4", Duration: &duration}},
		Timelines: []documentv2.Timeline{{
			ID: "timeline", Tracks: []documentv2.Track{{
				ID: "track-red", Kind: documentv2.TrackKindVideo,
				Clips: []documentv2.Clip{{
					ID: "clip-red", AssetID: "asset-red",
					TimelineRange: timeRange(0, 10), SourceRange: timeRange(0, 10),
				}},
			}},
		}},
	}
}

func transitionDocument() documentv2.Document {
	duration := rt(20, 1)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "transition-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "asset-red", Kind: documentv2.AssetKindVideo, URI: "red.mp4", Duration: &duration},
			{ID: "asset-blue", Kind: documentv2.AssetKindVideo, URI: "blue.mp4", Duration: &duration},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{
				{ID: "clip-red", AssetID: "asset-red", TimelineRange: timeRange(0, 5), SourceRange: timeRange(1, 5)},
				{ID: "clip-blue", AssetID: "asset-blue", TimelineRange: timeRange(5, 5), SourceRange: timeRange(1, 5)},
			},
		}}, Transitions: []documentv2.Transition{{
			ID: "transition", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "clip-red", IncomingClipID: "clip-blue",
			PreDuration: rt(1, 1), PostDuration: rt(2, 1),
		}}}},
	}
	return document
}

func retimedDocument() documentv2.Document {
	document := oneTrackDocument()
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(5, 1)
	return document
}

func withTransform(document documentv2.Document) documentv2.Document {
	zero, one, half := ratio(0, 1), ratio(1, 1), ratio(1, 2)
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{Transform: &documentv2.TransformProperty{
		Position: documentv2.Point{X: zero, Y: zero}, Scale: documentv2.Point{X: one, Y: one},
		RotationDegrees: zero, Anchor: documentv2.Point{X: half, Y: half},
	}}
	return document
}

func withCrop(document documentv2.Document) documentv2.Document {
	zero, one := ratio(0, 1), ratio(1, 1)
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{Crop: &documentv2.CropProperty{Rect: documentv2.Rect{
		X: zero, Y: zero, Width: one, Height: one,
	}}}
	return document
}

func withRenderingExtension(document documentv2.Document) documentv2.Document {
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{
		Namespace: "vendor.blur.v1", AffectsRendering: true, RequiredCapability: "vendor.blur.v1",
		Value: json.RawMessage(`{"radius":1}`),
	}}}
	return document
}

func textDocument() documentv2.Document {
	fontHash := "sha256:" + strings.Repeat("a", 64)
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "text-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "font", Kind: documentv2.AssetKindFont, URI: "font.ttf", ContentHash: fontHash},
			{ID: "text", Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{
				Text: "Cutly", FontAssetID: "font", FontSize: ratio(24, 1), Fill: documentv2.ColorRGBA16{R: 65535, G: 65535, B: 65535, A: 65535},
				Language: "en", Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart,
			}},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "text-track", Kind: documentv2.TrackKindText,
			Clips: []documentv2.Clip{{ID: "text-clip", AssetID: "text", TimelineRange: timeRange(0, 1), SourceRange: timeRange(0, 1)}},
		}}}},
	}
}

func vectorDocument() documentv2.Document {
	zero, one, ten := ratio(0, 1), ratio(1, 1), ratio(10, 1)
	fill := documentv2.ColorRGBA16{R: 65535, A: 65535}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "vector-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "vector", Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
			Profile: documentv2.VectorProfilePathV1, ViewBox: documentv2.Rect{X: zero, Y: zero, Width: ten, Height: ten},
			Paths: []documentv2.VectorPath{{
				ID: "path", FillRule: documentv2.VectorFillNonZero, Fill: &fill,
				Commands: []documentv2.VectorCommand{
					{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: zero, Y: zero}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: ten, Y: zero}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: one, Y: ten}}},
					{Op: documentv2.VectorClose},
				},
			}},
		}}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "vector-track", Kind: documentv2.TrackKindVector,
			Clips: []documentv2.Clip{{ID: "vector-clip", AssetID: "vector", TimelineRange: timeRange(0, 1), SourceRange: timeRange(0, 1)}},
		}}}},
	}
}

func timeRange(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}

func ratio(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func mustSurface(t *testing.T, width, height int, pixel cpucompositor.Pixel) *cpucompositor.Surface {
	t.Helper()
	pixels := make([]cpucompositor.Pixel, width*height)
	for index := range pixels {
		pixels[index] = pixel
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

func assertPixel(t *testing.T, surface *cpucompositor.Surface, want cpucompositor.Pixel) {
	t.Helper()
	if surface == nil {
		t.Fatal("surface is nil")
	}
	got, err := surface.Pixel(0, 0)
	if err != nil || got != want {
		t.Fatalf("pixel = %+v, want %+v, err=%v", got, want, err)
	}
}

func hasErrorIdentity(err error, code ErrorCode, path string) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code && typed.Path == path
}
