package evaluator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestEvaluateTimelineOrdersVisualLayersAndExcludesNonVisualTracks(t *testing.T) {
	program := compileValid(t, validDocument(t))
	evaluation, err := program.EvaluateTimeline("timeline-main", rt(2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.LayerOrder != VisualLayerOrderBackgroundToForegroundV1 {
		t.Fatalf("layer order = %q", evaluation.LayerOrder)
	}
	if got, want := len(evaluation.Layers), 3; got != want {
		t.Fatalf("layers = %d, want %d: %+v", got, want, evaluation.Layers)
	}
	wantTracks := []struct {
		id    documentv2.ID
		index int
	}{
		{id: "track-video", index: 0},
		{id: "track-text", index: 2},
		{id: "track-vector", index: 3},
	}
	for index, want := range wantTracks {
		layer := evaluation.Layers[index]
		if layer.Kind != VisualLayerClip || layer.TrackID != want.id || layer.TrackIndex != want.index ||
			layer.Clip == nil || layer.Transition != nil || !layer.Clip.Visible {
			t.Fatalf("layer %d = %+v, want clip on %s/%d", index, layer, want.id, want.index)
		}
	}
	for _, layer := range evaluation.Layers {
		if layer.TrackID == "track-audio" || layer.TrackID == "track-annotations" {
			t.Fatalf("non-visual track entered layer list: %+v", layer)
		}
	}
}

func TestEvaluateTimelineTransitionReplacesOrdinaryClipAcrossHalfOpenWindow(t *testing.T) {
	program := compileValid(t, validDocument(t))
	for _, sample := range []struct {
		at       documentv2.RationalTime
		wantKind VisualLayerKind
		wantClip documentv2.ID
	}{
		{at: rt(4, 1), wantKind: VisualLayerTransition},
		{at: rt(5, 1), wantKind: VisualLayerTransition},
		{at: rt(599, 100), wantKind: VisualLayerTransition},
		{at: rt(6, 1), wantKind: VisualLayerClip, wantClip: "clip-video-b"},
	} {
		evaluation, err := program.EvaluateTimeline("timeline-main", sample.at)
		if err != nil {
			t.Fatalf("at %+v: %v", sample.at, err)
		}
		if len(evaluation.Layers) == 0 || evaluation.Layers[0].TrackID != "track-video" {
			t.Fatalf("at %+v video layer missing: %+v", sample.at, evaluation.Layers)
		}
		layer := evaluation.Layers[0]
		if layer.Kind != sample.wantKind {
			t.Fatalf("at %+v kind = %s, want %s", sample.at, layer.Kind, sample.wantKind)
		}
		if sample.wantKind == VisualLayerTransition {
			if layer.Transition == nil || layer.Clip != nil || layer.Transition.Outgoing == nil || layer.Transition.Incoming == nil {
				t.Fatalf("at %+v transition layer = %+v", sample.at, layer)
			}
			continue
		}
		if layer.Clip == nil || layer.Transition != nil || layer.Clip.ClipID != sample.wantClip {
			t.Fatalf("at %+v clip layer = %+v", sample.at, layer)
		}
	}
}

func TestEvaluateTimelineZeroWidthTransitionIsOrdinaryHardCut(t *testing.T) {
	document := validDocument(t)
	document.Timelines[0].Transitions[0].PreDuration = rt(0, 1)
	document.Timelines[0].Transitions[0].PostDuration = rt(0, 1)
	program := compileValid(t, document)

	before, err := program.EvaluateTimeline("timeline-main", rt(499, 100))
	if err != nil {
		t.Fatal(err)
	}
	atCut, err := program.EvaluateTimeline("timeline-main", rt(5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if before.Layers[0].Kind != VisualLayerClip || before.Layers[0].Clip.ClipID != "clip-video-a" ||
		atCut.Layers[0].Kind != VisualLayerClip || atCut.Layers[0].Clip.ClipID != "clip-video-b" {
		t.Fatalf("hard cut before=%+v at=%+v", before.Layers[0], atCut.Layers[0])
	}
}

func TestEvaluateTimelineZeroWidthTransitionCannotBreakFollowingWindowIndex(t *testing.T) {
	duration := rt(4, 1)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "zero-width-index-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "asset-a", Kind: documentv2.AssetKindVideo, URI: "a.mp4", Duration: &duration},
			{ID: "asset-b", Kind: documentv2.AssetKindVideo, URI: "b.mp4", Duration: &duration},
			{ID: "asset-c", Kind: documentv2.AssetKindVideo, URI: "c.mp4", Duration: &duration},
		},
		Timelines: []documentv2.Timeline{{
			ID: "timeline", Tracks: []documentv2.Track{{
				ID: "track", Kind: documentv2.TrackKindVideo,
				Clips: []documentv2.Clip{
					{ID: "clip-a", AssetID: "asset-a", TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)}, SourceRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)}},
					{ID: "clip-b", AssetID: "asset-b", TimelineRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(1, 1)}, SourceRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)}},
					{ID: "clip-c", AssetID: "asset-c", TimelineRange: documentv2.TimeRange{Start: rt(2, 1), Duration: rt(1, 1)}, SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(1, 1)}},
				},
			}},
			Transitions: []documentv2.Transition{
				{
					// Both compiled windows start at t=1. The non-zero transition sorts first
					// by ID, which used to leave end times in descending order and invalidate
					// sort.Search's monotonic predicate.
					ID: "z-zero", Profile: documentv2.TransitionDissolveV1,
					OutgoingClipID: "clip-a", IncomingClipID: "clip-b",
					PreDuration: rt(0, 1), PostDuration: rt(0, 1),
				},
				{
					ID: "a-following", Profile: documentv2.TransitionDissolveV1,
					OutgoingClipID: "clip-b", IncomingClipID: "clip-c",
					PreDuration: rt(1, 1), PostDuration: rt(0, 1),
				},
			},
		}},
	}
	program := compileValid(t, document)

	evaluation, err := program.EvaluateTimeline("timeline", rt(3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Layers) != 1 || evaluation.Layers[0].Kind != VisualLayerTransition ||
		evaluation.Layers[0].Transition == nil || evaluation.Layers[0].Transition.TransitionID != "a-following" {
		t.Fatalf("following active window was lost behind zero-width record: %+v", evaluation.Layers)
	}

	// The hard-cut record remains explicitly queryable even though it is absent from the active
	// interval index.
	hardCut, err := program.EvaluateTransition("timeline", "z-zero", rt(1, 1))
	if err != nil || hardCut.Phase != documentv2.TransitionPhaseIncoming {
		t.Fatalf("hard-cut audit record = %+v err=%v", hardCut, err)
	}
}

