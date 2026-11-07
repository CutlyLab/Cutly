package cpuraster

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// Rasterize inverse-maps one already-normalized immutable source Surface through the evaluated
// crop/transform into a complete target Surface. Opacity and layer compositing intentionally run
// after this stage in the reference compositor.
func Rasterize(
	ctx context.Context,
	source *cpucompositor.Surface,
	width int,
	height int,
	transform evaluator.Transform,
	crop evaluator.ExactRect,
	limits Limits,
) (*cpucompositor.Surface, error) {
	if ctx == nil {
		return nil, rasterError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, err
	}
	normalizedLimits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	view, err := source.View()
	if err != nil {
		return nil, rasterError(CodeInputInvalid, "source", errors.Join(ErrInputInvalid, err))
	}
	shape, err := validateShape(view, width, height, normalizedLimits)
	if err != nil {
		return nil, err
	}
	geometry, err := compileGeometry(view.Width(), view.Height(), transform, crop, normalizedLimits.MaxExactBits)
	if err != nil {
		return nil, err
	}
	if err := contextFailure(ctx, "pixels"); err != nil {
		return nil, err
	}
	pixels, err := allocatePixels(shape.pixelCount)
	if err != nil {
		return nil, err
	}

	if geometry.isPixelIdentity(view, width, height) {
		for index := range pixels {
			if index&63 == 0 {
				if err := pixelContextFailure(ctx, index); err != nil {
					return nil, err
				}
			}
			pixel, pixelErr := view.Pixel(index%width, index/width)
			if pixelErr != nil {
				return nil, rasterError(CodeSampleFailed, fmt.Sprintf("pixels/%d", index), errors.Join(ErrSampleFailed, pixelErr))
			}
			pixels[index] = pixel
		}
	} else if geometry.scaleX.Sign() != 0 && geometry.scaleY.Sign() != 0 {
		sampler, samplerErr := newExactSampler(geometry, view.Width(), view.Height())
		if samplerErr != nil {
			return nil, samplerErr
		}
		processed := 0
		for y := 0; y < height; y++ {
			sampler.beginRow()
			for x := 0; x < width; x++ {
				if processed&63 == 0 {
					if err := pixelContextFailure(ctx, processed); err != nil {
						return nil, err
					}
				}
				pixel, sampleErr := sampler.sample(view)
				if sampleErr != nil {
					return nil, rasterError(CodeSampleFailed, fmt.Sprintf("pixels/%d", processed), errors.Join(ErrSampleFailed, sampleErr))
				}
				pixels[processed] = pixel
				sampler.advanceColumn()
				processed++
			}
			sampler.advanceRow()
		}
	}
	if err := contextFailure(ctx, "surface"); err != nil {
		return nil, err
	}
	result, err := cpucompositor.NewSurfaceFromPixelsWithLimits(width, height, pixels, cpucompositor.Limits{
		MaxWidth: width, MaxHeight: height, MaxBytes: shape.outputBytes,
	})
	if err != nil {
		return nil, rasterError(CodeSurfaceFailed, "surface", errors.Join(ErrSurfaceFailed, err))
	}
	if err := contextFailure(ctx, "surface"); err != nil {
		return nil, err
	}
	return result, nil
}

func (geometry compiledGeometry) isPixelIdentity(view cpucompositor.ReadView, width, height int) bool {
	return width == view.Width() && height == view.Height() &&
		geometry.positionX.Sign() == 0 && geometry.positionY.Sign() == 0 &&
		geometry.scaleX.Cmp(ratOne) == 0 && geometry.scaleY.Cmp(ratOne) == 0 &&
		geometry.sine.Sign() == 0 && geometry.cosine.Cmp(ratOne) == 0 &&
		geometry.cropLeft.Sign() == 0 && geometry.cropTop.Sign() == 0 &&
		geometry.cropRight.Cmp(new(big.Rat).SetInt64(int64(view.Width()))) == 0 &&
		geometry.cropBottom.Cmp(new(big.Rat).SetInt64(int64(view.Height()))) == 0
}

type checkedShape struct {
	pixelCount  int
	outputBytes uint64
}

