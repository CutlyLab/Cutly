package textlayout

import (
	"context"
	"math/big"
	"math/rand"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func TestScaleQMatchesIndependentBigRatOracle(t *testing.T) {
	random := rand.New(rand.NewSource(0x4355544c59))
	exact := exactContext{maxBits: DefaultMaxExactBits}
	for iteration := 0; iteration < 10_000; iteration++ {
		value := int64(random.Intn(2_000_001) - 1_000_000)
		numerator := int64(random.Intn(4096) + 1)
		denominator := int64(random.Intn(4096) + 1)
		divisor := gcd64(numerator, denominator)
		numerator /= divisor
		denominator /= divisor
		unitsPerEm := uint32(random.Intn(int(HardMaxUnitsPerEm-16+1)) + 16)
		scale := documentv2.Rational{Numerator: numerator, Denominator: denominator}
		got, err := exact.scaleQ(Q26_6(value), scale, unitsPerEm, "oracle")
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		oracle := new(big.Rat).SetFrac(
			new(big.Int).Mul(big.NewInt(value), big.NewInt(numerator)),
			new(big.Int).Mul(big.NewInt(denominator), new(big.Int).SetUint64(uint64(unitsPerEm))),
		)
		want := oracleRoundNearestEven(t, oracle)
		if int64(got) != want {
			t.Fatalf("iteration %d scale(%d,%d/%d,%d)=%d want %d", iteration, value, numerator, denominator, unitsPerEm, got, want)
		}
	}
}

func TestBaselineMatchesIndependentBigRatOracle(t *testing.T) {
	random := rand.New(rand.NewSource(0x424153454c494e45))
	exact := exactContext{maxBits: DefaultMaxExactBits}
	for iteration := 0; iteration < 5000; iteration++ {
		ascender := int32(random.Intn(32767) + 1)
		fontSize := reducedPositiveRational(random)
		lineHeight := reducedPositiveRational(random)
		unitsPerEm := uint32(random.Intn(int(HardMaxUnitsPerEm-16+1)) + 16)
		lineIndex := uint64(random.Intn(2048))
		got, err := exact.baseline(ascender, fontSize, lineHeight, unitsPerEm, lineIndex, "oracle")
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		fontTerm := new(big.Rat).SetFrac(
			new(big.Int).Mul(big.NewInt(int64(ascender)), big.NewInt(fontSize.Numerator)),
			new(big.Int).Mul(big.NewInt(fontSize.Denominator), new(big.Int).SetUint64(uint64(unitsPerEm))),
		)
		lineTerm := new(big.Rat).Mul(
			new(big.Rat).SetInt(new(big.Int).SetUint64(lineIndex)),
			new(big.Rat).SetFrac64(lineHeight.Numerator, lineHeight.Denominator),
		)
		oracle := new(big.Rat).Add(fontTerm, lineTerm)
		oracle.Mul(oracle, big.NewRat(64, 1))
		want := oracleRoundNearestEven(t, oracle)
		if int64(got) != want {
			t.Fatalf("iteration %d baseline=%d want %d", iteration, got, want)
		}
	}
}

func TestSignedNearestEvenTieCases(t *testing.T) {
	exact := exactContext{maxBits: DefaultMaxExactBits}
	tests := []struct{ numerator, denominator, want int64 }{
		{1, 2, 0}, {3, 2, 2}, {5, 2, 2}, {7, 2, 4},
		{-1, 2, 0}, {-3, 2, -2}, {-5, 2, -2}, {-7, 2, -4},
	}
	for _, test := range tests {
		got, err := exact.roundFraction(big.NewInt(test.numerator), big.NewInt(test.denominator), "tie")
		if err != nil || got != test.want {
			t.Fatalf("round(%d/%d)=%d,%v want %d", test.numerator, test.denominator, got, err, test.want)
		}
	}
}

func TestExactOverflowFailsClosed(t *testing.T) {
	input := oneGlyphInput("A")
	input.FontSize = documentv2.Rational{Numerator: int64(^uint64(0) >> 1), Denominator: 1}
	input.Lines[0].Glyphs[0].XAdvance = Q26_6(int64(^uint64(0) >> 1))
	layout, receipt, err := Build(testContext(), input, Limits{})
	if layout != nil || receipt != (Receipt{}) || err == nil {
		t.Fatalf("layout=%v receipt=%+v err=%v", layout, receipt, err)
	}
}

func FuzzScaleQMatchesBigRat(f *testing.F) {
	for _, seed := range []struct {
		value, numerator, denominator int64
		units                         uint32
	}{
		{1, 8, 1, 16}, {-3, 8, 1, 16}, {640, 64, 1, 64}, {12345, 7, 9, 1000},
	} {
		f.Add(seed.value, seed.numerator, seed.denominator, seed.units)
	}
	f.Fuzz(func(t *testing.T, value, numerator, denominator int64, units uint32) {
		if numerator == 0 || numerator == -1<<63 || denominator == 0 || denominator == -1<<63 {
			t.Skip()
		}
		if numerator < 0 {
			numerator = -numerator
		}
		if denominator < 0 {
			denominator = -denominator
		}
		if numerator > 1_000_000 || denominator > 1_000_000 {
			t.Skip()
		}
		units = 16 + units%(HardMaxUnitsPerEm-15)
		divisor := gcd64(numerator, denominator)
		numerator /= divisor
		denominator /= divisor
		exact := exactContext{maxBits: HardMaxExactBits}
		got, err := exact.scaleQ(Q26_6(value), documentv2.Rational{Numerator: numerator, Denominator: denominator}, units, "fuzz")
		oracle := new(big.Rat).SetFrac(
			new(big.Int).Mul(big.NewInt(value), big.NewInt(numerator)),
			new(big.Int).Mul(big.NewInt(denominator), new(big.Int).SetUint64(uint64(units))),
		)
		want, fits := oracleRoundNearestEvenIfInt64(oracle)
		if !fits {
			if err == nil {
				t.Fatalf("overflow returned %d", got)
			}
			return
		}
		if err != nil || int64(got) != want {
			t.Fatalf("scale=%d,%v want %d", got, err, want)
		}
	})
}

func oracleRoundNearestEven(t *testing.T, value *big.Rat) int64 {
	t.Helper()
	result, ok := oracleRoundNearestEvenIfInt64(value)
	if !ok {
		t.Fatalf("oracle result does not fit int64: %s", value)
	}
	return result
}

func oracleRoundNearestEvenIfInt64(value *big.Rat) (int64, bool) {
	negative := value.Sign() < 0
	numerator := new(big.Int).Abs(new(big.Int).Set(value.Num()))
	denominator := new(big.Int).Set(value.Denom())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	comparison := new(big.Int).Lsh(remainder, 1).Cmp(denominator)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}

func reducedPositiveRational(random *rand.Rand) documentv2.Rational {
	numerator := int64(random.Intn(4096) + 1)
	denominator := int64(random.Intn(4096) + 1)
	divisor := gcd64(numerator, denominator)
	return documentv2.Rational{Numerator: numerator / divisor, Denominator: denominator / divisor}
}

func gcd64(left, right int64) int64 {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func testContext() context.Context { return context.Background() }
