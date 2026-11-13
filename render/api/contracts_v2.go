package renderapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/jsonadmission"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const MaxCanonicalRenderRequestV2Bytes = renderplanv2.MaxCanonicalPlanBytes + MaxCanonicalExecutionManifestV2Bytes + (1 << 20)

var (
	ErrRenderRequestV2Invalid               = errors.New("render: request v2 is invalid")
	ErrDocumentBoundRenderRequestV2Required = errors.New("render: document-bound request v2 is required")
)

type RenderRequestV2 struct {
	Job      RenderJob           `json:"job"`
	Plan     renderplanv2.Plan   `json:"plan"`
	Manifest ExecutionManifestV2 `json:"execution_manifest"`
}

// DocumentBoundRenderRequestV2 is an immutable integrity handle suitable for
// persistence and host-authorized reconstruction. It proves that the plan was
// re-resolved from the authoritative Document and that the declared manifest
// and job match that plan. It is not execution authority: S3 must resolve
// resources beneath host-owned roots, rehash bytes, and attest executable,
// probe and program identities before producing an executable handle.
type DocumentBoundRenderRequestV2 struct {
	job      RenderJob
	plan     renderplanv2.DocumentBoundPlan
	manifest VerifiedExecutionManifestV2
	bound    bool
}

func (bound DocumentBoundRenderRequestV2) PlanFingerprint() string {
	if !bound.bound {
		return ""
	}
	return bound.plan.Fingerprint()
}

func (bound DocumentBoundRenderRequestV2) ManifestFingerprint() string {
	if !bound.bound {
		return ""
	}
	return bound.manifest.Fingerprint()
}

func (bound DocumentBoundRenderRequestV2) JobID() string {
	if !bound.bound {
		return ""
	}
	return bound.job.ID
}

func (bound DocumentBoundRenderRequestV2) Snapshot() (RenderRequestV2, error) {
	if !bound.bound {
		return RenderRequestV2{}, ErrDocumentBoundRenderRequestV2Required
	}
	plan, err := bound.plan.Snapshot()
	if err != nil {
		return RenderRequestV2{}, err
	}
	manifest, err := bound.manifest.Snapshot()
	if err != nil {
		return RenderRequestV2{}, err
	}
	return RenderRequestV2{Job: bound.job, Plan: plan, Manifest: manifest}, nil
}

// BindRenderRequestV2 strictly round-trips the wire value, re-resolves the
// semantic plan from document, and binds the declared manifest and exact job
// ranges. The returned handle proves integrity, not host execution authority.
func BindRenderRequestV2(ctx context.Context, document documentv2.Document, request RenderRequestV2) (DocumentBoundRenderRequestV2, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DocumentBoundRenderRequestV2{}, err
	}
	raw, err := CanonicalRenderRequestV2Context(ctx, request)
	if err != nil {
		return DocumentBoundRenderRequestV2{}, err
	}
	owned, err := DecodeRenderRequestV2(raw)
	if err != nil {
		return DocumentBoundRenderRequestV2{}, err
	}
	if err := ctx.Err(); err != nil {
		return DocumentBoundRenderRequestV2{}, err
	}
	boundPlan, err := renderplanv2.VerifyAgainstDocument(ctx, owned.Plan, document)
	if err != nil {
		return DocumentBoundRenderRequestV2{}, fmt.Errorf("%w: document binding: %v", ErrRenderRequestV2Invalid, err)
	}
	verifiedManifest, err := VerifyExecutionManifestV2(ctx, boundPlan, owned.Manifest)
	if err != nil {
		return DocumentBoundRenderRequestV2{}, fmt.Errorf("%w: execution manifest: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := ctx.Err(); err != nil {
		return DocumentBoundRenderRequestV2{}, err
	}
	return DocumentBoundRenderRequestV2{
		job: owned.Job, plan: boundPlan, manifest: verifiedManifest, bound: true,
	}, nil
}

func CanonicalRenderRequestV2(request RenderRequestV2) ([]byte, error) {
	return CanonicalRenderRequestV2Context(context.Background(), request)
}

// CanonicalRenderRequestV2Context validates admission limits before any
// potentially large clone or JSON allocation and observes cancellation between
// each protocol verification phase.
func CanonicalRenderRequestV2Context(ctx context.Context, request RenderRequestV2) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := preflightRenderRequestV2(ctx, request); err != nil {
		return nil, err
	}
	if err := validateRenderRequestV2Context(ctx, request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return canonicaljson.Marshal(request)
}

// DecodeRenderRequestV2 proves strict canonical wire integrity and internal
// identity consistency. BindRenderRequestV2 additionally proves equality with
// the authoritative Document, but neither operation grants host execution
// authority; S3 reconstruction remains mandatory.
func DecodeRenderRequestV2(raw []byte) (RenderRequestV2, error) {
	if len(raw) == 0 || len(raw) > MaxCanonicalRenderRequestV2Bytes {
		return RenderRequestV2{}, fmt.Errorf("%w: byte length %d", ErrRenderRequestV2Invalid, len(raw))
	}
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{
		MaxInputBytes: MaxCanonicalRenderRequestV2Bytes,
		MaxDepth:      160, MaxValues: 1 << 21,
		MaxStringBytes:   MaxCanonicalRenderRequestV2Bytes,
		MaxIntegerDigits: 20,
	})
	if err != nil {
		return RenderRequestV2{}, fmt.Errorf("%w: canonical JSON: %v", ErrRenderRequestV2Invalid, err)
	}
	if !bytes.Equal(raw, canonical) {
		return RenderRequestV2{}, fmt.Errorf("%w: noncanonical encoding", ErrRenderRequestV2Invalid)
	}
	var request RenderRequestV2
	if err := wirejson.Decode(raw, &request); err != nil {
		return RenderRequestV2{}, fmt.Errorf("%w: decode: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := validateRenderRequestV2(request); err != nil {
		return RenderRequestV2{}, err
	}
	return request, nil
}

