package evaluator

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func domainV2Document(t *testing.T) documentv2.Document {
	t.Helper()
	duration := rt(20, 1)
	d := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: "domain-v2", Revision: 1,
		Assets: []documentv2.Asset{{ID: "video", Kind: documentv2.AssetKindVideo, URI: "video.mp4", Duration: &duration}, {ID: "audio", Kind: documentv2.AssetKindAudio, URI: "audio.wav", Duration: &duration}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "audio-track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "audio-clip", AssetID: "audio", TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(6, 1)}, SourceRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(6, 1)}}}},
			{ID: "annotations", Kind: documentv2.TrackKindAnnotation},
			{ID: "video-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "a", AssetID: "video", TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(4, 1)}},
				{ID: "b", AssetID: "video", TimelineRange: documentv2.TimeRange{Start: rt(2, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(3, 1), Duration: rt(2, 1)}},
				{ID: "c", AssetID: "video", TimelineRange: documentv2.TimeRange{Start: rt(4, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)}},
			}},
		}, Transitions: []documentv2.Transition{
			{ID: "ab", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "a", IncomingClipID: "b", PreDuration: rt(1, 1), PostDuration: rt(1, 1)},
			{ID: "bc", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "b", IncomingClipID: "c", PreDuration: rt(0, 1), PostDuration: rt(0, 1)},
		}}},
	}
	if diagnostics := documentv2.Validate(d); documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("fixture: %+v", diagnostics)
	}
	return d
}

func domainV2Offered() videoedit.BackendCapabilities {
	return videoedit.BackendCapabilities{BackendID: "fixed-video-v2", TrackKinds: []videoedit.TrackKind{"video"}, AssetKinds: []videoedit.AssetKind{"video"}, Features: []videoedit.CapabilityID{videoedit.CapabilityVideoLinearRetime, videoedit.CapabilityID(documentv2.TransitionDissolveV1)}}
}

func domainV2Scope(start, duration documentv2.RationalTime, clips, transitions []documentv2.ID) DomainScope {
	return DomainScope{Domain: DomainVisual, RequestedRange: documentv2.TimeRange{Start: start, Duration: duration}, Selection: documentv2.CapabilitySelection{TimelineID: "timeline", ClipIDs: clips, TransitionIDs: transitions}}
}

func compileDomainV2(t *testing.T, d documentv2.Document, scope DomainScope, offered videoedit.BackendCapabilities) (*DomainProgram, *FrozenSnapshot) {
	t.Helper()
	p, s, diagnostics := CompileDomainSnapshotV2(context.Background(), d, scope, offered, domainTestLimits)
	if p == nil || s == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile: %+v", diagnostics)
	}
	return p, s
}

