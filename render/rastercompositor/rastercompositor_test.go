package rastercompositor

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
	"sync/atomic"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
)

const (
	redAssetHash  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blueAssetHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestNativePipelineAppliesCropTransformOpacityAndBindsSource(t *testing.T) {
	document := oneTrackDocument(documentv2.AssetKindImage)
	half, one := ratio(1, 2), ratio(1, 1)
	zero := ratio(0, 1)
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		Transform: &documentv2.TransformProperty{
			Position: documentv2.Point{X: one, Y: zero}, Scale: documentv2.Point{X: one, Y: one},
			RotationDegrees: zero, Anchor: documentv2.Point{X: half, Y: half},
		},
		Crop:    &documentv2.CropProperty{Rect: documentv2.Rect{X: half, Y: zero, Width: half, Height: one}},
		Opacity: &documentv2.OpacityProperty{Value: half},
	}
	source := mustSurfacePixels(t, 2, 1, []cpucompositor.Pixel{
		{R: 65535, A: 65535}, {B: 65535, A: 65535},
	})
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{"asset-red": source})
	program := compileProgram(t, document, DefaultConfig(3, 1))
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result.Surface, []cpucompositor.Pixel{{}, {}, {B: 32768, A: 32768}})
	if result.Surface == source {
		t.Fatal("result aliases provider-owned native surface")
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	request := requests[0]
	if request.ContractVersion != NativeSourceContractVersion || request.DocumentHash != program.DocumentHash() ||
		request.TimelineID != "timeline" || request.TimelineIndex != 0 || request.TrackID != "track-red" || request.TrackIndex != 0 ||
		request.ClipID != "clip-red" || request.ClipIndex != 0 || request.AssetID != "asset-red" || request.AssetIndex != 0 ||
		request.AssetKind != documentv2.AssetKindImage || request.AssetContentHash != redAssetHash ||
		request.TimelineTime != rt(0, 1) || request.LocalTime != rt(0, 1) || request.SourceTime != rt(0, 1) ||
		request.SourceProfile != SourceProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		request.DecodeProfile != DefaultImageDecodeProfileV1 || request.SelectionProfile != DefaultImageSelectionProfileV1 ||
		request.ColorProfile != DefaultColorProfileV1 || request.NormalizationProfile != DefaultNormalizationProfileV1 {
		t.Fatalf("incomplete source request: %+v", request)
	}
	if result.DocumentHash != program.DocumentHash() || result.TimelineID != "timeline" || result.TimelineTime != rt(0, 1) ||
		result.Width != 3 || result.Height != 1 || result.CompositorProfile != ProfileV1 ||
		result.SourceContractVersion != NativeSourceContractVersion || result.SourceProfile != SourceProfileV1 ||
		result.SurfaceProfile != cpucompositor.SurfaceProfileV1 || result.RasterProfile != cpuraster.ProfileV1 ||
		result.KernelProfile != cpucompositor.KernelProfileV1 || result.LayerOrder != LayerOrderProfile ||
		len(result.Sources) != 1 || result.Sources[0].Request != request || result.Sources[0].ColorProfile != request.ColorProfile {
		t.Fatalf("incomplete result binding: %+v", result)
	}
	actualHash, hashErr := result.Surface.CanonicalHash()
	if hashErr != nil || result.PixelHash != actualHash {
		t.Fatalf("result hash = %q, actual=%q err=%v", result.PixelHash, actualHash, hashErr)
	}
}

func TestKeyframesFlowThroughRasterAndOpacity(t *testing.T) {
	document := oneTrackDocument(documentv2.AssetKindImage)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Duration = rt(2, 1)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(2, 1)
	zero, one := ratio(0, 1), ratio(1, 1)
	p0, p1 := documentv2.Point{X: zero, Y: zero}, documentv2.Point{X: one, Y: zero}
	full := documentv2.Rect{X: zero, Y: zero, Width: one, Height: one}
	easing := documentv2.CubicBezier{X1: ratio(1, 3), Y1: ratio(1, 3), X2: ratio(2, 3), Y2: ratio(2, 3)}
	document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{
		{ID: "position", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint, Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &p0}, Interpolation: documentv2.InterpolationHold},
			{Offset: rt(1, 1), Value: documentv2.KeyframeValue{Point: &p1}},
		}},
		{ID: "crop", Target: documentv2.KeyframeTargetCrop, ValueType: documentv2.KeyframeValueRect, Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Rect: &full}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Rect: &full}},
		}},
		{ID: "opacity", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &zero}, Interpolation: documentv2.InterpolationCubic, Easing: &easing},
			{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: &one}},
		}},
	}
	program := compileProgram(t, document, DefaultConfig(2, 1))
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{
		"asset-red": mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535}),
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result.Surface, []cpucompositor.Pixel{{}, {R: 32768, A: 32768}})
}

