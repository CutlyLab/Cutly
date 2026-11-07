package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestFreezeDecodeAndDecodedRoots(t *testing.T) {
	draft := evidenceTestDraft()
	draft.Duration = videoedit.RationalTime{Value: 96000, Rate: 48000}
	originalFirst := draft.DecodedVideo.Chunks[0].First
	manifest, err := freeze(draft)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.Fingerprint == "" || manifest.DecodedVideo.RootHash == "" || manifest.DecodedAudio.RootHash == "" {
		t.Fatalf("frozen manifest = %+v", manifest)
	}
	if manifest.Duration != (videoedit.RationalTime{Value: 2, Rate: 1}) {
		t.Fatalf("duration was not normalized: %+v", manifest.Duration)
	}
	if manifest.DecodedVideo.Chunks[0].First != -2 || manifest.Warnings[0].Code != "A.WARNING" || manifest.StageTimings[0].Stage != "decode" {
		t.Fatalf("manifest not canonicalized: %+v", manifest)
	}
	if draft.DecodedVideo.Chunks[0].First != originalFirst {
		t.Fatal("Freeze mutated caller-owned chunks")
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Fingerprint != manifest.Fingerprint {
		t.Fatalf("fingerprint = %q, want %q", decoded.Fingerprint, manifest.Fingerprint)
	}

	tampered := manifest
	tampered.Artifact.SizeBytes++
	raw, _ = json.Marshal(tampered)
	if _, err := Decode(raw); !errors.Is(err, ErrEvidenceFingerprintMismatch) {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestDecodedRootBindsDomainRangeOrderAndBytes(t *testing.T) {
	sequence := DecodedSequence{
		Domain:    "rgba8.bt709.full.v1",
		Unit:      EvidenceFrames,
		UnitBytes: 16,
		Range:     videoedit.TickRange{First: -1, Count: 2},
		Chunks: []DecodedChunk{
			{First: -1, Count: 1, ContentHash: DigestDecodedBytes([]byte("frame-a"))},
			{First: 0, Count: 1, ContentHash: DigestDecodedBytes([]byte("frame-b"))},
		},
	}
	root, err := ComputeDecodedRoot(sequence)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*DecodedSequence){
		func(value *DecodedSequence) { value.Domain = "rgba8.bt2020.full.v1" },
		func(value *DecodedSequence) { value.UnitBytes++ },
		func(value *DecodedSequence) { value.Range.First-- },
		func(value *DecodedSequence) { value.Chunks[1].ContentHash = DigestDecodedBytes([]byte("frame-c")) },
	}
	for index, mutate := range mutations {
		changed := sequence
		changed.Chunks = append([]DecodedChunk(nil), sequence.Chunks...)
		mutate(&changed)
		got, changedErr := ComputeDecodedRoot(changed)
		if changedErr == nil && got == root {
			t.Fatalf("mutation %d did not change root", index)
		}
	}
}

func TestFreezeRejectsMissingOverlappingAndMismatchedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "missing chunk", mutate: func(value *Manifest) { value.DecodedVideo.Chunks = value.DecodedVideo.Chunks[:1] }},
		{name: "overlap", mutate: func(value *Manifest) { value.DecodedVideo.Chunks[1].First = -1 }},
		{name: "wrong count", mutate: func(value *Manifest) { value.FrameCount++ }},
		{name: "container digest uppercase", mutate: func(value *Manifest) { value.Artifact.ContentHash = strings.ToUpper(value.Artifact.ContentHash) }},
		{name: "unversioned degradation", mutate: func(value *Manifest) { value.DegradedCapabilities = []videoedit.CapabilityID{"video.blur"} }},
		{name: "negative timing", mutate: func(value *Manifest) { value.StageTimings[0].DurationMicros = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := evidenceTestDraft()
			test.mutate(&draft)
			if _, err := freeze(draft); !errors.Is(err, ErrEvidenceInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsUnknownAndNonCanonicalFields(t *testing.T) {
	manifest, err := freeze(evidenceTestDraft())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := Decode(withUnknown); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("unknown error = %v", err)
	}
	duplicateRoot := append([]byte(`{"schema_version":999,`), raw[1:]...)
	if _, err := Decode(duplicateRoot); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("duplicate field error = %v", err)
	}
	caseAlias := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, err := Decode(caseAlias); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("case alias error = %v", err)
	}
	var missingRequired map[string]json.RawMessage
	if err := json.Unmarshal(raw, &missingRequired); err != nil {
		t.Fatal(err)
	}
	delete(missingRequired, "peak_rss_bytes")
	missingRaw, _ := json.Marshal(missingRequired)
	if _, err := Decode(missingRaw); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("missing required zero field error = %v", err)
	}

	uncanonical := cloneManifest(manifest)
	uncanonical.StageTimings[0], uncanonical.StageTimings[1] = uncanonical.StageTimings[1], uncanonical.StageTimings[0]
	uncanonical.Fingerprint, _ = manifestFingerprint(uncanonical)
	raw, _ = json.Marshal(uncanonical)
	if _, err := Decode(raw); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("ordering error = %v", err)
	}
}

