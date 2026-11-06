package videoedit

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"
)

func sampleEvaluatorTarget(t *testing.T, sampleRate int64) RenderTarget {
	t.Helper()
	return RenderTarget{
		TimelineID: "timeline-1",
		Width:      1920,
		Height:     1080,
		FrameClock: FrameClock{
			Origin:          mustTime(t, 0, 1),
			FramesPerSecond: mustTime(t, 30, 1),
		},
		SampleClock: SampleClock{
			Origin:           mustTime(t, 0, 1),
			SamplesPerSecond: sampleRate,
		},
		Channels:    2,
		PixelFormat: "yuv420p",
		ColorSpace:  "bt709",
	}
}

func mustResolveSamplePlan(t *testing.T, document Document, requested TimeRange, sampleRate int64) RenderPlan {
	t.Helper()
	plan, diagnostics := Resolve(document, requested, sampleEvaluatorTarget(t, sampleRate))
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	return plan
}

func TestEvaluateSamplesSupportsCommonSampleRatesWithoutPerSampleResults(t *testing.T) {
	for _, sampleRate := range []int64{44100, 48000, 96000} {
		t.Run(strconv.FormatInt(sampleRate, 10), func(t *testing.T) {
			plan := mustResolveSamplePlan(
				t,
				validTestDocument(t),
				TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
				sampleRate,
			)
			evaluated, err := EvaluateSamples(plan, plan.SampleRange.First, plan.SampleRange.Count)
			if err != nil {
				t.Fatal(err)
			}
			if evaluated.CompositionSampleRange.Count != sampleRate {
				t.Fatalf("sample count = %d, want %d", evaluated.CompositionSampleRange.Count, sampleRate)
			}
			if len(evaluated.Audio) != 1 {
				t.Fatalf("audio spans = %+v, want one block span", evaluated.Audio)
			}
			span := evaluated.Audio[0]
			if span.CompositionSampleRange.Count != sampleRate {
				t.Fatalf("span count = %d, want %d", span.CompositionSampleRange.Count, sampleRate)
			}
			if want := (RationalTime{Value: 1, Rate: sampleRate}); span.SourceTimeStep != want {
				t.Fatalf("source step = %+v, want %+v", span.SourceTimeStep, want)
			}
		})
	}
}

func TestEvaluateSamplesUsesHalfOpenClipBoundaries(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[1].Clips = []Clip{
		{
			ID: "audio-a", AssetID: "audio-1",
			TimelineRange: TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		},
		{
			ID: "audio-b", AssetID: "audio-1",
			TimelineRange: TimeRange{Start: mustTime(t, 1, 1), Duration: mustTime(t, 1, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 2, 1), Duration: mustTime(t, 1, 1)},
		},
	}
	plan := mustResolveSamplePlan(
		t,
		document,
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1)},
		10,
	)
	evaluated, err := EvaluateSamples(plan, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluated.Audio) != 2 {
		t.Fatalf("audio spans = %+v", evaluated.Audio)
	}
	if want := (TickRange{First: 0, Count: 10}); evaluated.Audio[0].CompositionSampleRange != want {
		t.Fatalf("first span = %+v, want %+v", evaluated.Audio[0].CompositionSampleRange, want)
	}
	if want := (TickRange{First: 10, Count: 10}); evaluated.Audio[1].CompositionSampleRange != want {
		t.Fatalf("second span = %+v, want %+v", evaluated.Audio[1].CompositionSampleRange, want)
	}
	if evaluated.Audio[1].SourceStart != mustTime(t, 2, 1) {
		t.Fatalf("second source start = %+v", evaluated.Audio[1].SourceStart)
	}
}

func TestEvaluateSamplesSupportsNegativeCompositionIndices(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[1].Clips[0].TimelineRange = TimeRange{
		Start: mustTime(t, -1, 1), Duration: mustTime(t, 2, 1),
	}
	document.Timelines[0].Tracks[1].Clips[0].SourceRange = TimeRange{
		Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1),
	}
	plan := mustResolveSamplePlan(
		t,
		document,
		TimeRange{Start: mustTime(t, -1, 1), Duration: mustTime(t, 1, 1)},
		48000,
	)
	evaluated, err := EvaluateSamples(plan, -48000, 128)
	if err != nil {
		t.Fatal(err)
	}
	if evaluated.TimelineRange.Start != mustTime(t, -1, 1) {
		t.Fatalf("timeline start = %+v", evaluated.TimelineRange.Start)
	}
	if len(evaluated.Audio) != 1 || evaluated.Audio[0].SourceStart != mustTime(t, 0, 1) {
		t.Fatalf("audio spans = %+v", evaluated.Audio)
	}
}