func TestDomainSnapshotV2ExactHandlesIdentityAndNoCrossScopeReplay(t *testing.T) {
	d := domainV2Document(t)
	wantHash, _ := documentv2.DocumentHash(d)
	for _, test := range []struct {
		start, at, outgoingSource, incomingSource documentv2.RationalTime
		progress                                  string
		outgoingVisible, incomingVisible          bool
	}{
		{rt(1, 1), rt(3, 2), rt(4, 1), rt(5, 2), "1/4", true, false},
		{rt(2, 1), rt(5, 2), rt(6, 1), rt(7, 2), "3/4", false, true},
	} {
		scope := domainV2Scope(test.start, rt(1, 1), []documentv2.ID{"b", "a"}, []documentv2.ID{"ab"})
		p, snapshot := compileDomainV2(t, d, scope, domainV2Offered())
		if p.DocumentHash() != wantHash || snapshot.DocumentHash() != wantHash || len(snapshot.Timelines()[0].Tracks) != 3 || len(snapshot.Timelines()[0].Tracks[2].Clips) != 3 || len(snapshot.Timelines()[0].Transitions) != 2 {
			t.Fatal("full original graph/hash/index not retained")
		}
		if len(p.program.clips["timeline"]) != 2 || len(p.program.transitions["timeline"]) != 1 || p.program.transitions["timeline"]["bc"] != nil {
			t.Fatal("compiler expanded explicit scope")
		}
		value, err := p.EvaluateTimeline("timeline", test.at)
		if err != nil || len(value.Layers) != 1 || value.Layers[0].Kind != VisualLayerTransition {
			t.Fatalf("actual transition evaluation %+v %v", value, err)
		}
		tr := value.Layers[0].Transition
		if tr.Progress == nil || tr.Progress.String() != test.progress || tr.Outgoing.SourceTime != test.outgoingSource || tr.Incoming.SourceTime != test.incomingSource || tr.Outgoing.Visible != test.outgoingVisible || tr.Incoming.Visible != test.incomingVisible || tr.Outgoing.TrackIndex != 2 || tr.Incoming.ClipIndex != 1 {
			t.Fatalf("exact handle sample: %+v outgoing=%+v incoming=%+v", tr, tr.Outgoing, tr.Incoming)
		}
		explicit, err := p.EvaluateTransition("timeline", "ab", test.at)
		if err != nil || !reflect.DeepEqual(explicit, *tr) {
			t.Fatalf("explicit sample %v", err)
		}
		frame, err := p.EvaluateVisualFrame("timeline", videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(2, 1)}, test.at.Value)
		if err != nil || !reflect.DeepEqual(frame.Timeline, value) {
			t.Fatalf("frame transition %v", err)
		}
		for _, at := range []documentv2.RationalTime{rt(0, 1), rt(3, 1), {}} {
			if v, err := p.EvaluateTransition("timeline", "ab", at); err == nil || !reflect.DeepEqual(v, TransitionEvaluation{}) {
				t.Fatal("transition replay outside request")
			}
			if v, err := p.EvaluateTimeline("timeline", at); err == nil || !reflect.DeepEqual(v, TimelineEvaluation{}) {
				t.Fatal("timeline replay outside request")
			}
		}
		for _, ids := range [][2]documentv2.ID{{"other", "ab"}, {"timeline", "bc"}, {"timeline", "unknown"}} {
			if v, err := p.EvaluateTransition(ids[0], ids[1], test.at); err == nil || !reflect.DeepEqual(v, TransitionEvaluation{}) {
				t.Fatal("lookup escaped scope")
			}
		}
		if _, err := p.EvaluateClip("timeline", "a", test.at); err == nil {
			t.Fatal("visual raw clip authority")
		}
	}
}

func TestDomainSnapshotV2TransitionBoundaryClosure(t *testing.T) {
	for _, test := range []struct {
		name               string
		start, duration    documentv2.RationalTime
		clips, transitions []documentv2.ID
		wantClip           documentv2.ID
		wantTransition     bool
	}{
		{"window ends at request start", rt(3, 1), rt(1, 1), []documentv2.ID{"b"}, nil, "b", false},
		{"window starts at request end", rt(0, 1), rt(1, 1), []documentv2.ID{"a"}, nil, "a", false},
		{"window start included", rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"}, "", true},
		{"hard cut at request start", rt(4, 1), rt(1, 1), []documentv2.ID{"b", "c"}, []documentv2.ID{"bc"}, "c", false},
		{"hard cut before request", rt(9, 2), rt(1, 2), []documentv2.ID{"c"}, nil, "c", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, _ := compileDomainV2(t, domainV2Document(t), domainV2Scope(test.start, test.duration, test.clips, test.transitions), domainV2Offered())
			v, err := p.EvaluateTimeline("timeline", test.start)
			if err != nil || len(v.Layers) != 1 {
				t.Fatalf("sample %+v %v", v, err)
			}
			if test.wantTransition {
				if v.Layers[0].Transition == nil || v.Layers[0].Transition.Progress.String() != "0/1" {
					t.Fatal("start boundary not active")
				}
			} else if v.Layers[0].Clip == nil || v.Layers[0].Clip.ClipID != test.wantClip {
				t.Fatal("ordinary/hard-cut clip incorrect")
			}
			if len(test.transitions) == 1 && test.transitions[0] == "bc" {
				tr, err := p.EvaluateTransition("timeline", "bc", test.start)
				if err != nil || tr.Phase != documentv2.TransitionPhaseIncoming || tr.Outgoing != nil || tr.Incoming != nil {
					t.Fatal("hard cut must not sample handles")
				}
			}
		})
	}
	// Multiple transition records are owned/sorted, but hard cuts never become active layers.
	p, _ := compileDomainV2(t, domainV2Document(t), domainV2Scope(rt(1, 1), rt(4, 1), []documentv2.ID{"c", "b", "a"}, []documentv2.ID{"bc", "ab"}), domainV2Offered())
	if !reflect.DeepEqual(p.Scope().Selection.TransitionIDs, []documentv2.ID{"ab", "bc"}) {
		t.Fatal("transition scope not canonicalized")
	}
}

