# Cutly 0.1 RC known limitations

This release candidate is a deliberately restricted native hard-cut SDK. It
does not claim to be a general nonlinear editor yet.

## Supported render profile

- One continuous video track and one continuous audio track.
- Hard cuts only; no gaps, overlaps, retiming, clip effects, or transitions.
- MP4 output using H.264 video and AAC audio through a host-owned FFmpeg
  runtime.
- `yuv420p`, BT.709, mono or stereo output.
- Local, absolute, host-authorized file inputs and output.
- Linux and macOS are the initial render runtime targets. FreeBSD is a build
  target. Windows builds fail closed for rendering until equivalent secure
  handle inheritance and filesystem confinement are implemented.

Unsupported plans fail during plan, prepare, or preflight. Cutly never
silently substitutes a broader or browser-based renderer.

## Scheduler and cache boundary

The RC provides durable whole-job queue replay, final-artifact CAS storage,
checkpoints, and recovery after publication. It does not yet provide
cross-job stage reuse, chunk-level resume, distributed workers, or cloud
scheduling. A new job or publication URI can therefore invoke FFmpeg again.

## Player and UI boundary

The headless Player and timeline/UI primitives are experimental. Their exact
audio-master clock, seeking, buffering, looping, and stale-frame rejection are
stable enough for integration, but the RC does not ship a DOM component,
audio-device driver, proxy pipeline, or background-playback policy.

## Out-of-scope future work

General VFR and color pipelines, rotation/SAR/HDR, complete text shaping and
rasterization, browser/WebLayer rendering, GPU effects, plugin ecosystems,
and multi-node/cloud execution remain future profiles. They are not hidden
behind the current capability set.
