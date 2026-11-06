package documentv2

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestRequiredCapabilitiesV1HardCutMigrationNeedsNoNewFeature(t *testing.T) {
	duration := capabilityTime(4, 1)
	source := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion,
		ID:            "legacy-document",
		Revision:      7,
		Assets: []videoedit.Asset{{
			ID: "legacy-video", Kind: videoedit.AssetKindVideo, URI: "video.mp4", Duration: &duration,
		}},
		Timelines: []videoedit.Timeline{{
			ID: "legacy-timeline",
			Tracks: []videoedit.Track{{
				ID: "legacy-video-track", Kind: videoedit.TrackKindVideo,
				Clips: []videoedit.Clip{{
					ID: "legacy-video-clip", AssetID: "legacy-video",
					TimelineRange: capabilityRange(0, 4), SourceRange: capabilityRange(0, 4),
				}},
			}},
		}},
	}

	document, migrationDiagnostics, err := MigrateV1(source)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrorDiagnostics(migrationDiagnostics) {
		t.Fatalf("migration diagnostics: %+v", migrationDiagnostics)
	}
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("capability diagnostics: %+v", diagnostics)
	}
	if !slices.Equal(required.TrackKinds, []TrackKind{TrackKindVideo}) ||
		!slices.Equal(required.AssetKinds, []AssetKind{AssetKindVideo}) || len(required.Features) != 0 {
		t.Fatalf("legacy hard cut gained v2 requirements: %+v", required)
	}
	if !IsV1Compatible(document) {
		t.Fatalf("plain v1 hard cut is not v1-compatible: %+v", V1CompatibilityDiagnostics(document))
	}
	if diagnostics := CheckCapabilities(required, hardCutCapabilities()); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("hard-cut backend rejected migrated hard cut: %+v", diagnostics)
	}
}

func TestRequiredCapabilitiesMapsAdvancedSemanticsSortedAndUnique(t *testing.T) {
	document := advancedCapabilityDocument()
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("valid advanced document diagnostics: %+v", diagnostics)
	}
	wantTracks := []TrackKind{TrackKindAudio, TrackKindText, TrackKindVector, TrackKindVideo}
	wantAssets := []AssetKind{AssetKindAudio, AssetKindFont, AssetKindText, AssetKindVector, AssetKindVideo}
	wantFeatures := []videoedit.CapabilityID{
		videoedit.CapabilityAudioVarispeed,
		CapabilityAudioChannelMatrixV1,
		CapabilityAudioGainV1,
		CapabilityAudioSamplePeakLimiterV1,
		CapabilityAudioPanV1,
		CapabilityAudioRouteV1,
		CapabilityCropV1,
		CapabilityInterpolationCubicV1,
		CapabilityInterpolationHoldV1,
		CapabilityInterpolationLinearV1,
		CapabilityKeyframesV1,
		CapabilityOpacityV1,
		CapabilityTextV1,
		CapabilityTransformV1,
		videoedit.CapabilityID(TransitionDissolveV1),
		CapabilityVectorPathV1,
		"vendor.effect.v1",
		videoedit.CapabilityVideoLinearRetime,
	}
	if !slices.Equal(required.TrackKinds, wantTracks) {
		t.Errorf("track kinds = %q, want %q", required.TrackKinds, wantTracks)
	}
	if !slices.Equal(required.AssetKinds, wantAssets) {
		t.Errorf("asset kinds = %q, want %q", required.AssetKinds, wantAssets)
	}
	if !slices.Equal(required.Features, wantFeatures) {
		t.Errorf("features = %q, want %q", required.Features, wantFeatures)
	}

	again, againDiagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(againDiagnostics) || !slices.Equal(again.Features, required.Features) ||
		!slices.Equal(again.TrackKinds, required.TrackKinds) || !slices.Equal(again.AssetKinds, required.AssetKinds) {
		t.Fatalf("mapping is not repeatable: first=%+v second=%+v diagnostics=%+v", required, again, againDiagnostics)
	}
}

func TestRequiredCapabilitiesTreatsZeroTransitionAsHardCut(t *testing.T) {
	document := transitionCapabilityDocument(false)
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("zero transition diagnostics: %+v", diagnostics)
	}
	if slices.Contains(required.Features, videoedit.CapabilityID(TransitionDissolveV1)) {
		t.Fatalf("zero transition requested dissolve: %+v", required)
	}
	if !IsV1Compatible(document) {
		t.Fatalf("zero transition should down-convert as a hard cut: %+v", V1CompatibilityDiagnostics(document))
	}

	document = transitionCapabilityDocument(true)
	required, diagnostics = RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("dissolve diagnostics: %+v", diagnostics)
	}
	if !slices.Contains(required.Features, videoedit.CapabilityID(TransitionDissolveV1)) {
		t.Fatalf("non-zero transition did not request dissolve: %+v", required)
	}
	if IsV1Compatible(document) {
		t.Fatal("non-zero transition unexpectedly passed the v1 semantic gate")
	}
}

