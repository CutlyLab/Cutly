package wavencode

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
)

func TestEncodeStrictWAVHeaderPayloadHashAndRoundTrip(t *testing.T) {
	pcm := quantize(t, 48_000, -2, [][2]string{{"-1/1", "1/1"}, {"1/2", "-1/2"}, {"0/1", "32767/32768"}})
	result, err := Encode(context.Background(), pcm, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != ProfileV1 || result.InputQuantization != pcm16le.ProfileV1 ||
		result.InputSampleProfile != pcm.OutputSampleProfile || result.ArtifactHashProfile != ArtifactHashProfile ||
		result.SampleRate != 48_000 || result.FirstCompositionSample != -2 || result.FrameCount != 3 ||
		result.Channels != 2 || result.ChannelOrder != "FL,FR" || result.BitsPerSample != 16 ||
		result.DataBytes != 12 || result.ArtifactBytes != 56 || result.SourcePCMHash != pcm.PCMHash || result.Artifact == nil {
		t.Fatalf("result=%+v", result)
	}
	encoded, err := result.Artifact.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	assertHeader(t, encoded, 48_000, 12)
	pcmBytes, _ := pcm.PCM.Bytes()
	if !bytes.Equal(encoded[HeaderBytes:], pcmBytes) {
		t.Fatalf("payload=%x want=%x", encoded[HeaderBytes:], pcmBytes)
	}
	actualHash, err := result.Artifact.CanonicalHash()
	if err != nil || actualHash != result.ArtifactHash {
		t.Fatalf("hash=%s result=%s err=%v", actualHash, result.ArtifactHash, err)
	}
	decoded, err := wavdecode.Decode(context.Background(), encoded, wavdecode.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.EncodedContentHash != result.ArtifactHash || decoded.FrameCount != result.FrameCount ||
		decoded.SampleRate != 48_000 || decoded.DecoderChannelOrder != wavdecode.ChannelOrderStereoFLFR {
		t.Fatalf("roundtrip=%+v", decoded)
	}
	for frame := uint64(0); frame < result.FrameCount; frame++ {
		for channel := 0; channel < 2; channel++ {
			want, _ := pcm.PCM.Sample(frame, channel)
			got, sampleErr := decoded.PCM.Sample(frame, channel)
			if sampleErr != nil || got != want {
				t.Fatalf("sample(%d,%d)=%d want=%d err=%v", frame, channel, got, want, sampleErr)
			}
		}
	}
	encoded[0] = 'X'
	again, _ := result.Artifact.Bytes()
	if string(again[:4]) != "RIFF" {
		t.Fatal("artifact exposed mutable bytes")
	}
}

func TestEncodeEmptyPCMProducesCanonical44ByteWAV(t *testing.T) {
	pcm := quantize(t, 8_000, 7, nil)
	result, err := Encode(context.Background(), pcm, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.FrameCount != 0 || result.DataBytes != 0 || result.ArtifactBytes != HeaderBytes || result.Artifact.Size() != HeaderBytes {
		t.Fatalf("result=%+v", result)
	}
	encoded, _ := result.Artifact.Bytes()
	assertHeader(t, encoded, 8_000, 0)
	decoded, err := wavdecode.Decode(context.Background(), encoded, wavdecode.Limits{})
	if err != nil || decoded.FrameCount != 0 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestEncodeRejectsMutatedPCMClaimsLimitsRangeAndCancellation(t *testing.T) {
	base := quantize(t, 48_000, 0, [][2]string{{"0/1", "1/2"}})
	mutations := []struct {
		name string
		edit func(*pcm16le.Result)
		want error
	}{
		{name: "profile", edit: func(value *pcm16le.Result) { value.Profile = "vendor.v1" }, want: ErrInputInvalid},
		{name: "input domain", edit: func(value *pcm16le.Result) { value.InputSampleProfile = "vendor.v1" }, want: ErrInputInvalid},
		{name: "output domain", edit: func(value *pcm16le.Result) { value.OutputSampleProfile = "vendor.v1" }, want: ErrInputInvalid},
		{name: "channels", edit: func(value *pcm16le.Result) { value.Channels = 1 }, want: ErrInputInvalid},
		{name: "order", edit: func(value *pcm16le.Result) { value.ChannelOrder = "FR,FL" }, want: ErrInputInvalid},
		{name: "bytes per frame", edit: func(value *pcm16le.Result) { value.BytesPerFrame = 2 }, want: ErrInputInvalid},
		{name: "sample rate", edit: func(value *pcm16le.Result) { value.SampleRate = 0 }, want: ErrInputInvalid},
		{name: "frame count", edit: func(value *pcm16le.Result) { value.FrameCount++ }, want: ErrInputMismatch},
		{name: "hash", edit: func(value *pcm16le.Result) { value.PCMHash = "sha256:deadbeef" }, want: ErrInputMismatch},
		{name: "nil block", edit: func(value *pcm16le.Result) { value.PCM = nil }, want: ErrInputInvalid},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			result, err := Encode(context.Background(), input, DefaultLimits())
			if result.Artifact != nil || result.ArtifactHash != "" || !errors.Is(err, test.want) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, test.want)
			}
		})
	}

	overflow := base
	overflow.FirstCompositionSample = int64(^uint64(0) >> 1)
	if result, err := Encode(context.Background(), overflow, DefaultLimits()); result.Artifact != nil || !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("range overflow result=%+v err=%v", result, err)
	}
	highRate := base
	highRate.SampleRate = int64(^uint32(0))/pcm16le.BytesPerFrame + 1
	if result, err := Encode(context.Background(), highRate, DefaultLimits()); result.Artifact != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("rate overflow result=%+v err=%v", result, err)
	}
	limited := DefaultLimits()
	limited.MaxDataBytes = 3
	if result, err := Encode(context.Background(), base, limited); result.Artifact != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("data limit result=%+v err=%v", result, err)
	}
	if _, err := Encode(context.Background(), base, Limits{MaxDataBytes: 100, MaxArtifactBytes: 50}); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("invalid limits err=%v", err)
	}
	if result, err := Encode(nil, base, DefaultLimits()); result.Artifact != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Encode(ctx, base, DefaultLimits()); result.Artifact != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result=%+v err=%v", result, err)
	}
}

