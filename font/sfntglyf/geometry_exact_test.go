package sfntglyf

import (
	"context"
	"errors"
	"math/big"
	"testing"
)

func TestGeometryDyadicConstructorsNormalizeAndOwn(t *testing.T) {
	exact := newGeometryExactContext(context.Background(), 512, 100, 10_000)
	one, err := exact.f2dot14(0x4000, "one")
	if err != nil {
		t.Fatal(err)
	}
	if one.shift != 0 || one.numerator.Cmp(big.NewInt(1)) != 0 || one.exactBits() != 1 {
		t.Fatalf("one=%s/2^%d bits=%d", one.numerator, one.shift, one.exactBits())
	}
	minusTwo, err := exact.f2dot14(-0x8000, "minus_two")
	if err != nil {
		t.Fatal(err)
	}
	if minusTwo.shift != 0 || minusTwo.numerator.Cmp(big.NewInt(-2)) != 0 {
		t.Fatalf("minusTwo=%s/2^%d", minusTwo.numerator, minusTwo.shift)
	}
	max, err := exact.f2dot14(0x7fff, "max")
	if err != nil {
		t.Fatal(err)
	}
	if max.shift != 14 || max.numerator.Cmp(big.NewInt(0x7fff)) != 0 {
		t.Fatalf("max=%s/2^%d", max.numerator, max.shift)
	}
	f2zero, err := exact.f2dot14(0, "f2dot14_zero")
	if err != nil {
		t.Fatal(err)
	}
	if !f2zero.isZero() || f2zero.shift != 0 || f2zero.numerator == nil {
		t.Fatalf("f2dot14 zero=%+v", f2zero)
	}
	zero, err := exact.integer(0, "zero")
	if err != nil {
		t.Fatal(err)
	}
	if !zero.isZero() || zero.shift != 0 || zero.numerator == nil || zero.signTag() != 0 || len(zero.magnitudeBytes()) != 0 {
		t.Fatalf("zero=%+v", zero)
	}

	// Mutating a result cannot affect any constructor input or sibling value.
	copyOfOne := new(big.Int).Set(one.numerator)
	max.numerator.SetInt64(99)
	if one.numerator.Cmp(copyOfOne) != 0 {
		t.Fatal("dyadic numerators alias")
	}
}

func TestGeometryDyadicArithmeticMatchesRatOracle(t *testing.T) {
	tests := []struct {
		leftNumerator  int64
		leftShift      uint32
		rightNumerator int64
		rightShift     uint32
	}{
		{1, 1, 1, 2},
		{-3, 4, 7, 3},
		{0x7fff, 14, -0x8000, 14},
		{32767, 0, -32768, 0},
	}
	for _, test := range tests {
		exact := newGeometryExactContext(context.Background(), 512, 1_000, 100_000)
		left, err := exact.normalize(big.NewInt(test.leftNumerator), test.leftShift, "left")
		if err != nil {
			t.Fatal(err)
		}
		right, err := exact.normalize(big.NewInt(test.rightNumerator), test.rightShift, "right")
		if err != nil {
			t.Fatal(err)
		}
		leftRat := dyadicRat(test.leftNumerator, test.leftShift)
		rightRat := dyadicRat(test.rightNumerator, test.rightShift)

		add, err := exact.add(left, right, "add")
		if err != nil {
			t.Fatal(err)
		}
		assertDyadicRatEqual(t, add, new(big.Rat).Add(leftRat, rightRat))
		subtract, err := exact.subtract(left, right, "subtract")
		if err != nil {
			t.Fatal(err)
		}
		assertDyadicRatEqual(t, subtract, new(big.Rat).Sub(leftRat, rightRat))
		product, err := exact.multiply(left, right, "multiply")
		if err != nil {
			t.Fatal(err)
		}
		assertDyadicRatEqual(t, product, new(big.Rat).Mul(leftRat, rightRat))
		negated, err := exact.negate(left, "negate")
		if err != nil {
			t.Fatal(err)
		}
		assertDyadicRatEqual(t, negated, new(big.Rat).Neg(leftRat))
		comparison, err := exact.compare(left, right, "compare")
		if err != nil {
			t.Fatal(err)
		}
		if comparison != leftRat.Cmp(rightRat) {
			t.Fatalf("compare=%d want=%d", comparison, leftRat.Cmp(rightRat))
		}
	}
}

func TestGeometryDyadicFloorCeilNegativeAndPositive(t *testing.T) {
	tests := []struct {
		numerator int64
		shift     uint32
		wantFloor int64
		wantCeil  int64
	}{
		{3, 1, 1, 2},
		{-3, 1, -2, -1},
		{4, 1, 2, 2},
		{-4, 1, -2, -2},
		{1, 14, 0, 1},
		{-1, 14, -1, 0},
		{0, 0, 0, 0},
	}
	for _, test := range tests {
		exact := newGeometryExactContext(context.Background(), 512, 100, 10_000)
		value, err := exact.normalize(big.NewInt(test.numerator), test.shift, "value")
		if err != nil {
			t.Fatal(err)
		}
		floor, err := exact.floor(value, "floor")
		if err != nil {
			t.Fatal(err)
		}
		ceil, err := exact.ceil(value, "ceil")
		if err != nil {
			t.Fatal(err)
		}
		if !floor.IsInt64() || floor.Int64() != test.wantFloor || !ceil.IsInt64() || ceil.Int64() != test.wantCeil {
			t.Fatalf("%d/2^%d floor=%s ceil=%s want=%d,%d", test.numerator, test.shift, floor, ceil, test.wantFloor, test.wantCeil)
		}
	}
}

