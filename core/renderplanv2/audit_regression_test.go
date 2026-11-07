package renderplanv2

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func rejectResigned(t *testing.T, p Plan) {
	t.Helper()
	resign(t, &p)
	if v, e := Verify(p); e == nil || v.Valid() {
		t.Fatal("re-fingerprinted hostile plan verified")
	}
	raw, e := encode(p)
	if e != nil {
		t.Fatal(e)
	}
	if q, e := Decode(raw); e == nil || !reflect.DeepEqual(q, Plan{}) {
		t.Fatal("re-fingerprinted hostile wire accepted/nonzero")
	}
}
func without(p *Plan, f videoedit.CapabilityID) {
	for i, x := range p.RequiredCapabilities.Features {
		if x == f {
			p.RequiredCapabilities.Features = append(p.RequiredCapabilities.Features[:i], p.RequiredCapabilities.Features[i+1:]...)
			return
		}
	}
}

func TestInferableCapabilityMinima(t *testing.T) {
	mixed := resolveOK(t, mixedDoc(t), tr(0, 10))
	t.Run("all-graph-features", func(t *testing.T) {
		p := clone(t, mixed)
		p.RequiredCapabilities.Features = []videoedit.CapabilityID{}
		rejectResigned(t, p)
	})
	for _, f := range []videoedit.CapabilityID{documentv2.CapabilityAudioRouteV1, documentv2.CapabilityAudioGainV1, documentv2.CapabilityAudioPanV1} {
		t.Run(string(f), func(t *testing.T) { p := clone(t, mixed); without(&p, f); rejectResigned(t, p) })
	}
	for _, kind := range []documentv2.TrackKind{documentv2.TrackKindAudio, documentv2.TrackKindVideo} {
		t.Run(string(kind)+"-retime", func(t *testing.T) {
			d := transitionDoc()
			d.Timelines[0].Transitions = nil
			d.Timelines[0].Tracks = d.Timelines[0].Tracks[:1]
			d.Timelines[0].Tracks[0].Kind = kind
			d.Timelines[0].Tracks[0].Clips = d.Timelines[0].Tracks[0].Clips[:1]
			d.Timelines[0].Tracks[0].Clips[0].SourceRange = tr(2, 10)
			p := resolveOK(t, d, tr(0, 1))
			f := videoedit.CapabilityVideoLinearRetime
			if kind == documentv2.TrackKindAudio {
				f = videoedit.CapabilityAudioVarispeed
			}
			without(&p, f)
			rejectResigned(t, p)
		})
	}
	// Static source range ratios retain identity, not temporal retime semantics.
	d := mixedDoc(t)
	d.Timelines[0].Tracks[0].Clips[0].SourceRange = tr(0, 20)
	if _, e := Verify(resolveOK(t, d, tr(0, 1))); e != nil {
		t.Fatal(e)
	}
}

func TestKnownTrackAuthoredIntervals(t *testing.T) {
	base := resolveOK(t, mixedDoc(t), tr(0, 10))
	for _, tc := range []struct {
		name string
		r    documentv2.TimeRange
	}{{"overlap", tr(0, 10)}, {"inversion", tr(-10, 10)}} {
		t.Run(tc.name, func(t *testing.T) {
			p := clone(t, base)
			extra := p.Visual[0]
			extra.Binding.ClipID = "duplicate-image-distinct-id"
			extra.Binding.ClipIndex = 3
			extra.Binding.TimelineRange = tc.r
			if tc.name == "inversion" {
				p.RequestedRange = tr(-10, 20)
				p.FrameRange, _ = videoedit.CompileFrameTickRange(p.RequestedRange, p.Target.FrameClock)
				p.SampleRange, _ = videoedit.CompileSampleTickRange(p.RequestedRange, p.Target.SampleClock)
			}
			extra.VisibleTimelineRange = tc.r
			extra.VisibleSourceRange, _ = projected(extra.Binding, tc.r)
			p.Visual = append(p.Visual[:1], append([]PlannedClip{extra}, p.Visual[1:]...)...)
			rejectResigned(t, p)
		})
	}
	// Same-track bindings referenced only by transition handles must also enter
	// the ordering check. The later index claims an earlier non-overlapping time.
	p := resolveOK(t, transitionDoc(), documentv2.TimeRange{Start: rt(4, 1), Duration: rt(1, 2)})
	extra := p.Visual[0]
	extra.Binding.ClipID = "inverted-before-head-participant"
	extra.Binding.ClipIndex = 2
	extra.Binding.TimelineRange = tr(4, 1)
	extra.VisibleTimelineRange = p.Visual[0].VisibleTimelineRange
	extra.VisibleSourceRange, _ = projected(extra.Binding, extra.VisibleTimelineRange)
	p.Visual = append(p.Visual, extra)
	rejectResigned(t, p)
	// A gap in selected authored indices is not evidence of a malformed plan.
	d := transitionDoc()
	d.Timelines[0].Transitions = nil
	track := &d.Timelines[0].Tracks[0]
	track.Clips[0].TimelineRange = tr(0, 1)
	track.Clips[1].TimelineRange = tr(1, 1)
	track.Clips[1].Disabled = true
	last := track.Clips[0]
	last.ID = "last"
	last.TimelineRange = tr(2, 1)
	track.Clips = append(track.Clips, last)
	p = resolveOK(t, d, tr(0, 3))
	if len(p.Visual) != 2 || p.Visual[1].Binding.ClipIndex != 2 {
		t.Fatal("nonadjacent selected indices rejected")
	}
}

