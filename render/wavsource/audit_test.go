package wavsource

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/wavencode"
)

func TestDistinctReferencedAssetsFailBeforeExceedingBindingBudget(t *testing.T) {
	document := twoAssetDocument(
		strictWAV(4, 1, []int16{1, 2}),
		strictWAV(4, 1, []int16{3, 4}),
	)
	config := DefaultConfig()
	config.Limits.MaxBindings = 1
	provider, err := Compile(context.Background(), document, nil, config)
	if provider != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	var structured *Error
	if !errors.As(err, &structured) || structured.Path != "document/referenced_audio_assets" {
		t.Fatalf("error=%#v", err)
	}
}

func TestSortedDecodeErrorsRetainOriginalBindingIndex(t *testing.T) {
	encodedZ := strictWAV(4, 1, []int16{300, 400})
	encodedA := strictWAV(4, 1, []int16{100, 200})
	document := twoAssetDocument(encodedZ, encodedA)
	corruptA := append([]byte(nil), encodedA...)
	corruptA[44] ^= 1 // Remains a structurally valid WAV but no longer matches the frozen asset hash.
	bindings := []Binding{
		bindingFor(t, document, "z-asset", encodedZ),
		bindingFor(t, document, "a-asset", corruptA),
	}
	provider, err := Compile(context.Background(), document, bindings, DefaultConfig())
	if provider != nil || !errors.Is(err, ErrContentMismatch) {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	var structured *Error
	if !errors.As(err, &structured) || structured.Path != "bindings/1/content_hash" {
		t.Fatalf("error=%#v", err)
	}
}

func TestNonzeroIndexesAndSourceStartRetimeOracle(t *testing.T) {
	encoded := strictWAV(8, 1, []int16{-8, -4, 0, 4, 8, 12, 16, 20})
	document := audioDocument(encoded, 8, 8, rt(1, 1), rt(1, 2))

	unusedDuration := rt(1, 1)
	document.Assets = append([]documentv2.Asset{{
		ID: "unused-asset", Kind: documentv2.AssetKindAudio, URI: "unused.wav",
		ContentHash: digest(encoded), Duration: &unusedDuration,
	}}, document.Assets...)
	target := document.Timelines[0]
	target.ID = "target-timeline"
	targetTrack := target.Tracks[0]
	targetTrack.ID = "target-track"
	active := targetTrack.Clips[0]
	active.ID = "active-clip"
	active.TimelineRange = documentv2.TimeRange{Start: rt(2, 1), Duration: rt(1, 1)}
	active.SourceRange = documentv2.TimeRange{Start: rt(1, 4), Duration: rt(1, 2)}
	disabled := active
	disabled.ID = "disabled-clip"
	disabled.Disabled = true
	disabled.TimelineRange = documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 1)}
	disabled.SourceRange = documentv2.TimeRange{Start: rt(0, 1), Duration: rt(1, 4)}
	targetTrack.Clips = []documentv2.Clip{disabled, active}
	target.Tracks = []documentv2.Track{
		{ID: "leading-annotation-track", Kind: documentv2.TrackKindAnnotation},
		targetTrack,
	}
	document.Timelines = []documentv2.Timeline{
		{ID: "leading-timeline"},
		target,
	}

	provider, err := Compile(context.Background(), document, []Binding{bindingFor(t, document, "asset", encoded)}, DefaultConfig())
	if err != nil || provider == nil {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{
		Offered: offeredFor(t, document), Bindings: provider.MediaBindings(),
	})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	evaluation, err := program.EvaluateAt(context.Background(), "target-timeline", rt(5, 2), provider)
	if err != nil || len(evaluation.ActiveClips) != 1 {
		t.Fatalf("evaluation=%+v err=%v", evaluation, err)
	}
	receipt := evaluation.ActiveClips[0].Source
	request := receipt.Request
	if request.TimelineIndex != 1 || request.TrackIndex != 1 || request.ClipIndex != 1 || request.AssetIndex != 1 {
		t.Fatalf("request indexes=%+v", request)
	}
	if request.LocalTime != rt(1, 2) || request.SourceTime != rt(1, 2) {
		t.Fatalf("local=%+v source=%+v", request.LocalTime, request.SourceTime)
	}
	// Independent mapping oracle: 1/4 + (1/2 local)*(1/2 source duration / 1 timeline duration) = 1/2.
	assertExact(t, receipt.Channels[0], "1/4096")
	assertExact(t, evaluation.Output.Left, "1/4096")
	assertExact(t, evaluation.Output.Right, "1/4096")
}

