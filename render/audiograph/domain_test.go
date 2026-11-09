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

func domainFixture() (documentv2.Document, evaluator.DomainScope, CompileOptions) {
	d := baseAudioDocument()
	d.Timelines[0].Tracks[0].Clips[0].ID = "clip"
	d.Timelines[0].Tracks[0].Clips[0].TimelineRange.Duration = rt(1, 1)
	d.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(1, 1)
	s := evaluator.DomainScope{Domain: evaluator.DomainAudio, Selection: documentv2.CapabilitySelection{TimelineID: "timeline", ClipIDs: []documentv2.ID{"clip"}}, RequestedRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)}}
	return d, s, CompileOptions{Offered: videoedit.BackendCapabilities{BackendID: "fixed-audio", TrackKinds: []videoedit.TrackKind{"audio"}, AssetKinds: []videoedit.AssetKind{"audio"}, Features: []videoedit.CapabilityID{documentv2.CapabilityAudioGainV1, documentv2.CapabilityAudioPanV1, documentv2.CapabilityAudioChannelMatrixV1}}, Bindings: []MediaBinding{bindingFor(d.Assets[0], "mono", []string{"FC"})}}
}
func domainCompiled(t *testing.T) *DomainProgram {
	t.Helper()
	d, s, o := domainFixture()
	p, ds := CompileDomain(context.Background(), d, s, o)
	if p == nil || documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("compile: %+v", ds)
	}
	return p
}

func TestDomainFailureAndPreflightParity(t *testing.T) {
	tests := []struct {
		name   string
		change func(*documentv2.Document, *evaluator.DomainScope, *CompileOptions)
	}{
		{"visual", func(_ *documentv2.Document, s *evaluator.DomainScope, _ *CompileOptions) {
			s.Domain = evaluator.DomainVisual
		}},
		{"scope", func(_ *documentv2.Document, s *evaluator.DomainScope, _ *CompileOptions) { s.Selection.ClipIDs = nil }},
		{"capability", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) { o.Offered.TrackKinds = nil }},
		{"invalid original", func(d *documentv2.Document, _ *evaluator.DomainScope, _ *CompileOptions) { d.Assets[0].ID = "" }},
		{"limit", func(_ *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Limits.MaxDocumentNodes = 1
		}},
		{"scope admission", func(_ *documentv2.Document, s *evaluator.DomainScope, o *CompileOptions) {
			s.Selection.ClipIDs = make([]documentv2.ID, 1000)
			o.Limits.MaxDocumentNodes = 500
		}},
		{"speed", func(d *documentv2.Document, _ *evaluator.DomainScope, _ *CompileOptions) {
			d.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = rt(2, 1)
		}},
		{"buses", func(d *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			d.AudioBuses = []documentv2.AudioBus{{ID: "master", Kind: documentv2.AudioBusMaster, Properties: documentv2.AudioBusProperties{GainLinear: rat("1/1"), Pan: rat("0/1")}}}
			d.Timelines[0].Tracks[0].AudioBusID = "master"
			o.Offered.Features = append(o.Offered.Features, documentv2.CapabilityAudioRouteV1)
		}},
		{"extension", func(d *documentv2.Document, _ *evaluator.DomainScope, o *CompileOptions) {
			o.Offered.Features = append(o.Offered.Features, "test.no.v1")
			d.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{Namespace: "test.no.v1", AffectsRendering: true, RequiredCapability: "test.no.v1", Value: json.RawMessage(`{}`)}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, s, o := domainFixture()
			test.change(&d, &s, &o)
			p, ds := CompileDomain(context.Background(), d, s, o)
			if p != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("accepted %+v", ds)
			}
			r, ds := PreflightDomain(context.Background(), d, s, o.Offered, o.Limits)
			if !reflect.DeepEqual(r, DomainPreflightReceipt{}) || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("preflight accepted %+v", ds)
			}
		})
	}
	d, s, o := domainFixture()
	r, ds := PreflightDomain(context.Background(), d, s, o.Offered, o.Limits)
	if documentv2.HasErrorDiagnostics(ds) || r.DocumentHash == "" || r.Limits != DefaultLimits() {
		t.Fatalf("preflight %+v %+v", r, ds)
	}
	o.Bindings = nil
	p, ds := CompileDomain(context.Background(), d, s, o)
	if p != nil || !hasDiagnostic(ds, CodeBindingMissing) {
		t.Fatalf("binding required: %+v", ds)
	}
	d, s, o = domainFixture()
	o.Bindings = append(o.Bindings, o.Bindings[0])
	p, ds = CompileDomain(context.Background(), d, s, o)
	if p != nil || !hasDiagnostic(ds, CodeBindingDuplicate) {
		t.Fatalf("duplicate accepted: %+v", ds)
	}
}

