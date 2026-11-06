package videoedit

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func evaluatorTarget(t *testing.T, framesPerSecond RationalTime) RenderTarget {
	t.Helper()
	return RenderTarget{
		TimelineID: "timeline-1",
		Width:      1920,
		Height:     1080,
		FrameClock: FrameClock{
			Origin:          mustTime(t, 0, 1),
			FramesPerSecond: framesPerSecond,
		},
		SampleClock: SampleClock{
			Origin:           mustTime(t, 0, 1),
			SamplesPerSecond: 48000,
		},
		Channels:    2,
		PixelFormat: "yuv420p",
		ColorSpace:  "bt709",
	}
}

func mustResolveEvaluatorPlan(t *testing.T, document Document, requested TimeRange, target RenderTarget) RenderPlan {
	t.Helper()
	plan, diagnostics := Resolve(document, requested, target)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	return plan
}

func validManualEvaluatorPlan(t *testing.T) RenderPlan {
	t.Helper()
	requested := TimeRange{Start: mustTime(t, -1, 1), Duration: mustTime(t, 3, 1)}
	target := evaluatorTarget(t, mustTime(t, 30000, 1001))
	frameRange, err := CompileFrameTickRange(requested, target.FrameClock)
	if err != nil {
		t.Fatal(err)
	}
	sampleRange, err := CompileSampleTickRange(requested, target.SampleClock)
	if err != nil {
		t.Fatal(err)
	}
	return RenderPlan{
		SchemaVersion:   RenderPlanSchemaVersion,
		SemanticProfile: RenderSemanticProfile,
		RequestedRange:  requested,
		Target:          target,
		FrameRange:      frameRange,
		SampleRange:     sampleRange,
	}
}

func TestFrameTimeUsesExactCompositionNTSCClockAndSignedIndices(t *testing.T) {
	plan := validManualEvaluatorPlan(t)
	plan.Target.FrameClock.Origin = mustTime(t, 7, 2)

	at, err := FrameTime(plan, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustTime(t, 36001, 10000); at != want {
		t.Fatalf("frame time = %+v, want %+v", at, want)
	}

	negative, err := FrameTime(plan, -3)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustTime(t, 33999, 10000); negative != want {
		t.Fatalf("negative frame time = %+v, want %+v", negative, want)
	}
}

func TestFrameAndSampleTimeRejectPlanVersionAndSemanticProfile(t *testing.T) {
	plan := validManualEvaluatorPlan(t)

	for _, version := range []int{1, 2, RenderPlanSchemaVersion + 1} {
		unsupported := plan
		unsupported.SchemaVersion = version
		if _, err := FrameTime(unsupported, 0); !errors.Is(err, ErrRenderPlanSchemaUnsupported) {
			t.Fatalf("schema %d error = %v, want ErrRenderPlanSchemaUnsupported", version, err)
		}
	}

	unknownProfile := plan
	unknownProfile.SemanticProfile = "cutly.semantic.future"
	if _, err := SampleTime(unknownProfile, 0); !errors.Is(err, ErrRenderPlanSemanticProfileUnsupported) {
		t.Fatalf("profile error = %v, want ErrRenderPlanSemanticProfileUnsupported", err)
	}
}

func TestFrameTimeDetectsOverflow(t *testing.T) {
	plan := validManualEvaluatorPlan(t)
	plan.Target.FrameClock = FrameClock{
		Origin:          mustTime(t, 0, 1),
		FramesPerSecond: RationalTime{Value: 1, Rate: math.MaxInt64},
	}
	if _, err := FrameTime(plan, 2); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("overflow error = %v, want ErrTimeOverflow", err)
	}
}

func TestSampleTimeUsesCompositionClock(t *testing.T) {
	plan := validManualEvaluatorPlan(t)
	plan.Target.SampleClock = SampleClock{
		Origin:           mustTime(t, -1, 2),
		SamplesPerSecond: 48000,
	}
	at, err := SampleTime(plan, 24000)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustTime(t, 0, 1); at != want {
		t.Fatalf("sample time = %+v, want %+v", at, want)
	}
}

