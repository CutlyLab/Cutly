package visualcompositor

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

func TestImageOnlyPixelParityWithRasterCompositorV1(t *testing.T) {
	document := imageDocument(false)
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 65_535, A: 65_535}, {G: 65_535, A: 65_535},
		{B: 65_535, A: 65_535}, {R: 20_000, G: 30_000, B: 40_000, A: 65_535},
	})
	provider := nativeProvider(surface, nil)

	visual, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if visual == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("visual=%v diagnostics=%+v", visual, diagnostics)
	}
	legacy, diagnostics := rastercompositor.Compile(document, rastercompositor.DefaultConfig(2, 2))
	if legacy == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("legacy=%v diagnostics=%+v", legacy, diagnostics)
	}
	at := rt(1, 2)
	got, err := visual.RenderAt(context.Background(), "timeline", at, Providers{Native: provider})
	if err != nil {
		t.Fatal(err)
	}
	want, err := legacy.RenderAt(context.Background(), "timeline", at, provider)
	if err != nil {
		t.Fatal(err)
	}
	if got.PixelHash != want.PixelHash || got.Width != want.Width || got.Height != want.Height || len(got.Sources) != 1 ||
		got.Sources[0].Kind != SourceKindNative || got.Sources[0].Native == nil || got.Sources[0].Expression != nil {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	assertSurfaceHash(t, got.Surface, want.PixelHash)
	frame, err := visual.RenderFrame(context.Background(), "timeline", videoedit.FrameClock{
		Origin: rt(0, 1), FramesPerSecond: rt(2, 1),
	}, 1, Providers{Native: provider})
	if err != nil || frame.CompositionFrameIndex != 1 || frame.Result.TimelineTime != at || frame.Result.PixelHash != got.PixelHash {
		t.Fatalf("frame=%+v err=%v RenderAt=%+v", frame, err, got)
	}
}

func TestImageTransformCropOpacityAndKeyframeParity(t *testing.T) {
	document := imageDocument(true)
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 65_535, A: 65_535}, {G: 65_535, A: 65_535},
		{B: 65_535, A: 65_535}, {R: 65_535, G: 65_535, B: 65_535, A: 65_535},
	})
	provider := nativeProvider(surface, nil)
	visualConfig := DefaultConfig(2, 2)
	visual, diagnostics := Compile(context.Background(), document, visualConfig)
	if visual == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("visual diagnostics=%+v", diagnostics)
	}
	legacy, diagnostics := rastercompositor.Compile(document, rastercompositor.DefaultConfig(2, 2))
	if legacy == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("legacy diagnostics=%+v", diagnostics)
	}
	for _, at := range []documentv2.RationalTime{rt(0, 1), rt(1, 2), rt(3, 2)} {
		got, gotErr := visual.RenderAt(context.Background(), "timeline", at, Providers{Native: provider})
		want, wantErr := legacy.RenderAt(context.Background(), "timeline", at, provider)
		if gotErr != nil || wantErr != nil || got.PixelHash != want.PixelHash {
			t.Fatalf("at=%v got=%+v gotErr=%v want=%+v wantErr=%v", at, got, gotErr, want, wantErr)
		}
	}
}

func TestVectorOnlyAndImageVectorInterleavedZOrder(t *testing.T) {
	document := mixedDocument()
	nativeSurface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 30_000, A: 30_000}, {R: 30_000, A: 30_000},
		{R: 30_000, A: 30_000}, {R: 30_000, A: 30_000},
	})
	expression, err := expressionlayer.Compile(context.Background(), document, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{
		Native: nativeProvider(nativeSurface, nil), Expression: expression,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) != 2 || result.Sources[0].Kind != SourceKindNative || result.Sources[1].Kind != SourceKindExpression ||
		result.Sources[0].Evaluation.Clip == nil || result.Sources[1].Evaluation.Clip == nil ||
		result.Sources[0].Evaluation.Clip.TrackIndex != 0 || result.Sources[1].Evaluation.Clip.TrackIndex != 1 {
		t.Fatalf("ordered receipts=%+v", result.Sources)
	}
	vectorSurface, _, err := vectorraster.Rasterize(context.Background(), *document.Assets[1].Vector, vectorraster.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want, err := cpucompositor.CompositeOver(nativeSurface, vectorSurface)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, _ := want.CanonicalHash()
	if result.PixelHash != wantHash {
		t.Fatalf("z-order hash=%s want=%s", result.PixelHash, wantHash)
	}

	vectorOnly := document
	vectorOnly.Timelines[0].Tracks = vectorOnly.Timelines[0].Tracks[1:]
	vectorOnlyProgram, diagnostics := Compile(context.Background(), vectorOnly, DefaultConfig(2, 2))
	if vectorOnlyProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("vector-only diagnostics=%+v", diagnostics)
	}
	vectorOnlyProvider, err := expressionlayer.Compile(context.Background(), vectorOnly, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	vectorOnlyResult, err := vectorOnlyProgram.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Expression: vectorOnlyProvider})
	if err != nil || vectorOnlyResult.PixelHash != result.Sources[1].Expression.SurfaceIdentity.PixelHash {
		t.Fatalf("vector-only result=%+v err=%v", vectorOnlyResult, err)
	}
}

