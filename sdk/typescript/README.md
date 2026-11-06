# `@cutly/sdk`

This package is the dependency-free TypeScript/JavaScript protocol surface for
Cutly. The first frozen component is the cross-language canonical integer JSON
profile. It preserves arbitrary-size wire integers as tokens instead of
rounding them through JavaScript `Number`.

```js
import { digestCanonicalJSON } from "@cutly/sdk";

const result = await digestCanonicalJSON('{"revision":9223372036854775808}');
console.log(result.digest);
```

For object-model input, use bigint fields with `encodeCanonicalValue`; unsafe
numbers, `undefined`, accessors, sparse arrays, class instances and cycles fail
closed. `decodeCanonicalJSON` returns immutable null-prototype objects and
bigint number tokens.

The release-candidate Agent client uses the same strict envelope for project,
edit, and render actions. Request IDs are caller-owned for deterministic retry;
business rejection is returned as `ok: false`, while transport and correlation
failures throw `AgentProtocolError`.

```js
import {
  CutlyClient,
  HTTPAgentTransport,
  createAgentRequest,
  createRenderJob,
  decodeRenderJobSnapshotReceipt,
  decodeRenderPlanReceipt,
  decodeRenderPreflightReceipt,
  decodeRenderPrepareReceipt,
} from "@cutly/sdk";

const client = new CutlyClient(new HTTPAgentTransport("http://127.0.0.1:9817/v1/agent"));
const result = await client.call(createAgentRequest({
  requestId: "inspect-42",
  tool: "cutly_project",
  action: "inspect",
  projectId: "demo",
}));

// Typed convenience methods build the same strict envelope.
const status = await client.projectStatus({ requestId: "status-42", projectId: "demo" });

// Planning starts from the registered durable project.
const planned = await client.planRender({
  requestId: "render-plan-42", projectId: "demo", range, target,
});
const { plan } = await decodeRenderPlanReceipt(planned, {
  requestId: "render-plan-42", projectId: "demo", range, target,
});

// Host preparation owns FFmpeg, probe, backend and output identities.
const resources = [
  { assetId: "video", locator: "/srv/cutly/input/video.mp4" },
  { assetId: "audio", locator: "/srv/cutly/input/audio.wav" },
];
const prepared = await client.prepareRender({
  requestId: "prepare-42", projectId: "demo", plan,
  resources,
});
const { execution_manifest: executionManifest } = await decodeRenderPrepareReceipt(prepared, {
  requestId: "prepare-42", projectId: "demo", plan, resources,
});

// Preflight re-probes the same frozen environment before durable submission.
const ready = await client.preflightRender({
  requestId: "preflight-42", projectId: "demo", plan, executionManifest,
});
await decodeRenderPreflightReceipt(ready, {
  requestId: "preflight-42", projectId: "demo", plan, executionManifest,
});
const job = createRenderJob({
  jobId: "job-42", idempotencyKey: "job-42-once", plan, executionManifest,
  artifactUri: "file:///srv/cutly/output/job-42.mp4",
});
const queued = await client.submitRender({
  requestId: "submit-42", projectId: "demo", job,
});
const snapshot = decodeRenderJobSnapshotReceipt(queued, {
  requestId: "submit-42", projectId: "demo", action: "render", job,
});
```

The receipt decoders preserve business rejection as `AgentReceiptError`, reject
unknown fields and correlation drift, recompute plan/manifest fingerprints,
and verify request, backend, job, terminal-state and artifact bindings before
returning immutable null-prototype values. All wire integers are `bigint`. See
the complete runnable
[`typescript-render-agent.mjs`](../../examples/typescript-render-agent.mjs) for
terminal-state polling using the same validators.

`HeadlessPlayer` already provides the exact audio-master playback state machine:
NTSC-safe bigint frame selection, seek/revision epochs, explicit buffering,
frame-grid looping and stale asynchronous frame rejection. It owns no DOM and
does not mutate a Cutly Document.

`HeadlessTimeline` holds only selection/playhead state and dispatches the seven
versioned transaction operations. Its trim/move/split/delete/retrack/property
helpers, plus immutable waveform and caption lane read models, never expose a
mutable Document array.

`timelinePrimitive`, `trackPrimitive`, `clipPrimitive`, `trimHandle`,
`playheadPrimitive`, and `selectionPrimitive` provide the corresponding
immutable headless UI read surface. They are projections of compiled state,
not an alternate mutable project model; edits still go through
`HeadlessTimeline` typed operations and the Agent Plan → Apply protocol.
