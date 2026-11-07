package referencevisual

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const (
	ProfileSchemaVersion  = 1
	ProfileV1             = "cutly.reference-visual.native-png-vector-expression-export.v1"
	ProfileV2             = "cutly.reference-visual.native-png-vector-expression-text-export.v2"
	ManifestSchemaVersion = 1
	ManifestProfileV1     = "cutly.reference-visual.source-manifest.v1"
	ManifestProfileV2     = "cutly.reference-visual.source-manifest.v2"
	ResultSchemaVersion   = 1
)

type ProfileDescriptor struct {
	SchemaVersion                int                     `json:"schema_version"`
	Profile                      string                  `json:"profile"`
	NativeSourceProfile          string                  `json:"native_source_profile"`
	ExpressionSourceProfile      string                  `json:"expression_source_profile"`
	ExpressionHandleProfile      string                  `json:"expression_handle_profile"`
	VectorTransitionScopeProfile string                  `json:"vector_transition_scope_profile"`
	TextSourceProfile            string                  `json:"text_source_profile,omitempty"`
	TextContractVersion          int                     `json:"text_contract_version,omitempty"`
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
	DefaultTextConfig            *textlayer.Config       `json:"default_text_config,omitempty"`
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

var descriptorV2 = func() ProfileDescriptor {
	value := descriptorV1
	textConfig := defaultTextConfigV2()
	value.Profile = ProfileV2
	value.TextSourceProfile = textlayer.ProfileV1
	value.TextContractVersion = textlayer.ContractVersion
	value.CompositorProfile = visualcompositor.ProfileV2
	value.CompileSemantics = "Compile one visual Document v2 through the additive native-image/vector/static-Text compositor, independently freeze the same canonical DocumentHash, then compile strict PNG, expression and pinned-font Text providers. Publish only after all provider manifests exactly match enabled frozen assets, explicit font authorization and every stage identity/configuration cross-binding."
	value.RenderSemantics = "Evaluate one exact timeline time or composition frame, mix native PNG, viewport-vector expression and pinned-font static Text surfaces in frozen background-to-foreground order, apply compositor transform/crop/opacity, convert the complete linear RGBA16 target to straight sRGB RGBA8, then emit the deterministic stored-DEFLATE PNG profile."
	value.ManifestSemantics = "Bind one DocumentHash to defensive native-PNG, expression and Text manifests plus the transition-handle and scoped vector-transition contracts. Asset IDs/indexes/kinds/hashes/viewports, font identities and retained-surface accounting are checked against the same frozen document without cross-kind aliasing."
	value.ReceiptSemantics = "Bind the source manifest, ordered strict native/expression/Text compositor receipts, live target Surface hash, output-color frame hash and final PNG artifact hash. Every concrete provider request is replayed before output conversion; serialized receipts are unsigned structural claims and never offline pixel provenance."
	value.ResourceSemantics = "Apply every dependency hard limit plus overflow-checked aggregate retained-source, document-metadata, canonical/provider compile and sequential visual/color/PNG runtime budgets. Text compile accounting includes already-retained native/expression surfaces and the Text provider's complete compile-working envelope."
	value.OwnershipSemantics = "CompileV2 borrows the caller Document, PNG bindings, font bindings, encoded PNG bytes and ShapeProviders only until return. A successful Program retains immutable dependency programs/providers, intrinsic source Surfaces and value receipts; accessors are defensive and concurrent arbitrary-order renders are safe."
	value.FailureSemantics = "Any context, configuration, document, static-Text subset, binding, resource, provider, profile, geometry, time, hash or receipt mismatch fails closed with nil Program or a zero Result. No partial source cache, Surface, output Frame or PNG Artifact is published."
	value.ExcludedResponsibilities = "Temporal video, dynamic text paint, rich text, font fallback/system fonts, bidi inference, Text transitions, audio, vector strokes/curves, external resources, filesystem/network authorization, signatures, publication, scheduling, cache and muxing remain outside this v2 pipeline."
	value.DefaultTextConfig = &textConfig
	value.DefaultPipelineLimits = defaultPipelineLimitsV2()
	return value
}()

// DescriptorV2 describes the additive pinned-font static Text export profile. Descriptor remains
// the byte-stable v1 descriptor.
func DescriptorV2() ProfileDescriptor {
	value := descriptorV2
	if value.DefaultTextConfig != nil {
		copy := *value.DefaultTextConfig
		value.DefaultTextConfig = &copy
	}
	return value
}
