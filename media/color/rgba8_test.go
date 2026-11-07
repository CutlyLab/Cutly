package color

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestNormalizeRGBA8GoldenOddImageAndSurfaceInvariant(t *testing.T) {
	encoded := []byte{
		255, 0, 128, 0,
		128, 64, 32, 128,
		255, 255, 255, 255,
		1, 2, 3, 1,
		10, 11, 12, 127,
		13, 14, 15, 129,
		0, 128, 255, 254,
		250, 100, 50, 85,
		42, 84, 126, 170,
	}
	input := RGBA8Input{Width: 3, Height: 3, Stride: 12, Metadata: MetadataRGBA8SRGBV1(), Pixels: encoded}
	surface, err := NormalizeRGBA8(context.Background(), input, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if surface.Width() != 3 || surface.Height() != 3 {
		t.Fatalf("dimensions = %dx%d, want 3x3", surface.Width(), surface.Height())
	}
	want := []cpucompositor.Pixel{
		{},
		{R: 7101, G: 1687, B: 475, A: 32896},
		{R: 65535, G: 65535, B: 65535, A: 65535},
		{A: 257},
		{R: 99, G: 109, B: 120, A: 32639},
		{R: 134, G: 146, B: 158, A: 33153},
		{G: 14091, B: 65278, A: 65278},
		{R: 20883, G: 2784, B: 697, A: 21845},
		{R: 1011, G: 3873, B: 9115, A: 43690},
	}
	for index, expected := range want {
		got, pixelErr := surface.Pixel(index%3, index/3)
		if pixelErr != nil {
			t.Fatal(pixelErr)
		}
		if got != expected {
			t.Fatalf("pixel %d = %+v, want %+v", index, got, expected)
		}
		if got.R > got.A || got.G > got.A || got.B > got.A {
			t.Fatalf("pixel %d violates premultiplied surface invariant: %+v", index, got)
		}
	}
	gotHash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = "sha256:520d38b67e97d5d3d781dd2e070ba152d5d6ef55ba25b84219dfeced4f434a27"
	if gotHash != wantHash {
		t.Fatalf("golden surface hash = %q, want %q", gotHash, wantHash)
	}
}

func TestNormalizeRGBA8AlphaExpansionAndRounding(t *testing.T) {
	encoded := make([]byte, 256*4)
	for alpha := 0; alpha < 256; alpha++ {
		offset := alpha * 4
		encoded[offset], encoded[offset+1], encoded[offset+2], encoded[offset+3] = 255, 255, 255, byte(alpha)
	}
	surface, err := NormalizeRGBA8(context.Background(), RGBA8Input{
		Width: 256, Height: 1, Stride: 1024, Metadata: MetadataRGBA8SRGBV1(), Pixels: encoded,
	}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for alpha := 0; alpha < 256; alpha++ {
		got, pixelErr := surface.Pixel(alpha, 0)
		if pixelErr != nil {
			t.Fatal(pixelErr)
		}
		want := uint16(alpha * 257)
		if got != (cpucompositor.Pixel{R: want, G: want, B: want, A: want}) {
			t.Fatalf("alpha %d pixel = %+v, want every channel %d", alpha, got, want)
		}
	}

	for _, test := range []struct {
		numerator   uint64
		denominator uint64
		want        uint16
	}{
		{numerator: 1, denominator: 2, want: 0},
		{numerator: 3, denominator: 2, want: 2},
		{numerator: 5, denominator: 2, want: 2},
		{numerator: 7, denominator: 2, want: 4},
		{numerator: 4, denominator: 3, want: 1},
		{numerator: 5, denominator: 3, want: 2},
	} {
		if got := roundRatioEven(test.numerator, test.denominator); got != test.want {
			t.Fatalf("roundRatioEven(%d,%d) = %d, want %d", test.numerator, test.denominator, got, test.want)
		}
	}
}

func TestNormalizeRGBA8RejectsEveryMetadataMismatch(t *testing.T) {
	tests := []struct {
		name string
		path string
		edit func(*Metadata)
	}{
		{name: "profile", path: "metadata/profile", edit: func(value *Metadata) { value.Profile = "" }},
		{name: "pixel layout", path: "metadata/pixel_layout", edit: func(value *Metadata) { value.PixelLayout = "bgra8" }},
		{name: "row order", path: "metadata/row_order", edit: func(value *Metadata) { value.RowOrder = "bottom-up" }},
		{name: "alpha", path: "metadata/alpha_mode", edit: func(value *Metadata) { value.AlphaMode = "premultiplied" }},
		{name: "primaries", path: "metadata/primaries", edit: func(value *Metadata) { value.Primaries = "display-p3" }},
		{name: "white point", path: "metadata/white_point", edit: func(value *Metadata) { value.WhitePoint = "d50" }},
		{name: "transfer", path: "metadata/transfer", edit: func(value *Metadata) { value.Transfer = "bt709" }},
		{name: "range", path: "metadata/range", edit: func(value *Metadata) { value.Range = "limited" }},
		{name: "chroma", path: "metadata/chroma_subsampling", edit: func(value *Metadata) { value.ChromaSubsampling = "4:2:0" }},
		{name: "ICC omitted", path: "metadata/icc_profile", edit: func(value *Metadata) { value.ICCProfile = "" }},
		{name: "ICC present", path: "metadata/icc_profile", edit: func(value *Metadata) { value.ICCProfile = "embedded" }},
		{name: "HDR omitted", path: "metadata/hdr_metadata", edit: func(value *Metadata) { value.HDRMetadata = "" }},
		{name: "HDR present", path: "metadata/hdr_metadata", edit: func(value *Metadata) { value.HDRMetadata = "static-metadata" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(1, 1)
			test.edit(&input.Metadata)
			surface, err := NormalizeRGBA8(context.Background(), input, Limits{})
			if surface != nil || !errors.Is(err, ErrMetadataInvalid) {
				t.Fatalf("surface=%v err=%v, want metadata rejection", surface, err)
			}
			assertProfileError(t, err, CodeMetadataInvalid, test.path)
		})
	}
}

func TestNormalizeRGBA8DoesNotEchoHostileMetadata(t *testing.T) {
	input := validInput(1, 1)
	input.Metadata.Profile = strings.Repeat("untrusted", 128*1024)
	surface, err := NormalizeRGBA8(context.Background(), input, Limits{})
	if surface != nil || !errors.Is(err, ErrMetadataInvalid) {
		t.Fatalf("surface=%v err=%v, want metadata rejection", surface, err)
	}
	assertProfileError(t, err, CodeMetadataInvalid, "metadata/profile")
	if len(err.Error()) > 256 || strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("metadata error echoed untrusted input: length=%d", len(err.Error()))
	}
}

func TestNormalizeRGBA8RejectsDimensionsLayoutAndResourceAbuse(t *testing.T) {
	valid := validInput(2, 2)
	tests := []struct {
		name   string
		input  RGBA8Input
		limits Limits
		want   error
		code   ErrorCode
		path   string
	}{
		{name: "zero width", input: RGBA8Input{Height: 1, Metadata: MetadataRGBA8SRGBV1()}, want: ErrDimensionsInvalid, code: CodeDimensionsInvalid, path: "dimensions"},
		{name: "negative height", input: RGBA8Input{Width: 1, Height: -1, Stride: 4, Metadata: MetadataRGBA8SRGBV1()}, want: ErrDimensionsInvalid, code: CodeDimensionsInvalid, path: "dimensions"},
		{name: "bad stride", input: RGBA8Input{Width: 2, Height: 1, Stride: 9, Metadata: MetadataRGBA8SRGBV1(), Pixels: make([]byte, 8)}, want: ErrLayoutInvalid, code: CodeLayoutInvalid, path: "stride"},
		{name: "short buffer", input: RGBA8Input{Width: 2, Height: 1, Stride: 8, Metadata: MetadataRGBA8SRGBV1(), Pixels: make([]byte, 7)}, want: ErrLayoutInvalid, code: CodeLayoutInvalid, path: "pixels"},
		{name: "extra buffer", input: RGBA8Input{Width: 2, Height: 1, Stride: 8, Metadata: MetadataRGBA8SRGBV1(), Pixels: make([]byte, 9)}, want: ErrLayoutInvalid, code: CodeLayoutInvalid, path: "pixels"},
		{name: "dimension budget", input: valid, limits: Limits{MaxWidth: 1, MaxHeight: 2, MaxInputBytes: 16, MaxWorkingBytes: 64}, want: ErrResourceLimit, code: CodeResourceLimit, path: "dimensions"},
		{name: "input budget", input: valid, limits: Limits{MaxWidth: 2, MaxHeight: 2, MaxInputBytes: 15, MaxWorkingBytes: 64}, want: ErrResourceLimit, code: CodeResourceLimit, path: "pixels"},
		{name: "working budget", input: valid, limits: Limits{MaxWidth: 2, MaxHeight: 2, MaxInputBytes: 16, MaxWorkingBytes: 63}, want: ErrResourceLimit, code: CodeResourceLimit, path: "working_bytes"},
		{name: "invalid hard budget", input: valid, limits: Limits{MaxWidth: 2, MaxHeight: 2, MaxInputBytes: HardMaxInputBytes + 1, MaxWorkingBytes: 64}, want: ErrResourceLimit, code: CodeResourceLimit, path: "limits"},
		{name: "pixel count overflow", input: RGBA8Input{Width: math.MaxInt, Height: math.MaxInt, Metadata: MetadataRGBA8SRGBV1()}, limits: Limits{MaxWidth: math.MaxInt, MaxHeight: math.MaxInt, MaxInputBytes: HardMaxInputBytes, MaxWorkingBytes: HardMaxWorkingBytes}, want: ErrResourceLimit, code: CodeResourceLimit, path: "pixel_count"},
		{name: "stride overflow", input: RGBA8Input{Width: math.MaxInt, Height: 1, Metadata: MetadataRGBA8SRGBV1()}, limits: Limits{MaxWidth: math.MaxInt, MaxHeight: 1, MaxInputBytes: HardMaxInputBytes, MaxWorkingBytes: HardMaxWorkingBytes}, want: ErrResourceLimit, code: CodeResourceLimit, path: "stride"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface, err := NormalizeRGBA8(context.Background(), test.input, test.limits)
			if surface != nil || !errors.Is(err, test.want) {
				t.Fatalf("surface=%v err=%v, want %v", surface, err, test.want)
			}
			assertProfileError(t, err, test.code, test.path)
		})
	}
}

