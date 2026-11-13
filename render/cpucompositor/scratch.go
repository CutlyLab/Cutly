package cpucompositor

import (
	"fmt"
	"sync"

	"github.com/CutlyLab/Cutly/core/evaluator"
)

// DefaultScratchSurfaces bounds how many full surfaces one Scratch retains between frames. The
// bound trades retained bytes against steady-state allocation churn; surplus buffers are simply
// dropped for the garbage collector.
const DefaultScratchSurfaces = 4

// Scratch is a bounded pool of recycled full surfaces for a pipeline's per-frame intermediate
// buffers. It exists because the allocating kernels produced hundreds of megabytes of garbage
// per rendered frame; a Scratch reuses storage for every intermediate that a frame provably
// fully overwrites. Pixel mathematics, rounding and validation are identical to the allocating
// path: the differential gates in visualcompositor and rendersession assert byte equality.
//
// A Scratch is safe for concurrent use; each Target/Commit/Recycle step is serialized while the
// per-pixel work between them runs unlocked.
type Scratch struct {
	mu          sync.Mutex
	limits      Limits
	maxRetained int
	free        []*Surface
}

// NewScratch creates a pool whose surfaces are admitted under limits and which retains at most
// maxRetained surfaces between frames (DefaultScratchSurfaces when zero).
func NewScratch(limits Limits, maxRetained int) (*Scratch, error) {
	if maxRetained == 0 {
		maxRetained = DefaultScratchSurfaces
	}
	if maxRetained < 1 || maxRetained > 1024 {
		return nil, kernelError(CodeInvalidSurface, "scratch/retained", ErrInvalidSurface)
	}
	// Validate the limit envelope once up front by admitting a 1x1 probe shape.
	if _, err := checkedPixelCount(1, 1, limits); err != nil {
		return nil, err
	}
	return &Scratch{limits: limits, maxRetained: maxRetained}, nil
}

// Target lends one full surface storage for a producer that overwrites every pixel before
// Commit. Contents are undefined; reading them before a complete overwrite is a contract
// violation that Commit's per-pixel validation is the last line of defense against.
func (scratch *Scratch) Target(width, height int) (*WriteTarget, error) {
	if scratch == nil {
		return nil, kernelError(CodeInvalidSurface, "scratch", ErrInvalidSurface)
	}
	count, err := checkedPixelCount(width, height, scratch.limits)
	if err != nil {
		return nil, err
	}
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	for index := len(scratch.free) - 1; index >= 0; index-- {
		candidate := scratch.free[index]
		if candidate.width == width && candidate.height == height && len(candidate.pixels) == count {
			scratch.free = append(scratch.free[:index], scratch.free[index+1:]...)
			return &WriteTarget{surface: candidate, owner: scratch}, nil
		}
	}
	pixels, err := allocatePixels(count)
	if err != nil {
		return nil, err
	}
	return &WriteTarget{surface: &Surface{width: width, height: height, pixels: pixels}, owner: scratch}, nil
}

// Recycle returns a scratch-owned surface to the pool. Surfaces that escaped to callers — a
// frame's promoted output, a provider surface, anything not obtained from this Scratch — must
// never be recycled. Recycling drops surplus surfaces beyond the retention bound.
func (scratch *Scratch) Recycle(surface *Surface) {
	if scratch == nil || surface == nil {
		return
	}
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if len(scratch.free) >= scratch.maxRetained {
		return
	}
	scratch.free = append(scratch.free, surface)
}

// Retained reports how many surfaces the pool currently holds.
func (scratch *Scratch) Retained() int {
	if scratch == nil {
		return 0
	}
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	return len(scratch.free)
}

// WriteTarget is one lent surface storage. Pixels exposes the undefined-contents storage for a
// producer that overwrites all of it; Commit validates every pixel and returns the surface.
type WriteTarget struct {
	surface *Surface
	owner   *Scratch
}

// Pixels returns the target storage. Its contents are undefined until fully overwritten.
func (target *WriteTarget) Pixels() []Pixel {
	if target == nil {
		return nil
	}
	return target.surface.pixels
}

