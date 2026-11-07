package pcm16le

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
)

func TestQuantizeExactClippingScalingTiesAndBytes(t *testing.T) {
	samples := []audiograph.StereoSample{
		{Left: exact(t, "-2/1"), Right: exact(t, "2/1")},
		{Left: exact(t, "-1/1"), Right: exact(t, "1/1")},
		{Left: exact(t, "-1/2"), Right: exact(t, "1/2")},
		{Left: exact(t, "-1/65536"), Right: exact(t, "1/65536")},
		{Left: exact(t, "-3/65536"), Right: exact(t, "3/65536")},
		{Left: exact(t, "0/1"), Right: exact(t, "32767/32768")},
	}
	result, err := Quantize(context.Background(), 48_000, -3, samples, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != ProfileV1 || result.InputSampleProfile != audiograph.SampleDomainProfileV1 ||
		result.OutputSampleProfile != renderapi.DecodedAudioProfilePCM16LEV1 ||
		result.SampleRate != 48_000 || result.FirstCompositionSample != -3 ||
		result.FrameCount != uint64(len(samples)) || result.Channels != 2 || result.ChannelOrder != "FL,FR" ||
		result.BytesPerFrame != 4 || result.PCM == nil {
		t.Fatalf("result = %+v", result)
	}
	want := [][2]int16{
		{-32768, 32767}, {-32768, 32767}, {-16384, 16384}, {0, 0}, {-2, 2}, {0, 32767},
	}
	for frame := range want {
		for channel := 0; channel < 2; channel++ {
			got, sampleErr := result.PCM.Sample(uint64(frame), channel)
			if sampleErr != nil || got != want[frame][channel] {
				t.Fatalf("sample[%d][%d]=%d want=%d err=%v", frame, channel, got, want[frame][channel], sampleErr)
			}
		}
	}
	encoded, err := result.PCM.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(encoded); got != "0080ff7f0080ff7f00c0004000000000feff02000000ff7f" {
		t.Fatalf("bytes = %s", got)
	}
	hash, err := result.PCM.CanonicalHash()
	if err != nil || hash != result.PCMHash {
		t.Fatalf("hash=%s result=%s err=%v", hash, result.PCMHash, err)
	}
	encoded[0] = 0
	first, _ := result.PCM.Sample(0, 0)
	if first != -32768 {
		t.Fatalf("Bytes exposed mutable alias: %d", first)
	}
	var output bytes.Buffer
	if err := result.PCM.WriteCanonical(&output); err != nil || output.Len() != len(samples)*BytesPerFrame {
		t.Fatalf("write len=%d err=%v", output.Len(), err)
	}
}

func TestQuantizeRejectsInvalidInputsLimitsAndCancellationWithoutResult(t *testing.T) {
	valid := []audiograph.StereoSample{{Left: audiograph.Zero(), Right: audiograph.One()}}
	tests := []struct {
		name       string
		ctx        context.Context
		sampleRate int64
		samples    []audiograph.StereoSample
		limits     Limits
		want       error
	}{
		{name: "nil context", sampleRate: 1, samples: valid, limits: DefaultLimits(), want: ErrContextInvalid},
		{name: "sample rate", ctx: context.Background(), samples: valid, limits: DefaultLimits(), want: ErrInputInvalid},
		{name: "invalid exact", ctx: context.Background(), sampleRate: 1, samples: []audiograph.StereoSample{{Right: audiograph.Zero()}}, limits: DefaultLimits(), want: ErrInputInvalid},
		{name: "frame limit", ctx: context.Background(), sampleRate: 1, samples: valid, limits: Limits{MaxFrames: 1, MaxOutputBytes: 3, MaxExactBits: 8}, want: ErrInputInvalid},
		{name: "exact bits", ctx: context.Background(), sampleRate: 1, samples: []audiograph.StereoSample{{Left: exact(t, "65537/65536"), Right: audiograph.Zero()}}, limits: Limits{MaxFrames: 1, MaxOutputBytes: 4, MaxExactBits: 8}, want: ErrResourceLimit},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests = append(tests, struct {
		name       string
		ctx        context.Context
		sampleRate int64
		samples    []audiograph.StereoSample
		limits     Limits
		want       error
	}{name: "canceled", ctx: canceled, sampleRate: 1, samples: valid, limits: DefaultLimits(), want: ErrCanceled})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Quantize(test.ctx, test.sampleRate, 0, test.samples, test.limits)
			if result.PCM != nil || result.PCMHash != "" || !errors.Is(err, test.want) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, test.want)
			}
		})
	}

	limited := DefaultLimits()
	limited.MaxFrames = 1
	if result, err := Quantize(context.Background(), 1, 0, append(valid, valid...), limited); result.PCM != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("frame limit result=%+v err=%v", result, err)
	}
	if result, err := Quantize(context.Background(), 1, math.MaxInt64, valid, DefaultLimits()); result.PCM != nil || !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("overflow range result=%+v err=%v", result, err)
	}
}