func TestNormalizeRGBA8CancellationNeverReturnsPartialSurface(t *testing.T) {
	if surface, err := NormalizeRGBA8(nil, validInput(1, 1), Limits{}); surface != nil || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context surface=%v err=%v", surface, err)
	} else {
		assertProfileError(t, err, CodeContextInvalid, "context")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if surface, err := NormalizeRGBA8(canceled, validInput(1, 1), Limits{}); surface != nil ||
		!errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled surface=%v err=%v", surface, err)
	} else {
		assertProfileError(t, err, CodeCanceled, "context")
	}

	beforeAllocation := &cancelAfterChecksContext{cancelAt: 2}
	surface, err := NormalizeRGBA8(beforeAllocation, validInput(4, 4), Limits{})
	if surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-allocation cancellation surface=%v err=%v", surface, err)
	}
	assertProfileError(t, err, CodeCanceled, "pixels")

	ctx := &cancelAfterChecksContext{cancelAt: 5}
	surface, err = NormalizeRGBA8(ctx, validInput(4, 4), Limits{})
	if surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-frame cancellation surface=%v err=%v", surface, err)
	}
	assertProfileError(t, err, CodeCanceled, "pixels/row/2")

	afterSurfaceCopy := &cancelAfterChecksContext{cancelAt: 5}
	surface, err = NormalizeRGBA8(afterSurfaceCopy, validInput(1, 1), Limits{})
	if surface != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-copy cancellation surface=%v err=%v", surface, err)
	}
	assertProfileError(t, err, CodeCanceled, "surface")
}