func TestArtifactZeroValueWritersAndConcurrentReaders(t *testing.T) {
	result, err := Encode(context.Background(), quantize(t, 44_100, 0, [][2]string{{"1/4", "-1/4"}}), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var zero *Artifact
	if _, err := zero.Bytes(); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("zero bytes err=%v", err)
	}
	if _, err := (&Artifact{}).WriteTo(io.Discard); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("forged artifact err=%v", err)
	}
	if _, err := result.Artifact.WriteTo(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer err=%v", err)
	}
	if _, err := result.Artifact.WriteTo(errorWriter{}); !errors.Is(err, errWriter) {
		t.Fatalf("error writer err=%v", err)
	}
	for _, test := range []struct {
		name   string
		writer io.Writer
		want   error
	}{
		{name: "negative count", writer: invalidCountWriter{count: -1}, want: io.ErrShortWrite},
		{name: "oversized count", writer: invalidCountWriter{count: int(result.Artifact.Size()) + 1}, want: io.ErrShortWrite},
		{name: "invalid count with error", writer: invalidCountWriter{count: -1, err: errWriter}, want: errWriter},
	} {
		t.Run(test.name, func(t *testing.T) {
			if written, err := result.Artifact.WriteTo(test.writer); written != 0 || !errors.Is(err, test.want) {
				t.Fatalf("written=%d err=%v want=%v", written, err, test.want)
			}
		})
	}
	wantBytes, err := result.Artifact.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	hostile := &mutatingRetainingWriter{}
	if written, err := result.Artifact.WriteTo(hostile); err != nil || written != int64(len(wantBytes)) {
		t.Fatalf("hostile write written=%d err=%v", written, err)
	}
	if len(hostile.retained) == 0 {
		t.Fatal("hostile writer did not retain buffer")
	}
	hostile.retained[0] ^= 0xff
	gotBytes, err := result.Artifact.Bytes()
	if err != nil || !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("writer mutated artifact: got=%x want=%x err=%v", gotBytes, wantBytes, err)
	}
	want := result.ArtifactHash
	const workers = 32
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			switch index % 3 {
			case 0:
				got, hashErr := result.Artifact.CanonicalHash()
				if hashErr != nil {
					errorsOut <- hashErr
				} else if got != want {
					errorsOut <- errors.New("hash mismatch")
				}
			case 1:
				got, bytesErr := result.Artifact.Bytes()
				if bytesErr != nil {
					errorsOut <- bytesErr
				} else if !bytes.Equal(got, wantBytes) {
					errorsOut <- errors.New("bytes mismatch")
				}
			case 2:
				written, writeErr := result.Artifact.WriteTo(io.Discard)
				if writeErr != nil {
					errorsOut <- writeErr
				} else if written != int64(len(wantBytes)) {
					errorsOut <- errors.New("write count mismatch")
				}
			}
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func TestArtifactWriteToPreservesMultiChunkBytes(t *testing.T) {
	values := make([][2]string, 20_000) // 80 KiB crosses the 64 KiB defensive chunk boundary.
	for index := range values {
		values[index] = [2]string{"1/4", "-1/4"}
	}
	result, err := Encode(context.Background(), quantize(t, 48_000, 0, values), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want, err := result.Artifact.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	written, err := result.Artifact.WriteTo(&output)
	if err != nil || written != int64(len(want)) || !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("written=%d size=%d equal=%v err=%v", written, len(want), bytes.Equal(output.Bytes(), want), err)
	}
}

