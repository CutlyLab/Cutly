package vectorraster

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestRasterizeFullRectangleAndReceipt(t *testing.T) {
	source := testSource(2, 2, testRectPath("full", q(0, 1), q(0, 1), q(2, 1), q(2, 1), testColor(65_535, 65_535, 65_535, 65_535), true))
	surface, receipt, err := Rasterize(context.Background(), source, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			if got := mustPixel(t, surface, x, y); got != (cpucompositor.Pixel{R: 65_535, G: 65_535, B: 65_535, A: 65_535}) {
				t.Fatalf("pixel(%d,%d) = %+v", x, y, got)
			}
		}
	}
	hash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != ReceiptSchemaVersion || receipt.Profile != ReceiptProfileV1 ||
		receipt.RasterProfile != ProfileV1 || receipt.SourceProfile != documentv2.VectorProfilePathViewportPXV2 ||
		receipt.PixelHash != hash || receipt.WidthPixels != 2 || receipt.HeightPixels != 2 ||
		receipt.PathCount != 1 || receipt.CommandCount != 5 || receipt.ConceptualEdgeCount != 4 ||
		receipt.EffectiveEdgeCount != 2 || receipt.SampleCount != 2*2*64 || receipt.WorkUnits == 0 ||
		receipt.CanonicalSourceBytes == 0 || receipt.WorkingBytes == 0 || receipt.RetainedBytes != 2*2*8 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if _, err := json.Marshal(receipt); err != nil {
		t.Fatalf("Receipt is not serializable: %v", err)
	}
}

func TestRasterizeMeetLetterboxAndViewportClip(t *testing.T) {
	letterbox := testSource(4, 2, testRectPath("square", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(65_535, 0, 0, 65_535), true))
	letterbox.ViewBox.Width = q(1, 1)
	letterbox.ViewBox.Height = q(1, 1)
	surface, _, err := Rasterize(context.Background(), letterbox, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 2; y++ {
		for x, wantAlpha := range []uint16{0, 65_535, 65_535, 0} {
			if got := mustPixel(t, surface, x, y); got.A != wantAlpha {
				t.Fatalf("letterbox pixel(%d,%d) alpha = %d, want %d", x, y, got.A, wantAlpha)
			}
		}
	}

	clipped := testSource(2, 2, testRectPath("overscan", q(-1, 1), q(-1, 1), q(3, 1), q(3, 1), testColor(0, 65_535, 0, 65_535), true))
	surface, _, err = Rasterize(context.Background(), clipped, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			if got := mustPixel(t, surface, x, y); got != (cpucompositor.Pixel{G: 65_535, A: 65_535}) {
				t.Fatalf("clipped pixel(%d,%d) = %+v", x, y, got)
			}
		}
	}
}

