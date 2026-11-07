// Package resourceidentityv2 binds concrete native inspections to independently
// authorized Document v2 resource expectations. Wire receipts are identity claims,
// never proof of content, locator authority, outline validity or shaping capability.
// This is an S3 prerequisite, not reconstruction or an S4 provider bundle.
package resourceidentityv2

import (
	"errors"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	SchemaVersion   = 2
	Profile         = "cutly.resource-inspection.document-v2.v2"
	MaxReceiptBytes = 2 << 20
	MaxAssetIDBytes = 1024
)

var (
	ErrInvalid     = errors.New("resourceidentityv2: invalid identity")
	ErrLimit       = errors.New("resourceidentityv2: admission limit exceeded")
	ErrMismatch    = errors.New("resourceidentityv2: binding or observation mismatch")
	ErrUnsupported = errors.New("resourceidentityv2: unsupported resource kind")
	ErrContext     = errors.New("resourceidentityv2: nil context")
	ErrPanic       = errors.New("resourceidentityv2: operation panicked")
)

// Binding must come from the host's independent authorization, not a receipt or
// materialized-resource claim. No locator or filesystem authority is represented.
type Binding struct {
	AssetID     documentv2.ID        `json:"asset_id"`
	Kind        documentv2.AssetKind `json:"kind"`
	ContentHash string               `json:"content_hash"`
	SizeBytes   int64                `json:"size_bytes"`
}

type Inspector struct {
	Profile               string `json:"profile"`
	DescriptorFingerprint string `json:"descriptor_fingerprint"`
}

// PNG is the complete semantic projection of pngdecode.Result, excluding its live
// immutable Surface. PixelHash alone deliberately does not bind geometry/profiles.
type PNG struct {
	Width                uint32 `json:"width"`
	Height               uint32 `json:"height"`
	SRGBRenderingIntent  uint8  `json:"srgb_rendering_intent"`
	EncodedContentHash   string `json:"encoded_content_hash"`
	PixelHash            string `json:"pixel_hash"`
	DecodeProfile        string `json:"decode_profile"`
	NormalizationProfile string `json:"normalization_profile"`
	SurfaceProfile       string `json:"surface_profile"`
	ContentHashProfile   string `json:"content_hash_profile"`
}

// Receipt has exactly one kind-specific payload. Font is the strict sfnt/glyf
// CORE receipt: glyf bodies and layout remain explicitly opaque, not authorized.
// No live Font or Surface is serialized. Font portable workload charges ARE
// included: they are deterministic functions of bytes and the pinned parser
// profile, not host limits or measured allocator/RSS counters. Host Limits never
// enter a receipt. Empty arrays must be non-nil; absent payloads must be nil.
type Receipt struct {
	SchemaVersion        uint32            `json:"schema_version"`
	Profile              string            `json:"profile"`
	CanonicalJSONProfile string            `json:"canonical_json_profile"`
	Resource             Binding           `json:"resource"`
	Inspector            Inspector         `json:"inspector"`
	PNG                  *PNG              `json:"png,omitempty"`
	Font                 *sfntglyf.Receipt `json:"font,omitempty"`
	Fingerprint          string            `json:"fingerprint"`
}

// Verified is owned, verified wire identity only. It does not prove an inspection
// took place. Only InspectPNG/InspectFont/InspectAudio produce Inspected values.
type Verified struct {
	receipt   *Receipt
	canonical []byte
}

func (v Verified) Receipt() (Receipt, error) {
	if v.receipt == nil {
		return Receipt{}, ErrInvalid
	}
	return cloneReceipt(*v.receipt), nil
}
func (v Verified) CanonicalJSON() ([]byte, error) {
	if v.receipt == nil {
		return nil, ErrInvalid
	}
	return append([]byte(nil), v.canonical...), nil
}

// Inspected is an opaque successful observation of actual bytes. The immutable
// Surface/Font may be reused in future host-owned reconstruction. A decoded wire
// receipt cannot construct this value. Audio retains only the existing native
// probe, not encoded bytes or decoded PCM. Zero values are unusable.
type Inspected struct {
	binding     Binding
	receipt     *Receipt
	surface     *cpucompositor.Surface
	font        *sfntglyf.Font
	audio       *probe.ProbeManifest
	fingerprint string
}

func (i *Inspected) Binding() (Binding, error) {
	if i == nil || i.fingerprint == "" {
		return Binding{}, ErrInvalid
	}
	return i.binding, nil
}
func (i *Inspected) Receipt() (Receipt, error) {
	if i == nil || i.receipt == nil {
		return Receipt{}, ErrInvalid
	}
	return cloneReceipt(*i.receipt), nil
}

// Surface returns an independently owned struct header over immutable private
// pixels. Overwriting the returned exported struct cannot replace the retained
// observation. Surface has no locks or other noncopyable fields, and its public
// APIs expose no mutable interior storage.
func (i *Inspected) Surface() (*cpucompositor.Surface, error) {
	if i == nil || i.surface == nil {
		return nil, ErrInvalid
	}
	surface := *i.surface
	return &surface, nil
}

// Font returns an independently owned struct header over immutable private
// encoded/table/cmap storage. Font has no locks or other noncopyable fields, and
// its public APIs return values or defensive metadata copies. Replacing or
// zeroing this header cannot replace the retained observation.
func (i *Inspected) Font() (*sfntglyf.Font, error) {
	if i == nil || i.font == nil {
		return nil, ErrInvalid
	}
	font := *i.font
	return &font, nil
}
func (i *Inspected) Probe() (probe.ProbeManifest, error) {
	if i == nil || i.audio == nil {
		return probe.ProbeManifest{}, ErrInvalid
	}
	p := *i.audio
	p.Streams = append([]probe.Stream(nil), p.Streams...)
	for n := range p.Streams {
		s := &p.Streams[n]
		if s.DurationPTS != nil {
			x := *s.DurationPTS
			s.DurationPTS = &x
		}
		if s.Audio != nil {
			x := *s.Audio
			s.Audio = &x
		}
	}
	return p, nil
}
func cloneReceipt(r Receipt) Receipt {
	if r.PNG != nil {
		p := *r.PNG
		r.PNG = &p
	}
	if r.Font != nil {
		f := *r.Font
		f.Tables = append([]sfntglyf.TableReceipt{}, f.Tables...)
		f.OpaqueLayoutTables = append([]string{}, f.OpaqueLayoutTables...)
		r.Font = &f
	}
	return r
}
