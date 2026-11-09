package evaluator

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

var domainTestLimits = SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}

// Explicit fixed backend declarations, deliberately NOT constructed from requirements.
func domainTestOffered(domain Domain) videoedit.BackendCapabilities {
	if domain == DomainAudio {
		return videoedit.BackendCapabilities{BackendID: "fixed-audio", TrackKinds: []videoedit.TrackKind{"audio"}, AssetKinds: []videoedit.AssetKind{"audio"}}
	}
	return videoedit.BackendCapabilities{
		BackendID: "fixed-visual", TrackKinds: []videoedit.TrackKind{"video", "vector", "text"},
		AssetKinds: []videoedit.AssetKind{"image", "vector", "text", "font"},
		Features:   []videoedit.CapabilityID{documentv2.CapabilityTextV1, documentv2.CapabilityVectorPathV1},
	}
}

func domainTestDocument(t *testing.T) documentv2.Document {
	document := validDocument(t)
	document.Timelines[0].Transitions = nil
	document.AudioBuses = nil
	document.Assets[0].Kind = documentv2.AssetKindImage
	document.Assets[0].URI = "image.png"
	document.Assets[0].Duration = nil
	tracks := document.Timelines[0].Tracks
	for ti := range tracks {
		tracks[ti].AudioBusID = ""
		for ci := range tracks[ti].Clips {
			tracks[ti].Clips[ci].PropertySet = nil
			tracks[ti].Clips[ci].KeyframeLanes = nil
		}
	}
	// Interleave audio/annotation/visual tracks, retain original clip index 1 for selected image.
	image := tracks[0].Clips[0]
	image.ID = "image-selected"
	image.TimelineRange = documentv2.TimeRange{Start: rt(2, 1), Duration: rt(3, 1)}
	previous := image
	previous.ID = "image-before"
	previous.TimelineRange = documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)}
	previous.PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{Namespace: "test.unsupported.v1", AffectsRendering: true, RequiredCapability: "test.unsupported.v1", Value: json.RawMessage(`{"input":"not selected"}`)}}}
	tracks[0].Clips = []documentv2.Clip{previous, image}
	tracks[1].Clips[0].TimelineRange = documentv2.TimeRange{Start: rt(2, 1), Duration: rt(3, 1)}
	tracks[1].Clips[0].SourceRange.Duration = rt(3, 1)
	tracks[1].Metadata = map[string]json.RawMessage{"other_domain": json.RawMessage(`{"preserve":true}`)}
	document.Timelines[0].Tracks = []documentv2.Track{tracks[1], tracks[4], tracks[0], tracks[2], tracks[3]}
	if diagnostics := documentv2.Validate(document); documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("fixture: %+v", diagnostics)
	}
	return document
}

func domainTestScope(domain Domain) DomainScope {
	ids := []documentv2.ID{"clip-vector", "image-selected", "clip-text"}
	if domain == DomainAudio {
		ids = []documentv2.ID{"clip-audio"}
	}
	return DomainScope{Domain: domain, Selection: documentv2.CapabilitySelection{TimelineID: "timeline-main", ClipIDs: ids}, RequestedRange: documentv2.TimeRange{Start: rt(4, 2), Duration: rt(2, 1)}}
}

func compileDomainTest(t *testing.T, document documentv2.Document, domain Domain) (*DomainProgram, *FrozenSnapshot) {
	t.Helper()
	program, snapshot, diagnostics := CompileDomainSnapshot(context.Background(), document, domainTestScope(domain), domainTestOffered(domain), domainTestLimits)
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile: %+v", diagnostics)
	}
	return program, snapshot
}

