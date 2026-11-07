package referencevisualchunk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"github.com/CutlyLab/Cutly/render/backendstage"
	"github.com/CutlyLab/Cutly/render/chunkplan"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestProviderBindsProgramRequestAndCanonicalSurface(t *testing.T) {
	program, scope := testProgramAndScope(t)
	provider, err := New(context.Background(), program, scope)
	if err != nil {
		t.Fatal(err)
	}
	binding := provider.Binding()
	if !bindingValueOnly(binding) || binding.Profile != ProfileV1 || binding.ProgramProfile != referencevisual.ProfileV2 ||
		binding.DocumentHash != program.DocumentHash() || binding.PlanFingerprint != scope.PlanFingerprint ||
		binding.ExecutionManifestFingerprint != scope.ExecutionManifestFingerprint ||
		binding.ImplementationFingerprint != scope.ImplementationFingerprint || binding.Fingerprint != provider.BindingFingerprint() {
		t.Fatalf("binding=%+v", binding)
	}
	manifestFingerprint, err := canonicalFingerprint(program.SourceManifest())
	if err != nil || manifestFingerprint != binding.SourceManifestFingerprint {
		t.Fatalf("manifest fingerprint=%s err=%v binding=%s", manifestFingerprint, err, binding.SourceManifestFingerprint)
	}
	configFingerprint, err := canonicalFingerprint(program.Config())
	if err != nil || configFingerprint != binding.ProgramConfigFingerprint {
		t.Fatalf("config fingerprint=%s err=%v binding=%s", configFingerprint, err, binding.ProgramConfigFingerprint)
	}

	raw, err := CanonicalBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBinding(raw)
	if err != nil || decoded != binding {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
	var hostile map[string]any
	if err := json.Unmarshal(raw, &hostile); err != nil {
		t.Fatal(err)
	}
	hostile["unknown"] = int64(1)
	hostileRaw, err := canonicaljson.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBinding(hostileRaw); err == nil {
		t.Fatal("unknown binding field accepted")
	}
	for _, test := range []struct {
		name   string
		mutate func(*Binding)
	}{
		{name: "plan", mutate: func(value *Binding) { value.PlanFingerprint = digest('e') }},
		{name: "manifest", mutate: func(value *Binding) { value.ExecutionManifestFingerprint = digest('e') }},
		{name: "timeline", mutate: func(value *Binding) { value.TimelineID = "other" }},
		{name: "target", mutate: func(value *Binding) { value.Target.Width++ }},
		{name: "range", mutate: func(value *Binding) { value.FrameRange.First++ }},
		{name: "seed", mutate: func(value *Binding) { value.DeterministicSeed++ }},
		{name: "implementation", mutate: func(value *Binding) { value.ImplementationFingerprint = digest('e') }},
		{name: "program_profile", mutate: func(value *Binding) { value.ProgramProfile = referencevisual.ProfileV1 }},
		{name: "document", mutate: func(value *Binding) { value.DocumentHash = digest('e') }},
		{name: "source_manifest", mutate: func(value *Binding) { value.SourceManifestFingerprint = digest('e') }},
		{name: "config", mutate: func(value *Binding) { value.ProgramConfigFingerprint = digest('e') }},
		{name: "descriptor", mutate: func(value *Binding) { value.ProfileDescriptorFingerprint = digest('e') }},
	} {
		t.Run("binding_"+test.name, func(t *testing.T) {
			tampered := binding
			test.mutate(&tampered)
			if _, err := CanonicalBinding(tampered); !errors.Is(err, ErrBinding) {
				t.Fatalf("tampered binding err=%v", err)
			}
		})
	}

	request := requestFromScope(scope)
	surface, err := provider.RenderSurface(context.Background(), request, scope.FrameRange.First)
	if err != nil || surface == nil {
		t.Fatalf("surface=%v err=%v", surface, err)
	}
	expected, err := program.RenderSurfaceFrame(context.Background(), documentv2.ID(scope.TimelineID), scope.Target.FrameClock, scope.FrameRange.First)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(surfaceBytes(t, surface), surfaceBytes(t, expected.Result.Surface)) {
		t.Fatal("provider surface bytes drifted from Program")
	}
	hash, err := surface.CanonicalHash()
	if err != nil || hash != expected.Result.PixelHash {
		t.Fatalf("hash=%s want=%s err=%v", hash, expected.Result.PixelHash, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*backendstage.ReferenceChunkRequest)
	}{
		{name: "plan", mutate: func(value *backendstage.ReferenceChunkRequest) { value.PlanFingerprint = digest('e') }},
		{name: "manifest", mutate: func(value *backendstage.ReferenceChunkRequest) { value.ExecutionManifestFingerprint = digest('e') }},
		{name: "timeline", mutate: func(value *backendstage.ReferenceChunkRequest) { value.TimelineID = "other" }},
		{name: "target", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.Width++ }},
		{name: "target_frame_clock", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.FrameClock.FramesPerSecond.Value++ }},
		{name: "target_sample_clock", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.SampleClock.SamplesPerSecond++ }},
		{name: "target_channels", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.Channels++ }},
		{name: "target_pixel_format", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.PixelFormat = "rgba8" }},
		{name: "target_color_space", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.ColorSpace = "srgb" }},
		{name: "target_timeline", mutate: func(value *backendstage.ReferenceChunkRequest) { value.Target.TimelineID = "other" }},
		{name: "seed", mutate: func(value *backendstage.ReferenceChunkRequest) { value.DeterministicSeed++ }},
	} {
		t.Run("request_"+test.name, func(t *testing.T) {
			hostile := request
			test.mutate(&hostile)
			if got, renderErr := provider.RenderSurface(context.Background(), hostile, scope.FrameRange.First); got != nil || !errors.Is(renderErr, ErrRequestMismatch) {
				t.Fatalf("surface=%v err=%v", got, renderErr)
			}
		})
	}
	for _, frame := range []int64{scope.FrameRange.First - 1, scope.FrameRange.First + scope.FrameRange.Count} {
		if got, renderErr := provider.RenderSurface(context.Background(), request, frame); got != nil || !errors.Is(renderErr, ErrFrameRange) {
			t.Fatalf("out-of-range frame=%d surface=%v err=%v", frame, got, renderErr)
		}
	}
}