func validateShape(view cpucompositor.ReadView, width, height int, limits Limits) (checkedShape, error) {
	if view.Width() <= 0 || view.Height() <= 0 || width <= 0 || height <= 0 {
		return checkedShape{}, rasterError(CodeInputInvalid, "dimensions", ErrInputInvalid)
	}
	if view.Width() > limits.MaxWidth || view.Height() > limits.MaxHeight || width > limits.MaxWidth || height > limits.MaxHeight {
		return checkedShape{}, rasterError(CodeResourceLimit, "dimensions", ErrResourceLimit)
	}
	unsignedWidth, unsignedHeight := uint64(width), uint64(height)
	if unsignedHeight != 0 && unsignedWidth > math.MaxUint64/unsignedHeight {
		return checkedShape{}, rasterError(CodeResourceLimit, "pixel_count", ErrResourceLimit)
	}
	count := unsignedWidth * unsignedHeight
	if count > limits.MaxOutputPixels || count > HardMaxPixels || count > uint64(maxInt()) {
		return checkedShape{}, rasterError(CodeResourceLimit, "pixel_count", ErrResourceLimit)
	}
	if count > math.MaxUint64/cpucompositor.BytesPerPixel {
		return checkedShape{}, rasterError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	outputBytes := count * cpucompositor.BytesPerPixel
	if outputBytes > math.MaxUint64/2 || outputBytes*2 > limits.MaxWorkingBytes || outputBytes > cpucompositor.HardMaxSurfaceBytes {
		return checkedShape{}, rasterError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	return checkedShape{pixelCount: int(count), outputBytes: outputBytes}, nil
}

type compiledGeometry struct {
	positionX           *big.Rat
	positionY           *big.Rat
	scaleX              *big.Rat
	scaleY              *big.Rat
	anchorX             *big.Rat
	anchorY             *big.Rat
	sine                *big.Rat
	cosine              *big.Rat
	rotationDeterminant *big.Rat
	mapOriginX          *big.Rat
	mapOriginY          *big.Rat
	mapColumnX          *big.Rat
	mapColumnY          *big.Rat
	mapRowX             *big.Rat
	mapRowY             *big.Rat
	cropLeft            *big.Rat
	cropTop             *big.Rat
	cropRight           *big.Rat
	cropBottom          *big.Rat
}

func compileGeometry(sourceWidth, sourceHeight int, transform evaluator.Transform, crop evaluator.ExactRect, maxBits int) (compiledGeometry, error) {
	values := []struct {
		path  string
		value evaluator.ExactRational
	}{
		{"transform.position.x", transform.Position.X}, {"transform.position.y", transform.Position.Y},
		{"transform.scale.x", transform.Scale.X}, {"transform.scale.y", transform.Scale.Y},
		{"transform.rotation_degrees", transform.RotationDegrees},
		{"transform.anchor.x", transform.Anchor.X}, {"transform.anchor.y", transform.Anchor.Y},
		{"crop.x", crop.X}, {"crop.y", crop.Y}, {"crop.width", crop.Width}, {"crop.height", crop.Height},
	}
	decoded := make(map[string]*big.Rat, len(values))
	for _, item := range values {
		value, err := exactRational(item.value, item.path, maxBits)
		if err != nil {
			return compiledGeometry{}, err
		}
		decoded[item.path] = value
	}
	for _, path := range []string{"transform.anchor.x", "transform.anchor.y", "crop.x", "crop.y", "crop.width", "crop.height"} {
		if decoded[path].Cmp(ratZero) < 0 || decoded[path].Cmp(ratOne) > 0 {
			return compiledGeometry{}, rasterError(CodeGeometryInvalid, path, ErrGeometryInvalid)
		}
	}
	if decoded["crop.width"].Sign() <= 0 || decoded["crop.height"].Sign() <= 0 ||
		new(big.Rat).Add(decoded["crop.x"], decoded["crop.width"]).Cmp(ratOne) > 0 ||
		new(big.Rat).Add(decoded["crop.y"], decoded["crop.height"]).Cmp(ratOne) > 0 {
		return compiledGeometry{}, rasterError(CodeGeometryInvalid, "crop", ErrGeometryInvalid)
	}
	sine, cosine, err := sinCosQ48(decoded["transform.rotation_degrees"])
	if err != nil {
		return compiledGeometry{}, err
	}
	rotationDeterminant := new(big.Rat).Mul(cosine, cosine)
	rotationDeterminant.Add(rotationDeterminant, new(big.Rat).Mul(sine, sine))
	if rotationDeterminant.Sign() == 0 {
		return compiledGeometry{}, rasterError(CodeGeometryInvalid, "transform.rotation_degrees", ErrGeometryInvalid)
	}
	widthRat := new(big.Rat).SetInt64(int64(sourceWidth))
	heightRat := new(big.Rat).SetInt64(int64(sourceHeight))
	cropLeft := new(big.Rat).Mul(decoded["crop.x"], widthRat)
	cropTop := new(big.Rat).Mul(decoded["crop.y"], heightRat)
	cropRight := new(big.Rat).Mul(new(big.Rat).Add(decoded["crop.x"], decoded["crop.width"]), widthRat)
	cropBottom := new(big.Rat).Mul(new(big.Rat).Add(decoded["crop.y"], decoded["crop.height"]), heightRat)
	result := compiledGeometry{
		positionX: decoded["transform.position.x"], positionY: decoded["transform.position.y"],
		scaleX: decoded["transform.scale.x"], scaleY: decoded["transform.scale.y"],
		anchorX: new(big.Rat).Mul(decoded["transform.anchor.x"], widthRat),
		anchorY: new(big.Rat).Mul(decoded["transform.anchor.y"], heightRat),
		sine:    sine, cosine: cosine, rotationDeterminant: rotationDeterminant,
		cropLeft: cropLeft, cropTop: cropTop, cropRight: cropRight, cropBottom: cropBottom,
	}
	if result.scaleX.Sign() != 0 && result.scaleY.Sign() != 0 {
		// Inverse affine coefficients. Destination centers are x+1/2,y+1/2, so mapOrigin
		// includes both half-pixel terms and the translation/anchor offset. Raster iteration can
		// then advance with exact rational column/row increments instead of rebuilding the
		// complete matrix expression for every pixel.
		result.mapColumnX = new(big.Rat).Quo(new(big.Rat).Set(cosine), rotationDeterminant)
		result.mapColumnX.Quo(result.mapColumnX, result.scaleX)
		result.mapRowX = new(big.Rat).Quo(new(big.Rat).Set(sine), rotationDeterminant)
		result.mapRowX.Quo(result.mapRowX, result.scaleX)
		result.mapColumnY = new(big.Rat).Quo(new(big.Rat).Neg(new(big.Rat).Set(sine)), rotationDeterminant)
		result.mapColumnY.Quo(result.mapColumnY, result.scaleY)
		result.mapRowY = new(big.Rat).Quo(new(big.Rat).Set(cosine), rotationDeterminant)
		result.mapRowY.Quo(result.mapRowY, result.scaleY)

		translatedAnchorX := new(big.Rat).Add(result.positionX, result.anchorX)
		translatedAnchorY := new(big.Rat).Add(result.positionY, result.anchorY)
		offsetX := new(big.Rat).Sub(new(big.Rat).Set(result.anchorX), new(big.Rat).Mul(result.mapColumnX, translatedAnchorX))
		offsetX.Sub(offsetX, new(big.Rat).Mul(result.mapRowX, translatedAnchorY))
		offsetY := new(big.Rat).Sub(new(big.Rat).Set(result.anchorY), new(big.Rat).Mul(result.mapColumnY, translatedAnchorX))
		offsetY.Sub(offsetY, new(big.Rat).Mul(result.mapRowY, translatedAnchorY))
		result.mapOriginX = new(big.Rat).Add(offsetX, new(big.Rat).Mul(result.mapColumnX, ratHalf))
		result.mapOriginX.Add(result.mapOriginX, new(big.Rat).Mul(result.mapRowX, ratHalf))
		result.mapOriginY = new(big.Rat).Add(offsetY, new(big.Rat).Mul(result.mapColumnY, ratHalf))
		result.mapOriginY.Add(result.mapOriginY, new(big.Rat).Mul(result.mapRowY, ratHalf))
	}
	return result, nil
}

func exactRational(value evaluator.ExactRational, path string, maxBits int) (*big.Rat, error) {
	numerator, denominator := value.Numerator(), value.Denominator()
	if denominator.Sign() <= 0 {
		return nil, rasterError(CodeGeometryInvalid, path, ErrGeometryInvalid)
	}
	if numerator.BitLen() > maxBits || denominator.BitLen() > maxBits {
		return nil, rasterError(CodeResourceLimit, path, ErrResourceLimit)
	}
	return new(big.Rat).SetFrac(numerator, denominator), nil
}

type rationalPlane struct {
	denominator big.Int
	origin      big.Int
	column      big.Int
	row         big.Int
}

func newRationalPlane(origin, column, row *big.Rat) (rationalPlane, error) {
	if origin == nil || column == nil || row == nil || origin.Denom().Sign() <= 0 || column.Denom().Sign() <= 0 || row.Denom().Sign() <= 0 {
		return rationalPlane{}, ErrGeometryInvalid
	}
	var result rationalPlane
	result.denominator.Set(origin.Denom())
	result.denominator.Set(lcmPositive(&result.denominator, column.Denom()))
	result.denominator.Set(lcmPositive(&result.denominator, row.Denom()))
	setScaledNumerator(&result.origin, origin, &result.denominator)
	setScaledNumerator(&result.column, column, &result.denominator)
	setScaledNumerator(&result.row, row, &result.denominator)
	return result, nil
}

func lcmPositive(left, right *big.Int) *big.Int {
	var gcd, quotient big.Int
	gcd.GCD(nil, nil, left, right)
	quotient.Quo(left, &gcd)
	return new(big.Int).Mul(&quotient, right)
}

func setScaledNumerator(destination *big.Int, value *big.Rat, denominator *big.Int) {
	var factor big.Int
	factor.Quo(denominator, value.Denom())
	destination.Mul(value.Num(), &factor)
}

type exactSampler struct {
	xPlane rationalPlane
	yPlane rationalPlane

	rowX     big.Int
	rowY     big.Int
	currentX big.Int
	currentY big.Int
	gridDenX big.Int
	gridDenY big.Int
	totalDen big.Int

	cropMinX int64
	cropMaxX int64
	cropMinY int64
	cropMaxY int64

	gridNumX  big.Int
	gridNumY  big.Int
	floorX    big.Int
	floorY    big.Int
	remX      big.Int
	remY      big.Int
	wx0       big.Int
	wy0       big.Int
	w00       big.Int
	w10       big.Int
	w01       big.Int
	w11       big.Int
	sums      [4]big.Int
	temporary big.Int
	channel   big.Int
	quotient  big.Int
	remainder big.Int
	twice     big.Int
}

func newExactSampler(geometry compiledGeometry, sourceWidth, sourceHeight int) (*exactSampler, error) {
	if geometry.mapOriginX == nil || geometry.mapOriginY == nil || geometry.mapColumnX == nil || geometry.mapColumnY == nil ||
		geometry.mapRowX == nil || geometry.mapRowY == nil {
		return nil, rasterError(CodeGeometryInvalid, "transform", ErrGeometryInvalid)
	}
	xPlane, err := newRationalPlane(geometry.mapOriginX, geometry.mapColumnX, geometry.mapRowX)
	if err != nil {
		return nil, rasterError(CodeGeometryInvalid, "transform.x", err)
	}
	yPlane, err := newRationalPlane(geometry.mapOriginY, geometry.mapColumnY, geometry.mapRowY)
	if err != nil {
		return nil, rasterError(CodeGeometryInvalid, "transform.y", err)
	}
	minX, maxX, err := cropTapRange(geometry.cropLeft, geometry.cropRight, sourceWidth)
	if err != nil {
		return nil, rasterError(CodeGeometryInvalid, "crop.x", err)
	}
	minY, maxY, err := cropTapRange(geometry.cropTop, geometry.cropBottom, sourceHeight)
	if err != nil {
		return nil, rasterError(CodeGeometryInvalid, "crop.y", err)
	}
	result := &exactSampler{xPlane: xPlane, yPlane: yPlane, cropMinX: minX, cropMaxX: maxX, cropMinY: minY, cropMaxY: maxY}
	result.rowX.Set(&xPlane.origin)
	result.rowY.Set(&yPlane.origin)
	result.gridDenX.Lsh(new(big.Int).Set(&xPlane.denominator), 1)
	result.gridDenY.Lsh(new(big.Int).Set(&yPlane.denominator), 1)
	result.totalDen.Mul(&result.gridDenX, &result.gridDenY)
	if result.totalDen.Sign() <= 0 {
		return nil, rasterError(CodeGeometryInvalid, "transform.denominator", ErrGeometryInvalid)
	}
	return result, nil
}

func cropTapRange(left, right *big.Rat, maximum int) (int64, int64, error) {
	if left == nil || right == nil || maximum < 0 {
		return 0, 0, ErrGeometryInvalid
	}
	shiftedLeft := new(big.Rat).Sub(new(big.Rat).Set(left), ratHalf)
	shiftedRight := new(big.Rat).Sub(new(big.Rat).Set(right), ratHalf)
	minimum := ceilRat(shiftedLeft)
	maximumExclusive := ceilRat(shiftedRight)
	zero, limit := big.NewInt(0), new(big.Int).SetInt64(int64(maximum))
	if minimum.Cmp(zero) < 0 {
		minimum.SetInt64(0)
	}
	if maximumExclusive.Cmp(limit) > 0 {
		maximumExclusive.Set(limit)
	}
	if !minimum.IsInt64() || !maximumExclusive.IsInt64() || minimum.Cmp(maximumExclusive) > 0 {
		return 0, 0, ErrGeometryInvalid
	}
	return minimum.Int64(), maximumExclusive.Int64(), nil
}

func ceilRat(value *big.Rat) *big.Int {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	if value.Sign() > 0 && remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func (sampler *exactSampler) beginRow() {
	sampler.currentX.Set(&sampler.rowX)
	sampler.currentY.Set(&sampler.rowY)
}

func (sampler *exactSampler) advanceColumn() {
	sampler.currentX.Add(&sampler.currentX, &sampler.xPlane.column)
	sampler.currentY.Add(&sampler.currentY, &sampler.yPlane.column)
}

func (sampler *exactSampler) advanceRow() {
	sampler.rowX.Add(&sampler.rowX, &sampler.xPlane.row)
	sampler.rowY.Add(&sampler.rowY, &sampler.yPlane.row)
}

func (sampler *exactSampler) sample(view cpucompositor.ReadView) (cpucompositor.Pixel, error) {
	// Grid coordinate is source edge coordinate minus 1/2. Preserve a common, possibly
	// unreduced denominator: Euclidean DivMod then gives the exact floor and non-negative
	// fractional numerator without constructing or reducing a big.Rat for every pixel.
	sampler.gridNumX.Lsh(&sampler.currentX, 1)
	sampler.gridNumX.Sub(&sampler.gridNumX, &sampler.xPlane.denominator)
	sampler.gridNumY.Lsh(&sampler.currentY, 1)
	sampler.gridNumY.Sub(&sampler.gridNumY, &sampler.yPlane.denominator)
	sampler.floorX.DivMod(&sampler.gridNumX, &sampler.gridDenX, &sampler.remX)
	sampler.floorY.DivMod(&sampler.gridNumY, &sampler.gridDenY, &sampler.remY)
	sampler.wx0.Sub(&sampler.gridDenX, &sampler.remX)
	sampler.wy0.Sub(&sampler.gridDenY, &sampler.remY)
	sampler.w00.Mul(&sampler.wx0, &sampler.wy0)
	sampler.w10.Mul(&sampler.remX, &sampler.wy0)
	sampler.w01.Mul(&sampler.wx0, &sampler.remY)
	sampler.w11.Mul(&sampler.remX, &sampler.remY)
	for index := range sampler.sums {
		sampler.sums[index].SetInt64(0)
	}

	if sampler.floorX.IsInt64() && sampler.floorY.IsInt64() {
		x0, y0 := sampler.floorX.Int64(), sampler.floorY.Int64()
		if err := sampler.addTap(view, x0, y0, &sampler.w00); err != nil {
			return cpucompositor.Pixel{}, err
		}
		if x0 < math.MaxInt64 {
			if err := sampler.addTap(view, x0+1, y0, &sampler.w10); err != nil {
				return cpucompositor.Pixel{}, err
			}
		}
		if y0 < math.MaxInt64 {
			if err := sampler.addTap(view, x0, y0+1, &sampler.w01); err != nil {
				return cpucompositor.Pixel{}, err
			}
			if x0 < math.MaxInt64 {
				if err := sampler.addTap(view, x0+1, y0+1, &sampler.w11); err != nil {
					return cpucompositor.Pixel{}, err
				}
			}
		}
	}
	channels := [4]uint16{}
	for index := range sampler.sums {
		value, err := sampler.round(&sampler.sums[index])
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

func (sampler *exactSampler) addTap(view cpucompositor.ReadView, x, y int64, weight *big.Int) error {
	if weight.Sign() == 0 || x < sampler.cropMinX || x >= sampler.cropMaxX || y < sampler.cropMinY || y >= sampler.cropMaxY {
		return nil
	}
	pixel, err := view.Pixel(int(x), int(y))
	if err != nil {
		return err
	}
	channels := [4]uint16{pixel.R, pixel.G, pixel.B, pixel.A}
	for index, value := range channels {
		sampler.channel.SetUint64(uint64(value))
		sampler.temporary.Mul(weight, &sampler.channel)
		sampler.sums[index].Add(&sampler.sums[index], &sampler.temporary)
	}
	return nil
}

func (sampler *exactSampler) round(value *big.Int) (uint16, error) {
	if value.Sign() < 0 {
		return 0, ErrSampleFailed
	}
	sampler.quotient.QuoRem(value, &sampler.totalDen, &sampler.remainder)
	sampler.twice.Lsh(&sampler.remainder, 1)
	comparison := sampler.twice.Cmp(&sampler.totalDen)
	if comparison > 0 || comparison == 0 && sampler.quotient.Bit(0) == 1 {
		sampler.quotient.Add(&sampler.quotient, big.NewInt(1))
	}
	if !sampler.quotient.IsUint64() || sampler.quotient.Uint64() > math.MaxUint16 {
		return 0, ErrSampleFailed
	}
	return uint16(sampler.quotient.Uint64()), nil
}

func floorRat(value *big.Rat) *big.Int {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	if value.Sign() < 0 && remainder.Sign() != 0 {
		quotient.Sub(quotient, big.NewInt(1))
	}
	return quotient
}

func roundChannel(value *big.Rat) (uint16, error) {
	if value == nil || value.Sign() < 0 {
		return 0, ErrSampleFailed
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	comparison := new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(value.Denom())
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() || quotient.Uint64() > math.MaxUint16 {
		return 0, ErrSampleFailed
	}
	return uint16(quotient.Uint64()), nil
}

func normalizeLimits(value Limits) (Limits, error) {
	if value == (Limits{}) {
		value = DefaultLimits()
	}
	if value.MaxWidth <= 0 || value.MaxHeight <= 0 || value.MaxOutputPixels == 0 || value.MaxOutputPixels > HardMaxPixels ||
		value.MaxWorkingBytes < cpucompositor.BytesPerPixel*2 || value.MaxWorkingBytes > HardMaxWorking ||
		value.MaxExactBits <= 0 || value.MaxExactBits > HardMaxExactBits {
		return Limits{}, rasterError(CodeResourceLimit, "limits", ErrResourceLimit)
	}
	return value, nil
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return rasterError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return rasterError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func pixelContextFailure(ctx context.Context, index int) error {
	if ctx == nil {
		return rasterError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return rasterError(CodeCanceled, fmt.Sprintf("pixels/%d", index), errors.Join(ErrCanceled, err))
	}
	return nil
}

func allocatePixels(count int) (pixels []cpucompositor.Pixel, err error) {
	defer func() {
		if recover() != nil {
			pixels = nil
			err = rasterError(CodeAllocationFailed, "pixels", ErrAllocationFailed)
		}
	}()
	return make([]cpucompositor.Pixel, count), nil
}

func maxInt() int { return int(^uint(0) >> 1) }
