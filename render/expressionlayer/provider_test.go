package expressionlayer

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

func TestCompileFrameManifestAndCallerMutationIsolation(t *testing.T) {
	document := fixtureDocument()
	provider, err := Compile(context.Background(), document, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	manifest := provider.Manifest()
	if manifest.DocumentHash != provider.DocumentHash() || len(manifest.Assets) != 2 ||
		manifest.Assets[0].AssetID != "vector-a" || manifest.Assets[1].AssetID != "vector-b" {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, asset := range manifest.Assets {
		if asset.AssetSemanticHash != asset.RasterReceipt.SourceSemanticHash ||
			asset.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 ||
			asset.SurfaceIdentity.PixelHash != asset.RasterReceipt.PixelHash ||
			asset.SurfaceBytes != uint64(asset.RasterViewport.WidthPixels)*uint64(asset.RasterViewport.HeightPixels)*cpucompositor.BytesPerPixel {
			t.Fatalf("asset receipt is not cross-bound: %+v", asset)
		}
	}

	request := fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2})
	frame, err := provider.Frame(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Request != request || frame.ContractVersion != ContractVersion || frame.Surface == nil ||
		frame.AssetSemanticHash != request.AssetSemanticHash || frame.RasterReceipt.SourceSemanticHash != request.AssetSemanticHash ||
		frame.SurfaceIdentity.WidthPixels != 4 || frame.SurfaceIdentity.HeightPixels != 3 {
		t.Fatalf("frame = %+v", frame)
	}
	hash, err := frame.Surface.CanonicalHash()
	if err != nil || hash != frame.SurfaceIdentity.PixelHash {
		t.Fatalf("surface hash=%q err=%v identity=%+v", hash, err, frame.SurfaceIdentity)
	}
	second, err := provider.Frame(context.Background(), request)
	if err != nil || second.Surface != frame.Surface || second != frame {
		t.Fatalf("cached frame mismatch: first=%+v second=%+v err=%v", frame, second, err)
	}
	reused, err := provider.Frame(context.Background(), fixtureRequest(provider, "clip-b-repeat", documentv2.RationalTime{Value: 9, Rate: 2}))
	if err != nil || reused.Surface != frame.Surface || reused.AssetSemanticHash != frame.AssetSemanticHash {
		t.Fatalf("distinct clip did not reuse immutable expression surface: reused=%+v err=%v", reused, err)
	}

	// Compile deep-freezes canonical document bytes. Mutating every caller-owned nested layer after
	// success cannot alter the Provider or its cached surface.
	document.Assets[1].Vector.Viewport.WidthPixels = 99
	document.Assets[1].Vector.Paths[0].Fill.R = 0
	document.Assets[1].Vector.Paths[0].Commands[0].Points[0].X.Numerator = 999
	after, err := provider.Frame(context.Background(), request)
	if err != nil || after != frame {
		t.Fatalf("caller mutation changed provider: after=%+v err=%v", after, err)
	}

	manifest.Assets[0].AssetID = "mutated"
	again := provider.Manifest()
	if again.Assets[0].AssetID != "vector-a" {
		t.Fatal("Manifest returned provider-owned slice")
	}
	config := provider.Config()
	config.Limits.MaxExpressionAssets = 1
	if provider.Config().Limits.MaxExpressionAssets == 1 {
		t.Fatal("Config returned mutable provider state")
	}
}

func TestSemanticHashIsExpressionOnlyWhileDocumentHashScopesReplay(t *testing.T) {
	base := fixtureDocument()
	first, err := Compile(context.Background(), base, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	metadata := fixtureDocument()
	metadata.Assets[0].Metadata = map[string]json.RawMessage{"note": json.RawMessage(`"changed"`)}
	second, err := Compile(context.Background(), metadata, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if first.DocumentHash() == second.DocumentHash() ||
		first.assets["vector-b"].receipt.AssetSemanticHash != second.assets["vector-b"].receipt.AssetSemanticHash {
		t.Fatalf("metadata/document scope hash semantics are wrong: first=%s second=%s", first.DocumentHash(), second.DocumentHash())
	}
	paint := fixtureDocument()
	paint.Assets[0].Vector.Paths[0].Fill.B = 1
	third, err := Compile(context.Background(), paint, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if first.assets["vector-b"].receipt.AssetSemanticHash == third.assets["vector-b"].receipt.AssetSemanticHash {
		t.Fatal("paint change did not change expression semantic hash")
	}
}

func TestRasterResultIsIndependentlyBoundToFrozenExpression(t *testing.T) {
	modelAsset := fixtureDocument().Assets[0]
	asset := frozenAsset{index: 0, id: modelAsset.ID, kind: modelAsset.Kind, vector: *modelAsset.Vector}
	surface, receipt, err := vectorraster.Rasterize(context.Background(), asset.vector, vectorraster.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateRasterResult(context.Background(), asset, surface, receipt, "asset"); err != nil {
		t.Fatalf("valid raster result rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*vectorraster.Receipt)
	}{
		{"semantic hash", func(value *vectorraster.Receipt) { value.SourceSemanticHash = shaA() }},
		{"path count", func(value *vectorraster.Receipt) { value.PathCount++ }},
		{"command count", func(value *vectorraster.Receipt) { value.CommandCount++ }},
		{"conceptual edge count", func(value *vectorraster.Receipt) { value.ConceptualEdgeCount++ }},
		{"canonical source bytes", func(value *vectorraster.Receipt) { value.CanonicalSourceBytes++ }},
		{"pixel hash", func(value *vectorraster.Receipt) { value.PixelHash = shaA() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hostile := receipt
			test.mutate(&hostile)
			if got, err := validateRasterResult(context.Background(), asset, surface, hostile, "asset"); err == nil || got != (AssetReceipt{}) {
				t.Fatalf("hostile receipt accepted: got=%+v err=%v", got, err)
			}
		})
	}
	otherSurface, err := cpucompositor.NewSurface(4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := validateRasterResult(context.Background(), asset, otherSurface, receipt, "asset"); err == nil || got != (AssetReceipt{}) {
		t.Fatalf("surface/receipt mismatch accepted: got=%+v err=%v", got, err)
	}
}

func TestCompileRejectsUnsupportedEnabledProfileButIgnoresDisabledAndOtherKinds(t *testing.T) {
	document := fixtureDocument()
	document.Assets[1].Vector.Profile = documentv2.VectorProfilePathV1
	document.Assets[1].Vector.Viewport = nil
	if provider, err := Compile(context.Background(), document, DefaultConfig()); provider != nil || !errors.Is(err, ErrProfileUnsupported) {
		t.Fatalf("enabled v1 vector: provider=%v err=%v", provider, err)
	}

	document.Timelines[0].Tracks[0].Clips[1].Disabled = true
	provider, err := Compile(context.Background(), document, DefaultConfig())
	if err != nil {
		t.Fatalf("disabled v1 vector rejected: %v", err)
	}
	if got := provider.Manifest(); len(got.Assets) != 1 || got.Assets[0].AssetID != "vector-b" {
		t.Fatalf("disabled expression leaked into manifest: %+v", got)
	}

	empty := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "empty", Revision: 1}
	emptyProvider, err := Compile(context.Background(), empty, DefaultConfig())
	if err != nil || len(emptyProvider.Manifest().Assets) != 0 {
		t.Fatalf("empty document: provider=%v manifest=%+v err=%v", emptyProvider, emptyProvider.Manifest(), err)
	}
}

func TestFrameRejectsEveryMutatedBindingAndTime(t *testing.T) {
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	valid := fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2})
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{"contract", func(value *Request) { value.ContractVersion++ }},
		{"document hash", func(value *Request) { value.DocumentHash = shaA() }},
		{"timeline id", func(value *Request) { value.TimelineID = "other" }},
		{"timeline index", func(value *Request) { value.TimelineIndex++ }},
		{"track id", func(value *Request) { value.TrackID = "other" }},
		{"track index", func(value *Request) { value.TrackIndex++ }},
		{"track kind", func(value *Request) { value.TrackKind = documentv2.TrackKindVideo }},
		{"clip id", func(value *Request) { value.ClipID = "other" }},
		{"clip index", func(value *Request) { value.ClipIndex++ }},
		{"asset id", func(value *Request) { value.AssetID = "vector-a" }},
		{"asset index", func(value *Request) { value.AssetIndex++ }},
		{"asset kind", func(value *Request) { value.AssetKind = documentv2.AssetKindImage }},
		{"semantic profile", func(value *Request) { value.AssetSemanticHashProfile = "cutly.hash.other.v1" }},
		{"semantic hash", func(value *Request) { value.AssetSemanticHash = shaA() }},
		{"viewport", func(value *Request) { value.RasterViewport.WidthPixels++ }},
		{"timeline time end", func(value *Request) { value.TimelineTime = documentv2.RationalTime{Value: 2, Rate: 1} }},
		{"local time", func(value *Request) { value.LocalTime.Value++ }},
		{"source time", func(value *Request) { value.SourceTime.Value++ }},
		{"source profile", func(value *Request) { value.SourceProfile = "cutly.source.other.v1" }},
		{"selection profile", func(value *Request) { value.SelectionProfile = "cutly.selection.other.v1" }},
		{"expression profile", func(value *Request) { value.ExpressionProfile = documentv2.VectorProfilePathV1 }},
		{"evaluator profile", func(value *Request) { value.EvaluatorProfile = documentv2.EvaluatorVectorPathV1 }},
		{"raster profile", func(value *Request) { value.RasterProfile = "cutly.raster.other.v1" }},
		{"paint profile", func(value *Request) { value.PaintProfile = "cutly.color.other.v1" }},
		{"kernel profile", func(value *Request) { value.KernelProfile = "cutly.kernel.other.v1" }},
		{"surface profile", func(value *Request) { value.SurfaceProfile = "cutly.surface.other.v1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			frame, err := provider.Frame(context.Background(), request)
			if err == nil || frame != (Frame{}) {
				t.Fatalf("Frame accepted mutation: frame=%+v err=%v", frame, err)
			}
		})
	}
}

func TestFrameAcceptsOnlyTheClipHalfOpenVisibleInterval(t *testing.T) {
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	for _, at := range []documentv2.RationalTime{
		{Value: 0, Rate: 1}, // exact start
		{Value: 3, Rate: 2}, // end minus one half second
		{Value: 7, Rate: 4}, // another representable end-minus-epsilon sample
	} {
		request := fixtureRequest(provider, "clip-b", at)
		if frame, frameErr := provider.Frame(context.Background(), request); frameErr != nil || frame.Surface == nil {
			t.Fatalf("visible time %v rejected: frame=%+v err=%v", at, frame, frameErr)
		}
	}

	for _, at := range []documentv2.RationalTime{
		{Value: -1, Rate: 2}, // transition head / start minus epsilon
		{Value: 2, Rate: 1},  // exact exclusive end
		{Value: 5, Rate: 2},  // transition tail / after end
	} {
		// fixtureRequest deliberately supplies the exact evaluator-derived local and source times.
		// Rejection therefore proves the provider enforces visibility rather than merely noticing a
		// stale time echo.
		request := fixtureRequest(provider, "clip-b", at)
		frame, frameErr := provider.Frame(context.Background(), request)
		if frame != (Frame{}) || !errors.Is(frameErr, ErrRequestInvalid) {
			t.Fatalf("non-visible time %v accepted: frame=%+v err=%v", at, frame, frameErr)
		}
	}
}

func TestCompileAndFrameCancellationAreAtomic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if provider, err := Compile(ctx, fixtureDocument(), DefaultConfig()); provider != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("Compile canceled: provider=%v err=%v", provider, err)
	}
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	frame, err := provider.Frame(ctx, fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2}))
	if frame != (Frame{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("Frame canceled: frame=%+v err=%v", frame, err)
	}
	if provider, err := Compile(nil, fixtureDocument(), DefaultConfig()); provider != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context: provider=%v err=%v", provider, err)
	}
}