func TestGeometryDyadicNeverMutatesInputs(t *testing.T) {
	exact := newGeometryExactContext(context.Background(), 512, 100, 10_000)
	left, err := exact.normalize(big.NewInt(3), 4, "left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := exact.normalize(big.NewInt(-5), 2, "right")
	if err != nil {
		t.Fatal(err)
	}
	leftBefore, rightBefore := new(big.Int).Set(left.numerator), new(big.Int).Set(right.numerator)
	leftShift, rightShift := left.shift, right.shift
	operations := []func() error{
		func() error { _, err := exact.add(left, right, "add"); return err },
		func() error { _, err := exact.subtract(left, right, "sub"); return err },
		func() error { _, err := exact.multiply(left, right, "mul"); return err },
		func() error { _, err := exact.negate(left, "neg"); return err },
		func() error { _, err := exact.compare(left, right, "cmp"); return err },
		func() error { _, err := exact.floor(left, "floor"); return err },
		func() error { _, err := exact.ceil(right, "ceil"); return err },
	}
	for _, operation := range operations {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
	}
	if left.numerator.Cmp(leftBefore) != 0 || right.numerator.Cmp(rightBefore) != 0 || left.shift != leftShift || right.shift != rightShift {
		t.Fatalf("inputs mutated left=%s/2^%d right=%s/2^%d", left.numerator, left.shift, right.numerator, right.shift)
	}
}

func TestGeometryDyadicLimitsCancellationAndAccounting(t *testing.T) {
	t.Run("bit limit", func(t *testing.T) {
		exact := newGeometryExactContext(context.Background(), 8, 100, 10_000)
		if _, err := exact.integer(255, "fits"); err != nil {
			t.Fatal(err)
		}
		if _, err := exact.integer(256, "too_large"); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("operation limit", func(t *testing.T) {
		exact := newGeometryExactContext(context.Background(), 512, 1, 10_000)
		if _, err := exact.integer(1, "first"); err != nil {
			t.Fatal(err)
		}
		if _, err := exact.integer(2, "second"); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("limb limit exact boundary", func(t *testing.T) {
		exact := newGeometryExactContext(context.Background(), 512, 10, 1)
		if _, err := exact.integer(1, "one"); err != nil {
			t.Fatal(err)
		}
		if exact.Stats().LimbUnits != 1 {
			t.Fatalf("stats=%+v", exact.Stats())
		}
		if _, err := exact.integer(2, "two"); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("shared work limb path is preserved", func(t *testing.T) {
		work := &geometryWorkMeter{limit: 8}
		exact := newGeometryExactContextWithWork(context.Background(), 512, 10, 10_000, work)
		one := geometryDyadic{numerator: big.NewInt(1)}
		_, err := exact.add(one, one, "geometry/test/add")
		var typed *Error
		if !errors.As(err, &typed) || typed.Path != "geometry/test/add/work/exact_limbs" {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		exact := newGeometryExactContext(ctx, 512, 10, 10)
		if _, err := exact.integer(1, "canceled"); !errors.Is(err, ErrCanceled) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestGeometryDyadicNormalizedProductAndWideComparisonAtBitLimit(t *testing.T) {
	t.Run("product normalizes below raw product width", func(t *testing.T) {
		exact := newGeometryExactContext(context.Background(), 16, 10, 1_000)
		integer, err := exact.integer(-32768, "integer")
		if err != nil {
			t.Fatal(err)
		}
		coefficient, err := exact.f2dot14(3, "coefficient")
		if err != nil {
			t.Fatal(err)
		}
		product, err := exact.multiply(integer, coefficient, "product")
		if err != nil {
			t.Fatal(err)
		}
		if product.shift != 0 || product.numerator.Cmp(big.NewInt(-6)) != 0 {
			t.Fatalf("product=%s/2^%d", product.numerator, product.shift)
		}
	})
	t.Run("comparison temporary may be twice value width", func(t *testing.T) {
		exact := newGeometryExactContext(context.Background(), 15, 10, 1_000)
		integer, err := exact.integer(32767, "integer")
		if err != nil {
			t.Fatal(err)
		}
		fraction, err := exact.f2dot14(1, "fraction")
		if err != nil {
			t.Fatal(err)
		}
		comparison, err := exact.compare(integer, fraction, "compare")
		if err != nil {
			t.Fatal(err)
		}
		if comparison <= 0 {
			t.Fatalf("comparison=%d", comparison)
		}
	})
}

func dyadicRat(numerator int64, shift uint32) *big.Rat {
	denominator := new(big.Int).Lsh(big.NewInt(1), uint(shift))
	return new(big.Rat).SetFrac(big.NewInt(numerator), denominator)
}

func assertDyadicRatEqual(t *testing.T, got geometryDyadic, want *big.Rat) {
	t.Helper()
	denominator := new(big.Int).Lsh(big.NewInt(1), uint(got.shift))
	gotRat := new(big.Rat).SetFrac(new(big.Int).Set(got.number()), denominator)
	if gotRat.Cmp(want) != 0 {
		t.Fatalf("dyadic=%s/2^%d (%s) want=%s", got.numerator, got.shift, gotRat, want)
	}
}
