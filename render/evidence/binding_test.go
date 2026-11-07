package evidence

import (
	"errors"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestFreezeForRenderBindsRequestAndPublishedArtifact(t *testing.T) {
	request, draft := evidenceBindingFixture(t)
	manifest, err := FreezeForRender(request, draft)
	if err != nil {
		t.Fatal(err)
	}
	artifact := renderapi.RenderArtifact{
		URI: request.Job.ArtifactURI, ContentHash: manifest.Artifact.ContentHash, SizeBytes: manifest.Artifact.SizeBytes,
		ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		EvidenceURI:                  "file:///controlled/output.mp4.evidence.json",
		EvidenceFingerprint:          manifest.Fingerprint,
		Duration:                     manifest.Duration,
		FrameCount:                   manifest.FrameCount,
		SampleCount:                  manifest.SampleCount,
	}
	if err := ValidateAgainst(manifest, request, &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.ContentHash = evidenceDigest('1')
	if err := ValidateAgainst(manifest, request, &artifact); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("artifact mismatch error = %v", err)
	}
	artifact.ContentHash = manifest.Artifact.ContentHash
	artifact.EvidenceURI = "bogus://anywhere"
	if err := ValidateAgainst(manifest, request, &artifact); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("untrusted evidence URI error = %v", err)
	}
}

func TestFreezeForRenderRejectsSelfConsistentCrossObjectForgeries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "document", mutate: func(value *Manifest) { value.Document.Hash = evidenceDigest('1') }},
		{name: "backend", mutate: func(value *Manifest) { value.Backend.ID = "other-backend" }},
		{name: "runtime", mutate: func(value *Manifest) { value.Runtime.DecoderProfile = "other-decoder.v1" }},
		{name: "output", mutate: func(value *Manifest) { value.Output.ReferenceProfile = "other-reference.v1" }},
		{name: "job id", mutate: func(value *Manifest) { value.Job.ID = "other-job" }},
		{name: "idempotency", mutate: func(value *Manifest) { value.Job.IdempotencyKey = "other-attempt" }},
		{name: "timeline", mutate: func(value *Manifest) { value.Job.TimelineRange.Start = videoedit.RationalTime{Value: 1, Rate: 1000} }},
		{name: "duration", mutate: func(value *Manifest) {
			value.Duration = videoedit.RationalTime{Value: 3, Rate: 1}
			value.Job.TimelineRange.Duration = value.Duration
		}},
		{name: "artifact", mutate: func(value *Manifest) {
			value.Artifact.URI = "file:///controlled/other.mp4"
			value.Job.ArtifactURI = value.Artifact.URI
		}},
		{name: "decoded layout", mutate: func(value *Manifest) { value.DecodedAudio.Domain = "pcm_s16le.1hz.1ch.v1" }},
		{name: "decoded range", mutate: func(value *Manifest) {
			value.DecodedVideo.Range.First = 1
			value.DecodedVideo.Chunks[0].First = 1
			value.DecodedVideo.Chunks[1].First = 3
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, draft := evidenceBindingFixture(t)
			manifest, err := FreezeForRender(request, draft)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&manifest)
			forged, freezeErr := freeze(manifest)
			if freezeErr != nil {
				return
			}
			if err := ValidateAgainst(forged, request, nil); !errors.Is(err, ErrEvidenceInvalid) {
				t.Fatalf("error = %v, want ErrEvidenceInvalid", err)
			}
		})
	}
}

func TestEvidenceBindsExactJobAndEquivalentRationalDuration(t *testing.T) {
	requestA, draft := evidenceBindingFixture(t)
	requestA.Job.TimelineRange = videoedit.TimeRange{
		Start: videoedit.RationalTime{Value: 1, Rate: 1000}, Duration: videoedit.RationalTime{Value: 1, Rate: 1},
	}
	requestA.Job.FrameRange, _ = videoedit.CompileFrameTickRange(requestA.Job.TimelineRange, requestA.Plan.Target.FrameClock)
	requestA.Job.SampleRange, _ = videoedit.CompileSampleTickRange(requestA.Job.TimelineRange, requestA.Plan.Target.SampleClock)
	setDraftRanges(&draft, requestA.Job.FrameRange, requestA.Job.SampleRange)
	manifest, err := FreezeForRender(requestA, draft)
	if err != nil {
		t.Fatal(err)
	}

	requestB := requestA
	requestB.Job.ID = "job-2"
	requestB.Job.IdempotencyKey = "job-2-attempt-1"
	requestB.Job.TimelineRange.Start = videoedit.RationalTime{Value: 2, Rate: 1000}
	requestB.Job.FrameRange, _ = videoedit.CompileFrameTickRange(requestB.Job.TimelineRange, requestB.Plan.Target.FrameClock)
	requestB.Job.SampleRange, _ = videoedit.CompileSampleTickRange(requestB.Job.TimelineRange, requestB.Plan.Target.SampleClock)
	if requestA.Job.FrameRange != requestB.Job.FrameRange || requestA.Job.SampleRange != requestB.Job.SampleRange {
		t.Fatalf("test requires equal tick ranges: A=%+v/%+v B=%+v/%+v", requestA.Job.FrameRange, requestA.Job.SampleRange, requestB.Job.FrameRange, requestB.Job.SampleRange)
	}
	if err := ValidateAgainst(manifest, requestB, nil); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("cross-job replay error = %v", err)
	}

	normalizedRequest, normalizedDraft := evidenceBindingFixture(t)
	normalizedRequest.Job.TimelineRange.Duration = videoedit.RationalTime{Value: 2, Rate: 2}
	normalizedRequest.Job.FrameRange, _ = videoedit.CompileFrameTickRange(normalizedRequest.Job.TimelineRange, normalizedRequest.Plan.Target.FrameClock)
	normalizedRequest.Job.SampleRange, _ = videoedit.CompileSampleTickRange(normalizedRequest.Job.TimelineRange, normalizedRequest.Plan.Target.SampleClock)
	setDraftRanges(&normalizedDraft, normalizedRequest.Job.FrameRange, normalizedRequest.Job.SampleRange)
	normalized, err := FreezeForRender(normalizedRequest, normalizedDraft)
	if err != nil {
		t.Fatalf("equivalent rational duration rejected: %v", err)
	}
	if normalized.Duration != (videoedit.RationalTime{Value: 1, Rate: 1}) {
		t.Fatalf("duration not normalized: %+v", normalized.Duration)
	}
}

