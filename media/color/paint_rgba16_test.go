package color

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/big"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestPaintRGBA16AllExpandedRGBA8CodesMatchFrozenLUT(t *testing.T) {
	var converter paintEOTFConverter
	for code := 0; code < 256; code++ {
		expanded := uint16(code * 257)
		if got, want := converter.linearize(expanded), srgb8ToLinear16[code]; got != want {
			t.Fatalf("expanded code %d (%d) = %d, frozen RGBA8 LUT wants %d", code, expanded, got, want)
		}
		pixel := PaintRGBA16ToPixel(RGBA16Input{
			R: expanded, G: uint16((255 - code) * 257), B: expanded, A: math.MaxUint16,
		})
		if pixel.R != srgb8ToLinear16[code] || pixel.G != srgb8ToLinear16[255-code] ||
			pixel.B != srgb8ToLinear16[code] || pixel.A != math.MaxUint16 {
			t.Fatalf("opaque expanded code %d produced %+v", code, pixel)
		}
	}
}

func TestPaintRGBA16TransferTableGoldenAndExactBounds(t *testing.T) {
	encoded := make([]byte, (math.MaxUint16+1)*2)
	var converter paintEOTFConverter
	var previous uint16
	oracle := newPaintTransferOracle()
	for code := 0; code <= math.MaxUint16; code++ {
		got := converter.linearize(uint16(code))
		binary.BigEndian.PutUint16(encoded[code*2:], got)
		if code == 0 && got != 0 || code == math.MaxUint16 && got != math.MaxUint16 {
			t.Fatalf("transfer endpoint code=%d value=%d", code, got)
		}
		if code > 0 && got < previous {
			t.Fatalf("transfer table decreases at code %d: %d < %d", code, got, previous)
		}
		previous = got
		if code <= 2650 {
			want := oracleRoundPaintRatioEven(uint64(code)*25, 323)
			if got != want {
				t.Fatalf("linear code %d = %d, exact rational oracle wants %d", code, got, want)
			}
			continue
		}
		if !oracle.roundsTo(uint16(code), got) {
			t.Fatalf("nonlinear code %d = %d is outside independent exact rounding bounds", code, got)
		}
	}
	sum := sha256.Sum256(encoded)
	gotHash := "sha256:" + hex.EncodeToString(sum[:])
	if gotHash != PaintTransferTableSHA256V1 || gotHash != PaintDescriptor().TransferTableSHA256 {
		t.Fatalf("paint transfer table hash = %q, want %q", gotHash, PaintTransferTableSHA256V1)
	}
}

func TestPaintRGBA16BreakpointEndpointsAndTieSelection(t *testing.T) {
	var converter paintEOTFConverter
	tests := []struct {
		code uint16
		want uint16
	}{
		{0, 0}, {1, 0}, {2649, 205}, {2650, 205}, {2651, 205}, {2652, 205},
		{32768, 14028}, {65534, 65533}, {65535, 65535},
	}
	for _, test := range tests {
		if got := converter.linearize(test.code); got != test.want {
			t.Fatalf("code %d = %d, want %d", test.code, got, test.want)
		}
	}
	if uint64(2650)*paintSRGBBreakpointDenominator > paintSRGBBreakpointNumerator*65535 ||
		uint64(2651)*paintSRGBBreakpointDenominator <= paintSRGBBreakpointNumerator*65535 {
		t.Fatal("integer breakpoint does not split codes 2650/2651")
	}
	if !paintRoundsAtLeast(0, 2) {
		t.Fatal("exact half with even upper neighbor did not choose upper")
	}
	if paintRoundsAtLeast(0, 3) {
		t.Fatal("exact half with odd upper neighbor did not choose lower")
	}
	if !paintRoundsAtLeast(1, 3) || paintRoundsAtLeast(-1, 2) {
		t.Fatal("strict threshold comparison is incorrect")
	}
}

