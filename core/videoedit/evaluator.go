package videoedit

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrRenderPlanSchemaUnsupported means an evaluator received a cached or remote plan whose
	// execution schema it cannot safely interpret.
	ErrRenderPlanSchemaUnsupported = errors.New("videoedit: render plan schema is unsupported")
	// ErrRenderPlanSemanticProfileUnsupported means the evaluator does not implement the plan's
	// versioned semantic algorithms.
	ErrRenderPlanSemanticProfileUnsupported = errors.New("videoedit: render plan semantic profile is unsupported")
	// ErrFrameOutsideRenderRange means the composition frame index is outside FrameRange.
	ErrFrameOutsideRenderRange = errors.New("videoedit: frame is outside render range")
)

// EvaluatedClip is one active layer at a specific frame. TimelineTime is absolute document time;
// SourceTime is its exact linear mapping into the authored SourceRange. TrackIndex and ClipIndex
// retain the deterministic compositing/mixing order compiled by Resolve.
type EvaluatedClip struct {
	TrackID      ID                         `json:"track_id"`
	ClipID       ID                         `json:"clip_id"`
	AssetID      ID                         `json:"asset_id"`
	TrackIndex   int                        `json:"track_index"`
	ClipIndex    int                        `json:"clip_index"`
	TimelineTime RationalTime               `json:"timeline_time"`
	SourceTime   RationalTime               `json:"source_time"`
	Properties   map[string]json.RawMessage `json:"properties,omitempty"`
}

// FrameEvaluation is the backend-neutral result of seeking a RenderPlan to one output frame.
// Preview and export backends consume the same value so frame visibility and source-time mapping
// cannot drift between them.
type FrameEvaluation struct {
	CompositionFrameIndex int64           `json:"composition_frame_index"`
	TimelineTime          RationalTime    `json:"timeline_time"`
	Video                 []EvaluatedClip `json:"video,omitempty"`
	Audio                 []EvaluatedClip `json:"audio,omitempty"`
}

// FrameTime returns the absolute timeline time for a composition-wide frame index. It does not
// require the index to lie in this plan's requested range; EvaluateFrame performs that check.
func FrameTime(plan RenderPlan, compositionFrameIndex int64) (RationalTime, error) {
	if err := validateRenderPlanVersion(plan); err != nil {
		return RationalTime{}, err
	}
	return FrameTimeAt(plan.Target.FrameClock, compositionFrameIndex)
}

// SampleTime returns the absolute timeline time for a composition-wide sample index.
func SampleTime(plan RenderPlan, compositionSampleIndex int64) (RationalTime, error) {
	if err := validateRenderPlanVersion(plan); err != nil {
		return RationalTime{}, err
	}
	return SampleTimeAt(plan.Target.SampleClock, compositionSampleIndex)
}

// EvaluateFrame seeks a resolved plan to one composition frame index. Clip and plan ranges are
// half-open, and signed indices are valid when selected by the composition clock.
func EvaluateFrame(plan RenderPlan, compositionFrameIndex int64) (FrameEvaluation, error) {
	verified, err := VerifyRenderPlan(plan)
	if err != nil {
		return FrameEvaluation{}, err
	}
	return EvaluateVerifiedFrame(verified, compositionFrameIndex)
}

// EvaluateVerifiedFrame evaluates an already verified immutable plan without re-hashing or
// re-validating the complete plan for every frame.
func EvaluateVerifiedFrame(verified VerifiedRenderPlan, compositionFrameIndex int64) (FrameEvaluation, error) {
	plan, err := verified.value()
	if err != nil {
		return FrameEvaluation{}, err
	}
	contains, err := plan.FrameRange.Contains(compositionFrameIndex)
	if err != nil {
		return FrameEvaluation{}, err
	}
	if !contains {
		return FrameEvaluation{}, fmt.Errorf("%w: index %d is outside [%d,%d)", ErrFrameOutsideRenderRange, compositionFrameIndex, plan.FrameRange.First, plan.FrameRange.First+plan.FrameRange.Count)
	}
	timelineTime, err := FrameTimeAt(plan.Target.FrameClock, compositionFrameIndex)
	if err != nil {
		return FrameEvaluation{}, fmt.Errorf("videoedit: frame time: %w", err)
	}

	result := FrameEvaluation{CompositionFrameIndex: compositionFrameIndex, TimelineTime: timelineTime}
	result.Video, err = evaluateClipsAt(plan.Video, timelineTime)
	if err != nil {
		return FrameEvaluation{}, fmt.Errorf("videoedit: evaluate video: %w", err)
	}
	result.Audio, err = evaluateClipsAt(plan.Audio, timelineTime)
	if err != nil {
		return FrameEvaluation{}, fmt.Errorf("videoedit: evaluate audio: %w", err)
	}
	return result, nil
}