func TestDomainSnapshotV2InvalidClosureFailureZero(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*documentv2.Document, *DomainScope)
	}{
		{"unknown transition", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TransitionIDs[0] = "unknown" }},
		{"duplicate transition", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TransitionIDs = []documentv2.ID{"ab", "ab"} }},
		{"omitted transition", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TransitionIDs = nil }},
		{"extra transition", func(_ *documentv2.Document, s *DomainScope) {
			s.Selection.TransitionIDs = append(s.Selection.TransitionIDs, "bc")
		}},
		{"missing handle", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs = []documentv2.ID{"a"} }},
		{"extra clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs = append(s.Selection.ClipIDs, "c") }},
		{"duplicate clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs = []documentv2.ID{"a", "a"} }},
		{"unknown clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[1] = "unknown" }},
		{"audio clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[1] = "audio-clip" }},
		{"unknown domain", func(_ *documentv2.Document, s *DomainScope) { s.Domain = "other" }},
		{"unknown timeline", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TimelineID = "other" }},
		{"zero range", func(_ *documentv2.Document, s *DomainScope) { s.RequestedRange.Duration = rt(0, 1) }},
		{"range overflow", func(_ *documentv2.Document, s *DomainScope) { s.RequestedRange.Start = rt(math.MaxInt64, 1) }},
		{"unsupported transition", func(d *documentv2.Document, _ *DomainScope) { d.Timelines[0].Transitions[0].Profile = "unknown" }},
		{"disabled participant", func(d *documentv2.Document, _ *DomainScope) { d.Timelines[0].Tracks[2].Clips[1].Disabled = true }},
		{"missing source handle", func(d *documentv2.Document, _ *DomainScope) {
			d.Timelines[0].Tracks[2].Clips[1].SourceRange.Start = rt(0, 1)
		}},
		{"unsupported audio transition", func(d *documentv2.Document, _ *DomainScope) {
			track := &d.Timelines[0].Tracks[2]
			track.Kind = documentv2.TrackKindAudio
			for i := range track.Clips {
				track.Clips[i].AssetID = "audio"
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, scope := domainV2Document(t), domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"})
			test.change(&d, &scope)
			p, s, diagnostics := CompileDomainSnapshotV2(context.Background(), d, scope, domainV2Offered(), domainTestLimits)
			if p != nil || s != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("published failure: %+v", diagnostics)
			}
		})
	}
}

func TestDomainSnapshotV2AudioAndV1RemainIsolated(t *testing.T) {
	d := domainV2Document(t)
	scope := domainV2Scope(rt(1, 1), rt(4, 1), []documentv2.ID{"audio-clip"}, nil)
	scope.Domain = DomainAudio
	p, snapshot := compileDomainV2(t, d, scope, domainTestOffered(DomainAudio))
	if len(p.program.transitions["timeline"]) != 0 || len(p.program.clips["timeline"]) != 1 || len(snapshot.Timelines()[0].Transitions) != 2 {
		t.Fatal("audio compiled visual transitions or pruned discovery")
	}
	if clip, err := p.EvaluateClip("timeline", "audio-clip", rt(5, 2)); err != nil || clip.SourceTime != rt(5, 2) {
		t.Fatalf("audio sample %+v %v", clip, err)
	}
	if v, err := p.EvaluateTimeline("timeline", rt(2, 1)); err == nil || !reflect.DeepEqual(v, TimelineEvaluation{}) {
		t.Fatal("audio acquired visual authority")
	}
	if v, err := p.EvaluateTransition("timeline", "ab", rt(2, 1)); err == nil || !reflect.DeepEqual(v, TransitionEvaluation{}) {
		t.Fatal("audio acquired transition authority")
	}
	if v, err := p.EvaluateVisualFrame("timeline", videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(1, 1)}, 2); err == nil || !reflect.DeepEqual(v, VisualFrameEvaluation{}) {
		t.Fatal("audio acquired frame authority")
	}
	scope.Selection.TransitionIDs = []documentv2.ID{"ab"}
	if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, domainTestOffered(DomainAudio), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("audio visual transition selection allowed")
	}
	scope.Selection.TransitionIDs = nil
	if p, s, ds := CompileDomainSnapshot(context.Background(), d, scope, domainTestOffered(DomainAudio), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("V1 transition rejection relaxed")
	}
	for _, p := range []*DomainProgram{nil, {}, func() *DomainProgram { p, _ := compileDomainTest(t, domainTestDocument(t), DomainVisual); return p }()} {
		if v, err := p.EvaluateTransition("timeline-main", "ab", rt(2, 1)); err == nil || !reflect.DeepEqual(v, TransitionEvaluation{}) {
			t.Fatal("V1/zero program gained transition authority")
		}
	}
}

