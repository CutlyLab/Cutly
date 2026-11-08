package reconstructv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/preparev2"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

func factoryTime(v, r int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: v, Rate: r}
}
func factoryRat(v, r int64) documentv2.Rational {
	return documentv2.Rational{Numerator: v, Denominator: r}
}
func factoryDigest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

type factoryResource struct {
	binding resourceidentityv2.Binding
	locator string
	encoded []byte
}
type factoryHostSource struct {
	documentHash            string
	resources               map[documentv2.ID]factoryResource
	authorize, open, verify atomic.Int64
	// Faults are set only between calls, never concurrently with reconstruction.
	drift, fail documentv2.ID
	onOpen      func(documentv2.ID)
}

func (s *factoryHostSource) expected(hash string, claim renderapi.MaterializedResourceV2) (factoryResource, error) {
	r, ok := s.resources[claim.AssetID]
	if !ok || hash != s.documentHash || claim.LocatorScheme != renderapi.LocatorCAS || claim.Locator != r.locator || claim.Kind != r.binding.Kind || claim.ContentHash != r.binding.ContentHash || claim.SizeBytes != r.binding.SizeBytes {
		return factoryResource{}, errors.New("host independently rejected resource")
	}
	return r, nil
}
func (s *factoryHostSource) Authorize(ctx context.Context, hash string, claim renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
	s.authorize.Add(1)
	if e := ctx.Err(); e != nil {
		return resourceidentityv2.Binding{}, e
	}
	r, e := s.expected(hash, claim)
	return r.binding, e
}
func (s *factoryHostSource) Open(ctx context.Context, hash string, claim renderapi.MaterializedResourceV2, b resourceidentityv2.Binding) (io.ReadCloser, error) {
	s.open.Add(1)
	r, e := s.expected(hash, claim)
	if e != nil || b != r.binding {
		return nil, errors.New("host rejected open")
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	if s.onOpen != nil {
		s.onOpen(claim.AssetID)
	}
	if claim.AssetID == s.fail {
		return nil, errors.New("host injected late source failure")
	}
	owned := append([]byte(nil), r.encoded...)
	if claim.AssetID == s.drift {
		owned[len(owned)-1] ^= 1
	}
	return io.NopCloser(bytes.NewReader(owned)), nil
}
func factoryObservation(ctx context.Context, r factoryResource) (renderapi.MaterializedResourceV2, error) {
	b := r.binding
	claim := renderapi.MaterializedResourceV2{AssetID: b.AssetID, Kind: b.Kind, ContentHash: b.ContentHash, SizeBytes: b.SizeBytes, LocatorScheme: renderapi.LocatorCAS, Locator: r.locator}
	var observed *resourceidentityv2.Inspected
	var e error
	switch b.Kind {
	case documentv2.AssetKindImage:
		observed, e = resourceidentityv2.InspectPNG(ctx, b, r.encoded, pngdecode.DefaultLimits())
		claim.MaterializationProfile = renderapi.ImageMaterializationProfileV2
	case documentv2.AssetKindFont:
		observed, e = resourceidentityv2.InspectFont(ctx, b, r.encoded, sfntglyf.DefaultLimits())
		claim.MaterializationProfile = renderapi.FontMaterializationProfileV2
	case documentv2.AssetKindAudio:
		observed, e = resourceidentityv2.InspectAudio(ctx, b, r.encoded, wavdecode.DefaultLimits())
		claim.MaterializationProfile = renderapi.AVMaterializationProfileV2
	default:
		return claim, errors.New("unsupported host kind")
	}
	if e != nil {
		return renderapi.MaterializedResourceV2{}, e
	}
	if b.Kind == documentv2.AssetKindAudio {
		p, err := observed.Probe()
		if err != nil {
			return renderapi.MaterializedResourceV2{}, err
		}
		fp, err := probe.Fingerprint(p)
		if err != nil {
			return renderapi.MaterializedResourceV2{}, err
		}
		claim.ProbeFingerprint = &fp
	} else {
		receipt, err := observed.Receipt()
		if err != nil {
			return renderapi.MaterializedResourceV2{}, err
		}
		fp := receipt.Fingerprint
		claim.InspectionFingerprint = &fp
	}
	if e = resourceidentityv2.Check(ctx, b, claim, observed); e != nil {
		return renderapi.MaterializedResourceV2{}, e
	}
	return claim, nil
}
func (s *factoryHostSource) verifyActual(ctx context.Context, hash string, claim renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
	s.verify.Add(1)
	r, e := s.expected(hash, claim)
	if e != nil {
		return renderapi.MaterializedResourceV2{}, e
	}
	return factoryObservation(ctx, r)
}
func (s *factoryHostSource) reset() { s.authorize.Store(0); s.open.Store(0); s.verify.Store(0) }
func (s *factoryHostSource) requireZero(t *testing.T) {
	t.Helper()
	if s.authorize.Load() != 0 || s.open.Load() != 0 || s.verify.Load() != 0 {
		t.Fatalf("callbacks before admission: verify=%d authorize=%d open=%d", s.verify.Load(), s.authorize.Load(), s.open.Load())
	}
}

func factoryFixtureBytes(t *testing.T) map[documentv2.ID][]byte {
	t.Helper()
	raw, e := os.ReadFile("../../media/pngdecode/testdata/decode-v1.vectors.canonical.json")
	if e != nil {
		t.Fatal(e)
	}
	var pv struct {
		Vectors []struct {
			Encoded string `json:"encoded_png_base64"`
			Hash    string `json:"encoded_content_hash"`
		} `json:"vectors"`
	}
	if e = json.Unmarshal(raw, &pv); e != nil {
		t.Fatal(e)
	}
	png, e := base64.StdEncoding.DecodeString(pv.Vectors[0].Encoded)
	if e != nil || factoryDigest(png) != pv.Vectors[0].Hash {
		t.Fatal("PNG fixture", e)
	}
	raw, e = os.ReadFile("../../font/sfntglyf/testdata/minimal-font-v1.hex")
	if e != nil {
		t.Fatal(e)
	}
	font, e := hex.DecodeString(strings.TrimSpace(string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile("../wavsource/testdata/source-v1.vectors.canonical.json")
	if e != nil {
		t.Fatal(e)
	}
	var wv struct {
		Cases []struct {
			PCM      []int16 `json:"pcm"`
			Rate     uint32  `json:"sample_rate"`
			Channels uint16  `json:"channels"`
			Hash     string  `json:"expected_encoded_hash"`
		} `json:"cases"`
	}
	if e = json.Unmarshal(raw, &wv); e != nil {
		t.Fatal(e)
	}
	w := wv.Cases[0]
	wav := make([]byte, 44+len(w.PCM)*2)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], w.Channels)
	binary.LittleEndian.PutUint32(wav[24:], w.Rate)
	binary.LittleEndian.PutUint32(wav[28:], w.Rate*uint32(w.Channels)*2)
	binary.LittleEndian.PutUint16(wav[32:], w.Channels*2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(w.PCM)*2))
	for i, v := range w.PCM {
		binary.LittleEndian.PutUint16(wav[44+i*2:], uint16(v))
	}
	if factoryDigest(wav) != w.Hash {
		t.Fatal("WAV fixture bytes changed")
	}
	return map[documentv2.ID][]byte{"image": png, "font": font, "audio": wav}
}
func factoryMixedDocument(encoded map[documentv2.ID][]byte) documentv2.Document {
	zero := factoryRat(0, 1)
	pt := func(x, y int64) documentv2.Point { return documentv2.Point{X: factoryRat(x, 1), Y: factoryRat(y, 1)} }
	line := factoryRat(16, 1)
	duration := factoryTime(1, 1)
	span := documentv2.TimeRange{Start: factoryTime(0, 1), Duration: duration}
	fill := documentv2.ColorRGBA16{G: 65535, A: 32768}
	clip := func(id, asset documentv2.ID) documentv2.Clip {
		return documentv2.Clip{ID: id, AssetID: asset, TimelineRange: span, SourceRange: span}
	}
	image := clip("image-clip", "image")
	disabled := image
	disabled.ID = "disabled-image"
	disabled.Disabled = true
	disabled.TimelineRange.Start = factoryTime(-1, 1)
	return documentv2.Document{SchemaVersion: 2, ID: "real-mixed-factory", Revision: 7, Assets: []documentv2.Asset{
		{ID: "font", Kind: documentv2.AssetKindFont, URI: "host-only-font", ContentHash: factoryDigest(encoded["font"])},
		{ID: "vector", Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{Profile: documentv2.VectorProfilePathViewportPXV2, ViewBox: documentv2.Rect{X: zero, Y: zero, Width: factoryRat(2, 1), Height: factoryRat(2, 1)}, Viewport: &documentv2.RasterViewport{WidthPixels: 2, HeightPixels: 2}, Paths: []documentv2.VectorPath{{ID: "square", FillRule: documentv2.VectorFillNonZero, Fill: &fill, Commands: []documentv2.VectorCommand{{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{pt(0, 0)}}, {Op: documentv2.VectorLineTo, Points: []documentv2.Point{pt(2, 0)}}, {Op: documentv2.VectorLineTo, Points: []documentv2.Point{pt(2, 2)}}, {Op: documentv2.VectorLineTo, Points: []documentv2.Point{pt(0, 2)}}, {Op: documentv2.VectorClose}}}}}},
		{ID: "audio", Kind: documentv2.AssetKindAudio, URI: "host-only-wav", ContentHash: factoryDigest(encoded["audio"]), Duration: &duration},
		{ID: "image", Kind: documentv2.AssetKindImage, URI: "host-only-png", ContentHash: factoryDigest(encoded["image"])},
		{ID: "text", Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{Profile: documentv2.TextProfilePinnedFontViewportPXV2, Text: "A", FontAssetID: "font", FontSize: factoryRat(16, 1), LineHeight: &line, Viewport: &documentv2.RasterViewport{WidthPixels: 16, HeightPixels: 16}, Fill: documentv2.ColorRGBA16{R: 65535, G: 40000, A: 65535}, Language: "en", Script: documentv2.TextScriptLatin, Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart}},
	}, Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
		{ID: "notes", Kind: documentv2.TrackKindAnnotation}, {ID: "picture", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{disabled, image}},
		{ID: "sound", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{clip("audio-clip", "audio")}},
		{ID: "shape", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{clip("vector-clip", "vector")}},
		{ID: "more-notes", Kind: documentv2.TrackKindAnnotation}, {ID: "caption", Kind: documentv2.TrackKindText, Clips: []documentv2.Clip{clip("text-clip", "text")}},
	}}}, Metadata: map[string]json.RawMessage{"preserved": json.RawMessage(`{"original":true}`)}}
}

