package prepared

import (
	"context"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	RecordSchemaVersionV2 = 2
	RecordProfileV2       = "cutly.render.prepared-environment.v2"
	// RecordCanonicalJSONProfileV2 sorts the envelope's ASCII member names and
	// embeds each independently canonical subtree verbatim. Document uses
	// documentv2.CanonicalJSON (Go JSON ordering/escaping, preserved number
	// lexemes); Plan/Manifest retain their strict S1/S2 integer profiles. This is
	// deliberately NOT the integer-only canonicaljson profile.
	RecordCanonicalJSONProfileV2 = "cutly.json.prepared-document-v2-number-preserving.v1"
	MaxDocumentBytesV2           = 128 << 20
	MaxPlanBytesV2               = renderplanv2.MaxPlanBytes // 8 MiB, not the S2 request limit
	MaxManifestBytesV2           = renderapi.MaxManifestBytesV2
	MaxRecordBytesV2             = 160 << 20
)

var ErrInvalidEnvironmentV2 = errors.New("prepared: invalid document-v2 environment")

// EnvironmentV2 is a concrete, portable snapshot, not host authorization. It
// contains no live providers. Inputs must not be mutated during API calls.
// NewEnvironmentV2 and DecodeEnvironmentV2 return independently owned values;
// use VerifyEnvironmentV2 for an immutable handle safe for concurrent readers.
//
// The 128 MiB Document ceiling is an encoded ceiling, NOT a promise that every
// such document is executable: unchanged S1 admission additionally caps nodes at
// 200,000, collections at 16,384, depth at 64 and strings/RawMessage at 1 MiB,
// and conservatively charges six bytes per string byte plus per-node overhead.
// S2 similarly has conservative admission. Those smaller effective limits are
// retained, not relaxed by this codec. V1's 64 MiB store cannot store v2 records.
type EnvironmentV2 struct {
	Document documentv2.Document           `json:"document"`
	Plan     renderplanv2.Plan             `json:"plan"`
	Manifest renderapi.ExecutionManifestV2 `json:"execution_manifest"`
}

// KeyV2 cross-binds all three identities for future storage integration. The
// record also carries and checks these identities; none grants host authority.
type KeyV2 struct {
	DocumentHash                 string `json:"document_hash"`
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
}

// VerifiedEnvironmentV2 owns only canonical bytes and immutable identities. Its
// zero value is invalid. Getters return fresh copies, never internal byte slices.
type VerifiedEnvironmentV2 struct {
	canonical string
	key       KeyV2
}

func (v VerifiedEnvironmentV2) Valid() bool { return v.canonical != "" }
func (v VerifiedEnvironmentV2) Key() KeyV2  { return v.key }

// Canonical returns a fresh record buffer. No verification/authorization work is
// skipped by the constructors; this accessor only copies previously verified data.
func (v VerifiedEnvironmentV2) Canonical(ctx context.Context) (out []byte, err error) {
	defer recoverEnvironmentV2(&err, func() { out = nil })
	if err = contextEnvironmentV2(ctx); err != nil {
		return nil, err
	}
	if !v.Valid() {
		return nil, ErrInvalidEnvironmentV2
	}
	out = []byte(v.canonical)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Environment returns an owned concrete snapshot for future storage or host
// reconstruction. It does not perform resource I/O or authorize execution.
func (v VerifiedEnvironmentV2) Environment(ctx context.Context) (out EnvironmentV2, err error) {
	defer recoverEnvironmentV2(&err, func() { out = EnvironmentV2{} })
	if err = contextEnvironmentV2(ctx); err != nil {
		return out, err
	}
	if !v.Valid() {
		return out, ErrInvalidEnvironmentV2
	}
	var r recordV2
	// The immutable bytes have already passed strict decode and joint verification.
	if err = unmarshalEnvironmentV2([]byte(v.canonical), &r); err != nil {
		return EnvironmentV2{}, err
	}
	out, err = ownedEnvironmentV2(r)
	if err != nil {
		return EnvironmentV2{}, err
	}
	if err = ctx.Err(); err != nil {
		return EnvironmentV2{}, err
	}
	return out, nil
}
