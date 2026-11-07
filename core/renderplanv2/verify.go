package renderplanv2

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var featurePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9][0-9]*$`)

func validID(id documentv2.ID) bool { return strings.TrimSpace(string(id)) != "" && len(id) <= 1024 }
func sorted[T ~string](v []T) bool {
	if v == nil {
		return false
	}
	for i, s := range v {
		if s == "" || (i > 0 && v[i-1] >= s) {
			return false
		}
	}
	return true
}
func contains[T ~string](v []T, s T) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func validAsset(k documentv2.AssetKind) bool {
	switch k {
	case documentv2.AssetKindVideo, documentv2.AssetKindAudio, documentv2.AssetKindImage, documentv2.AssetKindFont, documentv2.AssetKindData, documentv2.AssetKindText, documentv2.AssetKindVector:
		return true
	}
	return false
}
func validBinding(b ClipBinding) bool {
	if !validID(b.TrackID) || !validID(b.ClipID) || !validID(b.AssetID) || !positive(b.TimelineRange) || !positive(b.SourceRange) {
		return false
	}
	switch b.TrackKind {
	case documentv2.TrackKindVideo:
		if b.AssetKind != documentv2.AssetKindVideo && b.AssetKind != documentv2.AssetKindImage {
			return false
		}
	case documentv2.TrackKindAudio:
		if b.AssetKind != documentv2.AssetKindAudio && b.AssetKind != documentv2.AssetKindVideo {
			return false
		}
	case documentv2.TrackKindText:
		if b.AssetKind != documentv2.AssetKindText {
			return false
		}
	case documentv2.TrackKindVector:
		if b.AssetKind != documentv2.AssetKindVector {
			return false
		}
	default:
		return false
	}
	if b.AssetKind == documentv2.AssetKindText {
		if !validID(b.FontAssetID) {
			return false
		}
	} else if b.FontAssetID != "" {
		return false
	}
	if b.TrackKind != documentv2.TrackKindAudio && b.AudioBusID != "" {
		return false
	}
	return b.AudioBusID == "" || validID(b.AudioBusID)
}
func before(a, b ClipBinding) bool {
	return a.TrackIndex < b.TrackIndex || (a.TrackIndex == b.TrackIndex && a.ClipIndex < b.ClipIndex)
}

func verifyStructure(p Plan) error {
	if p.SchemaVersion != SchemaVersion || p.Profile != ProfileV1 || p.CanonicalJSONProfile != canonicaljson.Profile {
		return invalid("unsupported schema/profile")
	}
	if !validID(p.DocumentID) || !validID(p.TimelineID) || !digestPattern.MatchString(p.DocumentHash) || !digestPattern.MatchString(p.Fingerprint) {
		return invalid("identity")
	}
	if !positive(p.RequestedRange) {
		return invalid("requested range")
	}
	t := p.Target
	if t.TimelineID != p.TimelineID || t.Width <= 0 || int64(t.Width) > 2147483647 || t.Height <= 0 || int64(t.Height) > 2147483647 || t.Channels <= 0 || int64(t.Channels) > 2147483647 || strings.TrimSpace(t.PixelFormat) == "" || strings.TrimSpace(t.ColorSpace) == "" {
		return invalid("target")
	}
	frame, err := videoedit.CompileFrameTickRange(p.RequestedRange, t.FrameClock)
	if err != nil || frame != p.FrameRange {
		return invalid("frame clock/range")
	}
	sample, err := videoedit.CompileSampleTickRange(p.RequestedRange, t.SampleClock)
	if err != nil || sample != p.SampleRange {
		return invalid("sample clock/range")
	}
	caps := p.RequiredCapabilities
	if !sorted(caps.TrackKinds) || !sorted(caps.AssetKinds) || !sorted(caps.Features) || !sorted(p.AudioBusIDs) {
		return invalid("unordered/null capability or bus set")
	}
	for _, k := range caps.TrackKinds {
		switch k {
		case documentv2.TrackKindVideo, documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector:
		default:
			return invalid("track kind")
		}
	}
	for _, k := range caps.AssetKinds {
		if !validAsset(k) {
			return invalid("asset kind")
		}
	}
	for _, f := range caps.Features {
		if !featurePattern.MatchString(string(f)) {
			return invalid("feature token")
		}
	}
	for _, id := range p.AudioBusIDs {
		if !validID(id) {
			return invalid("bus ID")
		}
	}
	if p.Visual == nil || p.Audio == nil || p.Transitions == nil || p.Resources == nil {
		return invalid("null collection")
	}
	resources := map[documentv2.ID]ResourceRef{}
	for i, r := range p.Resources {
		if !validID(r.AssetID) || !validAsset(r.Kind) || r.Kind == documentv2.AssetKindText || r.Kind == documentv2.AssetKindVector || (r.ContentHash != "" && !digestPattern.MatchString(r.ContentHash)) || (i > 0 && p.Resources[i-1].AssetID >= r.AssetID) {
			return invalid("resource identity/order")
		}
		if r.Kind == documentv2.AssetKindFont && r.ContentHash == "" {
			return invalid("unpinned font")
		}
		resources[r.AssetID] = r
	}
	bindings := map[documentv2.ID]ClipBinding{}
	slots := map[[2]uint32]documentv2.ID{}
	tracks := map[uint32]ClipBinding{}
	trackIndices := map[documentv2.ID]uint32{}
	assets := map[documentv2.ID]ClipBinding{}
	usedResources := map[documentv2.ID]bool{}
	addBinding := func(b ClipBinding) error {
		if !validBinding(b) {
			return invalid("clip binding")
		}
		if old, ok := bindings[b.ClipID]; ok && old != b {
			return invalid("conflicting clip binding")
		}
		slot := [2]uint32{b.TrackIndex, b.ClipIndex}
		if id, ok := slots[slot]; ok && id != b.ClipID {
			return invalid("duplicate clip slot")
		}
		slots[slot] = b.ClipID
		if old, ok := tracks[b.TrackIndex]; ok && (old.TrackID != b.TrackID || old.TrackKind != b.TrackKind || old.AudioBusID != b.AudioBusID) {
			return invalid("conflicting track slot")
		}
		tracks[b.TrackIndex] = b
		if index, ok := trackIndices[b.TrackID]; ok && index != b.TrackIndex {
			return invalid("duplicate track ID")
		}
		trackIndices[b.TrackID] = b.TrackIndex
		if old, ok := assets[b.AssetID]; ok && (old.AssetKind != b.AssetKind || old.FontAssetID != b.FontAssetID) {
			return invalid("conflicting asset binding")
		}
		assets[b.AssetID] = b
		if !contains(caps.TrackKinds, b.TrackKind) || !contains(caps.AssetKinds, b.AssetKind) {
			return invalid("missing binding capabilities")
		}
		if b.AudioBusID != "" && !contains(p.AudioBusIDs, b.AudioBusID) {
			return invalid("missing routed bus")
		}
		if b.FontAssetID != "" {
			r, ok := resources[b.FontAssetID]
			if !ok || r.Kind != documentv2.AssetKindFont || !contains(caps.AssetKinds, documentv2.AssetKindFont) {
				return invalid("missing pinned font")
			}
			usedResources[b.FontAssetID] = true
		}
		if b.AssetKind != documentv2.AssetKindText && b.AssetKind != documentv2.AssetKindVector {
			r, ok := resources[b.AssetID]
			if !ok || r.Kind != b.AssetKind {
				return invalid("missing resource")
			}
			usedResources[b.AssetID] = true
		}
		bindings[b.ClipID] = b
		return nil
	}
	ordinary := map[documentv2.ID]bool{}
	for groupIndex, group := range [][]PlannedClip{p.Visual, p.Audio} {
		for i, c := range group {
			b := c.Binding
			if (b.TrackKind == documentv2.TrackKindAudio) != (groupIndex == 1) {
				return invalid("clip in wrong group")
			}
			if ordinary[b.ClipID] || (i > 0 && !before(group[i-1].Binding, b)) {
				return invalid("clip duplicate/order")
			}
			ordinary[b.ClipID] = true
			if err := addBinding(b); err != nil {
				return err
			}
			r, ok, err := intersection(b.TimelineRange, p.RequestedRange)
			if err != nil || !ok || r != c.VisibleTimelineRange {
				return invalid("visible intersection")
			}
			src, err := projected(b, r)
			if err != nil || src != c.VisibleSourceRange {
				return invalid("visible source projection")
			}
		}
	}
	transitionIDs := map[documentv2.ID]bool{}
	transitionOut := map[documentv2.ID]bool{}
	transitionIn := map[documentv2.ID]bool{}
	for i, tr := range p.Transitions {
		if !validID(tr.TransitionID) || tr.Profile != documentv2.TransitionDissolveV1 || (i > 0 && p.Transitions[i-1].TransitionIndex >= tr.TransitionIndex) || tr.PreDuration.Validate() != nil || tr.PostDuration.Validate() != nil || tr.PreDuration.Value < 0 || tr.PostDuration.Value < 0 {
			return invalid("transition identity/order")
		}
		if transitionIDs[tr.TransitionID] {
			return invalid("duplicate transition")
		}
		transitionIDs[tr.TransitionID] = true
		if tr.PreDuration.Value == 0 && tr.PostDuration.Value == 0 {
			return invalid("inert hardcut in active transitions")
		}
		out, in := tr.Outgoing.Binding, tr.Incoming.Binding
		if out.TrackID != in.TrackID || out.TrackIndex != in.TrackIndex || out.TrackKind != in.TrackKind || uint64(out.ClipIndex)+1 != uint64(in.ClipIndex) || out.ClipID == in.ClipID || out.TrackKind == documentv2.TrackKindAudio {
			return invalid("transition participants")
		}
		if transitionOut[out.ClipID] || transitionIn[in.ClipID] {
			return invalid("duplicate transition edge")
		}
		transitionOut[out.ClipID] = true
		transitionIn[in.ClipID] = true
		if less(out.TimelineRange.Duration, tr.PreDuration) || less(in.TimelineRange.Duration, tr.PostDuration) {
			return invalid("transition crosses participant range")
		}
		end, e := out.TimelineRange.End()
		if e != nil || !equalTime(end, tr.Cut) || !equalTime(in.TimelineRange.Start, tr.Cut) {
			return invalid("transition cut")
		}
		whole, e := transitionRange(tr.Cut, tr.PreDuration, tr.PostDuration)
		if e != nil || whole != tr.TimelineRange {
			return invalid("transition window")
		}
		active, ok, e := intersection(whole, p.RequestedRange)
		if e != nil || !ok || active != tr.ActiveTimelineRange {
			return invalid("active transition range")
		}
		if sample, e := documentv2.SampleTransitionWindow(tr.Cut, documentv2.Transition{PreDuration: tr.PreDuration, PostDuration: tr.PostDuration}, active.Start); e != nil || sample.Phase != documentv2.TransitionPhaseActive {
			return invalid("transition phase")
		}
		if !contains(caps.Features, videoedit.CapabilityID(tr.Profile)) {
			return invalid("missing transition capability")
		}
		for _, part := range []TransitionParticipant{tr.Outgoing, tr.Incoming} {
			if e := addBinding(part.Binding); e != nil {
				return e
			}
			expected, e := participant(part.Binding, active)
			if e != nil || !reflect.DeepEqual(expected, part) {
				return invalid("transition sampled/handle spans")
			}
		}
	}
	for id, b := range bindings {
		_, visible, e := intersection(b.TimelineRange, p.RequestedRange)
		if e != nil || visible != ordinary[id] {
			return invalid("participant visible membership")
		}
	}
	if len(usedResources) != len(resources) {
		return invalid("unreferenced resource")
	}
	expected, e := fingerprint(p)
	if e != nil {
		return e
	}
	if expected != p.Fingerprint {
		return invalid("fingerprint mismatch")
	}
	return nil
}
