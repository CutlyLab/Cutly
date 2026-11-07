package vectorraster

import (
	"errors"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestValidateLimitsRejectsEveryZeroField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Limits)
	}{
		{"width", func(value *Limits) { value.MaxWidth = 0 }},
		{"height", func(value *Limits) { value.MaxHeight = 0 }},
		{"pixels", func(value *Limits) { value.MaxPixels = 0 }},
		{"paths", func(value *Limits) { value.MaxPaths = 0 }},
		{"commands", func(value *Limits) { value.MaxCommands = 0 }},
		{"edges", func(value *Limits) { value.MaxEdges = 0 }},
		{"exact bits", func(value *Limits) { value.MaxExactBits = 0 }},
		{"canonical bytes", func(value *Limits) { value.MaxCanonicalSourceBytes = 0 }},
		{"working bytes", func(value *Limits) { value.MaxWorkingBytes = 0 }},
		{"retained bytes", func(value *Limits) { value.MaxRetainedBytes = 0 }},
		{"work units", func(value *Limits) { value.MaxWorkUnits = 0 }},
	}
	if err := ValidateLimits(DefaultLimits()); err != nil {
		t.Fatalf("DefaultLimits rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.mutate(&limits)
			assertLimitsInvalid(t, limits)
		})
	}
}

func TestValidateLimitsRejectsEveryHardBoundOverflow(t *testing.T) {
	maxPixels := cpucompositor.HardMaxSurfaceBytes / cpucompositor.BytesPerPixel
	tests := []struct {
		name   string
		mutate func(*Limits)
	}{
		{"width", func(value *Limits) { value.MaxWidth = documentv2.RasterViewportMaxDimension + 1 }},
		{"height", func(value *Limits) { value.MaxHeight = documentv2.RasterViewportMaxDimension + 1 }},
		{"pixels", func(value *Limits) { value.MaxPixels = maxPixels + 1 }},
		{"paths", func(value *Limits) { value.MaxPaths = HardMaxPaths + 1 }},
		{"commands", func(value *Limits) { value.MaxCommands = HardMaxCommands + 1 }},
		{"edges", func(value *Limits) { value.MaxEdges = HardMaxEdges + 1 }},
		{"exact bits", func(value *Limits) { value.MaxExactBits = HardMaxExactBits + 1 }},
		{"canonical bytes", func(value *Limits) { value.MaxCanonicalSourceBytes = HardMaxCanonicalSourceBytes + 1 }},
		{"working bytes", func(value *Limits) { value.MaxWorkingBytes = HardMaxWorkingBytes + 1 }},
		{"retained bytes", func(value *Limits) { value.MaxRetainedBytes = cpucompositor.HardMaxSurfaceBytes + 1 }},
		{"work units", func(value *Limits) { value.MaxWorkUnits = HardMaxWorkUnits + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.mutate(&limits)
			assertLimitsInvalid(t, limits)
		})
	}
}

func TestValidateLimitsAcceptsEveryHardBound(t *testing.T) {
	limits := Limits{
		MaxWidth:  documentv2.RasterViewportMaxDimension,
		MaxHeight: documentv2.RasterViewportMaxDimension,
		MaxPixels: cpucompositor.HardMaxSurfaceBytes / cpucompositor.BytesPerPixel,
		MaxPaths:  HardMaxPaths, MaxCommands: HardMaxCommands, MaxEdges: HardMaxEdges,
		MaxExactBits: HardMaxExactBits, MaxCanonicalSourceBytes: HardMaxCanonicalSourceBytes,
		MaxWorkingBytes: HardMaxWorkingBytes, MaxRetainedBytes: cpucompositor.HardMaxSurfaceBytes,
		MaxWorkUnits: HardMaxWorkUnits,
	}
	if err := ValidateLimits(limits); err != nil {
		t.Fatalf("hard-bound Limits rejected: %v", err)
	}
}

func TestValidateLimitsRejectsRetainedBytesBelowOnePixel(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxRetainedBytes = cpucompositor.BytesPerPixel - 1
	assertLimitsInvalid(t, limits)
}

func assertLimitsInvalid(t *testing.T, limits Limits) {
	t.Helper()
	err := ValidateLimits(limits)
	if !errors.Is(err, ErrLimitsInvalid) {
		t.Fatalf("ValidateLimits error = %v, want ErrLimitsInvalid", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeLimitsInvalid || typed.Path != "limits" {
		t.Fatalf("ValidateLimits typed error = %#v", err)
	}
}
