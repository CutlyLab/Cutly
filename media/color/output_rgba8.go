package color

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// OutputResult binds one immutable raw RGBA8 frame to every profile needed to interpret it.
type OutputResult struct {
	Width               int         `json:"width"`
	Height              int         `json:"height"`
	Stride              int         `json:"stride"`
	PixelHash           string      `json:"pixel_hash"`
	InputSurfaceProfile string      `json:"input_surface_profile"`
	OutputProfile       string      `json:"output_profile"`
	OutputMetadata      Metadata    `json:"output_metadata"`
	HashProfile         string      `json:"hash_profile"`
	Frame               *RGBA8Frame `json:"-"`
}

// RGBA8Frame owns one complete immutable output frame. Its zero value is invalid.
type RGBA8Frame struct {
	width  int
	height int
	stride int
	pixels []byte
	hash   string
	valid  bool
}

// RGBA8ReadView retains an immutable Frame and supports row-at-a-time encoder consumption without
// allocating or exposing a frame-sized mutable alias. Its zero value is invalid.
type RGBA8ReadView struct {
	frame *RGBA8Frame
}

func (frame *RGBA8Frame) Width() int {
	if frame == nil || !frame.valid {
		return 0
	}
	return frame.width
}

func (frame *RGBA8Frame) Height() int {
	if frame == nil || !frame.valid {
		return 0
	}
	return frame.height
}

func (frame *RGBA8Frame) Stride() int {
	if frame == nil || !frame.valid {
		return 0
	}
	return frame.stride
}

func (frame *RGBA8Frame) PixelHash() (string, error) {
	if err := validateOutputFrame(frame); err != nil {
		return "", err
	}
	return frame.hash, nil
}

func (frame *RGBA8Frame) View() (RGBA8ReadView, error) {
	if err := validateOutputFrame(frame); err != nil {
		return RGBA8ReadView{}, err
	}
	return RGBA8ReadView{frame: frame}, nil
}

func (view RGBA8ReadView) Width() int {
	if view.frame == nil {
		return 0
	}
	return view.frame.Width()
}

func (view RGBA8ReadView) Height() int {
	if view.frame == nil {
		return 0
	}
	return view.frame.Height()
}

func (view RGBA8ReadView) Stride() int {
	if view.frame == nil {
		return 0
	}
	return view.frame.Stride()
}

// CopyRow copies exactly one tightly packed row into caller-owned storage. Destination must have
// exactly Stride bytes; the Frame never retains it.
func (view RGBA8ReadView) CopyRow(row int, destination []byte) error {
	if err := validateOutputFrame(view.frame); err != nil {
		return err
	}
	if row < 0 || row >= view.frame.height {
		return profileError(CodeOutputBounds, "frame/row", ErrOutputBounds)
	}
	if len(destination) != view.frame.stride {
		return profileError(CodeOutputBounds, "frame/row/destination", ErrOutputBounds)
	}
	offset := row * view.frame.stride
	copy(destination, view.frame.pixels[offset:offset+view.frame.stride])
	return nil
}

// Bytes returns a complete defensive copy. It never exposes mutable frame storage.
func (frame *RGBA8Frame) Bytes() ([]byte, error) {
	if err := validateOutputFrame(frame); err != nil {
		return nil, err
	}
	result, err := allocateOutputBytes(len(frame.pixels), "frame/bytes")
	if err != nil {
		return nil, err
	}
	copy(result, frame.pixels)
	return result, nil
}

// WriteCanonical writes raw top-down tightly packed RGBA8 bytes. External writers only receive
// defensive scratch chunks and therefore cannot mutate or retain the Frame backing storage.
func (frame *RGBA8Frame) WriteCanonical(writer io.Writer) error {
	if err := validateOutputFrame(frame); err != nil {
		return err
	}
	if writer == nil {
		return profileError(CodeOutputWriteFailed, "frame/writer", ErrOutputWriteFailed)
	}
	var scratch [OutputCanonicalWriteChunkSize]byte
	for offset := 0; offset < len(frame.pixels); {
		end := offset + len(scratch)
		if end > len(frame.pixels) {
			end = len(frame.pixels)
		}
		chunk := scratch[:end-offset]
		copy(chunk, frame.pixels[offset:end])
		written, err := writer.Write(chunk)
		if written < 0 || written > len(chunk) {
			return profileError(CodeOutputWriteFailed, "frame/writer", errors.Join(ErrOutputWriteFailed, fmt.Errorf("invalid write count %d for %d bytes", written, len(chunk))))
		}
		if err != nil {
			return profileError(CodeOutputWriteFailed, "frame/writer", errors.Join(ErrOutputWriteFailed, err))
		}
		if written != len(chunk) {
			return profileError(CodeOutputWriteFailed, "frame/writer", errors.Join(ErrOutputWriteFailed, io.ErrShortWrite))
		}
		offset = end
	}
	return nil
}

