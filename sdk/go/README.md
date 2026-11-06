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

ready, err := client.PreflightRender(ctx, "preflight-42", "demo", plan, executionManifest)
preview, err := client.RenderFrame(ctx, "frame-42", "demo", plan, executionManifest, frameRequest)
```

The same client exposes typed inspect/status/history, discard/revert, and
preflight/frame/render/status/cancel calls. Receipt decoders require the exact
canonical wire profile and reject unknown fields; validation helpers bind a
preflight or exact-frame receipt back to the caller's frozen plan and manifest.

Request IDs are caller-owned. `ok:false` is a valid business result; transport,
decode and correlation failures are Go errors. V1 transactions are not applied
directly to Document v2: doing so would silently erase typed v2 fields.
