package wavsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
)

func TestWAVSourceAudioGraphRetimeGainPanAndPCMEndToEnd(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{-32768, 0, 32767, 16384})
	document := audioDocument(encoded, 4, 4, rt(2, 1), rt(1, 1))
	document.Timelines[0].Tracks[0].Clips[0].PropertySet = &documentv2.PropertySet{
		AudioGain: &documentv2.AudioGainProperty{Linear: ratio(1, 2)},
		AudioPan:  &documentv2.AudioPanProperty{Value: ratio(1, 2)},
	}
	provider := compileProvider(t, document, encoded)
	bindings := provider.MediaBindings()
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: bindings})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	block, err := program.EvaluateBlock(context.Background(), "timeline", videoedit.SampleClock{
		Origin: rt(0, 1), SamplesPerSecond: 4,
	}, 0, 4, provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Samples) != 4 {
		t.Fatalf("samples=%d", len(block.Samples))
	}
	want := [][2]string{
		{"-1/4", "-1/2"}, {"-1/8", "-1/4"}, {"0/1", "0/1"}, {"32767/262144", "32767/131072"},
	}
	for index := range want {
		assertExact(t, block.Samples[index].Left, want[index][0])
		assertExact(t, block.Samples[index].Right, want[index][1])
	}
	quantized, err := pcm16le.Quantize(context.Background(), 4, 0, block.Samples, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	encodedPCM, _ := quantized.PCM.Bytes()
	if quantized.FrameCount != 4 || quantized.PCMHash != "sha256:b67b0ac98b4ea1e83a793f61c5bf87c48ffcf0af9bbf8e69900edc2911008ecd" {
		t.Fatalf("frame_count=%d PCM hash=%s", quantized.FrameCount, quantized.PCMHash)
	}
	if got := hex.EncodeToString(encodedPCM); got != "00e000c000f000e00000000000100020" {
		t.Fatalf("PCM=%s hash=%s", got, quantized.PCMHash)
	}
	manifest := provider.Manifest()
	if manifest.DocumentHash == "" || len(manifest.Assets) != 1 || manifest.Assets[0].FrameCount != 4 ||
		manifest.Assets[0].DecodeProfile != wavdecode.ProfileV1 || manifest.Assets[0].SelectionProfile != SelectionProfileV1 ||
		manifest.Assets[0].RetimeProfile != RetimeProfileV1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if bindings[0].Manifest.Duration != rt(4, 4) || bindings[0].Manifest.Streams[0].Audio.SampleFormat != "s16" ||
		bindings[0].Manifest.Streams[0].Audio.EncoderDelaySamples != 0 || bindings[0].Manifest.Streams[0].Audio.PaddingSamples != 0 {
		t.Fatalf("binding=%+v", bindings[0])
	}
}

func TestStereoAndTerminalCellHold(t *testing.T) {
	encoded := strictWAV(4, 2, []int16{-32768, 32767, 16384, -16384})
	document := audioDocument(encoded, 4, 2, rt(1, 2), rt(1, 2))
	provider := compileProvider(t, document, encoded)
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: provider.MediaBindings()})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	result, err := program.EvaluateAt(context.Background(), "timeline", rt(3, 8), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertExact(t, result.Output.Left, "1/2")
	assertExact(t, result.Output.Right, "-1/2")
	if len(result.ActiveClips) != 1 || result.ActiveClips[0].Source.Request.SourceTime != rt(3, 8) ||
		result.ActiveClips[0].Source.Request.AssetIndex != 0 || result.ActiveClips[0].Source.SampleDomain != audiograph.SourceSampleDomainProfileV1 {
		t.Fatalf("evaluation=%+v", result)
	}
}