func TestVectorDissolveUsesActualHandleContractAndOrderedReceipts(t *testing.T) {
	document := vectorTransitionDocument()
	expression, err := expressionlayer.Compile(context.Background(), document, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	for _, test := range []struct {
		at              documentv2.RationalTime
		outgoingVisible bool
		incomingVisible bool
	}{
		{at: rt(3, 2), outgoingVisible: true, incomingVisible: false},
		{at: rt(5, 2), outgoingVisible: false, incomingVisible: true},
	} {
		result, renderErr := program.RenderAt(context.Background(), "timeline", test.at, Providers{Expression: expression})
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		if len(result.Sources) != 2 {
			t.Fatalf("sources=%+v", result.Sources)
		}
		outgoing, incoming := result.Sources[0], result.Sources[1]
		if outgoing.Kind != SourceKindExpression || incoming.Kind != SourceKindExpression ||
			outgoing.Evaluation.Clip != nil || incoming.Evaluation.Clip != nil ||
			outgoing.Evaluation.Transition == nil || incoming.Evaluation.Transition == nil ||
			outgoing.Evaluation.Transition.TransitionID != "vector-dissolve" || incoming.Evaluation.Transition.TransitionID != "vector-dissolve" ||
			outgoing.Evaluation.Transition.Role != expressionlayer.TransitionHandleOutgoing ||
			incoming.Evaluation.Transition.Role != expressionlayer.TransitionHandleIncoming ||
			outgoing.Expression.TransitionRequest == nil || incoming.Expression.TransitionRequest == nil ||
			outgoing.Expression.VisibleRequest != nil || incoming.Expression.VisibleRequest != nil ||
			outgoing.Expression.TransitionRequest.Role != expressionlayer.TransitionHandleOutgoing ||
			incoming.Expression.TransitionRequest.Role != expressionlayer.TransitionHandleIncoming ||
			outgoing.Expression.TransitionRequest.TimelineTime != test.at || incoming.Expression.TransitionRequest.TimelineTime != test.at ||
			outgoing.Evaluation.Transition.Clip.Visible != test.outgoingVisible ||
			incoming.Evaluation.Transition.Clip.Visible != test.incomingVisible {
			t.Fatalf("at=%v outgoing=%+v incoming=%+v", test.at, outgoing, incoming)
		}
		evaluated, evalErr := program.evaluator.EvaluateTimeline("timeline", test.at)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		red, _, _ := vectorraster.Rasterize(context.Background(), *document.Assets[0].Vector, vectorraster.DefaultLimits())
		blue, _, _ := vectorraster.Rasterize(context.Background(), *document.Assets[1].Vector, vectorraster.DefaultLimits())
		want, dissolveErr := cpucompositor.Dissolve(red, blue,
			evaluated.Layers[0].Transition.OutgoingWeight, evaluated.Layers[0].Transition.IncomingWeight)
		if dissolveErr != nil {
			t.Fatal(dissolveErr)
		}
		wantHash, _ := want.CanonicalHash()
		if result.PixelHash != wantHash {
			t.Fatalf("at=%v hash=%s want=%s", test.at, result.PixelHash, wantHash)
		}
	}
}

func TestImageWipeAndFadeRenderEndToEndWithScratchParity(t *testing.T) {
	outgoing := testSurface(t, 4, 1, []cpucompositor.Pixel{
		{R: 65_535, A: 65_535}, {R: 65_535, A: 65_535}, {R: 65_535, A: 65_535}, {R: 65_535, A: 65_535},
	})
	incoming := testSurface(t, 4, 1, []cpucompositor.Pixel{
		{B: 65_535, A: 65_535}, {B: 65_535, A: 65_535}, {B: 65_535, A: 65_535}, {B: 65_535, A: 65_535},
	})
	provider := rastercompositor.NativeSourceProviderFunc(func(_ context.Context, request rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
		surface := outgoing
		if request.AssetID == "image-b" {
			surface = incoming
		}
		hash, err := surface.CanonicalHash()
		if err != nil {
			return rastercompositor.NativeSourceFrame{}, err
		}
		return rastercompositor.NativeSourceFrame{
			ContractVersion: request.ContractVersion, Request: request,
			Width: surface.Width(), Height: surface.Height(), AssetContentHash: request.AssetContentHash,
			PixelHash: hash, DecodeProfile: request.DecodeProfile, SelectionProfile: request.SelectionProfile,
			ColorProfile: request.ColorProfile, NormalizationProfile: request.NormalizationProfile,
			SourceProfile: request.SourceProfile, SurfaceProfile: request.SurfaceProfile, Surface: surface,
		}, nil
	})
	for _, test := range []struct {
		profile string
		want    []cpucompositor.Pixel
	}{
		{documentv2.TransitionWipeV1, []cpucompositor.Pixel{
			{B: 65_535, A: 65_535}, {B: 65_535, A: 65_535}, {R: 65_535, A: 65_535}, {R: 65_535, A: 65_535},
		}},
		{documentv2.TransitionFadeV1, []cpucompositor.Pixel{
			{A: 65_535}, {A: 65_535}, {A: 65_535}, {A: 65_535},
		}},
	} {
		t.Run(test.profile, func(t *testing.T) {
			document := imageTransitionDocument()
			document.Timelines[0].Transitions[0].ID = "transition"
			document.Timelines[0].Transitions[0].Profile = test.profile
			program, diagnostics := Compile(context.Background(), document, DefaultConfig(4, 1))
			if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
			if !slices.Contains(program.Capabilities().Features, videoedit.CapabilityID(test.profile)) {
				t.Fatalf("capabilities omit %q: %+v", test.profile, program.Capabilities())
			}
			got, err := program.RenderAt(context.Background(), "timeline", rt(2, 1), Providers{Native: provider})
			if err != nil {
				t.Fatal(err)
			}
			want := testSurface(t, 4, 1, test.want)
			wantHash, _ := want.CanonicalHash()
			if got.PixelHash != wantHash || len(got.Sources) != 2 {
				t.Fatalf("result hash=%s sources=%+v want=%s", got.PixelHash, got.Sources, wantHash)
			}
		})
	}
}

func TestMaliciousProviderEchoHashAndScopeFailClosed(t *testing.T) {
	document := imageDocument(false)
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{{A: 1}, {A: 1}, {A: 1}, {A: 1}})
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	for _, mutate := range []func(*rastercompositor.NativeSourceFrame){
		func(value *rastercompositor.NativeSourceFrame) { value.Request.ClipID = "other" },
		func(value *rastercompositor.NativeSourceFrame) { value.PixelHash = shaA() },
		func(value *rastercompositor.NativeSourceFrame) { value.AssetContentHash = shaA() },
	} {
		provider := nativeProvider(surface, mutate)
		if result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Native: provider}); err == nil || !zeroResult(result) {
			t.Fatalf("malicious native accepted: result=%+v err=%v", result, err)
		}
	}

	vectorDocument := mixedDocument()
	vectorDocument.Timelines[0].Tracks = vectorDocument.Timelines[0].Tracks[1:]
	base, err := expressionlayer.Compile(context.Background(), vectorDocument, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	vectorProgram, diagnostics := Compile(context.Background(), vectorDocument, DefaultConfig(2, 2))
	if vectorProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	malicious := expressionWrapper{base: base, mutateFrame: func(value *expressionlayer.Frame) {
		value.Request.AssetSemanticHash = shaA()
	}}
	if result, err := vectorProgram.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Expression: malicious}); err == nil || !zeroResult(result) {
		t.Fatalf("malicious expression accepted: result=%+v err=%v", result, err)
	}

	transitionDocument := vectorTransitionDocument()
	transitionBase, err := expressionlayer.Compile(context.Background(), transitionDocument, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	transitionProgram, diagnostics := Compile(context.Background(), transitionDocument, DefaultConfig(2, 2))
	if transitionProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("transition diagnostics=%+v", diagnostics)
	}
	for _, mutate := range []func(*expressionlayer.TransitionHandleFrame){
		func(value *expressionlayer.TransitionHandleFrame) { value.Request.TimelineIndex++ },
		func(value *expressionlayer.TransitionHandleFrame) {
			value.Request.Role = expressionlayer.TransitionHandleIncoming
		},
		func(value *expressionlayer.TransitionHandleFrame) { value.SurfaceIdentity.PixelHash = shaA() },
		func(value *expressionlayer.TransitionHandleFrame) { value.RasterReceipt.WorkingBytes = math.MaxUint64 },
	} {
		provider := expressionWrapper{base: transitionBase, mutateTransition: mutate}
		if result, renderErr := transitionProgram.RenderAt(context.Background(), "timeline", rt(3, 2), Providers{Expression: provider}); renderErr == nil || !zeroResult(result) {
			t.Fatalf("malicious transition response accepted: result=%+v err=%v", result, renderErr)
		}
	}
}

