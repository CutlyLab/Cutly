package transactionv2

import (
	"fmt"
	"reflect"

	d "github.com/CutlyLab/Cutly/core/documentv2"
)

// stateHash binds only the target and its structural location. Removed entities
// additionally bind insertion-slot neighbor IDs. This permits disjoint edits but
// prevents a revert from silently moving/replacing newer conflicting state.
func stateHash(doc *d.Document, op Operation) (string, error) {
	var state any
	entity := func(list any, id d.ID, pos *int) any {
		v := reflect.ValueOf(list)
		index := -1
		for i := 0; i < v.Len(); i++ {
			if v.Index(i).FieldByName("ID").Interface().(d.ID) == id {
				index = i
				break
			}
		}
		if index >= 0 {
			return struct {
				Present  bool
				Position int
				Value    any
			}{true, index, v.Index(index).Interface()}
		}
		slot := -1
		if pos != nil {
			slot = *pos
		}
		left, right := d.ID(""), d.ID("")
		if slot > 0 && slot <= v.Len() {
			left = v.Index(slot - 1).FieldByName("ID").Interface().(d.ID)
		}
		if slot >= 0 && slot < v.Len() {
			right = v.Index(slot).FieldByName("ID").Interface().(d.ID)
		}
		return struct {
			Present     bool
			Slot        int
			LengthValid bool
			Left, Right d.ID
		}{false, slot, slot >= 0 && slot <= v.Len(), left, right}
	}
	switch op.Kind {
	case AssetAdd, AssetRemove, AssetReplace:
		p := op.Asset
		state = entity(doc.Assets, p.AssetID, p.Position)
	case TimelineAdd, TimelineRemove, TimelineReplace:
		p := op.Timeline
		state = entity(doc.Timelines, p.TimelineID, p.Position)
	case BusAdd, BusRemove, BusReplace:
		p := op.Bus
		state = entity(doc.AudioBuses, p.BusID, p.Position)
	case TrackAdd, TrackRemove, TrackReplace:
		p := op.Track
		t, e := timeline(doc, p.TimelineID)
		if e != nil {
			return "", e
		}
		state = entity(t.Tracks, p.TrackID, p.Position)
	case ClipAdd, ClipRemove, ClipReplace, ClipDelete:
		p := op.Clip
		t, _, e := track(doc, p.TrackID)
		if e != nil {
			return "", e
		}
		state = entity(t.Clips, p.ClipID, p.Position)
	case LaneSet, LaneRemove:
		p := op.Lane
		c, _, _, e := clip(doc, p.ClipID)
		if e != nil {
			return "", e
		}
		state = entity(c.KeyframeLanes, p.LaneID, p.Position)
	case TransitionSet, TransitionRemove:
		p := op.Transition
		t, e := timeline(doc, p.TimelineID)
		if e != nil {
			return "", e
		}
		state = entity(t.Transitions, p.TransitionID, p.Position)
	case AnnotationSet, AnnotationRemove:
		p := op.Annotation
		list, e := annotations(doc, p.Scope)
		if e != nil {
			return "", e
		}
		state = entity(*list, p.AnnotationID, p.Position)
	case MetadataSet, MetadataRemove:
		p := op.Metadata
		m, e := metadata(doc, p.Scope)
		if e != nil {
			return "", e
		}
		v, present := (*m)[p.Key]
		state = struct {
			Present bool
			Value   any
		}{present, v}
	case TrackRoute:
		p := op.Route
		t, _, e := track(doc, p.TrackID)
		if e != nil {
			return "", e
		}
		state = t.AudioBusID
	case ClipRetrack:
		p := op.Retrack
		c, t, index, e := clip(doc, p.ClipID)
		if e != nil {
			return "", e
		}
		target, _, e := track(doc, p.TrackID)
		if e != nil {
			return "", e
		}
		state = struct {
			Clip     d.Clip
			Track    d.ID
			Position int
			Slot     any
		}{*c, t.ID, index, entity(target.Clips, p.ClipID, ptr(p.Position))}
	default:
		var id d.ID
		switch op.Kind {
		case ClipMove:
			id = op.Move.ClipID
		case ClipTrim:
			id = op.Trim.ClipID
		case ClipSplit:
			id = op.Split.ClipID
		case ClipSetDisabled:
			id = op.Disabled.ClipID
		case ClipSetAsset:
			id = op.AssetReference.ClipID
		case PropertySet:
			id = op.Properties.ClipID
		default:
			return "", fmt.Errorf("unsupported predicate operation %s", op.Kind)
		}
		c, t, index, e := clip(doc, id)
		if e != nil {
			return "", e
		}
		state = struct {
			Clip     d.Clip
			Track    d.ID
			Position int
		}{*c, t.ID, index}
	}
	// Bind target identity/payload routing as well as the selected postimage.
	target := op
	target.Expect = nil
	target.ID = ""
	// Routing is already checked when locating the typed target; its IDs cannot
	// select a different equal-content entity because every entity includes ID.
	return hashValue(struct {
		DocumentID d.ID
		Kind       Kind
		State      any
	}{doc.ID, target.Kind, state})
}
