package renderplanv2

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

	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	MaxPlanBytes     = 8 << 20
	MaxDocumentBytes = 128 << 20 // S1 conservative admission bound, not a persisted snapshot encoding limit
	MaxNodes         = 200000
	MaxDepth         = 64
	MaxCollection    = 16384
	MaxStringBytes   = 1 << 20
)

var (
	ErrInvalid          = errors.New("renderplanv2: invalid plan")
	ErrLimit            = errors.New("renderplanv2: admission limit exceeded")
	ErrDocumentMismatch = errors.New("renderplanv2: plan does not match document")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Admission scans the Go graph before any JSON allocation/validation. The encoded
// bound is deliberately conservative (six bytes per string byte). Raw document
// metadata is measured, not reinterpreted under the integer-only plan profile.
func admission(ctx context.Context, value any, maxBytes int) error {
	if ctx == nil {
		return invalid("nil context")
	}
	nodes, size := 0, 0
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if nodes > MaxNodes || depth > MaxDepth {
			return ErrLimit
		}
		size += 32
		if size > maxBytes {
			return ErrLimit
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Map:
			if v.Len() > MaxCollection {
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
			if v.Type().Elem().Kind() == reflect.Uint8 {
				if v.Len() > MaxStringBytes {
					return ErrLimit
				}
				size += v.Len() * 6
			} else {
				if v.Len() > MaxCollection {
					return ErrLimit
				}
				for i := 0; i < v.Len(); i++ {
					if err := walk(v.Index(i), depth+1); err != nil {
						return err
					}
				}
			}
		case reflect.String:
			if v.Len() > MaxStringBytes {
				return ErrLimit
			}
			if !utf8.ValidString(v.String()) {
				return invalid("invalid UTF-8")
			}
			size += v.Len() * 6
		}
		if size > maxBytes {
			return ErrLimit
		}
		return nil
	}
	return walk(reflect.ValueOf(value), 0)
}

func rawCanonical(raw []byte) ([]byte, error) {
	return canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{MaxInputBytes: MaxPlanBytes, MaxDepth: MaxDepth, MaxValues: MaxNodes, MaxStringBytes: MaxPlanBytes, MaxIntegerDigits: 20})
}

func encode(plan Plan) ([]byte, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	return rawCanonical(raw)
}

// fingerprint removes the member entirely from the preimage. It does not hash an
// empty-string placeholder, unlike the legacy render plan contract.
func fingerprint(plan Plan) (string, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	delete(fields, "fingerprint")
	raw, err = json.Marshal(fields)
	if err != nil {
		return "", err
	}
	raw, err = rawCanonical(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Canonical accepts only an already valid, fingerprinted Plan; it never repairs
// ordering, missing collections or stale fingerprints.
func Canonical(plan Plan) ([]byte, error) {
	if err := admission(context.Background(), plan, MaxPlanBytes); err != nil {
		return nil, err
	}
	if err := verifyStructure(plan); err != nil {
		return nil, err
	}
	return encode(plan)
}

// Decode requires the canonical integer JSON byte spelling (no trailing newline).
// All fields are exact-case, required unless explicitly optional on the reused
// RenderTarget type, and no field accepts null. Collection order is semantic.
func Decode(raw []byte) (Plan, error) {
	canonical, err := rawCanonical(raw)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !bytes.Equal(raw, canonical) {
		return Plan{}, invalid("noncanonical JSON")
	}
	var plan Plan
	if err = wirejson.Decode(raw, &plan); err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err = Verify(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// VerifiedPlan owns canonical bytes, not caller-owned slices. Its zero value is
// invalid. Getters decode fresh copies, so concurrent readers share no mutable
// slices. A standalone handle does not assert document closure or authorization.
type VerifiedPlan struct{ canonical string }

func Verify(plan Plan) (VerifiedPlan, error) {
	raw, err := Canonical(plan)
	if err != nil {
		return VerifiedPlan{}, err
	}
	return VerifiedPlan{canonical: string(raw)}, nil
}
func (v VerifiedPlan) Valid() bool { return v.canonical != "" }
func (v VerifiedPlan) Canonical() ([]byte, error) {
	if !v.Valid() {
		return nil, invalid("zero verified handle")
	}
	return []byte(v.canonical), nil
}
func (v VerifiedPlan) Plan() (Plan, error) {
	if !v.Valid() {
		return Plan{}, invalid("zero verified handle")
	}
	var p Plan
	err := json.Unmarshal([]byte(v.canonical), &p)
	return p, err
}
func (v VerifiedPlan) Fingerprint() string {
	p, err := v.Plan()
	if err != nil {
		return ""
	}
	return p.Fingerprint
}
