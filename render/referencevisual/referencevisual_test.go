package referencevisual

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"math"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	mediacolor "github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/referenceimage"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

func TestImageOnlyArtifactParityWithReferenceImageV1(t *testing.T) {
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 40, G: 120, B: 200, A: 192}, 0)
	document := imageDocument(contentHash(encoded), true)
	config := DefaultConfig(2, 2)
	config.BuildFingerprint = "reference-visual-test"
	program, diagnostics, err := Compile(context.Background(), document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	legacyConfig := referenceimage.DefaultConfig(2, 2)
	legacyConfig.BuildFingerprint = config.BuildFingerprint
	legacy, legacyDiagnostics, err := referenceimage.Compile(context.Background(), document, []referenceimage.Binding{{AssetID: "image", EncodedPNG: encoded}}, legacyConfig)
	if err != nil || legacy == nil || documentv2.HasErrorDiagnostics(legacyDiagnostics) {
		t.Fatalf("legacy compile diagnostics=%+v err=%v", legacyDiagnostics, err)
	}
	at := rt(1, 2)
	got, err := program.RenderAt(context.Background(), "timeline", at)
	if err != nil {
		t.Fatal(err)
	}
	want, err := legacy.RenderAt(context.Background(), "timeline", at)
	if err != nil {
		t.Fatal(err)
	}
	if got.Visual.PixelHash != want.Raster.PixelHash || got.Output.PixelHash != want.Output.PixelHash ||
		got.PNG.ArtifactHash != want.PNG.ArtifactHash || got.PNG.ArtifactBytes != want.PNG.ArtifactBytes {
		t.Fatalf("parity got visual=%s output=%s png=%s; want raster=%s output=%s png=%s",
			got.Visual.PixelHash, got.Output.PixelHash, got.PNG.ArtifactHash,
			want.Raster.PixelHash, want.Output.PixelHash, want.PNG.ArtifactHash)
	}
	gotBytes, _ := got.PNG.Artifact.Bytes()
	wantBytes, _ := want.PNG.Artifact.Bytes()
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatal("image-only artifact bytes differ from referenceimage v1")
	}
	if err := program.ValidateResult(context.Background(), got); err != nil {
		t.Fatalf("strict validation: %v", err)
	}
	serialized := got
	serialized.Visual.Surface = nil
	serialized.Output.Frame = nil
	serialized.PNG.Artifact = nil
	if err := program.ValidateSerializedReceipt(context.Background(), serialized); err != nil {
		t.Fatalf("serialized validation: %v", err)
	}
	if err := program.ValidateResult(context.Background(), serialized); err == nil {
		t.Fatal("strict validator accepted missing runtime objects")
	}
	clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(2, 1)}
	frame, err := program.RenderFrame(context.Background(), "timeline", clock, 1)
	if err != nil || frame.Result.PNG.ArtifactHash != got.PNG.ArtifactHash || frame.Result.TimelineTime != at {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
}

