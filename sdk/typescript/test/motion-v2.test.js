import assert from "node:assert/strict";
import test from "node:test";

import {
  MOTION_COMPOSITION_V2_PROFILE,
  MotionCompositionV2Error,
  createMotionCompositionV2,
  encodeCanonicalValue,
} from "../src/index.js";

const digest = (character) => `sha256:${character.repeat(64)}`;

test("agent-authored motion code compiles scenes and animation to Document v2", () => {
  const film = createMotionCompositionV2({ id: "cutly-launch", timelineId: "main", width: 1920, height: 1080, fps: 30, durationFrames: 90 });
  film
    .imageAsset({ id: "plate", uri: "cas://plate", contentHash: digest("a") })
    .fontAsset({ id: "font", uri: "cas://font", contentHash: digest("b") })
    .rectangleAsset({ id: "panel", width: 900, height: 420, fill: { r: 17, g: 19, b: 21, a: 65535 } })
    .textAsset({ id: "title", text: "VIDEO EDITING FOR AGENTS.", fontAssetId: "font", fontSize: 118, lineHeight: 126, width: 1200, height: 420, fill: { r: 65535, g: 65535, b: 65535 }, language: "en" });

  film.scene("hook", { startFrame: 0, durationFrames: 90 }, (scene) => {
    scene.image("plate-layer", "plate")
      .anchor("0.5", "0.5")
      .animateScale([{ frame: 0, value: [1, 1], easing: "power2.inOut" }, { frame: 90, value: ["1.04", "1.04"] }]);
    scene.shape("panel-layer", "panel", { startFrame: 6 }).position(80, 180);
    scene.text("title-layer", "title", { startFrame: 6 })
      .position(80, 180)
      .animatePosition([{ frame: 0, value: [-90, 180], easing: "expo.out" }, { frame: 18, value: [80, 180] }])
      .animateOpacity([{ frame: 0, value: 0 }, { frame: 12, value: 1 }]);
  });

  const document = film.build();
  assert.equal(document.metadata["cutly.motion"].profile, MOTION_COMPOSITION_V2_PROFILE);
  assert.equal(document.timelines[0].tracks.length, 3);
  assert.equal(document.timelines[0].tracks[2].clips[0].keyframe_lanes.length, 2);
  assert.deepEqual(document.timelines[0].tracks[2].clips[0].timeline_range.start, { value: 1n, rate: 5n });
  assert.ok(Object.isFrozen(document));
  assert.ok(Object.isFrozen(document.timelines[0].tracks[0].clips[0]));
  const canonical = new TextDecoder().decode(encodeCanonicalValue(document));
  assert.match(canonical, /"cutly.motion-composition.document-v2.v1"/);
  assert.match(canonical, /"cubic_bezier"/);
});

test("motion composition fails closed on missing assets and unsafe ranges", () => {
  const film = createMotionCompositionV2({ id: "bad", width: 1920, height: 1080, fps: 30, durationFrames: 60 });
  assert.throws(() => film.layer({ id: "late", trackKind: "vector", assetId: "missing", startFrame: 59, durationFrames: 2 }), MotionCompositionV2Error);
  film.layer({ id: "missing-layer", trackKind: "vector", assetId: "missing", durationFrames: 60 });
  assert.throws(() => film.build(), /missing asset/);
});

test("motion rationals reject binary floating-point ambiguity", () => {
  const film = createMotionCompositionV2({ id: "exact", width: 100, height: 100, fps: 30, durationFrames: 30 });
  film.rectangleAsset({ id: "rect", width: 10, height: 10, fill: {} });
  const layer = film.layer({ id: "rect-layer", trackKind: "vector", assetId: "rect", durationFrames: 30 });
  assert.throws(() => layer.scale(1.1), /exact integer/);
  layer.scale("1.1");
  assert.deepEqual(film.build().timelines[0].tracks[0].clips[0].property_set.transform.scale.x, { numerator: 11n, denominator: 10n });
});