func TestExactLinearSamplerIntegerFractionTerminalAndBounds(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{-32768, 0, 32767})
	decoded, err := wavdecode.Decode(context.Background(), encoded, wavdecode.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	entry := assetEntry{pcm: decoded.PCM, decoded: decoded}
	for _, test := range []struct {
		name string
		at   documentv2.RationalTime
		want string
	}{
		{name: "integer-first", at: rt(0, 1), want: "-1/1"},
		{name: "fraction-first-second", at: rt(1, 8), want: "-1/2"},
		{name: "integer-second", at: rt(1, 4), want: "0/1"},
		{name: "fraction-second-third", at: rt(3, 8), want: "32767/65536"},
		{name: "integer-final", at: rt(1, 2), want: "32767/32768"},
		{name: "terminal-hold", at: rt(5, 8), want: "32767/32768"},
	} {
		t.Run(test.name, func(t *testing.T) {
			channels, sampleErr := samplePCM(context.Background(), entry, test.at, DefaultMaxExactBits)
			if sampleErr != nil || len(channels) != 1 {
				t.Fatalf("channels=%v err=%v", channels, sampleErr)
			}
			assertExact(t, channels[0], test.want)
		})
	}
	for _, at := range []documentv2.RationalTime{rt(-1, 8), rt(3, 4), rt(7, 8)} {
		if channels, sampleErr := samplePCM(context.Background(), entry, at, DefaultMaxExactBits); channels != nil || !errors.Is(sampleErr, ErrSourceUnavailable) {
			t.Fatalf("at=%+v channels=%v err=%v", at, channels, sampleErr)
		}
	}
	if channels, sampleErr := samplePCM(context.Background(), entry, rt(3, 8), 10); channels != nil || !errors.Is(sampleErr, ErrExactLimit) {
		t.Fatalf("exact limit channels=%v err=%v", channels, sampleErr)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if channels, sampleErr := samplePCM(canceled, entry, rt(0, 1), DefaultMaxExactBits); channels != nil || !errors.Is(sampleErr, ErrCanceled) {
		t.Fatalf("canceled channels=%v err=%v", channels, sampleErr)
	}
}

func TestManifestSortedAssetIndexesAndAggregateBindingBudgets(t *testing.T) {
	encodedZ := strictWAV(4, 1, []int16{300, 400})
	encodedA := strictWAV(4, 1, []int16{100, 200})
	document := twoAssetDocument(encodedZ, encodedA)
	bindings := []Binding{
		bindingFor(t, document, "z-asset", encodedZ),
		bindingFor(t, document, "a-asset", encodedA),
	}
	provider, err := Compile(context.Background(), document, bindings, DefaultConfig())
	if err != nil || provider == nil {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	manifest := provider.Manifest()
	mediaBindings := provider.MediaBindings()
	if len(manifest.Assets) != 2 || len(mediaBindings) != 2 ||
		manifest.Assets[0].AssetID != "a-asset" || manifest.Assets[0].AssetIndex != 1 ||
		manifest.Assets[1].AssetID != "z-asset" || manifest.Assets[1].AssetIndex != 0 ||
		mediaBindings[0].AssetID != "a-asset" || mediaBindings[1].AssetID != "z-asset" {
		t.Fatalf("manifest=%+v bindings=%+v", manifest, mediaBindings)
	}
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: mediaBindings})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), provider)
	if err != nil || len(evaluation.ActiveClips) != 2 {
		t.Fatalf("evaluation=%+v err=%v", evaluation, err)
	}
	indexes := map[documentv2.ID]int{}
	for _, clip := range evaluation.ActiveClips {
		indexes[clip.AssetID] = clip.Source.Request.AssetIndex
	}
	if indexes["a-asset"] != 1 || indexes["z-asset"] != 0 {
		t.Fatalf("asset indexes=%v", indexes)
	}

	config := DefaultConfig()
	config.Limits.MaxTotalEncodedBytes = uint64(len(encodedZ) + len(encodedA) - 1)
	if partial, limitErr := Compile(context.Background(), document, bindings, config); partial != nil || !errors.Is(limitErr, ErrResourceLimit) {
		t.Fatalf("aggregate encoded partial=%v err=%v", partial, limitErr)
	}
	config = DefaultConfig()
	config.Limits.MaxRetainedBytes = uint64(len(encodedZ) + len(encodedA) - 2*44 - 1)
	if partial, limitErr := Compile(context.Background(), document, bindings, config); partial != nil || !errors.Is(limitErr, ErrResourceLimit) {
		t.Fatalf("aggregate retained partial=%v err=%v", partial, limitErr)
	}
}

