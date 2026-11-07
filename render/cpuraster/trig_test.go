package cpuraster

import (
	"math"
	"math/big"
	"testing"
)

func TestTrigQ48CanonicalNumerators(t *testing.T) {
	const oneQ48 = int64(1) << TrigFractionBits
	tests := []struct {
		degrees   int64
		sineQ48   int64
		cosineQ48 int64
	}{
		{0, 0, oneQ48},
		{30, 140737488355328, 243764480361061},
		{45, 199032864766430, 199032864766430},
		{60, 243764480361061, 140737488355328},
		{90, oneQ48, 0},
		{120, 243764480361061, -140737488355328},
		{180, 0, -oneQ48},
		{-45, -199032864766430, 199032864766430},
		{-90, -oneQ48, 0},
		{-150, -140737488355328, -243764480361061},
	}
	for _, test := range tests {
		sine, cosine, err := sinCosQ48(big.NewRat(test.degrees, 1))
		if err != nil {
			t.Fatal(err)
		}
		if got := q48Numerator(t, sine); got != test.sineQ48 {
			t.Fatalf("sin(%d) Q48 = %d, want %d", test.degrees, got, test.sineQ48)
		}
		if got := q48Numerator(t, cosine); got != test.cosineQ48 {
			t.Fatalf("cos(%d) Q48 = %d, want %d", test.degrees, got, test.cosineQ48)
		}
	}
}

func TestTrigDenseRationalAnglesAgainstIndependentMathOracle(t *testing.T) {
	const tolerance = 2.0 / (1 << TrigFractionBits)
	for numerator := int64(-5040); numerator <= 5040; numerator += 1009 {
		degrees := big.NewRat(numerator, 7)
		sine, cosine, err := sinCosQ48(degrees)
		if err != nil {
			t.Fatal(err)
		}
		actualSine, _ := sine.Float64()
		actualCosine, _ := cosine.Float64()
		angle := float64(numerator) / 7 * math.Pi / 180
		if math.Abs(actualSine-math.Sin(angle)) > tolerance || math.Abs(actualCosine-math.Cos(angle)) > tolerance {
			t.Fatalf("angle %s got sin %.17g cos %.17g; math oracle %.17g %.17g", degrees, actualSine, actualCosine, math.Sin(angle), math.Cos(angle))
		}
	}
}

func TestTrigHugePeriodReductionAndSignedNearestEven(t *testing.T) {
	periods := new(big.Int).Lsh(big.NewInt(1), 2048)
	numerator := new(big.Int).Mul(periods, big.NewInt(360))
	numerator.Add(numerator, big.NewInt(45))
	huge := new(big.Rat).SetInt(numerator)
	hugeSine, hugeCosine, err := sinCosQ48(huge)
	if err != nil {
		t.Fatal(err)
	}
	baseSine, baseCosine, err := sinCosQ48(big.NewRat(45, 1))
	if err != nil || hugeSine.Cmp(baseSine) != 0 || hugeCosine.Cmp(baseCosine) != 0 {
		t.Fatalf("huge-period result = %s,%s; base=%s,%s err=%v", hugeSine, hugeCosine, baseSine, baseCosine, err)
	}

	for _, test := range []struct {
		value *big.Rat
		bits  uint
		want  int64
	}{
		{big.NewRat(1, 2), 0, 0}, {big.NewRat(3, 2), 0, 2},
		{big.NewRat(-1, 2), 0, 0}, {big.NewRat(-3, 2), 0, -2},
		{big.NewRat(-5, 2), 0, -2}, {big.NewRat(-7, 2), 0, -4},
	} {
		got, quantizeErr := quantizeSignedQ(test.value, test.bits)
		if quantizeErr != nil || got != test.want {
			t.Fatalf("quantizeSignedQ(%s,%d) = %d,%v; want %d", test.value, test.bits, got, quantizeErr, test.want)
		}
	}
}

func q48Numerator(t *testing.T, value *big.Rat) int64 {
	t.Helper()
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), TrigFractionBits)))
	if scaled.Denom().Cmp(big.NewInt(1)) != 0 || !scaled.Num().IsInt64() {
		t.Fatalf("%s is not a signed Q48 rational", value)
	}
	return scaled.Num().Int64()
}
