package textassembly_test

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textraster"
)

func TestPublicFrozenTextToAuthenticatedStaticRaster(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenText(t, ctx, fontHash)

	source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, textassembly.DefaultLimits())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if source == nil || receipt != source.Receipt() || receipt.AssemblyHash == "" ||
		receipt.ShapingProvenanceStatus != textassembly.ShapingProvenanceStatusV1 ||
		receipt.PaintProvenanceStatus != textassembly.PaintProvenanceStatusV1 ||
		receipt.ShapeOutputHash == receipt.GlyphStreamHash || receipt.SourceOutlineSetHash == receipt.ScaledOutlineHash ||
		receipt.GlyphCount != 1 || receipt.UniqueGlyphCount != 1 || receipt.CommandCount != 0 {
		t.Fatalf("assembly receipt=%+v", receipt)
	}
	if got := reflect.TypeOf(source).NumMethod(); got != 1 {
		t.Fatalf("opaque Source exported method count=%d, want Receipt only", got)
	}

	surface, rasterReceipt, err := textassembly.RasterizeStatic(ctx, source, textassembly.DefaultRasterLimits())
	if err != nil {
		t.Fatalf("RasterizeStatic: %v", err)
	}
	if surface == nil || rasterReceipt.AssemblyHash != receipt.AssemblyHash ||
		rasterReceipt.InnerShapingStatus != textraster.ShapingProvenanceStatusV1 ||
		rasterReceipt.ShapingProvenanceStatus != textassembly.ShapingProvenanceStatusV1 ||
		rasterReceipt.PaintProvenanceStatus != textassembly.PaintProvenanceStatusV1 {
		t.Fatalf("raster receipt=%+v", rasterReceipt)
	}
	pixelHash, err := surface.CanonicalHash()
	if err != nil || pixelHash != rasterReceipt.PixelHash {
		t.Fatalf("surface hash=%q receipt=%q err=%v", pixelHash, rasterReceipt.PixelHash, err)
	}
	view, err := surface.View()
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < view.Height(); y++ {
		for x := 0; x < view.Width(); x++ {
			pixel, err := view.Pixel(x, y)
			if err != nil || pixel.R != 0 || pixel.G != 0 || pixel.B != 0 || pixel.A != 0 {
				t.Fatalf("pixel(%d,%d)=%+v err=%v", x, y, pixel, err)
			}
		}
	}
}

func TestFailureZeroForInvalidAndCanceledInputs(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenText(t, ctx, fontHash)
	for _, test := range []struct {
		name    string
		ctx     context.Context
		request textassembly.Request
		want    error
	}{
		{name: "nil frozen", ctx: ctx, request: textassembly.Request{Provider: provider}, want: textassembly.ErrInputInvalid},
		{name: "zero frozen", ctx: ctx, request: textassembly.Request{Text: &evaluator.FrozenTextSource{}, Provider: provider}, want: textassembly.ErrInputInvalid},
		{name: "nil provider", ctx: ctx, request: textassembly.Request{Text: frozen}, want: textassembly.ErrInputInvalid},
		{name: "nil context", request: textassembly.Request{Text: frozen, Provider: provider}, want: textassembly.ErrContextInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, receipt, err := textassembly.Assemble(test.ctx, test.request, textassembly.DefaultLimits())
			if source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, test.want) {
				t.Fatalf("source=%v receipt=%+v err=%v", source, receipt, err)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if source, receipt, err := textassembly.Assemble(canceled, textassembly.Request{Text: frozen, Provider: provider}, textassembly.DefaultLimits()); source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrCanceled) {
		t.Fatalf("canceled source=%v receipt=%+v err=%v", source, receipt, err)
	}
	if surface, receipt, err := textassembly.RasterizeStatic(ctx, nil, textassembly.DefaultRasterLimits()); surface != nil || receipt != (textassembly.RasterReceipt{}) || !errors.Is(err, textassembly.ErrInputInvalid) {
		t.Fatalf("nil raster surface=%v receipt=%+v err=%v", surface, receipt, err)
	}
}

func TestRejectsFrozenTextFromDifferentFontLineage(t *testing.T) {
	ctx := context.Background()
	provider, _ := buildProvider(t, ctx)
	foreign := buildFrozenText(t, ctx, "sha256:"+strings.Repeat("e", 64))
	source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{
		Text: foreign, Provider: provider,
	}, textassembly.DefaultLimits())
	if source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrBinding) {
		t.Fatalf("source=%v receipt=%+v err=%v", source, receipt, err)
	}
}