func TestCompileBindingDurationAssetAndLimitsFailAtomically(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{1, 2, 3, 4})
	base := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	binding := bindingFor(t, base, "asset", encoded)
	other := append([]byte(nil), encoded...)
	other[len(other)-1] ^= 1
	tests := []struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{
		{name: "missing", document: base, config: DefaultConfig(), want: ErrBindingMissing},
		{name: "duplicate", document: base, bindings: []Binding{binding, binding}, config: DefaultConfig(), want: ErrBindingInvalid},
		{name: "document hash", document: base, bindings: []Binding{{DocumentHash: digest([]byte("other")), AssetID: "asset", EncodedWAV: encoded}}, config: DefaultConfig(), want: ErrDocumentMismatch},
		{name: "content", document: base, bindings: []Binding{{DocumentHash: binding.DocumentHash, AssetID: "asset", EncodedWAV: other}}, config: DefaultConfig(), want: ErrContentMismatch},
		{name: "invalid config", document: base, bindings: []Binding{binding}, config: Config{Limits: Limits{MaxBindings: HardMaxBindings + 1}}, want: ErrConfigInvalid},
	}
	badDocument := documentWithHash(audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1)), []byte("bad"))
	tests = append(tests, struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{name: "bad wav", document: badDocument, bindings: []Binding{bindingFor(t, badDocument, "asset", []byte("bad"))}, config: DefaultConfig(), want: ErrDecodeFailed})
	wrongDuration := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	wrong := rt(2, 1)
	wrongDuration.Assets[0].Duration = &wrong
	tests = append(tests, struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{name: "duration", document: wrongDuration, bindings: []Binding{bindingFor(t, wrongDuration, "asset", encoded)}, config: DefaultConfig(), want: ErrDurationMismatch})
	video := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	video.Assets[0].Kind = documentv2.AssetKindVideo
	tests = append(tests, struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{name: "video asset", document: video, bindings: []Binding{bindingFor(t, video, "asset", encoded)}, config: DefaultConfig(), want: ErrDocumentInvalid})
	uriOnly := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	uriOnly.Assets[0].ContentHash = ""
	tests = append(tests, struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{name: "uri only", document: uriOnly, config: DefaultConfig(), want: ErrDocumentInvalid})
	emptyBytes := strictWAV(4, 1, nil)
	empty := audioDocument(emptyBytes, 4, 4, rt(1, 1), rt(1, 1))
	tests = append(tests, struct {
		name     string
		document documentv2.Document
		bindings []Binding
		config   Config
		want     error
	}{name: "empty", document: empty, bindings: []Binding{bindingFor(t, empty, "asset", emptyBytes)}, config: DefaultConfig(), want: ErrSourceUnavailable})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := Compile(context.Background(), test.document, test.bindings, test.config)
			if provider != nil || !errors.Is(err, test.want) {
				t.Fatalf("provider=%v err=%v want=%v", provider, err, test.want)
			}
		})
	}

	config := DefaultConfig()
	config.Limits.MaxTotalEncodedBytes = uint64(len(encoded) - 1)
	if provider, err := Compile(context.Background(), base, []Binding{binding}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("encoded limit provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.MaxRetainedBytes = uint64(len(encoded) - 45)
	if provider, err := Compile(context.Background(), base, []Binding{binding}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("retained limit provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.WAV.MaxWorkingBytes = 1
	if provider, err := Compile(context.Background(), base, []Binding{binding}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("decoder working limit provider=%v err=%v", provider, err)
	}
	config = DefaultConfig()
	config.Limits.MaxBindings = 1
	if provider, err := Compile(context.Background(), base, []Binding{binding, binding}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("binding count limit provider=%v err=%v", provider, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if provider, err := Compile(ctx, base, []Binding{binding}, DefaultConfig()); provider != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled provider=%v err=%v", provider, err)
	}
}

func TestDisabledClipNeedsNoBindingAndExtraBindingRejected(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{1})
	document := audioDocument(encoded, 4, 1, rt(1, 4), rt(1, 4))
	document.Timelines[0].Tracks[0].Clips[0].Disabled = true
	provider, err := Compile(context.Background(), document, nil, DefaultConfig())
	if err != nil || provider == nil || len(provider.MediaBindings()) != 0 {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	if extra, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig()); extra != nil || !errors.Is(err, ErrBindingExtra) {
		t.Fatalf("extra=%v err=%v", extra, err)
	}
}

func TestProviderRejectsHostileScopeBindingAndTimeRequests(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{-32768, 0, 32767, 1})
	document := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	provider := compileProvider(t, document, encoded)
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: provider.MediaBindings()})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	var valid audiograph.SourceRequest
	_, err := program.EvaluateAt(context.Background(), "timeline", rt(1, 4), audiograph.SourceProviderFunc(func(ctx context.Context, request audiograph.SourceRequest) (audiograph.SourceSample, error) {
		valid = cloneRequest(request)
		return provider.Sample(ctx, request)
	}))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*audiograph.SourceRequest)
		want   error
	}{
		{name: "document", mutate: func(v *audiograph.SourceRequest) { v.DocumentHash = digest([]byte("other")) }, want: ErrRequestInvalid},
		{name: "timeline", mutate: func(v *audiograph.SourceRequest) { v.TimelineID = "other" }, want: ErrRequestMismatch},
		{name: "timeline index", mutate: func(v *audiograph.SourceRequest) { v.TimelineIndex++ }, want: ErrRequestMismatch},
		{name: "track", mutate: func(v *audiograph.SourceRequest) { v.TrackID = "other" }, want: ErrRequestMismatch},
		{name: "clip", mutate: func(v *audiograph.SourceRequest) { v.ClipID = "other" }, want: ErrRequestMismatch},
		{name: "asset", mutate: func(v *audiograph.SourceRequest) { v.AssetID = "other" }, want: ErrRequestMismatch},
		{name: "content", mutate: func(v *audiograph.SourceRequest) { v.ResourceContentHash = digest([]byte("other")) }, want: ErrRequestMismatch},
		{name: "manifest", mutate: func(v *audiograph.SourceRequest) { v.ManifestFingerprint = digest([]byte("other")) }, want: ErrRequestMismatch},
		{name: "decode", mutate: func(v *audiograph.SourceRequest) { v.DecodeProfile = "vendor.decode.v1" }, want: ErrRequestInvalid},
		{name: "selection", mutate: func(v *audiograph.SourceRequest) { v.SelectionProfile = "vendor.selection.v1" }, want: ErrRequestInvalid},
		{name: "retime", mutate: func(v *audiograph.SourceRequest) { v.RetimeProfile = "vendor.retime.v1" }, want: ErrRequestInvalid},
		{name: "local", mutate: func(v *audiograph.SourceRequest) { v.LocalTime = rt(0, 1) }, want: ErrRequestMismatch},
		{name: "source", mutate: func(v *audiograph.SourceRequest) { v.SourceTime = rt(1, 2) }, want: ErrRequestMismatch},
		{name: "negative source", mutate: func(v *audiograph.SourceRequest) {
			v.SourceTime = rt(-1, 1)
			v.LocalTime = rt(-1, 1)
			v.TimelineTime = rt(-1, 1)
		}, want: ErrRequestMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneRequest(valid)
			test.mutate(&request)
			result, sampleErr := provider.Sample(context.Background(), request)
			if result.Channels != nil || !errors.Is(sampleErr, test.want) {
				t.Fatalf("result=%+v err=%v want=%v", result, sampleErr, test.want)
			}
		})
	}
	if result, err := provider.Sample(nil, valid); result.Channels != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context result=%+v err=%v", result, err)
	}
}