func TestEvaluateFrameUsesSignedCompositionIndices(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange = TimeRange{
		Start: mustTime(t, -1, 1), Duration: mustTime(t, 2, 1),
	}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = TimeRange{
		Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1),
	}
	document.Timelines[0].Tracks[1].Clips[0].TimelineRange = TimeRange{
		Start: mustTime(t, -1, 1), Duration: mustTime(t, 2, 1),
	}
	document.Timelines[0].Tracks[1].Clips[0].SourceRange = TimeRange{
		Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1),
	}
	requested := TimeRange{Start: mustTime(t, -1, 1), Duration: mustTime(t, 3, 4)}
	plan := mustResolveEvaluatorPlan(t, document, requested, evaluatorTarget(t, mustTime(t, 2, 1)))
	if want := (TickRange{First: -2, Count: 2}); plan.FrameRange != want {
		t.Fatalf("frame range = %+v, want %+v", plan.FrameRange, want)
	}

	evaluated, err := EvaluateFrame(plan, -2)
	if err != nil {
		t.Fatal(err)
	}
	if evaluated.CompositionFrameIndex != -2 || evaluated.TimelineTime != mustTime(t, -1, 1) {
		t.Fatalf("evaluation clock = %+v", evaluated)
	}
	if len(evaluated.Video) != 1 || evaluated.Video[0].SourceTime != mustTime(t, 0, 1) {
		t.Fatalf("video evaluation = %+v", evaluated.Video)
	}
	if _, err := EvaluateFrame(plan, 0); !errors.Is(err, ErrFrameOutsideRenderRange) {
		t.Fatalf("exclusive end error = %v", err)
	}
}

func TestVerifiedRenderPlanRejectsTamperingAndOwnsImmutableCopy(t *testing.T) {
	plan := mustResolveEvaluatorPlan(
		t,
		validTestDocument(t),
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		evaluatorTarget(t, mustTime(t, 30, 1)),
	)
	verified, err := VerifyRenderPlan(plan)
	if err != nil {
		t.Fatal(err)
	}

	plan.Video[0].Properties = map[string]json.RawMessage{"cutly.test.v1": json.RawMessage(`true`)}
	if _, err := EvaluateFrame(plan, plan.FrameRange.First); !errors.Is(err, ErrRenderPlanFingerprintMismatch) {
		t.Fatalf("tampered plan error = %v, want ErrRenderPlanFingerprintMismatch", err)
	}

	frame, err := EvaluateVerifiedFrame(verified, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Video) != 1 || len(frame.Video[0].Properties) != 0 {
		t.Fatalf("verified plan observed caller mutation: %+v", frame.Video)
	}
	if _, err := EvaluateVerifiedFrame(VerifiedRenderPlan{}, 0); !errors.Is(err, ErrVerifiedRenderPlanRequired) {
		t.Fatalf("zero verified handle error = %v", err)
	}
}

func TestFullAndSubrangePlansShareCompositionGrid(t *testing.T) {
	document := validTestDocument(t)
	target := evaluatorTarget(t, mustTime(t, 2, 1))
	full := mustResolveEvaluatorPlan(
		t,
		document,
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 5, 1)},
		target,
	)
	subrange := mustResolveEvaluatorPlan(
		t,
		document,
		TimeRange{Start: mustTime(t, 3, 4), Duration: mustTime(t, 3, 2)},
		target,
	)
	if want := (TickRange{First: 2, Count: 3}); subrange.FrameRange != want {
		t.Fatalf("subrange frames = %+v, want %+v", subrange.FrameRange, want)
	}

	for index := subrange.FrameRange.First; index < subrange.FrameRange.First+subrange.FrameRange.Count; index++ {
		fromFull, err := EvaluateFrame(full, index)
		if err != nil {
			t.Fatalf("full index %d: %v", index, err)
		}
		fromSubrange, err := EvaluateFrame(subrange, index)
		if err != nil {
			t.Fatalf("subrange index %d: %v", index, err)
		}
		if !reflect.DeepEqual(fromFull, fromSubrange) {
			t.Fatalf("index %d moved between plans:\nfull=%+v\nsub=%+v", index, fromFull, fromSubrange)
		}
	}
}