func TestRecoveredValueIsNeverFormatted(t *testing.T) {
	ctx := hostilePanicContext{}
	if source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{}, textassembly.DefaultLimits()); source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrInvariantFailed) {
		t.Fatalf("Assemble source=%v receipt=%+v err=%v", source, receipt, err)
	}
	if surface, receipt, err := textassembly.RasterizeStatic(ctx, nil, textassembly.DefaultRasterLimits()); surface != nil || receipt != (textassembly.RasterReceipt{}) || !errors.Is(err, textassembly.ErrInvariantFailed) {
		t.Fatalf("RasterizeStatic surface=%v receipt=%+v err=%v", surface, receipt, err)
	}
}

func TestOuterResourceExactAndNMinusOne(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenText(t, ctx, fontHash)
	source, assembly, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, textassembly.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	exact := textassembly.DefaultLimits()
	exact.MaxCanonicalBinaryBytes = assembly.CanonicalBinaryBytes
	exact.MaxWorkUnits = assembly.WorkUnits
	exact.MaxWorkingBytes = assembly.WorkingBytes
	exact.MaxRetainedBytes = assembly.RetainedBytes
	if got, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, exact); err != nil || got == nil || receipt != assembly {
		t.Fatalf("exact source=%v equal=%t err=%v", got, receipt == assembly, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*textassembly.Limits)
	}{
		{name: "canonical", cut: func(value *textassembly.Limits) { value.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(value *textassembly.Limits) { value.MaxWorkUnits-- }},
		{name: "working", cut: func(value *textassembly.Limits) { value.MaxWorkingBytes-- }},
		{name: "retained", cut: func(value *textassembly.Limits) { value.MaxRetainedBytes-- }},
	} {
		t.Run("assembly_"+test.name, func(t *testing.T) {
			below := exact
			test.cut(&below)
			got, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, below)
			if got != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrResourceLimit) {
				t.Fatalf("source=%v receipt=%+v err=%v", got, receipt, err)
			}
		})
	}

	_, raster, err := textassembly.RasterizeStatic(ctx, source, textassembly.DefaultRasterLimits())
	if err != nil {
		t.Fatal(err)
	}
	exactRaster := textassembly.DefaultRasterLimits()
	exactRaster.MaxCanonicalBinaryBytes = raster.CanonicalBinaryBytes
	exactRaster.MaxWorkUnits = raster.WorkUnits
	exactRaster.MaxWorkingBytes = raster.WorkingBytes
	exactRaster.MaxRetainedBytes = raster.RetainedBytes
	if got, receipt, err := textassembly.RasterizeStatic(ctx, source, exactRaster); err != nil || got == nil || receipt != raster {
		t.Fatalf("exact raster surface=%v equal=%t err=%v", got, receipt == raster, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*textassembly.RasterLimits)
	}{
		{name: "canonical", cut: func(value *textassembly.RasterLimits) { value.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(value *textassembly.RasterLimits) { value.MaxWorkUnits-- }},
		{name: "working", cut: func(value *textassembly.RasterLimits) { value.MaxWorkingBytes-- }},
		{name: "retained", cut: func(value *textassembly.RasterLimits) { value.MaxRetainedBytes-- }},
	} {
		t.Run("raster_"+test.name, func(t *testing.T) {
			below := exactRaster
			test.cut(&below)
			got, receipt, err := textassembly.RasterizeStatic(ctx, source, below)
			if got != nil || receipt != (textassembly.RasterReceipt{}) || !errors.Is(err, textassembly.ErrResourceLimit) {
				t.Fatalf("surface=%v receipt=%+v err=%v", got, receipt, err)
			}
		})
	}
}