func TestVectorOnlyMixedZOrderDissolveZeroWidthAndGap(t *testing.T) {
	vectorOnly := vectorDocument()
	program, diagnostics, err := Compile(context.Background(), vectorOnly, nil, DefaultConfig(2, 2))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("vector compile diagnostics=%+v err=%v", diagnostics, err)
	}
	vector, err := program.RenderAt(context.Background(), "timeline", rt(1, 2))
	if err != nil || len(vector.Visual.Sources) != 1 || vector.Visual.Sources[0].Kind != visualcompositor.SourceKindExpression ||
		vector.Visual.Sources[0].Expression.VisibleRequest == nil || vector.Visual.Sources[0].Expression.TransitionRequest != nil {
		t.Fatalf("vector=%+v err=%v", vector, err)
	}

	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 255, A: 255}, 0)
	mixed := mixedDocument(contentHash(encoded))
	mixedProgram, diagnostics, err := Compile(context.Background(), mixed, []Binding{{AssetID: "image", EncodedPNG: encoded}}, DefaultConfig(2, 2))
	if err != nil || mixedProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("mixed compile diagnostics=%+v err=%v", diagnostics, err)
	}
	mixedResult, err := mixedProgram.RenderAt(context.Background(), "timeline", rt(1, 2))
	if err != nil || len(mixedResult.Visual.Sources) != 2 || mixedResult.Visual.Sources[0].Kind != visualcompositor.SourceKindNative ||
		mixedResult.Visual.Sources[1].Kind != visualcompositor.SourceKindExpression ||
		mixedResult.Visual.Sources[0].Evaluation.Clip.TrackIndex != 0 || mixedResult.Visual.Sources[1].Evaluation.Clip.TrackIndex != 1 {
		t.Fatalf("mixed result=%+v err=%v", mixedResult, err)
	}

	transition := vectorTransitionDocument(1, 1)
	transitionProgram, diagnostics, err := Compile(context.Background(), transition, nil, DefaultConfig(2, 2))
	if err != nil || transitionProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("transition compile diagnostics=%+v err=%v", diagnostics, err)
	}
	dissolve, err := transitionProgram.RenderAt(context.Background(), "timeline", rt(3, 2))
	if err != nil || len(dissolve.Visual.Sources) != 2 {
		t.Fatalf("dissolve=%+v err=%v", dissolve, err)
	}
	outgoing, incoming := dissolve.Visual.Sources[0], dissolve.Visual.Sources[1]
	if outgoing.Expression.TransitionRequest == nil || incoming.Expression.TransitionRequest == nil ||
		outgoing.Expression.TransitionRequest.Role != expressionlayer.TransitionHandleOutgoing ||
		incoming.Expression.TransitionRequest.Role != expressionlayer.TransitionHandleIncoming ||
		outgoing.Expression.VisibleRequest != nil || incoming.Expression.VisibleRequest != nil ||
		outgoing.Evaluation.Transition.Clip.Visible != true || incoming.Evaluation.Transition.Clip.Visible != false {
		t.Fatalf("transition receipts outgoing=%+v incoming=%+v", outgoing, incoming)
	}

	hardCut := vectorTransitionDocument(0, 0)
	hardProgram, diagnostics, err := Compile(context.Background(), hardCut, nil, DefaultConfig(2, 2))
	if err != nil || hardProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("hard-cut compile diagnostics=%+v err=%v", diagnostics, err)
	}
	hard, err := hardProgram.RenderAt(context.Background(), "timeline", rt(2, 1))
	if err != nil || len(hard.Visual.Sources) != 1 || hard.Visual.Sources[0].Expression.VisibleRequest == nil ||
		hard.Visual.Sources[0].Expression.TransitionRequest != nil || hard.Visual.Sources[0].Evaluation.Transition != nil {
		t.Fatalf("zero-width hard cut=%+v err=%v", hard, err)
	}
	gap, err := program.RenderAt(context.Background(), "timeline", rt(10, 1))
	if err != nil || gap.Visual.Sources == nil || len(gap.Visual.Sources) != 0 {
		t.Fatalf("gap=%+v err=%v", gap, err)
	}
	transparent, _ := cpucompositor.NewSurface(2, 2)
	transparentHash, _ := transparent.CanonicalHash()
	transparentOutput, _ := mediacolor.ConvertSurfaceToRGBA8(context.Background(), transparent, mediacolor.DefaultOutputLimits())
	transparentPNG, _ := pngencode.Encode(context.Background(), transparentOutput, pngencode.DefaultLimits())
	if gap.Visual.PixelHash != transparentHash || gap.Output.PixelHash != transparentOutput.PixelHash || gap.PNG.ArtifactHash != transparentPNG.ArtifactHash {
		t.Fatalf("gap is not canonical transparent output: %+v", gap)
	}
}