func TestRequiredCapabilitiesIgnoresDisabledClipSemantics(t *testing.T) {
	document := baseCapabilityDocument()
	document.Timelines[0].Tracks[0].Clips[0].Disabled = true
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &PropertySet{
		Opacity: &OpacityProperty{Value: rationalHalf()},
		Extensions: []ExtensionProperty{{
			Namespace: "vendor.effect.v1", AffectsRendering: true,
			RequiredCapability: "vendor.effect.v1", Value: []byte(`{"amount":1}`),
		}},
	}
	document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []KeyframeLane{{
		ID: "disabled-lane", Target: KeyframeTargetOpacity, ValueType: KeyframeValueScalar,
		Keyframes: []Keyframe{{
			Offset: capabilityTime(0, 1), Value: KeyframeValue{Scalar: rationalPointer(rationalHalf())},
		}},
	}}
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("disabled clip document diagnostics: %+v", diagnostics)
	}
	if len(required.TrackKinds) != 0 || len(required.AssetKinds) != 0 || len(required.Features) != 0 {
		t.Fatalf("disabled clip leaked rendering requirements: %+v", required)
	}
}

func TestRequiredCapabilitiesDoesNotRetimeStaticImage(t *testing.T) {
	document := baseCapabilityDocument()
	document.Assets[0] = Asset{ID: "image", Kind: AssetKindImage, URI: "still.png"}
	clip := &document.Timelines[0].Tracks[0].Clips[0]
	clip.AssetID = "image"
	clip.TimelineRange = capabilityRange(0, 10)
	clip.SourceRange = capabilityRange(0, 1)
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("image document diagnostics: %+v", diagnostics)
	}
	if slices.Contains(required.Features, videoedit.CapabilityVideoLinearRetime) {
		t.Fatalf("static image incorrectly requested temporal retime: %+v", required)
	}
}

func TestRequiredCapabilitiesFailsClosedForInvalidRenderingExtension(t *testing.T) {
	document := baseCapabilityDocument()
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &PropertySet{
		Extensions: []ExtensionProperty{{
			Namespace: "vendor.effect.v1", AffectsRendering: true, Value: []byte(`{"amount":1}`),
		}},
	}
	required, diagnostics := RequiredCapabilities(document)
	if !HasErrorDiagnostics(diagnostics) {
		t.Fatal("rendering extension without a versioned capability was accepted")
	}
	if len(required.TrackKinds) != 0 || len(required.AssetKinds) != 0 || len(required.Features) != 0 {
		t.Fatalf("invalid document returned negotiable requirements: %+v", required)
	}
	if !capabilityHasDiagnostic(diagnostics, CodePropertyInvalid, "/required_capability") {
		t.Fatalf("missing stable extension diagnostic: %+v", diagnostics)
	}
}

func TestPreflightCapabilitiesReportsMissingExtensionConsumerAtDocumentPath(t *testing.T) {
	document := baseCapabilityDocument()
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &PropertySet{
		Extensions: []ExtensionProperty{{
			Namespace: "vendor.effect.v1", AffectsRendering: true,
			RequiredCapability: "vendor.effect.v1", Value: []byte(`{"amount":1}`),
		}},
	}
	diagnostics := PreflightCapabilities(document, hardCutCapabilities())
	if len(diagnostics) != 1 || diagnostics[0].Code != CodeExtensionNeedsConsumer ||
		diagnostics[0].Path != "/timelines/0/tracks/0/clips/0/property_set/extensions/0/required_capability" ||
		!strings.Contains(diagnostics[0].Message, "vendor.effect.v1") {
		t.Fatalf("extension rejection lost provenance: %+v", diagnostics)
	}
	offered := hardCutCapabilities()
	offered.Features = []videoedit.CapabilityID{"vendor.effect.v1"}
	if diagnostics := PreflightCapabilities(document, offered); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("declared extension consumer was rejected: %+v", diagnostics)
	}

	document.Timelines[0].Tracks[0].Clips[0].Disabled = true
	if diagnostics := PreflightCapabilities(document, hardCutCapabilities()); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("disabled extension leaked into preflight: %+v", diagnostics)
	}
}

