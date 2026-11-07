import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  AgentReceiptError,
  createRenderJob,
  decodeCanonicalJSON,
  decodeRenderJobHistoryPageReceipt,
  decodeRenderJobSnapshotReceipt,
  decodeRenderPlanReceipt,
  decodeRenderPreflightReceipt,
  decodeRenderPrepareReceipt,
  digestCanonicalValue,
  encodeCanonicalValue,
} from "../src/index.js";

const digest = (fill) => `sha256:${fill.repeat(64)}`;

async function fixtures() {
  const range = {
    start: { value: 0n, rate: 1n },
    duration: { value: 1n, rate: 1n },
  };
  const target = {
    timeline_id: "timeline",
    width: 1280n,
    height: 720n,
    frame_clock: {
      origin: { value: 0n, rate: 1n },
      frames_per_second: { value: 30n, rate: 1n },
    },
    sample_clock: {
      origin: { value: 0n, rate: 1n },
      samples_per_second: 48_000n,
    },
    channels: 2n,
    pixel_format: "yuv420p",
    color_space: "bt709",
  };
  const plan = await fingerprint({
    schema_version: 3n,
    semantic_profile: "cutly.semantic.v1",
    document_id: "document",
    revision: 7n,
    document_hash: digest("d"),
    timeline_id: "timeline",
    requested_range: range,
    target,
    frame_range: { first: 0n, count: 30n },
    sample_range: { first: 0n, count: 48_000n },
    required_capabilities: {
      track_kinds: ["audio", "video"],
      asset_kinds: ["audio", "video"],
    },
    video: [{
      track_id: "track-video", clip_id: "clip-video", asset_id: "video",
      track_index: 0n, clip_index: 0n,
      timeline_range: range, source_range: range,
      visible_timeline_range: range, visible_source_range: range,
    }],
    audio: [{
      track_id: "track-audio", clip_id: "clip-audio", asset_id: "audio",
      track_index: 1n, clip_index: 0n,
      timeline_range: range, source_range: range,
      visible_timeline_range: range, visible_source_range: range,
    }],
    resources: [
      { asset_id: "audio", kind: "audio", content_hash: digest("b") },
      { asset_id: "video", kind: "video", content_hash: digest("a") },
    ],
  });
  const manifest = await fingerprint({
    schema_version: 1n,
    plan_fingerprint: plan.fingerprint,
    semantic_profile: "cutly.semantic.v1",
    compiler_build: digest("c"),
    evaluator_build: digest("e"),
    resources: [
      {
        asset_id: "audio", kind: "audio", content_hash: digest("b"), size_bytes: 200n,
        locator_scheme: "file", locator: "/media/audio.wav", probe_fingerprint: digest("1"),
      },
      {
        asset_id: "video", kind: "video", content_hash: digest("a"), size_bytes: 400n,
        locator_scheme: "file", locator: "/media/video.mp4", probe_fingerprint: digest("2"),
      },
    ],
    backend: {
      backend_id: "ffmpeg-hardcut-v1",
      build_fingerprint: digest("3"),
      track_kinds: ["audio", "video"],
      asset_kinds: ["audio", "video"],
      max_width: 3840n,
      max_height: 2160n,
    },
    media_runtime: {
      ffmpeg_build: digest("4"),
      decoder_profile: "ffmpeg-hardcut-v1",
      encoder_profile: "ffmpeg-hardcut-v1",
      color_profile: "cutly.bt709.v1",
    },
    output: {
      reference_profile: "cutly.reference.hardcut.v1",
      format: "mp4",
      width: 1280n,
      height: 720n,
      pixel_format: "yuv420p",
      color_space: "bt709",
      video_codec: "h264",
      audio_codec: "aac",
      sample_rate: 48_000n,
      channels: 2n,
      decoded_video_profile: "cutly.decoded-video.rgba8.interleaved.top-down.tight.v1",
      decoded_audio_profile: "cutly.decoded-audio.pcm-s16le.interleaved.v1",
    },
    deterministic_seed: 73n,
  });
  const resources = [
    { assetId: "audio", locator: "/media/audio.wav" },
    { assetId: "video", locator: "/media/video.mp4" },
  ];
  const job = createRenderJob({
    jobId: "job-1",
    idempotencyKey: "job-1-once",
    artifactUri: "file:///tmp/cutly-job-1.mp4",
    plan,
    executionManifest: manifest,
  });
  return { range, target, plan, manifest, resources, job };
}