func TestTransitionProcessesBothClipsBeforeDissolveAndFrameParity(t *testing.T) {
	document := transitionDocument()
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		Opacity: &documentv2.OpacityProperty{Value: ratio(1, 2)},
	}
	document.Timelines[0].Tracks[0].Clips[1].PropertySet = &documentv2.PropertySet{
		Opacity: &documentv2.OpacityProperty{Value: ratio(1, 3)},
	}
	program := compileProgram(t, document, DefaultConfig(1, 1))
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{
		"asset-red":  mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535}),
		"asset-blue": mustSurface(t, 1, 1, cpucompositor.Pixel{B: 65535, A: 65535}),
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(5, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result.Surface, []cpucompositor.Pixel{{R: 21845, B: 7282, A: 29127}})
	if len(result.Sources) != 2 || result.Sources[0].Request.ClipID != "clip-red" || result.Sources[1].Request.ClipID != "clip-blue" ||
		result.Sources[0].Request.SourceTime != rt(6, 1) || result.Sources[1].Request.SourceTime != rt(1, 1) {
		t.Fatalf("transition source order/handles = %+v", result.Sources)
	}
	frame, err := program.RenderFrame(context.Background(), "timeline", videoedit.FrameClock{
		Origin: rt(0, 1), FramesPerSecond: rt(1, 1),
	}, 5, provider)
	if err != nil || frame.CompositionFrameIndex != 5 || frame.Result.PixelHash != result.PixelHash || frame.Result.TimelineTime != rt(5, 1) {
		t.Fatalf("frame parity = %+v err=%v; RenderAt=%+v", frame, err, result)
	}
}

func TestLayerOrderAndGapAreStable(t *testing.T) {
	program := compileProgram(t, twoTrackDocument(), DefaultConfig(1, 1))
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{
		"asset-red":  mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535}),
		"asset-blue": mustSurface(t, 1, 1, cpucompositor.Pixel{B: 32768, A: 32768}),
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(2, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result.Surface, []cpucompositor.Pixel{{R: 32767, B: 32768, A: 65535}})
	requests := provider.Requests()
	if len(requests) != 2 || requests[0].TrackIndex != 0 || requests[1].TrackIndex != 1 {
		t.Fatalf("provider order = %+v", requests)
	}
	before := len(requests)
	gap, err := program.RenderAt(context.Background(), "timeline", rt(20, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, gap.Surface, []cpucompositor.Pixel{{}})
	if len(gap.Sources) != 0 || len(provider.Requests()) != before {
		t.Fatalf("gap requested sources: result=%+v requests=%+v", gap, provider.Requests())
	}
}

