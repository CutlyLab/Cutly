// Package evidence defines Cutly's deterministic decoded-domain render evidence contract.
//
// Evidence intentionally does not promise bit-exact encoded containers. It records the artifact
// hash for integrity while repeatability is assessed from canonical decoded frames and PCM.
package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const SchemaVersion = 1

var (
	ErrEvidenceInvalid             = errors.New("evidence: manifest is invalid")
	ErrEvidenceFingerprintMismatch = errors.New("evidence: manifest fingerprint mismatch")
)

var capabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.v[1-9][0-9]*$`)

type DocumentIdentity struct {
	ID       videoedit.ID       `json:"id"`
	Revision videoedit.Revision `json:"revision"`
	Hash     string             `json:"hash"`
}

type BackendIdentity struct {
	ID               string `json:"id"`
	BuildFingerprint string `json:"build_fingerprint"`
}

type RuntimeIdentity struct {
	FFmpegBuild    string `json:"ffmpeg_build"`
	DecoderProfile string `json:"decoder_profile"`
	EncoderProfile string `json:"encoder_profile"`
	ColorProfile   string `json:"color_profile"`
}

// OutputProfile binds both encoded output and the pre-render-selected decoded evidence layouts.
type OutputProfile struct {
	ReferenceProfile    string `json:"reference_profile"`
	Format              string `json:"format"`
	Width               int    `json:"width"`
	Height              int    `json:"height"`
	PixelFormat         string `json:"pixel_format"`
	ColorSpace          string `json:"color_space"`
	VideoCodec          string `json:"video_codec"`
	AudioCodec          string `json:"audio_codec"`
	DecodedVideoProfile string `json:"decoded_video_profile"`
	DecodedAudioProfile string `json:"decoded_audio_profile"`
	AudioSampleFormat   string `json:"audio_sample_format"`
	SampleRate          int64  `json:"sample_rate"`
	Channels            int    `json:"channels"`
}

// JobIdentity binds evidence to one idempotent scheduling request, not merely to a plan and tick
// ranges that another job could happen to share.
type JobIdentity struct {
	SchemaVersion  int                 `json:"schema_version"`
	ID             string              `json:"id"`
	IdempotencyKey string              `json:"idempotency_key"`
	TimelineRange  videoedit.TimeRange `json:"timeline_range"`
	FrameRange     videoedit.TickRange `json:"frame_range"`
	SampleRange    videoedit.TickRange `json:"sample_range"`
	ArtifactURI    string              `json:"artifact_uri"`
}

type ArtifactIdentity struct {
	URI         string `json:"uri"`
	ContentHash string `json:"content_hash"`
	SizeBytes   int64  `json:"size_bytes"`
}

type Notice struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type Fallback struct {
	Capability videoedit.CapabilityID `json:"capability"`
	Selected   string                 `json:"selected"`
	Reason     string                 `json:"reason"`
}

type CacheEvidence struct {
	Hit bool   `json:"hit"`
	Key string `json:"key"`
}

type StageTiming struct {
	Stage          string `json:"stage"`
	DurationMicros int64  `json:"duration_micros"`
}

// Manifest is an immutable audit record for one completed artifact.
type Manifest struct {
	SchemaVersion                int                      `json:"schema_version"`
	Document                     DocumentIdentity         `json:"document"`
	PlanFingerprint              string                   `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                   `json:"execution_manifest_fingerprint"`
	Backend                      BackendIdentity          `json:"backend"`
	Runtime                      RuntimeIdentity          `json:"runtime"`
	Output                       OutputProfile            `json:"output"`
	Job                          JobIdentity              `json:"job"`
	Artifact                     ArtifactIdentity         `json:"artifact"`
	Duration                     videoedit.RationalTime   `json:"duration"`
	FrameCount                   int64                    `json:"frame_count"`
	SampleCount                  int64                    `json:"sample_count"`
	DecodedVideo                 *DecodedSequence         `json:"decoded_video,omitempty"`
	DecodedAudio                 *DecodedSequence         `json:"decoded_audio,omitempty"`
	Warnings                     []Notice                 `json:"warnings,omitempty"`
	Fallbacks                    []Fallback               `json:"fallbacks,omitempty"`
	DegradedCapabilities         []videoedit.CapabilityID `json:"degraded_capabilities,omitempty"`
	Cache                        CacheEvidence            `json:"cache"`
	StageTimings                 []StageTiming            `json:"stage_timings,omitempty"`
	PeakRSSBytes                 int64                    `json:"peak_rss_bytes"`
	Fingerprint                  string                   `json:"fingerprint"`
}

