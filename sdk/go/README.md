# Cutly Go SDK

Import `github.com/CutlyLab/Cutly/sdk/go/cutly` for the release-candidate
public façade. It exposes:

- strict Document v2 decode/hash/validation and immutable evaluator compile;
- explicitly versioned v1 transaction and RenderPlan helpers;
- RenderJob/ExecutionManifest protocol types;
- a transport-neutral Agent client and bounded HTTP transport.

```go
transport, err := cutly.NewHTTPTransport("http://127.0.0.1:9817/v1/agent", nil)
if err != nil { /* handle */ }
client, err := cutly.NewClient(transport)
if err != nil { /* handle */ }

result, err := client.Invoke(
    ctx,
    "inspect-42",
    cutly.ToolProject,
    "inspect",
    "demo",
    struct{}{},
)
```

Typed convenience methods avoid hand-writing action payloads:

```go
planned, err := client.PlanEditV1(ctx, "plan-42", "demo", transaction)
applied, err := client.ApplyEditV1(ctx, "apply-42", "demo", transaction.ID, transaction.ExpectedRevision)
snapshot, err := cutly.DecodeProjectInspectReceipt(result.Receipt)
```

The public render path starts from the durable project; callers do not build a
backend/runtime manifest by hand:

```go
planned, err := client.RenderPlan(ctx, "render-plan-42", "demo", renderRange, target)
if err != nil || !planned.OK { /* handle transport error or diagnostics */ }
planReceipt, err := cutly.DecodeRenderPlanReceipt(planned.Receipt)
err = cutly.ValidateRenderPlanReceipt(planReceipt, renderRange, target)

resources := []cutly.RenderPrepareResourceV1{
    {AssetID: "video", Locator: "/srv/cutly/input/video.mp4"},
    {AssetID: "audio", Locator: "/srv/cutly/input/audio.wav"},
}
prepared, err := client.PrepareRender(ctx, "prepare-42", "demo", planReceipt.Plan, resources, nil)
if err != nil || !prepared.OK { /* handle transport error or diagnostics */ }
prepareReceipt, err := cutly.DecodeRenderPrepareReceipt(prepared.Receipt)
err = cutly.ValidateRenderPrepareReceipt(prepareReceipt, planReceipt.Plan, resources, nil)

ready, err := client.PreflightRender(
    ctx, "preflight-42", "demo", planReceipt.Plan, prepareReceipt.ExecutionManifest,
)
if err != nil || !ready.OK { /* handle transport error or diagnostics */ }
job, err := cutly.BuildRenderJobV1(
    "job-42", "job-42-once", planReceipt.Plan, prepareReceipt.ExecutionManifest,
    "file:///srv/cutly/output/job-42.mp4",
)
queued, err := client.SubmitRender(ctx, "submit-42", "demo", job)
if err != nil || !queued.OK { /* handle transport error or diagnostics */ }
status, err := cutly.DecodeRenderJobSnapshotReceipt(queued.Receipt)
err = cutly.ValidateRenderJobSnapshotReceipt(status, job)
```

The same client exposes typed inspect/status/history, discard/revert,
plan/prepare/preflight/frame/render/status/cancel calls. Receipt decoders
require the exact canonical wire profile and reject unknown fields; validation
helpers bind receipts back to the caller's frozen inputs. A complete runnable
flow is in [`examples/go-render-agent`](../../examples/go-render-agent).

Exact-frame callers likewise pass the request they actually submitted; a
self-consistent receipt for a different frame is rejected:

```go
frameReceipt, err := cutly.DecodeRenderFrameReceipt(result.Receipt)
err = cutly.ValidateRenderFrameReceipt(frameReceipt, frameRequest, plan, manifest)
```

Request IDs are caller-owned. `ok:false` is a valid business result; transport,
decode and correlation failures are Go errors. V1 transactions are not applied
directly to Document v2: doing so would silently erase typed v2 fields.