func TestCompileFreezesPinnedAssetsConfigAndExactRetime(t *testing.T) {
	document := oneTrackDocument(documentv2.AssetKindVideo)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(5, 1)
	config := DefaultConfig(1, 1)
	config.Source.LinearVideoRetime = true
	config.BuildFingerprint = "compositor-build-a"
	config.SourceProfiles.Video.DecodeProfile = "vendor.decode.ffmpeg-8.v1"
	program := compileProgram(t, document, config)
	frozenHash := program.DocumentHash()
	document.Assets[0].ContentHash = blueAssetHash
	document.Assets[0].ID = "changed"
	document.Timelines[0].Tracks[0].Clips[0].AssetID = "changed"
	config.Width = 9
	config.SourceProfiles.Video.DecodeProfile = "vendor.decode.changed.v1"
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{
		"asset-red": mustSurface(t, 1, 1, cpucompositor.Pixel{R: 1, A: 1}),
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(4, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.Requests()[0]
	if result.DocumentHash != frozenHash || request.DocumentHash != frozenHash || request.AssetID != "asset-red" ||
		request.AssetContentHash != redAssetHash || request.SourceTime != rt(2, 1) || request.DecodeProfile != "vendor.decode.ffmpeg-8.v1" ||
		program.Config().Width != 1 || program.Config().BuildFingerprint != "compositor-build-a" {
		t.Fatalf("compiled snapshot changed: result=%+v request=%+v config=%+v", result, request, program.Config())
	}
}

func TestCompileFailsClosedForUnpinnedAndUnsupportedSemantics(t *testing.T) {
	unpinned := oneTrackDocument(documentv2.AssetKindImage)
	unpinned.Assets[0].ContentHash = ""
	if program, diagnostics := Compile(unpinned, DefaultConfig(1, 1)); program != nil || len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticCodeAssetUnpinned {
		t.Fatalf("unpinned compile = program=%v diagnostics=%+v", program, diagnostics)
	}
	tests := []struct {
		name string
		doc  documentv2.Document
	}{
		{name: "rendering extension", doc: withRenderingExtension(oneTrackDocument(documentv2.AssetKindImage))},
		{name: "text", doc: textDocument()},
		{name: "vector", doc: vectorDocument()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program, diagnostics := Compile(test.doc, DefaultConfig(1, 1))
			if program != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("compile = program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}
	retimed := oneTrackDocument(documentv2.AssetKindVideo)
	retimed.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(5, 1)
	if program, diagnostics := Compile(retimed, DefaultConfig(1, 1)); program != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("unauthorized retime compile = program=%v diagnostics=%+v", program, diagnostics)
	}
}

func TestCapabilitiesAreExactAndDefensive(t *testing.T) {
	capabilities, err := Capabilities(DefaultConfig(1920, 1080))
	if err != nil {
		t.Fatal(err)
	}
	wantFeatures := map[videoedit.CapabilityID]bool{
		documentv2.CapabilityTransformV1: true, documentv2.CapabilityCropV1: true,
		documentv2.CapabilityOpacityV1: true, documentv2.CapabilityKeyframesV1: true,
		documentv2.CapabilityInterpolationHoldV1: true, documentv2.CapabilityInterpolationLinearV1: true,
		documentv2.CapabilityInterpolationCubicV1:               true,
		videoedit.CapabilityID(documentv2.TransitionDissolveV1): true,
	}
	if len(capabilities.Features) != len(wantFeatures) || len(capabilities.AssetKinds) != 2 || len(capabilities.TrackKinds) != 1 ||
		capabilities.TrackKinds[0] != videoedit.TrackKindVideo || capabilities.MaxWidth != 1920 || capabilities.MaxHeight != 1080 {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	for _, feature := range capabilities.Features {
		if !wantFeatures[feature] {
			t.Fatalf("unexpected feature %q in %+v", feature, capabilities.Features)
		}
	}
	capabilities.Features[0] = "mutated.v1"
	again, err := Capabilities(DefaultConfig(1920, 1080))
	if err != nil || again.Features[0] == "mutated.v1" {
		t.Fatalf("capability alias: %+v err=%v", again, err)
	}
	config := DefaultConfig(1, 1)
	config.Source.LinearVideoRetime = true
	withRetime, err := Capabilities(config)
	if err != nil || !containsFeature(withRetime.Features, videoedit.CapabilityVideoLinearRetime) {
		t.Fatalf("explicit retime capability = %+v err=%v", withRetime, err)
	}
}

func TestHostileProviderResponsesFailWithoutPartialResult(t *testing.T) {
	program := compileProgram(t, oneTrackDocument(documentv2.AssetKindImage), DefaultConfig(1, 1))
	source := mustSurface(t, 1, 1, cpucompositor.Pixel{R: 7, A: 9})
	tests := []struct {
		name   string
		mutate func(*NativeSourceFrame)
		want   error
	}{
		{name: "response contract", mutate: func(frame *NativeSourceFrame) { frame.ContractVersion++ }, want: ErrSourceMismatch},
		{name: "document scope", mutate: func(frame *NativeSourceFrame) { frame.Request.DocumentHash = "sha256:" + strings.Repeat("f", 64) }, want: ErrSourceMismatch},
		{name: "timeline index", mutate: func(frame *NativeSourceFrame) { frame.Request.TimelineIndex++ }, want: ErrSourceMismatch},
		{name: "asset hash request", mutate: func(frame *NativeSourceFrame) { frame.Request.AssetContentHash = blueAssetHash }, want: ErrSourceMismatch},
		{name: "asset hash response", mutate: func(frame *NativeSourceFrame) { frame.AssetContentHash = blueAssetHash }, want: ErrSourceMismatch},
		{name: "pixel hash lie", mutate: func(frame *NativeSourceFrame) { frame.PixelHash = "sha256:" + strings.Repeat("0", 64) }, want: ErrSourceMismatch},
		{name: "pixel hash malformed", mutate: func(frame *NativeSourceFrame) { frame.PixelHash = "SHA256:bad" }, want: ErrSourceInvalid},
		{name: "width lie", mutate: func(frame *NativeSourceFrame) { frame.Width++ }, want: ErrSourceInvalid},
		{name: "nil surface", mutate: func(frame *NativeSourceFrame) { frame.Surface = nil }, want: ErrSourceInvalid},
		{name: "source profile", mutate: func(frame *NativeSourceFrame) { frame.SourceProfile = "vendor.source.v1" }, want: ErrSourceMismatch},
		{name: "surface profile", mutate: func(frame *NativeSourceFrame) { frame.SurfaceProfile = "vendor.surface.v1" }, want: ErrSourceMismatch},
		{name: "decode profile", mutate: func(frame *NativeSourceFrame) { frame.DecodeProfile = "vendor.decode.v1" }, want: ErrSourceMismatch},
		{name: "selection profile", mutate: func(frame *NativeSourceFrame) { frame.SelectionProfile = "vendor.selection.v1" }, want: ErrSourceMismatch},
		{name: "color profile", mutate: func(frame *NativeSourceFrame) { frame.ColorProfile = "vendor.color.v1" }, want: ErrSourceMismatch},
		{name: "normalization profile", mutate: func(frame *NativeSourceFrame) { frame.NormalizationProfile = "vendor.normalization.v1" }, want: ErrSourceMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := NativeSourceProviderFunc(func(_ context.Context, request NativeSourceRequest) (NativeSourceFrame, error) {
				frame := validFrame(t, request, source)
				test.mutate(&frame)
				return frame, nil
			})
			result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
			if result.Surface != nil || result.PixelHash != "" || !errors.Is(err, test.want) {
				t.Fatalf("result=%+v err=%v, want %v", result, err, test.want)
			}
		})
	}
}

func TestProviderFailureCancellationTypedNilAndResourcePreflight(t *testing.T) {
	program := compileProgram(t, oneTrackDocument(documentv2.AssetKindImage), DefaultConfig(1, 1))
	var nilProgram *Program
	if result, err := nilProgram.RenderAt(context.Background(), "timeline", rt(0, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
		return NativeSourceFrame{}, nil
	})); result.Surface != nil || !errors.Is(err, ErrProgramUnavailable) {
		t.Fatalf("nil program result=%+v err=%v", result, err)
	}
	if result, err := program.RenderAt(nil, "timeline", rt(0, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
		return NativeSourceFrame{}, nil
	})); result.Surface != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil context result=%+v err=%v", result, err)
	}
	var typedNil *syntheticProvider
	var typedNilFunc NativeSourceProviderFunc
	for name, provider := range map[string]NativeSourceProvider{"nil": nil, "typed pointer": typedNil, "typed function": typedNilFunc} {
		t.Run(name, func(t *testing.T) {
			result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
			if result.Surface != nil || !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	providerFailure := errors.New("decoder unavailable")
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
		return NativeSourceFrame{}, providerFailure
	}))
	if result.Surface != nil || !errors.Is(err, ErrSourceFailed) || !errors.Is(err, providerFailure) {
		t.Fatalf("provider failure result=%+v err=%v", result, err)
	}
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		result, err = program.RenderAt(context.Background(), "timeline", rt(0, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
			return NativeSourceFrame{}, cancellation
		}))
		if result.Surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, cancellation) || errors.Is(err, ErrSourceFailed) {
			t.Fatalf("provider cancellation %v result=%+v err=%v", cancellation, result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var called atomic.Bool
	result, err = program.RenderAt(ctx, "timeline", rt(0, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
		called.Store(true)
		return NativeSourceFrame{}, nil
	}))
	if result.Surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) || called.Load() {
		t.Fatalf("pre-cancel result=%+v err=%v called=%t", result, err, called.Load())
	}

	limitedConfig := DefaultConfig(1, 1)
	limitedConfig.Limits.MaxSourceRequests = 1
	limited := compileProgram(t, transitionDocument(), limitedConfig)
	called.Store(false)
	result, err = limited.RenderAt(context.Background(), "timeline", rt(5, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
		called.Store(true)
		return NativeSourceFrame{}, nil
	}))
	if result.Surface != nil || !errors.Is(err, ErrResourceLimit) || called.Load() {
		t.Fatalf("request preflight result=%+v err=%v called=%t", result, err, called.Load())
	}
	layerConfig := DefaultConfig(1, 1)
	layerConfig.Limits.MaxLayers = 1
	layerLimited := compileProgram(t, twoTrackDocument(), layerConfig)
	called.Store(false)
	result, err = layerLimited.RenderAt(context.Background(), "timeline", rt(0, 1), NativeSourceProviderFunc(func(context.Context, NativeSourceRequest) (NativeSourceFrame, error) {
		called.Store(true)
		return NativeSourceFrame{}, nil
	}))
	if result.Surface != nil || !errors.Is(err, ErrResourceLimit) || called.Load() {
		t.Fatalf("layer preflight result=%+v err=%v called=%t", result, err, called.Load())
	}
}