func TestEvaluateTimelineDisabledGapAndLookupFailures(t *testing.T) {
	document := validDocument(t)
	document.Timelines[0].Tracks[2].Clips[0].Disabled = true
	program := compileValid(t, document)
	evaluation, err := program.EvaluateTimeline("timeline-main", rt(2, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range evaluation.Layers {
		if layer.TrackID == "track-text" {
			t.Fatal("disabled text clip entered layer list")
		}
	}
	gap, err := program.EvaluateTimeline("timeline-main", rt(11, 1))
	if err != nil || len(gap.Layers) != 0 {
		t.Fatalf("gap = %+v err=%v", gap, err)
	}
	if _, err := program.EvaluateTimeline("missing", rt(0, 1)); !errors.Is(err, ErrProgramLookup) {
		t.Fatalf("missing timeline error = %v", err)
	}
	if _, err := program.EvaluateTimeline("timeline-main", rt(0, 0)); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("invalid time error = %v", err)
	}
}

func TestEvaluateVisualFrameUsesCompositionClock(t *testing.T) {
	program := compileValid(t, validDocument(t))
	clock := videoedit.FrameClock{
		Origin:          documentv2.RationalTime{Value: 1, Rate: 2},
		FramesPerSecond: documentv2.RationalTime{Value: 30000, Rate: 1001},
	}
	frame, err := program.EvaluateVisualFrame("timeline-main", clock, 45)
	if err != nil {
		t.Fatal(err)
	}
	want, err := videoedit.FrameTimeAt(clock, 45)
	if err != nil {
		t.Fatal(err)
	}
	if frame.CompositionFrameIndex != 45 || frame.Timeline.TimelineTime != want {
		t.Fatalf("frame = %+v, want time %+v", frame, want)
	}
	badClock := clock
	badClock.FramesPerSecond = rt(0, 1)
	if _, err := program.EvaluateVisualFrame("timeline-main", badClock, 0); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("bad clock error = %v", err)
	}
}