func factoryHost(t *testing.T, root string, document documentv2.Document) (Config, *factoryHostSource) {
	t.Helper()
	hash, e := documentv2.DocumentHash(document)
	if e != nil {
		t.Fatal(e)
	}
	source := &factoryHostSource{documentHash: hash, resources: make(map[documentv2.ID]factoryResource)}
	encoded := factoryFixtureBytes(t)
	for _, a := range document.Assets {
		b, ok := encoded[a.ID]
		if !ok {
			continue
		}
		binding := resourceidentityv2.Binding{AssetID: a.ID, Kind: a.Kind, ContentHash: factoryDigest(b), SizeBytes: int64(len(b))}
		source.resources[a.ID] = factoryResource{binding: binding, locator: "cas://sha256/" + strings.TrimPrefix(binding.ContentHash, "sha256:"), encoded: append([]byte(nil), b...)}
	}
	executable := filepath.Join(root, "measured-provider")
	if _, e = os.Stat(executable); os.IsNotExist(e) {
		if e = os.WriteFile(executable, []byte("independently measured reconstruction test provider\n"), 0700); e != nil {
			t.Fatal(e)
		}
	} else if e != nil {
		t.Fatal(e)
	}
	authority, e := preparev2.New(context.Background(), preparev2.Config{ProviderExecutable: executable, AllowLocalDevelopment: true, CompilerBuild: factoryDigest([]byte("factory compiler")), EvaluatorBuild: factoryDigest([]byte("factory evaluator")), Backend: renderapi.BackendCapabilitiesV2{BackendID: "native-test-host", BuildFingerprint: factoryDigest([]byte("factory backend")), TrackKinds: []documentv2.TrackKind{documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector, documentv2.TrackKindVideo}, AssetKinds: []documentv2.AssetKind{documentv2.AssetKindAudio, documentv2.AssetKindFont, documentv2.AssetKindImage, documentv2.AssetKindText, documentv2.AssetKindVector}, Features: []videoedit.CapabilityID{documentv2.CapabilityTextPinnedFontViewportPXV2, documentv2.CapabilityVectorPathViewportPXV2}, MaxWidth: 16, MaxHeight: 16}, MediaRuntime: renderapi.MediaRuntime{FFmpegBuild: factoryDigest([]byte("host media build")), DecoderProfile: "host.native.decoder.v1", EncoderProfile: "host.native.encoder.v1", ColorProfile: "host.native.color.v1"}, VerifyResource: source.verifyActual})
	if e != nil {
		t.Fatal(e)
	}
	return DefaultConfig(16, 16, authority, source), source
}
func factoryEnvironment(t *testing.T, config Config, source *factoryHostSource, d documentv2.Document) prepared.EnvironmentV2 {
	t.Helper()
	ctx := context.Background()
	span := documentv2.TimeRange{Start: factoryTime(0, 1), Duration: factoryTime(1, 1)}
	target := videoedit.RenderTarget{TimelineID: "timeline", Width: 16, Height: 16, FrameClock: videoedit.FrameClock{Origin: factoryTime(-1, 2), FramesPerSecond: factoryTime(2, 1)}, SampleClock: videoedit.SampleClock{Origin: factoryTime(-1, 2), SamplesPerSecond: 4}, Channels: 2, PixelFormat: "rgba16", ColorSpace: "linear-srgb"}
	plan, ds := renderplanv2.Resolve(ctx, d, span, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("plan %+v", ds)
	}
	draft := renderapi.ExecutionManifestV2{CompilerBuild: factoryDigest([]byte("untrusted draft")), ProviderBuild: factoryDigest([]byte("untrusted draft provider")), DeterministicSeed: 0, Output: renderapi.OutputSpecV2{ReferenceProfile: renderapi.OutputReferenceProfileV2, Format: "mp4", Width: 16, Height: 16, PixelFormat: "rgba16", ColorSpace: "linear-srgb", VideoCodec: "h264", AudioCodec: "aac", FrameClock: target.FrameClock, SampleClock: target.SampleClock, SampleRate: 4, Channels: 2, Intermediate: renderapi.IntermediateProfilesV2{VideoSurfaceProfile: renderapi.VideoSurfaceProfileV2, AudioBlockProfile: renderapi.AudioBlockProfileV2}, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1}}
	for _, ref := range plan.Resources {
		claim, e := factoryObservation(ctx, source.resources[ref.AssetID])
		if e != nil {
			t.Fatal(e)
		}
		draft.Resources = append(draft.Resources, claim)
	}
	manifest, e := config.Authority.Freeze(ctx, d, plan, draft)
	if e != nil {
		t.Fatal(e)
	}
	if manifest.CompilerBuild == draft.CompilerBuild || manifest.ProviderBuild == draft.ProviderBuild {
		t.Fatal("draft pins trusted")
	}
	env, e := prepared.NewEnvironmentV2(ctx, d, plan, manifest)
	if e != nil {
		t.Fatal(e)
	}
	source.reset()
	return env
}

