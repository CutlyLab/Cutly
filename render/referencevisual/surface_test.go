package referencevisual

import (
	"bytes"
	"context"
	"errors"
	stdcolor "image/color"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

func TestRenderSurfaceMixedNativeAndVectorParity(t *testing.T) {
	ctx := context.Background()
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 224, G: 48, B: 16, A: 192}, 0)
	document := mixedDocument(contentHash(encoded))
	tests := []struct {
		name    string
		compile func() (*Program, []documentv2.Diagnostic, error)
		profile string
	}{
		{
			name: "v1",
			compile: func() (*Program, []documentv2.Diagnostic, error) {
				return Compile(ctx, document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, DefaultConfig(2, 2))
			},
			profile: ProfileV1,
		},
		{
			name: "v2",
			compile: func() (*Program, []documentv2.Diagnostic, error) {
				return CompileV2(ctx, document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, nil, DefaultConfigV2(2, 2))
			},
			profile: ProfileV2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program, diagnostics, err := test.compile()
			if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("compile program=%v diagnostics=%+v err=%v", program, diagnostics, err)
			}
			at := rt(1, 2)
			surface, err := program.RenderSurfaceAt(ctx, "timeline", at)
			if err != nil {
				t.Fatal(err)
			}
			complete, err := program.RenderAt(ctx, "timeline", at)
			if err != nil {
				t.Fatal(err)
			}
			if program.Profile() != test.profile || len(surface.Sources) != 2 ||
				surface.Sources[0].Kind != visualcompositor.SourceKindNative ||
				surface.Sources[1].Kind != visualcompositor.SourceKindExpression ||
				!equalVisualReceipt(surface, complete.Visual) || surface.PixelHash != complete.Visual.PixelHash ||
				!bytes.Equal(canonicalSurfaceBytes(t, surface), canonicalSurfaceBytes(t, complete.Visual)) {
				t.Fatalf("mixed surface/full drift\nsurface=%+v\nfull=%+v", surface, complete.Visual)
			}
		})
	}
}

