package backendstage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
)

const (
	MaxChunkAssemblyBytesV2       = 8 << 20
	MaxChunkAssemblyDescriptorsV2 = 16384
	MaxChunkAssemblyStringBytesV2 = 1 << 20
	MaxChunkAssemblyNodesV2       = 200000
	MaxChunkAssemblyDepthV2       = 64
)

func assemblyContextV2(ctx context.Context) error {
	if ctx == nil {
		return assemblyInvalidV2("nil context")
	}
	return ctx.Err()
}

// No attacker-controlled interface, callback or RawMessage lives in a request.
// Count and bound the typed graph before Marshal, clone or root allocation.
func assemblyAdmissionV2(ctx context.Context, r ChunkAssemblyRequestV2) error {
	if err := assemblyContextV2(ctx); err != nil {
		return err
	}
	if err := assemblyHeaderV2(r); err != nil {
		return err
	}
	nodes, size := 0, 0
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		size += 32
		if nodes > MaxChunkAssemblyNodesV2 || depth > MaxChunkAssemblyDepthV2 || size > MaxChunkAssemblyBytesV2 {
			return ErrChunkAssemblyLimitV2
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
			if v.Len() > MaxChunkAssemblyDescriptorsV2 {
				return ErrChunkAssemblyLimitV2
			}
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.String:
			if v.Len() > MaxChunkAssemblyStringBytesV2 {
				return ErrChunkAssemblyLimitV2
			}
			if !utf8.ValidString(v.String()) {
				return assemblyInvalidV2("invalid UTF-8")
			}
			size += v.Len() * 6
		}
		if size > MaxChunkAssemblyBytesV2 {
			return ErrChunkAssemblyLimitV2
		}
		return nil
	}
	return walk(reflect.ValueOf(r), 0)
}

