package wavdecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
)

func TestDecodeStereoSuccessBindingsAndOwnership(t *testing.T) {
	samples := []int16{-32768, 32767, 0, -1, 1, 12345}
	encoded := strictWAV(t, 48_000, 2, samples)
	before := append([]byte(nil), encoded...)
	result, err := Decode(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.SampleRate != 48_000 || result.Channels != 2 || result.ChannelLayout != "stereo" ||
		result.DecoderChannelOrder != ChannelOrderStereoFLFR ||
		result.FrameCount != 3 || result.SampleCount != 6 || result.DataBytes != 12 || result.PCM == nil ||
		result.DecodeProfile != ProfileV1 || result.SampleDomain != SampleDomainProfileV1 ||
		result.ContentHashProfile != ContentHashProfileV1 || result.SampleHashProfile != SampleHashProfileV1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	digest := sha256.Sum256(before)
	if want := "sha256:" + hex.EncodeToString(digest[:]); result.EncodedContentHash != want {
		t.Fatalf("encoded hash = %q, want %q", result.EncodedContentHash, want)
	}
	if hash, hashErr := result.PCM.CanonicalHash(); hashErr != nil || hash != result.SampleHash {
		t.Fatalf("sample hash = %q, %v; result = %q", hash, hashErr, result.SampleHash)
	}
	if result.PCM.SampleRate() != 48_000 || result.PCM.Channels() != 2 ||
		result.PCM.ChannelLayout() != "stereo" || result.PCM.FrameCount() != 3 {
		t.Fatalf("unexpected PCM metadata")
	}
	order := result.PCM.ChannelOrder()
	if len(order) != 2 || order[0] != "FL" || order[1] != "FR" {
		t.Fatalf("channel order = %v", order)
	}
	order[0] = "mutated"
	if got := result.PCM.ChannelOrder(); got[0] != "FL" {
		t.Fatalf("channel order storage aliased: %v", got)
	}
	for frame := uint64(0); frame < result.FrameCount; frame++ {
		for channel := 0; channel < 2; channel++ {
			got, sampleErr := result.PCM.Sample(frame, channel)
			want := samples[int(frame)*2+channel]
			if sampleErr != nil || got != want {
				t.Fatalf("sample(%d,%d) = %d, %v; want %d", frame, channel, got, sampleErr, want)
			}
		}
	}
	for index := range encoded {
		encoded[index] ^= 0xff
	}
	if got, sampleErr := result.PCM.Sample(0, 0); sampleErr != nil || got != -32768 {
		t.Fatalf("owned sample after input mutation = %d, %v", got, sampleErr)
	}
	if hash, hashErr := result.PCM.CanonicalHash(); hashErr != nil || hash != result.SampleHash {
		t.Fatalf("hash after input mutation = %q, %v", hash, hashErr)
	}
}

func TestDecodeMonoAndEmptyData(t *testing.T) {
	mono, err := Decode(context.Background(), strictWAV(t, 44_100, 1, []int16{-1, 0, 1}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if mono.ChannelLayout != "mono" || mono.DecoderChannelOrder != ChannelOrderMonoFC || mono.FrameCount != 3 || mono.SampleCount != 3 ||
		!equalStrings(mono.PCM.ChannelOrder(), []string{"FC"}) {
		t.Fatalf("mono result = %+v, order %v", mono, mono.PCM.ChannelOrder())
	}
	empty, err := Decode(context.Background(), strictWAV(t, 8_000, 1, nil), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.FrameCount != 0 || empty.SampleCount != 0 || empty.DataBytes != 0 || empty.PCM == nil {
		t.Fatalf("empty result = %+v", empty)
	}
	const emptySHA256 = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if empty.SampleHash != emptySHA256 {
		t.Fatalf("empty sample hash = %q", empty.SampleHash)
	}
}

func TestDecodeExhaustiveSignedWordMapping(t *testing.T) {
	samples := make([]int16, 1<<16)
	for encodedWord := 0; encodedWord <= 0xffff; encodedWord++ {
		samples[encodedWord] = int16(uint16(encodedWord))
	}
	result, err := Decode(context.Background(), strictWAV(t, 48_000, 1, samples), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.FrameCount != 1<<16 || result.SampleCount != 1<<16 {
		t.Fatalf("counts = %d frames, %d samples", result.FrameCount, result.SampleCount)
	}
	for frame, want := range samples {
		got, sampleErr := result.PCM.Sample(uint64(frame), 0)
		if sampleErr != nil || got != want {
			t.Fatalf("word 0x%04x decoded as %d, %v; want %d", frame, got, sampleErr, want)
		}
	}
}

func TestDecodeRejectsOutOfProfileAndMalformedWAVE(t *testing.T) {
	baseMono := strictWAV(t, 48_000, 1, []int16{1, -2})
	baseStereo := strictWAV(t, 48_000, 2, []int16{1, -2})
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		is     error
	}{
		{name: "RIFX", mutate: func(value []byte) []byte { copy(value[0:4], "RIFX"); return value }, is: ErrInputInvalid},
		{name: "RF64", mutate: func(value []byte) []byte { copy(value[0:4], "RF64"); return value }, is: ErrInputInvalid},
		{name: "bad form", mutate: func(value []byte) []byte { copy(value[8:12], "AVI "); return value }, is: ErrInputInvalid},
		{name: "short RIFF declaration", mutate: func(value []byte) []byte {
			binary.LittleEndian.PutUint32(value[4:8], uint32(len(value)-9))
			return value
		}, is: ErrChunkInvalid},
		{name: "first chunk junk", mutate: func(value []byte) []byte { copy(value[12:16], "JUNK"); return value }, is: ErrProfileMismatch},
		{name: "extended fmt", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint32(value[16:20], 18); return value }, is: ErrProfileMismatch},
		{name: "float", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[20:22], 3); return value }, is: ErrProfileMismatch},
		{name: "extensible", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[20:22], 0xfffe); return value }, is: ErrProfileMismatch},
		{name: "three channels", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[22:24], 3); return value }, is: ErrProfileMismatch},
		{name: "zero sample rate", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint32(value[24:28], 0); return value }, is: ErrProfileMismatch},
		{name: "bad byte rate", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint32(value[28:32], 1); return value }, is: ErrProfileMismatch},
		{name: "bad block align", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[32:34], 1); return value }, is: ErrProfileMismatch},
		{name: "eight bit", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[34:36], 8); return value }, is: ErrProfileMismatch},
		{name: "data replaced by fact", mutate: func(value []byte) []byte { copy(value[36:40], "fact"); return value }, is: ErrProfileMismatch},
		{name: "data size past input", mutate: func(value []byte) []byte {
			binary.LittleEndian.PutUint32(value[40:44], uint32(len(value)))
			return value
		}, is: ErrChunkInvalid},
		{name: "nonzero RIFF padding", mutate: func(value []byte) []byte {
			binary.LittleEndian.PutUint32(value[40:44], 1)
			value[45] = 1
			return value
		}, is: ErrChunkInvalid},
		{name: "trailing bytes", mutate: func(value []byte) []byte {
			value = append(value, 0)
			binary.LittleEndian.PutUint32(value[4:8], uint32(len(value)-8))
			return value
		}, is: ErrChunkInvalid},
		{name: "extra chunk", mutate: func(value []byte) []byte {
			value = append(value, []byte{'J', 'U', 'N', 'K', 0, 0, 0, 0}...)
			binary.LittleEndian.PutUint32(value[4:8], uint32(len(value)-8))
			return value
		}, is: ErrProfileMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := append([]byte(nil), baseMono...)
			result, err := Decode(context.Background(), test.mutate(encoded), Limits{})
			if !errors.Is(err, test.is) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.is)
			}
			if result != (Result{}) {
				t.Fatalf("failure published partial result: %+v", result)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code == "" || typed.Path == "" {
				t.Fatalf("missing stable error evidence: %v", err)
			}
		})
	}

	misaligned := append([]byte(nil), baseStereo[:46]...)
	binary.LittleEndian.PutUint32(misaligned[4:8], uint32(len(misaligned)-8))
	binary.LittleEndian.PutUint32(misaligned[40:44], 2)
	if result, err := Decode(context.Background(), misaligned, Limits{}); !errors.Is(err, ErrProfileMismatch) || result != (Result{}) {
		t.Fatalf("misaligned result/error = %+v, %v", result, err)
	}

	for length := 0; length < minimumEncodedBytes; length++ {
		if result, err := Decode(context.Background(), make([]byte, length), Limits{}); !errors.Is(err, ErrInputInvalid) || result != (Result{}) {
			t.Fatalf("length %d result/error = %+v, %v", length, result, err)
		}
	}
}

