package renderapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	MaxManifestBytesV2      = 16 << 20
	MaxRequestBytesV2       = 32 << 20
	MaxConfigurationBytesV2 = 16 << 10
	MaxNodesV2              = renderplanv2.MaxNodes
	MaxDepthV2              = renderplanv2.MaxDepth
	MaxCollectionV2         = renderplanv2.MaxCollection
	MaxStringBytesV2        = renderplanv2.MaxStringBytes
)

var (
	ErrExecutionManifestInvalidV2 = errors.New("render: invalid document-v2 execution manifest")
	ErrRenderRequestInvalidV2     = errors.New("render: invalid document-v2 render request")
	ErrLimitV2                    = errors.New("render: document-v2 admission limit exceeded")
)

func invalidV2(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrExecutionManifestInvalidV2, fmt.Sprintf(format, args...))
}

// Scan before JSON allocation, including RawMessage bytes. No caller graph is
// retained. Inputs must not be concurrently modified during an API call.
func admissionV2(ctx context.Context, value any, maxBytes int) error {
	if ctx == nil {
		return invalidV2("nil context")
	}
	nodes, size := 0, 0
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		size += 32
		if nodes > MaxNodesV2 || depth > MaxDepthV2 || size > maxBytes {
			return ErrLimitV2
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !v.IsNil() {
				return walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				if v.Len() > MaxStringBytesV2 {
					return ErrLimitV2
				}
				size += v.Len() * 6
			} else {
				if v.Len() > MaxCollectionV2 {
					return ErrLimitV2
				}
				for i := 0; i < v.Len(); i++ {
					if err := walk(v.Index(i), depth+1); err != nil {
						return err
					}
				}
			}
		case reflect.String:
			if v.Len() > MaxStringBytesV2 {
				return ErrLimitV2
			}
			if !utf8.ValidString(v.String()) {
				return invalidV2("invalid UTF-8")
			}
			size += v.Len() * 6
		}
		if size > maxBytes {
			return ErrLimitV2
		}
		return nil
	}
	return walk(reflect.ValueOf(value), 0)
}
func rawCanonicalV2(raw []byte, max int) ([]byte, error) {
	return canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{MaxInputBytes: max, MaxDepth: MaxDepthV2, MaxValues: MaxNodesV2, MaxStringBytes: MaxStringBytesV2, MaxIntegerDigits: 20})
}
func encodeV2(v any, max int) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return rawCanonicalV2(raw, max)
}
func hashV2(v any) (string, error) {
	raw, err := encodeV2(v, MaxManifestBytesV2)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func manifestFingerprintV2(m ExecutionManifestV2) (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	delete(fields, "fingerprint")
	return hashV2(fields)
}
func strictRawV2(raw []byte, target any, max int) error {
	c, err := rawCanonicalV2(raw, max)
	if err != nil {
		return invalidV2("canonical JSON: %v", err)
	}
	if !bytes.Equal(c, raw) {
		return invalidV2("noncanonical JSON")
	}
	// Canonical parser bounds bytes/depth/values before wirejson's allocating shape pass.
	if err = wirejson.Decode(raw, target); err != nil {
		return invalidV2("decode: %v", err)
	}
	return nil
}
func rawConfigurationsV2(m ExecutionManifestV2) error {
	for domain, p := range []ProgramSelectionV2{m.Visual, m.Audio} {
		if p.Configuration != nil {
			raw := p.Configuration
			c, err := rawCanonicalV2(raw, MaxConfigurationBytesV2)
			if err != nil || !bytes.Equal(raw, c) || len(raw) == 0 || raw[0] != '{' {
				return invalidV2("configuration must be a bounded canonical object")
			}
			// Validate the concrete shape before an outer Marshal can compact or
			// otherwise normalize an untrusted RawMessage.
			if domain == 0 {
				var config VisualProgramConfigurationV2
				if err := wirejson.Decode(raw, &config); err != nil {
					return invalidV2("visual configuration: %v", err)
				}
			} else {
				var config AudioProgramConfigurationV2
				if err := wirejson.Decode(raw, &config); err != nil {
					return invalidV2("audio configuration: %v", err)
				}
			}
		}
	}
	return nil
}
func CanonicalExecutionManifestV2(m ExecutionManifestV2) ([]byte, error) {
	if err := admissionV2(context.Background(), m, MaxManifestBytesV2); err != nil {
		return nil, err
	}
	if err := rawConfigurationsV2(m); err != nil {
		return nil, err
	}
	if err := validateManifestV2(m); err != nil {
		return nil, err
	}
	return encodeV2(m, MaxManifestBytesV2)
}
func DecodeExecutionManifestV2(raw []byte) (ExecutionManifestV2, error) {
	var m ExecutionManifestV2
	if err := strictRawV2(raw, &m, MaxManifestBytesV2); err != nil {
		return ExecutionManifestV2{}, err
	}
	canonical, err := CanonicalExecutionManifestV2(m)
	if err != nil {
		return ExecutionManifestV2{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return ExecutionManifestV2{}, invalidV2("noncanonical typed manifest")
	}
	return m, nil
}

// VerifiedExecutionManifestV2 owns canonical bytes. A standalone verification does
// not prove plan/document closure, actual bytes, host authority or executability.
type VerifiedExecutionManifestV2 struct{ canonical string }

func VerifyExecutionManifestV2(m ExecutionManifestV2) (VerifiedExecutionManifestV2, error) {
	raw, err := CanonicalExecutionManifestV2(m)
	if err != nil {
		return VerifiedExecutionManifestV2{}, err
	}
	return VerifiedExecutionManifestV2{string(raw)}, nil
}
func (v VerifiedExecutionManifestV2) Valid() bool { return v.canonical != "" }
func (v VerifiedExecutionManifestV2) Canonical() ([]byte, error) {
	if !v.Valid() {
		return nil, invalidV2("zero verified handle")
	}
	return []byte(v.canonical), nil
}
func (v VerifiedExecutionManifestV2) Manifest() (ExecutionManifestV2, error) {
	if !v.Valid() {
		return ExecutionManifestV2{}, invalidV2("zero verified handle")
	}
	var m ExecutionManifestV2
	err := json.Unmarshal([]byte(v.canonical), &m)
	return m, err
}
func (v VerifiedExecutionManifestV2) Fingerprint() string { m, _ := v.Manifest(); return m.Fingerprint }

func VerifyExecutionManifestV2AgainstPlan(ctx context.Context, m ExecutionManifestV2, p renderplanv2.Plan) (out VerifiedExecutionManifestV2, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = VerifiedExecutionManifestV2{}
			err = invalidV2("verification panic: %v", r)
		}
	}()
	if err = admissionV2(ctx, p, MaxRequestBytesV2); err != nil {
		return out, err
	}
	if err = admissionV2(ctx, m, MaxManifestBytesV2); err != nil {
		return out, err
	}
	if _, err = renderplanv2.Verify(p); err != nil {
		return out, err
	}
	candidate, e := VerifyExecutionManifestV2(m)
	if e != nil {
		return out, e
	}
	if err = againstPlanV2(m, p); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return candidate, nil
}

