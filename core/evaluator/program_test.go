package evaluator

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestCompileCapabilityGateAndFrozenSnapshot(t *testing.T) {
	document := validDocument(t)
	if program, diagnostics := Compile(document, videoedit.BackendCapabilities{BackendID: "empty"}); program != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("missing-capability compile = program=%v diagnostics=%+v", program, diagnostics)
	}

	program := compileValid(t, document)
	if program.DocumentHash() == "" {
		t.Fatal("compiled program has no document hash")
	}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Start.Value = 100
	evaluation, err := program.EvaluateClip("timeline-main", "clip-video-a", rt(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.SourceTime != rt(3, 1) {
		t.Fatalf("frozen source time = %+v, want 3/1", evaluation.SourceTime)
	}
}

func TestEvaluateClipPropertiesAndDefensiveExtensions(t *testing.T) {
	document := validDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions = append(
		document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions,
		documentv2.ExtensionProperty{
			Namespace: "vendor.blur.v1", AffectsRendering: true,
			RequiredCapability: "vendor.blur.v1", Value: json.RawMessage(`{"radius":1}`),
		},
	)
	program := compileValid(t, document)
	video, err := program.EvaluateClip("timeline-main", "clip-video-a", rt(5, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !video.Visible || video.LocalTime != rt(5, 2) || video.SourceTime != rt(9, 2) {
		t.Fatalf("video time mapping = %+v", video)
	}
	if video.Properties.Transform == nil || video.Properties.Transform.Position.X.String() != "50/1" ||
		video.Properties.Transform.Position.Y.String() != "25/1" {
		t.Fatalf("cubic position = %+v", video.Properties.Transform)
	}
	if video.Properties.Opacity == nil || video.Properties.Opacity.String() != "1/1" ||
		video.Properties.Crop == nil || video.Properties.Crop.Width.String() != "1/1" {
		t.Fatalf("visual defaults = %+v", video.Properties)
	}
	if len(video.Properties.RenderingExtensions) != 1 || video.Properties.RenderingExtensions[0].Namespace != "vendor.blur.v1" {
		t.Fatalf("rendering extensions = %+v", video.Properties.RenderingExtensions)
	}
	originalExtension := append([]byte(nil), video.Properties.RenderingExtensions[0].Value...)
	video.Properties.RenderingExtensions[0].Value[0] ^= 0xff
	again, err := program.EvaluateClip("timeline-main", "clip-video-a", rt(5, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Properties.RenderingExtensions[0].Value, originalExtension) {
		t.Fatal("caller mutation changed compiled extension snapshot")
	}

	audio, err := program.EvaluateClip("timeline-main", "clip-audio", rt(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if audio.Properties.AudioGain == nil || audio.Properties.AudioGain.String() != "1/2" ||
		audio.Properties.AudioPan == nil || audio.Properties.AudioPan.String() != "0/1" {
		t.Fatalf("audio evaluation = %+v", audio.Properties)
	}
	vector, err := program.EvaluateClip("timeline-main", "clip-vector", rt(2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if vector.Properties.Opacity == nil || vector.Properties.Opacity.String() != "3/4" || vector.Properties.AudioGain != nil {
		t.Fatalf("vector evaluation = %+v", vector.Properties)
	}
	textAtEndpoint, err := program.EvaluateClip("timeline-main", "clip-text", rt(5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if textAtEndpoint.Visible || textAtEndpoint.Properties.TextFill == nil || textAtEndpoint.Properties.TextFill.G != 65535 {
		t.Fatalf("text endpoint clamp = %+v", textAtEndpoint)
	}
}

func TestProgramUsesCompiledPropertiesAndLaneIndex(t *testing.T) {
	program := compileValid(t, validDocument(t))
	compiled := program.clips["timeline-main"]["clip-video-a"]
	// compiledClip deliberately retains no raw PropertySet or KeyframeLanes graph: evaluation uses
	// only the compiled static snapshot and lane index.
	if compiled == nil || compiled.id != "clip-video-a" || compiled.static.Opacity == nil || len(compiled.lanes) == 0 {
		t.Fatalf("compact compiled clip=%+v", compiled)
	}

	evaluation, err := program.EvaluateClip("timeline-main", "clip-video-a", rt(5, 2))
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Properties.Opacity == nil || evaluation.Properties.Opacity.String() != "1/1" ||
		evaluation.Properties.Transform == nil || evaluation.Properties.Transform.Position.X.String() != "50/1" {
		t.Fatalf("compiled properties were not used: %+v", evaluation.Properties)
	}
}

func TestAudioChannelMatrixIsCapabilityGatedAndDefensivelyCopied(t *testing.T) {
	document := validDocument(t)
	matrix := &documentv2.AudioChannelMatrixProperty{
		SourceLayout: "stereo", SourceChannels: []string{"FL", "FR"},
		LeftCoefficients:  []documentv2.Rational{{Numerator: 1, Denominator: 1}, {Numerator: 0, Denominator: 1}},
		RightCoefficients: []documentv2.Rational{{Numerator: 0, Denominator: 1}, {Numerator: 1, Denominator: 1}},
	}
	audioClip := &document.Timelines[0].Tracks[1].Clips[0]
	audioClip.PropertySet.AudioChannelMatrix = matrix

	if _, err := EvaluateProperties(documentv2.TrackKindAudio, *audioClip, document.Assets[2], rt(0, 1)); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("standalone channel matrix error = %v", err)
	}
	program := compileValid(t, document)
	evaluation, err := program.EvaluateClip("timeline-main", "clip-audio", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	actual := evaluation.Properties.AudioChannelMatrix
	if actual == nil || actual.SourceLayout != "stereo" || len(actual.SourceChannels) != 2 ||
		actual.LeftCoefficients[0].String() != "1/1" || actual.RightCoefficients[1].String() != "1/1" {
		t.Fatalf("channel matrix = %+v", actual)
	}
	actual.SourceChannels[0] = "mutated"
	actual.LeftCoefficients[0] = exactInteger(0)
	again, err := program.EvaluateClip("timeline-main", "clip-audio", rt(0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if again.Properties.AudioChannelMatrix.SourceChannels[0] != "FL" ||
		again.Properties.AudioChannelMatrix.LeftCoefficients[0].String() != "1/1" {
		t.Fatalf("caller mutation changed compiled matrix: %+v", again.Properties.AudioChannelMatrix)
	}
}

func TestProgramSeekOrderAndConcurrentEvaluationAreStable(t *testing.T) {
	program := compileValid(t, validDocument(t))
	times := []documentv2.RationalTime{rt(-1, 1), rt(0, 1), rt(1, 3), rt(5, 2), rt(5, 1), rt(7, 1)}
	want := make(map[documentv2.RationalTime][]byte, len(times))
	for _, at := range times {
		evaluation, err := program.EvaluateClip("timeline-main", "clip-video-a", at)
		if err != nil {
			t.Fatalf("forward seek %v: %v", at, err)
		}
		want[at], err = json.Marshal(evaluation)
		if err != nil {
			t.Fatalf("marshal forward seek %v: %v", at, err)
		}
	}
	for index := len(times) - 1; index >= 0; index-- {
		at := times[index]
		evaluation, err := program.EvaluateClip("timeline-main", "clip-video-a", at)
		if err != nil {
			t.Fatalf("reverse seek %v: %v", at, err)
		}
		actual, err := json.Marshal(evaluation)
		if err != nil {
			t.Fatalf("marshal reverse seek %v: %v", at, err)
		}
		if !bytes.Equal(actual, want[at]) {
			t.Fatalf("seek-order state leaked at %v:\n%s\n%s", at, want[at], actual)
		}
	}

	type result struct {
		raw []byte
		err error
	}
	const workers = 32
	results := make(chan result, workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			evaluation, err := program.EvaluateTransition("timeline-main", "transition-main", rt(11, 2))
			if err != nil {
				results <- result{err: err}
				return
			}
			raw, err := json.Marshal(evaluation)
			results <- result{raw: raw, err: err}
		}()
	}
	var baseline []byte
	for worker := 0; worker < workers; worker++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent evaluation: %v", result.err)
		}
		if baseline == nil {
			baseline = result.raw
			continue
		}
		if !bytes.Equal(result.raw, baseline) {
			t.Fatalf("concurrent evaluation diverged:\n%s\n%s", baseline, result.raw)
		}
	}
}

func TestEvaluatePropertiesRejectsInvalidStandaloneContext(t *testing.T) {
	zero, one, two := documentv2.Rational{Numerator: 0, Denominator: 1}, documentv2.Rational{Numerator: 1, Denominator: 1}, documentv2.Rational{Numerator: 2, Denominator: 1}
	baseAsset := documentv2.Asset{ID: "asset", Kind: documentv2.AssetKindVideo, URI: "asset.mp4"}
	baseClip := documentv2.Clip{
		ID: "clip", AssetID: baseAsset.ID,
		TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
		SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
	}
	tests := []struct {
		name  string
		track documentv2.TrackKind
		asset documentv2.Asset
		clip  documentv2.Clip
	}{
		{name: "track asset mismatch", track: documentv2.TrackKindText, asset: baseAsset, clip: baseClip},
		{name: "asset id mismatch", track: documentv2.TrackKindVideo, asset: documentv2.Asset{ID: "other", Kind: documentv2.AssetKindVideo}, clip: baseClip},
	}
	withProperties := func(properties documentv2.PropertySet) documentv2.Clip {
		copy := baseClip
		copy.PropertySet = &properties
		return copy
	}
	tests = append(tests,
		struct {
			name  string
			track documentv2.TrackKind
			asset documentv2.Asset
			clip  documentv2.Clip
		}{name: "opacity range", track: documentv2.TrackKindVideo, asset: baseAsset, clip: withProperties(documentv2.PropertySet{Opacity: &documentv2.OpacityProperty{Value: two}})},
		struct {
			name  string
			track documentv2.TrackKind
			asset documentv2.Asset
			clip  documentv2.Clip
		}{name: "anchor range", track: documentv2.TrackKindVideo, asset: baseAsset, clip: withProperties(documentv2.PropertySet{Transform: &documentv2.TransformProperty{
			Position: documentv2.Point{X: zero, Y: zero}, Scale: documentv2.Point{X: one, Y: one}, RotationDegrees: zero,
			Anchor: documentv2.Point{X: two, Y: one},
		}})},
		struct {
			name  string
			track documentv2.TrackKind
			asset documentv2.Asset
			clip  documentv2.Clip
		}{name: "crop bounds", track: documentv2.TrackKindVideo, asset: baseAsset, clip: withProperties(documentv2.PropertySet{Crop: &documentv2.CropProperty{Rect: documentv2.Rect{
			X: documentv2.Rational{Numerator: 3, Denominator: 4}, Y: zero, Width: documentv2.Rational{Numerator: 1, Denominator: 2}, Height: one,
		}}})},
	)
	audioAsset := documentv2.Asset{ID: "audio", Kind: documentv2.AssetKindAudio, URI: "audio.wav"}
	audioClip := baseClip
	audioClip.AssetID = audioAsset.ID
	audioClip.PropertySet = &documentv2.PropertySet{AudioGain: &documentv2.AudioGainProperty{Linear: documentv2.Rational{Numerator: -1, Denominator: 1}}}
	tests = append(tests, struct {
		name  string
		track documentv2.TrackKind
		asset documentv2.Asset
		clip  documentv2.Clip
	}{name: "negative gain", track: documentv2.TrackKindAudio, asset: audioAsset, clip: audioClip})
	audioClip.PropertySet = &documentv2.PropertySet{AudioPan: &documentv2.AudioPanProperty{Value: two}}
	tests = append(tests, struct {
		name  string
		track documentv2.TrackKind
		asset documentv2.Asset
		clip  documentv2.Clip
	}{name: "pan range", track: documentv2.TrackKindAudio, asset: audioAsset, clip: audioClip})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EvaluateProperties(test.track, test.clip, test.asset, rt(0, 1)); !errors.Is(err, ErrInputInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEvaluatePropertiesRejectsDuplicateAndOutOfRangeLanes(t *testing.T) {
	asset := documentv2.Asset{ID: "asset", Kind: documentv2.AssetKindVideo, URI: "asset.mp4"}
	one := documentv2.Rational{Numerator: 1, Denominator: 1}
	baseLane := documentv2.KeyframeLane{
		ID: "lane-one", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &one}}},
	}
	baseClip := documentv2.Clip{
		ID: "clip", AssetID: asset.ID,
		TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
		SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
		KeyframeLanes: []documentv2.KeyframeLane{baseLane},
	}

	duplicate := baseClip
	second := baseLane
	second.ID = "lane-two"
	duplicate.KeyframeLanes = append(duplicate.KeyframeLanes, second)
	if _, err := EvaluateProperties(documentv2.TrackKindVideo, duplicate, asset, rt(0, 1)); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("duplicate lane error = %v", err)
	}

	outOfRange := baseClip
	outOfRange.KeyframeLanes = append([]documentv2.KeyframeLane(nil), baseClip.KeyframeLanes...)
	outOfRange.KeyframeLanes[0].Keyframes = []documentv2.Keyframe{{
		Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: &one},
	}}
	if _, err := EvaluateProperties(documentv2.TrackKindVideo, outOfRange, asset, rt(0, 1)); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("out-of-range lane error = %v", err)
	}
}

func TestStandalonePropertiesRejectRenderingExtensionsWithoutCapabilityGate(t *testing.T) {
	asset := documentv2.Asset{ID: "asset", Kind: documentv2.AssetKindVideo, URI: "asset.mp4"}
	clip := documentv2.Clip{
		ID: "clip", AssetID: asset.ID,
		TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
		SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
		PropertySet: &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{
			Namespace: "vendor.blur.v1", AffectsRendering: true,
			RequiredCapability: "vendor.blur.v1", Value: json.RawMessage(`{"radius":1}`),
		}}},
	}
	if _, err := EvaluateProperties(documentv2.TrackKindVideo, clip, asset, rt(0, 1)); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("standalone rendering extension error = %v", err)
	}
	clip.PropertySet.Extensions[0] = documentv2.ExtensionProperty{
		Namespace: "cutly.note.v1", AffectsRendering: false, Value: json.RawMessage(`{"intent":"review"}`),
	}
	properties, err := EvaluateProperties(documentv2.TrackKindVideo, clip, asset, rt(0, 1))
	if err != nil {
		t.Fatalf("non-rendering extension: %v", err)
	}
	if len(properties.RenderingExtensions) != 0 {
		t.Fatalf("non-rendering intent leaked into renderer: %+v", properties.RenderingExtensions)
	}
}

func TestEvaluateTransitionUsesSharedConformanceVectors(t *testing.T) {
	document := validDocument(t)
	document.Timelines[0].Transitions[0].PreDuration = rt(2, 1)
	document.Timelines[0].Transitions[0].PostDuration = rt(1, 1)
	program := compileValid(t, document)

	type vectorSample struct {
		At       documentv2.RationalTime    `json:"at"`
		Phase    documentv2.TransitionPhase `json:"phase"`
		Progress *documentv2.Rational       `json:"progress,omitempty"`
	}
	var fixture struct {
		Profile      string                  `json:"profile"`
		Cut          documentv2.RationalTime `json:"cut"`
		PreDuration  documentv2.RationalTime `json:"pre_duration"`
		PostDuration documentv2.RationalTime `json:"post_duration"`
		Samples      []vectorSample          `json:"samples"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "transition-dissolve-v1.vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for index, vector := range fixture.Samples {
		actual, err := program.EvaluateTransition("timeline-main", "transition-main", vector.At)
		if err != nil {
			t.Fatalf("sample %d: %v", index, err)
		}
		if actual.Phase != vector.Phase {
			t.Fatalf("sample %d phase = %s, want %s", index, actual.Phase, vector.Phase)
		}
		if vector.Progress == nil {
			if actual.Progress != nil {
				t.Fatalf("sample %d unexpected progress %s", index, actual.Progress)
			}
			continue
		}
		if actual.Progress == nil || actual.Progress.String() != rationalString(*vector.Progress) {
			t.Fatalf("sample %d progress = %v, want %s", index, actual.Progress, rationalString(*vector.Progress))
		}
		if actual.Outgoing == nil || actual.Incoming == nil {
			t.Fatalf("sample %d active transition omitted handle samples", index)
		}
		if !actual.OutgoingWeight.Equal(exactFromBig(newRat(vector.Progress.Denominator-vector.Progress.Numerator, vector.Progress.Denominator))) ||
			!actual.IncomingWeight.Equal(exactFromBig(newRat(vector.Progress.Numerator, vector.Progress.Denominator))) {
			t.Fatalf("sample %d weights = %s/%s", index, actual.OutgoingWeight.String(), actual.IncomingWeight.String())
		}
	}
}

func TestTransitionHandleSourceMappingAndHalfOpenEnd(t *testing.T) {
	program := compileValid(t, validDocument(t))
	at := rt(11, 2)
	evaluation, err := program.EvaluateTransition("timeline-main", "transition-main", at)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Phase != documentv2.TransitionPhaseActive || evaluation.Progress == nil || evaluation.Progress.String() != "3/4" {
		t.Fatalf("transition = %+v", evaluation)
	}
	if evaluation.Outgoing == nil || evaluation.Outgoing.Visible || evaluation.Outgoing.SourceTime != rt(15, 2) {
		t.Fatalf("outgoing tail handle = %+v", evaluation.Outgoing)
	}
	if evaluation.Incoming == nil || !evaluation.Incoming.Visible || evaluation.Incoming.SourceTime != rt(5, 2) {
		t.Fatalf("incoming sample = %+v", evaluation.Incoming)
	}
	atEnd, err := program.EvaluateTransition("timeline-main", "transition-main", rt(6, 1))
	if err != nil {
		t.Fatal(err)
	}
	if atEnd.Phase != documentv2.TransitionPhaseIncoming || atEnd.Progress != nil || atEnd.Outgoing != nil || atEnd.Incoming != nil ||
		atEnd.OutgoingWeight.String() != "0/1" || atEnd.IncomingWeight.String() != "1/1" {
		t.Fatalf("half-open transition end = %+v", atEnd)
	}
}

func TestExactClipAndSourceTimeMapping(t *testing.T) {
	clip := documentv2.Clip{
		TimelineRange: documentv2.TimeRange{Start: rt(10, 1), Duration: rt(2, 1)},
		SourceRange:   documentv2.TimeRange{Start: rt(3, 1), Duration: rt(5, 1)},
	}
	local, err := ClipLocalTime(clip, rt(11, 1))
	if err != nil || local != rt(1, 1) {
		t.Fatalf("local = %+v err=%v", local, err)
	}
	source, err := SourceTime(clip, rt(11, 1))
	if err != nil || source != rt(11, 2) {
		t.Fatalf("source = %+v err=%v", source, err)
	}
	head, err := SourceTime(clip, rt(9, 1))
	if err != nil || head != rt(1, 2) {
		t.Fatalf("head handle = %+v err=%v", head, err)
	}
}

func TestTimeOverflowIsExplicit(t *testing.T) {
	clip := documentv2.Clip{
		TimelineRange: documentv2.TimeRange{Start: rt(-9223372036854775808, 1), Duration: rt(1, 1)},
		SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
	}
	if _, err := ClipLocalTime(clip, rt(9223372036854775807, 1)); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("local overflow = %v", err)
	}
	clip = documentv2.Clip{
		TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)},
		SourceRange:   documentv2.TimeRange{Start: rt(9223372036854775806, 1), Duration: rt(1, 1)},
	}
	if _, err := SourceTime(clip, rt(2, 1)); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("source overflow = %v", err)
	}
}

func FuzzTimeMappingNeverPanics(f *testing.F) {
	f.Add(int64(0), int64(1), int64(1), int64(1), int64(0), int64(1), int64(1), int64(2))
	f.Add(int64(-1), int64(30000), int64(1001), int64(30000), int64(-5), int64(48000), int64(0), int64(1))
	f.Fuzz(func(
		t *testing.T,
		timelineStartValue, timelineRate, durationValue, durationRate,
		sourceStartValue, sourceRate, atValue, atRate int64,
	) {
		clip := documentv2.Clip{
			TimelineRange: documentv2.TimeRange{
				Start: rt(timelineStartValue, timelineRate), Duration: rt(durationValue, durationRate),
			},
			SourceRange: documentv2.TimeRange{
				Start: rt(sourceStartValue, sourceRate), Duration: rt(durationValue, durationRate),
			},
		}
		_, _ = ClipLocalTime(clip, rt(atValue, atRate))
		_, _ = SourceTime(clip, rt(atValue, atRate))
	})
}

func TestTransitionProgressWireOverflowIsExplicit(t *testing.T) {
	maximum := int64(9223372036854775807)
	duration := rt(4, 1)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "overflow-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "asset-out", Kind: documentv2.AssetKindVideo, URI: "out.mp4", Duration: &duration},
			{ID: "asset-in", Kind: documentv2.AssetKindVideo, URI: "in.mp4", Duration: &duration},
		},
		Timelines: []documentv2.Timeline{{
			ID: "timeline", Tracks: []documentv2.Track{{
				ID: "track", Kind: documentv2.TrackKindVideo,
				Clips: []documentv2.Clip{
					{ID: "out", AssetID: "asset-out", TimelineRange: documentv2.TimeRange{Start: rt(-2, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)}},
					{ID: "in", AssetID: "asset-in", TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)}},
				},
			}},
			Transitions: []documentv2.Transition{{
				ID: "transition", Profile: documentv2.TransitionDissolveV1,
				OutgoingClipID: "out", IncomingClipID: "in", PreDuration: rt(1, 1), PostDuration: rt(1, 1),
			}},
		}},
	}
	program := compileValid(t, document)
	_, err := program.EvaluateTransition("timeline", "transition", rt(2, maximum))
	if !errors.Is(err, ErrValueNotWireSafe) {
		t.Fatalf("transition progress overflow = %v", err)
	}
	var evaluationErr *Error
	if !errors.As(err, &evaluationErr) || evaluationErr.Code != CodeValueNotWireSafe || evaluationErr.Path != "transition.progress" {
		t.Fatalf("transition progress diagnostic = %#v", evaluationErr)
	}
}

func validDocument(t *testing.T) documentv2.Document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func compileValid(t *testing.T, document documentv2.Document) *Program {
	t.Helper()
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("requirements: %+v", diagnostics)
	}
	offered := videoedit.BackendCapabilities{BackendID: "evaluator-test", Features: required.Features}
	for _, kind := range required.TrackKinds {
		offered.TrackKinds = append(offered.TrackKinds, videoedit.TrackKind(kind))
	}
	for _, kind := range required.AssetKinds {
		offered.AssetKinds = append(offered.AssetKinds, videoedit.AssetKind(kind))
	}
	program, diagnostics := Compile(document, offered)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile: program=%v diagnostics=%+v", program, diagnostics)
	}
	return program
}

func rationalString(value documentv2.Rational) string {
	return exactFromBig(newRat(value.Numerator, value.Denominator)).String()
}

func newRat(numerator, denominator int64) *big.Rat {
	return new(big.Rat).SetFrac64(numerator, denominator)
}