func TestTinyOuterBudgetsRejectAtPreflight(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenText(t, ctx, fontHash)
	limits := textassembly.DefaultLimits()
	limits.MaxWorkingBytes = 1
	source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, limits)
	var typed *textassembly.Error
	if source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrResourceLimit) ||
		!errors.As(err, &typed) || !strings.HasPrefix(typed.Path, "assembly/preflight/") {
		t.Fatalf("source=%v receipt=%+v err=%v typed=%+v", source, receipt, err, typed)
	}

	source, _, err = textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, textassembly.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	rasterLimits := textassembly.DefaultRasterLimits()
	rasterLimits.MaxWorkingBytes = 1
	surface, rasterReceipt, err := textassembly.RasterizeStatic(ctx, source, rasterLimits)
	typed = nil
	if surface != nil || rasterReceipt != (textassembly.RasterReceipt{}) || !errors.Is(err, textassembly.ErrResourceLimit) ||
		!errors.As(err, &typed) || !strings.HasPrefix(typed.Path, "raster/preflight/") {
		t.Fatalf("surface=%v receipt=%+v err=%v typed=%+v", surface, rasterReceipt, err, typed)
	}
}

func TestRasterCancellationDuringOuterSurfaceHashIsFailureZero(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenTextViewport(t, ctx, fontHash, 128, 128)
	source, _, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, textassembly.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	baseline := &countingCancelContext{}
	if surface, receipt, err := textassembly.RasterizeStatic(baseline, source, textassembly.DefaultRasterLimits()); err != nil || surface == nil || receipt == (textassembly.RasterReceipt{}) {
		t.Fatalf("baseline surface=%v receipt=%+v err=%v", surface, receipt, err)
	}
	// Five context checks follow the outer surface hash (two for each binding hash and one before
	// publication). Cancel eight surface-hash checks before that boundary, after several pixel
	// blocks have already been hashed. The exact path assertion proves the inner raster completed.
	if baseline.checks <= 13 {
		t.Fatalf("baseline context checks=%d, want enough to cancel mid-hash", baseline.checks)
	}
	canceled := &countingCancelContext{cancelAt: baseline.checks - 13}
	surface, receipt, err := textassembly.RasterizeStatic(canceled, source, textassembly.DefaultRasterLimits())
	var typed *textassembly.Error
	if surface != nil || receipt != (textassembly.RasterReceipt{}) || !errors.Is(err, textassembly.ErrCanceled) ||
		!errors.As(err, &typed) || typed.Path != "raster/surface/hash" {
		t.Fatalf("surface=%v receipt=%+v err=%v typed=%+v checks=%d/%d", surface, receipt, err, typed, canceled.checks, baseline.checks)
	}
}

func TestOverHardNestedLimitsAreNotSilentlyClamped(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	frozen := buildFrozenText(t, ctx, fontHash)
	limits := textassembly.DefaultLimits()
	limits.Shape.MaxLines = sfntglyf.HardMaxShapeLines + 1
	if source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, limits); source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrLimitsInvalid) {
		t.Fatalf("shape over-hard source=%v receipt=%+v err=%v", source, receipt, err)
	}
	limits = textassembly.DefaultLimits()
	limits.LayoutSource.MaxCommands = sfntglyf.HardMaxLayoutSourceCommands + 1
	if source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, limits); source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrLimitsInvalid) {
		t.Fatalf("source over-hard source=%v receipt=%+v err=%v", source, receipt, err)
	}
}