func TestCompileAggregateLimitsFailBeforePublication(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Limits)
	}{
		{"assets", func(value *Limits) { value.MaxExpressionAssets = 1 }},
		{"scopes", func(value *Limits) { value.MaxClipScopes = 1 }},
		{"paths", func(value *Limits) { value.MaxTotalPaths = 1 }},
		{"commands", func(value *Limits) { value.MaxTotalCommands = 7 }},
		{"edges", func(value *Limits) { value.MaxTotalEdges = 7 }},
		{"retained", func(value *Limits) {
			value.MaxRetainedBytes = 96
			value.Vector.MaxRetainedBytes = 96
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			test.mutate(&config.Limits)
			provider, err := Compile(context.Background(), fixtureDocument(), config)
			if provider != nil || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("provider=%v err=%v", provider, err)
			}
		})
	}
}

func TestInvalidVectorLimitsFailEvenWithoutEnabledExpressions(t *testing.T) {
	config := DefaultConfig()
	config.Limits.Vector.MaxWidth = documentv2.RasterViewportMaxDimension + 1
	empty := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "empty", Revision: 1}
	if provider, err := Compile(context.Background(), empty, config); provider != nil || !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
}

func TestProviderConcurrentFrameAndManifest(t *testing.T) {
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2})
	want, err := provider.Frame(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wantManifest := provider.Manifest()
	errorsChannel := make(chan error, 64)
	for worker := 0; worker < 64; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 32; iteration++ {
				got, frameErr := provider.Frame(context.Background(), request)
				if frameErr != nil {
					errorsChannel <- frameErr
					return
				}
				if got != want || !slices.Equal(provider.Manifest().Assets, wantManifest.Assets) {
					errorsChannel <- errors.New("non-deterministic concurrent result")
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func FuzzProviderFrameRequestNeverPanics(f *testing.F) {
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		f.Fatal(err)
	}
	valid := fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2})
	f.Add(int64(0), int64(0), int64(1), int64(2))
	f.Add(int64(-1), int64(100), int64(2), int64(1))
	f.Fuzz(func(t *testing.T, assetIndex, clipIndex, value, rate int64) {
		request := valid
		request.AssetIndex = int(assetIndex)
		request.ClipIndex = int(clipIndex)
		request.TimelineTime = documentv2.RationalTime{Value: value, Rate: rate}
		frame, frameErr := provider.Frame(context.Background(), request)
		if frameErr != nil && frame != (Frame{}) {
			t.Fatalf("partial Frame on error: %+v", frame)
		}
	})
}

