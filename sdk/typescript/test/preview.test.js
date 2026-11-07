import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import { encodeCanonicalValue } from "../src/canonical-json.js";
import { HeadlessPlayer } from "../src/player.js";
import { AgentPreviewAdapter, PreviewFrameError, createExactFrameRequest } from "../src/preview.js";

const digest = (fill) => `sha256:${fill.repeat(64)}`;

function environment() {
  return {
    plan: {
      fingerprint: digest("a"),
      frame_range: { first: 0n, count: 30n },
    },
    executionManifest: {
      fingerprint: digest("b"),
      plan_fingerprint: digest("a"),
      backend: { backend_id: "cutly.ffmpeg-hardcut.v1", build_fingerprint: digest("c") },
      output: {
        reference_profile: "cutly.hardcut.bt709.v1",
        decoded_video_profile: "cutly.rgba8.bt709-full.v1",
        width: 64n,
        height: 64n,
      },
    },
  };
}

function player() {
  return new HeadlessPlayer({
    revision: 1n,
    frameClock: {
      origin: { value: 0n, rate: 1n },
      framesPerSecond: { value: 30n, rate: 1n },
    },
    sampleClock: { origin: { value: 0n, rate: 1n }, samplesPerSecond: 48_000n },
    frameRange: { first: 0n, count: 30n },
    sampleRange: { first: 0n, count: 48_000n },
  });
}

function frameArtifact(frame, manifest) {
  const timelineTime = { value: frame.composition_frame_index, rate: 30n };
  const evaluation = {
    composition_frame_index: frame.composition_frame_index,
    timeline_time: timelineTime,
  };
  return {
    uri: frame.artifact_uri,
    content_hash: digest("d"),
    size_bytes: 100n,
    decoded_content_hash: digest("e"),
    plan_fingerprint: frame.plan_fingerprint,
    execution_manifest_fingerprint: frame.execution_manifest_fingerprint,
    backend_id: manifest.backend.backend_id,
    backend_build_fingerprint: manifest.backend.build_fingerprint,
    reference_profile: manifest.output.reference_profile,
    png_encoding_profile: "cutly.png.rgba8.ffmpeg.single-frame.v1",
    decoded_video_profile: manifest.output.decoded_video_profile,
    width: manifest.output.width,
    height: manifest.output.height,
    composition_frame_index: frame.composition_frame_index,
    timeline_time: timelineTime,
    evaluation_fingerprint: digestValue(evaluation),
    evaluation,
  };
}

function digestValue(value) {
  return `sha256:${createHash("sha256").update(encodeCanonicalValue(value)).digest("hex")}`;
}

function successResult(input) {
  return {
    protocol_version: 1n,
    ok: true,
    request_id: input.requestId,
    tool: "cutly_render",
    action: "frame",
    state: "complete",
    project_id: input.projectId,
    changed: true,
    replayed: false,
    render_required: true,
    diagnostics: [],
    receipt: {
      profile: "cutly.agent-render-frame.v1",
      request: input.frame,
      artifact: frameArtifact(input.frame, input.executionManifest),
    },
    next_actions: [],
  };
}

function adapter(renderFrame) {
  const values = environment();
  return new AgentPreviewAdapter({
    client: { renderFrame },
    projectId: "project",
    plan: values.plan,
    executionManifest: values.executionManifest,
    requestIDForFrame: (token) => `preview-${token.seekEpoch}-${token.requestID}`,
    artifactURIForFrame: (token) => `file:///tmp/preview-${token.seekEpoch}-${token.requestID}.png`,
  });
}

test("createExactFrameRequest builds an immutable bigint-safe bound request", () => {
  const values = environment();
  const input = {
    id: "frame-1",
    idempotencyKey: "frame-1-once",
    plan: values.plan,
    executionManifest: values.executionManifest,
    compositionFrameIndex: 7n,
    artifactUri: "file:///tmp/frame-7.png",
  };
  const request = createExactFrameRequest(input);
  input.plan.frame_range.count = 1n;
  assert.equal(Object.getPrototypeOf(request), null);
  assert.equal(Object.isFrozen(request), true);
  assert.equal(request.schema_version, 1n);
  assert.equal(request.composition_frame_index, 7n);
  assert.equal(request.plan_fingerprint, digest("a"));
  assert.equal(request.execution_manifest_fingerprint, digest("b"));
  assert.equal(request.artifact_uri, "file:///tmp/frame-7.png");
  const fresh = environment();
  assert.throws(() => createExactFrameRequest({
    ...input, plan: fresh.plan, executionManifest: fresh.executionManifest, compositionFrameIndex: 30n,
  }), /outside plan\.frame_range/);
  assert.throws(() => createExactFrameRequest({
    ...input, plan: fresh.plan, executionManifest: fresh.executionManifest,
    artifactUri: "file:///tmp/../frame.png",
  }), /hostless file URL|clean absolute/);
});

