package renderapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestFreezeAndDecodeExecutionManifest(t *testing.T) {
	plan := executionTestPlan(t)
	draft := executionTestDraft(plan)
	// Freeze must canonicalize resource order before hashing.
	draft.Resources[0], draft.Resources[1] = draft.Resources[1], draft.Resources[0]

	manifest, err := FreezeExecutionManifest(plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != ExecutionManifestSchemaVersion || manifest.PlanFingerprint != plan.Fingerprint || manifest.Fingerprint == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.Resources[0].AssetID != "audio" || manifest.Resources[1].AssetID != "video" {
		t.Fatalf("resources not canonical: %+v", manifest.Resources)
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExecutionManifest(raw)
	if err != nil || decoded.Fingerprint != manifest.Fingerprint {
		t.Fatalf("decode = %+v, %v", decoded, err)
	}

	tampered := manifest
	tampered.Output.VideoCodec = "h265"
	raw, _ = json.Marshal(tampered)
	if _, err := DecodeExecutionManifest(raw); !errors.Is(err, ErrExecutionManifestFingerprintMismatch) {
		t.Fatalf("tamper error = %v", err)
	}

	uncanonical := manifest
	uncanonical.Resources[0], uncanonical.Resources[1] = uncanonical.Resources[1], uncanonical.Resources[0]
	raw, _ = json.Marshal(uncanonical)
	if _, err := DecodeExecutionManifest(raw); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("ordering error = %v", err)
	}
}

func TestFreezeExecutionManifestChecksPlanBackendResourcesAndOutput(t *testing.T) {
	plan := executionTestPlan(t)
	draft := executionTestDraft(plan)

	missingCapability := draft
	missingCapability.Backend.TrackKinds = []videoedit.TrackKind{videoedit.TrackKindVideo}
	if _, err := FreezeExecutionManifest(plan, missingCapability); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("capability error = %v", err)
	}

	changedResource := executionTestDraft(plan)
	changedResource.Resources[0].ContentHash = digest('1')
	if _, err := FreezeExecutionManifest(plan, changedResource); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("resource error = %v", err)
	}

	wrongOutput := executionTestDraft(plan)
	wrongOutput.Output.Width++
	if _, err := FreezeExecutionManifest(plan, wrongOutput); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("output error = %v", err)
	}

	negativeLimit := executionTestDraft(plan)
	negativeLimit.Backend.MaxWidth = -1
	if _, err := FreezeExecutionManifest(plan, negativeLimit); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("negative limit error = %v", err)
	}

	unversionedCapability := executionTestDraft(plan)
	unversionedCapability.Backend.Features = []videoedit.CapabilityID{"video.overlay"}
	if _, err := FreezeExecutionManifest(plan, unversionedCapability); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("capability ID error = %v", err)
	}

	nonCanonicalDigest := executionTestDraft(plan)
	nonCanonicalDigest.CompilerBuild = "sha256:" + strings.Repeat("A", 64)
	if _, err := FreezeExecutionManifest(plan, nonCanonicalDigest); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("digest error = %v", err)
	}

	wrongCASIdentity := executionTestDraft(plan)
	wrongCASIdentity.Resources[0].Locator = "cas://sha256/" + strings.Repeat("a", 64)
	if _, err := FreezeExecutionManifest(plan, wrongCASIdentity); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("CAS binding error = %v", err)
	}

	relativeFile := executionTestDraft(plan)
	relativeFile.Resources[1].Locator = "relative/video.mp4"
	if _, err := FreezeExecutionManifest(plan, relativeFile); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("file locator error = %v", err)
	}

	selfDescribedEvidence := executionTestDraft(plan)
	selfDescribedEvidence.Output.DecodedAudioProfile = "cutly.decoded-audio.arbitrary.v1"
	if _, err := FreezeExecutionManifest(plan, selfDescribedEvidence); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("decoded evidence profile error = %v", err)
	}
}