test("strict receipt decoders validate and bind the complete public render chain", async () => {
  const fixture = await fixtures();
  const planned = result({
    action: "plan", requestId: "plan", state: "planned", renderRequired: true,
    receipt: { profile: "cutly.agent-render-plan.v1", plan: fixture.plan },
  });
  const planReceipt = await decodeRenderPlanReceipt(planned, {
    requestId: "plan", projectId: "project", range: fixture.range, target: fixture.target,
  });
  assert.equal(planReceipt.plan.fingerprint, fixture.plan.fingerprint);
  assert.equal(Object.isFrozen(planReceipt), true);

  const prepared = result({
    action: "prepare", requestId: "prepare", state: "prepared", renderRequired: true,
    receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: fixture.manifest },
  });
  const prepareReceipt = await decodeRenderPrepareReceipt(prepared, {
    requestId: "prepare", projectId: "project", plan: fixture.plan,
    resources: fixture.resources, deterministicSeed: 73n,
  });
  assert.equal(prepareReceipt.execution_manifest.fingerprint, fixture.manifest.fingerprint);

  const preflighted = result({
    action: "preflight", requestId: "preflight", state: "ready", renderRequired: true,
    receipt: {
      profile: "cutly.agent-render-preflight.v1",
      plan_fingerprint: fixture.plan.fingerprint,
      execution_manifest_fingerprint: fixture.manifest.fingerprint,
      backend_id: fixture.manifest.backend.backend_id,
      backend_build_fingerprint: fixture.manifest.backend.build_fingerprint,
    },
  });
  const preflightReceipt = await decodeRenderPreflightReceipt(preflighted, {
    requestId: "preflight", projectId: "project", plan: fixture.plan,
    executionManifest: fixture.manifest,
  });
  assert.equal(preflightReceipt.backend_id, "ffmpeg-hardcut-v1");

  const queued = snapshot(fixture.job, "queued", 1n);
  const submitted = result({
    action: "render", requestId: "submit", state: "queued", renderRequired: true,
    jobId: fixture.job.id, receipt: queued,
  });
  assert.equal(decodeRenderJobSnapshotReceipt(submitted, {
    requestId: "submit", projectId: "project", action: "render", job: fixture.job,
  }).state, "queued");

  const complete = snapshot(fixture.job, "complete", 3n, {
    artifact: artifact(fixture.job),
  });
  const status = result({
    action: "status", requestId: "status", state: "complete", renderRequired: false,
    jobId: fixture.job.id, receipt: complete,
  });
  assert.equal(decodeRenderJobSnapshotReceipt(status, {
    requestId: "status", projectId: "project", action: "status", job: fixture.job,
  }).artifact.content_hash, digest("8"));
});