func TestSourceSampleOwnsCompleteRequestEcho(t *testing.T) {
	encoded := strictWAV(4, 2, []int16{-32768, 32767, 0, 0})
	document := audioDocument(encoded, 4, 2, rt(1, 2), rt(1, 2))
	provider := compileProvider(t, document, encoded)
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: provider.MediaBindings()})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	var request audiograph.SourceRequest
	_, err := program.EvaluateAt(context.Background(), "timeline", rt(1, 8), audiograph.SourceProviderFunc(func(ctx context.Context, value audiograph.SourceRequest) (audiograph.SourceSample, error) {
		request = cloneRequest(value)
		return provider.Sample(ctx, value)
	}))
	if err != nil {
		t.Fatal(err)
	}
	wantRequest := cloneRequest(request)
	sample, err := provider.Sample(context.Background(), request)
	if err != nil || sample.ContractVersion != audiograph.SourceContractVersion ||
		sample.SampleDomain != audiograph.SourceSampleDomainProfileV1 || !reflect.DeepEqual(sample.Request, wantRequest) {
		t.Fatalf("sample=%+v err=%v", sample, err)
	}
	request.DecoderChannels[0] = "MUTATED"
	if sample.Request.DecoderChannels[0] != "FL" {
		t.Fatal("response request aliases caller-owned decoder channels")
	}
	sample.Request.DecoderChannels[0] = "MUTATED"
	sample.Channels[0] = audiograph.Zero()
	again, err := provider.Sample(context.Background(), wantRequest)
	if err != nil || again.Request.DecoderChannels[0] != "FL" || again.Channels[0].String() != "-1/2" {
		t.Fatalf("provider retained response storage: sample=%+v err=%v", again, err)
	}
}

