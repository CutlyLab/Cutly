package transactionv2

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	d "github.com/CutlyLab/Cutly/core/documentv2"
)

func TestMultiopInverseSnapshots(t *testing.T) {
	doc := base()
	roundtrip(t, doc,
		Operation{Kind: LaneSet, Lane: &LaneEdit{ClipID: "c", LaneID: "l", Position: ptr(0), Value: &d.KeyframeLane{ID: "l", Target: d.KeyframeTargetOpacity, ValueType: d.KeyframeValueScalar, Keyframes: []d.Keyframe{{Offset: tm(0), Value: d.KeyframeValue{Scalar: ptr(rat(1))}}}}}},
		Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(1)}},
		Operation{Kind: LaneRemove, Lane: &LaneEdit{ClipID: "c", LaneID: "l"}},
		Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: Scope{ScopeDocument, "doc"}, Key: "one", Value: json.RawMessage(`1.0`)}},
		Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: Scope{ScopeDocument, "doc"}, Key: "one", Value: json.RawMessage(`2e1`)}},
	)
}
func TestTypedPropertyDomains(t *testing.T) {
	doc := base()
	gain := Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: &d.PropertySet{AudioGain: &d.AudioGainProperty{Linear: rat(2)}}}}
	audioDoc := base()
	audioDoc.Assets[0].Kind = d.AssetKindAudio
	audioDoc.Timelines[0].Tracks[0].Kind = d.TrackKindAudio
	audio := roundtrip(t, audioDoc, gain)
	if !audio.Impact.Audio || audio.Impact.Visual {
		t.Fatalf("gain impact %+v", audio.Impact)
	}
	opacity := roundtrip(t, doc, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: &d.PropertySet{Opacity: &d.OpacityProperty{Value: rat(0)}}}})
	if !opacity.Impact.Visual || opacity.Impact.Audio {
		t.Fatalf("opacity impact %+v", opacity.Impact)
	}
	props := &d.PropertySet{Transform: &d.TransformProperty{Position: d.Point{X: rat(0), Y: rat(0)}, Scale: d.Point{X: rat(1), Y: rat(1)}, RotationDegrees: rat(90), Anchor: d.Point{X: rat(0), Y: rat(0)}}, Crop: &d.CropProperty{Rect: d.Rect{X: rat(0), Y: rat(0), Width: rat(1), Height: rat(1)}}, AudioGain: &d.AudioGainProperty{Linear: rat(1)}, AudioPan: &d.AudioPanProperty{Value: rat(0)}, AudioChannelMatrix: &d.AudioChannelMatrixProperty{SourceLayout: "stereo", SourceChannels: []string{"FL", "FR"}, LeftCoefficients: []d.Rational{rat(1), rat(0)}, RightCoefficients: []d.Rational{rat(0), rat(1)}}}
	visualProps, _ := clone(props)
	visualProps.AudioGain = nil
	visualProps.AudioPan = nil
	visualProps.AudioChannelMatrix = nil
	r := roundtrip(t, doc, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: visualProps}})
	roundtrip(t, r.Document, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c"}})
	props.Transform = nil
	props.Crop = nil
	r = roundtrip(t, audioDoc, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: props}})
	roundtrip(t, r.Document, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c"}})
}
func TestPredicateDeletionPosition(t *testing.T) {
	doc := base()
	r := roundtrip(t, doc, Operation{Kind: AssetRemove, Asset: &AssetEdit{AssetID: "unused"}})
	// A disjoint metadata update may revert, but filling the removed slot cannot.
	newer := applyOK(t, r.Document, proposal(t, r.Document, Operation{Kind: AssetAdd, Asset: &AssetEdit{AssetID: "new", Position: ptr(2), Value: &d.Asset{ID: "new", Kind: d.AssetKindData, URI: "new"}}}))
	inv, err := Create(newer.Document, r.Inverse)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Apply(context.Background(), newer.Document, inv)
	if err != nil || out.Diagnostics[0].Code != CodePrecondition {
		t.Fatal("slot postimage conflict not enforced")
	}
}
func TestLargeBaseProfileAndInverseBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("large profile")
	}
	doc := base()
	doc.Metadata = map[string]json.RawMessage{}
	chunk := json.RawMessage(`"` + strings.Repeat("x", 900000) + `"`)
	for i := 0; i < 20; i++ {
		doc.Metadata[string(rune('a'+i))] = chunk
	}
	// Larger than the transaction cap: the document profile is independently128MiB.
	tx := proposal(t, doc, Operation{Kind: MetadataSet, Metadata: &MetadataEdit{Scope: Scope{ScopeDocument, "doc"}, Key: "small", Value: json.RawMessage(`1.25`)}})
	r := applyOK(t, doc, tx)
	if r.Impact.RenderRequired {
		t.Fatal("large metadata impact")
	}
	// Replacing a huge timeline would require an inverse larger than16MiB.
	doc = base()
	tl := &doc.Timelines[0]
	tl.Metadata = map[string]json.RawMessage{}
	for i := 0; i < 20; i++ {
		tl.Metadata[string(rune('a'+i))] = chunk
	}
	tx = proposal(t, doc, Operation{Kind: TimelineRemove, Timeline: &TimelineEdit{TimelineID: "tl"}})
	out, err := Apply(context.Background(), doc, tx)
	if err != nil || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != CodeLimit || out.Document.ID != "" {
		t.Fatal("oversized inverse accepted")
	}
}

