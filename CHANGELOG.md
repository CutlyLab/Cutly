# Changelog

Cutly follows semantic versioning. Until `1.0.0`, an RC may still make
additive API changes, while frozen wire profiles remain backward compatible
within their declared version.

## 0.1.0-rc.1

- Added the standalone Document v2 native SDK path: semantic plans, execution
  manifests, prepared environments, mixed video/image/vector/pinned-text/audio
  providers, isolated streaming workers, durable chunk reuse, H.264/AAC
  assembly, and Evidence v2.
- Added strict Document v2 Agent/HTTP/CLI lifecycles for durable project edits
  and render plan, prepare, preflight, content-addressed exact-frame preview,
  submit, status, history, and cancel. Action-specific schemas now correlate
  every tool/action/profile with a closed payload shape.
- Added number-preserving Go and TypeScript Document v2 clients, typed
  transactions, headless Player/UI primitives, and bound frame/audio preview
  adapters without a legacy fallback.
- Added portable release archives containing `cutly`, `cutlyd`,
  `cutly-worker-v2`, and the supported supervisor, with worker/runtime identity
  stamps and fail-closed signed-runtime acceptance-kit gates.
- Added the deterministic Document, transaction, RenderPlan,
  ExecutionManifest, RenderJob, evidence, and Agent v1 contracts.
- Added a no-Chrome FFmpeg hard-cut renderer with exact-frame preview,
  durable queue replay, whole-job checkpoints, content-addressed artifacts,
  and offline publication recovery.
- Added host-owned render planning and preparation, fresh media probing, and
  strict project, runtime, backend, and output identity binding.
- Added public Go and TypeScript SDKs, the `cutly` CLI, `cutlyd`, headless
  player/timeline primitives, the exact-frame Agent preview adapter, local
  project initialization, and caller-bound Go/TypeScript receipt validators.
- Added standalone target-repository CI, Linux/macOS runtime gates, portable
  build checks, npm package smoke tests, Apache-2.0 package alignment,
  checksummed release archives, and build provenance.

See [KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md) before integrating the RC.
