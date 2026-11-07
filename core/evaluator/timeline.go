package evaluator

import (
	"errors"
	"math/big"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const VisualLayerOrderBackgroundToForegroundV1 = "cutly.evaluator.visual-layer-order.background-to-foreground.v1"

type compiledTimeline struct {
	id     documentv2.ID
	tracks []compiledTrack
}

type compiledTrack struct {
	id          documentv2.ID
	kind        documentv2.TrackKind
	index       int
	clips       []*compiledClip
	transitions []*compiledTransition
}

func (track *compiledTrack) sortTransitions() {
	sort.Slice(track.transitions, func(left, right int) bool {
		comparison := track.transitions[left].start.Cmp(track.transitions[right].start)
		if comparison != 0 {
			return comparison < 0
		}
		return track.transitions[left].transition.ID < track.transitions[right].transition.ID
	})
}

func (track compiledTrack) activeClip(at *big.Rat) (*compiledClip, bool) {
	// Clips are validated in chronological, non-overlapping order. Find the last start not after
	// at, then apply the half-open end and disabled checks.
	next := sort.Search(len(track.clips), func(index int) bool {
		return track.clips[index].start.Cmp(at) > 0
	})
	if next == 0 {
		return nil, false
	}
	candidate := track.clips[next-1]
	if candidate.disabled || candidate.end.Cmp(at) <= 0 {
		return nil, false
	}
	return candidate, true
}

func (track compiledTrack) activeTransition(at *big.Rat) (*compiledTransition, bool) {
	// Validated non-zero transition windows on one track never overlap, so start order also gives
	// monotonic end order. Compile deliberately omits zero-width hard-cut records from this active
	// interval index while retaining them in Program.transitions for explicit lookup.
	index := sort.Search(len(track.transitions), func(index int) bool {
		return track.transitions[index].end.Cmp(at) > 0
	})
	if index == len(track.transitions) || track.transitions[index].start.Cmp(at) > 0 {
		return nil, false
	}
	return track.transitions[index], true
}

type VisualLayerKind string

const (
	VisualLayerClip       VisualLayerKind = "clip"
	VisualLayerTransition VisualLayerKind = "transition"
)

// VisualLayer is one compositor input. Exactly one of Clip or Transition is present. Transition
// inputs replace the ordinary clip on their track for the complete active window, so outgoing and
// incoming handles are never emitted a second time as standalone layers.
type VisualLayer struct {
	Kind       VisualLayerKind       `json:"kind"`
	TrackID    documentv2.ID         `json:"track_id"`
	TrackIndex int                   `json:"track_index"`
	Clip       *ClipEvaluation       `json:"clip,omitempty"`
	Transition *TransitionEvaluation `json:"transition,omitempty"`
}

// TimelineEvaluation lists visual inputs in deterministic background-to-foreground order. The
// Document track array is the z-order contract: lower indexes are composited first, and higher
// indexes are composited over them. Audio and annotation tracks never enter this list.
type TimelineEvaluation struct {
	TimelineID   documentv2.ID           `json:"timeline_id"`
	TimelineTime documentv2.RationalTime `json:"timeline_time"`
	LayerOrder   string                  `json:"layer_order"`
	Layers       []VisualLayer           `json:"layers,omitempty"`
}

// EvaluateTimeline selects active visual inputs using compiled binary-search indexes. It is safe
// for arbitrary seek order and concurrent readers because Program state is immutable.
func (program *Program) EvaluateTimeline(timelineID documentv2.ID, at documentv2.RationalTime) (TimelineEvaluation, error) {
	if program == nil {
		return TimelineEvaluation{}, evaluationError(CodeProgramLookup, "program", ErrProgramLookup)
	}
	timeline, exists := program.timelines[timelineID]
	if !exists {
		return TimelineEvaluation{}, evaluationError(CodeProgramLookup, "timeline_id", ErrProgramLookup)
	}
	atBig, err := timeBig(at)
	if err != nil {
		return TimelineEvaluation{}, evaluationError(CodeInputInvalid, "timeline_time", ErrInputInvalid)
	}
	result := TimelineEvaluation{
		TimelineID: timelineID, TimelineTime: at,
		LayerOrder: VisualLayerOrderBackgroundToForegroundV1,
	}
	for _, track := range timeline.tracks {
		if !visualTrackKind(track.kind) {
			continue
		}
		if transition, active := track.activeTransition(atBig); active {
			evaluated, evaluationErr := evaluateCompiledTransition(transition, at)
			if evaluationErr != nil {
				return TimelineEvaluation{}, evaluationErr
			}
			result.Layers = append(result.Layers, VisualLayer{
				Kind: VisualLayerTransition, TrackID: track.id, TrackIndex: track.index,
				Transition: &evaluated,
			})
			continue
		}
		clip, active := track.activeClip(atBig)
		if !active {
			continue
		}
		evaluated, evaluationErr := evaluateCompiledClip(clip, at)
		if evaluationErr != nil {
			return TimelineEvaluation{}, evaluationErr
		}
		if !evaluated.Visible {
			return TimelineEvaluation{}, evaluationError(CodeInputInvalid, "timeline.active_clip", ErrInputInvalid)
		}
		result.Layers = append(result.Layers, VisualLayer{
			Kind: VisualLayerClip, TrackID: track.id, TrackIndex: track.index, Clip: &evaluated,
		})
	}
	return result, nil
}

func visualTrackKind(kind documentv2.TrackKind) bool {
	return kind == documentv2.TrackKindVideo || kind == documentv2.TrackKindText || kind == documentv2.TrackKindVector
}

type VisualFrameEvaluation struct {
	CompositionFrameIndex int64              `json:"composition_frame_index"`
	Timeline              TimelineEvaluation `json:"timeline"`
}

// EvaluateVisualFrame is the only frame-index adapter in the v2 evaluator. It delegates to the
// existing exact composition FrameClock implementation; no renderer-local or floating clock is
// introduced.
func (program *Program) EvaluateVisualFrame(timelineID documentv2.ID, clock videoedit.FrameClock, compositionFrameIndex int64) (VisualFrameEvaluation, error) {
	at, err := videoedit.FrameTimeAt(clock, compositionFrameIndex)
	if err != nil {
		if errors.Is(err, videoedit.ErrTimeOverflow) {
			return VisualFrameEvaluation{}, evaluationError(CodeTimeOverflow, "frame_clock", ErrTimeOverflow)
		}
		return VisualFrameEvaluation{}, evaluationError(CodeInputInvalid, "frame_clock", ErrInputInvalid)
	}
	timeline, err := program.EvaluateTimeline(timelineID, at)
	if err != nil {
		return VisualFrameEvaluation{}, err
	}
	return VisualFrameEvaluation{CompositionFrameIndex: compositionFrameIndex, Timeline: timeline}, nil
}
