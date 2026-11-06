package pngsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/referencecompositor"
)

func TestVerifiedPNGEndToEndTransitionAndFrozenInputs(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	blue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 1)
	document := transitionDocument(contentHash(red), contentHash(blue))
	bindings := []Binding{bindingFor(t, document, "asset-red", red), bindingFor(t, document, "asset-blue", blue)}
	provider, err := Compile(context.Background(), document, bindings, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	program, diagnostics := referencecompositor.Compile(document, referencecompositor.DefaultConfig(1, 1))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compositor compile: program=%v diagnostics=%+v", program, diagnostics)
	}
	if provider.DocumentHash() != program.DocumentHash() {
		t.Fatalf("document hashes differ: provider=%s compositor=%s", provider.DocumentHash(), program.DocumentHash())
	}

	manifest := provider.Manifest()
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Profile != ProfileV1 || len(manifest.Assets) != 2 ||
		manifest.Assets[0].AssetID != "asset-blue" || manifest.Assets[1].AssetID != "asset-red" {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, receipt := range manifest.Assets {
		if receipt.DecodeProfile != pngdecode.ProfileV1 || receipt.SourceProfile != referencecompositor.SourceProfileV1 ||
			receipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 || receipt.PixelHash == "" || receipt.EncodedContentHash == "" {
			t.Fatalf("incomplete receipt: %+v", receipt)
		}
	}
	manifest.Assets[0].PixelHash = "mutated"
	manifest.Assets = append(manifest.Assets, AssetReceipt{})
	if frozen := provider.Manifest(); len(frozen.Assets) != 2 || frozen.Assets[0].PixelHash == "mutated" {
		t.Fatalf("manifest aliases provider storage: %+v", frozen)
	}

	// Compile copied all semantic state into immutable surfaces and canonical document programs.
	document.Assets[0].ContentHash = strings.Repeat("x", 71)
	for index := range red {
		red[index] = 0
	}
	for index := range blue {
		blue[index] = 0
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(5, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := result.Surface.Pixel(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pixel != (cpucompositor.Pixel{R: 32768, B: 32768, A: 65535}) {
		t.Fatalf("transition pixel = %+v", pixel)
	}
	if result.DocumentHash != provider.DocumentHash() || result.SourceProfile != referencecompositor.SourceProfileV1 {
		t.Fatalf("render binding = %+v", result)
	}
}

func TestCompileBindingAndDocumentFailuresAreAtomic(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	blue := strictPNG(t, 1, 1, color.NRGBA{B: 255, A: 255}, 0)
	wide := widePNG(t)
	base := oneImageDocument(contentHash(red))
	extraDocument := withUnreferencedImage(base, contentHash(blue))
	nonImageDocument := withDataAsset(base, contentHash(blue))
	badDocument := oneImageDocument(contentHash([]byte("not-png")))
	wideDocument := oneImageDocument(contentHash(wide))
	tests := []struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{
		{name: "missing", document: base, config: DefaultConfig(1, 1), want: ErrBindingMissing},
		{name: "duplicate", document: base, bindings: []Binding{bindingFor(t, base, "asset", red), bindingFor(t, base, "asset", red)}, config: DefaultConfig(1, 1), want: ErrBindingInvalid},
		{name: "unknown", document: base, bindings: []Binding{bindingFor(t, base, "unknown", red)}, config: DefaultConfig(1, 1), want: ErrBindingInvalid},
		{name: "cross document", document: base, bindings: []Binding{{DocumentHash: contentHash([]byte("other-document")), AssetID: "asset", EncodedPNG: red}}, config: DefaultConfig(1, 1), want: ErrDocumentMismatch},
		{name: "non image binding", document: nonImageDocument, bindings: []Binding{bindingFor(t, nonImageDocument, "data", blue)}, config: DefaultConfig(1, 1), want: ErrBindingInvalid},
		{name: "extra unreferenced", document: extraDocument, bindings: []Binding{bindingFor(t, extraDocument, "asset", red), bindingFor(t, extraDocument, "extra", blue)}, config: DefaultConfig(1, 1), want: ErrBindingExtra},
		{name: "hash mismatch", document: base, bindings: []Binding{bindingFor(t, base, "asset", blue)}, config: DefaultConfig(1, 1), want: ErrContentMismatch},
		{name: "bad png", document: badDocument, bindings: []Binding{bindingFor(t, badDocument, "asset", []byte("not-png"))}, config: DefaultConfig(1, 1), want: ErrDecodeFailed},
		{name: "dimension", document: wideDocument, bindings: []Binding{bindingFor(t, wideDocument, "asset", wide)}, config: DefaultConfig(1, 1), want: ErrDimensionMismatch},
		{name: "unpinned image", document: oneImageDocument(""), bindings: []Binding{{AssetID: "asset", EncodedPNG: red}}, config: DefaultConfig(1, 1), want: ErrDocumentInvalid},
		{name: "video clip", document: videoDocument(), bindings: nil, config: DefaultConfig(1, 1), want: ErrDocumentInvalid},
		{name: "invalid config", document: base, bindings: []Binding{{AssetID: "asset", EncodedPNG: red}}, config: Config{}, want: ErrConfigInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := Compile(context.Background(), test.document, test.bindings, test.config)
			if provider != nil || !errors.Is(err, test.want) {
				t.Fatalf("provider=%v err=%v, want %v", provider, err, test.want)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	provider, err := Compile(canceled, base, []Binding{bindingFor(t, base, "asset", red)}, DefaultConfig(1, 1))
	if provider != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled provider/error = %v, %v", provider, err)
	}
}

func TestCompileBudgetsBeforeDecodeAndDisabledClipNeedsNoBinding(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(red))
	config := DefaultConfig(1, 1)
	config.Limits.MaxRetainedBytes = 7
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, config); provider != nil || !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("invalid retained floor = %v, %v", provider, err)
	}
	config = DefaultConfig(2, 1)
	config.Limits.MaxRetainedBytes = 8
	wide := strictPNG(t, 2, 1, color.NRGBA{R: 255, A: 255}, 0)
	document = oneImageDocument(contentHash(wide))
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", wide)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("retained budget = %v, %v", provider, err)
	}
	config = DefaultConfig(1, 1)
	config.Limits.PNG.MaxEncodedBytes = uint64(len(red) - 1)
	document = oneImageDocument(contentHash(red))
	if provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("encoded budget = %v, %v", provider, err)
	}

	document = oneImageDocument(contentHash(red))
	document.Timelines[0].Tracks[0].Clips[0].Disabled = true
	provider, err := Compile(context.Background(), document, nil, DefaultConfig(1, 1))
	if err != nil || provider == nil || len(provider.Manifest().Assets) != 0 {
		t.Fatalf("disabled-only provider = %v, %v", provider, err)
	}

	t.Run("png peak working bytes preflight", func(t *testing.T) {
		wideDocument := oneImageDocument(contentHash(wide))
		config := DefaultConfig(2, 1)
		// 2*1*20 complete-frame bytes + 2*(1+2*4) scanline bytes = 58.
		config.Limits.PNG.MaxWorkingBytes = 57
		if provider, compileErr := Compile(context.Background(), wideDocument,
			[]Binding{bindingFor(t, wideDocument, "asset", wide)}, config); provider != nil || !errors.Is(compileErr, ErrConfigInvalid) {
			t.Fatalf("undersized PNG working budget = %v, %v", provider, compileErr)
		}
		config.Limits.PNG.MaxWorkingBytes = 58
		if provider, compileErr := Compile(context.Background(), wideDocument,
			[]Binding{bindingFor(t, wideDocument, "asset", wide)}, config); provider == nil || compileErr != nil {
			t.Fatalf("exact PNG working budget = %v, %v", provider, compileErr)
		}
	})

	t.Run("clip scope budget", func(t *testing.T) {
		scoped := oneImageDocument(contentHash(red))
		scoped.Timelines[0].Tracks[0].Clips = append(scoped.Timelines[0].Tracks[0].Clips, documentv2.Clip{
			ID: "clip-2", AssetID: "asset", TimelineRange: tr(10, 10), SourceRange: tr(0, 10),
		})
		config := DefaultConfig(1, 1)
		config.Limits.MaxClipScopes = 1
		if provider, compileErr := Compile(context.Background(), scoped,
			[]Binding{bindingFor(t, scoped, "asset", red)}, config); provider != nil || !errors.Is(compileErr, ErrResourceLimit) {
			t.Fatalf("clip scope budget = %v, %v", provider, compileErr)
		}
	})
}

