package renderplanv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/jsonadmission"
)

const (
	CodeRangeInvalid    = "PLAN.V2.RANGE_INVALID"
	CodeTargetInvalid   = "PLAN.V2.TARGET_INVALID"
	CodeTimelineMissing = "PLAN.V2.TIMELINE_NOT_FOUND"
	CodeResolveFailed   = "PLAN.V2.RESOLVE_FAILED"
	CodeCancelled       = "PLAN.V2.CANCELLED"
	CodeResourceLimit   = "PLAN.V2.RESOURCE_LIMIT"

	MaxDocumentAssets          = 16_384
	MaxDocumentTimelines       = 1_024
	MaxDocumentTracks          = 16_384
	MaxDocumentClips           = 16_384
	MaxDocumentTransitions     = 16_384
	MaxDocumentNodes           = 262_144
	MaxCanonicalDocumentBytes  = 128 << 20
	MaxDocumentAdmissionValues = 1 << 22
)

type clipLocation struct {
	track      documentv2.Track
	clip       documentv2.Clip
	trackIndex int
	clipIndex  int
	asset      documentv2.Asset
}

// Resolve validates a committed Document v2 and freezes the exact semantic
// closure selected by requestedRange and target. It performs no I/O.
func Resolve(ctx context.Context, document documentv2.Document, requestedRange documentv2.TimeRange, target videoedit.RenderTarget) (Plan, []documentv2.Diagnostic) {
	if ctx == nil {
		ctx = context.Background()
	}
	plan := newPlan()
	plan.DocumentID = document.ID
	plan.Revision = document.Revision
	plan.RequestedRange = requestedRange
	plan.Target = target

	var diagnostics []documentv2.Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, documentv2.Diagnostic{Code: code, Severity: documentv2.SeverityError, Path: path, Message: message})
	}
	if err := ctx.Err(); err != nil {
		add(CodeCancelled, "/", err.Error())
		return plan, diagnostics
	}
	if message, err := documentSizeError(ctx, document); err != nil {
		add(CodeCancelled, "/", err.Error())
		return plan, diagnostics
	} else if message != "" {
		add(CodeResourceLimit, "/", message)
		return plan, diagnostics
	}
	// The conservative estimate is an explicit worst-case gate for the legacy
	// Document validation/canonicalization phases, which do not accept context.
	// No complete JSON allocation occurs before this bounded traversal.
	if err := jsonadmission.Check(ctx, document, MaxCanonicalDocumentBytes, MaxDocumentAdmissionValues); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			add(CodeCancelled, "/", err.Error())
		} else {
			add(CodeResourceLimit, "/", err.Error())
		}
		return plan, diagnostics
	}
	diagnostics = append(diagnostics, documentv2.Validate(document)...)
	if err := ctx.Err(); err != nil {
		add(CodeCancelled, "/", err.Error())
		return plan, diagnostics
	}
	if err := validatePositiveRange(requestedRange); err != nil {
		add(CodeRangeInvalid, "/requested_range", err.Error())
	}
	validateTarget(target, add)

	timeline, timelineIndex, found := findTimeline(document.Timelines, target.TimelineID)
	if !found {
		add(CodeTimelineMissing, "/target/timeline_id", fmt.Sprintf("timeline %q does not exist", target.TimelineID))
	} else {
		plan.TimelineID = timeline.ID
		plan.Target.TimelineID = timeline.ID
	}
	// Validation has already completed above; avoid Document v2's validating
	// wrapper from repeating the same bounded full-document phase.
	canonicalDocument, err := videoedit.CanonicalJSON(document)
	if err != nil {
		add(CodeResolveFailed, "/document_hash", err.Error())
	} else if len(canonicalDocument) > MaxCanonicalDocumentBytes {
		add(CodeResourceLimit, "/document_hash", fmt.Sprintf("canonical document is %d bytes; limit is %d", len(canonicalDocument), MaxCanonicalDocumentBytes))
	} else {
		digest := sha256.Sum256(canonicalDocument)
		plan.DocumentHash = "sha256:" + hex.EncodeToString(digest[:])
	}
	if err := ctx.Err(); err != nil {
		add(CodeCancelled, "/", err.Error())
		return plan, diagnostics
	}
	if documentv2.HasErrorDiagnostics(diagnostics) || !found {
		return plan, diagnostics
	}

	if frameRange, err := videoedit.CompileFrameTickRange(requestedRange, target.FrameClock); err != nil {
		add(CodeRangeInvalid, "/frame_range", err.Error())
	} else {
		plan.FrameRange = frameRange
	}
	if sampleRange, err := videoedit.CompileSampleTickRange(requestedRange, target.SampleClock); err != nil {
		add(CodeRangeInvalid, "/sample_range", err.Error())
	} else {
		plan.SampleRange = sampleRange
	}
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return plan, diagnostics
	}

	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for index, asset := range document.Assets {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				add(CodeCancelled, "/", err.Error())
				return plan, diagnostics
			}
		}
		assets[asset.ID] = asset
	}
	locations := make(map[documentv2.ID]clipLocation)
	selected := make(map[documentv2.ID]struct{})
	for trackIndex, track := range timeline.Tracks {
		for clipIndex, clip := range track.Clips {
			if clipIndex&255 == 0 {
				if err := ctx.Err(); err != nil {
					add(CodeCancelled, "/", err.Error())
					return plan, diagnostics
				}
			}
			asset := assets[clip.AssetID]
			location := clipLocation{track: track, clip: clip, trackIndex: trackIndex, clipIndex: clipIndex, asset: asset}
			locations[clip.ID] = location
			if clip.Disabled || track.Kind == documentv2.TrackKindAnnotation {
				continue
			}
			visible, ok, intersectionErr := intersectRanges(clip.TimelineRange, requestedRange)
			if intersectionErr != nil {
				add(CodeResolveFailed, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d", timelineIndex, trackIndex, clipIndex), intersectionErr.Error())
				continue
			}
			if !ok {
				continue
			}
			visibleSource, projectionErr := projectRange(clip, visible)
			if projectionErr != nil {
				add(CodeResolveFailed, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/source_range", timelineIndex, trackIndex, clipIndex), projectionErr.Error())
				continue
			}
			planned := plannedClip(location, visible, visibleSource)
			switch track.Kind {
			case documentv2.TrackKindVideo, documentv2.TrackKindText, documentv2.TrackKindVector:
				plan.Visual = append(plan.Visual, planned)
			case documentv2.TrackKindAudio:
				plan.Audio = append(plan.Audio, planned)
			}
			selected[clip.ID] = struct{}{}
		}
	}

	for transitionIndex, transition := range timeline.Transitions {
		if err := ctx.Err(); err != nil {
			add(CodeCancelled, "/", err.Error())
			break
		}
		if transition.PreDuration.Value == 0 && transition.PostDuration.Value == 0 {
			continue
		}
		outgoing, outgoingOK := locations[transition.OutgoingClipID]
		incoming, incomingOK := locations[transition.IncomingClipID]
		if !outgoingOK || !incomingOK {
			continue // Document validation already reports this precisely.
		}
		window, windowErr := transitionWindow(outgoing.clip, transition)
		if windowErr != nil {
			add(CodeResolveFailed, fmt.Sprintf("/timelines/%d/transitions/%d", timelineIndex, transitionIndex), windowErr.Error())
			continue
		}
		selectedWindow, ok, intersectionErr := intersectRanges(window, requestedRange)
		if intersectionErr != nil {
			add(CodeResolveFailed, fmt.Sprintf("/timelines/%d/transitions/%d", timelineIndex, transitionIndex), intersectionErr.Error())
			continue
		}
		if !ok {
			continue
		}
		outgoingSource, outgoingErr := projectRange(outgoing.clip, selectedWindow)
		incomingSource, incomingErr := projectRange(incoming.clip, selectedWindow)
		if outgoingErr != nil || incomingErr != nil {
			add(CodeResolveFailed, fmt.Sprintf("/timelines/%d/transitions/%d", timelineIndex, transitionIndex), fmt.Sprintf("project transition handles: outgoing=%v incoming=%v", outgoingErr, incomingErr))
			continue
		}
		plan.Transitions = append(plan.Transitions, PlannedTransition{
			TransitionID: transition.ID, Profile: transition.Profile, TransitionIndex: transitionIndex,
			PreDuration: transition.PreDuration, PostDuration: transition.PostDuration,
			Window: window, SelectedWindow: selectedWindow,
			Outgoing: transitionSide(outgoing, outgoingSource), Incoming: transitionSide(incoming, incomingSource),
		})
		selected[outgoing.clip.ID] = struct{}{}
		selected[incoming.clip.ID] = struct{}{}
	}
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return plan, diagnostics
	}

	selectedDocument, selectionErr := buildSelectedDocument(ctx, document, timeline, selected, plan.Transitions, assets)
	if selectionErr != nil {
		add(CodeCancelled, "/", selectionErr.Error())
		return plan, diagnostics
	}
	if err := ctx.Err(); err != nil {
		add(CodeCancelled, "/", err.Error())
		return plan, diagnostics
	}
	required, requirementDiagnostics := documentv2.RequiredCapabilities(selectedDocument)
	if err := ctx.Err(); err != nil {
		add(CodeCancelled, "/", err.Error())
		return plan, diagnostics
	}
	if documentv2.HasErrorDiagnostics(requirementDiagnostics) {
		for _, diagnostic := range requirementDiagnostics {
			diagnostic.Code = CodeResolveFailed
			diagnostic.Path = "/selected_document" + diagnostic.Path
			diagnostics = append(diagnostics, diagnostic)
		}
		return plan, diagnostics
	}
	plan.RequiredCapabilities = required
	plan.Resources = collectResources(selectedDocument)

	fingerprint, fingerprintErr := fingerprint(plan)
	if fingerprintErr != nil {
		add(CodeResolveFailed, "/fingerprint", fingerprintErr.Error())
		return plan, diagnostics
	}
	plan.Fingerprint = fingerprint
	if _, err := Verify(plan); err != nil {
		add(CodeResolveFailed, "/", fmt.Sprintf("compiled plan failed self-verification: %v", err))
	}
	return plan, diagnostics
}