type factoryRenderIdentity struct {
	Key           prepared.KeyV2  `json:"key"`
	Bridge        json.RawMessage `json:"bridge"`
	SurfaceHashes []string        `json:"surface_hashes"`
	PNGHashes     []string        `json:"png_hashes"`
	PCMHash       string          `json:"pcm_hash"`
	PCMHex        string          `json:"pcm_hex"`
}

func factoryRender(t *testing.T, b *Bundle, env prepared.EnvironmentV2) factoryRenderIdentity {
	t.Helper()
	ctx := context.Background()
	bridge, e := b.Bridge(ctx)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := videoedit.CanonicalJSON(bridge)
	if e != nil {
		t.Fatal(e)
	}
	out := factoryRenderIdentity{Key: b.Key(), Bridge: raw}
	for i := env.Plan.FrameRange.First; i < env.Plan.FrameRange.First+env.Plan.FrameRange.Count; i++ {
		surface, e := b.RenderSurfaceFrame(ctx, i)
		if e != nil {
			t.Fatal(e)
		}
		frame, e := b.RenderFrame(ctx, i)
		if e != nil {
			t.Fatal(e)
		}
		out.SurfaceHashes = append(out.SurfaceHashes, surface.Result.PixelHash)
		out.PNGHashes = append(out.PNGHashes, frame.Result.PNG.ArtifactHash)
		if frame.Result.Visual.PixelHash != surface.Result.PixelHash {
			t.Fatal("surface/full frame differ")
		}
	}
	pcm, e := b.RenderPCM(ctx, env.Plan.SampleRange.First, env.Plan.SampleRange.Count)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := pcm.PCM.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	out.PCMHash = pcm.PCMHash
	out.PCMHex = hex.EncodeToString(encoded)
	if out.PCMHex != "0080008000000000ff7fff7f00400040" {
		t.Fatalf("not actual strict WAV PCM: %s", out.PCMHex)
	}
	return out
}