test("createExactFrameRequest rejects effectful and inexact input without invoking it", () => {
  const values = environment();
  let invoked = false;
  const input = {
    id: "frame",
    idempotencyKey: "frame-once",
    plan: values.plan,
    executionManifest: values.executionManifest,
    compositionFrameIndex: 0n,
    artifactUri: "file:///tmp/frame.png",
  };
  Object.defineProperty(input, "id", {
    enumerable: true,
    get() { invoked = true; return "frame"; },
  });
  assert.throws(() => createExactFrameRequest(input), /not safely canonicalizable/);
  assert.equal(invoked, false);
  assert.throws(() => createExactFrameRequest({
    ...input,
    id: "frame",
    compositionFrameIndex: Number.MAX_SAFE_INTEGER + 1,
  }), /not safely canonicalizable/);
});

test("AgentPreviewAdapter presents only a valid current frame", async () => {
  let observed;
  const source = adapter(async (input, options) => {
    observed = { input, options };
    return successResult(input);
  });
  const current = player();
  const controller = new AbortController();
  const rendered = await source.renderCurrentFrame(current, { signal: controller.signal });
  assert.equal(rendered.presented, true);
  assert.equal(rendered.artifact.uri, rendered.request.artifact_uri);
  assert.equal(current.snapshot().presentedRequestID, rendered.token.requestID);
  assert.equal(observed.options.signal, controller.signal);
  assert.equal(observed.input.frame.composition_frame_index, 0n);
});

test("AgentPreviewAdapter rejects stale seek and superseded concurrent frames", async () => {
  const pending = [];
  const source = adapter((input) => new Promise((resolve) => pending.push({ input, resolve })));
  const current = player();

  const staleSeek = source.renderCurrentFrame(current);
  current.seekFrame(4n);
  const stalePending = pending.shift();
  stalePending.resolve(successResult(stalePending.input));
  assert.equal((await staleSeek).presented, false);

  const first = source.renderCurrentFrame(current);
  const second = source.renderCurrentFrame(current);
  const firstPending = pending.shift();
  const secondPending = pending.shift();
  secondPending.resolve(successResult(secondPending.input));
  assert.equal((await second).presented, true);
  firstPending.resolve(successResult(firstPending.input));
  assert.equal((await first).presented, false);
});

test("AgentPreviewAdapter never presents rejection, invalid receipt, or aborted work", async () => {
  const rejectedPlayer = player();
  const rejected = adapter(async (input) => {
    const result = successResult(input);
    delete result.receipt;
    result.ok = false;
    result.state = "rejected";
    result.diagnostics = [{ code: "RENDER.FRAME_FAILED", severity: "error", message: "failed" }];
    return result;
  });
  await assert.rejects(() => rejected.renderCurrentFrame(rejectedPlayer), (error) =>
    error instanceof PreviewFrameError && error.code === "PREVIEW.RENDER_REJECTED");
  assert.equal(rejectedPlayer.snapshot().presentedRequestID, null);

  const invalidPlayer = player();
  const invalid = adapter(async (input) => {
    const result = successResult(input);
    result.receipt.artifact.composition_frame_index += 1n;
    return result;
  });
  await assert.rejects(() => invalid.renderCurrentFrame(invalidPlayer), (error) =>
    error instanceof PreviewFrameError && error.code === "PREVIEW.RECEIPT_INVALID");
  assert.equal(invalidPlayer.snapshot().presentedRequestID, null);

  const badEvaluationPlayer = player();
  const badEvaluation = adapter(async (input) => {
    const result = successResult(input);
    result.receipt.artifact.evaluation.timeline_time = {
      ...result.receipt.artifact.evaluation.timeline_time,
      value: result.receipt.artifact.evaluation.timeline_time.value + 1n,
    };
    result.receipt.artifact.evaluation_fingerprint = digestValue(result.receipt.artifact.evaluation);
    return result;
  });
  await assert.rejects(() => badEvaluation.renderCurrentFrame(badEvaluationPlayer), (error) =>
    error instanceof PreviewFrameError && error.code === "PREVIEW.RECEIPT_INVALID" && /evaluation time/.test(error.message));
  assert.equal(badEvaluationPlayer.snapshot().presentedRequestID, null);

  const badFingerprintPlayer = player();
  const badFingerprint = adapter(async (input) => {
    const result = successResult(input);
    result.receipt.artifact.evaluation_fingerprint = digest("0");
    return result;
  });
  await assert.rejects(() => badFingerprint.renderCurrentFrame(badFingerprintPlayer), (error) =>
    error instanceof PreviewFrameError && error.code === "PREVIEW.RECEIPT_INVALID" && /fingerprint/.test(error.message));
  assert.equal(badFingerprintPlayer.snapshot().presentedRequestID, null);

  let calls = 0;
  const aborted = adapter(async () => { calls += 1; throw new Error("must not run"); });
  const controller = new AbortController();
  controller.abort(new Error("stop"));
  await assert.rejects(() => aborted.renderCurrentFrame(player(), { signal: controller.signal }), /stop/);
  assert.equal(calls, 0);
});
