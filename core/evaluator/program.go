package evaluator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

type compiledClip struct {
	timelineID          documentv2.ID
	trackID             documentv2.ID
	id                  documentv2.ID
	assetID             documentv2.ID
	trackIndex          int
	clipIndex           int
	trackKind           documentv2.TrackKind
	disabled            bool
	timelineRange       documentv2.TimeRange
	sourceRange         documentv2.TimeRange
	start               *big.Rat
	end                 *big.Rat
	lanes               []compiledLane
	static              Properties
	renderingExtensions []documentv2.ExtensionProperty
}

type compiledTransition struct {
	timelineID documentv2.ID
	transition documentv2.Transition
	outgoing   *compiledClip
	incoming   *compiledClip
	cut        documentv2.RationalTime
	start      *big.Rat
	end        *big.Rat
}

// Program is an immutable, capability-checked clip/timeline evaluation snapshot. Compile freezes
// a canonical deep copy so an editor can keep mutating its working Document without changing a
// render already in flight. Program intentionally does not expose or execute AudioBuses (including
// a master limiter): a downstream AudioGraph compiler must freeze those fields from the same
// Document and bind its result to DocumentHash. Advertising an audio capability to Compile is a
// claim about that complete downstream backend, not evidence that this package performed DSP.
type Program struct {
	documentHash string
	clips        map[documentv2.ID]map[documentv2.ID]*compiledClip
	transitions  map[documentv2.ID]map[documentv2.ID]*compiledTransition
	timelines    map[documentv2.ID]compiledTimeline
}

// Compile validates schema semantics, performs backend capability preflight, and freezes a
// canonical snapshot. Any error diagnostic returns a nil Program; callers cannot accidentally
// evaluate a document whose rendering fields the selected backend would ignore.
func Compile(document documentv2.Document, offered videoedit.BackendCapabilities) (*Program, []documentv2.Diagnostic) {
	diagnostics := documentv2.PreflightCapabilities(document, offered)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, append(diagnostics, compileDiagnostic(err))
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, append(diagnostics, compileDiagnostic(err))
	}
	hash := canonicalDocumentHash(canonical)
	return compileFrozen(context.Background(), frozen, hash, diagnostics)
}

