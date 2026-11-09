package reconstructv2

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

// Called by the explicit CUTLY_TEST_FFMPEG opt-in real decoder test. No fake
// executable/decoder or PATH availability is accepted as real decode evidence.
func testMixedRealVideoFactory(t *testing.T, ctx context.Context, adapter *ffprobe.Adapter, decoder *videodecode.Adapter, path string, claim renderapi.MaterializedResourceV2, binding resourceidentityv2.Binding) {
	t.Helper()
	d := mixedDocument(t)
	duration := factoryTime(1, 5)
	d.Assets = append(d.Assets, documentv2.Asset{ID: binding.AssetID, Kind: documentv2.AssetKindVideo, URI: "host-video", ContentHash: binding.ContentHash, Duration: &duration})
	span := documentv2.TimeRange{Start: factoryTime(0, 1), Duration: duration}
	d.Timelines[0].Tracks = append(d.Timelines[0].Tracks, documentv2.Track{ID: "temporal", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "temporal-clip", AssetID: binding.AssetID, TimelineRange: span, SourceRange: span}}})
	// Original static clips remain selected in this short range. Their image dissolve
	// is outside it; the evaluator must not transitively select that transition.
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	hash, e := documentv2.DocumentHash(d)
	if e != nil {
		t.Fatal(e)
	}
	claim.LocatorScheme = renderapi.LocatorCAS
	claim.Locator = "cas://sha256/" + claim.ContentHash[len("sha256:"):]
	var staticSource *factoryHostSource
	config, source := mixedHostWithVerifier(t, root, d, func(ctx context.Context, h string, c renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		if c.AssetID != binding.AssetID {
			return staticSource.verifyActual(ctx, h, c)
		}
		if h != hash || !reflect.DeepEqual(c, claim) {
			return renderapi.MaterializedResourceV2{}, ErrResource
		}
		m, _, e := adapter.Probe(ctx, ffprobe.Request{AssetID: videoedit.ID(binding.AssetID), Path: path, ExpectedContentHash: binding.ContentHash})
		if e != nil {
			return renderapi.MaterializedResourceV2{}, e
		}
		fp, e := probe.Fingerprint(m)
		if e != nil || fp != *claim.ProbeFingerprint {
			return renderapi.MaterializedResourceV2{}, errors.Join(ErrResource, e)
		}
		return claim, nil
	})
	staticSource = source
	config.Static.Resources = testSource{authorize: func(ctx context.Context, h string, c renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
		if c.AssetID != binding.AssetID {
			return source.Authorize(ctx, h, c)
		}
		if h != hash || !reflect.DeepEqual(c, claim) {
			return resourceidentityv2.Binding{}, ErrResource
		}
		return binding, nil
	}, open: func(ctx context.Context, h string, c renderapi.MaterializedResourceV2, b resourceidentityv2.Binding) (io.ReadCloser, error) {
		if c.AssetID != binding.AssetID {
			return source.Open(ctx, h, c, b)
		}
		if h != hash || b != binding {
			return nil, ErrResource
		}
		return os.Open(path)
	}}
	runtime := renderapi.MediaRuntime{FFmpegBuild: factoryDigest([]byte("host media build")), DecoderProfile: "host.native.decoder.v1", EncoderProfile: "host.native.encoder.v1", ColorProfile: "host.native.color.v1"}
	config.Video = &MixedVideoConfig{Probe: adapter, Decoder: decoder, MediaRuntime: runtime, Scan: ffprobe.VideoScanOptions{MaxOutputBytes: 1 << 20, MaxFrames: 100}, Scratch: root, MaxDiskBytes: 1 << 20, MaxScanBytes: 1 << 20, MaxIndexedFrames: 100}
	// Conservative all-enabled gate needs the complete authored clip closure. Use
	// the full one-second range; temporal clip is active only during its 1/5 second.
	target := videoedit.RenderTarget{TimelineID: "timeline", Width: 16, Height: 16, FrameClock: videoedit.FrameClock{Origin: factoryTime(0, 1), FramesPerSecond: factoryTime(25, 1)}, SampleClock: videoedit.SampleClock{Origin: factoryTime(0, 1), SamplesPerSecond: 4}, Channels: 2, PixelFormat: "rgba16", ColorSpace: "linear-srgb"}
	plan, ds := renderplanv2.Resolve(ctx, d, documentv2.TimeRange{Start: factoryTime(0, 1), Duration: factoryTime(1, 1)}, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	draft := renderapi.ExecutionManifestV2{DeterministicSeed: 0, Output: renderapi.OutputSpecV2{ReferenceProfile: renderapi.OutputReferenceProfileV2, Format: "mp4", Width: 16, Height: 16, PixelFormat: "rgba16", ColorSpace: "linear-srgb", VideoCodec: "h264", AudioCodec: "aac", FrameClock: target.FrameClock, SampleClock: target.SampleClock, SampleRate: 4, Channels: 2, Intermediate: renderapi.IntermediateProfilesV2{VideoSurfaceProfile: renderapi.VideoSurfaceProfileV2, AudioBlockProfile: renderapi.AudioBlockProfileV2}, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1}}
	for _, r := range plan.Resources {
		if r.AssetID == binding.AssetID {
			draft.Resources = append(draft.Resources, claim)
		} else {
			c, e := factoryObservation(ctx, source.resources[r.AssetID])
			if e != nil {
				t.Fatal(e)
			}
			draft.Resources = append(draft.Resources, c)
		}
	}
	manifest, e := config.Static.Authority.Freeze(ctx, d, plan, draft)
	if e != nil {
		t.Fatal(e)
	}
	env, e := prepared.NewEnvironmentV2(ctx, d, plan, manifest)
	if e != nil {
		t.Fatal(e)
	}
	f, e := NewMixed(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	b, e := f.Reconstruct(ctx, env)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	for _, index := range []int64{4, 0, 2, 4} {
		r, e := b.RenderSurfaceFrame(ctx, index)
		if e != nil || r.Result.Surface == nil {
			t.Fatalf("mixed frame %d: %v", index, e)
		}
	}
	bridge, e := b.Bridge(ctx)
	if e != nil || len(bridge.Video) != 1 || bridge.RuntimeBinding == nil || bridge.RuntimeBinding.DecoderFingerprint != decoder.Fingerprint() {
		t.Fatalf("video bridge %v %v", bridge.Video, e)
	}
	if _, e = b.RenderPCM(ctx, 0, 4); e != nil {
		t.Fatal(e)
	}
	if e = b.Close(); e != nil {
		t.Fatal(e)
	}
}
