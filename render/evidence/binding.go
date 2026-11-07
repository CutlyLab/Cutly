package evidence

import (
	"encoding/json"
	"fmt"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// FreezeForRender canonicalizes evidence and then binds every derivable fact to a verified render
// request. This is the only public evidence constructor; an internally self-consistent manifest is
// not sufficient proof of which plan, runtime or job produced an artifact.
func FreezeForRender(request renderapi.RenderRequest, draft Manifest) (Manifest, error) {
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		return Manifest{}, fmt.Errorf("%w: render request: %v", ErrEvidenceInvalid, err)
	}
	plan, execution, job := request.Plan, request.Manifest, request.Job
	draft.Document = DocumentIdentity{ID: plan.DocumentID, Revision: plan.Revision, Hash: plan.DocumentHash}
	draft.PlanFingerprint = plan.Fingerprint
	draft.ExecutionManifestFingerprint = execution.Fingerprint
	draft.Backend = BackendIdentity{ID: execution.Backend.BackendID, BuildFingerprint: execution.Backend.BuildFingerprint}
	draft.Runtime = RuntimeIdentity{
		FFmpegBuild: execution.MediaRuntime.FFmpegBuild, DecoderProfile: execution.MediaRuntime.DecoderProfile,
		EncoderProfile: execution.MediaRuntime.EncoderProfile, ColorProfile: execution.MediaRuntime.ColorProfile,
	}
	draft.Output = OutputProfile{
		ReferenceProfile: execution.Output.ReferenceProfile, Format: execution.Output.Format,
		Width: execution.Output.Width, Height: execution.Output.Height, PixelFormat: execution.Output.PixelFormat,
		ColorSpace: execution.Output.ColorSpace, VideoCodec: execution.Output.VideoCodec, AudioCodec: execution.Output.AudioCodec,
		DecodedVideoProfile: execution.Output.DecodedVideoProfile, DecodedAudioProfile: execution.Output.DecodedAudioProfile,
		AudioSampleFormat: "s16le", SampleRate: execution.Output.SampleRate, Channels: execution.Output.Channels,
	}
	draft.Job = JobIdentity{
		SchemaVersion: job.SchemaVersion, ID: job.ID, IdempotencyKey: job.IdempotencyKey,
		TimelineRange: job.TimelineRange, FrameRange: job.FrameRange, SampleRange: job.SampleRange, ArtifactURI: job.ArtifactURI,
	}
	draft.Artifact.URI = job.ArtifactURI
	draft.Duration = job.TimelineRange.Duration
	draft.FrameCount = job.FrameRange.Count
	draft.SampleCount = job.SampleRange.Count
	frozen, err := freeze(draft)
	if err != nil {
		return Manifest{}, err
	}
	if err := ValidateAgainst(frozen, request, nil); err != nil {
		return Manifest{}, err
	}
	return frozen, nil
}

