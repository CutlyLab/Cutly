package nativepngsource

import (
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.source.verified-native-static-png.v1"
	ManifestSchemaVersion = 1

	SelectionProfileV1     = "cutly.selection.static-image-any-valid-source-time.v1"
	NormalizationProfileV1 = "cutly.normalization.png-native-top-down-square-pixel-no-exif.v1"

	DefaultMaxBindings                 = 256
	HardMaxBindings                    = 4096
	DefaultMaxClipScopes               = 65536
	HardMaxClipScopes                  = 1 << 20
	DefaultMaxDocumentNodes            = 262144
	HardMaxDocumentNodes               = 4 << 20
	DefaultMaxTracksPerTimeline        = 4096
	HardMaxTracksPerTimeline           = 65536
	DefaultMaxRetainedBytes     uint64 = 512 << 20
	HardMaxRetainedBytes        uint64 = 2 << 30
	DefaultMaxTotalEncoded      uint64 = 512 << 20
	HardMaxTotalEncoded         uint64 = 2 << 30
	DefaultMaxDocumentBytes     uint64 = 128 << 20
	HardMaxDocumentBytes        uint64 = 1 << 30
)

type Limits struct {
	MaxBindings               int              `json:"max_bindings"`
	MaxClipScopes             int              `json:"max_clip_scopes"`
	MaxDocumentNodes          int              `json:"max_document_nodes"`
	MaxTracksPerTimeline      int              `json:"max_tracks_per_timeline"`
	MaxRetainedBytes          uint64           `json:"max_retained_bytes"`
	MaxTotalEncodedBytes      uint64           `json:"max_total_encoded_bytes"`
	MaxCanonicalDocumentBytes uint64           `json:"max_canonical_document_bytes"`
	PNG                       pngdecode.Limits `json:"png"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxBindings: DefaultMaxBindings, MaxClipScopes: DefaultMaxClipScopes,
		MaxDocumentNodes: DefaultMaxDocumentNodes, MaxTracksPerTimeline: DefaultMaxTracksPerTimeline,
		MaxRetainedBytes: DefaultMaxRetainedBytes, MaxTotalEncodedBytes: DefaultMaxTotalEncoded,
		MaxCanonicalDocumentBytes: DefaultMaxDocumentBytes, PNG: pngdecode.DefaultLimits(),
	}
}

type Config struct {
	Limits Limits `json:"limits"`
}

func DefaultConfig() Config { return Config{Limits: DefaultLimits()} }

// ImageSourceProfiles returns the exact rastercompositor allowlist required by this provider.
func ImageSourceProfiles() rastercompositor.AssetSourceProfiles {
	return rastercompositor.AssetSourceProfiles{
		DecodeProfile: pngdecode.ProfileV1, SelectionProfile: SelectionProfileV1,
		ColorProfile: color.ProfileRGBA8SRGBV1, NormalizationProfile: NormalizationProfileV1,
	}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	DocumentSemantics        string `json:"document_semantics"`
	BindingSemantics         string `json:"binding_semantics"`
	RequestSemantics         string `json:"request_semantics"`
	StaticImageSemantics     string `json:"static_image_semantics"`
	DecodeProfile            string `json:"decode_profile"`
	SelectionProfile         string `json:"selection_profile"`
	ColorProfile             string `json:"color_profile"`
	NormalizationProfile     string `json:"normalization_profile"`
	SourceProfile            string `json:"source_profile"`
	SurfaceProfile           string `json:"surface_profile"`
	ManifestSemantics        string `json:"manifest_semantics"`
	EvidenceSemantics        string `json:"evidence_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	DocumentSemantics:        "Borrow one complete immutable Document v2 object graph only for Compile, conservatively preflight it, canonical-validate it, then deep-freeze and hash the one validated canonical representation. Build immutable request scopes only for enabled image-backed clips on video tracks; other valid asset kinds may coexist but are never served by this provider.",
	BindingSemantics:         "Require exactly one caller binding for every distinct enabled image source and no other binding. Every binding declares the exact frozen document hash and lends immutable encoded bytes only until Compile returns. The Document asset must carry canonical lowercase SHA-256 equal to every byte of the strict PNG; URI values are never opened or fetched.",
	RequestSemantics:         "Accept only the frozen document/timeline/track/clip/asset ID and array-index tuple, image kind and content hash, exact raster source/surface/decode/selection/color/normalization profiles, valid times whose local/source mapping equals the frozen clip equation, and the versioned native-source contract. No fallback or cross-document replay is accepted.",
	StaticImageSemantics:     "Return one immutable native-size surface for every request time that satisfies the frozen clip mapping. Timeline, local and source times are validated as scope evidence but never choose another image, edge hold, animation frame or retime algorithm.",
	DecodeProfile:            pngdecode.ProfileV1,
	SelectionProfile:         SelectionProfileV1,
	ColorProfile:             color.ProfileRGBA8SRGBV1,
	NormalizationProfile:     NormalizationProfileV1,
	SourceProfile:            rastercompositor.SourceProfileV1,
	SurfaceProfile:           cpucompositor.SurfaceProfileV1,
	ManifestSemantics:        "Return an AssetID-sorted defensive manifest binding document hash, asset ID/index, encoded and normalized-pixel hashes, native geometry, sRGB intent and every decode/selection/color/normalization/source/surface/hash profile.",
	EvidenceSemantics:        "The provider proves request consistency and actual immutable surface hash, not honest pixel derivation, worker identity, signature or publication. A containing canonical EvidenceManifest must bind this manifest, raster result, runtime and authorized resource resolution.",
	CancellationSemantics:    "Check cancellation before and throughout document preflight and scope indexing, before and after document freezing, throughout encoded hashing and strict PNG decode, between bindings, before request lookup and immediately before returning. Compile and Frame never return partial success.",
	AllocationSemantics:      "Before canonicalization preflight complete typed-document nodes, conservative encoded bytes and tracks per timeline; hash the one validated canonical byte representation without re-encoding it. Also preflight binding count, total encoded binding bytes and enabled-image scope count. Each strict PNG decode obeys its per-image working limit; after every decode, sum exact native width*height*8 retained surface bytes and reject above MaxRetainedBytes. Caller encoded storage and scheduler-wide concurrent work are excluded.",
	DefaultLimits:            DefaultLimits(),
	FailureSemantics:         "Any document, binding, hash, decode, profile, scope, time, limit or cancellation mismatch fails closed without a partial Provider or NativeSourceFrame.",
	ExcludedResponsibilities: "File/network resolution, authorization policy, video/APNG decode, alternate PNG profiles, EXIF orientation, non-square pixels, resize, crop, affine transform, opacity, compositing, masks, text/vector rasterization, output conversion, encoding, scheduling and publication are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
