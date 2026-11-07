package expressionlayer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

func TestTransitionHandleAcceptsActualOutOfVisibleEvaluatorTimeWithoutWideningV1(t *testing.T) {
	provider := transitionFixtureProvider(t)
	at := documentv2.RationalTime{Value: 5, Rate: 2}
	request := transitionFixtureRequest(t, provider, TransitionHandleOutgoing, at)
	if frame, err := provider.TransitionHandle(context.Background(), request); err != nil || frame.Surface == nil ||
		frame.ContractVersion != TransitionHandleContractVersion || frame.Request != request ||
		frame.AssetSemanticHash != request.AssetSemanticHash || frame.SurfaceIdentity.PixelHash != frame.RasterReceipt.PixelHash {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}

	// The original visible-only v1 contract remains fail-closed for the exact same handle time.
	visibleRequest := Request{
		ContractVersion: ContractVersion, DocumentHash: request.DocumentHash,
		TimelineID: request.TimelineID, TimelineIndex: request.TimelineIndex,
		TrackID: request.TrackID, TrackIndex: request.TrackIndex, TrackKind: request.TrackKind,
		ClipID: request.ClipID, ClipIndex: request.ClipIndex,
		AssetID: request.AssetID, AssetIndex: request.AssetIndex, AssetKind: request.AssetKind,
		AssetSemanticHashProfile: request.AssetSemanticHashProfile,
		AssetSemanticHash:        request.AssetSemanticHash, RasterViewport: request.RasterViewport,
		TimelineTime: request.TimelineTime, LocalTime: request.LocalTime, SourceTime: request.SourceTime,
		SourceProfile: request.SourceProfile, SelectionProfile: SelectionProfileV1,
		ExpressionProfile: request.ExpressionProfile, EvaluatorProfile: request.EvaluatorProfile,
		RasterProfile: request.RasterProfile, PaintProfile: request.PaintProfile,
		KernelProfile: request.KernelProfile, SurfaceProfile: request.SurfaceProfile,
	}
	if frame, err := provider.Frame(context.Background(), visibleRequest); frame != (Frame{}) || !errors.Is(err, ErrRequestInvalid) {
		t.Fatalf("visible v1 accepted transition handle: frame=%+v err=%v", frame, err)
	}
}

func TestTransitionHandleRejectsMutatedScopeRoleTimeAndProfiles(t *testing.T) {
	provider := transitionFixtureProvider(t)
	valid := transitionFixtureRequest(t, provider, TransitionHandleIncoming, documentv2.RationalTime{Value: 3, Rate: 2})
	tests := []struct {
		name   string
		mutate func(*TransitionHandleRequest)
	}{
		{"contract", func(value *TransitionHandleRequest) { value.ContractVersion++ }},
		{"document", func(value *TransitionHandleRequest) { value.DocumentHash = shaA() }},
		{"timeline", func(value *TransitionHandleRequest) { value.TimelineID = "other" }},
		{"timeline index", func(value *TransitionHandleRequest) { value.TimelineIndex++ }},
		{"track", func(value *TransitionHandleRequest) { value.TrackID = "other" }},
		{"track index", func(value *TransitionHandleRequest) { value.TrackIndex++ }},
		{"track kind", func(value *TransitionHandleRequest) { value.TrackKind = documentv2.TrackKindVideo }},
		{"transition", func(value *TransitionHandleRequest) { value.TransitionID = "other" }},
		{"transition index", func(value *TransitionHandleRequest) { value.TransitionIndex++ }},
		{"transition profile", func(value *TransitionHandleRequest) { value.TransitionProfile = "cutly.transition.other.v1" }},
		{"role", func(value *TransitionHandleRequest) { value.Role = TransitionHandleOutgoing }},
		{"invalid role", func(value *TransitionHandleRequest) { value.Role = TransitionHandleRole("sideways") }},
		{"clip", func(value *TransitionHandleRequest) { value.ClipID = "outgoing" }},
		{"clip index", func(value *TransitionHandleRequest) { value.ClipIndex++ }},
		{"asset", func(value *TransitionHandleRequest) { value.AssetID = "vector-red" }},
		{"asset index", func(value *TransitionHandleRequest) { value.AssetIndex++ }},
		{"asset kind", func(value *TransitionHandleRequest) { value.AssetKind = documentv2.AssetKindImage }},
		{"semantic hash profile", func(value *TransitionHandleRequest) { value.AssetSemanticHashProfile = "cutly.hash.other.v1" }},
		{"semantic hash", func(value *TransitionHandleRequest) { value.AssetSemanticHash = shaA() }},
		{"viewport", func(value *TransitionHandleRequest) { value.RasterViewport.WidthPixels++ }},
		{"local time", func(value *TransitionHandleRequest) { value.LocalTime.Value++ }},
		{"source time", func(value *TransitionHandleRequest) { value.SourceTime.Value++ }},
		{"selection profile", func(value *TransitionHandleRequest) { value.SelectionProfile = SelectionProfileV1 }},
		{"source profile", func(value *TransitionHandleRequest) { value.SourceProfile = "cutly.source.other.v1" }},
		{"expression profile", func(value *TransitionHandleRequest) { value.ExpressionProfile = documentv2.VectorProfilePathV1 }},
		{"evaluator profile", func(value *TransitionHandleRequest) { value.EvaluatorProfile = "cutly.evaluator.other.v1" }},
		{"raster profile", func(value *TransitionHandleRequest) { value.RasterProfile = "cutly.raster.other.v1" }},
		{"paint profile", func(value *TransitionHandleRequest) { value.PaintProfile = "cutly.color.other.v1" }},
		{"kernel profile", func(value *TransitionHandleRequest) { value.KernelProfile = "cutly.compositor.other.v1" }},
		{"surface profile", func(value *TransitionHandleRequest) { value.SurfaceProfile = "cutly.surface.other.v1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			frame, err := provider.TransitionHandle(context.Background(), request)
			if err == nil || frame != (TransitionHandleFrame{}) {
				t.Fatalf("mutation accepted: frame=%+v err=%v", frame, err)
			}
		})
	}
	for _, at := range []documentv2.RationalTime{{Value: 0, Rate: 1}, {Value: 3, Rate: 1}} {
		request := transitionFixtureRequest(t, provider, TransitionHandleIncoming, at)
		if frame, err := provider.TransitionHandle(context.Background(), request); frame != (TransitionHandleFrame{}) || !errors.Is(err, ErrRequestInvalid) {
			t.Fatalf("inactive time accepted: at=%v frame=%+v err=%v", at, frame, err)
		}
	}
}