func TestPaintRGBA16BigIntegerBounds(t *testing.T) {
	maxEncodedNumerator := new(big.Int).SetUint64(211 * 65535)
	maxLeft := new(big.Int).Exp(maxEncodedNumerator, big.NewInt(12), nil)
	maxLeft.Lsh(maxLeft, 5)
	rightScale := new(big.Int).Exp(big.NewInt(211), big.NewInt(12), nil)
	rightScale.Mul(rightScale, new(big.Int).Exp(big.NewInt(65535), big.NewInt(7), nil))
	maxThreshold := new(big.Int).SetUint64(2*65535 - 1)
	maxRight := new(big.Int).Exp(maxThreshold, big.NewInt(5), nil)
	maxRight.Mul(maxRight, rightScale)
	if maxEncodedNumerator.BitLen() != 24 || rightScale.BitLen() != 205 ||
		maxLeft.BitLen() != 290 || maxRight.BitLen() != 290 {
		t.Fatalf("bit bounds numerator=%d scale=%d left=%d right=%d",
			maxEncodedNumerator.BitLen(), rightScale.BitLen(), maxLeft.BitLen(), maxRight.BitLen())
	}
}

func TestPaintRGBA16AlphaPremultiplicationAndValueIsolation(t *testing.T) {
	if got := PaintRGBA16ToPixel(RGBA16Input{R: 65535, G: 32768, B: 1, A: 0}); got != (cpucompositor.Pixel{}) {
		t.Fatalf("alpha zero leaked hidden RGB: %+v", got)
	}

	linearSamples := []uint16{0, 1, 205, 14028, 32768, 65534, 65535}
	alphaSamples := []uint16{0, 1, 2, 32767, 32768, 32769, 65534, 65535}
	for _, linear := range linearSamples {
		for _, alpha := range alphaSamples {
			want := oracleRoundPaintRatioEven(uint64(linear)*uint64(alpha), 65535)
			if got := premultiplyPaintLinear16(linear, alpha); got != want {
				t.Fatalf("premultiply %d*%d/65535 = %d, want %d", linear, alpha, got, want)
			}
		}
	}

	input := RGBA16Input{R: 61000, G: 42000, B: 17000, A: 32768}
	got := PaintRGBA16ToPixel(input)
	var converter paintEOTFConverter
	want := cpucompositor.Pixel{
		R: oracleRoundPaintRatioEven(uint64(converter.linearize(input.R))*uint64(input.A), 65535),
		G: oracleRoundPaintRatioEven(uint64(converter.linearize(input.G))*uint64(input.A), 65535),
		B: oracleRoundPaintRatioEven(uint64(converter.linearize(input.B))*uint64(input.A), 65535),
		A: input.A,
	}
	if got != want || got.R > got.A || got.G > got.A || got.B > got.A {
		t.Fatalf("paint conversion = %+v, want valid %+v", got, want)
	}
	input = RGBA16Input{}
	if got != want {
		t.Fatal("caller value mutation changed an already returned Pixel")
	}
}

func TestPaintRGBA16PremultiplicationExhaustiveAxes(t *testing.T) {
	// 65535 is odd, so value*alpha/65535 can never land on an exact half. Adding
	// floor(65535/2) is therefore an independent closed-form nearest-integer oracle.
	linearAxes := []uint16{0, 1, 2, 205, 14028, 32767, 32768, 32769, 65534, 65535}
	alphaAxes := []uint16{0, 1, 2, 32767, 32768, 32769, 65534, 65535}
	check := func(linear, alpha uint16) uint16 {
		want := uint16((uint64(linear)*uint64(alpha) + 32767) / 65535)
		got := premultiplyPaintLinear16(linear, alpha)
		if got != want || got > alpha {
			t.Fatalf("premultiply %d*%d/65535 = %d, want %d and <= alpha", linear, alpha, got, want)
		}
		return got
	}
	for _, linear := range linearAxes {
		var previous uint16
		for alpha := 0; alpha <= math.MaxUint16; alpha++ {
			got := check(linear, uint16(alpha))
			if alpha > 0 && got < previous {
				t.Fatalf("premultiply decreases with alpha at linear=%d alpha=%d: %d < %d", linear, alpha, got, previous)
			}
			previous = got
		}
	}
	for _, alpha := range alphaAxes {
		var previous uint16
		for linear := 0; linear <= math.MaxUint16; linear++ {
			got := check(uint16(linear), alpha)
			if linear > 0 && got < previous {
				t.Fatalf("premultiply decreases with linear at alpha=%d linear=%d: %d < %d", alpha, linear, got, previous)
			}
			previous = got
		}
	}
}