func TestSourceByteLimitAndConfigValidation(t *testing.T) {
	config := DefaultConfig(1, 1)
	config.Limits.MaxSourceBytes = cpucompositor.BytesPerPixel
	program := compileProgram(t, oneTrackDocument(documentv2.AssetKindImage), config)
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{
		"asset-red": mustSurface(t, 2, 1, cpucompositor.Pixel{R: 1, A: 1}),
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
	if result.Surface != nil || !errors.Is(err, ErrSourceLimit) {
		t.Fatalf("source limit result=%+v err=%v", result, err)
	}
	dimensionConfig := DefaultConfig(1, 1)
	dimensionConfig.Limits.Raster.MaxWidth = 1
	dimensionLimited := compileProgram(t, oneTrackDocument(documentv2.AssetKindImage), dimensionConfig)
	result, err = dimensionLimited.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
	if result.Surface != nil || !errors.Is(err, ErrSourceLimit) {
		t.Fatalf("source dimension limit result=%+v err=%v", result, err)
	}
	invalid := DefaultConfig(1, 1)
	invalid.SourceProfiles.Image.ColorProfile = "not versioned"
	if _, err := Capabilities(invalid); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid profile error = %v", err)
	}
	invalid = DefaultConfig(1, 1)
	invalid.Source.Video = false
	invalid.Source.LinearVideoRetime = true
	if _, err := Capabilities(invalid); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("retime without video error = %v", err)
	}
	invalid = DefaultConfig(2, 1)
	invalid.Limits.Target.MaxWidth = 1
	if compiled, diagnostics := Compile(oneTrackDocument(documentv2.AssetKindImage), invalid); compiled != nil || len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticCodeConfigInvalid {
		t.Fatalf("invalid dimensions program=%v diagnostics=%+v", compiled, diagnostics)
	}
	for name, mutate := range map[string]func(*Config){
		"document nodes":  func(config *Config) { config.Limits.MaxDocumentNodes = HardMaxDocumentNodes + 1 },
		"timeline tracks": func(config *Config) { config.Limits.MaxTracksPerTimeline = HardMaxTracksPerTimeline + 1 },
		"document bytes":  func(config *Config) { config.Limits.MaxCanonicalDocumentBytes = HardMaxDocumentBytes + 1 },
	} {
		t.Run("invalid "+name, func(t *testing.T) {
			config := DefaultConfig(1, 1)
			mutate(&config)
			if _, capabilityErr := Capabilities(config); !errors.Is(capabilityErr, ErrInvalidConfig) {
				t.Fatalf("capability error = %v", capabilityErr)
			}
		})
	}
	for _, fingerprint := range []string{" leading", "trailing ", "line\nbreak", "non-ascii-雪"} {
		invalid = DefaultConfig(1, 1)
		invalid.BuildFingerprint = fingerprint
		if _, capabilityErr := Capabilities(invalid); !errors.Is(capabilityErr, ErrInvalidConfig) {
			t.Fatalf("fingerprint %q error = %v", fingerprint, capabilityErr)
		}
	}
}

