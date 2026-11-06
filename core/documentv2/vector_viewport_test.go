package documentv2

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestVectorViewportProfileValidationAndHardBounds(t *testing.T) {
	assertNoDiagnostics(t, Validate(vectorViewportDocument()))

	boundary := vectorViewportDocument()
	boundary.Assets[0].Vector.Viewport = &RasterViewport{WidthPixels: 1, HeightPixels: RasterViewportMaxDimension}
	assertNoDiagnostics(t, Validate(boundary))

	tests := []struct {
		name   string
		path   string
		mutate func(*Document)
	}{
		{
			name: "v1 forbids viewport", path: "/assets/0/vector/viewport",
			mutate: func(document *Document) { document.Assets[0].Vector.Profile = VectorProfilePathV1 },
		},
		{
			name: "v2 requires viewport", path: "/assets/0/vector/viewport",
			mutate: func(document *Document) { document.Assets[0].Vector.Viewport = nil },
		},
		{
			name: "unknown profile", path: "/assets/0/vector/profile",
			mutate: func(document *Document) { document.Assets[0].Vector.Profile = "cutly.vector.unknown.v9" },
		},
		{
			name: "zero width", path: "/assets/0/vector/viewport/width_pixels",
			mutate: func(document *Document) { document.Assets[0].Vector.Viewport.WidthPixels = 0 },
		},
		{
			name: "width above hard bound", path: "/assets/0/vector/viewport/width_pixels",
			mutate: func(document *Document) {
				document.Assets[0].Vector.Viewport.WidthPixels = RasterViewportMaxDimension + 1
			},
		},
		{
			name: "zero height", path: "/assets/0/vector/viewport/height_pixels",
			mutate: func(document *Document) { document.Assets[0].Vector.Viewport.HeightPixels = 0 },
		},
		{
			name: "height above hard bound", path: "/assets/0/vector/viewport/height_pixels",
			mutate: func(document *Document) {
				document.Assets[0].Vector.Viewport.HeightPixels = RasterViewportMaxDimension + 1
			},
		},
		{
			name: "fill is mandatory", path: "/assets/0/vector/paths/0/fill",
			mutate: func(document *Document) { document.Assets[0].Vector.Paths[0].Fill = nil },
		},
		{
			name: "stroke is rejected", path: "/assets/0/vector/paths/0/stroke",
			mutate: func(document *Document) {
				document.Assets[0].Vector.Paths[0].Stroke = &VectorStroke{
					Color: ColorRGBA16{R: 1, G: 2, B: 3, A: 65_535}, Width: rationalOne(),
					LineCap: VectorCapButt, LineJoin: VectorJoinMiter,
					MiterLimit: Rational{Numerator: 4, Denominator: 1},
				}
			},
		},
		{
			name: "cubic is rejected", path: "/assets/0/vector/paths/0/commands/1/op",
			mutate: func(document *Document) {
				document.Assets[0].Vector.Paths[0].Commands[1] = VectorCommand{
					Op: VectorCubicTo,
					Points: []Point{
						{X: Rational{Numerator: 30, Denominator: 1}, Y: Rational{Numerator: 10, Denominator: 1}},
						{X: Rational{Numerator: 50, Denominator: 1}, Y: Rational{Numerator: 20, Denominator: 1}},
						{X: Rational{Numerator: 70, Denominator: 1}, Y: Rational{Numerator: 30, Denominator: 1}},
					},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := vectorViewportDocument()
			test.mutate(&document)
			assertDiagnostic(t, Validate(document), CodeAssetInvalid, test.path)
		})
	}
}

func TestVectorViewportStrictDecodeRejectsUnknownFractionalAndOverflowDimensions(t *testing.T) {
	canonical, err := CanonicalJSON(vectorViewportDocument())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(canonical); err != nil {
		t.Fatalf("valid viewport document rejected: %v", err)
	}
	tests := []struct {
		name string
		from []byte
		to   []byte
	}{
		{name: "v1 profile with viewport", from: []byte(`"cutly.vector.path-viewport-px.v2"`), to: []byte(`"cutly.vector.path.v1"`)},
		{name: "unknown viewport field", from: []byte(`"width_pixels":320`), to: []byte(`"unknown":true,"width_pixels":320`)},
		{name: "fractional width", from: []byte(`"width_pixels":320`), to: []byte(`"width_pixels":320.5`)},
		{name: "negative width", from: []byte(`"width_pixels":320`), to: []byte(`"width_pixels":-1`)},
		{name: "hard-bound overflow", from: []byte(`"width_pixels":320`), to: []byte(`"width_pixels":4294967295`)},
		{name: "uint32 overflow", from: []byte(`"width_pixels":320`), to: []byte(`"width_pixels":4294967296`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hostile := bytes.Replace(canonical, test.from, test.to, 1)
			if bytes.Equal(hostile, canonical) {
				t.Fatalf("test replacement did not match canonical JSON: %s", canonical)
			}
			if _, err := Decode(hostile); !errors.Is(err, ErrDocumentInvalid) {
				t.Fatalf("Decode error = %v, want ErrDocumentInvalid", err)
			}
		})
	}
}

func TestVectorViewportCapabilityIsProfileSpecificAndPreflighted(t *testing.T) {
	document := vectorViewportDocument()
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("requirements diagnostics: %+v", diagnostics)
	}
	if !slices.Equal(required.TrackKinds, []TrackKind{TrackKindVector}) ||
		!slices.Equal(required.AssetKinds, []AssetKind{AssetKindVector}) ||
		!slices.Equal(required.Features, []videoedit.CapabilityID{CapabilityVectorPathViewportPXV2}) {
		t.Fatalf("requirements = %+v", required)
	}

	offered := videoedit.BackendCapabilities{
		BackendID:  "vector-v1-only",
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKind(TrackKindVector)},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKind(AssetKindVector)},
		Features:   []videoedit.CapabilityID{CapabilityVectorPathV1},
	}
	rejected := PreflightCapabilities(document, offered)
	if len(rejected) != 1 || rejected[0].Code != CodeCapabilityFeature ||
		!strings.Contains(rejected[0].Message, string(CapabilityVectorPathViewportPXV2)) {
		t.Fatalf("v1-only backend did not fail closed: %+v", rejected)
	}
	offered.BackendID = "vector-viewport-v2"
	offered.Features = []videoedit.CapabilityID{CapabilityVectorPathViewportPXV2}
	if diagnostics := PreflightCapabilities(document, offered); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("matching backend rejected: %+v", diagnostics)
	}
}