func TestEvaluateTimelineSeekOrderAndConcurrencyAreDeterministic(t *testing.T) {
	program := compileValid(t, validDocument(t))
	times := []documentv2.RationalTime{rt(0, 1), rt(9, 2), rt(5, 1), rt(6, 1), rt(2, 1), rt(11, 1)}
	baseline := make(map[string][]byte, len(times))
	for _, at := range times {
		evaluation, err := program.EvaluateTimeline("timeline-main", at)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(evaluation)
		if err != nil {
			t.Fatal(err)
		}
		baseline[fmt.Sprint(at)] = encoded
	}

	var wait sync.WaitGroup
	errorsOut := make(chan error, 32*len(times))
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for offset := range times {
				at := times[(len(times)-1-offset+worker)%len(times)]
				evaluation, err := program.EvaluateTimeline("timeline-main", at)
				if err != nil {
					errorsOut <- err
					continue
				}
				encoded, err := json.Marshal(evaluation)
				if err != nil {
					errorsOut <- err
					continue
				}
				if !bytes.Equal(encoded, baseline[fmt.Sprint(at)]) {
					errorsOut <- fmt.Errorf("at %+v changed across seek order", at)
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func BenchmarkEvaluateTimelineIndexed(b *testing.B) {
	const clipCount = 10000
	duration := rt(clipCount, 1)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "benchmark-document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset", Kind: documentv2.AssetKindVideo, URI: "video.mp4", Duration: &duration}},
		Timelines: []documentv2.Timeline{{
			ID: "timeline", Tracks: []documentv2.Track{{ID: "track", Kind: documentv2.TrackKindVideo}},
		}},
	}
	clips := make([]documentv2.Clip, 0, clipCount)
	for index := 0; index < clipCount; index++ {
		clips = append(clips, documentv2.Clip{
			ID: documentv2.ID(fmt.Sprintf("clip-%05d", index)), AssetID: "asset",
			TimelineRange: documentv2.TimeRange{Start: rt(int64(index), 1), Duration: rt(1, 1)},
			SourceRange:   documentv2.TimeRange{Start: rt(int64(index), 1), Duration: rt(1, 1)},
		})
	}
	document.Timelines[0].Tracks[0].Clips = clips
	program, diagnostics := Compile(document, videoedit.BackendCapabilities{
		BackendID: "benchmark", TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindVideo},
	})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		b.Fatalf("compile: %+v", diagnostics)
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		at := rt(int64(iteration%clipCount), 1)
		if _, err := program.EvaluateTimeline("timeline", at); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompiledLaneBinarySearch(b *testing.B) {
	const keyframeCount = 10000
	keyframes := make([]documentv2.Keyframe, keyframeCount)
	for index := range keyframes {
		value := documentv2.Rational{Numerator: int64(index), Denominator: 1}
		keyframes[index] = documentv2.Keyframe{
			Offset: rt(int64(index), 1), Value: documentv2.KeyframeValue{Scalar: &value},
		}
		if index != keyframeCount-1 {
			keyframes[index].Interpolation = documentv2.InterpolationLinear
		}
	}
	lanes, err := compileLanes([]documentv2.KeyframeLane{{
		Target:    documentv2.KeyframeTargetTransformRotation,
		ValueType: documentv2.KeyframeValueScalar,
		Keyframes: keyframes,
	}})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		at := rt(int64((iteration*7919)%keyframeCount), 1)
		if _, err := lanes[0].evaluate(at); err != nil {
			b.Fatal(err)
		}
	}
}