// freeze performs the internal canonicalization pass. Production callers use FreezeForRender so
// the internally valid evidence is also bound to the immutable render request.
func freeze(draft Manifest) (Manifest, error) {
	draft = cloneManifest(draft)
	draft.SchemaVersion = SchemaVersion
	draft.Fingerprint = ""
	canonicalize(&draft)
	for _, sequence := range []*DecodedSequence{draft.DecodedVideo, draft.DecodedAudio} {
		if sequence == nil {
			continue
		}
		want, err := ComputeDecodedRoot(*sequence)
		if err != nil {
			return Manifest{}, err
		}
		if sequence.RootHash != "" && sequence.RootHash != want {
			return Manifest{}, fmt.Errorf("%w: supplied decoded root does not match chunks", ErrEvidenceInvalid)
		}
		sequence.RootHash = want
	}
	if err := validate(draft); err != nil {
		return Manifest{}, err
	}
	fingerprint, err := manifestFingerprint(draft)
	if err != nil {
		return Manifest{}, err
	}
	draft.Fingerprint = fingerprint
	return draft, nil
}

// Decode strictly decodes a canonical frozen manifest and verifies all decoded roots and its
// manifest fingerprint.
func Decode(raw []byte) (Manifest, error) {
	var manifest Manifest
	if err := wirejson.Decode(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %v", ErrEvidenceInvalid, err)
	}
	if err := validate(manifest); err != nil {
		return Manifest{}, err
	}
	canonical := cloneManifest(manifest)
	canonical.Fingerprint = ""
	canonicalize(&canonical)
	if !manifestCanonical(manifest, canonical) {
		return Manifest{}, fmt.Errorf("%w: set, timing and chunk fields must be canonical", ErrEvidenceInvalid)
	}
	want, err := manifestFingerprint(canonical)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Fingerprint != want {
		return Manifest{}, fmt.Errorf("%w: got %q, want %q", ErrEvidenceFingerprintMismatch, manifest.Fingerprint, want)
	}
	return manifest, nil
}

