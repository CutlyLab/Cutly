package cpucompositor

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
)

const (
	BytesPerPixel       uint64 = 8
	HardMaxSurfaceBytes uint64 = 1 << 30
)

// Limits makes allocation policy explicit. It is a host resource boundary, not a rendering
// semantic: a surface either exists in full or creation fails without a partial result.
type Limits struct {
	MaxWidth  int
	MaxHeight int
	MaxBytes  uint64
}

// DefaultLimits returns a copy so one host cannot mutate process-wide allocation policy for
// another render. Hosts with tighter budgets pass an explicit Limits value.
func DefaultLimits() Limits {
	return Limits{MaxWidth: 16384, MaxHeight: 16384, MaxBytes: 512 << 20}
}

// Pixel is one linear-light premultiplied RGBA16 pixel. Valid values satisfy R,G,B <= A.
type Pixel struct {
	R uint16 `json:"r"`
	G uint16 `json:"g"`
	B uint16 `json:"b"`
	A uint16 `json:"a"`
}

func (value Pixel) valid() bool {
	return value.R <= value.A && value.G <= value.A && value.B <= value.A
}

// Surface owns its storage. Constructors and operations copy caller-provided pixels, so input
// aliasing cannot change a frozen render result.
type Surface struct {
	width  int
	height int
	pixels []Pixel
}

// ReadView is a zero-copy, read-only view over one validated immutable Surface. It lets
// downstream raster kernels validate a complete surface once and then sample many pixels without
// exposing mutable storage or rescanning every pixel for each lookup.
//
// A ReadView retains its Surface. Surface storage is immutable after construction, so views are
// safe for concurrent reads.
type ReadView struct {
	surface *Surface
}

func NewSurface(width, height int) (*Surface, error) {
	return NewSurfaceWithLimits(width, height, DefaultLimits())
}

func NewSurfaceWithLimits(width, height int, limits Limits) (*Surface, error) {
	count, err := checkedPixelCount(width, height, limits)
	if err != nil {
		return nil, err
	}
	pixels, err := allocatePixels(count)
	if err != nil {
		return nil, err
	}
	return &Surface{width: width, height: height, pixels: pixels}, nil
}

func NewSurfaceFromPixels(width, height int, pixels []Pixel) (*Surface, error) {
	return NewSurfaceFromPixelsWithLimits(width, height, pixels, DefaultLimits())
}

// NewSurfaceFromPixelsWithLimits copies a complete frame under the caller's explicit resource
// budget. The input slice is never retained.
func NewSurfaceFromPixelsWithLimits(width, height int, pixels []Pixel, limits Limits) (*Surface, error) {
	count, err := checkedPixelCount(width, height, limits)
	if err != nil {
		return nil, err
	}
	if len(pixels) != count {
		return nil, kernelError(CodeInvalidSurface, "pixels", ErrInvalidSurface)
	}
	for index, pixel := range pixels {
		if !pixel.valid() {
			return nil, kernelError(CodeInvalidPixel, fmt.Sprintf("pixels/%d", index), ErrInvalidPixel)
		}
	}
	copyPixels, err := allocatePixels(count)
	if err != nil {
		return nil, err
	}
	copy(copyPixels, pixels)
	return &Surface{width: width, height: height, pixels: copyPixels}, nil
}

func checkedPixelCount(width, height int, limits Limits) (int, error) {
	if width <= 0 || height <= 0 || limits.MaxWidth <= 0 || limits.MaxHeight <= 0 || limits.MaxBytes < BytesPerPixel || limits.MaxBytes > HardMaxSurfaceBytes {
		return 0, kernelError(CodeInvalidDimensions, "surface", ErrInvalidDimensions)
	}
	if width > limits.MaxWidth || height > limits.MaxHeight {
		return 0, kernelError(CodeResourceLimit, "surface.dimensions", ErrResourceLimit)
	}
	unsignedWidth, unsignedHeight := uint64(width), uint64(height)
	if unsignedHeight != 0 && unsignedWidth > math.MaxUint64/unsignedHeight {
		return 0, kernelError(CodeResourceLimit, "surface.pixel_count", ErrResourceLimit)
	}
	count := unsignedWidth * unsignedHeight
	if count > math.MaxUint64/BytesPerPixel || count*BytesPerPixel > limits.MaxBytes || count > uint64(maxInt()) {
		return 0, kernelError(CodeResourceLimit, "surface.bytes", ErrResourceLimit)
	}
	return int(count), nil
}

