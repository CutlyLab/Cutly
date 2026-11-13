package cpuraster

import (
	"math"
	"math/big"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// maxPlaneDenominator bounds each rational-plane denominator of the fixed-width sampler. With
// both denominators at most 2^22, the derived grid denominators (2x), tap-weight products
// (4 * denX * denY) and the per-channel sums (totalDen * 65535) all stay far inside uint64.
const maxPlaneDenominator = int64(1) << 22

// maxPlaneNumerator bounds every plane numerator excursion (origin plus full stepping) so the
// grid numerator 2*current-den never leaves the int64 range.
const maxPlaneNumerator = int64(1) << 62

// fastPlane is one rational plane resolved to fixed-width parts: value = numerator/denominator
// stepped by column (per target pixel) and row (per target line).
type fastPlane struct {
	denominator int64
	origin      int64
	column      int64
	row         int64
}

// fastSampler is the fixed-width equivalent of exactSampler: identical tap selection, weights
// and round-half-to-even quantization, computed with int64/uint64 arithmetic only. It is
// admitted only when every plane component and the full stepping excursion fit the checked
// bounds; anything else keeps the big.Int sampler.
type fastSampler struct {
	xPlane fastPlane
	yPlane fastPlane

	rowX, rowY         int64
	currentX, currentY int64
	gridDenX, gridDenY int64
	totalDen           uint64

	cropMinX, cropMaxX int64
	cropMinY, cropMaxY int64
}

// newFastSamplerSized admits the fixed-width sampler for the given planes and crop bounds, or
// returns nil when any component or derived bound exceeds the checked limits.
func newFastSamplerSized(xPlane, yPlane rationalPlane, sourceWidth, sourceHeight int, cropMinX, cropMaxX, cropMinY, cropMaxY int64) *fastSampler {
	fastX, ok := admitPlane(xPlane, sourceWidth, sourceHeight)
	if !ok {
		return nil
	}
	fastY, ok := admitPlane(yPlane, sourceWidth, sourceHeight)
	if !ok {
		return nil
	}
	gridDenX := fastX.denominator * 2
	gridDenY := fastY.denominator * 2
	if gridDenX < 0 || gridDenY < 0 {
		return nil
	}
	totalDen := uint64(gridDenX) * uint64(gridDenY)
	if totalDen == 0 || totalDen > uint64(math.MaxInt64) {
		return nil
	}
	return &fastSampler{
		xPlane: fastX, yPlane: fastY,
		rowX: fastX.origin, rowY: fastY.origin,
		gridDenX: gridDenX, gridDenY: gridDenY, totalDen: totalDen,
		cropMinX: cropMinX, cropMaxX: cropMaxX, cropMinY: cropMinY, cropMaxY: cropMaxY,
	}
}

// admitPlane resolves one plane when its denominator is small, its components fit int64, and
// the maximum stepping excursion (origin advanced across the complete target grid) cannot
// overflow.
func admitPlane(plane rationalPlane, width, height int) (fastPlane, bool) {
	if width <= 0 || height <= 0 {
		return fastPlane{}, false
	}
	denominator := plane.denominator
	if !denominator.IsInt64() || denominator.Sign() <= 0 || denominator.Int64() > maxPlaneDenominator {
		return fastPlane{}, false
	}
	var bound big.Int
	bound.Mul(big.NewInt(int64(width-1)), new(big.Int).Abs(&plane.column))
	var rowBound big.Int
	rowBound.Mul(big.NewInt(int64(height-1)), new(big.Int).Abs(&plane.row))
	bound.Add(&bound, &rowBound)
	bound.Add(&bound, new(big.Int).Abs(&plane.origin))
	if !bound.IsInt64() || bound.Int64() > maxPlaneNumerator {
		return fastPlane{}, false
	}
	if !plane.origin.IsInt64() || !plane.column.IsInt64() || !plane.row.IsInt64() {
		return fastPlane{}, false
	}
	return fastPlane{
		denominator: denominator.Int64(),
		origin:      plane.origin.Int64(),
		column:      plane.column.Int64(),
		row:         plane.row.Int64(),
	}, true
}

func (sampler *fastSampler) beginRow() {
	sampler.currentX = sampler.rowX
	sampler.currentY = sampler.rowY
}

func (sampler *fastSampler) advanceColumn() {
	sampler.currentX += sampler.xPlane.column
	sampler.currentY += sampler.yPlane.column
}

func (sampler *fastSampler) advanceRow() {
	sampler.rowX += sampler.xPlane.row
	sampler.rowY += sampler.yPlane.row
}

func (sampler *fastSampler) sample(view cpucompositor.ReadView) (cpucompositor.Pixel, error) {
	// Grid coordinate is source edge coordinate minus one half, in the doubled denominator —
	// the exact sampler's gridNum/gridDen pair verbatim.
	gridNumX := 2*sampler.currentX - sampler.xPlane.denominator
	gridNumY := 2*sampler.currentY - sampler.yPlane.denominator
	floorX, remX := euclideanDivMod(gridNumX, sampler.gridDenX)
	floorY, remY := euclideanDivMod(gridNumY, sampler.gridDenY)
	wx0 := uint64(sampler.gridDenX - remX)
	wy0 := uint64(sampler.gridDenY - remY)
	remXU, remYU := uint64(remX), uint64(remY)
	w00 := wx0 * wy0
	w10 := remXU * wy0
	w01 := wx0 * remYU
	w11 := remXU * remYU

	var sums [4]uint64
	if floorX >= math.MinInt64+1 && floorY >= math.MinInt64+1 {
		if err := sampler.addTap(view, floorX, floorY, w00, &sums); err != nil {
			return cpucompositor.Pixel{}, err
		}
		if floorX < math.MaxInt64 {
			if err := sampler.addTap(view, floorX+1, floorY, w10, &sums); err != nil {
				return cpucompositor.Pixel{}, err
			}
		}
		if floorY < math.MaxInt64 {
			if err := sampler.addTap(view, floorX, floorY+1, w01, &sums); err != nil {
				return cpucompositor.Pixel{}, err
			}
			if floorX < math.MaxInt64 {
				if err := sampler.addTap(view, floorX+1, floorY+1, w11, &sums); err != nil {
					return cpucompositor.Pixel{}, err
				}
			}
		}
	}
	channels := [4]uint16{}
	for index := range sums {
		value, err := roundHalfEvenUint16(sums[index], sampler.totalDen)
		if err != nil {
			return cpucompositor.Pixel{}, err
		}
		channels[index] = value
	}
	result := cpucompositor.Pixel{R: channels[0], G: channels[1], B: channels[2], A: channels[3]}
	if result.R > result.A || result.G > result.A || result.B > result.A {
		return cpucompositor.Pixel{}, ErrSampleFailed
	}
	return result, nil
}

func (sampler *fastSampler) addTap(view cpucompositor.ReadView, x, y int64, weight uint64, sums *[4]uint64) error {
	if weight == 0 || x < sampler.cropMinX || x >= sampler.cropMaxX || y < sampler.cropMinY || y >= sampler.cropMaxY {
		return nil
	}
	pixel, err := view.Pixel(int(x), int(y))
	if err != nil {
		return err
	}
	sums[0] += weight * uint64(pixel.R)
	sums[1] += weight * uint64(pixel.G)
	sums[2] += weight * uint64(pixel.B)
	sums[3] += weight * uint64(pixel.A)
	return nil
}

// euclideanDivMod returns the Euclidean floor quotient and non-negative remainder, matching
// big.Int.DivMod for every int64 input over a positive divisor.
func euclideanDivMod(numerator, denominator int64) (int64, int64) {
	quotient := numerator / denominator
	remainder := numerator % denominator
	if remainder < 0 {
		quotient--
		remainder += denominator
	}
	return quotient, remainder
}

// roundHalfEvenUint16 is the sampler's exact-rounding step: round-half-to-even of the
// non-negative weighted sum against the common denominator, failing when the result could not
// be a channel value.
func roundHalfEvenUint16(numerator, denominator uint64) (uint16, error) {
	quotient := numerator / denominator
	remainder := numerator % denominator
	half := denominator / 2
	aboveHalf := remainder > half
	exactHalf := denominator&1 == 0 && remainder == half
	if aboveHalf || exactHalf && quotient&1 == 1 {
		quotient++
	}
	if quotient > math.MaxUint16 {
		return 0, ErrSampleFailed
	}
	return uint16(quotient), nil
}