func TestCrossDocumentBindingAndRequestReplayRejected(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{1, 2, 3, 4})
	documentA := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	providerA := compileProvider(t, documentA, encoded)
	programA, diagnostics := audiograph.Compile(documentA, audiograph.CompileOptions{Offered: offeredFor(t, documentA), Bindings: providerA.MediaBindings()})
	if programA == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", programA, diagnostics)
	}
	var requestA audiograph.SourceRequest
	_, err := programA.EvaluateAt(context.Background(), "timeline", rt(0, 1), audiograph.SourceProviderFunc(func(ctx context.Context, request audiograph.SourceRequest) (audiograph.SourceSample, error) {
		requestA = cloneRequest(request)
		return providerA.Sample(ctx, request)
	}))
	if err != nil {
		t.Fatal(err)
	}
	documentB := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	documentB.Revision = 2
	providerB := compileProvider(t, documentB, encoded)
	if replay, replayErr := providerB.Sample(context.Background(), requestA); replay.Channels != nil || !errors.Is(replayErr, ErrRequestInvalid) {
		t.Fatalf("request replay=%+v err=%v", replay, replayErr)
	}
	if partial, replayErr := Compile(context.Background(), documentB, []Binding{bindingFor(t, documentA, "asset", encoded)}, DefaultConfig()); partial != nil || !errors.Is(replayErr, ErrDocumentMismatch) {
		t.Fatalf("binding replay provider=%v err=%v", partial, replayErr)
	}
}

func TestManifestBindingsOwnershipAndConcurrentSeekDeterminism(t *testing.T) {
	encoded := strictWAV(8, 2, []int16{-8, 8, -4, 4, 0, 0, 4, -4, 8, -8, 12, -12, 16, -16, 20, -20})
	document := audioDocument(encoded, 8, 8, rt(1, 1), rt(1, 1))
	provider := compileProvider(t, document, encoded)
	bindings := provider.MediaBindings()
	manifest := provider.Manifest()
	bindings[0].DecoderChannels[0] = "MUTATED"
	bindings[0].Manifest.Streams[0].Audio.ChannelLayout = "mutated"
	manifest.Assets[0].DecoderChannels[0] = "MUTATED"
	again := provider.MediaBindings()
	againManifest := provider.Manifest()
	if again[0].DecoderChannels[0] != "FL" || again[0].Manifest.Streams[0].Audio.ChannelLayout != "stereo" || againManifest.Assets[0].DecoderChannels[0] != "FL" {
		t.Fatal("provider exposed mutable manifest/binding storage")
	}
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: again})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	times := []documentv2.RationalTime{rt(7, 8), rt(1, 8), rt(5, 16), rt(0, 1), rt(3, 4)}
	want := make(map[documentv2.RationalTime][2]string)
	for _, at := range times {
		result, err := program.EvaluateAt(context.Background(), "timeline", at, provider)
		if err != nil {
			t.Fatal(err)
		}
		want[at] = [2]string{result.Output.Left.String(), result.Output.Right.String()}
	}
	const workers = 12
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers*len(times))
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := range times {
				at := times[(worker+index)%len(times)]
				result, err := program.EvaluateAt(context.Background(), "timeline", at, provider)
				if err != nil || result.Output.Left.String() != want[at][0] || result.Output.Right.String() != want[at][1] {
					errorsOut <- fmt.Errorf("at=%v output=%+v err=%v", at, result.Output, err)
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func TestDocumentPreflightAndCanonicalFixtures(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{-32768, 0, 32767, 1})
	document := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	document.Metadata = map[string]json.RawMessage{"escaped<key>": json.RawMessage(`{"nested":"<>&"}`)}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	measure := documentMeasure{ctx: context.Background(), nodeLimit: HardMaxDocumentNodes, byteLimit: HardMaxCanonicalDocumentBytes}
	if err := measure.addNodes(1); err != nil {
		t.Fatal(err)
	}
	if err := measure.walk(reflect.ValueOf(document)); err != nil || measure.encodedUpper < uint64(len(canonical)) {
		t.Fatalf("measure=%+v canonical=%d err=%v", measure, len(canonical), err)
	}

	provider := compileProvider(t, document, encoded)
	assertCanonicalFixture(t, "testdata/profile-v1.canonical.json", Descriptor())
	assertCanonicalFixture(t, "testdata/manifest-v1.canonical.json", provider.Manifest())
}

func TestDocumentAndClipScopePreflightLimits(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{1, 2, 3, 4})
	document := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	binding := bindingFor(t, document, "asset", encoded)
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "document nodes", mutate: func(config *Config) { config.Limits.MaxDocumentNodes = 1 }},
		{name: "document bytes", mutate: func(config *Config) { config.Limits.MaxCanonicalDocumentBytes = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			test.mutate(&config)
			if provider, err := Compile(context.Background(), document, []Binding{binding}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("provider=%v err=%v", provider, err)
			}
		})
	}

	twoClips := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	second := twoClips.Timelines[0].Tracks[0].Clips[0]
	second.ID = "clip-2"
	second.TimelineRange.Start = rt(1, 1)
	twoClips.Timelines[0].Tracks[0].Clips = append(twoClips.Timelines[0].Tracks[0].Clips, second)
	config := DefaultConfig()
	config.Limits.MaxClipScopes = 1
	if provider, err := Compile(context.Background(), twoClips, []Binding{bindingFor(t, twoClips, "asset", encoded)}, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("clip scope provider=%v err=%v", provider, err)
	}

	twoAssets := twoAssetDocument(strictWAV(4, 1, []int16{1, 2}), strictWAV(4, 1, []int16{3, 4}))
	config = DefaultConfig()
	config.Limits.MaxTracksPerTimeline = 1
	if provider, err := Compile(context.Background(), twoAssets, nil, config); provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("track limit provider=%v err=%v", provider, err)
	}
}

