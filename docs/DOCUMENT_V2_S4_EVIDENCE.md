# Document v2 S4 provider evidence

## Status — 2026-11-08

**Partial implementation; S4 exit is not accepted.** This ledger records source slices,
not a claim that the mixed production provider, M4, or the public SDK is complete.

Source baseline: `5a186be790bf9f70326e9779fd76356dd7fe716c`, branch
`codex/m4-sdk-completion`, with uncommitted S4/S5 source additions and the pre-existing
release/archive changes preserved. Tests are working-tree evidence, not fixed-clean-revision
release acceptance. No commit, push, tag, package publication or daemon default change occurred.

## Implemented slices

### Scoped evaluator V2

`core/evaluator/domain_snapshot_v2.go` adds `CompileDomainSnapshotV2`,
`DomainSnapshotDescriptorV2`, and scoped `DomainProgram.EvaluateTransition`.
The compiler preserves the full original Document/hash/indexes and fixed offered capabilities.
Visual scope includes exact visible intersections and selected dissolve/hard-cut participants,
including handles outside visible ranges. Audio scope does not gain visual transition authority.
The V1 unconditional transition rejection and descriptor remain unchanged.

The implementation agent ran normal/race/vet for evaluator, documentv2 and conformance.
Tests cover exact transition samples, half-open windows, replay, capabilities, allocation admission,
ownership, cancellation/panic and concurrent readers. Independent review is tracked separately;
passing these tests alone is not independent stage acceptance.

### Scoped AudioGraph V2

`render/audiograph/domain_v2.go` adds `CompileDomainV2`, `PreflightDomainV2`,
`DomainDescriptorV2` and schema-version-2 native receipts. It reuses unchanged DSP semantics,
compiling selected bus roots and ancestors in authored order with original bus indexes.
Matrix, gain/pan automation and master limiter use existing DSP; audio transitions and nonunit
retime remain rejected. V1 profiles and canonical fixtures remain unchanged.

Normal/race/vet passed for audiograph, evaluator, wavsource and pcm16le. New canonical V2
profile/receipt fixtures and arbitrary-scope/concatenated sample parity tests are included.

### Complete video scanner

`media/ffprobe/video_scan.go` adds `Adapter.ScanVideo`, `VideoScanOptions` and
`VideoScanResult`, returning complete `videodecode.FrameIndex` and
`VideoSemanticsEvidence` plus scanner/index/semantics fingerprints.
It binds exact probe/resource/header identity, scans all frames and side data, verifies explicit
PTS/durations and pure quarter-turn matrices, and rejects unsupported/dynamic color semantics.
Existing `Probe` and its default fingerprints are not reinterpreted.

The implementation agent ran ffprobe/decode normal tests, ffprobe race and vet. Actual FFmpeg
integration scanned five H.264 frames, accepted a 90-degree display matrix and rejected a flip.
`video_scan_timing_integration_test.go` subsequently added actual H.264 encoded timing cases:
MP4 VFR timebase 1/16000 with PTS [0,640,1280,2560,3840] and durations
[640,640,1280,1280,640]; MPEG-TS preserves negative PTS at timebase 1/90000 with
PTS [-7200,-3600,0,7200,14400] and positive durations [3600,3600,3600,3600,3600].
Independent FFprobe inspection is compared exactly to ScanVideo without rewriting the manifest.
MP4 edit lists discarded negative presentation frames during exploration, so the negative case uses
MPEG-TS copyts, not a relaxed scanner. Actual normal (2.519s), race (34.349s) and vet passed.
Parent reran both actual timing cases (`b5hh7t7p9`, exit0): 0.29s test time, 0.845s package time,
zero skips. This proves scanning, not zscale decoding or production dependency-closure authorization.
Independent scanner/snapshot review subsequently completed; its limited result is recorded below.

### Bounded resource transfer helpers