func TestDomainSnapshotMixedFullIdentityIndexesAndSelectionBeforeEvaluation(t *testing.T) {
	document := domainTestDocument(t)
	wantHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	visual, snapshot := compileDomainTest(t, document, DomainVisual)
	audio, audioSnapshot := compileDomainTest(t, document, DomainAudio)
	for _, hash := range []string{visual.DocumentHash(), audio.DocumentHash(), snapshot.DocumentHash(), audioSnapshot.DocumentHash()} {
		if hash != wantHash {
			t.Fatalf("hash %s want %s", hash, wantHash)
		}
	}
	// Full discovery includes unselected and opposite-domain graph, not just compiled tracks.
	if len(snapshot.Timelines()[0].Tracks) != 5 || len(snapshot.Timelines()[0].Tracks[2].Clips) != 2 {
		t.Fatal("snapshot was pruned")
	}
	if len(visual.program.clips["timeline-main"]) != 3 || visual.program.clips["timeline-main"]["image-before"] != nil || visual.program.clips["timeline-main"]["clip-audio"] != nil {
		t.Fatal("unselected graph compiled")
	}
	if len(audio.program.clips["timeline-main"]) != 1 {
		t.Fatal("audio compiled visual graph")
	}
	timeline, err := visual.EvaluateTimeline("timeline-main", rt(2, 1))
	if err != nil || len(timeline.Layers) != 3 {
		t.Fatalf("timeline=%+v err=%v", timeline, err)
	}
	for i, wantIndex := range []int{2, 3, 4} {
		if timeline.Layers[i].TrackIndex != wantIndex || timeline.Layers[i].Clip.TrackIndex != wantIndex {
			t.Fatal("track index changed")
		}
	}
	if timeline.Layers[0].Clip.ClipIndex != 1 || timeline.Layers[0].Clip.ClipID != "image-selected" {
		t.Fatal("same asset scope lost clip identity/index")
	}
	clip, err := audio.EvaluateClip("timeline-main", "clip-audio", rt(2, 1))
	if err != nil || clip.TrackIndex != 0 || clip.ClipIndex != 0 {
		t.Fatalf("clip=%+v err=%v", clip, err)
	}
	// Prove the old whole-document mixed-domain capability gate independently of the unknown
	// unselected extension used above to prove selection-before-compilation behavior.
	legacyDocument := domainTestDocument(t)
	legacyDocument.Timelines[0].Tracks[2].Clips[0].PropertySet = nil
	if p, d := Compile(legacyDocument, domainTestOffered(DomainVisual)); p != nil || !documentv2.HasErrorDiagnostics(d) {
		t.Fatal("legacy mixed capability gate relaxed")
	}
	if p, s, d := CompileSnapshot(context.Background(), legacyDocument, domainTestOffered(DomainAudio), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) {
		t.Fatal("legacy snapshot gate relaxed")
	}
	changed := domainTestDocument(t)
	changed.Timelines[0].Tracks[0].Metadata["other_domain"] = json.RawMessage(`{"preserve":false}`)
	other, _ := compileDomainTest(t, changed, DomainVisual)
	if other.DocumentHash() == visual.DocumentHash() {
		t.Fatal("other-domain metadata excluded from identity")
	}
}