func TestDigestDecodedBytesStable(t *testing.T) {
	if got, want := DigestDecodedBytes([]byte("cutly")), "sha256:543b380fa53a5a791715013129130e089e3f882d3a4220a1dcd17f5df6c52fb8"; got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
	if bytes.Equal([]byte(DigestDecodedBytes([]byte("a"))), []byte(DigestDecodedBytes([]byte("b")))) {
		t.Fatal("different decoded bytes produced equal digest")
	}
}

func evidenceTestDraft() Manifest {
	return Manifest{
		Document:                     DocumentIdentity{ID: "document-1", Revision: 7, Hash: evidenceDigest('a')},
		PlanFingerprint:              evidenceDigest('b'),
		ExecutionManifestFingerprint: evidenceDigest('c'),
		Backend:                      BackendIdentity{ID: "ffmpeg-hardcut", BuildFingerprint: evidenceDigest('d')},
		Runtime: RuntimeIdentity{
			FFmpegBuild: evidenceDigest('e'), DecoderProfile: "ffmpeg-sw-v1",
			EncoderProfile: "libx264-aac-v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: OutputProfile{
			ReferenceProfile: "cutly.reference.cpu.v1", Format: "mp4", Width: 2, Height: 2,
			PixelFormat: "yuv420p", ColorSpace: "bt709", VideoCodec: "h264", AudioCodec: "aac",
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
			AudioSampleFormat: "s16le", SampleRate: 48000, Channels: 2,
		},
		Job: JobIdentity{
			SchemaVersion: renderapi.RenderJobSchemaVersion, ID: "job-1", IdempotencyKey: "job-1-once",
			TimelineRange: videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}},
			FrameRange:    videoedit.TickRange{First: -2, Count: 4}, SampleRange: videoedit.TickRange{First: -96, Count: 192},
			ArtifactURI: "file:///controlled/output.mp4",
		},
		Artifact:    ArtifactIdentity{URI: "file:///controlled/output.mp4", ContentHash: evidenceDigest('f'), SizeBytes: 4096},
		Duration:    videoedit.RationalTime{Value: 2, Rate: 1},
		FrameCount:  4,
		SampleCount: 192,
		DecodedVideo: &DecodedSequence{
			Domain: renderapi.DecodedVideoProfileRGBA8V1, Unit: EvidenceFrames, UnitBytes: 16, Range: videoedit.TickRange{First: -2, Count: 4},
			Chunks: []DecodedChunk{
				{First: 0, Count: 2, ContentHash: DigestDecodedBytes([]byte("video-b"))},
				{First: -2, Count: 2, ContentHash: DigestDecodedBytes([]byte("video-a"))},
			},
		},
		DecodedAudio: &DecodedSequence{
			Domain: renderapi.DecodedAudioProfilePCM16LEV1, Unit: EvidenceSamples, UnitBytes: 4, Range: videoedit.TickRange{First: -96, Count: 192},
			Chunks: []DecodedChunk{
				{First: 0, Count: 96, ContentHash: DigestDecodedBytes([]byte("audio-b"))},
				{First: -96, Count: 96, ContentHash: DigestDecodedBytes([]byte("audio-a"))},
			},
		},
		Warnings: []Notice{
			{Code: "Z.WARNING", Message: "last warning"},
			{Code: "A.WARNING", Path: "/output", Message: "first warning"},
		},
		Fallbacks: []Fallback{
			{Capability: videoedit.CapabilityVideoLinearRetime, Selected: "ffmpeg.setpts", Reason: "pinned reference path"},
		},
		DegradedCapabilities: []videoedit.CapabilityID{videoedit.CapabilityAudioTimeStretch},
		Cache:                CacheEvidence{Hit: false, Key: "render-cache-key-v1"},
		StageTimings: []StageTiming{
			{Stage: "encode", DurationMicros: 8000},
			{Stage: "decode", DurationMicros: 4000},
		},
		PeakRSSBytes: 1048576,
	}
}

func evidenceDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