func TestDomainGuardsBeforeCallbacks(t *testing.T) {
	p := domainCompiled(t)
	ctx := context.Background()
	calls := 0
	provider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		calls++
		return sourceResponse(r, []Exact{One()}), nil
	})
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 4}
	for _, at := range []documentv2.RationalTime{rt(-1, 1), rt(2, 1), rt(5, 2), rt(1, 0)} {
		v, e := p.EvaluateAt(ctx, "timeline", at, provider)
		if e == nil || !reflect.DeepEqual(v, Evaluation{}) {
			t.Fatalf("at accepted %+v %v", v, e)
		}
	}
	if _, e := p.EvaluateAt(ctx, "hidden", rt(0, 1), provider); e == nil {
		t.Fatal("wrong timeline")
	}
	for _, index := range []int64{-1, 8, 9, math.MaxInt64} {
		v, e := p.EvaluateSample(ctx, "timeline", clock, index, provider)
		if e == nil || !reflect.DeepEqual(v, SampleEvaluation{}) {
			t.Fatalf("sample %d accepted", index)
		}
	}
	for _, pair := range [][2]int64{{-1, 0}, {0, -1}, {math.MaxInt64, 1}, {8, 0}, {8, 1}, {7, 2}, {9, 0}, {0, 9}} {
		v, e := p.EvaluateBlock(ctx, "timeline", clock, pair[0], pair[1], provider)
		if e == nil || !reflect.DeepEqual(v, BlockEvaluation{}) {
			t.Fatalf("block %+v accepted", pair)
		}
	}
	// Last sample at 7/4 is in scope, but exclusive endpoint 9/4 exceeds 2.
	uneven := videoedit.SampleClock{Origin: rt(1, 4), SamplesPerSecond: 2}
	if _, e := p.EvaluateBlock(ctx, "timeline", uneven, 0, 4, provider); e == nil {
		t.Fatal("partial end cell accepted")
	}
	overflow := videoedit.SampleClock{Origin: rt(math.MaxInt64, 1), SamplesPerSecond: 1}
	if _, e := p.EvaluateBlock(ctx, "timeline", overflow, 1, 0, provider); e == nil {
		t.Fatal("time overflow")
	}
	if calls != 0 {
		t.Fatalf("guard called provider %d times", calls)
	}
	v, e := p.EvaluateBlock(ctx, "timeline", clock, 0, 8, provider)
	if e != nil || len(v.Samples) != 8 || calls != 4 {
		t.Fatalf("exact end: %+v %v calls %d", v, e, calls)
	}
	calls = 0
	v, e = p.EvaluateBlock(ctx, "timeline", clock, 0, 0, provider)
	if e != nil || calls != 0 || len(v.Samples) != 0 {
		t.Fatalf("zero block %v", e)
	}
	// Entirely silent requested scope still has authority checks, not evaluator-on-active guards.
	d, s, o := domainFixture()
	s.RequestedRange = documentv2.TimeRange{Start: rt(3, 1), Duration: rt(1, 1)}
	s.Selection.ClipIDs = nil
	o.Bindings = nil
	silent, ds := CompileDomain(ctx, d, s, o)
	if silent == nil {
		t.Fatalf("silent compile %+v", ds)
	}
	if _, e = silent.EvaluateAt(ctx, "timeline", rt(2, 1), provider); e == nil {
		t.Fatal("silent outside")
	}
	if _, e = silent.EvaluateBlock(ctx, "timeline", clock, 12, 5, provider); e == nil {
		t.Fatal("silent spanning outside")
	}
	if _, e = silent.EvaluateAt(ctx, "timeline", rt(3, 1), provider); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("silent callbacks")
	}
	var zero DomainProgram
	if v, e := zero.EvaluateAt(ctx, "timeline", rt(0, 1), provider); e == nil || !reflect.DeepEqual(v, Evaluation{}) {
		t.Fatal("zero authority")
	}
}

type domainStepContext struct {
	context.Context
	checks int
	failAt int
	panics bool
}

