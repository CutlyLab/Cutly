package backendstage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	api "github.com/CutlyLab/Cutly/render/api"
	checkpointlocal "github.com/CutlyLab/Cutly/render/checkpoint/local"
	"github.com/CutlyLab/Cutly/render/chunkcachev2"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func digestChunkForNativeTest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func nativeServiceFixtureV2(t *testing.T) (*ServiceV2, prepared.VerifiedEnvironmentV2, prepared.EnvironmentV2, api.RenderJob) {
	t.Helper()
	ctx := context.Background()
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-request-gap-silence.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	request, err := api.DecodeRenderRequestV2(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../conformance/fixtures/document-v2-render-plan-gap.document.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	target := request.Plan.Target
	target.Width = 16
	target.Height = 16
	p, ds := renderplanv2.Resolve(ctx, d, request.Plan.RequestedRange, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	m := request.Manifest
	m.Output.Width = 16
	m.Output.Height = 16
	m.Visual = api.ProgramSelectionV2{}
	m.Audio = api.ProgramSelectionV2{}
	m, err = api.FreezeExecutionManifestV2(ctx, p, m)
	if err != nil {
		t.Fatal(err)
	}
	e, err := prepared.NewEnvironmentV2(ctx, d, p, m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := prepared.VerifyEnvironmentV2(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	objects, err := cas.New(filepath.Join(root, "cas"), cas.Options{})
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "index")
	if err = os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	chunks, err := chunkcachev2.New(index, objects, chunkcachev2.Options{MaxPayloadBytes: 16 << 20, MaxEntries: 100})
	if err != nil {
		t.Fatal(err)
	}
	j := request.Job
	j.ID = "native-unit"
	j.PlanFingerprint = p.Fingerprint
	j.ExecutionManifestFingerprint = m.Fingerprint
	j.FrameRange = p.FrameRange
	j.SampleRange = p.SampleRange
	j.TimelineRange = p.RequestedRange
	return &ServiceV2{config: ConfigV2{CAS: objects, Chunks: chunks, MemoryBytes: 512 << 20, DiskBytes: 256 << 20}}, v, e, j
}
func TestServiceV2NativePlanAndDurableWarmAssembly(t *testing.T) {
	s, v, e, j := nativeServiceFixtureV2(t)
	ctx := context.Background()
	p, err := s.buildPlanV2(ctx, j, v, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tasks) < 3 || p.Tasks[len(p.Tasks)-1].Profile != PublishStageProfileV2 {
		t.Fatal("native DAG missing chunks/publish")
	}
	x := executorV2{service: s, verified: v, environment: e, job: j, plan: p}
	cps := []stagedag.Checkpoint{}
	for _, task := range p.Tasks[:len(p.Tasks)-1] {
		identity, err := x.identityV2(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		data := make([]byte, identity.UnitBytes*identity.Range.Count)
		hash := digestChunkForNativeTest(data)
		receipt, err := chunkcachev2.NewReceipt(identity, hash, int64(len(data)), "historical-producer", 2)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.config.Chunks.Publish(ctx, identity, receipt, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		// No worker is configured in this unit: only a fully verified durable hit may succeed.
		out, err := x.chunkV2(ctx, task, 1)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Receipt, []byte("historical-producer")) {
			t.Fatal("producer invocation masqueraded as current checkpoint")
		}
		cp, err := stagedag.NewCheckpoint(p, task, out, 1)
		if err != nil {
			t.Fatal(err)
		}
		cps = append(cps, cp)
	}
	verified, err := x.assemblyV2(ctx, cps)
	if err != nil {
		t.Fatal(err)
	}
	r, err := verified.Request()
	if err != nil {
		t.Fatal(err)
	}
	if r.ProviderExecutableDigest != e.Manifest.ProviderBuild || r.Profile != ChunkAssemblyProfileV2 || r.Aggregate.Audio == nil || r.Aggregate.Video == nil {
		t.Fatal("native assembly binding")
	}
	j2 := j
	j2.ID = "native-current-job"
	p2, err := s.buildPlanV2(ctx, j2, v, e)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Tasks[0].CacheKey != p.Tasks[0].CacheKey {
		t.Fatal("cache identity contains lifecycle job")
	}
	out, err := x.chunkV2(ctx, p.Tasks[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := stagedag.NewCheckpoint(p2, p2.Tasks[0], out, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cp.JobID != j2.ID || cp.JobID == cps[0].JobID {
		t.Fatal("current-job checkpoint not fresh")
	}
	bad := append([]stagedag.Checkpoint{}, cps...)
	bad[0].Output.Receipt = json.RawMessage(`{"foreign":true}`)
	if _, err = x.assemblyV2(ctx, bad); err == nil {
		t.Fatal("foreign checkpoint receipt admitted")
	}
}

func TestServiceV2CheckpointConvergesAfterChunkIndexLoss(t *testing.T) {
	s, v, e, j := nativeServiceFixtureV2(t)
	ctx := context.Background()
	p, err := s.buildPlanV2(ctx, j, v, e)
	if err != nil {
		t.Fatal(err)
	}
	task := p.Tasks[0]
	x := executorV2{service: s, verified: v, environment: e, job: j, plan: p}
	identity, err := x.identityV2(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, identity.UnitBytes*identity.Range.Count)
	hash := digestChunkForNativeTest(data)
	historicalReceipt, err := chunkcachev2.NewReceipt(identity, hash, int64(len(data)), "historical-retry", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.config.Chunks.Publish(ctx, identity, historicalReceipt, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	out, err := x.chunkV2(ctx, task, 2)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := stagedag.NewCheckpoint(p, task, out, 2)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := checkpointlocal.New(filepath.Join(root, "checkpoints"), s.config.CAS, checkpointlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	store := CheckpointsV2{Service: s, Store: checkpoints}
	if err = store.Commit(ctx, historical); err != nil {
		t.Fatal(err)
	}

	// Lose only the job-neutral chunk index. The payload and the historical
	// checkpoint remain durable, so the wrapper must request recomputation
	// without turning the later attempt telemetry into a commit collision.
	freshIndex := filepath.Join(root, "fresh-index")
	if err = os.Mkdir(freshIndex, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh, err := chunkcachev2.New(freshIndex, s.config.CAS, chunkcachev2.Options{MaxPayloadBytes: 16 << 20, MaxEntries: 100})
	if err != nil {
		t.Fatal(err)
	}
	s.config.Chunks = fresh
	scope := stagedag.Scope{JobID: j.ID, PlanFingerprint: j.PlanFingerprint, ExecutionManifestFingerprint: j.ExecutionManifestFingerprint}
	if handle, lookupErr := fresh.Lookup(ctx, identity); !errors.Is(lookupErr, chunkcachev2.ErrMiss) || handle != nil {
		if handle != nil {
			_ = handle.Close()
		}
		t.Fatalf("fresh index lookup handle=%v err=%v", handle != nil, lookupErr)
	}
	recomputedReceipt, err := chunkcachev2.NewReceipt(identity, hash, int64(len(data)), "recomputed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Publish(ctx, identity, recomputedReceipt, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	out, err = x.chunkV2(ctx, task, 1)
	if err != nil {
		t.Fatal(err)
	}
	recomputed, err := stagedag.NewCheckpoint(p, task, out, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Commit(ctx, recomputed); err != nil {
		t.Fatalf("equivalent recomputation collided with historical attempt: %v", err)
	}
	loaded, found, err := checkpoints.Load(ctx, scope, task)
	if err != nil || !found || loaded.AttemptCount != historical.AttemptCount || !reflect.DeepEqual(loaded.Output, recomputed.Output) {
		t.Fatalf("converged checkpoint found=%v attempt=%d err=%v", found, loaded.AttemptCount, err)
	}
}
func TestServiceV2AdmissionBeforeChunksAndNativeNamespace(t *testing.T) {
	s, v, e, j := nativeServiceFixtureV2(t)
	s.config.MemoryBytes = 1
	if _, err := s.buildPlanV2(context.Background(), j, v, e); err == nil {
		t.Fatal("memory underadmission")
	}
	s.config.MemoryBytes = 512 << 20
	s.config.DiskBytes = 1
	if _, err := s.buildPlanV2(context.Background(), j, v, e); err == nil {
		t.Fatal("disk underadmission")
	}
	s.config.DiskBytes = 256 << 20
	p, err := s.buildPlanV2(context.Background(), j, v, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range p.Tasks {
		if task.Profile == StageProfileV1 || task.CacheKey.Namespace == CacheNamespaceV1 {
			t.Fatal("legacy task identity")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.buildPlanV2(ctx, j, v, e); err == nil {
		t.Fatal("canceled plan built")
	}
	if reflect.DeepEqual(e.Manifest, api.ExecutionManifest{}) {
		t.Fatal("legacy manifest")
	}
}