func validateRenderRequestV2(request RenderRequestV2) error {
	return validateRenderRequestV2Context(context.Background(), request)
}

func validateRenderRequestV2Context(ctx context.Context, request RenderRequestV2) error {
	if err := preflightRenderRequestV2(ctx, request); err != nil {
		return err
	}
	verifiedPlan, err := renderplanv2.VerifyContext(ctx, request.Plan)
	if err != nil {
		return fmt.Errorf("%w: plan: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := requestPlanSnapshotV2(ctx, verifiedPlan, request.Plan)
	if err != nil {
		return fmt.Errorf("%w: plan: %v", ErrRenderRequestV2Invalid, err)
	}
	manifestRaw, err := CanonicalExecutionManifestV2Context(ctx, request.Manifest)
	if err != nil {
		return fmt.Errorf("%w: execution manifest encoding: %v", ErrRenderRequestV2Invalid, err)
	}
	manifest, err := DecodeExecutionManifestV2(manifestRaw)
	if err != nil {
		return fmt.Errorf("%w: execution manifest: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := validateExecutionManifestV2AgainstPlan(plan, manifest); err != nil {
		return fmt.Errorf("%w: execution manifest binding: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	job := request.Job
	if err := validateRenderJob(job); err != nil {
		return fmt.Errorf("%w: %v", ErrRenderRequestV2Invalid, err)
	}
	if job.PlanFingerprint != plan.Fingerprint || job.PlanFingerprint != manifest.PlanFingerprint {
		return fmt.Errorf("%w: plan fingerprint mismatch", ErrRenderRequestV2Invalid)
	}
	if job.ExecutionManifestFingerprint != manifest.Fingerprint {
		return fmt.Errorf("%w: execution manifest fingerprint mismatch", ErrRenderRequestV2Invalid)
	}
	if err := timeRangeSubset(job.TimelineRange, plan.RequestedRange); err != nil {
		return fmt.Errorf("%w: timeline range: %v", ErrRenderRequestV2Invalid, err)
	}
	wantFrames, err := videoedit.CompileFrameTickRange(job.TimelineRange, plan.Target.FrameClock)
	if err != nil || wantFrames != job.FrameRange {
		return fmt.Errorf("%w: frame range is not derived from the job timeline range", ErrRenderRequestV2Invalid)
	}
	wantSamples, err := videoedit.CompileSampleTickRange(job.TimelineRange, plan.Target.SampleClock)
	if err != nil || wantSamples != job.SampleRange {
		return fmt.Errorf("%w: sample range is not derived from the job timeline range", ErrRenderRequestV2Invalid)
	}
	if err := tickRangeSubset(job.FrameRange, plan.FrameRange); err != nil {
		return fmt.Errorf("%w: frame range: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := tickRangeSubset(job.SampleRange, plan.SampleRange); err != nil {
		return fmt.Errorf("%w: sample range: %v", ErrRenderRequestV2Invalid, err)
	}
	return nil
}

func preflightRenderRequestV2(ctx context.Context, request RenderRequestV2) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan := request.Plan
	if len(plan.Visual) > renderplanv2.MaxPlanCollectionItems || len(plan.Audio) > renderplanv2.MaxPlanCollectionItems ||
		len(plan.Transitions) > renderplanv2.MaxPlanCollectionItems || len(plan.Resources) > renderplanv2.MaxPlanCollectionItems ||
		len(plan.RequiredCapabilities.TrackKinds) > renderplanv2.MaxPlanCollectionItems ||
		len(plan.RequiredCapabilities.AssetKinds) > renderplanv2.MaxPlanCollectionItems ||
		len(plan.RequiredCapabilities.Features) > renderplanv2.MaxPlanCollectionItems {
		return fmt.Errorf("%w: plan collection limit exceeded", ErrRenderRequestV2Invalid)
	}
	if err := preflightExecutionManifestV2Draft(ctx, request.Manifest); err != nil {
		return fmt.Errorf("%w: execution manifest: %v", ErrRenderRequestV2Invalid, err)
	}
	if err := jsonadmission.Check(ctx, request, MaxCanonicalRenderRequestV2Bytes, 1<<21); err != nil {
		return fmt.Errorf("%w: canonical admission: %v", ErrRenderRequestV2Invalid, err)
	}
	return ctx.Err()
}

// requestPlanSnapshotV2 obtains an owned Plan without weakening the public
// rule that only DocumentBoundPlan can expose a document-bound semantic
// snapshot. The verified handle is used only as proof that the supplied value
// passed the strict decoder; the returned value is independently re-decoded.
func requestPlanSnapshotV2(ctx context.Context, verified renderplanv2.VerifiedPlan, supplied renderplanv2.Plan) (renderplanv2.Plan, error) {
	if verified.Fingerprint() == "" || verified.Fingerprint() != supplied.Fingerprint {
		return renderplanv2.Plan{}, renderplanv2.ErrVerifiedPlanRequired
	}
	raw, err := renderplanv2.CanonicalContext(ctx, supplied)
	if err != nil {
		return renderplanv2.Plan{}, err
	}
	return renderplanv2.Decode(raw)
}