test("render history page decoder enforces cursor continuity and snapshot binding", async () => {
  const fixture = await fixtures();
  const completeArtifact = artifact(fixture.job);
  const completeSnapshot = snapshot(fixture.job, "complete", 3n, { artifact: completeArtifact });
  const submitted = {
    schema_version: 1n,
    job_id: fixture.job.id,
    sequence: 1n,
    kind: "submitted",
    state: "queued",
    job: fixture.job,
  };
  const started = {
    schema_version: 1n,
    job_id: fixture.job.id,
    sequence: 2n,
    kind: "started",
    state: "running",
  };
  const completed = {
    schema_version: 1n,
    job_id: fixture.job.id,
    sequence: 3n,
    kind: "completed",
    state: "complete",
    artifact: completeArtifact,
  };
  const page = {
    schema_version: 1n,
    profile: "cutly.render-job-history.v1",
    after_sequence: 0n,
    next_after_sequence: 2n,
    caught_up: false,
    events: [submitted, started],
    snapshot: completeSnapshot,
  };
  const response = result({
    action: "history",
    requestId: "history",
    state: "complete",
    renderRequired: false,
    jobId: fixture.job.id,
    receipt: page,
  });
  const decoded = decodeRenderJobHistoryPageReceipt(response, {
    requestId: "history",
    projectId: "project",
    job: fixture.job,
    afterSequence: 0n,
  });
  assert.equal(decoded.next_after_sequence, 2n);
  assert.equal(decoded.events[0].job.id, fixture.job.id);
  assert.equal(Object.isFrozen(decoded), true);
  assert.equal(Object.isFrozen(decoded.events), true);

  const finalPage = {
    ...page,
    after_sequence: 2n,
    next_after_sequence: 3n,
    caught_up: true,
    events: [completed],
  };
  assert.equal(decodeRenderJobHistoryPageReceipt(result({
    action: "history",
    requestId: "history-final",
    state: "complete",
    renderRequired: false,
    jobId: fixture.job.id,
    receipt: finalPage,
  }), {
    requestId: "history-final",
    projectId: "project",
    job: fixture.job,
    afterSequence: 2n,
  }).caught_up, true);

  const invalidPages = [
    { ...page, next_after_sequence: 1n },
    { ...page, caught_up: true },
    { ...page, events: [{ ...submitted, sequence: 2n }, started] },
    { ...page, events: [] },
    { ...page, hostile: true },
    { ...finalPage, snapshot: { ...completeSnapshot, state: "running", artifact: undefined } },
    { ...finalPage, events: [{ ...completed, artifact: { ...completeArtifact, uri: "file:///tmp/other.mp4" } }] },
    {
      ...page,
      next_after_sequence: 3n,
      caught_up: true,
      events: [submitted, {
        schema_version: 1n,
        job_id: fixture.job.id,
        sequence: 2n,
        kind: "failed",
        state: "failed",
        failure_class: "non_retryable",
        failure_message: "failed before an impossible completion",
      }, completed],
    },
    {
      ...page,
      events: [submitted, {
        schema_version: 1n,
        job_id: fixture.job.id,
        sequence: 2n,
        kind: "cancel_requested",
        state: "canceling",
      }],
    },
    {
      ...page,
      after_sequence: 1n,
      next_after_sequence: 2n,
      caught_up: true,
      events: [{
        schema_version: 1n,
        job_id: fixture.job.id,
        sequence: 2n,
        kind: "failed",
        state: "failed",
        failure_class: "non_retryable",
        failure_message: "x".repeat((64 << 10) + 1),
      }],
      snapshot: snapshot(fixture.job, "failed", 2n, {
        failure_class: "non_retryable",
        failure_message: "x".repeat((64 << 10) + 1),
      }),
    },
  ];
  for (const [index, invalid] of invalidPages.entries()) {
    assert.throws(() => decodeRenderJobHistoryPageReceipt(result({
      action: "history",
      requestId: `history-invalid-${index}`,
      state: invalid.snapshot.state,
      renderRequired: !new Set(["canceled", "failed", "complete"]).has(invalid.snapshot.state),
      jobId: fixture.job.id,
      receipt: invalid,
    }), {
      requestId: `history-invalid-${index}`,
      projectId: "project",
      job: fixture.job,
      afterSequence: invalid.after_sequence,
    }), (error) => error instanceof AgentReceiptError);
  }
  assert.throws(() => decodeRenderJobHistoryPageReceipt(response, {
    requestId: "history",
    projectId: "project",
    job: fixture.job,
    afterSequence: 1n,
  }), receiptCode("RECEIPT.BINDING_INVALID"));
});

test("render plan receipt validator accepts the Go conformance fixture", async () => {
  const raw = await readFile(
    new URL("../../../conformance/fixtures/render-plan-v3.valid.json", import.meta.url),
    "utf8",
  );
  const plan = decodeCanonicalJSON(raw);
  const decoded = await decodeRenderPlanReceipt(result({
    action: "plan", requestId: "fixture-plan", state: "planned", renderRequired: true,
    receipt: { profile: "cutly.agent-render-plan.v1", plan },
  }), {
    requestId: "fixture-plan", projectId: "project",
    range: plan.requested_range, target: plan.target,
  });
  assert.equal(decoded.plan.fingerprint, "sha256:34384100c28da27fb0c3f471602ef7af073de32dc3a3657d0c03385ad5393790");
});