func TestDomainSnapshotV2FixedCapabilitiesAndUnselectedGraph(t *testing.T) {
	d := domainV2Document(t)
	// This handle-only participant still needs all its rendering capabilities.
	d.Timelines[0].Tracks[2].Clips[1].PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{Namespace: "test.handle.v1", AffectsRendering: true, RequiredCapability: "test.handle.v1", Value: json.RawMessage(`{}`)}}}
	scope := domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"})
	for _, offered := range []videoedit.BackendCapabilities{domainV2Offered(), {BackendID: "empty"}} {
		if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, offered, domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatal("missing handle capabilities admitted")
		}
	}
	offered := domainV2Offered()
	offered.Features = append(offered.Features, "test.handle.v1")
	compileDomainV2(t, d, scope, offered)
	offered.Features = []videoedit.CapabilityID{videoedit.CapabilityVideoLinearRetime, "test.handle.v1"}
	if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, offered, domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("missing dissolve capability admitted")
	}
	// No inference from two selected clips: an out-of-range transition must not be compiled.
	d.Timelines[0].Tracks[2].Clips[1].PropertySet = nil
	offered = domainV2Offered()
	offered.Features = []videoedit.CapabilityID{videoedit.CapabilityVideoLinearRetime}
	compileDomainV2(t, d, domainV2Scope(rt(3, 1), rt(1, 1), []documentv2.ID{"b"}, nil), offered)
	// Hard cuts do not require the dissolve capability.
	compileDomainV2(t, d, domainV2Scope(rt(4, 1), rt(1, 1), []documentv2.ID{"b", "c"}, []documentv2.ID{"bc"}), offered)
}

func TestDomainSnapshotV2AudioBusCapabilitiesStillRequired(t *testing.T) {
	d := domainV2Document(t)
	properties := documentv2.AudioBusProperties{GainLinear: documentv2.Rational{Numerator: 1, Denominator: 1}, Pan: documentv2.Rational{Numerator: 0, Denominator: 1}}
	d.AudioBuses = []documentv2.AudioBus{
		{ID: "master", Kind: documentv2.AudioBusMaster, Properties: properties},
		{ID: "group", Kind: documentv2.AudioBusGroup, ParentBusID: "master", Properties: properties},
	}
	d.Timelines[0].Tracks[0].AudioBusID = "group"
	scope := domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"audio-clip"}, nil)
	scope.Domain = DomainAudio
	offered := domainTestOffered(DomainAudio)
	if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, offered, domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("routed bus capability gate relaxed")
	}
	offered.Features = []videoedit.CapabilityID{documentv2.CapabilityAudioRouteV1, documentv2.CapabilityAudioGainV1, documentv2.CapabilityAudioPanV1}
	p, _ := compileDomainV2(t, d, scope, offered)
	if v, err := p.EvaluateClip("timeline", "audio-clip", rt(3, 2)); err != nil || v.SourceTime != rt(3, 2) {
		t.Fatalf("routed audio sample %+v %v", v, err)
	}
	// Visual selection does not acquire unrelated audio bus requirements.
	compileDomainV2(t, d, domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"}), domainV2Offered())
}

