package backendstage

import (
	"context"
	"encoding/json"
	"io"

	"github.com/CutlyLab/Cutly/core/renderplanv2"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/ffmpegbackend"
)

// EvidenceBindingV2 preserves the complete V2 request identity without an
// evidence -> backendstage import cycle. It never converts a V2 request to V1.
func EvidenceBindingV2(v VerifiedChunkAssemblyRequestV2) (evidence.BindingV2, error) {
	raw, err := v.Canonical()
	if err != nil {
		return evidence.BindingV2{}, err
	}
	var binding evidence.BindingV2
	if err := json.Unmarshal(raw, &binding); err != nil {
		return evidence.BindingV2{}, err
	}
	return binding, nil
}

func VerifyAssemblyEvidenceV2(ctx context.Context, m evidence.ManifestV2, v VerifiedChunkAssemblyRequestV2) (evidence.VerifiedManifestV2, error) {
	binding, err := EvidenceBindingV2(v)
	if err != nil {
		return evidence.VerifiedManifestV2{}, err
	}
	return evidence.VerifyManifestV2AgainstBinding(ctx, m, binding)
}

// FFmpegChunkAssemblerV2 adapts the neutral encoding backend to the frozen V2
// protocol. This boundary avoids the existing V1 adapter import cycle. It owns
// the host authority and verifies AgainstPlan on EVERY call, including recovery.
type FFmpegChunkAssemblerV2 struct {
	backend  *ffmpegbackend.ChunkAssemblerV2
	plan     renderplanv2.Plan
	manifest api.ExecutionManifestV2
}

var _ ChunkAssemblerBackendV2 = (*FFmpegChunkAssemblerV2)(nil)

func NewFFmpegChunkAssemblerV2(ctx context.Context, o ffmpegbackend.ChunkAssemblerOptionsV2) (*FFmpegChunkAssemblerV2, error) {
	v, err := api.VerifyExecutionManifestV2AgainstPlan(ctx, o.Manifest, o.Plan)
	if err != nil {
		return nil, err
	}
	manifest, err := v.Manifest()
	if err != nil {
		return nil, err
	}
	raw, err := renderplanv2.Canonical(o.Plan)
	if err != nil {
		return nil, err
	}
	plan, err := renderplanv2.Decode(raw)
	if err != nil {
		return nil, err
	}
	backend, err := ffmpegbackend.NewChunkAssemblerV2(ctx, o)
	if err != nil {
		return nil, err
	}
	return &FFmpegChunkAssemblerV2{backend: backend, plan: plan, manifest: manifest}, nil
}
func (b *FFmpegChunkAssemblerV2) ID() string { return b.backend.ID() }
func (b *FFmpegChunkAssemblerV2) Close() error {
	if b == nil {
		return nil
	}
	return b.backend.Close()
}
func (b *FFmpegChunkAssemblerV2) Capabilities(ctx context.Context) (api.BackendCapabilitiesV2, error) {
	return b.backend.Capabilities(ctx)
}
func (b *FFmpegChunkAssemblerV2) binding(ctx context.Context, v VerifiedChunkAssemblyRequestV2) (evidence.VerifiedBindingV2, error) {
	if b == nil {
		return evidence.VerifiedBindingV2{}, ErrChunkAssemblyInvalidV2
	}
	r, err := v.Request()
	if err != nil {
		return evidence.VerifiedBindingV2{}, err
	}
	bound, err := VerifyChunkAssemblyRequestV2AgainstPlan(ctx, r, b.manifest, b.plan)
	if err != nil {
		return evidence.VerifiedBindingV2{}, err
	}
	binding, err := EvidenceBindingV2(bound)
	if err != nil {
		return evidence.VerifiedBindingV2{}, err
	}
	return evidence.VerifyBindingV2(ctx, binding)
}

type ffmpegChunkSourceV2 struct{ source ChunkSourceV2 }

func (s ffmpegChunkSourceV2) Open(ctx context.Context, d evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
	return s.source.Open(ctx, ChunkDescriptorV2{TaskID: d.TaskID, Domain: d.Domain, CacheKey: d.CacheKey, Range: d.Range, ContentHash: d.ContentHash, SizeBytes: d.SizeBytes, UnitBytes: d.UnitBytes})
}
func (b *FFmpegChunkAssemblerV2) AssembleChunks(ctx context.Context, v VerifiedChunkAssemblyRequestV2, s ChunkSourceV2, sink api.EventSink) (api.RenderArtifact, error) {
	binding, err := b.binding(ctx, v)
	if err != nil {
		return api.RenderArtifact{}, err
	}
	if s == nil {
		return api.RenderArtifact{}, ErrChunkAssemblyInvalidV2
	}
	return b.backend.AssembleChunks(ctx, binding, ffmpegChunkSourceV2{s}, sink)
}
func (b *FFmpegChunkAssemblerV2) InspectChunkAssembly(ctx context.Context, v VerifiedChunkAssemblyRequestV2) (api.RenderArtifact, bool, error) {
	binding, err := b.binding(ctx, v)
	if err != nil {
		return api.RenderArtifact{}, false, err
	}
	return b.backend.InspectChunkAssembly(ctx, binding)
}
func (b *FFmpegChunkAssemblerV2) OpenChunkAssembly(ctx context.Context, v VerifiedChunkAssemblyRequestV2) (PublishedMedia, bool, error) {
	binding, err := b.binding(ctx, v)
	if err != nil {
		return nil, false, err
	}
	m, found, err := b.backend.OpenChunkAssembly(ctx, binding)
	if err != nil || !found {
		return nil, found, err
	}
	return m, true, nil
}
