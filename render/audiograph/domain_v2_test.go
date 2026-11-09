package audiograph

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func domainV2Fixture() (documentv2.Document, evaluator.DomainScope, CompileOptions) {
	d, s, o := domainFixture()
	d.AudioBuses = []documentv2.AudioBus{
		{ID: "unused", Kind: documentv2.AudioBusGroup, ParentBusID: "master", Properties: documentv2.AudioBusProperties{GainLinear: rat("9/1"), Pan: rat("0/1")}},
		{ID: "child", Kind: documentv2.AudioBusGroup, ParentBusID: "parent", Properties: documentv2.AudioBusProperties{GainLinear: rat("2/1"), Pan: rat("1/2")}},
		{ID: "master", Kind: documentv2.AudioBusMaster, Properties: documentv2.AudioBusProperties{GainLinear: rat("1/2"), Pan: rat("0/1"), SamplePeakLimiter: &documentv2.AudioSamplePeakLimiterProperty{CeilingLinear: rat("3/4")}}},
		{ID: "parent", Kind: documentv2.AudioBusGroup, ParentBusID: "master", Properties: documentv2.AudioBusProperties{GainLinear: rat("2/1"), Pan: rat("0/1")}},
	}
	tr := &d.Timelines[0].Tracks[0]
	tr.AudioBusID = "child"
	clip := &tr.Clips[0]
	clip.TimelineRange.Start = rt(1, 1)
	clip.TimelineRange.Duration = rt(2, 1)
	clip.SourceRange.Duration = rt(2, 1)
	clip.PropertySet = &documentv2.PropertySet{AudioGain: &documentv2.AudioGainProperty{Linear: rat("2/1")}, AudioChannelMatrix: &documentv2.AudioChannelMatrixProperty{SourceLayout: "stereo", SourceChannels: []string{"FL", "FR"}, LeftCoefficients: rationals("1/1", "1/2"), RightCoefficients: rationals("0/1", "1/1")}}
	clip.KeyframeLanes = []documentv2.KeyframeLane{
		{ID: "pan", Target: documentv2.KeyframeTargetAudioPan, ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(rat("-1/1"))}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(rat("1/1"))}},
		}},
		{ID: "gain", Target: documentv2.KeyframeTargetAudioGain, ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(rat("1/1"))}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(rat("2/1"))}},
		}},
	}
	s.RequestedRange = documentv2.TimeRange{Start: rt(1, 2), Duration: rt(3, 1)}
	o.Offered.Features = append(o.Offered.Features, documentv2.CapabilityAudioRouteV1, documentv2.CapabilityAudioSamplePeakLimiterV1, documentv2.CapabilityKeyframesV1, documentv2.CapabilityInterpolationLinearV1)
	o.Bindings = []MediaBinding{bindingFor(d.Assets[0], "stereo", []string{"FL", "FR"})}
	return d, s, o
}

func compileDomainV2Test(t *testing.T, d documentv2.Document, s evaluator.DomainScope, o CompileOptions) *DomainProgram {
	t.Helper()
	p, ds := CompileDomainV2(context.Background(), d, s, o)
	if p == nil || documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("compile V2: %+v", ds)
	}
	return p
}
func domainV2Provider() SourceProvider {
	return SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		return sourceResponse(r, []Exact{One(), One()}), nil
	})
}

