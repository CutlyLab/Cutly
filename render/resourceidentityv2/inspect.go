package resourceidentityv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

// prehash admits and hashes borrowed bytes before invoking any parser/decoder or
// allocating proportional buffers. Callers MUST keep encoded immutable until
// Inspect returns. Parser-owned Font storage is the only complete encoded copy.
func prehash(ctx context.Context, b Binding, kind documentv2.AssetKind, encoded []byte, max, hard uint64) error {
	if e := contextError(ctx); e != nil {
		return e
	}
	if e := validBinding(b); e != nil {
		return e
	}
	if b.Kind != kind {
		return ErrMismatch
	}
	if max == 0 || max > hard || uint64(len(encoded)) > max {
		return ErrLimit
	}
	if b.SizeBytes != int64(len(encoded)) {
		return ErrMismatch
	}
	h := sha256.New()
	for offset := 0; offset < len(encoded); {
		if e := contextError(ctx); e != nil {
			return e
		}
		end := offset + (32 << 10)
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = h.Write(encoded[offset:end])
		offset = end
	}
	if e := contextError(ctx); e != nil {
		return e
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != b.ContentHash {
		return ErrMismatch
	}
	return nil
}
func newReceipt(b Binding) (Receipt, error) {
	in, e := inspector(b.Kind)
	if e != nil {
		return Receipt{}, e
	}
	return Receipt{SchemaVersion: SchemaVersion, Profile: Profile, CanonicalJSONProfile: canonicaljson.Profile, Resource: b, Inspector: in}, nil
}
func finish(ctx context.Context, r Receipt, i *Inspected) (*Inspected, error) {
	fp, e := Fingerprint(ctx, r)
	if e != nil {
		return nil, e
	}
	r.Fingerprint = fp
	v, e := Verify(ctx, r)
	if e != nil {
		return nil, e
	}
	i.binding = r.Resource
	i.receipt = v.receipt
	i.fingerprint = fp
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	return i, nil
}

// InspectPNG requires independently authorized expected bindings, not wire
// claims. Host limits affect admission only, never successful receipt identity.
func InspectPNG(ctx context.Context, expected Binding, encoded []byte, limits pngdecode.Limits) (out *Inspected, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = ErrPanic
		}
	}()
	maximum := limits.MaxEncodedBytes
	if limits == (pngdecode.Limits{}) {
		maximum = pngdecode.DefaultMaxEncodedBytes
	}
	if err = prehash(ctx, expected, documentv2.AssetKindImage, encoded, maximum, pngdecode.HardMaxEncodedBytes); err != nil {
		return nil, err
	}
	p, e := pngdecode.Decode(ctx, encoded, limits)
	if e != nil {
		return nil, fmt.Errorf("resourceidentityv2: PNG: %w", e)
	}
	if p.EncodedContentHash != expected.ContentHash || p.Surface == nil {
		return nil, ErrMismatch
	}
	r, e := newReceipt(expected)
	if e != nil {
		return nil, e
	}
	r.PNG = &PNG{Width: uint32(p.Width), Height: uint32(p.Height), SRGBRenderingIntent: p.SRGBRenderingIntent, EncodedContentHash: p.EncodedContentHash, PixelHash: p.PixelHash, DecodeProfile: p.DecodeProfile, NormalizationProfile: p.NormalizationProfile, SurfaceProfile: p.SurfaceProfile, ContentHashProfile: p.ContentHashProfile}
	return finish(ctx, r, &Inspected{surface: p.Surface})
}

// InspectFont retains the parser's already-owned immutable Font, without making
// another encoded copy. This core gate does not validate glyf outlines or layout.
func InspectFont(ctx context.Context, expected Binding, encoded []byte, limits sfntglyf.Limits) (out *Inspected, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = ErrPanic
		}
	}()
	maximum := limits.MaxEncodedBytes
	if maximum == 0 {
		maximum = sfntglyf.DefaultMaxEncodedBytes
	}
	if err = prehash(ctx, expected, documentv2.AssetKindFont, encoded, maximum, sfntglyf.HardMaxEncodedBytes); err != nil {
		return nil, err
	}
	font, f, e := sfntglyf.Parse(ctx, encoded, limits)
	if e != nil {
		return nil, fmt.Errorf("resourceidentityv2: font: %w", e)
	}
	if f.ContentHash != expected.ContentHash || f.EncodedBytes != uint64(expected.SizeBytes) || font == nil {
		return nil, ErrMismatch
	}
	// Core Font.Receipt historically returns nil for empty layout tables. This
	// projection has one spelling: required [] (never null) for an empty set.
	if f.OpaqueLayoutTables == nil {
		f.OpaqueLayoutTables = []string{}
	}
	r, e := newReceipt(expected)
	if e != nil {
		return nil, e
	}
	r.Font = &f
	return finish(ctx, r, &Inspected{font: font})
}