func validateTarget(target videoedit.RenderTarget, add func(string, string, string)) {
	if strings.TrimSpace(string(target.TimelineID)) == "" {
		add(CodeTargetInvalid, "/target/timeline_id", "timeline_id is required")
	}
	if target.Width <= 0 || target.Height <= 0 || target.Channels <= 0 ||
		target.Width > math.MaxInt32 || target.Height > math.MaxInt32 || target.Channels > math.MaxInt32 {
		add(CodeTargetInvalid, "/target", "width, height and channels must be positive signed 32-bit integers")
	}
	if strings.TrimSpace(target.PixelFormat) == "" || strings.TrimSpace(target.ColorSpace) == "" {
		add(CodeTargetInvalid, "/target", "pixel_format and color_space are required")
	}
	if err := target.FrameClock.Validate(); err != nil {
		add(CodeTargetInvalid, "/target/frame_clock", err.Error())
	}
	if err := target.SampleClock.Validate(); err != nil {
		add(CodeTargetInvalid, "/target/sample_clock", err.Error())
	}
}

func findTimeline(values []documentv2.Timeline, id documentv2.ID) (documentv2.Timeline, int, bool) {
	for index, value := range values {
		if value.ID == id {
			return value, index, true
		}
	}
	return documentv2.Timeline{}, -1, false
}