func TestDocumentPreflightEncodedEstimateNeverUndercountsCanonicalBytes(t *testing.T) {
	documents := []documentv2.Document{
		oneTrackDocument(documentv2.AssetKindImage),
		transitionDocument(),
		textDocument(),
		vectorDocument(),
		withRenderingExtension(oneTrackDocument(documentv2.AssetKindImage)),
	}
	documents[0].Metadata = map[string]json.RawMessage{
		"escaped\"<key>": json.RawMessage(`{"nested":"<>&\\u0000","array":[1,2,3]}`),
	}
	for index, document := range documents {
		canonical, err := documentv2.CanonicalJSON(document)
		if err != nil {
			t.Fatalf("document %d canonicalize: %v", index, err)
		}
		measure := documentMeasure{nodeLimit: HardMaxDocumentNodes, byteLimit: HardMaxDocumentBytes}
		if err := measure.addNodes(1); err != nil {
			t.Fatal(err)
		}
		if err := measure.walk(reflect.ValueOf(document)); err != nil {
			t.Fatalf("document %d measure: %v", index, err)
		}
		if measure.encodedUpper < uint64(len(canonical)) {
			t.Fatalf("document %d estimate=%d undercounts canonical=%d", index, measure.encodedUpper, len(canonical))
		}
	}
}

func TestCompileDocumentLimitsRunBeforeFreezeAndEvaluatorAllocation(t *testing.T) {
	tests := []struct {
		name     string
		document documentv2.Document
		mutate   func(*Config)
	}{
		{
			name: "structural nodes", document: oneTrackDocument(documentv2.AssetKindImage),
			mutate: func(config *Config) { config.Limits.MaxDocumentNodes = 1 },
		},
		{
			name: "timeline tracks", document: twoTrackDocument(),
			mutate: func(config *Config) { config.Limits.MaxTracksPerTimeline = 1 },
		},
		{
			name: "encoded bytes", document: oneTrackDocument(documentv2.AssetKindImage),
			mutate: func(config *Config) { config.Limits.MaxCanonicalDocumentBytes = 1 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig(1, 1)
			test.mutate(&config)
			program, diagnostics := Compile(test.document, config)
			if program != nil || len(diagnostics) != 1 || diagnostics[0].Code != DiagnosticCodeDocumentLimit || diagnostics[0].Severity != documentv2.SeverityError {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}
}

func TestRasterResourceFailureKeepsStableCompositorClassification(t *testing.T) {
	config := DefaultConfig(1, 1)
	// The default visual anchor is 1/2, so one exact bit cannot represent its denominator.
	config.Limits.Raster.MaxExactBits = 1
	program := compileProgram(t, oneTrackDocument(documentv2.AssetKindImage), config)
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{
		"asset-red": mustSurface(t, 1, 1, cpucompositor.Pixel{R: 1, A: 1}),
	}))
	var typed *Error
	if result.Surface != nil || !errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrRasterFailed) ||
		!errors.As(err, &typed) || typed.Code != CodeResourceLimit {
		t.Fatalf("result=%+v err=%v typed=%+v", result, err, typed)
	}
}