func TestFactoryMixedNativeAndFreshProcessRestart(t *testing.T) {
	ctx := context.Background()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	document := factoryMixedDocument(factoryFixtureBytes(t))
	config, source := factoryHost(t, root, document)
	env := factoryEnvironment(t, config, source, document)
	if env.Plan.FrameRange.First != 1 || env.Plan.SampleRange.First != 2 || env.Plan.FrameRange.Count != 2 || env.Plan.SampleRange.Count != 4 {
		t.Fatal("negative-origin positive tick fixture drift")
	}
	if len(env.Plan.Visual) != 3 || env.Plan.Visual[0].Binding.TrackIndex != 1 || env.Plan.Visual[0].Binding.ClipIndex != 1 || env.Plan.Audio[0].Binding.TrackIndex != 2 {
		t.Fatal("original mixed indexes lost")
	}
	factory, e := New(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	source.requireZero(t)
	bundle, e := factory.Reconstruct(ctx, env)
	if e != nil || !bundle.Valid() {
		t.Fatalf("reconstruct: %v", e)
	}
	expected := factoryRender(t, bundle, env)
	if source.verify.Load() != 3 || source.authorize.Load() != 3 || source.open.Load() != 3 {
		t.Fatal("actual host resources not independently observed")
	}
	bridge, e := bundle.Bridge(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if bridge.DocumentHash != env.Plan.DocumentHash || bridge.NativeVisual.SourceManifest.DocumentHash != bridge.DocumentHash || bridge.NativeAudio.WAVManifest.DocumentHash != bridge.DocumentHash || len(bridge.ValidatedResources) != 3 || len(bridge.NativeAudio.Receipt.Sources) != 1 || bridge.NativeAudio.Receipt.Sources[0].AssetIndex != 2 || bridge.NativeAudio.Receipt.Sources[0].AssetID != "audio" {
		t.Fatal("bridge detached from original")
	}
	visualManifest := bridge.NativeVisual.SourceManifest
	if visualManifest.NativePNG.DocumentHash != bridge.DocumentHash || visualManifest.Expression.DocumentHash != bridge.DocumentHash || visualManifest.Text == nil || visualManifest.Text.DocumentHash != bridge.DocumentHash ||
		len(visualManifest.NativePNG.Assets) != 1 || visualManifest.NativePNG.Assets[0].AssetID != "image" || visualManifest.NativePNG.Assets[0].AssetIndex != 3 ||
		len(visualManifest.Expression.Assets) != 1 || visualManifest.Expression.Assets[0].AssetID != "vector" || visualManifest.Expression.Assets[0].AssetIndex != 1 ||
		len(visualManifest.Text.Assets) != 1 || visualManifest.Text.Assets[0].AssetID != "text" || visualManifest.Text.Assets[0].AssetIndex != 4 || visualManifest.Text.Assets[0].FontAssetID != "font" || visualManifest.Text.Assets[0].FontAssetIndex != 0 {
		t.Fatal("real visual/font manifests lost original asset identities")
	}
	bridge.ValidatedResources[0].ContentHash = "mutated"
	bridge.NativeVisual.Config.TextConfig.Limits.MaxBindings = 1
	bridge.NativeAudio.Receipt.Sources[0].DecoderChannels[0] = "BAD"
	again, e := bundle.Bridge(ctx)
	if e != nil {
		t.Fatal(e)
	}
	againRaw, _ := videoedit.CanonicalJSON(again)
	if !bytes.Equal(againRaw, expected.Bridge) {
		t.Fatal("Bundle Bridge aliases retained state")
	}
	config.Visual.TextConfig.Limits.MaxBindings = 1
	env.Document.Revision++
	if got := factoryRender(t, bundle, env); !reflect.DeepEqual(got, expected) {
		t.Fatal("caller mutation changed bundle")
	}
	env.Document.Revision--
	storeRoot := filepath.Join(root, "store")
	store, e := prepared.New(storeRoot, prepared.Options{})
	if e != nil {
		t.Fatal(e)
	}
	stored, created, e := store.PutV2(ctx, env.Document, env.Plan, env.Manifest)
	if e != nil || !created {
		t.Fatalf("persist %v created=%v", e, created)
	}
	_ = stored
	reopened, e := prepared.New(storeRoot, prepared.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolved, e := reopened.ResolveV2(ctx, expected.Key.PlanFingerprint, expected.Key.ExecutionManifestFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	freshConfig, _ := factoryHost(t, root, resolved.Document)
	fresh, e := New(ctx, freshConfig)
	if e != nil {
		t.Fatal(e)
	}
	rebuilt, e := fresh.Reconstruct(ctx, resolved)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(factoryRender(t, rebuilt, resolved), expected) {
		t.Fatal("same-process reopen differs")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFactoryRestartSubprocess$", "-test.count=1")
	command.Env = append(os.Environ(), "CUTLY_FACTORY_RESTART=1", "CUTLY_FACTORY_ROOT="+root, "CUTLY_FACTORY_PLAN="+expected.Key.PlanFingerprint, "CUTLY_FACTORY_MANIFEST="+expected.Key.ExecutionManifestFingerprint)
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("fresh process: %v\n%s", e, output)
	}
	raw, e := os.ReadFile(filepath.Join(root, "restart-result.json"))
	if e != nil {
		t.Fatal(e)
	}
	var actual factoryRenderIdentity
	if e = json.Unmarshal(raw, &actual); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("fresh-process key/bridge/surface/PCM mismatch\n%s", raw)
	}
}
func TestFactoryRestartSubprocess(t *testing.T) {
	if os.Getenv("CUTLY_FACTORY_RESTART") != "1" {
		t.Skip("parent launches fresh executable with durable environment key only")
	}
	ctx := context.Background()
	root := os.Getenv("CUTLY_FACTORY_ROOT")
	store, e := prepared.New(filepath.Join(root, "store"), prepared.Options{})
	if e != nil {
		t.Fatal(e)
	}
	env, e := store.ResolveV2(ctx, os.Getenv("CUTLY_FACTORY_PLAN"), os.Getenv("CUTLY_FACTORY_MANIFEST"))
	if e != nil {
		t.Fatal(e)
	}
	// Rebuild independent host byte map and measured Authority. No live handle/config/receipt is
	// passed by the parent, and serialized locators do not select filesystem paths.
	config, _ := factoryHost(t, root, env.Document)
	factory, e := New(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := factory.Reconstruct(ctx, env)
	if e != nil {
		t.Fatal(e)
	}
	identity := factoryRender(t, bundle, env)
	raw, e := json.Marshal(identity)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "restart-result.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}

type factoryPanicValue struct{}

func (factoryPanicValue) String() string { panic("recovered callback value must not be formatted") }

func TestFactoryActualSourcesAndPreflightFailureZero(t *testing.T) {
	for _, mode := range []string{"tamper", "pins", "unsupported", "source-drift", "late-open", "callback-cancel", "callback-panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			d := factoryMixedDocument(factoryFixtureBytes(t))
			config, source := factoryHost(t, root, d)
			env := factoryEnvironment(t, config, source, d)
			reconstructContext := ctx
			var cancel context.CancelFunc
			switch mode {
			case "tamper":
				env.Document.Revision++
			case "pins":
				env.Manifest.CompilerBuild = factoryDigest([]byte("other compiler"))
				env.Manifest, e = renderapi.FreezeExecutionManifestV2(ctx, env.Plan, env.Manifest)
				if e != nil {
					t.Fatal(e)
				}
				env, e = prepared.NewEnvironmentV2(ctx, env.Document, env.Plan, env.Manifest)
				if e != nil {
					t.Fatal(e)
				}
			case "unsupported":
				later := env.Document.Timelines[0].Tracks[1].Clips[1]
				later.ID = "outside-scope-same-asset"
				later.TimelineRange.Start = factoryTime(2, 1)
				env.Document.Timelines[0].Tracks[1].Clips = append(env.Document.Timelines[0].Tracks[1].Clips, later)
				source.documentHash, e = documentv2.DocumentHash(env.Document)
				if e != nil {
					t.Fatal(e)
				}
				env = factoryEnvironment(t, config, source, env.Document)
			case "source-drift":
				source.drift = "image"
			case "late-open":
				source.fail = "image"
			case "callback-cancel":
				reconstructContext, cancel = context.WithCancel(ctx)
				defer cancel()
				source.onOpen = func(id documentv2.ID) {
					if id == "image" {
						cancel()
					}
				}
			case "callback-panic":
				source.onOpen = func(id documentv2.ID) {
					if id == "image" {
						panic(factoryPanicValue{})
					}
				}
			}
			source.reset()
			factory, e := New(ctx, config)
			if e != nil {
				t.Fatal(e)
			}
			bundle, e := factory.Reconstruct(reconstructContext, env)
			if e == nil || bundle != nil {
				t.Fatal("failure published partial Bundle")
			}
			if mode == "tamper" || mode == "pins" || mode == "unsupported" {
				source.requireZero(t)
			} else if source.open.Load() == 0 {
				t.Fatal("source failure not exercised")
			}
			if mode == "callback-cancel" || mode == "callback-panic" {
				if source.open.Load() != 3 {
					t.Fatal("callback did not fail after earlier resources were retained")
				}
				if mode == "callback-cancel" && !errors.Is(e, context.Canceled) {
					t.Fatalf("callback cancellation lost: %v", e)
				}
				if mode == "callback-panic" && !errors.Is(e, ErrPanic) {
					t.Fatalf("callback panic lost: %v", e)
				}
				source.onOpen = nil
				source.reset()
				recovered, err := factory.Reconstruct(ctx, env)
				if err != nil || !recovered.Valid() {
					t.Fatalf("failed call poisoned factory: %v", err)
				}
				_ = factoryRender(t, recovered, env)
				if source.open.Load() != 3 || source.authorize.Load() != 3 || source.verify.Load() != 3 {
					t.Fatal("successful retry reused partial failed-call state")
				}
			}
		})
	}
}

func TestFactoryBundleGuardsAndConcurrentOwnedResults(t *testing.T) {
	ctx := context.Background()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	d := factoryMixedDocument(factoryFixtureBytes(t))
	config, source := factoryHost(t, root, d)
	env := factoryEnvironment(t, config, source, d)
	factory, e := New(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := factory.Reconstruct(ctx, env)
	if e != nil {
		t.Fatal(e)
	}
	expected := factoryRender(t, bundle, env)
	for _, index := range []int64{-1, 0, 3} {
		if v, e := bundle.RenderSurfaceFrame(ctx, index); e == nil || !reflect.ValueOf(v).IsZero() {
			t.Fatal("out of range surface")
		}
		if v, e := bundle.RenderFrame(ctx, index); e == nil || !reflect.ValueOf(v).IsZero() {
			t.Fatal("out of range frame")
		}
	}
	for _, pair := range [][2]int64{{-1, 0}, {2, -1}, {1, 1}, {5, 2}, {6, 0}, {2, 1 << 62}} {
		if v, e := bundle.RenderPCM(ctx, pair[0], pair[1]); e == nil || !reflect.ValueOf(v).IsZero() {
			t.Fatalf("bad PCM request %+v", pair)
		}
	}
	if _, e := bundle.RenderPCM(ctx, 2, 0); e != nil {
		t.Fatal("valid zero block", e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, bad := range []context.Context{nil, canceled} {
		if v, e := bundle.RenderPCM(bad, 2, 1); e == nil || !reflect.ValueOf(v).IsZero() {
			t.Fatal("bad context PCM")
		}
		if v, e := bundle.RenderSurfaceFrame(bad, 1); e == nil || !reflect.ValueOf(v).IsZero() {
			t.Fatal("bad context surface")
		}
		if v, e := bundle.Bridge(bad); e == nil || !reflect.ValueOf(v).IsZero() {
			t.Fatal("bad context bridge")
		}
	}
	source.reset()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !reflect.DeepEqual(factoryRender(t, bundle, env), expected) {
				t.Error("concurrent seek identity")
			}
		}()
	}
	wg.Wait()
	source.requireZero(t)
}
