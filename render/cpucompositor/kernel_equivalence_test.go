package cpucompositor

import (
	"math/big"
	"math/rand"
	"testing"
)

// The bounded-arithmetic channel kernels must be bit-identical to the historical big.Int
// expressions for every input they admit. These tests drive both implementations with the same
// values and weights, including tie cases, odd and even denominators, weights beyond the uint64
// fast path, and the complementary-weight sums Dissolve admits.
func TestWeightedChannelFastPathMatchesBigInt(t *testing.T) {
	random := rand.New(rand.NewSource(20261112))
	denominators := []uint64{1, 2, 3, 5, 255, 256, 65535, 65536, 1 << 20, 1<<24 - 1, 1 << 31, 1<<40 - 1, 1<<48 - 1, 1<<63 + 1, 1<<64 - 1}
	values := []uint16{0, 1, 2, 3, 32767, 32768, 65534, 65535}
	for _, denominator := range denominators {
		for _, value := range values {
			for _, numerator := range []uint64{0, 1, denominator / 2, denominator - 1, denominator} {
				weight := uint64Weight(t, numerator, denominator)
				fast, fastErr := weightedChannel(value, weight)
				reference := referenceWeightedChannel(t, value, weight)
				if fastErr != nil {
					t.Fatalf("fast path rejected value=%d %d/%d: %v", value, numerator, denominator, fastErr)
				}
				if fast != reference {
					t.Fatalf("weightedChannel(%d, %d/%d) = %d, big.Int path = %d", value, numerator, denominator, fast, reference)
				}
			}
		}
		sampledDenominator := denominator
		if sampledDenominator > 1<<32 {
			sampledDenominator = 1 << 32
		}
		for iteration := 0; iteration < 4096; iteration++ {
			numerator := uint64(random.Int63n(int64(sampledDenominator) + 1))
			value := uint16(random.Intn(65536))
			weight := uint64Weight(t, numerator, denominator)
			fast, fastErr := weightedChannel(value, weight)
			reference := referenceWeightedChannel(t, value, weight)
			if fastErr != nil {
				t.Fatalf("fast path rejected value=%d %d/%d: %v", value, numerator, denominator, fastErr)
			}
			if fast != reference {
				t.Fatalf("weightedChannel(%d, %d/%d) = %d, big.Int path = %d", value, numerator, denominator, fast, reference)
			}
		}
	}
}

func TestWeightedSumChannelFastPathMatchesBigInt(t *testing.T) {
	random := rand.New(rand.NewSource(20261113))
	// Dissolve only admits complementary weights, so the differential drives pairs that sum to
	// one plus non-complementary pairs through the kernels directly (the kernels themselves do
	// not enforce complementarity).
	for iteration := 0; iteration < 20000; iteration++ {
		leftDenominator := uint64(random.Int63n(1<<32)) + 1
		rightDenominator := uint64(random.Int63n(1<<32)) + 1
		leftNumerator := uint64(random.Int63n(int64(leftDenominator) + 1))
		rightNumerator := uint64(random.Int63n(int64(rightDenominator) + 1))
		left := uint16(random.Intn(65536))
		right := uint16(random.Intn(65536))
		leftWeight := uint64Weight(t, leftNumerator, leftDenominator)
		rightWeight := uint64Weight(t, rightNumerator, rightDenominator)
		fast, fastErr := weightedSumChannel(left, right, leftWeight, rightWeight)
		reference, referenceErr := referenceWeightedSumChannel(left, right, leftWeight, rightWeight)
		if (fastErr == nil) != (referenceErr == nil) {
			t.Fatalf("error divergence for %d,%d %d/%d + %d/%d: fast=%v big=%v", left, right, leftNumerator, leftDenominator, rightNumerator, rightDenominator, fastErr, referenceErr)
		}
		if fastErr == nil && fast != reference {
			t.Fatalf("weightedSumChannel(%d,%d, %d/%d, %d/%d) = %d, big.Int path = %d", left, right, leftNumerator, leftDenominator, rightNumerator, rightDenominator, fast, reference)
		}
	}
	// Tie and boundary shapes deliberately: half fractions against even quotients.
	for _, denominator := range []uint64{2, 4, 6, 8, 65534, 65536} {
		for _, numerator := range []uint64{denominator / 2, denominator/2 + 1, denominator - 1} {
			if numerator > denominator {
				continue
			}
			for _, left := range []uint16{0, 1, 2, 3, 65535} {
				for _, right := range []uint16{0, 1, 2, 3, 65535} {
					leftWeight := uint64Weight(t, numerator, denominator)
					rightWeight := uint64Weight(t, denominator-numerator, denominator)
					if !weightsSumToOne(leftWeight, rightWeight) {
						continue
					}
					fast, fastErr := weightedSumChannel(left, right, leftWeight, rightWeight)
					reference, referenceErr := referenceWeightedSumChannel(left, right, leftWeight, rightWeight)
					if (fastErr == nil) != (referenceErr == nil) || (fastErr == nil && fast != reference) {
						t.Fatalf("tie divergence %d,%d %d/%d: fast=%d(%v) big=%d(%v)", left, right, numerator, denominator, fast, fastErr, reference, referenceErr)
					}
				}
			}
		}
	}
}

