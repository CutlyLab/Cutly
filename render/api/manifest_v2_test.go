package renderapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestFreezeDecodeAndVerifyExecutionManifestV2(t *testing.T) {
	_, plan, bound := executionV2Plan(t)
	draft := executionV2Draft(plan)
	slices.Reverse(draft.Resources)
	slices.Reverse(draft.Backend.TrackKinds)
	slices.Reverse(draft.Backend.AssetKinds)
	slices.Reverse(draft.Backend.Features)

	verified, err := FreezeExecutionManifestV2(context.Background(), bound, draft)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verified.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != ExecutionManifestV2SchemaVersion || manifest.Profile != ExecutionManifestV2Profile ||
		manifest.PlanFingerprint != plan.Fingerprint || manifest.DocumentHash != plan.DocumentHash || manifest.Fingerprint == "" {
		t.Fatalf("manifest identity = %+v", manifest)
	}
	if string(manifest.Visual.Configuration) != `{"a":1,"z":2}` || string(manifest.Audio.Configuration) != `{"gain":1}` {
		t.Fatalf("program configurations = %s / %s", manifest.Visual.Configuration, manifest.Audio.Configuration)
	}
	for index := 1; index < len(manifest.Resources); index++ {
		if manifest.Resources[index-1].AssetID >= manifest.Resources[index].AssetID {
			t.Fatalf("resources are not canonical: %+v", manifest.Resources)
		}
	}
	raw, err := CanonicalExecutionManifestV2(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExecutionManifestV2(raw)
	if err != nil || decoded.Fingerprint != manifest.Fingerprint {
		t.Fatalf("decoded fingerprint=%q err=%v", decoded.Fingerprint, err)
	}
	reverified, err := VerifyExecutionManifestV2(context.Background(), bound, decoded)
	if err != nil || reverified.Fingerprint() != manifest.Fingerprint || reverified.PlanFingerprint() != plan.Fingerprint {
		t.Fatalf("reverified identity=%q/%q err=%v", reverified.Fingerprint(), reverified.PlanFingerprint(), err)
	}

	manifest.Resources[0].AssetID = "mutated"
	manifest.Visual.Configuration[0] = '['
	second, err := verified.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if second.Resources[0].AssetID == "mutated" || bytes.HasPrefix(second.Visual.Configuration, []byte{'['}) {
		t.Fatal("verified manifest retained caller mutation")
	}
	if _, err := (VerifiedExecutionManifestV2{}).Snapshot(); !errors.Is(err, ErrVerifiedExecutionManifestV2Required) {
		t.Fatalf("zero verified snapshot error = %v", err)
	}
}

func TestFreezeExecutionManifestV2RequiresDocumentBinding(t *testing.T) {
	_, plan, _ := executionV2Plan(t)
	if _, err := FreezeExecutionManifestV2(context.Background(), renderplanv2.DocumentBoundPlan{}, executionV2Draft(plan)); !errors.Is(err, renderplanv2.ErrDocumentBoundRequired) {
		t.Fatalf("zero document binding error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FreezeExecutionManifestV2(cancelled, renderplanv2.DocumentBoundPlan{}, executionV2Draft(plan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestFreezeExecutionManifestV2RejectsPlanAndRuntimeDrift(t *testing.T) {
	_, plan, bound := executionV2Plan(t)
	tests := []struct {
		name   string
		mutate func(*ExecutionManifestV2)
	}{
		{name: "capability gap", mutate: func(value *ExecutionManifestV2) {
			value.Backend.Features = value.Backend.Features[1:]
		}},
		{name: "resource substitution", mutate: func(value *ExecutionManifestV2) {
			value.Resources[0].ContentHash = digest('0')
			value.Resources[0].Locator = "cas://sha256/" + digest('0')[len("sha256:"):]
		}},
		{name: "output drift", mutate: func(value *ExecutionManifestV2) { value.Output.Width++ }},
		{name: "missing visual program", mutate: func(value *ExecutionManifestV2) { value.Visual = ProgramSelectionV2{} }},
		{name: "missing audio program", mutate: func(value *ExecutionManifestV2) { value.Audio = ProgramSelectionV2{} }},
		{name: "wrong pipeline transport", mutate: func(value *ExecutionManifestV2) { value.TransportProfile = TransportBoundedRingV1 }},
		{name: "media inspection confusion", mutate: func(value *ExecutionManifestV2) {
			for index := range value.Resources {
				if value.Resources[index].Kind == documentv2.AssetKindVideo {
					value.Resources[index].InspectionFingerprint = digest('1')
					return
				}
			}
		}},
		{name: "static probe confusion", mutate: func(value *ExecutionManifestV2) {
			for index := range value.Resources {
				if value.Resources[index].Kind == documentv2.AssetKindFont {
					value.Resources[index].ProbeFingerprint = digest('1')
					return
				}
			}
		}},
		{name: "noninteger configuration", mutate: func(value *ExecutionManifestV2) { value.Visual.Configuration = json.RawMessage(`{"value":1.5}`) }},
		{name: "oversized configuration", mutate: func(value *ExecutionManifestV2) {
			value.Visual.Configuration = append([]byte{'"'}, bytes.Repeat([]byte{'a'}, MaxProgramConfigurationV2Bytes)...)
			value.Visual.Configuration = append(value.Visual.Configuration, '"')
		}},
		{name: "raw absolute file path", mutate: func(value *ExecutionManifestV2) {
			value.Resources[0].LocatorScheme = LocatorFile
			value.Resources[0].Locator = "/etc/passwd"
		}},
		{name: "backend width outside protocol", mutate: func(value *ExecutionManifestV2) {
			value.Backend.MaxWidth = math.MaxInt32 + 1
		}},
		{name: "aggregate materialization budget", mutate: func(value *ExecutionManifestV2) {
			for index := range value.Resources {
				value.Resources[index].SizeBytes = MaxMaterializedResourceBytesV2/2 + 1
			}
		}},
		{name: "resource admission limit", mutate: func(value *ExecutionManifestV2) {
			value.Resources = make([]MaterializedResourceV2, MaxExecutionManifestV2Resources+1)
		}},
		{name: "feature admission limit", mutate: func(value *ExecutionManifestV2) {
			value.Backend.Features = make([]videoedit.CapabilityID, MaxExecutionManifestV2Features+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := executionV2Draft(plan)
			test.mutate(&draft)
			if _, err := FreezeExecutionManifestV2(context.Background(), bound, draft); !errors.Is(err, ErrExecutionManifestV2Invalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestFreezeExecutionManifestV2AcceptsPortableFileURIDeclaration(t *testing.T) {
	_, plan, bound := executionV2Plan(t)
	draft := executionV2Draft(plan)
	draft.Resources[0].LocatorScheme = LocatorFile
	draft.Resources[0].Locator = "file:///controlled/media/input.bin"
	if _, err := FreezeExecutionManifestV2(context.Background(), bound, draft); err != nil {
		t.Fatalf("portable file URI declaration: %v", err)
	}
}

func TestDecodeExecutionManifestV2RejectsWireAndBindingTampering(t *testing.T) {
	_, plan, bound := executionV2Plan(t)
	verified, err := FreezeExecutionManifestV2(context.Background(), bound, executionV2Draft(plan))
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := verified.Snapshot()
	raw, _ := CanonicalExecutionManifestV2(manifest)

	withWhitespace := append([]byte("\n"), raw...)
	if _, err := DecodeExecutionManifestV2(withWhitespace); !errors.Is(err, ErrExecutionManifestV2Invalid) {
		t.Fatalf("noncanonical error = %v", err)
	}
	duplicate := bytes.Replace(raw, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1)
	if _, err := DecodeExecutionManifestV2(duplicate); !errors.Is(err, ErrExecutionManifestV2Invalid) {
		t.Fatalf("duplicate error = %v", err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeExecutionManifestV2(unknown); !errors.Is(err, ErrExecutionManifestV2Invalid) {
		t.Fatalf("unknown error = %v", err)
	}
	if _, err := DecodeExecutionManifestV2(bytes.Repeat([]byte{' '}, MaxCanonicalExecutionManifestV2Bytes+1)); !errors.Is(err, ErrExecutionManifestV2Invalid) {
		t.Fatalf("oversized error = %v", err)
	}

	tampered := manifest
	tampered.Output.VideoCodec = "h265"
	tamperedRaw, _ := canonicaljson.Marshal(tampered)
	if _, err := DecodeExecutionManifestV2(tamperedRaw); !errors.Is(err, ErrExecutionManifestV2FingerprintMismatch) {
		t.Fatalf("fingerprint tamper error = %v", err)
	}

	omitted := cloneExecutionManifestV2(manifest)
	omitted.Resources = omitted.Resources[1:]
	omitted.Fingerprint, err = executionManifestV2Fingerprint(omitted)
	if err != nil {
		t.Fatal(err)
	}
	omittedRaw, err := CanonicalExecutionManifestV2(omitted)
	if err != nil {
		t.Fatal(err)
	}
	omittedDecoded, err := DecodeExecutionManifestV2(omittedRaw)
	if err != nil {
		t.Fatalf("wire-valid omission failed before document binding: %v", err)
	}
	if _, err := VerifyExecutionManifestV2(context.Background(), bound, omittedDecoded); !errors.Is(err, ErrExecutionManifestV2Invalid) {
		t.Fatalf("binding omission error = %v", err)
	}

	configTamper := cloneExecutionManifestV2(manifest)
	configTamper.Visual.Configuration = json.RawMessage(`{"a":2,"z":2}`)
	configTamper.Fingerprint, _ = executionManifestV2Fingerprint(configTamper)
	configRaw, _ := canonicaljson.Marshal(configTamper)
	if _, err := DecodeExecutionManifestV2(configRaw); !errors.Is(err, ErrExecutionManifestV2Invalid) {
		t.Fatalf("configuration fingerprint error = %v", err)
	}
}

func TestVerifiedExecutionManifestV2ConcurrentReadSafety(t *testing.T) {
	_, plan, bound := executionV2Plan(t)
	verified, err := FreezeExecutionManifestV2(context.Background(), bound, executionV2Draft(plan))
	if err != nil {
		t.Fatal(err)
	}
	want := verified.Fingerprint()
	var group sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				value, snapshotErr := verified.Snapshot()
				if snapshotErr != nil || value.Fingerprint != want || verified.Fingerprint() != want {
					t.Errorf("snapshot fingerprint=%q err=%v", value.Fingerprint, snapshotErr)
					return
				}
				value.Visual.Configuration[0] = '['
			}
		}()
	}
	group.Wait()
}

func TestExecutionManifestV2ProtocolFixtures(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("testdata", "execution-manifest-document-v2-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeExecutionManifestV2(bytes.TrimSpace(valid))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Fingerprint != "sha256:36f02a89e082e9c779067d7b2305f32bad9c7b5cd48a6605112cbe309aaed7dd" {
		t.Fatalf("fixture fingerprint = %q", manifest.Fingerprint)
	}
	canonical, err := CanonicalExecutionManifestV2(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "execution-manifest-document-v2-v2.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(want)) || !bytes.Equal(bytes.TrimSpace(valid), bytes.TrimSpace(want)) {
		t.Fatal("execution manifest v2 canonical fixture drift")
	}
	invalid, err := os.ReadFile(filepath.Join("testdata", "execution-manifest-document-v2-v2.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeExecutionManifestV2(bytes.TrimSpace(invalid)); !errors.Is(err, ErrExecutionManifestV2FingerprintMismatch) {
		t.Fatalf("invalid fixture error = %v", err)
	}
}

func executionV2Plan(t *testing.T) (documentv2.Document, renderplanv2.Plan, renderplanv2.DocumentBoundPlan) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	requested := documentv2.TimeRange{Start: videoedit.RationalTime{Value: 4, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}}
	target := videoedit.RenderTarget{
		TimelineID: "timeline-main", Width: 1920, Height: 1080,
		FrameClock:  videoedit.FrameClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, FramesPerSecond: videoedit.RationalTime{Value: 24, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "rgba16", ColorSpace: "linear-bt709",
	}
	plan, diagnostics := renderplanv2.Resolve(context.Background(), document, requested, target)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	bound, err := renderplanv2.VerifyAgainstDocument(context.Background(), plan, document)
	if err != nil {
		t.Fatal(err)
	}
	return document, plan, bound
}

func executionV2Draft(plan renderplanv2.Plan) ExecutionManifestV2 {
	resources := make([]MaterializedResourceV2, len(plan.Resources))
	for index, planned := range plan.Resources {
		contentHash := planned.ContentHash
		if contentHash == "" {
			contentHash = digest('a')
		}
		resources[index] = MaterializedResourceV2{
			AssetID: planned.AssetID, Kind: planned.Kind, ContentHash: contentHash,
			SizeBytes: int64(index + 1), LocatorScheme: LocatorCAS,
			Locator:                "cas://sha256/" + contentHash[len("sha256:"):],
			MaterializationProfile: "cutly.materialize.cas.v1",
		}
		switch planned.Kind {
		case documentv2.AssetKindVideo, documentv2.AssetKindAudio:
			resources[index].ProbeFingerprint = digest('b')
		default:
			resources[index].InspectionFingerprint = digest('c')
		}
	}
	return ExecutionManifestV2{
		CompilerBuild:    digest('1'),
		EvaluatorBuild:   digest('2'),
		ProviderBuild:    digest('3'),
		PipelineProfile:  PipelineChunkedRestartableV1,
		TransportProfile: TransportCASSpoolV1,
		Resources:        resources,
		Backend: BackendCapabilitiesV2{
			BackendID: "native-cpu", BuildFingerprint: digest('4'),
			TrackKinds: append([]documentv2.TrackKind(nil), plan.RequiredCapabilities.TrackKinds...),
			AssetKinds: append([]documentv2.AssetKind(nil), plan.RequiredCapabilities.AssetKinds...),
			Features:   append([]videoedit.CapabilityID(nil), plan.RequiredCapabilities.Features...),
			MaxWidth:   8192, MaxHeight: 8192,
		},
		MediaRuntime: MediaRuntime{
			FFmpegBuild: digest('5'), DecoderProfile: "cutly.decode.native-media.v1",
			EncoderProfile: "cutly.encode.h264-aac.v1", TextShaperBuild: digest('6'),
			ColorProfile: "cutly.color.linear-bt709.v1",
		},
		Output: OutputSpecV2{
			ReferenceProfile: "cutly.reference.document-v2.v1", Format: "mp4",
			Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			Intermediate: IntermediateProfilesV2{
				VideoSurfaceProfile: IntermediateVideoSurfaceRGBA16V1,
				AudioBlockProfile:   IntermediateAudioBlockPCM16LEV1,
			},
			DecodedVideoProfile: DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: DecodedAudioProfilePCM16LEV1,
		},
		Visual: ProgramSelectionV2{
			Enabled: true, Profile: "cutly.program.visual.reference.v1",
			DescriptorFingerprint: digest('7'), Configuration: json.RawMessage(`{"z":2,"a":1}`),
			SourceManifestFingerprint: digest('8'),
		},
		Audio: ProgramSelectionV2{
			Enabled: true, Profile: "cutly.program.audio.reference.v1",
			DescriptorFingerprint: digest('9'), Configuration: json.RawMessage(`{"gain":1}`),
			SourceManifestFingerprint: digest('a'),
		},
		DeterministicSeed: 42,
	}
}