func TestImmutableProgramDefensiveCopiesSeekConcurrencyCancelAndLimits(t *testing.T) {
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{G: 255, A: 255}, 0)
	document := mixedDocument(contentHash(encoded))
	bindings := []Binding{{AssetID: "image", EncodedPNG: encoded}}
	program, diagnostics, err := Compile(context.Background(), document, bindings, DefaultConfig(2, 2))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile diagnostics=%+v err=%v", diagnostics, err)
	}
	want, err := program.RenderAt(context.Background(), "timeline", rt(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	document.ID = "mutated"
	document.Assets[1].Vector.Paths[0].Fill.R = 1
	for index := range encoded {
		encoded[index] = 0
	}
	manifest := program.SourceManifest()
	manifest.NativePNG.Assets[0].PixelHash = "mutated"
	manifest.Expression.Assets[0].SurfaceIdentity.PixelHash = "mutated"
	againManifest := program.SourceManifest()
	if againManifest.NativePNG.Assets[0].PixelHash == "mutated" || againManifest.Expression.Assets[0].SurfaceIdentity.PixelHash == "mutated" {
		t.Fatal("manifest accessor aliases Program storage")
	}
	for _, at := range []documentv2.RationalTime{rt(10, 1), rt(0, 1), rt(1, 2), rt(-1, 1), rt(1, 2)} {
		got, renderErr := program.RenderAt(context.Background(), "timeline", at)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		if at == rt(1, 2) && got.PNG.ArtifactHash != want.PNG.ArtifactHash {
			t.Fatal("arbitrary seek changed deterministic output")
		}
	}
	const workers = 24
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			at := rt(int64(index%3), 2)
			got, renderErr := program.RenderAt(context.Background(), "timeline", at)
			if renderErr == nil && at == rt(1, 2) && got.PNG.ArtifactHash != want.PNG.ArtifactHash {
				renderErr = errors.New("concurrent output drift")
			}
			errs <- renderErr
		}(worker)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	artifact, _ := want.PNG.Artifact.Bytes()
	artifact[0] = 0
	artifactAgain, _ := want.PNG.Artifact.Bytes()
	if artifactAgain[0] == 0 {
		t.Fatal("artifact Bytes aliases immutable storage")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, renderErr := program.RenderAt(ctx, "timeline", rt(1, 2)); !reflect.DeepEqual(result, Result{}) || !errors.Is(renderErr, ErrCanceled) {
		t.Fatalf("canceled result=%+v err=%v", result, renderErr)
	}
	invalid := DefaultConfig(2, 2)
	invalid.NativeLimits.MaxBindings = nativepngsource.HardMaxBindings + 1
	if got, _, compileErr := Compile(context.Background(), mixedDocument(contentHash(strictPNG(t, 1, 1, stdcolor.NRGBA{A: 255}, 0))), nil, invalid); got != nil || !errors.Is(compileErr, ErrConfigInvalid) {
		t.Fatalf("invalid hard limit program=%v err=%v", got, compileErr)
	}
	limited := DefaultConfig(2, 2)
	limited.PipelineLimits.MaxRetainedSourceBytes = 8
	if got, _, compileErr := Compile(context.Background(), vectorDocument(), nil, limited); got != nil || !errors.Is(compileErr, ErrResourceLimit) {
		t.Fatalf("aggregate limit program=%v err=%v", got, compileErr)
	}
}

func TestHostileManifestAndResultTamperingFailsClosed(t *testing.T) {
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{B: 255, A: 255}, 0)
	document := mixedDocument(contentHash(encoded))
	program, diagnostics, err := Compile(context.Background(), document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, DefaultConfig(2, 2))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile diagnostics=%+v err=%v", diagnostics, err)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Result){
		func(value *Result) { value.SourceManifest.TotalRetainedSurfaceBytes++ },
		func(value *Result) { value.SourceManifest.NativePNG.Assets[0].DecodeProfile = "hostile" },
		func(value *Result) { value.SourceManifest.Expression.Assets[0].ConceptualEdgeCount++ },
		func(value *Result) { value.StageBinding.VisualPixelHash = shaA() },
		func(value *Result) { value.Visual.Sources[0].Native.PixelHash = shaA() },
		func(value *Result) {
			value.Visual.Sources[1].Expression.SurfaceIdentity.PixelHash = shaA()
			value.Visual.Sources[1].Expression.RasterReceipt.PixelHash = shaA()
		},
		func(value *Result) { value.Output.PixelHash = shaA() },
		func(value *Result) { value.PNG.ArtifactHash = shaA() },
	}
	for index, mutate := range mutations {
		copy := cloneResultValue(result)
		mutate(&copy)
		if err := program.ValidateResult(context.Background(), copy); err == nil {
			t.Fatalf("mutation %d accepted", index)
		}
		copy.Visual.Surface = nil
		copy.Output.Frame = nil
		copy.PNG.Artifact = nil
		if err := program.ValidateSerializedReceipt(context.Background(), copy); err == nil {
			t.Fatalf("serialized mutation %d accepted", index)
		}
	}
	manifest := cloneSourceManifest(program.manifest)
	manifest.Expression.Assets[0].ConceptualEdgeCount++
	manifest.Expression.Assets[0].RasterReceipt.ConceptualEdgeCount++
	if validExpressionManifest(context.Background(), manifest.Expression, document, program.config.ExpressionLimits) {
		t.Fatal("synchronized conceptual-edge tamper accepted")
	}
}

