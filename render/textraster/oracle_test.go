package textraster

import (
	"math/big"
	"math/rand"
	"testing"
)

// TestQuadraticCrossingsAgainstDirectRootOracle keeps the production algebraic-sign and
// threshold implementation honest with an independent high-precision root evaluator. Cases whose
// root or x crossing is deliberately too close to a contract boundary are skipped here and covered
// by the exact endpoint/equality fixtures in crossing_test.go.
func TestQuadraticCrossingsAgainstDirectRootOracle(t *testing.T) {
	const columns = 24
	exact := exactContext{maxBits: 512}
	random := rand.New(rand.NewSource(0x4355544c59))
	checked := 0
	for attempt := 0; attempt < 4_000 && checked < 800; attempt++ {
		segment := rasterSegment{kind: segmentQuad,
			p0: rasterPoint{x: int64(random.Intn(257) - 64), y: int64(random.Intn(257) - 64)},
			p1: rasterPoint{x: int64(random.Intn(257) - 64), y: int64(random.Intn(257) - 64)},
			p2: rasterPoint{x: int64(random.Intn(257) - 64), y: int64(random.Intn(257) - 64)},
		}
		sampleY := int64(random.Intn(24))*8 + 4
		want, ok := directQuadraticOracle(segment, sampleY, columns)
		if !ok {
			continue
		}
		delta := make([]int64, columns+1)
		if err := addQuadraticCrossings(exact, segment, sampleY, columns, delta, "oracle/quad"); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		var winding int64
		for column := 0; column < columns; column++ {
			winding += delta[column]
			if winding != want[column] {
				t.Fatalf("attempt %d column %d: winding=%d want=%d segment=%+v y=%d delta=%v",
					attempt, column, winding, want[column], segment, sampleY, delta)
			}
		}
		if winding+delta[columns] != 0 {
			t.Fatalf("attempt %d: unclosed delta=%v", attempt, delta)
		}
		checked++
	}
	if checked < 500 {
		t.Fatalf("insufficient non-boundary oracle cases: %d", checked)
	}
}

func directQuadraticOracle(segment rasterSegment, sampleY int64, columns int) ([]int64, bool) {
	const precision = uint(256)
	integer := func(value int64) *big.Float {
		return new(big.Float).SetPrec(precision).SetMode(big.ToNearestEven).SetInt64(value)
	}
	coefficient := func(p0, p1, p2, offset int64) (a, b, c int64) {
		return p0 - 2*p1 + p2, 2 * (p1 - p0), p0 - offset
	}
	ay, by, cy := coefficient(segment.p0.y, segment.p1.y, segment.p2.y, sampleY)
	if ay == 0 {
		return nil, false
	}
	discriminant := by*by - 4*ay*cy
	if discriminant <= 0 {
		return nil, false
	}
	ax, bx, cx := coefficient(segment.p0.x, segment.p1.x, segment.p2.x, 0)
	sqrtD := new(big.Float).SetPrec(precision).Sqrt(integer(discriminant))
	denominator := integer(2 * ay)
	zero := integer(0)
	one := integer(1)
	boundary := new(big.Float).SetPrec(precision).SetMantExp(integer(1), -120)
	result := make([]int64, columns)
	for _, sign := range []int64{-1, 1} {
		numerator := new(big.Float).SetPrec(precision).Neg(integer(by))
		rootTerm := new(big.Float).SetPrec(precision).Mul(integer(sign), sqrtD)
		numerator.Add(numerator, rootTerm)
		root := new(big.Float).SetPrec(precision).Quo(numerator, denominator)
		fromZero := new(big.Float).SetPrec(precision).Abs(new(big.Float).Sub(root, zero))
		fromOne := new(big.Float).SetPrec(precision).Abs(new(big.Float).Sub(root, one))
		if fromZero.Cmp(boundary) <= 0 || fromOne.Cmp(boundary) <= 0 {
			return nil, false
		}
		if root.Cmp(zero) <= 0 || root.Cmp(one) >= 0 {
			continue
		}
		rootSquared := new(big.Float).SetPrec(precision).Mul(root, root)
		x := new(big.Float).SetPrec(precision).Mul(integer(ax), rootSquared)
		x.Add(x, new(big.Float).SetPrec(precision).Mul(integer(bx), root))
		x.Add(x, integer(cx))
		for column := 0; column < columns; column++ {
			sampleX := integer(int64(column)*8 + 4)
			difference := new(big.Float).SetPrec(precision).Sub(x, sampleX)
			if new(big.Float).SetPrec(precision).Abs(new(big.Float).Set(difference)).Cmp(boundary) <= 0 {
				return nil, false
			}
			if difference.Sign() > 0 {
				result[column] += sign
			}
		}
	}
	return result, true
}