func TestRasterizeMeetWithFractionalNonzeroViewBoxOriginMatchesOracle(t *testing.T) {
	source := testSource(4, 4, testRectPath(
		"fractional-viewbox", q(10, 3), q(-7, 5), q(4, 1), q(-16, 15),
		testColor(65_535, 0, 0, 65_535), true,
	))
	source.ViewBox = documentv2.Rect{
		X: q(10, 3), Y: q(-7, 5), Width: q(2, 3), Height: q(1, 3),
	}
	assertMatchesOracle(t, source)
	surface, _, err := Rasterize(context.Background(), source, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 4; y++ {
		wantAlpha := uint16(0)
		if y == 1 || y == 2 {
			wantAlpha = 65_535
		}
		for x := 0; x < 4; x++ {
			if got := mustPixel(t, surface, x, y); got.A != wantAlpha {
				t.Fatalf("fractional meet pixel(%d,%d) alpha = %d, want %d", x, y, got.A, wantAlpha)
			}
		}
	}
}

func TestRasterizeBoundaryAndNearestEvenCoverage(t *testing.T) {
	boundary := testSource(1, 1, testRectPath("boundary", q(1, 16), q(1, 16), q(9, 16), q(9, 16), testColor(65_535, 65_535, 65_535, 65_535), true))
	surface, _, err := Rasterize(context.Background(), boundary, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := mustPixel(t, surface, 0, 0); got != (cpucompositor.Pixel{R: 16_384, G: 16_384, B: 16_384, A: 16_384}) {
		t.Fatalf("top/left inclusive, bottom/right exclusive pixel = %+v", got)
	}

	half := testSource(1, 1, testRectPath("half", q(0, 1), q(0, 1), q(1, 2), q(1, 1), testColor(65_535, 65_535, 65_535, 65_535), true))
	surface, _, err = Rasterize(context.Background(), half, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := mustPixel(t, surface, 0, 0); got != (cpucompositor.Pixel{R: 32_768, G: 32_768, B: 32_768, A: 32_768}) {
		t.Fatalf("half-coverage nearest-even pixel = %+v", got)
	}

	for _, test := range []struct {
		name      string
		alpha     uint16
		wantAlpha uint16
	}{
		{name: "tie-down-to-even", alpha: 32, wantAlpha: 0},
		{name: "tie-up-to-even", alpha: 96, wantAlpha: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := testSource(1, 1, testRectPath(
				test.name, q(0, 1), q(0, 1), q(1, 8), q(1, 8),
				testColor(0, 0, 0, test.alpha), true,
			))
			surface, _, err := Rasterize(context.Background(), source, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if got := mustPixel(t, surface, 0, 0); got.A != test.wantAlpha {
				t.Fatalf("one-sample coverage alpha = %d, want %d", got.A, test.wantAlpha)
			}
		})
	}
}

func TestRasterizeOpenClosureHolesAndSelfIntersection(t *testing.T) {
	open := testSource(3, 3, documentv2.VectorPath{
		ID: "open", FillRule: documentv2.VectorFillNonZero, Fill: testColor(65_535, 0, 0, 65_535),
		Commands: []documentv2.VectorCommand{
			move(q(0, 1), q(0, 1)), line(q(3, 1), q(0, 1)), line(q(0, 1), q(3, 1)),
			move(q(3, 1), q(3, 1)), line(q(2, 1), q(3, 1)), line(q(3, 1), q(2, 1)),
		},
	})
	closed := open
	closed.Paths = []documentv2.VectorPath{clonePath(open.Paths[0])}
	closed.Paths[0].ID = "closed"
	closed.Paths[0].Commands = []documentv2.VectorCommand{
		move(q(0, 1), q(0, 1)), line(q(3, 1), q(0, 1)), line(q(0, 1), q(3, 1)), closeCommand(),
		move(q(3, 1), q(3, 1)), line(q(2, 1), q(3, 1)), line(q(3, 1), q(2, 1)), closeCommand(),
	}
	openSurface, _, err := Rasterize(context.Background(), open, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	closedSurface, _, err := Rasterize(context.Background(), closed, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertSurfaceEqual(t, openSurface, closedSurface)

	outer := rectangleCommands(q(0, 1), q(0, 1), q(3, 1), q(3, 1), true)
	inner := rectangleCommands(q(1, 1), q(1, 1), q(2, 1), q(2, 1), true)
	holePath := documentv2.VectorPath{
		ID: "hole", FillRule: documentv2.VectorFillEvenOdd, Fill: testColor(0, 65_535, 0, 65_535),
		Commands: append(outer, inner...),
	}
	hole := testSource(3, 3, holePath)
	holeSurface, _, err := Rasterize(context.Background(), hole, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := mustPixel(t, holeSurface, 1, 1); got != (cpucompositor.Pixel{}) {
		t.Fatalf("evenodd center = %+v, want transparent", got)
	}
	hole.Paths[0].FillRule = documentv2.VectorFillNonZero
	nonzeroSurface, _, err := Rasterize(context.Background(), hole, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := mustPixel(t, nonzeroSurface, 1, 1); got.A != 65_535 {
		t.Fatalf("nonzero same-orientation center = %+v, want filled", got)
	}

	bowtie := testSource(3, 3, documentv2.VectorPath{
		ID: "bowtie", FillRule: documentv2.VectorFillEvenOdd, Fill: testColor(0, 0, 65_535, 50_000),
		Commands: []documentv2.VectorCommand{
			move(q(0, 1), q(0, 1)), line(q(3, 1), q(3, 1)), line(q(0, 1), q(3, 1)), line(q(3, 1), q(0, 1)), closeCommand(),
		},
	})
	assertMatchesOracle(t, bowtie)
}

func TestRasterizePathOrderAndAlpha(t *testing.T) {
	red := testRectPath("red", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(65_535, 0, 0, 32_768), true)
	blue := testRectPath("blue", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(0, 0, 65_535, 32_768), true)
	redThenBlue := testSource(1, 1, red, blue)
	blueThenRed := testSource(1, 1, blue, red)
	first, firstReceipt, err := Rasterize(context.Background(), redThenBlue, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, secondReceipt, err := Rasterize(context.Background(), blueThenRed, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if firstReceipt.PixelHash == secondReceipt.PixelHash || mustPixel(t, first, 0, 0) == mustPixel(t, second, 0, 0) {
		t.Fatalf("path order was not observable: first=%+v second=%+v", mustPixel(t, first, 0, 0), mustPixel(t, second, 0, 0))
	}
	assertMatchesOracle(t, redThenBlue)
	assertMatchesOracle(t, blueThenRed)
}

func TestRasterizeRejectsHostileSourcesWithoutPartialResult(t *testing.T) {
	base := testSource(2, 2, testRectPath("full", q(0, 1), q(0, 1), q(2, 1), q(2, 1), testColor(1, 2, 3, 4), true))
	tests := []struct {
		name   string
		cause  error
		mutate func(*documentv2.VectorSource)
	}{
		{"v1", ErrProfileUnsupported, func(value *documentv2.VectorSource) { value.Profile = documentv2.VectorProfilePathV1 }},
		{"nil viewport", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Viewport = nil }},
		{"stroke", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Stroke = &documentv2.VectorStroke{} }},
		{"cubic", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Commands[1].Op = documentv2.VectorCubicTo }},
		{"zero denominator", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Commands[0].Points[0].X.Denominator = 0 }},
		{"noncanonical rational", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Commands[0].Points[0].X = qRaw(2, 2) }},
		{"close empty payload", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Commands[4].Points = []documentv2.Point{} }},
		{"line without move", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Commands[0].Op = documentv2.VectorLineTo }},
		{"nil fill", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Fill = nil }},
		{"empty paths", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths = nil }},
		{"empty commands", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths[0].Commands = nil }},
		{"duplicate id", ErrSourceInvalid, func(value *documentv2.VectorSource) { value.Paths = append(value.Paths, clonePath(value.Paths[0])) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := cloneSource(base)
			test.mutate(&source)
			surface, receipt, err := Rasterize(context.Background(), source, DefaultLimits())
			if !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, want %v", err, test.cause)
			}
			if surface != nil || receipt != (Receipt{}) {
				t.Fatalf("partial result = (%v, %+v)", surface, receipt)
			}
		})
	}
}