func canonicalDocumentHash(canonical []byte) string {
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func compileFrozen(
	ctx context.Context,
	frozen documentv2.Document,
	hash string,
	diagnostics []documentv2.Diagnostic,
) (*Program, []documentv2.Diagnostic) {
	return compileFrozenSelection(ctx, frozen, hash, diagnostics, nil)
}

// compileFrozenSelection retains the full frozen graph's positions while compiling only the
// explicitly authorized clips. A nil scope preserves the legacy whole-document compiler.
func compileFrozenSelection(
	ctx context.Context,
	frozen documentv2.Document,
	hash string,
	diagnostics []documentv2.Diagnostic,
	scope *DomainScope,
) (*Program, []documentv2.Diagnostic) {
	var selected, selectedTransitions map[documentv2.ID]struct{}
	if scope != nil {
		selected = make(map[documentv2.ID]struct{}, len(scope.Selection.ClipIDs))
		for _, id := range scope.Selection.ClipIDs {
			selected[id] = struct{}{}
		}
		selectedTransitions = make(map[documentv2.ID]struct{}, len(scope.Selection.TransitionIDs))
		for _, id := range scope.Selection.TransitionIDs {
			selectedTransitions[id] = struct{}{}
		}
	}
	assets := make(map[documentv2.ID]documentv2.Asset, len(frozen.Assets))
	for _, asset := range frozen.Assets {
		assets[asset.ID] = asset
	}
	program := &Program{
		documentHash: hash,
		clips:        make(map[documentv2.ID]map[documentv2.ID]*compiledClip, len(frozen.Timelines)),
		transitions:  make(map[documentv2.ID]map[documentv2.ID]*compiledTransition, len(frozen.Timelines)),
		timelines:    make(map[documentv2.ID]compiledTimeline, len(frozen.Timelines)),
	}
	var visits uint64
	checkpoint := func() error {
		visits++
		if visits&255 == 0 {
			return ctx.Err()
		}
		return nil
	}
	for _, timeline := range frozen.Timelines {
		if err := checkpoint(); err != nil {
			return nil, append(diagnostics, compileDiagnostic(err))
		}
		if scope != nil && timeline.ID != scope.Selection.TimelineID {
			continue
		}
		clipIndex := make(map[documentv2.ID]*compiledClip)
		compiledTracks := make([]compiledTrack, len(timeline.Tracks))
		for trackPosition, track := range timeline.Tracks {
			if err := checkpoint(); err != nil {
				return nil, append(diagnostics, compileDiagnostic(err))
			}
			compiledTracks[trackPosition] = compiledTrack{
				id: track.ID, kind: track.Kind, index: trackPosition,
				clips: make([]*compiledClip, 0, len(track.Clips)),
			}
			for clipPosition, clip := range track.Clips {
				if err := checkpoint(); err != nil {
					return nil, append(diagnostics, compileDiagnostic(err))
				}
				if scope != nil {
					if _, authorized := selected[clip.ID]; !authorized {
						continue
					}
				}
				end, endErr := clip.TimelineRange.End()
				if endErr != nil {
					return nil, append(diagnostics, compileDiagnostic(endErr))
				}
				startBig, startErr := timeBig(clip.TimelineRange.Start)
				if startErr != nil {
					return nil, append(diagnostics, compileDiagnostic(startErr))
				}
				endBig, endBigErr := timeBig(end)
				if endBigErr != nil {
					return nil, append(diagnostics, compileDiagnostic(endBigErr))
				}
				lanes, laneErr := compileLanes(clip.KeyframeLanes)
				if laneErr != nil {
					return nil, append(diagnostics, compileDiagnostic(laneErr))
				}
				staticProperties, staticErr := evaluateStaticProperties(track.Kind, clip, assets[clip.AssetID])
				if staticErr != nil {
					return nil, append(diagnostics, compileDiagnostic(staticErr))
				}
				var renderingExtensions []documentv2.ExtensionProperty
				if clip.PropertySet != nil {
					renderingExtensions = cloneRenderingExtensions(clip.PropertySet.Extensions)
				}
				compiled := &compiledClip{
					timelineID: timeline.ID, trackID: track.ID,
					id: clip.ID, assetID: clip.AssetID, disabled: clip.Disabled,
					trackIndex: trackPosition, clipIndex: clipPosition, trackKind: track.Kind,
					timelineRange: clip.TimelineRange, sourceRange: clip.SourceRange,
					start: startBig, end: endBig, lanes: lanes, static: staticProperties,
					renderingExtensions: renderingExtensions,
				}
				clipIndex[clip.ID] = compiled
				compiledTracks[trackPosition].clips = append(compiledTracks[trackPosition].clips, compiled)
			}
		}
		program.clips[timeline.ID] = clipIndex
		transitionIndex := make(map[documentv2.ID]*compiledTransition, len(timeline.Transitions))
		for _, transition := range timeline.Transitions {
			if err := checkpoint(); err != nil {
				return nil, append(diagnostics, compileDiagnostic(err))
			}
			if scope != nil {
				if _, authorized := selectedTransitions[transition.ID]; !authorized {
					continue
				}
			}
			outgoing := clipIndex[transition.OutgoingClipID]
			incoming := clipIndex[transition.IncomingClipID]
			if outgoing == nil || incoming == nil {
				return nil, append(diagnostics, compileDiagnostic(ErrProgramLookup))
			}
			cut, cutErr := outgoing.timelineRange.End()
			if cutErr != nil {
				return nil, append(diagnostics, compileDiagnostic(cutErr))
			}
			cutBig, cutBigErr := timeBig(cut)
			if cutBigErr != nil {
				return nil, append(diagnostics, compileDiagnostic(cutBigErr))
			}
			preBig, preErr := timeBig(transition.PreDuration)
			if preErr != nil {
				return nil, append(diagnostics, compileDiagnostic(preErr))
			}
			postBig, postErr := timeBig(transition.PostDuration)
			if postErr != nil {
				return nil, append(diagnostics, compileDiagnostic(postErr))
			}
			compiled := &compiledTransition{
				timelineID: timeline.ID, transition: transition,
				outgoing: outgoing, incoming: incoming, cut: cut,
				start: new(big.Rat).Sub(cutBig, preBig), end: new(big.Rat).Add(cutBig, postBig),
			}
			transitionIndex[transition.ID] = compiled
			// A zero-width transition remains addressable through EvaluateTransition so callers can
			// audit the authored hard-cut record, but it can never own a timeline interval. Keeping
			// it out of the active-window index also preserves the monotonic end ordering required
			// by compiledTrack.activeTransition when a following transition starts at the same
			// instant (its pre-duration may consume the complete intervening clip).
			if compiled.start.Cmp(compiled.end) < 0 {
				compiledTracks[outgoing.trackIndex].transitions = append(compiledTracks[outgoing.trackIndex].transitions, compiled)
			}
		}
		for index := range compiledTracks {
			compiledTracks[index].sortTransitions()
		}
		program.transitions[timeline.ID] = transitionIndex
		program.timelines[timeline.ID] = compiledTimeline{id: timeline.ID, tracks: compiledTracks}
	}
	if err := ctx.Err(); err != nil {
		return nil, append(diagnostics, compileDiagnostic(err))
	}
	return program, diagnostics
}

func compileDiagnostic(err error) documentv2.Diagnostic {
	return documentv2.Diagnostic{
		Code: string(CodeCompileInvalid), Severity: documentv2.SeverityError,
		Path: "/", Message: fmt.Sprintf("could not freeze evaluator program: %v", err),
	}
}

func (program *Program) DocumentHash() string {
	if program == nil {
		return ""
	}
	return program.documentHash
}

type ClipEvaluation struct {
	TimelineID   documentv2.ID           `json:"timeline_id"`
	TrackID      documentv2.ID           `json:"track_id"`
	ClipID       documentv2.ID           `json:"clip_id"`
	AssetID      documentv2.ID           `json:"asset_id"`
	TrackIndex   int                     `json:"track_index"`
	ClipIndex    int                     `json:"clip_index"`
	TimelineTime documentv2.RationalTime `json:"timeline_time"`
	LocalTime    documentv2.RationalTime `json:"local_time"`
	SourceTime   documentv2.RationalTime `json:"source_time"`
	Visible      bool                    `json:"visible"`
	Properties   Properties              `json:"properties"`
}

// EvaluateClip evaluates any exact timeline time, including transition handle time outside the
// clip's visible half-open range. Visible tells ordinary layer selection whether the clip itself
// is active; transition evaluation explicitly owns the two exceptional handle samples.
func (program *Program) EvaluateClip(timelineID, clipID documentv2.ID, at documentv2.RationalTime) (ClipEvaluation, error) {
	if program == nil {
		return ClipEvaluation{}, evaluationError(CodeProgramLookup, "program", ErrProgramLookup)
	}
	timeline, exists := program.clips[timelineID]
	if !exists {
		return ClipEvaluation{}, evaluationError(CodeProgramLookup, "timeline_id", ErrProgramLookup)
	}
	clip, exists := timeline[clipID]
	if !exists || clip.disabled {
		return ClipEvaluation{}, evaluationError(CodeProgramLookup, "clip_id", ErrProgramLookup)
	}
	return evaluateCompiledClip(clip, at)
}

func evaluateCompiledClip(compiled *compiledClip, at documentv2.RationalTime) (ClipEvaluation, error) {
	clip := compiled.timeClip()
	local, err := ClipLocalTime(clip, at)
	if err != nil {
		return ClipEvaluation{}, err
	}
	source, err := SourceTime(clip, at)
	if err != nil {
		return ClipEvaluation{}, err
	}
	// The only authorized capability-gated property path is through a Program that already passed
	// Compile's document-provenance capability preflight.
	properties, err := evaluateCompiledProperties(compiled, at)
	if err != nil {
		return ClipEvaluation{}, err
	}
	properties.RenderingExtensions = cloneRenderingExtensions(compiled.renderingExtensions)
	visible, err := rangeContains(compiled.timelineRange, at)
	if err != nil {
		return ClipEvaluation{}, err
	}
	return ClipEvaluation{
		TimelineID: compiled.timelineID, TrackID: compiled.trackID,
		ClipID: compiled.id, AssetID: compiled.assetID,
		TrackIndex: compiled.trackIndex, ClipIndex: compiled.clipIndex,
		TimelineTime: at, LocalTime: local, SourceTime: source, Visible: visible,
		Properties: properties,
	}, nil
}

func (compiled *compiledClip) timeClip() documentv2.Clip {
	return documentv2.Clip{
		ID: compiled.id, AssetID: compiled.assetID, Disabled: compiled.disabled,
		TimelineRange: compiled.timelineRange, SourceRange: compiled.sourceRange,
	}
}

func rangeContains(value documentv2.TimeRange, at documentv2.RationalTime) (bool, error) {
	end, err := value.End()
	if err != nil || at.Validate() != nil {
		return false, evaluationError(CodeInputInvalid, "range", ErrInputInvalid)
	}
	fromStart, err := at.Compare(value.Start)
	if err != nil {
		return false, evaluationError(CodeInputInvalid, "range.start", ErrInputInvalid)
	}
	beforeEnd, err := at.Compare(end)
	if err != nil {
		return false, evaluationError(CodeInputInvalid, "range.end", ErrInputInvalid)
	}
	return fromStart >= 0 && beforeEnd < 0, nil
}

type TransitionEvaluation struct {
	TimelineID     documentv2.ID              `json:"timeline_id"`
	TransitionID   documentv2.ID              `json:"transition_id"`
	Profile        string                     `json:"profile"`
	TimelineTime   documentv2.RationalTime    `json:"timeline_time"`
	Phase          documentv2.TransitionPhase `json:"phase"`
	Progress       *ExactRational             `json:"progress,omitempty"`
	OutgoingWeight ExactRational              `json:"outgoing_weight"`
	IncomingWeight ExactRational              `json:"incoming_weight"`
	Outgoing       *ClipEvaluation            `json:"outgoing,omitempty"`
	Incoming       *ClipEvaluation            `json:"incoming,omitempty"`
}

// EvaluateTransition reuses documentv2.SampleTransitionWindow as the sole interval/progress
// contract. During the active half-open window it evaluates both exact handle samples; outside
// that window it returns phase and weights only, leaving normal clip selection to the caller.
func (program *Program) EvaluateTransition(timelineID, transitionID documentv2.ID, at documentv2.RationalTime) (TransitionEvaluation, error) {
	if program == nil {
		return TransitionEvaluation{}, evaluationError(CodeProgramLookup, "program", ErrProgramLookup)
	}
	timeline, exists := program.transitions[timelineID]
	if !exists {
		return TransitionEvaluation{}, evaluationError(CodeProgramLookup, "timeline_id", ErrProgramLookup)
	}
	compiled, exists := timeline[transitionID]
	if !exists {
		return TransitionEvaluation{}, evaluationError(CodeProgramLookup, "transition_id", ErrProgramLookup)
	}
	return evaluateCompiledTransition(compiled, at)
}

func evaluateCompiledTransition(compiled *compiledTransition, at documentv2.RationalTime) (TransitionEvaluation, error) {
	if err := at.Validate(); err != nil {
		return TransitionEvaluation{}, evaluationError(CodeInputInvalid, "timeline_time", ErrInputInvalid)
	}
	sample, err := documentv2.SampleTransitionWindow(compiled.cut, compiled.transition, at)
	if err != nil {
		// Compile has already validated cut/pre/post and at was checked above. The shared
		// documentv2 contract can therefore fail here only when its exact progress cannot fit
		// the portable int64 Rational wire type.
		return TransitionEvaluation{}, evaluationError(CodeValueNotWireSafe, "transition.progress", ErrValueNotWireSafe)
	}
	zero, one := exactInteger(0), exactInteger(1)
	result := TransitionEvaluation{
		TimelineID: compiled.timelineID, TransitionID: compiled.transition.ID, Profile: compiled.transition.Profile,
		TimelineTime: at, Phase: sample.Phase,
		OutgoingWeight: zero, IncomingWeight: zero,
	}
	switch sample.Phase {
	case documentv2.TransitionPhaseOutgoing:
		result.OutgoingWeight = one
		return result, nil
	case documentv2.TransitionPhaseIncoming:
		result.IncomingWeight = one
		return result, nil
	case documentv2.TransitionPhaseActive:
		if sample.Progress == nil {
			return TransitionEvaluation{}, evaluationError(CodeInputInvalid, "transition.progress", ErrInputInvalid)
		}
		progress, progressErr := exactFromDocument(*sample.Progress)
		if progressErr != nil {
			return TransitionEvaluation{}, progressErr
		}
		progressBig, progressBigErr := progress.big()
		if progressBigErr != nil {
			return TransitionEvaluation{}, progressBigErr
		}
		outgoingWeight := exactFromBig(new(big.Rat).Sub(big.NewRat(1, 1), progressBig))
		outgoing, outgoingErr := evaluateCompiledClip(compiled.outgoing, at)
		if outgoingErr != nil {
			return TransitionEvaluation{}, outgoingErr
		}
		incoming, incomingErr := evaluateCompiledClip(compiled.incoming, at)
		if incomingErr != nil {
			return TransitionEvaluation{}, incomingErr
		}
		result.Progress = &progress
		result.OutgoingWeight = outgoingWeight
		result.IncomingWeight = progress
		result.Outgoing = &outgoing
		result.Incoming = &incoming
		return result, nil
	default:
		return TransitionEvaluation{}, evaluationError(CodeInputInvalid, "transition.phase", ErrInputInvalid)
	}
}
