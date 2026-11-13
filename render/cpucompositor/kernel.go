package cpucompositor

import (
	"fmt"
	"math/big"
	"math/bits"

	"github.com/CutlyLab/Cutly/core/evaluator"
)

const maxWeightBits = 4096

type exactWeight struct {
	numerator   big.Int
	denominator big.Int
}

func readWeight(value evaluator.ExactRational, path string) (exactWeight, error) {
	numerator, denominator := value.Numerator(), value.Denominator()
	if numerator == nil || denominator == nil || denominator.Sign() <= 0 || numerator.Sign() < 0 || numerator.Cmp(denominator) > 0 ||
		numerator.BitLen() > maxWeightBits || denominator.BitLen() > maxWeightBits {
		return exactWeight{}, kernelError(CodeInvalidWeight, path, ErrInvalidWeight)
	}
	var result exactWeight
	result.numerator.Set(numerator)
	result.denominator.Set(denominator)
	return result, nil
}

func weightsSumToOne(left, right exactWeight) bool {
	leftTerm := new(big.Int).Mul(&left.numerator, &right.denominator)
	rightTerm := new(big.Int).Mul(&right.numerator, &left.denominator)
	numerator := new(big.Int).Add(leftTerm, rightTerm)
	denominator := new(big.Int).Mul(&left.denominator, &right.denominator)
	return numerator.Cmp(denominator) == 0
}

// ApplyOpacity multiplies all four premultiplied channels by one exact evaluator weight. Inputs
// are never mutated and invalid weights fail before output allocation.
func ApplyOpacity(source *Surface, opacity evaluator.ExactRational) (*Surface, error) {
	if err := validateSurface(source, "source"); err != nil {
		return nil, err
	}
	weight, err := readWeight(opacity, "opacity")
	if err != nil {
		return nil, err
	}
	output, err := emptyLike(source)
	if err != nil {
		return nil, err
	}
	if fast, ok := resolveFastWeight(weight); ok {
		for index, pixel := range source.pixels {
			output.pixels[index] = scalePixelFast(pixel, fast)
		}
		return output, nil
	}
	for index, pixel := range source.pixels {
		result, pixelErr := scalePixel(pixel, weight)
		if pixelErr != nil {
			return nil, kernelError(CodeInvalidWeight, fmt.Sprintf("opacity/pixels/%d", index), pixelErr)
		}
		output.pixels[index] = result
	}
	return output, nil
}

// Dissolve combines two same-size surfaces using the evaluator's exact complementary weights.
// Quantization happens after the two exact weighted terms are added, never once per term.
func Dissolve(outgoing, incoming *Surface, outgoingWeight, incomingWeight evaluator.ExactRational) (*Surface, error) {
	if err := matchingSurfaces(outgoing, incoming); err != nil {
		return nil, err
	}
	left, err := readWeight(outgoingWeight, "outgoing_weight")
	if err != nil {
		return nil, err
	}
	right, err := readWeight(incomingWeight, "incoming_weight")
	if err != nil {
		return nil, err
	}
	if !weightsSumToOne(left, right) {
		return nil, kernelError(CodeInvalidWeight, "transition.weights", ErrInvalidWeight)
	}
	output, err := emptyLike(outgoing)
	if err != nil {
		return nil, err
	}
	if fastLeft, okLeft := resolveFastWeight(left); okLeft {
		if fastRight, okRight := resolveFastWeight(right); okRight {
			for index := range outgoing.pixels {
				output.pixels[index] = dissolvePixelFast(outgoing.pixels[index], incoming.pixels[index], fastLeft, fastRight)
			}
			return output, nil
		}
	}
	for index := range outgoing.pixels {
		pixel, pixelErr := dissolvePixel(outgoing.pixels[index], incoming.pixels[index], left, right)
		if pixelErr != nil {
			return nil, kernelError(CodeInvalidWeight, fmt.Sprintf("transition/pixels/%d", index), pixelErr)
		}
		output.pixels[index] = pixel
	}
	return output, nil
}

