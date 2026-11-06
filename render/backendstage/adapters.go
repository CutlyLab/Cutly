package backendstage

import (
	"context"
	"errors"
	"io"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/ffmpegbackend"
	"github.com/CutlyLab/Cutly/render/prepared"
)

// EnvironmentSource resolves one immutable environment by the two identities
// carried by RenderJob. Implementations must be safe for concurrent calls and
// return caller-owned values. prepared.Store implements this interface directly.
type EnvironmentSource interface {
	Resolve(context.Context, string, string) (prepared.Environment, error)
}

type EnvironmentSourceFunc func(context.Context, string, string) (prepared.Environment, error)

func (function EnvironmentSourceFunc) Resolve(ctx context.Context, plan, manifest string) (prepared.Environment, error) {
	return function(ctx, plan, manifest)
}

// PublishedMedia is a verified, rewound descriptor together with its immutable
// artifact identity. The descriptor, not a caller-selected path, is streamed
// into CAS.
type PublishedMedia interface {
	io.ReadSeekCloser
	Artifact() renderapi.RenderArtifact
}

// Backend is the recovery-capable backend boundary required by the durable
// adapter. Implementations must be safe for concurrent calls.
type Backend interface {
	renderapi.RenderBackend
	InspectPublished(context.Context, renderapi.RenderRequest) (renderapi.RenderArtifact, bool, error)
	OpenPublished(context.Context, renderapi.RenderRequest) (PublishedMedia, bool, error)
}

// BackendResolver selects a host-owned backend from a frozen manifest. It must
// be safe for concurrent calls and may not construct executable paths or trust
// identities from mutable job fields.
type BackendResolver interface {
	ResolveBackend(context.Context, renderapi.ExecutionManifest) (Backend, error)
}

type BackendResolverFunc func(context.Context, renderapi.ExecutionManifest) (Backend, error)

func (function BackendResolverFunc) ResolveBackend(ctx context.Context, manifest renderapi.ExecutionManifest) (Backend, error) {
	return function(ctx, manifest)
}

// AdaptFFmpeg exposes ffmpegbackend.Backend through the backend-neutral media
// descriptor interface without changing the backend's concrete public API.
func AdaptFFmpeg(backend *ffmpegbackend.Backend) (Backend, error) {
	if backend == nil {
		return nil, errors.New("backendstage: FFmpeg backend is required")
	}
	return ffmpegAdapter{backend: backend}, nil
}

type ffmpegAdapter struct {
	backend *ffmpegbackend.Backend
}

func (adapter ffmpegAdapter) ID() string { return adapter.backend.ID() }

func (adapter ffmpegAdapter) Capabilities(ctx context.Context) (videoedit.BackendCapabilities, error) {
	return adapter.backend.Capabilities(ctx)
}

func (adapter ffmpegAdapter) Preflight(ctx context.Context, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) []videoedit.Diagnostic {
	return adapter.backend.Preflight(ctx, plan, manifest)
}

func (adapter ffmpegAdapter) Render(ctx context.Context, request renderapi.RenderRequest, sink renderapi.EventSink) (renderapi.RenderArtifact, error) {
	return adapter.backend.Render(ctx, request, sink)
}

func (adapter ffmpegAdapter) InspectPublished(ctx context.Context, request renderapi.RenderRequest) (renderapi.RenderArtifact, bool, error) {
	return adapter.backend.InspectPublished(ctx, request)
}

func (adapter ffmpegAdapter) OpenPublished(ctx context.Context, request renderapi.RenderRequest) (PublishedMedia, bool, error) {
	return adapter.backend.OpenPublished(ctx, request)
}

var _ Backend = ffmpegAdapter{}