// Commit validates every pixel exactly like NewSurfaceFromPixelsWithLimits and returns the
// surface without copying. The surface remains owned by the caller's frame bookkeeping: mutate
// it only through the documented scratch kernels and either promote it (never recycle) or
// Recycle it after compositing.
func (target *WriteTarget) Commit() (*Surface, error) {
	if target == nil || target.surface == nil {
		return nil, kernelError(CodeInvalidSurface, "scratch/target", ErrInvalidSurface)
	}
	for index, pixel := range target.surface.pixels {
		if !pixel.valid() {
			return nil, kernelError(CodeInvalidPixel, fmt.Sprintf("scratch/pixels/%d", index), ErrInvalidPixel)
		}
	}
	surface := target.surface
	target.surface = nil
	return surface, nil
}

// Release returns an uncommitted target to the pool without validation. It exists for error
// paths that abandon a partially written buffer.
func (target *WriteTarget) Release() {
	if target == nil || target.surface == nil {
		return
	}
	target.owner.Recycle(target.surface)
	target.surface = nil
}

// ApplyOpacityInto multiplies all four premultiplied channels of a scratch-owned surface by one
// exact weight, in place. It performs the identical arithmetic and validation as ApplyOpacity
// without allocating; the mutating contract restricts it to surfaces whose storage the caller
// owns for the frame (Scratch targets), never published render results.
func ApplyOpacityInto(target *Surface, opacity evaluator.ExactRational) error {
	if err := validateSurface(target, "target"); err != nil {
		return err
	}
	weight, err := readWeight(opacity, "opacity")
	if err != nil {
		return err
	}
	if fast, ok := resolveFastWeight(weight); ok {
		for index := range target.pixels {
			target.pixels[index] = scalePixelFast(target.pixels[index], fast)
		}
		return nil
	}
	for index := range target.pixels {
		result, pixelErr := scalePixel(target.pixels[index], weight)
		if pixelErr != nil {
			return kernelError(CodeInvalidWeight, fmt.Sprintf("opacity/pixels/%d", index), pixelErr)
		}
		target.pixels[index] = result
	}
	return nil
}

// DissolveInto combines two same-size surfaces under complementary exact weights into target,
// in place. target may alias outgoing or incoming: every output pixel reads only the same
// position of its inputs. Arithmetic, quantization and validation match Dissolve exactly.
func DissolveInto(target, outgoing, incoming *Surface, outgoingWeight, incomingWeight evaluator.ExactRational) error {
	if err := matchingSurfaces(outgoing, incoming); err != nil {
		return err
	}
	if err := matchingSurfaces(target, outgoing); err != nil {
		return err
	}
	left, err := readWeight(outgoingWeight, "outgoing_weight")
	if err != nil {
		return err
	}
	right, err := readWeight(incomingWeight, "incoming_weight")
	if err != nil {
		return err
	}
	if !weightsSumToOne(left, right) {
		return kernelError(CodeInvalidWeight, "transition.weights", ErrInvalidWeight)
	}
	if fastLeft, okLeft := resolveFastWeight(left); okLeft {
		if fastRight, okRight := resolveFastWeight(right); okRight {
			for index := range outgoing.pixels {
				target.pixels[index] = dissolvePixelFast(outgoing.pixels[index], incoming.pixels[index], fastLeft, fastRight)
			}
			return nil
		}
	}
	for index := range outgoing.pixels {
		pixel, pixelErr := dissolvePixel(outgoing.pixels[index], incoming.pixels[index], left, right)
		if pixelErr != nil {
			return kernelError(CodeInvalidWeight, fmt.Sprintf("transition/pixels/%d", index), pixelErr)
		}
		target.pixels[index] = pixel
	}
	return nil
}

// CompositeOverInto applies Porter-Duff source-over of source onto destination, in place. It is
// the allocating CompositeOver without the output surface: each output pixel reads only the
// same position of destination and source, so in-place update is exact.
func CompositeOverInto(destination, source *Surface) error {
	if err := matchingSurfaces(destination, source); err != nil {
		return err
	}
	for index := range destination.pixels {
		pixel, pixelErr := sourceOverPixel(destination.pixels[index], source.pixels[index])
		if pixelErr != nil {
			return kernelError(CodeInvalidPixel, fmt.Sprintf("source_over/pixels/%d", index), pixelErr)
		}
		destination.pixels[index] = pixel
	}
	return nil
}
