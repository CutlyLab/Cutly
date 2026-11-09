package backendstage

import (
	"context"
	"errors"
	"reflect"

	"github.com/CutlyLab/Cutly/render/chunkcachev2"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

// CheckpointsV2 validates durable native chunk admission on every checkpoint hit.
// A missing/corrupt index or payload recomputes only that chunk, even when an old
// current-job checkpoint still exists. Producer provenance stays historical.
type CheckpointsV2 struct {
	Service *ServiceV2
	Store   stagedag.CheckpointStore
}

type outputEquivalentCheckpointStore interface {
	CommitOutputEquivalent(context.Context, stagedag.Checkpoint) error
}

func (c CheckpointsV2) Load(ctx context.Context, scope stagedag.Scope, t stagedag.Task) (stagedag.Checkpoint, bool, error) {
	if c.Service == nil || nilLike(c.Store) {
		return stagedag.Checkpoint{}, false, ErrConfiguration
	}
	cp, found, err := c.Store.Load(ctx, scope, t)
	if err != nil || !found {
		return cp, found, err
	}
	if t.ID == PublishTaskIDV2 {
		return cp, true, nil
	}
	e, err := c.Service.config.Environments.ResolveV2(ctx, scope.PlanFingerprint, scope.ExecutionManifestFingerprint)
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	// Reuse only the native verified environment; the job-neutral scope has no
	// document or manifest payload and cannot be used as a legacy provider.
	if e.Manifest.ProviderBuild != c.Service.config.Worker.ExecutableDigest() {
		return stagedag.Checkpoint{}, false, ErrEnvironment
	}
	v, err := prepared.VerifyEnvironmentV2(ctx, e)
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	e, err = v.Environment(ctx)
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	x := executorV2{service: c.Service, verified: v, environment: e}
	identity, err := x.identityV2(ctx, t)
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	h, err := c.Service.config.Chunks.Lookup(ctx, identity)
	if errors.Is(err, chunkcachev2.ErrMiss) {
		return stagedag.Checkpoint{}, false, nil
	}
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	out, err := chunkOutputV2(h)
	err = errors.Join(err, h.Close())
	if err != nil {
		return stagedag.Checkpoint{}, false, err
	}
	if !reflect.DeepEqual(out, cp.Output) {
		return stagedag.Checkpoint{}, false, nil
	}
	return cp, true, nil
}
func (c CheckpointsV2) Commit(ctx context.Context, cp stagedag.Checkpoint) error {
	if c.Service == nil || nilLike(c.Store) {
		return ErrConfiguration
	}
	if cp.TaskID != PublishTaskIDV2 {
		if store, ok := c.Store.(outputEquivalentCheckpointStore); ok {
			return store.CommitOutputEquivalent(ctx, cp)
		}
	}
	return c.Store.Commit(ctx, cp)
}