func TestDomainSnapshotScopeValidationFailureZero(t *testing.T) {
	tests := []struct {
		name   string
		change func(*documentv2.Document, *DomainScope)
	}{
		{"unknown domain", func(_ *documentv2.Document, s *DomainScope) { s.Domain = "unknown" }},
		{"empty timeline", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TimelineID = "" }},
		{"unknown timeline", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TimelineID = "absent" }},
		{"unknown clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[0] = "absent" }},
		{"opposite clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[0] = "clip-audio" }},
		{"empty clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[0] = "" }},
		{"duplicate clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[0] = s.Selection.ClipIDs[1] }},
		{"missing clip", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs = s.Selection.ClipIDs[:2] }},
		{"nonintersecting same asset", func(_ *documentv2.Document, s *DomainScope) { s.Selection.ClipIDs[1] = "image-before" }},
		{"disabled clip", func(d *documentv2.Document, _ *DomainScope) { d.Timelines[0].Tracks[3].Clips[0].Disabled = true }},
		{"invalid other domain reference", func(d *documentv2.Document, _ *DomainScope) { d.Timelines[0].Tracks[0].Clips[0].AssetID = "absent" }},
		{"invalid unselected lane", func(d *documentv2.Document, _ *DomainScope) {
			d.Timelines[0].Tracks[2].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{{ID: "bad"}}
		}},
		{"zero range", func(_ *documentv2.Document, s *DomainScope) { s.RequestedRange.Duration = rt(0, 1) }},
		{"negative duration", func(_ *documentv2.Document, s *DomainScope) { s.RequestedRange.Duration = rt(-1, 1) }},
		{"invalid rational", func(_ *documentv2.Document, s *DomainScope) { s.RequestedRange.Start.Rate = 0 }},
		{"range overflow", func(_ *documentv2.Document, s *DomainScope) { s.RequestedRange.Start = rt(math.MaxInt64, 1) }},
		{"selected transition", func(_ *documentv2.Document, s *DomainScope) { s.Selection.TransitionIDs = []documentv2.ID{"unknown"} }},
		{"authored hard cut", func(d *documentv2.Document, _ *DomainScope) {
			d.Timelines[0].Transitions = []documentv2.Transition{{ID: "cut", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "image-before", IncomingClipID: "image-selected", PreDuration: rt(0, 1), PostDuration: rt(0, 1)}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, scope := domainTestDocument(t), domainTestScope(DomainVisual)
			test.change(&document, &scope)
			p, s, d := CompileDomainSnapshot(context.Background(), document, scope, domainTestOffered(DomainVisual), domainTestLimits)
			if p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) {
				t.Fatalf("failure published p=%v s=%v d=%+v", p, s, d)
			}
		})
	}
}

func TestDomainSnapshotMissingCapabilities(t *testing.T) {
	for _, missing := range []string{"track", "asset", "feature"} {
		t.Run(missing, func(t *testing.T) {
			offered := domainTestOffered(DomainVisual)
			switch missing {
			case "track":
				offered.TrackKinds = nil
			case "asset":
				offered.AssetKinds = nil
			case "feature":
				offered.Features = nil
			}
			p, s, d := CompileDomainSnapshot(context.Background(), domainTestDocument(t), domainTestScope(DomainVisual), offered, domainTestLimits)
			if p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) {
				t.Fatalf("missing capability: %+v", d)
			}
		})
	}
}