// dissolvePixelFast combines one pixel under resolved bounded complementary weights using the
// same 128-bit cross-multiplied numerator and round-half-to-even quotient as
// weightedSumChannel's bounded path.
func dissolvePixelFast(outgoing, incoming Pixel, left, right fastWeight) Pixel {
	return Pixel{
		R: weightedSumChannelFast(outgoing.R, incoming.R, left, right),
		G: weightedSumChannelFast(outgoing.G, incoming.G, left, right),
		B: weightedSumChannelFast(outgoing.B, incoming.B, left, right),
		A: weightedSumChannelFast(outgoing.A, incoming.A, left, right),
	}
}

// weightedSumChannelFast is weightedSumChannel with weights already resolved to uint64 parts;
// the magnitudes admitted by the fit checks keep every intermediate inside 128 bits.
func weightedSumChannelFast(left, right uint16, leftWeight, rightWeight fastWeight) uint16 {
	leftHi, leftLo := bits.Mul64(uint64(left), leftWeight.numerator)
	if leftHi != 0 {
		return weightedSumChannelBig(left, right, leftWeight, rightWeight)
	}
	leftHi, leftLo = bits.Mul64(leftLo, rightWeight.denominator)
	if leftHi != 0 {
		return weightedSumChannelBig(left, right, leftWeight, rightWeight)
	}
	rightHi, rightLo := bits.Mul64(uint64(right), rightWeight.numerator)
	if rightHi != 0 {
		return weightedSumChannelBig(left, right, leftWeight, rightWeight)
	}
	rightHi, rightLo = bits.Mul64(rightLo, leftWeight.denominator)
	if rightHi != 0 {
		return weightedSumChannelBig(left, right, leftWeight, rightWeight)
	}
	denominatorHi, denominatorLo := bits.Mul64(leftWeight.denominator, rightWeight.denominator)
	if denominatorHi != 0 {
		return weightedSumChannelBig(left, right, leftWeight, rightWeight)
	}
	lo, carry := bits.Add64(leftLo, rightLo, 0)
	rounded, _ := roundWideUint16Even(carry, lo, denominatorLo)
	return rounded
}

// weightedSumChannelBig is the exact big.Int fallback for a bounded-weight pixel whose
// intermediates outgrew 128 bits (possible with weights near the uint64 ceiling).
func weightedSumChannelBig(left, right uint16, leftWeight, rightWeight fastWeight) uint16 {
	leftTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(left)), new(big.Int).SetUint64(leftWeight.numerator))
	leftTerm.Mul(leftTerm, new(big.Int).SetUint64(rightWeight.denominator))
	rightTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(right)), new(big.Int).SetUint64(rightWeight.numerator))
	rightTerm.Mul(rightTerm, new(big.Int).SetUint64(leftWeight.denominator))
	numerator := new(big.Int).Add(leftTerm, rightTerm)
	denominator := new(big.Int).Mul(new(big.Int).SetUint64(leftWeight.denominator), new(big.Int).SetUint64(rightWeight.denominator))
	value, err := roundUint16Even(numerator, denominator)
	if err != nil {
		return 0
	}
	return value
}

// CompositeOver applies Porter-Duff source-over without mutating destination or source.
func CompositeOver(destination, source *Surface) (*Surface, error) {
	if err := matchingSurfaces(destination, source); err != nil {
		return nil, err
	}
	output, err := emptyLike(destination)
	if err != nil {
		return nil, err
	}
	for index := range destination.pixels {
		pixel, pixelErr := sourceOverPixel(destination.pixels[index], source.pixels[index])
		if pixelErr != nil {
			return nil, kernelError(CodeInvalidPixel, fmt.Sprintf("source_over/pixels/%d", index), pixelErr)
		}
		output.pixels[index] = pixel
	}
	return output, nil
}

