package nativevideo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

type fakeSource struct {
	mu       sync.Mutex
	identity videodecode.SourceIdentity
	surface  *cpucompositor.Surface
	calls    []videoedit.RationalTime
}

func (source *fakeSource) Identity() (videodecode.SourceIdentity, error) { return source.identity, nil }

func (source *fakeSource) Frame(_ context.Context, at videoedit.RationalTime) (*cpucompositor.Surface, videodecode.FrameReceipt, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls = append(source.calls, at)
	hash, _ := source.surface.CanonicalHash()
	return source.surface, videodecode.FrameReceipt{
		AdapterFingerprint:   source.identity.AdapterFingerprint,
		ProbeFingerprint:     source.identity.ProbeFingerprint,
		IndexFingerprint:     source.identity.IndexFingerprint,
		SemanticsFingerprint: source.identity.SemanticsFingerprint,
		ResourceHash:         source.identity.ResourceHash,
		StreamIndex:          source.identity.StreamIndex,
		SurfaceHash:          hash,
		Width:                source.surface.Width(), Height: source.surface.Height(), Profiles: source.identity.Profiles,
	}, nil
}

func TestProviderBridgesExactScopedRequest(t *testing.T) {
	document, hash, source := providerFixture(t)
	provider, err := Compile(context.Background(), document, []Binding{{DocumentHash: hash, AssetID: "asset", Source: source}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(hash)
	frame, err := provider.Frame(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Request != request || frame.Surface != source.surface || frame.PixelHash == "" || frame.AssetContentHash != source.identity.ResourceHash {
		t.Fatalf("frame = %+v", frame)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.calls) != 1 || !equivalentTime(source.calls[0], request.SourceTime) {
		t.Fatalf("calls = %+v", source.calls)
	}
}

func TestProviderRejectsScopeTimeProfileAndIdentitySubstitution(t *testing.T) {
	document, hash, source := providerFixture(t)
	provider, err := Compile(context.Background(), document, []Binding{{DocumentHash: hash, AssetID: "asset", Source: source}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*rastercompositor.NativeSourceRequest){
		"scope":   func(request *rastercompositor.NativeSourceRequest) { request.ClipID = "other" },
		"time":    func(request *rastercompositor.NativeSourceRequest) { request.SourceTime.Value++ },
		"profile": func(request *rastercompositor.NativeSourceRequest) { request.DecodeProfile = "other.decode.v1" },
	} {
		t.Run(name, func(t *testing.T) {
			request := validRequest(hash)
			mutate(&request)
			if _, err := provider.Frame(context.Background(), request); !errors.Is(err, ErrMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	source.identity.ResourceHash = "sha256:" + strings.Repeat("f", 64)
	if _, err := Compile(context.Background(), document, []Binding{{DocumentHash: hash, AssetID: "asset", Source: source}}, Limits{}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("identity substitution error = %v", err)
	}
}

func TestProviderRejectsMissingExtraAndCanceledCompile(t *testing.T) {
	document, hash, source := providerFixture(t)
	if _, err := Compile(context.Background(), document, nil, Limits{}); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := Compile(context.Background(), document, []Binding{{DocumentHash: hash, AssetID: "other", Source: source}}, Limits{}); !errors.Is(err, ErrExtra) {
		t.Fatalf("extra error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compile(ctx, document, nil, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestProviderRejectsUnpinnedVideoResource(t *testing.T) {
	document, hash, source := providerFixture(t)
	document.Assets[0].ContentHash = ""
	hash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	source.identity.ResourceHash = ""
	if _, err := Compile(context.Background(), document, []Binding{{DocumentHash: hash, AssetID: "asset", Source: source}}, Limits{}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("empty resource hash error = %v", err)
	}
}

func TestProfilesAreConcreteDecoderProfiles(t *testing.T) {
	profiles := Profiles()
	if profiles.DecodeProfile != videodecode.AdapterSemanticProfileV1 || profiles.SelectionProfile != videodecode.SelectionSemanticProfileV1 ||
		profiles.ColorProfile != videodecode.ColorSemanticProfileV1 || profiles.NormalizationProfile != videodecode.NormalizationSemanticProfileV1 {
		t.Fatalf("profiles = %+v", profiles)
	}
}

func providerFixture(t *testing.T) (documentv2.Document, string, *fakeSource) {
	t.Helper()
	resourceHash := "sha256:" + strings.Repeat("1", 64)
	duration := videoedit.RationalTime{Value: 10, Rate: 1}
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "document", Revision: 1,
		Assets: []documentv2.Asset{{ID: "asset", Kind: documentv2.AssetKindVideo, URI: "clip.mp4", ContentHash: resourceHash, Duration: &duration}},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{{
			ID: "track", Kind: documentv2.TrackKindVideo,
			Clips: []documentv2.Clip{{ID: "clip", AssetID: "asset", TimelineRange: timeRange(0, 10), SourceRange: timeRange(0, 10)}},
		}}}},
	}
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(1, 1, []cpucompositor.Pixel{{R: 1, G: 2, B: 3, A: 0xffff}})
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{
		identity: videodecode.SourceIdentity{
			AdapterFingerprint:   "sha256:" + strings.Repeat("2", 64),
			ProbeFingerprint:     "sha256:" + strings.Repeat("3", 64),
			IndexFingerprint:     "sha256:" + strings.Repeat("4", 64),
			SemanticsFingerprint: "sha256:" + strings.Repeat("5", 64),
			ResourceHash:         resourceHash, StreamIndex: 0,
			Profiles: videodecode.Profiles{Decode: videodecode.AdapterSemanticProfileV1, Selection: videodecode.SelectionSemanticProfileV1, Color: videodecode.ColorSemanticProfileV1, Normalization: videodecode.NormalizationSemanticProfileV1},
		},
		surface: surface,
	}
	return document, documentHash, source
}

func validRequest(documentHash string) rastercompositor.NativeSourceRequest {
	profiles := Profiles()
	return rastercompositor.NativeSourceRequest{
		ContractVersion: rastercompositor.NativeSourceContractVersion, DocumentHash: documentHash,
		TimelineID: "timeline", TimelineIndex: 0, TrackID: "track", TrackIndex: 0, ClipID: "clip", ClipIndex: 0,
		AssetID: "asset", AssetIndex: 0, AssetKind: documentv2.AssetKindVideo, AssetContentHash: "sha256:" + strings.Repeat("1", 64),
		TimelineTime: rationalTime(2), LocalTime: rationalTime(2), SourceTime: rationalTime(2),
		SourceProfile: rastercompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		DecodeProfile: profiles.DecodeProfile, SelectionProfile: profiles.SelectionProfile,
		ColorProfile: profiles.ColorProfile, NormalizationProfile: profiles.NormalizationProfile,
	}
}

func rationalTime(value int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: 1}
}

func timeRange(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{Start: rationalTime(start), Duration: rationalTime(duration)}
}