func TestValidateResultAuthenticatesNativeTransitionPairsAndSeparatesSerializedBoundary(t *testing.T) {
	document := imageTransitionDocument()
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{{R: 10, A: 10}, {R: 20, A: 20}, {R: 30, A: 30}, {R: 40, A: 40}})
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(3, 2), Providers{Native: nativeProvider(surface, nil)})
	if err != nil || len(result.Sources) != 2 || result.Sources[0].Evaluation.Transition == nil || result.Sources[1].Evaluation.Transition == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := program.ValidateResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{"missing pair", func(value *Result) { value.Sources = value.Sources[:1] }},
		{"duplicate outgoing", func(value *Result) { value.Sources[1] = value.Sources[0] }},
		{"transition id", func(value *Result) { value.Sources[0].Evaluation.Transition.TransitionID = "other" }},
		{"transition index", func(value *Result) { value.Sources[0].Evaluation.Transition.TransitionIndex++ }},
		{"transition profile", func(value *Result) {
			value.Sources[0].Evaluation.Transition.TransitionProfile = "cutly.transition.other.v1"
		}},
		{"transition role", func(value *Result) {
			value.Sources[0].Evaluation.Transition.Role = expressionlayer.TransitionHandleIncoming
		}},
		{"native replay", func(value *Result) {
			value.Sources[1].Native = value.Sources[0].Native
			value.Sources[1].Evaluation.Transition.Clip = value.Sources[0].Evaluation.Transition.Clip
		}},
		{"nested document", func(value *Result) { value.Sources[0].Native.Request.DocumentHash = shaA() }},
		{"nested contract", func(value *Result) { value.Sources[0].Native.ContractVersion++ }},
		{"build", func(value *Result) { value.BuildFingerprint = "other" }},
		{"geometry", func(value *Result) { value.Width++ }},
		{"final hash", func(value *Result) { value.PixelHash = shaA() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := cloneResult(t, result)
			test.mutate(&mutated)
			if err := program.ValidateResult(context.Background(), mutated); err == nil {
				t.Fatalf("hostile result accepted: %+v", mutated)
			}
		})
	}

	serialized := cloneResult(t, result)
	serialized.Surface = nil
	serialized.PixelHash = shaA()
	if err := program.ValidateResult(context.Background(), serialized); err == nil {
		t.Fatal("strict validation accepted a surface-free receipt")
	}
	if err := program.ValidateSerializedReceipt(context.Background(), serialized); err != nil {
		t.Fatalf("structural validation exceeded its documented pixel-evidence boundary: %v", err)
	}
}