func TestProviderAcceptsV1Program(t *testing.T) {
	_, scope := testProgramAndScope(t)
	document := testVectorDocument()
	config := referencevisual.DefaultConfig(4, 3)
	config.BuildFingerprint = digest('b')
	program, diagnostics, err := referencevisual.Compile(context.Background(), document, nil, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile Program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	provider, err := New(context.Background(), program, scope)
	if err != nil || provider.Binding().ProgramProfile != referencevisual.ProfileV1 {
		t.Fatalf("provider=%v binding=%+v err=%v", provider, provider.Binding(), err)
	}
	surface, err := provider.RenderSurface(context.Background(), requestFromScope(scope), scope.FrameRange.First)
	if err != nil || surface == nil || surface.Width() != scope.Target.Width || surface.Height() != scope.Target.Height {
		t.Fatalf("surface=%v err=%v", surface, err)
	}
	expected, err := program.RenderSurfaceFrame(context.Background(), "timeline", scope.Target.FrameClock, scope.FrameRange.First)
	if err != nil || len(expected.Result.Sources) != 1 || !bytes.Equal(surfaceBytes(t, surface), surfaceBytes(t, expected.Result.Surface)) {
		t.Fatalf("non-empty V1 surface mismatch expected=%+v err=%v", expected, err)
	}
}

func TestCanonicalProfileAndBindingFixtures(t *testing.T) {
	program, scope := testProgramAndScope(t)
	provider, err := New(context.Background(), program, scope)
	if err != nil {
		t.Fatal(err)
	}
	descriptorCanonical, err := canonicaljson.Marshal(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	descriptorFixture, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil || !bytes.Equal(bytes.TrimSpace(descriptorFixture), descriptorCanonical) {
		t.Fatalf("descriptor fixture err=%v\nwant=%s\ngot=%s", err, bytes.TrimSpace(descriptorFixture), descriptorCanonical)
	}
	if got, want := DescriptorHash(), "sha256:4d5510bcd3f1bcf141e0fab050bf6266962ea92fe05dc9412ac1d93d51524fbc"; got != want {
		t.Fatalf("descriptor hash=%s want=%s", got, want)
	}
	bindingCanonical, err := CanonicalBinding(provider.Binding())
	if err != nil {
		t.Fatal(err)
	}
	bindingFixture, err := os.ReadFile("testdata/binding-v1.canonical.json")
	if err != nil || !bytes.Equal(bytes.TrimSpace(bindingFixture), bindingCanonical) {
		t.Fatalf("binding fixture err=%v\nwant=%s\ngot=%s", err, bytes.TrimSpace(bindingFixture), bindingCanonical)
	}
	if fingerprint, err := canonicalFingerprint(provider.Binding()); err != nil || fingerprint != "sha256:a3495e15bae6d6ec7fd0eed29146d15f91ccdc8289f6425f13d9bc64248f020f" {
		t.Fatalf("binding canonical hash=%s err=%v", fingerprint, err)
	}
	decoded, err := DecodeBinding(bytes.TrimSpace(bindingFixture))
	if err != nil || decoded != provider.Binding() {
		t.Fatalf("decoded binding=%+v err=%v", decoded, err)
	}
}

func TestDecodeBindingRejectsNonCanonicalMalformedAndOverflow(t *testing.T) {
	program, scope := testProgramAndScope(t)
	provider, err := New(context.Background(), program, scope)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := CanonicalBinding(provider.Binding())
	if err != nil {
		t.Fatal(err)
	}
	var missing map[string]any
	if err := json.Unmarshal(raw, &missing); err != nil {
		t.Fatal(err)
	}
	delete(missing, "document_hash")
	missingRaw, err := canonicaljson.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	overflow := provider.Binding()
	overflow.FrameRange = videoedit.TickRange{First: math.MaxInt64, Count: 1}
	overflow.Fingerprint, err = bindingFingerprint(overflow)
	if err != nil {
		t.Fatal(err)
	}
	overflowRaw, err := canonicaljson.Marshal(overflow)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "trailing_whitespace", raw: append(append([]byte(nil), raw...), '\n')},
		{name: "missing_field", raw: missingRaw},
		{name: "oversized", raw: bytes.Repeat([]byte{'x'}, MaxBindingBytes+1)},
		{name: "invalid_utf8", raw: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
		{name: "range_overflow", raw: overflowRaw},
	} {
		t.Run(test.name, func(t *testing.T) {
			if value, decodeErr := DecodeBinding(test.raw); value != (Binding{}) || decodeErr == nil {
				t.Fatalf("binding=%+v err=%v", value, decodeErr)
			}
		})
	}
}

func TestProviderConcurrentArbitraryOrderCancellationAndOwnership(t *testing.T) {
	program, scope := testProgramAndScope(t)
	provider, err := New(context.Background(), program, scope)
	if err != nil {
		t.Fatal(err)
	}
	wantBinding := provider.Binding()
	wantResources := provider.VideoResources()

	// Mutating caller-owned construction values cannot alter the Provider.
	scope.Target.Width++
	scope.TimelineID = "mutated"
	if provider.Binding() != wantBinding || provider.VideoResources() != wantResources {
		t.Fatal("Provider retained caller-owned construction state")
	}

	request := backendstage.ReferenceChunkRequest{
		PlanFingerprint: wantBinding.PlanFingerprint, ExecutionManifestFingerprint: wantBinding.ExecutionManifestFingerprint,
		TimelineID: wantBinding.TimelineID, Target: wantBinding.Target, DeterministicSeed: wantBinding.DeterministicSeed,
	}
	const workers = 24
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			frame := wantBinding.FrameRange.First + int64((workers-index-1)%int(wantBinding.FrameRange.Count))
			surface, renderErr := provider.RenderSurface(context.Background(), request, frame)
			if renderErr == nil && (surface == nil || surface.Width() != wantBinding.Target.Width || surface.Height() != wantBinding.Target.Height) {
				renderErr = errors.New("concurrent surface identity drift")
			}
			errs <- renderErr
		}(index)
	}
	wait.Wait()
	close(errs)
	for renderErr := range errs {
		if renderErr != nil {
			t.Fatal(renderErr)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if surface, renderErr := provider.RenderSurface(canceled, request, wantBinding.FrameRange.First); surface != nil || !errors.Is(renderErr, ErrCanceled) || !errors.Is(renderErr, context.Canceled) {
		t.Fatalf("canceled surface=%v err=%v", surface, renderErr)
	}
	if surface, renderErr := provider.RenderSurface(nil, request, wantBinding.FrameRange.First); surface != nil || !errors.Is(renderErr, ErrConfiguration) {
		t.Fatalf("nil context surface=%v err=%v", surface, renderErr)
	}
	broken := *provider
	broken.program = nil
	if surface, renderErr := broken.RenderSurface(context.Background(), request, wantBinding.FrameRange.First); surface != nil || !errors.Is(renderErr, ErrConfiguration) {
		t.Fatalf("broken provider surface=%v err=%v", surface, renderErr)
	}
}

func TestVideoResourcesExactOneLessAndOverflow(t *testing.T) {
	program, scope := testProgramAndScope(t)
	provider, err := New(context.Background(), program, scope)
	if err != nil {
		t.Fatal(err)
	}
	resources := provider.VideoResources()
	config := program.Config()
	wantMemory := config.PipelineLimits.MaxRuntimeWorkingBytes + providerFixedMemoryBytes
	unit := uint64(config.Width * config.Height * int(cpucompositor.BytesPerPixel))
	wantDisk := 2*unit*uint64(chunkplan.FrameGroupCount) + cas.PublicationOverheadBytes
	if resources != (stagedag.ResourceEstimate{MemoryBytes: wantMemory, DiskBytes: wantDisk}) {
		t.Fatalf("resources=%+v want memory=%d disk=%d", resources, wantMemory, wantDisk)
	}
	if err := provider.ValidateVideoResources(resources); err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateVideoResources(stagedag.ResourceEstimate{MemoryBytes: resources.MemoryBytes - 1, DiskBytes: resources.DiskBytes}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("memory N-1 err=%v", err)
	}
	if err := provider.ValidateVideoResources(stagedag.ResourceEstimate{MemoryBytes: resources.MemoryBytes, DiskBytes: resources.DiskBytes - 1}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("disk N-1 err=%v", err)
	}
	if _, err := resourceEstimate(math.MaxUint64, 2, 1, 1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("pixel overflow err=%v", err)
	}
	if _, err := resourceEstimate(1, 1, uint64(chunkplan.FrameGroupCount)+1, 1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("frame overflow err=%v", err)
	}
	if _, err := resourceEstimate(1, 1, 1, math.MaxUint64); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("memory overflow err=%v", err)
	}
}