func maxInt() int { return int(^uint(0) >> 1) }

func allocatePixels(count int) (pixels []Pixel, err error) {
	defer func() {
		if recover() != nil {
			pixels = nil
			err = kernelError(CodeAllocationFailed, "surface.pixels", ErrAllocationFailed)
		}
	}()
	return make([]Pixel, count), nil
}

func (surface *Surface) Width() int {
	if surface == nil {
		return 0
	}
	return surface.width
}

func (surface *Surface) Height() int {
	if surface == nil {
		return 0
	}
	return surface.height
}

// View validates the complete Surface and returns a zero-copy immutable sampling view.
func (surface *Surface) View() (ReadView, error) {
	if err := validateSurface(surface, "surface"); err != nil {
		return ReadView{}, err
	}
	return ReadView{surface: surface}, nil
}

func (view ReadView) Width() int {
	if view.surface == nil {
		return 0
	}
	return view.surface.width
}

func (view ReadView) Height() int {
	if view.surface == nil {
		return 0
	}
	return view.surface.height
}

// Pixel returns one pixel without rescanning the already validated surface. A zero ReadView and
// out-of-range coordinates fail closed.
func (view ReadView) Pixel(x, y int) (Pixel, error) {
	if view.surface == nil || view.surface.width <= 0 || view.surface.height <= 0 ||
		x < 0 || y < 0 || x >= view.surface.width || y >= view.surface.height {
		return Pixel{}, kernelError(CodeBounds, "view.pixel", ErrBounds)
	}
	return view.surface.pixels[y*view.surface.width+x], nil
}

func (surface *Surface) Pixel(x, y int) (Pixel, error) {
	if err := validateSurface(surface, "surface"); err != nil {
		return Pixel{}, err
	}
	if x < 0 || y < 0 || x >= surface.width || y >= surface.height {
		return Pixel{}, kernelError(CodeBounds, "pixel", ErrBounds)
	}
	return surface.pixels[y*surface.width+x], nil
}

func validateSurface(surface *Surface, path string) error {
	if surface == nil || surface.width <= 0 || surface.height <= 0 {
		return kernelError(CodeInvalidSurface, path, ErrInvalidSurface)
	}
	if surface.width > maxInt()/surface.height || len(surface.pixels) != surface.width*surface.height {
		return kernelError(CodeInvalidSurface, path, ErrInvalidSurface)
	}
	for index, pixel := range surface.pixels {
		if !pixel.valid() {
			return kernelError(CodeInvalidPixel, fmt.Sprintf("%s/pixels/%d", path, index), ErrInvalidPixel)
		}
	}
	return nil
}

func cloneSurface(surface *Surface) (*Surface, error) {
	if err := validateSurface(surface, "surface"); err != nil {
		return nil, err
	}
	pixels, err := allocatePixels(len(surface.pixels))
	if err != nil {
		return nil, err
	}
	copy(pixels, surface.pixels)
	return &Surface{width: surface.width, height: surface.height, pixels: pixels}, nil
}

// WriteCanonical writes the architecture-independent surface byte contract without allocating a
// second frame-sized buffer.
func (surface *Surface) WriteCanonical(writer io.Writer) error {
	if err := validateSurface(surface, "surface"); err != nil {
		return err
	}
	if writer == nil {
		return kernelError(CodeInvalidSurface, "writer", ErrInvalidSurface)
	}
	var encoded [8]byte
	for index, pixel := range surface.pixels {
		binary.BigEndian.PutUint16(encoded[0:2], pixel.R)
		binary.BigEndian.PutUint16(encoded[2:4], pixel.G)
		binary.BigEndian.PutUint16(encoded[4:6], pixel.B)
		binary.BigEndian.PutUint16(encoded[6:8], pixel.A)
		written, err := writer.Write(encoded[:])
		if err != nil {
			return fmt.Errorf("cpucompositor: write canonical pixel %d: %w", index, err)
		}
		if written != len(encoded) {
			return fmt.Errorf("cpucompositor: write canonical pixel %d: %w", index, io.ErrShortWrite)
		}
	}
	return nil
}

// CanonicalHash hashes only the profile-defined pixel bytes. Width, height and the profile ID
// must be bound separately by the containing render/evidence manifest.
func (surface *Surface) CanonicalHash() (string, error) {
	hash := sha256.New()
	if err := surface.WriteCanonical(hash); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