func TestEmptyBlockAndAccessFailures(t *testing.T) {
	result, err := Quantize(context.Background(), 44_100, 7, nil, DefaultLimits())
	if err != nil || result.PCM == nil || result.FrameCount != 0 || result.PCM.FrameCount() != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := result.PCM.Sample(0, 0); !errors.Is(err, ErrBounds) {
		t.Fatalf("bounds error=%v", err)
	}
	var nilBlock *Block
	if _, err := nilBlock.Bytes(); !errors.Is(err, ErrOutputInvalid) {
		t.Fatalf("nil bytes error=%v", err)
	}
	if err := result.PCM.WriteCanonical(nil); !errors.Is(err, ErrOutputInvalid) {
		t.Fatalf("nil writer error=%v", err)
	}
	if result.PCMHash != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty hash=%s", result.PCMHash)
	}
	zero := &Block{}
	if _, err := zero.Bytes(); !errors.Is(err, ErrOutputInvalid) {
		t.Fatalf("zero Block accepted: %v", err)
	}
}

func TestDescriptorIsDefensiveValue(t *testing.T) {
	first := Descriptor()
	second := Descriptor()
	if !reflect.DeepEqual(first, second) || first.Profile != ProfileV1 || first.DefaultLimits != DefaultLimits() {
		t.Fatalf("descriptor mismatch: %+v %+v", first, second)
	}
}