func TestDomainV2RoutingAutomationMatrixLimiterAndArbitraryRangeParity(t *testing.T) {
	d, s, o := domainV2Fixture()
	p := compileDomainV2Test(t, d, s, o)
	provider := domainV2Provider()
	at, err := p.EvaluateAt(context.Background(), "timeline", rt(2, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	// The existing stereo-linked limiter preserves the 3:4 channel ratio.
	assertStereo(t, at.Output, "9/16", "3/4")
	if len(at.Buses) != 3 || at.Buses[0].BusID != "child" || at.Buses[0].BusIndex != 1 || at.Buses[1].BusIndex != 2 || at.Buses[2].BusIndex != 3 || !equalIDs(at.Buses[1].ChildBuses, []documentv2.ID{"parent"}) || !equalIDs(at.Buses[2].ChildBuses, []documentv2.ID{"child"}) || !equalIDs(at.Buses[0].DirectTracks, []documentv2.ID{"track-audio"}) {
		t.Fatalf("routing/index evidence: %+v", at.Buses)
	}
	if at.Profile != ProfileV1 || at.SampleDomain != SampleDomainProfileV1 {
		t.Fatal("DSP profiles changed")
	}
	// Full and independently compiled arbitrary range share original local automation time.
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 4}
	whole, err := p.EvaluateBlock(context.Background(), "timeline", clock, 2, 12, provider)
	if err != nil {
		t.Fatal(err)
	}
	var concatenated []StereoSample
	for _, r := range [][2]int64{{2, 3}, {5, 1}, {6, 5}, {11, 3}} {
		sub := s
		sub.RequestedRange = documentv2.TimeRange{Start: rt(r[0], 4), Duration: rt(r[1], 4)}
		sub.Selection.ClipIDs = nil
		if r[0] < 12 && r[0]+r[1] > 4 {
			sub.Selection.ClipIDs = []documentv2.ID{"clip"}
		}
		options := o
		if len(sub.Selection.ClipIDs) == 0 {
			options.Bindings = nil
		}
		scoped := compileDomainV2Test(t, d, sub, options)
		block, e := scoped.EvaluateBlock(context.Background(), "timeline", clock, r[0], r[1], provider)
		if e != nil {
			t.Fatal(e)
		}
		concatenated = append(concatenated, block.Samples...)
	}
	if !reflect.DeepEqual(whole.Samples, concatenated) {
		t.Fatal("arbitrary-range vs concatenation differs")
	}
	// Random and reverse seeks are stateless, including silent edges.
	for i := int64(13); i >= 2; i-- {
		single, e := p.EvaluateSample(context.Background(), "timeline", clock, i, provider)
		if e != nil || !reflect.DeepEqual(single.Evaluation.Output, whole.Samples[i-2]) {
			t.Fatalf("seek %d: %v", i, e)
		}
	}
	if old, ds := CompileDomain(context.Background(), d, s, o); old != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("V1 accepted buses")
	}
	if _, ds := PreflightDomain(context.Background(), d, s, o.Offered, o.Limits); !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("V1 preflight accepted buses")
	}
}

func TestDomainV2ReceiptProfileIdentityAndOwnership(t *testing.T) {
	d, s, o := domainV2Fixture()
	p := compileDomainV2Test(t, d, s, o)
	want := p.Receipt()
	hash, _ := documentv2.DocumentHash(d)
	if want.SchemaVersion != 2 || want.Profile != DomainCompileProfileV2 || want.DocumentHash != hash || !reflect.DeepEqual(want.Offered, o.Offered) || len(want.Sources) != 1 {
		t.Fatalf("receipt %+v", want)
	}
	for _, pair := range []struct {
		value any
		hash  string
	}{{DomainDescriptorV2(), want.DomainDescriptorHash}, {evaluator.DomainSnapshotDescriptorV2(), want.EvaluatorDescriptorHash}, {Descriptor(), want.DSPDescriptorHash}} {
		got, e := domainHash(pair.value)
		if e != nil || got != pair.hash {
			t.Fatal("descriptor identity")
		}
	}
	unsigned := want
	unsigned.ReceiptHash = ""
	got, e := domainHash(unsigned)
	if e != nil || got != want.ReceiptHash {
		t.Fatal("receipt hash")
	}
	encoded, _ := videoedit.CanonicalJSON(unsigned)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	if _, ok := fields["receipt_hash"]; ok {
		t.Fatal("self hash present")
	}
	pre, ds := PreflightDomainV2(context.Background(), d, s, o.Offered, o.Limits)
	if documentv2.HasErrorDiagnostics(ds) || pre.DocumentHash != want.DocumentHash || !reflect.DeepEqual(pre.Scope, want.Scope) || pre.Limits != want.Limits {
		t.Fatalf("preflight %+v", ds)
	}
	d.AudioBuses[1].Properties.GainLinear = rat("99/1")
	d.Assets[0].ContentHash = "bad"
	s.Selection.ClipIDs[0] = "bad"
	o.Offered.Features[0] = "bad"
	o.Bindings[0].DecoderChannels[0] = "BAD"
	r := p.Receipt()
	r.Sources[0].DecoderChannels[0] = "BAD"
	r.Offered.Features[0] = "bad"
	r.Scope.Selection.ClipIDs[0] = "bad"
	scope := p.Scope()
	scope.Selection.ClipIDs[0] = "bad"
	if !reflect.DeepEqual(p.Receipt(), want) {
		t.Fatal("aliased receipt")
	}
	baseline, err := p.EvaluateAt(context.Background(), "timeline", rt(2, 1), domainV2Provider())
	if err != nil {
		t.Fatal(err)
	}
	baseline.Buses[0].DirectTracks[0] = "bad"
	baseline.ActiveClips[0].Source.Request.DecoderChannels[0] = "BAD"
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				v, e := p.EvaluateAt(context.Background(), "timeline", rt(2, 1), domainV2Provider())
				if e != nil || v.Buses[0].DirectTracks[0] != "track-audio" || v.ActiveClips[0].Source.Request.DecoderChannels[0] != "FL" || !reflect.DeepEqual(p.Receipt(), want) {
					t.Error("concurrent owned state", e)
				}
			}
		}()
	}
	wg.Wait()
}

