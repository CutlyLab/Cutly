package audiograph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

func TestLegacyMonoAndStereoMixUsesExactSourceMapping(t *testing.T) {
	document := baseAudioDocument()
	secondAsset := documentv2.Asset{
		ID: "asset-stereo", Kind: documentv2.AssetKindAudio, URI: "stereo.wav", ContentHash: digest('b'),
		Duration: timePointer(rt(20, 1)),
	}
	document.Assets = append(document.Assets, secondAsset)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange = documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = documentv2.TimeRange{Start: rt(10, 1), Duration: rt(4, 1)}
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{
		ID: "track-stereo", Kind: documentv2.TrackKindAudio,
		Clips: []documentv2.Clip{{
			ID: "clip-stereo", AssetID: secondAsset.ID,
			TimelineRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)},
			SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(2, 1)},
		}},
	})
	bindings := []MediaBinding{
		bindingFor(document.Assets[0], "mono", []string{"FC"}),
		bindingFor(secondAsset, "stereo", []string{"FL", "FR"}),
	}
	program := compileForTest(t, document, bindings)

	provider := SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		switch request.ClipID {
		case "clip-audio":
			if request.SourceTime != rt(12, 1) || request.ChannelLayout != "mono" ||
				request.ResourceContentHash != document.Assets[0].ContentHash || request.ResourceSizeBytes != 1024 ||
				!equalStrings(request.DecoderChannels, []string{"FC"}) {
				t.Fatalf("mono request = %+v", request)
			}
			return sourceResponse(request, []Exact{mustExact(t, "1/2")}), nil
		case "clip-stereo":
			if request.SourceTime != rt(1, 1) || request.ChannelLayout != "stereo" || request.AssetIndex != 1 ||
				request.TrackIndex != 1 || request.ClipIndex != 0 || request.TimelineIndex != 0 {
				t.Fatalf("stereo request = %+v", request)
			}
			return sourceResponse(request, []Exact{mustExact(t, "1/4"), mustExact(t, "-1/4")}), nil
		default:
			return SourceSample{}, fmt.Errorf("unexpected clip %s", request.ClipID)
		}
	})
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(2, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertStereo(t, evaluation.Output, "3/4", "1/4")
	if len(evaluation.ActiveClips) != 2 || evaluation.ActiveClips[0].TrackIndex != 0 || evaluation.ActiveClips[1].TrackIndex != 1 {
		t.Fatalf("active order = %+v", evaluation.ActiveClips)
	}

	// The end is half-open: no source read and exact silence at t=3.
	var calls atomic.Int64
	silence, err := program.EvaluateAt(context.Background(), "timeline", rt(3, 1), SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) {
		calls.Add(1)
		return SourceSample{}, errors.New("must not be called")
	}))
	if err != nil || calls.Load() != 0 {
		t.Fatalf("half-open end = %+v, err=%v calls=%d", silence, err, calls.Load())
	}
	assertStereo(t, silence.Output, "0/1", "0/1")
}

func TestExplicitSixChannelMatrixHasNoImplicitClipping(t *testing.T) {
	document := baseAudioDocument()
	clip := &document.Timelines[0].Tracks[0].Clips[0]
	clip.PropertySet = &documentv2.PropertySet{AudioChannelMatrix: &documentv2.AudioChannelMatrixProperty{
		SourceLayout: "5.1", SourceChannels: []string{"FL", "FR", "FC", "LFE", "SL", "SR"},
		LeftCoefficients:  rationals("1/1", "0/1", "1/2", "0/1", "1/4", "0/1"),
		RightCoefficients: rationals("0/1", "1/1", "1/2", "0/1", "0/1", "1/4"),
	}}
	binding := bindingFor(document.Assets[0], "5.1", []string{"FL", "FR", "FC", "LFE", "SL", "SR"})
	program := compileForTest(t, document, []MediaBinding{binding})
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		return sourceResponse(request, exacts(t, "1/1", "2/1", "3/1", "4/1", "5/1", "6/1")), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertStereo(t, evaluation.Output, "15/4", "5/1")
	if evaluation.Output.Left.Sign() <= 0 || evaluation.Output.Right.Sign() <= 0 {
		t.Fatal("matrix result was unexpectedly clipped")
	}
}