func TestRenderSurfaceV2MixedNativeVectorAndTextParity(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildReferenceVisualTextProvider(t, ctx)
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 192, G: 32, A: 255}, 0)
	document := mixedDocument(contentHash(encoded))
	textDocument := referenceVisualTextDocument(fontHash, true)
	document.Assets = append(document.Assets, textDocument.Assets...)
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, textDocument.Timelines[0].Tracks[0])
	documentHash := referenceVisualDocumentHash(t, document)
	program, diagnostics, err := CompileV2(ctx, document, []Binding{{AssetID: "image", EncodedPNG: encoded}}, []textlayer.FontBinding{{
		DocumentHash: documentHash, FontAssetID: "font", Provider: font,
	}}, DefaultConfigV2(16, 16))
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	surface, err := program.RenderSurfaceAt(ctx, "timeline", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	complete, err := program.RenderAt(ctx, "timeline", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(30, 1)}
	frame, err := program.RenderSurfaceFrame(ctx, "timeline", clock, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(surface.Sources) != 3 || surface.Sources[0].Kind != visualcompositor.SourceKindNative ||
		surface.Sources[1].Kind != visualcompositor.SourceKindExpression || surface.Sources[2].Kind != visualcompositor.SourceKindText ||
		!equalVisualReceipt(surface, complete.Visual) || !equalVisualReceipt(surface, frame.Result) ||
		surface.PixelHash != complete.Visual.PixelHash || surface.PixelHash != frame.Result.PixelHash ||
		!bytes.Equal(canonicalSurfaceBytes(t, surface), canonicalSurfaceBytes(t, complete.Visual)) ||
		!bytes.Equal(canonicalSurfaceBytes(t, surface), canonicalSurfaceBytes(t, frame.Result)) {
		t.Fatalf("V2 mixed surface/full/frame drift\nsurface=%+v\nfull=%+v\nframe=%+v", surface, complete.Visual, frame.Result)
	}
}

func TestRenderSurfaceMatchesCompleteExportAndFramePath(t *testing.T) {
	tests := []struct {
		name    string
		compile func(context.Context, documentv2.Document) (*Program, error)
		profile string
	}{
		{
			name: "v1",
			compile: func(ctx context.Context, document documentv2.Document) (*Program, error) {
				program, _, err := Compile(ctx, document, nil, DefaultConfig(3, 2))
				return program, err
			},
			profile: ProfileV1,
		},
		{
			name: "v2",
			compile: func(ctx context.Context, document documentv2.Document) (*Program, error) {
				program, _, err := CompileV2(ctx, document, nil, nil, DefaultConfigV2(3, 2))
				return program, err
			},
			profile: ProfileV2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			document := documentv2.Document{
				SchemaVersion: documentv2.SchemaVersion, ID: "surface-gap", Revision: 1,
				Timelines: []documentv2.Timeline{{ID: "timeline"}},
			}
			program, err := test.compile(ctx, document)
			if err != nil || program == nil {
				t.Fatalf("compile program=%v err=%v", program, err)
			}
			if program.Profile() != test.profile {
				t.Fatalf("Profile()=%q want %q", program.Profile(), test.profile)
			}
			at := rt(3, 2)
			surface, err := program.RenderSurfaceAt(ctx, "timeline", at)
			if err != nil {
				t.Fatal(err)
			}
			complete, err := program.RenderAt(ctx, "timeline", at)
			if err != nil {
				t.Fatal(err)
			}
			if !equalVisualReceipt(surface, complete.Visual) || surface.Surface == nil || complete.Visual.Surface == nil ||
				!bytes.Equal(canonicalSurfaceBytes(t, surface), canonicalSurfaceBytes(t, complete.Visual)) {
				t.Fatalf("surface/full visual drift\nsurface=%+v\nfull=%+v", surface, complete.Visual)
			}

			clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(2, 1)}
			frame, err := program.RenderSurfaceFrame(ctx, "timeline", clock, 3)
			if err != nil {
				t.Fatal(err)
			}
			if frame.FrameClock != clock || frame.CompositionFrameIndex != 3 || frame.Result.TimelineTime != at ||
				!equalVisualReceipt(frame.Result, surface) || !bytes.Equal(canonicalSurfaceBytes(t, frame.Result), canonicalSurfaceBytes(t, surface)) {
				t.Fatalf("surface frame mismatch: %+v", frame)
			}
			fullFrame, err := program.RenderFrame(ctx, "timeline", clock, 3)
			if err != nil || !equalVisualReceipt(fullFrame.Result.Visual, frame.Result) {
				t.Fatalf("full frame mismatch result=%+v err=%v", fullFrame, err)
			}
		})
	}
}

func TestRenderSurfaceFailureIsZero(t *testing.T) {
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "surface-failure", Revision: 1,
		Timelines: []documentv2.Timeline{{ID: "timeline"}},
	}
	program, _, err := CompileV2(context.Background(), document, nil, nil, DefaultConfigV2(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, renderErr := program.RenderSurfaceAt(canceled, "timeline", rt(0, 1)); !reflect.DeepEqual(result, visualcompositor.Result{}) || !errors.Is(renderErr, ErrCanceled) {
		t.Fatalf("canceled RenderSurfaceAt result=%+v err=%v", result, renderErr)
	}
	if result, renderErr := program.RenderSurfaceFrame(canceled, "timeline", videoedit.FrameClock{
		Origin: rt(0, 1), FramesPerSecond: rt(30, 1),
	}, 0); !reflect.DeepEqual(result, SurfaceFrameResult{}) || !errors.Is(renderErr, ErrCanceled) {
		t.Fatalf("canceled RenderSurfaceFrame result=%+v err=%v", result, renderErr)
	}
	if result, renderErr := program.RenderSurfaceFrame(context.Background(), "timeline", videoedit.FrameClock{}, 0); !reflect.DeepEqual(result, SurfaceFrameResult{}) || !errors.Is(renderErr, ErrRenderFailed) {
		t.Fatalf("invalid clock result=%+v err=%v", result, renderErr)
	}
}

func canonicalSurfaceBytes(t testing.TB, value visualcompositor.Result) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if value.Surface == nil {
		t.Fatal("nil surface")
	}
	if err := value.Surface.WriteCanonical(&buffer); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