func TestAdmissionAndConfigClassification(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(value *Config) { value.CompositorLimits.MaxTargetWorkingBytes = 47 },
		func(value *Config) { value.CompositorLimits.MaxDocumentNodes = -1 },
	} {
		config := DefaultConfig(2, 2)
		mutate(&config)
		program, _, err := Compile(context.Background(), vectorDocument(), nil, config)
		if program != nil || !errors.Is(err, ErrConfigInvalid) {
			t.Fatalf("invalid compositor config program=%v err=%v", program, err)
		}
	}
	countConfig := DefaultConfig(2, 2)
	countConfig.NativeLimits.MaxBindings = 1
	if program, _, err := Compile(context.Background(), documentv2.Document{}, []Binding{{}, {}}, countConfig); program != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("binding count program=%v err=%v", program, err)
	}
	encodedConfig := DefaultConfig(2, 2)
	encodedConfig.NativeLimits.MaxTotalEncodedBytes = 8
	if program, _, err := Compile(context.Background(), documentv2.Document{}, []Binding{{EncodedPNG: make([]byte, 9)}}, encodedConfig); program != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("encoded limit program=%v err=%v", program, err)
	}
	overflow := DefaultConfig(2, 2)
	overflow.PipelineLimits.MaxCompileWorkingBytes = ^uint64(0)
	if program, _, err := Compile(context.Background(), vectorDocument(), nil, overflow); program != nil || !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("pipeline hard cap program=%v err=%v", program, err)
	}
	hard := DefaultConfig(2, 2)
	hard.NativeLimits.MaxBindings = nativepngsource.HardMaxBindings + 1
	if program, _, err := Compile(context.Background(), documentv2.Document{}, make([]Binding, nativepngsource.HardMaxBindings+1), hard); program != nil || !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("invalid hard binding config program=%v err=%v", program, err)
	}
}

func TestUnsupportedSourcesAndAtomicFailures(t *testing.T) {
	video := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "video", Revision: 1,
		Assets: []documentv2.Asset{{ID: "video", Kind: documentv2.AssetKindVideo, URI: "video.mp4", ContentHash: shaA()}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip", AssetID: "video", TimelineRange: tr(0, 1), SourceRange: tr(0, 1)}}}}}},
	}
	textLike := vectorDocument()
	textLike.ID = "text-like"
	textLike.Timelines[0].Tracks[0].Kind = documentv2.TrackKindText
	audioLike := vectorDocument()
	audioLike.ID = "audio-like"
	audioLike.Timelines[0].Tracks[0].Kind = documentv2.TrackKindAudio
	for _, document := range []documentv2.Document{video, textLike, audioLike} {
		program, _, err := Compile(context.Background(), document, nil, DefaultConfig(2, 2))
		if program != nil || err == nil {
			t.Fatalf("unsupported document published program=%v err=%v", program, err)
		}
	}
	encoded := strictPNG(t, 1, 1, stdcolor.NRGBA{A: 255}, 0)
	document := imageDocument(contentHash(encoded), false)
	if program, _, err := Compile(context.Background(), document, nil, DefaultConfig(1, 1)); program != nil || err == nil {
		t.Fatalf("missing binding program=%v err=%v", program, err)
	}
	if program, _, err := Compile(context.Background(), document, []Binding{{AssetID: "image", EncodedPNG: encoded}, {AssetID: "extra", EncodedPNG: encoded}}, DefaultConfig(1, 1)); program != nil || err == nil {
		t.Fatalf("extra binding program=%v err=%v", program, err)
	}
}

