package visualcompositor

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func FuzzRenderAtProviderResponsesNeverPublishPartialResults(f *testing.F) {
	for _, seed := range []struct {
		value int64
		rate  uint16
		mode  uint8
	}{{0, 1, 0}, {1, 2, 1}, {3, 2, 2}, {10, 1, 3}} {
		f.Add(seed.value, seed.rate, seed.mode)
	}
	document := imageDocument(false)
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		f.Fatalf("diagnostics=%+v", diagnostics)
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(2, 2, []cpucompositor.Pixel{{A: 1}, {A: 2}, {A: 3}, {A: 4}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, value int64, rate uint16, mode uint8) {
		if rate == 0 {
			rate = 1
		}
		var mutate func(*rastercompositor.NativeSourceFrame)
		switch mode % 4 {
		case 1:
			mutate = func(frame *rastercompositor.NativeSourceFrame) { frame.Request.ClipIndex++ }
		case 2:
			mutate = func(frame *rastercompositor.NativeSourceFrame) { frame.PixelHash = shaA() }
		case 3:
			mutate = func(frame *rastercompositor.NativeSourceFrame) { frame.Surface = nil }
		}
		result, renderErr := program.RenderAt(context.Background(), "timeline", documentv2.RationalTime{Value: value, Rate: int64(rate)}, Providers{
			Native: nativeProvider(surface, mutate),
		})
		if renderErr != nil {
			if !zeroResult(result) {
				t.Fatalf("error published partial result=%+v err=%v", result, renderErr)
			}
			return
		}
		if err := program.ValidateResult(context.Background(), result); err != nil {
			t.Fatalf("successful result invalid: %v", err)
		}
	})
}
