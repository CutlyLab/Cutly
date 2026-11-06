package color

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// RGBA8Input is one complete, caller-owned encoded image. Pixels are borrowed read-only for the
// duration of NormalizeRGBA8 and must not be mutated until it returns. Read-only inputs may be
// shared across concurrent calls; returned Surfaces never alias them or each other.
type RGBA8Input struct {
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Stride   int      `json:"stride"`
	Metadata Metadata `json:"metadata"`
	Pixels   []byte   `json:"-"`
}

// NormalizeRGBA8 converts one explicitly described image to Cutly's immutable working surface.
// On every error it returns nil; a partially converted frame is never observable.
func NormalizeRGBA8(ctx context.Context, input RGBA8Input, limits Limits) (*cpucompositor.Surface, error) {
	if ctx == nil {
		return nil, profileError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, err
	}
	normalizedLimits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if input.Metadata != MetadataRGBA8SRGBV1() {
		return nil, metadataMismatch(input.Metadata)
	}
	shape, err := validateShape(input, normalizedLimits)
	if err != nil {
		return nil, err
	}
	if err := contextFailure(ctx, "pixels"); err != nil {
		return nil, err
	}

	pixels, err := allocatePixelScratch(shape.pixelCount)
	if err != nil {
		return nil, err
	}
	for y := 0; y < input.Height; y++ {
		if err := contextFailure(ctx, fmt.Sprintf("pixels/row/%d", y)); err != nil {
			return nil, err
		}
		rowOffset := y * input.Stride
		pixelOffset := y * input.Width
		for x := 0; x < input.Width; x++ {
			offset := rowOffset + x*4
			alpha8 := input.Pixels[offset+3]
			alpha16 := uint16(alpha8) * 257
			if alpha8 == 0 {
				pixels[pixelOffset+x] = cpucompositor.Pixel{}
				continue
			}
			pixels[pixelOffset+x] = cpucompositor.Pixel{
				R: premultiplyLinear16(srgb8ToLinear16[input.Pixels[offset]], alpha8),
				G: premultiplyLinear16(srgb8ToLinear16[input.Pixels[offset+1]], alpha8),
				B: premultiplyLinear16(srgb8ToLinear16[input.Pixels[offset+2]], alpha8),
				A: alpha16,
			}
		}
	}
	if err := contextFailure(ctx, "surface"); err != nil {
		return nil, err
	}
	surface, err := cpucompositor.NewSurfaceFromPixelsWithLimits(input.Width, input.Height, pixels, cpucompositor.Limits{
		MaxWidth: input.Width, MaxHeight: input.Height, MaxBytes: shape.outputBytes,
	})
	if err != nil {
		return nil, profileError(CodeSurfaceFailed, "surface", errors.Join(ErrSurfaceFailed, err))
	}
	if err := contextFailure(ctx, "surface"); err != nil {
		return nil, err
	}
	return surface, nil
}

type checkedShape struct {
	pixelCount   int
	inputBytes   uint64
	outputBytes  uint64
	workingBytes uint64
}