func validate(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrEvidenceInvalid, manifest.SchemaVersion)
	}
	if manifest.Document.ID == "" || !validDigest(manifest.Document.Hash) {
		return fmt.Errorf("%w: document identity is incomplete", ErrEvidenceInvalid)
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "plan_fingerprint", value: manifest.PlanFingerprint},
		{name: "execution_manifest_fingerprint", value: manifest.ExecutionManifestFingerprint},
		{name: "backend.build_fingerprint", value: manifest.Backend.BuildFingerprint},
		{name: "runtime.ffmpeg_build", value: manifest.Runtime.FFmpegBuild},
		{name: "artifact.content_hash", value: manifest.Artifact.ContentHash},
	} {
		if !validDigest(item.value) {
			return fmt.Errorf("%w: %s must be a canonical sha256 digest", ErrEvidenceInvalid, item.name)
		}
	}
	if strings.TrimSpace(manifest.Backend.ID) == "" || strings.TrimSpace(manifest.Artifact.URI) == "" || manifest.Artifact.SizeBytes <= 0 {
		return fmt.Errorf("%w: backend and artifact identities are incomplete", ErrEvidenceInvalid)
	}
	for _, value := range []string{
		manifest.Runtime.DecoderProfile, manifest.Runtime.EncoderProfile, manifest.Runtime.ColorProfile,
		manifest.Output.ReferenceProfile, manifest.Output.Format, manifest.Output.PixelFormat, manifest.Output.ColorSpace,
		manifest.Output.VideoCodec, manifest.Output.AudioCodec, manifest.Output.DecodedVideoProfile,
		manifest.Output.DecodedAudioProfile, manifest.Output.AudioSampleFormat,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: runtime and output profile fields are required", ErrEvidenceInvalid)
		}
	}
	if manifest.Output.Width <= 0 || manifest.Output.Height <= 0 || manifest.Output.SampleRate <= 0 || manifest.Output.Channels <= 0 {
		return fmt.Errorf("%w: output dimensions, sample rate and channels must be positive", ErrEvidenceInvalid)
	}
	if manifest.Output.DecodedVideoProfile != renderapi.DecodedVideoProfileRGBA8V1 ||
		manifest.Output.DecodedAudioProfile != renderapi.DecodedAudioProfilePCM16LEV1 ||
		manifest.Output.AudioSampleFormat != "s16le" {
		return fmt.Errorf("%w: unsupported decoded output layout", ErrEvidenceInvalid)
	}
	if manifest.Job.SchemaVersion != renderapi.RenderJobSchemaVersion || strings.TrimSpace(manifest.Job.ID) == "" ||
		strings.TrimSpace(manifest.Job.IdempotencyKey) == "" || strings.TrimSpace(manifest.Job.ArtifactURI) == "" {
		return fmt.Errorf("%w: render job identity is incomplete", ErrEvidenceInvalid)
	}
	if err := manifest.Job.TimelineRange.Validate(); err != nil || manifest.Job.TimelineRange.Duration.Value <= 0 {
		return fmt.Errorf("%w: render job timeline range is invalid", ErrEvidenceInvalid)
	}
	if err := manifest.Job.FrameRange.Validate(); err != nil {
		return fmt.Errorf("%w: render job frame range: %v", ErrEvidenceInvalid, err)
	}
	if err := manifest.Job.SampleRange.Validate(); err != nil {
		return fmt.Errorf("%w: render job sample range: %v", ErrEvidenceInvalid, err)
	}
	if _, err := renderapi.EvidenceSidecarURI(manifest.Job.ArtifactURI); err != nil {
		return fmt.Errorf("%w: render job artifact URI: %v", ErrEvidenceInvalid, err)
	}
	if manifest.Job.ArtifactURI != manifest.Artifact.URI {
		return fmt.Errorf("%w: render job and artifact URI differ", ErrEvidenceInvalid)
	}
	if err := manifest.Duration.Validate(); err != nil || manifest.Duration.Value <= 0 {
		return fmt.Errorf("%w: duration must be valid and positive", ErrEvidenceInvalid)
	}
	if manifest.FrameCount < 0 || manifest.SampleCount < 0 {
		return fmt.Errorf("%w: frame/sample counts cannot be negative", ErrEvidenceInvalid)
	}
	if !rationalEquivalent(manifest.Duration, manifest.Job.TimelineRange.Duration) ||
		manifest.FrameCount != manifest.Job.FrameRange.Count || manifest.SampleCount != manifest.Job.SampleRange.Count {
		return fmt.Errorf("%w: duration or counts do not match render job", ErrEvidenceInvalid)
	}
	if err := validateSequenceCount(manifest.DecodedVideo, EvidenceFrames, manifest.Job.FrameRange, manifest.Output); err != nil {
		return err
	}
	if err := validateSequenceCount(manifest.DecodedAudio, EvidenceSamples, manifest.Job.SampleRange, manifest.Output); err != nil {
		return err
	}
	for index, notice := range manifest.Warnings {
		if strings.TrimSpace(notice.Code) == "" || strings.TrimSpace(notice.Message) == "" {
			return fmt.Errorf("%w: warning %d is incomplete", ErrEvidenceInvalid, index)
		}
		if index > 0 && noticeKey(manifest.Warnings[index-1]) == noticeKey(notice) {
			return fmt.Errorf("%w: duplicate warning %q", ErrEvidenceInvalid, notice.Code)
		}
	}
	for index, fallback := range manifest.Fallbacks {
		if !capabilityPattern.MatchString(string(fallback.Capability)) || strings.TrimSpace(fallback.Selected) == "" || strings.TrimSpace(fallback.Reason) == "" {
			return fmt.Errorf("%w: fallback %d is incomplete", ErrEvidenceInvalid, index)
		}
		if index > 0 && manifest.Fallbacks[index-1].Capability == fallback.Capability {
			return fmt.Errorf("%w: duplicate fallback %q", ErrEvidenceInvalid, fallback.Capability)
		}
	}
	for index, capability := range manifest.DegradedCapabilities {
		if !capabilityPattern.MatchString(string(capability)) || (index > 0 && manifest.DegradedCapabilities[index-1] == capability) {
			return fmt.Errorf("%w: degraded capability %d is empty or duplicated", ErrEvidenceInvalid, index)
		}
	}
	if strings.TrimSpace(manifest.Cache.Key) == "" {
		return fmt.Errorf("%w: cache key is required", ErrEvidenceInvalid)
	}
	for index, timing := range manifest.StageTimings {
		if strings.TrimSpace(timing.Stage) == "" || timing.DurationMicros < 0 {
			return fmt.Errorf("%w: stage timing %d is invalid", ErrEvidenceInvalid, index)
		}
		if index > 0 && manifest.StageTimings[index-1].Stage == timing.Stage {
			return fmt.Errorf("%w: duplicate stage timing %q", ErrEvidenceInvalid, timing.Stage)
		}
	}
	if manifest.PeakRSSBytes < 0 {
		return fmt.Errorf("%w: peak RSS cannot be negative", ErrEvidenceInvalid)
	}
	if manifest.Fingerprint != "" && !validDigest(manifest.Fingerprint) {
		return fmt.Errorf("%w: fingerprint must be a canonical sha256 digest", ErrEvidenceInvalid)
	}
	return nil
}