func TestDomainSnapshotV2OtherTimelineAndEmptyDomain(t *testing.T) {
	d := domainV2Document(t)
	d.Timelines = append(d.Timelines, documentv2.Timeline{ID: "other", Tracks: []documentv2.Track{{ID: "other-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "other-clip", AssetID: "video", TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)}}}}}})
	scope := domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"})
	p, snapshot := compileDomainV2(t, d, scope, domainV2Offered())
	if len(p.program.timelines) != 1 || len(snapshot.Timelines()) != 2 {
		t.Fatal("other timeline compiled/pruned")
	}
	scope.Selection.ClipIDs[0] = "other-clip"
	if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, domainV2Offered(), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("cross-timeline closure accepted")
	}
	scope.Domain = DomainAudio
	scope.Selection = documentv2.CapabilitySelection{TimelineID: "other"}
	p, _ = compileDomainV2(t, d, scope, domainTestOffered(DomainAudio))
	if v, err := p.EvaluateClip("other", "other-clip", rt(3, 2)); err == nil || !reflect.DeepEqual(v, ClipEvaluation{}) {
		t.Fatal("empty domain gained clip authority")
	}
}

func TestDomainSnapshotV2NativeVisualTransitionProfiles(t *testing.T) {
	for _, kind := range []documentv2.TrackKind{documentv2.TrackKindVector, documentv2.TrackKindText} {
		t.Run(string(kind), func(t *testing.T) {
			path := "../documentv2/testdata/vector-transition-document-v2.canonical.json"
			if kind == documentv2.TrackKindText {
				path = "../documentv2/testdata/text-pinned-font-viewport-v2.document.canonical.json"
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			d, err := documentv2.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			offered := videoedit.BackendCapabilities{BackendID: "fixed-native", TrackKinds: []videoedit.TrackKind{videoedit.TrackKind(kind)}, Features: []videoedit.CapabilityID{videoedit.CapabilityID(documentv2.TransitionDissolveV1)}}
			transitionID := documentv2.ID("vector-dissolve")
			if kind == documentv2.TrackKindVector {
				offered.AssetKinds = []videoedit.AssetKind{"vector"}
				offered.Features = append(offered.Features, documentv2.CapabilityVectorPathViewportPXV2, documentv2.CapabilityVectorTransitionScopeV1)
			} else {
				offered.AssetKinds = []videoedit.AssetKind{"text", "font"}
				offered.Features = append(offered.Features, documentv2.CapabilityTextPinnedFontViewportPXV2, documentv2.CapabilityTextTransitionScopeV1)
				first := &d.Timelines[0].Tracks[0].Clips[0]
				first.ID = "outgoing"
				first.TimelineRange.Duration, first.SourceRange.Duration = rt(2, 1), rt(2, 1)
				second := *first
				second.ID, second.TimelineRange.Start = "incoming", rt(2, 1)
				d.Timelines[0].Tracks[0].Clips = append(d.Timelines[0].Tracks[0].Clips, second)
				transitionID = "text-dissolve"
				d.Timelines[0].Transitions = []documentv2.Transition{{ID: transitionID, Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "outgoing", IncomingClipID: "incoming", PreDuration: rt(1, 1), PostDuration: rt(1, 1)}}
			}
			scope := domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"outgoing", "incoming"}, []documentv2.ID{transitionID})
			p, _ := compileDomainV2(t, d, scope, offered)
			v, err := p.EvaluateTransition("timeline", transitionID, rt(3, 2))
			if err != nil || v.Progress.String() != "1/4" || v.Incoming.Visible || v.Incoming.SourceTime != rt(-1, 2) {
				t.Fatalf("native handle sample %+v %v", v, err)
			}
			// The profile-specific transition scope feature cannot be synthesized.
			offered.Features = offered.Features[:len(offered.Features)-1]
			if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, offered, domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatal("native transition feature omitted")
			}
		})
	}
}

func TestDomainSnapshotV2AdmissionPanicCancellationFailureZero(t *testing.T) {
	d, scope := domainV2Document(t), domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"})
	probe := &domainCancelContext{Context: context.Background(), cancelAt: math.MaxInt}
	p, _, ds := CompileDomainSnapshotV2(probe, d, scope, domainV2Offered(), domainTestLimits)
	if p == nil {
		t.Fatalf("probe %+v", ds)
	}
	for _, stage := range []int{1, 10, probe.calls / 2, probe.calls - 1, probe.calls} {
		ctx := &domainCancelContext{Context: context.Background(), cancelAt: stage}
		if p, s, ds := CompileDomainSnapshotV2(ctx, d, scope, domainV2Offered(), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatalf("cancel published at %d %+v", stage, ds)
		}
		formatted := false
		panicCtx := &domainPanicContext{Context: context.Background(), panicAt: stage, formatted: &formatted}
		if p, s, ds := CompileDomainSnapshotV2(panicCtx, d, scope, domainV2Offered(), domainTestLimits); p != nil || s != nil || len(ds) != 1 || ds[0].Code != CodeDomainSnapshotPanic || formatted {
			t.Fatalf("panic published/formatted at %d %+v", stage, ds)
		}
	}
	if p, s, ds := CompileDomainSnapshotV2(nil, d, scope, domainV2Offered(), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("nil context")
	}
	for _, limits := range []SnapshotLimits{{}, {MaxCanonicalDocumentBytes: 1 << 20}, {MaxDocumentNodes: 1 << 20}, {MaxCanonicalDocumentBytes: 100, MaxDocumentNodes: 1 << 20}, {MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 2}} {
		if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, domainV2Offered(), limits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatal("resource limit ignored")
		}
	}
	for _, input := range []string{"clip IDs", "transition IDs", "offers"} {
		scope := cloneDomainScope(scope)
		offered := domainV2Offered()
		large := strings.Repeat("x", 1<<20)
		switch input {
		case "clip IDs":
			scope.Selection.ClipIDs = []documentv2.ID{documentv2.ID(large)}
		case "transition IDs":
			scope.Selection.TransitionIDs = []documentv2.ID{documentv2.ID(large)}
		case "offers":
			offered.BackendID = large
		}
		if p, s, ds := CompileDomainSnapshotV2(context.Background(), d, scope, offered, domainTestLimits); p != nil || s != nil || len(ds) == 0 || !strings.Contains(ds[0].Message, ErrCanonicalDocumentLimit.Error()) {
			t.Fatalf("preflight not first: %s %+v", input, ds)
		}
	}
}