func TestAggregateBoundariesAndManifestHostility(t *testing.T) {
	base, _, err := normalizeConfig(DefaultConfig(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := checkedAdd(base.NativeLimits.MaxRetainedBytes, base.ExpressionLimits.MaxRetainedBytes)
	canonicalWorking, _ := checkedMul(minimumCanonicalLimit(base), canonicalCompileMultiplier)
	metadata, _ := checkedMul(uint64(base.CompositorLimits.MaxDocumentNodes), metadataBytesPerDocumentNode)
	transient := base.NativeLimits.PNG.MaxWorkingBytes
	if base.ExpressionLimits.Vector.MaxWorkingBytes > transient {
		transient = base.ExpressionLimits.Vector.MaxWorkingBytes
	}
	compilePeak, _ := checkedSum(retained, transient, canonicalWorking, metadata)
	frame, output, artifact, row, ok := configuredRuntimeSizes(base)
	if !ok {
		t.Fatal("runtime shape overflow")
	}
	visual, _ := checkedMul(frame, 6)
	colorPeak, _ := checkedAdd(frame, output)
	pngPeak, _ := checkedSum(frame, output, artifact, row)
	stagePeak := visual
	if colorPeak > stagePeak {
		stagePeak = colorPeak
	}
	if pngPeak > stagePeak {
		stagePeak = pngPeak
	}
	runtimePeak, _ := checkedSum(retained, metadata, stagePeak)
	exact := DefaultConfig(2, 2)
	exact.PipelineLimits = PipelineLimits{MaxRetainedSourceBytes: retained, MaxCompileWorkingBytes: compilePeak, MaxRuntimeWorkingBytes: runtimePeak}
	if program, diagnostics, compileErr := Compile(context.Background(), vectorDocument(), nil, exact); compileErr != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("exact aggregate boundary program=%v diagnostics=%+v err=%v", program, diagnostics, compileErr)
	}
	for _, mutate := range []func(*PipelineLimits){
		func(value *PipelineLimits) { value.MaxRetainedSourceBytes-- },
		func(value *PipelineLimits) { value.MaxCompileWorkingBytes-- },
		func(value *PipelineLimits) { value.MaxRuntimeWorkingBytes-- },
	} {
		config := exact
		mutate(&config.PipelineLimits)
		if program, _, compileErr := Compile(context.Background(), vectorDocument(), nil, config); program != nil || !errors.Is(compileErr, ErrResourceLimit) {
			t.Fatalf("one-less aggregate program=%v err=%v limits=%+v", program, compileErr, config.PipelineLimits)
		}
	}

	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 255, A: 255}, 0)
	document := mixedDocument(contentHash(encoded))
	program, diagnostics, err := Compile(context.Background(), document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, DefaultConfig(2, 2))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("mixed compile diagnostics=%+v err=%v", diagnostics, err)
	}
	manifest := cloneSourceManifest(program.manifest)
	tooSmall := program.config
	tooSmall.PipelineLimits.MaxRetainedSourceBytes = manifest.NativeRetainedSurfaceBytes
	if validCombinedManifest(manifest, tooSmall) {
		t.Fatal("combined manifest accepted individually-in-range but aggregate-over-limit surfaces")
	}
	duplicate := cloneSourceManifest(program.manifest)
	duplicate.Expression.Assets[0].AssetID = duplicate.NativePNG.Assets[0].AssetID
	duplicate.Expression.Assets[0].AssetIndex = duplicate.NativePNG.Assets[0].AssetIndex
	if validCombinedManifest(duplicate, program.config) {
		t.Fatal("combined manifest accepted cross-provider duplicate asset identity")
	}
	for _, mutate := range []func(*SourceManifest){
		func(value *SourceManifest) { value.NativeRetainedSurfaceBytes++ },
		func(value *SourceManifest) { value.ExpressionRetainedSurfaceBytes++ },
		func(value *SourceManifest) { value.TotalRetainedSurfaceBytes++ },
	} {
		tampered := cloneSourceManifest(program.manifest)
		mutate(&tampered)
		if validCombinedManifest(tampered, program.config) {
			t.Fatal("combined manifest accepted tampered retained-surface receipt")
		}
	}
	overflowManifest := cloneSourceManifest(program.manifest)
	overflowManifest.Expression.Assets[0].SurfaceBytes = math.MaxUint64
	if validCombinedManifest(overflowManifest, program.config) {
		t.Fatal("combined manifest accepted retained-surface overflow")
	}
	nilViewport := mixedDocument(contentHash(encoded))
	nilViewport.Assets[1].Vector.Viewport = nil
	if validExpressionManifest(context.Background(), program.manifest.Expression, nilViewport, program.config.ExpressionLimits) {
		t.Fatal("expression manifest accepted nil frozen vector viewport")
	}
	badNative := cloneNativeManifest(program.manifest.NativePNG)
	badNative.Assets[0].Width = -1
	if validNativeManifest(context.Background(), badNative, document, program.config.NativeLimits) {
		t.Fatal("native manifest accepted negative geometry")
	}
}