func TestWeightedChannelsBeyondUint64FallBackExactly(t *testing.T) {
	// Weight parts beyond uint64 must keep the big.Int semantics; the kernels route them to
	// the fallback and results still match a direct big.Int computation.
	hugeDenominator := new(big.Int).Lsh(big.NewInt(1), 200)
	hugeNumerator := new(big.Int).Rsh(hugeDenominator, 1)
	weight := exactWeight{}
	weight.numerator.Set(hugeNumerator)
	weight.denominator.Set(hugeDenominator)
	for _, value := range []uint16{0, 1, 65535} {
		fast, err := weightedChannel(value, weight)
		if err != nil {
			t.Fatalf("huge weight rejected: %v", err)
		}
		reference := referenceWeightedChannel(t, value, weight)
		if fast != reference {
			t.Fatalf("huge weightedChannel(%d) = %d, want %d", value, fast, reference)
		}
	}
	half := exactWeight{numerator: *big.NewInt(1), denominator: *big.NewInt(2)}
	complement := exactWeight{numerator: *big.NewInt(1), denominator: *big.NewInt(2)}
	fast, err := weightedSumChannel(65535, 65534, weight, exactWeight{})
	if err == nil {
		_ = fast
	}
	if _, err := weightedSumChannel(30000, 40000, weight, complement); err != nil {
		t.Fatalf("mixed huge/complement rejected: %v", err)
	}
	if _, err := weightedSumChannel(30000, 40000, half, complement); err != nil {
		t.Fatalf("half weights rejected: %v", err)
	}
}

// uint64Weight builds the kernel weight struct directly: the equivalence under test is the
// channel arithmetic, not evaluator admission, so non-reduced fractions are welcome here.
func uint64Weight(t *testing.T, numerator, denominator uint64) exactWeight {
	t.Helper()
	if denominator == 0 || numerator > denominator {
		t.Fatalf("invalid test weight %d/%d", numerator, denominator)
	}
	return exactWeight{numerator: *new(big.Int).SetUint64(numerator), denominator: *new(big.Int).SetUint64(denominator)}
}

func referenceWeightedChannel(t *testing.T, value uint16, weight exactWeight) uint16 {
	numerator := new(big.Int).Mul(new(big.Int).SetUint64(uint64(value)), &weight.numerator)
	result, err := roundUint16Even(numerator, &weight.denominator)
	if err != nil {
		t.Fatalf("big.Int weightedChannel(%d) rejected: %v", value, err)
	}
	return result
}

func referenceWeightedSumChannel(left, right uint16, leftWeight, rightWeight exactWeight) (uint16, error) {
	leftTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(left)), &leftWeight.numerator)
	leftTerm.Mul(leftTerm, &rightWeight.denominator)
	rightTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(right)), &rightWeight.numerator)
	rightTerm.Mul(rightTerm, &leftWeight.denominator)
	numerator := new(big.Int).Add(leftTerm, rightTerm)
	denominator := new(big.Int).Mul(&leftWeight.denominator, &rightWeight.denominator)
	return roundUint16Even(numerator, denominator)
}