func plannedClip(location clipLocation, visible, source documentv2.TimeRange) PlannedClip {
	return PlannedClip{
		TrackID: location.track.ID, ClipID: location.clip.ID, AssetID: location.clip.AssetID,
		TrackKind: location.track.Kind, AssetKind: location.asset.Kind,
		TrackIndex: location.trackIndex, ClipIndex: location.clipIndex,
		TimelineRange: location.clip.TimelineRange, SourceRange: location.clip.SourceRange,
		VisibleTimelineRange: visible, VisibleSourceRange: source,
	}
}

func transitionSide(location clipLocation, source documentv2.TimeRange) TransitionSide {
	return TransitionSide{
		TrackID: location.track.ID, ClipID: location.clip.ID, AssetID: location.clip.AssetID,
		TrackKind: location.track.Kind, AssetKind: location.asset.Kind,
		TrackIndex: location.trackIndex, ClipIndex: location.clipIndex,
		SelectedSourceRange: source,
	}
}

func transitionWindow(outgoing documentv2.Clip, transition documentv2.Transition) (documentv2.TimeRange, error) {
	cut, err := outgoing.TimelineRange.End()
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	start, err := cut.Sub(transition.PreDuration)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	duration, err := transition.PreDuration.Add(transition.PostDuration)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	window := documentv2.TimeRange{Start: start, Duration: duration}
	return window, validatePositiveRange(window)
}

func projectRange(clip documentv2.Clip, selected documentv2.TimeRange) (documentv2.TimeRange, error) {
	offset, err := selected.Start.Sub(clip.TimelineRange.Start)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	sourceOffset, err := scaleTime(offset, clip.SourceRange.Duration, clip.TimelineRange.Duration)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	sourceStart, err := clip.SourceRange.Start.Add(sourceOffset)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	sourceDuration, err := scaleTime(selected.Duration, clip.SourceRange.Duration, clip.TimelineRange.Duration)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	return documentv2.TimeRange{Start: sourceStart, Duration: sourceDuration}, nil
}