func TestCompileRejectsMissingOrUnprovenBindings(t *testing.T) {
	document := baseAudioDocument()
	valid := bindingFor(document.Assets[0], "mono", []string{"FC"})
	tests := []struct {
		name     string
		mutate   func(*documentv2.Document, *MediaBinding)
		bindings func(MediaBinding) []MediaBinding
		code     string
	}{
		{name: "missing", bindings: func(MediaBinding) []MediaBinding { return nil }, code: CodeBindingMissing},
		{name: "hash mismatch", mutate: func(_ *documentv2.Document, binding *MediaBinding) {
			binding.Manifest.Resource.ContentHash = digest('f')
		}, code: CodeResourceMismatch},
		{name: "decoder order", mutate: func(_ *documentv2.Document, binding *MediaBinding) {
			binding.Manifest.Streams[0].Audio.Channels = 2
			binding.Manifest.Streams[0].Audio.ChannelLayout = "stereo"
			binding.DecoderChannels = []string{"FR", "FL"}
		}, code: CodeChannelLayoutInvalid},
		{name: "surround without matrix", mutate: func(_ *documentv2.Document, binding *MediaBinding) {
			binding.Manifest.Streams[0].Audio.Channels = 6
			binding.Manifest.Streams[0].Audio.ChannelLayout = "5.1"
			binding.DecoderChannels = []string{"FL", "FR", "FC", "LFE", "SL", "SR"}
		}, code: CodeChannelLayoutInvalid},
		{name: "matrix authored order mismatch", mutate: func(document *documentv2.Document, binding *MediaBinding) {
			document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{AudioChannelMatrix: &documentv2.AudioChannelMatrixProperty{
				SourceLayout: "stereo", SourceChannels: []string{"FL", "FR"},
				LeftCoefficients: rationals("1/1", "0/1"), RightCoefficients: rationals("0/1", "1/1"),
			}}
			binding.Manifest.Streams[0].Audio.Channels = 2
			binding.Manifest.Streams[0].Audio.ChannelLayout = "stereo"
			binding.DecoderChannels = []string{"FR", "FL"}
		}, code: CodeChannelOrderInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := document
			candidate.Assets = append([]documentv2.Asset(nil), document.Assets...)
			candidate.Timelines = cloneTimelines(document.Timelines)
			binding := valid
			binding.DecoderChannels = append([]string(nil), valid.DecoderChannels...)
			binding.Manifest.Streams = append([]probe.Stream(nil), valid.Manifest.Streams...)
			audioCopy := *valid.Manifest.Streams[0].Audio
			binding.Manifest.Streams[0].Audio = &audioCopy
			if test.mutate != nil {
				test.mutate(&candidate, &binding)
			}
			bindings := []MediaBinding{binding}
			if test.bindings != nil {
				bindings = test.bindings(binding)
			}
			program, diagnostics := Compile(candidate, CompileOptions{Offered: offeredFor(t, candidate), Bindings: bindings})
			if program != nil || !hasDiagnostic(diagnostics, test.code) {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}
}

func TestCompileRejectsDuplicateBindingsAndPreflightsLimitsFirst(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	invalidFirst := binding
	invalidFirst.Manifest.SchemaVersion = 999
	if program, diagnostics := Compile(document, CompileOptions{
		Offered: offeredFor(t, document), Bindings: []MediaBinding{invalidFirst, binding},
	}); program != nil || !hasDiagnostic(diagnostics, CodeBindingDuplicate) {
		t.Fatalf("duplicate binding = program=%v diagnostics=%+v", program, diagnostics)
	}

	second := audioTrack("track-second", "clip-second", document.Assets[0].ID, "")
	document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, second)
	limits := DefaultLimits()
	limits.MaxAudioTracksPerTimeline = 1
	var providerRelevantOffered videoedit.BackendCapabilities // deliberately missing every capability
	program, diagnostics := Compile(document, CompileOptions{
		Offered: providerRelevantOffered, Bindings: []MediaBinding{binding}, Limits: limits,
	})
	if program != nil || !hasDiagnostic(diagnostics, CodeCompileLimit) || hasDiagnostic(diagnostics, documentv2.CodeCapabilityTrack) {
		t.Fatalf("raw limit must fail before capability/probe work: program=%v diagnostics=%+v", program, diagnostics)
	}

	streamLimitedDocument := baseAudioDocument()
	streamLimitedBinding := bindingFor(streamLimitedDocument.Assets[0], "mono", []string{"FC"})
	streamLimitedBinding.Manifest.Streams = append(streamLimitedBinding.Manifest.Streams, probe.Stream{})
	streamLimits := DefaultLimits()
	streamLimits.MaxProbeStreamsPerBinding = 1
	program, diagnostics = Compile(streamLimitedDocument, CompileOptions{
		Offered: providerRelevantOffered, Bindings: []MediaBinding{streamLimitedBinding}, Limits: streamLimits,
	})
	if program != nil || !hasDiagnostic(diagnostics, CodeCompileLimit) || hasDiagnostic(diagnostics, CodeProbeInvalid) {
		t.Fatalf("probe stream limit must fail before manifest validation: program=%v diagnostics=%+v", program, diagnostics)
	}
}

func TestCompilePreflightsCompleteDocumentAndManifestResources(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	tests := []struct {
		name   string
		mutate func(*documentv2.Document, *MediaBinding, *Limits)
	}{
		{name: "document nodes", mutate: func(_ *documentv2.Document, _ *MediaBinding, limits *Limits) {
			limits.MaxDocumentNodes = 1
		}},
		{name: "document bytes", mutate: func(document *documentv2.Document, _ *MediaBinding, limits *Limits) {
			document.Metadata = map[string]json.RawMessage{"large": json.RawMessage(`"` + strings.Repeat("x", 4096) + `"`)}
			limits.MaxCanonicalDocumentBytes = 1024
		}},
		{name: "all track kinds", mutate: func(document *documentv2.Document, _ *MediaBinding, limits *Limits) {
			document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{ID: "annotation", Kind: documentv2.TrackKindAnnotation})
			limits.MaxTracksPerTimeline = 1
		}},
		{name: "non audio clips", mutate: func(document *documentv2.Document, _ *MediaBinding, limits *Limits) {
			document.Timelines[0].Tracks = append(document.Timelines[0].Tracks, documentv2.Track{
				ID: "annotation", Kind: documentv2.TrackKindAnnotation,
				Clips: []documentv2.Clip{{ID: "a"}, {ID: "b"}},
			})
			limits.MaxClipsPerTrack = 1
		}},
		{name: "manifest bytes", mutate: func(_ *documentv2.Document, binding *MediaBinding, limits *Limits) {
			binding.Manifest.Probe.Implementation = strings.Repeat("p", 4096)
			limits.MaxCanonicalManifestBytes = 1024
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := document
			candidate.Timelines = cloneTimelines(document.Timelines)
			candidateBinding := binding
			limits := DefaultLimits()
			test.mutate(&candidate, &candidateBinding, &limits)
			program, diagnostics := Compile(candidate, CompileOptions{
				Offered: videoedit.BackendCapabilities{}, Bindings: []MediaBinding{candidateBinding}, Limits: limits,
			})
			if program != nil || !hasDiagnostic(diagnostics, CodeCompileLimit) || hasDiagnostic(diagnostics, documentv2.CodeCapabilityTrack) {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}

	for name, mutate := range map[string]func(*Limits){
		"bindings hard max":       func(value *Limits) { value.MaxBindings = HardMaxBindings + 1 },
		"streams hard max":        func(value *Limits) { value.MaxProbeStreamsPerBinding = HardMaxProbeStreamsPerBinding + 1 },
		"timelines hard max":      func(value *Limits) { value.MaxTimelines = HardMaxTimelines + 1 },
		"audio tracks hard max":   func(value *Limits) { value.MaxAudioTracksPerTimeline = HardMaxAudioTracksPerTimeline + 1 },
		"clips hard max":          func(value *Limits) { value.MaxClipsPerTrack = HardMaxClipsPerTrack + 1 },
		"buses hard max":          func(value *Limits) { value.MaxBuses = HardMaxBuses + 1 },
		"block hard max":          func(value *Limits) { value.MaxBlockSamples = HardMaxBlockSamples + 1 },
		"exact hard max":          func(value *Limits) { value.MaxExactBits = HardMaxGraphExactBits + 1 },
		"capabilities hard max":   func(value *Limits) { value.MaxCapabilityEntries = HardMaxCapabilityEntries + 1 },
		"nodes hard max":          func(value *Limits) { value.MaxDocumentNodes = HardMaxDocumentNodes + 1 },
		"tracks hard max":         func(value *Limits) { value.MaxTracksPerTimeline = HardMaxTracksPerTimeline + 1 },
		"document bytes hard max": func(value *Limits) { value.MaxCanonicalDocumentBytes = HardMaxCanonicalDocumentBytes + 1 },
		"manifest bytes hard max": func(value *Limits) { value.MaxCanonicalManifestBytes = HardMaxCanonicalManifestBytes + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			limits := DefaultLimits()
			mutate(&limits)
			if program, diagnostics := Compile(document, CompileOptions{Offered: offeredFor(t, document), Bindings: []MediaBinding{binding}, Limits: limits}); program != nil || !hasDiagnostic(diagnostics, CodeCompileLimit) {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}
	capabilityLimits := DefaultLimits()
	capabilityLimits.MaxCapabilityEntries = 1
	offered := offeredFor(t, document)
	offered.Features = append(offered.Features, "vendor.extra-a.v1", "vendor.extra-b.v1")
	if program, diagnostics := Compile(document, CompileOptions{Offered: offered, Bindings: []MediaBinding{binding}, Limits: capabilityLimits}); program != nil || !hasDiagnostic(diagnostics, CodeCompileLimit) {
		t.Fatalf("capability limit program=%v diagnostics=%+v", program, diagnostics)
	}
}

func TestAudioRenderingExtensionFailsClosed(t *testing.T) {
	document := baseAudioDocument()
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		Extensions: []documentv2.ExtensionProperty{{
			Namespace: "vendor.audio-filter.v1", AffectsRendering: true,
			RequiredCapability: "vendor.audio-filter.v1", Value: json.RawMessage(`{"amount":1}`),
		}},
	}
	offered := offeredFor(t, document)
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	if program, diagnostics := Compile(document, CompileOptions{Offered: offered, Bindings: []MediaBinding{binding}}); program != nil || !hasDiagnostic(diagnostics, CodeExtensionUnsupported) {
		t.Fatalf("extension compile = program=%v diagnostics=%+v", program, diagnostics)
	}
}

func TestCompileRequiresLimiterCapabilityAndRuntimeConsumesLimiter(t *testing.T) {
	document := baseAudioDocument()
	document.AudioBuses = []documentv2.AudioBus{{
		ID: "master", Kind: documentv2.AudioBusMaster,
		Properties: documentv2.AudioBusProperties{
			GainLinear: rat("1/1"), Pan: rat("0/1"),
			SamplePeakLimiter: &documentv2.AudioSamplePeakLimiterProperty{CeilingLinear: rat("1/2")},
		},
	}}
	document.Timelines[0].Tracks[0].AudioBusID = "master"
	binding := bindingFor(document.Assets[0], "stereo", []string{"FL", "FR"})

	offered := offeredFor(t, document)
	withoutLimiter := offered
	withoutLimiter.Features = removeCapability(withoutLimiter.Features, documentv2.CapabilityAudioSamplePeakLimiterV1)
	if program, diagnostics := Compile(document, CompileOptions{Offered: withoutLimiter, Bindings: []MediaBinding{binding}}); program != nil || !hasDiagnostic(diagnostics, documentv2.CodeCapabilityFeature) {
		t.Fatalf("missing limiter capability = program=%v diagnostics=%+v", program, diagnostics)
	}

	// The generic evaluator accepts and freezes clip semantics but exposes no bus output. The
	// AudioGraph compiler independently freezes and executes the limiter rather than dropping it.
	if generic, diagnostics := evaluator.Compile(document, offered); generic == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("generic compile = %v, %+v", generic, diagnostics)
	}
	program := compileWithOffered(t, document, []MediaBinding{binding}, offered)
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		return sourceResponse(request, exacts(t, "2/1", "-1/1")), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertStereo(t, evaluation.Output, "1/2", "-1/4")
	if len(evaluation.Buses) != 1 {
		t.Fatalf("bus evidence = %+v", evaluation.Buses)
	}
	assertStereo(t, evaluation.Buses[0].Input, "2/1", "-1/1")
}

func TestBusDAGUsesTrackThenDocumentChildOrderAndProperties(t *testing.T) {
	document := baseAudioDocument()
	assets := []documentv2.Asset{
		document.Assets[0],
		{ID: "asset-b", Kind: documentv2.AssetKindAudio, URI: "b.wav", ContentHash: digest('b'), Duration: timePointer(rt(10, 1))},
		{ID: "asset-c", Kind: documentv2.AssetKindAudio, URI: "c.wav", ContentHash: digest('c'), Duration: timePointer(rt(10, 1))},
	}
	document.Assets = assets
	document.AudioBuses = []documentv2.AudioBus{
		{ID: "master", Kind: documentv2.AudioBusMaster, Properties: documentv2.AudioBusProperties{GainLinear: rat("1/2"), Pan: rat("0/1")}},
		{ID: "group-a", Kind: documentv2.AudioBusGroup, ParentBusID: "master", Properties: documentv2.AudioBusProperties{GainLinear: rat("2/1"), Pan: rat("0/1")}},
		{ID: "group-b", Kind: documentv2.AudioBusGroup, ParentBusID: "master", Properties: documentv2.AudioBusProperties{GainLinear: rat("1/1"), Pan: rat("1/2")}},
	}
	tracks := []documentv2.Track{
		audioTrack("track-master", "clip-master", assets[0].ID, "master"),
		audioTrack("track-a", "clip-a", assets[1].ID, "group-a"),
		audioTrack("track-b", "clip-b", assets[2].ID, "group-b"),
	}
	document.Timelines[0].Tracks = tracks
	bindings := []MediaBinding{
		bindingFor(assets[0], "stereo", []string{"FL", "FR"}),
		bindingFor(assets[1], "stereo", []string{"FL", "FR"}),
		bindingFor(assets[2], "stereo", []string{"FL", "FR"}),
	}
	program := compileForTest(t, document, bindings)
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		switch request.ClipID {
		case "clip-master":
			return sourceResponse(request, exacts(t, "1/1", "1/1")), nil
		case "clip-a":
			return sourceResponse(request, exacts(t, "1/2", "1/2")), nil
		case "clip-b":
			return sourceResponse(request, exacts(t, "2/1", "2/1")), nil
		default:
			return SourceSample{}, errors.New("unexpected clip")
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	// group-a => (1,1); group-b pan +1/2 => (1,2); master input => (3,4), gain 1/2.
	assertStereo(t, evaluation.Output, "3/2", "2/1")
	master := evaluation.Buses[0]
	if !equalIDs(master.DirectTracks, []documentv2.ID{"track-master"}) || !equalIDs(master.ChildBuses, []documentv2.ID{"group-a", "group-b"}) {
		t.Fatalf("master stable order = %+v", master)
	}
	if !equalIDs(evaluation.Buses[1].DirectTracks, []documentv2.ID{"track-a"}) || !equalIDs(evaluation.Buses[2].DirectTracks, []documentv2.ID{"track-b"}) {
		t.Fatalf("group routes = %+v", evaluation.Buses)
	}
}

func TestGainPanAutomationBlockParitySeekAndConcurrency(t *testing.T) {
	document := baseAudioDocument()
	clip := &document.Timelines[0].Tracks[0].Clips[0]
	clip.TimelineRange.Duration = rt(2, 1)
	clip.SourceRange.Duration = rt(2, 1)
	clip.PropertySet = &documentv2.PropertySet{AudioGain: &documentv2.AudioGainProperty{Linear: rat("2/1")}}
	clip.KeyframeLanes = []documentv2.KeyframeLane{{
		ID: "pan-lane", Target: documentv2.KeyframeTargetAudioPan, ValueType: documentv2.KeyframeValueScalar,
		Keyframes: []documentv2.Keyframe{
			{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(rat("-1/1"))}, Interpolation: documentv2.InterpolationLinear},
			{Offset: rt(2, 1), Value: documentv2.KeyframeValue{Scalar: rationalPointer(rat("1/1"))}},
		},
	}}
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	program := compileForTest(t, document, []MediaBinding{binding})
	quarter := mustExact(t, "1/4")
	provider := SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		return sourceResponse(request, []Exact{quarter}), nil
	})
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 2}
	block, err := program.EvaluateBlock(context.Background(), "timeline", clock, 0, 4, provider)
	if err != nil {
		t.Fatal(err)
	}
	if block.Profile != ProfileV1 || block.SampleDomain != SampleDomainProfileV1 ||
		block.SourceContractVersion != SourceContractVersion || block.SourceProfile != SourceProfileV1 ||
		block.DocumentHash != program.DocumentHash() || block.TimelineID != "timeline" || block.SampleClock != clock {
		t.Fatalf("incomplete block binding: %+v", block)
	}
	want := [][2]string{{"1/2", "0/1"}, {"1/2", "1/4"}, {"1/2", "1/2"}, {"1/4", "1/2"}}
	for index := range block.Samples {
		assertStereo(t, block.Samples[index], want[index][0], want[index][1])
		single, err := program.EvaluateSample(context.Background(), "timeline", clock, int64(index), provider)
		if err != nil || !single.Evaluation.Output.Left.Equal(block.Samples[index].Left) || !single.Evaluation.Output.Right.Equal(block.Samples[index].Right) {
			t.Fatalf("block/single mismatch at %d: %+v %v", index, single, err)
		}
	}

	baseline, _ := json.Marshal(block)
	const workers = 24
	results := make(chan []byte, workers)
	errorsChannel := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		go func(reverse bool) {
			if reverse {
				for index := int64(3); index >= 0; index-- {
					if _, err := program.EvaluateSample(context.Background(), "timeline", clock, index, provider); err != nil {
						errorsChannel <- err
						return
					}
				}
			}
			actual, err := program.EvaluateBlock(context.Background(), "timeline", clock, 0, 4, provider)
			if err != nil {
				errorsChannel <- err
				return
			}
			raw, err := json.Marshal(actual)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- raw
		}(worker%2 == 0)
	}
	for worker := 0; worker < workers; worker++ {
		select {
		case err := <-errorsChannel:
			t.Fatal(err)
		case actual := <-results:
			if !bytes.Equal(actual, baseline) {
				t.Fatalf("concurrent output diverged:\n%s\n%s", baseline, actual)
			}
		}
	}
}

