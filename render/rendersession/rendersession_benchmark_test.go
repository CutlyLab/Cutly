package rendersession

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

// Benchmarks compare the per-frame cost of the complete reference export against the session's
// production profile on the same compiled pipeline. The differential test above proves the two
// produce identical bytes; these numbers show what the production verification profile saves.
func benchmarkSession(b *testing.B, width, height int) *Session {
	document, bindings, config := sessionFixtureTB(b, width, height)
	session, err := Open(context.Background(), document, bindings, config, Options{})
	if err != nil {
		b.Fatal(err)
	}
	return session
}

func BenchmarkReferenceRenderAt640x480(b *testing.B) {
	document, bindings, config := sessionFixtureTB(b, 640, 480)
	program, diagnostics, err := referencevisual.Compile(context.Background(), document, bindings, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		b.Fatalf("compile diagnostics=%+v err=%v", diagnostics, err)
	}
	at := videoedit.RationalTime{Value: 35, Rate: 30}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := program.RenderAt(context.Background(), "timeline", at); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSessionRenderFrame640x480(b *testing.B) {
	session := benchmarkSession(b, 640, 480)
	defer session.Close()
	at := videoedit.RationalTime{Value: 35, Rate: 30}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := session.RenderFrame(context.Background(), "timeline", at); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSessionRenderSurface640x480(b *testing.B) {
	session := benchmarkSession(b, 640, 480)
	defer session.Close()
	at := videoedit.RationalTime{Value: 35, Rate: 30}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := session.RenderSurface(context.Background(), "timeline", at); err != nil {
			b.Fatal(err)
		}
	}
}