// ValidateAgainst verifies that evidence belongs to request and, when provided, to the published
// RenderArtifact. Decode followed by ValidateAgainst is the consumer-side trust boundary.
func ValidateAgainst(manifest Manifest, request renderapi.RenderRequest, artifact *renderapi.RenderArtifact) error {
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		return fmt.Errorf("%w: render request: %v", ErrEvidenceInvalid, err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("%w: encode evidence: %v", ErrEvidenceInvalid, err)
	}
	verified, err := Decode(raw)
	if err != nil {
		return fmt.Errorf("%w: evidence: %v", ErrEvidenceInvalid, err)
	}
	manifest = verified
	plan, execution, job := request.Plan, request.Manifest, request.Job
	if manifest.Document.ID != plan.DocumentID || manifest.Document.Revision != plan.Revision || manifest.Document.Hash != plan.DocumentHash {
		return fmt.Errorf("%w: document identity does not match render plan", ErrEvidenceInvalid)
	}
	if manifest.PlanFingerprint != plan.Fingerprint || manifest.ExecutionManifestFingerprint != execution.Fingerprint {
		return fmt.Errorf("%w: plan or execution fingerprint does not match request", ErrEvidenceInvalid)
	}
	if manifest.Backend.ID != execution.Backend.BackendID || manifest.Backend.BuildFingerprint != execution.Backend.BuildFingerprint {
		return fmt.Errorf("%w: backend identity does not match execution manifest", ErrEvidenceInvalid)
	}
	if manifest.Runtime.FFmpegBuild != execution.MediaRuntime.FFmpegBuild ||
		manifest.Runtime.DecoderProfile != execution.MediaRuntime.DecoderProfile ||
		manifest.Runtime.EncoderProfile != execution.MediaRuntime.EncoderProfile ||
		manifest.Runtime.ColorProfile != execution.MediaRuntime.ColorProfile {
		return fmt.Errorf("%w: media runtime does not match execution manifest", ErrEvidenceInvalid)
	}
	if manifest.Output.ReferenceProfile != execution.Output.ReferenceProfile ||
		manifest.Output.Format != execution.Output.Format ||
		manifest.Output.Width != execution.Output.Width ||
		manifest.Output.Height != execution.Output.Height ||
		manifest.Output.PixelFormat != execution.Output.PixelFormat ||
		manifest.Output.ColorSpace != execution.Output.ColorSpace ||
		manifest.Output.VideoCodec != execution.Output.VideoCodec ||
		manifest.Output.AudioCodec != execution.Output.AudioCodec ||
		manifest.Output.DecodedVideoProfile != execution.Output.DecodedVideoProfile ||
		manifest.Output.DecodedAudioProfile != execution.Output.DecodedAudioProfile ||
		manifest.Output.AudioSampleFormat != "s16le" ||
		manifest.Output.SampleRate != execution.Output.SampleRate ||
		manifest.Output.Channels != execution.Output.Channels {
		return fmt.Errorf("%w: output profile does not match execution manifest", ErrEvidenceInvalid)
	}
	if manifest.Job.SchemaVersion != job.SchemaVersion || manifest.Job.ID != job.ID || manifest.Job.IdempotencyKey != job.IdempotencyKey ||
		!timeRangeEquivalent(manifest.Job.TimelineRange, job.TimelineRange) || manifest.Job.FrameRange != job.FrameRange ||
		manifest.Job.SampleRange != job.SampleRange || manifest.Job.ArtifactURI != job.ArtifactURI {
		return fmt.Errorf("%w: render job identity does not match request", ErrEvidenceInvalid)
	}
	if manifest.Artifact.URI != job.ArtifactURI {
		return fmt.Errorf("%w: artifact URI does not match render job", ErrEvidenceInvalid)
	}
	if !rationalEquivalent(manifest.Duration, job.TimelineRange.Duration) || manifest.FrameCount != job.FrameRange.Count || manifest.SampleCount != job.SampleRange.Count {
		return fmt.Errorf("%w: duration or decoded counts do not match render job", ErrEvidenceInvalid)
	}
	if err := validateBoundSequence(manifest.DecodedVideo, EvidenceFrames, job.FrameRange, manifest.Output); err != nil {
		return err
	}
	if err := validateBoundSequence(manifest.DecodedAudio, EvidenceSamples, job.SampleRange, manifest.Output); err != nil {
		return err
	}
	if artifact != nil {
		if artifact.URI != manifest.Artifact.URI || artifact.ContentHash != manifest.Artifact.ContentHash || artifact.SizeBytes != manifest.Artifact.SizeBytes {
			return fmt.Errorf("%w: published artifact identity does not match evidence", ErrEvidenceInvalid)
		}
		if artifact.ExecutionManifestFingerprint != manifest.ExecutionManifestFingerprint || artifact.EvidenceFingerprint != manifest.Fingerprint {
			return fmt.Errorf("%w: published artifact fingerprints do not match evidence", ErrEvidenceInvalid)
		}
		expectedEvidenceURI, sidecarErr := renderapi.EvidenceSidecarURI(job.ArtifactURI)
		if sidecarErr != nil || artifact.EvidenceURI != expectedEvidenceURI {
			return fmt.Errorf("%w: published artifact evidence locator is not the authorized sidecar", ErrEvidenceInvalid)
		}
		if !rationalEquivalent(artifact.Duration, manifest.Duration) || artifact.FrameCount != manifest.FrameCount || artifact.SampleCount != manifest.SampleCount {
			return fmt.Errorf("%w: published artifact timing does not match evidence", ErrEvidenceInvalid)
		}
	}
	return nil
}

func validateBoundSequence(sequence *DecodedSequence, unit EvidenceUnit, expected videoedit.TickRange, output OutputProfile) error {
	if expected.Count == 0 {
		if sequence != nil {
			return fmt.Errorf("%w: zero-length %s work must not have decoded evidence", ErrEvidenceInvalid, unit)
		}
		return nil
	}
	if sequence == nil || sequence.Unit != unit || sequence.Range != expected {
		return fmt.Errorf("%w: decoded %s evidence does not match the job range", ErrEvidenceInvalid, unit)
	}
	domain, unitBytes, err := expectedDecodedLayout(output, unit)
	if err != nil {
		return err
	}
	if sequence.Domain != domain || sequence.UnitBytes != unitBytes {
		return fmt.Errorf("%w: decoded %s layout does not match frozen execution output", ErrEvidenceInvalid, unit)
	}
	return nil
}
