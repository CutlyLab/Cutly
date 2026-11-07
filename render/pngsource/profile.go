package pngsource

import (
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/referencecompositor"
)

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.source.verified-static-png.v1"
	ManifestSchemaVersion = 1

	DefaultMaxBindings             = 256
	HardMaxBindings                = 4096
	DefaultMaxClipScopes           = 65536
	HardMaxClipScopes              = 1 << 20
	DefaultMaxRetainedBytes uint64 = 512 << 20
	HardMaxRetainedBytes    uint64 = 2 << 30
)

type Limits struct {
	MaxBindings      int              `json:"max_bindings"`
	MaxClipScopes    int              `json:"max_clip_scopes"`
	MaxRetainedBytes uint64           `json:"max_retained_bytes"`
	PNG              pngdecode.Limits `json:"png"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxBindings: DefaultMaxBindings, MaxClipScopes: DefaultMaxClipScopes,
		MaxRetainedBytes: DefaultMaxRetainedBytes,
		PNG:              pngdecode.DefaultLimits(),
	}
}

type Config struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Limits Limits `json:"limits"`
}

func DefaultConfig(width, height int) Config {
	return Config{Width: width, Height: height, Limits: DefaultLimits()}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	DocumentSemantics        string `json:"document_semantics"`
	BindingSemantics         string `json:"binding_semantics"`
	RequestSemantics         string `json:"request_semantics"`
	StaticImageSemantics     string `json:"static_image_semantics"`
	DecodeProfile            string `json:"decode_profile"`
	SourceProfile            string `json:"source_profile"`
	SurfaceProfile           string `json:"surface_profile"`
	ReceiptSemantics         string `json:"receipt_semantics"`
	EvidenceSemantics        string `json:"evidence_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	DocumentSemantics:        "Canonical-validate, deep-freeze and hash one complete Document v2. Every enabled clip on a video track must reference an image asset; referenced image IDs and exact timeline/track/clip indexes are frozen as the only authorized request scopes.",
	BindingSemantics:         "Require exactly one caller binding for every distinct referenced image asset and no other binding. Every binding declares and exactly matches the frozen document hash. Each Document asset must contain a canonical lowercase sha256 content_hash, which must equal the hash of the complete strict PNG bytes. URI values are never opened or fetched.",
	RequestSemantics:         "Accept only the frozen document hash, authorized timeline/track/clip/asset/index tuple, valid exact time values, frozen target dimensions, reference-compositor source profile and CPU surface profile. Any mismatch fails without fallback.",
	StaticImageSemantics:     "A verified image is one immutable surface for every valid SourceTime of each authorized clip. SourceTime is validated but never used for temporal selection, edge holding or retiming.",
	DecodeProfile:            pngdecode.ProfileV1,
	SourceProfile:            referencecompositor.SourceProfileV1,
	SurfaceProfile:           cpucompositor.SurfaceProfileV1,
	ReceiptSemantics:         "Return a deterministic AssetID-sorted manifest binding document hash, asset identity, encoded-content and normalized-pixel hashes, geometry, sRGB intent and every decode/color/source/surface profile. Manifest slices are defensive copies.",
	EvidenceSemantics:        "The manifest is a deterministic provider record, not a signature, worker attestation or complete render proof. A caller must bind it together with the compositor result, execution identity and authorized resource resolution in an EvidenceManifest before treating it as evidence.",
	CancellationSemantics:    "Check cancellation before and after document freezing, before every binding decode, inside strict PNG decode/normalization, after each decode and before publishing the complete provider. Request service checks before lookup and before returning a surface. Compile never returns a partial provider.",
	AllocationSemantics:      "Bound enabled video-clip request scopes and preflight binding count plus targetWidth*targetHeight*8*distinctAssetCount retained surface bytes before decode. Because every image must match the target, also prove the pngdecode-v1 peak width*height*20+2*(1+width*4) fits its frozen per-image working limit before reading bindings. Retained surfaces are immutable and shared by concurrent reads; scheduler-wide concurrent memory remains separate.",
	DefaultLimits:            DefaultLimits(),
	ExcludedResponsibilities: "URI resolution, network/file authorization, video or animated-image decode, alternate PNG profiles, resize, crop, transform, rotation/SAR, masks, text/vector rasterization, output conversion, encoding, scheduling and publication are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
