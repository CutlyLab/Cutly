package nativepngsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func TestNativePNGTransformCropOpacityEndToEnd(t *testing.T) {
	encoded := decodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAAXNSR0ICQMB9xQAAABZJREFUeJwACQD2/wP/AAD/AUAgAQMADhwCZN2J4wgAAAAASUVORK5CYII=")
	document := oneImageDocument(contentHash(encoded))
	half, one, zero := ratio(1, 2), ratio(1, 1), ratio(0, 1)
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		Transform: &documentv2.TransformProperty{
			Position: documentv2.Point{X: one, Y: zero}, Scale: documentv2.Point{X: one, Y: one},
			RotationDegrees: zero, Anchor: documentv2.Point{X: half, Y: half},
		},
		Crop:    &documentv2.CropProperty{Rect: documentv2.Rect{X: half, Y: zero, Width: half, Height: one}},
		Opacity: &documentv2.OpacityProperty{Value: half},
	}
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	config := rastercompositor.DefaultConfig(3, 1)
	config.SourceProfiles.Image = ImageSourceProfiles()
	program, diagnostics := rastercompositor.Compile(document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	// The right source pixel is partially transparent and therefore proves decode/color,
	// native-size crop/transform and post-raster opacity in one path.
	want := []cpucompositor.Pixel{{}, {}, {R: 3550, G: 844, B: 238, A: 16448}}
	assertPixels(t, result.Surface, want)
	if result.PixelHash != "sha256:fc213ed7bb5062893d0ad7b29c112b13aac09cbbf684b48dfae48e694e8a25e2" {
		t.Fatalf("transform vector hash=%s", result.PixelHash)
	}
	if result.DocumentHash != provider.DocumentHash() || len(result.Sources) != 1 ||
		result.Sources[0].DecodeProfile != pngdecode.ProfileV1 ||
		result.Sources[0].SelectionProfile != SelectionProfileV1 ||
		result.Sources[0].NormalizationProfile != NormalizationProfileV1 {
		t.Fatalf("result bindings = %+v", result)
	}
	manifest := provider.Manifest()
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Profile != ProfileV1 || len(manifest.Assets) != 1 ||
		manifest.Assets[0].Width != 2 || manifest.Assets[0].Height != 1 || manifest.Assets[0].AssetIndex != 0 {
		t.Fatalf("manifest=%+v", manifest)
	}
	manifest.Assets[0].PixelHash = "mutated"
	if provider.Manifest().Assets[0].PixelHash == "mutated" {
		t.Fatal("manifest aliases provider storage")
	}
	for index := range encoded {
		encoded[index] = 0
	}
	again, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), provider)
	if err != nil || again.PixelHash != result.PixelHash {
		t.Fatalf("owned provider result=%+v err=%v", again, err)
	}
}

