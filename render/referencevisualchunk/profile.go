package referencevisualchunk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	SchemaVersion       = 1
	ProfileV1           = "cutly.reference-visual.host-bound-surface-provider.v1"
	DescriptorProfileV1 = "cutly.reference-visual.host-bound-surface-provider-descriptor.v1"
	MaxBindingBytes     = 64 << 10

	providerFixedMemoryBytes = uint64(1 << 20)
)

var (
	ErrConfiguration   = errors.New("referencevisualchunk: invalid configuration")
	ErrBinding         = errors.New("referencevisualchunk: invalid binding")
	ErrRequestMismatch = errors.New("referencevisualchunk: request does not match binding")
	ErrFrameRange      = errors.New("referencevisualchunk: frame is outside binding")
	ErrResourceLimit   = errors.New("referencevisualchunk: resource limit exceeded")
	ErrRender          = errors.New("referencevisualchunk: surface render failed")
	ErrCanceled        = errors.New("referencevisualchunk: canceled")
	ErrInvariant       = errors.New("referencevisualchunk: invariant failed")
)

type ProfileDescriptor struct {
	Profile                  string `json:"profile"`
	BindingProfile           string `json:"binding_profile"`
	CanonicalJSONProfile     string `json:"canonical_json_profile"`
	RequestSemantics         string `json:"request_semantics"`
	IdentitySemantics        string `json:"identity_semantics"`
	SurfaceSemantics         string `json:"surface_semantics"`
	ResourceSemantics        string `json:"resource_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	Profile:                  DescriptorProfileV1,
	BindingProfile:           ProfileV1,
	CanonicalJSONProfile:     canonicaljson.Profile,
	RequestSemantics:         "One Provider accepts only the exact plan fingerprint, execution-manifest fingerprint, timeline, complete RenderTarget and deterministic seed frozen into its Binding, and only composition frame indexes in the bound half-open range.",
	IdentitySemantics:        "The Binding fingerprint covers the complete request scope plus the Program profile, DocumentHash, source-manifest fingerprint, normalized Config fingerprint and a caller-declared implementation fingerprint. It is deterministic integrity metadata, not executable attestation, and it does not prove that legacy plan fingerprints derive from the Document v2 Program.",
	SurfaceSemantics:         "RenderSurface uses referencevisual.RenderSurfaceFrame and returns its immutable canonical linear-BT.709 premultiplied RGBA16 Surface only after concrete-provider replay, exact receipt comparison and an independently cancelable pixel hash check.",
	ResourceSemantics:        "VideoResources reserves the Program's complete declared runtime-working ceiling plus fixed adapter overhead, and twice the largest fixed-64-frame canonical surface payload plus CAS publication metadata. Checked arithmetic fails construction before proportional work.",
	FailureSemantics:         "Nil context, malformed identity, request drift, out-of-range frames, cancellation, panic, render mismatch and arithmetic overflow return no Surface. No fallback profile, partial surface or transient classification is invented.",
	ExcludedResponsibilities: "Durable Document v2 plan/manifest/prepared storage, Program reconstruction, media authorization, audio/PCM, codec assembly, publication, worker sandboxing, signatures and distributed scheduling are outside this host-bound embedding adapter.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

func DescriptorHash() string {
	raw, err := canonicaljson.Marshal(descriptorV1)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