func TestSourceFailuresAndLimitsReturnNoPartialBlock(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	limits := DefaultLimits()
	limits.MaxBlockSamples = 2
	program, diagnostics := Compile(document, CompileOptions{Offered: offeredFor(t, document), Bindings: []MediaBinding{binding}, Limits: limits})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile = %v %+v", program, diagnostics)
	}
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 48_000}
	var precheckCalls atomic.Int64
	if block, err := program.EvaluateBlock(context.Background(), "timeline", clock, 0, 3, SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		precheckCalls.Add(1)
		return sourceResponse(request, []Exact{One()}), nil
	})); block.Samples != nil || !errors.Is(err, ErrLimitExceeded) || precheckCalls.Load() != 0 {
		t.Fatalf("limit block=%+v err=%v provider_calls=%d", block, err, precheckCalls.Load())
	}

	var calls atomic.Int64
	block, err := program.EvaluateBlock(context.Background(), "timeline", clock, 0, 2, SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		if calls.Add(1) == 2 {
			return SourceSample{}, errors.New("decoder stopped")
		}
		return sourceResponse(request, []Exact{One()}), nil
	}))
	if block.Samples != nil || !errors.Is(err, ErrSourceProvider) {
		t.Fatalf("partial failure block=%+v err=%v", block, err)
	}
	if _, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		return sourceResponse(request, []Exact{{}}), nil
	})); !errors.Is(err, ErrSourceSampleInvalid) {
		t.Fatalf("invalid exact sample error = %v", err)
	}
	huge, err := NewExact(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 5000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		return sourceResponse(request, []Exact{huge}), nil
	})); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("huge exact sample error = %v", err)
	}
}