func TestRasterizeResourceLimitsAndCheckedWork(t *testing.T) {
	source := testSource(2, 2,
		testRectPath("one", q(0, 1), q(0, 1), q(2, 1), q(2, 1), testColor(65_535, 0, 0, 65_535), true),
		testRectPath("two", q(0, 1), q(0, 1), q(2, 1), q(2, 1), testColor(0, 0, 65_535, 32_768), true),
	)
	_, baseline, err := Rasterize(context.Background(), source, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		cause  error
		mutate func(*Limits)
	}{
		{"dimensions", ErrResourceLimit, func(value *Limits) { value.MaxWidth = 1 }},
		{"pixels", ErrResourceLimit, func(value *Limits) { value.MaxPixels = 3 }},
		{"paths", ErrResourceLimit, func(value *Limits) { value.MaxPaths = 1 }},
		{"commands", ErrResourceLimit, func(value *Limits) { value.MaxCommands = 9 }},
		{"edges", ErrResourceLimit, func(value *Limits) { value.MaxEdges = 7 }},
		{"canonical", ErrResourceLimit, func(value *Limits) { value.MaxCanonicalSourceBytes = baseline.CanonicalSourceBytes - 1 }},
		{"working", ErrResourceLimit, func(value *Limits) { value.MaxWorkingBytes = baseline.WorkingBytes - 1 }},
		{"retained", ErrResourceLimit, func(value *Limits) { value.MaxRetainedBytes = baseline.RetainedBytes - 1 }},
		{"work", ErrResourceLimit, func(value *Limits) { value.MaxWorkUnits = baseline.WorkUnits - 1 }},
		{"exact", ErrExactLimit, func(value *Limits) { value.MaxExactBits = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.mutate(&limits)
			surface, receipt, err := Rasterize(context.Background(), source, limits)
			if !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, want %v", err, test.cause)
			}
			if surface != nil || receipt != (Receipt{}) {
				t.Fatalf("partial result = (%v, %+v)", surface, receipt)
			}
		})
	}
	if baseline.SampleCount != 2*2*64*2 {
		t.Fatalf("SampleCount = %d, want per-path sample count", baseline.SampleCount)
	}
}

