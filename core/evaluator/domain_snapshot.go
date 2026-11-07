package evaluator

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

// Domain is closed: annotation tracks belong to neither rendering domain.
type Domain string

const (
	DomainVisual Domain = "visual"
	DomainAudio  Domain = "audio"

	DomainSnapshotProfileV1   = "cutly.evaluator.domain-snapshot.v1"
	CodeDomainSnapshotInvalid = "EVALUATOR.V2.DOMAIN_SNAPSHOT_INVALID"
	CodeDomainSnapshotPanic   = "EVALUATOR.V2.DOMAIN_SNAPSHOT_PANIC"
)

// DomainScope describes the complete enabled same-domain clip intersection with RequestedRange
// on one timeline. Clip IDs are a set, not an asset filter. V1 rejects all transitions on that
// timeline and requires a positive requested duration. Rational time representations are retained.
type DomainScope struct {
	Domain         Domain
	Selection      documentv2.CapabilitySelection
	RequestedRange documentv2.TimeRange
}

// DomainSnapshotProfileDescriptor describes this additive contract separately from legacy Compile.
// All fields are values; callers may modify the returned descriptor without affecting the profile.
type DomainSnapshotProfileDescriptor struct {
	SchemaVersion       int    `json:"schema_version"`
	Profile             string `json:"profile"`
	DocumentSemantics   string `json:"document_semantics"`
	ScopeSemantics      string `json:"scope_semantics"`
	CapabilitySemantics string `json:"capability_semantics"`
	EvaluationSemantics string `json:"evaluation_semantics"`
	TransitionSemantics string `json:"transition_semantics"`
	ResourceSemantics   string `json:"resource_semantics"`
	FailureSemantics    string `json:"failure_semantics"`
}

func DomainSnapshotDescriptor() DomainSnapshotProfileDescriptor {
	return DomainSnapshotProfileDescriptor{
		SchemaVersion: 1, Profile: DomainSnapshotProfileV1,
		DocumentSemantics:   "Structurally validate and canonically freeze the complete original Document; preserve its canonical hash and original track and clip indexes. FrozenSnapshot describes the full document, not rendering authority.",
		ScopeSemantics:      "One visual or audio domain and one timeline; positive exact half-open requested range; ClipIDs equal the complete enabled same-domain positive intersection. Scope returns owned, sorted IDs and original rational representations. Annotation tracks belong to neither domain.",
		CapabilitySemantics: "RequiredCapabilitiesForSelection on the full frozen Document, checked against caller-supplied fixed offered capabilities. No global preflight relaxation and no synthesized offers.",
		EvaluationSemantics: "Compile only selected clips from the full frozen graph. Visual exposes checked timeline and exact FrameClock evaluation; audio exposes checked selected clip evaluation. Reject other-domain methods, other timelines, unselected clips, times outside requested range, and audio times outside the clip visible range. No raw Program is published.",
		TransitionSemantics: "V1 rejects all selected transition IDs and every authored transition on the selected timeline, including hard cuts and out-of-range transitions.",
		ResourceSemantics:   "Both SnapshotLimits must be positive. The conservative non-allocating node and encoded-byte admission walk charges the Document, scope and offered capabilities together before validation, canonical allocation or selection-sized allocation; actual full canonical Document bytes are checked again. Limits are logical admission bounds, not RSS or execution deadlines. No resource IO, decoding, rasterization or DSP occurs.",
		FailureSemantics:    "Nil context, invalid limits, invalid graph or scope, missing capability, cancellation and unexpected panic publish neither program nor snapshot. Panic values are never formatted. Evaluations fail with zero results; immutable state and owned getters support concurrent readers. Context is checked throughout bounded traversal and before publication; synchronous validation and canonicalization are not interruptible mid-call.",
	}
}

// DomainProgram is opaque rendering authority for exactly one frozen domain scope. The private
// evaluator cannot be recovered through this API. A nil or zero DomainProgram authorizes nothing.
type DomainProgram struct {
	program *Program
	scope   DomainScope
}

func (program *DomainProgram) DocumentHash() string {
	if program == nil || program.program == nil {
		return ""
	}
	return program.program.DocumentHash()
}

// Scope returns a defensive copy; no caller-owned selection storage is retained at compilation.
func (program *DomainProgram) Scope() DomainScope {
	if program == nil || program.program == nil {
		return DomainScope{}
	}
	return cloneDomainScope(program.scope)
}

func cloneDomainScope(scope DomainScope) DomainScope {
	scope.Selection.ClipIDs = append([]documentv2.ID(nil), scope.Selection.ClipIDs...)
	scope.Selection.TransitionIDs = append([]documentv2.ID(nil), scope.Selection.TransitionIDs...)
	return scope
}

