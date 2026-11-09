# `@cutly/sdk`

This package is Cutly's dependency-free TypeScript/JavaScript authoring and
protocol surface. An agent or developer writes ordinary composition code;
`MotionCompositionV2` compiles it into the exact Document v2 consumed by the
native renderer. Document v2 is the deterministic intermediate representation,
not the interface authors are expected to write by hand.

```js
import { createMotionCompositionV2 } from "@cutly/sdk";

const film = createMotionCompositionV2({
  id: "launch", width: 1920, height: 1080, fps: 30, durationFrames: 150,
});

film
  .fontAsset({ id: "font", uri: "file:///fonts/Inter.ttf", contentHash: "sha256:…" })
  .rectangleAsset({ id: "panel", width: 900, height: 420, fill: { r: 800, g: 1200, b: 1800 } })
  .textAsset({
    id: "title", text: "VIDEO, WRITTEN AS CODE.", fontAssetId: "font",
    fontSize: 112, lineHeight: 124, width: 1500, height: 180,
    fill: { r: 65535, g: 65535, b: 65535 },
  });

film.scene("hook", { durationFrames: 150 }, (scene) => {
  scene.shape("panel-layer", "panel");
  scene.text("title-layer", "title", { startFrame: 12 })
    .position(210, 410)
    .animateOpacity([{ frame: 0, value: 0 }, { frame: 14, value: 1 }]);
});

const document = film.build();
```

The complete runnable example is
[`typescript-motion-composition.mjs`](../../examples/typescript-motion-composition.mjs).
It emits canonical Document v2 JSON which can be initialized as a project and
rendered through the native pipeline.

The protocol layer also includes the cross-language canonical integer JSON
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

Document v2 uses the independent `CutlyAgentV2Client` and
`HTTPAgentV2Transport` at `/v2/agent`. Its receipts remain exact canonical
bytes so arbitrary-precision metadata is never rounded through JavaScript
`Number`. The frozen restricted RC client below remains available for
explicit legacy integrations.

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

`DocumentV2Player` selects the v2 player profile. Bind it to native or daemon
providers with `DocumentV2PreviewAdapter`, which validates plan/manifest,
frame/sample ranges and stale-request identity before presenting a frame or
returning a PCM preview block.

`HeadlessTimeline` holds only selection/playhead state and dispatches the seven
versioned transaction operations. Its trim/move/split/delete/retrack/property
helpers, plus immutable waveform and caption lane read models, never expose a
mutable Document array.

`timelinePrimitive`, `trackPrimitive`, `clipPrimitive`, `trimHandle`,
`playheadPrimitive`, and `selectionPrimitive` provide the corresponding
immutable headless UI read surface. They are projections of compiled state,
not an alternate mutable project model; edits still go through
`HeadlessTimeline` typed operations and the Agent Plan → Apply protocol.