test("render plan validator rejects self-consistent fingerprints over invalid derived semantics", async () => {
  const fixture = await fixtures();
  const cases = [
    {
      name: "compiled frame range",
      change: (plan) => ({ ...plan, frame_range: { ...plan.frame_range, count: plan.frame_range.count + 1n } }),
    },
    {
      name: "visible source projection",
      change: (plan) => ({
        ...plan,
        video: [{
          ...plan.video[0],
          visible_source_range: {
            ...plan.video[0].visible_source_range,
            start: { value: 1n, rate: 1n },
          },
        }],
      }),
    },
    {
      name: "derived capabilities",
      change: (plan) => ({
        ...plan,
        required_capabilities: { ...plan.required_capabilities, asset_kinds: ["video"] },
      }),
    },
    {
      name: "resource reference",
      change: (plan) => ({
        ...plan,
        audio: [{ ...plan.audio[0], asset_id: "missing" }],
      }),
    },
  ];
  for (const entry of cases) {
    await assert.rejects(async () => {
      const plan = await fingerprint(entry.change(fixture.plan));
      await decodeRenderPlanReceipt(result({
        action: "plan", requestId: `invalid-${entry.name}`, state: "planned", renderRequired: true,
        receipt: { profile: "cutly.agent-render-plan.v1", plan },
      }), {
        requestId: `invalid-${entry.name}`, projectId: "project",
        range: plan.requested_range, target: plan.target,
      });
    }, receiptCode("RECEIPT.INVALID"), entry.name);
  }
});

test("receipt validators use Go UTF-8 ordering for Unicode identities", async () => {
  const fixture = await fixtures();
  const first = "\uE000";
  const second = "\u{10000}";
  // Go compares UTF-8 bytes, where U+E000 precedes U+10000. JavaScript's
  // native UTF-16 relational order is the reverse.
  assert.equal(first < second, false);
  assert.equal(Buffer.compare(Buffer.from(first), Buffer.from(second)) < 0, true);

  const plan = await fingerprint({
    ...fixture.plan,
    audio: [{ ...fixture.plan.audio[0], asset_id: first }],
    video: [{ ...fixture.plan.video[0], asset_id: second }],
    resources: [
      { ...fixture.plan.resources[0], asset_id: first },
      { ...fixture.plan.resources[1], asset_id: second },
    ],
  });
  await decodeRenderPlanReceipt(result({
    action: "plan", requestId: "unicode-plan", state: "planned", renderRequired: true,
    receipt: { profile: "cutly.agent-render-plan.v1", plan },
  }), {
    requestId: "unicode-plan", projectId: "project",
    range: plan.requested_range, target: plan.target,
  });

  const manifest = await fingerprint({
    ...fixture.manifest,
    plan_fingerprint: plan.fingerprint,
    resources: [
      { ...fixture.manifest.resources[0], asset_id: first },
      { ...fixture.manifest.resources[1], asset_id: second },
    ],
    fonts: [
      { family: first, content_hash: digest("5"), locator: "/fonts/first.ttf" },
      { family: second, content_hash: digest("6"), locator: "/fonts/second.ttf" },
    ],
    plugins: [
      { id: first, version: "1", content_hash: digest("7") },
      { id: second, version: "1", content_hash: digest("8") },
    ],
  });
  const resources = [
    { assetId: first, locator: "/media/audio.wav" },
    { assetId: second, locator: "/media/video.mp4" },
  ];
  const decoded = await decodeRenderPrepareReceipt(result({
    action: "prepare", requestId: "unicode-prepare", state: "prepared", renderRequired: true,
    receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: manifest },
  }), {
    requestId: "unicode-prepare", projectId: "project", plan, resources, deterministicSeed: 73n,
  });
  assert.equal(decoded.execution_manifest.fingerprint, manifest.fingerprint);
});