`render/reconstructv2/video_snapshot.go` implements a finite 32 KiB-buffer transfer into an
owned private directory, exact byte/hash verification, read-only completion and idempotent cleanup.
`video_resource.go` binds a same-runtime re-probe to the authorized claim before complete scan and
descriptor-bound decoder opening. Additive MixedFactory/MixedBundle integration is implemented
and has its own limited review, described below. These helpers themselves grant no
environment, runtime, output-path or publication authority.

Normal and race snapshot tests passed for ownership, short/extra/hash mismatch, invalid read counts,
no progress, callback panic, close panic, cancellation, admission before callbacks and cleanup.
`go vet ./render/reconstructv2` passed.

### Mixed temporal visual V3 and compositor V2

`referencevisual.ConfigV3/ProgramV3/SourceManifestV3` and
`visualcompositor.CompileDomainV2/DomainDescriptorV2` are additive profiles, preserving frozen
S3/V1/V2 structs and fixture hashes. Full original document/hash/indexes and configuration-owned
fixed offers are retained. Native PNG/video dispatch, vector handles and pinned static text share
RGBA16 composition and frozen RGBA8/PNG output. Video sources are borrowed, never closed by this
wrapper; the host owns their lifetime, decode budgets and scheduling.

Tests cover four-kind mixed dispatch, heterogeneous video-to-PNG dissolve, exact retimed incoming
and outgoing handles, pixel oracles, half-open hard cuts/dissolves, ownership/concurrency and
failure-zero paths. Video sources in these tests are explicitly source-contract doubles, not actual
FFmpeg decoding. Per-render logical wrapper budgets do not prove global RSS or decoder-process caps.

Implementation normal/race/vet passed for referencevisual, visualcompositor, nativevideo and evaluator.
Parent normal checks for the same packages and `git diff --check` passed (`b3kwqgdg3`). A fresh
JS/Wasm compile-only core/protocol/render-api/resourceidentity subset passed (`bixjypiuh`).
Independent visual slice review found a verified P2: the wrapper called host Source.Identity
before nativevideo's MaxClipScopes admission. The three-video-clip fixture with MaxClipScopes=1
returned a zero failed program but had already made one Identity callback. The original implementer
completed a scope-admission helper before all font/source callbacks and added a no-callback
regression. Limits 1/2 reject the three-clip fixture with zero Identity/Frame calls; limit 3 succeeds.
Implementation focused normal/race/vet passed after the repair. Reviewer `a44621201ab0f5801`
then independently reverified the original P2 overlay and complete referencevisual/visualcompositor
normal/race/vet with no remaining verified P0/P1/P2. An additional 24 quarter-tick RGBA16 oracle
checks covered 2x source time, pre/post handles, window boundaries and hard cuts; source-request
admission at a two-handle frame made zero callbacks on failure. This limited visual audit excludes
actual FFmpeg decode, reconstruction and whole-stage acceptance.

### Additive production mixed reconstruction

`MixedFactory/NewMixed/MixedBundle` preserve the original full verified environment, add exact
host runtime mapping over concrete adapter options/identities, and admit aggregate disk/scan/frame
and logical memory budgets before resource callbacks. Disk-backed video snapshots use the complete
scan and decoder; actual static PNG/font/WAV resources use retained inspections. The bundle's RW
lifecycle lease makes Close wait for active renders and permanently rejects new renders afterwards.
Transition-only source closure is deduplicated by original ClipID before admission.

The actual PNG/font/WAV + dissolve/audio-bus/limiter fixture, arbitrary-range PCM/concurrent reads,
late panic/drift/cancel failure-zero, Close lifecycle and fresh subprocess static-mixed restart pass.
Final implementation normal/race/vet passed (2.716s / 26.368s / exit0). Reviewer
`a4f86231433249251` reported no verified P0/P1/P2 within this mixed reconstruction slice;
independent normal/race/vet passed (2.204s / 26.727s / exit0), including an overlay confirming that
transition-only clip-count overflow rejects before callbacks. This does not prove whole S4 exit.

