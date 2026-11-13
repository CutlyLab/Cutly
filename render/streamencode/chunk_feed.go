package streamencode

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// FrameWriter is the encoder-side contract of the chunk feeder; *Encoder satisfies it.
type FrameWriter interface {
	FrameBytes() int
	WriteFrame(ctx context.Context, frame []byte) error
}

// FeedChunkVideo streams one ordered chunk's canonical surface payload (raw big-endian
// premultiplied-linear RGBA16, width*height*8 bytes per frame — exactly what the reference
// chunk profile spools) into the encoder as straight-alpha RGBA8 frames. The conversion runs
// through the exact reference output-color stage (linear BT.709 to sRGB), so frames fed from
// chunks are pixel-identical to frames rendered straight through a session.
func FeedChunkVideo(ctx context.Context, writer FrameWriter, payload io.Reader, frameCount int64, width, height int) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrConfiguration)
	}
	if writer == nil || payload == nil || width <= 0 || height <= 0 || frameCount < 0 {
		return fmt.Errorf("%w: writer, payload, positive geometry and frame count are required", ErrConfiguration)
	}
	unitBytes := int64(uint64(width) * uint64(height) * cpucompositor.BytesPerPixel)
	if int64(writer.FrameBytes()) != int64(width)*int64(height)*4 {
		return fmt.Errorf("%w: encoder frame is %d bytes, geometry wants %d", ErrConfiguration, writer.FrameBytes(), int64(width)*int64(height)*4)
	}
	limits := color.DefaultOutputLimits()
	for frame := int64(0); frame < frameCount; frame++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := make([]byte, unitBytes)
		if _, err := io.ReadFull(payload, raw); err != nil {
			return fmt.Errorf("%w: chunk frame %d: %v", ErrEncodeFailed, frame, err)
		}
		surface, err := surfaceFromCanonical(raw, width, height)
		if err != nil {
			return fmt.Errorf("%w: chunk frame %d: %v", ErrEncodeFailed, frame, err)
		}
		output, err := color.ConvertSurfaceToRGBA8(ctx, surface, limits)
		if err != nil {
			return fmt.Errorf("%w: chunk frame %d color: %v", ErrEncodeFailed, frame, err)
		}
		frameBytes, err := output.Frame.Bytes()
		if err != nil {
			return fmt.Errorf("%w: chunk frame %d bytes: %v", ErrEncodeFailed, frame, err)
		}
		if err := writer.WriteFrame(ctx, frameBytes); err != nil {
			return fmt.Errorf("%w: chunk frame %d: %v", ErrEncodeFailed, frame, err)
		}
	}
	var extra [1]byte
	if count, err := payload.Read(extra[:]); count != 0 || !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: chunk payload has trailing bytes after %d frames", ErrEncodeFailed, frameCount)
	}
	return nil
}

// surfaceFromCanonical decodes the reference chunk unit format back into a validated surface.
func surfaceFromCanonical(raw []byte, width, height int) (*cpucompositor.Surface, error) {
	pixels := make([]cpucompositor.Pixel, width*height)
	for index := range pixels {
		offset := index * int(cpucompositor.BytesPerPixel)
		pixels[index] = cpucompositor.Pixel{
			R: uint16(raw[offset])<<8 | uint16(raw[offset+1]),
			G: uint16(raw[offset+2])<<8 | uint16(raw[offset+3]),
			B: uint16(raw[offset+4])<<8 | uint16(raw[offset+5]),
			A: uint16(raw[offset+6])<<8 | uint16(raw[offset+7]),
		}
	}
	return cpucompositor.NewSurfaceFromPixels(width, height, pixels)
}

// directFrameBytes converts one surface through the exact output-color stage; the
// differential test uses it to prove the chunk shell adds nothing.
func directFrameBytes(ctx context.Context, surface *cpucompositor.Surface) ([]byte, error) {
	output, err := color.ConvertSurfaceToRGBA8(ctx, surface, color.DefaultOutputLimits())
	if err != nil {
		return nil, err
	}
	return output.Frame.Bytes()
}
