package key

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const (
	SchemaVersion = 1
	ProfileV1     = "cutly.cache.stage-key.v1"

	MaxNormalizedInputs = 4096
	MaxWireBytes        = 2 << 20
	MaxCanonicalBytes   = 1 << 20
	MaxTokenBytes       = 192
	MaxRoleBytes        = 128
)

var (
	ErrInvalid          = errors.New("cache key: invalid input")
	ErrCanonical        = errors.New("cache key: input is not canonical")
	ErrDigestMismatch   = errors.New("cache key: digest mismatch")
	versionedToken      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9][0-9]*$`)
	normalizedRoleToken = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

// IndexRange is a half-open composition index range [Start, Start+Count). Nil FrameRange or
// SampleRange explicitly means that the stage is independent of that domain. When present, a
// range must be non-empty and non-negative.
type IndexRange struct {
	Start int64 `json:"start"`
	Count int64 `json:"count"`
}

// NormalizedInput binds one ordered input role to immutable bytes. Ordinals are zero-based and
// contiguous within a role. Inputs are canonically ordered by Role then Ordinal.
type NormalizedInput struct {
	Role        string `json:"role"`
	Ordinal     uint32 `json:"ordinal"`
	ContentHash string `json:"content_hash"`
}

// Input is the complete preimage for a v1 stage cache key. There are intentionally no JobID or
// ArtifactURI fields. Namespace and ArtifactKind are independently versioned so storage policy
// and payload interpretation cannot alias merely because the remaining render inputs match.
type Input struct {
	SchemaVersion                int               `json:"schema_version"`
	Profile                      string            `json:"profile"`
	Namespace                    string            `json:"namespace"`
	ArtifactKind                 string            `json:"artifact_kind"`
	PlanFingerprint              string            `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string            `json:"execution_manifest_fingerprint"`
	StageProfile                 string            `json:"stage_profile"`
	Inputs                       []NormalizedInput `json:"inputs"`
	FrameRange                   *IndexRange       `json:"frame_range,omitempty"`
	SampleRange                  *IndexRange       `json:"sample_range,omitempty"`
	OutputProfile                string            `json:"output_profile"`
}

// Key is a self-describing CAS identity. Digest is the SHA-256 of CanonicalBinary(Input).
type Key struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`
	Namespace     string `json:"namespace"`
	ArtifactKind  string `json:"artifact_kind"`
	Digest        string `json:"digest"`
}

// DecodeInput is the strict JSON boundary for the cache-key protocol. It rejects duplicate,
// unknown, case-aliased, missing required fields and noncanonical normalized-input ordering.
func DecodeInput(raw []byte) (Input, error) {
	if len(raw) == 0 || len(raw) > MaxWireBytes {
		return Input{}, fmt.Errorf("%w: wire bytes %d exceed [1,%d]", ErrInvalid, len(raw), MaxWireBytes)
	}
	var input Input
	if err := wirejson.Decode(raw, &input); err != nil {
		return Input{}, fmt.Errorf("%w: decode: %w", ErrInvalid, err)
	}
	if err := Validate(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// Validate enforces the exact v1 language without silently sorting, trimming, or defaulting.
func Validate(input Input) error {
	if input.SchemaVersion != SchemaVersion || input.Profile != ProfileV1 {
		return fmt.Errorf("%w: schema/profile %d/%q", ErrInvalid, input.SchemaVersion, input.Profile)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "namespace", value: input.Namespace},
		{name: "artifact_kind", value: input.ArtifactKind},
		{name: "stage_profile", value: input.StageProfile},
		{name: "output_profile", value: input.OutputProfile},
	} {
		if !canonicalVersionedToken(field.value) {
			return fmt.Errorf("%w: %s must be a canonical versioned token", ErrInvalid, field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "plan_fingerprint", value: input.PlanFingerprint},
		{name: "execution_manifest_fingerprint", value: input.ExecutionManifestFingerprint},
	} {
		if !CanonicalSHA256(field.value) {
			return fmt.Errorf("%w: %s must be canonical lowercase sha256", ErrInvalid, field.name)
		}
	}
	if input.Inputs == nil {
		return fmt.Errorf("%w: inputs must be an explicit array", ErrCanonical)
	}
	if len(input.Inputs) > MaxNormalizedInputs {
		return fmt.Errorf("%w: normalized inputs %d exceed %d", ErrInvalid, len(input.Inputs), MaxNormalizedInputs)
	}
	var previous NormalizedInput
	for index, item := range input.Inputs {
		if len(item.Role) == 0 || len(item.Role) > MaxRoleBytes || !normalizedRoleToken.MatchString(item.Role) {
			return fmt.Errorf("%w: inputs[%d].role", ErrInvalid, index)
		}
		if !CanonicalSHA256(item.ContentHash) {
			return fmt.Errorf("%w: inputs[%d].content_hash", ErrInvalid, index)
		}
		if index == 0 || item.Role != previous.Role {
			if item.Ordinal != 0 {
				return fmt.Errorf("%w: inputs[%d] first ordinal for role %q must be zero", ErrCanonical, index, item.Role)
			}
			if index > 0 && item.Role <= previous.Role {
				return fmt.Errorf("%w: normalized input roles must be strictly sorted", ErrCanonical)
			}
		} else if item.Ordinal != previous.Ordinal+1 {
			return fmt.Errorf("%w: inputs[%d] ordinal must be contiguous", ErrCanonical, index)
		}
		previous = item
	}
	if err := validateRange("frame_range", input.FrameRange); err != nil {
		return err
	}
	if err := validateRange("sample_range", input.SampleRange); err != nil {
		return err
	}
	return nil
}

// ValidateKey validates the self-describing identity surface. Recomputing it requires the Input
// and is performed by Verify.
func ValidateKey(value Key) error {
	if value.SchemaVersion != SchemaVersion || value.Profile != ProfileV1 {
		return fmt.Errorf("%w: key schema/profile %d/%q", ErrInvalid, value.SchemaVersion, value.Profile)
	}
	if !canonicalVersionedToken(value.Namespace) || !canonicalVersionedToken(value.ArtifactKind) {
		return fmt.Errorf("%w: key namespace/artifact kind", ErrInvalid)
	}
	if !CanonicalSHA256(value.Digest) {
		return fmt.Errorf("%w: key digest", ErrInvalid)
	}
	return nil
}

func validateRange(name string, value *IndexRange) error {
	if value == nil {
		return nil
	}
	if value.Start < 0 || value.Count <= 0 || value.Start > (1<<63-1)-value.Count {
		return fmt.Errorf("%w: %s must be a nonnegative, nonempty, nonoverflowing range", ErrInvalid, name)
	}
	return nil
}

func canonicalVersionedToken(value string) bool {
	return len(value) > 0 && len(value) <= MaxTokenBytes && strings.TrimSpace(value) == value && versionedToken.MatchString(value)
}

// CanonicalSHA256 reports whether value is exactly sha256: followed by 64 lowercase hex digits.
func CanonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
