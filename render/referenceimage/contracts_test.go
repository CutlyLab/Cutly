package referenceimage

import (
	"context"
	"errors"
	stdcolor "image/color"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	mediacolor "github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func TestCompileFailureMatrixIsAtomic(t *testing.T) {
	encoded := strictPNG(t, 1, 1, colorPixel(255, 0, 0, 255), 0)
	document := singleImageDocument(contentHash(encoded))
	binding := []Binding{{AssetID: "asset", EncodedPNG: encoded}}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		document  documentv2.Document
		bindings  []Binding
		config    Config
		want      error
		wantCode  ErrorCode
		wantDiags bool
	}{
		{name: "nil context", ctx: nil, document: document, bindings: binding, config: DefaultConfig(1, 1), want: ErrContextInvalid, wantCode: CodeContextInvalid},
		{name: "canceled", ctx: canceled, document: document, bindings: binding, config: DefaultConfig(1, 1), want: ErrCanceled, wantCode: CodeCanceled},
		{name: "zero dimensions", ctx: context.Background(), document: document, bindings: binding, config: DefaultConfig(0, 1), want: ErrConfigInvalid, wantCode: CodeConfigInvalid},
		{name: "output hard width", ctx: context.Background(), document: document, bindings: binding, config: configWithOutputWidth(mediacolor.HardMaxOutputDimension + 1), want: ErrConfigInvalid, wantCode: CodeConfigInvalid},
		{name: "output bytes", ctx: context.Background(), document: document, bindings: binding, config: configWithOutputBytes(4), want: ErrResourceLimit, wantCode: CodeResourceLimit},
		{name: "png artifact bytes", ctx: context.Background(), document: document, bindings: binding, config: configWithPNGArtifactBytes(86), want: ErrResourceLimit, wantCode: CodeResourceLimit},
		{name: "source config", ctx: context.Background(), document: document, bindings: binding, config: configWithSourceBindings(nativepngsource.HardMaxBindings + 1), want: ErrConfigInvalid, wantCode: CodeConfigInvalid},
		{name: "compositor config", ctx: context.Background(), document: document, bindings: binding, config: configWithCompositorLayers(rastercompositor.HardMaxLayers + 1), want: ErrConfigInvalid, wantCode: CodeConfigInvalid, wantDiags: true},
		{name: "fingerprint", ctx: context.Background(), document: document, bindings: binding, config: configWithFingerprint(" forged "), want: ErrConfigInvalid, wantCode: CodeConfigInvalid, wantDiags: true},
		{name: "missing binding", ctx: context.Background(), document: document, config: DefaultConfig(1, 1), want: ErrSourceCompile, wantCode: CodeSourceCompile},
		{name: "content mismatch", ctx: context.Background(), document: document, bindings: []Binding{{AssetID: "asset", EncodedPNG: []byte("not the pinned PNG")}}, config: DefaultConfig(1, 1), want: ErrSourceCompile, wantCode: CodeSourceCompile},
		{name: "unsupported video", ctx: context.Background(), document: videoDocument(), config: DefaultConfig(1, 1), want: ErrDocumentInvalid, wantCode: CodeDocumentInvalid, wantDiags: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program, diagnostics, err := Compile(test.ctx, test.document, test.bindings, test.config)
			if program != nil || !errors.Is(err, test.want) {
				t.Fatalf("program=%v diagnostics=%+v err=%v want=%v", program, diagnostics, err, test.want)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != test.wantCode {
				t.Fatalf("typed error=%+v want code=%s", typed, test.wantCode)
			}
			if test.wantDiags && !documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("missing compositor diagnostics: %+v", diagnostics)
			}
		})
	}
}

func TestRenderFailuresReturnZeroAndForgedProgramFailsClosed(t *testing.T) {
	var nilProgram *Program
	result, err := nilProgram.RenderAt(context.Background(), "timeline", rt(0, 1))
	assertZeroResult(t, result, err, ErrProgramUnavailable, CodeProgramUnavailable)

	encoded := strictPNG(t, 1, 1, colorPixel(255, 0, 0, 255), 0)
	program, _, err := Compile(context.Background(), singleImageDocument(contentHash(encoded)), []Binding{{AssetID: "asset", EncodedPNG: encoded}}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = program.RenderAt(canceled, "timeline", rt(0, 1))
	assertZeroResult(t, result, err, ErrCanceled, CodeCanceled)
	result, err = program.RenderAt(nil, "timeline", rt(0, 1))
	assertZeroResult(t, result, err, ErrContextInvalid, CodeContextInvalid)
	result, err = program.RenderAt(context.Background(), "missing", rt(0, 1))
	assertZeroResult(t, result, err, ErrRenderFailed, CodeRenderFailed)

	forged := *program
	forged.config.Width = 2
	result, err = forged.RenderAt(context.Background(), "timeline", rt(0, 1))
	assertZeroResult(t, result, err, ErrProgramUnavailable, CodeProgramUnavailable)

	frame, frameErr := program.RenderFrame(context.Background(), "timeline", videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(0, 1)}, 1)
	if frame.Result.PNG.Artifact != nil || frame.Result.DocumentHash != "" || frameErr == nil {
		t.Fatalf("partial hostile frame=%+v err=%v", frame, frameErr)
	}
}

func assertZeroResult(t testing.TB, result Result, err error, wanted error, code ErrorCode) {
	t.Helper()
	if err == nil || !errors.Is(err, wanted) || result.DocumentHash != "" || result.Raster.Surface != nil ||
		result.Output.Frame != nil || result.PNG.Artifact != nil || result.SourceManifest.Assets != nil {
		t.Fatalf("partial result=%+v err=%v want=%v", result, err, wanted)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("typed=%+v want code=%s", typed, code)
	}
}

func configWithOutputWidth(width int) Config {
	value := DefaultConfig(1, 1)
	value.OutputLimits.MaxWidth = width
	return value
}
func configWithOutputBytes(bytes uint64) Config {
	value := DefaultConfig(2, 1)
	value.OutputLimits.MaxOutputBytes = bytes
	return value
}
func configWithPNGArtifactBytes(bytes uint64) Config {
	value := DefaultConfig(2, 1)
	value.PNGLimits.MaxArtifactBytes = bytes
	return value
}
func configWithSourceBindings(count int) Config {
	value := DefaultConfig(1, 1)
	value.SourceLimits.MaxBindings = count
	return value
}
func configWithCompositorLayers(count int) Config {
	value := DefaultConfig(1, 1)
	value.CompositorLimits.MaxLayers = count
	return value
}
func configWithFingerprint(value string) Config {
	config := DefaultConfig(1, 1)
	config.BuildFingerprint = value
	return config
}

func singleImageDocument(hash string) documentv2.Document {
	return documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset", Kind: documentv2.AssetKindImage, URI: "asset.png", ContentHash: hash}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip", AssetID: "asset", TimelineRange: tr(0, 1), SourceRange: tr(0, 1)}}}}}},
	}
}

func videoDocument() documentv2.Document {
	duration := rt(1, 1)
	return documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "video-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "video", Kind: documentv2.AssetKindVideo, URI: "video.mp4", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Duration: &duration}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip", AssetID: "video", TimelineRange: tr(0, 1), SourceRange: tr(0, 1)}}}}}},
	}
}

func colorPixel(r, g, b, a uint8) stdcolor.NRGBA { return stdcolor.NRGBA{R: r, G: g, B: b, A: a} }