test("receipt fingerprints follow Go omitempty normalization", async () => {
  const raw = await readFile(
    new URL("../../../conformance/fixtures/render-plan-v3.valid.json", import.meta.url),
    "utf8",
  );
  const goPlan = decodeCanonicalJSON(raw);
  const planWithEmptySet = {
    ...goPlan,
    required_capabilities: { ...goPlan.required_capabilities, features: [] },
  };
  const decodedPlan = await decodeRenderPlanReceipt(result({
    action: "plan", requestId: "empty-plan", state: "planned", renderRequired: true,
    receipt: { profile: "cutly.agent-render-plan.v1", plan: planWithEmptySet },
  }), {
    requestId: "empty-plan", projectId: "project",
    range: planWithEmptySet.requested_range, target: planWithEmptySet.target,
  });
  assert.equal(decodedPlan.plan.fingerprint, goPlan.fingerprint);

  const fixture = await fixtures();
  const manifestWithEmptyFields = {
    ...fixture.manifest,
    browser_fingerprint: "",
    fonts: [],
    plugins: [],
    backend: { ...fixture.manifest.backend, features: [] },
    media_runtime: { ...fixture.manifest.media_runtime, text_shaper_build: "" },
  };
  const prepared = result({
    action: "prepare", requestId: "empty-prepare", state: "prepared", renderRequired: true,
    receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: manifestWithEmptyFields },
  });
  await decodeRenderPrepareReceipt(prepared, {
    requestId: "empty-prepare", projectId: "project", plan: fixture.plan,
    resources: fixture.resources, deterministicSeed: 73n,
  });

  const wronglySigned = { ...manifestWithEmptyFields, fingerprint: "" };
  wronglySigned.fingerprint = (await digestCanonicalValue(wronglySigned)).digest;
  await assert.rejects(
    () => decodeRenderPrepareReceipt(result({
      action: "prepare", requestId: "wrong-empty", state: "prepared", renderRequired: true,
      receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: wronglySigned },
    }), {
      requestId: "wrong-empty", projectId: "project", plan: fixture.plan,
      resources: fixture.resources, deterministicSeed: 73n,
    }),
    receiptCode("RECEIPT.FINGERPRINT_INVALID"),
  );
});

test("execution manifest validator enforces Go font locator semantics", async () => {
  const fixture = await fixtures();
  const manifest = await fingerprint({
    ...fixture.manifest,
    fonts: [{ family: "Pinned", content_hash: digest("5"), locator: "relative/font.ttf" }],
  });
  await assert.rejects(
    () => decodeRenderPrepareReceipt(result({
      action: "prepare", requestId: "bad-font", state: "prepared", renderRequired: true,
      receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: manifest },
    }), {
      requestId: "bad-font", projectId: "project", plan: fixture.plan,
      resources: fixture.resources, deterministicSeed: 73n,
    }),
    receiptCode("RECEIPT.INVALID"),
  );
});

test("receipt decoders reject unknown fields, effectful values, and correlation failures", async () => {
  const fixture = await fixtures();
  const base = result({
    action: "plan", requestId: "plan", state: "planned", renderRequired: true,
    receipt: { profile: "cutly.agent-render-plan.v1", plan: fixture.plan },
  });
  const unknown = { ...base, receipt: { ...base.receipt, hostile: true } };
  await assert.rejects(
    () => decodeRenderPlanReceipt(unknown, {
      requestId: "plan", projectId: "project", range: fixture.range, target: fixture.target,
    }),
    receiptCode("RECEIPT.INVALID"),
  );
  await assert.rejects(
    () => decodeRenderPlanReceipt(base, {
      requestId: "other", projectId: "project", range: fixture.range, target: fixture.target,
    }),
    receiptCode("RECEIPT.CORRELATION_INVALID"),
  );

  let invoked = false;
  const effectful = Object.defineProperty({}, "requestId", {
    enumerable: true,
    get() { invoked = true; return "plan"; },
  });
  await assert.rejects(
    () => decodeRenderPlanReceipt(base, effectful),
    receiptCode("RECEIPT.INPUT_INVALID"),
  );
  assert.equal(invoked, false);

  const rejected = result({
    action: "plan", requestId: "plan", state: "rejected", renderRequired: false,
    ok: false,
    diagnostics: [{ code: "RENDER.PLAN_REJECTED", severity: "error", message: "no" }],
  });
  await assert.rejects(
    () => decodeRenderPlanReceipt(rejected, {
      requestId: "plan", projectId: "project", range: fixture.range, target: fixture.target,
    }),
    receiptCode("RECEIPT.RENDER_REJECTED"),
  );
});

