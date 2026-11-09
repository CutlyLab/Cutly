package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	MaxEvidenceBytesV2       = 16 << 20
	MaxEvidenceDescriptorsV2 = 16384
	MaxEvidenceStringBytesV2 = 1 << 20
	MaxEvidenceNodesV2       = 400000
	MaxEvidenceDepthV2       = 64
)

func admissionV2(ctx context.Context, m ManifestV2) error {
	if err := contextV2(ctx); err != nil {
		return err
	}
	// Constant-size identity checks precede all graph traversal and hashing.
	if m.SchemaVersion != SchemaVersionV2 || m.Profile != ProfileV2 || m.CanonicalJSONProfile != canonicaljson.Profile || !validDigest(m.WorkerExecutableDigest) || !validDigest(m.Artifact.ContentHash) || m.Artifact.SizeBytes <= 0 || m.Artifact.URI != m.Assembly.ArtifactURI {
		return invalidV2("envelope/worker/artifact identity")
	}
	if m.Fingerprint != "" && !validDigest(m.Fingerprint) {
		return invalidV2("fingerprint shape")
	}
	if err := bindingHeaderV2(m.Assembly); err != nil {
		return err
	}
	return graphAdmissionV2(ctx, m)
}

func graphAdmissionV2(ctx context.Context, value any) error {
	return graphAdmissionLimitsV2(ctx, value, MaxEvidenceBytesV2, MaxEvidenceNodesV2)
}
func graphAdmissionLimitsV2(ctx context.Context, value any, maxBytes, maxNodes int) error {
	nodes, size := 0, 0
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		size += 32
		if nodes > maxNodes || depth > MaxEvidenceDepthV2 || size > maxBytes {
			return ErrEvidenceLimitV2
		}
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				return walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice:
			if v.Len() > MaxEvidenceDescriptorsV2 {
				return ErrEvidenceLimitV2
			}
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.String:
			if v.Len() > MaxEvidenceStringBytesV2 {
				return ErrEvidenceLimitV2
			}
			if !utf8.ValidString(v.String()) {
				return invalidV2("invalid UTF-8")
			}
			size += v.Len() * 6
		}
		if size > maxBytes {
			return ErrEvidenceLimitV2
		}
		return nil
	}
	return walk(reflect.ValueOf(value), 0)
}

// CanonicalBindingV2 validates the complete neutral assembly contract and returns
// owned canonical bytes. It is standalone identity validation, not plan closure.
func CanonicalBindingV2(ctx context.Context, b BindingV2) (out []byte, err error) {
	defer recoverV2(&out, &err)
	if err = contextV2(ctx); err != nil {
		return out, err
	}
	if err = bindingHeaderV2(b); err != nil {
		return out, err
	}
	if err = validateBindingV2(ctx, b); err != nil {
		return out, err
	}
	out, err = encodeV2(b)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifiedBindingV2 owns standalone-verified canonical request identity. A host
// must still establish native plan closure and publication authorization.
type VerifiedBindingV2 struct{ canonical string }

func (v VerifiedBindingV2) Valid() bool { return v.canonical != "" }
func (v VerifiedBindingV2) Canonical() ([]byte, error) {
	if !v.Valid() {
		return nil, invalidV2("zero verified binding")
	}
	return []byte(v.canonical), nil
}
func (v VerifiedBindingV2) Binding() (BindingV2, error) {
	if !v.Valid() {
		return BindingV2{}, invalidV2("zero verified binding")
	}
	var b BindingV2
	err := json.Unmarshal([]byte(v.canonical), &b)
	return b, err
}
func (v VerifiedBindingV2) Fingerprint() string { b, _ := v.Binding(); return b.Fingerprint }
func VerifyBindingV2(ctx context.Context, b BindingV2) (out VerifiedBindingV2, err error) {
	defer recoverV2(&out, &err)
	raw, e := CanonicalBindingV2(ctx, b)
	if e != nil {
		return out, e
	}
	return VerifiedBindingV2{canonical: string(raw)}, nil
}

func rawCanonicalV2(raw []byte) ([]byte, error) {
	if len(raw) > MaxEvidenceBytesV2 {
		return nil, ErrEvidenceLimitV2
	}
	return canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{MaxInputBytes: MaxEvidenceBytesV2, MaxDepth: MaxEvidenceDepthV2, MaxValues: MaxEvidenceNodesV2, MaxStringBytes: MaxEvidenceStringBytesV2, MaxIntegerDigits: 20})
}
func encodeV2(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return rawCanonicalV2(raw)
}

