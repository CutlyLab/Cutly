# Cutly native README demo

This six-second demo is rendered by Cutly's Document v2 mixed-media pipeline.
It is not a browser capture or a precomposed video input.

The document composes:

- three independently generated PNG image assets;
- two native linear-light dissolve transitions;
- a vector card layer;
- three pinned-font text assets with keyframed opacity;
- a generated 48 kHz stereo WAV routed through a group bus and master limiter;
- native RGBA16 and PCM chunks assembled as H.264/AAC MP4.

The assembler re-decodes the published media and writes an independently
verifiable Evidence v2 sidecar before publication.

## Files

- [`cutly-native-demo.mp4`](cutly-native-demo.mp4) — 640×360, 24 fps, six seconds.
- [`cutly-native-demo.mp4.evidence.json`](cutly-native-demo.mp4.evidence.json) — Evidence v2.
- [`cutly-native-demo.metadata.json`](cutly-native-demo.metadata.json) — frozen Document, RenderPlan, ExecutionManifest, mixed bridge, and artifact identity.
- [`cover.png`](cover.png) — a three-scene README preview.

## Verified output

```text
MP4 SHA-256:      5d418ebde16b0d583dafbdb4f8eda1b07d62829b5a2e369229cd8dc13050a14d
Evidence SHA-256: f29d91d426bb3f78d0194a128dad3e9ba43ac2b4e3f9f933cef2c69a3743f463
Duration:         6.000 seconds
Video:            H.264, 640×360, yuv420p, 24 fps
Audio:            AAC, 48 kHz, stereo
```