func TestSeekOrderConcurrentReadersAndProviderSurfaceRemainStable(t *testing.T) {
	program := compileProgram(t, transitionDocument(), DefaultConfig(1, 1))
	red := mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535})
	blue := mustSurface(t, 1, 1, cpucompositor.Pixel{B: 65535, A: 65535})
	redBefore, _ := red.CanonicalHash()
	blueBefore, _ := blue.CanonicalHash()
	provider := newSyntheticProvider(map[documentv2.ID]*cpucompositor.Surface{"asset-red": red, "asset-blue": blue})
	times := []documentv2.RationalTime{rt(7, 1), rt(4, 1), rt(5, 1), rt(0, 1), rt(6, 1), rt(20, 1)}
	baseline := make(map[documentv2.RationalTime]string, len(times))
	for _, at := range times {
		result, err := program.RenderAt(context.Background(), "timeline", at, provider)
		if err != nil {
			t.Fatal(err)
		}
		baseline[at] = result.PixelHash
	}
	const workers = 16
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
					errorsOut <- fmt.Errorf("at %v hash=%s want=%s", at, result.PixelHash, baseline[at])
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
	redAfter, _ := red.CanonicalHash()
	blueAfter, _ := blue.CanonicalHash()
	if redAfter != redBefore || blueAfter != blueBefore {
		t.Fatalf("provider surfaces mutated: red %s/%s blue %s/%s", redBefore, redAfter, blueBefore, blueAfter)
	}
}

type rasterVectorCase struct {
	Name           string                  `json:"name"`
	Scenario       string                  `json:"scenario"`
	At             documentv2.RationalTime `json:"at"`
	ExpectedPixels []cpucompositor.Pixel   `json:"expected_pixels"`
	ExpectedHash   string                  `json:"expected_hash"`
}

type rasterVectors struct {
	Profile               string             `json:"profile"`
	SourceContractVersion int                `json:"source_contract_version"`
	Width                 int                `json:"width"`
	Height                int                `json:"height"`
	Cases                 []rasterVectorCase `json:"cases"`
}

func TestCanonicalRasterCompositorVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/compositor-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture rasterVectors
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) || fixture.Profile != ProfileV1 ||
		fixture.SourceContractVersion != NativeSourceContractVersion || len(fixture.Cases) == 0 {
		t.Fatalf("invalid/non-canonical vectors: fixture=%+v\ncanonical=%s", fixture, canonical)
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			var document documentv2.Document
			var surfaces map[documentv2.ID]*cpucompositor.Surface
			switch vector.Scenario {
			case "gap":
				document = oneTrackDocument(documentv2.AssetKindImage)
				surfaces = map[documentv2.ID]*cpucompositor.Surface{"asset-red": mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535})}
			case "z_order":
				document = twoTrackDocument()
				surfaces = map[documentv2.ID]*cpucompositor.Surface{
					"asset-red":  mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535}),
					"asset-blue": mustSurface(t, 1, 1, cpucompositor.Pixel{B: 32768, A: 32768}),
				}
			case "transition":
				document = transitionDocument()
				surfaces = map[documentv2.ID]*cpucompositor.Surface{
					"asset-red":  mustSurface(t, 1, 1, cpucompositor.Pixel{R: 65535, A: 65535}),
					"asset-blue": mustSurface(t, 1, 1, cpucompositor.Pixel{B: 65535, A: 65535}),
				}
			default:
				t.Fatalf("unknown scenario %q", vector.Scenario)
			}
			program := compileProgram(t, document, DefaultConfig(fixture.Width, fixture.Height))
			result, err := program.RenderAt(context.Background(), "timeline", vector.At, newSyntheticProvider(surfaces))
			if err != nil {
				t.Fatal(err)
			}
			assertPixels(t, result.Surface, vector.ExpectedPixels)
			if result.PixelHash != vector.ExpectedHash {
				t.Fatalf("hash=%s want=%s", result.PixelHash, vector.ExpectedHash)
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
		t.Fatalf("%v\ncanonical descriptor=%s", err, canonical)
	}
	var fixture ProfileDescriptor
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatalf("%v\ncanonical descriptor=%s", err, canonical)
	}
	fixtureCanonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture, Descriptor()) || !bytes.Equal(bytes.TrimSpace(raw), fixtureCanonical) || !bytes.Equal(canonical, fixtureCanonical) {
		t.Fatalf("fixture differs\nwant=%s\ngot=%s", canonical, bytes.TrimSpace(raw))
	}
	encoded, err := json.Marshal(Descriptor())
	if err != nil || len(encoded) == 0 {
		t.Fatalf("descriptor marshal=%s err=%v", encoded, err)
	}
}