func TestTransitionHandleExactHalfOpenWindowAsymmetricAndZeroWidthScopes(t *testing.T) {
	document := transitionFixtureDocument(t)
	document.Timelines[0].Transitions[0].PreDuration = documentv2.RationalTime{Value: 1, Rate: 2}
	document.Timelines[0].Transitions[0].PostDuration = documentv2.RationalTime{Value: 3, Rate: 2}
	provider, err := Compile(context.Background(), document, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		at   documentv2.RationalTime
		ok   bool
	}{
		{"start epsilon", documentv2.RationalTime{Value: 1499, Rate: 1000}, false},
		{"start", documentv2.RationalTime{Value: 3, Rate: 2}, true},
		{"cut", documentv2.RationalTime{Value: 2, Rate: 1}, true},
		{"end epsilon", documentv2.RationalTime{Value: 3499, Rate: 1000}, true},
		{"end", documentv2.RationalTime{Value: 7, Rate: 2}, false},
	} {
		for _, role := range []TransitionHandleRole{TransitionHandleOutgoing, TransitionHandleIncoming} {
			t.Run(test.name+"/"+string(role), func(t *testing.T) {
				request := transitionFixtureRequest(t, provider, role, test.at)
				frame, callErr := provider.TransitionHandle(context.Background(), request)
				if test.ok && (callErr != nil || frame.Surface == nil) {
					t.Fatalf("valid handle rejected: frame=%+v err=%v", frame, callErr)
				}
				if !test.ok && (callErr == nil || frame != (TransitionHandleFrame{})) {
					t.Fatalf("inactive handle accepted: frame=%+v err=%v", frame, callErr)
				}
			})
		}
	}

	zeroDocument := transitionFixtureDocument(t)
	zeroDocument.Timelines[0].Transitions[0].PreDuration = documentv2.RationalTime{Value: 0, Rate: 1}
	zeroDocument.Timelines[0].Transitions[0].PostDuration = documentv2.RationalTime{Value: 0, Rate: 1}
	zero, err := Compile(context.Background(), zeroDocument, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.transitions) != 0 {
		t.Fatalf("zero-width transition retained %d handle scopes", len(zero.transitions))
	}
	request := transitionFixtureRequest(t, provider, TransitionHandleOutgoing, documentv2.RationalTime{Value: 2, Rate: 1})
	request.DocumentHash = zero.documentHash
	zeroEntry := zero.assets[request.AssetID]
	request.AssetSemanticHash = zeroEntry.receipt.AssetSemanticHash
	request.RasterViewport = zeroEntry.receipt.RasterViewport
	if frame, callErr := zero.TransitionHandle(context.Background(), request); callErr == nil || frame != (TransitionHandleFrame{}) {
		t.Fatalf("zero-width handle accepted: frame=%+v err=%v", frame, callErr)
	}
}

