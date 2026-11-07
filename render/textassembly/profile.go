package textassembly

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

const (
	SchemaVersion              = 1
	ProfileV1                  = "cutly.render.text-assembly.static-glyf.v1"
	ReceiptProfileV1           = "cutly.render.text-assembly.static-glyf.receipt.v1"
	DescriptorHashProfileV1    = "cutly.hash.sha256.text-assembly-descriptor-json.v1"
	AssemblyHashProfileV1      = "cutly.hash.sha256.text-assembly-binding-be.v1"
	ShapingProvenanceStatusV1  = "cutly.text-shaping-provenance.provider-authenticated-by-assembly.v1"
	PaintProvenanceStatusV1    = "cutly.text-paint-provenance.frozen-source-static-fill.v1"
	CapabilityStatusV1         = "provider-authenticated-static-text-layout-ready-for-private-raster"
	RasterReceiptProfileV1     = "cutly.render.text-assembly.static-raster.receipt.v1"
	RasterBindingHashProfileV1 = "cutly.hash.sha256.text-assembly-static-raster-binding-be.v1"
)

type ProfileDescriptor struct {
	SchemaVersion                int          `json:"schema_version"`
	Profile                      string       `json:"profile"`
	ReceiptProfile               string       `json:"receipt_profile"`
	RasterReceiptProfile         string       `json:"raster_receipt_profile"`
	DescriptorHashProfile        string       `json:"descriptor_hash_profile"`
	AssemblyHashProfile          string       `json:"assembly_hash_profile"`
	RasterBindingHashProfile     string       `json:"raster_binding_hash_profile"`
	FrozenTextProfile            string       `json:"frozen_text_profile"`
	ShapeProviderProfile         string       `json:"shape_provider_profile"`
	LayoutSourceProfile          string       `json:"layout_source_profile"`
	LayoutProfile                string       `json:"layout_profile"`
	RasterProfile                string       `json:"raster_profile"`
	InnerShapingProvenanceStatus string       `json:"inner_shaping_provenance_status"`
	ShapingProvenanceStatus      string       `json:"shaping_provenance_status"`
	PaintProvenanceStatus        string       `json:"paint_provenance_status"`
	CapabilityStatus             string       `json:"capability_status"`
	DescriptorHashSemantics      string       `json:"descriptor_hash_semantics"`
	ReceiptSemantics             string       `json:"receipt_semantics"`
	BindingEncoding              string       `json:"binding_encoding"`
	AssemblySemantics            string       `json:"assembly_semantics"`
	RasterSemantics              string       `json:"raster_semantics"`
	OwnershipSemantics           string       `json:"ownership_semantics"`
	FailureSemantics             string       `json:"failure_semantics"`
	ResourceSemantics            string       `json:"resource_semantics"`
	CancellationSemantics        string       `json:"cancellation_semantics"`
	DefaultLimits                Limits       `json:"default_limits"`
	DefaultRasterLimits          RasterLimits `json:"default_raster_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion: SchemaVersion, Profile: ProfileV1, ReceiptProfile: ReceiptProfileV1,
	RasterReceiptProfile: RasterReceiptProfileV1, DescriptorHashProfile: DescriptorHashProfileV1,
	AssemblyHashProfile: AssemblyHashProfileV1, RasterBindingHashProfile: RasterBindingHashProfileV1,
	FrozenTextProfile: evaluator.FrozenTextSourceProfileV1, ShapeProviderProfile: sfntglyf.ShapeProviderProfileV1,
	LayoutSourceProfile: sfntglyf.LayoutSourceProfileV1, LayoutProfile: textlayout.ProfileV1,
	RasterProfile: textraster.ProfileV1, InnerShapingProvenanceStatus: textraster.ShapingProvenanceStatusV1,
	ShapingProvenanceStatus: ShapingProvenanceStatusV1,
	PaintProvenanceStatus:   PaintProvenanceStatusV1,
	CapabilityStatus:        CapabilityStatusV1,
	DescriptorHashSemantics: "Canonical descriptor bytes are the exact minified encoding/json output of this ordered map-free Go struct, not videoedit.CanonicalJSON or JCS. DescriptorHash is SHA-256 over u64be(len(descriptor-hash-profile)), exact profile UTF-8, u64be(len(those bytes)), and those exact bytes.",
	ReceiptSemantics:        "Receipt and RasterReceipt are unsigned serialized structural evidence and never authorize rendering by themselves. Only a live opaque Source returned by Assemble carries the private Layout and LayoutSource required by RasterizeStatic. Hash profiles must travel with every digest.",
	BindingEncoding:         "str is u64be UTF-8 byte length followed by exact bytes; u16/u32/u64 are unsigned big-endian and i64 is its two's-complement u64 bit pattern. AssemblyHash order: domain, profile schema, receipt schema; profile, descriptor hash profile/value, assembly hash profile; frozen text profile/binding hash profile/value, document hash profile/value, text asset ID, font asset ID, font content hash profile/value, semantic hash profile/value, layout-input hash profile/value; provider profile/hash profile/value, dependency fingerprint profile/value, layout-graph hash profile/value, source-outline hash profile/value, layout-source hash profile/value; shape input hash profile/value, shape output profile/hash profile/value, layout receipt profile, glyph-stream hash profile/value, scaled-outline hash profile/value, placement hash profile/value, layout hash profile/value, shaping/paint/capability statuses; text/font indices, viewport, font size, fill, counts and resources. AssemblyHash itself is omitted. Raster BindingHash order: domain, profile schema, receipt schema; profile, descriptor hash profile/value, binding hash profile, assembly hash profile/value, inner receipt/raster/binding-hash profiles and binding hash, inner/outer shaping status, paint status, coverage hash profile/value, pixel hash profile/value, dimensions and resources. BindingHash itself is omitted.",
	AssemblySemantics:       "Accept only an opaque live-replayed FrozenTextSource and a live ShapeProvider. Bind the provider's complete LayoutSource first, shape only the frozen text/run fields, cross-check every font/provider/graph/outline identity, derive the exact glyph closure, and privately build the deterministic Layout.",
	RasterSemantics:         "RasterizeStatic is the only raster outlet. It supplies the package-private authenticated Layout and frozen static fill to textraster. The inner receipt remains caller-layout-unverified; the outer receipt authenticates shaping because callers cannot inject or retrieve that Layout.",
	OwnershipSemantics:      "A Source retains only opaque immutable trust roots, one immutable Layout, a scalar fill, and a scalar receipt. It exposes no shaped stream, outline, Layout, LayoutSource, slices, maps, readers, or low-level raster request.",
	FailureSemantics:        "Nil, zero, mismatched or drifted trust roots, unsupported profiles, missing glyphs, malformed closure geometry, resource exhaustion, cancellation and panic fail closed with nil output and a strictly zero receipt.",
	ResourceSemantics:       "Let L=lines,G=glyphs,U=unique glyphs,C=commands,Q=command point occurrences,A=AssemblyHash preimage bytes,S=owned Receipt string bytes. BridgeWorking=65536+24L+84G+32U+48C+16Q and BridgeWork=L+8G+8U+U*bits.Len64(U)+4C+2Q+A. Assembly CanonicalBinaryBytes=LayoutSource.canonical+Shape.canonical+Layout.canonical+A; WorkUnits=LayoutSource.work+Shape.work+Layout.work+BridgeWork; RetainedBytes=FrozenText.retained+LayoutSource.retained+Layout.retained+4096+S+71; WorkingBytes=max(RetainedBytes,FrozenText.retained+LayoutSource.working+Shape.working+Layout.working+BridgeWorking). Child limits are capped before calls; map/scan pre-admission uses the safe G/U and source/layout-bounded C/Q envelope. For static raster with binding preimage R and owned RasterReceipt strings Z: canonical=inner.canonical+R, work=inner.work+R, retained=inner.retained+4096+Z+71, working=max(inner.working,retained). The wrapper precomputes exact inner canonical bytes while separately admitting textraster's deterministic 9*pixel_count+16384 inner upper. All arithmetic is checked uint64; limits never select lower quality.",
	CancellationSemantics:   "A non-nil context is checked before validation, around every provider child call, periodically inside line/glyph/outline/command/materialization loops, during every outer hash, and immediately before publication. Cancellation and unexpected panic return nil output and a strictly zero receipt; recovered values are never formatted or otherwise executed.",
	DefaultLimits:           DefaultLimits(), DefaultRasterLimits: DefaultRasterLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

func DescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(descriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var descriptorHashV1 = computeDescriptorHash()

func DescriptorHash() string { return descriptorHashV1 }

func computeDescriptorHash() string {
	canonical := DescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(DescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(DescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