func TestProfileCanonicalFixture(t *testing.T) {
	want, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("%v\ncanonical descriptor=%s", err, want)
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

type vectorCase struct {
	Name           string      `json:"name"`
	Inputs         [][2]string `json:"inputs"`
	Expected       [][2]int16  `json:"expected"`
	ExpectedHex    string      `json:"expected_hex"`
	ExpectedSHA256 string      `json:"expected_sha256"`
}

type vectorsFixture struct {
	Profile             string       `json:"profile"`
	InputSampleProfile  string       `json:"input_sample_profile"`
	OutputSampleProfile string       `json:"output_sample_profile"`
	SampleRate          int64        `json:"sample_rate"`
	Cases               []vectorCase `json:"cases"`
}

func TestCanonicalQuantizationVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/quantize-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture vectorsFixture
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) || fixture.Profile != ProfileV1 ||
		fixture.InputSampleProfile != audiograph.SampleDomainProfileV1 ||
		fixture.OutputSampleProfile != renderapi.DecodedAudioProfilePCM16LEV1 ||
		fixture.SampleRate <= 0 || len(fixture.Cases) == 0 {
		t.Fatalf("invalid/noncanonical vectors\ncanonical=%s", canonical)
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			if vector.Name == "" || len(vector.Expected) != len(vector.Inputs) ||
				len(vector.ExpectedHex) != len(vector.Inputs)*BytesPerFrame*2 {
				t.Fatalf("invalid vector shape: %+v", vector)
			}
			samples := make([]audiograph.StereoSample, len(vector.Inputs))
			for index, input := range vector.Inputs {
				samples[index] = audiograph.StereoSample{Left: exact(t, input[0]), Right: exact(t, input[1])}
			}
			result, err := Quantize(context.Background(), fixture.SampleRate, 0, samples, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := result.PCM.Bytes()
			if hex.EncodeToString(encoded) != vector.ExpectedHex || result.PCMHash != vector.ExpectedSHA256 {
				t.Fatalf("hex=%s hash=%s", hex.EncodeToString(encoded), result.PCMHash)
			}
			for frame, expected := range vector.Expected {
				for channel := 0; channel < Channels; channel++ {
					actual, sampleErr := result.PCM.Sample(uint64(frame), channel)
					if sampleErr != nil || actual != expected[channel] {
						t.Fatalf("frame=%d channel=%d actual=%d want=%d err=%v", frame, channel, actual, expected[channel], sampleErr)
					}
				}
			}
		})
	}
	if _, err := json.Marshal(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestRoundRatioTiesEvenAgainstBigRatOracle(t *testing.T) {
	for numerator := int64(-1000); numerator <= 1000; numerator++ {
		for denominator := int64(1); denominator <= 97; denominator++ {
			got := roundRatioTiesEven(big.NewInt(numerator), big.NewInt(denominator))
			want := oracleRound(big.NewRat(numerator, denominator))
			if got.Cmp(want) != 0 {
				t.Fatalf("%d/%d => %s want %s", numerator, denominator, got, want)
			}
		}
	}
}

func TestQuantizerAgainstIndependentFloorDistanceOracle(t *testing.T) {
	var quantizer exactQuantizer
	for denominator := int64(1); denominator <= 257; denominator++ {
		for numerator := int64(-2 * denominator); numerator <= 2*denominator; numerator++ {
			value, err := audiograph.NewRatio(numerator, denominator)
			if err != nil {
				t.Fatal(err)
			}
			got, err := quantizer.quantize(value, DefaultMaxExactBits)
			if err != nil {
				t.Fatalf("%d/%d: %v", numerator, denominator, err)
			}
			want := oracleQuantize(value)
			if got != want {
				t.Fatalf("%d/%d => %d want %d", numerator, denominator, got, want)
			}
		}
	}
	largeNumerator := new(big.Int).Lsh(big.NewInt(1), 2047)
	largeDenominator := new(big.Int).Sub(new(big.Int).Set(largeNumerator), big.NewInt(1))
	large, err := audiograph.NewExact(largeNumerator, largeDenominator)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := quantizer.quantize(large, 4096); err != nil || got != math.MaxInt16 {
		t.Fatalf("large rational got=%d err=%v", got, err)
	}
}

func TestCancellationDuringConversionAndHashReturnsNoPartialResult(t *testing.T) {
	samples := make([]audiograph.StereoSample, 2048)
	for index := range samples {
		samples[index] = audiograph.StereoSample{Left: audiograph.One(), Right: audiograph.Zero()}
	}
	for _, cancelAt := range []int64{4, 6} {
		ctx := &cancelAfterContext{cancelAt: cancelAt}
		result, err := Quantize(ctx, 48_000, 0, samples, DefaultLimits())
		if result != (Result{}) || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelAt=%d result=%+v err=%v calls=%d", cancelAt, result, err, ctx.calls.Load())
		}
	}
}