func TestRasterizeWorkingBudgetPrecedesPathIDIndex(t *testing.T) {
	const pathCount = 4097
	paths := make([]documentv2.VectorPath, pathCount)
	fill := testColor(1, 2, 3, 4)
	for index := range paths {
		paths[index] = documentv2.VectorPath{
			ID:       documentv2.ID("path-" + strconv.Itoa(index)),
			FillRule: documentv2.VectorFillNonZero,
			Fill:     fill,
			Commands: []documentv2.VectorCommand{move(q(0, 1), q(0, 1))},
		}
	}
	// Keep the source invalid too, but only at the final ID. A resource preflight must reject the
	// tiny working budget before constructing and populating a path-sized duplicate-ID map.
	paths[len(paths)-1].ID = paths[0].ID
	source := testSource(1, 1, paths...)
	limits := DefaultLimits()
	limits.MaxPaths = pathCount
	limits.MaxWorkingBytes = 1
	surface, receipt, err := Rasterize(context.Background(), source, limits)
	if !errors.Is(err, ErrResourceLimit) || surface != nil || receipt != (Receipt{}) {
		t.Fatalf("working preflight = surface %v receipt %+v err %v", surface, receipt, err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeResourceLimit || typed.Path != "source/working_bytes" {
		t.Fatalf("working preflight error = %#v", err)
	}
}

func TestRasterizePathCancellationDuringCrossingClassification(t *testing.T) {
	edge := func(x1, y1, x2, y2 int64, winding int64, ordinal int) rasterEdge {
		var result rasterEdge
		result.x1.SetInt64(x1)
		result.y1.SetInt64(y1)
		result.x2.SetInt64(x2)
		result.y2.SetInt64(y2)
		result.startRow = 0
		result.endRow = 8
		result.winding = winding
		result.ordinal = ordinal
		return result
	}
	path := rasterPath{
		rule: documentv2.VectorFillNonZero,
		edges: []rasterEdge{
			edge(0, 0, 0, 1, 1, 0),
			edge(1, 1, 1, 0, -1, 1),
		},
	}
	coverage := make([]uint8, 1)
	err := rasterizePath(&countingCancelContext{remaining: 2}, exactContext{maxBits: 512}, &path, 1, 1, coverage)
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("crossing cancellation error = %v", err)
	}
	if coverage[0] != 0 {
		t.Fatalf("crossing cancellation published partial coverage %d", coverage[0])
	}
}

func TestCanonicalSourceHashCancellation(t *testing.T) {
	payload := make([]byte, canonicalHashChunkBytes*3)
	hash, err := hashCanonicalSource(&countingCancelContext{remaining: 2}, payload)
	if hash != "" || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canonical hash cancellation = hash %q err %v", hash, err)
	}
}

