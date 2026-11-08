package reconstructv2

import (
	"context"
	"errors"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
)

var ErrUnsupported = errors.New("reconstructv2: unsupported reconstruction scope")

type reconstructionScopes struct {
	visual evaluator.DomainScope
	audio  evaluator.DomainScope
}

// selectScopes is a private support gate AFTER joint Environment verification,
// not a replacement for S1 against-Document verification. Whole-document native
// source compilers require every enabled rendering clip to be in the plan. Reject
// narrower closures rather than prune the Document or read unauthorized resources.
// Native domain preflights must still check their supported rendering properties.
func selectScopes(ctx context.Context, document documentv2.Document, plan renderplanv2.Plan) (result reconstructionScopes, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = reconstructionScopes{}
		}
	}()
	if ctx == nil {
		return result, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if len(document.Timelines) != 1 || document.Timelines[0].ID != plan.TimelineID || len(document.AudioBuses) != 0 || len(plan.AudioBusIDs) != 0 || len(plan.Transitions) != 0 || len(document.Timelines[0].Transitions) != 0 {
		return result, ErrUnsupported
	}
	if plan.RequestedRange.Validate() != nil || plan.RequestedRange.Duration.Value <= 0 || (len(plan.Visual) != 0 && plan.FrameRange.Count == 0) || (len(plan.Audio) != 0 && plan.SampleRange.Count == 0) {
		return result, ErrUnsupported
	}
	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for _, asset := range document.Assets {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if _, exists := assets[asset.ID]; exists {
			return result, ErrUnsupported
		}
		assets[asset.ID] = asset
	}
	selected := make(map[documentv2.ID]renderplanv2.ClipBinding, len(plan.Visual)+len(plan.Audio))
	result.visual = evaluator.DomainScope{Domain: evaluator.DomainVisual, Selection: documentv2.CapabilitySelection{TimelineID: plan.TimelineID, ClipIDs: []documentv2.ID{}}, RequestedRange: plan.RequestedRange}
	result.audio = evaluator.DomainScope{Domain: evaluator.DomainAudio, Selection: documentv2.CapabilitySelection{TimelineID: plan.TimelineID, ClipIDs: []documentv2.ID{}}, RequestedRange: plan.RequestedRange}
	for domain, clips := range [][]renderplanv2.PlannedClip{plan.Visual, plan.Audio} {
		for _, clip := range clips {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			binding := clip.Binding
			if _, exists := selected[binding.ClipID]; exists || (binding.TrackKind == documentv2.TrackKindAudio) != (domain == 1) {
				return result, ErrUnsupported
			}
			selected[binding.ClipID] = binding
			if domain == 0 {
				result.visual.Selection.ClipIDs = append(result.visual.Selection.ClipIDs, binding.ClipID)
			} else {
				result.audio.Selection.ClipIDs = append(result.audio.Selection.ClipIDs, binding.ClipID)
			}
		}
	}
	resources := make(map[documentv2.ID]renderplanv2.ResourceRef)
	addResource := func(id documentv2.ID, kind documentv2.AssetKind) bool {
		asset, exists := assets[id]
		if !exists || asset.Kind != kind || asset.ContentHash == "" {
			return false
		}
		resources[id] = renderplanv2.ResourceRef{AssetID: id, Kind: kind, ContentHash: asset.ContentHash}
		return true
	}
	for ti, track := range document.Timelines[0].Tracks {
		for ci, clip := range track.Clips {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			if clip.Disabled || track.Kind == documentv2.TrackKindAnnotation {
				continue
			}
			asset, exists := assets[clip.AssetID]
			if !exists {
				return result, ErrUnsupported
			}
			binding := renderplanv2.ClipBinding{TrackID: track.ID, ClipID: clip.ID, AssetID: asset.ID, TrackKind: track.Kind, AssetKind: asset.Kind, TrackIndex: uint32(ti), ClipIndex: uint32(ci), TimelineRange: clip.TimelineRange, SourceRange: clip.SourceRange, AudioBusID: track.AudioBusID}
			if asset.Text != nil {
				binding.FontAssetID = asset.Text.FontAssetID
			}
			actual, exists := selected[clip.ID]
			if !exists || actual != binding {
				return result, ErrUnsupported
			}
			delete(selected, clip.ID)
			switch {
			case track.Kind == documentv2.TrackKindVideo && asset.Kind == documentv2.AssetKindImage:
				if !addResource(asset.ID, documentv2.AssetKindImage) {
					return result, ErrUnsupported
				}
			case track.Kind == documentv2.TrackKindAudio && asset.Kind == documentv2.AssetKindAudio:
				if asset.Duration == nil || !addResource(asset.ID, documentv2.AssetKindAudio) {
					return result, ErrUnsupported
				}
			case track.Kind == documentv2.TrackKindText && asset.Kind == documentv2.AssetKindText:
				if asset.Text == nil || asset.Text.Profile != documentv2.TextProfilePinnedFontViewportPXV2 || !addResource(asset.Text.FontAssetID, documentv2.AssetKindFont) {
					return result, ErrUnsupported
				}
			case track.Kind == documentv2.TrackKindVector && asset.Kind == documentv2.AssetKindVector:
				if asset.Vector == nil {
					return result, ErrUnsupported
				}
			default:
				return result, ErrUnsupported
			}
		}
	}
	if len(selected) != 0 || len(resources) != len(plan.Resources) {
		return result, ErrUnsupported
	}
	for _, resource := range plan.Resources {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if expected, exists := resources[resource.AssetID]; !exists || expected != resource {
			return result, ErrUnsupported
		}
		delete(resources, resource.AssetID)
	}
	if len(resources) != 0 {
		return result, ErrUnsupported
	}
	sort.Slice(result.visual.Selection.ClipIDs, func(i, j int) bool { return result.visual.Selection.ClipIDs[i] < result.visual.Selection.ClipIDs[j] })
	sort.Slice(result.audio.Selection.ClipIDs, func(i, j int) bool { return result.audio.Selection.ClipIDs[i] < result.audio.Selection.ClipIDs[j] })
	return result, ctx.Err()
}