func TestGapArbitrarySeekConcurrencyCancellationAndLimits(t *testing.T) {
	document := imageDocument(false)
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{{R: 10, A: 10}, {R: 20, A: 20}, {R: 30, A: 30}, {R: 40, A: 40}})
	provider := nativeProvider(surface, nil)
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	providers := Providers{Native: provider}
	gap, err := program.RenderAt(context.Background(), "timeline", rt(10, 1), providers)
	if err != nil || len(gap.Sources) != 0 || gap.Sources == nil {
		t.Fatalf("gap=%+v err=%v", gap, err)
	}
	transparent, _ := cpucompositor.NewSurface(2, 2)
	transparentHash, _ := transparent.CanonicalHash()
	if gap.PixelHash != transparentHash {
		t.Fatalf("gap hash=%s want=%s", gap.PixelHash, transparentHash)
	}
	gapCancel := &cancelAfterErrContext{Context: context.Background(), allow: 1}
	if result, err := program.RenderAt(gapCancel, "timeline", rt(10, 1), providers); !zeroResult(result) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("pre-allocation gap cancel result=%+v err=%v", result, err)
	}

	want, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), providers)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []documentv2.RationalTime{rt(10, 1), rt(3, 2), rt(1, 2), rt(-1, 1), rt(1, 2)} {
		got, seekErr := program.RenderAt(context.Background(), "timeline", at, providers)
		if seekErr != nil {
			t.Fatal(seekErr)
		}
		if at == rt(1, 2) && got.PixelHash != want.PixelHash {
			t.Fatalf("seek drift=%s want=%s", got.PixelHash, want.PixelHash)
		}
	}
	var wait sync.WaitGroup
	errs := make(chan error, 32)
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, renderErr := program.RenderAt(context.Background(), "timeline", rt(1, 2), providers)
			if renderErr == nil && got.PixelHash != want.PixelHash {
				renderErr = errors.New("concurrent pixel drift")
			}
			errs <- renderErr
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := program.RenderAt(ctx, "timeline", rt(1, 2), providers); !zeroResult(result) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled result=%+v err=%v", result, err)
	}

	transitionDocument := vectorTransitionDocument()
	config := DefaultConfig(2, 2)
	config.Limits.MaxSourceRequests = 1
	limited, diagnostics := Compile(context.Background(), transitionDocument, config)
	if limited == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("limited compile diagnostics=%+v", diagnostics)
	}
	expression, err := expressionlayer.Compile(context.Background(), transitionDocument, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := limited.RenderAt(context.Background(), "timeline", rt(3, 2), Providers{Expression: expression}); !zeroResult(result) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("limit result=%+v err=%v", result, err)
	}
}