func TestDecodeLimitsAndCancellationNeverPublish(t *testing.T) {
	encoded := strictWAV(t, 48_000, 2, []int16{1, 2, 3, 4, 5, 6})
	tests := []struct {
		name   string
		limits Limits
	}{
		{name: "encoded", limits: Limits{MaxEncodedBytes: uint64(len(encoded) - 1)}},
		{name: "sample rate", limits: Limits{MaxSampleRate: 47_999}},
		{name: "frame count", limits: Limits{MaxFrames: 2}},
		{name: "working bytes", limits: Limits{MaxWorkingBytes: 11}},
		{name: "hard sample rate config", limits: Limits{MaxSampleRate: HardMaxSampleRate + 1}},
		{name: "hard frame config", limits: Limits{MaxFrames: HardMaxFrames + 1}},
		{name: "hard encoded config", limits: Limits{MaxEncodedBytes: HardMaxEncodedBytes + 1}},
		{name: "hard working config", limits: Limits{MaxWorkingBytes: HardMaxWorkingBytes + 1}},
		{name: "too-small encoded config", limits: Limits{MaxEncodedBytes: minimumEncodedBytes - 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Decode(context.Background(), encoded, test.limits)
			if !errors.Is(err, ErrResourceLimit) || result != (Result{}) {
				t.Fatalf("result/error = %+v, %v", result, err)
			}
		})
	}
	if _, err := Decode(context.Background(), encoded, Limits{MaxWorkingBytes: 12}); err != nil {
		t.Fatalf("exact working budget: %v", err)
	}
	if result, err := Decode(nil, encoded, Limits{}); !errors.Is(err, ErrContextInvalid) || result != (Result{}) {
		t.Fatalf("nil context result/error = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Decode(ctx, encoded, Limits{}); !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) || result != (Result{}) {
		t.Fatalf("canceled result/error = %+v, %v", result, err)
	}
}

func TestCancellationHelpers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if hash, err := hashEncoded(ctx, make([]byte, contextStrideBytes*2)); hash != "" || !errors.Is(err, ErrCanceled) {
		t.Fatalf("encoded hash/error = %q, %v", hash, err)
	}
	if err := decodeSamples(ctx, make([]byte, contextStrideBytes*2), make([]int16, contextStrideBytes)); !errors.Is(err, ErrCanceled) {
		t.Fatalf("decode error = %v", err)
	}
	if hash, err := hashSamples(ctx, make([]int16, contextStrideBytes)); hash != "" || !errors.Is(err, ErrCanceled) {
		t.Fatalf("sample hash/error = %q, %v", hash, err)
	}
}