func FuzzHostileSourceProtocolNeverPublishesPartialResult(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1})
	f.Add([]byte{3, 5, 8, 13, 21})
	f.Fuzz(func(t *testing.T, input []byte) {
		program := compileProgram(t, oneTrackDocument(documentv2.AssetKindImage), DefaultConfig(1, 1))
		surface := mustSurface(t, 1, 1, cpucompositor.Pixel{R: 7, G: 5, B: 3, A: 9})
		provider := NativeSourceProviderFunc(func(_ context.Context, request NativeSourceRequest) (NativeSourceFrame, error) {
			frame, frameErr := validFrameNoTest(request, surface)
			if frameErr != nil {
				return NativeSourceFrame{}, frameErr
			}
			for index, value := range input {
				if index >= 64 {
					break
				}
				switch value % 20 {
				case 0:
					// Preserve one valid path in every corpus.
				case 1:
					frame.ContractVersion = int(value)
				case 2:
					frame.Request.ContractVersion = int(value)
				case 3:
					frame.Request.DocumentHash = blueAssetHash
				case 4:
					frame.Request.TimelineID = documentv2.ID(fmt.Sprintf("timeline-%d", value))
				case 5:
					frame.Request.TrackIndex = int(value)
				case 6:
					frame.Request.ClipIndex = int(value)
				case 7:
					frame.Request.AssetIndex = int(value)
				case 8:
					frame.Request.SourceTime = rt(int64(value), 1)
				case 9:
					frame.Width = int(value)
				case 10:
					frame.Height = int(value)
				case 11:
					frame.AssetContentHash = blueAssetHash
				case 12:
					frame.PixelHash = blueAssetHash
				case 13:
					frame.DecodeProfile = fmt.Sprintf("fuzz.decode.%d.v1", value)
				case 14:
					frame.SelectionProfile = fmt.Sprintf("fuzz.selection.%d.v1", value)
				case 15:
					frame.ColorProfile = fmt.Sprintf("fuzz.color.%d.v1", value)
				case 16:
					frame.NormalizationProfile = fmt.Sprintf("fuzz.normalization.%d.v1", value)
				case 17:
					frame.Surface = nil
				case 18:
					return frame, context.Canceled
				case 19:
					return frame, errors.New("fuzz provider failure")
				}
			}
			return frame, nil
		})
		result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
		if err != nil {
			if result.Surface != nil || result.PixelHash != "" || result.DocumentHash != "" || len(result.Sources) != 0 {
				t.Fatalf("partial result=%+v err=%v", result, err)
			}
			return
		}
		actualHash, hashErr := result.Surface.CanonicalHash()
		if hashErr != nil || result.PixelHash != actualHash || len(result.Sources) != 1 {
			t.Fatalf("successful result=%+v hash=%q err=%v", result, actualHash, hashErr)
		}
	})
}

type syntheticProvider struct {
	mu       sync.Mutex
	surfaces map[documentv2.ID]*cpucompositor.Surface
	requests []NativeSourceRequest
}

func newSyntheticProvider(surfaces map[documentv2.ID]*cpucompositor.Surface) *syntheticProvider {
	return &syntheticProvider{surfaces: surfaces}
}

func (provider *syntheticProvider) Frame(_ context.Context, request NativeSourceRequest) (NativeSourceFrame, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	surface := provider.surfaces[request.AssetID]
	if surface == nil {
		return NativeSourceFrame{}, fmt.Errorf("asset %q missing", request.AssetID)
	}
	return validFrameNoTest(request, surface)
}

func (provider *syntheticProvider) Requests() []NativeSourceRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]NativeSourceRequest(nil), provider.requests...)
}