func TestSurfaceValidatesCompleteFrozenRequest(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(red))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	valid := validRequest(provider.DocumentHash())
	if surface, err := provider.Surface(context.Background(), valid); err != nil || surface == nil {
		t.Fatalf("valid request = %v, %v", surface, err)
	}

	tests := []struct {
		name   string
		mutate func(*referencecompositor.SourceRequest)
		want   error
	}{
		{name: "document", mutate: func(value *referencecompositor.SourceRequest) { value.DocumentHash = contentHash([]byte("other")) }, want: ErrDocumentMismatch},
		{name: "timeline", mutate: func(value *referencecompositor.SourceRequest) { value.TimelineID = "other" }, want: ErrRequestInvalid},
		{name: "track", mutate: func(value *referencecompositor.SourceRequest) { value.TrackID = "other" }, want: ErrRequestInvalid},
		{name: "clip", mutate: func(value *referencecompositor.SourceRequest) { value.ClipID = "other" }, want: ErrRequestInvalid},
		{name: "asset", mutate: func(value *referencecompositor.SourceRequest) { value.AssetID = "other" }, want: ErrRequestInvalid},
		{name: "track index", mutate: func(value *referencecompositor.SourceRequest) { value.TrackIndex = 1 }, want: ErrRequestInvalid},
		{name: "clip index", mutate: func(value *referencecompositor.SourceRequest) { value.ClipIndex = 1 }, want: ErrRequestInvalid},
		{name: "source profile", mutate: func(value *referencecompositor.SourceRequest) { value.SourceProfile = "other" }, want: ErrRequestInvalid},
		{name: "surface profile", mutate: func(value *referencecompositor.SourceRequest) { value.SurfaceProfile = "other" }, want: ErrRequestInvalid},
		{name: "width", mutate: func(value *referencecompositor.SourceRequest) { value.Width = 2 }, want: ErrRequestInvalid},
		{name: "time", mutate: func(value *referencecompositor.SourceRequest) { value.SourceTime.Rate = 0 }, want: ErrRequestInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if surface, err := provider.Surface(context.Background(), request); surface != nil || !errors.Is(err, test.want) {
				t.Fatalf("surface=%v err=%v, want %v", surface, err, test.want)
			}
		})
	}

	if surface, err := provider.Surface(nil, valid); surface != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context = %v, %v", surface, err)
	}
	var nilProvider *Provider
	if surface, err := nilProvider.Surface(context.Background(), valid); surface != nil || !errors.Is(err, ErrProviderInvalid) {
		t.Fatalf("nil provider = %v, %v", surface, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if surface, err := provider.Surface(canceled, valid); surface != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled request = %v, %v", surface, err)
	}

	// Image media is static: valid exact times, including transition-handle times outside the
	// ordinary clip interval, select the same immutable image after scope authorization.
	staticHandle := valid
	staticHandle.TimelineTime = rt(-1000, 3)
	staticHandle.LocalTime = rt(-1000, 3)
	staticHandle.SourceTime = rt(999999, 7)
	if surface, surfaceErr := provider.Surface(context.Background(), staticHandle); surface == nil || surfaceErr != nil {
		t.Fatalf("static image handle request = %v, %v", surface, surfaceErr)
	}
}