func TestDomainSnapshotRuntimeBoundaryAndZeroValues(t *testing.T) {
	document := domainTestDocument(t)
	visual, _ := compileDomainTest(t, document, DomainVisual)
	audio, _ := compileDomainTest(t, document, DomainAudio)
	for _, at := range []documentv2.RationalTime{rt(1, 1), rt(4, 1), rt(5, 1), {}} {
		if value, err := visual.EvaluateTimeline("timeline-main", at); err == nil || !reflect.DeepEqual(value, TimelineEvaluation{}) {
			t.Fatalf("visual outside scope: %+v %v", value, err)
		}
		if value, err := audio.EvaluateClip("timeline-main", "clip-audio", at); err == nil || !reflect.DeepEqual(value, ClipEvaluation{}) {
			t.Fatalf("audio outside scope: %+v %v", value, err)
		}
	}
	if _, err := visual.EvaluateTimeline("wrong", rt(2, 1)); err == nil {
		t.Fatal("wrong timeline")
	}
	if _, err := audio.EvaluateClip("wrong", "clip-audio", rt(2, 1)); err == nil {
		t.Fatal("wrong audio timeline")
	}
	if _, err := visual.EvaluateClip("timeline-main", "image-selected", rt(2, 1)); err == nil {
		t.Fatal("visual clip method allowed")
	}
	if _, err := audio.EvaluateTimeline("timeline-main", rt(2, 1)); err == nil {
		t.Fatal("audio timeline allowed")
	}
	for _, id := range []documentv2.ID{"image-selected", "absent", "image-before"} {
		if _, err := audio.EvaluateClip("timeline-main", id, rt(2, 1)); err == nil {
			t.Fatal("clip lookup escaped")
		}
	}
	clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(30000, 1001)}
	frame, err := visual.EvaluateVisualFrame("timeline-main", clock, 60)
	if err != nil {
		t.Fatal(err)
	}
	wantTime, _ := videoedit.FrameTimeAt(clock, 60)
	if frame.Timeline.TimelineTime != wantTime {
		t.Fatal("frame clock drift")
	}
	for _, program := range []*DomainProgram{nil, {}, audio} {
		if value, err := program.EvaluateVisualFrame("timeline-main", clock, 60); err == nil || !reflect.DeepEqual(value, VisualFrameEvaluation{}) {
			t.Fatal("wrong/zero domain frame")
		}
	}
	for _, index := range []int64{59, 120} {
		if _, err := visual.EvaluateVisualFrame("timeline-main", clock, index); err == nil {
			t.Fatal("frame range escaped")
		}
	}
	if _, err := visual.EvaluateVisualFrame("wrong", clock, 60); err == nil {
		t.Fatal("frame timeline escaped")
	}
	if _, err := visual.EvaluateVisualFrame("timeline-main", videoedit.FrameClock{}, 0); err == nil {
		t.Fatal("invalid frame clock")
	}
	overflowClock := videoedit.FrameClock{Origin: rt(math.MaxInt64, 1), FramesPerSecond: rt(1, 1)}
	if _, err := visual.EvaluateVisualFrame("timeline-main", overflowClock, math.MaxInt64); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("overflow=%v", err)
	}
	for _, zero := range []*DomainProgram{nil, {}} {
		if zero.DocumentHash() != "" || !reflect.DeepEqual(zero.Scope(), DomainScope{}) {
			t.Fatal("zero getter")
		}
		if _, err := zero.EvaluateTimeline("timeline-main", rt(2, 1)); err == nil {
			t.Fatal("zero timeline")
		}
		if _, err := zero.EvaluateClip("timeline-main", "clip-audio", rt(2, 1)); err == nil {
			t.Fatal("zero clip")
		}
	}
	// Scope may extend past an audio clip; clip handles must still be rejected.
	scope := domainTestScope(DomainAudio)
	scope.RequestedRange = documentv2.TimeRange{Start: rt(1, 1), Duration: rt(5, 1)}
	p, _, d := CompileDomainSnapshot(context.Background(), document, scope, domainTestOffered(DomainAudio), domainTestLimits)
	if p == nil {
		t.Fatalf("compile wider audio scope: %+v", d)
	}
	for _, at := range []documentv2.RationalTime{rt(1, 1), rt(5, 1)} {
		if _, err := p.EvaluateClip("timeline-main", "clip-audio", at); err == nil {
			t.Fatal("audio transition handle escaped")
		}
	}
}

type domainPanicValue struct{ formatted *bool }

func (p domainPanicValue) String() string { *p.formatted = true; panic("must not format") }

type domainPanicContext struct {
	context.Context
	calls, panicAt int
	formatted      *bool
}

func (ctx *domainPanicContext) Err() error {
	ctx.calls++
	if ctx.calls == ctx.panicAt {
		panic(domainPanicValue{ctx.formatted})
	}
	return nil
}

type domainCancelContext struct {
	context.Context
	calls, cancelAt int
}

func (ctx *domainCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestDomainSnapshotAdmissionCancellationPanicFailureZero(t *testing.T) {
	document, scope := domainTestDocument(t), domainTestScope(DomainVisual)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, &domainCancelContext{Context: context.Background(), cancelAt: 10}, &domainCancelContext{Context: context.Background(), cancelAt: 40}} {
		p, s, d := CompileDomainSnapshot(ctx, document, scope, domainTestOffered(DomainVisual), domainTestLimits)
		if p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) {
			t.Fatalf("context failure: %+v", d)
		}
	}
	for _, limits := range []SnapshotLimits{{}, {MaxCanonicalDocumentBytes: 1 << 20}, {MaxDocumentNodes: 1 << 20}, {MaxCanonicalDocumentBytes: 100, MaxDocumentNodes: 1 << 20}, {MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 2}} {
		p, s, d := CompileDomainSnapshot(context.Background(), document, scope, domainTestOffered(DomainVisual), limits)
		if p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) {
			t.Fatalf("limit failure: %+v", d)
		}
	}
	for _, stage := range []int{1, 10, 30} {
		formatted := false
		ctx := &domainPanicContext{Context: context.Background(), panicAt: stage, formatted: &formatted}
		p, s, d := CompileDomainSnapshot(ctx, document, scope, domainTestOffered(DomainVisual), domainTestLimits)
		if p != nil || s != nil || len(d) != 1 || d[0].Code != CodeDomainSnapshotPanic || formatted {
			t.Fatalf("panic leaked/formatted: %+v calls=%d", d, ctx.calls)
		}
	}
	// Both auxiliary caller inputs are charged before allocation/validation.
	for _, oversized := range []string{"selection", "offered"} {
		scope := domainTestScope(DomainVisual)
		offered := domainTestOffered(DomainVisual)
		if oversized == "selection" {
			scope.Selection.ClipIDs = []documentv2.ID{documentv2.ID(strings.Repeat("x", 1<<20))}
		} else {
			offered.BackendID = strings.Repeat("x", 1<<20)
		}
		p, s, d := CompileDomainSnapshot(context.Background(), document, scope, offered, domainTestLimits)
		if p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) || !strings.Contains(d[0].Message, ErrCanonicalDocumentLimit.Error()) {
			t.Fatalf("unbounded %s: %+v", oversized, d)
		}
	}
}

