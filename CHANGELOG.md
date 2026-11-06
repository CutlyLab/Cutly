# Changelog

Cutly follows semantic versioning. Until `1.0.0`, an RC may still make
additive API changes, while frozen wire profiles remain backward compatible
within their declared version.

## 0.1.0-rc.1

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