func TestNativePNGTransitionEndToEnd(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	blue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 1)
	document := transitionDocument(contentHash(red), contentHash(blue))
	provider, err := Compile(context.Background(), document, []Binding{
		bindingFor(t, document, "asset-red", red), bindingFor(t, document, "asset-blue", blue),
	}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	config := rastercompositor.DefaultConfig(1, 1)
	config.SourceProfiles.Image = ImageSourceProfiles()
	program, diagnostics := rastercompositor.Compile(document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(5, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertPixels(t, result.Surface, []cpucompositor.Pixel{{R: 32768, B: 32768, A: 65535}})
	if result.PixelHash != "sha256:9cad8b14070f7fab5505adc041b1bdd7d70d5fda2736ac11080538536a886a26" {
		t.Fatalf("transition vector hash=%s", result.PixelHash)
	}
	if len(result.Sources) != 2 || result.Sources[0].Request.ClipID != "clip-red" || result.Sources[1].Request.ClipID != "clip-blue" {
		t.Fatalf("source order=%+v", result.Sources)
	}
	assets := provider.Manifest().Assets
	if len(assets) != 2 || assets[0].AssetID != "asset-blue" || assets[1].AssetID != "asset-red" {
		t.Fatalf("manifest order=%+v", assets)
	}
}

func TestCompileFreezesCanonicalDocumentAndOwnsDecodedPixels(t *testing.T) {
	encoded := strictPNG(t, 1, 1, color.NRGBA{R: 255, G: 64, A: 255}, 0)
	document := oneImageDocument(contentHash(encoded))
	wantHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(t, document, "timeline", "track", "clip", "asset", 0, 0, 0, 0)
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provider.DocumentHash() != wantHash || provider.Manifest().DocumentHash != wantHash {
		t.Fatalf("provider hash=%q manifest hash=%q want=%q", provider.DocumentHash(), provider.Manifest().DocumentHash, wantHash)
	}

	// Compile must retain neither the caller's nested document slices nor encoded PNG bytes.
	document.Assets[0].ID = "mutated-asset"
	document.Timelines[0].ID = "mutated-timeline"
	for index := range encoded {
		encoded[index] = 0
	}
	frame, err := provider.Frame(context.Background(), request)
	if err != nil || frame.Surface == nil || frame.Request != request || frame.AssetContentHash != request.AssetContentHash {
		t.Fatalf("frozen frame=%+v err=%v", frame, err)
	}
}

func TestCompileBindingDocumentAndMixedAssetFailuresAreAtomic(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	blue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 0)
	base := oneImageDocument(contentHash(red))
	extra := base
	extra.Assets = append(extra.Assets, documentv2.Asset{ID: "extra", Kind: documentv2.AssetKindImage, URI: "extra.png", ContentHash: contentHash(blue)})
	tests := []struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{
		{name: "missing", document: base, config: DefaultConfig(), want: ErrBindingMissing},
		{name: "duplicate", document: base, bindings: []Binding{bindingFor(t, base, "asset", red), bindingFor(t, base, "asset", red)}, config: DefaultConfig(), want: ErrBindingInvalid},
		{name: "unknown", document: base, bindings: []Binding{bindingFor(t, base, "unknown", red)}, config: DefaultConfig(), want: ErrBindingInvalid},
		{name: "cross document", document: base, bindings: []Binding{{DocumentHash: contentHash([]byte("other")), AssetID: "asset", EncodedPNG: red}}, config: DefaultConfig(), want: ErrDocumentMismatch},
		{name: "extra", document: extra, bindings: []Binding{bindingFor(t, extra, "asset", red), bindingFor(t, extra, "extra", blue)}, config: DefaultConfig(), want: ErrBindingExtra},
		{name: "content mismatch", document: base, bindings: []Binding{bindingFor(t, base, "asset", blue)}, config: DefaultConfig(), want: ErrContentMismatch},
		{name: "decode", document: oneImageDocument(contentHash([]byte("bad"))), bindings: []Binding{bindingFor(t, oneImageDocument(contentHash([]byte("bad"))), "asset", []byte("bad"))}, config: DefaultConfig(), want: ErrDecodeFailed},
		{name: "unpinned", document: oneImageDocument(""), bindings: []Binding{{AssetID: "asset", EncodedPNG: red}}, config: DefaultConfig(), want: ErrDocumentInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := Compile(context.Background(), test.document, test.bindings, test.config)
			if provider != nil || !errors.Is(err, test.want) {
				t.Fatalf("provider=%v err=%v want=%v", provider, err, test.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if provider, err := Compile(canceled, base, []Binding{bindingFor(t, base, "asset", red)}, DefaultConfig()); provider != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled compile provider=%v err=%v", provider, err)
	}

	mixed := mixedImageVideoDocument(contentHash(red))
	provider, err := Compile(context.Background(), mixed, []Binding{bindingFor(t, mixed, "asset", red)}, DefaultConfig())
	if err != nil || provider == nil || len(provider.Manifest().Assets) != 1 {
		t.Fatalf("mixed provider=%v err=%v", provider, err)
	}
	request := validRequest(t, mixed, "image-timeline", "image-track", "clip", "asset", 1, 0, 0, 0)
	request.AssetKind = documentv2.AssetKindVideo
	if frame, frameErr := provider.Frame(context.Background(), request); frame.Surface != nil || !errors.Is(frameErr, ErrRequestInvalid) {
		t.Fatalf("video request frame=%+v err=%v", frame, frameErr)
	}
}

func TestHostileRequestsFailClosedAndTimesAreFrozen(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(red))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	base := validRequest(t, document, "timeline", "track", "clip", "asset", 0, 0, 0, 0)
	if frame, err := provider.Frame(context.Background(), base); err != nil || frame.Surface == nil || frame.Request != base {
		t.Fatalf("valid frame=%+v err=%v", frame, err)
	}
	tests := []struct {
		name   string
		mutate func(*rastercompositor.NativeSourceRequest)
		want   error
	}{
		{name: "contract", mutate: func(value *rastercompositor.NativeSourceRequest) { value.ContractVersion++ }, want: ErrRequestMismatch},
		{name: "document", mutate: func(value *rastercompositor.NativeSourceRequest) { value.DocumentHash = contentHash([]byte("other")) }, want: ErrRequestMismatch},
		{name: "timeline", mutate: func(value *rastercompositor.NativeSourceRequest) { value.TimelineID = "other" }, want: ErrRequestMismatch},
		{name: "timeline index", mutate: func(value *rastercompositor.NativeSourceRequest) { value.TimelineIndex++ }, want: ErrRequestMismatch},
		{name: "track", mutate: func(value *rastercompositor.NativeSourceRequest) { value.TrackID = "other" }, want: ErrRequestMismatch},
		{name: "track index", mutate: func(value *rastercompositor.NativeSourceRequest) { value.TrackIndex++ }, want: ErrRequestMismatch},
		{name: "clip", mutate: func(value *rastercompositor.NativeSourceRequest) { value.ClipID = "other" }, want: ErrRequestMismatch},
		{name: "clip index", mutate: func(value *rastercompositor.NativeSourceRequest) { value.ClipIndex++ }, want: ErrRequestMismatch},
		{name: "asset", mutate: func(value *rastercompositor.NativeSourceRequest) { value.AssetID = "other" }, want: ErrRequestMismatch},
		{name: "asset index", mutate: func(value *rastercompositor.NativeSourceRequest) { value.AssetIndex++ }, want: ErrRequestMismatch},
		{name: "asset hash", mutate: func(value *rastercompositor.NativeSourceRequest) {
			value.AssetContentHash = contentHash([]byte("other"))
		}, want: ErrRequestMismatch},
		{name: "kind", mutate: func(value *rastercompositor.NativeSourceRequest) { value.AssetKind = documentv2.AssetKindVideo }, want: ErrRequestInvalid},
		{name: "decode profile", mutate: func(value *rastercompositor.NativeSourceRequest) { value.DecodeProfile = "vendor.decode.v1" }, want: ErrRequestMismatch},
		{name: "selection profile", mutate: func(value *rastercompositor.NativeSourceRequest) { value.SelectionProfile = "vendor.selection.v1" }, want: ErrRequestMismatch},
		{name: "source profile", mutate: func(value *rastercompositor.NativeSourceRequest) { value.SourceProfile = "vendor.source.v1" }, want: ErrRequestMismatch},
		{name: "surface profile", mutate: func(value *rastercompositor.NativeSourceRequest) { value.SurfaceProfile = "vendor.surface.v1" }, want: ErrRequestMismatch},
		{name: "color profile", mutate: func(value *rastercompositor.NativeSourceRequest) { value.ColorProfile = "vendor.color.v1" }, want: ErrRequestMismatch},
		{name: "normalization profile", mutate: func(value *rastercompositor.NativeSourceRequest) {
			value.NormalizationProfile = "vendor.normalization.v1"
		}, want: ErrRequestMismatch},
		{name: "timeline time", mutate: func(value *rastercompositor.NativeSourceRequest) { value.TimelineTime = rt(1, 1) }, want: ErrRequestMismatch},
		{name: "local time", mutate: func(value *rastercompositor.NativeSourceRequest) { value.LocalTime = rt(1, 1) }, want: ErrRequestMismatch},
		{name: "source time", mutate: func(value *rastercompositor.NativeSourceRequest) { value.SourceTime = rt(1, 1) }, want: ErrRequestMismatch},
		{name: "invalid time", mutate: func(value *rastercompositor.NativeSourceRequest) { value.TimelineTime.Rate = 0 }, want: ErrRequestInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			frame, err := provider.Frame(context.Background(), request)
			if frame.Surface != nil || !errors.Is(err, test.want) {
				t.Fatalf("frame=%+v err=%v want=%v", frame, err, test.want)
			}
		})
	}
	if frame, err := provider.Frame(nil, base); frame.Surface != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context frame=%+v err=%v", frame, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if frame, err := provider.Frame(canceled, base); frame.Surface != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled frame=%+v err=%v", frame, err)
	}
	var nilProvider *Provider
	if frame, err := nilProvider.Frame(context.Background(), base); frame != (rastercompositor.NativeSourceFrame{}) || !errors.Is(err, ErrProviderInvalid) {
		t.Fatalf("nil provider frame=%+v err=%v", frame, err)
	}
}