func TestMultipleTimelineScopesShareOnePinnedAsset(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(red))
	document.Timelines = append(document.Timelines, documentv2.Timeline{
		ID: "timeline-2", Tracks: []documentv2.Track{{
			ID: "track-2", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip-2", AssetID: "asset", TimelineRange: tr(0, 5), SourceRange: tr(0, 5)}},
		}},
	})
	provider, err := Compile(context.Background(), document,
		[]Binding{bindingFor(t, document, "asset", red)}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if manifest := provider.Manifest(); len(manifest.Assets) != 1 || manifest.Assets[0].AssetID != "asset" {
		t.Fatalf("shared asset manifest = %+v", manifest)
	}
	request := validRequest(provider.DocumentHash())
	request.TimelineID, request.TrackID, request.ClipID = "timeline-2", "track-2", "clip-2"
	if surface, surfaceErr := provider.Surface(context.Background(), request); surface == nil || surfaceErr != nil {
		t.Fatalf("second timeline request = %v, %v", surface, surfaceErr)
	}
}

func TestProviderConcurrentReadDeterminism(t *testing.T) {
	red := strictPNG(t, 1, 1, color.NRGBA{R: 255, A: 255}, 0)
	document := oneImageDocument(contentHash(red))
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", red)}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(provider.DocumentHash())
	baseline, err := provider.Surface(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	baselineHash, _ := baseline.CanonicalHash()
	const workers = 24
	var group sync.WaitGroup
	errorsSeen := make(chan error, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 20; iteration++ {
				surface, surfaceErr := provider.Surface(context.Background(), request)
				if surfaceErr != nil {
					errorsSeen <- surfaceErr
					return
				}
				hash, hashErr := surface.CanonicalHash()
				if hashErr != nil || hash != baselineHash {
					errorsSeen <- errors.New("concurrent surface differs")
					return
				}
				manifest := provider.Manifest()
				if len(manifest.Assets) != 1 || manifest.Assets[0].PixelHash != baselineHash {
					errorsSeen <- errors.New("concurrent manifest differs")
					return
				}
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func TestConfigAndManifestReturnCopies(t *testing.T) {
	config := DefaultConfig(7, 9)
	normalized, err := normalizeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized, config) {
		t.Fatalf("normalized config = %+v, want %+v", normalized, config)
	}
	if _, err := normalizeConfig(Config{Width: 1, Height: 1, Limits: Limits{MaxBindings: HardMaxBindings + 1, MaxClipScopes: 1, MaxRetainedBytes: 8, PNG: pngdecode.DefaultLimits()}}); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("binding hard limit error = %v", err)
	}
	if _, err := normalizeConfig(Config{Width: 1, Height: 1, Limits: Limits{MaxBindings: 1, MaxClipScopes: HardMaxClipScopes + 1, MaxRetainedBytes: 8, PNG: pngdecode.DefaultLimits()}}); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("clip scope hard limit error = %v", err)
	}
}

func TestEncodedHashChecksCancellationBetweenChunks(t *testing.T) {
	ctx := &cancelAfterErrContext{cancelAt: 2}
	if hash, err := encodedSHA256(ctx, make([]byte, 256<<10), "hash"); hash != "" ||
		!errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("hash/error = %q, %v", hash, err)
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
		config := DefaultConfig(1, 1)
		config.Limits.PNG = pngdecode.Limits{MaxWidth: 8, MaxHeight: 8, MaxEncodedBytes: 1 << 20, MaxWorkingBytes: 8 * 8 * 20}
		config.Limits.MaxRetainedBytes = 8 * 8 * 8
		provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, config)
		if err != nil {
			if provider != nil {
				t.Fatalf("failure published partial provider: %v, %v", provider, err)
			}
			return
		}
		if provider == nil || provider.Manifest().DocumentHash == "" || len(provider.Manifest().Assets) != 1 {
			t.Fatalf("success omitted provider bindings: %v", provider)
		}
	})
}