func (c *domainStepContext) Err() error {
	c.checks++
	if c.failAt > 0 && c.checks >= c.failAt {
		if c.panics {
			panic(domainPanicValue{})
		}
		return context.Canceled
	}
	return nil
}

type domainPanicValue struct{}

func (domainPanicValue) String() string { panic("panic value formatted") }
func (domainPanicValue) Error() string  { panic("panic value formatted") }
func TestDomainCancellationAndPanicEveryCheckpoint(t *testing.T) {
	d, s, o := domainFixture()
	for _, ctx := range []context.Context{nil, &domainStepContext{Context: context.Background(), failAt: 1}, &domainStepContext{Context: context.Background(), failAt: 1, panics: true}} {
		if p, ds := CompileDomain(ctx, d, s, o); p != nil || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatal("context accepted")
		}
		if r, ds := PreflightDomain(ctx, d, s, o.Offered, o.Limits); !reflect.DeepEqual(r, DomainPreflightReceipt{}) || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatal("preflight context accepted")
		}
	}
	baseline := &domainStepContext{Context: context.Background()}
	if p, ds := CompileDomain(baseline, d, s, o); p == nil {
		t.Fatalf("baseline %+v", ds)
	}
	for _, panics := range []bool{false, true} {
		for checkpoint := 1; checkpoint <= baseline.checks; checkpoint++ {
			ctx := &domainStepContext{Context: context.Background(), failAt: checkpoint, panics: panics}
			if p, ds := CompileDomain(ctx, d, s, o); p != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("compile checkpoint %d/%d panic %v accepted", checkpoint, baseline.checks, panics)
			}
		}
	}
	preBaseline := &domainStepContext{Context: context.Background()}
	if _, ds := PreflightDomain(preBaseline, d, s, o.Offered, o.Limits); documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("preflight baseline %+v", ds)
	}
	for _, panics := range []bool{false, true} {
		for checkpoint := 1; checkpoint <= preBaseline.checks; checkpoint++ {
			ctx := &domainStepContext{Context: context.Background(), failAt: checkpoint, panics: panics}
			r, ds := PreflightDomain(ctx, d, s, o.Offered, o.Limits)
			if !reflect.DeepEqual(r, DomainPreflightReceipt{}) || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("preflight checkpoint %d panic %v accepted", checkpoint, panics)
			}
		}
	}
	p := domainCompiled(t)
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 4}
	provider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		return sourceResponse(r, []Exact{One()}), nil
	})
	for _, kind := range []string{"at", "sample", "block", "zero", "gap"} {
		run := func(ctx context.Context) (bool, error) {
			switch kind {
			case "at":
				v, e := p.EvaluateAt(ctx, "timeline", rt(0, 1), provider)
				return reflect.DeepEqual(v, Evaluation{}), e
			case "sample":
				v, e := p.EvaluateSample(ctx, "timeline", clock, 0, provider)
				return reflect.DeepEqual(v, SampleEvaluation{}), e
			case "block":
				v, e := p.EvaluateBlock(ctx, "timeline", clock, 0, 4, provider)
				return reflect.DeepEqual(v, BlockEvaluation{}), e
			case "zero":
				v, e := p.EvaluateBlock(ctx, "timeline", clock, 0, 0, provider)
				return reflect.DeepEqual(v, BlockEvaluation{}), e
			default:
				v, e := p.EvaluateAt(ctx, "timeline", rt(3, 2), provider)
				return reflect.DeepEqual(v, Evaluation{}), e
			}
		}
		ctx := &domainStepContext{Context: context.Background()}
		if _, e := run(ctx); e != nil {
			t.Fatal(e)
		}
		for _, panics := range []bool{false, true} {
			for i := 1; i <= ctx.checks; i++ {
				zero, e := run(&domainStepContext{Context: context.Background(), failAt: i, panics: panics})
				if e == nil || !zero {
					t.Fatalf("%s checkpoint %d panic %v", kind, i, panics)
				}
			}
		}
	}
	hostile := SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) { panic(domainPanicValue{}) })
	if v, e := p.EvaluateAt(context.Background(), "timeline", rt(0, 1), hostile); e == nil || !reflect.DeepEqual(v, Evaluation{}) {
		t.Fatal("provider panic")
	}
	// Real context cancellation from a callback returns zero, not a partial sample/block.
	ctx, cancel := context.WithCancel(context.Background())
	cancelProvider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		cancel()
		return sourceResponse(r, []Exact{One()}), nil
	})
	if v, e := p.EvaluateBlock(ctx, "timeline", clock, 0, 4, cancelProvider); e == nil || !reflect.DeepEqual(v, BlockEvaluation{}) {
		t.Fatal("callback cancellation")
	}
}

