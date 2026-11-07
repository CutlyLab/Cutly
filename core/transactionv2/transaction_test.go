package transactionv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	d "github.com/CutlyLab/Cutly/core/documentv2"
)

func tm(n int64) d.RationalTime { return d.RationalTime{Value: n, Rate: 1} }
func rat(n int64) d.Rational    { return d.Rational{Numerator: n, Denominator: 1} }
func rng(start, duration int64) d.TimeRange {
	return d.TimeRange{Start: tm(start), Duration: tm(duration)}
}
func base() d.Document {
	return d.Document{SchemaVersion: 2, ID: "doc", Revision: 7, Assets: []d.Asset{{ID: "a", Kind: d.AssetKindVideo, URI: "a.mp4", Duration: ptr(tm(100))}, {ID: "b", Kind: d.AssetKindVideo, URI: "b.mp4", Duration: ptr(tm(100))}, {ID: "unused", Kind: d.AssetKindData, URI: "unused"}}, Timelines: []d.Timeline{{ID: "tl", Tracks: []d.Track{{ID: "tr", Kind: d.TrackKindVideo, Clips: []d.Clip{{ID: "c", AssetID: "a", TimelineRange: rng(0, 4), SourceRange: rng(10, 8)}}}, {ID: "tr2", Kind: d.TrackKindVideo}}}, {ID: "empty"}}}
}
func proposal(t *testing.T, doc d.Document, ops ...Operation) Transaction {
	t.Helper()
	for i := range ops {
		ops[i].ID = fmt.Sprintf("op%d", i)
	}
	tx, err := Create(doc, Transaction{ID: "tx", IdempotencyKey: "idem", Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func applyOK(t *testing.T, doc d.Document, tx Transaction) Result {
	t.Helper()
	r, e := Apply(context.Background(), doc, tx)
	if e != nil || len(r.Diagnostics) > 0 {
		t.Fatalf("apply: %v %+v", e, r.Diagnostics)
	}
	return r
}
func roundtrip(t *testing.T, doc d.Document, ops ...Operation) Result {
	t.Helper()
	tx := proposal(t, doc, ops...)
	before, _ := d.CanonicalJSON(doc)
	r := applyOK(t, doc, tx)
	if r.Document.Revision != doc.Revision+1 {
		t.Fatal("revision")
	}
	for _, op := range r.Inverse.Operations {
		if op.Expect == nil {
			t.Fatal("missing inverse predicate")
		}
	}
	undo := applyOK(t, r.Document, r.Inverse)
	undo.Document.Revision = doc.Revision
	after, _ := d.CanonicalJSON(undo.Document)
	if !bytes.Equal(before, after) {
		t.Fatalf("roundtrip mismatch\n%s\n%s", before, after)
	}
	unchanged, _ := d.CanonicalJSON(doc)
	if !bytes.Equal(before, unchanged) {
		t.Fatal("input mutated")
	}
	return r
}
func TestCatalogRoundtrips(t *testing.T) {
	cases := []struct {
		name string
		op   Operation
	}{
		{"asset_add", Operation{Kind: AssetAdd, Asset: &AssetEdit{AssetID: "new", Position: ptr(1), Value: &d.Asset{ID: "new", Kind: d.AssetKindData, URI: "new"}}}},
		{"asset_remove", Operation{Kind: AssetRemove, Asset: &AssetEdit{AssetID: "unused"}}},
		{"asset_replace", Operation{Kind: AssetReplace, Asset: &AssetEdit{AssetID: "unused", Position: ptr(2), Value: &d.Asset{ID: "unused", Kind: d.AssetKindData, URI: "different"}}}},
		{"timeline_add", Operation{Kind: TimelineAdd, Timeline: &TimelineEdit{TimelineID: "new", Position: ptr(1), Value: &d.Timeline{ID: "new"}}}},
		{"timeline_remove", Operation{Kind: TimelineRemove, Timeline: &TimelineEdit{TimelineID: "empty"}}},
		{"timeline_replace", Operation{Kind: TimelineReplace, Timeline: &TimelineEdit{TimelineID: "empty", Position: ptr(1), Value: &d.Timeline{ID: "empty", Name: "changed"}}}},
		{"track_add", Operation{Kind: TrackAdd, Track: &TrackEdit{TimelineID: "tl", TrackID: "new", Position: ptr(1), Value: &d.Track{ID: "new", Kind: d.TrackKindVideo}}}},
		{"track_remove", Operation{Kind: TrackRemove, Track: &TrackEdit{TimelineID: "tl", TrackID: "tr2"}}},
		{"track_replace", Operation{Kind: TrackReplace, Track: &TrackEdit{TimelineID: "tl", TrackID: "tr2", Position: ptr(1), Value: &d.Track{ID: "tr2", Kind: d.TrackKindVideo, Name: "changed"}}}},
		{"clip_add", Operation{Kind: ClipAdd, Clip: &ClipEdit{TrackID: "tr", ClipID: "new", Position: ptr(1), Value: &d.Clip{ID: "new", AssetID: "a", TimelineRange: rng(10, 2), SourceRange: rng(0, 2)}}}},
		{"clip_remove", Operation{Kind: ClipRemove, Clip: &ClipEdit{TrackID: "tr", ClipID: "c"}}},
		{"clip_replace", Operation{Kind: ClipReplace, Clip: &ClipEdit{TrackID: "tr", ClipID: "c", Position: ptr(0), Value: &d.Clip{ID: "c", AssetID: "a", TimelineRange: rng(1, 2), SourceRange: rng(1, 2)}}}},
		{"clip_delete", Operation{Kind: ClipDelete, Clip: &ClipEdit{TrackID: "tr", ClipID: "c"}}},
		{"clip_move", Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(5)}}},
		{"clip_trim", Operation{Kind: ClipTrim, Trim: &TrimEdit{ClipID: "c", TimelineRange: rng(1, 2), SourceRange: rng(12, 4)}}},
		{"clip_retrack", Operation{Kind: ClipRetrack, Retrack: &RetrackEdit{ClipID: "c", TrackID: "tr2", Position: 0}}},
		{"clip_set_disabled", Operation{Kind: ClipSetDisabled, Disabled: &DisabledEdit{ClipID: "c", Disabled: true}}},
		{"clip_set_asset", Operation{Kind: ClipSetAsset, AssetReference: &AssetReferenceEdit{ClipID: "c", AssetID: "b"}}},
		{"property_set", Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: &d.PropertySet{Opacity: &d.OpacityProperty{Value: d.Rational{Numerator: 1, Denominator: 2}}}}}},
		{"lane_set", Operation{Kind: LaneSet, Lane: &LaneEdit{ClipID: "c", LaneID: "lane", Position: ptr(0), Value: &d.KeyframeLane{ID: "lane", Target: d.KeyframeTargetOpacity, ValueType: d.KeyframeValueScalar, Keyframes: []d.Keyframe{{Offset: tm(0), Value: d.KeyframeValue{Scalar: ptr(rat(1))}}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { roundtrip(t, base(), tc.op) })
	}
}
func TestBusesAndRoute(t *testing.T) {
	doc := base()
	master := d.AudioBus{ID: "master", Kind: d.AudioBusMaster, Properties: d.AudioBusProperties{GainLinear: rat(1), Pan: rat(0)}}
	group := d.AudioBus{ID: "group", Kind: d.AudioBusGroup, ParentBusID: "master", Properties: master.Properties}
	doc.AudioBuses = []d.AudioBus{master, group}
	doc.Timelines[0].Tracks = append(doc.Timelines[0].Tracks, d.Track{ID: "audio", Kind: d.TrackKindAudio, AudioBusID: "master"})
	roundtrip(t, doc, Operation{Kind: BusAdd, Bus: &BusEdit{BusID: "new", Position: ptr(1), Value: &d.AudioBus{ID: "new", Kind: d.AudioBusGroup, ParentBusID: "master", Properties: master.Properties}}})
	roundtrip(t, doc, Operation{Kind: BusRemove, Bus: &BusEdit{BusID: "group"}})
	master.Properties.SamplePeakLimiter = &d.AudioSamplePeakLimiterProperty{CeilingLinear: rat(1)}
	roundtrip(t, doc, Operation{Kind: BusReplace, Bus: &BusEdit{BusID: "master", Position: ptr(0), Value: &master}})
	roundtrip(t, doc, Operation{Kind: TrackRoute, Route: &RouteEdit{TrackID: "audio", BusID: "group"}})
	roundtrip(t, base(), Operation{Kind: TrackRoute, Route: &RouteEdit{TrackID: "tr", BusID: ""}})
}
func TestMetadataAnnotationsAndLanes(t *testing.T) {
	for _, scope := range []Scope{{ScopeDocument, "doc"}, {ScopeAsset, "a"}, {ScopeTimeline, "tl"}, {ScopeTrack, "tr"}} {
		t.Run("metadata_"+string(scope.Kind), func(t *testing.T) {
			doc := base()
			r := roundtrip(t, doc, Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: scope, Key: "x", Value: json.RawMessage(`{"fraction":1.25,"exp":1e+3,"null":null}`)}})
			if r.Impact.RenderRequired || r.Impact.Visual || r.Impact.Audio {
				t.Fatal("metadata rendered")
			}
			roundtrip(t, r.Document, Operation{Kind: MetadataRemove, Metadata: &MetadataEdit{Scope: scope, Key: "x"}})
		})
	}
	for _, scope := range []Scope{{ScopeDocument, "doc"}, {ScopeTimeline, "tl"}, {ScopeTrack, "tr"}, {ScopeClip, "c"}} {
		t.Run("annotation_"+string(scope.Kind), func(t *testing.T) {
			op := Operation{Kind: AnnotationSet, Annotation: &AnnotationEdit{Scope: scope, AnnotationID: "ann", Position: ptr(0), Value: &d.Annotation{ID: "ann", Namespace: "intent", Value: json.RawMessage(`null`)}}}
			r := roundtrip(t, base(), op)
			roundtrip(t, r.Document, Operation{Kind: AnnotationRemove, Annotation: &AnnotationEdit{Scope: scope, AnnotationID: "ann"}})
			op.Annotation.Value.Value = json.RawMessage(`1.5`)
			roundtrip(t, r.Document, op)
		})
	}
	doc := base()
	lane := d.KeyframeLane{ID: "lane", Target: d.KeyframeTargetOpacity, ValueType: d.KeyframeValueScalar, Keyframes: []d.Keyframe{{Offset: tm(0), Value: d.KeyframeValue{Scalar: ptr(rat(1))}}}}
	doc.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []d.KeyframeLane{lane}
	roundtrip(t, doc, Operation{Kind: LaneRemove, Lane: &LaneEdit{ClipID: "c", LaneID: "lane"}})
	lane.Keyframes[0].Value.Scalar = ptr(rat(0))
	roundtrip(t, doc, Operation{Kind: LaneSet, Lane: &LaneEdit{ClipID: "c", LaneID: "lane", Position: ptr(0), Value: &lane}})
}
func TestSplitExactAndNestedIDs(t *testing.T) {
	doc := base()
	old := doc.Timelines[0].Tracks[0].Clips[0]
	left, right := old, old
	left.TimelineRange = rng(0, 1)
	left.SourceRange = rng(10, 2)
	right.ID = "right"
	right.TimelineRange = rng(1, 3)
	right.SourceRange = rng(12, 6)
	op := Operation{Kind: ClipSplit, Split: &SplitEdit{ClipID: "c", At: tm(1), Left: left, Right: right}}
	roundtrip(t, doc, op)
	op.Split.Right.SourceRange.Start = tm(11)
	tx := proposal(t, doc, op)
	r, e := Apply(context.Background(), doc, tx)
	if e != nil || len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != CodeSplit || r.Document.ID != "" {
		t.Fatalf("inexact split accepted %+v %v", r, e)
	}
	op.Split.Right = right
	op.Split.Left.Annotations = []d.Annotation{{ID: "same", Namespace: "intent", Value: json.RawMessage(`1`)}}
	op.Split.Right.Annotations = op.Split.Left.Annotations
	tx = proposal(t, doc, op)
	r, e = Apply(context.Background(), doc, tx)
	if e != nil || !d.HasErrorDiagnostics(r.Diagnostics) || r.Document.ID != "" {
		t.Fatal("nested duplicate accepted")
	}
}
func TestTransitionRoundtrip(t *testing.T) {
	doc := base()
	c := &doc.Timelines[0].Tracks[0].Clips[0]
	c.SourceRange = rng(10, 4)
	right := *c
	right.ID = "right"
	right.TimelineRange = rng(4, 4)
	right.SourceRange = rng(20, 4)
	doc.Timelines[0].Tracks[0].Clips = append(doc.Timelines[0].Tracks[0].Clips, right)
	v := d.Transition{ID: "transition", Profile: d.TransitionDissolveV1, OutgoingClipID: "c", IncomingClipID: "right", PreDuration: tm(1), PostDuration: tm(1)}
	r := roundtrip(t, doc, Operation{Kind: TransitionSet, Transition: &TransitionEdit{TimelineID: "tl", TransitionID: "transition", Position: ptr(0), Value: &v}})
	roundtrip(t, r.Document, Operation{Kind: TransitionRemove, Transition: &TransitionEdit{TimelineID: "tl", TransitionID: "transition"}})
	v.PreDuration = d.RationalTime{Value: 1, Rate: 2}
	roundtrip(t, r.Document, Operation{Kind: TransitionSet, Transition: &TransitionEdit{TimelineID: "tl", TransitionID: "transition", Position: ptr(0), Value: &v}})
}
func TestAtomicAndConflicts(t *testing.T) {
	doc := base()
	tx := proposal(t, doc, Operation{Kind: ClipSetAsset, AssetReference: &AssetReferenceEdit{ClipID: "c", AssetID: "later"}}, Operation{Kind: AssetAdd, Asset: &AssetEdit{AssetID: "later", Position: ptr(0), Value: &d.Asset{ID: "later", Kind: d.AssetKindVideo, URI: "later"}}})
	roundtrip(t, doc, tx.Operations...)
	for _, mutate := range []func(*Transaction){func(x *Transaction) { x.ExpectedRevision++ }, func(x *Transaction) { x.ExpectedDocumentHash = "sha256:" + strings.Repeat("0", 64) }, func(x *Transaction) { x.DocumentID = "other" }} {
		copy, _ := clone(tx)
		copy.Digest = ""
		mutate(&copy)
		r, e := Apply(context.Background(), doc, copy)
		if e != nil || len(r.Diagnostics) == 0 || r.Document.ID != "" || len(r.Inverse.Operations) != 0 {
			t.Fatal("CAS not atomic")
		}
	}
	bad := proposal(t, doc, Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(5)}}, Operation{Kind: AssetRemove, Asset: &AssetEdit{AssetID: "a"}})
	r, e := Apply(context.Background(), doc, bad)
	if e != nil || !d.HasErrorDiagnostics(r.Diagnostics) || r.Document.ID != "" {
		t.Fatal("invalid final doc exposed")
	}
	changed := roundtrip(t, doc, Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(5)}})
	disjoint := applyOK(t, changed.Document, proposal(t, changed.Document, Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: Scope{ScopeDocument, "doc"}, Key: "x", Value: json.RawMessage(`1`)}}))
	inverse, e := Create(disjoint.Document, changed.Inverse)
	if e != nil {
		t.Fatal(e)
	}
	applyOK(t, disjoint.Document, inverse)
	conflict := applyOK(t, changed.Document, proposal(t, changed.Document, Operation{Kind: ClipSetDisabled, Disabled: &DisabledEdit{ClipID: "c", Disabled: true}}))
	inverse, e = Create(conflict.Document, changed.Inverse)
	if e != nil {
		t.Fatal(e)
	}
	r, e = Apply(context.Background(), conflict.Document, inverse)
	if e != nil || r.Diagnostics[0].Code != CodePrecondition || r.Document.ID != "" {
		t.Fatal("conflicting inverse overwritten")
	}
}
func TestCodecStrictNumberPreserving(t *testing.T) {
	tx := proposal(t, base(), Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: Scope{ScopeDocument, "doc"}, Key: "x", Value: json.RawMessage(`{"fraction":1.50,"exp":1e+3}`)}})
	raw, e := CanonicalJSON(tx)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(raw, []byte(`1.50`)) || !bytes.Contains(raw, []byte(`1e+3`)) {
		t.Fatal("number spelling changed")
	}
	decoded, e := Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := Digest(tx)
	b, _ := Digest(decoded)
	if a != b {
		t.Fatal("digest parity")
	}
	copy := tx
	copy.Digest = ""
	c, _ := Digest(copy)
	if a != c {
		t.Fatal("self digest included")
	}
	attacks := [][]byte{append([]byte(" "), raw...), bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1), bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"Schema_version":2`), 1), bytes.Replace(raw, []byte(`,"schema_version":2`), nil, 1), bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":null`), 1), bytes.Replace(raw, []byte(`"key":"x"`), []byte(`"key":"\ud800"`), 1), bytes.Replace(raw, []byte(`"key":"x"`), []byte{'"', 'k', 'e', 'y', '"', ':', '"', 0xff, '"'}, 1), bytes.Replace(raw, []byte(`"key":"x"`), []byte(`"key":"x","unknown":1`), 1)}
	for i, attack := range attacks {
		if _, e := Decode(attack); e == nil {
			t.Fatalf("attack %d accepted %s", i, attack)
		}
	}
	for _, rawValue := range []json.RawMessage{json.RawMessage(`{"x":1,"x":2}`), json.RawMessage(`"\ud800"`)} {
		copy := tx
		copy.Digest = ""
		copy.Operations[0].Metadata.Value = rawValue
		if _, e := CanonicalJSON(copy); e == nil {
			t.Fatal("unsafe raw accepted")
		}
	}
}
func TestOwnershipCancellationConcurrency(t *testing.T) {
	doc := base()
	tx := proposal(t, doc, Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: Scope{ScopeDocument, "doc"}, Key: "x", Value: json.RawMessage(`{"n":1.25}`)}})
	r := applyOK(t, doc, tx)
	tx.Operations[0].Metadata.Value[0] = '['
	if !json.Valid(r.Document.Metadata["x"]) {
		t.Fatal("caller raw alias")
	}
	r.Document.Assets[0].URI = "mutated"
	if doc.Assets[0].URI == "mutated" {
		t.Fatal("document alias")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, e := Apply(ctx, doc, Transaction{})
	if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(out, Result{}) {
		t.Fatal("cancel failure leaked state")
	}
	tx = proposal(t, doc, Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(1)}})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := Apply(context.Background(), doc, tx)
			if e != nil || len(r.Diagnostics) > 0 {
				t.Errorf("concurrent %v %+v", e, r.Diagnostics)
			}
		}()
	}
	wg.Wait()
}
func TestLimitsAndUnion(t *testing.T) {
	doc := base()
	tx := proposal(t, doc, Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(1)}})
	cases := []func(*Transaction){func(x *Transaction) { x.Operations[0].Disabled = &DisabledEdit{ClipID: "c"} }, func(x *Transaction) { x.Operations = append(x.Operations, x.Operations[0]) }, func(x *Transaction) { x.ID = strings.Repeat("x", 257) }, func(x *Transaction) { x.Operations[0].Move.ClipID = "" }, func(x *Transaction) { x.Operations[0].Kind = "unknown" }}
	for _, f := range cases {
		copy, _ := clone(tx)
		copy.Digest = ""
		f(&copy)
		if !d.HasErrorDiagnostics(Validate(copy)) {
			t.Fatal("invalid union accepted")
		}
	}
	bad := base()
	bad.Metadata = map[string]json.RawMessage{"x": json.RawMessage(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65))}
	r, e := Apply(context.Background(), bad, tx)
	if e != nil || r.Document.ID != "" || len(r.Diagnostics) == 0 {
		t.Fatal("depth accepted")
	}
}
