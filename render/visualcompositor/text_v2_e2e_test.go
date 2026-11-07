package visualcompositor

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/textlayer"
)

func TestRealTextLayerProviderComposesEndToEnd(t *testing.T) {
	ctx := context.Background()
	shapeProvider, fontHash := buildVisualShapeProvider(t, ctx)
	document := textV2VisualDocument(false)
	document.Assets[0].ContentHash = fontHash
	lineHeight := documentv2.Rational{Numerator: 16, Denominator: 1}
	document.Assets[1].Text.FontSize = documentv2.Rational{Numerator: 16, Denominator: 1}
	document.Assets[1].Text.LineHeight = &lineHeight
	document.Assets[1].Text.Viewport = &documentv2.RasterViewport{WidthPixels: 16, HeightPixels: 16}

	snapshot, diagnostics := evaluator.FreezeSnapshot(ctx, document, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: 1 << 20,
		MaxDocumentNodes:          1 << 20,
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	textProvider, err := textlayer.Compile(ctx, document, []textlayer.FontBinding{{
		DocumentHash: snapshot.DocumentHash(), FontAssetID: "font", Provider: shapeProvider,
	}}, textlayer.DefaultConfig())
	if err != nil {
		t.Fatalf("textlayer Compile: %v", err)
	}
	program, diagnostics := Compile(ctx, document, DefaultConfigV2(16, 16))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("visual program=%v diagnostics=%+v", program, diagnostics)
	}
	result, err := program.RenderAt(ctx, "timeline", rt(1, 2), Providers{Text: textProvider})
	if err != nil {
		t.Fatalf("RenderAt: %v", err)
	}
	manifest := textProvider.Manifest()
	if len(manifest.Assets) != 1 || len(result.Sources) != 1 || result.Sources[0].Text == nil ||
		result.Sources[0].Text.SurfaceIdentity != manifest.Assets[0].SurfaceIdentity ||
		result.PixelHash != manifest.Assets[0].SurfaceIdentity.PixelHash {
		t.Fatalf("manifest=%+v result=%+v", manifest, result)
	}
	if err := program.ValidateResult(ctx, result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
}

func buildVisualShapeProvider(t *testing.T, ctx context.Context) (*sfntglyf.ShapeProvider, string) {
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