func validateShape(input RGBA8Input, limits Limits) (checkedShape, error) {
	if input.Width <= 0 || input.Height <= 0 {
		return checkedShape{}, profileError(CodeDimensionsInvalid, "dimensions", ErrDimensionsInvalid)
	}
	if input.Width > limits.MaxWidth || input.Height > limits.MaxHeight {
		return checkedShape{}, profileError(CodeResourceLimit, "dimensions", ErrResourceLimit)
	}
	width, height := uint64(input.Width), uint64(input.Height)
	if width > math.MaxUint64/height {
		return checkedShape{}, profileError(CodeResourceLimit, "pixel_count", ErrResourceLimit)
	}
	count := width * height
	if count > uint64(maxInt()) {
		return checkedShape{}, profileError(CodeResourceLimit, "pixel_count", ErrResourceLimit)
	}
	if width > uint64(maxInt())/BytesPerInputPixel {
		return checkedShape{}, profileError(CodeResourceLimit, "stride", ErrResourceLimit)
	}
	expectedStride := int(width * BytesPerInputPixel)
	if input.Stride != expectedStride {
		return checkedShape{}, profileError(CodeLayoutInvalid, "stride", ErrLayoutInvalid)
	}
	inputBytes, ok := checkedBytes(count, BytesPerInputPixel)
	if !ok || inputBytes > limits.MaxInputBytes {
		return checkedShape{}, profileError(CodeResourceLimit, "pixels", ErrResourceLimit)
	}
	if uint64(len(input.Pixels)) != inputBytes {
		return checkedShape{}, profileError(CodeLayoutInvalid, "pixels", ErrLayoutInvalid)
	}
	outputBytes, ok := checkedBytes(count, BytesPerOutputPixel)
	if !ok || outputBytes > cpucompositor.HardMaxSurfaceBytes {
		return checkedShape{}, profileError(CodeResourceLimit, "surface", ErrResourceLimit)
	}
	workingBytes, ok := checkedBytes(outputBytes, 2)
	if !ok || workingBytes > limits.MaxWorkingBytes {
		return checkedShape{}, profileError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	return checkedShape{
		pixelCount: int(count), inputBytes: inputBytes,
		outputBytes: outputBytes, workingBytes: workingBytes,
	}, nil
}

func normalizeLimits(value Limits) (Limits, error) {
	if value == (Limits{}) {
		value = DefaultLimits()
	}
	if value.MaxWidth <= 0 || value.MaxHeight <= 0 || value.MaxInputBytes < BytesPerInputPixel ||
		value.MaxInputBytes > HardMaxInputBytes || value.MaxWorkingBytes < BytesPerOutputPixel*2 ||
		value.MaxWorkingBytes > HardMaxWorkingBytes {
		return Limits{}, profileError(CodeResourceLimit, "limits", ErrResourceLimit)
	}
	return value, nil
}

func metadataMismatch(actual Metadata) error {
	expected := MetadataRGBA8SRGBV1()
	fields := []struct {
		path     string
		actual   any
		expected any
	}{
		{"metadata/profile", actual.Profile, expected.Profile},
		{"metadata/pixel_layout", actual.PixelLayout, expected.PixelLayout},
		{"metadata/row_order", actual.RowOrder, expected.RowOrder},
		{"metadata/alpha_mode", actual.AlphaMode, expected.AlphaMode},
		{"metadata/primaries", actual.Primaries, expected.Primaries},
		{"metadata/white_point", actual.WhitePoint, expected.WhitePoint},
		{"metadata/transfer", actual.Transfer, expected.Transfer},
		{"metadata/range", actual.Range, expected.Range},
		{"metadata/chroma_subsampling", actual.ChromaSubsampling, expected.ChromaSubsampling},
		{"metadata/icc_profile", actual.ICCProfile, expected.ICCProfile},
		{"metadata/hdr_metadata", actual.HDRMetadata, expected.HDRMetadata},
	}
	for _, field := range fields {
		if field.actual != field.expected {
			return profileError(CodeMetadataInvalid, field.path, ErrMetadataInvalid)
		}
	}
	return profileError(CodeMetadataInvalid, "metadata", ErrMetadataInvalid)
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return profileError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return profileError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func allocatePixelScratch(count int) (pixels []cpucompositor.Pixel, err error) {
	defer func() {
		if recover() != nil {
			pixels = nil
			err = profileError(CodeAllocationFailed, "pixels", ErrAllocationFailed)
		}
	}()
	return make([]cpucompositor.Pixel, count), nil
}

func premultiplyLinear16(value uint16, alpha uint8) uint16 {
	return roundRatioEven(uint64(value)*uint64(alpha), 255)
}

func roundRatioEven(numerator, denominator uint64) uint16 {
	quotient, remainder := numerator/denominator, numerator%denominator
	twice := remainder * 2
	if twice > denominator || twice == denominator && quotient&1 == 1 {
		quotient++
	}
	return uint16(quotient)
}

func checkedBytes(left, right uint64) (uint64, bool) {
	if right != 0 && left > math.MaxUint64/right {
		return 0, false
	}
	return left * right, true
}

func maxInt() int { return int(^uint(0) >> 1) }

// SRGB8ToLinear16LUT returns a defensive copy of the normative table for discovery and external
// conformance runners. Runtime conversion reads the private fixed array directly.
func SRGB8ToLinear16LUT() []uint16 {
	return append([]uint16(nil), srgb8ToLinear16[:]...)
}
