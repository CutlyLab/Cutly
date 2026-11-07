package textlayer_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
)

func TestCompileCachesOneSurfacePerTextAssetAndFramesAreConcurrent(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	appendSecondTextClip(&document)
	hash := freezeHash(t, document)
	provider, err := textlayer.Compile(ctx, document, []textlayer.FontBinding{{
		DocumentHash: hash, FontAssetID: "asset-font", Provider: font,
	}}, textlayer.DefaultConfig())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	manifest := provider.Manifest()
	if provider.DocumentHash() != hash || manifest.DocumentHash != hash || len(manifest.Assets) != 1 ||
		manifest.Assets[0].AssetID != "asset-text" || manifest.RetainedSurfaceBytes != 16*16*cpucompositor.BytesPerPixel {
		t.Fatalf("manifest=%+v", manifest)
	}
	mutated := provider.Manifest()
	mutated.Assets[0].AssetID = "mutated"
	if got := provider.Manifest().Assets[0].AssetID; got != "asset-text" {
		t.Fatalf("Manifest aliases retained receipt: %q", got)
	}

	firstRequest := frameRequest(t, document, manifest.Assets[0], 0, documentv2.RationalTime{Value: 1, Rate: 1})
	secondRequest := frameRequest(t, document, manifest.Assets[0], 1, documentv2.RationalTime{Value: 6, Rate: 1})
	first, err := provider.Frame(ctx, firstRequest)
	if err != nil {
		t.Fatalf("first Frame: %v", err)
	}
	second, err := provider.Frame(ctx, secondRequest)
	if err != nil {
		t.Fatalf("second Frame: %v", err)
	}
	if first.Surface == nil || first.Surface != second.Surface || first.Receipt.Request != firstRequest ||
		second.Receipt.Request != secondRequest || first.Receipt.SurfaceIdentity != manifest.Assets[0].SurfaceIdentity ||
		first.Receipt.Assembly != manifest.Assets[0].Assembly || first.Receipt.Raster != manifest.Assets[0].Raster {
		t.Fatalf("frames do not share exact cached derivation: first=%+v second=%+v", first.Receipt, second.Receipt)
	}

	var wait sync.WaitGroup
	errorsOut := make(chan error, 32)
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			frame, frameErr := provider.Frame(ctx, firstRequest)
			if frameErr != nil {
				errorsOut <- frameErr
				return
			}
			if frame.Surface != first.Surface || frame.Receipt != first.Receipt {
				errorsOut <- errors.New("concurrent frame drift")
			}
		}()
	}
	wait.Wait()
	close(errorsOut)
	for concurrentErr := range errorsOut {
		t.Fatal(concurrentErr)
	}
}

