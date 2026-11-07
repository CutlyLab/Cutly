# Native video decode slice

This package is the first bounded general-video decode slice. It does not
encode or mux media.

## Lifecycle

1. Resolve a controlled, zscale-enabled FFmpeg runtime and its complete
   dependency fingerprint.
2. Construct an `Adapter` with `New`. Construction hashes the executable,
   captures bounded version/capability inventories, and rejects a build
   without the exact filters, pixel format, and muxer used by profile v1.
3. Obtain a validated `probe.ProbeManifest` and a complete decoded-frame scan.
   Freeze the scan with `NewFrameIndex`; never derive VFR timestamps from an
   average frame rate. A controlled scanner must also emit
   `VideoSemanticsEvidence` proving a complete progressive-only,
   static-color, rotation-only display-matrix scan. Its build/options identity
   and canonical evidence fingerprint are mandatory; omitted evidence fails
   closed.
4. Call `Adapter.Open` with the canonical index and semantic-evidence
   fingerprints. Open copies and
   hashes the local regular file into a bounded private snapshot, removes all
   pathnames, and retains only a read-only descriptor.
5. Call `Source.Frame` at an exact rational source time. Selection uses the
   half-open display-PTS interval, seeks no later than the preceding keyframe,
   decodes forward, and requires FFmpeg's selected frame to have the exact
   indexed PTS.
6. Close the source. Returned `cpucompositor.Surface` values own immutable
   linear-light BT.709 premultiplied RGBA16 pixels.

`render/nativevideo` freezes document clip scopes and adapts these sources to
`rastercompositor.NativeSourceProvider`. Its `Profiles` result must be placed
in the compositor's video source-profile configuration; the compositor's
generic defaults deliberately do not imply this decoder.

## Profile-v1 media semantics

Supported inputs are static-semantics, opaque SDR YUV: 8/10-bit planar
4:2:0/4:2:2/4:4:4; explicit BT.709 matrix and primaries;
BT.709 or IEC 61966-2-1 transfer; explicit limited/full range and
left/center/top-left chroma location; positive explicit SAR; and 0, ±90, or
±180 degree FFprobe display-matrix rotation. Positive rotation is interpreted
counterclockwise. SAR is converted to square pixels with nearest-even width
rounding before rotation. zscale runs with exact gamma (`agamma=0`), bilinear
geometry, no dithering, and linear BT.709 RGB output.

HDR, alpha, implicit/unknown color facts, arbitrary rotation, other pixel
formats, missing duration/complete keyframe evidence, frame-index gaps at the
requested time, URLs, symlink inputs, and changed bytes/runtime binaries fail
closed. There is no edge hold or CFR synthesis.

## Boundaries and remaining work

Input bytes, index bytes/count, dimensions, raw output, stderr, per-allocation
size, decoded pixels, thread count, and decode wall time are bounded.
`MaxFrameBytes` is a real two-buffer Go working-set budget: FFmpeg output is
decoded directly into one pixel scratch slice and Surface creation makes the
single immutable copy. There is no third raw-frame buffer. A Darwin/Linux/
FreeBSD process group is killed on cancellation, and a request canceled while
waiting for the descriptor gate returns without waiting for the running
decode. FFmpeg receives an inherited fd with protocol allowlisting; caller
paths never enter arguments and no shell runs. Input leaves are opened with
`O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC` and accepted only after descriptor `fstat`,
so a raced FIFO or symlink cannot block the host. Other operating systems fail
closed at adapter construction.

The current `media/ffprobe` manifest retains only a keyframe summary, so pinned
producers for both the complete `FrameRecord` artifact and
`VideoSemanticsEvidence` are still needed for an end-to-end ingestion flow.
Typed evidence closes the decoder boundary, but a caller-authored assertion is
not a substitute for such a scanner.

Aggregate child RSS still needs a worker/container/cgroup or platform sandbox
limit; FFmpeg's `max_alloc` is only a per-allocation boundary. The executable
must resolve to a symlink-free regular file with no write permission bits;
every component must be owned by root or the current effective user, and
group/world-writable non-sticky ancestors are rejected. Its inode and bytes are checked
before and after setup/decode. This trusts the executable owner: eliminating
the final hash-to-exec window and pinning the complete dynamic-library closure
requires a controlled immutable runtime image or fd-exec platform adapter.

Adapter identity includes GOOS, GOARCH, the fixed FFmpeg `-cpuflags 0`
argument and a mandatory CPU-feature fingerprint supplied by the controlled
runtime resolver. `-cpuflags 0` does **not** disable zscale/libzimg dispatch:
FFmpeg configures it as `ZIMG_CPU_AUTO_64B`. Exact pixels are therefore claimed
only for the same CPU-feature fingerprint, executable and complete dependency
fingerprint. The current API requires both fingerprints from a controlled
resolver; it does not discover or lock the dynamic-library closure or collect
CPU features itself. A future controlled libzimg backend may instead pin
`ZIMG_CPU_NONE`.

`TestRealFFmpegDescriptorDecode` is an opt-in real-runtime integration test:
set `CUTLY_TEST_FFMPEG` to a pinned zscale-enabled FFmpeg. Default tests use a
fake executable to deterministically verify descriptor binding, exact
arguments, cancellation, output limits, and injection resistance.