func TestDomainOwnsStateAndConcurrentReaders(t *testing.T) {
	d, s, o := domainFixture()
	p, ds := CompileDomain(context.Background(), d, s, o)
	if p == nil {
		t.Fatalf("compile %+v", ds)
	}
	want := p.Receipt()
	wantHash := p.DocumentHash()
	d.Timelines[0].Tracks[0].Clips[0].AssetID = "bad"
	d.Assets[0].ContentHash = "bad"
	s.Selection.ClipIDs[0] = "bad"
	o.Bindings[0].DecoderChannels[0] = "BAD"
	o.Offered.TrackKinds[0] = "video"
	r := p.Receipt()
	r.Scope.Selection.ClipIDs[0] = "bad"
	r.Offered.TrackKinds[0] = "video"
	r.Sources[0].DecoderChannels[0] = "BAD"
	ss := p.Scope()
	ss.Selection.ClipIDs[0] = "bad"
	if !reflect.DeepEqual(p.Receipt(), want) || p.DocumentHash() != wantHash {
		t.Fatal("aliased state")
	}
	unhashed := want
	unhashed.ReceiptHash = ""
	got, err := domainHash(unhashed)
	if err != nil || got != want.ReceiptHash {
		t.Fatal("receipt self exclusion")
	}
	raw, _ := videoedit.CanonicalJSON(unhashed)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["receipt_hash"]; ok {
		t.Fatal("self hash member not omitted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				r := p.Receipt()
				r.Sources[0].DecoderChannels[0] = "BAD"
				s := p.Scope()
				s.Selection.ClipIDs[0] = "BAD"
				_, e := p.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
					return sourceResponse(r, []Exact{One()}), nil
				}))
				if e != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
}

func TestDomainReusesLegacyGainPanMatrixExactly(t *testing.T) {
	d, s, o := domainFixture()
	d.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		AudioGain:          &documentv2.AudioGainProperty{Linear: rat("2/1")},
		AudioPan:           &documentv2.AudioPanProperty{Value: rat("1/2")},
		AudioChannelMatrix: &documentv2.AudioChannelMatrixProperty{SourceLayout: "5.1", SourceChannels: []string{"FL", "FR", "FC", "LFE", "SL", "SR"}, LeftCoefficients: rationals("1/1", "0/1", "1/2", "0/1", "1/4", "0/1"), RightCoefficients: rationals("0/1", "1/1", "1/2", "0/1", "0/1", "1/4")},
	}
	o.Bindings = []MediaBinding{bindingFor(d.Assets[0], "5.1", []string{"FL", "FR", "FC", "LFE", "SL", "SR"})}
	domain, ds := CompileDomain(context.Background(), d, s, o)
	if domain == nil {
		t.Fatalf("domain %+v", ds)
	}
	legacy, ds := Compile(d, o)
	if legacy == nil {
		t.Fatalf("legacy %+v", ds)
	}
	provider := SourceProviderFunc(func(_ context.Context, r SourceRequest) (SourceSample, error) {
		return sourceResponse(r, exacts(t, "1/1", "2/1", "3/1", "4/1", "5/1", "6/1")), nil
	})
	a, e := domain.EvaluateAt(context.Background(), "timeline", rt(1, 2), provider)
	if e != nil {
		t.Fatal(e)
	}
	b, e := legacy.EvaluateAt(context.Background(), "timeline", rt(1, 2), provider)
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("legacy numeric/receipt parity differs", e)
	}
	assertStereo(t, a.Output, "15/4", "10/1")
}

func TestDomainCanonicalFixtures(t *testing.T) {
	p := domainCompiled(t)
	for _, test := range []struct {
		path  string
		value any
	}{
		{"testdata/domain-compile-v1.profile.canonical.json", DomainDescriptor()},
		{"testdata/domain-compile-v1.receipt.canonical.json", p.Receipt()},
	} {
		canonical, err := videoedit.CanonicalJSON(test.value)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.TrimSpace(raw), canonical) {
			t.Fatalf("domain fixture %s differs", test.path)
		}
	}
}