func TestPaintRGBA16ConcurrentDeterminism(t *testing.T) {
	input := RGBA16Input{R: 60001, G: 40003, B: 20011, A: 50021}
	want := PaintRGBA16ToPixel(input)
	const workers = 16
	var group sync.WaitGroup
	errors := make(chan cpucompositor.Pixel, workers)
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 25; iteration++ {
				if got := PaintRGBA16ToPixel(input); got != want {
					errors <- got
					return
				}
			}
		}()
	}
	group.Wait()
	close(errors)
	for got := range errors {
		t.Fatalf("concurrent result drift: got %+v want %+v", got, want)
	}
}

func FuzzPaintRGBA16ToPixelNeverPanics(f *testing.F) {
	f.Add(uint16(0), uint16(0), uint16(0), uint16(0))
	f.Add(uint16(65535), uint16(32768), uint16(2651), uint16(65535))
	f.Add(uint16(60001), uint16(40003), uint16(20011), uint16(50021))
	f.Fuzz(func(t *testing.T, r, g, b, a uint16) {
		input := RGBA16Input{R: r, G: g, B: b, A: a}
		first := PaintRGBA16ToPixel(input)
		second := PaintRGBA16ToPixel(input)
		if first != second {
			t.Fatalf("nondeterministic result: %+v != %+v", first, second)
		}
		if first.A != a || first.R > a || first.G > a || first.B > a {
			t.Fatalf("invalid premultiplied result for %+v: %+v", input, first)
		}
		if a == 0 && first != (cpucompositor.Pixel{}) {
			t.Fatalf("alpha zero leaked color: %+v", first)
		}
	})
}

type paintTransferOracle struct {
	rightScale big.Int
}

func newPaintTransferOracle() paintTransferOracle {
	var result paintTransferOracle
	base211, base65535 := big.NewInt(211), big.NewInt(65535)
	result.rightScale.Exp(base211, big.NewInt(12), nil)
	result.rightScale.Mul(&result.rightScale, new(big.Int).Exp(base65535, big.NewInt(7), nil))
	return result
}

func (oracle *paintTransferOracle) roundsTo(code, rounded uint16) bool {
	encodedNumerator := new(big.Int).SetUint64(uint64(code)*200 + 11*65535)
	left := new(big.Int).Exp(encodedNumerator, big.NewInt(12), nil)
	left.Lsh(left, 5)
	if rounded > 0 {
		comparison := left.Cmp(oracle.boundary(uint32(rounded)))
		if comparison < 0 || comparison == 0 && rounded&1 == 1 {
			return false
		}
	}
	if rounded < math.MaxUint16 {
		upper := uint32(rounded) + 1
		comparison := left.Cmp(oracle.boundary(upper))
		if comparison > 0 || comparison == 0 && upper&1 == 0 {
			return false
		}
	}
	return true
}

func (oracle *paintTransferOracle) boundary(upper uint32) *big.Int {
	value := new(big.Int).SetUint64(uint64(upper)*2 - 1)
	value.Exp(value, big.NewInt(5), nil)
	return value.Mul(value, &oracle.rightScale)
}

func oracleRoundPaintRatioEven(numerator, denominator uint64) uint16 {
	numeratorBig := new(big.Int).SetUint64(numerator)
	denominatorBig := new(big.Int).SetUint64(denominator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numeratorBig, denominatorBig, remainder)
	doubled := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := doubled.Cmp(denominatorBig)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return uint16(quotient.Uint64())
}