func validRequest(documentHash string) referencecompositor.SourceRequest {
	return referencecompositor.SourceRequest{
		DocumentHash: documentHash, TimelineID: "timeline", TrackID: "track", ClipID: "clip", AssetID: "asset",
		TrackIndex: 0, ClipIndex: 0, TimelineTime: rt(0, 1), LocalTime: rt(0, 1), SourceTime: rt(0, 1),
		Width: 1, Height: 1, SourceProfile: referencecompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
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

func withUnreferencedImage(document documentv2.Document, hash string) documentv2.Document {
	document.Assets = append(document.Assets, documentv2.Asset{ID: "extra", Kind: documentv2.AssetKindImage, URI: "extra.png", ContentHash: hash})
	return document
}

func withDataAsset(document documentv2.Document, hash string) documentv2.Document {
	document.Assets = append(document.Assets, documentv2.Asset{ID: "data", Kind: documentv2.AssetKindData, URI: "data.bin", ContentHash: hash})
	return document
}

func videoDocument() documentv2.Document {
	duration := rt(10, 1)
	document := oneImageDocument(contentHash([]byte("placeholder")))
	document.Assets[0] = documentv2.Asset{ID: "asset", Kind: documentv2.AssetKindVideo, URI: "video.mp4", Duration: &duration}
	return document
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}

func tr(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}

func contentHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func bindingFor(t testing.TB, document documentv2.Document, assetID documentv2.ID, encoded []byte) Binding {
	t.Helper()
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	return Binding{DocumentHash: documentHash, AssetID: assetID, EncodedPNG: encoded}
}

func widePNG(t *testing.T) []byte {
	t.Helper()
	return strictPNG(t, 2, 1, color.NRGBA{A: 255}, 0)
}

func strictPNG(t *testing.T, width, height int, pixel color.NRGBA, intent byte) []byte {
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
	imageValue := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	imageValue.SetNRGBA(0, 0, pixel)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, forceAlphaImage{NRGBA: imageValue}); err != nil {
		f.Fatal(err)
	}
	return insertSRGBForFuzz(f, encoded.Bytes(), 0)
}

type forceAlphaImage struct{ *image.NRGBA }

func (forceAlphaImage) Opaque() bool { return false }

func insertSRGB(t *testing.T, encoded []byte, intent byte) []byte {
	t.Helper()
	return insertSRGBCommon(t, encoded, intent)
}

func insertSRGBForFuzz(f *testing.F, encoded []byte, intent byte) []byte {
	f.Helper()
	return insertSRGBCommon(f, encoded, intent)
}

type fatalHelper interface {
	Helper()
	Fatal(...any)
}

type cancelAfterErrContext struct {
	calls    atomic.Int64
	cancelAt int64
}

func (value *cancelAfterErrContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *cancelAfterErrContext) Done() <-chan struct{}       { return nil }
func (value *cancelAfterErrContext) Value(any) any               { return nil }
func (value *cancelAfterErrContext) Err() error {
	if value.calls.Add(1) >= value.cancelAt {
		return context.Canceled
	}
	return nil
}

func insertSRGBCommon(test fatalHelper, encoded []byte, intent byte) []byte {
	test.Helper()
	if len(encoded) < 33 || string(encoded[12:16]) != "IHDR" {
		test.Fatal("test PNG lacks IHDR")
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
