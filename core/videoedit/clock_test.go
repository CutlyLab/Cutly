package videoedit

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestCompileFrameTickRangeUsesCompositionClock(t *testing.T) {
	clock := FrameClock{
		Origin:          RationalTime{Value: 0, Rate: 1},
		FramesPerSecond: RationalTime{Value: 30000, Rate: 1001},
	}
	requested := TimeRange{
		Start:    RationalTime{Value: 1001 * 10, Rate: 30000},
		Duration: RationalTime{Value: 1001 * 5, Rate: 30000},
	}

	got, err := CompileFrameTickRange(requested, clock)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TickRange{First: 10, Count: 5}); got != want {
		t.Fatalf("range = %+v, want %+v", got, want)
	}
}

func TestCompileFrameTickRangeRoundsExactHalfOpenBoundsUp(t *testing.T) {
	clock := FrameClock{
		Origin:          RationalTime{Value: 0, Rate: 1},
		FramesPerSecond: RationalTime{Value: 30, Rate: 1},
	}
	requested := TimeRange{
		Start:    RationalTime{Value: 1, Rate: 20}, // frame position 1.5
		Duration: RationalTime{Value: 1, Rate: 10}, // end position 4.5
	}

	got, err := CompileFrameTickRange(requested, clock)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TickRange{First: 2, Count: 3}); got != want {
		t.Fatalf("range = %+v, want %+v", got, want)
	}
	for _, index := range []int64{2, 3, 4} {
		contains, err := got.Contains(index)
		if err != nil || !contains {
			t.Fatalf("index %d: contains=%v err=%v", index, contains, err)
		}
	}
}

func TestCompileFrameTickRangeSupportsNegativeIndices(t *testing.T) {
	clock := FrameClock{
		Origin:          RationalTime{Value: 0, Rate: 1},
		FramesPerSecond: RationalTime{Value: 2, Rate: 1},
	}
	requested := TimeRange{
		Start:    RationalTime{Value: -3, Rate: 4},
		Duration: RationalTime{Value: 1, Rate: 1},
	}

	got, err := CompileFrameTickRange(requested, clock)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TickRange{First: -1, Count: 2}); got != want {
		t.Fatalf("range = %+v, want %+v", got, want)
	}
}