func TestCancellationIsStableAndNeverReturnsPartialSamples(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	program := compileForTest(t, document, []MediaBinding{binding})
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 48_000}

	preCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int64
	block, err := program.EvaluateBlock(preCanceled, "timeline", clock, 0, 2, SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		calls.Add(1)
		return sourceResponse(request, []Exact{One()}), nil
	}))
	assertCanceled(t, err)
	if block.Samples != nil || calls.Load() != 0 {
		t.Fatalf("pre-cancel block=%+v calls=%d", block, calls.Load())
	}

	midContext, cancelMid := context.WithCancel(context.Background())
	calls.Store(0)
	block, err = program.EvaluateBlock(midContext, "timeline", clock, 0, 3, SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		if calls.Add(1) == 2 {
			cancelMid()
		}
		return sourceResponse(request, []Exact{One()}), nil
	}))
	assertCanceled(t, err)
	if block.Samples != nil || calls.Load() != 2 {
		t.Fatalf("mid-cancel block=%+v calls=%d", block, calls.Load())
	}

	_, err = program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) {
		return SourceSample{}, context.Canceled
	}))
	assertCanceled(t, err)

	var nilFunction SourceProviderFunc
	if response, directErr := nilFunction.Sample(context.Background(), SourceRequest{}); directErr == nil || response.Channels != nil {
		t.Fatalf("direct typed-nil response=%+v err=%v", response, directErr)
	}
	if _, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), nilFunction); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("typed-nil provider error = %v", err)
	}
	if _, err := program.EvaluateAt(nil, "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		return sourceResponse(request, []Exact{One()}), nil
	})); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("nil context error = %v", err)
	}
}