// Fingerprints exclude the fingerprint member entirely (not an empty string).
func digestObjectV2(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	delete(fields, "fingerprint")
	raw, err = encodeV2(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func fingerprintV2(m ManifestV2) (string, error)       { return digestObjectV2(m) }
func bindingFingerprintV2(b BindingV2) (string, error) { return digestObjectV2(b) }

// FingerprintManifestV2 validates a draft, then hashes it without fingerprint.
// A present fingerprint must be canonical but need not already equal the result.
func FingerprintManifestV2(ctx context.Context, m ManifestV2) (out string, err error) {
	defer recoverV2(&out, &err)
	if err = admissionV2(ctx, m); err != nil {
		return out, err
	}
	if err = validateV2(ctx, m); err != nil {
		return out, err
	}
	out, err = fingerprintV2(m)
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return out, nil
}
func canonicalV2(ctx context.Context, m ManifestV2) ([]byte, error) {
	if err := admissionV2(ctx, m); err != nil {
		return nil, err
	}
	if err := validateV2(ctx, m); err != nil {
		return nil, err
	}
	want, err := fingerprintV2(m)
	if err != nil {
		return nil, err
	}
	if want != m.Fingerprint {
		return nil, invalidV2("manifest fingerprint")
	}
	raw, err := encodeV2(m)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

// CanonicalManifestV2 validates without repairing, sorting or re-signing input.
func CanonicalManifestV2(m ManifestV2) (out []byte, err error) {
	defer recoverV2(&out, &err)
	return canonicalV2(context.Background(), m)
}
func DecodeManifestV2(raw []byte) (out ManifestV2, err error) {
	defer recoverV2(&out, &err)
	canonical, e := rawCanonicalV2(raw)
	if e != nil {
		return out, e
	}
	if !bytes.Equal(canonical, raw) {
		return out, invalidV2("noncanonical JSON")
	}
	var m ManifestV2
	if err = wirejson.Decode(raw, &m); err != nil {
		return out, fmt.Errorf("%w: %w", ErrEvidenceInvalidV2, err)
	}
	canonical, err = canonicalV2(context.Background(), m)
	if err != nil {
		return out, err
	}
	if !bytes.Equal(canonical, raw) {
		return out, invalidV2("noncanonical typed manifest")
	}
	return m, nil
}

// VerifiedManifestV2 owns canonical bytes; getters never expose shared slices.
// Standalone validity is not a claim of request/plan authorization.
type VerifiedManifestV2 struct{ canonical string }

func (v VerifiedManifestV2) Valid() bool { return v.canonical != "" }
func (v VerifiedManifestV2) Canonical() ([]byte, error) {
	if !v.Valid() {
		return nil, invalidV2("zero verified handle")
	}
	return []byte(v.canonical), nil
}
func (v VerifiedManifestV2) Manifest() (ManifestV2, error) {
	if !v.Valid() {
		return ManifestV2{}, invalidV2("zero verified handle")
	}
	var m ManifestV2
	err := json.Unmarshal([]byte(v.canonical), &m)
	return m, err
}
func (v VerifiedManifestV2) Fingerprint() string { m, _ := v.Manifest(); return m.Fingerprint }
func VerifyManifestV2(m ManifestV2) (out VerifiedManifestV2, err error) {
	defer recoverV2(&out, &err)
	raw, e := canonicalV2(context.Background(), m)
	if e != nil {
		return out, e
	}
	return VerifiedManifestV2{string(raw)}, nil
}

// VerifyManifestV2AgainstBinding rejects foreign fixed-size identities before
// admission, cloning or hashing either request's chunk/decoded graph. It then
// validates both graphs and compares the complete canonical assembly request.
// It is the adapter's responsibility to obtain binding from a plan-bound,
// verified ChunkAssemblyRequestV2, not an untrusted caller assertion.
func VerifyManifestV2AgainstBinding(ctx context.Context, m ManifestV2, b BindingV2) (out VerifiedManifestV2, err error) {
	defer recoverV2(&out, &err)
	if err = contextV2(ctx); err != nil {
		return out, err
	}
	a := m.Assembly
	if a.SchemaVersion != b.SchemaVersion || a.Profile != b.Profile || a.CanonicalJSONProfile != b.CanonicalJSONProfile || a.JobID != b.JobID || a.ArtifactURI != b.ArtifactURI || a.DocumentHash != b.DocumentHash || a.PlanFingerprint != b.PlanFingerprint || a.ExecutionManifestFingerprint != b.ExecutionManifestFingerprint || a.TimelineRange != b.TimelineRange || a.FrameRange != b.FrameRange || a.SampleRange != b.SampleRange || a.Target != b.Target || a.MediaRuntime != b.MediaRuntime || a.Output != b.Output || a.DeterministicSeed != b.DeterministicSeed || a.ProviderExecutableDigest != b.ProviderExecutableDigest || a.Fingerprint != b.Fingerprint {
		return out, invalidV2("assembly identity binding")
	}
	// Bound b independently before walking it, even if its claimed fingerprint
	// matches the evidence. Never rely solely on a caller-supplied digest.
	expected := m
	expected.Assembly = b
	if err = admissionV2(ctx, expected); err != nil {
		return out, err
	}
	if err = validateBindingV2(ctx, b); err != nil {
		return out, err
	}
	raw, e := canonicalV2(ctx, m)
	if e != nil {
		return out, e
	}
	left, e := encodeV2(a)
	if e != nil {
		return out, e
	}
	right, e := encodeV2(b)
	if e != nil {
		return out, e
	}
	if !bytes.Equal(left, right) {
		return out, invalidV2("ordered assembly binding")
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return VerifiedManifestV2{string(raw)}, nil
}