func TestSurfaceConstructionErrorPreservesAllocationIdentity(t *testing.T) {
	err := surfaceConstructionError(errors.Join(errors.New("wrapped"), cpucompositor.ErrAllocationFailed))
	if !errors.Is(err, ErrAllocationFailed) || !errors.Is(err, cpucompositor.ErrAllocationFailed) {
		t.Fatalf("allocation identity = %v", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeAllocationFailed || typed.Path != "surface" {
		t.Fatalf("allocation error = %#v", err)
	}

	err = surfaceConstructionError(cpucompositor.ErrInvalidPixel)
	if !errors.Is(err, ErrSurfaceFailed) || !errors.Is(err, cpucompositor.ErrInvalidPixel) {
		t.Fatalf("surface identity = %v", err)
	}
	if !errors.As(err, &typed) || typed.Code != CodeSurfaceFailed || typed.Path != "surface" {
		t.Fatalf("surface error = %#v", err)
	}
}

func TestRasterizeCancellationNoPartialResult(t *testing.T) {
	source := testSource(32, 32, testRectPath("full", q(0, 1), q(0, 1), q(32, 1), q(32, 1), testColor(65_535, 0, 0, 65_535), true))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{canceled, &countingCancelContext{remaining: 20}} {
		surface, receipt, err := Rasterize(ctx, source, DefaultLimits())
		if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
		if surface != nil || receipt != (Receipt{}) {
			t.Fatalf("cancellation returned partial result")
		}
	}
}

func TestRasterizeResultDoesNotAliasInputAndConcurrentCallsAgree(t *testing.T) {
	source := testSource(4, 4, testRectPath("full", q(0, 1), q(0, 1), q(4, 1), q(4, 1), testColor(65_535, 12_345, 0, 50_000), true))
	surface, receipt, err := Rasterize(context.Background(), source, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	wantHash := receipt.PixelHash
	source.Viewport.WidthPixels = 1
	source.Paths[0].Fill.R = 0
	source.Paths[0].Commands[1].Points[0].X = q(1, 4)
	gotHash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Fatalf("input mutation changed frozen Surface: %s != %s", gotHash, wantHash)
	}

	concurrentSource := testSource(8, 8, testRectPath("full", q(0, 1), q(0, 1), q(8, 1), q(8, 1), testColor(10_000, 20_000, 30_000, 40_000), true))
	const workers = 8
	hashes := make(chan string, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, got, callErr := Rasterize(context.Background(), concurrentSource, DefaultLimits())
			if callErr != nil {
				errorsChannel <- callErr
				return
			}
			hashes <- got.PixelHash
		}()
	}
	wait.Wait()
	close(hashes)
	close(errorsChannel)
	for callErr := range errorsChannel {
		t.Fatal(callErr)
	}
	var first string
	for hash := range hashes {
		if first == "" {
			first = hash
		} else if hash != first {
			t.Fatalf("concurrent hash = %s, want %s", hash, first)
		}
	}
}

type countingCancelContext struct {
	remaining int64
}

func (value *countingCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *countingCancelContext) Done() <-chan struct{}       { return nil }
func (value *countingCancelContext) Value(any) any               { return nil }
func (value *countingCancelContext) Err() error {
	if atomic.AddInt64(&value.remaining, -1) <= 0 {
		return context.Canceled
	}
	return nil
}

func testSource(width, height uint32, paths ...documentv2.VectorPath) documentv2.VectorSource {
	return documentv2.VectorSource{
		Profile:  documentv2.VectorProfilePathViewportPXV2,
		ViewBox:  documentv2.Rect{X: q(0, 1), Y: q(0, 1), Width: q(int64(width), 1), Height: q(int64(height), 1)},
		Viewport: &documentv2.RasterViewport{WidthPixels: width, HeightPixels: height},
		Paths:    paths,
	}
}

func testRectPath(id string, left, top, right, bottom documentv2.Rational, fill *documentv2.ColorRGBA16, closed bool) documentv2.VectorPath {
	return documentv2.VectorPath{
		ID: documentv2.ID(id), FillRule: documentv2.VectorFillNonZero, Fill: fill,
		Commands: rectangleCommands(left, top, right, bottom, closed),
	}
}

func rectangleCommands(left, top, right, bottom documentv2.Rational, closed bool) []documentv2.VectorCommand {
	result := []documentv2.VectorCommand{
		move(left, top), line(right, top), line(right, bottom), line(left, bottom),
	}
	if closed {
		result = append(result, closeCommand())
	}
	return result
}

func move(x, y documentv2.Rational) documentv2.VectorCommand {
	return documentv2.VectorCommand{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: x, Y: y}}}
}

