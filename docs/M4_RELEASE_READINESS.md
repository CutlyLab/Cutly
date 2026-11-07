# M4 release readiness

This document records the release boundary for Cutly `0.1.0-rc.1`. It is an
acceptance checklist for the restricted native hard-cut SDK, not a claim that
Cutly is already a general nonlinear editor.

## Broad Document v2 M4 tracking

The active broad M4 implementation is tracked in
[`DOCUMENT_V2_NATIVE_SDK_ROADMAP.md`](DOCUMENT_V2_NATIVE_SDK_ROADMAP.md), not by the
restricted RC matrix below. As of 2026-11-07, S1 is accepted, S2 implementation has
passed local gates but lacks final independent audit closure, and the independent
S7a mutation/project prerequisite is complete and independently reviewed at `ea396d3`
(see `DOCUMENT_V2_S7A_EVIDENCE.md`). S3–S8 broad runtime/product acceptance
is not complete. Manyfy integration remains explicitly deferred by the owner.

Release automation in source is not execution evidence. The signed-runtime
verifier and build-owned trust policy already exist, but tagged workflow builds
remain development builds until approved media bundles and trust pins are supplied.
The checked-in dependency notices/SBOM cover source dependencies, not an approved
FFmpeg binary-distribution closure. Production signing keys, license approval,
actual multi-platform runs and one-hour/nightly soak evidence cannot be fabricated
from unit-test names or cross-compilation. Push, tag, publication and daemon-default
changes still require explicit owner authorization.

The independent restricted-RC evidence runner and its measured synthetic streaming/RSS baseline
are tracked in [`RELEASE_EVIDENCE_RUNNER.md`](RELEASE_EVIDENCE_RUNNER.md). They are release-test
infrastructure, not broad Document v2 runtime acceptance or a signed media-bundle claim.

## Completion matrix

| M4 surface | Source status | Implemented acceptance boundary | Evidence |
| --- | --- | --- | --- |
| Native rendering | Implemented for the RC profile | Host-owned FFmpeg planning, preparation, fresh probing, preflight, exact-frame PNG preview, H.264/AAC MP4 publication, and evidence sidecar binding | `TestPublicGoSDKHTTPRenderEndToEnd`; `TestRenderApplicationEndToEndAndOfflinePublicationRecovery` |
| Scheduler and cache | Implemented within the documented whole-job boundary | Durable queue journal/replay, worker recovery, whole-job checkpoints, final-artifact CAS, and offline publication recovery | scheduler replay/concurrency tests, checkpoint quarantine tests, CAS publication tests, and the daemon end-to-end tests |
| Public Go and TypeScript SDKs | Implemented for the complete RC render path | Local project creation/open, durable plan, host prepare, preflight, render-job construction, submit/status, strict caller-bound receipts, artifact/evidence access, and runnable Go/TypeScript examples | Go SDK race tests, TypeScript runtime/hostile-receipt tests, packed-package smoke, and the public-Go-over-HTTP end-to-end test |
| CLI and Agent | Implemented for the RC workflow | `cutly init`, `cutly` version/help, `cutlyd`, bounded canonical Agent envelopes, project/edit/render routes, capability discovery, and stable diagnostics | command tests, protocol/conformance fixtures, HTTP transport tests, and daemon end-to-end tests |
| Player and UI primitives | Implemented as an experimental integration surface | Exact audio-master `HeadlessPlayer`, typed timeline/edit operations, immutable UI read models, and `AgentPreviewAdapter` exact-frame presentation with seek/concurrency stale-frame rejection | player, operations, preview-adapter, abort, and invalid-receipt tests in the TypeScript suite |
| Release engineering | Prepared in source; not externally published | Standalone target-root CI/release workflows, one Go/npm RC version, Apache-2.0 license alignment, portable command archives, checksums, npm provenance, and GitHub build attestations | `.github/workflows/ci.yml`, `.github/workflows/release.yml`, package metadata tests, dry-run package inspection, and the checklist in `RELEASING.md` |

The scheduler row deliberately means whole-job durability. It does not mean
cross-job stage-cache reuse, chunk-level resume, or distributed scheduling.
Likewise, the Player/UI row does not promise a DOM editor, audio-device driver,
media proxy pipeline, or background-playback policy.

## Release-gate evidence

Run these commands from the standalone Cutly repository root on the exact
commit proposed for the tag:

```sh
go mod tidy -diff
go vet ./...
go test -race -count=1 ./...
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run pack:check
```

The full race suite includes the production HTTP mux and public Go SDK path:

```text
durable project -> plan -> prepare -> fresh-probe preflight
                -> render job -> durable scheduler -> FFmpeg
                -> MP4 + evidence
```

That test needs FFmpeg/FFprobe and permission to bind a local loopback server.
The target CI additionally checks Go formatting, protocol schemas and fixtures,
Node 20 compatibility, installation/import of the packed npm artifact, portable
Go builds, and a real macOS FFmpeg render. The release workflow rebuilds six
command archives and will not publish npm until verification and every archive
build have succeeded.

For a local portable-build check equivalent to the CI boundary, run the
following for `linux/386`, `windows/amd64`, `freebsd/amd64`, and
`darwin/amd64`:

```sh
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> \
  go test -exec=true ./agent/... ./cmd/... ./sdk/go/...
```

## Source readiness versus public release

The SDK source can be release-candidate ready while publication is still
pending. At the time of this checklist, the following are external release
operations and are intentionally **not** recorded as complete:

- synchronize the reviewed `Manyfy/cutly` tree into the root of
  `github.com/CutlyLab/Cutly` through a normal feature branch and PR;
- verify the target CI on the resulting target-repository commit;
- provision or confirm ownership of the `@cutly` npm scope and configure
  `NPM_TOKEN` or trusted publishing;
- create and push `v0.1.0-rc.1` from the audited target `main` commit;
- verify the published Go module, npm provenance, archive checksums, GitHub
  attestations, and a clean-consumer install.

No incubation-repository commit, local test pass, or dry-run package is a
substitute for those external operations. Do not claim the RC is published
until the target tag, npm package, GitHub release, and consumer verification
all exist.

## Non-blocking follow-up profiles

These items are intentionally outside the restricted M4 RC and do not block
its source acceptance:

- an action-specific request/receipt schema catalog and richer capability
  discovery, published as a compatible future Agent contract rather than by
  mutating the frozen v1 envelope;
- cross-job stage caching, chunk resume, distributed workers, and cloud
  scheduling;
- general NLE behavior such as gaps/overlaps, retiming, transitions, effects,
  broad VFR/color/HDR handling, complete text shaping/rasterization, browser
  layers, and GPU/plugin execution;
- production DOM/audio-device/proxy integrations around the experimental
  Player/UI primitives.

The exact supported and unsupported media semantics remain normative in
[`../KNOWN_LIMITATIONS.md`](../KNOWN_LIMITATIONS.md).
