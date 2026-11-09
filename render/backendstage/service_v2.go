package backendstage

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/chunkcachev2"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/stagedag"
	"github.com/CutlyLab/Cutly/render/worker"
)

const ServiceProfileV2 = "cutly.stage.reference-chunks.document-v2.v2"
const PublishStageProfileV2 = "cutly.stage.chunk-publish.document-v2.v2"
const PublishTaskIDV2 = "zz-publish"

// EnvironmentSourceV2 never resolves a legacy prepared manifest.
type EnvironmentSourceV2 interface {
	ResolveV2(context.Context, string, string) (prepared.EnvironmentV2, error)
}
type AssemblerResolverV2 interface {
	ResolveAssemblerV2(context.Context, prepared.VerifiedEnvironmentV2) (ChunkAssemblerBackendV2, error)
}
type AssemblerResolverFuncV2 func(context.Context, prepared.VerifiedEnvironmentV2) (ChunkAssemblerBackendV2, error)

func (f AssemblerResolverFuncV2) ResolveAssemblerV2(ctx context.Context, e prepared.VerifiedEnvironmentV2) (ChunkAssemblerBackendV2, error) {
	return f(ctx, e)
}

// ConfigV2 contains only host-selected implementations and resource authority.
// Worker is concrete: production execution cannot substitute an in-process V1 provider.
type ConfigV2 struct {
	Environments           EnvironmentSourceV2
	Worker                 *worker.RunnerV2
	Chunks                 *chunkcachev2.Store
	CAS                    *cas.Store
	Assemblers             AssemblerResolverV2
	MemoryBytes, DiskBytes uint64
	Progress               api.EventSink
}
type ServiceV2 struct{ config ConfigV2 }

func NewV2(c ConfigV2) (*ServiceV2, error) {
	if nilLike(c.Environments) || c.Worker == nil || c.Chunks == nil || c.CAS == nil || nilLike(c.Assemblers) || c.MemoryBytes == 0 || c.DiskBytes == 0 {
		return nil, ErrConfiguration
	}
	return &ServiceV2{config: c}, nil
}

