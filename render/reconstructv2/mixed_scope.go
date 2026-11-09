package reconstructv2

import (
	"context"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
)

// selectMixedScopes is deliberately conservative: all enabled rendering clips of
// the single authored timeline must be authorized by the plan. Temporal closure
// is independently checked by the V2 evaluator; transition participants are not
// expanded transitively. Neither the Document nor its original indexes is pruned.
func selectMixedScopes(ctx context.Context, d documentv2.Document, p renderplanv2.Plan) (out reconstructionScopes, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			out = reconstructionScopes{}
		}
	}()
	if ctx == nil {
		return out, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if len(d.Timelines) != 1 || d.Timelines[0].ID != p.TimelineID || p.RequestedRange.Validate() != nil || p.RequestedRange.Duration.Value <= 0 {
		return out, ErrUnsupported
	}
	out.visual = evaluator.DomainScope{Domain: evaluator.DomainVisual, Selection: documentv2.CapabilitySelection{TimelineID: p.TimelineID, ClipIDs: []documentv2.ID{}, TransitionIDs: []documentv2.ID{}}, RequestedRange: p.RequestedRange}
	out.audio = out.visual
	out.audio.Domain = evaluator.DomainAudio
	selected := map[documentv2.ID]renderplanv2.ClipBinding{}
	visual := map[documentv2.ID]bool{}
	add := func(b renderplanv2.ClipBinding, audio bool) bool {
		if old, ok := selected[b.ClipID]; ok {
			return !audio && visual[b.ClipID] && old == b
		}
		if (b.TrackKind == documentv2.TrackKindAudio) != audio {
			return false
		}
		selected[b.ClipID] = b
		if audio {
			out.audio.Selection.ClipIDs = append(out.audio.Selection.ClipIDs, b.ClipID)
		} else {
			visual[b.ClipID] = true
			out.visual.Selection.ClipIDs = append(out.visual.Selection.ClipIDs, b.ClipID)
		}
		return true
	}
	for _, c := range p.Visual {
		if _, ok := selected[c.Binding.ClipID]; ok || !add(c.Binding, false) {
			return out, ErrUnsupported
		}
	}
	for _, c := range p.Audio {
		if _, ok := selected[c.Binding.ClipID]; ok || !add(c.Binding, true) {
			return out, ErrUnsupported
		}
	}
	transitions := map[documentv2.ID]bool{}
	for _, tr := range p.Transitions {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if transitions[tr.TransitionID] || !add(tr.Outgoing.Binding, false) || !add(tr.Incoming.Binding, false) {
			return out, ErrUnsupported
		}
		transitions[tr.TransitionID] = true
		out.visual.Selection.TransitionIDs = append(out.visual.Selection.TransitionIDs, tr.TransitionID)
	}
	assets := map[documentv2.ID]documentv2.Asset{}
	for _, a := range d.Assets {
		if _, ok := assets[a.ID]; ok {
			return out, ErrUnsupported
		}
		assets[a.ID] = a
	}
	resources := map[documentv2.ID]renderplanv2.ResourceRef{}
	addResource := func(id documentv2.ID, kind documentv2.AssetKind) bool {
		a, ok := assets[id]
		if !ok || a.Kind != kind || a.ContentHash == "" {
			return false
		}
		resources[id] = renderplanv2.ResourceRef{AssetID: id, Kind: kind, ContentHash: a.ContentHash}
		return true
	}
	for ti, t := range d.Timelines[0].Tracks {
		for ci, c := range t.Clips {
			if err = ctx.Err(); err != nil {
				return out, err
			}
			if c.Disabled || t.Kind == documentv2.TrackKindAnnotation {
				continue
			}
			a, ok := assets[c.AssetID]
			if !ok {
				return out, ErrUnsupported
			}
			expected := renderplanv2.ClipBinding{TrackID: t.ID, ClipID: c.ID, AssetID: a.ID, TrackKind: t.Kind, AssetKind: a.Kind, TrackIndex: uint32(ti), ClipIndex: uint32(ci), TimelineRange: c.TimelineRange, SourceRange: c.SourceRange, AudioBusID: t.AudioBusID}
			if a.Text != nil {
				expected.FontAssetID = a.Text.FontAssetID
			}
			if actual, ok := selected[c.ID]; !ok || !reflect.DeepEqual(actual, expected) {
				return out, ErrUnsupported
			}
			delete(selected, c.ID)
			switch {
			case t.Kind == documentv2.TrackKindVideo && (a.Kind == documentv2.AssetKindImage || a.Kind == documentv2.AssetKindVideo):
				if !addResource(a.ID, a.Kind) {
					return out, ErrUnsupported
				}
			case t.Kind == documentv2.TrackKindAudio && a.Kind == documentv2.AssetKindAudio:
				if a.Duration == nil || !addResource(a.ID, a.Kind) {
					return out, ErrUnsupported
				}
			case t.Kind == documentv2.TrackKindText && a.Kind == documentv2.AssetKindText:
				if a.Text == nil || a.Text.Profile != documentv2.TextProfilePinnedFontViewportPXV2 || !addResource(a.Text.FontAssetID, documentv2.AssetKindFont) {
					return out, ErrUnsupported
				}
			case t.Kind == documentv2.TrackKindVector && a.Kind == documentv2.AssetKindVector:
				if a.Vector == nil {
					return out, ErrUnsupported
				}
			default:
				return out, ErrUnsupported
			}
		}
	}
	if len(selected) != 0 || len(resources) != len(p.Resources) {
		return out, ErrUnsupported
	}
	for _, r := range p.Resources {
		if resources[r.AssetID] != r {
			return out, ErrUnsupported
		}
		delete(resources, r.AssetID)
	}
	if len(resources) != 0 {
		return out, ErrUnsupported
	}
	sort.Slice(out.visual.Selection.ClipIDs, func(i, j int) bool { return out.visual.Selection.ClipIDs[i] < out.visual.Selection.ClipIDs[j] })
	sort.Slice(out.audio.Selection.ClipIDs, func(i, j int) bool { return out.audio.Selection.ClipIDs[i] < out.audio.Selection.ClipIDs[j] })
	sort.Slice(out.visual.Selection.TransitionIDs, func(i, j int) bool {
		return out.visual.Selection.TransitionIDs[i] < out.visual.Selection.TransitionIDs[j]
	})
	return out, ctx.Err()
}