func validFrame(t *testing.T, request NativeSourceRequest, surface *cpucompositor.Surface) NativeSourceFrame {
	t.Helper()
	frame, err := validFrameNoTest(request, surface)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func validFrameNoTest(request NativeSourceRequest, surface *cpucompositor.Surface) (NativeSourceFrame, error) {
	hash, err := surface.CanonicalHash()
	if err != nil {
		return NativeSourceFrame{}, err
	}
	return NativeSourceFrame{
		ContractVersion: NativeSourceContractVersion, Request: request,
		Width: surface.Width(), Height: surface.Height(), AssetContentHash: request.AssetContentHash, PixelHash: hash,
		DecodeProfile: request.DecodeProfile, SelectionProfile: request.SelectionProfile,
		ColorProfile: request.ColorProfile, NormalizationProfile: request.NormalizationProfile,
		SourceProfile: request.SourceProfile, SurfaceProfile: request.SurfaceProfile, Surface: surface,
	}, nil
}

func compileProgram(t *testing.T, document documentv2.Document, config Config) *Program {
	t.Helper()
	program, diagnostics := Compile(document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile program=%v diagnostics=%+v", program, diagnostics)
	}
	return program
}

func oneTrackDocument(kind documentv2.AssetKind) documentv2.Document {
	duration := rt(10, 1)
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset-red", Kind: kind, URI: "red.bin", ContentHash: redAssetHash, Duration: &duration}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track-red", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip-red", AssetID: "asset-red", TimelineRange: timeRange(0, 10), SourceRange: timeRange(0, 10)}},
		}}}},
	}
}

func twoTrackDocument() documentv2.Document {
	document := oneTrackDocument(documentv2.AssetKindImage)
	duration := rt(10, 1)
	document.Assets = append(document.Assets, documentv2.Asset{ID: "asset-blue", Kind: documentv2.AssetKindImage, URI: "blue.bin", ContentHash: blueAssetHash, Duration: &duration})
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{
		ID: "track-blue", Kind: documentv2.TrackKindVideo,
		Clips: []documentv2.Clip{{ID: "clip-blue", AssetID: "asset-blue", TimelineRange: timeRange(0, 10), SourceRange: timeRange(0, 10)}},
	})
	return document
}

func transitionDocument() documentv2.Document {
	duration := rt(20, 1)
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "transition-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "asset-red", Kind: documentv2.AssetKindVideo, URI: "red.mp4", ContentHash: redAssetHash, Duration: &duration},
			{ID: "asset-blue", Kind: documentv2.AssetKindVideo, URI: "blue.mp4", ContentHash: blueAssetHash, Duration: &duration},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{
				{ID: "clip-red", AssetID: "asset-red", TimelineRange: timeRange(0, 5), SourceRange: timeRange(1, 5)},
				{ID: "clip-blue", AssetID: "asset-blue", TimelineRange: timeRange(5, 5), SourceRange: timeRange(1, 5)},
			},
		}}, Transitions: []documentv2.Transition{{
			ID: "transition", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "clip-red", IncomingClipID: "clip-blue", PreDuration: rt(1, 1), PostDuration: rt(2, 1),
		}}}},
	}
}

func withRenderingExtension(document documentv2.Document) documentv2.Document {
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{
		Namespace: "vendor.blur.v1", AffectsRendering: true, RequiredCapability: "vendor.blur.v1", Value: json.RawMessage(`{"radius":1}`),
	}}}
	return document
}

func textDocument() documentv2.Document {
	fontHash := "sha256:" + strings.Repeat("c", 64)
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "text-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "font", Kind: documentv2.AssetKindFont, URI: "font.ttf", ContentHash: fontHash},
			{ID: "text", Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{
				Text: "Cutly", FontAssetID: "font", FontSize: ratio(24, 1), Fill: documentv2.ColorRGBA16{R: 65535, A: 65535},
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
			Paths: []documentv2.VectorPath{{ID: "path", FillRule: documentv2.VectorFillNonZero, Fill: &fill, Commands: []documentv2.VectorCommand{
				{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: zero, Y: zero}}},
				{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: ten, Y: zero}}},
				{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: one, Y: ten}}}, {Op: documentv2.VectorClose},
			}}},
		}}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "vector-track", Kind: documentv2.TrackKindVector,
			Clips: []documentv2.Clip{{ID: "vector-clip", AssetID: "vector", TimelineRange: timeRange(0, 1), SourceRange: timeRange(0, 1)}},
		}}}},
	}
}

func containsFeature(values []videoedit.CapabilityID, wanted videoedit.CapabilityID) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
	return mustSurfacePixels(t, width, height, pixels)
}

func mustSurfacePixels(t *testing.T, width, height int, pixels []cpucompositor.Pixel) *cpucompositor.Surface {
	t.Helper()
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

func assertPixels(t *testing.T, surface *cpucompositor.Surface, wanted []cpucompositor.Pixel) {
	t.Helper()
	if surface == nil {
		t.Fatal("surface is nil")
	}
	if surface.Width()*surface.Height() != len(wanted) {
		t.Fatalf("surface geometry=%dx%d wanted pixels=%d", surface.Width(), surface.Height(), len(wanted))
	}
	for index, want := range wanted {
		got, err := surface.Pixel(index%surface.Width(), index/surface.Width())
		if err != nil || got != want {
			t.Fatalf("pixel %d=%+v want=%+v err=%v", index, got, want, err)
		}
	}
}