func TestNormalizeRGBA8OwnsOutputAndIsConcurrentSafe(t *testing.T) {
	input := validInput(17, 9)
	surface, err := NormalizeRGBA8(context.Background(), input, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantPixel, err := surface.Pixel(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	for index := range input.Pixels {
		input.Pixels[index] ^= 0xff
	}
	gotPixel, err := surface.Pixel(0, 0)
	if err != nil || gotPixel != wantPixel {
		t.Fatalf("surface aliases caller bytes: pixel=%+v want=%+v err=%v", gotPixel, wantPixel, err)
	}
	gotHash, err := surface.CanonicalHash()
	if err != nil || gotHash != wantHash {
		t.Fatalf("surface hash changed after caller mutation: %q want %q err=%v", gotHash, wantHash, err)
	}

	shared := validInput(17, 9)
	baseline, err := NormalizeRGBA8(context.Background(), shared, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	baselineHash, err := baseline.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 10; iteration++ {
				got, normalizeErr := NormalizeRGBA8(context.Background(), shared, Limits{})
				if normalizeErr != nil {
					errorsSeen <- normalizeErr
					return
				}
				hash, hashErr := got.CanonicalHash()
				if hashErr != nil {
					errorsSeen <- hashErr
					return
				}
				if hash != baselineHash {
					errorsSeen <- errors.New("concurrent normalization produced a different canonical hash")
					return
				}
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for concurrentErr := range errorsSeen {
		t.Fatal(concurrentErr)
	}
}

func FuzzNormalizeRGBA8NeverPanics(f *testing.F) {
	f.Add(int64(1), int64(1), int64(4), []byte{0, 0, 0, 0}, uint8(0))
	f.Add(int64(math.MaxInt64), int64(math.MinInt64), int64(-1), []byte{1, 2, 3}, uint8(1))
	f.Add(int64(3), int64(3), int64(12), make([]byte, 37), uint8(0))
	f.Fuzz(func(t *testing.T, width, height, stride int64, pixels []byte, invalidMetadata uint8) {
		metadata := MetadataRGBA8SRGBV1()
		if invalidMetadata&1 != 0 {
			metadata.Transfer = ""
		}
		limits := Limits{MaxWidth: 32, MaxHeight: 32, MaxInputBytes: 32 * 32 * 4, MaxWorkingBytes: 32 * 32 * 16}
		surface, err := NormalizeRGBA8(context.Background(), RGBA8Input{
			Width: int(width), Height: int(height), Stride: int(stride), Metadata: metadata, Pixels: pixels,
		}, limits)
		if err != nil {
			if surface != nil {
				t.Fatal("failed normalization returned a partial surface")
			}
			return
		}
		if surface == nil || surface.Width() != int(width) || surface.Height() != int(height) {
			t.Fatalf("successful normalization returned wrong surface: %v", surface)
		}
		for y := 0; y < surface.Height(); y++ {
			for x := 0; x < surface.Width(); x++ {
				pixel, pixelErr := surface.Pixel(x, y)
				if pixelErr != nil || pixel.R > pixel.A || pixel.G > pixel.A || pixel.B > pixel.A {
					t.Fatalf("surface invariant at %d,%d: %+v err=%v", x, y, pixel, pixelErr)
				}
			}
		}
	})
}

func validInput(width, height int) RGBA8Input {
	pixels := make([]byte, width*height*4)
	for index := 0; index < width*height; index++ {
		offset := index * 4
		pixels[offset] = byte(index*29 + 1)
		pixels[offset+1] = byte(index*47 + 2)
		pixels[offset+2] = byte(index*71 + 3)
		pixels[offset+3] = byte(index*17 + 4)
	}
	return RGBA8Input{
		Width: width, Height: height, Stride: width * 4,
		Metadata: MetadataRGBA8SRGBV1(), Pixels: pixels,
	}
}

func assertProfileError(t *testing.T, err error, code ErrorCode, path string) {
	t.Helper()
	var profileErr *Error
	if !errors.As(err, &profileErr) {
		t.Fatalf("error %v has type %T, want *color.Error", err, err)
	}
	if profileErr.Code != code || profileErr.Path != path {
		t.Fatalf("error code/path = %s/%q, want %s/%q", profileErr.Code, profileErr.Path, code, path)
	}
}

type cancelAfterChecksContext struct {
	calls    atomic.Int64
	cancelAt int64
}

func (value *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *cancelAfterChecksContext) Done() <-chan struct{}       { return nil }
func (value *cancelAfterChecksContext) Value(any) any               { return nil }
func (value *cancelAfterChecksContext) Err() error {
	if value.calls.Add(1) >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