func TestDomainSnapshotOwnershipAndConcurrentReaders(t *testing.T) {
	document, scope, offered := domainTestDocument(t), domainTestScope(DomainVisual), domainTestOffered(DomainVisual)
	program, snapshot, d := CompileDomainSnapshot(context.Background(), document, scope, offered, domainTestLimits)
	if program == nil {
		t.Fatalf("compile: %+v", d)
	}
	before, err := program.EvaluateTimeline("timeline-main", rt(3, 1))
	if err != nil {
		t.Fatal(err)
	}
	document.Timelines[0].Tracks[2].Clips[1].AssetID = "mutated"
	document.Assets[4].Text.Fill.R = 0
	scope.Selection.ClipIDs[0] = "mutated"
	offered.Features[0] = "mutated"
	ownedScope := program.Scope()
	ownedScope.Selection.ClipIDs[0] = "mutated"
	assets := snapshot.Assets()
	assets[0].ID = "mutated"
	timelines := snapshot.Timelines()
	timelines[0].Tracks[2].Clips[1].ID = "mutated"
	if program.Scope().Selection.ClipIDs[0] == "mutated" || snapshot.Assets()[0].ID == "mutated" || snapshot.Timelines()[0].Tracks[2].Clips[1].ID == "mutated" {
		t.Fatal("getter alias")
	}
	if program.Scope().RequestedRange.Start != rt(4, 2) {
		t.Fatal("requested rational representation lost")
	}
	after, err := program.EvaluateTimeline("timeline-main", rt(3, 1))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("input alias")
	}
	before.Layers[1].Clip.Properties.TextFill.R = 0
	after, _ = program.EvaluateTimeline("timeline-main", rt(3, 1))
	if after.Layers[1].Clip.Properties.TextFill.R == 0 {
		t.Fatal("evaluation alias")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				value, err := program.EvaluateTimeline("timeline-main", rt(5, 2))
				if err != nil || len(value.Layers) != 3 {
					t.Errorf("concurrent eval: %v", err)
					return
				}
				value.Layers[1].Clip.Properties.TextFill.R = 0
				s := program.Scope()
				s.Selection.ClipIDs[0] = "local"
				a := snapshot.Assets()
				a[0].ID = "local"
			}
		}()
	}
	wg.Wait()
}

