# Render performance baseline (reference visual export path)

First instrumented baseline for the Document v2 reference-visual export path, taken before any
production fast-path work. All numbers come from the opt-in harness
`render/referencevisual/perf_baseline_test.go` on one Apple M4 host (Go 1.27.3, single-threaded
render, homebrew FFmpeg not involved — this path is CPU-only).

Reproduce:

```sh
CUTLY_PERF_BASELINE=1 CUTLY_PERF_FRAMES=150 \
CUTLY_PERF_REPORT=/tmp/baseline.json \
go test ./render/referencevisual -run TestPerfBaselineReport -v -timeout 30m
```

## Workload

1920x1080 target, 150 frames at 30 fps. Two strict-profile PNG image assets (full-frame backdrop
plus half-size overlay entering at frame 30) and three vector expression layers with 48-segment
paths at full-viewport raster (entering at frames 0, 15 and 30). This mirrors the composition
shape the host `cutly_reference_visual` export produces: image plus vector layers, no temporal
video, no audio.

## Results (per frame, warm compiled program)

| Segment | P50 | P95 | Max | Notes |
| --- | --- | --- | --- | --- |
| full `RenderAt` | 1544 ms | 1610 ms | 1794 ms | complete reference export |
| `RenderSurfaceAt` | 1480 ms | 1556 ms | 1745 ms | drops color+PNG stages |
| raw visual composite | 1404 ms | 1468 ms | 1735 ms | evaluation + raster + composite only |
| no-session host pattern | 813 ms | 1024 ms | 1121 ms | decode + full compile + render, 2-3 active layers only |

Micro segments: one full-surface canonical SHA-256 12.5 ms, linear RGBA16 to sRGB RGBA8
conversion 39.8 ms, deterministic PNG encode 21.1 ms. Peak RSS 548 MB. Allocation churn
872 MB per rendered frame (855 MB of it inside the raw composite).

Cold segments (per compile): document decode + canonicalize ~1.7 ms, evaluator/compositor
compile 2.5 ms, native PNG source compile 80.2 ms, expression layer compile 78.8 ms, complete
referencevisual compile 162.3 ms.

## Findings

1. **Composite dominates.** 91% of per-frame time (1404 of 1544 ms P50) is the pure-Go
   exact-rational raster/composite chain, not hashing, not encoding, not process overhead.
   Per-frame cost scales near-linearly with active layers (~250-350 ms per full-viewport layer;
   early frames with 2-3 layers render roughly 2x faster than the 5-layer mid-section, which
   also explains the mean/P50 spread).
2. **Allocation churn is the second face of the same bottleneck.** ~50 fresh full-resolution
   RGBA16 intermediate surfaces are allocated per frame across rasterize/opacity/composite
   steps (872 MB/frame) with no pooling anywhere in the module.
3. **The no-session host pattern pays one full compile per frame** (~162 ms plus 847 MB of
   compile allocations) on top of rendering, because each host render call re-decodes the
   document, re-reads and re-decodes every PNG, and re-compiles the program. A session that
   compiles once removes this entirely.
4. **Redundant verification is real but secondary**: the reference wrapper re-hashes the final
   surface ~3 additional times per frame and re-invokes every source provider (replay) with
   another per-source hash, ~76 ms/frame at this size (~5%).
5. **Necessary output work is modest**: color conversion plus deterministic PNG encode is
   ~61 ms/frame.

## Implications for the production path

- A RenderSession (compile once, render many) removes the no-session compile cost and, with a
  production verification profile, the redundant re-hashing — but the decisive lever for this
  path is eliminating per-intermediate surface allocation in the raster/composite chain
  (buffer reuse that keeps every computed byte identical, validated by differential testing
  against this reference).
- Temporal video decode costs (per-frame FFmpeg process spawn, binary re-hash, double decode)
  documented for `media/decode` do not appear in this workload; they belong to the video-layer
  path and are addressed by later decode-session work.

The harness stays opt-in (`CUTLY_PERF_BASELINE=1`) so ordinary test runs do not pay for it.

## Re-measurement after the bounded-arithmetic channel kernels

The first production-path change to land from this baseline replaced the per-channel
`big.Int` arithmetic in `cpucompositor` weighted channels with bit-identical bounded uint64
fast paths (big.Int fallback preserved for weights beyond uint64), proven equal by exhaustive
differential kernel tests, the dissolve/golden vector suites and the render-session
byte-equality gate. Same host, same workload, same harness:

