package referenceimage

import (
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.reference-image.static-png-export.v1"
	ResultSchemaVersion  = 1
)

// ProfileDescriptor documents the exact assembly boundary. BuildFingerprint is an optional
// caller-supplied reproducibility label; it is neither verified worker identity nor attestation.
type ProfileDescriptor struct {
	SchemaVersion            int                     `json:"schema_version"`
	Profile                  string                  `json:"profile"`
	SourceProfile            string                  `json:"source_profile"`
	CompositorProfile        string                  `json:"compositor_profile"`
	OutputColorProfile       string                  `json:"output_color_profile"`
	PNGProfile               string                  `json:"png_profile"`
	CompileSemantics         string                  `json:"compile_semantics"`
	RenderSemantics          string                  `json:"render_semantics"`
	ReceiptSemantics         string                  `json:"receipt_semantics"`
	BuildIdentitySemantics   string                  `json:"build_identity_semantics"`
	FailureSemantics         string                  `json:"failure_semantics"`
	ExcludedResponsibilities string                  `json:"excluded_responsibilities"`
	DefaultSourceLimits      nativepngsource.Limits  `json:"default_source_limits"`
	DefaultCompositorLimits  rastercompositor.Limits `json:"default_compositor_limits"`
	DefaultOutputLimits      color.OutputLimits      `json:"default_output_limits"`
	DefaultPNGLimits         pngencode.Limits        `json:"default_png_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	SourceProfile:            nativepngsource.ProfileV1,
	CompositorProfile:        rastercompositor.ProfileV1,
	OutputColorProfile:       color.ProfileLinearRGBA16ToRGBA8SRGBV1,
	PNGProfile:               pngencode.ProfileV1,
	CompileSemantics:         "Borrow one complete immutable Document v2 object graph and caller PNG bindings only until Compile returns; capability-check and deep-freeze the document, compute its canonical hash in this assembly, strictly decode every enabled image binding, and publish one immutable concurrent Program only after all stages agree.",
	RenderSemantics:          "Evaluate one exact timeline time or composition frame, request only strict native static PNG surfaces, apply evaluator transform/crop/opacity/keyframes/dissolve and multitrack order in the deterministic CPU raster compositor, convert the complete target surface to straight-alpha sRGB RGBA8, then encode the deterministic strict PNG profile.",
	ReceiptSemantics:         "Bind the complete source manifest, ordered compositor source receipts, raster receipt, output-color receipt and PNG receipt. Recompute accessible surface/frame/artifact hashes and cross-check geometry and semantic profiles before returning.",
	BuildIdentitySemantics:   "BuildFingerprint is frozen and echoed exactly when supplied. Empty means unstamped; any non-empty value is an unverified reproducibility label, not authorization, signature, provenance, attestation, or proof of worker identity.",
	FailureSemantics:         "Any context, configuration, document, binding, resource, stage, profile, geometry or hash mismatch fails closed with no Program or a zero render result. Compositor diagnostics are retained and defensively copied.",
	ExcludedResponsibilities: "Video/APNG/audio decode, text/vector rasterization, filesystem or network resolution, authorization, signatures, trusted build identity, scheduling, persistence, publication and a public compatibility guarantee are outside this static PNG reference vertical slice.",
	DefaultSourceLimits:      nativepngsource.DefaultLimits(),
	DefaultCompositorLimits:  rastercompositor.DefaultLimits(),
	DefaultOutputLimits:      color.DefaultOutputLimits(),
	DefaultPNGLimits:         pngencode.DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