func TestCheckCapabilitiesMakesHardCutBackendRejectV2Semantics(t *testing.T) {
	document := baseCapabilityDocument()
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &PropertySet{
		Opacity: &OpacityProperty{Value: rationalHalf()},
	}
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("typed property diagnostics: %+v", diagnostics)
	}
	first := CheckCapabilities(required, hardCutCapabilities())
	second := CheckCapabilities(required, hardCutCapabilities())
	if !HasErrorDiagnostics(first) || len(first) != 1 || first[0].Code != CodeCapabilityFeature ||
		!strings.Contains(first[0].Message, string(CapabilityOpacityV1)) {
		t.Fatalf("hard-cut rejection is incomplete: %+v", first)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("hard-cut rejection is not stable: first=%+v second=%+v", first, second)
	}
}

func hardCutCapabilities() videoedit.BackendCapabilities {
	return videoedit.BackendCapabilities{
		BackendID:  "cutly.ffmpeg-hardcut.v1",
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo, videoedit.TrackKindAudio},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindVideo, videoedit.AssetKindAudio},
	}
}

func baseCapabilityDocument() Document {
	duration := capabilityTime(20, 1)
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            "capability-document",
		Revision:      1,
		Assets:        []Asset{{ID: "video", Kind: AssetKindVideo, URI: "video.mp4", Duration: &duration}},
		Timelines: []Timeline{{
			ID: "timeline",
			Tracks: []Track{{
				ID: "video-track", Kind: TrackKindVideo,
				Clips: []Clip{{
					ID: "video-clip", AssetID: "video",
					TimelineRange: capabilityRange(0, 4), SourceRange: capabilityRange(0, 4),
				}},
			}},
		}},
	}
}

func transitionCapabilityDocument(nonZero bool) Document {
	document := baseCapabilityDocument()
	track := &document.Timelines[0].Tracks[0]
	track.Clips[0].ID = "outgoing"
	track.Clips = append(track.Clips, Clip{
		ID: "incoming", AssetID: "video",
		TimelineRange: capabilityRange(4, 4), SourceRange: capabilityRange(1, 4),
	})
	window := capabilityTime(0, 1)
	if nonZero {
		window = capabilityTime(1, 1)
	}
	document.Timelines[0].Transitions = []Transition{{
		ID: "transition", Profile: TransitionDissolveV1,
		OutgoingClipID: "outgoing", IncomingClipID: "incoming",
		PreDuration: window, PostDuration: window,
	}}
	return document
}