func TestRequestRetimeMappingAcceptsEquivalentTimebases(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(red))
	clip := &document.Timelines[0].Tracks[0].Clips[0]
	clip.TimelineRange = documentv2.TimeRange{Start: rt(2, 1), Duration: rt(4, 1)}
	clip.SourceRange = documentv2.TimeRange{Start: rt(10, 1), Duration: rt(8, 1)}
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(t, document, "timeline", "track", "clip", "asset", 0, 0, 0, 3)
	request.LocalTime = rt(2, 2)
	request.SourceTime = rt(24, 2)
	if frame, err := provider.Frame(context.Background(), request); err != nil || frame.Surface == nil {
		t.Fatalf("equivalent mapping frame=%+v err=%v", frame, err)
	}
	request.SourceTime = rt(13, 1)
	if frame, err := provider.Frame(context.Background(), request); frame.Surface != nil || !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("wrong mapping frame=%+v err=%v", frame, err)
	}
	overflow := request
	overflow.TimelineTime = rt(1<<63-1, 1)
	overflow.LocalTime = rt(1<<63-1, 1)
	overflow.SourceTime = rt(1<<63-1, 1)
	if frame, err := provider.Frame(context.Background(), overflow); frame.Surface != nil || !errors.Is(err, ErrRequestInvalid) {
		t.Fatalf("overflow mapping frame=%+v err=%v", frame, err)
	}
}

