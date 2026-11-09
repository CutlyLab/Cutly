package reconstructv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

func TestOpenVideoResourceRealDecodeAndCleanup(t *testing.T) {
	ffmpeg := os.Getenv("CUTLY_TEST_FFMPEG")
	if ffmpeg == "" {
		t.Skip("set CUTLY_TEST_FFMPEG to a controlled zscale-enabled FFmpeg; PATH availability is not decode-profile evidence")
	}
	var err error
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "source.mp4")
	command := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=16x16:r=25", "-frames:v", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv", "-chroma_sample_location", "left", "-y", path)
	if out, e := command.CombinedOutput(); e != nil {
		t.Fatalf("generate: %v: %s", e, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	claim, binding := diskFixture(raw)
	// These are deliberately test-only identity labels. This fixture tests real
	// process/byte causality, not production runtime-closure authorization.
	digest := func(label string) string {
		h := sha256.Sum256([]byte(label))
		return "sha256:" + hex.EncodeToString(h[:])
	}
	adapter, err := ffprobe.New(ctx, ffprobePath, ffprobe.DefaultBuildIdentity(digest("test-only ffprobe closure")), ffprobe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Homebrew executable directories may be group writable. Install a private
	// measured test executable rather than weakening the decoder's trust checks.
	privateRoot, err := os.MkdirTemp(".", ".cutly-video-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(privateRoot) })
	privateRoot, err = filepath.Abs(privateRoot)
	if err != nil {
		t.Fatal(err)
	}
	privateRoot, err = filepath.EvalSymlinks(privateRoot)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	privateFFmpeg := filepath.Join(privateRoot, "ffmpeg")
	if err := os.WriteFile(privateFFmpeg, binary, 0500); err != nil {
		t.Fatal(err)
	}
	decoder, err := videodecode.New(ctx, privateFFmpeg, videodecode.DefaultBuildIdentity(digest("test-only ffmpeg closure"), digest("test-only cpu")), videodecode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, diagnostics, err := adapter.Probe(ctx, ffprobe.Request{AssetID: videoedit.ID(claim.AssetID), Path: path, ExpectedContentHash: claim.ContentHash})
	if err != nil {
		t.Fatalf("probe: %v %v", err, diagnostics)
	}
	fingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	claim.MaterializationProfile = renderapi.AVMaterializationProfileV2
	claim.ProbeFingerprint = &fingerprint
	source := testSource{
		authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
			return binding, nil
		},
		open: func(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error) {
			return os.Open(path)
		},
	}
	scratch := t.TempDir()
	config := videoResourceConfig{probe: adapter, decoder: decoder, scan: ffprobe.VideoScanOptions{MaxOutputBytes: 1 << 20, MaxFrames: 100}, maximumBytes: 1 << 20, scratch: scratch}
	opened, err := openVideoResource(ctx, source, "document", claim, config)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.source.Close()
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("transfer snapshot retained: %v %v", entries, err)
	}
	testMixedRealVideoFactory(t, ctx, adapter, decoder, path, claim, binding)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{4, 0, 2, 4} {
		surface, receipt, err := opened.source.Frame(ctx, videoedit.RationalTime{Value: at, Rate: 25})
		if err != nil || surface == nil || receipt.ResourceHash != claim.ContentHash || receipt.FrameOrdinal != int(at) {
			t.Fatalf("frame %d: %v %v", at, receipt, err)
		}
	}
	if err := opened.source.Close(); err != nil {
		t.Fatal(err)
	}
	if surface, receipt, err := opened.source.Frame(ctx, videoedit.RationalTime{Rate: 25}); err == nil || surface != nil || !reflect.DeepEqual(receipt, videodecode.FrameReceipt{}) {
		t.Fatalf("closed source: %v %v %v", surface, receipt, err)
	}
}

func TestOpenVideoResourceInvalidPolicyNoCallbacks(t *testing.T) {
	claim, _ := diskFixture([]byte{1})
	source := testSource{authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
		t.Fatal("callback before policy admission")
		return resourceidentityv2.Binding{}, nil
	}}
	result, err := openVideoResource(context.Background(), source, "document", claim, videoResourceConfig{})
	if err == nil || !reflect.DeepEqual(result, openedVideoResource{}) {
		t.Fatalf("failure-zero: %v %v", result, err)
	}
}