type controlledContext struct {
	calls             int
	panicAt, cancelAt int
}

func (*controlledContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*controlledContext) Done() <-chan struct{}       { return nil }
func (*controlledContext) Value(any) any               { return nil }
func (c *controlledContext) Err() error {
	c.calls++
	if c.calls == c.panicAt {
		panic("context panic")
	}
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}
func TestCancellationAndPanicAfterMutation(t *testing.T) {
	doc := base()
	tx := proposal(t, doc, Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(1)}}, Operation{Kind: ClipSetDisabled, Disabled: &DisabledEdit{ClipID: "c", Disabled: true}})
	for _, ctx := range []*controlledContext{{cancelAt: 3}, {panicAt: 3}} {
		out, err := Apply(ctx, doc, tx)
		if err == nil || !reflect.DeepEqual(out, Result{}) {
			t.Fatalf("failure leaked candidate %+v %v", out, err)
		}
		if ctx.panicAt > 0 && !errors.Is(err, ErrInternal) {
			t.Fatal(err)
		}
	}
	if doc.Timelines[0].Tracks[0].Clips[0].Disabled {
		t.Fatal("panic/cancel mutated source")
	}
}
func TestRevisionOverflowAndShape(t *testing.T) {
	doc := base()
	doc.Revision = ^d.Revision(0)
	tx := proposal(t, doc, Operation{Kind: ClipMove, Move: &MoveEdit{ClipID: "c", At: tm(1)}})
	r, err := Apply(context.Background(), doc, tx)
	if err != nil || r.Diagnostics[0].Code != CodeRevisionOverflow {
		t.Fatal("overflow accepted")
	}
	doc = base()
	op := Operation{Kind: LaneSet, Lane: &LaneEdit{ClipID: "c", LaneID: "bad", Position: ptr(0), Value: &d.KeyframeLane{ID: "bad", Target: d.KeyframeTargetOpacity, ValueType: d.KeyframeValueScalar}}}
	if _, err := Create(doc, Transaction{ID: "tx", IdempotencyKey: "idem", Operations: []Operation{op}}); err == nil {
		t.Fatal("null required keyframes accepted")
	}
}
func TestExtensionRenderingIntent(t *testing.T) {
	intent := d.ExtensionProperty{Namespace: "cutly.note.v1", Value: json.RawMessage(`{"note":"intent only"}`)}
	for _, kind := range []d.TrackKind{d.TrackKindVideo, d.TrackKindAudio} {
		t.Run(string(kind), func(t *testing.T) {
			doc := base()
			if kind == d.TrackKindAudio {
				doc.Assets[0].Kind = d.AssetKindAudio
				doc.Timelines[0].Tracks[0].Kind = kind
			}
			for _, replaceClip := range []bool{false, true} {
				properties := &d.PropertySet{Extensions: []d.ExtensionProperty{intent}}
				op := Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: properties}}
				if replaceClip {
					value := doc.Timelines[0].Tracks[0].Clips[0]
					value.PropertySet = properties
					op = Operation{Kind: ClipReplace, Clip: &ClipEdit{TrackID: "tr", ClipID: "c", Position: ptr(0), Value: &value}}
				}
				r := roundtrip(t, doc, op)
				if !r.Impact.IdentityChanged || r.Impact.RenderRequired || r.Impact.Visual || r.Impact.Audio || len(r.Impact.TimelineIDs) != 0 {
					t.Fatalf("intent-only extension impact %+v", r.Impact)
				}
				if len(r.Document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions) != 1 {
					t.Fatal("impact projection modified committed extension")
				}
			}
			properties := &d.PropertySet{Extensions: []d.ExtensionProperty{intent}}
			if kind == d.TrackKindAudio {
				properties.AudioGain = &d.AudioGainProperty{Linear: rat(2)}
			} else {
				properties.Opacity = &d.OpacityProperty{Value: rat(0)}
			}
			mixed := roundtrip(t, doc, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: properties}})
			if !mixed.Impact.RenderRequired || mixed.Impact.Audio != (kind == d.TrackKindAudio) || mixed.Impact.Visual != (kind == d.TrackKindVideo) {
				t.Fatalf("mixed typed/intent impact %+v", mixed.Impact)
			}
			rendering := intent
			rendering.AffectsRendering = true
			rendering.RequiredCapability = "cutly.custom.render.v1"
			properties = &d.PropertySet{Extensions: []d.ExtensionProperty{intent, rendering}}
			// Namespaces must be unique, including nonrendering entries.
			properties.Extensions[1].Namespace = "cutly.render.v1"
			r := roundtrip(t, doc, Operation{Kind: PropertySet, Properties: &PropertiesEdit{ClipID: "c", Value: properties}})
			if !r.Impact.RenderRequired || !r.Impact.Audio || (kind == d.TrackKindVideo && !r.Impact.Visual) {
				t.Fatalf("rendering extension impact %+v", r.Impact)
			}
		})
	}
	// Assets/buses have no ExtensionProperty fields. Their analogous intent
	// boundaries are asset metadata and bus display names; typed replacement
	// must retain those in identity while excluding them from both projections.
	doc := base()
	asset := doc.Assets[0]
	asset.Metadata = map[string]json.RawMessage{"intent": json.RawMessage(`1.25`)}
	r := roundtrip(t, doc, Operation{Kind: AssetReplace, Asset: &AssetEdit{AssetID: asset.ID, Position: ptr(0), Value: &asset}})
	if r.Impact.RenderRequired || len(r.Impact.TimelineIDs) != 0 {
		t.Fatalf("asset metadata impact %+v", r.Impact)
	}
	doc.AudioBuses = []d.AudioBus{{ID: "master", Kind: d.AudioBusMaster, Properties: d.AudioBusProperties{GainLinear: rat(1), Pan: rat(0)}}}
	bus := doc.AudioBuses[0]
	bus.Name = "intent only"
	r = roundtrip(t, doc, Operation{Kind: BusReplace, Bus: &BusEdit{BusID: bus.ID, Position: ptr(0), Value: &bus}})
	if r.Impact.RenderRequired || len(r.Impact.TimelineIDs) != 0 {
		t.Fatalf("bus name impact %+v", r.Impact)
	}
}

func TestUnicodeEscapeAllocationBudget(t *testing.T) {
	for _, scalar := range []string{" ", " ", "<", ">", "&"} {
		t.Run(scalar, func(t *testing.T) {
			typed := strings.Repeat(scalar, 1024)
			raw := json.RawMessage(`"` + typed + `"`)
			for _, value := range []any{typed, raw} {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = boundedValue(value, len(encoded)-1); err == nil {
					t.Fatalf("budget undercounts escaped %T", value)
				}
				if err = boundedValue(value, len(encoded)); err != nil {
					t.Fatalf("exact escaped budget %T: %v", value, err)
				}
			}
		})
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":2}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 100000 {
			t.Skip()
		}
		tx, err := Decode(raw)
		if err == nil {
			again, e := CanonicalJSON(tx)
			if e != nil || string(again) != string(raw) {
				t.Fatal("decode did not preserve exact canonical bytes")
			}
		}
	})
}
