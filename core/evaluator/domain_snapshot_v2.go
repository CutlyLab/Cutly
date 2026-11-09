package evaluator

import (
	"context"
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const DomainSnapshotProfileV2 = "cutly.evaluator.domain-snapshot.v2"

// DomainSnapshotDescriptorV2 is additive: the V1 descriptor and admission stay unchanged.
func DomainSnapshotDescriptorV2() DomainSnapshotProfileDescriptor {
	descriptor := DomainSnapshotDescriptor()
	descriptor.SchemaVersion = 2
	descriptor.Profile = DomainSnapshotProfileV2
	descriptor.ScopeSemantics = "One visual or audio domain and one timeline; positive exact half-open requested range. ClipIDs equal all enabled same-domain positive visible intersections plus both participants of every selected visual transition. IDs are explicit sets, returned owned and sorted; original rational representations and full-document indexes are retained. Annotation belongs to neither domain."
	descriptor.TransitionSemantics = "Only structurally valid dissolve.v1 visual transitions and their zero-width hard cuts. Visual TransitionIDs equal exactly the positive windows [cut-pre,cut+post) intersecting the request, plus hard cuts whose cut lies in the half-open request; both participants are required even outside visible range. No transitive transition expansion from handle-only clips. Audio requires no transition IDs and ignores authored visual transitions. Unknown, duplicate, omitted and extra IDs fail admission."
	descriptor.EvaluationSemantics = "Compile only explicitly selected clips and transitions from the full frozen graph. Checked visual timeline/FrameClock evaluation emits exact dissolve handle samples or ordinary hard-cut layers. Explicit visual transition lookup is limited to selected IDs, timeline and requested time (outside its active window returns phase/weights only). Audio exposes only checked visible selected clip evaluation, never visual authority. No raw Program is published."
	return descriptor
}

// CompileDomainSnapshotV2 freezes the complete original Document, admitting only the exact
// domain/range closure. offered remains the caller's fixed declaration. No Document is pruned,
// no resource IO is performed, and V1's unconditional transition rejection is not relaxed.
func CompileDomainSnapshotV2(
	ctx context.Context,
	document documentv2.Document,
	scope DomainScope,
	offered videoedit.BackendCapabilities,
	limits SnapshotLimits,
) (*DomainProgram, *FrozenSnapshot, []documentv2.Diagnostic) {
	return compileDomainSnapshot(ctx, document, scope, offered, limits, validateDomainScopeV2)
}

type domainScopeClipV2 struct {
	clip     documentv2.Clip
	kind     documentv2.TrackKind
	expected bool
}

// Called only after the combined non-allocating resource admission and full graph validation.
func validateDomainScopeV2(ctx context.Context, document documentv2.Document, scope DomainScope) *documentv2.Diagnostic {
	invalid := func(path, message string) *documentv2.Diagnostic {
		value := domainSnapshotDiagnostic(CodeDomainSnapshotInvalid, path, message)
		return &value
	}
	if scope.Domain != DomainVisual && scope.Domain != DomainAudio {
		return invalid("/scope/domain", "domain must be visual or audio")
	}
	if scope.RequestedRange.Validate() != nil || scope.RequestedRange.Duration.Value <= 0 {
		return invalid("/scope/requested_range", "requested range must be valid and have positive duration")
	}
	var timeline *documentv2.Timeline
	for index := range document.Timelines {
		if ctx.Err() != nil {
			return invalid("/scope", "domain snapshot compilation canceled")
		}
		if document.Timelines[index].ID == scope.Selection.TimelineID {
			timeline = &document.Timelines[index]
			break
		}
	}
	if timeline == nil || scope.Selection.TimelineID == "" {
		return invalid("/scope/selection/timeline_id", "selected timeline must exist")
	}
	if len(scope.Selection.TransitionIDs) > len(timeline.Transitions) || scope.Domain == DomainAudio && len(scope.Selection.TransitionIDs) != 0 {
		return invalid("/scope/selection/transition_ids", "transition selection exceeds authored visual domain authority")
	}
	// Saturate at caller count; never sum unbounded authored lengths in machine integers.
	count := 0
	for _, track := range timeline.Tracks {
		if ctx.Err() != nil {
			return invalid("/scope", "domain snapshot compilation canceled")
		}
		if len(track.Clips) >= len(scope.Selection.ClipIDs)-count {
			count = len(scope.Selection.ClipIDs)
			break
		}
		count += len(track.Clips)
	}
	if len(scope.Selection.ClipIDs) > count {
		return invalid("/scope/selection/clip_ids", "selected clip count exceeds authored clip count")
	}
	selected := make(map[documentv2.ID]bool, len(scope.Selection.ClipIDs))
	for _, id := range scope.Selection.ClipIDs {
		if ctx.Err() != nil {
			return invalid("/scope", "domain snapshot compilation canceled")
		}
		if _, exists := selected[id]; exists || id == "" {
			return invalid("/scope/selection/clip_ids", "selected clip IDs must be nonempty and unique")
		}
		selected[id] = false
	}
	transitions := make(map[documentv2.ID]bool, len(scope.Selection.TransitionIDs))
	for _, id := range scope.Selection.TransitionIDs {
		if ctx.Err() != nil {
			return invalid("/scope", "domain snapshot compilation canceled")
		}
		if _, exists := transitions[id]; exists || id == "" {
			return invalid("/scope/selection/transition_ids", "selected transition IDs must be nonempty and unique")
		}
		transitions[id] = false
	}
	requestStart, _ := timeBig(scope.RequestedRange.Start)
	requestEndTime, _ := scope.RequestedRange.End()
	requestEnd, _ := timeBig(requestEndTime)
	clips := make(map[documentv2.ID]domainScopeClipV2)
	for _, track := range timeline.Tracks {
		for _, clip := range track.Clips {
			if ctx.Err() != nil {
				return invalid("/scope", "domain snapshot compilation canceled")
			}
			endTime, _ := clip.TimelineRange.End() // Full structural validation checked wire arithmetic.
			start, _ := timeBig(clip.TimelineRange.Start)
			end, _ := timeBig(endTime)
			matches := scope.Domain == DomainVisual && visualTrackKind(track.Kind) || scope.Domain == DomainAudio && track.Kind == documentv2.TrackKindAudio
			expected := matches && !clip.Disabled && clip.TimelineRange.Duration.Value > 0 && start.Cmp(requestEnd) < 0 && end.Cmp(requestStart) > 0
			clips[clip.ID] = domainScopeClipV2{clip: clip, kind: track.Kind, expected: expected}
		}
	}
	for _, transition := range timeline.Transitions {
		if ctx.Err() != nil {
			return invalid("/scope", "domain snapshot compilation canceled")
		}
		outgoing, incoming := clips[transition.OutgoingClipID], clips[transition.IncomingClipID]
		// Do not silently grant future schema transition domains/profiles. Today's validator already
		// rejects audio/annotation transitions, including hard cuts, on the complete Document.
		if transition.Profile != documentv2.TransitionDissolveV1 || !visualTrackKind(outgoing.kind) || outgoing.kind != incoming.kind {
			return invalid("/scope/selection/transition_ids", "unsupported domain transition")
		}
		cutTime, _ := outgoing.clip.TimelineRange.End()
		cut, _ := timeBig(cutTime)
		pre, _ := timeBig(transition.PreDuration)
		post, _ := timeBig(transition.PostDuration)
		start := new(big.Rat).Sub(cut, pre)
		end := new(big.Rat).Add(cut, post)
		expected := false
		if scope.Domain == DomainVisual {
			if start.Cmp(end) == 0 {
				expected = cut.Cmp(requestStart) >= 0 && cut.Cmp(requestEnd) < 0
			} else {
				expected = start.Cmp(requestEnd) < 0 && end.Cmp(requestStart) > 0
			}
		}
		_, included := transitions[transition.ID]
		if included != expected {
			return invalid("/scope/selection/transition_ids", "selection must equal exact same-domain transition intersection with requested range")
		}
		if included {
			transitions[transition.ID] = true
			outgoing.expected, incoming.expected = true, true
			clips[transition.OutgoingClipID], clips[transition.IncomingClipID] = outgoing, incoming
		}
	}
	for _, found := range transitions {
		if !found {
			return invalid("/scope/selection/transition_ids", "selected transition must belong to selected timeline")
		}
	}
	for id, entry := range clips {
		if ctx.Err() != nil {
			return invalid("/scope", "domain snapshot compilation canceled")
		}
		_, included := selected[id]
		if included != entry.expected {
			return invalid("/scope/selection/clip_ids", "selection must equal enabled same-domain visible intersections and selected transition participants")
		}
		if included {
			selected[id] = true
		}
	}
	for _, found := range selected {
		if !found {
			return invalid("/scope/selection/clip_ids", "selected clip must belong to selected timeline")
		}
	}
	return nil
}

// EvaluateTransition authorizes only explicit selected visual transition records at requested
// times. It never grants raw clip/handle access or permits replay in another scope. V1 compiled
// programs have no selected transitions and therefore authorize no transition lookups.
func (program *DomainProgram) EvaluateTransition(timelineID, transitionID documentv2.ID, at documentv2.RationalTime) (result TransitionEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result = TransitionEvaluation{}
			err = evaluationError(CodeInputInvalid, "domain_scope", ErrInputInvalid)
		}
	}()
	if err := program.check(DomainVisual, timelineID, at); err != nil {
		return TransitionEvaluation{}, err
	}
	return program.program.EvaluateTransition(timelineID, transitionID, at)
}