func TestDomainSnapshotNativeV2SourcesAndSnapshotHeaderIsolation(t *testing.T) {
	document := domainTestDocument(t)
	text := document.Assets[4].Text
	text.Profile = documentv2.TextProfilePinnedFontViewportPXV2
	text.Text, text.Language, text.Script = "Cutly", "en", documentv2.TextScriptLatin
	text.LineHeight = &documentv2.Rational{Numerator: 60, Denominator: 1}
	text.Viewport = &documentv2.RasterViewport{WidthPixels: 640, HeightPixels: 180}
	vector := document.Assets[5].Vector
	vector.Profile = documentv2.VectorProfilePathViewportPXV2
	vector.Viewport = &documentv2.RasterViewport{WidthPixels: 320, HeightPixels: 180}
	vector.Paths[0].Stroke = nil
	// The native v2 profile supports straight segments; keep the source fixture bounded and simple.
	vector.Paths[0].Commands = []documentv2.VectorCommand{vector.Paths[0].Commands[0], vector.Paths[0].Commands[1], {Op: documentv2.VectorClose}}
	offered := domainTestOffered(DomainVisual)
	offered.Features = []videoedit.CapabilityID{documentv2.CapabilityTextPinnedFontViewportPXV2, documentv2.CapabilityVectorPathViewportPXV2}
	visual, snapshot, d := CompileDomainSnapshot(context.Background(), document, domainTestScope(DomainVisual), offered, domainTestLimits)
	if visual == nil || snapshot == nil || documentv2.HasErrorDiagnostics(d) {
		t.Fatalf("native v2: %+v", d)
	}
	audio, _ := compileDomainTest(t, document, DomainAudio)
	wantHash, _ := documentv2.DocumentHash(document)
	if visual.DocumentHash() != wantHash || audio.DocumentHash() != wantHash {
		t.Fatal("v2 mixed original identity lost")
	}
	assets := snapshot.Assets()
	if assets[4].TextSemanticHash == "" || assets[5].VectorSemanticHash == "" {
		t.Fatal("native source metadata missing")
	}
	before, err := visual.EvaluateTimeline("timeline-main", rt(3, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Neither input source pointers nor a returned snapshot header can change the opaque evaluator.
	*text = documentv2.TextSource{}
	*vector = documentv2.VectorSource{}
	*snapshot = FrozenSnapshot{}
	after, err := visual.EvaluateTimeline("timeline-main", rt(3, 1))
	if err != nil || !reflect.DeepEqual(before, after) || visual.DocumentHash() != wantHash {
		t.Fatal("source or snapshot header alias")
	}
}

func TestDomainSnapshotOtherTimelineAndEmptyDomainIsolation(t *testing.T) {
	document := domainTestDocument(t)
	other := documentv2.Timeline{ID: "other", Tracks: []documentv2.Track{{ID: "other-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "other-clip", AssetID: "asset-video-a", TimelineRange: documentv2.TimeRange{Start: rt(2, 1), Duration: rt(2, 1)}, SourceRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)}}}}}}
	document.Timelines = append(document.Timelines, other)
	visual, snapshot := compileDomainTest(t, document, DomainVisual)
	if len(visual.program.timelines) != 1 || len(snapshot.Timelines()) != 2 {
		t.Fatal("other timeline was compiled or omitted from discovery")
	}
	if _, err := visual.EvaluateTimeline("other", rt(2, 1)); err == nil {
		t.Fatal("other timeline escaped")
	}
	scope := domainTestScope(DomainVisual)
	scope.Selection.ClipIDs[0] = "other-clip"
	if p, s, d := CompileDomainSnapshot(context.Background(), document, scope, domainTestOffered(DomainVisual), domainTestLimits); p != nil || s != nil || !documentv2.HasErrorDiagnostics(d) {
		t.Fatal("cross-timeline selection accepted")
	}
	// An absent domain has an empty exact closure and authorizes no clips.
	scope.Selection = documentv2.CapabilitySelection{TimelineID: "other"}
	scope.Domain = DomainAudio
	p, s, d := CompileDomainSnapshot(context.Background(), document, scope, domainTestOffered(DomainAudio), domainTestLimits)
	if p == nil || s == nil || documentv2.HasErrorDiagnostics(d) {
		t.Fatalf("empty exact closure: %+v", d)
	}
	if _, err := p.EvaluateClip("other", "other-clip", rt(2, 1)); err == nil {
		t.Fatal("empty closure authorized clip")
	}
}

func TestDomainSnapshotDescriptorSeparateAndOwned(t *testing.T) {
	descriptor := DomainSnapshotDescriptor()
	if descriptor.Profile != DomainSnapshotProfileV1 || descriptor.SchemaVersion != 1 || descriptor.ScopeSemantics == "" || descriptor.CapabilitySemantics == "" {
		t.Fatal("incomplete descriptor")
	}
	descriptor.Profile = "changed"
	if DomainSnapshotDescriptor().Profile != DomainSnapshotProfileV1 {
		t.Fatal("descriptor aliases")
	}
}
