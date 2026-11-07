package color

import (
	"math/big"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	paintSRGBBreakpointNumerator   uint64 = 809
	paintSRGBBreakpointDenominator uint64 = 20000
	paintSRGBLinearDivisor         uint64 = 323
	paintSRGBLinearMultiplier      uint64 = 25
)

// RGBA16Input is one unpremultiplied sRGB paint color. Alpha is linear and all channels use the
// complete uint16 code range. It is a scalar paint primitive, not an image or Surface contract.
type RGBA16Input struct {
	R uint16 `json:"r"`
	G uint16 `json:"g"`
	B uint16 `json:"b"`
	A uint16 `json:"a"`
}

// PaintRGBA16ToPixel converts one straight-alpha sRGB RGBA16 paint to the compositor's exact
// linear-light BT.709/D65 premultiplied Pixel. The operation is pure, fixed-bounded and uses no
// process-wide mutable lookup or cache.
func PaintRGBA16ToPixel(input RGBA16Input) cpucompositor.Pixel {
	if input.A == 0 {
		return cpucompositor.Pixel{}
	}
	var converter paintEOTFConverter
	linearR := converter.linearize(input.R)
	linearG := linearR
	if input.G != input.R {
		linearG = converter.linearize(input.G)
	}
	linearB := linearR
	if input.B != input.R {
		if input.B == input.G {
			linearB = linearG
		} else {
			linearB = converter.linearize(input.B)
		}
	}
	return cpucompositor.Pixel{
		R: premultiplyPaintLinear16(linearR, input.A),
		G: premultiplyPaintLinear16(linearG, input.A),
		B: premultiplyPaintLinear16(linearB, input.A),
		A: input.A,
	}
}

// paintEOTFConverter is deliberately caller-local. It lazily constructs the nonlinear comparison
// scale only if a channel is above the exact sRGB breakpoint and never escapes one conversion.
type paintEOTFConverter struct {
	initialized bool
	rightScale  big.Int
}

func (converter *paintEOTFConverter) linearize(code uint16) uint16 {
	if uint64(code)*paintSRGBBreakpointDenominator <= paintSRGBBreakpointNumerator*65535 {
		return roundRatioEven(uint64(code)*paintSRGBLinearMultiplier, paintSRGBLinearDivisor)
	}
	if !converter.initialized {
		converter.initialize()
	}
	return converter.linearizeNonlinear(code)
}

func (converter *paintEOTFConverter) initialize() {
	var base, exponent, factor big.Int
	base.SetUint64(211)
	exponent.SetUint64(12)
	converter.rightScale.Exp(&base, &exponent, nil)
	base.SetUint64(65535)
	exponent.SetUint64(7)
	factor.Exp(&base, &exponent, nil)
	converter.rightScale.Mul(&converter.rightScale, &factor)
	converter.initialized = true
}

// linearizeNonlinear rounds
//
//	65535 * (((code/65535)+11/200)/(211/200))^(12/5)
//
// once to uint16. To decide whether the rounded answer is at least candidate, compare the exact
// value with candidate-1/2. Raising positive sides to the fifth power and cancelling common
// factors gives the integer-only comparison
//
//	32*(200*code+11*65535)^12
//
// against
//
//	(2*candidate-1)^5 * 211^12 * 65535^7.
//
// Sixteen binary decisions cover all 65536 possible results. At equality, candidate is selected
// exactly when it is the even neighbor.
func (converter *paintEOTFConverter) linearizeNonlinear(code uint16) uint16 {
	encodedNumerator := uint64(code)*200 + 11*65535
	var base, exponent, left big.Int
	base.SetUint64(encodedNumerator)
	exponent.SetUint64(12)
	left.Exp(&base, &exponent, nil)
	left.Lsh(&left, 5)

	low, high := uint32(0), uint32(65535)
	var threshold, right big.Int
	exponent.SetUint64(5)
	for low < high {
		candidate := low + (high-low+1)/2
		threshold.SetUint64(uint64(candidate)*2 - 1)
		right.Exp(&threshold, &exponent, nil)
		right.Mul(&right, &converter.rightScale)
		comparison := left.Cmp(&right)
		if paintRoundsAtLeast(comparison, candidate) {
			low = candidate
		} else {
			high = candidate - 1
		}
	}
	return uint16(low)
}

func paintRoundsAtLeast(comparison int, upperCandidate uint32) bool {
	return comparison > 0 || comparison == 0 && upperCandidate&1 == 0
}

func premultiplyPaintLinear16(value, alpha uint16) uint16 {
	return roundRatioEven(uint64(value)*uint64(alpha), 65535)
}