Support is conservative: one authored timeline with all enabled clips represented in the plan;
strict native WAV audio, not compressed codecs. Budgets are per-reconstruction/render logical Go
envelopes, not global concurrent quotas or codec-process RSS limits. Explicit runtime mapping is
host policy, not standalone signed closure proof. The full real-video factory test compiled but
**SKIPPED** because `CUTLY_TEST_FFMPEG` is unset; actual-video subprocess restart remains unproved.
Frozen S3 New/Factory/Bundle/bridge and fixtures were not broadened.

## Actual environment failure and explicit skip

The first real decoder integration attempt failed with:

```
videodecode: FFmpeg executable is invalid: executable ancestor is group/world writable
```

The test then installed a private measured executable without weakening the path checks.
It reached the next real incompatibility:

```
videodecode: FFmpeg executable is invalid: required filter "zscale" is absent
```

The available PATH FFmpeg 9.0.2 is therefore not acceptable evidence for the frozen production
video decode/color profile. The new real decode test now follows the existing decoder integration
convention: `CUTLY_TEST_FFMPEG` must explicitly select a controlled zscale-enabled build.
Its latest execution **SKIPPED**, not passed. Test-only closure/CPU labels are explicitly not
production runtime-closure authorization evidence. No runtime installation or profile relaxation
was performed.

## Guard correction and remaining runtime boundary

The initial brace-expansion and runtime-variable command shapes were refused with an explicit
instruction to use statically verifiable separate commands. Treating that as a permanent ban on the
underlying read-only work was overly conservative. The original visual implementer and scanner
reviewer resumed using plain commands in this worktree, without changing permissions or bypassing
an actual user/policy denial. Visual/compositor and mixed reconstruction implementation is active;
its completion and audit are not implied by the earlier helper tests.

A separate attempt to delegate downloading/building private FFmpeg/libzimg was explicitly denied
pending user approval of the specific external source URLs/repositories. That action was not retried
through another tool or agent. The missing controlled zscale runtime remains a real acceptance gap.

## Repository and SDK checks

After the source slices and concurrent additive S5 request protocol landed:

- `go build ./...` passed.
- `go test -count=1 ./...` passed (task `b2fer75h9`). Opt-in tests may skip; the missing real decode
  integration above remains an explicit unmet gate.
- Linux amd64, Linux 386 and Windows amd64 full `go test -exec=true ./...` compiled successfully
  with `CGO_ENABLED=0` (task `bp907vwud`).
- JS/Wasm compiled the documented core/protocol/render-api/resourceidentity/conformance subset
  using `go test -exec=true`; this is compile-only, not target runtime evidence.
- `npm --prefix sdk/typescript test` passed all 49 tests, zero skips.
- `npm --prefix sdk/typescript run pack:check` passed the existing package dry run: 12 files,
  43.8 kB compressed / 201.8 kB unpacked (task `b88itgbkr`).
- An actual `cutly-sdk-0.1.0-rc.1.tgz` was built under `/tmp/cutly-sdk-build.f4JBsD`, installed into
  an isolated consumer with scripts disabled, and imported using Node ESM. `CutlyClient` was
  verified to be a function. Package shasum: `060c129d75cf50c5ba57c0e2d055dca4a961bff4`.
  No npm publication occurred. This verifies the existing SDK package, not deferred S7 public v2
  surfaces or a Node 20/22 runtime matrix (local Node is 24).
- A later Linux 386, Windows amd64, FreeBSD amd64 and Darwin amd64 compile-only matrix passed
  for `./agent/... ./cmd/... ./sdk/go/...` with `CGO_ENABLED=0` and `go test -exec=true`
  (`b70cmf1bi`, exit0). These are source-snapshot cross-compiles, not execution on those targets;
  subsequent S5/S6 edits require fresh gates.

### Independent evaluator/audio slice review