func TestCompileSubTickRanges(t *testing.T) {
	clock := FrameClock{
		Origin:          RationalTime{Value: 0, Rate: 1},
		FramesPerSecond: RationalTime{Value: 30, Rate: 1},
	}
	tests := []struct {
		name      string
		requested TimeRange
		want      TickRange
	}{
		{
			name: "contains origin tick",
			requested: TimeRange{
				Start:    RationalTime{Value: 0, Rate: 1},
				Duration: RationalTime{Value: 1, Rate: 100},
			},
			want: TickRange{First: 0, Count: 1},
		},
		{
			name: "falls between ticks",
			requested: TimeRange{
				Start:    RationalTime{Value: 1, Rate: 100},
				Duration: RationalTime{Value: 1, Rate: 100},
			},
			want: TickRange{First: 1, Count: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompileFrameTickRange(tt.requested, clock)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("range = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCompileSampleTickRangeSupportsCommonRates(t *testing.T) {
	for _, sampleRate := range []int64{44100, 48000, 96000} {
		t.Run(big.NewInt(sampleRate).String(), func(t *testing.T) {
			got, err := CompileSampleTickRange(
				TimeRange{
					Start:    RationalTime{Value: 1, Rate: 1},
					Duration: RationalTime{Value: 2, Rate: 1},
				},
				SampleClock{
					Origin:           RationalTime{Value: 0, Rate: 1},
					SamplesPerSecond: sampleRate,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if want := (TickRange{First: sampleRate, Count: 2 * sampleRate}); got != want {
				t.Fatalf("range = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCompositionClockTimeAtSignedIndices(t *testing.T) {
	frameTime, err := FrameTimeAt(
		FrameClock{
			Origin:          RationalTime{Value: 1, Rate: 2},
			FramesPerSecond: RationalTime{Value: 30000, Rate: 1001},
		},
		-3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: 3999, Rate: 10000}); frameTime != want {
		t.Fatalf("frame time = %+v, want %+v", frameTime, want)
	}

	sampleTime, err := SampleTimeAt(
		SampleClock{Origin: RationalTime{Value: -1, Rate: 2}, SamplesPerSecond: 48000},
		24000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: 0, Rate: 1}); sampleTime != want {
		t.Fatalf("sample time = %+v, want %+v", sampleTime, want)
	}
}

func TestClockCompilationUsesBigIntermediates(t *testing.T) {
	requested := TimeRange{
		Start:    RationalTime{Value: math.MaxInt64, Rate: math.MaxInt64 - 1},
		Duration: RationalTime{Value: 0, Rate: 1},
	}
	clock := FrameClock{
		Origin:          RationalTime{Value: 0, Rate: 1},
		FramesPerSecond: RationalTime{Value: math.MaxInt64 - 1, Rate: math.MaxInt64},
	}
	got, err := CompileFrameTickRange(requested, clock)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TickRange{First: 1, Count: 0}); got != want {
		t.Fatalf("range = %+v, want %+v", got, want)
	}
}

func TestClockCompilationAndTickRangeDetectOverflow(t *testing.T) {
	_, err := CompileFrameTickRange(
		TimeRange{
			Start:    RationalTime{Value: math.MaxInt64, Rate: 1},
			Duration: RationalTime{Value: 0, Rate: 1},
		},
		FrameClock{
			Origin:          RationalTime{Value: 0, Rate: 1},
			FramesPerSecond: RationalTime{Value: math.MaxInt64, Rate: 1},
		},
	)
	if !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("compile overflow = %v, want ErrTimeOverflow", err)
	}

	if err := (TickRange{First: math.MaxInt64, Count: 1}).Validate(); !errors.Is(err, ErrInvalidTickRange) {
		t.Fatalf("tick range overflow = %v, want ErrInvalidTickRange", err)
	}
	if err := (TickRange{First: 0, Count: -1}).Validate(); !errors.Is(err, ErrInvalidTickRange) {
		t.Fatalf("negative count = %v, want ErrInvalidTickRange", err)
	}
}

func TestClockValidation(t *testing.T) {
	if _, err := CompileFrameTickRange(
		TimeRange{Start: RationalTime{Value: 0, Rate: 1}, Duration: RationalTime{Value: 1, Rate: 1}},
		FrameClock{Origin: RationalTime{Value: 0, Rate: 1}, FramesPerSecond: RationalTime{Value: 0, Rate: 1}},
	); !errors.Is(err, ErrInvalidFrameClock) {
		t.Fatalf("zero frame rate = %v, want ErrInvalidFrameClock", err)
	}
	if _, err := CompileSampleTickRange(
		TimeRange{Start: RationalTime{Value: 0, Rate: 1}, Duration: RationalTime{Value: 1, Rate: 1}},
		SampleClock{Origin: RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 0},
	); !errors.Is(err, ErrInvalidSampleClock) {
		t.Fatalf("zero sample rate = %v, want ErrInvalidSampleClock", err)
	}
}

func TestCeilBigRatToInt64UsesMathematicalCeiling(t *testing.T) {
	tests := []struct {
		value *big.Rat
		want  int64
	}{
		{value: big.NewRat(3, 2), want: 2},
		{value: big.NewRat(-3, 2), want: -1},
		{value: big.NewRat(-2, 1), want: -2},
		{value: big.NewRat(0, 1), want: 0},
	}
	for _, tt := range tests {
		got, err := ceilBigRatToInt64(tt.value)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Fatalf("ceil(%s) = %d, want %d", tt.value.RatString(), got, tt.want)
		}
	}
}
