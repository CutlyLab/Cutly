package color

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"
)

func TestSRGBLUTCanonicalHashMonotonicityAndEndpoints(t *testing.T) {
	table := SRGB8ToLinear16LUT()
	if len(table) != 256 {
		t.Fatalf("LUT length = %d, want 256", len(table))
	}
	if table[0] != 0 || table[255] != 65535 {
		t.Fatalf("LUT endpoints = %d,%d, want 0,65535", table[0], table[255])
	}
	encoded := make([]byte, len(table)*2)
	for index, value := range table {
		binary.BigEndian.PutUint16(encoded[index*2:], value)
		if index > 0 && value <= table[index-1] {
			t.Fatalf("LUT is not strictly increasing at %d: %d <= %d", index, value, table[index-1])
		}
	}
	sum := sha256.Sum256(encoded)
	gotHash := "sha256:" + hex.EncodeToString(sum[:])
	if gotHash != LUTSHA256V1 || gotHash != Descriptor().LUTSHA256 {
		t.Fatalf("LUT hash = %q, want %q", gotHash, LUTSHA256V1)
	}

	// Discovery returns a copy: one caller cannot mutate the process-wide normative table.
	table[1] = 65535
	if got := SRGB8ToLinear16LUT()[1]; got != 20 {
		t.Fatalf("LUT discovery aliases normative storage: entry 1 = %d", got)
	}
}

func TestSRGBLUTEveryEntryMatchesExactOracle(t *testing.T) {
	table := SRGB8ToLinear16LUT()
	for code, got := range table {
		want := exactSRGBOracle(uint64(code))
		if got != want {
			t.Fatalf("LUT[%d] = %d, exact rational oracle wants %d", code, got, want)
		}
	}
}

// exactSRGBOracle uses integer arithmetic only and is deliberately independent of the production
// table. In the nonlinear branch it compares fifth powers, so no root, float, or approximation is
// involved when selecting the nearest-even uint16 value.
func exactSRGBOracle(code uint64) uint16 {
	if code <= 10 {
		// 65535 * (code/255) / 12.92 = (65535*25*code)/(255*323).
		numerator := new(big.Int).SetUint64(65535 * 25 * code)
		denominator := new(big.Int).SetUint64(255 * 323)
		return roundBigRatioEven(numerator, denominator)
	}

	low, high := uint64(0), uint64(65535)
	for low < high {
		middle := low + (high-low)/2
		comparison := compareNonlinearSRGBToHalf(code, middle)
		// middle is selected at an exact middle+1/2 only when it is the even neighbor.
		if comparison < 0 || comparison == 0 && middle&1 == 0 {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return uint16(low)
}

// compareNonlinearSRGBToHalf compares
//
//	65535 * ((40*code+561)/10761)^(12/5)
//
// with candidate+1/2. Raising both positive sides to the fifth power preserves order.
func compareNonlinearSRGBToHalf(code, candidate uint64) int {
	p := new(big.Int).SetUint64(40*code + 561)
	q := new(big.Int).SetUint64(10761)
	left := new(big.Int).Exp(new(big.Int).SetUint64(65535), big.NewInt(5), nil)
	left.Mul(left, new(big.Int).Exp(p, big.NewInt(12), nil))
	left.Lsh(left, 5)

	twiceHalf := new(big.Int).SetUint64(2*candidate + 1)
	right := new(big.Int).Exp(twiceHalf, big.NewInt(5), nil)
	right.Mul(right, new(big.Int).Exp(q, big.NewInt(12), nil))
	return left.Cmp(right)
}

func roundBigRatioEven(numerator, denominator *big.Int) uint16 {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := twiceRemainder.Cmp(denominator)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return uint16(quotient.Uint64())
}
