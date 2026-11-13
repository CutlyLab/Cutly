package prepared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

// rebuildFixture assembles one internally consistent image+vector environment whose manifest
// pins the real referencevisual descriptor and source-manifest identities.
func rebuildFixture(t *testing.T) (documentv2.Document, renderplanv2.Plan, renderapi.ExecutionManifestV2, []byte, referencevisual.Config) {
	t.Helper()
	encoded := rebuildPNG(t, 3, 2)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "rebuild-document", Revision: 3,
		Assets: []documentv2.Asset{
			{ID: "image", Kind: documentv2.AssetKindImage, URI: "image.png", ContentHash: digestOf(encoded)},
			{ID: "vector", Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
				Profile:  documentv2.VectorProfilePathViewportPXV2,
				ViewBox:  documentv2.Rect{X: documentv2.Rational{Numerator: 0, Denominator: 1}, Y: documentv2.Rational{Numerator: 0, Denominator: 1}, Width: documentv2.Rational{Numerator: 6, Denominator: 1}, Height: documentv2.Rational{Numerator: 4, Denominator: 1}},
				Viewport: &documentv2.RasterViewport{WidthPixels: 6, HeightPixels: 4},
				Paths: []documentv2.VectorPath{{
					ID: "path", FillRule: documentv2.VectorFillNonZero,
					Fill: &documentv2.ColorRGBA16{R: 65_535, A: 32_768},
					Commands: []documentv2.VectorCommand{
						{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: 0, Denominator: 1}, Y: documentv2.Rational{Numerator: 0, Denominator: 1}}}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: 6, Denominator: 1}, Y: documentv2.Rational{Numerator: 0, Denominator: 1}}}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: 6, Denominator: 1}, Y: documentv2.Rational{Numerator: 4, Denominator: 1}}}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: documentv2.Rational{Numerator: 0, Denominator: 1}, Y: documentv2.Rational{Numerator: 4, Denominator: 1}}}},
						{Op: documentv2.VectorClose},
					},
				}},
			}},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "image-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "image-clip", AssetID: "image",
				TimelineRange: documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 2}, Duration: videoedit.RationalTime{Value: 2, Rate: 2}},
				SourceRange:   documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 2}, Duration: videoedit.RationalTime{Value: 2, Rate: 2}},
			}}},
			{ID: "vector-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{
				ID: "vector-clip", AssetID: "vector",
				TimelineRange: documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 2}, Duration: videoedit.RationalTime{Value: 2, Rate: 2}},
				SourceRange:   documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 2}, Duration: videoedit.RationalTime{Value: 2, Rate: 2}},
			}}},
		}}},
	}
	config := referencevisual.DefaultConfig(6, 4)
	program, diagnostics, err := referencevisual.Compile(context.Background(), document, []referencevisual.Binding{{AssetID: "image", EncodedPNG: encoded}}, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("reference compile diagnostics=%+v err=%v", diagnostics, err)
	}
	requested := documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 1, Rate: 1}}
	target := videoedit.RenderTarget{
		TimelineID: "timeline", Width: 6, Height: 4,
		FrameClock:  videoedit.FrameClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, FramesPerSecond: videoedit.RationalTime{Value: 2, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "rgba16", ColorSpace: "linear-bt709",
	}
	plan, resolveDiagnostics := renderplanv2.Resolve(context.Background(), document, requested, target)
	if documentv2.HasErrorDiagnostics(resolveDiagnostics) {
		t.Fatalf("resolve diagnostics: %+v", resolveDiagnostics)
	}
	descriptorFingerprint, err := canonicalFingerprint(referencevisual.Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	sourceManifestFingerprint, err := canonicalFingerprint(program.SourceManifest())
	if err != nil {
		t.Fatal(err)
	}
	// The visual configuration pins the canonical serialization of the exact referencevisual
	// program configuration; the factory verifies this binding.
	configRaw, err := videoedit.CanonicalJSON(program.Config())
	if err != nil {
		t.Fatal(err)
	}
	bound, err := renderplanv2.VerifyAgainstDocument(context.Background(), plan, document)
	if err != nil {
		t.Fatal(err)
	}
	resources := make([]renderapi.MaterializedResourceV2, len(plan.Resources))
	for index, planned := range plan.Resources {
		contentHash := planned.ContentHash
		if contentHash == "" {
			contentHash = digest('a')
		}
		resources[index] = renderapi.MaterializedResourceV2{
			AssetID: planned.AssetID, Kind: planned.Kind, ContentHash: contentHash,
			SizeBytes: int64(index + 1), LocatorScheme: renderapi.LocatorCAS,
			Locator:                "cas://sha256/" + contentHash[len("sha256:"):],
			MaterializationProfile: "cutly.materialize.cas.v1",
			InspectionFingerprint:  digest('c'),
		}
	}
	verified, err := renderapi.FreezeExecutionManifestV2(context.Background(), bound, renderapi.ExecutionManifestV2{
		CompilerBuild:    digest('1'),
		EvaluatorBuild:   digest('2'),
		ProviderBuild:    digest('3'),
		PipelineProfile:  renderapi.PipelineChunkedRestartableV1,
		TransportProfile: renderapi.TransportCASSpoolV1,
		Resources:        resources,
		Backend: renderapi.BackendCapabilitiesV2{
			BackendID: "native-cpu", BuildFingerprint: digest('4'),
			TrackKinds: append([]documentv2.TrackKind(nil), plan.RequiredCapabilities.TrackKinds...),
			AssetKinds: append([]documentv2.AssetKind(nil), plan.RequiredCapabilities.AssetKinds...),
			Features:   append([]videoedit.CapabilityID(nil), plan.RequiredCapabilities.Features...),
			MaxWidth:   8192, MaxHeight: 8192,
		},
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: digest('5'), DecoderProfile: "cutly.decode.native-media.v1",
			EncoderProfile: "cutly.encode.h264-aac.v1", ColorProfile: "cutly.color.linear-bt709.v1",
		},
		Output: renderapi.OutputSpecV2{
			ReferenceProfile: "cutly.reference.document-v2.v1", Format: "mp4",
			Width: 6, Height: 4, PixelFormat: "rgba16", ColorSpace: "linear-bt709",
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: 48000, Channels: 2,
			Intermediate: renderapi.IntermediateProfilesV2{
				VideoSurfaceProfile: renderapi.IntermediateVideoSurfaceRGBA16V1,
				AudioBlockProfile:   renderapi.IntermediateAudioBlockPCM16LEV1,
			},
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		Visual: renderapi.ProgramSelectionV2{
			Enabled: true, Profile: ReferenceVisualProgramProfile,
			DescriptorFingerprint:     descriptorFingerprint,
			SourceManifestFingerprint: sourceManifestFingerprint,
			Configuration:             json.RawMessage(configRaw),
		},
		Audio: renderapi.ProgramSelectionV2{
			Enabled: true, Profile: "cutly.program.audio.reference.v1",
			DescriptorFingerprint:     digest('9'),
			Configuration:             json.RawMessage(`{"gain":1}`),
			SourceManifestFingerprint: digest('a'),
		},
		DeterministicSeed: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verified.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return document, plan, manifest, encoded, config
}

func TestRebuildReferenceVisualRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, manifest, encoded, config := rebuildFixture(t)
	if _, _, err := store.PutV2(context.Background(), document, plan, manifest); err != nil {
		t.Fatal(err)
	}
	// Restart: reconstruct from the persisted environment plus authorized resources only.
	reopened, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := reopened.ResolveV2(context.Background(), plan.Fingerprint, manifest.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	source := staticSource{resources: map[documentv2.ID][]byte{"image": encoded}}
	program, err := RebuildReferenceVisual(context.Background(), environment, source, config)
	if err != nil {
		t.Fatal(err)
	}
	if program.DocumentHash() != plan.DocumentHash {
		t.Fatalf("rebuilt document hash = %s", program.DocumentHash())
	}
	// Exit evidence: the reconstructed program renders the identical frame hash.
	reference, referenceConfig, err := rebuildReferenceProgram(t, document, encoded, config)
	if err != nil {
		t.Fatal(err)
	}
	at := documentv2.RationalTime{Value: 1, Rate: 2}
	rebuilt, err := program.RenderAt(context.Background(), "timeline", at)
	if err != nil {
		t.Fatal(err)
	}
	original, err := reference.RenderAt(context.Background(), "timeline", at)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.PNG.ArtifactHash != original.PNG.ArtifactHash || rebuilt.Visual.PixelHash != original.Visual.PixelHash {
		t.Fatalf("frame divergence rebuilt=%s original=%s", rebuilt.PNG.ArtifactHash, original.PNG.ArtifactHash)
	}
	// Both programs ran under their normalized configurations; the fixture input config is the
	// un-normalized caller form.
	if referenceConfig != program.Config() {
		t.Fatal("rebuilt program configuration diverged from the reference program")
	}
}

func TestRebuildReferenceVisualRejectsSubstitutions(t *testing.T) {
	document, plan, manifest, encoded, config := rebuildFixture(t)
	source := staticSource{resources: map[documentv2.ID][]byte{"image": encoded}}

	// Geometry substitution.
	stretched := config
	stretched.Width, stretched.Height = 12, 8
	if _, err := RebuildReferenceVisual(context.Background(), EnvironmentV2{Document: document, Plan: plan, Manifest: manifest}, source, stretched); err == nil {
		t.Fatal("geometry substitution accepted")
	}

	// Descriptor substitution.
	foreign := manifest
	foreign.Visual.DescriptorFingerprint = digest('f')
	if _, err := RebuildReferenceVisual(context.Background(), EnvironmentV2{Document: document, Plan: plan, Manifest: foreign}, source, config); err == nil {
		t.Fatal("foreign descriptor accepted")
	}

	// Program profile substitution.
	otherProfile := manifest
	otherProfile.Visual.Profile = "cutly.program.visual.production.v1"
	if _, err := RebuildReferenceVisual(context.Background(), EnvironmentV2{Document: document, Plan: plan, Manifest: otherProfile}, source, config); err == nil {
		t.Fatal("non-reference program profile accepted")
	}

	// Content substitution: bytes that do not rehash to the pinned content hash.
	tampered := append(append([]byte(nil), encoded...), 0)
	if _, err := RebuildReferenceVisual(context.Background(), EnvironmentV2{Document: document, Plan: plan, Manifest: manifest}, staticSource{resources: map[documentv2.ID][]byte{"image": tampered}}, config); err == nil {
		t.Fatal("tampered resource accepted")
	}

	// Missing resource fails closed.
	if _, err := RebuildReferenceVisual(context.Background(), EnvironmentV2{Document: document, Plan: plan, Manifest: manifest}, staticSource{}, config); err == nil {
		t.Fatal("missing resource accepted")
	}

	// Cancellation before admission.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RebuildReferenceVisual(canceled, EnvironmentV2{Document: document, Plan: plan, Manifest: manifest}, source, config); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rebuild = %v", err)
	}
}

func rebuildReferenceProgram(t *testing.T, document documentv2.Document, encoded []byte, config referencevisual.Config) (*referencevisual.Program, referencevisual.Config, error) {
	t.Helper()
	program, diagnostics, err := referencevisual.Compile(context.Background(), document, []referencevisual.Binding{{AssetID: "image", EncodedPNG: encoded}}, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, config, err
	}
	return program, program.Config(), nil
}

type staticSource struct {
	resources map[documentv2.ID][]byte
}

func (source staticSource) EncodedPNG(_ context.Context, assetID documentv2.ID, _ string) ([]byte, error) {
	if encoded, exists := source.resources[assetID]; exists {
		return append([]byte(nil), encoded...), nil
	}
	return nil, os.ErrNotExist
}

func digestOf(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// rebuildPNG encodes one small strict-profile PNG (IHDR + injected sRGB chunk).
func rebuildPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value.SetNRGBA(x, y, stdcolor.NRGBA{R: uint8(40 * x), G: uint8(60 * y), B: 128, A: 254})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatal(err)
	}
	encodedBytes := encoded.Bytes()
	length := int(binary.BigEndian.Uint32(encodedBytes[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = 0
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(encodedBytes)+len(chunk))
	result = append(result, encodedBytes[:end]...)
	result = append(result, chunk...)
	return append(result, encodedBytes[end:]...)
}