func TestCanonicalProfileManifestAndResultFixtures(t *testing.T) {
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 32, G: 96, B: 224, A: 192}, 0)
	document := mixedDocument(contentHash(encoded))
	config := DefaultConfig(2, 2)
	config.BuildFingerprint = "reference-visual-fixture"
	program, diagnostics, err := Compile(context.Background(), document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile diagnostics=%+v err=%v", diagnostics, err)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	empty := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "empty-fixture-document", Revision: 1,
		Timelines: []documentv2.Timeline{{ID: "timeline"}}}
	emptyProgram, emptyDiagnostics, err := Compile(context.Background(), empty, nil, config)
	if err != nil || emptyProgram == nil || documentv2.HasErrorDiagnostics(emptyDiagnostics) {
		t.Fatalf("empty compile diagnostics=%+v err=%v", emptyDiagnostics, err)
	}
	emptyResult, err := emptyProgram.RenderAt(context.Background(), "timeline", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		path  string
		value any
	}{
		{"testdata/profile-v1.canonical.json", Descriptor()},
		{"testdata/source-manifest-v1.canonical.json", result.SourceManifest},
		{"testdata/result-v1.canonical.json", emptyResult},
		{"testdata/golden-v1.canonical.json", resultGoldenFrom(t, result)},
	}
	for _, fixture := range fixtures {
		canonical, canonicalErr := videoedit.CanonicalJSON(fixture.value)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		onDisk, readErr := os.ReadFile(fixture.path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(bytes.TrimSpace(onDisk), canonical) {
			t.Errorf("fixture %s mismatch\nGOT_CANONICAL=%s", fixture.path, canonical)
		}
	}
	profileBytes, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var decodedProfile ProfileDescriptor
	if err := wirejson.Decode(bytes.TrimSpace(profileBytes), &decodedProfile); err != nil {
		t.Fatalf("strict profile fixture decode: %v", err)
	}
	profileCanonical, err := videoedit.CanonicalJSON(decodedProfile)
	if err != nil || !bytes.Equal(profileCanonical, bytes.TrimSpace(profileBytes)) {
		t.Fatalf("profile fixture roundtrip err=%v", err)
	}
	manifestBytes, err := os.ReadFile("testdata/source-manifest-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var decodedManifest SourceManifest
	if err := wirejson.Decode(bytes.TrimSpace(manifestBytes), &decodedManifest); err != nil {
		t.Fatalf("strict manifest fixture decode: %v", err)
	}
	manifestCanonical, err := videoedit.CanonicalJSON(decodedManifest)
	if err != nil || !bytes.Equal(manifestCanonical, bytes.TrimSpace(manifestBytes)) || !equalSourceManifest(decodedManifest, result.SourceManifest) {
		t.Fatalf("manifest fixture roundtrip err=%v", err)
	}
	resultBytes, err := os.ReadFile("testdata/result-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := wirejson.Decode(bytes.TrimSpace(resultBytes), &decoded); err != nil {
		t.Fatalf("strict result fixture decode: %v", err)
	}
	recanonical, err := videoedit.CanonicalJSON(decoded)
	if err != nil || !bytes.Equal(recanonical, bytes.TrimSpace(resultBytes)) {
		t.Fatalf("result fixture roundtrip err=%v", err)
	}
	if err := emptyProgram.ValidateSerializedReceipt(context.Background(), decoded); err != nil {
		t.Fatalf("serialized fixture validation: %v", err)
	}
	if err := emptyProgram.ValidateResult(context.Background(), decoded); err == nil {
		t.Fatal("strict live validator accepted serialized fixture without runtime objects")
	}
}

type resultGolden struct {
	SchemaVersion        int                     `json:"schema_version"`
	Profile              string                  `json:"profile"`
	Scenario             string                  `json:"scenario"`
	DocumentHash         string                  `json:"document_hash"`
	TimelineID           documentv2.ID           `json:"timeline_id"`
	TimelineTime         documentv2.RationalTime `json:"timeline_time"`
	NativeAssetCount     int                     `json:"native_asset_count"`
	ExpressionAssetCount int                     `json:"expression_asset_count"`
	OrderedSourceCount   int                     `json:"ordered_source_count"`
	VisualPixelHash      string                  `json:"visual_pixel_hash"`
	OutputPixelHash      string                  `json:"output_pixel_hash"`
	PNGArtifactHash      string                  `json:"png_artifact_hash"`
	SerializedResultHash string                  `json:"serialized_result_hash"`
}

func resultGoldenFrom(t testing.TB, value Result) resultGolden {
	t.Helper()
	canonical, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return resultGolden{
		SchemaVersion: ResultSchemaVersion, Profile: ProfileV1, Scenario: "mixed-native-png-vector-visible-at-half-second",
		DocumentHash: value.DocumentHash, TimelineID: value.TimelineID, TimelineTime: value.TimelineTime,
		NativeAssetCount: len(value.SourceManifest.NativePNG.Assets), ExpressionAssetCount: len(value.SourceManifest.Expression.Assets),
		OrderedSourceCount: len(value.Visual.Sources), VisualPixelHash: value.Visual.PixelHash, OutputPixelHash: value.Output.PixelHash,
		PNGArtifactHash: value.PNG.ArtifactHash, SerializedResultHash: contentHash(canonical),
	}
}

func FuzzCompileAndRenderFailClosed(f *testing.F) {
	f.Add(int64(0), int64(1), uint64(DefaultMaxRetainedSourceBytes))
	f.Add(int64(1), int64(2), uint64(8))
	f.Add(int64(-1), int64(1), uint64(1<<20))
	f.Fuzz(func(t *testing.T, value, rate int64, retained uint64) {
		config := DefaultConfig(2, 2)
		config.PipelineLimits.MaxRetainedSourceBytes = retained
		program, _, err := Compile(context.Background(), vectorDocument(), nil, config)
		if err != nil {
			if program != nil {
				t.Fatal("failed Compile returned a Program")
			}
			return
		}
		result, renderErr := program.RenderAt(context.Background(), "timeline", rt(value, rate))
		if renderErr != nil && !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("failed RenderAt returned a partial Result: %+v", result)
		}
	})
}