// CompositeLayers composites a non-empty back-to-front slice. It allocates exactly one output
// surface and mutates only that private buffer, so input aliasing and layer completion order cannot
// affect the result.
func CompositeLayers(layers []*Surface) (*Surface, error) {
	if len(layers) == 0 {
		return nil, kernelError(CodeNoLayers, "layers", ErrNoLayers)
	}
	for index, layer := range layers {
		if err := validateSurface(layer, fmt.Sprintf("layers/%d", index)); err != nil {
			return nil, err
		}
		if index > 0 && (layer.width != layers[0].width || layer.height != layers[0].height) {
			return nil, kernelError(CodeSurfaceMismatch, fmt.Sprintf("layers/%d", index), ErrSurfaceMismatch)
		}
	}
	result, err := cloneSurface(layers[0])
	if err != nil {
		return nil, err
	}
	for layerIndex := 1; layerIndex < len(layers); layerIndex++ {
		source := layers[layerIndex]
		for pixelIndex := range result.pixels {
			pixel, pixelErr := sourceOverPixel(result.pixels[pixelIndex], source.pixels[pixelIndex])
			if pixelErr != nil {
				return nil, kernelError(CodeInvalidPixel, fmt.Sprintf("layers/%d/pixels/%d", layerIndex, pixelIndex), pixelErr)
			}
			result.pixels[pixelIndex] = pixel
		}
	}
	return result, nil
}

func matchingSurfaces(left, right *Surface) error {
	if err := validateSurface(left, "left"); err != nil {
		return err
	}
	if err := validateSurface(right, "right"); err != nil {
		return err
	}
	if left.width != right.width || left.height != right.height {
		return kernelError(CodeSurfaceMismatch, "surface", ErrSurfaceMismatch)
	}
	return nil
}

func emptyLike(source *Surface) (*Surface, error) {
	pixels, err := allocatePixels(len(source.pixels))
	if err != nil {
		return nil, err
	}
	return &Surface{width: source.width, height: source.height, pixels: pixels}, nil
}

func scalePixel(value Pixel, weight exactWeight) (Pixel, error) {
	r, err := weightedChannel(value.R, weight)
	if err != nil {
		return Pixel{}, err
	}
	g, err := weightedChannel(value.G, weight)
	if err != nil {
		return Pixel{}, err
	}
	b, err := weightedChannel(value.B, weight)
	if err != nil {
		return Pixel{}, err
	}
	a, err := weightedChannel(value.A, weight)
	if err != nil {
		return Pixel{}, err
	}
	return Pixel{R: r, G: g, B: b, A: a}, nil
}

func dissolvePixel(outgoing, incoming Pixel, outgoingWeight, incomingWeight exactWeight) (Pixel, error) {
	r, err := weightedSumChannel(outgoing.R, incoming.R, outgoingWeight, incomingWeight)
	if err != nil {
		return Pixel{}, err
	}
	g, err := weightedSumChannel(outgoing.G, incoming.G, outgoingWeight, incomingWeight)
	if err != nil {
		return Pixel{}, err
	}
	b, err := weightedSumChannel(outgoing.B, incoming.B, outgoingWeight, incomingWeight)
	if err != nil {
		return Pixel{}, err
	}
	a, err := weightedSumChannel(outgoing.A, incoming.A, outgoingWeight, incomingWeight)
	if err != nil {
		return Pixel{}, err
	}
	return Pixel{R: r, G: g, B: b, A: a}, nil
}

// fastWeight is one exact weight resolved to uint64 parts once per kernel invocation. All
// per-pixel work then runs on fixed-width arithmetic; weights beyond uint64 keep the big.Int
// loops.
type fastWeight struct {
	numerator   uint64
	denominator uint64
}

func resolveFastWeight(weight exactWeight) (fastWeight, bool) {
	if !weight.numerator.IsUint64() || !weight.denominator.IsUint64() {
		return fastWeight{}, false
	}
	return fastWeight{numerator: weight.numerator.Uint64(), denominator: weight.denominator.Uint64()}, true
}