// ConvertSurfaceToRGBA8 converts one immutable reference working surface into exact profile-v1
// straight-alpha sRGB RGBA8 bytes. No partial result is returned.
func ConvertSurfaceToRGBA8(ctx context.Context, surface *cpucompositor.Surface, limits OutputLimits) (OutputResult, error) {
	if ctx == nil {
		return OutputResult{}, profileError(CodeOutputContextInvalid, "context", ErrContextInvalid)
	}
	if err := outputContextFailure(ctx, "context"); err != nil {
		return OutputResult{}, err
	}
	normalizedLimits, err := normalizeOutputLimits(limits)
	if err != nil {
		return OutputResult{}, err
	}
	shape, err := validateOutputShape(surface, normalizedLimits)
	if err != nil {
		return OutputResult{}, err
	}
	if err := outputContextFailure(ctx, "surface"); err != nil {
		return OutputResult{}, err
	}
	view, err := surface.View()
	if err != nil {
		return OutputResult{}, profileError(CodeOutputSurfaceInvalid, "surface", errors.Join(ErrOutputSurfaceInvalid, err))
	}
	if view.Width() != shape.width || view.Height() != shape.height {
		return OutputResult{}, profileError(CodeOutputSurfaceInvalid, "surface/dimensions", ErrOutputSurfaceInvalid)
	}
	if err := outputContextFailure(ctx, "surface"); err != nil {
		return OutputResult{}, err
	}
	pixels, err := allocateOutputBytes(shape.outputBytes, "output/pixels")
	if err != nil {
		return OutputResult{}, err
	}
	for y := 0; y < shape.height; y++ {
		if contextErr := ctx.Err(); contextErr != nil {
			return OutputResult{}, profileError(CodeOutputCanceled, fmt.Sprintf("output/rows/%d", y), errors.Join(ErrCanceled, contextErr))
		}
		rowOffset := y * shape.stride
		for x := 0; x < shape.width; x++ {
			pixel, pixelErr := view.Pixel(x, y)
			if pixelErr != nil {
				return OutputResult{}, profileError(CodeOutputSurfaceInvalid, fmt.Sprintf("surface/pixels/%d", y*shape.width+x), errors.Join(ErrOutputSurfaceInvalid, pixelErr))
			}
			offset := rowOffset + x*4
			pixels[offset], pixels[offset+1], pixels[offset+2], pixels[offset+3] = outputPixel(pixel)
		}
	}
	hash, err := hashOutputBytes(ctx, pixels)
	if err != nil {
		return OutputResult{}, err
	}
	if err := outputContextFailure(ctx, "publish"); err != nil {
		return OutputResult{}, err
	}
	frame := &RGBA8Frame{
		width: shape.width, height: shape.height, stride: shape.stride,
		pixels: pixels, hash: hash, valid: true,
	}
	return OutputResult{
		Width: shape.width, Height: shape.height, Stride: shape.stride, PixelHash: hash,
		InputSurfaceProfile: cpucompositor.SurfaceProfileV1,
		OutputProfile:       ProfileLinearRGBA16ToRGBA8SRGBV1,
		OutputMetadata:      MetadataRGBA8SRGBOutputV1(),
		HashProfile:         OutputRGBAHashProfileV1,
		Frame:               frame,
	}, nil
}

type outputShape struct {
	width       int
	height      int
	stride      int
	outputBytes int
}

func validateOutputShape(surface *cpucompositor.Surface, limits OutputLimits) (outputShape, error) {
	if surface == nil {
		return outputShape{}, profileError(CodeOutputSurfaceInvalid, "surface", ErrOutputSurfaceInvalid)
	}
	width, height := surface.Width(), surface.Height()
	if width <= 0 || height <= 0 {
		return outputShape{}, profileError(CodeOutputSurfaceInvalid, "surface/dimensions", ErrOutputSurfaceInvalid)
	}
	if width > limits.MaxWidth || height > limits.MaxHeight ||
		width > HardMaxOutputDimension || height > HardMaxOutputDimension {
		return outputShape{}, profileError(CodeOutputResourceLimit, "surface/dimensions", ErrResourceLimit)
	}
	unsignedWidth, unsignedHeight := uint64(width), uint64(height)
	if unsignedHeight != 0 && unsignedWidth > math.MaxUint64/unsignedHeight {
		return outputShape{}, profileError(CodeOutputResourceLimit, "output/pixel_count", ErrResourceLimit)
	}
	pixelCount := unsignedWidth * unsignedHeight
	if pixelCount > math.MaxUint64/BytesPerOutputRGBA8Pixel {
		return outputShape{}, profileError(CodeOutputResourceLimit, "output/bytes", ErrResourceLimit)
	}
	outputBytes := pixelCount * BytesPerOutputRGBA8Pixel
	if outputBytes > limits.MaxOutputBytes || outputBytes > uint64(maxInt()) {
		return outputShape{}, profileError(CodeOutputResourceLimit, "output/bytes", ErrResourceLimit)
	}
	if unsignedWidth > uint64(maxInt())/BytesPerOutputRGBA8Pixel {
		return outputShape{}, profileError(CodeOutputResourceLimit, "output/stride", ErrResourceLimit)
	}
	return outputShape{
		width: width, height: height, stride: width * int(BytesPerOutputRGBA8Pixel),
		outputBytes: int(outputBytes),
	}, nil
}