func TestRequestTransitionHandleMappings(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	blue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 0)
	document := transitionDocument(contentHash(red), contentHash(blue))
	provider, err := Compile(context.Background(), document, []Binding{
		bindingFor(t, document, "asset-red", red), bindingFor(t, document, "asset-blue", blue),
	}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		request rastercompositor.NativeSourceRequest
	}{
		{name: "outgoing post handle", request: validRequest(t, document, "timeline", "track", "clip-red", "asset-red", 0, 0, 0, 5)},
		{name: "incoming pre handle", request: validRequest(t, document, "timeline", "track", "clip-blue", "asset-blue", 0, 0, 1, 4)},
	}
	tests[0].request.TimelineTime = rt(11, 2)
	tests[0].request.LocalTime = rt(11, 2)
	tests[0].request.SourceTime = rt(11, 2)
	tests[1].request.TimelineTime = rt(9, 2)
	tests[1].request.LocalTime = rt(-1, 2)
	tests[1].request.SourceTime = rt(-1, 2)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame, frameErr := provider.Frame(context.Background(), test.request)
			if frameErr != nil || frame.Surface == nil || frame.Request != test.request {
				t.Fatalf("frame=%+v err=%v", frame, frameErr)
			}
		})
	}
}

func TestBindingDiagnosticsUseCallerIndexAndDistinctRequirementsAreBounded(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	blue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 0)
	document := transitionDocument(contentHash(red), contentHash(blue))
	bindings := []Binding{
		bindingFor(t, document, "asset-red", red),
		bindingFor(t, document, "asset-blue", red),
	}
	provider, err := Compile(context.Background(), document, bindings, DefaultConfig())
	var typed *Error
	if provider != nil || !errors.Is(err, ErrContentMismatch) || !errors.As(err, &typed) || typed.Path != "bindings/1/content_hash" {
		t.Fatalf("provider=%v err=%v typed=%+v", provider, err, typed)
	}

	config := DefaultConfig()
	config.Limits.MaxBindings = 1
	provider, err = Compile(context.Background(), document, nil, config)
	typed = nil
	if provider != nil || !errors.Is(err, ErrResourceLimit) || !errors.As(err, &typed) || typed.Path != "document/referenced_images" {
		t.Fatalf("provider=%v err=%v typed=%+v", provider, err, typed)
	}
}

