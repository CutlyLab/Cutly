import assert from "node:assert/strict";
import test from "node:test";

import { DocumentV2Player, DocumentV2PreviewAdapter } from "../src/index.js";

const hash = (fill) => `sha256:${fill.repeat(64)}`;
const player = () => new DocumentV2Player({
  revision: 1n,
  frameClock: { origin: { value: 0n, rate: 1n }, framesPerSecond: { value: 30n, rate: 1n } },
  sampleClock: { origin: { value: 0n, rate: 1n }, samplesPerSecond: 48000n },
  frameRange: { first: 0n, count: 30n },
  sampleRange: { first: 0n, count: 48000n },
});

function adapter(frameHook) {
  return new DocumentV2PreviewAdapter({
    planFingerprint: hash("a"), executionManifestFingerprint: hash("b"),
    renderFrame: frameHook ?? (async (request) => ({ ...request, content_hash: hash("c"), uri: "memory:frame" })),
    renderAudio: async (request) => ({ ...request, content_hash: hash("d"), pcm_profile: "cutly.pcm.s32le.planar.v2" }),
  });
}

test("Document v2 preview presents only a bound current frame", async () => {
  const current = player();
  const rendered = await adapter().renderCurrentFrame(current);
  assert.equal(rendered.presented, true);
  assert.equal(rendered.frame.composition_frame_index, "0");
  assert.equal(current.snapshot().presentedRequestID, rendered.token.requestID);
});

test("Document v2 preview rejects stale and identity-drifted frames", async () => {
  let release;
  const pending = adapter((request) => new Promise((resolve) => { release = () => resolve({ ...request, content_hash: hash("c"), uri: "memory:frame" }); }));
  const current = player();
  const work = pending.renderCurrentFrame(current);
  current.seekFrame(2n);
  release();
  assert.equal((await work).presented, false);
  await assert.rejects(() => adapter(async (request) => ({ ...request, plan_fingerprint: hash("0"), content_hash: hash("c"), uri: "memory:frame" })).renderCurrentFrame(player()), /FRAME_BINDING/u);
});

test("Document v2 audio preview binds the exact sample range", async () => {
  const rendered = await adapter().renderAudioRange({ first: "1600", count: "800" });
  assert.deepEqual(rendered.sample_range, { first: "1600", count: "800" });
  assert.equal(rendered.pcm_profile, "cutly.pcm.s32le.planar.v2");
});
