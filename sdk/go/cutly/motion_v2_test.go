package cutly

import (
	"errors"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func TestBuildMotionCompositionV2CompilesLayeredAnimation(t *testing.T) {
	white := ColorRGBA16{R: 65535, G: 65535, B: 65535, A: 65535}
	black := ColorRGBA16{A: 65535}
	one, zero := MotionRationalV2(1, 1), MotionRationalV2(0, 1)
	identity := MotionIdentityTransformV2()
	identity.Position = MotionPointV2(80, 1, 180, 1)
	document, err := BuildMotionCompositionV2(MotionCompositionV2{
		ID: "launch", Revision: 1, TimelineID: "main", Name: "Native launch", Width: 1920, Height: 1080, FramesPerSecond: 30, DurationFrames: 90,
		Assets: []Asset{
			MotionImageAssetV2("plate", "cas://plate", digestString('a')),
			MotionRectangleAssetV2("panel", 900, 420, black),
			MotionFontAssetV2("font", "cas://font", digestString('b')),
			MotionTextAssetV2(MotionTextAssetSpecV2{ID: "title", Text: "VIDEO EDITING FOR AGENTS.", FontAssetID: "font", FontSize: MotionRationalV2(118, 1), LineHeight: MotionRationalV2(126, 1), Width: 1200, Height: 420, Fill: white, Language: "en", Script: documentv2.TextScriptLatin, Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart}),
		},
		Layers: []MotionLayerV2{
			{ID: "plate-layer", TrackKind: documentv2.TrackKindVideo, AssetID: "plate", DurationFrames: 90, Transform: &MotionTransformV2{Position: MotionPointV2(0, 1, 0, 1), Scale: MotionPointV2(1, 1, 1, 1), RotationDegrees: zero, Anchor: MotionPointV2(1, 2, 1, 2)}, Animation: MotionAnimationV2{Scale: []MotionPointKeyframeV2{{Frame: 0, Value: MotionPointV2(1, 1, 1, 1), Interpolation: documentv2.InterpolationCubic, Easing: MotionEasePower2InOutV2()}, {Frame: 90, Value: MotionPointV2(26, 25, 26, 25)}}}},
			{ID: "panel-layer", TrackKind: documentv2.TrackKindVector, AssetID: "panel", StartFrame: 6, DurationFrames: 84, Transform: &identity},
			{ID: "title-layer", TrackKind: documentv2.TrackKindText, AssetID: "title", StartFrame: 6, DurationFrames: 84, Transform: &identity, Opacity: &one, Animation: MotionAnimationV2{Position: []MotionPointKeyframeV2{{Frame: 0, Value: MotionPointV2(-90, 1, 180, 1), Interpolation: documentv2.InterpolationCubic, Easing: MotionEaseExpoOutV2()}, {Frame: 18, Value: MotionPointV2(80, 1, 180, 1)}}, Opacity: []MotionScalarKeyframeV2{{Frame: 0, Value: zero, Interpolation: documentv2.InterpolationLinear}, {Frame: 12, Value: one}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := ValidateDocument(document); documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	if len(document.Timelines) != 1 || len(document.Timelines[0].Tracks) != 3 || len(document.Timelines[0].Tracks[2].Clips[0].KeyframeLanes) != 2 {
		t.Fatalf("compiled graph: %+v", document.Timelines)
	}
	clip := document.Timelines[0].Tracks[2].Clips[0]
	if clip.TimelineRange.Start != (RationalTime{Value: 1, Rate: 5}) || clip.TimelineRange.Duration != (RationalTime{Value: 14, Rate: 5}) {
		t.Fatalf("frame times were not reduced: %+v", clip.TimelineRange)
	}
	raw, err := CanonicalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalDocument(again)
	if err != nil || string(raw) != string(second) {
		t.Fatal("motion document is not stable")
	}
}

func TestBuildMotionCompositionV2RejectsInvalidFrameRanges(t *testing.T) {
	_, err := BuildMotionCompositionV2(MotionCompositionV2{ID: "bad", Revision: 1, TimelineID: "main", Width: 1920, Height: 1080, FramesPerSecond: 30, DurationFrames: 60, Assets: []Asset{MotionRectangleAssetV2("rect", 10, 10, ColorRGBA16{A: 65535})}, Layers: []MotionLayerV2{{ID: "late", TrackKind: documentv2.TrackKindVector, AssetID: "rect", StartFrame: 59, DurationFrames: 2}}})
	if !errors.Is(err, ErrMotionCompositionV2) {
		t.Fatalf("expected motion error, got %v", err)
	}
}