func TestContextWritersCancelWithoutPublishing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelWriter{cancel: cancel}
	hashWriter := &contextHashWriter{ctx: ctx, writer: writer}
	if _, err := hashWriter.Write(make([]byte, 128<<10)); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("hash cancel err=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	sliceWriter := &contextSliceWriter{ctx: ctx, destination: make([]byte, 128<<10)}
	cancel()
	if _, err := sliceWriter.Write(make([]byte, 128<<10)); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("copy cancel err=%v", err)
	}
}

func TestEncodeCancellationDuringInputHashAndPayloadReturnsZeroResult(t *testing.T) {
	values := make([][2]string, 20_000) // 80 KiB, so every byte stage crosses a cancellation boundary.
	for index := range values {
		values[index] = [2]string{"1/4", "-1/4"}
	}
	pcm := quantize(t, 48_000, 0, values)
	for _, cancelAfter := range []int{3, 6, 8} {
		ctx := &stepCancelContext{cancelAfter: cancelAfter}
		result, err := Encode(ctx, pcm, DefaultLimits())
		if result != (Result{}) || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelAfter=%d result=%+v err=%v", cancelAfter, result, err)
		}
	}
}

func TestProfileCanonicalFixture(t *testing.T) {
	want, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("%v\ncanonical=%s", err, want)
	}
	var fixture ProfileDescriptor
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	got, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture, Descriptor()) || !bytes.Equal(bytes.TrimSpace(raw), got) || !bytes.Equal(got, want) {
		t.Fatalf("fixture differs\nwant=%s\ngot=%s", want, bytes.TrimSpace(raw))
	}
}

