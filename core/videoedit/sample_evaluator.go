package videoedit

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrSampleRangeInvalid means a requested sample block has a negative count, overflows its
	// signed index representation, or cannot be mapped to exact timeline time.
	ErrSampleRangeInvalid = errors.New("videoedit: sample range is invalid")
	// ErrSampleRangeOutsideRenderRange means a requested block is not wholly contained in the
	// frozen RenderPlan.SampleRange.
	ErrSampleRangeOutsideRenderRange = errors.New("videoedit: sample range is outside render range")
)

// AudioSpan describes one clip's deterministic contribution to a contiguous output sample range.
// It is intentionally block-oriented: memory grows with active clips and cut boundaries, not with
// the number of PCM samples being rendered.
type AudioSpan struct {
	TrackID                ID                         `json:"track_id"`
	ClipID                 ID                         `json:"clip_id"`
	AssetID                ID                         `json:"asset_id"`
	TrackIndex             int                        `json:"track_index"`
	ClipIndex              int                        `json:"clip_index"`
	CompositionSampleRange TickRange                  `json:"composition_sample_range"`
	TimelineStart          RationalTime               `json:"timeline_start"`
	SourceStart            RationalTime               `json:"source_start"`
	SourceTimeStep         RationalTime               `json:"source_time_step"`
	Properties             map[string]json.RawMessage `json:"properties,omitempty"`
}

// SampleEvaluation is the backend-neutral result of evaluating one contiguous PCM block.
type SampleEvaluation struct {
	CompositionSampleRange TickRange   `json:"composition_sample_range"`
	TimelineRange          TimeRange   `json:"timeline_range"`
	Audio                  []AudioSpan `json:"audio,omitempty"`
}

// EvaluateSamples resolves the audio clips contributing to [first, first+count). The requested
// range uses composition-wide sample indices and must be wholly contained in plan.SampleRange.
func EvaluateSamples(plan RenderPlan, first, count int64) (SampleEvaluation, error) {
	verified, err := VerifyRenderPlan(plan)
	if err != nil {
		return SampleEvaluation{}, err
	}
	return EvaluateVerifiedSamples(verified, first, count)
}

// EvaluateVerifiedSamples evaluates a block from an already verified immutable plan.
func EvaluateVerifiedSamples(verified VerifiedRenderPlan, first, count int64) (SampleEvaluation, error) {
	plan, err := verified.value()
	if err != nil {
		return SampleEvaluation{}, err
	}
	requested := TickRange{First: first, Count: count}
	if err := requested.Validate(); err != nil {
		return SampleEvaluation{}, fmt.Errorf("%w: %w", ErrSampleRangeInvalid, err)
	}
	contained, err := tickRangeContains(plan.SampleRange, requested)
	if err != nil {
		return SampleEvaluation{}, fmt.Errorf("%w: %w", ErrSampleRangeInvalid, err)
	}
	if !contained {
		planEnd, _ := plan.SampleRange.EndExclusive()
		requestedEnd, _ := requested.EndExclusive()
		return SampleEvaluation{}, fmt.Errorf(
			"%w: [%d,%d) is outside [%d,%d)",
			ErrSampleRangeOutsideRenderRange,
			requested.First,
			requestedEnd,
			plan.SampleRange.First,
			planEnd,
		)
	}

	timelineRange, err := sampleTickRangeToTimelineRange(plan.Target.SampleClock, requested)
	if err != nil {
		return SampleEvaluation{}, fmt.Errorf("%w: %w", ErrSampleRangeInvalid, err)
	}
	result := SampleEvaluation{
		CompositionSampleRange: requested,
		TimelineRange:          timelineRange,
	}
	if requested.Count == 0 {
		return result, nil
	}

	for _, clip := range plan.Audio {
		clipSamples, err := CompileSampleTickRange(clip.VisibleTimelineRange, plan.Target.SampleClock)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q sample range: %w", clip.ClipID, err)
		}
		spanRange, intersects, err := intersectTickRanges(requested, clipSamples)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q sample intersection: %w", clip.ClipID, err)
		}
		if !intersects {
			continue
		}

		timelineStart, err := SampleTimeAt(plan.Target.SampleClock, spanRange.First)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q timeline start: %w", clip.ClipID, err)
		}
		timelineOffset, err := timelineStart.Sub(clip.TimelineRange.Start)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q timeline offset: %w", clip.ClipID, err)
		}
		sourceOffset, err := scaleTimeByRatio(timelineOffset, clip.SourceRange.Duration, clip.TimelineRange.Duration)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q source offset: %w", clip.ClipID, err)
		}
		sourceStart, err := clip.SourceRange.Start.Add(sourceOffset)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q source start: %w", clip.ClipID, err)
		}
		sourceStep, err := scaleTimeByRatio(
			RationalTime{Value: 1, Rate: plan.Target.SampleClock.SamplesPerSecond},
			clip.SourceRange.Duration,
			clip.TimelineRange.Duration,
		)
		if err != nil {
			return SampleEvaluation{}, fmt.Errorf("videoedit: clip %q source step: %w", clip.ClipID, err)
		}

		result.Audio = append(result.Audio, AudioSpan{
			TrackID:                clip.TrackID,
			ClipID:                 clip.ClipID,
			AssetID:                clip.AssetID,
			TrackIndex:             clip.TrackIndex,
			ClipIndex:              clip.ClipIndex,
			CompositionSampleRange: spanRange,
			TimelineStart:          timelineStart,
			SourceStart:            sourceStart,
			SourceTimeStep:         sourceStep,
			Properties:             cloneRawMap(clip.Properties),
		})
	}
	return result, nil
}

func sampleTickRangeToTimelineRange(clock SampleClock, value TickRange) (TimeRange, error) {
	end, err := value.EndExclusive()
	if err != nil {
		return TimeRange{}, err
	}
	startTime, err := SampleTimeAt(clock, value.First)
	if err != nil {
		return TimeRange{}, err
	}
	endTime, err := SampleTimeAt(clock, end)
	if err != nil {
		return TimeRange{}, err
	}
	duration, err := endTime.Sub(startTime)
	if err != nil {
		return TimeRange{}, err
	}
	return TimeRange{Start: startTime, Duration: duration}, nil
}

func tickRangeContains(outer, inner TickRange) (bool, error) {
	outerEnd, err := outer.EndExclusive()
	if err != nil {
		return false, err
	}
	innerEnd, err := inner.EndExclusive()
	if err != nil {
		return false, err
	}
	return inner.First >= outer.First && innerEnd <= outerEnd, nil
}

func intersectTickRanges(left, right TickRange) (TickRange, bool, error) {
	leftEnd, err := left.EndExclusive()
	if err != nil {
		return TickRange{}, false, err
	}
	rightEnd, err := right.EndExclusive()
	if err != nil {
		return TickRange{}, false, err
	}
	start := left.First
	if right.First > start {
		start = right.First
	}
	end := leftEnd
	if rightEnd < end {
		end = rightEnd
	}
	if end <= start {
		return TickRange{}, false, nil
	}
	return TickRange{First: start, Count: end - start}, true, nil
}
