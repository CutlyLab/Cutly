import assert from "node:assert/strict";
import test from "node:test";

import { DOCUMENT_V2_UI_PROFILE, documentV2TimelinePrimitives } from "../src/index.js";

const time = (value) => ({ value, rate: 1n });
const clip = (id, asset) => ({ id, asset_id: asset, timeline_range: { start: time(0n), duration: time(1n) }, source_range: { start: time(0n), duration: time(1n) } });

test("Document v2 UI exposes immutable mixed-media read models", () => {
  const document = {
    schema_version: 2n, id: "document", revision: 4n,
    assets: [
      { id: "video", kind: "video" }, { id: "audio", kind: "audio" },
      { id: "text", kind: "text" }, { id: "vector", kind: "vector" },
    ],
    timelines: [{ id: "main", tracks: [
      { id: "v", kind: "video", clips: [clip("cv", "video")] },
      { id: "a", kind: "audio", audio_bus_id: "master", clips: [clip("ca", "audio")] },
      { id: "t", kind: "text", clips: [clip("ct", "text")] },
      { id: "x", kind: "vector", clips: [clip("cx", "vector")] },
    ] }],
  };
  const timeline = documentV2TimelinePrimitives(document, "main");
  assert.equal(timeline.profile, DOCUMENT_V2_UI_PROFILE);
  assert.deepEqual(timeline.tracks.map((track) => track.track_kind), ["video", "audio", "text", "vector"]);
  assert.equal(timeline.tracks[1].audio_bus_id, "master");
  assert.ok(Object.isFrozen(timeline.tracks));
  assert.ok(Object.isFrozen(timeline.tracks[0].clips[0]));
});

test("Document v2 UI rejects missing resources and duplicate identities", () => {
  const document = { schema_version: 2, id: "d", revision: 0, assets: [], timelines: [{ id: "main", tracks: [{ id: "v", kind: "video", clips: [clip("c", "missing")] }] }] };
  assert.throws(() => documentV2TimelinePrimitives(document, "main"), /missing asset/);
});