func TestZeroBlockStillValidatesProgramTimelineAndProvider(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	program := compileForTest(t, document, []MediaBinding{binding})
	clock := videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 48_000}
	provider := SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) {
		t.Fatal("zero block called provider")
		return SourceSample{}, nil
	})
	block, err := program.EvaluateBlock(context.Background(), "timeline", clock, 0, 0, provider)
	if err != nil || block.CompositionSampleRange != (videoedit.TickRange{First: 0, Count: 0}) || block.Samples != nil {
		t.Fatalf("zero block = %+v, %v", block, err)
	}
	if _, err := program.EvaluateBlock(context.Background(), "missing", clock, 0, 0, provider); !errors.Is(err, ErrProgramLookup) {
		t.Fatalf("missing timeline zero block error = %v", err)
	}
	var nilProvider SourceProviderFunc
	if _, err := program.EvaluateBlock(context.Background(), "timeline", clock, 0, 0, nilProvider); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("nil provider zero block error = %v", err)
	}
	var nilProgram *Program
	if _, err := nilProgram.EvaluateSample(context.Background(), "timeline", clock, 0, provider); !errors.Is(err, ErrProgramLookup) {
		t.Fatalf("nil program sample error = %v", err)
	}
	if samples, err := allocateStereoSamples(-1); samples != nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("allocation panic conversion samples=%v err=%v", samples, err)
	}
}

func TestCompileFreezesDocumentAndBinding(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "stereo", []string{"FL", "FR"})
	program := compileForTest(t, document, []MediaBinding{binding})
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{AudioGain: &documentv2.AudioGainProperty{Linear: rat("0/1")}}
	binding.DecoderChannels[0] = "MUTATED"
	binding.Manifest.Streams[0].Audio.ChannelLayout = "mutated"
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		if request.ChannelLayout != "stereo" || !equalStrings(request.DecoderChannels, []string{"FL", "FR"}) {
			t.Fatalf("binding was not frozen: %+v", request)
		}
		response := sourceResponse(request, exacts(t, "1/1", "1/1"))
		request.DecoderChannels[0] = "CALLER_MUTATION"
		return response, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertStereo(t, evaluation.Output, "1/1", "1/1")
	if len(evaluation.ActiveClips) != 1 ||
		!equalStrings(evaluation.ActiveClips[0].Source.Request.DecoderChannels, []string{"FL", "FR"}) {
		t.Fatalf("receipt was rewritten through provider argument alias: %+v", evaluation.ActiveClips)
	}
}

func TestSourceRequestAndReceiptBindCompleteFrozenScopeAndProfiles(t *testing.T) {
	document := baseAudioDocument()
	binding := bindingFor(document.Assets[0], "mono", []string{"FC"})
	binding.DecodeProfile = "vendor.decode.strict-pcm.v7"
	binding.SelectionProfile = "vendor.select.linear-point.v2"
	binding.RetimeProfile = "vendor.retime.varispeed.v3"
	program := compileForTest(t, document, []MediaBinding{binding})
	var providerChannels []Exact
	var providerRequest SourceRequest
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(3, 2), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		providerRequest = cloneSourceRequest(request)
		providerChannels = []Exact{mustExact(t, "3/8")}
		return sourceResponse(request, providerChannels), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := providerRequest
	if request.ContractVersion != SourceContractVersion || request.DocumentHash != program.DocumentHash() ||
		request.TimelineID != "timeline" || request.TimelineIndex != 0 || request.TrackID != "track-audio" || request.TrackIndex != 0 ||
		request.ClipID != "clip-audio" || request.ClipIndex != 0 || request.AssetID != "asset-audio" || request.AssetIndex != 0 ||
		request.AssetKind != documentv2.AssetKindAudio || request.TimelineTime != rt(3, 2) || request.LocalTime != rt(3, 2) ||
		request.SourceTime != rt(3, 2) || request.SourceProfile != SourceProfileV1 || request.SampleDomain != SourceSampleDomainProfileV1 ||
		request.DecodeProfile != binding.DecodeProfile || request.SelectionProfile != binding.SelectionProfile || request.RetimeProfile != binding.RetimeProfile ||
		request.ManifestFingerprint == "" || request.ResourceContentHash != document.Assets[0].ContentHash || request.ResourceSizeBytes != 1024 ||
		request.StreamIndex != 0 || request.SampleRate != 48_000 || request.ChannelLayout != "mono" || !equalStrings(request.DecoderChannels, []string{"FC"}) {
		t.Fatalf("incomplete request: %+v", request)
	}
	if evaluation.Profile != ProfileV1 || evaluation.SampleDomain != SampleDomainProfileV1 ||
		evaluation.SourceContractVersion != SourceContractVersion || evaluation.SourceProfile != SourceProfileV1 ||
		evaluation.DocumentHash != program.DocumentHash() || len(evaluation.ActiveClips) != 1 {
		t.Fatalf("incomplete evaluation binding: %+v", evaluation)
	}
	receipt := evaluation.ActiveClips[0].Source
	if receipt.ContractVersion != SourceContractVersion || receipt.SampleDomain != SourceSampleDomainProfileV1 ||
		!equalSourceRequest(receipt.Request, request) || len(receipt.Channels) != 1 || receipt.Channels[0].String() != "3/8" {
		t.Fatalf("receipt = %+v", receipt)
	}
	// Returned provider storage and provider-call request storage must not alias retained evidence.
	providerChannels[0] = Zero()
	providerRequest.DecoderChannels[0] = "MUTATED"
	if receipt.Channels[0].String() != "3/8" || receipt.Request.DecoderChannels[0] != "FC" {
		t.Fatalf("receipt aliases provider storage: %+v", receipt)
	}
}