func vectorViewportDocument() Document {
	zero := rationalZero()
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            "vector-viewport-document",
		Revision:      1,
		Assets: []Asset{{
			ID: "vector-viewport", Kind: AssetKindVector,
			Vector: &VectorSource{
				Profile: VectorProfilePathViewportPXV2,
				ViewBox: Rect{
					X: Rational{Numerator: 10, Denominator: 1}, Y: Rational{Numerator: -20, Denominator: 1},
					Width: Rational{Numerator: 100, Denominator: 1}, Height: Rational{Numerator: 50, Denominator: 1},
				},
				Viewport: &RasterViewport{WidthPixels: 320, HeightPixels: 180},
				Paths: []VectorPath{{
					ID: "open-triangle", FillRule: VectorFillNonZero,
					Fill: &ColorRGBA16{R: 65_535, G: 12_345, B: 0, A: 65_535},
					Commands: []VectorCommand{
						{Op: VectorMoveTo, Points: []Point{{X: Rational{Numerator: 10, Denominator: 1}, Y: Rational{Numerator: -20, Denominator: 1}}}},
						{Op: VectorLineTo, Points: []Point{{X: Rational{Numerator: 110, Denominator: 1}, Y: Rational{Numerator: -20, Denominator: 1}}}},
						{Op: VectorLineTo, Points: []Point{{X: Rational{Numerator: 60, Denominator: 1}, Y: Rational{Numerator: 30, Denominator: 1}}}},
					},
				}, {
					ID: "closed-overlay", FillRule: VectorFillEvenOdd,
					Fill: &ColorRGBA16{R: 0, G: 40_000, B: 65_535, A: 32_768},
					Commands: []VectorCommand{
						{Op: VectorMoveTo, Points: []Point{{X: Rational{Numerator: 40, Denominator: 1}, Y: zero}}},
						{Op: VectorLineTo, Points: []Point{{X: Rational{Numerator: 80, Denominator: 1}, Y: zero}}},
						{Op: VectorLineTo, Points: []Point{{X: Rational{Numerator: 80, Denominator: 1}, Y: Rational{Numerator: 20, Denominator: 1}}}},
						{Op: VectorClose},
					},
				}},
			},
		}},
		Timelines: []Timeline{{
			ID: "timeline",
			Tracks: []Track{{
				ID: "vector-track", Kind: TrackKindVector,
				Clips: []Clip{{
					ID: "vector-clip", AssetID: "vector-viewport",
					TimelineRange: testRange(0, 1), SourceRange: testRange(0, 1),
				}},
			}},
		}},
	}
}