func FuzzCompileNeverPublishesPartialProvider(f *testing.F) {
	f.Add(uint32(4), uint32(3), false, false)
	f.Add(uint32(0), uint32(65_537), true, false)
	f.Add(uint32(1), uint32(1), false, true)
	f.Fuzz(func(t *testing.T, width, height uint32, legacyProfile, disabled bool) {
		document := fixtureDocument()
		document.Assets[0].Vector.Viewport.WidthPixels = width
		document.Assets[0].Vector.Viewport.HeightPixels = height
		document.Timelines[0].Tracks[0].Clips[0].Disabled = disabled
		document.Timelines[0].Tracks[0].Clips[2].Disabled = disabled
		if legacyProfile {
			document.Assets[0].Vector.Profile = documentv2.VectorProfilePathV1
			document.Assets[0].Vector.Viewport = nil
		}
		provider, err := Compile(context.Background(), document, DefaultConfig())
		if err != nil && provider != nil {
			t.Fatal("Compile returned a partial Provider")
		}
	})
}

func fixtureRequest(provider *Provider, clipID documentv2.ID, timelineTime documentv2.RationalTime) Request {
	allowed := provider.clips[clipScopeKey{timeline: "timeline", track: "vector-track", clip: clipID}]
	entry := provider.assets[allowed.asset.id]
	local, _ := evaluator.ClipLocalTime(allowed.clip, timelineTime)
	source, _ := evaluator.SourceTime(allowed.clip, timelineTime)
	return Request{
		ContractVersion: ContractVersion, DocumentHash: provider.documentHash,
		TimelineID: "timeline", TimelineIndex: allowed.timelineIndex,
		TrackID: "vector-track", TrackIndex: allowed.trackIndex, TrackKind: documentv2.TrackKindVector,
		ClipID: clipID, ClipIndex: allowed.clipIndex,
		AssetID: allowed.asset.id, AssetIndex: allowed.asset.index, AssetKind: documentv2.AssetKindVector,
		AssetSemanticHashProfile: vectorraster.SourceHashProfileV1,
		AssetSemanticHash:        entry.receipt.AssetSemanticHash, RasterViewport: entry.receipt.RasterViewport,
		TimelineTime: timelineTime, LocalTime: local, SourceTime: source,
		SourceProfile: SourceProfileV1, SelectionProfile: SelectionProfileV1,
		ExpressionProfile: documentv2.VectorProfilePathViewportPXV2,
		EvaluatorProfile:  documentv2.EvaluatorVectorPathViewportPXV2,
		RasterProfile:     vectorraster.ProfileV1, PaintProfile: entry.receipt.PaintProfile,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
}

func fixtureDocument() documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "expression-document", Revision: 7,
		Assets: []documentv2.Asset{
			vectorAsset("vector-b", "path-b", documentv2.ColorRGBA16{R: 65_535, A: 65_535}),
			vectorAsset("vector-a", "path-a", documentv2.ColorRGBA16{G: 65_535, A: 32_768}),
		},
		Timelines: []documentv2.Timeline{{
			ID: "timeline", Tracks: []documentv2.Track{{
				ID: "vector-track", Kind: documentv2.TrackKindVector,
				Clips: []documentv2.Clip{
					{ID: "clip-b", AssetID: "vector-b", TimelineRange: timeRange(0, 2), SourceRange: timeRange(0, 2)},
					{ID: "clip-a", AssetID: "vector-a", TimelineRange: timeRange(2, 2), SourceRange: timeRange(0, 2)},
					{ID: "clip-b-repeat", AssetID: "vector-b", TimelineRange: timeRange(4, 2), SourceRange: timeRange(0, 2)},
				},
			}},
		}},
	}
}

