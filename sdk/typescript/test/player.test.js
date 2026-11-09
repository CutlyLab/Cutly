import assert from "node:assert/strict";
import test from "node:test";

import {
  DOCUMENT_V2_PLAYER_PROFILE,
  DocumentV2Player,
  HeadlessPlayer,
  PlayerStateError,
  frameIndexAtSample,
  sampleIndexAtFrame,
} from "../src/index.js";

function config(revision = "7") {
  return {
    revision,
    frameClock: {
      origin: { value: "0", rate: "1" },
      framesPerSecond: { value: "30000", rate: "1001" },
    },
    sampleClock: {
      origin: { value: "0", rate: "1" },
      samplesPerSecond: "48000",
    },
    frameRange: { first: "0", count: "300" },
    sampleRange: { first: "0", count: "480480" },
  };
}

test("uses the exact audio master clock for NTSC frame selection", () => {
  const source = config();
  assert.equal(frameIndexAtSample(source.frameClock, source.sampleClock, "0"), 0n);
  assert.equal(frameIndexAtSample(source.frameClock, source.sampleClock, "1601"), 0n);
  assert.equal(frameIndexAtSample(source.frameClock, source.sampleClock, "1602"), 1n);
  assert.equal(sampleIndexAtFrame(source.frameClock, source.sampleClock, "1"), 1602n);

  const player = new HeadlessPlayer(source);
  assert.equal(player.play().phase, "playing");
  const state = player.advanceAudioSample("1602");
  assert.equal(state.frameIndex, "1");
  assert.equal(state.sampleIndex, "1602");
});

test("Document v2 player uses an explicit profile with the same deterministic clock", () => {
  const player = new DocumentV2Player(config());
  assert.equal(player.snapshot().profile, DOCUMENT_V2_PLAYER_PROFILE);
  assert.equal(player.requestFrame().profile, DOCUMENT_V2_PLAYER_PROFILE);
  player.load(config("8"));
  assert.equal(player.snapshot().profile, DOCUMENT_V2_PLAYER_PROFILE);
  assert.equal(player.advanceAudioSample("1602").frameIndex, "1");
});

test("rejects stale async frames across seek and revision changes", () => {
  const player = new HeadlessPlayer(config());
  const first = player.requestFrame();
  player.seekFrame("10");
  assert.equal(player.acceptFrame(first), false);
  const current = player.requestFrame();
  assert.equal(player.acceptFrame(current), true);
  assert.equal(player.snapshot().presentedRequestID, current.requestID);

  player.load(config("8"));
  assert.equal(player.acceptFrame(current), false);
  assert.equal(player.snapshot().revision, "8");
  assert.equal(player.snapshot().presentedRequestID, null);
});

test("has explicit buffering transitions and merges adjacent ranges", () => {
  const player = new HeadlessPlayer(config());
  player.setBufferedFrameRanges([{ first: "0", count: "1" }]);
  assert.equal(player.play().phase, "playing");
  assert.equal(player.seekFrame("2").phase, "buffering");
  const state = player.setBufferedFrameRanges([
    { first: "0", count: "2" },
    { first: "2", count: "3" },
  ]);
  assert.equal(state.phase, "playing");
  assert.deepEqual(state.bufferedFrameRanges, [{ first: "0", count: "5" }]);
});

test("loops on the frame grid and emits a new seek epoch", () => {
  const player = new HeadlessPlayer(config());
  player.setLoopFrameRange({ first: "2", count: "3" });
  player.seekFrame("4");
  player.play();
  const before = BigInt(player.snapshot().seekEpoch);
  const sampleAtFive = sampleIndexAtFrame(config().frameClock, config().sampleClock, "5");
  const state = player.advanceAudioSample(sampleAtFive);
  assert.equal(state.frameIndex, "2");
  assert.ok(BigInt(state.seekEpoch) > before);
  assert.equal(state.phase, "playing");
});

test("fails closed for inexact numbers and invalid ranges", () => {
  assert.throws(
    () => new HeadlessPlayer({ ...config(), revision: Number.MAX_SAFE_INTEGER + 1 }),
    (error) => error instanceof PlayerStateError && error.code === "PLAYER.INTEGER_INVALID",
  );
  const player = new HeadlessPlayer(config());
  assert.throws(
    () => player.seekFrame("300"),
    (error) => error instanceof PlayerStateError && error.code === "PLAYER.RANGE_INVALID",
  );
  assert.throws(() => player.setLoopFrameRange({ first: "0", count: "0" }), /PLAYER.RANGE_INVALID/);
  assert.throws(() => player.setBufferedFrameRanges([{ first: "299", count: "2" }]), /PLAYER.BUFFER_RANGE_INVALID/);
  assert.throws(
    () => frameIndexAtSample(
      { origin: { value: "-9223372036854775808", rate: "1" }, framesPerSecond: { value: "9223372036854775807", rate: "1" } },
      { origin: { value: "9223372036854775807", rate: "1" }, samplesPerSecond: "1" },
      "9223372036854775807",
    ),
    (error) => error instanceof PlayerStateError && error.code === "PLAYER.TIME_OVERFLOW",
  );
});

test("ends without reading an exclusive-end frame", () => {
  const player = new HeadlessPlayer(config());
  player.play();
  const state = player.advanceAudioSample("480480");
  assert.equal(state.phase, "ended");
  assert.equal(state.frameIndex, "299");
  assert.equal(state.sampleIndex, "480480");
  assert.equal(state.desiredPlaying, false);
});