func TestDocumentPreflightEncodedEstimateIsConservative(t *testing.T) {
	encoded := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(encoded))
	document.Metadata = map[string]json.RawMessage{
		"alpha": json.RawMessage(`{"html":"<>&","line":"\u2028","items":[1,true,null]}`),
		"beta":  json.RawMessage(`{"nested":{"z":0,"a":"quoted\\\"value"}}`),
		"gamma": json.RawMessage(`[]`),
	}
	documents := []documentv2.Document{
		document,
		transitionDocument(contentHash(encoded), contentHash(strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 0))),
		mixedImageVideoDocument(contentHash(encoded)),
	}
	for index, candidate := range documents {
		canonical, err := documentv2.CanonicalJSON(candidate)
		if err != nil {
			t.Fatalf("document %d: %v", index, err)
		}
		frozen, err := documentv2.Decode(canonical)
		if err != nil {
			t.Fatalf("document %d decode: %v", index, err)
		}
		frozenHash, err := documentv2.DocumentHash(frozen)
		if err != nil || frozenHash != contentHash(canonical) {
			t.Fatalf("document %d frozen hash=%q canonical hash=%q err=%v", index, frozenHash, contentHash(canonical), err)
		}
		measure := documentMeasure{
			ctx: context.Background(), nodeLimit: HardMaxDocumentNodes, byteLimit: HardMaxDocumentBytes,
		}
		if err := measure.addNodes(1); err != nil {
			t.Fatal(err)
		}
		if err := measure.walk(reflect.ValueOf(candidate)); err != nil {
			t.Fatalf("document %d measure: %v", index, err)
		}
		if measure.encodedUpper < uint64(len(canonical)) {
			t.Fatalf("document %d estimate=%d canonical=%d", index, measure.encodedUpper, len(canonical))
		}
		limits := DefaultLimits()
		limits.MaxCanonicalDocumentBytes = uint64(len(canonical) - 1)
		if err := preflightDocument(context.Background(), candidate, limits); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("document %d preflight err=%v", index, err)
		}
	}
}

func TestConfigAndSurfaceArithmeticFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "negative bindings", mutate: func(value *Config) { value.Limits.MaxBindings = -1 }},
		{name: "bindings hard max", mutate: func(value *Config) { value.Limits.MaxBindings = HardMaxBindings + 1 }},
		{name: "scope hard max", mutate: func(value *Config) { value.Limits.MaxClipScopes = HardMaxClipScopes + 1 }},
		{name: "node hard max", mutate: func(value *Config) { value.Limits.MaxDocumentNodes = HardMaxDocumentNodes + 1 }},
		{name: "track hard max", mutate: func(value *Config) { value.Limits.MaxTracksPerTimeline = HardMaxTracksPerTimeline + 1 }},
		{name: "retained too small", mutate: func(value *Config) { value.Limits.MaxRetainedBytes = cpucompositor.BytesPerPixel - 1 }},
		{name: "retained hard max", mutate: func(value *Config) { value.Limits.MaxRetainedBytes = HardMaxRetainedBytes + 1 }},
		{name: "total encoded too small", mutate: func(value *Config) { value.Limits.MaxTotalEncodedBytes = 7 }},
		{name: "document bytes hard max", mutate: func(value *Config) { value.Limits.MaxCanonicalDocumentBytes = HardMaxDocumentBytes + 1 }},
		{name: "png encoded hard max", mutate: func(value *Config) { value.Limits.PNG.MaxEncodedBytes = pngdecode.HardMaxEncodedBytes + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			test.mutate(&config)
			if normalized, err := normalizeConfig(config); normalized != (Config{}) || !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("normalized=%+v err=%v", normalized, err)
			}
		})
	}
	if bytes, ok := checkedSurfaceBytes(2, 3); !ok || bytes != 48 {
		t.Fatalf("surface bytes=%d ok=%v", bytes, ok)
	}
	maxInt := int(^uint(0) >> 1)
	for _, dimensions := range [][2]int{{0, 1}, {-1, 1}, {maxInt, maxInt}} {
		if bytes, ok := checkedSurfaceBytes(dimensions[0], dimensions[1]); ok || bytes != 0 {
			t.Fatalf("dimensions=%v bytes=%d ok=%v", dimensions, bytes, ok)
		}
	}
}