func vectorAsset(id, pathID documentv2.ID, fill documentv2.ColorRGBA16) documentv2.Asset {
	point := func(x, y int64) documentv2.Point {
		return documentv2.Point{X: rational(x), Y: rational(y)}
	}
	return documentv2.Asset{
		ID: id, Kind: documentv2.AssetKindVector,
		Vector: &documentv2.VectorSource{
			Profile:  documentv2.VectorProfilePathViewportPXV2,
			ViewBox:  documentv2.Rect{X: rational(0), Y: rational(0), Width: rational(4), Height: rational(3)},
			Viewport: &documentv2.RasterViewport{WidthPixels: 4, HeightPixels: 3},
			Paths: []documentv2.VectorPath{{
				ID: pathID, FillRule: documentv2.VectorFillNonZero, Fill: &fill,
				Commands: []documentv2.VectorCommand{
					{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{point(0, 0)}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(4, 0)}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(4, 3)}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{point(0, 3)}},
					{Op: documentv2.VectorClose},
				},
			}},
		},
	}
}

func rational(value int64) documentv2.Rational {
	return documentv2.Rational{Numerator: value, Denominator: 1}
}

func timeRange(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{
		Start:    documentv2.RationalTime{Value: start, Rate: 1},
		Duration: documentv2.RationalTime{Value: duration, Rate: 1},
	}
}

func shaA() string { return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }
