package referencevisual

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.reference-visual.native-png-vector-expression-export.v1"
	ManifestSchemaVersion = 1
	ManifestProfileV1     = "cutly.reference-visual.source-manifest.v1"
	ResultSchemaVersion   = 1
)

type ProfileDescriptor struct {
	SchemaVersion                int                     `json:"schema_version"`
	Profile                      string                  `json:"profile"`
	NativeSourceProfile          string                  `json:"native_source_profile"`
	ExpressionSourceProfile      string                  `json:"expression_source_profile"`
	ExpressionHandleProfile      string                  `json:"expression_handle_profile"`
	VectorTransitionScopeProfile string                  `json:"vector_transition_scope_profile"`
	CompositorProfile            string                  `json:"compositor_profile"`
	OutputColorProfile           string                  `json:"output_color_profile"`
	PNGProfile                   string                  `json:"png_profile"`
	CompileSemantics             string                  `json:"compile_semantics"`
	RenderSemantics              string                  `json:"render_semantics"`
	ManifestSemantics            string                  `json:"manifest_semantics"`
	ReceiptSemantics             string                  `json:"receipt_semantics"`
	BuildIdentitySemantics       string                  `json:"build_identity_semantics"`
	ResourceSemantics            string                  `json:"resource_semantics"`
	OwnershipSemantics           string                  `json:"ownership_semantics"`
	FailureSemantics             string                  `json:"failure_semantics"`
	ExcludedResponsibilities     string                  `json:"excluded_responsibilities"`
	DefaultNativeLimits          nativepngsource.Limits  `json:"default_native_limits"`
	DefaultExpressionLimits      expressionlayer.Limits  `json:"default_expression_limits"`
	DefaultCompositorLimits      visualcompositor.Limits `json:"default_compositor_limits"`
	DefaultOutputLimits          color.OutputLimits      `json:"default_output_limits"`
	DefaultPNGLimits             pngencode.Limits        `json:"default_png_limits"`
	DefaultPipelineLimits        PipelineLimits          `json:"default_pipeline_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:                ProfileSchemaVersion,
	Profile:                      ProfileV1,
	NativeSourceProfile:          nativepngsource.ProfileV1,
	ExpressionSourceProfile:      expressionlayer.ProfileV1,
	ExpressionHandleProfile:      expressionlayer.TransitionHandleProfileV2,
	VectorTransitionScopeProfile: documentv2.VectorTransitionScopeProfileV1,
	CompositorProfile:            visualcompositor.ProfileV1,
	OutputColorProfile:           color.ProfileLinearRGBA16ToRGBA8SRGBV1,
	PNGProfile:                   pngencode.ProfileV1,
	CompileSemantics:             "Compile one visual-only Document v2 into the mixed native-image/vector compositor, independently freeze the same canonical DocumentHash, then compile strict PNG and expression providers. Publish only after both provider manifests exactly match enabled frozen assets and every stage agrees on identity and configuration.",
	RenderSemantics:              "Evaluate one exact timeline time or composition frame, mix native PNG and viewport-vector expression surfaces through the shared evaluator and CPU visual compositor, convert the complete linear RGBA16 target to straight sRGB RGBA8, then emit the deterministic stored-DEFLATE PNG profile.",
	ManifestSemantics:            "Bind the one DocumentHash to defensive native-PNG and expression manifests plus the explicit transition-handle contract and scoped vector-transition capability. Asset IDs/indexes/kinds/hashes/viewports are checked against the same frozen document.",
	ReceiptSemantics:             "Bind the source manifest, ordered strict native/expression compositor receipts, live target Surface hash, output-color frame hash and final PNG artifact hash. Provider requests are replayed before output conversion. Serializable receipts alone are unsigned structural claims and do not authenticate offline pixel provenance.",
	BuildIdentitySemantics:       "BuildFingerprint is a caller-supplied, format-limited reproducibility label frozen and echoed exactly across the visual and wrapper receipts. It is not authorization, worker identity, a signature, provenance, attestation or proof that two executables are equivalent.",
	ResourceSemantics:            "Each dependency enforces its own hard limits. This assembly additionally applies an overflow-checked logical accounting model per Program compile and per render: retained intrinsic surface bytes, conservative document metadata, canonical/provider compile work, and the maximum sequential visual/color/PNG stage peak must fit explicit pipeline budgets. The model excludes caller-owned Document and encoded binding storage, caller-owned live Result objects supplied to strict validation, Go allocator/runtime overhead and RSS, scheduler-global state, and multiplication by concurrent renders.",
	OwnershipSemantics:           "Compile borrows the caller Document, binding slice and PNG bytes only until return. A successful Program retains immutable dependency programs/providers and value receipts; accessors return defensive slices and concurrent arbitrary-order renders are safe.",
	FailureSemantics:             "Any context, configuration, document, binding, resource, provider, profile, geometry, time, hash or receipt mismatch fails closed with nil Program or a zero Result. No partial Surface, output Frame or PNG Artifact is returned.",
	ExcludedResponsibilities:     "Temporal video, text, audio, vector strokes/curves, external resources, filesystem/network authorization, signatures, publication, scheduling, cache, muxing and a public compatibility guarantee are outside this v1 pipeline.",
	DefaultNativeLimits:          nativepngsource.DefaultLimits(),
	DefaultExpressionLimits:      expressionlayer.DefaultLimits(),
	DefaultCompositorLimits:      visualcompositor.DefaultLimits(),
	DefaultOutputLimits:          color.DefaultOutputLimits(),
	DefaultPNGLimits:             pngencode.DefaultLimits(),
	DefaultPipelineLimits:        DefaultPipelineLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