func TestKnownTransitionWindowsDoNotOverlap(t *testing.T) {
	d := transitionDoc()
	track := &d.Timelines[0].Tracks[0]
	last := track.Clips[1]
	last.ID = "third"
	last.TimelineRange = tr(10, 5)
	track.Clips = append(track.Clips, last)
	second := d.Timelines[0].Transitions[0]
	second.ID = "second-dissolve"
	second.OutgoingClipID = "in"
	second.IncomingClipID = "third"
	d.Timelines[0].Transitions = append(d.Timelines[0].Transitions, second)
	p := resolveOK(t, d, tr(0, 15))
	// Individually valid windows now overlap inside the middle clip; all
	// ordinary bindings and source projections remain unchanged.
	p.Transitions[0].PostDuration = rt(3, 1)
	p.Transitions[1].PreDuration = rt(3, 1)
	for i := range p.Transitions {
		trn := &p.Transitions[i]
		trn.TimelineRange, _ = transitionRange(trn.Cut, trn.PreDuration, trn.PostDuration)
		trn.ActiveTimelineRange, _, _ = intersection(trn.TimelineRange, p.RequestedRange)
		trn.Outgoing, _ = participant(trn.Outgoing.Binding, trn.ActiveTimelineRange)
		trn.Incoming, _ = participant(trn.Incoming.Binding, trn.ActiveTimelineRange)
	}
	rejectResigned(t, p)
	// Touching half-open windows remain valid.
	p.Transitions[0].PostDuration = rt(2, 1)
	trn := &p.Transitions[0]
	trn.TimelineRange, _ = transitionRange(trn.Cut, trn.PreDuration, trn.PostDuration)
	trn.ActiveTimelineRange, _, _ = intersection(trn.TimelineRange, p.RequestedRange)
	trn.Outgoing, _ = participant(trn.Outgoing.Binding, trn.ActiveTimelineRange)
	trn.Incoming, _ = participant(trn.Incoming.Binding, trn.ActiveTimelineRange)
	resign(t, &p)
	if _, e := Verify(p); e != nil {
		t.Fatal("touching transition windows rejected", e)
	}
}

