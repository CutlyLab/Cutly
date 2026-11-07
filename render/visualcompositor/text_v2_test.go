package visualcompositor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

func TestStaticTextV2CompositionReceiptsAndSerializedBoundary(t *testing.T) {
	document := textV2VisualDocument(false)
	program, diagnostics := Compile(context.Background(), document, DefaultConfigV2(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	if programV1, diagnosticsV1 := Compile(context.Background(), document, DefaultConfig(2, 2)); programV1 != nil || !documentv2.HasErrorDiagnostics(diagnosticsV1) {
		t.Fatalf("v1 accepted text: program=%v diagnostics=%+v", programV1, diagnosticsV1)
	}
	capabilities := program.Capabilities()
	if capabilities.BackendID != ProfileV2 ||
		!slices.Contains(capabilities.TrackKinds, videoedit.TrackKind(documentv2.TrackKindText)) ||
		!slices.Contains(capabilities.AssetKinds, videoedit.AssetKind(documentv2.AssetKindFont)) ||
		!slices.Contains(capabilities.AssetKinds, videoedit.AssetKind(documentv2.AssetKindText)) ||
		!slices.Contains(capabilities.Features, documentv2.CapabilityTextPinnedFontViewportPXV2) {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	textOnlyConfig := DefaultConfigV2(2, 2)
	textOnlyConfig.Source = SourceCapabilities{StaticTextV2: true}
	textOnlyCapabilities, err := Capabilities(textOnlyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(textOnlyCapabilities.Features, documentv2.CapabilityTextTransitionScopeV1) ||
		slices.Contains(textOnlyCapabilities.Features, videoedit.CapabilityID(documentv2.TransitionDissolveV1)) {
		t.Fatalf("static Text advertised transition capability: %+v", textOnlyCapabilities)
	}
	if result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{}); !zeroResult(result) ||
		!errors.Is(err, ErrTextProviderUnavailable) {
		t.Fatalf("missing provider result=%+v err=%v", result, err)
	}

	fill := document.Assets[1].Text.Fill
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 50_000, A: 50_000}, {R: 40_000, A: 40_000},
		{R: 30_000, A: 30_000}, {R: 20_000, A: 20_000},
	})
	provider := fakeTextProvider(surface, fill, nil)
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Text: provider})
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != ProfileV2 || result.TextContractVersion != textlayer.ContractVersion || len(result.Sources) != 1 ||
		result.Sources[0].Kind != SourceKindText || result.Sources[0].Text == nil ||
		result.Sources[0].Native != nil || result.Sources[0].Expression != nil ||
		result.Sources[0].Evaluation.Clip == nil || result.Sources[0].Evaluation.Transition != nil ||
		result.Sources[0].Text.Request.FrozenTextBindingHash != program.assets["text"].textBindingHash {
		t.Fatalf("result=%+v", result)
	}
	if err := program.ValidateResult(context.Background(), result); err != nil {
		t.Fatalf("live validation: %v", err)
	}
	serialized := cloneResult(t, result)
	serialized.Surface = nil
	if err := program.ValidateSerializedReceipt(context.Background(), serialized); err != nil {
		t.Fatalf("serialized validation: %v", err)
	}
	mutated := serialized
	mutated.Sources = append([]SourceReceipt(nil), serialized.Sources...)
	textReceipt := *mutated.Sources[0].Text
	textReceipt.Raster.PixelHash = shaA()
	mutated.Sources[0].Text = &textReceipt
	if err := program.ValidateSerializedReceipt(context.Background(), mutated); err == nil {
		t.Fatal("mutated nested text receipt accepted")
	}

	malicious := fakeTextProvider(surface, fill, func(value *textlayer.Frame) {
		value.Receipt.Request.ClipID = "other"
	})
	if got, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Text: malicious}); err == nil || !zeroResult(got) {
		t.Fatalf("malicious request echo accepted: result=%+v err=%v", got, err)
	}
	malicious = fakeTextProvider(surface, fill, func(value *textlayer.Frame) {
		value.Receipt.SurfaceIdentity.PixelHash = shaA()
	})
	if got, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Text: malicious}); err == nil || !zeroResult(got) {
		t.Fatalf("malicious surface hash accepted: result=%+v err=%v", got, err)
	}
}