type sourceVectorCase struct {
	Name                  string      `json:"name"`
	SampleRate            uint32      `json:"sample_rate"`
	Channels              uint16      `json:"channels"`
	PCM                   []int16     `json:"pcm"`
	SourceTimes           [][2]int64  `json:"source_times"`
	ExpectedSource        [][]string  `json:"expected_source"`
	ExpectedStereo        [][2]string `json:"expected_stereo"`
	ExpectedEncodedHash   string      `json:"expected_encoded_hash"`
	ExpectedSampleHash    string      `json:"expected_sample_hash"`
	ExpectedQuantizedHex  string      `json:"expected_quantized_hex"`
	ExpectedQuantizedHash string      `json:"expected_quantized_hash"`
}

type sourceVectorsFixture struct {
	Profile             string             `json:"profile"`
	DecodeProfile       string             `json:"decode_profile"`
	DecoderSampleDomain string             `json:"decoder_sample_domain"`
	ContentHashProfile  string             `json:"content_hash_profile"`
	SampleHashProfile   string             `json:"sample_hash_profile"`
	SelectionProfile    string             `json:"selection_profile"`
	RetimeProfile       string             `json:"retime_profile"`
	SourceProfile       string             `json:"source_profile"`
	GraphSampleDomain   string             `json:"graph_sample_domain"`
	Cases               []sourceVectorCase `json:"cases"`
}

func TestCanonicalWAVSourceVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/source-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture sourceVectorsFixture
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) || fixture.Profile != ProfileV1 ||
		fixture.DecodeProfile != wavdecode.ProfileV1 || fixture.DecoderSampleDomain != wavdecode.SampleDomainProfileV1 ||
		fixture.ContentHashProfile != wavdecode.ContentHashProfileV1 || fixture.SampleHashProfile != wavdecode.SampleHashProfileV1 ||
		fixture.SelectionProfile != SelectionProfileV1 || fixture.RetimeProfile != RetimeProfileV1 ||
		fixture.SourceProfile != audiograph.SourceProfileV1 || fixture.GraphSampleDomain != audiograph.SourceSampleDomainProfileV1 ||
		len(fixture.Cases) == 0 {
		t.Fatalf("invalid/noncanonical vectors\ncanonical=%s", canonical)
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			if vector.Name == "" || vector.SampleRate == 0 || (vector.Channels != 1 && vector.Channels != 2) ||
				len(vector.PCM) == 0 || len(vector.PCM)%int(vector.Channels) != 0 ||
				len(vector.SourceTimes) != len(vector.ExpectedSource) || len(vector.SourceTimes) != len(vector.ExpectedStereo) {
				t.Fatalf("invalid vector shape: %+v", vector)
			}
			encoded := strictWAV(vector.SampleRate, vector.Channels, vector.PCM)
			frames := uint64(len(vector.PCM) / int(vector.Channels))
			duration := rt(int64(frames), int64(vector.SampleRate))
			document := audioDocument(encoded, vector.SampleRate, frames, duration, duration)
			provider := compileProvider(t, document, encoded)
			manifest := provider.Manifest()
			if len(manifest.Assets) != 1 || manifest.Assets[0].EncodedContentHash != vector.ExpectedEncodedHash ||
				manifest.Assets[0].SampleHash != vector.ExpectedSampleHash ||
				manifest.Assets[0].ContentHashProfile != fixture.ContentHashProfile ||
				manifest.Assets[0].SampleHashProfile != fixture.SampleHashProfile {
				t.Fatalf("manifest=%+v expected_encoded=%s expected_sample=%s", manifest, vector.ExpectedEncodedHash, vector.ExpectedSampleHash)
			}
			program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(t, document), Bindings: provider.MediaBindings()})
			if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
			}
			outputs := make([]audiograph.StereoSample, len(vector.SourceTimes))
			for index, pair := range vector.SourceTimes {
				at := rt(pair[0], pair[1])
				evaluation, evaluateErr := program.EvaluateAt(context.Background(), "timeline", at, provider)
				if evaluateErr != nil || len(evaluation.ActiveClips) != 1 {
					t.Fatalf("at=%+v evaluation=%+v err=%v", at, evaluation, evaluateErr)
				}
				receipt := evaluation.ActiveClips[0].Source
				if len(receipt.Channels) != len(vector.ExpectedSource[index]) {
					t.Fatalf("at=%+v receipt=%+v", at, receipt)
				}
				for channel, expected := range vector.ExpectedSource[index] {
					assertExact(t, receipt.Channels[channel], expected)
				}
				assertExact(t, evaluation.Output.Left, vector.ExpectedStereo[index][0])
				assertExact(t, evaluation.Output.Right, vector.ExpectedStereo[index][1])
				outputs[index] = evaluation.Output
			}
			quantized, quantizeErr := pcm16le.Quantize(context.Background(), int64(vector.SampleRate), 0, outputs, pcm16le.DefaultLimits())
			if quantizeErr != nil {
				t.Fatal(quantizeErr)
			}
			encodedPCM, bytesErr := quantized.PCM.Bytes()
			if bytesErr != nil || quantized.FrameCount != uint64(len(outputs)) ||
				hex.EncodeToString(encodedPCM) != vector.ExpectedQuantizedHex || quantized.PCMHash != vector.ExpectedQuantizedHash {
				t.Fatalf("PCM frames=%d hex=%s hash=%s err=%v", quantized.FrameCount, hex.EncodeToString(encodedPCM), quantized.PCMHash, bytesErr)
			}
		})
	}
}

func FuzzProviderSourceTimeNeverPanics(f *testing.F) {
	encoded := strictWAV(8, 1, []int16{-32768, -100, 0, 100, 32767, 20, -20, 1})
	document := audioDocument(encoded, 8, 8, rt(1, 1), rt(1, 1))
	provider := compileProvider(f, document, encoded)
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{Offered: offeredFor(f, document), Bindings: provider.MediaBindings()})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		f.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	f.Add(int64(1), int64(2))
	f.Add(int64(7), int64(8))
	f.Fuzz(func(t *testing.T, value, rate int64) {
		if rate == 0 {
			rate = 1
		}
		if rate < 0 {
			if rate == mathMinInt64 {
				return
			}
			rate = -rate
		}
		at := rt(value, rate)
		result, err := program.EvaluateAt(context.Background(), "timeline", at, provider)
		if err == nil {
			if !result.Output.Left.Valid() || !result.Output.Right.Valid() {
				t.Fatalf("invalid success: %+v", result)
			}
		}
	})
}

const mathMinInt64 = -1 << 63

func compileProvider(t testing.TB, document documentv2.Document, encoded []byte) *Provider {
	t.Helper()
	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig())
	if err != nil || provider == nil {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	return provider
}

func bindingFor(t testing.TB, document documentv2.Document, assetID documentv2.ID, encoded []byte) Binding {
	t.Helper()
	hash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	return Binding{DocumentHash: hash, AssetID: assetID, EncodedWAV: encoded}
}

