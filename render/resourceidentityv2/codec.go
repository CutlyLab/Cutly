package resourceidentityv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	cutlycolor "github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrContext
	}
	return ctx.Err()
}
func digest(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func validDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validBinding(b Binding) error {
	if len(b.AssetID) > MaxAssetIDBytes || !utf8.ValidString(string(b.AssetID)) || strings.TrimSpace(string(b.AssetID)) == "" || !validDigest(b.ContentHash) || b.SizeBytes <= 0 {
		return ErrInvalid
	}
	switch b.Kind {
	case documentv2.AssetKindImage, documentv2.AssetKindFont, documentv2.AssetKindAudio:
		return nil
	default:
		return ErrUnsupported
	}
}

// Admission walks bounded typed metadata before any JSON marshal or clone. No
// byte buffers, live resources, custom marshalers or unbounded graphs are accepted.
func admission(ctx context.Context, r Receipt) error {
	nodes, size := 0, 0
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if e := contextError(ctx); e != nil {
			return e
		}
		nodes++
		size += 32
		if nodes > 65536 || size > MaxReceiptBytes {
			return ErrLimit
		}
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				return walk(v.Elem())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if e := walk(v.Field(i)); e != nil {
					return e
				}
			}
		case reflect.Slice:
			if v.Len() > int(sfntglyf.HardMaxTables) {
				return ErrLimit
			}
			for i := 0; i < v.Len(); i++ {
				if e := walk(v.Index(i)); e != nil {
					return e
				}
			}
		case reflect.String:
			if v.Len() > MaxAssetIDBytes {
				return ErrLimit
			}
			if !utf8.ValidString(v.String()) {
				return ErrInvalid
			}
			size += 6 * v.Len()
		}
		if size > MaxReceiptBytes {
			return ErrLimit
		}
		return nil
	}
	return walk(reflect.ValueOf(r))
}
func inspector(kind documentv2.AssetKind) (Inspector, error) {
	var descriptor any
	var profile string
	switch kind {
	case documentv2.AssetKindImage:
		descriptor = pngdecode.Descriptor()
		profile = pngdecode.ProfileV1
	case documentv2.AssetKindFont:
		descriptor = sfntglyf.Descriptor()
		profile = sfntglyf.ProfileV1
	default:
		return Inspector{}, ErrUnsupported
	}
	raw, e := canonicaljson.Marshal(descriptor)
	if e != nil {
		return Inspector{}, e
	}
	return Inspector{Profile: profile, DescriptorFingerprint: digest(raw)}, nil
}

// InspectorFor returns the pinned descriptor identity, never a host policy digest.
func InspectorFor(kind documentv2.AssetKind) (Inspector, error) { return inspector(kind) }

func validate(r Receipt) error {
	if r.SchemaVersion != SchemaVersion || r.Profile != Profile || r.CanonicalJSONProfile != canonicaljson.Profile {
		return ErrInvalid
	}
	if e := validBinding(r.Resource); e != nil {
		return e
	}
	want, e := inspector(r.Resource.Kind)
	if e != nil {
		return e
	}
	if r.Inspector != want {
		return ErrMismatch
	}
	switch r.Resource.Kind {
	case documentv2.AssetKindImage:
		if r.PNG == nil || r.Font != nil {
			return ErrInvalid
		}
		p := r.PNG
		if p.Width == 0 || p.Height == 0 || p.Width > 2147483647 || p.Height > 2147483647 || uint64(p.Width)*uint64(p.Height) > cpucompositor.HardMaxSurfaceBytes/cpucompositor.BytesPerPixel || p.SRGBRenderingIntent > 3 || p.EncodedContentHash != r.Resource.ContentHash || !validDigest(p.PixelHash) || p.DecodeProfile != pngdecode.ProfileV1 || p.NormalizationProfile != cutlycolor.ProfileRGBA8SRGBV1 || p.SurfaceProfile != cpucompositor.SurfaceProfileV1 || p.ContentHashProfile != pngdecode.ContentHashProfileV1 || uint64(r.Resource.SizeBytes) > pngdecode.HardMaxEncodedBytes {
			return ErrInvalid
		}
	case documentv2.AssetKindFont:
		if r.Font == nil || r.PNG != nil {
			return ErrInvalid
		}
		if e := validateFont(*r.Font, r.Resource); e != nil {
			return e
		}
	default:
		return ErrUnsupported
	}
	return nil
}