func BenchmarkRenderAtVector(b *testing.B) {
	program, diagnostics, err := Compile(context.Background(), vectorDocument(), nil, DefaultConfig(2, 2))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		b.Fatalf("compile diagnostics=%+v err=%v", diagnostics, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, renderErr := program.RenderAt(context.Background(), "timeline", rt(1, 2))
		if renderErr != nil || result.PNG.ArtifactHash == "" {
			b.Fatalf("result=%+v err=%v", result, renderErr)
		}
	}
}

func BenchmarkCompileMixed(b *testing.B) {
	encoded := strictPNG(b, 2, 2, stdcolor.NRGBA{R: 64, G: 128, B: 192, A: 255}, 0)
	document := mixedDocument(contentHash(encoded))
	bindings := []Binding{{AssetID: "image", EncodedPNG: encoded}}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		program, diagnostics, err := Compile(context.Background(), document, bindings, DefaultConfig(2, 2))
		if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
			b.Fatalf("program=%v diagnostics=%+v err=%v", program, diagnostics, err)
		}
	}
}

func BenchmarkValidateResultMixed(b *testing.B) {
	encoded := strictPNG(b, 2, 2, stdcolor.NRGBA{R: 64, G: 128, B: 192, A: 255}, 0)
	program, diagnostics, err := Compile(context.Background(), mixedDocument(contentHash(encoded)), []Binding{{AssetID: "image", EncodedPNG: encoded}}, DefaultConfig(2, 2))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		b.Fatalf("program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if err := program.ValidateResult(context.Background(), result); err != nil {
			b.Fatal(err)
		}
	}
}

func cloneResultValue(value Result) Result {
	result := value
	result.SourceManifest = cloneSourceManifest(value.SourceManifest)
	result.Visual.Sources = append([]visualcompositor.SourceReceipt(nil), value.Visual.Sources...)
	for index := range result.Visual.Sources {
		if native := result.Visual.Sources[index].Native; native != nil {
			copy := *native
			result.Visual.Sources[index].Native = &copy
		}
		if expression := result.Visual.Sources[index].Expression; expression != nil {
			copy := *expression
			if expression.VisibleRequest != nil {
				request := *expression.VisibleRequest
				copy.VisibleRequest = &request
			}
			if expression.TransitionRequest != nil {
				request := *expression.TransitionRequest
				copy.TransitionRequest = &request
			}
			result.Visual.Sources[index].Expression = &copy
		}
	}
	return result
}