func TestDomainV2AdmissionFailuresAndSelectedBindingOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*documentv2.Document, *evaluator.DomainScope, *CompileOptions)
	}{
		{"route capability", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Offered.Features = removeCapability(o.Offered.Features, documentv2.CapabilityAudioRouteV1)
		}},
		{"limiter capability", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Offered.Features = removeCapability(o.Offered.Features, documentv2.CapabilityAudioSamplePeakLimiterV1)
		}},
		{"automation capability", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Offered.Features = removeCapability(o.Offered.Features, documentv2.CapabilityKeyframesV1)
		}},
		{"matrix capability", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Offered.Features = removeCapability(o.Offered.Features, documentv2.CapabilityAudioChannelMatrixV1)
		}},
		{"bus budget", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) { o.Limits.MaxBuses = 1 }},
		{"full graph budget", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Limits.MaxDocumentNodes = 1
		}},
		{"scope budget", func(_ *documentv2.Document, s *evaluator.DomainScope, o *CompileOptions) {
			s.Selection.ClipIDs = make([]documentv2.ID, 1000)
			o.Limits.MaxDocumentNodes = 500
		}},
		{"byte budget", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Limits.MaxCanonicalDocumentBytes = 1
		}},
		{"nonunit", func(d *documentv2.Document, _ *evaluator.DomainScope, _ *CompileOptions) {
			d.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(3, 1)
		}},
		{"wrong domain", func(_ *documentv2.Document, s *evaluator.DomainScope, _ *CompileOptions) {
			s.Domain = evaluator.DomainVisual
		}},
		{"incomplete selection", func(_ *documentv2.Document, s *evaluator.DomainScope, _ *CompileOptions) { s.Selection.ClipIDs = nil }},
		{"selected extension", func(d *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			d.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions = []documentv2.ExtensionProperty{{Namespace: "test.extension.v1", AffectsRendering: true, RequiredCapability: "test.extension.v1", Value: json.RawMessage(`{}`)}}
			o.Offered.Features = append(o.Offered.Features, "test.extension.v1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, s, o := domainV2Fixture()
			tc.change(&d, &s, &o)
			if p, ds := CompileDomainV2(context.Background(), d, s, o); p != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("compile accepted %+v", ds)
			}
			if r, ds := PreflightDomainV2(context.Background(), d, s, o.Offered, o.Limits); !reflect.DeepEqual(r, DomainPreflightReceipt{}) || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("preflight accepted %+v", ds)
			}
		})
	}
	d, s, o := domainV2Fixture()
	hidden := d.Assets[0]
	hidden.ID = "hidden"
	d.Assets = append(d.Assets, hidden)
	tr := audioTrack("hidden-track", "hidden-clip", "hidden", "unused")
	tr.Clips[0].TimelineRange.Start = rt(5, 1)
	tr.Clips[0].PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{Namespace: "test.hidden.v1", AffectsRendering: true, RequiredCapability: "test.hidden.v1", Value: json.RawMessage(`{}`)}}}
	d.Timelines[0].Tracks = append(d.Timelines[0].Tracks, tr)
	p := compileDomainV2Test(t, d, s, o)
	if len(p.Receipt().Sources) != 1 {
		t.Fatal("unselected binding")
	}
	o.Bindings = append(o.Bindings, bindingFor(hidden, "mono", []string{"FC"}))
	if p, ds := CompileDomainV2(context.Background(), d, s, o); p != nil || !hasDiagnostic(ds, CodeDomainInvalidV2) {
		t.Fatalf("extra binding accepted %+v", ds)
	}
	o.Bindings = nil
	if p, ds := CompileDomainV2(context.Background(), d, s, o); p != nil || !hasDiagnostic(ds, CodeBindingMissing) {
		t.Fatalf("missing binding accepted %+v", ds)
	}
}

