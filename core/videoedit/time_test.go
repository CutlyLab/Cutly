package videoedit

import (
	"errors"
	"math"
	"testing"
)

func mustTime(t *testing.T, value, rate int64) RationalTime {
	t.Helper()
	r, err := NewRationalTime(value, rate)
	if err != nil {
		t.Fatalf("NewRationalTime(%d, %d): %v", value, rate, err)
	}
	return r
}

func TestRationalTimeNormalizes(t *testing.T) {
	tests := []struct {
		name        string
		value, rate int64
		want        RationalTime
	}{
		{name: "NTSC frame", value: 2002, rate: 60000, want: RationalTime{Value: 1001, Rate: 30000}},
		{name: "negative denominator", value: 2, rate: -4, want: RationalTime{Value: -1, Rate: 2}},
		{name: "zero", value: 0, rate: 48000, want: RationalTime{Value: 0, Rate: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewRationalTime(tt.value, tt.rate)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRationalTimeValidate(t *testing.T) {
	if err := (RationalTime{Value: 1, Rate: 48000}).Validate(); err != nil {
		t.Fatalf("valid audio time: %v", err)
	}
	for _, rate := range []int64{0, -1} {
		if err := (RationalTime{Value: 1, Rate: rate}).Validate(); !errors.Is(err, ErrInvalidRate) {
			t.Fatalf("rate %d: got %v, want ErrInvalidRate", rate, err)
		}
	}
	if _, err := NewRationalTime(1, 0); !errors.Is(err, ErrInvalidRate) {
		t.Fatalf("zero-rate constructor: got %v, want ErrInvalidRate", err)
	}
}

func TestRationalTimeArithmetic(t *testing.T) {
	oneThird := mustTime(t, 1, 3)
	oneSixth := mustTime(t, 1, 6)

	sum, err := oneThird.Add(oneSixth)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: 1, Rate: 2}); sum != want {
		t.Fatalf("sum = %+v, want %+v", sum, want)
	}

	difference, err := oneThird.Sub(mustTime(t, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: -1, Rate: 6}); difference != want {
		t.Fatalf("difference = %+v, want %+v", difference, want)
	}

	cmp, err := RationalTime{Value: math.MaxInt64, Rate: math.MaxInt64 - 1}.Compare(
		RationalTime{Value: math.MaxInt64 - 1, Rate: math.MaxInt64},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cmp <= 0 {
		t.Fatalf("overflow-safe comparison returned %d, want > 0", cmp)
	}
}

func TestRationalTimeArithmeticDetectsOverflow(t *testing.T) {
	_, err := (RationalTime{Value: math.MaxInt64, Rate: 1}).Add(RationalTime{Value: 1, Rate: 1})
	if !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("Add overflow = %v, want ErrTimeOverflow", err)
	}

	_, err = (RationalTime{Value: math.MaxInt64, Rate: 1}).Rescale(math.MaxInt64)
	if !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("Rescale overflow = %v, want ErrTimeOverflow", err)
	}
}

func TestRationalTimeNormalizeDetectsSignFlipOverflow(t *testing.T) {
	_, err := NewRationalTime(math.MinInt64, -1)
	if !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("Normalize sign overflow = %v, want ErrTimeOverflow", err)
	}
}

func TestRationalTimeRescalesNTSCFramesTo48kAudioExactly(t *testing.T) {
	oneFrame := mustTime(t, 1001, 30000)
	if _, err := oneFrame.Rescale(48000); !errors.Is(err, ErrInexactRescale) {
		t.Fatalf("one frame cannot land on an integer audio sample: got %v", err)
	}

	fiveFrames := mustTime(t, 5*1001, 30000)
	audio, err := fiveFrames.Rescale(48000)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: 8008, Rate: 48000}); audio != want {
		t.Fatalf("audio time = %+v, want %+v", audio, want)
	}
	cmp, err := audio.Compare(fiveFrames)
	if err != nil || cmp != 0 {
		t.Fatalf("rescaled time changed value: cmp=%d err=%v", cmp, err)
	}
}

func TestTimeRangeEndAndValidation(t *testing.T) {
	r := TimeRange{
		Start:    mustTime(t, 1001, 30000),
		Duration: mustTime(t, 5*1001, 30000),
	}
	end, err := r.End()
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: 1001, Rate: 5000}); end != want {
		t.Fatalf("end = %+v, want %+v", end, want)
	}

	negative := TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, -1, 2)}
	if err := negative.Validate(); !errors.Is(err, ErrNegativeDuration) {
		t.Fatalf("negative duration: got %v, want ErrNegativeDuration", err)
	}

	badStart := TimeRange{Start: RationalTime{Value: 0, Rate: 0}, Duration: mustTime(t, 1, 1)}
	if err := badStart.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Fatalf("bad start: got %v, want ErrInvalidRate", err)
	}
}

func TestTimeRangeEndDetectsOverflow(t *testing.T) {
	rangeWithOverflow := TimeRange{
		Start:    RationalTime{Value: math.MaxInt64, Rate: 1},
		Duration: RationalTime{Value: 1, Rate: 1},
	}
	if err := rangeWithOverflow.Validate(); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("Validate overflow = %v, want ErrTimeOverflow", err)
	}
	if _, err := rangeWithOverflow.End(); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("End overflow = %v, want ErrTimeOverflow", err)
	}
}

func TestRescalePreservesNegativeExactValues(t *testing.T) {
	value := RationalTime{Value: -1, Rate: 2}
	rescaled, err := value.Rescale(48000)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RationalTime{Value: -24000, Rate: 48000}); rescaled != want {
		t.Fatalf("rescaled = %+v, want %+v", rescaled, want)
	}
}