func TestStaticTextV2TransformCropOpacityKeyframes(t *testing.T) {
	document := textV2VisualDocument(true)
	program, diagnostics := Compile(context.Background(), document, DefaultConfigV2(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 65_535, A: 65_535}, {G: 65_535, A: 65_535},
		{B: 65_535, A: 65_535}, {R: 65_535, G: 65_535, B: 65_535, A: 65_535},
	})
	provider := fakeTextProvider(surface, document.Assets[1].Text.Fill, nil)
	first, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), Providers{Text: provider})
	if err != nil {
		t.Fatal(err)
	}
	middle, err := program.RenderAt(context.Background(), "timeline", rt(1, 1), Providers{Text: provider})
	if err != nil {
		t.Fatal(err)
	}
	if first.PixelHash == middle.PixelHash || first.Sources[0].Text.Request.TimelineTime != rt(0, 1) ||
		middle.Sources[0].Text.Request.TimelineTime != rt(1, 1) {
		t.Fatalf("keyframed results did not diverge: first=%+v middle=%+v", first, middle)
	}
}

func TestStaticTextV2MixedZOrderAndSubsetGates(t *testing.T) {
	document := mixedDocument()
	textOnly := textV2VisualDocument(false)
	document.Assets = append(document.Assets, textOnly.Assets...)
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, textOnly.Timelines[0].Tracks[0])
	program, diagnostics := Compile(context.Background(), document, DefaultConfigV2(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	expression, err := expressionlayer.Compile(context.Background(), document, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	native := testSurface(t, 2, 2, []cpucompositor.Pixel{{R: 20_000, A: 20_000}, {R: 20_000, A: 20_000}, {R: 20_000, A: 20_000}, {R: 20_000, A: 20_000}})
	textSurface := testSurface(t, 2, 2, []cpucompositor.Pixel{{G: 30_000, A: 30_000}, {G: 30_000, A: 30_000}, {G: 30_000, A: 30_000}, {G: 30_000, A: 30_000}})
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{
		Native: nativeProvider(native, nil), Expression: expression,
		Text: fakeTextProvider(textSurface, textOnly.Assets[1].Text.Fill, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) != 3 || result.Sources[0].Kind != SourceKindNative ||
		result.Sources[1].Kind != SourceKindExpression || result.Sources[2].Kind != SourceKindText {
		t.Fatalf("source order=%+v", result.Sources)
	}
	vector, _, err := vectorraster.Rasterize(context.Background(), *document.Assets[1].Vector, vectorraster.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want, err := cpucompositor.CompositeOver(native, vector)
	if err == nil {
		want, err = cpucompositor.CompositeOver(want, textSurface)
	}
	if err != nil {
		t.Fatal(err)
	}
	wantHash, _ := want.CanonicalHash()
	if result.PixelHash != wantHash {
		t.Fatalf("mixed z-order hash=%s want=%s", result.PixelHash, wantHash)
	}

	fillLane := textV2VisualDocument(false)
	colorValue := documentv2.ColorRGBA16{B: 1, A: 65_535}
	fillLane.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{{
		ID: "fill", Target: documentv2.KeyframeTargetTextFill, ValueType: documentv2.KeyframeValueColor,
		Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Color: &colorValue}}},
	}}
	if got, diagnostics := Compile(context.Background(), fillLane, DefaultConfigV2(2, 2)); got != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("text.fill accepted: program=%v diagnostics=%+v", got, diagnostics)
	}
	nonVisualLane := textV2VisualDocument(false)
	gain := rat(1, 1)
	nonVisualLane.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{{
		ID: "gain", Target: documentv2.KeyframeTargetAudioGain, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &gain}}},
	}}
	if got, diagnostics := Compile(context.Background(), nonVisualLane, DefaultConfigV2(2, 2)); got != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("non-visual Text lane accepted: program=%v diagnostics=%+v", got, diagnostics)
	}
	extension := textV2VisualDocument(false)
	extension.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		Extensions: []documentv2.ExtensionProperty{{Namespace: "metadata.note.v1", Value: []byte(`{"value":1}`)}},
	}
	if got, diagnostics := Compile(context.Background(), extension, DefaultConfigV2(2, 2)); got != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("Text extension accepted: program=%v diagnostics=%+v", got, diagnostics)
	}

	hardCut := textV2TransitionDocument()
	if got, diagnostics := Compile(context.Background(), hardCut, DefaultConfigV2(2, 2)); got == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("zero-width hard cut rejected: program=%v diagnostics=%+v", got, diagnostics)
	}
	hardCut.Timelines[0].Transitions[0].PreDuration = rt(1, 2)
	hardCut.Timelines[0].Transitions[0].PostDuration = rt(1, 2)
	if got, diagnostics := Compile(context.Background(), hardCut, DefaultConfigV2(2, 2)); got != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("non-zero text transition accepted: program=%v diagnostics=%+v", got, diagnostics)
	}
}