func TestDomainV2GuardsBudgetsCancellationAndPanic(t *testing.T) {
	d, s, o := domainV2Fixture()
	o.Limits.MaxBlockSamples = 2
	p := compileDomainV2Test(t, d, s, o)
	calls := 0
	provider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		calls++
		return sourceResponse(r, []Exact{One(), One()}), nil
	})
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 4}
	for _, pair := range [][2]int64{{-1, 1}, {2, -1}, {math.MaxInt64, 1}, {14, 0}, {13, 2}, {1, 1}, {2, 3}} {
		if v, e := p.EvaluateBlock(context.Background(), "timeline", clock, pair[0], pair[1], provider); e == nil || !reflect.DeepEqual(v, BlockEvaluation{}) {
			t.Fatalf("guard accepted %+v", pair)
		}
	}
	for _, at := range []documentv2.RationalTime{rt(0, 1), rt(7, 2), rt(1, 0)} {
		if v, e := p.EvaluateAt(context.Background(), "timeline", at, provider); e == nil || !reflect.DeepEqual(v, Evaluation{}) {
			t.Fatal("time guard")
		}
	}
	if _, e := p.EvaluateAt(context.Background(), "wrong", rt(2, 1), provider); e == nil {
		t.Fatal("timeline guard")
	}
	if calls != 0 {
		t.Fatal("IO before guard")
	}
	// Every compile/preflight context checkpoint fails closed, including bus closure loops.
	for _, preflight := range []bool{false, true} {
		run := func(ctx context.Context) bool {
			if preflight {
				r, ds := PreflightDomainV2(ctx, d, s, o.Offered, o.Limits)
				return reflect.DeepEqual(r, DomainPreflightReceipt{}) && documentv2.HasErrorDiagnostics(ds)
			}
			p, ds := CompileDomainV2(ctx, d, s, o)
			return p == nil && documentv2.HasErrorDiagnostics(ds)
		}
		if !run(nil) {
			t.Fatal("nil context")
		}
		baseline := &domainStepContext{Context: context.Background()}
		if run(baseline) {
			t.Fatal("baseline")
		}
		for _, panics := range []bool{false, true} {
			for checkpoint := 1; checkpoint <= baseline.checks; checkpoint++ {
				if !run(&domainStepContext{Context: context.Background(), failAt: checkpoint, panics: panics}) {
					t.Fatalf("preflight %v checkpoint %d panic %v", preflight, checkpoint, panics)
				}
			}
		}
	}
	for _, kind := range []string{"at", "block", "zero", "gap"} {
		run := func(ctx context.Context) bool {
			if kind == "at" || kind == "gap" {
				at := rt(2, 1)
				if kind == "gap" {
					at = rt(1, 2)
				}
				v, e := p.EvaluateAt(ctx, "timeline", at, provider)
				return e != nil && reflect.DeepEqual(v, Evaluation{})
			}
			count := int64(2)
			if kind == "zero" {
				count = 0
			}
			v, e := p.EvaluateBlock(ctx, "timeline", clock, 4, count, provider)
			return e != nil && reflect.DeepEqual(v, BlockEvaluation{})
		}
		baseline := &domainStepContext{Context: context.Background()}
		if run(baseline) {
			t.Fatal("runtime baseline")
		}
		for _, panics := range []bool{false, true} {
			for i := 1; i <= baseline.checks; i++ {
				if !run(&domainStepContext{Context: context.Background(), failAt: i, panics: panics}) {
					t.Fatalf("%s checkpoint %d panic %v", kind, i, panics)
				}
			}
		}
	}
	hostile := SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) { panic(domainPanicValue{}) })
	if v, e := p.EvaluateAt(context.Background(), "timeline", rt(2, 1), hostile); e == nil || !reflect.DeepEqual(v, Evaluation{}) {
		t.Fatal("provider panic")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelProvider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		cancel()
		return sourceResponse(r, []Exact{One(), One()}), nil
	})
	if v, e := p.EvaluateBlock(ctx, "timeline", clock, 4, 2, cancelProvider); e == nil || !reflect.DeepEqual(v, BlockEvaluation{}) {
		t.Fatal("partial canceled block")
	}
}