Reviewer `a44621201ab0f5801` completed the limited evaluator/audio source review and reported no
verified P0/P1/P2. Normal/race/vet passed for those two packages, both directly and with
`/tmp/cutly-s4-review-overlay.json`. Extra tests enumerated 136 requested ranges with quarter-time
samples against the whole-graph evaluator, plus shared-ancestor multi-route audio DSP/index parity.
This review explicitly excludes video, assembly/reconstruction and whole-stage acceptance.

### Full race failure — not hidden by command exit status

Task `baog0h9hw` ran the full race suite followed by vet/tidy/diff commands. Its final shell exit
was zero, but the **race suite failed** in
`TestScanVideoUsesFrozenManifestAndDescriptorAfterPathReplacement`:

- writer: `media/ffprobe/video_scan_test.go:343`, mutating the borrowed manifest;
- reader: `media/ffprobe/video_scan.go:79`, initial JSON deep copy in `ScanVideo`.

The test's attempted timing synchronization did not reliably prove that the initial copy had
completed under whole-repository contention. The scanner implementation agent was asked to repair
this test with an actual helper-start signal, preserve descriptor/ownership assertions, and rerun
race tests. The repair uses a channel closed when `exec.CommandContext` observes the test context,
which follows the initial owned manifest copy; this supplies a Go happens-before edge before the
caller mutation. Focused `go test -race ./media/ffprobe -count=10` passed (348.006s).

A fresh `go test -race -count=1 ./...` then passed, task `b35rxvsx3`, exit0, including ffprobe
35.919s and reconstruction 26.294s. A separate `go vet ./... && go mod tidy -diff && git diff --check`
passed, task `bja3utv0x`, exit0. These are the source snapshots actually built by those commands;
concurrent visual/mixed/S5 additions require new gates when stable. The earlier failure is retained
here rather than erased by the later success.

### Independent scanner/snapshot slice review

Reviewer `a4f86231433249251` reported no verified P0/P1/P2 in the scanner, checked-runner diff,
video snapshot/resource helpers and corresponding tests. Independent normal/vet passed; ffprobe
race passed (44.473s), reconstruction race passed on retry after concurrent visual symbols settled
(24.169s). Real `TestRealFFmpegCompleteVideoScan` executed and passed in 0.47s, not a skip.
This does not include the in-progress mixed Factory or prove real zscale decode. Later mixed source
changes temporarily blocked the separate decode-state command and are excluded from that result.

The host-admission getters added afterwards return normalized decoder/probe options and bound
identity by value. Their focused normal/race/vet passed (`b0z3pfmg1`); these values grant no extra
runtime authority and do not independently prove dependency closure. The same reviewer independently
checked these four getter/test files, found no verified P0/P1/P2, and passed focused race tests
(1.342s / 1.201s) and vet.

## Controlled runtime continuation — 2026-11-09

The earlier local-runtime gap was closed for technical acceptance with a controlled FFmpeg 9.0.2
build containing `zscale` and `libx264`; static x264/zimg inputs left no Homebrew dynamic-library
dependency in the bundled tools. A real 320x180 H.264/BT.709 plus 48 kHz stereo WAV prepared
fixture passed signed-bundle mixed smoke, 100 daemon-kill recoveries and a full one-hour soak.
Exact timings, decoded roots and RSS measurements are recorded in `DOCUMENT_V2_S6_EVIDENCE.md`.
The bundle used an explicitly local ephemeral acceptance authority, not an owner-approved release
key, and the run does not expand the tested fixture into the full VFR/HDR/rotation matrix.

## Remaining S4 exit requirements

- Repeat the now-passing controlled mixed path with owner-approved release keys/bundles and the
  exact release-candidate binaries.
- Complete mixed fixture with animation and transition, real VFR/negative PTS/color/rotation
  evidence, arbitrary-order/concurrent roots and preview/export parity.
- Extend the passing zscale-enabled path through the remaining failure/media matrix, then finish
  independent P0/P1/P2 review.
- Consolidate full stage gates. S5 backend/Evidence and S6 production runtime remain downstream;
  the local 100-run faults and full one-hour S6 soak do not substitute for formal release authority.