func TestDescriptorV2IsAdditiveAndV1RemainsUnchanged(t *testing.T) {
	v1 := Descriptor()
	v2 := DescriptorV2()
	if v1.Profile != ProfileV1 || v1.TextProviderProfile != "" || v1.TextContractVersion != 0 ||
		v2.Profile != ProfileV2 || v2.TextProviderProfile != textlayer.ProfileV1 ||
		v2.TextContractVersion != textlayer.ContractVersion || v2.DefaultLimits != v1.DefaultLimits {
		t.Fatalf("v1=%+v v2=%+v", v1, v2)
	}
}

func TestStaticTextV2CancellationPanicAndResourceEvidenceFailClosed(t *testing.T) {
	document := textV2VisualDocument(false)
	program, diagnostics := Compile(context.Background(), document, DefaultConfigV2(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 1, A: 1}, {R: 2, A: 2}, {R: 3, A: 3}, {R: 4, A: 4},
	})
	fill := document.Assets[1].Text.Fill
	panicking := textlayer.ProviderFunc(func(context.Context, textlayer.Request) (textlayer.Frame, error) {
		panic("provider panic must not escape")
	})
	if result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Text: panicking}); !zeroResult(result) || !errors.Is(err, ErrSourceFailed) {
		t.Fatalf("panic result=%+v err=%v", result, err)
	}

	minimum, ok := minimumTextRasterResources(*document.Assets[1].Text.Viewport, 1)
	if !ok {
		t.Fatal("minimum resource derivation failed")
	}
	tests := []struct {
		name   string
		mutate func(*textlayer.Frame)
	}{
		{name: "raster-canonical", mutate: func(frame *textlayer.Frame) { frame.Receipt.Raster.CanonicalBinaryBytes = minimum.canonical - 1 }},
		{name: "raster-work", mutate: func(frame *textlayer.Frame) { frame.Receipt.Raster.WorkUnits = minimum.work - 1 }},
		{name: "raster-working", mutate: func(frame *textlayer.Frame) { frame.Receipt.Raster.WorkingBytes = minimum.working - 1 }},
		{name: "raster-retained", mutate: func(frame *textlayer.Frame) { frame.Receipt.Raster.RetainedBytes = minimum.retained - 1 }},
		{name: "assembly-canonical", mutate: func(frame *textlayer.Frame) {
			minimum, _ := minimumTextAssemblyResources(frame.Receipt.Assembly)
			frame.Receipt.Assembly.CanonicalBinaryBytes = minimum.canonical - 1
		}},
		{name: "assembly-work", mutate: func(frame *textlayer.Frame) {
			minimum, _ := minimumTextAssemblyResources(frame.Receipt.Assembly)
			frame.Receipt.Assembly.WorkUnits = minimum.work - 1
		}},
		{name: "assembly-working", mutate: func(frame *textlayer.Frame) {
			minimum, _ := minimumTextAssemblyResources(frame.Receipt.Assembly)
			frame.Receipt.Assembly.WorkingBytes = minimum.working - 1
		}},
		{name: "assembly-retained", mutate: func(frame *textlayer.Frame) {
			minimum, _ := minimumTextAssemblyResources(frame.Receipt.Assembly)
			frame.Receipt.Assembly.RetainedBytes = minimum.retained - 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := fakeTextProvider(surface, fill, test.mutate)
			result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Text: provider})
			if !zeroResult(result) || err == nil {
				t.Fatalf("impossible %s evidence accepted: result=%+v err=%v", test.name, result, err)
			}
		})
	}

	largeDocument := textV2VisualDocument(false)
	largeDocument.Assets = make([]documentv2.Asset, 1024)
	for index := range largeDocument.Assets {
		largeDocument.Assets[index] = documentv2.Asset{ID: documentv2.ID(fmt.Sprintf("asset-%d", index)), Kind: documentv2.AssetKindImage, ContentHash: shaA()}
	}
	cancelScan := &cancelAfterErrContext{Context: context.Background(), allow: 1}
	if _, err := validateStaticTextSubset(cancelScan, largeDocument); !errors.Is(err, ErrCanceled) {
		t.Fatalf("static Text subset cancellation err=%v", err)
	}
	preCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := validateStaticTextSubset(preCanceled, largeDocument); !errors.Is(err, ErrCanceled) {
		t.Fatalf("pre-allocation static Text cancellation err=%v", err)
	}
}