func setDraftRanges(draft *Manifest, frames, samples videoedit.TickRange) {
	draft.DecodedVideo.Range = frames
	draft.DecodedVideo.Chunks = []DecodedChunk{{First: frames.First, Count: frames.Count, ContentHash: DigestDecodedBytes([]byte("video"))}}
	draft.DecodedAudio.Range = samples
	draft.DecodedAudio.Chunks = []DecodedChunk{{First: samples.First, Count: samples.Count, ContentHash: DigestDecodedBytes([]byte("audio"))}}
}

func evidenceBindingFixture(t *testing.T) (renderapi.RenderRequest, Manifest) {
	t.Helper()
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion,
		ID:            "document-1",
		Revision:      7,
		Timelines:     []videoedit.Timeline{{ID: "timeline-1"}},
	}
	requested := videoedit.TimeRange{
		Start:    videoedit.RationalTime{Value: 0, Rate: 1},
		Duration: videoedit.RationalTime{Value: 2, Rate: 1},
	}
	plan, diagnostics := videoedit.Resolve(document, requested, videoedit.RenderTarget{
		TimelineID: "timeline-1", Width: 320, Height: 180,
		FrameClock: videoedit.FrameClock{
			Origin:          videoedit.RationalTime{Value: 0, Rate: 1},
			FramesPerSecond: videoedit.RationalTime{Value: 2, Rate: 1},
		},
		SampleClock: videoedit.SampleClock{
			Origin: videoedit.RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 96,
		},
		Channels: 2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	execution, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: evidenceDigest('a'), EvaluatorBuild: evidenceDigest('b'),
		Backend: videoedit.BackendCapabilities{
			BackendID: "ffmpeg-hardcut", BuildFingerprint: evidenceDigest('d'), MaxWidth: 1920, MaxHeight: 1080,
		},
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: evidenceDigest('e'), DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.reference.cpu.v1", Format: "mp4", Width: 320, Height: 180,
			PixelFormat: "yuv420p", ColorSpace: "bt709", VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: 96, Channels: 2,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := renderapi.RenderRequest{
		Plan: plan, Manifest: execution,
		Job: renderapi.RenderJob{
			SchemaVersion: renderapi.RenderJobSchemaVersion, ID: "job-1", IdempotencyKey: "job-1-attempt-1",
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: execution.Fingerprint,
			TimelineRange: requested, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange,
			ArtifactURI: "file:///controlled/output.mp4",
		},
	}
	draft := evidenceTestDraft()
	draft.Artifact = ArtifactIdentity{ContentHash: evidenceDigest('f'), SizeBytes: 4096}
	draft.DecodedVideo = &DecodedSequence{
		Domain: renderapi.DecodedVideoProfileRGBA8V1, Unit: EvidenceFrames, UnitBytes: 320 * 180 * 4, Range: plan.FrameRange,
		Chunks: []DecodedChunk{
			{First: 0, Count: 2, ContentHash: DigestDecodedBytes([]byte("video-a"))},
			{First: 2, Count: 2, ContentHash: DigestDecodedBytes([]byte("video-b"))},
		},
	}
	draft.DecodedAudio = &DecodedSequence{
		Domain: renderapi.DecodedAudioProfilePCM16LEV1, Unit: EvidenceSamples, UnitBytes: 4, Range: plan.SampleRange,
		Chunks: []DecodedChunk{
			{First: 0, Count: 96, ContentHash: DigestDecodedBytes([]byte("audio-a"))},
			{First: 96, Count: 96, ContentHash: DigestDecodedBytes([]byte("audio-b"))},
		},
	}
	return request, draft
}
