package color

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"math/big"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestConvertSurfaceToRGBA8OpaqueAllCodesRoundTrip(t *testing.T) {
	pixels := make([]cpucompositor.Pixel, 256)
	for code := 0; code < 256; code++ {
		pixels[code] = cpucompositor.Pixel{
			R: srgb8ToLinear16[code],
			G: srgb8ToLinear16[255-code],
			B: srgb8ToLinear16[(code*73)%256],
			A: 65535,
		}
	}
	surface := mustOutputSurface(t, 256, 1, pixels)
	result, err := ConvertSurfaceToRGBA8(context.Background(), surface, OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := result.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for code := 0; code < 256; code++ {
		offset := code * 4
		want := [4]byte{byte(code), byte(255 - code), byte((code * 73) % 256), 255}
		if actual := [4]byte{got[offset], got[offset+1], got[offset+2], got[offset+3]}; actual != want {
			t.Fatalf("code %d = %v, want %v", code, actual, want)
		}
	}
	if result.Width != 256 || result.Height != 1 || result.Stride != 1024 ||
		result.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		result.OutputProfile != ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		result.OutputMetadata != MetadataRGBA8SRGBOutputV1() ||
		result.HashProfile != OutputRGBAHashProfileV1 {
		t.Fatalf("result identity is incomplete: %+v", result)
	}
}

func TestConvertSurfaceToRGBA8AlphaAndTransparentRules(t *testing.T) {
	pixels := make([]cpucompositor.Pixel, 256)
	for alpha := 0; alpha < 256; alpha++ {
		expanded := uint16(alpha * 257)
		pixels[alpha] = cpucompositor.Pixel{R: expanded, G: expanded, B: expanded, A: expanded}
	}
	result, err := ConvertSurfaceToRGBA8(context.Background(), mustOutputSurface(t, 256, 1, pixels), OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := result.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for alpha := 0; alpha < 256; alpha++ {
		offset := alpha * 4
		want := [4]byte{255, 255, 255, byte(alpha)}
		if alpha == 0 {
			want = [4]byte{}
		}
		if actual := [4]byte{got[offset], got[offset+1], got[offset+2], got[offset+3]}; actual != want {
			t.Fatalf("alpha %d = %v, want %v", alpha, actual, want)
		}
	}

	lowAlpha := mustOutputSurface(t, 2, 1, []cpucompositor.Pixel{
		{R: 128, G: 64, B: 1, A: 128},
		{R: 129, G: 129, B: 129, A: 129},
	})
	low, err := ConvertSurfaceToRGBA8(context.Background(), lowAlpha, OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	lowBytes, err := low.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lowBytes, []byte{0, 0, 0, 0, 255, 255, 255, 1}) {
		t.Fatalf("low-alpha boundary = %v", lowBytes)
	}
}

func TestLinear16ToSRGB8MatchesIndependentNearestOracleExhaustively(t *testing.T) {
	for value := 0; value <= math.MaxUint16; value++ {
		want := 0
		wantDistance := uint32(math.MaxUint32)
		for code, decoded := range srgb8ToLinear16 {
			distance := absoluteDifference(uint16(value), decoded)
			if distance < wantDistance || distance == wantDistance && code&1 == 0 && want&1 == 1 {
				want, wantDistance = code, distance
			}
		}
		if got := linear16ToSRGB8(uint16(value)); got != byte(want) {
			t.Fatalf("linear %d -> %d, want %d (distance %d)", value, got, want, wantDistance)
		}
	}
}

func TestOutputAlphaAndUnpremultiplicationRoundingOracle(t *testing.T) {
	for alpha := 0; alpha <= math.MaxUint16; alpha++ {
		want := exactRoundRatioEvenOracle(uint64(alpha)*255, 65535)
		_, _, _, got := outputPixel(cpucompositor.Pixel{A: uint16(alpha)})
		if got != byte(want) {
			t.Fatalf("alpha %d -> %d, want %d", alpha, got, want)
		}
	}

	for alpha := 1; alpha <= math.MaxUint16; alpha++ {
		channels := [...]uint16{0, 1, uint16(alpha / 2), uint16(alpha - 1), uint16(alpha)}
		for _, channel := range channels {
			if int(channel) > alpha {
				continue
			}
			want := exactRoundRatioEvenOracle(uint64(channel)*65535, uint64(alpha))
			if got := unpremultiplyLinear16(channel, uint16(alpha)); got != want {
				t.Fatalf("channel/alpha %d/%d -> %d, want %d", channel, alpha, got, want)
			}
		}
	}

	for alpha := 2; alpha <= math.MaxUint16; alpha += 2 {
		if got := unpremultiplyLinear16(uint16(alpha/2), uint16(alpha)); got != 32768 {
			t.Fatalf("odd quotient half tie at alpha %d = %d, want 32768", alpha, got)
		}
	}
	for alpha := 6; alpha <= math.MaxUint16; alpha += 6 {
		if got := unpremultiplyLinear16(uint16(alpha/6), uint16(alpha)); got != 10922 {
			t.Fatalf("even quotient half tie at alpha %d = %d, want 10922", alpha, got)
		}
	}
	if got := unpremultiplyLinear16(math.MaxUint16, math.MaxUint16); got != math.MaxUint16 {
		t.Fatalf("maximum product = %d, want %d", got, uint16(math.MaxUint16))
	}
	for alpha := uint16(0); alpha <= 128; alpha++ {
		gotR, gotG, gotB, gotA := outputPixel(cpucompositor.Pixel{R: alpha, G: alpha, B: alpha, A: alpha})
		if got := [4]byte{gotR, gotG, gotB, gotA}; got != ([4]byte{}) {
			t.Fatalf("alpha that quantizes to zero leaked hidden color: alpha=%d output=%v", alpha, got)
		}
		if alpha == 128 {
			break
		}
	}
}

func TestOutputPixelUnpremultiplicationUsesOriginalAlphaAndNearestEven(t *testing.T) {
	tests := []struct {
		name  string
		pixel cpucompositor.Pixel
		want  [4]byte
	}{
		{name: "transparent", pixel: cpucompositor.Pixel{}, want: [4]byte{}},
		{name: "opaque midpoint lower even", pixel: cpucompositor.Pixel{R: 10, A: 65535}, want: [4]byte{0, 0, 0, 255}},
		{name: "opaque midpoint upper even", pixel: cpucompositor.Pixel{R: 30, A: 65535}, want: [4]byte{2, 0, 0, 255}},
		{name: "original alpha divisor", pixel: cpucompositor.Pixel{R: 7101, G: 1687, B: 475, A: 32896}, want: [4]byte{128, 64, 32, 128}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, g, b, a := outputPixel(test.pixel)
			if got := [4]byte{r, g, b, a}; got != test.want {
				t.Fatalf("outputPixel(%+v) = %v, want %v", test.pixel, got, test.want)
			}
		})
	}
}

func TestConvertSurfaceToRGBA8GoldenHashAndDefensiveAccessors(t *testing.T) {
	surface := mustOutputSurface(t, 2, 2, []cpucompositor.Pixel{
		{},
		{R: 7101, G: 1687, B: 475, A: 32896},
		{R: 65535, G: 65535, B: 65535, A: 65535},
		{R: 99, G: 109, B: 120, A: 32639},
	})
	result, err := ConvertSurfaceToRGBA8(context.Background(), surface, OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = "sha256:a68c817a3db80843316465d47b9040acc2d90daae534941e2a6ba06067d36403"
	if result.PixelHash != wantHash {
		t.Fatalf("golden hash = %q, want %q", result.PixelHash, wantHash)
	}
	if got, err := result.Frame.PixelHash(); err != nil || got != wantHash {
		t.Fatalf("frame hash = %q err=%v", got, err)
	}
	first, err := result.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := append([]byte(nil), first...)
	first[0] ^= 0xff
	second, err := result.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, wantBytes) {
		t.Fatal("Bytes exposed mutable frame storage")
	}
	var encoded bytes.Buffer
	if err := result.Frame.WriteCanonical(&encoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded.Bytes(), wantBytes) {
		t.Fatalf("WriteCanonical = %v, want %v", encoded.Bytes(), wantBytes)
	}

	view, err := result.Frame.View()
	if err != nil {
		t.Fatal(err)
	}
	if view.Width() != 2 || view.Height() != 2 || view.Stride() != 8 {
		t.Fatalf("view geometry = %dx%d/%d", view.Width(), view.Height(), view.Stride())
	}
	row := make([]byte, view.Stride())
	if err := view.CopyRow(1, row); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(row, wantBytes[8:]) {
		t.Fatalf("row = %v, want %v", row, wantBytes[8:])
	}
	row[0] ^= 0xff
	if err := view.CopyRow(1, row); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(row, wantBytes[8:]) {
		t.Fatal("CopyRow exposed mutable frame storage")
	}
	if err := view.CopyRow(-1, row); !errors.Is(err, ErrOutputBounds) {
		t.Fatalf("negative row error=%v", err)
	}
	if err := view.CopyRow(2, row); !errors.Is(err, ErrOutputBounds) {
		t.Fatalf("high row error=%v", err)
	}
	if err := view.CopyRow(0, row[:7]); !errors.Is(err, ErrOutputBounds) {
		t.Fatalf("short destination error=%v", err)
	}
	if err := (RGBA8ReadView{}).CopyRow(0, row); !errors.Is(err, ErrOutputFrameInvalid) {
		t.Fatalf("zero view error=%v", err)
	}
}

func TestRGBA8FrameWriteCanonicalDoesNotExposeBacking(t *testing.T) {
	pixels := make([]cpucompositor.Pixel, 4096*5)
	for index := range pixels {
		pixels[index] = cpucompositor.Pixel{R: uint16(index), G: uint16(index), B: uint16(index), A: 65535}
	}
	result, err := ConvertSurfaceToRGBA8(context.Background(), mustOutputSurface(t, 4096, 5, pixels), OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := result.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	writer := &hostileOutputWriter{}
	if err := result.Frame.WriteCanonical(writer); err != nil {
		t.Fatal(err)
	}
	for _, retained := range writer.retained {
		for index := range retained {
			retained[index] ^= 0x3c
		}
	}
	after, err := result.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("hostile writer mutated frame backing storage")
	}
	if writer.calls < 2 {
		t.Fatalf("large frame used %d write call(s), want multiple defensive chunks", writer.calls)
	}
	if !bytes.Equal(writer.captured, before) {
		t.Fatal("WriteCanonical did not present the original complete bytes to the writer")
	}
}

func TestRGBA8FrameRejectsInvalidWritersAndForgedFrames(t *testing.T) {
	result, err := ConvertSurfaceToRGBA8(context.Background(), mustOutputSurface(t, 1, 1, []cpucompositor.Pixel{{A: 65535}}), OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		writer io.Writer
		want   error
	}{
		{name: "nil", writer: nil, want: ErrOutputWriteFailed},
		{name: "short", writer: outputCountWriter{count: 3}, want: ErrOutputWriteFailed},
		{name: "negative", writer: outputCountWriter{count: -1}, want: ErrOutputWriteFailed},
		{name: "too long", writer: outputCountWriter{count: 5}, want: ErrOutputWriteFailed},
		{name: "error", writer: outputCountWriter{count: 0, err: io.ErrClosedPipe}, want: ErrOutputWriteFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := result.Frame.WriteCanonical(test.writer); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
	for _, frame := range []*RGBA8Frame{nil, {}, {valid: true}} {
		if bytes, err := frame.Bytes(); bytes != nil || !errors.Is(err, ErrOutputFrameInvalid) {
			t.Fatalf("forged frame Bytes=%v err=%v", bytes, err)
		}
		if err := frame.WriteCanonical(io.Discard); !errors.Is(err, ErrOutputFrameInvalid) {
			t.Fatalf("forged frame WriteCanonical err=%v", err)
		}
	}
}

func TestConvertSurfaceToRGBA8RejectsContextSurfaceAndLimits(t *testing.T) {
	valid := mustOutputSurface(t, 2, 2, []cpucompositor.Pixel{{}, {}, {}, {}})
	if result, err := ConvertSurfaceToRGBA8(nil, valid, OutputLimits{}); result != (OutputResult{}) || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context result=%+v err=%v", result, err)
	} else {
		assertProfileError(t, err, CodeOutputContextInvalid, "context")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := ConvertSurfaceToRGBA8(canceled, valid, OutputLimits{}); result != (OutputResult{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled result=%+v err=%v", result, err)
	} else {
		assertProfileError(t, err, CodeOutputCanceled, "context")
	}
	if result, err := ConvertSurfaceToRGBA8(context.Background(), nil, OutputLimits{}); result != (OutputResult{}) || !errors.Is(err, ErrOutputSurfaceInvalid) {
		t.Fatalf("nil surface result=%+v err=%v", result, err)
	}
	if result, err := ConvertSurfaceToRGBA8(context.Background(), &cpucompositor.Surface{}, OutputLimits{}); result != (OutputResult{}) || !errors.Is(err, ErrOutputSurfaceInvalid) {
		t.Fatalf("zero surface result=%+v err=%v", result, err)
	}
	tests := []struct {
		name   string
		limits OutputLimits
		path   string
	}{
		{name: "width", limits: OutputLimits{MaxWidth: 1, MaxHeight: 2, MaxOutputBytes: 16}, path: "surface/dimensions"},
		{name: "bytes", limits: OutputLimits{MaxWidth: 2, MaxHeight: 2, MaxOutputBytes: 15}, path: "output/bytes"},
		{name: "hard bytes", limits: OutputLimits{MaxWidth: 2, MaxHeight: 2, MaxOutputBytes: HardMaxOutputRGBA8Bytes + 1}, path: "output/limits"},
		{name: "hard width", limits: OutputLimits{MaxWidth: HardMaxOutputDimension + 1, MaxHeight: 2, MaxOutputBytes: 16}, path: "output/limits"},
		{name: "hard height", limits: OutputLimits{MaxWidth: 2, MaxHeight: HardMaxOutputDimension + 1, MaxOutputBytes: 16}, path: "output/limits"},
		{name: "platform max dimensions", limits: OutputLimits{MaxWidth: math.MaxInt, MaxHeight: math.MaxInt, MaxOutputBytes: 16}, path: "output/limits"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ConvertSurfaceToRGBA8(context.Background(), valid, test.limits)
			if result != (OutputResult{}) || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertProfileError(t, err, CodeOutputResourceLimit, test.path)
		})
	}

	mid := &cancelAfterChecksContext{cancelAt: 5}
	if result, err := ConvertSurfaceToRGBA8(mid, valid, OutputLimits{}); result != (OutputResult{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("mid-frame result=%+v err=%v", result, err)
	} else {
		assertProfileError(t, err, CodeOutputCanceled, "output/rows/1")
	}
}

func TestConvertSurfaceToRGBA8HardDimensionGate(t *testing.T) {
	boundaryPixels := make([]cpucompositor.Pixel, HardMaxOutputDimension)
	boundary, err := cpucompositor.NewSurfaceFromPixelsWithLimits(
		HardMaxOutputDimension, 1, boundaryPixels,
		cpucompositor.Limits{
			MaxWidth: HardMaxOutputDimension, MaxHeight: 1,
			MaxBytes: uint64(HardMaxOutputDimension) * cpucompositor.BytesPerPixel,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ConvertSurfaceToRGBA8(context.Background(), boundary, OutputLimits{
		MaxWidth: HardMaxOutputDimension, MaxHeight: 1,
		MaxOutputBytes: uint64(HardMaxOutputDimension) * BytesPerOutputRGBA8Pixel,
	})
	if err != nil || result.Width != HardMaxOutputDimension || result.Height != 1 {
		t.Fatalf("hard dimension boundary result=%+v err=%v", result, err)
	}

	oversizedWidth := HardMaxOutputDimension + 1
	oversized, err := cpucompositor.NewSurfaceFromPixelsWithLimits(
		oversizedWidth, 1, make([]cpucompositor.Pixel, oversizedWidth),
		cpucompositor.Limits{
			MaxWidth: oversizedWidth, MaxHeight: 1,
			MaxBytes: uint64(oversizedWidth) * cpucompositor.BytesPerPixel,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err = ConvertSurfaceToRGBA8(context.Background(), oversized, OutputLimits{
		MaxWidth: HardMaxOutputDimension, MaxHeight: 1,
		MaxOutputBytes: uint64(oversizedWidth) * BytesPerOutputRGBA8Pixel,
	})
	if result != (OutputResult{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized skinny surface result=%+v err=%v", result, err)
	}
	assertProfileError(t, err, CodeOutputResourceLimit, "surface/dimensions")
}

func TestConvertSurfaceToRGBA8ConcurrentAccessIsStable(t *testing.T) {
	pixels := make([]cpucompositor.Pixel, 37*19)
	for index := range pixels {
		alpha := uint16(index*97%65535 + 1)
		pixels[index] = cpucompositor.Pixel{
			R: uint16(index*13) % (alpha + 1),
			G: uint16(index*17) % (alpha + 1),
			B: uint16(index*19) % (alpha + 1),
			A: alpha,
		}
	}
	surface := mustOutputSurface(t, 37, 19, pixels)
	baseline, err := ConvertSurfaceToRGBA8(context.Background(), surface, OutputLimits{})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 10; iteration++ {
				got, convertErr := ConvertSurfaceToRGBA8(context.Background(), surface, OutputLimits{})
				if convertErr != nil {
					errs <- convertErr
					return
				}
				if got.PixelHash != baseline.PixelHash {
					errs <- errors.New("concurrent conversion hash drift")
					return
				}
				var sink bytes.Buffer
				if writeErr := baseline.Frame.WriteCanonical(&sink); writeErr != nil {
					errs <- writeErr
					return
				}
				if _, bytesErr := baseline.Frame.Bytes(); bytesErr != nil {
					errs <- bytesErr
					return
				}
				view, viewErr := baseline.Frame.View()
				if viewErr != nil {
					errs <- viewErr
					return
				}
				row := make([]byte, view.Stride())
				if rowErr := view.CopyRow(iteration%view.Height(), row); rowErr != nil {
					errs <- rowErr
					return
				}
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func FuzzConvertSurfaceToRGBA8NeverPanics(f *testing.F) {
	f.Add(uint8(1), uint8(1), []byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(uint8(7), uint8(9), []byte{255, 1, 2, 3, 4, 5, 6, 7})
	f.Fuzz(func(t *testing.T, widthByte, heightByte uint8, data []byte) {
		width, height := int(widthByte%16)+1, int(heightByte%16)+1
		pixels := make([]cpucompositor.Pixel, width*height)
		for index := range pixels {
			var values [4]uint16
			for channel := range values {
				offset := (index*8 + channel*2)
				if len(data) != 0 {
					values[channel] = uint16(data[offset%len(data)])<<8 | uint16(data[(offset+1)%len(data)])
				}
			}
			alpha := values[3]
			pixels[index] = cpucompositor.Pixel{
				R: uint16(uint32(values[0]) * uint32(alpha) / 65535),
				G: uint16(uint32(values[1]) * uint32(alpha) / 65535),
				B: uint16(uint32(values[2]) * uint32(alpha) / 65535),
				A: alpha,
			}
		}
		surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
		if err != nil {
			t.Fatal(err)
		}
		result, err := ConvertSurfaceToRGBA8(context.Background(), surface, OutputLimits{
			MaxWidth: 16, MaxHeight: 16, MaxOutputBytes: 16 * 16 * 4,
		})
		if err != nil {
			if result != (OutputResult{}) {
				t.Fatal("failed conversion returned partial result")
			}
			return
		}
		if result.Frame == nil || result.Width != width || result.Height != height || result.Stride != width*4 {
			t.Fatalf("invalid successful result: %+v", result)
		}
		bytes, bytesErr := result.Frame.Bytes()
		if bytesErr != nil || len(bytes) != width*height*4 {
			t.Fatalf("invalid successful frame: len=%d err=%v", len(bytes), bytesErr)
		}
	})
}

func absoluteDifference(left, right uint16) uint32 {
	if left >= right {
		return uint32(left - right)
	}
	return uint32(right - left)
}

func exactRoundRatioEvenOracle(numerator, denominator uint64) uint16 {
	if denominator == 0 {
		panic("zero denominator in test oracle")
	}
	numeratorBig := new(big.Int).SetUint64(numerator)
	denominatorBig := new(big.Int).SetUint64(denominator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numeratorBig, denominatorBig, remainder)
	doubledRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := doubledRemainder.Cmp(denominatorBig)
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() || quotient.Uint64() > math.MaxUint16 {
		panic("test oracle result does not fit uint16")
	}
	return uint16(quotient.Uint64())
}

func mustOutputSurface(t *testing.T, width, height int, pixels []cpucompositor.Pixel) *cpucompositor.Surface {
	t.Helper()
	surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

type hostileOutputWriter struct {
	retained [][]byte
	captured []byte
	calls    int
}

func (writer *hostileOutputWriter) Write(value []byte) (int, error) {
	writer.calls++
	writer.captured = append(writer.captured, value...)
	writer.retained = append(writer.retained, value)
	for index := range value {
		value[index] ^= 0xa5
	}
	return len(value), nil
}

type outputCountWriter struct {
	count int
	err   error
}

func (writer outputCountWriter) Write([]byte) (int, error) { return writer.count, writer.err }