// scalePixelFast scales one pixel by a resolved bounded weight: identical arithmetic to
// scalePixel through roundWideUint16Even.
func scalePixelFast(value Pixel, weight fastWeight) Pixel {
	return Pixel{
		R: scaleChannelFast(value.R, weight),
		G: scaleChannelFast(value.G, weight),
		B: scaleChannelFast(value.B, weight),
		A: scaleChannelFast(value.A, weight),
	}
}

func scaleChannelFast(value uint16, weight fastWeight) uint16 {
	hi, lo := bits.Mul64(uint64(value), weight.numerator)
	rounded, _ := roundWideUint16Even(hi, lo, weight.denominator)
	return rounded
}

// weightedChannel scales one channel by an exact weight. The bounded-arithmetic path below is
// bit-identical to the historical big.Int expression: readWeight guarantees 0 <= numerator <=
// denominator, so the quotient never exceeds the channel value, and roundWideUint16Even
// reproduces roundUint16Even's round-half-to-even quotient rule without any allocation. Weights
// whose parts do not fit uint64 keep the exact big.Int path.
func weightedChannel(value uint16, weight exactWeight) (uint16, error) {
	numerator, denominator, ok := weight.uint64Parts()
	if ok {
		hi, lo := bits.Mul64(uint64(value), numerator)
		return roundWideUint16Even(hi, lo, denominator)
	}
	bigNumerator := new(big.Int).Mul(new(big.Int).SetUint64(uint64(value)), &weight.numerator)
	return roundUint16Even(bigNumerator, &weight.denominator)
}

// weightedSumChannel combines two channels under complementary exact weights. The bounded path
// forms the same cross-multiplied numerator and common denominator as the big.Int path using
// 128-bit intermediates; Dissolve's weights-sum-to-one admission keeps the quotient within
// uint16, so the wide division is exact and allocation-free.
func weightedSumChannel(left, right uint16, leftWeight, rightWeight exactWeight) (uint16, error) {
	leftNumerator, leftDenominator, leftOK := leftWeight.uint64Parts()
	rightNumerator, rightDenominator, rightOK := rightWeight.uint64Parts()
	if leftOK && rightOK {
		leftHi, leftLo := bits.Mul64(uint64(left), leftNumerator)
		if leftHi == 0 {
			leftHi, leftLo = bits.Mul64(leftLo, rightDenominator)
			if leftHi == 0 {
				rightHi, rightLo := bits.Mul64(uint64(right), rightNumerator)
				if rightHi == 0 {
					rightHi, rightLo = bits.Mul64(rightLo, leftDenominator)
					if rightHi == 0 {
						denominatorHi, denominatorLo := bits.Mul64(leftDenominator, rightDenominator)
						if denominatorHi == 0 {
							lo, carry := bits.Add64(leftLo, rightLo, 0)
							return roundWideUint16Even(uint64(carry), lo, denominatorLo)
						}
					}
				}
			}
		}
	}
	leftTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(left)), &leftWeight.numerator)
	leftTerm.Mul(leftTerm, &rightWeight.denominator)
	rightTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(right)), &rightWeight.numerator)
	rightTerm.Mul(rightTerm, &leftWeight.denominator)
	numerator := new(big.Int).Add(leftTerm, rightTerm)
	denominator := new(big.Int).Mul(&leftWeight.denominator, &rightWeight.denominator)
	return roundUint16Even(numerator, denominator)
}

// uint64Parts returns the weight as uint64 parts when both fit.
func (weight exactWeight) uint64Parts() (numerator, denominator uint64, ok bool) {
	if weight.numerator.IsUint64() && weight.denominator.IsUint64() {
		return weight.numerator.Uint64(), weight.denominator.Uint64(), true
	}
	return 0, 0, false
}