func TestEvaluateFrameMapsClippedSourceTimeAndUsesHalfOpenBounds(t *testing.T) {
	document := validTestDocument(t)
	requested := TimeRange{Start: mustTime(t, 9, 2), Duration: mustTime(t, 2, 1)}
	plan := mustResolveEvaluatorPlan(t, document, requested, evaluatorTarget(t, mustTime(t, 2, 1)))
	if want := (TickRange{First: 9, Count: 4}); plan.FrameRange != want {
		t.Fatalf("frame range = %+v, want %+v", plan.FrameRange, want)
	}

	first, err := EvaluateFrame(plan, 9)
	if err != nil {
		t.Fatal(err)
	}
	if first.TimelineTime != mustTime(t, 9, 2) {
		t.Fatalf("timeline time = %+v", first.TimelineTime)
	}
	if len(first.Video) != 1 || first.Video[0].SourceTime != mustTime(t, 11, 2) {
		t.Fatalf("first video layers = %+v", first.Video)
	}
	if len(first.Audio) != 1 || first.Audio[0].SourceTime != mustTime(t, 9, 2) {
		t.Fatalf("first audio layers = %+v", first.Audio)
	}

	atVideoEnd, err := EvaluateFrame(plan, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(atVideoEnd.Video) != 0 || len(atVideoEnd.Audio) != 1 {
		t.Fatalf("half-open clip boundary: video=%+v audio=%+v", atVideoEnd.Video, atVideoEnd.Audio)
	}
	if _, err := EvaluateFrame(plan, 8); !errors.Is(err, ErrFrameOutsideRenderRange) {
		t.Fatalf("before-start error = %v", err)
	}
	if _, err := EvaluateFrame(plan, 13); !errors.Is(err, ErrFrameOutsideRenderRange) {
		t.Fatalf("render-end error = %v", err)
	}
}

func TestResolveAndEvaluateFrameUseExactFasterRetimeMapping(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange = TimeRange{
		Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1),
	}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = TimeRange{
		Start: mustTime(t, 0, 1), Duration: mustTime(t, 4, 1),
	}
	requested := TimeRange{Start: mustTime(t, 1, 2), Duration: mustTime(t, 1, 1)}
	plan := mustResolveEvaluatorPlan(t, document, requested, evaluatorTarget(t, mustTime(t, 2, 1)))
	visible := plan.Video[0].VisibleSourceRange
	if visible.Start != mustTime(t, 1, 1) || visible.Duration != mustTime(t, 2, 1) {
		t.Fatalf("visible source range = %+v, want [1,3)", visible)
	}

	evaluated, err := EvaluateFrame(plan, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluated.Video) != 1 || evaluated.Video[0].SourceTime != mustTime(t, 2, 1) {
		t.Fatalf("faster retimed frame = %+v", evaluated.Video)
	}
}

func TestResolveAndEvaluateFrameUseExactSlowerRetimeMapping(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange = TimeRange{
		Start: mustTime(t, 0, 1), Duration: mustTime(t, 4, 1),
	}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = TimeRange{
		Start: mustTime(t, 1, 1), Duration: mustTime(t, 2, 1),
	}
	requested := TimeRange{Start: mustTime(t, 1, 1), Duration: mustTime(t, 2, 1)}
	plan := mustResolveEvaluatorPlan(t, document, requested, evaluatorTarget(t, mustTime(t, 2, 1)))
	visible := plan.Video[0].VisibleSourceRange
	if visible.Start != mustTime(t, 3, 2) || visible.Duration != mustTime(t, 1, 1) {
		t.Fatalf("visible source range = %+v, want [3/2,5/2)", visible)
	}

	evaluated, err := EvaluateFrame(plan, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluated.Video) != 1 || evaluated.Video[0].SourceTime != mustTime(t, 2, 1) {
		t.Fatalf("slower retimed frame = %+v", evaluated.Video)
	}
}

func TestEvaluateFramePreservesLayerOrderAndClonesProperties(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].Properties = map[string]json.RawMessage{
		"cutly.opacity.v1": json.RawMessage(`{"value":0.5}`),
	}
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, Track{
		ID:   "video-track-2",
		Kind: TrackKindVideo,
		Clips: []Clip{{
			ID:            "video-clip-2",
			AssetID:       "video-1",
			TimelineRange: TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 2, 1), Duration: mustTime(t, 2, 1)},
		}},
	})
	plan := mustResolveEvaluatorPlan(
		t,
		document,
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		evaluatorTarget(t, mustTime(t, 30, 1)),
	)

	first, err := EvaluateFrame(plan, plan.FrameRange.First)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EvaluateFrame(plan, plan.FrameRange.First)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Video) != 2 || first.Video[0].ClipID != "video-clip" || first.Video[1].ClipID != "video-clip-2" {
		t.Fatalf("layer order = %+v", first.Video)
	}
	first.Video[0].Properties["cutly.opacity.v1"][0] = 'x'
	if string(second.Video[0].Properties["cutly.opacity.v1"]) != `{"value":0.5}` {
		t.Fatalf("evaluation leaked property mutation: %s", second.Video[0].Properties["cutly.opacity.v1"])
	}
}