func TestDSPConsumesDefensiveReceiptNotProviderResponseAlias(t *testing.T) {
	expected := SourceRequest{
		ContractVersion: SourceContractVersion, DocumentHash: digest('a'), TimelineID: "timeline", TrackID: "track",
		ClipID: "clip", AssetID: "asset", AssetKind: documentv2.AssetKindAudio,
		TimelineTime: rt(0, 1), LocalTime: rt(0, 1), SourceTime: rt(0, 1),
		SourceProfile: SourceProfileV1, SampleDomain: SourceSampleDomainProfileV1,
		DecodeProfile: "vendor.decode.v1", SelectionProfile: "vendor.select.v1", RetimeProfile: "vendor.retime.v1",
		ManifestFingerprint: digest('b'), ResourceContentHash: digest('c'), ResourceSizeBytes: 1,
		SampleRate: 48_000, ChannelLayout: "mono", DecoderChannels: []string{"FC"},
	}
	providerChannels := []Exact{mustExact(t, "3/8")}
	response := sourceResponse(expected, providerChannels)
	receipt, err := validateSourceSample(response, expected, compiledBinding{decoderChannels: []string{"FC"}}, DefaultLimits().MaxExactBits)
	if err != nil {
		t.Fatal(err)
	}
	response.Channels[0] = mustExact(t, "7/8")
	providerChannels[0] = Zero()
	stereo, err := sourceToStereo(receipt, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	assertStereo(t, stereo, "3/8", "3/8")
	if receipt.Channels[0].String() != "3/8" {
		t.Fatalf("receipt changed through provider alias: %+v", receipt)
	}
}

func TestHostileSourceResponseMismatchesFailClosed(t *testing.T) {
	document := baseAudioDocument()
	program := compileForTest(t, document, []MediaBinding{bindingFor(document.Assets[0], "mono", []string{"FC"})})
	tests := []struct {
		name   string
		mutate func(*SourceSample)
	}{
		{name: "response contract", mutate: func(value *SourceSample) { value.ContractVersion++ }},
		{name: "request contract", mutate: func(value *SourceSample) { value.Request.ContractVersion++ }},
		{name: "sample domain", mutate: func(value *SourceSample) { value.SampleDomain = SampleDomainProfileV1 }},
		{name: "document", mutate: func(value *SourceSample) { value.Request.DocumentHash = digest('f') }},
		{name: "timeline id", mutate: func(value *SourceSample) { value.Request.TimelineID = "other" }},
		{name: "timeline index", mutate: func(value *SourceSample) { value.Request.TimelineIndex++ }},
		{name: "track index", mutate: func(value *SourceSample) { value.Request.TrackIndex++ }},
		{name: "clip index", mutate: func(value *SourceSample) { value.Request.ClipIndex++ }},
		{name: "asset index", mutate: func(value *SourceSample) { value.Request.AssetIndex++ }},
		{name: "asset kind", mutate: func(value *SourceSample) { value.Request.AssetKind = documentv2.AssetKindVideo }},
		{name: "timeline time", mutate: func(value *SourceSample) { value.Request.TimelineTime = rt(1, 1) }},
		{name: "local time", mutate: func(value *SourceSample) { value.Request.LocalTime = rt(1, 1) }},
		{name: "source time", mutate: func(value *SourceSample) { value.Request.SourceTime = rt(1, 1) }},
		{name: "source profile", mutate: func(value *SourceSample) { value.Request.SourceProfile = "vendor.source.v1" }},
		{name: "request domain", mutate: func(value *SourceSample) { value.Request.SampleDomain = SampleDomainProfileV1 }},
		{name: "decode profile", mutate: func(value *SourceSample) { value.Request.DecodeProfile = "vendor.decode.v1" }},
		{name: "selection profile", mutate: func(value *SourceSample) { value.Request.SelectionProfile = "vendor.selection.v1" }},
		{name: "retime profile", mutate: func(value *SourceSample) { value.Request.RetimeProfile = "vendor.retime.v1" }},
		{name: "manifest", mutate: func(value *SourceSample) { value.Request.ManifestFingerprint = digest('f') }},
		{name: "resource hash", mutate: func(value *SourceSample) { value.Request.ResourceContentHash = digest('f') }},
		{name: "resource size", mutate: func(value *SourceSample) { value.Request.ResourceSizeBytes++ }},
		{name: "stream", mutate: func(value *SourceSample) { value.Request.StreamIndex++ }},
		{name: "sample rate", mutate: func(value *SourceSample) { value.Request.SampleRate++ }},
		{name: "layout", mutate: func(value *SourceSample) { value.Request.ChannelLayout = "stereo" }},
		{name: "channel order", mutate: func(value *SourceSample) { value.Request.DecoderChannels[0] = "FR" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
				response := sourceResponse(request, []Exact{One()})
				test.mutate(&response)
				return response, nil
			}))
			if evaluation.DocumentHash != "" || len(evaluation.ActiveClips) != 0 || !errors.Is(err, ErrSourceMismatch) {
				t.Fatalf("evaluation=%+v err=%v", evaluation, err)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != CodeSourceMismatch {
				t.Fatalf("typed error=%#v", typed)
			}
		})
	}
}

func TestCrossProgramResponseReplayFailsOnDocumentHash(t *testing.T) {
	documentA := baseAudioDocument()
	documentB := baseAudioDocument()
	documentB.Revision = 2
	programA := compileForTest(t, documentA, []MediaBinding{bindingFor(documentA.Assets[0], "mono", []string{"FC"})})
	programB := compileForTest(t, documentB, []MediaBinding{bindingFor(documentB.Assets[0], "mono", []string{"FC"})})
	var replay SourceSample
	if _, err := programA.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		replay = sourceResponse(request, []Exact{One()})
		return replay, nil
	})); err != nil {
		t.Fatal(err)
	}
	if programA.DocumentHash() == programB.DocumentHash() {
		t.Fatal("documents unexpectedly share a hash")
	}
	if evaluation, err := programB.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(context.Context, SourceRequest) (SourceSample, error) {
		return replay, nil
	})); evaluation.DocumentHash != "" || !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("replay evaluation=%+v err=%v", evaluation, err)
	}
}