func audioDocument(encoded []byte, sampleRate uint32, frames uint64, timelineDuration, sourceDuration documentv2.RationalTime) documentv2.Document {
	duration := documentv2.RationalTime{Value: int64(frames), Rate: int64(sampleRate)}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset", Kind: documentv2.AssetKindAudio, URI: "ignored.wav", ContentHash: digest(encoded), Duration: &duration}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track", Kind: documentv2.TrackKindAudio,
			Clips: []documentv2.Clip{{
				ID: "clip", AssetID: "asset",
				TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: timelineDuration},
				SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: sourceDuration},
			}},
		}}}},
	}
}

func twoAssetDocument(encodedZ, encodedA []byte) documentv2.Document {
	duration := rt(1, 2)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "z-asset", Kind: documentv2.AssetKindAudio, URI: "ignored-z.wav", ContentHash: digest(encodedZ), Duration: &duration},
			{ID: "a-asset", Kind: documentv2.AssetKindAudio, URI: "ignored-a.wav", ContentHash: digest(encodedA), Duration: &duration},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "z-track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{
				ID: "z-clip", AssetID: "z-asset",
				TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: duration},
				SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: duration},
			}}},
			{ID: "a-track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{
				ID: "a-clip", AssetID: "a-asset",
				TimelineRange: documentv2.TimeRange{Start: rt(0, 1), Duration: duration},
				SourceRange:   documentv2.TimeRange{Start: rt(0, 1), Duration: duration},
			}}},
		}}},
	}
	return document
}

func documentWithHash(document documentv2.Document, encoded []byte) documentv2.Document {
	document.Assets[0].ContentHash = digest(encoded)
	return document
}

func offeredFor(t testing.TB, document documentv2.Document) videoedit.BackendCapabilities {
	t.Helper()
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("requirements=%+v", diagnostics)
	}
	offered := videoedit.BackendCapabilities{BackendID: "wavsource-test", Features: append([]videoedit.CapabilityID(nil), required.Features...)}
	for _, kind := range required.TrackKinds {
		offered.TrackKinds = append(offered.TrackKinds, videoedit.TrackKind(kind))
	}
	for _, kind := range required.AssetKinds {
		offered.AssetKinds = append(offered.AssetKinds, videoedit.AssetKind(kind))
	}
	return offered
}

func strictWAV(sampleRate uint32, channels uint16, samples []int16) []byte {
	dataBytes := len(samples) * 2
	result := make([]byte, 44+dataBytes)
	copy(result[0:4], "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
	copy(result[8:12], "WAVE")
	copy(result[12:16], "fmt ")
	binary.LittleEndian.PutUint32(result[16:20], 16)
	binary.LittleEndian.PutUint16(result[20:22], 1)
	binary.LittleEndian.PutUint16(result[22:24], channels)
	binary.LittleEndian.PutUint32(result[24:28], sampleRate)
	blockAlign := channels * 2
	binary.LittleEndian.PutUint32(result[28:32], sampleRate*uint32(blockAlign))
	binary.LittleEndian.PutUint16(result[32:34], blockAlign)
	binary.LittleEndian.PutUint16(result[34:36], 16)
	copy(result[36:40], "data")
	binary.LittleEndian.PutUint32(result[40:44], uint32(dataBytes))
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(result[44+index*2:46+index*2], uint16(sample))
	}
	return result
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}

func ratio(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func assertExact(t testing.TB, actual audiograph.Exact, expected string) {
	t.Helper()
	want, err := audiograph.ParseExact(expected)
	if err != nil {
		t.Fatal(err)
	}
	if !actual.Equal(want) {
		t.Fatalf("exact=%s want=%s", actual.String(), want.String())
	}
}

func assertCanonicalFixture(t testing.TB, path string, value any) {
	t.Helper()
	want, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\ncanonical=%s", err, want)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), want) {
		got := bytes.TrimSpace(raw)
		first := 0
		for first < len(got) && first < len(want) && got[first] == want[first] {
			first++
		}
		start, endWant, endGot := first-40, first+80, first+80
		if start < 0 {
			start = 0
		}
		if endWant > len(want) {
			endWant = len(want)
		}
		if endGot > len(got) {
			endGot = len(got)
		}
		t.Fatalf("fixture differs at byte %d (want_len=%d got_len=%d)\nwant_window=%q\ngot_window=%q", first, len(want), len(got), want[start:endWant], got[start:endGot])
	}
}