func TestDomainV2MixedVisualTransitionsFullIdentityAndNoVisualIO(t *testing.T) {
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the authored visual dissolve, vector/text/font assets and visual property lanes.
	// Only audio DSP is configured for this fixed audio-only backend.
	tr := &d.Timelines[0].Tracks[1]
	tr.Clips[0].PropertySet = nil
	tr.Clips[0].KeyframeLanes = nil
	tr.Clips[0].TimelineRange = documentv2.TimeRange{Start: rt(2, 1), Duration: rt(1, 1)}
	tr.Clips[0].SourceRange = documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)}
	before := tr.Clips[0]
	before.ID = "outside"
	before.TimelineRange.Start = rt(0, 1)
	tr.Clips = append([]documentv2.Clip{before}, tr.Clips...)
	tracks := d.Timelines[0].Tracks
	d.Timelines[0].Tracks = []documentv2.Track{tracks[0], tracks[4], tracks[1], tracks[2], tracks[3]}
	d.Timelines = append([]documentv2.Timeline{{ID: "other"}}, d.Timelines...)
	scope := evaluator.DomainScope{Domain: evaluator.DomainAudio, Selection: documentv2.CapabilitySelection{TimelineID: "timeline-main", ClipIDs: []documentv2.ID{"clip-audio"}}, RequestedRange: documentv2.TimeRange{Start: rt(2, 1), Duration: rt(1, 1)}}
	options := CompileOptions{Offered: videoedit.BackendCapabilities{BackendID: "fixed-audio-v2", TrackKinds: []videoedit.TrackKind{"audio"}, AssetKinds: []videoedit.AssetKind{"audio"}, Features: []videoedit.CapabilityID{documentv2.CapabilityAudioRouteV1, documentv2.CapabilityAudioGainV1, documentv2.CapabilityAudioPanV1}}, Bindings: []MediaBinding{bindingFor(d.Assets[2], "mono", []string{"FC"})}}
	p := compileDomainV2Test(t, d, scope, options)
	wantHash, _ := documentv2.DocumentHash(d)
	calls := 0
	provider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		calls++
		if r.AssetID != "asset-audio" || r.AssetIndex != 2 || r.TimelineIndex != 1 || r.TrackIndex != 2 || r.ClipIndex != 1 || r.DocumentHash != wantHash {
			t.Errorf("original identity/indexes %+v", r)
		}
		return sourceResponse(r, []Exact{One()}), nil
	})
	v, e := p.EvaluateAt(context.Background(), "timeline-main", rt(5, 2), provider)
	if e != nil || calls != 1 || p.DocumentHash() != wantHash {
		t.Fatalf("mixed %+v %v", v, e)
	}
	assertStereo(t, v.Output, "3/4", "3/4")
	if old, ds := CompileDomain(context.Background(), d, scope, options); old != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("V1 accepted visual coexistence")
	}
	if old, ds := Compile(d, options); old != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("legacy whole-document capabilities relaxed")
	}
	scope.Selection.TransitionIDs = []documentv2.ID{"transition-main"}
	if p, ds := CompileDomainV2(context.Background(), d, scope, options); p != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("audio transition IDs authorized")
	}
	scope.Selection.TransitionIDs = nil
	d.Assets[0].URI = "different-visual-input.mov"
	changed := compileDomainV2Test(t, d, scope, options)
	if changed.DocumentHash() == wantHash || changed.Receipt().ReceiptHash == p.Receipt().ReceiptHash {
		t.Fatal("visual original pruned from identity")
	}
	d.Assets[0].ID = ""
	if p, ds := CompileDomainV2(context.Background(), d, scope, options); p != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("invalid visual graph skipped validation")
	}
}

func TestDomainV2SilentSelectionDoesNotExecuteUnofferedBuses(t *testing.T) {
	d, s, o := domainV2Fixture()
	s.RequestedRange = documentv2.TimeRange{Start: rt(4, 1), Duration: rt(1, 1)}
	s.Selection.ClipIDs = nil
	o.Bindings = nil
	o.Offered = videoedit.BackendCapabilities{BackendID: "silence"}
	p := compileDomainV2Test(t, d, s, o)
	calls := 0
	provider := SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) { calls++; panic("unexpected callback") })
	v, e := p.EvaluateAt(context.Background(), "timeline", rt(4, 1), provider)
	if e != nil || calls != 0 || len(v.Buses) != 0 || len(p.Receipt().Sources) != 0 {
		t.Fatalf("silent %+v %v", v, e)
	}
	assertStereo(t, v.Output, "0/1", "0/1")
}

