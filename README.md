# Cutly SDK

Cutly `0.1.0-rc.1` is an independently buildable, no-Chrome video-editing SDK
with a local Agent daemon, public Go and TypeScript clients, CLI, deterministic
render contracts, durable job recovery, and headless Player/timeline building
blocks. The RC intentionally supports a restricted native FFmpeg hard-cut
profile; it does not present itself as a general nonlinear editor.

The complete public path is implemented and acceptance-tested:

```text
Document v1 → durable local project → RenderPlan → host-owned prepare
            → fresh-probe preflight → durable RenderJob → MP4 + evidence
```

- Go: `github.com/CutlyLab/Cutly/sdk/go/cutly`
- TypeScript: `@cutly/sdk`
- Commands: `cutly` and `cutlyd`
- Runnable flows: [`examples/`](examples/)
- Exact supported boundary: [`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md)
- Security reporting: [`SECURITY.md`](SECURITY.md)

The source is currently developed in the `Manyfy/cutly` incubation directory,
but that directory is the complete root tree of the public
`github.com/CutlyLab/Cutly` repository: `go.mod`, `LICENSE`, `sdk/`, `cmd/`, and
the target repository's `.github/` all live at that root after a reviewed
sync. A public release is identified by one audited target-repository commit
and the matching `v0.1.0-rc.1` tag; see [`RELEASING.md`](RELEASING.md).

## Current scope

- exact rational time and half-open ranges;
- versioned Document v1 plus the strict Document v2 schema gate and canonical hashing;
- typed, atomic transactions with inverse and idempotency;
- stable diagnostics;
- strict wire decoding and duplicate-key-safe canonical JSON for fingerprinted protocols;
- deterministic v1-to-v2 migration, typed properties/keyframes/transitions, pinned-font text,
  deterministic vectors, and a capability-described base AudioGraph;
- a profile-specific vector viewport contract defining an intrinsic pixel surface, exact xMidYMid
  meet mapping, and transparent letterbox/clip for a fill-only move/line/close native raster slice;
- an exact-rational 8×8 sample-center vector fill reference raster with nonzero/evenodd rules,
  nearest-even coverage, path-order source-over, canonical receipts, resource limits and fuzz/oracle
  conformance, plus a document/clip/time/profile-bound immutable expression-layer provider;
- an additive pinned-font Text v2 contract with an explicit intrinsic viewport, line height and
  script, frozen Unicode 17 validation tables, font-content-bound semantic/layout-input hashes,
  strict schema fixtures and capability discovery; shaping, font parsing and rasterization remain
  separately versioned implementation stages rather than implicit host behavior;
- a deterministic shaped-input text layout primitive with immutable read views, font-space Q26.6
  nearest-even scaling, direct baselines, logical LTR/RTL alignment, an explicit HarfBuzz-raw RTL
  traversal-normalization boundary, strict outline closure, portable logical resource charges,
  cancellation, and independently bound glyph/outline/placement/layout hashes; upstream Document,
  font, and shape-provider identities remain unauthenticated claims until the later live provider;
- a T1b0 host-owned text-shape dependency candidate lock with exact Go module origins/sums,
  selected-package and MVS-only reachability, SPDX 2.3 SBOM, byte-locked notices/licenses/patent
  grants, compiled fingerprint and tamper gates; the candidate is deliberately absent from
  `go.mod` and does not advertise a Text capability;
- a strict T1b1a TrueType/SFNT core gate for one canonical static-glyf profile: packed table
  directory/checksum validation, exact `head`/`maxp`/`hhea`/`hmtx`/`loca` cross-checks and strict
  Unicode cmap format 4/12 normalization with portable resource receipts; `glyf` ranges and
  GDEF/GPOS/GSUB remain explicitly opaque and are not authorized for outlines or shaping;
- a T1b1b-1 unhinted static-glyf structure gate that scans every simple/compound body, validates
  flags/deltas/instructions/point arguments, proves an acyclic bounded component graph and matches
  seven `maxp` outline maxima under a canonical graph hash; geometry, implied points, transforms,
  Q26.6 commands and Text capability remain deliberately unauthorized;
- a T1b1b-2a exact real-control geometry gate that rebinds the immutable phase-one graph, expands
  compound transforms with normalized dyadic arithmetic and no intermediate rounding, validates
  scaled/unscaled/default offsets, point attachment, glyph/head/hhea bounds, head bit 1 and recursive
  last-wins `USE_MY_METRICS`, and emits a canonical streaming geometry receipt under incremental
  execution budgets; implied points, Q26.6 commands, shaping and Text capability remain deferred;
- a T1b1b-2b immutable all-glyph `OutlineSet` gate that live-rebinds both the outline graph and exact
  geometry, synthesizes TrueType implied on-curve points in the exact dyadic domain, emits one
  canonical Move/Line/Quad/Close stream per glyph, and performs exactly one symmetric nearest-even
  font-space Q26.6 conversion under shared incremental work and portable allocation budgets;
  GDEF/GSUB/GPOS, shaping, rasterization and Text capability remain deliberately unauthorized;
- a private T1b1c-a static OpenType-layout foundation that live-rebinds the complete
  `OutlineSet -> GeometryFont -> OutlineFont -> Font` lineage, content-binds GDEF/GSUB/GPOS in fixed
  order, and fully validates GDEF 1.0/1.2 plus non-variable 1.3, Coverage/ClassDef, attachment
  points, ligature carets, MarkGlyphSets and Device tables through a deterministic typed-offset
  graph. Compact owned models, exact pre-allocation work/working/retained/canonical gates,
  cancellation and canonical receipts are frozen internally; GSUB/GPOS remain opaque and no public
  `LayoutGraph`, shaping API or Text capability is advertised yet;
- an additive scoped viewport-vector dissolve capability and transition-handle v2 provider contract
  that bind the exact transition/role/clip/asset/time/profile scope without widening the visible-only
  expression-provider v1 contract or charging zero-width hard cuts for an inactive capability;
- a capability-gated, immutable Document v2 evaluator with exact clip/source mapping, precompiled
  hold/linear/Q0.32 cubic keyframes, transition handle sampling, and indexed active visual layers;
- a deterministic CPU working-surface/blend kernel for linear-light BT.709 premultiplied RGBA16,
  exact opacity/dissolve, Porter-Duff source-over, stable layer order, and canonical pixel hashes;
- strict PNG ingest and a document/content-bound native image provider that normalize RGBA8 sRGB
  into immutable native-size linear BT.709 premultiplied RGBA16 surfaces;
- a native raster compositor that connects exact source requests to crop, affine transform,
  opacity, dissolve, stable multi-track composition, transparent gaps, cancellation, and frame parity;
- a visual-only mixed reference compositor that dispatches native image/video surfaces and intrinsic
  viewport-vector expression surfaces through that same evaluator/raster/kernel path, with strict
  clip/transition and native/expression receipt unions plus live result self-consistency validation;
- deterministic output color conversion and a strict single-IDAT stored-DEFLATE PNG encoder,
  assembled by `render/referenceimage` into an immutable, concurrent exact-frame vertical slice;
- a mixed deterministic PNG export assembly in `render/referencevisual` that owns strict native-PNG
  and vector-expression providers, binds their compile-time manifests to ordered source receipts,
  records the Visual→RGBA8→PNG hash chain, provides trusted live re-render validation, and keeps
  serialized validation explicitly structural rather than claiming offline pixel provenance;
- an exact pre-quantization AudioGraph runtime with immutable probe/channel-order bindings,
  channel matrices, gain/pan automation, stable track/group/master routing, an explicit master
  sample-peak limiter, SampleClock blocks, cancellation, and rational resource limits;
- a strict PCM16 WAV decoder/provider, exact rational linear-varispeed source evaluation,
  ties-to-even PCM16LE output, and deterministic RIFF/WAV encoding for a full debug-audio slice;
- RenderPlan v3, composition clocks, semantic capability requirements, and frame/source-time evaluation;
- exact sample evaluation on the composition sample clock;
- immutable ProbeManifest contracts and a conservative real FFprobe adapter with a bounded,
  exact presentation-PTS frame-index scan;
- frozen ExecutionManifest, RenderJob, progress, artifact, and decoded-domain EvidenceManifest
  contracts, including exact Job/layout/unit-byte binding and an authorized sidecar URI;
- a restricted no-Chrome FFmpeg hard-cut backend with verified input materialization, no-replace
  MP4/evidence publication, and exact single-frame PNG publication through the same evaluator and
  video filter compiler;
- legacy ChatCut EDL adapter;
- protocol schemas and conformance fixtures.

RenderPlan v3 is now the only executable plan format. It uses composition-wide frame/sample clocks,
backend-independent capability requirements, logical resource references, and strict fingerprinted
decoding. Legacy v2 fixtures remain only to prove fail-closed rejection.

The release-candidate renderer is deliberately narrow rather than a general
native compositor. It accepts one continuous video track and one continuous
audio track with hard cuts; gaps, overlaps, clip properties, retiming, and
media facts whose semantics are not frozen are rejected before execution.
Exact PNG preview uses the same frozen plan/environment and is connected to
the TypeScript `HeadlessPlayer` through `AgentPreviewAdapter`. The local daemon
owns a persistent queue, trusted probe registry, prepared environments, final
artifact CAS, whole-job checkpoints, worker replay, and publication recovery.
These are whole-job durability guarantees, not claims of cross-job stage-cache
reuse or distributed scheduling.

Document v2 and `core/evaluator` are the base semantic gate for M2. The native static-image path now
executes strict PNG decode, color normalization, native-size source binding, crop/affine raster,
opacity/dissolve/multi-track composition, output color conversion, and deterministic PNG encoding.
The strict WAV path executes decode, exact linear varispeed, AudioGraph, PCM16LE quantization, and
deterministic WAV encoding. These reference/debug slices still do not constitute the complete M2
renderer: native image/vector mixing now shares one deterministic visual surface compositor, while
the mixed PNG artifact/manifest wrapper is complete and byte-identical to `referenceimage v1` for
image-only inputs. Manyfy exposes that wrapper through the additive, restricted
`cutly_reference_visual` preflight/render tool without changing the existing image-only tool.
General video decode, VFR/PTS selection, rotation/SAR/YUV/HDR normalization, Text v2 complete
GSUB/GPOS validation, pinned shaping and raster,
stroke/cubic support, AAC delay/priming, generalized encode/mux profiles, and
distributed or chunked scheduling remain separate stages. The published
`cutly.audio.graph.v1` contract is
deliberately a deterministic base graph (clip/bus gain, linear-balance mono/stereo pan and
single-parent routing). Document v2 also exposes two independent, opt-in contracts without changing
that base profile: `cutly.audio.channel-matrix.stereo.v1` for an exact probe-bound clip matrix and
`cutly.audio.limiter.sample-peak-instant.v1` for a master-only stereo-linked sample-peak limiter.
Their typed fields, capabilities and evaluator descriptors are frozen and consumed by the exact
AudioGraph runtime; neither operation is a hidden backend default. The WAV source and PCM16LE
packages are separate versioned stages rather than hidden behavior inside the graph.

The v1 migration is deterministic and fail-closed, not an assertion that every historically valid
v1 document is representable in v2. Inputs that conflict with stronger v2 invariants—such as an
unpinned URI-only font or a track/asset-kind mismatch—return stable diagnostics and no partial v2
document. Unknown legacy properties that are otherwise representable remain losslessly wrapped
behind the dedicated `legacy.property.v1` consumer capability.

M1 acceptance now includes ten real same-profile renders with identical decoded RGBA/PCM roots,
exact-frame/export semantic parity, bounded cancellation, a 16 MiB versus 256 MiB independent
process MaxRSS gate, and fail-closed process/disk fault injection. PNG and lossy H.264 output share
the evaluator and source-time mapping; they are compared with a declared visual tolerance rather
than falsely claiming decoded-byte equality. An opt-in macOS acceptance test also fills an isolated
16 MiB HFS+ disk image to real ENOSPC and verifies that no media, evidence, or staging name remains.

Manyfy host integration fails closed unless the packaged sidecar is build-stamped with three
canonical SHA-256 identities:

- `CUTLY_SDK_BUILD_DIGEST`;
- `CUTLY_FFMPEG_RUNTIME_DEPENDENCY_FINGERPRINT`;
- `CUTLY_FFPROBE_RUNTIME_DEPENDENCY_FINGERPRINT`.

The packaged resolver ignores runtime environment overrides; an unstamped development build reads
those variables only after the developer explicitly opts in with
`CUTLY_ALLOW_DEV_RUNTIME_IDENTITY=1`. These values are host-owned trust inputs, not Agent
parameters. The generic tool also re-probes every resource and requires the live ProbeManifest
fingerprint to match the frozen execution manifest; a self-consistent but Agent-authored
media-facts claim is not trusted. Production rollout still requires a pinned/signed FFmpeg and
FFprobe dependency-closure resolver; a build-time declaration alone is not presented as that
missing supply-chain proof.

The secure process profile currently requires Unix inherited file descriptors. The packages remain
cross-compilable, but construction fails closed on Windows until equivalent handle inheritance and
filesystem confinement are implemented; it never silently falls back to reopening mutable paths.

## Boundary

Production code and tests in this module must not import Manyfy, ChatCut, UI, tool, or host-runtime
packages. Manyfy consumes this module through the local replacement in the parent `go.mod`; the
dependency direction never reverses.

```text
Manyfy adapters/tools
        |
        v
github.com/CutlyLab/Cutly/core/videoedit + core/documentv2
        |
        +--> core/evaluator
        +--> font/sfntglyf
        +--> media/probe + media/ffprobe + media/pngdecode + media/wavdecode + media/color
        +--> render/api + render/ffmpegbackend
        +--> render/audiograph + render/wavsource + render/pcm16le + render/wavencode
        +--> render/vectorraster + render/expressionlayer
        +--> render/visualcompositor --> render/expressionlayer + native source provider
        |                           --> render/cpuraster --> render/cpucompositor
        `--> render/referenceimage --> render/nativepngsource
                                  --> render/rastercompositor --> render/cpuraster
                                                              --> render/cpucompositor
                                  --> render/pngencode
```

## Verify

From this directory:

```bash
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
npm --prefix sdk/typescript test
npm --prefix sdk/typescript pack --dry-run
```

The target repository workflow `.github/workflows/ci.yml` runs the same
independent-module, boundary, schema, fixture, vet, race, Node 20,
packed-package, portable-build, and real Linux/macOS FFmpeg checks. Tagged
releases additionally produce checksums and provenance through
`.github/workflows/release.yml`.
