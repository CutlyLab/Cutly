package sfntglyf

import (
	"context"
	"errors"
	"math/big"
	"testing"
)

func TestOutlineSetQ26NearestEvenAndExactMidpoint(t *testing.T) {
	tests := []struct {
		numerator int64
		shift     uint32
		want      Q26_6
	}{
		{1, 7, 0}, {3, 7, 2}, {5, 7, 2},
		{-1, 7, 0}, {-3, 7, -2}, {-5, 7, -2},
		{3, 1, 96}, {-3, 1, -96}, {1, 100, 0},
	}
	for _, test := range tests {
		exact := newGeometryExactContext(context.Background(), 528, 100, 10_000)
		input, err := exact.normalize(big.NewInt(test.numerator), test.shift, "test/input")
		if err != nil {
			t.Fatal(err)
		}
		before := new(big.Int).Set(input.numerator)
		got, err := outlineSetRoundQ26(exact, input, "test/round")
		if err != nil || got != test.want {
			t.Fatalf("%d/2^%d got=%d want=%d err=%v", test.numerator, test.shift, got, test.want, err)
		}
		if input.numerator.Cmp(before) != 0 {
			t.Fatal("round mutated input")
		}
	}
	exact := newGeometryExactContext(context.Background(), 528, 100, 10_000)
	left, _ := exact.normalize(big.NewInt(1), 7, "test/left")
	right, _ := exact.normalize(big.NewInt(5), 7, "test/right")
	midpoint, err := outlineSetMidpoint(exact, left, right, "test/midpoint")
	if err != nil {
		t.Fatal(err)
	}
	if midpoint.numerator.Cmp(big.NewInt(3)) != 0 || midpoint.shift != 7 {
		t.Fatalf("midpoint=%s/2^%d", midpoint.numerator, midpoint.shift)
	}
	got, err := outlineSetRoundQ26(exact, midpoint, "test/midpoint_round")
	if err != nil || got != 2 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}

func TestOutlineSetExactAlignmentAndRoundTemporaryBudgets(t *testing.T) {
	left := geometryDyadic{numerator: big.NewInt(32767)}
	right := geometryDyadic{numerator: big.NewInt(1), shift: 511}
	exact := newGeometryExactContext(context.Background(), 526, 20, 10_000)
	midpoint, err := outlineSetMidpoint(exact, left, right, "test/alignment")
	if err != nil {
		t.Fatal(err)
	}
	if midpoint.exactBits() != 526 {
		t.Fatalf("bits=%d", midpoint.exactBits())
	}
	tooNarrow := newGeometryExactContext(context.Background(), 525, 20, 10_000)
	if _, err := outlineSetMidpoint(tooNarrow, left, right, "test/alignment"); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("expected exact limit, got %v", err)
	}
	integer := geometryDyadic{numerator: big.NewInt(32767)}
	if _, err := outlineSetRoundQ26(newGeometryExactContext(context.Background(), 21, 10, 100), integer, "test/round_bits"); err != nil {
		t.Fatal(err)
	}
	if _, err := outlineSetRoundQ26(newGeometryExactContext(context.Background(), 20, 10, 100), integer, "test/round_bits"); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("expected round exact limit, got %v", err)
	}
}

func TestOutlineSetMidpointAndQ26AgainstIndependentBigRatOracle(t *testing.T) {
	values := []geometryDyadic{
		{numerator: big.NewInt(-17), shift: 9}, {numerator: big.NewInt(-5), shift: 7},
		{numerator: big.NewInt(-1), shift: 20}, {numerator: big.NewInt(0)},
		{numerator: big.NewInt(1), shift: 20}, {numerator: big.NewInt(3), shift: 7},
		{numerator: big.NewInt(5), shift: 7}, {numerator: big.NewInt(32767)},
	}
	for leftIndex, left := range values {
		for rightIndex, right := range values {
			exact := newGeometryExactContext(context.Background(), 528, 100, 100_000)
			got, err := outlineSetMidpoint(exact, left, right, "test/oracle_midpoint")
			if err != nil {
				t.Fatalf("%d/%d: %v", leftIndex, rightIndex, err)
			}
			want := new(big.Rat).Add(outlineSetOracleRat(left), outlineSetOracleRat(right))
			want.Quo(want, big.NewRat(2, 1))
			if outlineSetOracleRat(got).Cmp(want) != 0 {
				t.Fatalf("%d/%d got=%s want=%s", leftIndex, rightIndex, outlineSetOracleRat(got), want)
			}
		}
	}
	for numerator := int64(-33); numerator <= 33; numerator++ {
		for shift := uint32(0); shift <= 12; shift++ {
			input := geometryDyadic{numerator: big.NewInt(numerator), shift: shift}
			if numerator == 0 {
				input.shift = 0
			}
			if numerator != 0 && shift > 0 && numerator&1 == 0 {
				continue
			}
			exact := newGeometryExactContext(context.Background(), 528, 10, 10_000)
			got, err := outlineSetRoundQ26(exact, input, "test/oracle_round")
			if err != nil {
				t.Fatal(err)
			}
			want := outlineSetOracleQ26(outlineSetOracleRat(input))
			if got != want {
				t.Fatalf("%d/2^%d got=%d want=%d", numerator, shift, got, want)
			}
		}
	}
}

func outlineSetOracleRat(value geometryDyadic) *big.Rat {
	denominator := new(big.Int).Lsh(big.NewInt(1), uint(value.shift))
	return new(big.Rat).SetFrac(new(big.Int).Set(value.number()), denominator)
}

func outlineSetOracleQ26(value *big.Rat) Q26_6 {
	scaled := new(big.Rat).Mul(new(big.Rat).Set(value), big.NewRat(64, 1))
	numerator := new(big.Int).Abs(scaled.Num())
	denominator := new(big.Int).Set(scaled.Denom())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := twiceRemainder.Cmp(denominator)
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}
	if scaled.Sign() < 0 {
		quotient.Neg(quotient)
	}
	return Q26_6(quotient.Int64())
}
