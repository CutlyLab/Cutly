package videodecode

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// boundedBuffer retains at most limit bytes while continuing to report writes
// as consumed. That prevents an untrusted child from blocking on a full pipe.
type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func newBoundedBuffer(limit int64) *boundedBuffer { return &boundedBuffer{limit: limit} }

func (value *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := value.limit - int64(value.buffer.Len())
	if remaining > 0 {
		keep := int64(len(data))
		if keep > remaining {
			keep = remaining
		}
		_, _ = value.buffer.Write(data[:int(keep)])
	}
	if int64(original) > remaining {
		value.exceeded = true
	}
	return original, nil
}

func (value *boundedBuffer) String() string { return value.buffer.String() }

// rgba64PixelWriter consumes FFmpeg's byte stream directly into one Pixel
// scratch slice. Surface construction makes the one required immutable copy,
// so live decoded-frame storage is exactly two RGBA16 frame buffers instead of
// raw bytes plus scratch pixels plus a Surface copy.
type rgba64PixelWriter struct {
	pixels       []cpucompositor.Pixel
	expected     int64
	accepted     int64
	pixelIndex   int
	pending      [8]byte
	pendingBytes int
	exceeded     bool
	invalidAlpha int
}

func newRGBA64PixelWriter(width, height int, options Options) (writer *rgba64PixelWriter, err error) {
	frameBytes, err := checkedDecodedFrameBytes(width, height, options.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	count := frameBytes / 8
	if count <= 0 || count > int64(maxIntValue()) {
		return nil, fmt.Errorf("%w: decoded pixel count", ErrResourceLimit)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			writer = nil
			err = fmt.Errorf("%w: allocate decoded pixel buffer", ErrResourceLimit)
		}
	}()
	return &rgba64PixelWriter{
		pixels:       make([]cpucompositor.Pixel, int(count)),
		expected:     frameBytes,
		invalidAlpha: -1,
	}, nil
}

func (writer *rgba64PixelWriter) Write(data []byte) (int, error) {
	original := len(data)
	if original == 0 {
		return 0, nil
	}
	remaining := writer.expected - writer.accepted
	keep := len(data)
	if remaining <= 0 {
		writer.exceeded = true
		return original, nil
	}
	if int64(keep) > remaining {
		keep = int(remaining)
		writer.exceeded = true
	}
	writer.consume(data[:keep])
	writer.accepted += int64(keep)
	return original, nil
}

func (writer *rgba64PixelWriter) consume(data []byte) {
	if writer.pendingBytes > 0 {
		needed := len(writer.pending) - writer.pendingBytes
		if needed > len(data) {
			needed = len(data)
		}
		copy(writer.pending[writer.pendingBytes:], data[:needed])
		writer.pendingBytes += needed
		data = data[needed:]
		if writer.pendingBytes == len(writer.pending) {
			writer.storePixel(writer.pending[:])
			writer.pendingBytes = 0
		}
	}
	for len(data) >= len(writer.pending) {
		writer.storePixel(data[:len(writer.pending)])
		data = data[len(writer.pending):]
	}
	if len(data) > 0 {
		copy(writer.pending[:], data)
		writer.pendingBytes = len(data)
	}
}

func (writer *rgba64PixelWriter) storePixel(raw []byte) {
	if writer.pixelIndex >= len(writer.pixels) {
		writer.exceeded = true
		return
	}
	alpha := binary.LittleEndian.Uint16(raw[6:8])
	if alpha != math.MaxUint16 && writer.invalidAlpha < 0 {
		writer.invalidAlpha = writer.pixelIndex
	}
	writer.pixels[writer.pixelIndex] = cpucompositor.Pixel{
		R: binary.LittleEndian.Uint16(raw[0:2]),
		G: binary.LittleEndian.Uint16(raw[2:4]),
		B: binary.LittleEndian.Uint16(raw[4:6]),
		A: alpha,
	}
	writer.pixelIndex++
}

func (writer *rgba64PixelWriter) Surface(width, height int, options Options) (*cpucompositor.Surface, error) {
	if writer == nil {
		return nil, fmt.Errorf("%w: missing decoded output", ErrOutput)
	}
	if writer.exceeded {
		return nil, fmt.Errorf("%w: decoded output exceeds exact frame size", ErrResourceLimit)
	}
	if writer.accepted != writer.expected || writer.pendingBytes != 0 || writer.pixelIndex != len(writer.pixels) {
		return nil, fmt.Errorf("%w: decoded bytes %d, expected %d", ErrOutput, writer.accepted, writer.expected)
	}
	if writer.invalidAlpha >= 0 {
		return nil, fmt.Errorf("%w: unexpected alpha at pixel %d", ErrOutput, writer.invalidAlpha)
	}
	return cpucompositor.NewSurfaceFromPixelsWithLimits(width, height, writer.pixels, cpucompositor.Limits{
		MaxWidth: options.MaxWidth, MaxHeight: options.MaxHeight, MaxBytes: uint64(writer.expected),
	})
}

func maxIntValue() int { return int(^uint(0) >> 1) }