// InspectAudio maps only the real native wavsource.Inspect ProbeManifest and its
// existing probe.Fingerprint. It invents neither an FFprobe receipt nor another
// probe wire format, and retains no decoded PCM or encoded buffer.
func InspectAudio(ctx context.Context, expected Binding, encoded []byte, limits wavdecode.Limits) (out *Inspected, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = ErrPanic
		}
	}()
	if err = contextError(ctx); err != nil {
		return nil, err
	}
	if err = validBinding(expected); err != nil {
		return nil, err
	}
	if expected.Kind != documentv2.AssetKindAudio {
		return nil, ErrMismatch
	}
	p, e := wavsource.Inspect(ctx, expected.AssetID, expected.ContentHash, expected.SizeBytes, encoded, limits)
	if e != nil {
		return nil, e
	}
	if string(p.Resource.AssetID) != string(expected.AssetID) || p.Resource.ContentHash != expected.ContentHash || p.Resource.SizeBytes != expected.SizeBytes {
		return nil, ErrMismatch
	}
	fp, e := probe.Fingerprint(p)
	if e != nil {
		return nil, e
	}
	if err = contextError(ctx); err != nil {
		return nil, err
	}
	return &Inspected{binding: expected, audio: &p, fingerprint: fp}, nil
}

// Check binds an S2 claim to an ACTUAL inspection and an independently authorized
// expected Binding. It never trusts the claim as authority. Locators are outside
// this package: the caller must authorize them separately. General video is
// explicitly unsupported. A zero Inspected or a merely Verified receipt fails.
func Check(ctx context.Context, expected Binding, claim renderapi.MaterializedResourceV2, actual *Inspected) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
	}()
	if err = contextError(ctx); err != nil {
		return err
	}
	if err = validBinding(expected); err != nil {
		return err
	}
	if actual == nil || actual.fingerprint == "" || actual.binding != expected || (Binding{claim.AssetID, claim.Kind, claim.ContentHash, claim.SizeBytes}) != expected {
		return ErrMismatch
	}
	switch expected.Kind {
	case documentv2.AssetKindImage, documentv2.AssetKindFont:
		profile := renderapi.ImageMaterializationProfileV2
		if expected.Kind == documentv2.AssetKindFont {
			profile = renderapi.FontMaterializationProfileV2
		}
		if actual.receipt == nil || claim.MaterializationProfile != profile || claim.ProbeFingerprint != nil || claim.InspectionFingerprint == nil || *claim.InspectionFingerprint != actual.fingerprint {
			return ErrMismatch
		}
	case documentv2.AssetKindAudio:
		if actual.audio == nil || claim.MaterializationProfile != renderapi.AVMaterializationProfileV2 || claim.InspectionFingerprint != nil || claim.ProbeFingerprint == nil || *claim.ProbeFingerprint != actual.fingerprint {
			return ErrMismatch
		}
	default:
		return ErrUnsupported
	}
	return contextError(ctx)
}

// Matches rejects even correctly re-signed persisted claims that differ from
// actual bytes. Verify alone checks identity integrity, not observation truth.
func Matches(ctx context.Context, expected Binding, persisted Receipt, actual *Inspected) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
	}()
	if err = contextError(ctx); err != nil {
		return err
	}
	if err = validBinding(expected); err != nil {
		return err
	}
	if actual == nil || actual.receipt == nil || actual.binding != expected || persisted.Resource != expected {
		return ErrMismatch
	}
	same, e := Equal(ctx, persisted, *actual.receipt)
	if e != nil {
		return e
	}
	if !same {
		return ErrMismatch
	}
	return nil
}