func TestLimitsDisabledClipAndConcurrentReads(t *testing.T) {
	wide := strictPNG(t, 2, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(wide))
	config := DefaultConfig()
	config.Limits.MaxRetainedBytes = 8
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("retained provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.MaxClipScopes = 1
	document.Timelines[0].Tracks[0].Clips = append(document.Timelines[0].Tracks[0].Clips, documentv2.Clip{
		ID: "second", AssetID: "asset", TimelineRange: tr(10, 10), SourceRange: tr(0, 10),
	})
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("scope provider=%v err=%v", provider, err)
	}
	document = oneImageDocument(contentHash(wide))
	config = DefaultConfig()
	config.Limits.MaxTotalEncodedBytes = uint64(len(wide) - 1)
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("total encoded provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.PNG.MaxEncodedBytes = uint64(len(wide) - 1)
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("per-image encoded provider=%v err=%v", provider, err)
	}
	smallRed := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	smallBlue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 0)
	twoImages := transitionDocument(contentHash(smallRed), contentHash(smallBlue))
	config = DefaultConfig()
	config.Limits.MaxRetainedBytes = 8
	if provider, err := Compile(context.Background(), twoImages, []Binding{
		bindingFor(t, twoImages, "asset-red", smallRed), bindingFor(t, twoImages, "asset-blue", smallBlue),
	}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("aggregate retained provider=%v err=%v", provider, err)
	}
	config.Limits.MaxRetainedBytes = 16
	if provider, err := Compile(context.Background(), twoImages, []Binding{
		bindingFor(t, twoImages, "asset-red", smallRed), bindingFor(t, twoImages, "asset-blue", smallBlue),
	}, config); provider == nil || err != nil {
		t.Fatalf("exact aggregate retained provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.MaxTracksPerTimeline = 1
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{ID: "empty", Kind: documentv2.TrackKindVideo})
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("track limit provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.MaxDocumentNodes = 1
	document = oneImageDocument(contentHash(wide))
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("node limit provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.MaxCanonicalDocumentBytes = 64
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("document byte limit provider=%v err=%v", provider, err)
	}
	document = oneImageDocument(contentHash(wide))
	document.Timelines[0].Tracks[0].Clips[0].Disabled = true
	if provider, err := Compile(context.Background(), document, nil, DefaultConfig()); err != nil || provider == nil || len(provider.Manifest().Assets) != 0 {
		t.Fatalf("disabled provider=%v err=%v", provider, err)
	}

	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document = oneImageDocument(contentHash(red))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(t, document, "timeline", "track", "clip", "asset", 0, 0, 0, 0)
	const workers = 32
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			frame, frameErr := provider.Frame(context.Background(), request)
			if frameErr != nil || frame.PixelHash != provider.Manifest().Assets[0].PixelHash {
				errorsOut <- errors.New("concurrent frame differs")
			}
		}()
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func FuzzCompileNeverPublishesPartialProvider(f *testing.F) {
	f.Add([]byte("not-png"))
	f.Add(strictPNGForFuzz(f, color.NRGBA{R: 128, A: 128}))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 1<<20 {
			t.Skip()
		}
		document := oneImageDocument(contentHash(encoded))
		config := DefaultConfig()
		config.Limits.PNG = pngdecode.Limits{MaxWidth: 8, MaxHeight: 8, MaxEncodedBytes: 1 << 20, MaxWorkingBytes: 8 * 8 * 20}
		config.Limits.MaxRetainedBytes = 8 * 8 * 8
		provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, config)
		if err != nil {
			if provider != nil {
				t.Fatalf("partial provider=%v err=%v", provider, err)
			}
			return
		}
		if provider == nil || provider.DocumentHash() == "" || len(provider.Manifest().Assets) != 1 {
			t.Fatalf("incomplete success=%v", provider)
		}
	})
}