func TestBlockConcurrentReadsAndWriterFailures(t *testing.T) {
	samples := []audiograph.StereoSample{{Left: exact(t, "-1/3"), Right: exact(t, "2/3")}}
	result, err := Quantize(context.Background(), 48_000, 0, samples, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := result.PCM.Bytes(); err != nil {
				errorsOut <- err
				return
			}
			if hash, err := result.PCM.CanonicalHash(); err != nil || hash != result.PCMHash {
				errorsOut <- errors.New("concurrent canonical hash mismatch")
				return
			}
			if _, err := result.PCM.Sample(0, 1); err != nil {
				errorsOut <- err
			}
		}()
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
	if err := result.PCM.WriteCanonical(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error=%v", err)
	}
	sentinel := errors.New("writer stopped")
	if err := result.PCM.WriteCanonical(errorWriter{err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("writer error=%v", err)
	}
	before, _ := result.PCM.CanonicalHash()
	mutating := &mutatingRetainingWriter{}
	if err := result.PCM.WriteCanonical(mutating); err != nil {
		t.Fatal(err)
	}
	for _, retained := range mutating.retained {
		for index := range retained {
			retained[index] ^= 0xff
		}
	}
	after, _ := result.PCM.CanonicalHash()
	if after != before || after != result.PCMHash {
		t.Fatalf("writer received backing alias: before=%s after=%s", before, after)
	}
	if err := result.PCM.WriteCanonical(invalidCountWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("invalid writer count error=%v", err)
	}
}

func FuzzQuantizeNeverPanicsOrPublishesPartial(f *testing.F) {
	f.Add(int64(1), int64(2), int64(-3), int64(4))
	f.Add(int64(32767), int64(32768), int64(-32768), int64(32768))
	f.Fuzz(func(t *testing.T, ln, ld, rn, rd int64) {
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
		result, err := Quantize(context.Background(), 48_000, 0, []audiograph.StereoSample{{Left: left, Right: right}}, DefaultLimits())
		if err != nil {
			if result.PCM != nil || result.PCMHash != "" {
				t.Fatalf("partial result=%+v err=%v", result, err)
			}
			return
		}
		if result.PCM == nil || result.FrameCount != 1 {
			t.Fatalf("success result=%+v", result)
		}
		var quantizer exactQuantizer
		leftPCM, _ := quantizer.quantize(left, DefaultMaxExactBits)
		rightPCM, _ := quantizer.quantize(right, DefaultMaxExactBits)
		if leftPCM != oracleQuantize(left) || rightPCM != oracleQuantize(right) {
			t.Fatalf("quantizer/oracle mismatch: %d/%d", leftPCM, rightPCM)
		}
	})
}

func exact(t testing.TB, value string) audiograph.Exact {
	t.Helper()
	result, err := audiograph.ParseExact(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func oracleRound(value *big.Rat) *big.Int {
	sign := value.Sign()
	absNumerator := new(big.Int).Abs(new(big.Int).Set(value.Num()))
	denominator := value.Denom()
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(absNumerator, denominator, remainder)
	comparison := new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(denominator)
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) != 0) {
		quotient.Add(quotient, big.NewInt(1))
	}
	if sign < 0 {
		quotient.Neg(quotient)
	}
	return quotient
}

func oracleQuantize(value audiograph.Exact) int16 {
	rational := new(big.Rat).SetFrac(value.Numerator(), value.Denominator())
	minusOne, plusOne := big.NewRat(-1, 1), big.NewRat(1, 1)
	if rational.Cmp(minusOne) <= 0 {
		return math.MinInt16
	}
	if rational.Cmp(plusOne) >= 0 {
		return math.MaxInt16
	}
	scaled := new(big.Rat).Mul(rational, big.NewRat(32768, 1))
	floor := new(big.Int).Div(new(big.Int).Set(scaled.Num()), scaled.Denom())
	ceil := new(big.Int).Add(new(big.Int).Set(floor), big.NewInt(1))
	floorRat := new(big.Rat).SetInt(floor)
	ceilRat := new(big.Rat).SetInt(ceil)
	distanceToFloor := new(big.Rat).Sub(new(big.Rat).Set(scaled), floorRat)
	distanceToCeil := new(big.Rat).Sub(ceilRat, scaled)
	selected := floor
	comparison := distanceToFloor.Cmp(distanceToCeil)
	if comparison > 0 || (comparison == 0 && floor.Bit(0) != 0) {
		selected = ceil
	}
	if selected.Cmp(big.NewInt(math.MinInt16)) < 0 {
		return math.MinInt16
	}
	if selected.Cmp(big.NewInt(math.MaxInt16)) > 0 {
		return math.MaxInt16
	}
	return int16(selected.Int64())
}

type cancelAfterContext struct {
	cancelAt int64
	calls    atomic.Int64
}

func (value *cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *cancelAfterContext) Done() <-chan struct{}       { return nil }
func (value *cancelAfterContext) Value(any) any               { return nil }
func (value *cancelAfterContext) Err() error {
	if value.calls.Add(1) >= value.cancelAt {
		return context.Canceled
	}
	return nil
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return len(value) - 1, nil
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

type mutatingRetainingWriter struct {
	retained [][]byte
}

func (writer *mutatingRetainingWriter) Write(value []byte) (int, error) {
	writer.retained = append(writer.retained, value)
	if len(value) != 0 {
		value[0] ^= 0xff
	}
	return len(value), nil
}

type invalidCountWriter struct{}

func (invalidCountWriter) Write(value []byte) (int, error) { return len(value) + 1, nil }
