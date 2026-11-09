package cutly

import (
	"context"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/prepared"
)

type RenderPlanV2 = renderplanv2.Plan
type VerifiedRenderPlanV2 = renderplanv2.VerifiedPlan
type ExecutionManifestV2 = renderapi.ExecutionManifestV2
type VerifiedExecutionManifestV2 = renderapi.VerifiedExecutionManifestV2
type RenderRequestV2 = renderapi.RenderRequestV2
type RenderJobV2 = renderapi.RenderJob
type VerifiedRenderRequestV2 = renderapi.VerifiedRenderRequestV2
type BackendCapabilitiesV2 = renderapi.BackendCapabilitiesV2
type MaterializedResourceV2 = renderapi.MaterializedResourceV2
type OutputSpecV2 = renderapi.OutputSpecV2
type PreparedEnvironmentV2 = prepared.EnvironmentV2
type VerifiedPreparedEnvironmentV2 = prepared.VerifiedEnvironmentV2
type PreparedKeyV2 = prepared.KeyV2
type EvidenceManifestV2 = evidence.ManifestV2
type VerifiedEvidenceManifestV2 = evidence.VerifiedManifestV2
type EvidenceBindingV2 = evidence.BindingV2
type VerifiedEvidenceBindingV2 = evidence.VerifiedBindingV2

const (
	ExecutionManifestProfileV2   = renderapi.ExecutionManifestProfileV2
	RenderRequestProfileV2       = renderapi.RenderRequestProfileV2
	PreparedEnvironmentProfileV2 = prepared.RecordProfileV2
	EvidenceManifestProfileV2    = evidence.ProfileV2
)

func ResolveRenderPlanV2(ctx context.Context, document Document, renderRange TimeRange, target RenderTargetV3) (RenderPlanV2, []Diagnostic) {
	return renderplanv2.Resolve(ctx, document, renderRange, target)
}

func DecodeRenderPlanV2(raw []byte) (RenderPlanV2, error) {
	return renderplanv2.Decode(raw)
}

func CanonicalRenderPlanV2(plan RenderPlanV2) ([]byte, error) {
	return renderplanv2.Canonical(plan)
}

func VerifyRenderPlanV2(plan RenderPlanV2) (VerifiedRenderPlanV2, error) {
	return renderplanv2.Verify(plan)
}

func DecodeExecutionManifestV2(raw []byte) (ExecutionManifestV2, error) {
	return renderapi.DecodeExecutionManifestV2(raw)
}

func CanonicalExecutionManifestV2(manifest ExecutionManifestV2) ([]byte, error) {
	return renderapi.CanonicalExecutionManifestV2(manifest)
}

func VerifyExecutionManifestV2AgainstPlan(ctx context.Context, manifest ExecutionManifestV2, plan RenderPlanV2) (VerifiedExecutionManifestV2, error) {
	return renderapi.VerifyExecutionManifestV2AgainstPlan(ctx, manifest, plan)
}

func DecodeRenderRequestV2(raw []byte) (RenderRequestV2, error) {
	return renderapi.DecodeRenderRequestV2(raw)
}

func CanonicalRenderRequestV2(request RenderRequestV2) ([]byte, error) {
	return renderapi.CanonicalRenderRequestV2(request)
}

func VerifyRenderRequestV2AgainstPlan(ctx context.Context, request RenderRequestV2, plan RenderPlanV2) (VerifiedRenderRequestV2, error) {
	return renderapi.VerifyRenderRequestV2AgainstPlan(ctx, request, plan)
}

func DecodePreparedEnvironmentV2(ctx context.Context, raw []byte) (PreparedEnvironmentV2, error) {
	return prepared.DecodeEnvironmentV2(ctx, raw)
}

func CanonicalPreparedEnvironmentV2(ctx context.Context, environment PreparedEnvironmentV2) ([]byte, error) {
	return prepared.CanonicalEnvironmentV2(ctx, environment)
}

func VerifyPreparedEnvironmentV2(ctx context.Context, environment PreparedEnvironmentV2) (VerifiedPreparedEnvironmentV2, error) {
	return prepared.VerifyEnvironmentV2(ctx, environment)
}

func DecodeEvidenceManifestV2(raw []byte) (EvidenceManifestV2, error) {
	return evidence.DecodeManifestV2(raw)
}

func CanonicalEvidenceManifestV2(manifest EvidenceManifestV2) ([]byte, error) {
	return evidence.CanonicalManifestV2(manifest)
}

func VerifyEvidenceManifestV2(manifest EvidenceManifestV2) (VerifiedEvidenceManifestV2, error) {
	return evidence.VerifyManifestV2(manifest)
}

func VerifyEvidenceManifestV2AgainstBinding(ctx context.Context, manifest EvidenceManifestV2, binding EvidenceBindingV2) (VerifiedEvidenceManifestV2, error) {
	return evidence.VerifyManifestV2AgainstBinding(ctx, manifest, binding)
}