func validateSequenceCount(sequence *DecodedSequence, unit EvidenceUnit, expected videoedit.TickRange, output OutputProfile) error {
	if expected.Count == 0 {
		if sequence != nil {
			return fmt.Errorf("%w: decoded %s must be absent when count is zero", ErrEvidenceInvalid, unit)
		}
		return nil
	}
	if sequence == nil {
		return fmt.Errorf("%w: decoded %s is required when count is positive", ErrEvidenceInvalid, unit)
	}
	if sequence.Unit != unit || sequence.Range != expected {
		return fmt.Errorf("%w: decoded %s range does not match render job", ErrEvidenceInvalid, unit)
	}
	domain, unitBytes, err := expectedDecodedLayout(output, unit)
	if err != nil {
		return err
	}
	if sequence.Domain != domain || sequence.UnitBytes != unitBytes {
		return fmt.Errorf("%w: decoded %s layout does not match frozen output profile", ErrEvidenceInvalid, unit)
	}
	return validateDecodedSequence(*sequence)
}

func expectedDecodedLayout(output OutputProfile, unit EvidenceUnit) (string, int64, error) {
	switch unit {
	case EvidenceFrames:
		if output.DecodedVideoProfile != renderapi.DecodedVideoProfileRGBA8V1 || output.Width <= 0 || output.Height <= 0 ||
			int64(output.Width) > math.MaxInt64/int64(output.Height) || int64(output.Width)*int64(output.Height) > math.MaxInt64/4 {
			return "", 0, fmt.Errorf("%w: decoded video layout is invalid", ErrEvidenceInvalid)
		}
		return output.DecodedVideoProfile, int64(output.Width) * int64(output.Height) * 4, nil
	case EvidenceSamples:
		if output.DecodedAudioProfile != renderapi.DecodedAudioProfilePCM16LEV1 || output.AudioSampleFormat != "s16le" ||
			output.Channels <= 0 || int64(output.Channels) > math.MaxInt64/2 {
			return "", 0, fmt.Errorf("%w: decoded audio layout is invalid", ErrEvidenceInvalid)
		}
		return output.DecodedAudioProfile, int64(output.Channels) * 2, nil
	default:
		return "", 0, fmt.Errorf("%w: decoded evidence unit %q", ErrEvidenceInvalid, unit)
	}
}

func rationalEquivalent(left, right videoedit.RationalTime) bool {
	comparison, err := left.Compare(right)
	return err == nil && comparison == 0
}

func timeRangeEquivalent(left, right videoedit.TimeRange) bool {
	return rationalEquivalent(left.Start, right.Start) && rationalEquivalent(left.Duration, right.Duration)
}

func canonicalize(manifest *Manifest) {
	if normalized, err := manifest.Duration.Normalize(); err == nil {
		manifest.Duration = normalized
	}
	if normalized, err := manifest.Job.TimelineRange.Start.Normalize(); err == nil {
		manifest.Job.TimelineRange.Start = normalized
	}
	if normalized, err := manifest.Job.TimelineRange.Duration.Normalize(); err == nil {
		manifest.Job.TimelineRange.Duration = normalized
	}
	canonicalizeDecodedSequence(manifest.DecodedVideo)
	canonicalizeDecodedSequence(manifest.DecodedAudio)
	sort.Slice(manifest.Warnings, func(i, j int) bool { return noticeKey(manifest.Warnings[i]) < noticeKey(manifest.Warnings[j]) })
	sort.Slice(manifest.Fallbacks, func(i, j int) bool { return manifest.Fallbacks[i].Capability < manifest.Fallbacks[j].Capability })
	sort.Slice(manifest.DegradedCapabilities, func(i, j int) bool { return manifest.DegradedCapabilities[i] < manifest.DegradedCapabilities[j] })
	sort.Slice(manifest.StageTimings, func(i, j int) bool { return manifest.StageTimings[i].Stage < manifest.StageTimings[j].Stage })
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.Warnings = append([]Notice(nil), manifest.Warnings...)
	clone.Fallbacks = append([]Fallback(nil), manifest.Fallbacks...)
	clone.DegradedCapabilities = append([]videoedit.CapabilityID(nil), manifest.DegradedCapabilities...)
	clone.StageTimings = append([]StageTiming(nil), manifest.StageTimings...)
	if manifest.DecodedVideo != nil {
		value := *manifest.DecodedVideo
		value.Chunks = append([]DecodedChunk(nil), manifest.DecodedVideo.Chunks...)
		clone.DecodedVideo = &value
	}
	if manifest.DecodedAudio != nil {
		value := *manifest.DecodedAudio
		value.Chunks = append([]DecodedChunk(nil), manifest.DecodedAudio.Chunks...)
		clone.DecodedAudio = &value
	}
	return clone
}

func manifestCanonical(got, canonical Manifest) bool {
	got.Fingerprint = ""
	left, leftErr := videoedit.CanonicalJSON(got)
	right, rightErr := videoedit.CanonicalJSON(canonical)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func manifestFingerprint(manifest Manifest) (string, error) {
	manifest.Fingerprint = ""
	canonical, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		return "", fmt.Errorf("%w: canonical JSON: %v", ErrEvidenceInvalid, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func noticeKey(value Notice) string {
	return value.Code + "\x00" + value.Path + "\x00" + value.Message
}