func FuzzEncodeNeverPanicsOrPublishesPartial(f *testing.F) {
	f.Add(int64(48_000), int64(0), int64(1), int64(2), int64(-1), int64(2))
	f.Fuzz(func(t *testing.T, sampleRate, first, ln, ld, rn, rd int64) {
		if sampleRate <= 0 || sampleRate > 1_000_000 {
			sampleRate = 48_000
		}
		if ld == 0 {
			ld = 1
		}
		if rd == 0 {
			rd = 1
		}
		left, leftErr := audiograph.NewRatio(ln, ld)
		right, rightErr := audiograph.NewRatio(rn, rd)
		if leftErr != nil || rightErr != nil {
			return
		}
		pcm, quantizeErr := pcm16le.Quantize(context.Background(), sampleRate, first,
			[]audiograph.StereoSample{{Left: left, Right: right}}, pcm16le.DefaultLimits())
		if quantizeErr != nil {
			return
		}
		result, err := Encode(context.Background(), pcm, DefaultLimits())
		if err != nil {
			if result.Artifact != nil || result.ArtifactHash != "" {
				t.Fatalf("partial result=%+v err=%v", result, err)
			}
			return
		}
		encoded, _ := result.Artifact.Bytes()
		decoded, decodeErr := wavdecode.Decode(context.Background(), encoded, wavdecode.Limits{})
		if decodeErr != nil || decoded.FrameCount != 1 || decoded.EncodedContentHash != result.ArtifactHash {
			t.Fatalf("decoded=%+v err=%v result=%+v", decoded, decodeErr, result)
		}
	})
}

func quantize(t testing.TB, sampleRate, first int64, values [][2]string) pcm16le.Result {
	t.Helper()
	samples := make([]audiograph.StereoSample, len(values))
	for index, value := range values {
		left, err := audiograph.ParseExact(value[0])
		if err != nil {
			t.Fatal(err)
		}
		right, err := audiograph.ParseExact(value[1])
		if err != nil {
			t.Fatal(err)
		}
		samples[index] = audiograph.StereoSample{Left: left, Right: right}
	}
	result, err := pcm16le.Quantize(context.Background(), sampleRate, first, samples, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertHeader(t testing.TB, encoded []byte, sampleRate, dataBytes uint32) {
	t.Helper()
	if len(encoded) != HeaderBytes+int(dataBytes) || string(encoded[0:4]) != "RIFF" ||
		binary.LittleEndian.Uint32(encoded[4:8]) != 36+dataBytes || string(encoded[8:12]) != "WAVE" ||
		string(encoded[12:16]) != "fmt " || binary.LittleEndian.Uint32(encoded[16:20]) != 16 ||
		binary.LittleEndian.Uint16(encoded[20:22]) != 1 || binary.LittleEndian.Uint16(encoded[22:24]) != 2 ||
		binary.LittleEndian.Uint32(encoded[24:28]) != sampleRate || binary.LittleEndian.Uint32(encoded[28:32]) != sampleRate*4 ||
		binary.LittleEndian.Uint16(encoded[32:34]) != 4 || binary.LittleEndian.Uint16(encoded[34:36]) != 16 ||
		string(encoded[36:40]) != "data" || binary.LittleEndian.Uint32(encoded[40:44]) != dataBytes {
		t.Fatalf("invalid header: %x", encoded)
	}
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) / 2, nil }

var errWriter = errors.New("writer failed")

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errWriter }

type invalidCountWriter struct {
	count int
	err   error
}

func (writer invalidCountWriter) Write([]byte) (int, error) { return writer.count, writer.err }

type mutatingRetainingWriter struct {
	retained []byte
}

func (writer *mutatingRetainingWriter) Write(value []byte) (int, error) {
	if len(value) > 0 {
		value[0] ^= 0xff
	}
	writer.retained = value
	return len(value), nil
}

type cancelWriter struct {
	cancel context.CancelFunc
	called bool
}

type stepCancelContext struct {
	calls       int
	cancelAfter int
}

func (*stepCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*stepCancelContext) Done() <-chan struct{}       { return nil }
func (*stepCancelContext) Value(any) any               { return nil }
func (ctx *stepCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAfter {
		return context.Canceled
	}
	return nil
}

func (writer *cancelWriter) Write(value []byte) (int, error) {
	if !writer.called {
		writer.called = true
		writer.cancel()
	}
	return len(value), nil
}