func TestPCMCanonicalSerializationAndBounds(t *testing.T) {
	result, err := Decode(context.Background(), strictWAV(t, 8_000, 1, []int16{-32768, -1, 0, 1, 32767}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x80, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x01, 0x7f, 0xff}
	var actual bytes.Buffer
	if err := result.PCM.WriteCanonical(&actual); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual.Bytes(), want) {
		t.Fatalf("canonical = %x, want %x", actual.Bytes(), want)
	}
	for _, coordinate := range []struct {
		frame   uint64
		channel int
	}{{5, 0}, {0, -1}, {0, 1}} {
		if sample, sampleErr := result.PCM.Sample(coordinate.frame, coordinate.channel); sample != 0 || !errors.Is(sampleErr, ErrBounds) {
			t.Fatalf("out-of-bounds sample = %d, %v", sample, sampleErr)
		}
	}
	if err := result.PCM.WriteCanonical(nil); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("nil writer error = %v", err)
	}
	if err := result.PCM.WriteCanonical(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v", err)
	}
	var nilPCM *PCM16
	if nilPCM.SampleRate() != 0 || nilPCM.Channels() != 0 || nilPCM.FrameCount() != 0 ||
		nilPCM.ChannelLayout() != "" || nilPCM.ChannelOrder() != nil {
		t.Fatal("nil PCM accessors did not fail closed")
	}
	if _, err := nilPCM.Sample(0, 0); !errors.Is(err, ErrBounds) {
		t.Fatalf("nil sample error = %v", err)
	}
	if _, err := nilPCM.CanonicalHash(); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("nil hash error = %v", err)
	}
}

func TestDecodeConcurrentDeterminism(t *testing.T) {
	encoded := strictWAV(t, 96_000, 2, []int16{-32768, 32767, -1, 1, 123, -456})
	want, err := Decode(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, decodeErr := Decode(context.Background(), encoded, Limits{})
			if decodeErr != nil {
				errorsChannel <- decodeErr
				return
			}
			if got.EncodedContentHash != want.EncodedContentHash || got.SampleHash != want.SampleHash || got.FrameCount != want.FrameCount {
				errorsChannel <- errors.New("concurrent decode differs")
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func FuzzDecodeNeverPanicsOrPublishesOnError(f *testing.F) {
	f.Add([]byte{})
	f.Add(strictWAVForSamples(48_000, 1, []int16{-32768, 0, 32767}))
	f.Add(strictWAVForSamples(48_000, 2, []int16{-1, 1}))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 1<<20 {
			t.Skip()
		}
		result, err := Decode(context.Background(), encoded, Limits{
			MaxSampleRate: 192_000, MaxFrames: 1 << 18,
			MaxEncodedBytes: 1 << 20, MaxWorkingBytes: 1 << 20,
		})
		if err != nil && result != (Result{}) {
			t.Fatalf("error published partial result: %+v, %v", result, err)
		}
		if err == nil {
			if result.PCM == nil || result.EncodedContentHash == "" || result.SampleHash == "" ||
				result.PCM.FrameCount() != result.FrameCount || result.PCM.Channels() != result.Channels {
				t.Fatalf("success omitted evidence: %+v", result)
			}
		}
	})
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return len(value) - 1, nil
}

func strictWAV(t *testing.T, sampleRate uint32, channels uint16, samples []int16) []byte {
	t.Helper()
	if channels == 0 || len(samples)%int(channels) != 0 {
		t.Fatalf("invalid test samples")
	}
	return strictWAVForSamples(sampleRate, channels, samples)
}

func strictWAVForSamples(sampleRate uint32, channels uint16, samples []int16) []byte {
	dataBytes := len(samples) * 2
	result := make([]byte, minimumEncodedBytes+dataBytes)
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

func equalStrings(left, right []string) bool {
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
