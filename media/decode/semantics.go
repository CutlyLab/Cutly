package videodecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	VideoSemanticsSchemaVersion = 1
	VideoSemanticsProfileV1     = "cutly.media.video-semantics-scan.v1"
	MaxVideoSemanticsBytes      = 64 << 10
)

// VideoSemanticsObservations are facts that require a complete frame/side-data
// scan and cannot be inferred from ProbeManifest v1. Profile v1 accepts only a
// progressive stream with static color facts and a pure quarter-turn display
// matrix; flips, shear/perspective and dynamic color fail closed.
type VideoSemanticsObservations struct {
	ScanComplete       bool `json:"scan_complete"`
	ProgressiveOnly    bool `json:"progressive_only"`
	StaticColorOnly    bool `json:"static_color_only"`
	RotationOnlyMatrix bool `json:"rotation_only_matrix"`
	RotationDegrees    int  `json:"rotation_degrees"`
}

// VideoSemanticsEvidence freezes the complete-scan producer identity and its
// assertions against one probe/resource/stream. The scanner build and options
// fingerprints are part of cache causality just like the primary probe.
type VideoSemanticsEvidence struct {
	SchemaVersion             int                        `json:"schema_version"`
	Profile                   string                     `json:"profile"`
	ProbeFingerprint          string                     `json:"probe_fingerprint"`
	ResourceHash              string                     `json:"resource_hash"`
	StreamIndex               int                        `json:"stream_index"`
	ScannerImplementation     string                     `json:"scanner_implementation"`
	ScannerBuildFingerprint   string                     `json:"scanner_build_fingerprint"`
	ScannerOptionsFingerprint string                     `json:"scanner_options_fingerprint"`
	Observations              VideoSemanticsObservations `json:"observations"`
}

func NewVideoSemanticsEvidence(
	ctx context.Context,
	manifest probe.ProbeManifest,
	streamIndex int,
	implementation, buildFingerprint, optionsFingerprint string,
	observations VideoSemanticsObservations,
) (VideoSemanticsEvidence, string, error) {
	if ctx == nil {
		return VideoSemanticsEvidence{}, "", fmt.Errorf("%w: context is required", ErrInvalid)
	}
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		return VideoSemanticsEvidence{}, "", fmt.Errorf("%w: invalid probe manifest: %v", ErrInvalid, err)
	}
	evidence := VideoSemanticsEvidence{
		SchemaVersion: VideoSemanticsSchemaVersion, Profile: VideoSemanticsProfileV1,
		ProbeFingerprint: probeFingerprint, ResourceHash: manifest.Resource.ContentHash, StreamIndex: streamIndex,
		ScannerImplementation: implementation, ScannerBuildFingerprint: buildFingerprint,
		ScannerOptionsFingerprint: optionsFingerprint, Observations: observations,
	}
	fingerprint, err := VideoSemanticsEvidenceFingerprint(ctx, manifest, evidence)
	if err != nil {
		return VideoSemanticsEvidence{}, "", err
	}
	return evidence, fingerprint, nil
}

func VideoSemanticsEvidenceFingerprint(ctx context.Context, manifest probe.ProbeManifest, evidence VideoSemanticsEvidence) (string, error) {
	if err := ValidateVideoSemanticsEvidence(ctx, manifest, evidence); err != nil {
		return "", err
	}
	canonical, err := canonicaljson.Marshal(evidence)
	if err != nil {
		return "", fmt.Errorf("%w: semantic evidence encoding: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func CanonicalVideoSemanticsEvidence(ctx context.Context, manifest probe.ProbeManifest, evidence VideoSemanticsEvidence) ([]byte, error) {
	if err := ValidateVideoSemanticsEvidence(ctx, manifest, evidence); err != nil {
		return nil, err
	}
	canonical, err := canonicaljson.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("%w: semantic evidence encoding: %v", ErrInvalid, err)
	}
	if len(canonical) > MaxVideoSemanticsBytes {
		return nil, fmt.Errorf("%w: semantic evidence bytes exceed %d", ErrResourceLimit, MaxVideoSemanticsBytes)
	}
	return canonical, nil
}

func DecodeVideoSemanticsEvidence(ctx context.Context, manifest probe.ProbeManifest, raw []byte) (VideoSemanticsEvidence, string, error) {
	if ctx == nil {
		return VideoSemanticsEvidence{}, "", fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return VideoSemanticsEvidence{}, "", err
	}
	if len(raw) == 0 || len(raw) > MaxVideoSemanticsBytes {
		return VideoSemanticsEvidence{}, "", fmt.Errorf("%w: semantic evidence byte length", ErrResourceLimit)
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = MaxVideoSemanticsBytes
	limits.MaxStringBytes = MaxVideoSemanticsBytes
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil {
		return VideoSemanticsEvidence{}, "", fmt.Errorf("%w: semantic evidence canonical JSON: %v", ErrInvalid, err)
	}
	var evidence VideoSemanticsEvidence
	if err := wirejson.Decode(canonical, &evidence); err != nil {
		return VideoSemanticsEvidence{}, "", fmt.Errorf("%w: semantic evidence decode: %v", ErrInvalid, err)
	}
	fingerprint, err := VideoSemanticsEvidenceFingerprint(ctx, manifest, evidence)
	if err != nil {
		return VideoSemanticsEvidence{}, "", err
	}
	return evidence, fingerprint, nil
}

func ValidateVideoSemanticsEvidence(ctx context.Context, manifest probe.ProbeManifest, evidence VideoSemanticsEvidence) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if evidence.SchemaVersion != VideoSemanticsSchemaVersion || evidence.Profile != VideoSemanticsProfileV1 ||
		!fingerprintPattern.MatchString(evidence.ProbeFingerprint) || !fingerprintPattern.MatchString(evidence.ResourceHash) ||
		evidence.StreamIndex < 0 || !canonicalScannerImplementation(evidence.ScannerImplementation) ||
		!fingerprintPattern.MatchString(evidence.ScannerBuildFingerprint) || !fingerprintPattern.MatchString(evidence.ScannerOptionsFingerprint) {
		return fmt.Errorf("%w: invalid video semantic evidence header", ErrInvalid)
	}
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil || evidence.ProbeFingerprint != probeFingerprint || evidence.ResourceHash != manifest.Resource.ContentHash {
		return fmt.Errorf("%w: semantic evidence probe/resource binding", ErrIndexMismatch)
	}
	var stream *probe.Stream
	for index := range manifest.Streams {
		if manifest.Streams[index].Index == evidence.StreamIndex {
			stream = &manifest.Streams[index]
			break
		}
	}
	if stream == nil || stream.Kind != probe.StreamKindVideo || stream.Video == nil {
		return fmt.Errorf("%w: semantic evidence video stream", ErrIndexMismatch)
	}
	observations := evidence.Observations
	if !observations.ScanComplete || !observations.ProgressiveOnly || !observations.StaticColorOnly ||
		!observations.RotationOnlyMatrix || observations.RotationDegrees != stream.Video.RotationDegrees {
		return fmt.Errorf("%w: profile v1 requires complete progressive/static-color/rotation-only evidence", ErrUnsupported)
	}
	return ctx.Err()
}

func canonicalScannerImplementation(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}