func scaleTime(value, numerator, denominator documentv2.RationalTime) (documentv2.RationalTime, error) {
	if value.Validate() != nil || numerator.Validate() != nil || denominator.Validate() != nil || denominator.Value <= 0 {
		return documentv2.RationalTime{}, errors.New("invalid rational time ratio")
	}
	result := new(big.Rat).Mul(
		new(big.Rat).SetFrac64(value.Value, value.Rate),
		new(big.Rat).Quo(new(big.Rat).SetFrac64(numerator.Value, numerator.Rate), new(big.Rat).SetFrac64(denominator.Value, denominator.Rate)),
	)
	if !result.Num().IsInt64() || !result.Denom().IsInt64() {
		return documentv2.RationalTime{}, videoedit.ErrTimeOverflow
	}
	return documentv2.RationalTime{Value: result.Num().Int64(), Rate: result.Denom().Int64()}, nil
}

func intersectRanges(left, right documentv2.TimeRange) (documentv2.TimeRange, bool, error) {
	leftEnd, err := left.End()
	if err != nil {
		return documentv2.TimeRange{}, false, err
	}
	rightEnd, err := right.End()
	if err != nil {
		return documentv2.TimeRange{}, false, err
	}
	start := left.Start
	if comparison, compareErr := right.Start.Compare(start); compareErr != nil {
		return documentv2.TimeRange{}, false, compareErr
	} else if comparison > 0 {
		start = right.Start
	}
	end := leftEnd
	if comparison, compareErr := rightEnd.Compare(end); compareErr != nil {
		return documentv2.TimeRange{}, false, compareErr
	} else if comparison < 0 {
		end = rightEnd
	}
	duration, err := end.Sub(start)
	if err != nil {
		return documentv2.TimeRange{}, false, err
	}
	comparison, err := duration.Compare(documentv2.RationalTime{Value: 0, Rate: 1})
	if err != nil {
		return documentv2.TimeRange{}, false, err
	}
	if comparison <= 0 {
		return documentv2.TimeRange{}, false, nil
	}
	return documentv2.TimeRange{Start: start, Duration: duration}, true, nil
}

func validatePositiveRange(value documentv2.TimeRange) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Duration.Value <= 0 {
		return errors.New("duration must be positive")
	}
	return nil
}

func buildSelectedDocument(ctx context.Context, document documentv2.Document, timeline documentv2.Timeline, selected map[documentv2.ID]struct{}, transitions []PlannedTransition, assets map[documentv2.ID]documentv2.Asset) (documentv2.Document, error) {
	result := documentv2.Document{SchemaVersion: document.SchemaVersion, ID: document.ID, Revision: document.Revision}
	selectedTransitions := make(map[documentv2.ID]struct{}, len(transitions))
	for _, transition := range transitions {
		selectedTransitions[transition.TransitionID] = struct{}{}
	}
	selectedAssets := make(map[documentv2.ID]struct{})
	selectedAudio := false
	filteredTimeline := documentv2.Timeline{ID: timeline.ID}
	for trackIndex, track := range timeline.Tracks {
		if trackIndex&255 == 0 && ctx.Err() != nil {
			return documentv2.Document{}, ctx.Err()
		}
		filteredTrack := documentv2.Track{ID: track.ID, Kind: track.Kind, AudioBusID: track.AudioBusID}
		for _, clip := range track.Clips {
			if _, ok := selected[clip.ID]; !ok {
				continue
			}
			copy := clip
			copy.Annotations = nil
			filteredTrack.Clips = append(filteredTrack.Clips, copy)
			selectedAssets[clip.AssetID] = struct{}{}
			if track.Kind == documentv2.TrackKindAudio {
				selectedAudio = true
			}
		}
		if len(filteredTrack.Clips) > 0 {
			filteredTimeline.Tracks = append(filteredTimeline.Tracks, filteredTrack)
		}
	}
	for _, transition := range timeline.Transitions {
		if _, ok := selectedTransitions[transition.ID]; ok {
			filteredTimeline.Transitions = append(filteredTimeline.Transitions, transition)
		}
	}
	assetVisit := 0
	for assetID := range selectedAssets {
		if assetVisit&255 == 0 && ctx.Err() != nil {
			return documentv2.Document{}, ctx.Err()
		}
		assetVisit++
		asset := assets[assetID]
		asset.Metadata = nil
		result.Assets = append(result.Assets, asset)
		if asset.Kind == documentv2.AssetKindText && asset.Text != nil {
			selectedAssets[asset.Text.FontAssetID] = struct{}{}
		}
	}
	// The loop above may discover indirect pinned fonts. Append them after direct
	// assets, then restore original Document order for canonical validation.
	assetVisit = 0
	for assetID := range selectedAssets {
		if assetVisit&255 == 0 && ctx.Err() != nil {
			return documentv2.Document{}, ctx.Err()
		}
		assetVisit++
		found := false
		for _, asset := range result.Assets {
			if asset.ID == assetID {
				found = true
				break
			}
		}
		if !found {
			asset := assets[assetID]
			asset.Metadata = nil
			result.Assets = append(result.Assets, asset)
		}
	}
	order := make(map[documentv2.ID]int, len(document.Assets))
	for index, asset := range document.Assets {
		order[asset.ID] = index
	}
	sort.Slice(result.Assets, func(i, j int) bool { return order[result.Assets[i].ID] < order[result.Assets[j].ID] })
	result.Timelines = []documentv2.Timeline{filteredTimeline}
	if selectedAudio {
		result.AudioBuses = append([]documentv2.AudioBus(nil), document.AudioBuses...)
	}
	return result, nil
}