func (s *ServiceV2) resolveV2(ctx context.Context, j api.RenderJob) (prepared.VerifiedEnvironmentV2, prepared.EnvironmentV2, error) {
	if s == nil || ctx == nil {
		return prepared.VerifiedEnvironmentV2{}, prepared.EnvironmentV2{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return prepared.VerifiedEnvironmentV2{}, prepared.EnvironmentV2{}, err
	}
	e, err := s.config.Environments.ResolveV2(ctx, j.PlanFingerprint, j.ExecutionManifestFingerprint)
	if err != nil {
		return prepared.VerifiedEnvironmentV2{}, prepared.EnvironmentV2{}, err
	}
	// Reject foreign executable identities before traversing the original document.
	if e.Manifest.ProviderBuild != s.config.Worker.ExecutableDigest() {
		return prepared.VerifiedEnvironmentV2{}, prepared.EnvironmentV2{}, fmt.Errorf("%w: measured native worker differs from provider build", ErrEnvironment)
	}
	v, err := prepared.VerifyEnvironmentV2(ctx, e)
	if err != nil {
		return v, prepared.EnvironmentV2{}, err
	}
	e, err = v.Environment(ctx)
	if err != nil {
		return v, e, err
	}
	r := api.RenderRequestV2{SchemaVersion: 2, Profile: api.RenderRequestProfileV2, CanonicalJSONProfile: canonicaljson.Profile, Job: j, Plan: e.Plan, Manifest: e.Manifest}
	if _, err = api.VerifyRenderRequestV2AgainstPlan(ctx, r, e.Plan); err != nil {
		return v, e, err
	}
	// Initial native worker partitioning is relative to the complete prepared plan.
	// A subset requires a separately prepared native plan, never a V1 translation.
	if j.FrameRange != e.Plan.FrameRange || j.SampleRange != e.Plan.SampleRange || j.TimelineRange != e.Plan.RequestedRange {
		return v, e, fmt.Errorf("%w: native service requires the complete prepared range", ErrPlan)
	}
	return v, e, nil
}
func (s *ServiceV2) assemblerV2(ctx context.Context, v prepared.VerifiedEnvironmentV2, e prepared.EnvironmentV2) (ChunkAssemblerBackendV2, error) {
	a, err := s.config.Assemblers.ResolveAssemblerV2(ctx, v)
	if err != nil {
		return nil, err
	}
	if nilLike(a) {
		return nil, ErrBackend
	}
	offered, err := a.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	// Offered is independently fixed by the host implementation, not copied from RequiredCapabilities.
	if a.ID() != offered.BackendID || !reflect.DeepEqual(offered, e.Manifest.Backend) {
		return nil, fmt.Errorf("%w: native assembler capabilities differ from frozen host offer", ErrBackend)
	}
	return a, nil
}
func (s *ServiceV2) ResolveStagePlan(ctx context.Context, j api.RenderJob) (stagedag.Plan, error) {
	v, e, err := s.resolveV2(ctx, j)
	if err != nil {
		return stagedag.Plan{}, err
	}
	if _, err = s.assemblerV2(ctx, v, e); err != nil {
		return stagedag.Plan{}, err
	}
	return s.buildPlanV2(ctx, j, v, e)
}
func (s *ServiceV2) ResolveExecutor(ctx context.Context, j api.RenderJob, p stagedag.Plan) (stagedag.Executor, error) {
	v, e, err := s.resolveV2(ctx, j)
	if err != nil {
		return nil, err
	}
	want, err := s.buildPlanV2(ctx, j, v, e)
	if err != nil {
		return nil, err
	}
	p, err = exactPlan(p, want)
	if err != nil {
		return nil, err
	}
	a, err := s.assemblerV2(ctx, v, e)
	if err != nil {
		return nil, err
	}
	return &executorV2{service: s, verified: v, environment: e, job: j, plan: p, assembler: a}, nil
}
func (s *ServiceV2) ResolveArtifact(ctx context.Context, j api.RenderJob, p stagedag.Plan, result stagedag.RunResult) (api.RenderArtifact, error) {
	v, e, err := s.resolveV2(ctx, j)
	if err != nil {
		return api.RenderArtifact{}, err
	}
	want, err := s.buildPlanV2(ctx, j, v, e)
	if err != nil {
		return api.RenderArtifact{}, err
	}
	p, err = exactPlan(p, want)
	if err != nil {
		return api.RenderArtifact{}, err
	}
	if len(result.Checkpoints) != len(p.Tasks) {
		return api.RenderArtifact{}, ErrArtifact
	}
	for i, t := range p.Tasks {
		if err = stagedag.ValidateCheckpoint(p, t, result.Checkpoints[i]); err != nil {
			return api.RenderArtifact{}, err
		}
	}
	a, err := s.assemblerV2(ctx, v, e)
	if err != nil {
		return api.RenderArtifact{}, err
	}
	x := executorV2{service: s, verified: v, environment: e, job: j, plan: p, assembler: a}
	request, err := x.assemblyV2(ctx, result.Checkpoints[:len(p.Tasks)-1])
	if err != nil {
		return api.RenderArtifact{}, err
	}
	artifact, found, err := a.InspectChunkAssembly(ctx, request)
	if err != nil || !found {
		return api.RenderArtifact{}, errors.Join(ErrArtifact, err)
	}
	cp := result.Checkpoints[len(p.Tasks)-1]
	raw, err := canonicaljson.Marshal(publishReceiptV2{Profile: PublishStageProfileV2, RequestFingerprint: request.Fingerprint(), Artifact: artifact})
	if err != nil || string(raw) != string(cp.Output.Receipt) || cp.Output.ContentHash != artifact.ContentHash || cp.Output.SizeBytes != artifact.SizeBytes {
		return api.RenderArtifact{}, errors.Join(ErrArtifact, err)
	}
	reader, err := s.config.CAS.Open(ctx, p.Tasks[len(p.Tasks)-1].CacheKey, "native-final-verify")
	if err != nil {
		return api.RenderArtifact{}, err
	}
	if reader.Entry.PayloadHash != artifact.ContentHash || reader.Entry.PayloadSize != artifact.SizeBytes {
		return api.RenderArtifact{}, errors.Join(ErrArtifact, reader.Close())
	}
	if err = reader.Close(); err != nil {
		return api.RenderArtifact{}, err
	}
	return artifact, nil
}

type executorV2 struct {
	service     *ServiceV2
	verified    prepared.VerifiedEnvironmentV2
	environment prepared.EnvironmentV2
	job         api.RenderJob
	plan        stagedag.Plan
	assembler   ChunkAssemblerBackendV2
}

func (x *executorV2) Execute(ctx context.Context, t stagedag.Task, d []stagedag.Dependency) (stagedag.StageOutput, error) {
	return x.ExecuteAttempt(ctx, t, d, 1)
}
func (x *executorV2) ExecuteAttempt(ctx context.Context, t stagedag.Task, d []stagedag.Dependency, attempt uint8) (stagedag.StageOutput, error) {
	if x == nil || ctx == nil {
		return stagedag.StageOutput{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return stagedag.StageOutput{}, err
	}
	found := false
	for _, candidate := range x.plan.Tasks {
		if candidate.ID == t.ID {
			found = reflect.DeepEqual(candidate, t)
			break
		}
	}
	if !found || attempt == 0 || attempt > t.Retry.MaxAttempts {
		return stagedag.StageOutput{}, ErrExecution
	}
	if t.ID != PublishTaskIDV2 {
		if len(d) != 0 {
			return stagedag.StageOutput{}, ErrExecution
		}
		return x.chunkV2(ctx, t, attempt)
	}
	if len(d) != len(x.plan.Tasks)-1 {
		return stagedag.StageOutput{}, ErrExecution
	}
	cps := make([]stagedag.Checkpoint, len(d))
	for i, dep := range d {
		if dep.TaskID != x.plan.Tasks[i].ID {
			return stagedag.StageOutput{}, ErrExecution
		}
		if err := stagedag.ValidateCheckpoint(x.plan, x.plan.Tasks[i], dep.Checkpoint); err != nil {
			return stagedag.StageOutput{}, err
		}
		cps[i] = dep.Checkpoint
	}
	return x.publishV2(ctx, t, cps)
}

type publishReceiptV2 struct {
	Profile            string             `json:"profile"`
	RequestFingerprint string             `json:"request_fingerprint"`
	Artifact           api.RenderArtifact `json:"artifact"`
}
