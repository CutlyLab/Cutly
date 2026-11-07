package expressionlayer

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func BenchmarkCompile(b *testing.B) {
	document := fixtureDocument()
	config := DefaultConfig()
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		provider, err := Compile(context.Background(), document, config)
		if err != nil || provider == nil {
			b.Fatalf("Compile: provider=%v err=%v", provider, err)
		}
	}
}

func BenchmarkFrameCached(b *testing.B) {
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	request := fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2})
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		frame, frameErr := provider.Frame(context.Background(), request)
		if frameErr != nil || frame.Surface == nil {
			b.Fatalf("Frame: frame=%+v err=%v", frame, frameErr)
		}
	}
}