func assemblyRawCanonicalV2(raw []byte) ([]byte, error) {
	if len(raw) > MaxChunkAssemblyBytesV2 {
		return nil, ErrChunkAssemblyLimitV2
	}
	return canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{MaxInputBytes: MaxChunkAssemblyBytesV2, MaxDepth: MaxChunkAssemblyDepthV2, MaxValues: MaxChunkAssemblyNodesV2, MaxStringBytes: MaxChunkAssemblyStringBytesV2, MaxIntegerDigits: 20})
}
func assemblyEncodeV2(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return assemblyRawCanonicalV2(raw)
}
func assemblyFingerprintV2(r ChunkAssemblyRequestV2) (string, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	delete(fields, "fingerprint")
	raw, err = assemblyEncodeV2(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func assemblyRecoverV2[T any](out *T, err *error) {
	if x := recover(); x != nil {
		var zero T
		*out = zero
		*err = fmt.Errorf("%w: boundary panic: %v", ErrChunkAssemblyInvalidV2, x)
	}
	if *err != nil {
		var zero T
		*out = zero
	}
}
func assemblyCanonicalV2(ctx context.Context, r ChunkAssemblyRequestV2) ([]byte, error) {
	if err := assemblyAdmissionV2(ctx, r); err != nil {
		return nil, err
	}
	if err := assemblyValidateV2(ctx, r); err != nil {
		return nil, err
	}
	raw, err := assemblyEncodeV2(r)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

// CanonicalChunkAssemblyRequestV2 requires a valid fingerprint; it does not fix,
// reorder, deduplicate or re-sign malformed caller data.
func CanonicalChunkAssemblyRequestV2(r ChunkAssemblyRequestV2) (out []byte, err error) {
	defer assemblyRecoverV2(&out, &err)
	return assemblyCanonicalV2(context.Background(), r)
}
func DecodeChunkAssemblyRequestV2(raw []byte) (out ChunkAssemblyRequestV2, err error) {
	defer assemblyRecoverV2(&out, &err)
	canonical, e := assemblyRawCanonicalV2(raw)
	if e != nil {
		return out, e
	}
	if !bytes.Equal(canonical, raw) {
		return out, assemblyInvalidV2("noncanonical JSON")
	}
	var r ChunkAssemblyRequestV2
	if err = wirejson.Decode(raw, &r); err != nil {
		return out, fmt.Errorf("%w: %w", ErrChunkAssemblyInvalidV2, err)
	}
	canonical, err = CanonicalChunkAssemblyRequestV2(r)
	if err != nil {
		return out, err
	}
	if !bytes.Equal(canonical, raw) {
		return out, assemblyInvalidV2("noncanonical typed request")
	}
	return r, nil
}

// VerifiedChunkAssemblyRequestV2 owns canonical bytes. It proves standalone
// consistency only unless obtained from AgainstPlan. Accessors return clones.
type VerifiedChunkAssemblyRequestV2 struct{ canonical string }

func (v VerifiedChunkAssemblyRequestV2) Valid() bool { return v.canonical != "" }
func (v VerifiedChunkAssemblyRequestV2) Canonical() ([]byte, error) {
	if !v.Valid() {
		return nil, assemblyInvalidV2("zero verified handle")
	}
	return []byte(v.canonical), nil
}
func (v VerifiedChunkAssemblyRequestV2) Request() (ChunkAssemblyRequestV2, error) {
	if !v.Valid() {
		return ChunkAssemblyRequestV2{}, assemblyInvalidV2("zero verified handle")
	}
	var r ChunkAssemblyRequestV2
	err := json.Unmarshal([]byte(v.canonical), &r)
	return r, err
}
func (v VerifiedChunkAssemblyRequestV2) Fingerprint() string {
	r, _ := v.Request()
	return r.Fingerprint
}
func VerifyChunkAssemblyRequestV2(r ChunkAssemblyRequestV2) (out VerifiedChunkAssemblyRequestV2, err error) {
	defer assemblyRecoverV2(&out, &err)
	raw, err := CanonicalChunkAssemblyRequestV2(r)
	if err != nil {
		return out, err
	}
	return VerifiedChunkAssemblyRequestV2{string(raw)}, nil
}

// VerifyChunkAssemblyRequestV2AgainstPlan checks cheap exact identities BEFORE
// any graph admission/clone/hash, then verifies native Document-v2 plan closure.
// No conversion to a V1 plan or request is performed. Actual document closure
// still requires renderplanv2.VerifyAgainstDocument at the host boundary.
func VerifyChunkAssemblyRequestV2AgainstPlan(ctx context.Context, r ChunkAssemblyRequestV2, m api.ExecutionManifestV2, p renderplanv2.Plan) (out VerifiedChunkAssemblyRequestV2, err error) {
	defer assemblyRecoverV2(&out, &err)
	if err = assemblyContextV2(ctx); err != nil {
		return out, err
	}
	if err = assemblyIdentityV2(r, m, p); err != nil {
		return out, err
	}
	if err = assemblyAdmissionV2(ctx, r); err != nil {
		return out, err
	}
	if _, err = api.VerifyExecutionManifestV2AgainstPlan(ctx, m, p); err != nil {
		return out, err
	}
	raw, e := assemblyCanonicalV2(ctx, r)
	if e != nil {
		return out, e
	}
	return VerifiedChunkAssemblyRequestV2{string(raw)}, nil
}

// FreezeChunkAssemblyRequestV2 fills only envelope and fingerprint. Every input
// binding, aggregate root and descriptor must already be exact; no repair or
// V1 translation occurs. The returned value is owned, never caller-aliased.
func FreezeChunkAssemblyRequestV2(ctx context.Context, p renderplanv2.Plan, m api.ExecutionManifestV2, draft ChunkAssemblyRequestV2) (out ChunkAssemblyRequestV2, err error) {
	defer assemblyRecoverV2(&out, &err)
	if err = assemblyContextV2(ctx); err != nil {
		return out, err
	}
	if err = assemblyIdentityV2(draft, m, p); err != nil {
		return out, err
	}
	draft.SchemaVersion = 2
	draft.Profile = ChunkAssemblyProfileV2
	draft.CanonicalJSONProfile = canonicaljson.Profile
	draft.Fingerprint = "sha256:" + string(bytes.Repeat([]byte("0"), 64))
	if err = assemblyAdmissionV2(ctx, draft); err != nil {
		return out, err
	}
	draft.Fingerprint, err = assemblyFingerprintV2(draft)
	if err != nil {
		return out, err
	}
	verified, e := VerifyChunkAssemblyRequestV2AgainstPlan(ctx, draft, m, p)
	if e != nil {
		return out, e
	}
	result, e := verified.Request()
	if e != nil {
		return out, e
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return result, nil
}