func TestDecodeExecutionManifestRejectsUnknownFieldsAndDuplicates(t *testing.T) {
	plan := executionTestPlan(t)
	manifest, err := FreezeExecutionManifest(plan, executionTestDraft(plan))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeExecutionManifest(withUnknown); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicateRoot := append([]byte(`{"schema_version":999,`), raw[1:]...)
	if _, err := DecodeExecutionManifest(duplicateRoot); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("duplicate field error = %v", err)
	}
	caseAlias := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, err := DecodeExecutionManifest(caseAlias); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("case alias error = %v", err)
	}
	var missingRequired map[string]json.RawMessage
	if err := json.Unmarshal(raw, &missingRequired); err != nil {
		t.Fatal(err)
	}
	delete(missingRequired, "deterministic_seed")
	missingRaw, _ := json.Marshal(missingRequired)
	if _, err := DecodeExecutionManifest(missingRaw); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("missing required zero field error = %v", err)
	}

	duplicate := manifest
	duplicate.Resources = append(duplicate.Resources, duplicate.Resources[len(duplicate.Resources)-1])
	duplicate.Fingerprint, _ = executionManifestFingerprint(duplicate)
	raw, _ = json.Marshal(duplicate)
	if _, err := DecodeExecutionManifest(raw); !errors.Is(err, ErrExecutionManifestInvalid) {
		t.Fatalf("duplicate resource error = %v", err)
	}
}

func executionTestPlan(t *testing.T) videoedit.RenderPlan {
	t.Helper()
	ten := rt(10, 1)
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion,
		ID:            "document",
		Revision:      1,
		Assets: []videoedit.Asset{
			{ID: "video", Kind: videoedit.AssetKindVideo, URI: "video.mp4", ContentHash: digest('a'), Duration: &ten},
			{ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.wav", ContentHash: digest('b'), Duration: &ten},
		},
		Timelines: []videoedit.Timeline{{
			ID: "timeline",
			Tracks: []videoedit.Track{
				{ID: "v", Kind: videoedit.TrackKindVideo, Clips: []videoedit.Clip{{ID: "vc", AssetID: "video", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}},
				{ID: "a", Kind: videoedit.TrackKindAudio, Clips: []videoedit.Clip{{ID: "ac", AssetID: "audio", TimelineRange: tr(0, 2), SourceRange: tr(0, 2)}}},
			},
		}},
	}
	plan, diagnostics := videoedit.Resolve(document, tr(0, 2), videoedit.RenderTarget{
		TimelineID:  "timeline",
		Width:       1920,
		Height:      1080,
		FrameClock:  videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(30, 1)},
		SampleClock: videoedit.SampleClock{Origin: rt(0, 1), SamplesPerSecond: 48000},
		Channels:    2,
		PixelFormat: "yuv420p",
		ColorSpace:  "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	return plan
}

func executionTestDraft(plan videoedit.RenderPlan) ExecutionManifest {
	return ExecutionManifest{
		CompilerBuild:  digest('c'),
		EvaluatorBuild: digest('d'),
		Resources: []MaterializedResource{
			{AssetID: "audio", Kind: videoedit.AssetKindAudio, ContentHash: digest('b'), SizeBytes: 200, LocatorScheme: LocatorCAS, Locator: "cas://sha256/" + strings.Repeat("b", 64), ProbeFingerprint: digest('e')},
			{AssetID: "video", Kind: videoedit.AssetKindVideo, ContentHash: digest('a'), SizeBytes: 100, LocatorScheme: LocatorFile, Locator: "/controlled/video.mp4", ProbeFingerprint: digest('f')},
		},
		Backend: videoedit.BackendCapabilities{
			BackendID:        "native-cpu",
			BuildFingerprint: digest('1'),
			TrackKinds:       []videoedit.TrackKind{videoedit.TrackKindVideo, videoedit.TrackKindAudio},
			AssetKinds:       []videoedit.AssetKind{videoedit.AssetKindVideo, videoedit.AssetKindAudio},
			MaxWidth:         3840,
			MaxHeight:        2160,
		},
		MediaRuntime: MediaRuntime{FFmpegBuild: digest('2'), DecoderProfile: "ffmpeg-sw-v1", EncoderProfile: "libx264-aac-v1", ColorProfile: "cutly.bt709-linear.v1"},
		Output: OutputSpec{
			ReferenceProfile: "cutly.reference.hardcut.v1",
			Format:           "mp4", Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			DecodedVideoProfile: DecodedVideoProfileRGBA8V1, DecodedAudioProfile: DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 42,
	}
}

func rt(value, rate int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: rate}
}

func tr(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}

func digest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
