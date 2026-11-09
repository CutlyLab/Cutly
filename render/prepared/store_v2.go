package prepared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const completionV2 = "cutly.render.prepared-complete.v2"
const namespaceV2 = "v2"

// storageSessionV2 never accepts paths supplied by a caller. All names are
// single-component canonical hashes underneath pinned directory descriptors.
type storageSessionV2 interface {
	read(context.Context, string) ([]byte, error)
	publish(context.Context, string, []byte) error
	sync(string) error
	check() error
}

func keyNameV2(plan, manifest string) (string, error) {
	if !cachekey.CanonicalSHA256(plan) || !cachekey.CanonicalSHA256(manifest) {
		return "", ErrInvalidEnvironmentV2
	}
	h := sha256.Sum256([]byte(RecordProfileV2 + "\x00" + plan + "\x00" + manifest))
	return hex.EncodeToString(h[:]), nil
}
func completionBytesV2(raw []byte) []byte {
	h := sha256.Sum256(raw)
	return []byte(completionV2 + "\nsha256:" + hex.EncodeToString(h[:]) + "\n")
}

// PutV2 jointly verifies, owns and durably publishes a document-v2 environment.
// A failure returns no environment and created=false, including when a visible
// publication has uncertain durability (errors.Is(err, ErrDurabilityUncertain)).
// Inputs must not be concurrently mutated. V1 namespaces and limits are untouched.
func (s *Store) PutV2(ctx context.Context, doc documentv2.Document, plan renderplanv2.Plan, manifest renderapi.ExecutionManifestV2) (out EnvironmentV2, created bool, err error) {
	defer func() {
		if recover() != nil {
			out = EnvironmentV2{}
			created = false
			err = fmt.Errorf("%w: storage panic", ErrInvalidEnvironmentV2)
		}
	}()
	if s == nil || ctx == nil {
		return out, false, ErrInvalidEnvironmentV2
	}
	if err = ctx.Err(); err != nil {
		return out, false, err
	}
	if !storageSupportedV2() {
		return out, false, ErrUnsupported
	}
	verified, e := VerifyEnvironmentV2(ctx, EnvironmentV2{Document: doc, Plan: plan, Manifest: manifest})
	if e != nil {
		return out, false, e
	}
	raw, e := verified.Canonical(ctx)
	if e != nil {
		return out, false, e
	}
	if len(raw) > s.maxRecordBytesV2 {
		return out, false, ErrLimit
	}
	key := verified.Key()
	name, e := keyNameV2(key.PlanFingerprint, key.ExecutionManifestFingerprint)
	if e != nil {
		return out, false, e
	}
	// Prepare the owned result before the commit point, so no codec work can fail
	// after publication without a durability classification.
	result, e := verified.Environment(ctx)
	if e != nil {
		return out, false, e
	}
	made := false
	err = s.withStorageV2(ctx, true, func(x storageSessionV2) error {
		old, e := x.read(ctx, name)
		if e == nil {
			if !bytes.Equal(old, raw) {
				return ErrCollision
			}
			return x.sync(name)
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if e = x.publish(ctx, name, raw); e != nil {
			return e
		}
		made = true
		return nil
	})
	if err != nil {
		return EnvironmentV2{}, false, err
	}
	return result, made, nil
}

// ResolveV2 verifies an exact v2 entry and returns independently owned values.
// It never falls back to the v1 namespace, including when v2 is absent.
func (s *Store) ResolveV2(ctx context.Context, planFingerprint, manifestFingerprint string) (out EnvironmentV2, err error) {
	defer func() {
		if recover() != nil {
			out = EnvironmentV2{}
			err = fmt.Errorf("%w: storage panic", ErrInvalidEnvironmentV2)
		}
	}()
	if s == nil || ctx == nil {
		return out, ErrInvalidEnvironmentV2
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if !storageSupportedV2() {
		return out, ErrUnsupported
	}
	name, e := keyNameV2(planFingerprint, manifestFingerprint)
	if e != nil {
		return out, e
	}
	var result EnvironmentV2
	err = s.withStorageV2(ctx, false, func(x storageSessionV2) error {
		raw, e := x.read(ctx, name)
		if e != nil {
			return e
		}
		env, e := DecodeEnvironmentV2(ctx, raw)
		if e != nil {
			return errors.Join(ErrCorrupt, e)
		}
		if env.Plan.Fingerprint != planFingerprint || env.Manifest.Fingerprint != manifestFingerprint {
			return ErrCollision
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = x.check(); e != nil {
			return e
		}
		result = env
		return nil
	})
	if err != nil {
		return EnvironmentV2{}, err
	}
	return result, nil
}