func documentSizeError(ctx context.Context, document documentv2.Document) (string, error) {
	if len(document.Assets) > MaxDocumentAssets {
		return fmt.Sprintf("asset count %d exceeds limit %d", len(document.Assets), MaxDocumentAssets), nil
	}
	if len(document.Timelines) > MaxDocumentTimelines {
		return fmt.Sprintf("timeline count %d exceeds limit %d", len(document.Timelines), MaxDocumentTimelines), nil
	}
	nodes, tracks, clips, transitions := 1, 0, 0, 0
	addNodes := func(count int) bool {
		if count < 0 || count > MaxDocumentNodes-nodes {
			return false
		}
		nodes += count
		return true
	}
	if !addNodes(len(document.Assets)) || !addNodes(len(document.AudioBuses)) || !addNodes(len(document.Annotations)) || !addNodes(len(document.Timelines)) {
		return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
	}
	var visits uint64
	checkpoint := func() error {
		visits++
		if visits == 1 || visits&255 == 0 {
			return ctx.Err()
		}
		return nil
	}
	for _, asset := range document.Assets {
		if err := checkpoint(); err != nil {
			return "", err
		}
		if asset.Vector != nil {
			if !addNodes(len(asset.Vector.Paths)) {
				return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
			}
			for _, path := range asset.Vector.Paths {
				if !addNodes(len(path.Commands)) {
					return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
				}
				for _, command := range path.Commands {
					if !addNodes(len(command.Points)) {
						return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
					}
				}
			}
		}
	}
	for _, timeline := range document.Timelines {
		if err := checkpoint(); err != nil {
			return "", err
		}
		if len(timeline.Tracks) > MaxDocumentTracks-tracks {
			return fmt.Sprintf("track count exceeds limit %d", MaxDocumentTracks), nil
		}
		tracks += len(timeline.Tracks)
		if len(timeline.Transitions) > MaxDocumentTransitions-transitions {
			return fmt.Sprintf("transition count exceeds limit %d", MaxDocumentTransitions), nil
		}
		transitions += len(timeline.Transitions)
		if !addNodes(len(timeline.Tracks)) || !addNodes(len(timeline.Transitions)) || !addNodes(len(timeline.Annotations)) {
			return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
		}
		for _, track := range timeline.Tracks {
			if err := checkpoint(); err != nil {
				return "", err
			}
			if len(track.Clips) > MaxDocumentClips-clips {
				return fmt.Sprintf("clip count exceeds limit %d", MaxDocumentClips), nil
			}
			clips += len(track.Clips)
			if !addNodes(len(track.Clips)) || !addNodes(len(track.Annotations)) {
				return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
			}
			for _, clip := range track.Clips {
				if !addNodes(len(clip.KeyframeLanes)) || !addNodes(len(clip.Annotations)) {
					return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
				}
				if clip.PropertySet != nil && !addNodes(len(clip.PropertySet.Extensions)) {
					return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
				}
				for _, lane := range clip.KeyframeLanes {
					if !addNodes(len(lane.Keyframes)) {
						return fmt.Sprintf("document node count exceeds limit %d", MaxDocumentNodes), nil
					}
				}
			}
		}
	}
	return "", nil
}

func collectResources(selected documentv2.Document) []ResourceRef {
	seen := make(map[documentv2.ID]ResourceRef)
	for _, asset := range selected.Assets {
		if asset.Kind == documentv2.AssetKindText || asset.Kind == documentv2.AssetKindVector {
			continue
		}
		if asset.URI == "" && asset.ContentHash == "" {
			continue
		}
		seen[asset.ID] = ResourceRef{AssetID: asset.ID, Kind: asset.Kind, ContentHash: asset.ContentHash}
	}
	result := make([]ResourceRef, 0, len(seen))
	for _, resource := range seen {
		result = append(result, resource)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AssetID < result[j].AssetID })
	return result
}