test("plan, prepare, and preflight validators reject fingerprint and immutable binding drift", async () => {
  const fixture = await fixtures();
  const tamperedPlan = { ...fixture.plan, document_id: "tampered" };
  await assert.rejects(
    () => decodeRenderPlanReceipt(result({
      action: "plan", requestId: "plan", state: "planned", renderRequired: true,
      receipt: { profile: "cutly.agent-render-plan.v1", plan: tamperedPlan },
    }), {
      requestId: "plan", projectId: "project", range: fixture.range, target: fixture.target,
    }),
    receiptCode("RECEIPT.FINGERPRINT_INVALID"),
  );

  const prepared = result({
    action: "prepare", requestId: "prepare", state: "prepared", renderRequired: true,
    receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: fixture.manifest },
  });
  await assert.rejects(
    () => decodeRenderPrepareReceipt(prepared, {
      requestId: "prepare", projectId: "project", plan: fixture.plan,
      resources: [{ ...fixture.resources[0], locator: "/media/other.wav" }, fixture.resources[1]],
      deterministicSeed: 73n,
    }),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );
  await assert.rejects(
    () => decodeRenderPrepareReceipt(prepared, {
      requestId: "prepare", projectId: "project", plan: fixture.plan,
      resources: fixture.resources, deterministicSeed: 74n,
    }),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );
  const undersizedManifest = await fingerprint({
    ...fixture.manifest,
    backend: { ...fixture.manifest.backend, max_width: fixture.plan.target.width - 1n },
  });
  await assert.rejects(
    () => decodeRenderPrepareReceipt(result({
      action: "prepare", requestId: "undersized", state: "prepared", renderRequired: true,
      receipt: { profile: "cutly.agent-render-prepare.v1", execution_manifest: undersizedManifest },
    }), {
      requestId: "undersized", projectId: "project", plan: fixture.plan,
      resources: fixture.resources, deterministicSeed: 73n,
    }),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );

  const preflighted = result({
    action: "preflight", requestId: "preflight", state: "ready", renderRequired: true,
    receipt: {
      profile: "cutly.agent-render-preflight.v1",
      plan_fingerprint: fixture.plan.fingerprint,
      execution_manifest_fingerprint: fixture.manifest.fingerprint,
      backend_id: "other-backend",
      backend_build_fingerprint: fixture.manifest.backend.build_fingerprint,
    },
  });
  await assert.rejects(
    () => decodeRenderPreflightReceipt(preflighted, {
      requestId: "preflight", projectId: "project", plan: fixture.plan,
      executionManifest: fixture.manifest,
    }),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );
});

test("render snapshot decoder enforces exact identities and terminal unions", async () => {
  const fixture = await fixtures();
  const binding = { requestId: "status", projectId: "project", action: "status", job: fixture.job };
  const decode = (value, overrides = {}) => decodeRenderJobSnapshotReceipt(result({
    action: "status", requestId: "status", state: value.state,
    renderRequired: !new Set(["canceled", "failed", "complete"]).has(value.state),
    jobId: fixture.job.id, receipt: value, ...overrides,
  }), binding);

  assert.throws(
    () => decode({ ...snapshot(fixture.job, "queued", 1n), hostile: true }),
    receiptCode("RECEIPT.INVALID"),
  );
  assert.throws(
    () => decode(snapshot(fixture.job, "complete", 2n, { artifact: artifact(fixture.job) })),
    receiptCode("RECEIPT.INVALID"),
  );
  assert.throws(
    () => decode(snapshot(fixture.job, "complete", 3n)),
    receiptCode("RECEIPT.INVALID"),
  );
  assert.throws(
    () => decode(snapshot(fixture.job, "queued", 1n, { artifact: artifact(fixture.job) })),
    receiptCode("RECEIPT.INVALID"),
  );

  const wrongArtifact = { ...artifact(fixture.job), evidence_uri: "file:///tmp/foreign.evidence.json" };
  assert.throws(
    () => decode(snapshot(fixture.job, "complete", 3n, { artifact: wrongArtifact })),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );
  const wrongJob = { ...fixture.job, id: "job-2" };
  assert.throws(
    () => decode(snapshot(wrongJob, "queued", 1n)),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );
  assert.throws(
    () => decode(snapshot(fixture.job, "queued", 1n), { jobId: "other" }),
    receiptCode("RECEIPT.CORRELATION_INVALID"),
  );
});

