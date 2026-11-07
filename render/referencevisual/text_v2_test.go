package referencevisual

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	stdcolor "image/color"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

func TestTextV2DeterministicPNGArtifactClosure(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildReferenceVisualTextProvider(t, ctx)
	document := referenceVisualTextDocument(fontHash, true)
	hash := referenceVisualDocumentHash(t, document)
	config := DefaultConfigV2(16, 16)
	config.BuildFingerprint = "reference-visual-text-v2"
	program, diagnostics, err := CompileV2(ctx, document, nil, []textlayer.FontBinding{{
		DocumentHash: hash, FontAssetID: "font", Provider: font,
	}}, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("CompileV2 program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	manifest := program.SourceManifest()
	if program.DocumentHash() != hash || program.profile() != ProfileV2 || manifest.Profile != ManifestProfileV2 ||
		manifest.Text == nil || manifest.TextProviderProfile != textlayer.ProfileV1 ||
		manifest.TextContractVersion != textlayer.ContractVersion || len(manifest.Text.Assets) != 1 ||
		manifest.TextRetainedSurfaceBytes != 16*16*8 || manifest.TotalRetainedSurfaceBytes != manifest.TextRetainedSurfaceBytes {
		t.Fatalf("manifest=%+v", manifest)
	}
	result, err := program.RenderAt(ctx, "timeline", rt(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != ProfileV2 || result.Visual.Profile != visualcompositor.ProfileV2 ||
		result.Visual.TextContractVersion != textlayer.ContractVersion || len(result.Visual.Sources) != 1 ||
		result.Visual.Sources[0].Kind != visualcompositor.SourceKindText || result.Visual.Sources[0].Text == nil ||
		result.PNG.Artifact == nil || result.PNG.ArtifactHash == "" {
		t.Fatalf("result=%+v", result)
	}
	if result.PNG.ArtifactHash != "sha256:83fa1543ebd8e05078bc1c4bc493738501ea5c5b2bccc28324df27e9e472f916" {
		t.Fatalf("Text v2 artifact hash=%s", result.PNG.ArtifactHash)
	}
	profileCanonical, _ := videoedit.CanonicalJSON(DescriptorV2())
	manifestCanonical, _ := videoedit.CanonicalJSON(result.SourceManifest)
	resultCanonical, _ := videoedit.CanonicalJSON(result)
	if contentHash(profileCanonical) != "sha256:d1e7a4fbe3a29fa3b32d0d9c8ec89f6ef0363996eafa780e5ad81306884b0dd2" ||
		contentHash(manifestCanonical) != "sha256:1084a798926787d83e40d1ae6df8fd00437aad7e4501c88d083426180c33eea7" ||
		contentHash(resultCanonical) != "sha256:24c514a0ba8bf100ed001ab31f5130261b37cec7bbbc62ecfd4aed1c2497f7e4" {
		t.Fatalf("canonical hashes profile=%s manifest=%s result=%s", contentHash(profileCanonical), contentHash(manifestCanonical), contentHash(resultCanonical))
	}
	if err := program.ValidateResult(ctx, result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	serialized := cloneResultValue(result)
	serialized.Visual.Surface = nil
	serialized.Output.Frame = nil
	serialized.PNG.Artifact = nil
	if err := program.ValidateSerializedReceipt(ctx, serialized); err != nil {
		t.Fatalf("ValidateSerializedReceipt: %v", err)
	}

	second, err := program.RenderAt(ctx, "timeline", rt(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := result.PNG.Artifact.Bytes()
	secondBytes, _ := second.PNG.Artifact.Bytes()
	if result.PNG.ArtifactHash != second.PNG.ArtifactHash || !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("Text v2 PNG artifact drifted")
	}

	const workers = 16
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, renderErr := program.RenderAt(ctx, "timeline", rt(1, 2))
			if renderErr == nil && value.PNG.ArtifactHash != result.PNG.ArtifactHash {
				renderErr = errors.New("concurrent Text artifact drift")
			}
			errs <- renderErr
		}()
	}
	wait.Wait()
	close(errs)
	for renderErr := range errs {
		if renderErr != nil {
			t.Fatal(renderErr)
		}
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if value, renderErr := program.RenderAt(canceled, "timeline", rt(1, 2)); !reflect.DeepEqual(value, Result{}) || !errors.Is(renderErr, ErrCanceled) {
		t.Fatalf("canceled value=%+v err=%v", value, renderErr)
	}
	if legacy, _, legacyErr := Compile(ctx, document, nil, DefaultConfig(16, 16)); legacy != nil || legacyErr == nil {
		t.Fatalf("v1 accepted Text document: program=%v err=%v", legacy, legacyErr)
	}
}

func TestCanonicalV2ProfileManifestResultAndGoldenFixtures(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfigV2(2, 2)
	config.BuildFingerprint = "reference-visual-v2-fixture"
	document := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "empty-v2-fixture-document", Revision: 1,
		Timelines: []documentv2.Timeline{{ID: "timeline"}}}
	program, diagnostics, err := CompileV2(ctx, document, nil, nil, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	result, err := program.RenderAt(ctx, "timeline", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	profileCanonical, _ := videoedit.CanonicalJSON(DescriptorV2())
	manifestCanonical, _ := videoedit.CanonicalJSON(result.SourceManifest)
	resultCanonical, _ := videoedit.CanonicalJSON(result)
	golden := textV2CanonicalGolden{
		SchemaVersion: 1, Profile: ProfileV2, Scenario: "empty-v2-gap",
		ProfileCanonicalHash: contentHash(profileCanonical), ManifestCanonicalHash: contentHash(manifestCanonical),
		ResultCanonicalHash: contentHash(resultCanonical), PNGArtifactHash: result.PNG.ArtifactHash,
	}
	fixtures := []struct {
		path  string
		want  string
		value any
	}{
		{"testdata/profile-v2.canonical.json", "sha256:d1e7a4fbe3a29fa3b32d0d9c8ec89f6ef0363996eafa780e5ad81306884b0dd2", DescriptorV2()},
		{"testdata/source-manifest-v2.canonical.json", "sha256:ae45b50b3264f21dce6f16136c9a735181b8b70ed13a9057741b830fdb44bc92", result.SourceManifest},
		{"testdata/result-v2.canonical.json", "sha256:e66e27e21a945aa49be06df7c6bcb1d23a2fc5efbd81c38e407b3bf39f0e7e41", result},
		{"testdata/golden-v2.canonical.json", "sha256:0a6d7390c06a93b8cd4949c90436a23553382db096a6f57ce1464a3f1ad4a406", golden},
	}
	for _, fixture := range fixtures {
		canonical, canonicalErr := videoedit.CanonicalJSON(fixture.value)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		onDisk, readErr := os.ReadFile(fixture.path)
		if readErr != nil || !bytes.Equal(bytes.TrimSpace(onDisk), canonical) || contentHash(canonical) != fixture.want {
			t.Fatalf("fixture=%s err=%v hash=%s canonical=%s", fixture.path, readErr, contentHash(canonical), canonical)
		}
		unknown := append([]byte(`{"unknown_v2_field":true,`), canonical[1:]...)
		switch fixture.path {
		case "testdata/profile-v2.canonical.json":
			var decoded ProfileDescriptor
			if err := wirejson.Decode(canonical, &decoded); err != nil {
				t.Fatalf("profile strict decode: %v", err)
			}
			roundtrip, _ := videoedit.CanonicalJSON(decoded)
			if !bytes.Equal(roundtrip, canonical) || wirejson.Decode(unknown, &decoded) == nil {
				t.Fatal("profile strict roundtrip/unknown-field failure")
			}
		case "testdata/source-manifest-v2.canonical.json":
			var decoded SourceManifest
			if err := wirejson.Decode(canonical, &decoded); err != nil {
				t.Fatalf("manifest strict decode: %v", err)
			}
			roundtrip, _ := videoedit.CanonicalJSON(decoded)
			if !bytes.Equal(roundtrip, canonical) || !equalSourceManifest(decoded, result.SourceManifest) || wirejson.Decode(unknown, &decoded) == nil {
				t.Fatal("manifest strict roundtrip/unknown-field failure")
			}
		case "testdata/result-v2.canonical.json":
			var decoded Result
			if err := wirejson.Decode(canonical, &decoded); err != nil {
				t.Fatalf("result strict decode: %v", err)
			}
			roundtrip, _ := videoedit.CanonicalJSON(decoded)
			if !bytes.Equal(roundtrip, canonical) || wirejson.Decode(unknown, &decoded) == nil {
				t.Fatal("result strict roundtrip/unknown-field failure")
			}
			if err := program.ValidateSerializedReceipt(ctx, decoded); err != nil {
				t.Fatalf("serialized V2 fixture: %v", err)
			}
		case "testdata/golden-v2.canonical.json":
			var decoded textV2CanonicalGolden
			if err := wirejson.Decode(canonical, &decoded); err != nil {
				t.Fatalf("golden strict decode: %v", err)
			}
			roundtrip, _ := videoedit.CanonicalJSON(decoded)
			if !bytes.Equal(roundtrip, canonical) || wirejson.Decode(unknown, &decoded) == nil {
				t.Fatal("golden strict roundtrip/unknown-field failure")
			}
		}
	}
	if result.Profile != ProfileV2 || result.Visual.Profile != visualcompositor.ProfileV2 || result.SourceManifest.Text == nil ||
		len(result.SourceManifest.Text.Assets) != 0 || result.Visual.Sources == nil || len(result.Visual.Sources) != 0 {
		t.Fatalf("V2 gap drifted: %+v", result)
	}
}

type textV2CanonicalGolden struct {
	SchemaVersion         int    `json:"schema_version"`
	Profile               string `json:"profile"`
	Scenario              string `json:"scenario"`
	ProfileCanonicalHash  string `json:"profile_canonical_hash"`
	ManifestCanonicalHash string `json:"manifest_canonical_hash"`
	ResultCanonicalHash   string `json:"result_canonical_hash"`
	PNGArtifactHash       string `json:"png_artifact_hash"`
}

func TestTextV2MixedOrderTransformCropOpacityAndImageParity(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildReferenceVisualTextProvider(t, ctx)
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 255, A: 255}, 0)
	document := mixedDocument(contentHash(encoded))
	textDocument := referenceVisualTextDocument(fontHash, true)
	document.Assets = append(document.Assets, textDocument.Assets...)
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, textDocument.Timelines[0].Tracks[0])
	hash := referenceVisualDocumentHash(t, document)
	program, diagnostics, err := CompileV2(ctx, document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, []textlayer.FontBinding{{
		DocumentHash: hash, FontAssetID: "font", Provider: font,
	}}, DefaultConfigV2(16, 16))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("CompileV2 diagnostics=%+v err=%v", diagnostics, err)
	}
	first, err := program.RenderAt(ctx, "timeline", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	middle, err := program.RenderAt(ctx, "timeline", rt(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Visual.Sources) != 3 || first.Visual.Sources[0].Kind != visualcompositor.SourceKindNative ||
		first.Visual.Sources[1].Kind != visualcompositor.SourceKindExpression || first.Visual.Sources[2].Kind != visualcompositor.SourceKindText ||
		first.Visual.Sources[2].Text.Request.TimelineTime != rt(0, 1) || middle.Visual.Sources[2].Text.Request.TimelineTime != rt(1, 1) {
		t.Fatalf("first=%+v middle=%+v", first.Visual.Sources, middle.Visual.Sources)
	}

	image := imageDocument(contentHash(encoded), true)
	v1, _, err := Compile(ctx, image, []Binding{{AssetID: "image", EncodedPNG: encoded}}, DefaultConfig(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	v2, _, err := CompileV2(ctx, image, []Binding{{AssetID: "image", EncodedPNG: encoded}}, nil, DefaultConfigV2(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	v1Result, _ := v1.RenderAt(ctx, "timeline", rt(1, 2))
	v2Result, _ := v2.RenderAt(ctx, "timeline", rt(1, 2))
	v1Bytes, _ := v1Result.PNG.Artifact.Bytes()
	v2Bytes, _ := v2Result.PNG.Artifact.Bytes()
	if v2Result.Profile != ProfileV2 || v2Result.SourceManifest.Text == nil || len(v2Result.SourceManifest.Text.Assets) != 0 ||
		v1Result.PNG.ArtifactHash != v2Result.PNG.ArtifactHash || !bytes.Equal(v1Bytes, v2Bytes) {
		t.Fatalf("image parity v1=%s v2=%s", v1Result.PNG.ArtifactHash, v2Result.PNG.ArtifactHash)
	}
}

func TestTextV2BindingSubsetTamperAndAggregateBoundaries(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildReferenceVisualTextProvider(t, ctx)
	document := referenceVisualTextDocument(fontHash, false)
	hash := referenceVisualDocumentHash(t, document)
	valid := textlayer.FontBinding{DocumentHash: hash, FontAssetID: "font", Provider: font}
	for _, test := range []struct {
		name     string
		bindings []textlayer.FontBinding
	}{
		{name: "missing"},
		{name: "duplicate", bindings: []textlayer.FontBinding{valid, valid}},
		{name: "cross-document", bindings: []textlayer.FontBinding{{DocumentHash: shaA(), FontAssetID: "font", Provider: font}}},
		{name: "extra", bindings: []textlayer.FontBinding{valid, {DocumentHash: hash, FontAssetID: "extra", Provider: font}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if program, _, err := CompileV2(ctx, document, nil, test.bindings, DefaultConfigV2(16, 16)); program != nil || err == nil {
				t.Fatalf("program=%v err=%v", program, err)
			}
		})
	}

	fillLane := referenceVisualTextDocument(fontHash, false)
	fill := documentv2.ColorRGBA16{B: 1, A: 65_535}
	fillLane.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{{
		ID: "fill", Target: documentv2.KeyframeTargetTextFill, ValueType: documentv2.KeyframeValueColor,
		Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Color: &fill}}},
	}}
	if program, _, err := CompileV2(ctx, fillLane, nil, nil, DefaultConfigV2(16, 16)); program != nil || !errors.Is(err, ErrDocumentInvalid) {
		t.Fatalf("dynamic fill program=%v err=%v", program, err)
	}
	transition := referenceVisualTextDocument(fontHash, false)
	incoming := transition.Timelines[0].Tracks[0].Clips[0]
	incoming.ID = "text-incoming"
	incoming.TimelineRange = tr(2, 2)
	transition.Timelines[0].Tracks[0].Clips = append(transition.Timelines[0].Tracks[0].Clips, incoming)
	transition.Timelines[0].Transitions = []documentv2.Transition{{
		ID: "text-dissolve", Profile: documentv2.TransitionDissolveV1,
		OutgoingClipID: "text-clip", IncomingClipID: "text-incoming", PreDuration: rt(1, 2), PostDuration: rt(1, 2),
	}}
	if program, _, err := CompileV2(ctx, transition, nil, nil, DefaultConfigV2(16, 16)); program != nil || !errors.Is(err, ErrDocumentInvalid) {
		t.Fatalf("Text transition program=%v err=%v", program, err)
	}

	program, _, err := CompileV2(ctx, document, nil, []textlayer.FontBinding{valid}, DefaultConfigV2(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	result, err := program.RenderAt(ctx, "timeline", rt(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	tampered := cloneResultValue(result)
	textReceipt := *tampered.Visual.Sources[0].Text
	textReceipt.Assembly.AssemblyHash = shaA()
	tampered.Visual.Sources[0].Text = &textReceipt
	tampered.Visual.Surface = nil
	tampered.Output.Frame = nil
	tampered.PNG.Artifact = nil
	if err := program.ValidateSerializedReceipt(ctx, tampered); err == nil {
		t.Fatal("tampered Text derivation accepted")
	}
	for index, mutate := range []func(*textlayer.AssetReceipt){
		func(value *textlayer.AssetReceipt) {
			value.TextSemanticHash = shaA()
			value.Assembly.TextSemanticHash = shaA()
		},
		func(value *textlayer.AssetReceipt) { value.Assembly.DescriptorHash = shaA() },
		func(value *textlayer.AssetReceipt) { value.Assembly.ShapeProviderProfile = "hostile" },
		func(value *textlayer.AssetReceipt) { value.Assembly.CanonicalBinaryBytes = 1 },
		func(value *textlayer.AssetReceipt) { value.Raster.BindingHashProfile = "hostile" },
		func(value *textlayer.AssetReceipt) { value.Raster.CoverageHashProfile = "hostile" },
		func(value *textlayer.AssetReceipt) { value.Raster.WorkingBytes = 1 },
	} {
		manifest := program.SourceManifest()
		mutate(&manifest.Text.Assets[0])
		if validTextManifest(ctx, *manifest.Text, document, program.config.TextConfig.Limits) {
			t.Fatalf("deep Text manifest tamper %d accepted", index)
		}
	}

	base, _, err := normalizeConfigMode(DefaultConfigV2(16, 16), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := checkedSum(base.NativeLimits.MaxRetainedBytes, base.ExpressionLimits.MaxRetainedBytes, base.TextConfig.Limits.MaxTotalStaticSurfaceBytes)
	metadata, _ := checkedMul(uint64(base.CompositorLimits.MaxDocumentNodes), metadataBytesPerDocumentNode)
	textCompile, _ := checkedSum(base.NativeLimits.MaxRetainedBytes, base.ExpressionLimits.MaxRetainedBytes, base.TextConfig.Limits.MaxCompileWorkingBytes, metadata)
	if base.PipelineLimits.MaxRetainedSourceBytes < retained || base.PipelineLimits.MaxCompileWorkingBytes < textCompile {
		t.Fatalf("incoherent defaults config=%+v retained=%d textCompile=%d", base.PipelineLimits, retained, textCompile)
	}
	limited := DefaultConfigV2(16, 16)
	limited.PipelineLimits.MaxRetainedSourceBytes = retained - 1
	if got, _, compileErr := CompileV2(ctx, document, nil, []textlayer.FontBinding{valid}, limited); got != nil || !errors.Is(compileErr, ErrResourceLimit) {
		t.Fatalf("N-1 retained program=%v err=%v", got, compileErr)
	}
	limited = DefaultConfigV2(16, 16)
	limited.PipelineLimits.MaxCompileWorkingBytes = textCompile - 1
	if got, _, compileErr := CompileV2(ctx, document, nil, []textlayer.FontBinding{valid}, limited); got != nil || !errors.Is(compileErr, ErrResourceLimit) {
		t.Fatalf("N-1 text compile program=%v err=%v", got, compileErr)
	}
}

func TestTextV2MidHashCancellationClassifiesCanceled(t *testing.T) {
	surface, err := cpucompositor.NewSurface(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	ctx := &referenceVisualCancelHashContext{cancelAt: 3}
	err = validateReplaySurface(ctx, surface, 64, 64, hash, "text/replay")
	var typed *Error
	if !errors.Is(err, ErrCanceled) || !errors.As(err, &typed) || typed.Code != CodeCanceled || ctx.checks != 4 {
		t.Fatalf("err=%v typed=%+v checks=%d", err, typed, ctx.checks)
	}
}

func TestTextV2AggregateExactAndNMinusOne(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildReferenceVisualTextProvider(t, ctx)
	document := referenceVisualTextDocument(fontHash, false)
	hash := referenceVisualDocumentHash(t, document)
	binding := []textlayer.FontBinding{{DocumentHash: hash, FontAssetID: "font", Provider: font}}
	base, _, err := normalizeConfigMode(DefaultConfigV2(16, 16), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := checkedSum(base.NativeLimits.MaxRetainedBytes, base.ExpressionLimits.MaxRetainedBytes, base.TextConfig.Limits.MaxTotalStaticSurfaceBytes)
	metadata, _ := checkedMul(uint64(base.CompositorLimits.MaxDocumentNodes), metadataBytesPerDocumentNode)
	canonicalWorking, _ := checkedMul(minimumCanonicalLimitMode(base, true), canonicalCompileMultiplier)
	transient := base.NativeLimits.PNG.MaxWorkingBytes
	if base.ExpressionLimits.Vector.MaxWorkingBytes > transient {
		transient = base.ExpressionLimits.Vector.MaxWorkingBytes
	}
	genericCompile, _ := checkedSum(retained, transient, canonicalWorking, metadata)
	textCompile, _ := checkedSum(base.NativeLimits.MaxRetainedBytes, base.ExpressionLimits.MaxRetainedBytes,
		base.TextConfig.Limits.MaxCompileWorkingBytes, metadata)
	compilePeak := genericCompile
	if textCompile > compilePeak {
		compilePeak = textCompile
	}
	frame, output, artifact, row, ok := configuredRuntimeSizes(base)
	if !ok {
		t.Fatal("runtime shape overflow")
	}
	visualPeak, _ := checkedMul(frame, 6)
	colorPeak, _ := checkedAdd(frame, output)
	pngPeak, _ := checkedSum(frame, output, artifact, row)
	stagePeak := visualPeak
	if colorPeak > stagePeak {
		stagePeak = colorPeak
	}
	if pngPeak > stagePeak {
		stagePeak = pngPeak
	}
	runtimePeak, _ := checkedSum(retained, metadata, stagePeak)
	exact := DefaultConfigV2(16, 16)
	exact.PipelineLimits = PipelineLimits{
		MaxRetainedSourceBytes: retained,
		MaxCompileWorkingBytes: compilePeak,
		MaxRuntimeWorkingBytes: runtimePeak,
	}
	if program, diagnostics, compileErr := CompileV2(ctx, document, nil, binding, exact); compileErr != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("exact aggregate program=%v diagnostics=%+v err=%v", program, diagnostics, compileErr)
	}
	for _, test := range []struct {
		name   string
		mutate func(*PipelineLimits)
	}{
		{"retained", func(value *PipelineLimits) { value.MaxRetainedSourceBytes-- }},
		{"generic-or-text-compile", func(value *PipelineLimits) { value.MaxCompileWorkingBytes-- }},
		{"runtime", func(value *PipelineLimits) { value.MaxRuntimeWorkingBytes-- }},
	} {
		t.Run(test.name+"-n-minus-one", func(t *testing.T) {
			config := exact
			test.mutate(&config.PipelineLimits)
			if program, _, compileErr := CompileV2(ctx, document, nil, binding, config); program != nil || !errors.Is(compileErr, ErrResourceLimit) {
				t.Fatalf("program=%v err=%v limits=%+v", program, compileErr, config.PipelineLimits)
			}
		})
	}
	for _, mutate := range []func(*PipelineLimits){
		func(value *PipelineLimits) { value.MaxRetainedSourceBytes = math.MaxUint64 },
		func(value *PipelineLimits) { value.MaxCompileWorkingBytes = math.MaxUint64 },
		func(value *PipelineLimits) { value.MaxRuntimeWorkingBytes = math.MaxUint64 },
	} {
		config := exact
		mutate(&config.PipelineLimits)
		if program, _, compileErr := CompileV2(ctx, document, nil, binding, config); program != nil || !errors.Is(compileErr, ErrConfigInvalid) {
			t.Fatalf("overflow config program=%v err=%v", program, compileErr)
		}
	}
}

func TestTextV2ConfigDescriptorAndManifestOwnership(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildReferenceVisualTextProvider(t, ctx)
	document := referenceVisualTextDocument(fontHash, false)
	hash := referenceVisualDocumentHash(t, document)
	config := DefaultConfigV2(16, 16)
	wantTextConfig := *config.TextConfig
	program, diagnostics, err := CompileV2(ctx, document, nil, []textlayer.FontBinding{{
		DocumentHash: hash, FontAssetID: "font", Provider: font,
	}}, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	config.TextConfig.Limits.MaxBindings = 1
	first := program.Config()
	if first.TextConfig == nil || *first.TextConfig != wantTextConfig {
		t.Fatalf("caller config mutation reached Program: %+v", first.TextConfig)
	}
	first.TextConfig.Limits.MaxBindings = 2
	second := program.Config()
	if second.TextConfig == nil || *second.TextConfig != wantTextConfig {
		t.Fatalf("Config accessor aliases Program: %+v", second.TextConfig)
	}
	manifest := program.SourceManifest()
	manifest.Text.Profile = "mutated"
	manifest.Text.Assets[0].Assembly.AssemblyHash = shaA()
	again := program.SourceManifest()
	if again.Text == nil || again.Text.Profile != textlayer.ProfileV1 || again.Text.Assets[0].Assembly.AssemblyHash == shaA() {
		t.Fatalf("manifest accessor aliases Program: %+v", again.Text)
	}
	descriptor := DescriptorV2()
	if descriptor.DefaultTextConfig == nil {
		t.Fatal("V2 descriptor omitted Text config")
	}
	descriptor.DefaultTextConfig.Limits.MaxBindings = 1
	againDescriptor := DescriptorV2()
	if againDescriptor.DefaultTextConfig == nil || *againDescriptor.DefaultTextConfig != defaultTextConfigV2() ||
		Descriptor().DefaultTextConfig != nil || Descriptor().TextSourceProfile != "" || Descriptor().TextContractVersion != 0 {
		t.Fatalf("descriptor ownership/v1 drift: v2=%+v v1=%+v", againDescriptor, Descriptor())
	}
}

type referenceVisualCancelHashContext struct {
	checks   int
	cancelAt int
}

func (*referenceVisualCancelHashContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*referenceVisualCancelHashContext) Done() <-chan struct{}       { return nil }
func (value *referenceVisualCancelHashContext) Err() error {
	value.checks++
	if value.checks >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
func (*referenceVisualCancelHashContext) Value(any) any { return nil }

func referenceVisualTextDocument(fontHash string, properties bool) documentv2.Document {
	lineHeight := rat(16, 1)
	clip := documentv2.Clip{ID: "text-clip", AssetID: "text", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}
	if properties {
		start, end := point(0, 1, 0, 1), point(2, 1, 0, 1)
		one, half := rat(1, 1), rat(1, 2)
		clip.PropertySet = &documentv2.PropertySet{
			Transform: &documentv2.TransformProperty{Position: start, Scale: point(1, 1, 1, 1), RotationDegrees: rat(0, 1), Anchor: point(1, 2, 1, 2)},
			Crop:      &documentv2.CropProperty{Rect: documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: one, Height: one}},
			Opacity:   &documentv2.OpacityProperty{Value: one},
		}
		clip.KeyframeLanes = []documentv2.KeyframeLane{
			{ID: "position", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint, Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &start}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Point: &end}},
			}},
			{ID: "opacity", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{
				{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &one}, Interpolation: documentv2.InterpolationLinear},
				{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: &half}},
			}},
		}
	}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "text-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "font", Kind: documentv2.AssetKindFont, URI: "font.ttf", ContentHash: fontHash},
			{ID: "text", Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{
				Profile: documentv2.TextProfilePinnedFontViewportPXV2, Text: "A", FontAssetID: "font",
				FontSize: rat(16, 1), LineHeight: &lineHeight, Viewport: &documentv2.RasterViewport{WidthPixels: 16, HeightPixels: 16},
				Fill: documentv2.ColorRGBA16{R: 65_535, G: 40_000, A: 65_535}, Language: "en",
				Script: documentv2.TextScriptLatin, Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart,
			}},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "text-track", Kind: documentv2.TrackKindText, Clips: []documentv2.Clip{clip},
		}}}},
	}
}

func referenceVisualDocumentHash(t testing.TB, document documentv2.Document) string {
	t.Helper()
	snapshot, diagnostics := evaluator.FreezeSnapshot(context.Background(), document, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20,
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	return snapshot.DocumentHash()
}

func buildReferenceVisualTextProvider(t testing.TB, ctx context.Context) (*sfntglyf.ShapeProvider, string) {
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