func TestCancelableSurfaceHashPreservesCanceledClassification(t *testing.T) {
	pixels := make([]cpucompositor.Pixel, 64*64)
	surface, err := cpucompositor.NewSurfaceFromPixelsWithLimits(64, 64, pixels, cpucompositor.Limits{
		MaxWidth: 64, MaxHeight: 64, MaxBytes: uint64(len(pixels)) * cpucompositor.BytesPerPixel,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := &triggeredCancelContext{Context: context.Background(), triggered: true, allowAfterTrigger: 1}
	hash, err := canonicalSurfaceHash(ctx, surface, "test/hash", CodeSourceMismatch, ErrSourceMismatch)
	var typed *Error
	if hash != "" || !errors.Is(err, ErrCanceled) || !errors.As(err, &typed) || typed.Code != CodeCanceled || ctx.checksAfterTrigger < 3 {
		t.Fatalf("hash=%q err=%v typed=%+v checks=%d", hash, err, typed, ctx.checksAfterTrigger)
	}
}

type triggeredCancelContext struct {
	context.Context
	triggered          bool
	allowAfterTrigger  int
	checksAfterTrigger int
}

func (value *triggeredCancelContext) Err() error {
	if !value.triggered {
		return nil
	}
	value.checksAfterTrigger++
	if value.checksAfterTrigger > value.allowAfterTrigger {
		return context.Canceled
	}
	return nil
}

func textV2VisualDocument(withProperties bool) documentv2.Document {
	lineHeight := rat(1, 1)
	clip := documentv2.Clip{ID: "text-clip", AssetID: "text", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}
	if withProperties {
		clip.PropertySet = &documentv2.PropertySet{
			Transform: &documentv2.TransformProperty{
				Position: point(0, 1, 0, 1), Scale: point(1, 1, 1, 1), RotationDegrees: rat(0, 1), Anchor: point(1, 2, 1, 2),
			},
			Crop: &documentv2.CropProperty{Rect: documentv2.Rect{
				X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 1), Height: rat(1, 1),
			}},
			Opacity: &documentv2.OpacityProperty{Value: rat(1, 1)},
		}
		positionStart, positionEnd := point(0, 1, 0, 1), point(1, 1, 0, 1)
		opacityStart, opacityEnd := rat(1, 1), rat(1, 2)
		cropStart := documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 1), Height: rat(1, 1)}
		cropEnd := documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 2), Height: rat(1, 1)}
		clip.KeyframeLanes = []documentv2.KeyframeLane{
			{ID: "position", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint, Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &positionStart}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Point: &positionEnd}},
			}},
			{ID: "crop", Target: documentv2.KeyframeTargetCrop, ValueType: documentv2.KeyframeValueRect, Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Rect: &cropStart}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Rect: &cropEnd}},
			}},
			{ID: "opacity", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &opacityStart}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: &opacityEnd}},
			}},
		}
	}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "text-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "font", Kind: documentv2.AssetKindFont, URI: "font.ttf", ContentHash: shaA()},
			{ID: "text", Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{
				Profile: documentv2.TextProfilePinnedFontViewportPXV2, Text: "A", FontAssetID: "font",
				FontSize: rat(1, 1), LineHeight: &lineHeight,
				Viewport: &documentv2.RasterViewport{WidthPixels: 2, HeightPixels: 2},
				Fill:     documentv2.ColorRGBA16{R: 65_535, G: 50_000, A: 65_535}, Language: "en",
				Script: documentv2.TextScriptLatin, Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart,
			}},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "text-track", Kind: documentv2.TrackKindText, Clips: []documentv2.Clip{clip},
		}}}},
	}
}