func TestCompileOwnsDecodedSamplesAfterCallerMutation(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{-32768, 0, 32767, 1})
	document := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	provider := compileProvider(t, document, encoded)
	for index := 44; index < len(encoded); index++ {
		encoded[index] ^= 0xff
	}
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{
		Offered: offeredFor(t, document), Bindings: provider.MediaBindings(),
	})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	evaluation, err := program.EvaluateAt(context.Background(), "timeline", rt(0, 1), provider)
	if err != nil {
		t.Fatal(err)
	}
	assertExact(t, evaluation.Output.Left, "-1/1")
	assertExact(t, evaluation.Output.Right, "-1/1")
}

func TestFullWAVSourceGraphPCMEncodeDecodeRoundTrip(t *testing.T) {
	encoded := strictWAV(4, 2, []int16{-32768, 32767, -16384, 16384, 0, 1, 32767, -32768})
	document := audioDocument(encoded, 4, 4, rt(1, 1), rt(1, 1))
	provider := compileProvider(t, document, encoded)
	program, diagnostics := audiograph.Compile(document, audiograph.CompileOptions{
		Offered: offeredFor(t, document), Bindings: provider.MediaBindings(),
	})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	block, err := program.EvaluateBlock(context.Background(), "timeline", videoedit.SampleClock{
		Origin: rt(0, 1), SamplesPerSecond: 4,
	}, 0, 4, provider)
	if err != nil {
		t.Fatal(err)
	}
	quantized, err := pcm16le.Quantize(context.Background(), 4, 0, block.Samples, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	wave, err := wavencode.Encode(context.Background(), quantized, wavencode.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := wave.Artifact.Bytes()
	if err != nil || digest(artifact) != wave.ArtifactHash {
		t.Fatalf("artifact hash=%s err=%v", wave.ArtifactHash, err)
	}
	roundTrip, err := wavdecode.Decode(context.Background(), artifact, wavdecode.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.FrameCount != 4 || roundTrip.Channels != 2 || roundTrip.EncodedContentHash != wave.ArtifactHash {
		t.Fatalf("round trip=%+v", roundTrip)
	}
	pcmBytes, err := quantized.PCM.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for frame := uint64(0); frame < roundTrip.FrameCount; frame++ {
		for channel := 0; channel < 2; channel++ {
			offset := int(frame)*4 + channel*2
			want := int16(binary.LittleEndian.Uint16(pcmBytes[offset : offset+2]))
			got, sampleErr := roundTrip.PCM.Sample(frame, channel)
			if sampleErr != nil || got != want {
				t.Fatalf("frame=%d channel=%d got=%d want=%d err=%v", frame, channel, got, want, sampleErr)
			}
		}
	}
}

type stepCancelContext struct {
	remaining int
	done      chan struct{}
	once      sync.Once
}

func newStepCancelContext(remaining int) *stepCancelContext {
	return &stepCancelContext{remaining: remaining, done: make(chan struct{})}
}

func (ctx *stepCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *stepCancelContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *stepCancelContext) Value(any) any               { return nil }
func (ctx *stepCancelContext) Err() error {
	if ctx.remaining > 1 {
		ctx.remaining--
		return nil
	}
	ctx.remaining = 0
	ctx.once.Do(func() { close(ctx.done) })
	return context.Canceled
}

func TestCanonicalDocumentHashHonorsCancellationDuringBytes(t *testing.T) {
	ctx := newStepCancelContext(2)
	hash, err := hashBytes(ctx, make([]byte, 96<<10), "document/hash")
	if hash != "" || !errors.Is(err, ErrCanceled) {
		t.Fatalf("hash=%q err=%v", hash, err)
	}
}