| Segment (P50) | Baseline | After | Speedup |
| --- | --- | --- | --- |
| full `RenderAt` | 1544 ms | 764 ms | 2.0x |
| `RenderSurfaceAt` | 1480 ms | 701 ms | 2.1x |
| raw visual composite | 1404 ms | 631 ms | 2.2x |
| no-session host pattern | 813 ms | 394 ms | 2.1x |
| allocation churn / frame | 872 MB | 266 MB | 3.3x |
| peak RSS | 548 MB | 484 MB | — |
| complete 150-frame baseline run | 739 s | 323 s | 2.3x |

At 640x480 the same change measured larger (365 ms -> 99.5 ms per reference frame, 3.7x;
614 MB -> 39.5 MB allocated per frame) because smaller surfaces make allocation overhead a
bigger share of the frame. At 1080p the remaining cost is now dominated by the fixed
full-surface memory traffic of the raster/composite chain itself: the residual ~266 MB/frame
churn is almost exactly the fresh intermediate-surface allocations.

## Re-measurement after scratch-surface reuse and host session adoption

The bounded Scratch pool (cpucompositor WriteTarget lend/commit, in-place kernels,
RasterizeInto, visualcompositor RenderAtWithScratch) removes the intermediate-surface churn
with byte-identical outputs; render-session frames drop to 5.2 MB allocated per 640x480 frame.
The Manyfy host gained an additive session-backed sequence path (one preflight, one compiled
session, per-frame publication through the same no-replace publisher), parity-gated against
the per-frame path.

End-to-end through the complete host service (stable reads, preflight, render, evidence
canonicalization, durable publication), same host, both paths producing byte-identical
artifacts:

| Workload | Per-frame path (today) | Sequence path | Speedup |
| --- | --- | --- | --- |
| 640x360, 40 frames | 78.5 ms/frame | 50.2 ms/frame | 1.56x |
| 1920x1080, 150 frames | 480.0 ms/frame (72.0 s) | 283.3 ms/frame (42.5 s) | 1.69x |

## Bounded parallel frame rendering

The render session gained a bounded parallel pass (RenderFrames/RenderFramesFunc): N workers
draw from a bounded dispatch queue, results flow through a reorder window, and the observer
sees strictly request-ordered frames — so publication order and artifacts are identical to the
serial path regardless of worker count. Deep validation stays deterministic by request index,
any frame or observer failure cancels the pass with no partial output, and the worker count is
host-chosen under a hard ceiling. The host sequence path exposes it as an optional Workers
field with ordered publication.

End-to-end through the complete host service at 1920x1080 x 150 frames, all paths producing
byte-identical artifacts:

| Path | Total | Per frame | Speedup |
| --- | --- | --- | --- |
| per-frame host path (historical) | 70.7 s | 471.3 ms | 1.0x |
| sequence, serial | 32.6 s | 217.1 ms | 2.17x |
| sequence, 4 workers | 9.9 s | 66.2 ms | 7.12x |

From the original instrumented baseline (1544 ms/frame for this workload class) the combined
production path is a ~23x per-frame reduction. Compute parallelism only pays above the
serialized publication floor: at 640x360 the serial sequence (26.5 ms/frame) beats 4 workers
(30.0 ms/frame) because per-frame compute is small against ordered durable publication.

Video-layer decode sessions (separate work, media/decode) measured 27.2 -> 14.0 ms per
decode+replay pair on a pinned FFmpeg by halving decoder spawns and caching executable
identity verification.



## Fixed-width raster sampler and hoisted weight kernels

Profiling the parallel frame path showed rasterPixels at 50% of CPU with the big.Int sampler
machinery inside, and the opacity kernels still resolving big-to-uint64 weight fit per pixel
channel. Two byte-identical changes landed:

- the channel kernels resolve each weight to uint64 parts once per kernel invocation and run
  the whole surface loop on fixed-width arithmetic (big.Int loops remain the fallback);
- cpuraster gains a fixed-width sampler admitted when both rational-plane denominators are
  <= 2^22 and every numerator excursion fits int64: identical tap selection, Euclidean floor
  semantics, bilinear weights and round-half-to-even quantization on int64/uint64 only, with
  the big.Int sampler kept verbatim as the fallback for larger geometries.