func TestEvaluateSamplesMapsFastAndSlowVarispeed(t *testing.T) {
	tests := []struct {
		name             string
		timelineDuration RationalTime
		sourceDuration   RationalTime
		first            int64
		wantSourceStart  RationalTime
		wantSourceStep   RationalTime
	}{
		{
			name:             "fast two x",
			timelineDuration: mustTime(t, 4, 1),
			sourceDuration:   mustTime(t, 8, 1),
			first:            48000,
			wantSourceStart:  mustTime(t, 2, 1),
			wantSourceStep:   mustTime(t, 1, 24000),
		},
		{
			name:             "slow half x",
			timelineDuration: mustTime(t, 8, 1),
			sourceDuration:   mustTime(t, 4, 1),
			first:            48000,
			wantSourceStart:  mustTime(t, 1, 2),
			wantSourceStep:   mustTime(t, 1, 96000),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := validTestDocument(t)
			document.Timelines[0].Tracks[1].Clips[0].TimelineRange.Duration = tt.timelineDuration
			document.Timelines[0].Tracks[1].Clips[0].SourceRange.Duration = tt.sourceDuration
			plan := mustResolveSamplePlan(
				t,
				document,
				TimeRange{Start: mustTime(t, 0, 1), Duration: tt.timelineDuration},
				48000,
			)
			evaluated, err := EvaluateSamples(plan, tt.first, 256)
			if err != nil {
				t.Fatal(err)
			}
			if len(evaluated.Audio) != 1 {
				t.Fatalf("audio spans = %+v", evaluated.Audio)
			}
			span := evaluated.Audio[0]
			if span.SourceStart != tt.wantSourceStart || span.SourceTimeStep != tt.wantSourceStep {
				t.Fatalf("source mapping = start %+v step %+v, want start %+v step %+v", span.SourceStart, span.SourceTimeStep, tt.wantSourceStart, tt.wantSourceStep)
			}
		})
	}
}

func TestEvaluateSamplesChunkSplitHasExactSourceSeam(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[1].Clips[0].TimelineRange.Duration = mustTime(t, 4, 1)
	document.Timelines[0].Tracks[1].Clips[0].SourceRange.Duration = mustTime(t, 8, 1)
	plan := mustResolveSamplePlan(
		t,
		document,
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 4, 1)},
		48000,
	)
	whole, err := EvaluateSamples(plan, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	left, err := EvaluateSamples(plan, 0, 333)
	if err != nil {
		t.Fatal(err)
	}
	right, err := EvaluateSamples(plan, 333, 667)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Audio) != 1 || len(left.Audio) != 1 || len(right.Audio) != 1 {
		t.Fatalf("unexpected spans: whole=%+v left=%+v right=%+v", whole.Audio, left.Audio, right.Audio)
	}
	if whole.Audio[0].SourceStart != left.Audio[0].SourceStart || whole.Audio[0].SourceTimeStep != left.Audio[0].SourceTimeStep || whole.Audio[0].SourceTimeStep != right.Audio[0].SourceTimeStep {
		t.Fatalf("chunk mapping changed")
	}
	leftDuration, err := scaleTimeByRatio(
		RationalTime{Value: left.Audio[0].CompositionSampleRange.Count, Rate: 1},
		left.Audio[0].SourceTimeStep,
		RationalTime{Value: 1, Rate: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantRightStart, err := left.Audio[0].SourceStart.Add(leftDuration)
	if err != nil {
		t.Fatal(err)
	}
	if right.Audio[0].SourceStart != wantRightStart {
		t.Fatalf("chunk seam = right start %+v, want %+v", right.Audio[0].SourceStart, wantRightStart)
	}
}

func TestEvaluateSamplesPreservesLayerOrderAndClonesProperties(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[1].Clips[0].Properties = map[string]json.RawMessage{
		"cutly.audio.gain.v1": json.RawMessage(`{"value":0.5}`),
	}
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, Track{
		ID:   "audio-track-2",
		Kind: TrackKindAudio,
		Clips: []Clip{{
			ID: "audio-clip-2", AssetID: "audio-1",
			TimelineRange: TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 2, 1), Duration: mustTime(t, 2, 1)},
		}},
	})
	plan := mustResolveSamplePlan(
		t,
		document,
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		48000,
	)
	first, err := EvaluateSamples(plan, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EvaluateSamples(plan, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Audio) != 2 || first.Audio[0].ClipID != "audio-clip" || first.Audio[1].ClipID != "audio-clip-2" {
		t.Fatalf("layer order = %+v", first.Audio)
	}
	first.Audio[0].Properties["cutly.audio.gain.v1"][0] = 'x'
	if string(second.Audio[0].Properties["cutly.audio.gain.v1"]) != `{"value":0.5}` {
		t.Fatalf("evaluation leaked property mutation: %s", second.Audio[0].Properties["cutly.audio.gain.v1"])
	}
}

func TestEvaluateSamplesRejectsInvalidAndOutOfRangeBlocks(t *testing.T) {
	plan := mustResolveSamplePlan(
		t,
		validTestDocument(t),
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		48000,
	)
	tests := []struct {
		name        string
		first       int64
		count       int64
		wantInvalid bool
	}{
		{name: "negative count", first: 0, count: -1, wantInvalid: true},
		{name: "index overflow", first: math.MaxInt64, count: 1, wantInvalid: true},
		{name: "before plan", first: -1, count: 1},
		{name: "past plan", first: 48000, count: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EvaluateSamples(plan, tt.first, tt.count)
			if tt.wantInvalid {
				if !errors.Is(err, ErrSampleRangeInvalid) {
					t.Fatalf("error = %v, want ErrSampleRangeInvalid", err)
				}
				return
			}
			if !errors.Is(err, ErrSampleRangeOutsideRenderRange) {
				t.Fatalf("error = %v, want ErrSampleRangeOutsideRenderRange", err)
			}
		})
	}

	empty, err := EvaluateSamples(plan, 48000, 0)
	if err != nil {
		t.Fatalf("empty block at exclusive end: %v", err)
	}
	if empty.CompositionSampleRange != (TickRange{First: 48000, Count: 0}) || len(empty.Audio) != 0 {
		t.Fatalf("empty block = %+v", empty)
	}
}