func TestCapabilitiesDeclareOnlyEnabledVisualSourcesAndRejectTextAudio(t *testing.T) {
	config := DefaultConfig(2, 2)
	config.Source.Video = false
	config.Source.Image = false
	capabilities, err := Capabilities(config)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(capabilities.TrackKinds, []videoedit.TrackKind{videoedit.TrackKind(documentv2.TrackKindVector)}) ||
		!slices.Equal(capabilities.AssetKinds, []videoedit.AssetKind{videoedit.AssetKind(documentv2.AssetKindVector)}) ||
		slices.Contains(capabilities.TrackKinds, videoedit.TrackKind(documentv2.TrackKindText)) ||
		slices.Contains(capabilities.TrackKinds, videoedit.TrackKind(documentv2.TrackKindAudio)) {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	if !slices.Contains(capabilities.Features, documentv2.CapabilityVectorTransitionScopeV1) {
		t.Fatalf("scoped vector transition capability missing: %+v", capabilities)
	}
	capabilities.Features[0] = "mutated.v1"
	again, err := Capabilities(config)
	if err != nil || slices.Contains(again.Features, videoedit.CapabilityID("mutated.v1")) {
		t.Fatalf("capability storage aliased caller: %+v err=%v", again, err)
	}

	text := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "text-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "font", Kind: documentv2.AssetKindFont, URI: "font.ttf", ContentHash: shaB()}, {
			ID: "text", Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{
				Text: "x", FontAssetID: "font", FontSize: rat(12, 1), Fill: documentv2.ColorRGBA16{A: 65_535},
				Language: "en", Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart,
			},
		}}, Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "text-track", Kind: documentv2.TrackKindText,
			Clips: []documentv2.Clip{{ID: "text-clip", AssetID: "text", TimelineRange: tr(0, 1), SourceRange: tr(0, 1)}}}}}}}
	if program, diagnostics := Compile(context.Background(), text, DefaultConfig(2, 2)); program != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("text program=%v diagnostics=%+v", program, diagnostics)
	}
	duration := rt(2, 1)
	audio := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "audio-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "audio", Kind: documentv2.AssetKindAudio, URI: "audio.wav", ContentHash: shaB(), Duration: &duration}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "audio-track", Kind: documentv2.TrackKindAudio,
			Clips: []documentv2.Clip{{ID: "audio-clip", AssetID: "audio", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}}}}}}
	if program, diagnostics := Compile(context.Background(), audio, DefaultConfig(2, 2)); program != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("audio program=%v diagnostics=%+v", program, diagnostics)
	}
}