func TestDistinctTextAssetsSharingOneFontRemainDistinct(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	secondAsset := document.Assets[4]
	secondAsset.ID = "asset-text-2"
	secondText := *secondAsset.Text
	secondAsset.Text = &secondText
	document.Assets = append(document.Assets, secondAsset)
	secondClip := document.Timelines[0].Tracks[2].Clips[0]
	secondClip.ID = "clip-text-2"
	secondClip.AssetID = secondAsset.ID
	secondClip.TimelineRange.Start = documentv2.RationalTime{Value: 5, Rate: 1}
	secondClip.SourceRange.Start = documentv2.RationalTime{Value: 0, Rate: 1}
	document.Timelines[0].Tracks[2].Clips = append(document.Timelines[0].Tracks[2].Clips, secondClip)
	hash := freezeHash(t, document)
	provider, err := textlayer.Compile(ctx, document, []textlayer.FontBinding{{
		DocumentHash: hash, FontAssetID: "asset-font", Provider: font,
	}}, textlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	manifest := provider.Manifest()
	if len(manifest.Assets) != 2 || manifest.Assets[0].AssetID != "asset-text" || manifest.Assets[1].AssetID != "asset-text-2" ||
		manifest.Assets[0].FrozenTextBindingHash == manifest.Assets[1].FrozenTextBindingHash ||
		manifest.Assets[0].TextSemanticHash != manifest.Assets[1].TextSemanticHash {
		t.Fatalf("distinct AssetID manifest=%+v", manifest)
	}
	first, err := provider.Frame(ctx, frameRequest(t, document, manifest.Assets[0], 0, documentv2.RationalTime{Value: 1, Rate: 1}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Frame(ctx, frameRequest(t, document, manifest.Assets[1], 1, documentv2.RationalTime{Value: 6, Rate: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Surface == second.Surface || first.Receipt.SurfaceIdentity != second.Receipt.SurfaceIdentity {
		t.Fatalf("assets were hash-deduplicated or raster drifted: first=%p second=%p", first.Surface, second.Surface)
	}
}

func TestStaticSubsetRejectsDynamicPaintTransitionsLegacyAndExtensions(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	base := textDocument(t, fontHash)
	baseHash := freezeHash(t, base)
	for _, test := range []struct {
		name   string
		mutate func(*documentv2.Document)
	}{
		{name: "text fill lane", mutate: func(document *documentv2.Document) {
			document.Timelines[0].Tracks[2].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{validFillLane()}
		}},
		{name: "text transition", mutate: func(document *documentv2.Document) {
			appendSecondTextClip(document)
			document.Timelines[0].Transitions = append(document.Timelines[0].Transitions, documentv2.Transition{
				ID: "text-transition", Profile: documentv2.TransitionDissolveV1,
				OutgoingClipID: "clip-text", IncomingClipID: "clip-text-2",
				PreDuration:  documentv2.RationalTime{Value: 1, Rate: 1},
				PostDuration: documentv2.RationalTime{Value: 1, Rate: 1},
			})
		}},
		{name: "legacy", mutate: func(document *documentv2.Document) {
			text := document.Assets[4].Text
			text.Profile, text.Script, text.LineHeight, text.Viewport = "", "", nil, nil
		}},
		{name: "rendering extension", mutate: func(document *documentv2.Document) {
			clip := &document.Timelines[0].Tracks[2].Clips[0]
			clip.PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{
				Namespace: "example.paint.v1", AffectsRendering: true,
				RequiredCapability: "example.paint.v1", Value: json.RawMessage(`{"enabled":true}`),
			}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := cloneDocument(t, base)
			test.mutate(&document)
			provider, err := textlayer.Compile(ctx, document, []textlayer.FontBinding{{
				DocumentHash: baseHash, FontAssetID: "asset-font", Provider: font,
			}}, textlayer.DefaultConfig())
			if provider != nil || !errors.Is(err, textlayer.ErrProfileUnsupported) {
				t.Fatalf("provider=%v err=%v", provider, err)
			}
		})
	}

	hardCut := cloneDocument(t, base)
	appendSecondTextClip(&hardCut)
	hardCut.Timelines[0].Transitions = append(hardCut.Timelines[0].Transitions, documentv2.Transition{
		ID: "text-hard-cut", Profile: documentv2.TransitionDissolveV1,
		OutgoingClipID: "clip-text", IncomingClipID: "clip-text-2",
		PreDuration:  documentv2.RationalTime{Value: 0, Rate: 1},
		PostDuration: documentv2.RationalTime{Value: 0, Rate: 1},
	})
	hardCutHash := freezeHash(t, hardCut)
	provider, err := textlayer.Compile(ctx, hardCut, []textlayer.FontBinding{{
		DocumentHash: hardCutHash, FontAssetID: "asset-font", Provider: font,
	}}, textlayer.DefaultConfig())
	if err != nil || provider == nil || len(provider.Manifest().Assets) != 1 {
		t.Fatalf("zero-width text hard cut provider=%v err=%v", provider, err)
	}
}

func TestFontBindingsAreExactExplicitAndDocumentScoped(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	hash := freezeHash(t, document)
	valid := textlayer.FontBinding{DocumentHash: hash, FontAssetID: "asset-font", Provider: font}
	for _, test := range []struct {
		name     string
		bindings []textlayer.FontBinding
	}{
		{name: "missing"},
		{name: "duplicate", bindings: []textlayer.FontBinding{valid, valid}},
		{name: "cross document", bindings: []textlayer.FontBinding{{DocumentHash: "sha256:" + strings.Repeat("f", 64), FontAssetID: "asset-font", Provider: font}}},
		{name: "extra", bindings: []textlayer.FontBinding{valid, {DocumentHash: hash, FontAssetID: "unused-font", Provider: font}}},
		{name: "wrong id", bindings: []textlayer.FontBinding{{DocumentHash: hash, FontAssetID: "other-font", Provider: font}}},
		{name: "nil provider", bindings: []textlayer.FontBinding{{DocumentHash: hash, FontAssetID: "asset-font"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, err := textlayer.Compile(ctx, document, test.bindings, textlayer.DefaultConfig())
			if provider != nil || !errors.Is(err, textlayer.ErrBindingInvalid) {
				t.Fatalf("provider=%v err=%v", provider, err)
			}
		})
	}

	// Equal bytes under a different AssetID remain a distinct authorization edge.
	aliased := cloneDocument(t, document)
	fontAsset := aliased.Assets[3]
	fontAsset.ID = "asset-font-alias"
	aliased.Assets = append(aliased.Assets, fontAsset)
	aliased.Assets[4].Text.FontAssetID = fontAsset.ID
	aliasHash := freezeHash(t, aliased)
	provider, err := textlayer.Compile(ctx, aliased, []textlayer.FontBinding{{
		DocumentHash: aliasHash, FontAssetID: "asset-font", Provider: font,
	}}, textlayer.DefaultConfig())
	if provider != nil || !errors.Is(err, textlayer.ErrBindingInvalid) {
		t.Fatalf("implicit equal-hash alias provider=%v err=%v", provider, err)
	}
	provider, err = textlayer.Compile(ctx, aliased, []textlayer.FontBinding{{
		DocumentHash: aliasHash, FontAssetID: "asset-font-alias", Provider: font,
	}}, textlayer.DefaultConfig())
	if err != nil || provider == nil {
		t.Fatalf("explicit alias binding provider=%v err=%v", provider, err)
	}
}

func TestFrameRejectsDriftAndFailureIsZero(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	hash := freezeHash(t, document)
	provider, err := textlayer.Compile(ctx, document, []textlayer.FontBinding{{DocumentHash: hash, FontAssetID: "asset-font", Provider: font}}, textlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := frameRequest(t, document, provider.Manifest().Assets[0], 0, documentv2.RationalTime{Value: 1, Rate: 1})
	for _, test := range []struct {
		name   string
		mutate func(*textlayer.Request)
		want   error
	}{
		{name: "document", mutate: func(value *textlayer.Request) { value.DocumentHash = "sha256:" + strings.Repeat("a", 64) }, want: textlayer.ErrRequestMismatch},
		{name: "binding hash", mutate: func(value *textlayer.Request) { value.FrozenTextBindingHash = "sha256:" + strings.Repeat("b", 64) }, want: textlayer.ErrRequestMismatch},
		{name: "end excluded", mutate: func(value *textlayer.Request) {
			value.TimelineTime = documentv2.RationalTime{Value: 5, Rate: 1}
			value.LocalTime = value.TimelineTime
			value.SourceTime = value.TimelineTime
		}, want: textlayer.ErrRequestInvalid},
		{name: "profile", mutate: func(value *textlayer.Request) { value.PaintProfile = "unknown" }, want: textlayer.ErrRequestMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			frame, frameErr := provider.Frame(ctx, changed)
			if frame != (textlayer.Frame{}) || !errors.Is(frameErr, test.want) {
				t.Fatalf("frame=%+v err=%v", frame, frameErr)
			}
		})
	}
	if frame, frameErr := provider.Frame(nil, request); frame != (textlayer.Frame{}) || !errors.Is(frameErr, textlayer.ErrContextInvalid) {
		t.Fatalf("nil context frame=%+v err=%v", frame, frameErr)
	}
	var nilProvider *textlayer.Provider
	if frame, frameErr := nilProvider.Frame(ctx, request); frame != (textlayer.Frame{}) || !errors.Is(frameErr, textlayer.ErrProviderInvalid) {
		t.Fatalf("nil provider frame=%+v err=%v", frame, frameErr)
	}
}

func TestCompileLimitsCancellationAndDisabledText(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	hash := freezeHash(t, document)
	binding := []textlayer.FontBinding{{DocumentHash: hash, FontAssetID: "asset-font", Provider: font}}

	tiny := textlayer.DefaultConfig()
	tiny.Limits.MaxTotalStaticSurfaceBytes = cpucompositor.BytesPerPixel
	if provider, err := textlayer.Compile(ctx, document, binding, tiny); provider != nil || !errors.Is(err, textlayer.ErrResourceLimit) {
		t.Fatalf("surface-limited provider=%v err=%v", provider, err)
	}
	canonicalTiny := textlayer.DefaultConfig()
	canonicalTiny.Limits.MaxCanonicalDocumentBytes = 1
	if provider, err := textlayer.Compile(ctx, document, binding, canonicalTiny); provider != nil || !errors.Is(err, textlayer.ErrResourceLimit) {
		t.Fatalf("canonical-limited provider=%v err=%v", provider, err)
	}
	workingTiny := textlayer.DefaultConfig()
	workingTiny.Limits.MaxCompileWorkingBytes = 1
	if provider, err := textlayer.Compile(ctx, document, binding, workingTiny); provider != nil || !errors.Is(err, textlayer.ErrResourceLimit) {
		t.Fatalf("pre-freeze working-limited provider=%v err=%v", provider, err)
	}
	baseline, err := textlayer.Compile(ctx, document, binding, textlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	baselineAsset := baseline.Manifest().Assets[0]
	snapshot, diagnostics := evaluator.FreezeSnapshot(ctx, document, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20,
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	baseBytes := snapshot.CanonicalBytes() + baseline.Manifest().RetainedSurfaceBytes
	peak := baselineAsset.Assembly.RetainedBytes + baselineAsset.Raster.WorkingBytes
	if peak == 0 {
		t.Fatal("zero raster peak")
	}
	peakTiny := textlayer.DefaultConfig()
	peakTiny.Limits.MaxCompileWorkingBytes = baseBytes + peak - 1
	if provider, err := textlayer.Compile(ctx, document, binding, peakTiny); provider != nil || !errors.Is(err, textlayer.ErrResourceLimit) {
		t.Fatalf("peak-limited provider=%v err=%v", provider, err)
	}
	invalid := textlayer.DefaultConfig()
	invalid.Limits.MaxBindings = textlayer.HardMaxBindings + 1
	if provider, err := textlayer.Compile(ctx, document, binding, invalid); provider != nil || !errors.Is(err, textlayer.ErrConfigInvalid) {
		t.Fatalf("invalid-config provider=%v err=%v", provider, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if provider, err := textlayer.Compile(canceled, document, binding, textlayer.DefaultConfig()); provider != nil || !errors.Is(err, textlayer.ErrCanceled) {
		t.Fatalf("canceled provider=%v err=%v", provider, err)
	}
	if provider, err := textlayer.Compile(nil, document, binding, textlayer.DefaultConfig()); provider != nil || !errors.Is(err, textlayer.ErrContextInvalid) {
		t.Fatalf("nil-context provider=%v err=%v", provider, err)
	}

	disabled := cloneDocument(t, document)
	disabled.Timelines[0].Tracks[2].Clips[0].Disabled = true
	// Unsupported dynamic paint on a disabled clip creates no executable provider scope.
	disabled.Timelines[0].Tracks[2].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{validFillLane()}
	provider, err := textlayer.Compile(ctx, disabled, nil, textlayer.DefaultConfig())
	if err != nil || provider == nil || len(provider.Manifest().Assets) != 0 || provider.Manifest().RetainedSurfaceBytes != 0 {
		t.Fatalf("disabled provider=%v manifest=%+v err=%v", provider, provider.Manifest(), err)
	}
}

func validFillLane() documentv2.KeyframeLane {
	return documentv2.KeyframeLane{
		ID: "fill", Target: documentv2.KeyframeTargetTextFill, ValueType: documentv2.KeyframeValueColor,
		Keyframes: []documentv2.Keyframe{{
			Offset:        documentv2.RationalTime{Value: 0, Rate: 1},
			Value:         documentv2.KeyframeValue{Color: &documentv2.ColorRGBA16{R: 1, G: 2, B: 3, A: 65535}},
			Interpolation: documentv2.InterpolationHold,
		}, {
			Offset: documentv2.RationalTime{Value: 5, Rate: 1},
			Value:  documentv2.KeyframeValue{Color: &documentv2.ColorRGBA16{R: 4, G: 5, B: 6, A: 65535}},
		}},
	}
}

func frameRequest(t *testing.T, document documentv2.Document, asset textlayer.AssetReceipt, clipIndex int, timelineTime documentv2.RationalTime) textlayer.Request {
	t.Helper()
	timeline := document.Timelines[0]
	track := timeline.Tracks[2]
	clip := track.Clips[clipIndex]
	local, err := evaluator.ClipLocalTime(clip, timelineTime)
	if err != nil {
		t.Fatal(err)
	}
	source, err := evaluator.SourceTime(clip, timelineTime)
	if err != nil {
		t.Fatal(err)
	}
	return textlayer.Request{
		ContractVersion: textlayer.ContractVersion, DocumentHash: asset.Assembly.DocumentHash,
		TimelineID: timeline.ID, TimelineIndex: 0, TrackID: track.ID, TrackIndex: 2,
		TrackKind: track.Kind, ClipID: clip.ID, ClipIndex: clipIndex,
		AssetID: asset.AssetID, AssetIndex: asset.AssetIndex, AssetKind: asset.AssetKind,
		FrozenTextBindingHashProfile: asset.FrozenTextBindingHashProfile,
		FrozenTextBindingHash:        asset.FrozenTextBindingHash,
		TextSemanticHashProfile:      asset.TextSemanticHashProfile, TextSemanticHash: asset.TextSemanticHash,
		LayoutInputHashProfile: asset.LayoutInputHashProfile, LayoutInputHash: asset.LayoutInputHash,
		FontAssetID: asset.FontAssetID, FontAssetIndex: asset.FontAssetIndex,
		FontContentHashProfile: asset.FontContentHashProfile, FontContentHash: asset.FontContentHash,
		RasterViewport: asset.RasterViewport, TimelineTime: timelineTime, LocalTime: local, SourceTime: source,
		SourceProfile: textlayer.SourceProfileV1, SelectionProfile: textlayer.SelectionProfileV1,
		TextProfile:      documentv2.TextProfilePinnedFontViewportPXV2,
		EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
		RasterProfile:    documentv2.TextRasterProfileSupersample8x8V1,
		PaintProfile:     color.ProfileRGBA16SRGBPaintV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
}

func appendSecondTextClip(document *documentv2.Document) {
	first := document.Timelines[0].Tracks[2].Clips[0]
	first.ID = "clip-text-2"
	first.TimelineRange.Start = documentv2.RationalTime{Value: 5, Rate: 1}
	first.SourceRange.Start = documentv2.RationalTime{Value: 0, Rate: 1}
	first.KeyframeLanes = nil
	document.Timelines[0].Tracks[2].Clips = append(document.Timelines[0].Tracks[2].Clips, first)
}

func textDocument(t *testing.T, fontHash string) documentv2.Document {
	t.Helper()
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	document.Assets[3].ContentHash = fontHash
	lineHeight := documentv2.Rational{Numerator: 16, Denominator: 1}
	text := document.Assets[4].Text
	text.Profile = documentv2.TextProfilePinnedFontViewportPXV2
	text.Text = "A"
	text.FontSize = documentv2.Rational{Numerator: 16, Denominator: 1}
	text.LineHeight = &lineHeight
	text.Viewport = &documentv2.RasterViewport{WidthPixels: 16, HeightPixels: 16}
	text.Language = "en"
	text.Script = documentv2.TextScriptLatin
	text.Direction = documentv2.TextDirectionLTR
	text.Alignment = documentv2.TextAlignmentStart
	document.Timelines[0].Tracks[2].Clips[0].KeyframeLanes = nil
	return document
}

func cloneDocument(t *testing.T, document documentv2.Document) documentv2.Document {
	t.Helper()
	encoded, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := documentv2.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return cloned
}

func freezeHash(t *testing.T, document documentv2.Document) string {
	t.Helper()
	snapshot, diagnostics := evaluator.FreezeSnapshot(context.Background(), document, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20,
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("FreezeSnapshot snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	return snapshot.DocumentHash()
}

func buildProvider(t *testing.T, ctx context.Context) (*sfntglyf.ShapeProvider, string) {
	t.Helper()
	fixture, err := os.ReadFile("../../font/sfntglyf/testdata/minimal-font-v1.hex")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	font, fontReceipt, err := sfntglyf.Parse(ctx, encoded, sfntglyf.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	outlines, _, err := sfntglyf.DecodeOutlineFont(ctx, font, sfntglyf.DefaultOutlineLimits())
	if err != nil {
		t.Fatal(err)
	}
	geometry, _, err := sfntglyf.ValidateOutlineGeometry(ctx, outlines, sfntglyf.DefaultGeometryLimits())
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := sfntglyf.MaterializeOutlineSet(ctx, geometry, sfntglyf.DefaultOutlineSetLimits())
	if err != nil {
		t.Fatal(err)
	}
	graph, _, err := sfntglyf.BuildLayoutGraph(ctx, set, sfntglyf.DefaultLayoutGraphLimits())
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := sfntglyf.BindShapeProvider(ctx, graph, sfntglyf.DefaultShapeProviderLimits())
	if err != nil {
		t.Fatal(err)
	}
	return provider, fontReceipt.ContentHash
}

func TestPublicTypesDoNotExposeIntermediateTrustRoots(t *testing.T) {
	for _, value := range []any{textlayer.AssetReceipt{}, textlayer.FrameReceipt{}, textlayer.Manifest{}} {
		typeOf := reflect.TypeOf(value)
		for index := 0; index < typeOf.NumField(); index++ {
			field := typeOf.Field(index)
			spelling := field.Type.String()
			if strings.Contains(spelling, "FrozenTextSource") || strings.Contains(spelling, "ShapeProvider") ||
				strings.Contains(spelling, "textassembly.Source") || strings.Contains(spelling, "textlayout.Layout") {
				t.Fatalf("%s.%s exposes trust root %s", typeOf.Name(), field.Name, spelling)
			}
		}
	}
}