func domainSnapshotDiagnostic(code, path, message string) documentv2.Diagnostic {
	return documentv2.Diagnostic{Code: code, Severity: documentv2.SeverityError, Path: path, Message: message}
}

// CompileDomainSnapshot admits and freezes the ORIGINAL full graph before constructing only the
// checked selection. offered is a fixed backend declaration, never inferred from requirements.
// Legacy Compile and CompileSnapshot retain their whole-document capability behavior.
func CompileDomainSnapshot(
	ctx context.Context,
	document documentv2.Document,
	scope DomainScope,
	offered videoedit.BackendCapabilities,
	limits SnapshotLimits,
) (program *DomainProgram, snapshot *FrozenSnapshot, diagnostics []documentv2.Diagnostic) {
	defer func() {
		if recover() != nil {
			program, snapshot = nil, nil
			diagnostics = []documentv2.Diagnostic{domainSnapshotDiagnostic(CodeDomainSnapshotPanic, "/", "domain snapshot compilation panicked")}
		}
	}()
	invalid := func(path, message string) (*DomainProgram, *FrozenSnapshot, []documentv2.Diagnostic) {
		return nil, nil, []documentv2.Diagnostic{domainSnapshotDiagnostic(CodeDomainSnapshotInvalid, path, message)}
	}
	if ctx == nil || limits.MaxCanonicalDocumentBytes == 0 || limits.MaxDocumentNodes == 0 {
		return invalid("/", "non-nil context and positive snapshot limits are required")
	}
	if ctx.Err() != nil {
		return invalid("/", "domain snapshot compilation canceled")
	}
	measure := snapshotMeasure{ctx: ctx, nodeLimit: limits.MaxDocumentNodes, byteLimit: limits.MaxCanonicalDocumentBytes}
	for _, input := range []any{document, scope, offered} {
		if err := measure.addNodes(1); err != nil {
			return nil, nil, []documentv2.Diagnostic{compileDiagnostic(err)}
		}
		if err := measure.walk(reflect.ValueOf(input)); err != nil {
			return nil, nil, []documentv2.Diagnostic{compileDiagnostic(err)}
		}
	}
	if ctx.Err() != nil {
		return invalid("/", "domain snapshot compilation canceled")
	}
	diagnostics = documentv2.Validate(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	if ctx.Err() != nil {
		return invalid("/", "domain snapshot compilation canceled")
	}
	frozen, frozenSnapshot, diagnostics := freezeSnapshotDocument(ctx, document, limits, diagnostics)
	if frozenSnapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	if diagnostic := validateDomainScope(ctx, frozen, scope); diagnostic != nil {
		return nil, nil, append(diagnostics, *diagnostic)
	}
	// Clone only after admission and independent shape/closure checks; canonicalize set order for
	// stable owned scope discovery without changing exact requested rational representations.
	scope = cloneDomainScope(scope)
	sort.Slice(scope.Selection.ClipIDs, func(i, j int) bool { return scope.Selection.ClipIDs[i] < scope.Selection.ClipIDs[j] })
	required, selectionDiagnostics := documentv2.RequiredCapabilitiesForSelection(ctx, frozen, scope.Selection)
	diagnostics = append(diagnostics, selectionDiagnostics...)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	diagnostics = append(diagnostics, documentv2.CheckCapabilities(required, offered)...)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	compiled, diagnostics := compileFrozenSelection(ctx, frozen, frozenSnapshot.DocumentHash(), diagnostics, &scope)
	if compiled == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	if ctx.Err() != nil {
		return invalid("/", "domain snapshot compilation canceled")
	}
	return &DomainProgram{program: compiled, scope: scope}, frozenSnapshot, diagnostics
}

func validateDomainScope(ctx context.Context, document documentv2.Document, scope DomainScope) *documentv2.Diagnostic {
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
	if len(scope.Selection.TransitionIDs) != 0 || len(timeline.Transitions) != 0 {
		return invalid("/scope/selection/transition_ids", "domain snapshot v1 does not support transitions on the selected timeline")
	}
	// Bound the caller-sized map by an authored clip count before allocation.
	count := 0
	for _, track := range timeline.Tracks {
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
	end, _ := scope.RequestedRange.End()
	for _, track := range timeline.Tracks {
		for _, clip := range track.Clips {
			if ctx.Err() != nil {
				return invalid("/scope", "domain snapshot compilation canceled")
			}
			_, included := selected[clip.ID]
			clipEnd, _ := clip.TimelineRange.End() // Full graph structural validation precedes this call.
			startsBeforeEnd, _ := clip.TimelineRange.Start.Compare(end)
			endsAfterStart, _ := clipEnd.Compare(scope.RequestedRange.Start)
			matchesDomain := scope.Domain == DomainVisual && visualTrackKind(track.Kind) || scope.Domain == DomainAudio && track.Kind == documentv2.TrackKindAudio
			expected := matchesDomain && !clip.Disabled && clip.TimelineRange.Duration.Value > 0 && startsBeforeEnd < 0 && endsAfterStart > 0
			if included != expected {
				return invalid("/scope/selection/clip_ids", "selection must equal enabled same-domain clip intersection with requested range")
			}
			if included {
				selected[clip.ID] = true
			}
		}
	}
	for _, found := range selected {
		if !found {
			return invalid("/scope/selection/clip_ids", "selected clip must belong to selected timeline")
		}
	}
	return nil
}

func (program *DomainProgram) check(domain Domain, timelineID documentv2.ID, at documentv2.RationalTime) error {
	if program == nil || program.program == nil || program.scope.Domain != domain || timelineID != program.scope.Selection.TimelineID {
		return evaluationError(CodeProgramLookup, "domain_scope", ErrProgramLookup)
	}
	contained, err := rangeContains(program.scope.RequestedRange, at)
	if err != nil || !contained {
		return evaluationError(CodeInputInvalid, "domain_scope.requested_range", ErrInputInvalid)
	}
	return nil
}

func (program *DomainProgram) EvaluateTimeline(timelineID documentv2.ID, at documentv2.RationalTime) (result TimelineEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result = TimelineEvaluation{}
			err = evaluationError(CodeInputInvalid, "domain_scope", ErrInputInvalid)
		}
	}()
	if err := program.check(DomainVisual, timelineID, at); err != nil {
		return TimelineEvaluation{}, err
	}
	return program.program.EvaluateTimeline(timelineID, at)
}

func (program *DomainProgram) EvaluateVisualFrame(timelineID documentv2.ID, clock videoedit.FrameClock, compositionFrameIndex int64) (result VisualFrameEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result = VisualFrameEvaluation{}
			err = evaluationError(CodeInputInvalid, "domain_scope", ErrInputInvalid)
		}
	}()
	// The legacy adapter owns the exact FrameClock arithmetic and overflow classification. Its
	// internal program is already scoped; check domain first and requested time before evaluation.
	if program == nil || program.program == nil || program.scope.Domain != DomainVisual || timelineID != program.scope.Selection.TimelineID {
		return VisualFrameEvaluation{}, evaluationError(CodeProgramLookup, "domain_scope", ErrProgramLookup)
	}
	at, timeErr := videoedit.FrameTimeAt(clock, compositionFrameIndex)
	if timeErr != nil {
		return VisualFrameEvaluation{}, domainFrameTimeError(timeErr)
	}
	timeline, err := program.EvaluateTimeline(timelineID, at)
	if err != nil {
		return VisualFrameEvaluation{}, err
	}
	return VisualFrameEvaluation{CompositionFrameIndex: compositionFrameIndex, Timeline: timeline}, nil
}

func domainFrameTimeError(err error) error {
	// FrameTimeAt returns the shared time sentinel, as does the legacy evaluator adapter.
	if errors.Is(err, videoedit.ErrTimeOverflow) {
		return evaluationError(CodeTimeOverflow, "frame_clock", ErrTimeOverflow)
	}
	return evaluationError(CodeInputInvalid, "frame_clock", ErrInputInvalid)
}

// EvaluateClip is audio-only and unlike legacy Program does not authorize transition handles.
func (program *DomainProgram) EvaluateClip(timelineID, clipID documentv2.ID, at documentv2.RationalTime) (result ClipEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result = ClipEvaluation{}
			err = evaluationError(CodeInputInvalid, "domain_scope", ErrInputInvalid)
		}
	}()
	if err := program.check(DomainAudio, timelineID, at); err != nil {
		return ClipEvaluation{}, err
	}
	clip := program.program.clips[timelineID][clipID]
	if clip == nil || clip.disabled {
		return ClipEvaluation{}, evaluationError(CodeProgramLookup, "clip_id", ErrProgramLookup)
	}
	contained, err := rangeContains(clip.timelineRange, at)
	if err != nil || !contained {
		return ClipEvaluation{}, evaluationError(CodeInputInvalid, "clip.timeline_range", ErrInputInvalid)
	}
	return evaluateCompiledClip(clip, at)
}