func TestBindingProfilesAreRequiredCanonicalAndFrozen(t *testing.T) {
	document := baseAudioDocument()
	valid := bindingFor(document.Assets[0], "mono", []string{"FC"})
	for name, mutate := range map[string]func(*MediaBinding){
		"decode":    func(value *MediaBinding) { value.DecodeProfile = "" },
		"selection": func(value *MediaBinding) { value.SelectionProfile = "not versioned" },
		"retime":    func(value *MediaBinding) { value.RetimeProfile = "VENDOR.v1" },
	} {
		t.Run(name, func(t *testing.T) {
			binding := valid
			mutate(&binding)
			if program, diagnostics := Compile(document, CompileOptions{Offered: offeredFor(t, document), Bindings: []MediaBinding{binding}}); program != nil || !hasDiagnostic(diagnostics, CodeSourceProfileInvalid) {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
		})
	}
	binding := valid
	program := compileForTest(t, document, []MediaBinding{binding})
	binding.DecodeProfile = "vendor.changed.v1"
	if _, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), SourceProviderFunc(func(_ context.Context, request SourceRequest) (SourceSample, error) {
		if request.DecodeProfile != valid.DecodeProfile {
			t.Fatalf("profile mutation leaked: %+v", request)
		}
		return sourceResponse(request, []Exact{Zero()}), nil
	})); err != nil {
		t.Fatal(err)
	}
}

