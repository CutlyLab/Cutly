package transactionv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	d "github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

type opError struct{ code, message string }

func (e opError) Error() string    { return e.message }
func fault(code, msg string) error { return opError{code, msg} }

// Apply never mutates or aliases either argument. Failure returns only diagnostics
// or an error; all candidate/inverse/hash fields are zero, including on panic or
// cancellation. The candidate is revision N+1; only the store may publish it.
func Apply(ctx context.Context, document d.Document, tx Transaction) (result Result, err error) {
	defer func() {
		if recover() != nil {
			result = Result{}
			err = ErrInternal
		}
		if err != nil {
			result = Result{}
		}
	}()
	if ctx == nil {
		return Result{}, errors.New("transactionv2: nil context")
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	reject := func(code, path, msg string) (Result, error) {
		return Result{Diagnostics: []d.Diagnostic{diagnostic(code, path, msg)}}, nil
	}
	if err = boundedValue(document, MaxDocumentBytes); err != nil {
		return reject(CodeLimit, "/document", err.Error())
	}
	if ds := d.Validate(document); d.HasErrorDiagnostics(ds) {
		return Result{Diagnostics: ds}, nil
	}
	if ds := Validate(tx); d.HasErrorDiagnostics(ds) {
		return Result{Diagnostics: ds}, nil
	}
	if document.ID != tx.DocumentID {
		return reject(CodeDocumentConflict, "/document_id", "transaction targets a different document")
	}
	if document.Revision != tx.ExpectedRevision {
		return reject(CodeRevisionConflict, "/expected_revision", "stale revision")
	}
	hash, e := d.DocumentHash(document)
	if e != nil {
		return Result{}, e
	}
	if hash != tx.ExpectedDocumentHash {
		return reject(CodeHashConflict, "/expected_document_hash", "base document hash mismatch")
	}
	if document.Revision == ^d.Revision(0) {
		return reject(CodeRevisionOverflow, "/revision", "revision overflow")
	}
	// Canonical ownership also normalizes open JSON without changing number spelling.
	raw, e := videoedit.CanonicalJSON(document)
	if e != nil {
		return Result{}, e
	}
	var working d.Document
	if e = json.Unmarshal(raw, &working); e != nil {
		return Result{}, e
	}
	txRaw, e := CanonicalJSON(tx)
	if e != nil {
		return Result{}, e
	}
	var ownedTx Transaction
	if e = json.Unmarshal(txRaw, &ownedTx); e != nil {
		return Result{}, e
	}
	tx = ownedTx
	groups := make([][]Operation, 0, len(tx.Operations))
	inverseBytes, inverseCount := 0, 0
	for i, op := range tx.Operations {
		if e = ctx.Err(); e != nil {
			return Result{}, e
		}
		if op.Expect != nil {
			actual, se := stateHash(&working, op)
			if se != nil || actual != op.Expect.Hash {
				return reject(CodePrecondition, fmt.Sprintf("/operations/%d/expect", i), "target no longer matches the recorded postimage")
			}
		}
		inverses, se := mutate(&working, op)
		if se != nil {
			code := CodeOperationInvalid
			var oe opError
			if errors.As(se, &oe) {
				code = oe.code
			}
			return reject(code, fmt.Sprintf("/operations/%d", i), se.Error())
		}
		// A split removes the right clip before replacing the left. Their
		// predicates are independent (the left index is unchanged by removal).
		// All other groups contain exactly one inverse: no full-document clone
		// per operation is necessary, even for 128 MiB bases.
		if se = boundedValue(inverses, MaxTransactionBytes-inverseBytes); se != nil {
			return reject(CodeLimit, "/inverse", se.Error())
		}
		for j := range inverses {
			h, he := stateHash(&working, inverses[j])
			if he != nil {
				return Result{}, he
			}
			inverses[j].Expect = &Precondition{Hash: h}
		}
		encoded, se := json.Marshal(inverses)
		if se != nil {
			return Result{}, se
		}
		inverseBytes += len(encoded)
		inverseCount += len(inverses)
		if inverseBytes > MaxTransactionBytes || inverseCount > MaxOperations {
			return reject(CodeLimit, "/inverse", "complete inverse exceeds transaction profile")
		}
		ownedInverses, se := clone(inverses)
		if se != nil {
			return Result{}, se
		}
		groups = append(groups, ownedInverses)
		if e = boundedValue(working, MaxDocumentBytes); e != nil {
			return reject(CodeLimit, "/document", e.Error())
		}
	}
	working.Revision++
	if ds := d.Validate(working); d.HasErrorDiagnostics(ds) {
		return Result{Diagnostics: ds}, nil
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	committed, e := d.DocumentHash(working)
	if e != nil {
		return Result{}, e
	}
	digest, e := Digest(tx)
	if e != nil {
		return Result{}, e
	}
	inverse := Transaction{ID: "inverse-" + digest[7:], IdempotencyKey: "inverse-" + digest[7:]}
	for i := len(groups) - 1; i >= 0; i-- {
		inverse.Operations = append(inverse.Operations, groups[i]...)
	}
	for i := range inverse.Operations {
		inverse.Operations[i].ID = fmt.Sprintf("inverse-%d", i)
	}
	inverse, e = Create(working, inverse)
	if e != nil {
		return reject(CodeLimit, "/inverse", e.Error())
	}
	impact := renderImpact(document, working)
	// Detach inverse, candidate and input from one another, including raw slices.
	owned, e := clone(inverse)
	if e != nil {
		return Result{}, e
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	return Result{Document: working, Inverse: owned, Impact: impact, DocumentHash: committed, TransactionDigest: digest}, nil
}
func clone[T any](v T) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func ptr[T any](v T) *T { return &v }

func mutate(doc *d.Document, op Operation) ([]Operation, error) {
	one := func(v Operation) ([]Operation, error) { return []Operation{v}, nil }
	switch op.Kind {
	case AssetAdd, AssetRemove, AssetReplace:
		p := op.Asset
		mode := entityMode(op.Kind)
		old, pos, err := edit(&doc.Assets, p.AssetID, p.Position, p.Value, mode, func(x d.Asset) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		return one(Operation{Kind: inverseKind(op.Kind), Asset: &AssetEdit{AssetID: p.AssetID, Position: pos, Value: old}})
	case TimelineAdd, TimelineRemove, TimelineReplace:
		p := op.Timeline
		old, pos, err := edit(&doc.Timelines, p.TimelineID, p.Position, p.Value, entityMode(op.Kind), func(x d.Timeline) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		return one(Operation{Kind: inverseKind(op.Kind), Timeline: &TimelineEdit{TimelineID: p.TimelineID, Position: pos, Value: old}})
	case TrackAdd, TrackRemove, TrackReplace:
		p := op.Track
		t, err := timeline(doc, p.TimelineID)
		if err != nil {
			return nil, err
		}
		old, pos, err := edit(&t.Tracks, p.TrackID, p.Position, p.Value, entityMode(op.Kind), func(x d.Track) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		return one(Operation{Kind: inverseKind(op.Kind), Track: &TrackEdit{TimelineID: p.TimelineID, TrackID: p.TrackID, Position: pos, Value: old}})
	case ClipAdd, ClipRemove, ClipReplace, ClipDelete:
		p := op.Clip
		t, _, err := track(doc, p.TrackID)
		if err != nil {
			return nil, err
		}
		old, pos, err := edit(&t.Clips, p.ClipID, p.Position, p.Value, entityMode(op.Kind), func(x d.Clip) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		return one(Operation{Kind: inverseKind(op.Kind), Clip: &ClipEdit{TrackID: p.TrackID, ClipID: p.ClipID, Position: pos, Value: old}})
	case BusAdd, BusRemove, BusReplace:
		p := op.Bus
		old, pos, err := edit(&doc.AudioBuses, p.BusID, p.Position, p.Value, entityMode(op.Kind), func(x d.AudioBus) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		return one(Operation{Kind: inverseKind(op.Kind), Bus: &BusEdit{BusID: p.BusID, Position: pos, Value: old}})
	case LaneSet, LaneRemove:
		p := op.Lane
		c, _, _, err := clip(doc, p.ClipID)
		if err != nil {
			return nil, err
		}
		mode := setMode(op.Kind, c.KeyframeLanes, p.LaneID, func(x d.KeyframeLane) d.ID { return x.ID })
		old, pos, err := edit(&c.KeyframeLanes, p.LaneID, p.Position, p.Value, mode, func(x d.KeyframeLane) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		kind := LaneSet
		if mode == "add" {
			kind = LaneRemove
		}
		return one(Operation{Kind: kind, Lane: &LaneEdit{ClipID: p.ClipID, LaneID: p.LaneID, Position: pos, Value: old}})
	case TransitionSet, TransitionRemove:
		p := op.Transition
		t, err := timeline(doc, p.TimelineID)
		if err != nil {
			return nil, err
		}
		mode := setMode(op.Kind, t.Transitions, p.TransitionID, func(x d.Transition) d.ID { return x.ID })
		old, pos, err := edit(&t.Transitions, p.TransitionID, p.Position, p.Value, mode, func(x d.Transition) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		kind := TransitionSet
		if mode == "add" {
			kind = TransitionRemove
		}
		return one(Operation{Kind: kind, Transition: &TransitionEdit{TimelineID: p.TimelineID, TransitionID: p.TransitionID, Position: pos, Value: old}})
	case AnnotationSet, AnnotationRemove:
		p := op.Annotation
		list, err := annotations(doc, p.Scope)
		if err != nil {
			return nil, err
		}
		mode := setMode(op.Kind, *list, p.AnnotationID, func(x d.Annotation) d.ID { return x.ID })
		old, pos, err := edit(list, p.AnnotationID, p.Position, p.Value, mode, func(x d.Annotation) d.ID { return x.ID })
		if err != nil {
			return nil, err
		}
		kind := AnnotationSet
		if mode == "add" {
			kind = AnnotationRemove
		}
		return one(Operation{Kind: kind, Annotation: &AnnotationEdit{Scope: p.Scope, AnnotationID: p.AnnotationID, Position: pos, Value: old}})
	case MetadataSet, MetadataRemove:
		p := op.Metadata
		m, err := metadata(doc, p.Scope)
		if err != nil {
			return nil, err
		}
		old, present := (*m)[p.Key]
		if op.Kind == MetadataRemove && !present {
			return nil, fault(CodeMissing, "metadata key missing")
		}
		if op.Kind == MetadataRemove {
			delete(*m, p.Key)
		} else {
			if *m == nil {
				*m = map[string]json.RawMessage{}
			}
			(*m)[p.Key] = p.Value
		}
		kind := MetadataRemove
		if present {
			kind = MetadataSet
		}
		return one(Operation{Kind: kind, Metadata: &MetadataEdit{Scope: p.Scope, Key: p.Key, Value: old}})
	case TrackRoute:
		p := op.Route
		t, _, err := track(doc, p.TrackID)
		if err != nil {
			return nil, err
		}
		old := t.AudioBusID
		t.AudioBusID = p.BusID
		return one(Operation{Kind: TrackRoute, Route: &RouteEdit{TrackID: p.TrackID, BusID: old}})
	case ClipRetrack:
		p := op.Retrack
		c, source, pos, err := clip(doc, p.ClipID)
		if err != nil {
			return nil, err
		}
		target, _, err := track(doc, p.TrackID)
		if err != nil {
			return nil, err
		}
		value := *c
		oldTrack := source.ID
		source.Clips = append(source.Clips[:pos], source.Clips[pos+1:]...)
		if p.Position < 0 || p.Position > len(target.Clips) {
			return nil, fault(CodeOperationInvalid, "invalid retrack position")
		}
		target.Clips = insert(target.Clips, p.Position, value)
		return one(Operation{Kind: ClipRetrack, Retrack: &RetrackEdit{ClipID: p.ClipID, TrackID: oldTrack, Position: pos}})
	case ClipMove, ClipTrim, ClipSplit, ClipSetDisabled, ClipSetAsset, PropertySet:
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
		}
		c, t, pos, err := clip(doc, id)
		if err != nil {
			return nil, err
		}
		old := *c
		inverse := Operation{Kind: ClipReplace, Clip: &ClipEdit{TrackID: t.ID, ClipID: id, Position: ptr(pos), Value: &old}}
		switch op.Kind {
		case ClipMove:
			c.TimelineRange.Start = op.Move.At
		case ClipTrim:
			c.TimelineRange = op.Trim.TimelineRange
			c.SourceRange = op.Trim.SourceRange
		case ClipSetDisabled:
			c.Disabled = op.Disabled.Disabled
		case ClipSetAsset:
			c.AssetID = op.AssetReference.AssetID
		case PropertySet:
			c.PropertySet = op.Properties.Value
		case ClipSplit:
			if err = checkSplit(old, *op.Split); err != nil {
				return nil, err
			}
			if _, _, _, e := clip(doc, op.Split.Right.ID); e == nil {
				return nil, fault(CodeConflict, "split right clip already exists")
			}
			*c = op.Split.Left
			t.Clips = insert(t.Clips, pos+1, op.Split.Right)
			return []Operation{{Kind: ClipRemove, Clip: &ClipEdit{TrackID: t.ID, ClipID: op.Split.Right.ID}}, inverse}, nil
		}
		return one(inverse)
	}
	return nil, fault(CodeOperationInvalid, "unsupported operation")
}
func entityMode(k Kind) string {
	switch k {
	case AssetAdd, TimelineAdd, TrackAdd, ClipAdd, BusAdd:
		return "add"
	case AssetRemove, TimelineRemove, TrackRemove, ClipRemove, ClipDelete, BusRemove:
		return "remove"
	}
	return "replace"
}
func inverseKind(k Kind) Kind {
	switch k {
	case AssetAdd:
		return AssetRemove
	case AssetRemove:
		return AssetAdd
	case TimelineAdd:
		return TimelineRemove
	case TimelineRemove:
		return TimelineAdd
	case TrackAdd:
		return TrackRemove
	case TrackRemove:
		return TrackAdd
	case ClipAdd:
		return ClipRemove
	case ClipRemove, ClipDelete:
		return ClipAdd
	case BusAdd:
		return BusRemove
	case BusRemove:
		return BusAdd
	}
	return k
}
func setMode[T any](k Kind, list []T, id d.ID, get func(T) d.ID) string {
	if k == LaneRemove || k == TransitionRemove || k == AnnotationRemove {
		return "remove"
	}
	for _, x := range list {
		if get(x) == id {
			return "replace"
		}
	}
	return "add"
}
func insert[T any](list []T, pos int, v T) []T {
	list = append(list, v)
	copy(list[pos+1:], list[pos:len(list)-1])
	list[pos] = v
	return list
}
func edit[T any](list *[]T, id d.ID, pos *int, value *T, mode string, get func(T) d.ID) (*T, *int, error) {
	index := -1
	for i, x := range *list {
		if get(x) == id {
			index = i
			break
		}
	}
	if mode == "add" {
		if index >= 0 {
			return nil, nil, fault(CodeConflict, "entity already exists")
		}
		if pos == nil || *pos < 0 || *pos > len(*list) || value == nil {
			return nil, nil, fault(CodeOperationInvalid, "invalid insertion position/value")
		}
		*list = insert(*list, *pos, *value)
		return nil, nil, nil
	}
	if index < 0 {
		return nil, nil, fault(CodeMissing, "entity missing")
	}
	old := (*list)[index]
	if mode == "remove" {
		*list = append((*list)[:index], (*list)[index+1:]...)
	} else {
		if pos == nil || *pos != index || value == nil {
			return nil, nil, fault(CodeConflict, "replace position no longer matches")
		}
		(*list)[index] = *value
	}
	return &old, ptr(index), nil
}
func timeline(doc *d.Document, id d.ID) (*d.Timeline, error) {
	for i := range doc.Timelines {
		if doc.Timelines[i].ID == id {
			return &doc.Timelines[i], nil
		}
	}
	return nil, fault(CodeMissing, "timeline missing")
}
func track(doc *d.Document, id d.ID) (*d.Track, *d.Timeline, error) {
	for i := range doc.Timelines {
		t := &doc.Timelines[i]
		for j := range t.Tracks {
			if t.Tracks[j].ID == id {
				return &t.Tracks[j], t, nil
			}
		}
	}
	return nil, nil, fault(CodeMissing, "track missing")
}
func clip(doc *d.Document, id d.ID) (*d.Clip, *d.Track, int, error) {
	for i := range doc.Timelines {
		for j := range doc.Timelines[i].Tracks {
			t := &doc.Timelines[i].Tracks[j]
			for k := range t.Clips {
				if t.Clips[k].ID == id {
					return &t.Clips[k], t, k, nil
				}
			}
		}
	}
	return nil, nil, 0, fault(CodeMissing, "clip missing")
}
func metadata(doc *d.Document, s Scope) (*map[string]json.RawMessage, error) {
	switch s.Kind {
	case ScopeDocument:
		if s.ID == doc.ID {
			return &doc.Metadata, nil
		}
	case ScopeAsset:
		for i := range doc.Assets {
			if doc.Assets[i].ID == s.ID {
				return &doc.Assets[i].Metadata, nil
			}
		}
	case ScopeTimeline:
		t, e := timeline(doc, s.ID)
		if e == nil {
			return &t.Metadata, nil
		}
	case ScopeTrack:
		t, _, e := track(doc, s.ID)
		if e == nil {
			return &t.Metadata, nil
		}
	}
	return nil, fault(CodeMissing, "metadata scope missing or unsupported")
}
func annotations(doc *d.Document, s Scope) (*[]d.Annotation, error) {
	switch s.Kind {
	case ScopeDocument:
		if s.ID == doc.ID {
			return &doc.Annotations, nil
		}
	case ScopeTimeline:
		t, e := timeline(doc, s.ID)
		if e == nil {
			return &t.Annotations, nil
		}
	case ScopeTrack:
		t, _, e := track(doc, s.ID)
		if e == nil {
			return &t.Annotations, nil
		}
	case ScopeClip:
		c, _, _, e := clip(doc, s.ID)
		if e == nil {
			return &c.Annotations, nil
		}
	}
	return nil, fault(CodeMissing, "annotation scope missing or unsupported")
}
func timeEqual(a, b d.RationalTime) bool { n, e := a.Compare(b); return e == nil && n == 0 }
func rangeEqual(a, b d.TimeRange) bool {
	return timeEqual(a.Start, b.Start) && timeEqual(a.Duration, b.Duration)
}
func checkSplit(old d.Clip, p SplitEdit) error {
	fail := func() error {
		return fault(CodeSplit, "split replacements must exactly partition original timeline and source ranges, retain source asset and caller-author all nested IDs/animation")
	}
	end, err := old.TimelineRange.End()
	if err != nil {
		return fail()
	}
	a, e1 := p.At.Compare(old.TimelineRange.Start)
	b, e2 := p.At.Compare(end)
	if e1 != nil || e2 != nil || a <= 0 || b >= 0 {
		return fail()
	}
	source, err := evaluator.SourceTime(old, p.At)
	if err != nil {
		return fail()
	}
	sourceEnd, err := old.SourceRange.End()
	if err != nil {
		return fail()
	}
	leftDuration, err := p.At.Sub(old.TimelineRange.Start)
	if err != nil {
		return fail()
	}
	rightDuration, err := end.Sub(p.At)
	if err != nil {
		return fail()
	}
	leftSource, err := source.Sub(old.SourceRange.Start)
	if err != nil {
		return fail()
	}
	rightSource, err := sourceEnd.Sub(source)
	if err != nil {
		return fail()
	}
	if p.Left.ID != old.ID || p.Right.ID == old.ID || p.Left.AssetID != old.AssetID || p.Right.AssetID != old.AssetID || !rangeEqual(p.Left.TimelineRange, d.TimeRange{Start: old.TimelineRange.Start, Duration: leftDuration}) || !rangeEqual(p.Right.TimelineRange, d.TimeRange{Start: p.At, Duration: rightDuration}) || !rangeEqual(p.Left.SourceRange, d.TimeRange{Start: old.SourceRange.Start, Duration: leftSource}) || !rangeEqual(p.Right.SourceRange, d.TimeRange{Start: source, Duration: rightSource}) {
		return fail()
	}
	return nil
}

// Rendering identity ignores only explicitly non-rendering intent. Render
// changes conservatively invalidate whole timelines, independently for visual
// and audio domains; returned IDs include removed timelines and are sorted.
func renderImpact(before, after d.Document) RenderImpact {
	out := RenderImpact{IdentityChanged: true, TimelineIDs: []d.ID{}}
	b, _ := clone(before)
	a, _ := clone(after)
	stripIntent(&b)
	stripIntent(&a)
	if reflect.DeepEqual(b, a) {
		return out
	}
	bv, _ := clone(b)
	av, _ := clone(a)
	ba, _ := clone(b)
	aa, _ := clone(a)
	projectDomain(&bv, false)
	projectDomain(&av, false)
	projectDomain(&ba, true)
	projectDomain(&aa, true)
	visualChanged := !reflect.DeepEqual(bv, av)
	audioChanged := !reflect.DeepEqual(ba, aa)
	ids := map[d.ID]bool{}
	for _, doc := range []d.Document{b, a} {
		for _, tl := range doc.Timelines {
			ids[tl.ID] = true
			for _, tr := range tl.Tracks {
				switch tr.Kind {
				case d.TrackKindAudio:
					out.Audio = true
				case d.TrackKindVideo:
					out.Visual = true
					out.Audio = true
				case d.TrackKindText, d.TrackKindVector:
					out.Visual = true
				}
			}
		}
	}
	out.Visual = out.Visual && visualChanged
	out.Audio = out.Audio && audioChanged
	for id := range ids {
		out.TimelineIDs = append(out.TimelineIDs, id)
	}
	sort.Slice(out.TimelineIDs, func(i, j int) bool { return out.TimelineIDs[i] < out.TimelineIDs[j] })
	out.RenderRequired = out.Visual || out.Audio
	return out
}
func stripIntent(doc *d.Document) {
	doc.Revision = 0
	doc.Metadata = nil
	doc.Annotations = nil
	for i := range doc.Assets {
		doc.Assets[i].Metadata = nil
	}
	for i := range doc.Timelines {
		t := &doc.Timelines[i]
		t.Name = ""
		t.Metadata = nil
		t.Annotations = nil
		for j := range t.Tracks {
			tr := &t.Tracks[j]
			tr.Name = ""
			tr.Metadata = nil
			tr.Annotations = nil
			for k := range tr.Clips {
				tr.Clips[k].Annotations = nil
			}
		}
	}
	for i := range doc.AudioBuses {
		doc.AudioBuses[i].Name = ""
	}
}

// projectDomain removes the opposite render domain without weakening identity.
func projectDomain(doc *d.Document, audio bool) {
	keepAssets := map[d.ID]bool{}
	for i := range doc.Timelines {
		tl := &doc.Timelines[i]
		tracks := tl.Tracks[:0]
		for _, tr := range tl.Tracks {
			if audio && tr.Kind != d.TrackKindAudio && tr.Kind != d.TrackKindVideo {
				continue
			}
			if !audio && tr.Kind != d.TrackKindVideo && tr.Kind != d.TrackKindText && tr.Kind != d.TrackKindVector {
				continue
			}
			if !audio {
				tr.AudioBusID = ""
			}
			for j := range tr.Clips {
				c := &tr.Clips[j]
				keepAssets[c.AssetID] = true
				if c.PropertySet != nil {
					p := c.PropertySet
					if audio {
						p.Transform = nil
						p.Opacity = nil
						p.Crop = nil
					} else {
						p.AudioGain = nil
						p.AudioPan = nil
						p.AudioChannelMatrix = nil
					}
					if reflect.DeepEqual(*p, d.PropertySet{}) {
						c.PropertySet = nil
					}
				}
				lanes := c.KeyframeLanes[:0]
				for _, lane := range c.KeyframeLanes {
					isAudio := lane.Target == d.KeyframeTargetAudioGain || lane.Target == d.KeyframeTargetAudioPan
					if isAudio == audio {
						lanes = append(lanes, lane)
					}
				}
				c.KeyframeLanes = lanes
			}
			tracks = append(tracks, tr)
		}
		tl.Tracks = tracks
		if len(tracks) == 0 {
			tl.Transitions = nil
		}
	}
	// Font dependencies participate only in the visual domain.
	if !audio {
		for _, a := range doc.Assets {
			if keepAssets[a.ID] && a.Text != nil {
				keepAssets[a.Text.FontAssetID] = true
			}
		}
	}
	assets := doc.Assets[:0]
	for _, a := range doc.Assets {
		if keepAssets[a.ID] {
			assets = append(assets, a)
		}
	}
	doc.Assets = assets
	if !audio {
		doc.AudioBuses = nil
	}
}
