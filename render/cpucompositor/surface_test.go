package cpucompositor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"
	"time"
)

func TestSurfaceOwnershipInvariantAndCanonicalBytes(t *testing.T) {
	input := []Pixel{{R: 1, G: 2, B: 3, A: 4}, {R: 0x1111, G: 0x2222, B: 0x3333, A: 0x4444}}
	surface, err := NewSurfaceFromPixels(2, 1, input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = Pixel{A: 9}
	got, err := surface.Pixel(0, 0)
	if err != nil || got != (Pixel{R: 1, G: 2, B: 3, A: 4}) {
		t.Fatalf("owned pixel = %+v err=%v", got, err)
	}
	var encoded bytes.Buffer
	if err := surface.WriteCanonical(&encoded); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0, 1, 0, 2, 0, 3, 0, 4,
		0x11, 0x11, 0x22, 0x22, 0x33, 0x33, 0x44, 0x44,
	}
	if !bytes.Equal(encoded.Bytes(), want) {
		t.Fatalf("canonical bytes = %x, want %x", encoded.Bytes(), want)
	}
	firstHash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := surface.CanonicalHash()
	wantHash := "sha256:9b186cb2c3b590eec27ee42df897f25576e6ad22e9a0db82e2f968f15059fac0"
	if err != nil || firstHash != wantHash || secondHash != wantHash {
		t.Fatalf("canonical hash = %q / %q, want %q err=%v", firstHash, secondHash, wantHash, err)
	}
}