func CanonicalRenderRequestV2(r RenderRequestV2) ([]byte, error) {
	if err := admissionV2(context.Background(), r, MaxRequestBytesV2); err != nil {
		return nil, err
	}
	if err := rawConfigurationsV2(r.Manifest); err != nil {
		return nil, err
	}
	if err := validateRequestV2(context.Background(), r); err != nil {
		return nil, err
	}
	return encodeV2(r, MaxRequestBytesV2)
}
func DecodeRenderRequestV2(raw []byte) (RenderRequestV2, error) {
	var r RenderRequestV2
	if err := strictRawV2(raw, &r, MaxRequestBytesV2); err != nil {
		return RenderRequestV2{}, err
	}
	canonical, err := CanonicalRenderRequestV2(r)
	if err != nil {
		return RenderRequestV2{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return RenderRequestV2{}, fmt.Errorf("%w: noncanonical typed request", ErrRenderRequestInvalidV2)
	}
	return r, nil
}

type VerifiedRenderRequestV2 struct{ canonical string }

func VerifyRenderRequestV2(r RenderRequestV2) (VerifiedRenderRequestV2, error) {
	raw, err := CanonicalRenderRequestV2(r)
	if err != nil {
		return VerifiedRenderRequestV2{}, err
	}
	return VerifiedRenderRequestV2{string(raw)}, nil
}

// VerifyRenderRequestV2AgainstPlan additionally requires equality with an independently supplied plan.
func VerifyRenderRequestV2AgainstPlan(ctx context.Context, r RenderRequestV2, p renderplanv2.Plan) (out VerifiedRenderRequestV2, err error) {
	defer func() {
		if x := recover(); x != nil {
			out = VerifiedRenderRequestV2{}
			err = fmt.Errorf("%w: panic: %v", ErrRenderRequestInvalidV2, x)
		}
	}()
	if err = admissionV2(ctx, r, MaxRequestBytesV2); err != nil {
		return out, err
	}
	if _, err = VerifyExecutionManifestV2AgainstPlan(ctx, r.Manifest, p); err != nil {
		return out, err
	}
	if r.Plan.Fingerprint != p.Fingerprint {
		return out, fmt.Errorf("%w: independent plan mismatch", ErrRenderRequestInvalidV2)
	}
	out, err = VerifyRenderRequestV2(r)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return VerifiedRenderRequestV2{}, err
	}
	return out, nil
}
func (v VerifiedRenderRequestV2) Valid() bool { return v.canonical != "" }
func (v VerifiedRenderRequestV2) Canonical() ([]byte, error) {
	if !v.Valid() {
		return nil, fmt.Errorf("%w: zero verified handle", ErrRenderRequestInvalidV2)
	}
	return []byte(v.canonical), nil
}
func (v VerifiedRenderRequestV2) Request() (RenderRequestV2, error) {
	if !v.Valid() {
		return RenderRequestV2{}, fmt.Errorf("%w: zero verified handle", ErrRenderRequestInvalidV2)
	}
	var r RenderRequestV2
	err := json.Unmarshal([]byte(v.canonical), &r)
	return r, err
}