// roundWideUint16Even returns the round-half-to-even quotient of the 128-bit numerator by the
// uint64 denominator, failing when the quotient cannot be a channel value. It allocates nothing.
func roundWideUint16Even(numeratorHi, numeratorLo, denominator uint64) (uint16, error) {
	if denominator == 0 {
		return 0, ErrInvalidWeight
	}
	if numeratorHi >= denominator {
		// A quotient at or beyond 2^64 cannot be a channel value; Div64 would also panic on it.
		return 0, ErrInvalidWeight
	}
	quotient, remainder := bits.Div64(numeratorHi, numeratorLo, denominator)
	// Compare remainder/denominator with one half without forming remainder*2.
	half := denominator / 2
	aboveHalf := remainder > half
	exactHalf := denominator&1 == 0 && remainder == half
	if aboveHalf || exactHalf && quotient&1 == 1 {
		quotient++
	}
	if quotient > mathMaxUint16 {
		return 0, ErrInvalidWeight
	}
	return uint16(quotient), nil
}

func roundUint16Even(numerator, denominator *big.Int) (uint16, error) {
	if numerator == nil || denominator == nil || numerator.Sign() < 0 || denominator.Sign() <= 0 {
		return 0, ErrInvalidWeight
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	comparison := new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(denominator)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() || quotient.Uint64() > mathMaxUint16 {
		return 0, ErrInvalidWeight
	}
	return uint16(quotient.Uint64()), nil
}

const mathMaxUint16 uint64 = 1<<16 - 1

func sourceOverPixel(destination, source Pixel) (Pixel, error) {
	inverseAlpha := uint64(mathMaxUint16 - uint64(source.A))
	channels := [4]uint16{source.R, source.G, source.B, source.A}
	destinationChannels := [4]uint16{destination.R, destination.G, destination.B, destination.A}
	var output [4]uint16
	for index := range channels {
		// Form the complete Porter-Duff expression before its sole quantization boundary:
		//
		//   (source*65535 + destination*(65535-sourceAlpha)) / 65535
		//
		// Adding source after rounding the destination term happens to be equivalent for v1
		// because 65535 is odd and an exact half tie is impossible. Keeping the complete
		// numerator here makes the profile's one-quantization rule explicit and robust to a
		// future channel profile using a different denominator.
		numerator := uint64(channels[index])*mathMaxUint16 + uint64(destinationChannels[index])*inverseAlpha
		quantized, quantizeErr := roundRatioUint16Even(numerator, mathMaxUint16)
		if quantizeErr != nil {
			return Pixel{}, ErrInvalidPixel
		}
		output[index] = quantized
	}
	result := Pixel{R: output[0], G: output[1], B: output[2], A: output[3]}
	if !result.valid() {
		return Pixel{}, ErrInvalidPixel
	}
	return result, nil
}

func roundRatioUint16Even(numerator, denominator uint64) (uint16, error) {
	if denominator == 0 {
		return 0, ErrInvalidWeight
	}
	quotient, remainder := numerator/denominator, numerator%denominator
	// Compare remainder/denominator with one half without forming remainder*2, which
	// would overflow for an otherwise valid general uint64 ratio.
	half := denominator / 2
	aboveHalf := remainder > half
	exactHalf := denominator&1 == 0 && remainder == half
	if aboveHalf || exactHalf && quotient&1 == 1 {
		quotient++
	}
	if quotient > mathMaxUint16 {
		return 0, ErrInvalidWeight
	}
	return uint16(quotient), nil
}

// WipeLeft produces the wipe transition: the incoming surface reveals from left to right
// with a hard edge at the given exact progress (0 = all outgoing, 1 = all incoming).
func WipeLeft(outgoing, incoming *Surface, progress evaluator.ExactRational) (*Surface, error) {
	if err := matchingSurfaces(outgoing, incoming); err != nil {
		return nil, err
	}
	fraction, err := readWeight(progress, "wipe.progress")
	if err != nil {
		return nil, err
	}
	output, err := emptyLike(outgoing)
	if err != nil {
		return nil, err
	}
	if err := wipeLeftPixels(output, outgoing, incoming, fraction); err != nil {
		return nil, err
	}
	return output, nil
}

// FadeThroughBlack produces the fade transition: outgoing fades to black then incoming
// fades in, with the exact progress determining the blend (0 = outgoing, 0.5 = black, 1 = incoming).
func FadeThroughBlack(outgoing, incoming *Surface, progress evaluator.ExactRational) (*Surface, error) {
	if err := matchingSurfaces(outgoing, incoming); err != nil {
		return nil, err
	}
	fraction, err := readWeight(progress, "fade.progress")
	if err != nil {
		return nil, err
	}
	output, err := emptyLike(outgoing)
	if err != nil {
		return nil, err
	}
	if err := fadeThroughBlackPixels(output, outgoing, incoming, fraction); err != nil {
		return nil, err
	}
	return output, nil
}

// WipeLeftInto is the allocation-free form of WipeLeft. Target may alias either input.
func WipeLeftInto(target, outgoing, incoming *Surface, progress evaluator.ExactRational) error {
	if err := matchingSurfaces(outgoing, incoming); err != nil {
		return err
	}
	if err := matchingSurfaces(target, outgoing); err != nil {
		return err
	}
	fraction, err := readWeight(progress, "wipe.progress")
	if err != nil {
		return err
	}
	return wipeLeftPixels(target, outgoing, incoming, fraction)
}

// FadeThroughBlackInto is the allocation-free form of FadeThroughBlack. Target may alias either
// input because every output pixel depends only on the corresponding input pixels.
func FadeThroughBlackInto(target, outgoing, incoming *Surface, progress evaluator.ExactRational) error {
	if err := matchingSurfaces(outgoing, incoming); err != nil {
		return err
	}
	if err := matchingSurfaces(target, outgoing); err != nil {
		return err
	}
	fraction, err := readWeight(progress, "fade.progress")
	if err != nil {
		return err
	}
	return fadeThroughBlackPixels(target, outgoing, incoming, fraction)
}

func wipeLeftPixels(target, outgoing, incoming *Surface, progress exactWeight) error {
	boundaryValue := new(big.Int).Mul(big.NewInt(int64(outgoing.width)), &progress.numerator)
	boundaryValue.Quo(boundaryValue, &progress.denominator)
	if !boundaryValue.IsInt64() {
		return kernelError(CodeInvalidWeight, "wipe.progress", ErrInvalidWeight)
	}
	boundary := int(boundaryValue.Int64())
	for index := range outgoing.pixels {
		if index%outgoing.width < boundary {
			target.pixels[index] = incoming.pixels[index]
		} else {
			target.pixels[index] = outgoing.pixels[index]
		}
	}
	return nil
}

func fadeThroughBlackPixels(target, outgoing, incoming *Surface, progress exactWeight) error {
	twiceNumerator := new(big.Int).Lsh(new(big.Int).Set(&progress.numerator), 1)
	black := Pixel{A: uint16(mathMaxUint16)}
	var source *Surface
	var sourceWeight, blackWeight exactWeight
	if twiceNumerator.Cmp(&progress.denominator) <= 0 {
		source = outgoing
		sourceWeight.numerator.Sub(&progress.denominator, twiceNumerator)
		blackWeight.numerator.Set(twiceNumerator)
	} else {
		source = incoming
		sourceWeight.numerator.Sub(twiceNumerator, &progress.denominator)
		blackWeight.numerator.Sub(new(big.Int).Lsh(new(big.Int).Set(&progress.denominator), 1), twiceNumerator)
	}
	sourceWeight.denominator.Set(&progress.denominator)
	blackWeight.denominator.Set(&progress.denominator)
	if fastSource, sourceOK := resolveFastWeight(sourceWeight); sourceOK {
		if fastBlack, blackOK := resolveFastWeight(blackWeight); blackOK {
			for index := range source.pixels {
				target.pixels[index] = dissolvePixelFast(source.pixels[index], black, fastSource, fastBlack)
			}
			return nil
		}
	}
	for index := range source.pixels {
		pixel, err := dissolvePixel(source.pixels[index], black, sourceWeight, blackWeight)
		if err != nil {
			return kernelError(CodeInvalidWeight, fmt.Sprintf("fade/pixels/%d", index), err)
		}
		target.pixels[index] = pixel
	}
	return nil
}
