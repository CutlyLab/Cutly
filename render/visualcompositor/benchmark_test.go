package visualcompositor

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
)

func BenchmarkRenderAtImage(b *testing.B) {
	document := imageDocument(false)
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		b.Fatalf("diagnostics=%+v", diagnostics)
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(2, 2, []cpucompositor.Pixel{{A: 1}, {A: 2}, {A: 3}, {A: 4}})
	if err != nil {
		b.Fatal(err)
	}
	providers := Providers{Native: nativeProvider(surface, nil)}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), providers); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderAtVectorTransition(b *testing.B) {
	document := vectorTransitionDocument()
	provider, err := expressionlayer.Compile(context.Background(), document, expressionlayer.DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	program, diagnostics := Compile(context.Background(), document, DefaultConfig(2, 2))
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		b.Fatalf("diagnostics=%+v", diagnostics)
	}
	providers := Providers{Expression: provider}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := program.RenderAt(context.Background(), "timeline", rt(3, 2), providers); err != nil {
			b.Fatal(err)
		}
	}
}