func TestNilContextAndTypedNilProvidersFailWithoutPartialResults(t *testing.T) {
	document := imageDocument(false)
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{{A: 1}, {A: 1}, {A: 1}, {A: 1}})
	if result, err := program.RenderAt(nil, "timeline", rt(0, 1), Providers{Native: nativeProvider(surface, nil)}); err == nil || !zeroResult(result) {
		t.Fatalf("nil context result=%+v err=%v", result, err)
	}
	var typedNil rastercompositor.NativeSourceProviderFunc
	if result, err := program.RenderAt(context.Background(), "timeline", rt(0, 1), Providers{Native: typedNil}); err == nil || !zeroResult(result) {
		t.Fatalf("typed nil native result=%+v err=%v", result, err)
	}
	if compiled, diagnostics := Compile(nil, document, DefaultConfig(2, 2)); compiled != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("nil compile context program=%v diagnostics=%+v", compiled, diagnostics)
	}

	vector := mixedDocument()
	vector.Timelines[0].Tracks = vector.Timelines[0].Tracks[1:]
	vectorProgram, diagnostics := Compile(context.Background(), vector, DefaultConfig(2, 2))
	if vectorProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("vector diagnostics=%+v", diagnostics)
	}
	var expression *expressionlayer.Provider
	if result, err := vectorProgram.RenderAt(context.Background(), "timeline", rt(0, 1), Providers{Expression: expression}); err == nil || !zeroResult(result) {
		t.Fatalf("typed nil expression result=%+v err=%v", result, err)
	}
}

type expressionWrapper struct {
	base             *expressionlayer.Provider
	mutateFrame      func(*expressionlayer.Frame)
	mutateTransition func(*expressionlayer.TransitionHandleFrame)
}

type cancelAfterErrContext struct {
	context.Context
	allow int
}

func (value *cancelAfterErrContext) Err() error {
	if value.allow > 0 {
		value.allow--
		return nil
	}
	return context.Canceled
}

func (value expressionWrapper) Frame(ctx context.Context, request expressionlayer.Request) (expressionlayer.Frame, error) {
	frame, err := value.base.Frame(ctx, request)
	if err == nil && value.mutateFrame != nil {
		value.mutateFrame(&frame)
	}
	return frame, err
}

func (value expressionWrapper) TransitionHandle(ctx context.Context, request expressionlayer.TransitionHandleRequest) (expressionlayer.TransitionHandleFrame, error) {
	frame, err := value.base.TransitionHandle(ctx, request)
	if err == nil && value.mutateTransition != nil {
		value.mutateTransition(&frame)
	}
	return frame, err
}

func nativeProvider(surface *cpucompositor.Surface, mutate func(*rastercompositor.NativeSourceFrame)) rastercompositor.NativeSourceProvider {
	return rastercompositor.NativeSourceProviderFunc(func(_ context.Context, request rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
		hash, err := surface.CanonicalHash()
		if err != nil {
			return rastercompositor.NativeSourceFrame{}, err
		}
		frame := rastercompositor.NativeSourceFrame{
			ContractVersion: request.ContractVersion, Request: request,
			Width: surface.Width(), Height: surface.Height(), AssetContentHash: request.AssetContentHash,
			PixelHash: hash, DecodeProfile: request.DecodeProfile, SelectionProfile: request.SelectionProfile,
			ColorProfile: request.ColorProfile, NormalizationProfile: request.NormalizationProfile,
			SourceProfile: request.SourceProfile, SurfaceProfile: request.SurfaceProfile, Surface: surface,
		}
		if mutate != nil {
			mutate(&frame)
		}
		return frame, nil
	})
}

func imageDocument(withProperties bool) documentv2.Document {
	clip := documentv2.Clip{ID: "image-clip", AssetID: "image", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}
	if withProperties {
		clip.PropertySet = &documentv2.PropertySet{
			Transform: &documentv2.TransformProperty{
				Position: point(0, 1, 0, 1), Scale: point(1, 1, 1, 1), RotationDegrees: rat(0, 1), Anchor: point(1, 2, 1, 2),
			},
			Crop:    &documentv2.CropProperty{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(1, 2), Height: rat(1, 1)}},
			Opacity: &documentv2.OpacityProperty{Value: rat(1, 1)},
		}
		positionStart, positionEnd := point(0, 1, 0, 1), point(1, 1, 0, 1)
		opacityStart, opacityEnd := rat(1, 1), rat(1, 2)
		clip.KeyframeLanes = []documentv2.KeyframeLane{{
			ID: "position-lane", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint,
			Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &positionStart}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Point: &positionEnd}},
			},
		}, {
			ID: "opacity-lane", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &opacityStart}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: &opacityEnd}},
			},
		}}
	}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "image-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png", ContentHash: shaB()}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "image-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{clip},
		}}}},
	}
}

