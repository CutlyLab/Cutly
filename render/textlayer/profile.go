package textlayer

import (
	"encoding/json"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
)

const (
	ProfileSchemaVersion  = 1
	ManifestSchemaVersion = 1
	ProfileV1             = "cutly.provider.text-layer.pinned-font-static-surface.v1"
	SourceProfileV1       = "cutly.source.text-layer-intrinsic-surface.v1"
	SelectionProfileV1    = "cutly.selection.static-text-visible-time.v1"
	SurfaceHashProfileV1  = "cutly.hash.sha256.surface-rgba16be-pixels.v1"
)

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	ContractVersion          int    `json:"contract_version"`
	SourceProfile            string `json:"source_profile"`
	SelectionProfile         string `json:"selection_profile"`
	FrozenTextProfile        string `json:"frozen_text_profile"`
	TextProfile              string `json:"text_profile"`
	EvaluatorProfile         string `json:"evaluator_profile"`
	AssemblyProfile          string `json:"assembly_profile"`
	RasterProfile            string `json:"raster_profile"`
	PaintProfile             string `json:"paint_profile"`
	SurfaceProfile           string `json:"surface_profile"`
	SurfaceHashProfile       string `json:"surface_hash_profile"`
	DocumentSemantics        string `json:"document_semantics"`
	BindingSemantics         string `json:"binding_semantics"`
	StaticSubsetSemantics    string `json:"static_subset_semantics"`
	RequestSemantics         string `json:"request_semantics"`
	ResponseSemantics        string `json:"response_semantics"`
	ManifestSemantics        string `json:"manifest_semantics"`
	ResourceSemantics        string `json:"resource_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	OwnershipSemantics       string `json:"ownership_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
	DefaultLimits            Limits `json:"default_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion: ProfileSchemaVersion, Profile: ProfileV1, ContractVersion: ContractVersion,
	SourceProfile: SourceProfileV1, SelectionProfile: SelectionProfileV1,
	FrozenTextProfile: evaluator.FrozenTextSourceProfileV1,
	TextProfile:       documentv2.TextProfilePinnedFontViewportPXV2,
	EvaluatorProfile:  documentv2.EvaluatorTextPinnedFontViewportPXV2,
	AssemblyProfile:   textassembly.ProfileV1, RasterProfile: documentv2.TextRasterProfileSupersample8x8V1,
	PaintProfile: color.ProfileRGBA16SRGBPaintV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	SurfaceHashProfile:       SurfaceHashProfileV1,
	DocumentSemantics:        "Freeze one complete validated Document v2 through evaluator.FreezeSnapshot without claiming backend capabilities. Index only enabled Text-track clips and bind every request to the exact canonical DocumentHash, IDs, indexes, immutable text identities and visible time mapping.",
	BindingSemantics:         "Require an explicit DocumentHash plus FontAssetID to live ShapeProvider authorization for every distinct font used by enabled Text assets. Duplicate, missing, extra, cross-document, wrong-ID and wrong-content bindings fail; equal font hashes never create an implicit authorization edge.",
	StaticSubsetSemantics:    "Accept only cutly.text.pinned-font-viewport-px.v2 with its frozen static fill. Reject every text.fill keyframe lane, every nonzero transition involving a Text track, legacy text, unsupported profiles and affects-rendering extension properties. Transform, crop and opacity remain compositor responsibilities.",
	RequestSemantics:         "Require the exact frozen timeline/track/clip/asset scope; binding, semantic, layout and font identities; viewport; profile chain; and evaluator-equivalent timeline, local and source times inside the clip's half-open visible interval.",
	ResponseSemantics:        "Echo the complete Request and return only a provider-owned immutable intrinsic Surface, the provider-authenticated textassembly receipt, static-raster receipt and independently rechecked canonical pixel identity.",
	ManifestSemantics:        "Return a defensive AssetID-sorted list with exactly one derivation record per distinct enabled Text asset. Multiple clips referencing one asset share one immutable cached surface; no cross-AssetID hash deduplication occurs.",
	ResourceSemantics:        "Bound bindings, clip scopes, text/font assets, tracks, complete document nodes and canonical bytes before source binding. Pre-admit the overflow-checked sum of width*height*8 cached surfaces; each frozen-source, assembly and raster stage has explicit nested limits; total compile working evidence is checked without retaining temporary trust roots.",
	CancellationSemantics:    "Check a non-nil context before and throughout freezing, indexing, authorization, every sequential bind/assemble/raster/hash stage, request validation and publication. Cancellation discards all local state and returns nil Provider or a zero Frame.",
	OwnershipSemantics:       "Compile borrows Document and ShapeProvider values only until return. A successful Provider retains value-only clip scopes and receipts plus immutable Surfaces; it retains no font provider, FrozenTextSource, textassembly.Source, layout, outline, shaped stream, raw font bytes, maps supplied by callers or mutable cache.",
	FailureSemantics:         "Invalid document, subset, binding, profile, request, resource, child receipt, surface identity, cancellation or panic fails closed. Compile returns no partial Provider and Frame returns a strictly zero value on failure.",
	ExcludedResponsibilities: "Dynamic text paint, rich text, fallback/system fonts, bidi inference, transitions, transform/crop/opacity composition, output color conversion, encoding, signing, scheduling, persistence and publication are outside this provider.",
	DefaultLimits:            DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

func DescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(descriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}