func line(x, y documentv2.Rational) documentv2.VectorCommand {
	return documentv2.VectorCommand{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: x, Y: y}}}
}

func closeCommand() documentv2.VectorCommand {
	return documentv2.VectorCommand{Op: documentv2.VectorClose}
}

func q(numerator, denominator int64) documentv2.Rational {
	value := new(bigRatForTest).canonical(numerator, denominator)
	return documentv2.Rational{Numerator: value.numerator, Denominator: value.denominator}
}

func qRaw(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

type canonicalTestRational struct{ numerator, denominator int64 }
type bigRatForTest canonicalTestRational

func (*bigRatForTest) canonical(numerator, denominator int64) canonicalTestRational {
	if denominator <= 0 {
		panic("test rational denominator")
	}
	if numerator == 0 {
		return canonicalTestRational{0, 1}
	}
	a, b := numerator, denominator
	if a < 0 {
		a = -a
	}
	for b != 0 {
		a, b = b, a%b
	}
	return canonicalTestRational{numerator / a, denominator / a}
}

func testColor(r, g, b, a uint16) *documentv2.ColorRGBA16 {
	return &documentv2.ColorRGBA16{R: r, G: g, B: b, A: a}
}

func mustPixel(t *testing.T, surface *cpucompositor.Surface, x, y int) cpucompositor.Pixel {
	t.Helper()
	pixel, err := surface.Pixel(x, y)
	if err != nil {
		t.Fatal(err)
	}
	return pixel
}

func assertSurfaceEqual(t *testing.T, left, right *cpucompositor.Surface) {
	t.Helper()
	leftHash, err := left.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := right.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if left.Width() != right.Width() || left.Height() != right.Height() || leftHash != rightHash {
		t.Fatalf("surfaces differ: %dx%d %s vs %dx%d %s", left.Width(), left.Height(), leftHash, right.Width(), right.Height(), rightHash)
	}
}

func cloneSource(value documentv2.VectorSource) documentv2.VectorSource {
	result := value
	if value.Viewport != nil {
		viewport := *value.Viewport
		result.Viewport = &viewport
	}
	result.Paths = make([]documentv2.VectorPath, len(value.Paths))
	for index := range value.Paths {
		result.Paths[index] = clonePath(value.Paths[index])
	}
	return result
}

func clonePath(value documentv2.VectorPath) documentv2.VectorPath {
	result := value
	if value.Fill != nil {
		fill := *value.Fill
		result.Fill = &fill
	}
	if value.Stroke != nil {
		stroke := *value.Stroke
		result.Stroke = &stroke
	}
	result.Commands = make([]documentv2.VectorCommand, len(value.Commands))
	for index, command := range value.Commands {
		result.Commands[index] = command
		result.Commands[index].Points = append([]documentv2.Point(nil), command.Points...)
	}
	return result
}