func normalizeOutputLimits(value OutputLimits) (OutputLimits, error) {
	if value == (OutputLimits{}) {
		value = DefaultOutputLimits()
	}
	if value.MaxWidth <= 0 || value.MaxHeight <= 0 ||
		value.MaxWidth > HardMaxOutputDimension || value.MaxHeight > HardMaxOutputDimension ||
		value.MaxOutputBytes < BytesPerOutputRGBA8Pixel || value.MaxOutputBytes > HardMaxOutputRGBA8Bytes {
		return OutputLimits{}, profileError(CodeOutputResourceLimit, "output/limits", ErrResourceLimit)
	}
	return value, nil
}

func outputPixel(pixel cpucompositor.Pixel) (r, g, b, a byte) {
	a = byte(roundRatioEven(uint64(pixel.A)*255, 65535))
	if pixel.A == 0 || a == 0 {
		return 0, 0, 0, 0
	}
	r = linear16ToSRGB8(unpremultiplyLinear16(pixel.R, pixel.A))
	g = linear16ToSRGB8(unpremultiplyLinear16(pixel.G, pixel.A))
	b = linear16ToSRGB8(unpremultiplyLinear16(pixel.B, pixel.A))
	return r, g, b, a
}

func unpremultiplyLinear16(channel, alpha uint16) uint16 {
	if alpha == 0 || channel == 0 {
		return 0
	}
	return roundRatioEven(uint64(channel)*65535, uint64(alpha))
}

var linear16ToSRGB8Lookup = buildLinear16ToSRGB8Lookup()

func buildLinear16ToSRGB8Lookup() [1 << 16]byte {
	var result [1 << 16]byte
	for value := range result {
		result[value] = nearestSRGB8(uint16(value))
	}
	return result
}

// linear16ToSRGB8 reads a process-wide immutable table derived only from the normative forward
// LUT and nearest/even rule.
func linear16ToSRGB8(value uint16) byte {
	return linear16ToSRGB8Lookup[value]
}

func nearestSRGB8(value uint16) byte {
	low, high := 0, len(srgb8ToLinear16)
	for low < high {
		middle := low + (high-low)/2
		if srgb8ToLinear16[middle] < value {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low == 0 {
		return 0
	}
	if low == len(srgb8ToLinear16) {
		return 255
	}
	if srgb8ToLinear16[low] == value {
		return byte(low)
	}
	lower := low - 1
	lowerDistance := uint32(value) - uint32(srgb8ToLinear16[lower])
	upperDistance := uint32(srgb8ToLinear16[low]) - uint32(value)
	if upperDistance < lowerDistance || upperDistance == lowerDistance && low&1 == 0 {
		return byte(low)
	}
	return byte(lower)
}

func hashOutputBytes(ctx context.Context, pixels []byte) (string, error) {
	hash := sha256.New()
	for offset := 0; offset < len(pixels); {
		if err := outputContextFailure(ctx, "output/hash"); err != nil {
			return "", err
		}
		end := offset + OutputCanonicalWriteChunkSize
		if end > len(pixels) {
			end = len(pixels)
		}
		_, _ = hash.Write(pixels[offset:end])
		offset = end
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func validateOutputFrame(frame *RGBA8Frame) error {
	if frame == nil || !frame.valid || frame.width <= 0 || frame.height <= 0 ||
		frame.stride <= 0 || frame.hash == "" {
		return profileError(CodeOutputFrameInvalid, "frame", ErrOutputFrameInvalid)
	}
	if frame.width > maxInt()/int(BytesPerOutputRGBA8Pixel) ||
		frame.stride != frame.width*int(BytesPerOutputRGBA8Pixel) ||
		frame.height > maxInt()/frame.stride || len(frame.pixels) != frame.height*frame.stride {
		return profileError(CodeOutputFrameInvalid, "frame/layout", ErrOutputFrameInvalid)
	}
	return nil
}

func allocateOutputBytes(count int, path string) (bytes []byte, err error) {
	defer func() {
		if recover() != nil {
			bytes = nil
			err = profileError(CodeOutputAllocationFailed, path, ErrAllocationFailed)
		}
	}()
	return make([]byte, count), nil
}

func outputContextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return profileError(CodeOutputContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return profileError(CodeOutputCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}