func TestNewRejectsUnboundOrInvalidScope(t *testing.T) {
	program, scope := testProgramAndScope(t)
	tests := []struct {
		name   string
		mutate func(*Scope)
	}{
		{name: "plan", mutate: func(value *Scope) { value.PlanFingerprint = "" }},
		{name: "manifest", mutate: func(value *Scope) { value.ExecutionManifestFingerprint = "" }},
		{name: "implementation", mutate: func(value *Scope) { value.ImplementationFingerprint = "" }},
		{name: "blank_timeline", mutate: func(value *Scope) { value.TimelineID = " "; value.Target.TimelineID = " " }},
		{name: "timeline", mutate: func(value *Scope) { value.Target.TimelineID = "other" }},
		{name: "geometry", mutate: func(value *Scope) { value.Target.Width++ }},
		{name: "clock", mutate: func(value *Scope) { value.Target.FrameClock = videoedit.FrameClock{} }},
		{name: "sample_clock", mutate: func(value *Scope) { value.Target.SampleClock = videoedit.SampleClock{} }},
		{name: "blank_pixel_format", mutate: func(value *Scope) { value.Target.PixelFormat = "\t" }},
		{name: "blank_color_space", mutate: func(value *Scope) { value.Target.ColorSpace = " " }},
		{name: "range", mutate: func(value *Scope) { value.FrameRange.Count = 0 }},
		{name: "negative_range", mutate: func(value *Scope) { value.FrameRange.First = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hostile := scope
			test.mutate(&hostile)
			if got, err := New(context.Background(), program, hostile); got != nil || err == nil {
				t.Fatalf("Provider=%v err=%v", got, err)
			}
		})
	}
	if got, err := New(nil, program, scope); got != nil || !errors.Is(err, ErrConfiguration) {
		t.Fatalf("nil context Provider=%v err=%v", got, err)
	}
	if got, err := New(context.Background(), nil, scope); got != nil || !errors.Is(err, ErrConfiguration) {
		t.Fatalf("nil Program Provider=%v err=%v", got, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := New(canceled, program, scope); got != nil || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled Provider=%v err=%v", got, err)
	}
	if got, err := New(panicContext{}, program, scope); got != nil || !errors.Is(err, ErrInvariant) {
		t.Fatalf("panic context Provider=%v err=%v", got, err)
	}
	provider, err := New(context.Background(), program, scope)
	if err != nil {
		t.Fatal(err)
	}
	if surface, renderErr := provider.RenderSurface(panicContext{}, requestFromScope(scope), scope.FrameRange.First); surface != nil || !errors.Is(renderErr, ErrInvariant) {
		t.Fatalf("panic context surface=%v err=%v", surface, renderErr)
	}
}

func testProgramAndScope(t testing.TB) (*referencevisual.Program, Scope) {
	t.Helper()
	document := testVectorDocument()
	config := referencevisual.DefaultConfigV2(4, 3)
	config.BuildFingerprint = digest('b')
	program, diagnostics, err := referencevisual.CompileV2(context.Background(), document, nil, nil, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile Program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	target := videoedit.RenderTarget{
		TimelineID: "timeline", Width: 4, Height: 3,
		FrameClock:  videoedit.FrameClock{Origin: timeValue(0, 1), FramesPerSecond: timeValue(30, 1)},
		SampleClock: videoedit.SampleClock{Origin: timeValue(0, 1), SamplesPerSecond: 48_000},
		Channels:    2, PixelFormat: "rgba16be", ColorSpace: "linear-bt709",
	}
	return program, Scope{
		PlanFingerprint: digest('a'), ExecutionManifestFingerprint: digest('c'), TimelineID: "timeline",
		Target: target, FrameRange: videoedit.TickRange{First: 10, Count: 70}, DeterministicSeed: 42,
		ImplementationFingerprint: digest('d'),
	}
}

func testVectorDocument() documentv2.Document {
	fill := documentv2.ColorRGBA16{R: 48_000, G: 12_000, B: 4_000, A: 65_535}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion,
		ID:            "chunk-surface-vector",
		Revision:      1,
		Assets: []documentv2.Asset{{
			ID: "vector", Kind: documentv2.AssetKindVector,
			Vector: &documentv2.VectorSource{
				Profile:  documentv2.VectorProfilePathViewportPXV2,
				ViewBox:  documentv2.Rect{X: rational(0, 1), Y: rational(0, 1), Width: rational(4, 1), Height: rational(3, 1)},
				Viewport: &documentv2.RasterViewport{WidthPixels: 4, HeightPixels: 3},
				Paths: []documentv2.VectorPath{{
					ID: "path", FillRule: documentv2.VectorFillNonZero, Fill: &fill,
					Commands: []documentv2.VectorCommand{
						{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{pointValue(0, 1, 0, 1)}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{pointValue(4, 1, 0, 1)}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{pointValue(4, 1, 3, 1)}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{pointValue(0, 1, 3, 1)}},
						{Op: documentv2.VectorClose},
					},
				}},
			},
		}},
		Timelines: []documentv2.Timeline{{
			ID: "timeline",
			Tracks: []documentv2.Track{{
				ID: "vector-track", Kind: documentv2.TrackKindVector,
				Clips: []documentv2.Clip{{
					ID: "vector-clip", AssetID: "vector",
					TimelineRange: documentv2.TimeRange{Start: timeValue(0, 1), Duration: timeValue(4, 1)},
					SourceRange:   documentv2.TimeRange{Start: timeValue(0, 1), Duration: timeValue(4, 1)},
				}},
			}},
		}},
	}
}

func rational(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func pointValue(xNumerator, xDenominator, yNumerator, yDenominator int64) documentv2.Point {
	return documentv2.Point{X: rational(xNumerator, xDenominator), Y: rational(yNumerator, yDenominator)}
}

type panicContext struct{}

func (panicContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (panicContext) Done() <-chan struct{}       { return nil }
func (panicContext) Err() error                  { panic("context panic") }
func (panicContext) Value(any) any               { return nil }

func requestFromScope(value Scope) backendstage.ReferenceChunkRequest {
	return backendstage.ReferenceChunkRequest{
		PlanFingerprint: value.PlanFingerprint, ExecutionManifestFingerprint: value.ExecutionManifestFingerprint,
		TimelineID: value.TimelineID, Target: value.Target, DeterministicSeed: value.DeterministicSeed,
	}
}

func surfaceBytes(t testing.TB, surface *cpucompositor.Surface) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if surface == nil {
		t.Fatal("nil Surface")
	}
	if err := surface.WriteCanonical(&buffer); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func digest(character byte) string {
	return "sha256:" + string(bytes.Repeat([]byte{character}, 64))
}

func timeValue(value, rate int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: rate}
}

func TestBindingIsAValueContract(t *testing.T) {
	if !bindingValueOnly(Binding{}) {
		t.Fatal("Binding gained borrowed reference fields")
	}
	if !reflect.DeepEqual((*Provider)(nil).Binding(), Binding{}) ||
		(*Provider)(nil).VideoResources() != (stagedag.ResourceEstimate{}) || (*Provider)(nil).BindingFingerprint() != "" {
		t.Fatal("nil Provider accessors are not zero")
	}
}

func bindingValueOnly(value Binding) bool {
	typeOf := reflect.TypeOf(value)
	for index := 0; index < typeOf.NumField(); index++ {
		switch typeOf.Field(index).Type.Kind() {
		case reflect.Map, reflect.Pointer, reflect.Slice, reflect.Interface, reflect.Func:
			return false
		}
	}
	return true
}
