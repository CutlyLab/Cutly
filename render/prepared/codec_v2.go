package prepared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

type recordV2 struct {
	CanonicalJSONProfile         string          `json:"canonical_json_profile"`
	Document                     json.RawMessage `json:"document"`
	DocumentHash                 string          `json:"document_hash"`
	ExecutionManifest            json.RawMessage `json:"execution_manifest"`
	ExecutionManifestFingerprint string          `json:"execution_manifest_fingerprint"`
	Plan                         json.RawMessage `json:"plan"`
	PlanFingerprint              string          `json:"plan_fingerprint"`
	Profile                      string          `json:"profile"`
	SchemaVersion                uint32          `json:"schema_version"`
}

func contextEnvironmentV2(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidEnvironmentV2
	}
	return ctx.Err()
}
func recoverEnvironmentV2(err *error, zero func()) {
	if recover() != nil {
		zero()
		// Never format a caller-controlled panic value (its String method may panic).
		*err = fmt.Errorf("%w: panic", ErrInvalidEnvironmentV2)
	}
}
func invalidEnvironmentV2(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidEnvironmentV2, err)
}

// NewEnvironmentV2 owns and jointly verifies a snapshot. It never freezes a
// manifest: supplied identity pins must already match the Document and Plan.
func NewEnvironmentV2(ctx context.Context, d documentv2.Document, p renderplanv2.Plan, m renderapi.ExecutionManifestV2) (out EnvironmentV2, err error) {
	defer recoverEnvironmentV2(&err, func() { out = EnvironmentV2{} })
	raw, err := CanonicalEnvironmentV2(ctx, EnvironmentV2{d, p, m})
	if err != nil {
		return out, err
	}
	var r recordV2
	if err = unmarshalEnvironmentV2(raw, &r); err != nil {
		return out, err
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

// CanonicalEnvironmentV2 checks typed admission before marshal, owns/strictly
// decodes each subtree, re-resolves S1 against Document and verifies S2 against
// Plan. It returns the number-preserving v2 record, not an integer-only encoding.
func CanonicalEnvironmentV2(ctx context.Context, e EnvironmentV2) (out []byte, err error) {
	defer recoverEnvironmentV2(&err, func() { out = nil })
	if err = contextEnvironmentV2(ctx); err != nil {
		return nil, err
	}
	for _, part := range []struct {
		v   any
		max int
	}{{e.Document, MaxDocumentBytesV2}, {e.Plan, MaxPlanBytesV2}, {e.Manifest, MaxManifestBytesV2}} {
		if err = admitEnvironmentV2(ctx, reflect.ValueOf(part.v), part.max); err != nil {
			return nil, err
		}
	}
	d, err := canonicalDocumentEnvironmentV2(ctx, e.Document)
	if err != nil {
		return nil, err
	}
	p, err := renderplanv2.Canonical(e.Plan)
	if err != nil {
		return nil, invalidEnvironmentV2(err)
	}
	m, err := renderapi.CanonicalExecutionManifestV2(e.Manifest)
	if err != nil {
		return nil, invalidEnvironmentV2(err)
	}
	sum := sha256.Sum256(d)
	r := recordV2{RecordCanonicalJSONProfileV2, d, "sha256:" + hex.EncodeToString(sum[:]), m, e.Manifest.Fingerprint, p, e.Plan.Fingerprint, RecordProfileV2, RecordSchemaVersionV2}
	if err = verifyRecordEnvironmentV2(ctx, r); err != nil {
		return nil, err
	}
	out, err = encodeRecordEnvironmentV2(r)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// DecodeEnvironmentV2 admits raw input before allocating its shape, rejects all
// noncanonical typed aliases, and returns no partial value on any failure.
func DecodeEnvironmentV2(ctx context.Context, raw []byte) (out EnvironmentV2, err error) {
	defer recoverEnvironmentV2(&err, func() { out = EnvironmentV2{} })
	if err = contextEnvironmentV2(ctx); err != nil {
		return out, err
	}
	if err = scanEnvironmentV2(ctx, raw, MaxRecordBytesV2, renderplanv2.MaxNodes*3+32, renderplanv2.MaxDepth+2); err != nil {
		return out, err
	}
	var r recordV2
	if err = wirejson.Decode(raw, &r); err != nil {
		return out, invalidEnvironmentV2(err)
	}
	if err = verifyRecordEnvironmentV2(ctx, r); err != nil {
		return out, err
	}
	want, err := encodeRecordEnvironmentV2(r)
	if err != nil {
		return out, err
	}
	if !bytes.Equal(raw, want) {
		return out, ErrInvalidEnvironmentV2
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

// VerifyEnvironmentV2 returns an owned immutable handle after the same complete
// structural checks as the constructor. Structural validity is NOT authorization.
func VerifyEnvironmentV2(ctx context.Context, e EnvironmentV2) (out VerifiedEnvironmentV2, err error) {
	defer recoverEnvironmentV2(&err, func() { out = VerifiedEnvironmentV2{} })
	raw, err := CanonicalEnvironmentV2(ctx, e)
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return VerifiedEnvironmentV2{string(raw), KeyV2{e.Plan.DocumentHash, e.Plan.Fingerprint, e.Manifest.Fingerprint}}, nil
}

func canonicalDocumentEnvironmentV2(ctx context.Context, d documentv2.Document) ([]byte, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, invalidEnvironmentV2(err)
	}
	if err = scanEnvironmentV2(ctx, raw, MaxDocumentBytesV2, renderplanv2.MaxNodes, renderplanv2.MaxDepth); err != nil {
		return nil, err
	}
	// Reuse the number-preserving Document profile, never canonicaljson.Marshal.
	canonical, err := videoedit.CanonicalJSON(d)
	if err != nil {
		return nil, invalidEnvironmentV2(err)
	}
	if len(canonical) > MaxDocumentBytesV2 {
		return nil, ErrLimit
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return canonical, nil
}
func verifyRecordEnvironmentV2(ctx context.Context, r recordV2) error {
	if err := contextEnvironmentV2(ctx); err != nil {
		return err
	}
	if r.SchemaVersion != RecordSchemaVersionV2 || r.Profile != RecordProfileV2 || r.CanonicalJSONProfile != RecordCanonicalJSONProfileV2 {
		return ErrInvalidEnvironmentV2
	}
	for _, part := range []struct {
		raw []byte
		max int
	}{{r.Document, MaxDocumentBytesV2}, {r.Plan, MaxPlanBytesV2}, {r.ExecutionManifest, MaxManifestBytesV2}} {
		if err := scanEnvironmentV2(ctx, part.raw, part.max, renderplanv2.MaxNodes, renderplanv2.MaxDepth); err != nil {
			return err
		}
	}
	var d documentv2.Document
	if err := wirejson.Decode(r.Document, &d); err != nil {
		return invalidEnvironmentV2(err)
	}
	if err := admitEnvironmentV2(ctx, reflect.ValueOf(d), MaxDocumentBytesV2); err != nil {
		return err
	}
	canonical, err := canonicalDocumentEnvironmentV2(ctx, d)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, r.Document) {
		return ErrInvalidEnvironmentV2
	}
	sum := sha256.Sum256(canonical)
	if r.DocumentHash != "sha256:"+hex.EncodeToString(sum[:]) {
		return ErrInvalidEnvironmentV2
	}
	p, err := renderplanv2.Decode(r.Plan)
	if err != nil {
		return invalidEnvironmentV2(err)
	}
	// S1 Decode does not itself promise typed byte equality for optional fields.
	pc, err := renderplanv2.Canonical(p)
	if err != nil || !bytes.Equal(pc, r.Plan) {
		return ErrInvalidEnvironmentV2
	}
	m, err := renderapi.DecodeExecutionManifestV2(r.ExecutionManifest)
	if err != nil {
		return invalidEnvironmentV2(err)
	}
	if p.DocumentHash != r.DocumentHash || m.DocumentHash != r.DocumentHash || p.Fingerprint != r.PlanFingerprint || m.Fingerprint != r.ExecutionManifestFingerprint || m.PlanFingerprint != p.Fingerprint {
		return ErrInvalidEnvironmentV2
	}
	if err = renderplanv2.VerifyAgainstDocument(ctx, p, d); err != nil {
		return invalidEnvironmentV2(err)
	}
	if _, err = renderapi.VerifyExecutionManifestV2AgainstPlan(ctx, m, p); err != nil {
		return invalidEnvironmentV2(err)
	}
	return ctx.Err()
}

// Assemble only the ASCII envelope; a generic re-marshal would change S1/S2
// Unicode ordering/escaping or Document numeric spellings. Checked aggregation
// precedes the final allocation. 128+8+16 MiB plus headers fits the 160 MiB cap.
func encodeRecordEnvironmentV2(r recordV2) ([]byte, error) {
	parts := []struct {
		name string
		raw  []byte
	}{
		{"canonical_json_profile", nil}, {"document", r.Document}, {"document_hash", nil}, {"execution_manifest", r.ExecutionManifest}, {"execution_manifest_fingerprint", nil}, {"plan", r.Plan}, {"plan_fingerprint", nil}, {"profile", nil}, {"schema_version", nil},
	}
	values := map[string]any{"canonical_json_profile": r.CanonicalJSONProfile, "document_hash": r.DocumentHash, "execution_manifest_fingerprint": r.ExecutionManifestFingerprint, "plan_fingerprint": r.PlanFingerprint, "profile": r.Profile, "schema_version": r.SchemaVersion}
	size := 2
	for i := range parts {
		if parts[i].raw == nil {
			var err error
			parts[i].raw, err = json.Marshal(values[parts[i].name])
			if err != nil {
				return nil, invalidEnvironmentV2(err)
			}
		}
		n := len(parts[i].raw)
		overhead := len(parts[i].name) + 4
		if n > MaxRecordBytesV2-size || overhead > MaxRecordBytesV2-size-n {
			return nil, ErrLimit
		}
		size += n + overhead
	}
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, p := range parts {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '"')
		out = append(out, p.name...)
		out = append(out, '"', ':')
		out = append(out, p.raw...)
	}
	return append(out, '}'), nil
}
func unmarshalEnvironmentV2(raw []byte, r *recordV2) error { return json.Unmarshal(raw, r) }
func ownedEnvironmentV2(r recordV2) (out EnvironmentV2, err error) {
	if err = json.Unmarshal(r.Document, &out.Document); err != nil {
		return EnvironmentV2{}, err
	}
	if err = json.Unmarshal(r.Plan, &out.Plan); err != nil {
		return EnvironmentV2{}, err
	}
	if err = json.Unmarshal(r.ExecutionManifest, &out.Manifest); err != nil {
		return EnvironmentV2{}, err
	}
	return out, nil
}

// Conservative typed admission precedes any marshal/semantic walk, including
// open RawMessage metadata. Subtraction avoids overflow; cycles hit depth bounds.
func admitEnvironmentV2(ctx context.Context, v reflect.Value, maximum int) error {
	budget := uint64(maximum)
	nodes := 0
	take := func(n uint64) error {
		if n > budget {
			return ErrLimit
		}
		budget -= n
		return nil
	}
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if nodes > renderplanv2.MaxNodes || depth > renderplanv2.MaxDepth {
			return ErrLimit
		}
		if err := take(32); err != nil {
			return err
		}
		if !v.IsValid() {
			return nil
		}
		if v.Type() == reflect.TypeOf(json.RawMessage{}) {
			if v.IsNil() {
				return nil
			}
			if v.Len() > renderplanv2.MaxStringBytes {
				return ErrLimit
			}
			if err := take(uint64(v.Len()) * 6); err != nil {
				return err
			}
			return scanEnvironmentV2(ctx, v.Bytes(), renderplanv2.MaxStringBytes, renderplanv2.MaxNodes, renderplanv2.MaxDepth)
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() && strings.Split(v.Type().Field(i).Tag.Get("json"), ",")[0] != "-" {
					if err := walk(v.Field(i), depth+1); err != nil {
						return err
					}
				}
			}
		case reflect.Map:
			if v.Len() > renderplanv2.MaxCollection {
				return ErrLimit
			}
			it := v.MapRange()
			for it.Next() {
				if err := walk(it.Key(), depth+1); err != nil {
					return err
				}
				if err := walk(it.Value(), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if v.Len() > renderplanv2.MaxCollection {
				return ErrLimit
			}
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.String:
			if v.Len() > renderplanv2.MaxStringBytes {
				return ErrLimit
			}
			if !utf8.ValidString(v.String()) {
				return ErrInvalidEnvironmentV2
			}
			return take(uint64(v.Len()) * 6)
		}
		return nil
	}
	return walk(v, 0)
}