func TestDomainV2RejectsAudioTransitionsAndBindingBudgets(t *testing.T) {
	d, s, o := domainV2Fixture()
	incoming := d.Timelines[0].Tracks[0].Clips[0]
	incoming.ID = "incoming"
	incoming.TimelineRange.Start = rt(3, 1)
	d.Timelines[0].Tracks[0].Clips = append(d.Timelines[0].Tracks[0].Clips, incoming)
	for _, duration := range []documentv2.RationalTime{rt(0, 1), rt(1, 2)} {
		d.Timelines[0].Transitions = []documentv2.Transition{{ID: "audio-transition", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "clip", IncomingClipID: "incoming", PreDuration: duration, PostDuration: duration}}
		s.Selection.ClipIDs = []documentv2.ID{"clip", "incoming"}
		s.Selection.TransitionIDs = nil
		for _, selected := range []bool{false, true} {
			if selected {
				s.Selection.TransitionIDs = []documentv2.ID{"audio-transition"}
			}
			if p, ds := CompileDomainV2(context.Background(), d, s, o); p != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatal("audio transition accepted")
			}
			if r, ds := PreflightDomainV2(context.Background(), d, s, o.Offered, o.Limits); !reflect.DeepEqual(r, DomainPreflightReceipt{}) || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatal("audio transition preflight accepted")
			}
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*CompileOptions)
	}{
		{"binding count", func(o *CompileOptions) { o.Limits.MaxBindings = 1; o.Bindings = append(o.Bindings, o.Bindings[0]) }},
		{"binding byte budget", func(o *CompileOptions) { o.Limits.MaxCanonicalManifestBytes = 1 }},
		{"duplicate", func(o *CompileOptions) { o.Bindings = append(o.Bindings, o.Bindings[0]) }},
		{"resource identity", func(o *CompileOptions) { o.Bindings[0].Manifest.Resource.ContentHash = digest('b') }},
		{"matrix channel order", func(o *CompileOptions) { o.Bindings[0].DecoderChannels = []string{"FR", "FL"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, s, o := domainV2Fixture()
			tc.change(&o)
			if p, ds := CompileDomainV2(context.Background(), d, s, o); p != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("binding admitted %+v", ds)
			}
		})
	}
}

func TestDomainV2ImplicitMasterReusesV1NumbersWithoutChangingReceipts(t *testing.T) {
	d, s, o := domainFixture()
	v1, ds := CompileDomain(context.Background(), d, s, o)
	if v1 == nil {
		t.Fatal(ds)
	}
	v2 := compileDomainV2Test(t, d, s, o)
	provider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		return sourceResponse(r, []Exact{One()}), nil
	})
	for _, at := range []documentv2.RationalTime{rt(0, 1), rt(1, 2), rt(3, 2)} {
		a, e := v1.EvaluateAt(context.Background(), "timeline", at, provider)
		if e != nil {
			t.Fatal(e)
		}
		b, e := v2.EvaluateAt(context.Background(), "timeline", at, provider)
		if e != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("V1 DSP/evaluation receipt parity", e)
		}
	}
	if v1.Receipt().Profile != DomainCompileProfileV1 || v1.Receipt().SchemaVersion != 1 || v2.Receipt().ReceiptHash == v1.Receipt().ReceiptHash {
		t.Fatal("versioned admission receipts")
	}
}

func TestDomainV2CanonicalFixtures(t *testing.T) {
	d, s, o := domainV2Fixture()
	p := compileDomainV2Test(t, d, s, o)
	for _, tc := range []struct {
		path  string
		value any
	}{{"testdata/domain-compile-v2.profile.canonical.json", DomainDescriptorV2()}, {"testdata/domain-compile-v2.receipt.canonical.json", p.Receipt()}} {
		canonical, e := videoedit.CanonicalJSON(tc.value)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := os.ReadFile(tc.path)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(bytes.TrimSpace(raw), canonical) {
			t.Fatalf("fixture differs %s", tc.path)
		}
	}
}