func TestTransitionHandleCompileFreezesClipTransitionAndRejectsCrossScopeReplay(t *testing.T) {
	document := transitionFixtureDocument(t)
	provider, err := Compile(context.Background(), document, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := transitionFixtureRequest(t, provider, TransitionHandleOutgoing, documentv2.RationalTime{Value: 5, Rate: 2})
	document.Timelines[0].Transitions[0].ID = "mutated-transition"
	document.Timelines[0].Transitions[0].PostDuration.Value = 0
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Duration.Value = 1
	document.Assets[0].Vector.Paths[0].Commands[0].Points[0].X.Numerator = 999
	if frame, callErr := provider.TransitionHandle(context.Background(), request); callErr != nil || frame.Surface == nil {
		t.Fatalf("caller mutation changed provider: frame=%+v err=%v", frame, callErr)
	}

	replay := request
	replay.TimelineID = "other-timeline"
	replay.TimelineIndex++
	replay.TransitionID = "other-transition"
	replay.TransitionIndex++
	if frame, callErr := provider.TransitionHandle(context.Background(), replay); callErr == nil || frame != (TransitionHandleFrame{}) {
		t.Fatalf("cross timeline/transition replay accepted: frame=%+v err=%v", frame, callErr)
	}
	if len(provider.transitions) > provider.config.Limits.MaxClipScopes*2 {
		t.Fatalf("transition scopes=%d limit=%d", len(provider.transitions), provider.config.Limits.MaxClipScopes*2)
	}
}

func TestTransitionHandleCancellationAndConcurrentReads(t *testing.T) {
	provider := transitionFixtureProvider(t)
	request := transitionFixtureRequest(t, provider, TransitionHandleOutgoing, documentv2.RationalTime{Value: 5, Rate: 2})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if frame, err := provider.TransitionHandle(ctx, request); frame != (TransitionHandleFrame{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled frame=%+v err=%v", frame, err)
	}
	if frame, err := provider.TransitionHandle(nil, request); frame != (TransitionHandleFrame{}) || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context frame=%+v err=%v", frame, err)
	}
	want, err := provider.TransitionHandle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 32)
	for worker := 0; worker < 32; worker++ {
		go func() {
			got, callErr := provider.TransitionHandle(context.Background(), request)
			if callErr == nil && got != want {
				callErr = errors.New("non-deterministic transition frame")
			}
			done <- callErr
		}()
	}
	for worker := 0; worker < 32; worker++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestTransitionHandleDescriptorCanonicalFixture(t *testing.T) {
	descriptor := TransitionHandleDescriptor()
	if descriptor.Profile != TransitionHandleProfileV2 || descriptor.ContractVersion != TransitionHandleContractVersion ||
		descriptor.BaseVisibleProvider != ProfileV1 || descriptor.ScopeAmendmentProfile != documentv2.VectorTransitionScopeProfileV1 {
		t.Fatalf("descriptor=%+v", descriptor)
	}
	encoded, err := videoedit.CanonicalJSON(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/transition-handle-profile-v2.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSpace(want)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("fixture drift\nwant %s\n got %s", want, encoded)
	}
	var decoded TransitionHandleProfileDescriptor
	if err := wirejson.Decode(want, &decoded); err != nil || decoded != descriptor {
		t.Fatalf("strict decode: decoded=%+v err=%v", decoded, err)
	}
}

func transitionFixtureProvider(t *testing.T) *Provider {
	t.Helper()
	document := transitionFixtureDocument(t)
	provider, err := Compile(context.Background(), document, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func transitionFixtureDocument(t *testing.T) documentv2.Document {
	t.Helper()
	raw, err := os.ReadFile("../../core/documentv2/testdata/vector-transition-document-v2.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func transitionFixtureRequest(
	t *testing.T,
	provider *Provider,
	role TransitionHandleRole,
	at documentv2.RationalTime,
) TransitionHandleRequest {
	t.Helper()
	allowed := provider.transitions[transitionScopeKey{timeline: "timeline", transition: "vector-dissolve", role: role}]
	clip := provider.clips[allowed.clipKey]
	entry := provider.assets[clip.asset.id]
	local, err := evaluator.ClipLocalTime(clip.clip, at)
	if err != nil {
		t.Fatal(err)
	}
	source, err := evaluator.SourceTime(clip.clip, at)
	if err != nil {
		t.Fatal(err)
	}
	return TransitionHandleRequest{
		ContractVersion: TransitionHandleContractVersion, DocumentHash: provider.documentHash,
		TimelineID: "timeline", TimelineIndex: allowed.timelineIndex,
		TrackID: allowed.trackID, TrackIndex: allowed.trackIndex, TrackKind: clip.trackKind,
		TransitionID: allowed.transition.ID, TransitionIndex: allowed.transitionIndex,
		TransitionProfile: allowed.transition.Profile, Role: role,
		ClipID: clip.clip.ID, ClipIndex: clip.clipIndex,
		AssetID: clip.asset.id, AssetIndex: clip.asset.index, AssetKind: clip.asset.kind,
		AssetSemanticHashProfile: vectorraster.SourceHashProfileV1,
		AssetSemanticHash:        entry.receipt.AssetSemanticHash, RasterViewport: entry.receipt.RasterViewport,
		TimelineTime: at, LocalTime: local, SourceTime: source,
		SourceProfile: SourceProfileV1, SelectionProfile: TransitionHandleSelectionProfileV2,
		ExpressionProfile: documentv2.VectorProfilePathViewportPXV2,
		EvaluatorProfile:  documentv2.EvaluatorVectorPathViewportPXV2,
		RasterProfile:     vectorraster.ProfileV1, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
}