test("render snapshot evidence URI follows Go decoded-path sidecar semantics", async () => {
  const fixture = await fixtures();
  for (const [artifactUri, evidenceUri] of [
    ["file:///tmp/%41.mp4", "file:///tmp/A.mp4.evidence.json"],
    ["file:///tmp/a%20b.mp4", "file:///tmp/a%20b.mp4.evidence.json"],
    ["file:///tmp/a%2Fb.mp4", "file:///tmp/a/b.mp4.evidence.json"],
  ]) {
    const job = { ...fixture.job, artifact_uri: artifactUri };
    const completeArtifact = { ...artifact(job), evidence_uri: evidenceUri };
    const response = result({
      action: "status", requestId: "status", state: "complete", renderRequired: false,
      jobId: job.id, receipt: snapshot(job, "complete", 3n, { artifact: completeArtifact }),
    });
    assert.equal(decodeRenderJobSnapshotReceipt(response, {
      requestId: "status", projectId: "project", action: "status", job,
    }).artifact.evidence_uri, evidenceUri);
  }

  const job = { ...fixture.job, artifact_uri: "file:///tmp/%41.mp4" };
  const rawSuffix = { ...artifact(job), evidence_uri: `${job.artifact_uri}.evidence.json` };
  assert.throws(
    () => decodeRenderJobSnapshotReceipt(result({
      action: "status", requestId: "status", state: "complete", renderRequired: false,
      jobId: job.id, receipt: snapshot(job, "complete", 3n, { artifact: rawSuffix }),
    }), { requestId: "status", projectId: "project", action: "status", job }),
    receiptCode("RECEIPT.BINDING_INVALID"),
  );
});

async function fingerprint(value) {
  const fingerprinted = { ...value };
  fingerprinted.fingerprint = (await digestCanonicalValue({ ...value, fingerprint: "" })).digest;
  return decodeCanonicalJSON(encodeCanonicalValue(fingerprinted));
}

function result({
  action,
  requestId,
  state,
  renderRequired,
  receipt,
  jobId,
  ok = true,
  diagnostics = [],
}) {
  return {
    protocol_version: 1n,
    ok,
    request_id: requestId,
    tool: "cutly_render",
    action,
    state,
    project_id: "project",
    ...(jobId === undefined ? {} : { job_id: jobId }),
    changed: false,
    replayed: false,
    render_required: renderRequired,
    diagnostics,
    ...(receipt === undefined ? {} : { receipt }),
    next_actions: [],
  };
}

function snapshot(job, state, lastSequence, optional = {}) {
  return {
    schema_version: 1n,
    job,
    state,
    last_sequence: lastSequence,
    ...optional,
  };
}

function artifact(job) {
  return {
    uri: job.artifact_uri,
    content_hash: digest("8"),
    size_bytes: 1024n,
    execution_manifest_fingerprint: job.execution_manifest_fingerprint,
    evidence_uri: `${job.artifact_uri}.evidence.json`,
    evidence_fingerprint: digest("9"),
    duration: job.timeline_range.duration,
    frame_count: job.frame_range.count,
    sample_count: job.sample_range.count,
  };
}

function receiptCode(code) {
  return (error) => error instanceof AgentReceiptError && error.code === code;
}