func mixedDocument() documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "mixed-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png", ContentHash: shaB()},
			vectorAsset("vector", "vector-path", documentv2.ColorRGBA16{B: 65_535, A: 32_768}),
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "image-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "image-clip", AssetID: "image", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}},
			{ID: "vector-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{ID: "vector-clip", AssetID: "vector", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}},
		}}},
	}
}

func vectorTransitionDocument() documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "vector-transition-document", Revision: 1,
		Assets: []documentv2.Asset{
			vectorAsset("vector-red", "path-red", documentv2.ColorRGBA16{R: 65_535, A: 65_535}),
			vectorAsset("vector-blue", "path-blue", documentv2.ColorRGBA16{B: 65_535, A: 65_535}),
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "vector-track", Kind: documentv2.TrackKindVector,
			Clips: []documentv2.Clip{
				{ID: "outgoing", AssetID: "vector-red", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)},
				{ID: "incoming", AssetID: "vector-blue", TimelineRange: tr(2, 2), SourceRange: tr(0, 2)},
			},
		}}, Transitions: []documentv2.Transition{{
			ID: "vector-dissolve", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "outgoing", IncomingClipID: "incoming", PreDuration: rt(1, 1), PostDuration: rt(1, 1),
		}}}},
	}
}

func imageTransitionDocument() documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "image-transition-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "image-a", Kind: documentv2.AssetKindImage, URI: "a.png", ContentHash: shaA()},
			{ID: "image-b", Kind: documentv2.AssetKindImage, URI: "b.png", ContentHash: shaB()},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "image-track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{
				{ID: "outgoing", AssetID: "image-a", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)},
				{ID: "incoming", AssetID: "image-b", TimelineRange: tr(2, 2), SourceRange: tr(0, 2)},
			},
		}}, Transitions: []documentv2.Transition{{
			ID: "image-dissolve", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "outgoing", IncomingClipID: "incoming", PreDuration: rt(1, 1), PostDuration: rt(1, 1),
		}}}},
	}
}

func vectorAsset(id, pathID documentv2.ID, fill documentv2.ColorRGBA16) documentv2.Asset {
	return documentv2.Asset{ID: id, Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
		Profile:  documentv2.VectorProfilePathViewportPXV2,
		ViewBox:  documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(2, 1), Height: rat(2, 1)},
		Viewport: &documentv2.RasterViewport{WidthPixels: 2, HeightPixels: 2},
		Paths: []documentv2.VectorPath{{
			ID: pathID, FillRule: documentv2.VectorFillNonZero, Fill: &fill,
			Commands: []documentv2.VectorCommand{
				{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{point(0, 1, 0, 1)}},
				{Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(2, 1, 0, 1)}},
				{Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(2, 1, 2, 1)}},
				{Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(0, 1, 2, 1)}},
				{Op: documentv2.VectorClose},
			},
		}},
	}}
}

func testSurface(t *testing.T, width, height int, pixels []cpucompositor.Pixel) *cpucompositor.Surface {
	t.Helper()
	value, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertSurfaceHash(t *testing.T, surface *cpucompositor.Surface, want string) {
	t.Helper()
	got, err := surface.CanonicalHash()
	if err != nil || got != want {
		t.Fatalf("surface hash=%s err=%v want=%s", got, err, want)
	}
}

func zeroResult(value Result) bool {
	return value.SchemaVersion == 0 && value.Profile == "" && value.Surface == nil && value.Sources == nil && value.PixelHash == ""
}

func cloneResult(t *testing.T, value Result) Result {
	t.Helper()
	encoded, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := wirejson.Decode(encoded, &result); err != nil {
		t.Fatal(err)
	}
	result.Surface = value.Surface
	return result
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}
func tr(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}
func rat(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}
func point(xn, xd, yn, yd int64) documentv2.Point {
	return documentv2.Point{X: rat(xn, xd), Y: rat(yn, yd)}
}
func shaA() string { return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }
func shaB() string { return "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }
