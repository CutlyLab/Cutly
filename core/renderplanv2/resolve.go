package renderplanv2

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	CodeInvalid  = "RENDER_PLAN.V2.INVALID"
	CodeCanceled = "RENDER_PLAN.V2.CANCELED"
	CodeLimit    = "RENDER_PLAN.V2.LIMIT"
)

func diagnostic(err error) []documentv2.Diagnostic {
	code := CodeInvalid
	if err == ErrLimit {
		code = CodeLimit
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		code = CodeCanceled
	}
	return []documentv2.Diagnostic{{Code: code, Severity: documentv2.SeverityError, Path: "", Message: err.Error()}}
}

// Resolve validates the entire document before selecting enabled, intersecting
// clips and active transition participants. All failures return a zero Plan.
// It performs no I/O; caller-owned documents must not be mutated during the call.
func Resolve(ctx context.Context, document documentv2.Document, requested documentv2.TimeRange, target videoedit.RenderTarget) (result Plan, diagnostics []documentv2.Diagnostic) {
	defer func() {
		if r := recover(); r != nil {
			result = Plan{}
			diagnostics = diagnostic(fmt.Errorf("renderplanv2: resolver panic: %v", r))
		}
	}()
	if err := admission(ctx, document, MaxDocumentBytes); err != nil {
		return Plan{}, diagnostic(err)
	}
	if ds := documentv2.Validate(document); documentv2.HasErrorDiagnostics(ds) {
		return Plan{}, ds
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, diagnostic(err)
	}
	if !positive(requested) {
		return Plan{}, diagnostic(invalid("requested range"))
	}
	var timeline *documentv2.Timeline
	for i := range document.Timelines {
		if target.TimelineID == document.Timelines[i].ID || (target.TimelineID == "" && len(document.Timelines) == 1) {
			timeline = &document.Timelines[i]
			break
		}
	}
	if timeline == nil {
		return Plan{}, diagnostic(invalid("timeline must be unique or explicit"))
	}
	target.TimelineID = timeline.ID
	p := Plan{SchemaVersion: SchemaVersion, Profile: ProfileV1, CanonicalJSONProfile: canonicaljson.Profile, DocumentID: document.ID, Revision: document.Revision, TimelineID: timeline.ID, RequestedRange: requested, Target: target, Visual: []PlannedClip{}, Audio: []PlannedClip{}, Transitions: []PlannedTransition{}, AudioBusIDs: []documentv2.ID{}, Resources: []ResourceRef{}}
	var err error
	p.FrameRange, err = videoedit.CompileFrameTickRange(requested, target.FrameClock)
	if err != nil {
		return Plan{}, diagnostic(err)
	}
	p.SampleRange, err = videoedit.CompileSampleTickRange(requested, target.SampleClock)
	if err != nil {
		return Plan{}, diagnostic(err)
	}
	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for _, a := range document.Assets {
		assets[a.ID] = a
	}
	bindings := map[documentv2.ID]ClipBinding{}
	selected := map[documentv2.ID]bool{}
	resourceIDs := map[documentv2.ID]bool{}
	busIDs := map[documentv2.ID]bool{}
	buses := map[documentv2.ID]documentv2.AudioBus{}
	for _, b := range document.AudioBuses {
		buses[b.ID] = b
	}
	add := func(b ClipBinding) {
		selected[b.ClipID] = true
		if b.AssetKind == documentv2.AssetKindText {
			resourceIDs[b.FontAssetID] = true
		} else if b.AssetKind != documentv2.AssetKindVector {
			resourceIDs[b.AssetID] = true
		}
		if b.TrackKind == documentv2.TrackKindAudio {
			for id := b.AudioBusID; id != "" && !busIDs[id]; {
				busIDs[id] = true
				id = buses[id].ParentBusID
			}
		}
	}
	for ti, track := range timeline.Tracks {
		for ci, clip := range track.Clips {
			if err := ctx.Err(); err != nil {
				return Plan{}, diagnostic(err)
			}
			if clip.Disabled || track.Kind == documentv2.TrackKindAnnotation {
				continue
			}
			a := assets[clip.AssetID]
			b := ClipBinding{TrackID: track.ID, ClipID: clip.ID, AssetID: a.ID, TrackKind: track.Kind, AssetKind: a.Kind, TrackIndex: uint32(ti), ClipIndex: uint32(ci), TimelineRange: clip.TimelineRange, SourceRange: clip.SourceRange, AudioBusID: track.AudioBusID}
			if a.Text != nil {
				b.FontAssetID = a.Text.FontAssetID
			}
			bindings[b.ClipID] = b
			visible, ok, e := intersection(clip.TimelineRange, requested)
			if e != nil {
				return Plan{}, diagnostic(e)
			}
			if !ok {
				continue
			}
			source, e := projected(b, visible)
			if e != nil {
				return Plan{}, diagnostic(e)
			}
			planned := PlannedClip{Binding: b, VisibleTimelineRange: visible, VisibleSourceRange: source}
			if track.Kind == documentv2.TrackKindAudio {
				p.Audio = append(p.Audio, planned)
			} else {
				p.Visual = append(p.Visual, planned)
			}
			add(b)
		}
	}
	transitionIDs := []documentv2.ID{}
	for i, tr := range timeline.Transitions {
		if err := ctx.Err(); err != nil {
			return Plan{}, diagnostic(err)
		}
		if tr.PreDuration.Value == 0 && tr.PostDuration.Value == 0 {
			continue
		}
		out, in := bindings[tr.OutgoingClipID], bindings[tr.IncomingClipID]
		cut, e := out.TimelineRange.End()
		if e != nil {
			return Plan{}, diagnostic(e)
		}
		whole, e := transitionRange(cut, tr.PreDuration, tr.PostDuration)
		if e != nil {
			return Plan{}, diagnostic(e)
		}
		active, ok, e := intersection(whole, requested)
		if e != nil {
			return Plan{}, diagnostic(e)
		}
		if !ok {
			continue
		}
		outgoing, e := participant(out, active)
		if e != nil {
			return Plan{}, diagnostic(e)
		}
		incoming, e := participant(in, active)
		if e != nil {
			return Plan{}, diagnostic(e)
		}
		p.Transitions = append(p.Transitions, PlannedTransition{TransitionID: tr.ID, TransitionIndex: uint32(i), Profile: tr.Profile, Cut: cut, PreDuration: tr.PreDuration, PostDuration: tr.PostDuration, TimelineRange: whole, ActiveTimelineRange: active, Outgoing: outgoing, Incoming: incoming})
		transitionIDs = append(transitionIDs, tr.ID)
		add(out)
		add(in)
	}
	clips := make([]documentv2.ID, 0, len(selected))
	for id := range selected {
		clips = append(clips, id)
	}
	sort.Slice(clips, func(i, j int) bool { return clips[i] < clips[j] })
	req, ds := documentv2.RequiredCapabilitiesForSelection(ctx, document, documentv2.CapabilitySelection{TimelineID: timeline.ID, ClipIDs: clips, TransitionIDs: transitionIDs})
	if documentv2.HasErrorDiagnostics(ds) {
		return Plan{}, ds
	}
	p.RequiredCapabilities = CapabilityRequirements{TrackKinds: append([]documentv2.TrackKind{}, req.TrackKinds...), AssetKinds: append([]documentv2.AssetKind{}, req.AssetKinds...), Features: append([]videoedit.CapabilityID{}, req.Features...)}
	for id := range resourceIDs {
		a := assets[id]
		p.Resources = append(p.Resources, ResourceRef{AssetID: id, Kind: a.Kind, ContentHash: a.ContentHash})
	}
	sort.Slice(p.Resources, func(i, j int) bool { return p.Resources[i].AssetID < p.Resources[j].AssetID })
	for id := range busIDs {
		p.AudioBusIDs = append(p.AudioBusIDs, id)
	}
	sort.Slice(p.AudioBusIDs, func(i, j int) bool { return p.AudioBusIDs[i] < p.AudioBusIDs[j] })
	if err := ctx.Err(); err != nil {
		return Plan{}, diagnostic(err)
	}
	// Preserve the original document's historical canonical profile (including
	// non-integer metadata); never hash a pruned or integer-normalized document.
	p.DocumentHash, err = documentv2.DocumentHash(document)
	if err != nil {
		return Plan{}, diagnostic(err)
	}
	if err := admission(ctx, p, MaxPlanBytes); err != nil {
		return Plan{}, diagnostic(err)
	}
	p.Fingerprint, err = fingerprint(p)
	if err != nil {
		return Plan{}, diagnostic(err)
	}
	if _, err = Verify(p); err != nil {
		return Plan{}, diagnostic(err)
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, diagnostic(err)
	}
	return p, nil
}

// VerifyAgainstDocument proves complete authored closure by re-resolving and
// comparing canonical bytes. Standalone Verify alone cannot prove an omitted
// clip, transition, bus ancestor, property capability or document hash is honest.
func VerifyAgainstDocument(ctx context.Context, p Plan, document documentv2.Document) error {
	if ctx == nil {
		return invalid("nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := Canonical(p)
	if err != nil {
		return err
	}
	expected, ds := Resolve(ctx, document, p.RequestedRange, p.Target)
	if documentv2.HasErrorDiagnostics(ds) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", ErrDocumentMismatch, ds[0].Message)
	}
	canonical, err := Canonical(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return ErrDocumentMismatch
	}
	return ctx.Err()
}
