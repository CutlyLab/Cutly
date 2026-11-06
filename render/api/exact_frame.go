package renderapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const (
	ExactFrameRequestSchemaVersion = 1
	// ExactFramePNGProfileV1 freezes the PNG encoder contract selected by the M1 reference
	// backend. DecodedContentHash remains the D1 identity; encoded PNG bytes are additionally
	// hashed so publication can be verified without pretending every PNG encoder is equivalent.
	ExactFramePNGProfileV1 = "cutly.png.rgba8.ffmpeg.single-frame.v1"
)

var (
	ErrExactFrameRequestInvalid  = errors.New("render: exact frame request is invalid")
	ErrExactFrameArtifactInvalid = errors.New("render: exact frame artifact is invalid")
)

// ExactFrameRequest selects one composition-wide frame from an immutable plan and execution
// environment. ArtifactURI is a publication destination, never a semantic resource locator.
type ExactFrameRequest struct {
	SchemaVersion                int    `json:"schema_version"`
	ID                           string `json:"id"`
	IdempotencyKey               string `json:"idempotency_key"`
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
	CompositionFrameIndex        int64  `json:"composition_frame_index"`
	ArtifactURI                  string `json:"artifact_uri"`
}

// ExactFrameRenderRequest carries the immutable values needed by a backend. The standalone wire
// contract is ExactFrameRequest; Plan and Manifest are loaded by their fingerprints and verified
// again at the worker boundary.
type ExactFrameRenderRequest struct {
	Frame    ExactFrameRequest
	Plan     videoedit.RenderPlan
	Manifest ExecutionManifest
}

