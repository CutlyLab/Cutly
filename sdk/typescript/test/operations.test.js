import assert from "node:assert/strict";
import test from "node:test";

import {
  HEADLESS_TIMELINE_PROFILE,
  HEADLESS_UI_PROFILE,
  HeadlessTimeline,
  buildTransaction,
  captionLane,
  clipPrimitive,
  deleteClip,
  encodeCanonicalValue,
  moveClip,
  playheadPrimitive,
  setClipProperty,
  selectionPrimitive,
  trimClip,
  trimHandle,
  trackPrimitive,
  timelinePrimitive,
  waveformLane,
} from "../src/index.js";

const time = (value, rate = 1n) => ({ value, rate });
const range = (start, duration) => ({ start: time(start), duration: time(duration) });

test("builds canonical bigint-safe typed transactions", () => {
  const operations = [
    moveClip({ id: "move-1", clipId: "clip-1", at: time(2n, 1n) }),
    trimClip({ id: "trim-1", clipId: "clip-1", sourceRange: range(1n, 3n), ripple: true }),
    setClipProperty({ id: "property-1", clipId: "clip-1", property: "opacity", value: { numerator: 1n, denominator: 2n } }),
  ];
  const transaction = buildTransaction({
    id: "tx-1", idempotencyKey: "turn-1", expectedRevision: 18_446_744_073_709_551_615n, operations,
  });
  const encoded = new TextDecoder().decode(encodeCanonicalValue(transaction));
  assert.match(encoded, /"expected_revision":18446744073709551615/);
  assert.match(encoded, /"op":"trim"/);
  assert.ok(Object.isFrozen(transaction));
  assert.equal(transaction.operations[0].at.value, 2n);
});

test("property omission is an explicit delete and false ripple is canonical omission", () => {
  const property = setClipProperty({ id: "p", clipId: "c", property: "gain" });
  assert.equal(Object.hasOwn(property, "value"), false);
  const deletion = deleteClip({ id: "d", clipId: "c", ripple: false });
  assert.equal(Object.hasOwn(deletion, "ripple"), false);
});

test("headless timeline keeps ephemeral state and dispatches only typed operations", () => {
  const intents = [];
  const timeline = new HeadlessTimeline({ revision: "12", dispatch: (intent) => { intents.push(intent); return intent.operation.op; } });
  timeline.select({ trackIDs: ["video", "video"], clipIDs: ["clip-1"] });
  timeline.setPlayhead(time(1001n, 30000n));
  assert.equal(timeline.move({ id: "m", clipId: "clip-1", at: time(2n) }), "move");
  assert.equal(intents.length, 1);
  assert.equal(intents[0].profile, HEADLESS_TIMELINE_PROFILE);
  assert.equal(intents[0].expectedRevision, "12");
  assert.equal(intents[0].operation.clip_id, "clip-1");
  assert.deepEqual(timeline.snapshot().selectedTrackIDs, ["video"]);
  assert.deepEqual(timeline.snapshot().playhead, { value: "1001", rate: "30000" });
});

test("creates immutable waveform and caption lane read models", () => {
  const digest = `sha256:${"a".repeat(64)}`;
  const waveform = waveformLane({ id: "wave", trackId: "audio", assetId: "music", sampleRange: { first: 0n, count: 48000n }, peaksDigest: digest });
  const captions = captionLane({ id: "captions", trackId: "text", cues: [{ id: "cue-1", range: range(0n, 1n), text: "Cutly" }] });
  assert.equal(waveform.sample_range.count, 48000n);
  assert.equal(captions.cues[0].text, "Cutly");
  assert.ok(Object.isFrozen(captions.cues));
});

test("builds immutable timeline, track, clip, trim, playhead and selection primitives", () => {
  const clip = clipPrimitive({
    id: "clip-1", trackId: "video-1", assetId: "asset-1",
    timelineRange: range(1n, 3n), sourceRange: range(10n, 6n), label: "Opening",
  });
  const track = trackPrimitive({ id: "video-1", kind: "video", clips: [clip], label: "Video" });
  const timeline = timelinePrimitive({ id: "timeline-1", revision: 9n, tracks: [track] });
  const start = trimHandle({ clip, edge: "start" });
  const end = trimHandle({ clip, edge: "end" });
  const playhead = playheadPrimitive({ time: time(1001n, 30000n), frameIndex: 1n, sampleIndex: 1602n });
  const selection = selectionPrimitive({ trackIDs: ["video-1", "video-1"], clipIDs: ["clip-1"] });

  assert.equal(timeline.profile, HEADLESS_UI_PROFILE);
  assert.equal(timeline.tracks[0].clips[0].id, "clip-1");
  assert.equal(start.timeline_time.value, 1n);
  assert.equal(end.timeline_time.value, 4n);
  assert.equal(end.source_time.value, 16n);
  assert.equal(playhead.sample_index, 1602n);
  assert.deepEqual(selection.track_ids, ["video-1"]);
  assert.ok(Object.isFrozen(timeline.tracks));
  assert.ok(Object.isFrozen(timeline.tracks[0].clips));
});

test("headless UI primitives reject cross-track, duplicate and unsorted models", () => {
  const first = clipPrimitive({
    id: "first", trackId: "video", assetId: "a", timelineRange: range(2n, 1n), sourceRange: range(0n, 1n),
  });
  const earlier = clipPrimitive({
    id: "earlier", trackId: "video", assetId: "b", timelineRange: range(1n, 1n), sourceRange: range(0n, 1n),
  });
  const other = clipPrimitive({
    id: "other", trackId: "other", assetId: "c", timelineRange: range(3n, 1n), sourceRange: range(0n, 1n),
  });
  assert.throws(() => trackPrimitive({ id: "video", kind: "video", clips: [first, earlier] }), /sorted/);
  assert.throws(() => trackPrimitive({ id: "video", kind: "video", clips: [other] }), /another track/);
  assert.throws(() => trackPrimitive({ id: "video", kind: "video", clips: [first, first] }), /duplicate clip/);
  const track = trackPrimitive({ id: "video", kind: "video", clips: [first] });
  assert.throws(() => timelinePrimitive({ id: "t", revision: 0n, tracks: [track, track] }), /duplicate track/);
  assert.throws(() => trimHandle({ clip: first, edge: "middle" }), /edge/);
});

test("operation builders fail before dispatching ambiguous input", () => {
  assert.throws(() => moveClip({ id: "m", clipId: " ", at: time(0n) }), /non-blank/);
  assert.throws(() => trimClip({ id: "t", clipId: "c", sourceRange: range(0n, 0n) }), /duration must be positive/);
  assert.throws(() => trimClip({
    id: "t", clipId: "c",
    sourceRange: { start: time(9_223_372_036_854_775_807n), duration: time(1n) },
  }), /end overflows/);
  assert.throws(() => setClipProperty({ id: "p", clipId: "c", property: " gain ", value: 1n }), /leading or trailing/);
  const operation = moveClip({ id: "same", clipId: "c", at: time(0n) });
  assert.throws(() => buildTransaction({ id: "tx", idempotencyKey: "key", expectedRevision: 0n, operations: [operation, operation] }), /duplicate operation id/);
});
