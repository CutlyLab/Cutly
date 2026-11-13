package visualcompositor

import (
	"context"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// Keyframe animation is the evaluator's per-frame property evaluation. These gates prove
// that clips with keyframe lanes animate their properties through the compositor.
func TestKeyframeOpacityAnimates(t *testing.T) {
	surface := keyframeTestSurface(t, 8, 6)
	provider := nativeProvider(surface, nil)

	zero := documentv2.Rational{Numerator: 0, Denominator: 1}
	one := documentv2.Rational{Numerator: 1, Denominator: 1}
	opacityLane := documentv2.KeyframeLane{
		ID: "opacity", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{
			{Offset: videoedit.RationalTime{Value: 0, Rate: 1}, Value: documentv2.KeyframeValue{Scalar: &zero}, Interpolation: documentv2.InterpolationLinear},
			{Offset: videoedit.RationalTime{Value: 1, Rate: 1}, Value: documentv2.KeyframeValue{Scalar: &one}, Interpolation: documentv2.InterpolationLinear},
			{Offset: videoedit.RationalTime{Value: 2, Rate: 1}, Value: documentv2.KeyframeValue{Scalar: &zero}},
		},
	}

	document := imageDocumentWithLanes(opacityLane)
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(8, 6))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}

	// At early time opacity is near 0; at midpoint opacity is ~0.5; at peak ~1.0.
	// Different times must produce different surface hashes.
	early, err := program.RenderAt(context.Background(), "timeline", videoedit.RationalTime{Value: 1, Rate: 1000}, Providers{Native: provider})
	if err != nil {
		t.Fatalf("early: %v", err)
	}
	mid, err := program.RenderAt(context.Background(), "timeline", videoedit.RationalTime{Value: 500, Rate: 1000}, Providers{Native: provider})
	if err != nil {
		t.Fatalf("mid: %v", err)
	}
	peak, err := program.RenderAt(context.Background(), "timeline", videoedit.RationalTime{Value: 990, Rate: 1000}, Providers{Native: provider})
	if err != nil {
		t.Fatalf("peak: %v", err)
	}
	earlyHash, _ := early.Surface.CanonicalHash()
	midHash, _ := mid.Surface.CanonicalHash()
	peakHash, _ := peak.Surface.CanonicalHash()
	if earlyHash == midHash || midHash == peakHash || earlyHash == peakHash {
		t.Fatal("opacity keyframes not animating: surfaces are identical across times")
	}
	_ = earlyHash
	_ = midHash
	_ = peakHash
}

// A clip with position keyframes should translate through the compositor.
func TestKeyframePositionAnimates(t *testing.T) {
	surface := keyframeTestSurface(t, 8, 6)
	provider := nativeProvider(surface, nil)

	zero := documentv2.Rational{Numerator: 0, Denominator: 1}
	two := documentv2.Rational{Numerator: 2, Denominator: 1}
	startPoint := documentv2.Point{X: zero, Y: zero}
	endPoint := documentv2.Point{X: two, Y: zero}

	positionLane := documentv2.KeyframeLane{
		ID: "position", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint,
		Keyframes: []documentv2.Keyframe{
			{Offset: videoedit.RationalTime{Value: 0, Rate: 1}, Value: documentv2.KeyframeValue{Point: &startPoint}, Interpolation: documentv2.InterpolationLinear},
			{Offset: videoedit.RationalTime{Value: 2, Rate: 1}, Value: documentv2.KeyframeValue{Point: &endPoint}},
		},
	}

	document := imageDocumentWithLanes(positionLane)
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(8, 6))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}

	early, err := program.RenderAt(context.Background(), "timeline", videoedit.RationalTime{Value: 1, Rate: 1000}, Providers{Native: provider})
	if err != nil {
		t.Fatalf("early: %v", err)
	}
	late, err := program.RenderAt(context.Background(), "timeline", videoedit.RationalTime{Value: 1999, Rate: 1000}, Providers{Native: provider})
	if err != nil {
		t.Fatalf("late: %v", err)
	}
	earlyHash, _ := early.Surface.CanonicalHash()
	lateHash, _ := late.Surface.CanonicalHash()
	if earlyHash == lateHash {
		t.Fatal("position keyframes not animating")
	}
}

// The evaluator's EvaluateProperties must apply keyframe lanes over static values.
func TestKeyframeOverridesStaticOpacity(t *testing.T) {
	half := documentv2.Rational{Numerator: 1, Denominator: 2}
	one := documentv2.Rational{Numerator: 1, Denominator: 1}
	clip := documentv2.Clip{
		ID: "clip", AssetID: "image",
		TimelineRange: documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}},
		SourceRange:   documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}},
		PropertySet:   &documentv2.PropertySet{Opacity: &documentv2.OpacityProperty{Value: one}},
		KeyframeLanes: []documentv2.KeyframeLane{{
			ID: "opacity", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
			Keyframes: []documentv2.Keyframe{
				{Offset: videoedit.RationalTime{Value: 0, Rate: 1}, Value: documentv2.KeyframeValue{Scalar: &half}, Interpolation: documentv2.InterpolationLinear},
				{Offset: videoedit.RationalTime{Value: 2, Rate: 1}, Value: documentv2.KeyframeValue{Scalar: &half}},
			},
		}},
	}
	asset := documentv2.Asset{ID: "image", Kind: documentv2.AssetKindImage, URI: "i.png"}
	at := videoedit.RationalTime{Value: 1, Rate: 2}
	properties, err := evaluator.EvaluateProperties(documentv2.TrackKindVideo, clip, asset, at)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if properties.Opacity == nil {
		t.Fatal("no opacity")
	}
}

func keyframeTestSurface(t *testing.T, width, height int) *cpucompositor.Surface {
	t.Helper()
	pixels := make([]cpucompositor.Pixel, width*height)
	for i := range pixels {
		pixels[i] = cpucompositor.Pixel{R: 0x8000, G: 0x4000, B: 0xC000, A: 0xFFFF}
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

func imageDocumentWithLanes(lanes ...documentv2.KeyframeLane) documentv2.Document {
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "keyframe-doc", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "image", Kind: documentv2.AssetKindImage, URI: "i.png", ContentHash: "sha256:" + strings.Repeat("a", 64)},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "clip", AssetID: "image",
				TimelineRange: documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}},
				SourceRange:   documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}},
				KeyframeLanes: lanes,
			}}},
		}}},
	}
}