func advancedCapabilityDocument() Document {
	document := transitionCapabilityDocument(true)
	duration := capabilityTime(20, 1)
	document.Assets = append(document.Assets,
		Asset{ID: "audio", Kind: AssetKindAudio, URI: "audio.wav", Duration: &duration},
		Asset{ID: "font", Kind: AssetKindFont, URI: "font.woff2", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Asset{ID: "text", Kind: AssetKindText, Text: &TextSource{
			Text: "Cutly", FontAssetID: "font", FontSize: Rational{Numerator: 48, Denominator: 1},
			Fill:     ColorRGBA16{R: 65535, G: 65535, B: 65535, A: 65535},
			Language: "en", Direction: TextDirectionLTR, Alignment: TextAlignmentStart,
		}},
		Asset{ID: "vector", Kind: AssetKindVector, Vector: &VectorSource{
			Profile: VectorProfilePathV1,
			ViewBox: Rect{X: rationalZero(), Y: rationalZero(), Width: Rational{Numerator: 100, Denominator: 1}, Height: Rational{Numerator: 100, Denominator: 1}},
			Paths: []VectorPath{{
				ID: "vector-path", FillRule: VectorFillNonZero,
				Commands: []VectorCommand{
					{Op: VectorMoveTo, Points: []Point{{X: rationalZero(), Y: rationalZero()}}},
					{Op: VectorLineTo, Points: []Point{{X: rationalOne(), Y: rationalOne()}}},
				},
				Fill: &ColorRGBA16{R: 65535, A: 65535},
			}},
		}},
	)
	document.AudioBuses = []AudioBus{{
		ID: "master", Kind: AudioBusMaster,
		Properties: AudioBusProperties{
			GainLinear: rationalOne(), Pan: rationalZero(),
			SamplePeakLimiter: &AudioSamplePeakLimiterProperty{CeilingLinear: Rational{Numerator: 19, Denominator: 20}},
		},
	}}
	videoClip := &document.Timelines[0].Tracks[0].Clips[0]
	videoClip.PropertySet = &PropertySet{
		Transform: &TransformProperty{
			Position: Point{X: rationalZero(), Y: rationalZero()}, Scale: Point{X: rationalOne(), Y: rationalOne()},
			RotationDegrees: rationalZero(), Anchor: Point{X: rationalHalf(), Y: rationalHalf()},
		},
		Opacity: &OpacityProperty{Value: rationalOne()},
		Crop:    &CropProperty{Rect: Rect{X: rationalZero(), Y: rationalZero(), Width: rationalOne(), Height: rationalOne()}},
		Extensions: []ExtensionProperty{{
			Namespace: "vendor.effect.v1", AffectsRendering: true,
			RequiredCapability: "vendor.effect.v1", Value: []byte(`{"amount":1}`),
		}},
	}
	videoClip.KeyframeLanes = []KeyframeLane{{
		ID: "transform-position-lane", Target: KeyframeTargetTransformPosition, ValueType: KeyframeValuePoint,
		Keyframes: []Keyframe{
			{Offset: capabilityTime(0, 1), Value: KeyframeValue{Point: pointPointer(Point{X: rationalZero(), Y: rationalZero()})}, Interpolation: InterpolationHold},
			{Offset: capabilityTime(1, 1), Value: KeyframeValue{Point: pointPointer(Point{X: rationalOne(), Y: rationalOne()})}, Interpolation: InterpolationLinear},
			{Offset: capabilityTime(2, 1), Value: KeyframeValue{Point: pointPointer(Point{X: Rational{Numerator: 2, Denominator: 1}, Y: Rational{Numerator: 2, Denominator: 1}})}, Interpolation: InterpolationCubic, Easing: &CubicBezier{
				X1: Rational{Numerator: 1, Denominator: 4}, Y1: Rational{Numerator: 1, Denominator: 4},
				X2: Rational{Numerator: 3, Denominator: 4}, Y2: Rational{Numerator: 3, Denominator: 4},
			}},
			{Offset: capabilityTime(4, 1), Value: KeyframeValue{Point: pointPointer(Point{X: Rational{Numerator: 3, Denominator: 1}, Y: Rational{Numerator: 3, Denominator: 1}})}},
		},
	}}
	document.Timelines[0].Tracks[0].Clips[1].PropertySet = &PropertySet{
		Opacity: &OpacityProperty{Value: rationalOne()}, // duplicate requirement must deduplicate
	}
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks,
		Track{ID: "audio-track", Kind: TrackKindAudio, AudioBusID: "master", Clips: []Clip{{
			ID: "audio-clip", AssetID: "audio",
			TimelineRange: capabilityRange(0, 8), SourceRange: capabilityRange(0, 4),
			PropertySet: &PropertySet{
				AudioGain: &AudioGainProperty{Linear: rationalOne()}, AudioPan: &AudioPanProperty{Value: rationalZero()},
				AudioChannelMatrix: &AudioChannelMatrixProperty{
					SourceLayout: "stereo", SourceChannels: []string{"FL", "FR"},
					LeftCoefficients:  []Rational{rationalOne(), rationalZero()},
					RightCoefficients: []Rational{rationalZero(), rationalOne()},
				},
			},
		}}},
		Track{ID: "text-track", Kind: TrackKindText, Clips: []Clip{{
			ID: "text-clip", AssetID: "text", TimelineRange: capabilityRange(0, 4), SourceRange: capabilityRange(0, 4),
		}}},
		Track{ID: "vector-track", Kind: TrackKindVector, Clips: []Clip{{
			ID: "vector-clip", AssetID: "vector", TimelineRange: capabilityRange(0, 4), SourceRange: capabilityRange(0, 4),
		}}},
	)
	// A video retime is represented by the incoming temporal clip; its transition handles remain
	// valid because the source starts one second into a twenty-second asset.
	document.Timelines[0].Tracks[0].Clips[1].SourceRange.Duration = capabilityTime(2, 1)
	return document
}

func capabilityTime(value, rate int64) RationalTime {
	return RationalTime{Value: value, Rate: rate}
}

func capabilityRange(start, duration int64) TimeRange {
	return TimeRange{Start: capabilityTime(start, 1), Duration: capabilityTime(duration, 1)}
}

func rationalPointer(value Rational) *Rational { return &value }
func pointPointer(value Point) *Point          { return &value }

func capabilityHasDiagnostic(diagnostics []Diagnostic, code, pathFragment string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code && strings.Contains(diagnostic.Path, pathFragment) {
			return true
		}
	}
	return false
}