func scopedTransitionDoc(t *testing.T, kind documentv2.TrackKind) documentv2.Document {
	t.Helper()
	if kind == documentv2.TrackKindVector {
		raw, e := os.ReadFile("../documentv2/testdata/vector-transition-document-v2.canonical.json")
		if e != nil {
			t.Fatal(e)
		}
		d, e := documentv2.Decode(raw)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	d := transitionDoc()
	source := documentv2.TextSource{Profile: documentv2.TextProfilePinnedFontViewportPXV2, Text: "Hello", FontAssetID: "font", FontSize: documentv2.Rational{Numerator: 12, Denominator: 1}, LineHeight: &documentv2.Rational{Numerator: 16, Denominator: 1}, Viewport: &documentv2.RasterViewport{WidthPixels: 200, HeightPixels: 50}, Fill: documentv2.ColorRGBA16{A: 65535}, Language: "en", Script: "Latn", Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart}
	for i := range d.Assets {
		d.Assets[i].Kind = documentv2.AssetKindText
		d.Assets[i].URI = ""
		s := source
		d.Assets[i].Text = &s
	}
	d.Assets = append(d.Assets, documentv2.Asset{ID: "font", Kind: documentv2.AssetKindFont, ContentHash: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"})
	d.Timelines[0].Tracks[0].Kind = kind
	return d
}
func TestTransitionSourceScopeCapabilities(t *testing.T) {
	for _, kind := range []documentv2.TrackKind{documentv2.TrackKindText, documentv2.TrackKindVector} {
		t.Run(string(kind), func(t *testing.T) {
			d := scopedTransitionDoc(t, kind)
			trn := d.Timelines[0].Transitions[0]
			cut := d.Timelines[0].Tracks[0].Clips[1].TimelineRange.Start
			start, e := cut.Sub(trn.PreDuration)
			if e != nil {
				t.Fatal(e)
			}
			p := resolveOK(t, d, documentv2.TimeRange{Start: start, Duration: trn.PreDuration})
			if len(p.Transitions) != 1 || len(p.Transitions[0].Incoming.Handles) != 0 {
				t.Fatal("static handle-only transition scope")
			}
			if e := VerifyAgainstDocument(context.Background(), p, d); e != nil {
				t.Fatal(e)
			}
			features := []videoedit.CapabilityID{documentv2.CapabilityTextTransitionScopeV1, documentv2.CapabilityTextPinnedFontViewportPXV2}
			if kind == documentv2.TrackKindVector {
				features = []videoedit.CapabilityID{documentv2.CapabilityVectorTransitionScopeV1, documentv2.CapabilityVectorPathViewportPXV2}
			}
			for _, f := range features {
				q := clone(t, p)
				if !contains(q.RequiredCapabilities.Features, f) {
					t.Fatal("resolver missing scope/source feature", f)
				}
				without(&q, f)
				rejectResigned(t, q)
			}
		})
	}
}

func TestNestedBusMasterLimiterClosure(t *testing.T) {
	d := mixedDoc(t)
	for i := range d.AudioBuses {
		if d.AudioBuses[i].Kind == documentv2.AudioBusMaster {
			d.AudioBuses[i].Properties.SamplePeakLimiter = &documentv2.AudioSamplePeakLimiterProperty{CeilingLinear: documentv2.Rational{Numerator: 9, Denominator: 10}}
		}
	}
	props := documentv2.AudioBusProperties{GainLinear: documentv2.Rational{Numerator: 1, Denominator: 1}, Pan: documentv2.Rational{Denominator: 1}}
	d.AudioBuses = append(d.AudioBuses, documentv2.AudioBus{ID: "nested-bus", Kind: documentv2.AudioBusGroup, ParentBusID: "bus-music", Properties: props}, documentv2.AudioBus{ID: "unused-bus", Kind: documentv2.AudioBusGroup, ParentBusID: "bus-master", Properties: props})
	for i := range d.Timelines[0].Tracks {
		if d.Timelines[0].Tracks[i].Kind == documentv2.TrackKindAudio {
			d.Timelines[0].Tracks[i].AudioBusID = "nested-bus"
		}
	}
	p := resolveOK(t, d, tr(0, 1))
	if !reflect.DeepEqual(p.AudioBusIDs, []documentv2.ID{"bus-master", "bus-music", "nested-bus"}) || !contains(p.RequiredCapabilities.Features, documentv2.CapabilityAudioSamplePeakLimiterV1) {
		t.Fatal("nested/master limiter closure", p.AudioBusIDs, p.RequiredCapabilities)
	}
	if e := VerifyAgainstDocument(context.Background(), p, d); e != nil {
		t.Fatal(e)
	}
	// A limiter is not inferable from IDs alone, so omission is detected only
	// against the Document. Minimum graph features remain standalone mandatory.
	without(&p, documentv2.CapabilityAudioSamplePeakLimiterV1)
	resign(t, &p)
	if _, e := Verify(p); e != nil {
		t.Fatal(e)
	}
	if VerifyAgainstDocument(context.Background(), p, d) == nil {
		t.Fatal("limiter omission not rejected against document")
	}
}

type checkpointContext struct {
	context.Context
	calls, cancelAt int
}

func (c *checkpointContext) Err() error {
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}
func TestEveryObservedCancellationCheckpoint(t *testing.T) {
	d := transitionDoc()
	counter := &checkpointContext{Context: context.Background()}
	p, ds := Resolve(counter, d, tr(4, 1), target())
	if documentv2.HasErrorDiagnostics(ds) || p.Fingerprint == "" {
		t.Fatal(ds)
	}
	total := counter.calls
	if total < 10 {
		t.Fatal("test did not traverse meaningful checkpoints")
	}
	for checkpoint := 1; checkpoint <= total; checkpoint++ {
		ctx := &checkpointContext{Context: context.Background(), cancelAt: checkpoint}
		p, ds := Resolve(ctx, d, tr(4, 1), target())
		if !reflect.DeepEqual(p, Plan{}) || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatalf("checkpoint %d/%d published partial plan", checkpoint, total)
		}
	}
	if e := VerifyAgainstDocument(panicContext{context.Background()}, Plan{}, d); !errors.Is(e, ErrInvalid) {
		t.Fatal("document verification panic escaped/error not invalid", e)
	}
}