func textV2TransitionDocument() documentv2.Document {
	result := textV2VisualDocument(false)
	track := &result.Timelines[0].Tracks[0]
	track.Clips[0].TimelineRange = tr(0, 2)
	track.Clips[0].SourceRange = tr(0, 2)
	incoming := track.Clips[0]
	incoming.ID = "text-incoming"
	incoming.TimelineRange = tr(2, 2)
	track.Clips = append(track.Clips, incoming)
	result.Timelines[0].Transitions = []documentv2.Transition{{
		ID: "text-cut", Profile: documentv2.TransitionDissolveV1,
		OutgoingClipID: "text-clip", IncomingClipID: "text-incoming",
		PreDuration: rt(0, 1), PostDuration: rt(0, 1),
	}}
	return result
}

func fakeTextProvider(
	surface *cpucompositor.Surface,
	fill documentv2.ColorRGBA16,
	mutate func(*textlayer.Frame),
) textlayer.TextLayerProvider {
	return textlayer.ProviderFunc(func(ctx context.Context, request textlayer.Request) (textlayer.Frame, error) {
		pixelHash, err := surface.CanonicalHashContext(ctx)
		if err != nil {
			return textlayer.Frame{}, err
		}
		minimumRaster, ok := minimumTextRasterResources(request.RasterViewport, 1)
		if !ok {
			return textlayer.Frame{}, errors.New("test raster resource overflow")
		}
		assembly := textassembly.Receipt{
			SchemaVersion: textassembly.SchemaVersion, Profile: textassembly.ReceiptProfileV1,
			DescriptorHashProfile: textassembly.DescriptorHashProfileV1, DescriptorHash: textassembly.DescriptorHash(),
			AssemblyHashProfile: textassembly.AssemblyHashProfileV1, AssemblyHash: shaB(),
			FrozenTextProfile:            evaluator.FrozenTextSourceProfileV1,
			FrozenTextBindingHashProfile: request.FrozenTextBindingHashProfile,
			FrozenTextBindingHash:        request.FrozenTextBindingHash,
			DocumentHashProfile:          evaluator.FrozenTextSourceDocumentHashProfileV1, DocumentHash: request.DocumentHash,
			TextAssetID: string(request.AssetID), TextAssetIndex: uint64(request.AssetIndex),
			FontAssetID: string(request.FontAssetID), FontAssetIndex: uint64(request.FontAssetIndex),
			FontContentHashProfile: request.FontContentHashProfile, FontContentHash: request.FontContentHash,
			TextSemanticHashProfile: request.TextSemanticHashProfile, TextSemanticHash: request.TextSemanticHash,
			LayoutInputHashProfile: request.LayoutInputHashProfile, LayoutInputHash: request.LayoutInputHash,
			ShapeProviderProfile:     sfntglyf.ShapeProviderProfileV1,
			ShapeProviderHashProfile: sfntglyf.ShapeProviderHashProfileV1, ShapeProviderHash: shaA(),
			DependencyFingerprintProfile: sfntglyf.ShapeProviderDescriptor().DependencyFingerprintProfile,
			DependencyFingerprint:        sfntglyf.ShapeProviderDescriptor().DependencyFingerprint,
			LayoutGraphHashProfile:       sfntglyf.LayoutGraphCanonicalHashProfileV1, LayoutGraphHash: shaA(),
			SourceOutlineSetHashProfile: sfntglyf.OutlineSetHashProfileV1, SourceOutlineSetHash: shaA(),
			LayoutSourceHashProfile: sfntglyf.LayoutSourceHashProfileV1, LayoutSourceHash: shaA(),
			ShapeInputHashProfile: sfntglyf.ShapeInputHashProfileV1, ShapeInputHash: shaA(),
			ShapeOutputProfile:     sfntglyf.ShapeOutputProfileV1,
			ShapeOutputHashProfile: sfntglyf.ShapeOutputHashProfileV1, ShapeOutputHash: shaA(),
			LayoutReceiptProfile:   textlayout.ReceiptProfileV1,
			GlyphStreamHashProfile: textlayout.GlyphStreamHashProfileV1, GlyphStreamHash: shaA(),
			ScaledOutlineHashProfile: textlayout.OutlineSetHashProfileV1, ScaledOutlineHash: shaA(),
			PlacementHashProfile: textlayout.PlacementHashProfileV1, PlacementHash: shaA(),
			LayoutHashProfile: textlayout.LayoutHashProfileV1, LayoutHash: shaA(),
			ShapingProvenanceStatus: textassembly.ShapingProvenanceStatusV1,
			PaintProvenanceStatus:   textassembly.PaintProvenanceStatusV1, CapabilityStatus: textassembly.CapabilityStatusV1,
			WidthPixels: request.RasterViewport.WidthPixels, HeightPixels: request.RasterViewport.HeightPixels,
			FontSizeNumerator: 1, FontSizeDenominator: 1,
			FillR: fill.R, FillG: fill.G, FillB: fill.B, FillA: fill.A,
			LineCount: 1, GlyphCount: 1, UniqueGlyphCount: 1,
		}
		minimumAssembly, ok := minimumTextAssemblyResources(assembly)
		if !ok {
			return textlayer.Frame{}, errors.New("test assembly resource overflow")
		}
		assembly.CanonicalBinaryBytes = minimumAssembly.canonical
		assembly.WorkUnits = minimumAssembly.work
		assembly.WorkingBytes = minimumAssembly.working
		assembly.RetainedBytes = minimumAssembly.retained
		raster := textassembly.RasterReceipt{
			SchemaVersion: textassembly.SchemaVersion, Profile: textassembly.RasterReceiptProfileV1,
			DescriptorHashProfile: textassembly.DescriptorHashProfileV1, DescriptorHash: textassembly.DescriptorHash(),
			BindingHashProfile: textassembly.RasterBindingHashProfileV1, BindingHash: shaB(),
			AssemblyHashProfile: textassembly.AssemblyHashProfileV1, AssemblyHash: assembly.AssemblyHash,
			InnerReceiptProfile: textraster.ReceiptProfileV1, InnerRasterProfile: textraster.ProfileV1,
			InnerBindingHashProfile: textraster.BindingHashProfileV1, InnerBindingHash: shaA(),
			InnerShapingStatus:      textraster.ShapingProvenanceStatusV1,
			ShapingProvenanceStatus: textassembly.ShapingProvenanceStatusV1,
			PaintProvenanceStatus:   textassembly.PaintProvenanceStatusV1,
			CoverageHashProfile:     textraster.CoverageHashProfileV1, CoverageHash: shaA(),
			PixelHashProfile: textraster.PixelHashProfileV1, PixelHash: pixelHash,
			WidthPixels: request.RasterViewport.WidthPixels, HeightPixels: request.RasterViewport.HeightPixels,
			CanonicalBinaryBytes: minimumRaster.canonical, WorkUnits: minimumRaster.work,
			WorkingBytes: minimumRaster.working, RetainedBytes: minimumRaster.retained,
		}
		frame := textlayer.Frame{Receipt: textlayer.FrameReceipt{
			ContractVersion: textlayer.ContractVersion, Request: request, Assembly: assembly, Raster: raster,
			SurfaceIdentity: textlayer.SurfaceIdentity{
				WidthPixels: request.RasterViewport.WidthPixels, HeightPixels: request.RasterViewport.HeightPixels,
				PixelHashProfile: textlayer.SurfaceHashProfileV1, PixelHash: pixelHash,
				SurfaceProfile: cpucompositor.SurfaceProfileV1,
			},
		}, Surface: surface}
		if mutate != nil {
			mutate(&frame)
		}
		return frame, nil
	})
}
