import {
  createMotionCompositionV2,
  encodeCanonicalValue,
} from "../sdk/typescript/src/index.js";

// This is the source an agent (or a developer) authors. Cutly compiles it to
// Document v2; callers do not hand-write the renderer's internal document.
const film = createMotionCompositionV2({
  id: "cutly-code-first-demo",
  name: "Cutly code-first launch card",
  width: 1920,
  height: 1080,
  fps: 30,
  durationFrames: 150,
});

film
  .fontAsset({
    id: "display-font",
    uri: process.env.CUTLY_FONT_URI ?? "file:///path/to/Inter-SemiBold.ttf",
    contentHash: process.env.CUTLY_FONT_SHA256 ?? `sha256:${"0".repeat(64)}`,
  })
  .rectangleAsset({
    id: "background",
    width: 1920,
    height: 1080,
    fill: { r: 1400, g: 1700, b: 2200, a: 65535 },
  })
  .rectangleAsset({
    id: "accent",
    width: 620,
    height: 12,
    fill: { r: 5000, g: 52000, b: 65535, a: 65535 },
  })
  .textAsset({
    id: "title",
    text: "VIDEO, WRITTEN AS CODE.",
    fontAssetId: "display-font",
    fontSize: 112,
    lineHeight: 124,
    width: 1500,
    height: 180,
    fill: { r: 65535, g: 65535, b: 65535, a: 65535 },
    language: "en",
  })
  .textAsset({
    id: "subtitle",
    text: "Agent-authored. Native-rendered. Evidence-bound.",
    fontAssetId: "display-font",
    fontSize: 42,
    lineHeight: 54,
    width: 1500,
    height: 90,
    fill: { r: 43000, g: 45500, b: 50000, a: 65535 },
    language: "en",
  });

film.scene("launch-card", { durationFrames: 150 }, (scene) => {
  scene.shape("background-layer", "background");

  scene.shape("accent-layer", "accent", { startFrame: 8, durationFrames: 142 })
    .position(210, 345)
    .animateScale([
      { frame: 0, value: ["0.02", 1], easing: "expo.out" },
      { frame: 22, value: [1, 1] },
    ]);

  scene.text("title-layer", "title", { startFrame: 12, durationFrames: 138 })
    .position(210, 410)
    .animatePosition([
      { frame: 0, value: [90, 410], easing: "power3.out" },
      { frame: 24, value: [210, 410] },
    ])
    .animateOpacity([
      { frame: 0, value: 0 },
      { frame: 14, value: 1 },
    ]);

  scene.text("subtitle-layer", "subtitle", { startFrame: 28, durationFrames: 122 })
    .position(214, 620)
    .animatePosition([
      { frame: 0, value: [214, 660], easing: "power3.out" },
      { frame: 18, value: [214, 620] },
    ])
    .animateOpacity([
      { frame: 0, value: 0 },
      { frame: 12, value: 1 },
    ]);
});

// Canonical bytes can be stored as a project document or sent to the Cutly
// daemon for planning, native rendering, and Evidence v2 publication.
process.stdout.write(encodeCanonicalValue(film.build()));
process.stdout.write("\n");