func FuzzFrameNeverPanicsOrPublishesOnMismatch(f *testing.F) {
	red := strictPNGForFuzz(f, color.NRGBA{R: 255, A: 255})
	document := oneImageDocument(contentHash(red))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(f, document, "asset", red)}, DefaultConfig())
	if err != nil {
		f.Fatal(err)
	}
	base := validRequest(f, document, "timeline", "track", "clip", "asset", 0, 0, 0, 0)
	f.Add(int64(0), int64(1), uint8(0), "")
	f.Add(int64(1), int64(1), uint8(3), "other")
	f.Fuzz(func(t *testing.T, value, rate int64, selector uint8, token string) {
		if len(token) > 256 {
			t.Skip()
		}
		request := base
		if rate == 0 {
			rate = 1
		}
		switch selector % 8 {
		case 0:
			request.TimelineTime = rt(value, rate)
		case 1:
			request.LocalTime = rt(value, rate)
		case 2:
			request.SourceTime = rt(value, rate)
		case 3:
			request.ClipID = documentv2.ID(token)
		case 4:
			request.AssetID = documentv2.ID(token)
		case 5:
			request.DecodeProfile = token
		case 6:
			request.AssetIndex = int(value)
		case 7:
			request.ContractVersion = int(value)
		}
		frame, err := provider.Frame(context.Background(), request)
		if err != nil {
			if frame.Surface != nil || frame.PixelHash != "" {
				t.Fatalf("partial frame=%+v err=%v", frame, err)
			}
			return
		}
		if frame.Surface == nil || frame.Request != request || frame.PixelHash == "" {
			t.Fatalf("incomplete frame=%+v", frame)
		}
	})
}

func oneImageDocument(hash string) documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset", Kind: documentv2.AssetKindImage, URI: "ignored.png", ContentHash: hash}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip", AssetID: "asset", TimelineRange: tr(0, 10), SourceRange: tr(0, 10)}},
		}}}},
	}
}

func mixedImageVideoDocument(imageHash string) documentv2.Document {
	duration := rt(10, 1)
	document := oneImageDocument(imageHash)
	document.Assets = append(document.Assets, documentv2.Asset{
		ID: "video", Kind: documentv2.AssetKindVideo, URI: "ignored.mp4",
		ContentHash: contentHash([]byte("video")), Duration: &duration,
	})
	document.Timelines[0].ID = "image-timeline"
	document.Timelines[0].Tracks[0].ID = "image-track"
	document.Timelines = append(document.Timelines, documentv2.Timeline{ID: "video-timeline", Tracks: []documentv2.Track{{
		ID: "video-track", Kind: documentv2.TrackKindVideo,
		Clips: []documentv2.Clip{{ID: "video-clip", AssetID: "video", TimelineRange: tr(0, 10), SourceRange: tr(0, 10)}},
	}}})
	return document
}

func transitionDocument(redHash, blueHash string) documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "asset-red", Kind: documentv2.AssetKindImage, URI: "red.png", ContentHash: redHash},
			{ID: "asset-blue", Kind: documentv2.AssetKindImage, URI: "blue.png", ContentHash: blueHash},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{
				{ID: "clip-red", AssetID: "asset-red", TimelineRange: tr(0, 5), SourceRange: tr(0, 5)},
				{ID: "clip-blue", AssetID: "asset-blue", TimelineRange: tr(5, 5), SourceRange: tr(0, 5)},
			},
		}}, Transitions: []documentv2.Transition{{
			ID: "transition", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "clip-red", IncomingClipID: "clip-blue", PreDuration: rt(1, 1), PostDuration: rt(1, 1),
		}}}},
	}
}