// Fingerprint validates semantic shape and hashes the canonical receipt with the
// fingerprint member entirely absent. The supplied fingerprint may be empty or
// exactly correct; a nonempty stale digest is rejected (no re-signing alias).
func Fingerprint(ctx context.Context, r Receipt) (out string, err error) {
	defer func() {
		if recover() != nil {
			out = ""
			err = ErrPanic
		}
	}()
	if err = admission(ctx, r); err != nil {
		return "", err
	}
	if err = validate(r); err != nil {
		return "", err
	}
	// Explicit projection prevents accidentally hashing a blank self member.
	body := struct {
		SchemaVersion        uint32            `json:"schema_version"`
		Profile              string            `json:"profile"`
		CanonicalJSONProfile string            `json:"canonical_json_profile"`
		Resource             Binding           `json:"resource"`
		Inspector            Inspector         `json:"inspector"`
		PNG                  *PNG              `json:"png,omitempty"`
		Font                 *sfntglyf.Receipt `json:"font,omitempty"`
	}{r.SchemaVersion, r.Profile, r.CanonicalJSONProfile, r.Resource, r.Inspector, r.PNG, r.Font}
	raw, e := canonicaljson.Marshal(body)
	if e != nil {
		return "", e
	}
	out = digest(raw)
	if r.Fingerprint != "" && r.Fingerprint != out {
		return "", ErrMismatch
	}
	if err = contextError(ctx); err != nil {
		return "", err
	}
	return out, nil
}

func Verify(ctx context.Context, r Receipt) (out Verified, err error) {
	defer func() {
		if recover() != nil {
			out = Verified{}
			err = ErrPanic
		}
	}()
	fp, e := Fingerprint(ctx, r)
	if e != nil {
		return out, e
	}
	if r.Fingerprint != fp {
		return out, ErrMismatch
	}
	owned := cloneReceipt(r)
	raw, e := canonicaljson.Marshal(owned)
	if e != nil {
		return out, e
	}
	if err = contextError(ctx); err != nil {
		return out, err
	}
	return Verified{receipt: &owned, canonical: raw}, nil
}

// Decode accepts noncanonical formatting but requires exact typed member names,
// presence, integer grammar and union shape. Unknown/duplicate/case-folded fields,
// nulls, missing required zeros and explicit empty optional payloads are rejected.
func Decode(ctx context.Context, raw []byte) (out Verified, err error) {
	defer func() {
		if recover() != nil {
			out = Verified{}
			err = ErrPanic
		}
	}()
	if err = contextError(ctx); err != nil {
		return out, err
	}
	if len(raw) > MaxReceiptBytes {
		return out, ErrLimit
	}
	canonical, e := canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{MaxInputBytes: MaxReceiptBytes, MaxDepth: 12, MaxValues: 65536, MaxStringBytes: MaxReceiptBytes, MaxIntegerDigits: 20})
	if e != nil {
		return out, fmt.Errorf("%w: %w", ErrInvalid, e)
	}
	var r Receipt
	if e = wirejson.Decode(canonical, &r); e != nil {
		return out, fmt.Errorf("%w: %w", ErrInvalid, e)
	}
	v, e := Verify(ctx, r)
	if e != nil {
		return out, e
	}
	if !bytes.Equal(canonical, v.canonical) {
		return out, ErrInvalid
	}
	return v, nil
}

// Equal compares verified canonical claims, not merely their self fingerprints.
func Equal(ctx context.Context, a, b Receipt) (equal bool, err error) {
	defer func() {
		if recover() != nil {
			equal = false
			err = ErrPanic
		}
	}()
	av, e := Verify(ctx, a)
	if e != nil {
		return false, e
	}
	bv, e := Verify(ctx, b)
	if e != nil {
		return false, e
	}
	if err = contextError(ctx); err != nil {
		return false, err
	}
	return bytes.Equal(av.canonical, bv.canonical), nil
}