func TestNestedLayoutCapsReachUpstreamAdmission(t *testing.T) {
	ctx := context.Background()
	provider, fontHash := buildProvider(t, ctx)
	multiline := buildFrozenText(t, ctx, fontHash, "A\nA")
	limits := textassembly.DefaultLimits()
	limits.Layout.MaxLines = 1
	if source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: multiline, Provider: provider}, limits); source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrResourceLimit) {
		t.Fatalf("line cap source=%v receipt=%+v err=%v", source, receipt, err)
	}

	frozen := buildFrozenText(t, ctx, fontHash)
	limits = textassembly.DefaultLimits()
	limits.Layout.MaxWidth = 15
	source, receipt, err := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, limits)
	var typed *textassembly.Error
	if source != nil || receipt != (textassembly.Receipt{}) || !errors.Is(err, textassembly.ErrResourceLimit) ||
		!errors.As(err, &typed) || typed.Path != "assembly/preflight/layout/counts" {
		t.Fatalf("viewport cap source=%v receipt=%+v err=%v typed=%+v", source, receipt, err, typed)
	}
}

type hostilePanicContext struct{}

func (hostilePanicContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (hostilePanicContext) Done() <-chan struct{}       { return nil }
func (hostilePanicContext) Err() error                  { panic(hostilePanicValue{}) }
func (hostilePanicContext) Value(any) any               { return nil }

type hostilePanicValue struct{}

func (hostilePanicValue) String() string { panic("recovered value was formatted") }

type countingCancelContext struct {
	checks   int
	cancelAt int
}

func (*countingCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*countingCancelContext) Done() <-chan struct{}       { return nil }
func (value *countingCancelContext) Err() error {
	value.checks++
	if value.cancelAt != 0 && value.checks >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
func (*countingCancelContext) Value(any) any { return nil }

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

func buildFrozenText(t *testing.T, ctx context.Context, fontHash string, textValue ...string) *evaluator.FrozenTextSource {
	return buildFrozenTextViewport(t, ctx, fontHash, 16, 16, textValue...)
}

func buildFrozenTextViewport(t *testing.T, ctx context.Context, fontHash string, width, height uint32, textValue ...string) *evaluator.FrozenTextSource {
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
	document.Assets[4].Text.Profile = documentv2.TextProfilePinnedFontViewportPXV2
	document.Assets[4].Text.Text = "A"
	if len(textValue) != 0 {
		document.Assets[4].Text.Text = textValue[0]
	}
	document.Assets[4].Text.FontSize = documentv2.Rational{Numerator: 16, Denominator: 1}
	document.Assets[4].Text.LineHeight = &lineHeight
	document.Assets[4].Text.Viewport = &documentv2.RasterViewport{WidthPixels: width, HeightPixels: height}
	document.Assets[4].Text.Language = "en"
	document.Assets[4].Text.Script = documentv2.TextScriptLatin
	document.Assets[4].Text.Direction = documentv2.TextDirectionLTR
	document.Assets[4].Text.Alignment = documentv2.TextAlignmentStart
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("required capabilities: %+v", diagnostics)
	}
	offered := videoedit.BackendCapabilities{BackendID: "textassembly-test", Features: required.Features}
	for _, kind := range required.TrackKinds {
		offered.TrackKinds = append(offered.TrackKinds, videoedit.TrackKind(kind))
	}
	for _, kind := range required.AssetKinds {
		offered.AssetKinds = append(offered.AssetKinds, videoedit.AssetKind(kind))
	}
	_, snapshot, diagnostics := evaluator.CompileSnapshot(ctx, document, offered, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20,
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("CompileSnapshot snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	frozen, _, err := snapshot.BindTextSource(ctx, document.Assets[4].ID, 4, evaluator.DefaultFrozenTextSourceLimits())
	if err != nil {
		t.Fatalf("BindTextSource: %v", err)
	}
	return frozen
}