func TestGoldenOperatorVectorsAndCanonicalFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "audio-graph-v1.vectors.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int    `json:"schema_version"`
		Profile       string `json:"profile"`
		Vectors       []struct {
			Kind              string            `json:"kind"`
			Input             json.RawMessage   `json:"input"`
			Output            map[string]string `json:"output"`
			Pan               string            `json:"pan"`
			Ceiling           string            `json:"ceiling"`
			LeftCoefficients  []string          `json:"left_coefficients"`
			RightCoefficients []string          `json:"right_coefficients"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(json.RawMessage(bytes.TrimSpace(raw)))
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		t.Fatalf("fixture is not canonical: err=%v\nwant=%s\ngot=%s", err, raw, canonical)
	}
	if fixture.SchemaVersion != 1 || fixture.Profile != ProfileV1 || len(fixture.Vectors) != 3 {
		t.Fatalf("fixture identity = %+v", fixture)
	}
	for _, vector := range fixture.Vectors {
		var actual StereoSample
		switch vector.Kind {
		case "pan":
			input := decodeStereoStrings(t, vector.Input)
			actual, err = panStereo(input, mustExact(t, vector.Pan))
		case "matrix":
			var inputs []string
			if err = json.Unmarshal(vector.Input, &inputs); err == nil {
				left, right := Zero(), Zero()
				for index := range inputs {
					var term Exact
					term, err = exactMul(mustExact(t, inputs[index]), mustExact(t, vector.LeftCoefficients[index]))
					if err == nil {
						left, err = exactAdd(left, term)
					}
					if err != nil {
						break
					}
					term, err = exactMul(mustExact(t, inputs[index]), mustExact(t, vector.RightCoefficients[index]))
					if err == nil {
						right, err = exactAdd(right, term)
					}
					if err != nil {
						break
					}
				}
				actual = StereoSample{Left: left, Right: right}
			}
		case "limiter":
			actual, err = limitStereo(decodeStereoStrings(t, vector.Input), mustExact(t, vector.Ceiling))
		default:
			t.Fatalf("unknown vector kind %q", vector.Kind)
		}
		if err != nil {
			t.Fatalf("%s: %v", vector.Kind, err)
		}
		assertStereo(t, actual, vector.Output["left"], vector.Output["right"])
	}
}

func FuzzExactReferenceOperatorsDoNotPanic(f *testing.F) {
	for _, seed := range [][4]int64{
		{0, 1, 0, 1}, {1, 2, -1, 3}, {1<<62 - 1, 1, -(1 << 62), 7},
	} {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}
	f.Fuzz(func(t *testing.T, leftNumerator, leftDenominator, rightNumerator, rightDenominator int64) {
		if leftDenominator == 0 || rightDenominator == 0 {
			return
		}
		left, err := NewRatio(leftNumerator, leftDenominator)
		if err != nil {
			return
		}
		right, err := NewRatio(rightNumerator, rightDenominator)
		if err != nil {
			return
		}
		value := StereoSample{Left: left, Right: right}
		if _, err := addStereo(value, value); err != nil {
			t.Fatal(err)
		}
		if _, err := panStereo(value, Zero()); err != nil {
			t.Fatal(err)
		}
		if _, err := limitStereo(value, One()); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzSourceResponseEchoRejectsMutations(f *testing.F) {
	f.Add(uint8(0), "other")
	f.Add(uint8(7), "vendor.changed.v9")
	expected := SourceRequest{
		ContractVersion: SourceContractVersion, DocumentHash: digest('a'),
		TimelineID: "timeline", TimelineIndex: 1, TrackID: "track", TrackIndex: 2,
		ClipID: "clip", ClipIndex: 3, AssetID: "asset", AssetIndex: 4, AssetKind: documentv2.AssetKindAudio,
		TimelineTime: rt(1, 2), LocalTime: rt(1, 3), SourceTime: rt(1, 4),
		SourceProfile: SourceProfileV1, SampleDomain: SourceSampleDomainProfileV1,
		DecodeProfile: "vendor.decode.v1", SelectionProfile: "vendor.select.v1", RetimeProfile: "vendor.retime.v1",
		ManifestFingerprint: digest('b'), ResourceContentHash: digest('c'), ResourceSizeBytes: 9,
		StreamIndex: 5, SampleRate: 48_000, ChannelLayout: "mono", DecoderChannels: []string{"FC"},
	}
	binding := compiledBinding{decoderChannels: []string{"FC"}}
	f.Fuzz(func(t *testing.T, field uint8, replacement string) {
		response := sourceResponse(expected, []Exact{One()})
		switch field % 10 {
		case 0:
			response.ContractVersion = 2
		case 1:
			response.Request.DocumentHash += replacement + "x"
		case 2:
			response.Request.TimelineID += documentv2.ID(replacement + "x")
		case 3:
			response.Request.AssetIndex++
		case 4:
			response.Request.SourceTime.Value++
		case 5:
			response.Request.DecodeProfile += replacement + "x"
		case 6:
			response.Request.ManifestFingerprint += replacement + "x"
		case 7:
			response.Request.SampleRate++
		case 8:
			response.Request.DecoderChannels[0] += replacement + "x"
		case 9:
			response.SampleDomain += replacement + "x"
		}
		if receipt, err := validateSourceSample(response, expected, binding, DefaultLimits().MaxExactBits); receipt.Channels != nil || !errors.Is(err, ErrSourceMismatch) {
			t.Fatalf("field=%d replacement=%q receipt=%+v err=%v", field, replacement, receipt, err)
		}
	})
}

func baseAudioDocument() documentv2.Document {
	asset := documentv2.Asset{
		ID: "asset-audio", Kind: documentv2.AssetKindAudio, URI: "audio.wav", ContentHash: digest('a'),
		Duration: timePointer(rt(20, 1)),
	}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{asset},
		Timelines: []documentv2.Timeline{{
			ID: "timeline",
			Tracks: []documentv2.Track{{
				ID: "track-audio", Kind: documentv2.TrackKindAudio,
				Clips: []documentv2.Clip{{
					ID: "clip-audio", AssetID: asset.ID,
					TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(10, 1)},
					SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(10, 1)},
				}},
			}},
		}},
	}
}

func audioTrack(trackID, clipID, assetID, busID documentv2.ID) documentv2.Track {
	return documentv2.Track{
		ID: trackID, Kind: documentv2.TrackKindAudio, AudioBusID: busID,
		Clips: []documentv2.Clip{{
			ID: clipID, AssetID: assetID,
			TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: rt(10, 1)},
			SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: rt(10, 1)},
		}},
	}
}

func bindingFor(asset documentv2.Asset, layout string, channels []string) MediaBinding {
	durationPTS := int64(960_000)
	return MediaBinding{
		AssetID: asset.ID, StreamIndex: 0, DecoderChannels: append([]string(nil), channels...),
		DecodeProfile: "cutly.test.decode.pcm.v1", SelectionProfile: "cutly.test.selection.exact-point.v1",
		RetimeProfile: "cutly.test.retime.linear-varispeed.v1",
		Manifest: probe.ProbeManifest{
			SchemaVersion: probe.SchemaVersion,
			Resource:      probe.ResourceIdentity{AssetID: asset.ID, ContentHash: asset.ContentHash, SizeBytes: 1024},
			Probe:         probe.ProbeIdentity{Implementation: "test", BuildFingerprint: digest('d'), OptionsFingerprint: digest('e')},
			Container:     probe.ContainerInfo{FormatName: "wav", StartTime: rt(0, 1), Seekable: true},
			Duration:      rt(20, 1),
			Streams: []probe.Stream{{
				Index: 0, Kind: probe.StreamKindAudio, Codec: "pcm_s24le", TimeBase: rt(1, 48_000), DurationPTS: &durationPTS,
				Audio: &probe.AudioStream{SampleRate: 48_000, Channels: len(channels), ChannelLayout: layout, SampleFormat: "s32", BitsPerSample: 24},
			}},
		},
	}
}

func sourceResponse(request SourceRequest, channels []Exact) SourceSample {
	return SourceSample{
		ContractVersion: SourceContractVersion, Request: cloneSourceRequest(request),
		SampleDomain: SourceSampleDomainProfileV1, Channels: cloneExactSlice(channels),
	}
}

func offeredFor(t *testing.T, document documentv2.Document) videoedit.BackendCapabilities {
	t.Helper()
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("requirements: %+v", diagnostics)
	}
	offered := videoedit.BackendCapabilities{BackendID: "audiograph-test", Features: append([]videoedit.CapabilityID(nil), required.Features...)}
	for _, kind := range required.TrackKinds {
		offered.TrackKinds = append(offered.TrackKinds, videoedit.TrackKind(kind))
	}
	for _, kind := range required.AssetKinds {
		offered.AssetKinds = append(offered.AssetKinds, videoedit.AssetKind(kind))
	}
	return offered
}

func compileForTest(t *testing.T, document documentv2.Document, bindings []MediaBinding) *Program {
	t.Helper()
	return compileWithOffered(t, document, bindings, offeredFor(t, document))
}

func compileWithOffered(t *testing.T, document documentv2.Document, bindings []MediaBinding, offered videoedit.BackendCapabilities) *Program {
	t.Helper()
	program, diagnostics := Compile(document, CompileOptions{Offered: offered, Bindings: bindings})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile: program=%v diagnostics=%+v", program, diagnostics)
	}
	return program
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}

func rat(value string) documentv2.Rational {
	exact, err := ParseExact(value)
	if err != nil || !exact.Numerator().IsInt64() || !exact.Denominator().IsInt64() {
		panic(value)
	}
	return documentv2.Rational{Numerator: exact.Numerator().Int64(), Denominator: exact.Denominator().Int64()}
}

func rationals(values ...string) []documentv2.Rational {
	result := make([]documentv2.Rational, len(values))
	for index, value := range values {
		result[index] = rat(value)
	}
	return result
}

func mustExact(t *testing.T, value string) Exact {
	t.Helper()
	result, err := ParseExact(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func exacts(t *testing.T, values ...string) []Exact {
	t.Helper()
	result := make([]Exact, len(values))
	for index, value := range values {
		result[index] = mustExact(t, value)
	}
	return result
}

func assertStereo(t *testing.T, actual StereoSample, left, right string) {
	t.Helper()
	if actual.Left.String() != left || actual.Right.String() != right {
		t.Fatalf("stereo = (%s,%s), want (%s,%s)", actual.Left.String(), actual.Right.String(), left, right)
	}
}

func digest(character byte) string {
	return "sha256:" + string(bytes.Repeat([]byte{character}, 64))
}

func timePointer(value documentv2.RationalTime) *documentv2.RationalTime { return &value }
func rationalPointer(value documentv2.Rational) *documentv2.Rational     { return &value }

func hasDiagnostic(values []documentv2.Diagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func assertCanceled(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeCanceled {
		t.Fatalf("cancellation typed error = %#v", typed)
	}
}

func removeCapability(values []videoedit.CapabilityID, remove videoedit.CapabilityID) []videoedit.CapabilityID {
	result := make([]videoedit.CapabilityID, 0, len(values))
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}

func equalIDs(left, right []documentv2.ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneTimelines(values []documentv2.Timeline) []documentv2.Timeline {
	result := append([]documentv2.Timeline(nil), values...)
	for timelineIndex := range result {
		result[timelineIndex].Tracks = append([]documentv2.Track(nil), result[timelineIndex].Tracks...)
		for trackIndex := range result[timelineIndex].Tracks {
			result[timelineIndex].Tracks[trackIndex].Clips = append([]documentv2.Clip(nil), result[timelineIndex].Tracks[trackIndex].Clips...)
			for clipIndex := range result[timelineIndex].Tracks[trackIndex].Clips {
				clip := &result[timelineIndex].Tracks[trackIndex].Clips[clipIndex]
				if clip.PropertySet != nil {
					copy := *clip.PropertySet
					clip.PropertySet = &copy
				}
			}
		}
	}
	return result
}

func decodeStereoStrings(t *testing.T, raw json.RawMessage) StereoSample {
	t.Helper()
	var value map[string]string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return StereoSample{Left: mustExact(t, value["left"]), Right: mustExact(t, value["right"])}
}