func TestCanonicalHashContextMatchesCanonicalHashAndCancelsMidStream(t *testing.T) {
	pixels := make([]Pixel, 4_096)
	for index := range pixels {
		alpha := uint16(index)
		pixels[index] = Pixel{R: alpha / 4, G: alpha / 3, B: alpha / 2, A: alpha}
	}
	surface, err := NewSurfaceFromPixelsWithLimits(64, 64, pixels, Limits{
		MaxWidth: 64, MaxHeight: 64, MaxBytes: uint64(len(pixels)) * BytesPerPixel,
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := surface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	got, err := surface.CanonicalHashContext(context.Background())
	if err != nil || got != want {
		t.Fatalf("context hash=%q want=%q err=%v", got, want, err)
	}
	if got, err := surface.CanonicalHashContext(nil); got != "" || !errors.Is(err, ErrInvalidSurface) {
		t.Fatalf("nil context hash=%q err=%v", got, err)
	}
	ctx := &cancelHashContext{cancelAt: 3}
	if got, err := surface.CanonicalHashContext(ctx); got != "" || !errors.Is(err, context.Canceled) || ctx.checks != 3 {
		t.Fatalf("canceled hash=%q err=%v checks=%d", got, err, ctx.checks)
	}
}

type cancelHashContext struct {
	checks   int
	cancelAt int
}

func (*cancelHashContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*cancelHashContext) Done() <-chan struct{}       { return nil }
func (value *cancelHashContext) Err() error {
	value.checks++
	if value.checks >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
func (*cancelHashContext) Value(any) any { return nil }

func TestReadViewIsZeroCopyImmutableAndBoundsChecked(t *testing.T) {
	surface, err := NewSurfaceFromPixels(2, 1, []Pixel{{R: 1, A: 2}, {G: 3, A: 4}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := surface.View()
	if err != nil {
		t.Fatal(err)
	}
	if view.Width() != 2 || view.Height() != 1 {
		t.Fatalf("view dimensions = %dx%d", view.Width(), view.Height())
	}
	if got, pixelErr := view.Pixel(1, 0); pixelErr != nil || got != (Pixel{G: 3, A: 4}) {
		t.Fatalf("view pixel = %+v, %v", got, pixelErr)
	}
	for _, point := range [][2]int{{-1, 0}, {0, -1}, {2, 0}, {0, 1}} {
		if _, pixelErr := view.Pixel(point[0], point[1]); !errors.Is(pixelErr, ErrBounds) {
			t.Fatalf("Pixel(%d,%d) error = %v", point[0], point[1], pixelErr)
		}
	}
	var zero ReadView
	if zero.Width() != 0 || zero.Height() != 0 {
		t.Fatalf("zero view dimensions = %dx%d", zero.Width(), zero.Height())
	}
	if _, pixelErr := zero.Pixel(0, 0); !errors.Is(pixelErr, ErrBounds) {
		t.Fatalf("zero view error = %v", pixelErr)
	}

	// There is no mutating view API. Concurrent reads observe the same owned Surface bytes.
	want, _ := view.Pixel(0, 0)
	const workers = 16
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				got, pixelErr := view.Pixel(0, 0)
				if pixelErr != nil || got != want {
					errorsSeen <- fmt.Errorf("view changed: %+v, %v", got, pixelErr)
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

func TestSurfaceDimensionsLimitsAndBoundsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		width  int
		height int
		limits Limits
		want   error
	}{
		{name: "zero width", width: 0, height: 1, limits: DefaultLimits(), want: ErrInvalidDimensions},
		{name: "negative height", width: 1, height: -1, limits: DefaultLimits(), want: ErrInvalidDimensions},
		{name: "bad budget", width: 1, height: 1, limits: Limits{MaxWidth: 1, MaxHeight: 1, MaxBytes: HardMaxSurfaceBytes + 1}, want: ErrInvalidDimensions},
		{name: "dimension budget", width: 3, height: 1, limits: Limits{MaxWidth: 2, MaxHeight: 2, MaxBytes: 32}, want: ErrResourceLimit},
		{name: "byte budget", width: 2, height: 2, limits: Limits{MaxWidth: 2, MaxHeight: 2, MaxBytes: 24}, want: ErrResourceLimit},
		{name: "hostile max", width: math.MaxInt, height: math.MaxInt, limits: DefaultLimits(), want: ErrResourceLimit},
		{name: "pixel count overflow", width: math.MaxInt, height: math.MaxInt, limits: Limits{MaxWidth: math.MaxInt, MaxHeight: math.MaxInt, MaxBytes: HardMaxSurfaceBytes}, want: ErrResourceLimit},
		{name: "byte count overflow", width: math.MaxInt, height: 2, limits: Limits{MaxWidth: math.MaxInt, MaxHeight: math.MaxInt, MaxBytes: HardMaxSurfaceBytes}, want: ErrResourceLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			if surface, err := NewSurfaceWithLimits(test.width, test.height, test.limits); surface != nil || !errors.Is(err, test.want) {
				t.Fatalf("surface=%v err=%v, want %v", surface, err, test.want)
			}
		})
	}

	surface, err := NewSurface(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := surface.Pixel(1, 0); !errors.Is(err, ErrBounds) {
		t.Fatalf("bounds error = %v", err)
	}
	if _, err := NewSurfaceFromPixels(1, 1, nil); !errors.Is(err, ErrInvalidSurface) {
		t.Fatalf("length error = %v", err)
	}
	if _, err := NewSurfaceFromPixels(1, 1, []Pixel{{R: 1, A: 0}}); !errors.Is(err, ErrInvalidPixel) {
		t.Fatalf("premultiply error = %v", err)
	}
	if _, err := NewSurfaceFromPixelsWithLimits(2, 1, []Pixel{{}, {}}, Limits{MaxWidth: 2, MaxHeight: 1, MaxBytes: 8}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("explicit input budget error = %v", err)
	}
}

func TestSurfaceZeroValueNilAndShortWriterNeverPanic(t *testing.T) {
	invalid := []*Surface{nil, {}, {width: 1, height: 1}, {width: 1, height: 1, pixels: []Pixel{{R: 1}}}}
	for index, surface := range invalid {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("case %d panicked: %v", index, recovered)
				}
			}()
			if _, err := surface.Pixel(0, 0); err == nil {
				t.Fatalf("case %d unexpectedly valid", index)
			}
			if err := surface.WriteCanonical(io.Discard); err == nil {
				t.Fatalf("case %d unexpectedly wrote", index)
			}
		}()
	}
	valid, err := NewSurface(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.WriteCanonical(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v", err)
	}
	if err := valid.WriteCanonical(nil); !errors.Is(err, ErrInvalidSurface) {
		t.Fatalf("nil writer error = %v", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }

func FuzzSurfaceInputsNeverPanic(f *testing.F) {
	f.Add(int64(1), int64(1), int64(0), int64(0), uint16(0), uint16(0), uint16(0), uint16(0))
	f.Add(int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MinInt64), int64(math.MaxInt64), uint16(65535), uint16(65535), uint16(65535), uint16(65535))
	f.Fuzz(func(t *testing.T, width, height, x, y int64, r, g, b, a uint16) {
		// A tight policy makes hostile dimensions fail before allocation while still exercising
		// every platform-int conversion and bounds branch.
		limits := Limits{MaxWidth: 32, MaxHeight: 32, MaxBytes: 32 * 32 * BytesPerPixel}
		surface, err := NewSurfaceWithLimits(int(width), int(height), limits)
		if err == nil {
			_, _ = surface.Pixel(int(x), int(y))
			_ = surface.WriteCanonical(io.Discard)
			_, _ = surface.CanonicalHash()
		}

		fromPixels, fromPixelsErr := NewSurfaceFromPixelsWithLimits(1, 1, []Pixel{{R: r, G: g, B: b, A: a}}, limits)
		if fromPixelsErr == nil {
			_, _ = fromPixels.Pixel(int(x), int(y))
			_ = fromPixels.WriteCanonical(io.Discard)
		}
	})
}