func imageDocument(hash string, properties bool) documentv2.Document {
	clip := documentv2.Clip{ID: "image-clip", AssetID: "image", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}
	if properties {
		start, end := point(0, 1, 0, 1), point(1, 1, 0, 1)
		one, half := rat(1, 1), rat(1, 2)
		clip.PropertySet = &documentv2.PropertySet{
			Transform: &documentv2.TransformProperty{Position: start, Scale: point(1, 1, 1, 1), RotationDegrees: rat(0, 1), Anchor: point(1, 2, 1, 2)},
			Crop:      &documentv2.CropProperty{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: half, Height: one}},
			Opacity:   &documentv2.OpacityProperty{Value: one},
		}
		clip.KeyframeLanes = []documentv2.KeyframeLane{{
			ID: "position", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint,
			Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &start}, Interpolation: documentv2.InterpolationLinear}, {Offset: rt(2, 1), Value: documentv2.KeyframeValue{Point: &end}}},
		}}
	}
	return documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "image-document", Revision: 1,
		Assets:    []documentv2.Asset{{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png", ContentHash: hash}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "image-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{clip}}}}},
	}
}

func vectorDocument() documentv2.Document {
	return documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "vector-document", Revision: 1,
		Assets: []documentv2.Asset{vectorAsset("vector", "path", documentv2.ColorRGBA16{B: 65_535, A: 65_535})},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "vector-track", Kind: documentv2.TrackKindVector,
			Clips: []documentv2.Clip{{ID: "vector-clip", AssetID: "vector", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}}}}},
	}
}

func mixedDocument(hash string) documentv2.Document {
	return documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "mixed-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png", ContentHash: hash}, vectorAsset("vector", "path", documentv2.ColorRGBA16{B: 65_535, A: 32_768})},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "image-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "image-clip", AssetID: "image", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}},
			{ID: "vector-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{ID: "vector-clip", AssetID: "vector", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}},
		}}},
	}
}

func vectorTransitionDocument(pre, post int64) documentv2.Document {
	return documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "vector-transition-document", Revision: 1,
		Assets: []documentv2.Asset{vectorAsset("red", "red-path", documentv2.ColorRGBA16{R: 65_535, A: 65_535}), vectorAsset("blue", "blue-path", documentv2.ColorRGBA16{B: 65_535, A: 65_535})},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "vector-track", Kind: documentv2.TrackKindVector,
			Clips: []documentv2.Clip{{ID: "outgoing", AssetID: "red", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}, {ID: "incoming", AssetID: "blue", TimelineRange: tr(2, 2), SourceRange: tr(0, 2)}}}},
			Transitions: []documentv2.Transition{{ID: "dissolve", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "outgoing", IncomingClipID: "incoming", PreDuration: rt(pre, 1), PostDuration: rt(post, 1)}}}},
	}
}

func vectorAsset(id, pathID documentv2.ID, fill documentv2.ColorRGBA16) documentv2.Asset {
	return documentv2.Asset{ID: id, Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
		Profile: documentv2.VectorProfilePathViewportPXV2, ViewBox: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(2, 1), Height: rat(2, 1)},
		Viewport: &documentv2.RasterViewport{WidthPixels: 2, HeightPixels: 2}, Paths: []documentv2.VectorPath{{ID: pathID, FillRule: documentv2.VectorFillNonZero, Fill: &fill,
			Commands: []documentv2.VectorCommand{{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{point(0, 1, 0, 1)}}, {Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(2, 1, 0, 1)}}, {Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(2, 1, 2, 1)}}, {Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(0, 1, 2, 1)}}, {Op: documentv2.VectorClose}}}},
	}}
}

func strictPNG(t testing.TB, width, height int, pixel stdcolor.NRGBA, intent byte) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, forceAlphaImage{NRGBA: value}); err != nil {
		t.Fatal(err)
	}
	bytes := encoded.Bytes()
	length := int(binary.BigEndian.Uint32(bytes[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = intent
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(bytes)+len(chunk))
	result = append(result, bytes[:end]...)
	result = append(result, chunk...)
	return append(result, bytes[end:]...)
}

type forceAlphaImage struct{ *image.NRGBA }

func (forceAlphaImage) Opaque() bool { return false }

func contentHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
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