func TestDomainSnapshotV2CheckedOverflowAndEvaluationFailureZero(t *testing.T) {
	d := domainV2Document(t)
	scope := domainV2Scope(rt(1, 1), rt(2, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"})
	p, _ := compileDomainV2(t, d, scope, domainV2Offered())
	// Exact progress has denominator 2*MaxInt64: no float rounding, wrap or partial result.
	at := rt(math.MaxInt64-1, math.MaxInt64-2)
	if v, err := p.EvaluateTransition("timeline", "ab", at); !errors.Is(err, ErrValueNotWireSafe) || !reflect.DeepEqual(v, TransitionEvaluation{}) {
		t.Fatalf("progress overflow %+v %v", v, err)
	}
	if v, err := p.EvaluateTimeline("timeline", at); !errors.Is(err, ErrValueNotWireSafe) || !reflect.DeepEqual(v, TimelineEvaluation{}) {
		t.Fatalf("timeline overflow %+v %v", v, err)
	}
	clock := videoedit.FrameClock{Origin: rt(math.MaxInt64, 1), FramesPerSecond: rt(1, 1)}
	if v, err := p.EvaluateVisualFrame("timeline", clock, 1); !errors.Is(err, ErrTimeOverflow) || !reflect.DeepEqual(v, VisualFrameEvaluation{}) {
		t.Fatalf("clock overflow %+v %v", v, err)
	}
	// Unexpected internal evaluation panics must not publish any result.
	p.program.transitions["timeline"]["ab"].outgoing = nil
	if v, err := p.EvaluateTransition("timeline", "ab", rt(3, 2)); err == nil || !reflect.DeepEqual(v, TransitionEvaluation{}) {
		t.Fatalf("panic result %+v %v", v, err)
	}
	if v, err := p.EvaluateTimeline("timeline", rt(3, 2)); err == nil || !reflect.DeepEqual(v, TimelineEvaluation{}) {
		t.Fatalf("timeline panic result %+v %v", v, err)
	}
}

func TestDomainSnapshotV2AdjacentDissolvesDoNotExpandFromHandleOnlyClips(t *testing.T) {
	d := domainV2Document(t)
	d.Timelines[0].Transitions[1].PreDuration = rt(1, 1)
	// The two windows exactly touch at 3. Selecting b only as ab's handle does not select bc.
	p, _ := compileDomainV2(t, d, domainV2Scope(rt(1, 1), rt(1, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"}), domainV2Offered())
	if len(p.program.transitions["timeline"]) != 1 || p.program.clips["timeline"]["c"] != nil {
		t.Fatal("transitive closure granted")
	}
	p, _ = compileDomainV2(t, d, domainV2Scope(rt(3, 1), rt(1, 1), []documentv2.ID{"b", "c"}, []documentv2.ID{"bc"}), domainV2Offered())
	v, err := p.EvaluateTimeline("timeline", rt(3, 1))
	if err != nil || len(v.Layers) != 1 || v.Layers[0].Transition.TransitionID != "bc" || v.Layers[0].Transition.Progress.String() != "0/1" {
		t.Fatalf("touching windows %+v %v", v, err)
	}
}

func TestDomainSnapshotV2TransitionPhaseOnlyOutsideWindowWithinScope(t *testing.T) {
	p, _ := compileDomainV2(t, domainV2Document(t), domainV2Scope(rt(0, 1), rt(4, 1), []documentv2.ID{"a", "b"}, []documentv2.ID{"ab"}), domainV2Offered())
	for _, test := range []struct {
		at    documentv2.RationalTime
		phase documentv2.TransitionPhase
	}{{rt(0, 1), documentv2.TransitionPhaseOutgoing}, {rt(3, 1), documentv2.TransitionPhaseIncoming}} {
		v, err := p.EvaluateTransition("timeline", "ab", test.at)
		if err != nil || v.Phase != test.phase || v.Outgoing != nil || v.Incoming != nil || v.Progress != nil {
			t.Fatalf("phase-only %+v %v", v, err)
		}
	}
}

func TestDomainSnapshotV2OwnedImmutableConcurrentScope(t *testing.T) {
	d, scope, offered := domainV2Document(t), domainV2Scope(rt(2, 2), rt(4, 1), []documentv2.ID{"c", "b", "a"}, []documentv2.ID{"bc", "ab"}), domainV2Offered()
	p, snapshot := compileDomainV2(t, d, scope, offered)
	before, _ := p.EvaluateTransition("timeline", "ab", rt(3, 2))
	d.Timelines[0].Transitions[0].PreDuration = rt(0, 1)
	d.Timelines[0].Tracks[2].Clips[1].SourceRange.Start = rt(10, 1)
	scope.Selection.ClipIDs[0], scope.Selection.TransitionIDs[0], offered.Features[0] = "mutated", "mutated", "mutated"
	owned := p.Scope()
	owned.Selection.ClipIDs[0], owned.Selection.TransitionIDs[0] = "mutated", "mutated"
	if p.Scope().RequestedRange.Start != rt(2, 2) {
		t.Fatal("rational representation lost")
	}
	timelines := snapshot.Timelines()
	timelines[0].Transitions[0].ID = "mutated"
	after, _ := p.EvaluateTransition("timeline", "ab", rt(3, 2))
	if !reflect.DeepEqual(before, after) || snapshot.Timelines()[0].Transitions[0].ID != "ab" {
		t.Fatal("caller alias")
	}
	*before.Progress = exactInteger(999)
	*before.Incoming.Properties.Opacity = exactInteger(999)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				v, err := p.EvaluateTransition("timeline", "ab", rt(3, 2))
				if err != nil || v.Progress.String() != "1/4" || v.Incoming.SourceTime != rt(5, 2) || v.Incoming.Properties.Opacity.String() != "1/1" {
					t.Errorf("concurrent sample %+v %v", v, err)
					return
				}
				*v.Progress = exactInteger(999)
				*v.Incoming.Properties.Opacity = exactInteger(999)
				s := p.Scope()
				s.Selection.TransitionIDs[0] = "local"
				ts := snapshot.Timelines()
				ts[0].Transitions[0].ID = "local"
			}
		}()
	}
	wg.Wait()
	descriptor := DomainSnapshotDescriptorV2()
	if descriptor.SchemaVersion != 2 || descriptor.Profile != DomainSnapshotProfileV2 || descriptor.TransitionSemantics == "" || DomainSnapshotDescriptor().Profile != DomainSnapshotProfileV1 {
		t.Fatal("profile identity")
	}
	descriptor.Profile = "mutated"
	if DomainSnapshotDescriptorV2().Profile != DomainSnapshotProfileV2 {
		t.Fatal("descriptor alias")
	}
}