Gates: a randomized-geometry differential drives both samplers pixel-for-pixel (700+
transform/crop shapes admitted, every sampled pixel equal, zero divergence), a full-raster
byte-identity differential forces the big sampler over the same geometries, the golden and
oracle raster suites, and the complete render-session byte-equality suite. Benchmark effect:
640x480 20-frame pass 77.8 -> 35.2 ms/frame serial and 20.5 -> 9.2 ms/frame at 4 workers
(2.2x). The 1080p end-to-end fixture is unchanged (66.0 ms/frame at 4 workers): its layers
render through the identity-copy path, which these kernels intentionally do not touch; real
transform-heavy compositions receive the kernel speedup directly.


## Streaming encode (cutly.pipeline.stream-encode.v1, first cut)

render/streamencode runs one resident FFmpeg fed straight-alpha RGBA8 frames through a bounded
stdin pipe — no per-frame processes, no intermediate PNG files, pipe blocking as the natural
backpressure. The executable posture mirrors media/decode (regular, executable,
immutable-by-mode, symlink-free, owner-bounded, hashed at Start with optional host-pinned
fingerprint) and the encoder flags mirror the whole-job backend's determinism set (single
thread, bitexact, pinned creation time, stripped metadata). Session.EncodeFrames renders with
bounded parallelism and streams the ordered RGBA output directly into the encoder.

Gates: byte-identical MP4s across repeated encodes of the same sequence (and distinct output
for distinct sequences), frame-size accounting, zero-frame finish rejection, pinned-fingerprint
mismatch rejection, cancellation before start and between frames, abort semantics, and an
end-to-end session test producing a deterministic MP4 at 4 workers. Host-side video
publication protocol (MP4 evidence artifacts) remains the assembler-boundary follow-up.

## Metal evaluation (deferred with reasoning)

The plan positions GPU as a later accelerator behind a CPU fallback. The concrete assessment
after this round:

- The remaining per-frame core work after phases 3-6 is per-pixel kernels (opacity, composite,
  color conversion) plus identity raster copies; on this workload the streaming path already
  renders 150 1080p frames in ~10s at 4 workers, inside the window the plan reserved for GPU.
- The protocol's every equality gate is byte/hash equality from exact-rational CPU arithmetic.
  GPU float pipelines cannot be byte-identical to that math; admitting them requires a
  tolerance-based differential profile — a protocol change that deserves its own stage, not a
  performance patch.
- Go has no first-class Metal path: cgo bindings or a Swift helper subprocess would break the
  pure-Go darwin/linux build the SDK currently ships.

Recommendation: defer Metal. Revisit only when 4K/multi-layer workloads exceed the CPU path's
budget, and then as a dedicated stage that defines the tolerance profile first and keeps the
deterministic CPU path as the reference and fallback.


## Temporal video layers in the reference pipeline (S4 slice)

referencevisual gains the additive host-bound video mode: CompileV2Video accepts nativevideo
bindings (host-authorized decoder sources whose identity must pin each video asset's declared
content hash), advertises the video capability with the decoder's semantic profiles, and routes
the single Native provider slot by request asset kind — renders and live replay verification
use the same router. rendersession.OpenVideo carries the mode into the production session, so
parallel rendering, deep validation and streaming encode all apply to video layers unchanged.

End-to-end gate (pinned FFmpeg, 64x48, 24 frames, video layer + vector overlay, 2 workers):
the raw per-frame decoder Source and the bounded decode Session produce byte-identical
artifacts for every frame, deep validation passes through the full reference path, and the
decode Session measured 16.3 -> 12.1 ms/frame against the raw Source binding.

Both formerly open boundaries are now closed:

- The Manyfy publisher gained a publishedArtifact streaming-write abstraction (the PNG path
  byte-identical), and the host gained RenderSequenceVideo: one preflight, one compiled
  session, one resident encoder, one durable MP4 publication with a canonical
  VisualVideoEvidence sidecar. Gates: byte-deterministic MP4s across runs, no-replace
  republication preserving published bytes, artifact/evidence cross-binding, and full
  structural rejection of wrong extensions, mixed output children and missing encoders.
- The chunk assembler x stream-encode bridge landed as streamencode.FeedChunkVideo: it
  streams the reference chunk profile's canonical surface payloads (raw big-endian
  premultiplied-linear RGBA16 units) through the exact reference output-color stage into the
  resident encoder, gated by a differential test proving chunk-fed encodes are byte-identical
  to direct encodes. Start now rejects odd dimensions up front (yuv420p chroma requires even
  geometry — previously a late encoder exit).
