package cpucompositor

import (
	"fmt"
	"math/big"

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
	for index := range outgoing.pixels {
		pixel, pixelErr := dissolvePixel(outgoing.pixels[index], incoming.pixels[index], left, right)
		if pixelErr != nil {
			return nil, kernelError(CodeInvalidWeight, fmt.Sprintf("transition/pixels/%d", index), pixelErr)
		}
		output.pixels[index] = pixel
	}
	return output, nil
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

func weightedChannel(value uint16, weight exactWeight) (uint16, error) {
	numerator := new(big.Int).Mul(new(big.Int).SetUint64(uint64(value)), &weight.numerator)
	return roundUint16Even(numerator, &weight.denominator)
}

func weightedSumChannel(left, right uint16, leftWeight, rightWeight exactWeight) (uint16, error) {
	leftTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(left)), &leftWeight.numerator)
	leftTerm.Mul(leftTerm, &rightWeight.denominator)
	rightTerm := new(big.Int).Mul(new(big.Int).SetUint64(uint64(right)), &rightWeight.numerator)
	rightTerm.Mul(rightTerm, &leftWeight.denominator)
	numerator := new(big.Int).Add(leftTerm, rightTerm)
	denominator := new(big.Int).Mul(&leftWeight.denominator, &rightWeight.denominator)
	return roundUint16Even(numerator, denominator)
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