func validateRenderPlanForEvaluation(plan RenderPlan) error {
	if err := validateRenderPlanVersion(plan); err != nil {
		return err
	}
	if err := plan.RequestedRange.Validate(); err != nil || plan.RequestedRange.Duration.Value <= 0 {
		if err != nil {
			return fmt.Errorf("videoedit: invalid requested range: %w", err)
		}
		return errors.New("videoedit: invalid requested range: duration must be positive")
	}
	if err := plan.Target.FrameClock.Validate(); err != nil {
		return err
	}
	if err := plan.Target.SampleClock.Validate(); err != nil {
		return err
	}
	if err := plan.FrameRange.Validate(); err != nil {
		return err
	}
	if err := plan.SampleRange.Validate(); err != nil {
		return err
	}
	return validateSemanticRenderPlan(plan)
}

func validateRenderPlanVersion(plan RenderPlan) error {
	if plan.SchemaVersion != RenderPlanSchemaVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrRenderPlanSchemaUnsupported, plan.SchemaVersion, RenderPlanSchemaVersion)
	}
	if plan.SemanticProfile != RenderSemanticProfile {
		return fmt.Errorf("%w: got %q, want %q", ErrRenderPlanSemanticProfileUnsupported, plan.SemanticProfile, RenderSemanticProfile)
	}
	return nil
}

func evaluateClipsAt(clips []PlannedClip, timelineTime RationalTime) ([]EvaluatedClip, error) {
	var evaluated []EvaluatedClip
	for _, clip := range clips {
		active, err := rangeContains(clip.VisibleTimelineRange, timelineTime)
		if err != nil {
			return nil, fmt.Errorf("clip %q has invalid visible timeline range: %w", clip.ClipID, err)
		}
		if !active {
			continue
		}
		timelineOffset, err := timelineTime.Sub(clip.TimelineRange.Start)
		if err != nil {
			return nil, fmt.Errorf("clip %q timeline offset: %w", clip.ClipID, err)
		}
		sourceOffset, err := scaleTimeByRatio(timelineOffset, clip.SourceRange.Duration, clip.TimelineRange.Duration)
		if err != nil {
			return nil, fmt.Errorf("clip %q source offset: %w", clip.ClipID, err)
		}
		sourceTime, err := clip.SourceRange.Start.Add(sourceOffset)
		if err != nil {
			return nil, fmt.Errorf("clip %q source time: %w", clip.ClipID, err)
		}
		evaluated = append(evaluated, EvaluatedClip{
			TrackID:      clip.TrackID,
			ClipID:       clip.ClipID,
			AssetID:      clip.AssetID,
			TrackIndex:   clip.TrackIndex,
			ClipIndex:    clip.ClipIndex,
			TimelineTime: timelineTime,
			SourceTime:   sourceTime,
			Properties:   cloneRawMap(clip.Properties),
		})
	}
	return evaluated, nil
}

func rangeContains(value TimeRange, at RationalTime) (bool, error) {
	end, err := value.End()
	if err != nil {
		return false, err
	}
	fromStart, err := at.Compare(value.Start)
	if err != nil {
		return false, err
	}
	beforeEnd, err := at.Compare(end)
	if err != nil {
		return false, err
	}
	return fromStart >= 0 && beforeEnd < 0, nil
}