// ExactFrameArtifact binds the published PNG to both the semantic evaluator result and the
// frozen execution environment. DecodedContentHash is over one tightly packed, top-down RGBA8
// frame as named by DecodedVideoProfile.
type ExactFrameArtifact struct {
	URI                          string                    `json:"uri"`
	ContentHash                  string                    `json:"content_hash"`
	SizeBytes                    int64                     `json:"size_bytes"`
	DecodedContentHash           string                    `json:"decoded_content_hash"`
	PlanFingerprint              string                    `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                    `json:"execution_manifest_fingerprint"`
	BackendID                    string                    `json:"backend_id"`
	BackendBuildFingerprint      string                    `json:"backend_build_fingerprint"`
	ReferenceProfile             string                    `json:"reference_profile"`
	PNGEncodingProfile           string                    `json:"png_encoding_profile"`
	DecodedVideoProfile          string                    `json:"decoded_video_profile"`
	Width                        int                       `json:"width"`
	Height                       int                       `json:"height"`
	CompositionFrameIndex        int64                     `json:"composition_frame_index"`
	TimelineTime                 videoedit.RationalTime    `json:"timeline_time"`
	EvaluationFingerprint        string                    `json:"evaluation_fingerprint"`
	Evaluation                   videoedit.FrameEvaluation `json:"evaluation"`
}

// ExactFrameBackend is an optional extension implemented by renderers whose preview path can
// honor the same semantic evaluator and reference profile as export.
type ExactFrameBackend interface {
	RenderFrame(context.Context, ExactFrameRenderRequest, EventSink) (ExactFrameArtifact, error)
}

// DecodeExactFrameRequest applies the same strict wire rules as the other fingerprint-bound
// contracts: exact field names, no duplicates, no unknowns, integer tokens only, and one value.
func DecodeExactFrameRequest(raw []byte) (ExactFrameRequest, error) {
	var request ExactFrameRequest
	if err := wirejson.Decode(raw, &request); err != nil {
		return ExactFrameRequest{}, fmt.Errorf("%w: decode: %v", ErrExactFrameRequestInvalid, err)
	}
	if err := validateExactFrameRequest(request); err != nil {
		return ExactFrameRequest{}, err
	}
	return request, nil
}

// ValidateExactFrameRenderRequest verifies the plan, execution manifest, immutable identities,
// publication URI, and exact composition index before a backend opens any media.
func ValidateExactFrameRenderRequest(request ExactFrameRenderRequest) error {
	planRaw, err := json.Marshal(request.Plan)
	if err != nil {
		return fmt.Errorf("%w: encode plan: %v", ErrExactFrameRequestInvalid, err)
	}
	verifiedPlan, err := videoedit.DecodeRenderPlan(planRaw)
	if err != nil {
		return fmt.Errorf("%w: plan: %v", ErrExactFrameRequestInvalid, err)
	}
	manifestRaw, err := json.Marshal(request.Manifest)
	if err != nil {
		return fmt.Errorf("%w: encode execution manifest: %v", ErrExactFrameRequestInvalid, err)
	}
	verifiedManifest, err := DecodeExecutionManifest(manifestRaw)
	if err != nil {
		return fmt.Errorf("%w: execution manifest: %v", ErrExactFrameRequestInvalid, err)
	}
	if err := validateManifestAgainstPlan(verifiedPlan, verifiedManifest); err != nil {
		return fmt.Errorf("%w: execution manifest binding: %v", ErrExactFrameRequestInvalid, err)
	}
	if diagnostics := videoedit.CheckCapabilities(verifiedPlan, verifiedManifest.Backend); videoedit.HasErrorDiagnostics(diagnostics) {
		return fmt.Errorf("%w: backend preflight: %s at %s", ErrExactFrameRequestInvalid, diagnostics[0].Code, diagnostics[0].Path)
	}
	if err := validateExactFrameRequest(request.Frame); err != nil {
		return err
	}
	if request.Frame.PlanFingerprint != verifiedPlan.Fingerprint || request.Frame.PlanFingerprint != verifiedManifest.PlanFingerprint {
		return fmt.Errorf("%w: plan fingerprint mismatch", ErrExactFrameRequestInvalid)
	}
	if request.Frame.ExecutionManifestFingerprint != verifiedManifest.Fingerprint {
		return fmt.Errorf("%w: execution manifest fingerprint mismatch", ErrExactFrameRequestInvalid)
	}
	verified, err := videoedit.VerifyRenderPlan(verifiedPlan)
	if err != nil {
		return fmt.Errorf("%w: verify plan: %v", ErrExactFrameRequestInvalid, err)
	}
	if _, err := videoedit.EvaluateVerifiedFrame(verified, request.Frame.CompositionFrameIndex); err != nil {
		return fmt.Errorf("%w: frame index: %v", ErrExactFrameRequestInvalid, err)
	}
	return nil
}

// FrameEvaluationFingerprint returns the canonical semantic identity recorded alongside an exact
// frame. It excludes backend and PNG bytes by design.
func FrameEvaluationFingerprint(evaluation videoedit.FrameEvaluation) (string, error) {
	canonical, err := videoedit.CanonicalJSON(evaluation)
	if err != nil {
		return "", fmt.Errorf("%w: canonical evaluation: %v", ErrExactFrameArtifactInvalid, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ValidateExactFrameArtifact cross-checks every request-derived field and recomputes the
// evaluator identity. Callers still verify ContentHash and SizeBytes against the opened file.
func ValidateExactFrameArtifact(artifact ExactFrameArtifact, request ExactFrameRenderRequest) error {
	if err := ValidateExactFrameRenderRequest(request); err != nil {
		return fmt.Errorf("%w: request: %v", ErrExactFrameArtifactInvalid, err)
	}
	if artifact.URI != request.Frame.ArtifactURI || !validDigest(artifact.ContentHash) || artifact.SizeBytes <= 0 || !validDigest(artifact.DecodedContentHash) {
		return fmt.Errorf("%w: publication identity is incomplete", ErrExactFrameArtifactInvalid)
	}
	if artifact.PlanFingerprint != request.Plan.Fingerprint || artifact.ExecutionManifestFingerprint != request.Manifest.Fingerprint {
		return fmt.Errorf("%w: plan or execution identity mismatch", ErrExactFrameArtifactInvalid)
	}
	if artifact.BackendID != request.Manifest.Backend.BackendID || artifact.BackendBuildFingerprint != request.Manifest.Backend.BuildFingerprint {
		return fmt.Errorf("%w: backend identity mismatch", ErrExactFrameArtifactInvalid)
	}
	if artifact.ReferenceProfile != request.Manifest.Output.ReferenceProfile || artifact.PNGEncodingProfile != ExactFramePNGProfileV1 ||
		artifact.DecodedVideoProfile != request.Manifest.Output.DecodedVideoProfile {
		return fmt.Errorf("%w: reference or pixel-layout profile mismatch", ErrExactFrameArtifactInvalid)
	}
	if artifact.Width != request.Manifest.Output.Width || artifact.Height != request.Manifest.Output.Height ||
		artifact.CompositionFrameIndex != request.Frame.CompositionFrameIndex {
		return fmt.Errorf("%w: dimensions or frame index mismatch", ErrExactFrameArtifactInvalid)
	}
	verified, err := videoedit.VerifyRenderPlan(request.Plan)
	if err != nil {
		return fmt.Errorf("%w: verify plan: %v", ErrExactFrameArtifactInvalid, err)
	}
	evaluation, err := videoedit.EvaluateVerifiedFrame(verified, request.Frame.CompositionFrameIndex)
	if err != nil {
		return fmt.Errorf("%w: evaluate frame: %v", ErrExactFrameArtifactInvalid, err)
	}
	if artifact.TimelineTime != evaluation.TimelineTime || !reflect.DeepEqual(artifact.Evaluation, evaluation) {
		return fmt.Errorf("%w: semantic evaluation mismatch", ErrExactFrameArtifactInvalid)
	}
	fingerprint, err := FrameEvaluationFingerprint(evaluation)
	if err != nil || artifact.EvaluationFingerprint != fingerprint {
		return fmt.Errorf("%w: evaluation fingerprint mismatch", ErrExactFrameArtifactInvalid)
	}
	return nil
}

func validateExactFrameRequest(request ExactFrameRequest) error {
	if request.SchemaVersion != ExactFrameRequestSchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrExactFrameRequestInvalid, request.SchemaVersion)
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("%w: identity and idempotency key are required", ErrExactFrameRequestInvalid)
	}
	if !validDigest(request.PlanFingerprint) || !validDigest(request.ExecutionManifestFingerprint) {
		return fmt.Errorf("%w: plan and execution fingerprints must be canonical sha256 digests", ErrExactFrameRequestInvalid)
	}
	if err := validateArtifactURI(request.ArtifactURI); err != nil {
		return fmt.Errorf("%w: artifact URI: %v", ErrExactFrameRequestInvalid, err)
	}
	parsed, err := url.ParseRequestURI(request.ArtifactURI)
	if err != nil || filepath.Ext(parsed.Path) != ".png" || filepath.Base(parsed.Path) == ".png" {
		return fmt.Errorf("%w: artifact URI must select a named .png file", ErrExactFrameRequestInvalid)
	}
	return nil
}