func validRequest(t testing.TB, document documentv2.Document, timelineID, trackID, clipID, assetID documentv2.ID, timelineIndex, trackIndex, clipIndex int, at int64) rastercompositor.NativeSourceRequest {
	t.Helper()
	hash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	assetIndex := -1
	var asset documentv2.Asset
	for index := range document.Assets {
		if document.Assets[index].ID == assetID {
			assetIndex, asset = index, document.Assets[index]
		}
	}
	if assetIndex < 0 {
		t.Fatal("asset missing")
	}
	profiles := ImageSourceProfiles()
	return rastercompositor.NativeSourceRequest{
		ContractVersion: rastercompositor.NativeSourceContractVersion, DocumentHash: hash,
		TimelineID: timelineID, TimelineIndex: timelineIndex, TrackID: trackID, TrackIndex: trackIndex,
		ClipID: clipID, ClipIndex: clipIndex, AssetID: assetID, AssetIndex: assetIndex,
		AssetKind: asset.Kind, AssetContentHash: asset.ContentHash,
		TimelineTime: rt(at, 1), LocalTime: rt(at, 1), SourceTime: rt(at, 1),
		SourceProfile: rastercompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		DecodeProfile: profiles.DecodeProfile, SelectionProfile: profiles.SelectionProfile,
		ColorProfile: profiles.ColorProfile, NormalizationProfile: profiles.NormalizationProfile,
	}
}

func bindingFor(t testing.TB, document documentv2.Document, assetID documentv2.ID, encoded []byte) Binding {
	t.Helper()
	hash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	return Binding{DocumentHash: hash, AssetID: assetID, EncodedPNG: encoded}
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}
func tr(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}
func ratio(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func contentHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func assertPixels(t testing.TB, surface *cpucompositor.Surface, wanted []cpucompositor.Pixel) {
	t.Helper()
	if surface == nil || surface.Width()*surface.Height() != len(wanted) {
		t.Fatalf("surface=%v wanted=%d", surface, len(wanted))
	}
	for index, want := range wanted {
		got, err := surface.Pixel(index%surface.Width(), index/surface.Width())
		if err != nil || got != want {
			t.Fatalf("pixel[%d]=%+v want=%+v err=%v", index, got, want, err)
		}
	}
}

func decodeBase64(t testing.TB, value string) []byte {
	t.Helper()
	result, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func strictPNG(t testing.TB, width, height int, pixel color.NRGBA, intent byte) []byte {
	t.Helper()
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageValue.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, forceAlphaImage{NRGBA: imageValue}); err != nil {
		t.Fatal(err)
	}
	return insertSRGB(t, encoded.Bytes(), intent)
}

func strictPNGForFuzz(f *testing.F, pixel color.NRGBA) []byte {
	f.Helper()
	return strictPNG(f, 1, 1, pixel, 0)
}

type forceAlphaImage struct{ *image.NRGBA }

func (forceAlphaImage) Opaque() bool { return false }

func insertSRGB(t testing.TB, encoded []byte, intent byte) []byte {
	t.Helper()
	if len(encoded) < 33 || string(encoded[12:16]) != "IHDR" {
		t.Fatal("test PNG lacks IHDR")
	}
	length := int(binary.BigEndian.Uint32(encoded[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = intent
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(encoded)+len(chunk))
	result = append(result, encoded[:end]...)
	result = append(result, chunk...)
	return append(result, encoded[end:]...)
}

func TestImageSourceProfilesAndDescriptorAreDefensiveValues(t *testing.T) {
	profiles := ImageSourceProfiles()
	if profiles.DecodeProfile != pngdecode.ProfileV1 || profiles.SelectionProfile != SelectionProfileV1 ||
		profiles.NormalizationProfile != NormalizationProfileV1 {
		t.Fatalf("profiles=%+v", profiles)
	}
	if !reflect.DeepEqual(Descriptor(), Descriptor()) || Descriptor().DefaultLimits != DefaultLimits() {
		t.Fatalf("descriptor=%+v", Descriptor())
	}
	if strings.TrimSpace(Descriptor().EvidenceSemantics) == "" {
		t.Fatal("missing evidence semantics")
	}
}
