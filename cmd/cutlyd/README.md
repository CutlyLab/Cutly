# cutlyd

`cutlyd` is Cutly's loopback Agent daemon. With render flags enabled it owns
one project's durable scheduler, trusted probe registry, prepared environments,
CAS, checkpoints, FFmpeg backend, and bounded worker pool.

## Render runtime

Rendering is opt-in. The state, output, and input roots must already exist, be
real directories, and be physically disjoint (including aliases and nested
paths). The state directory is permanently bound to the exact project ID,
resolved project root, and resolved output root. An unmarked directory that
already contains durable data is rejected; migration must be explicit.

```sh
cutlyd \
  --project-id demo \
  --project-dir /srv/cutly/projects/demo \
  --render-state-dir /srv/cutly/state/demo \
  --render-output-dir /srv/cutly/output/demo \
  --render-input-root /srv/cutly/media/demo \
  --ffmpeg /opt/cutly/bin/ffmpeg \
  --ffprobe /opt/cutly/bin/ffprobe \
  --sdk-build-digest sha256:<64-lowercase-hex> \
  --compiler-build sha256:<64-lowercase-hex> \
  --evaluator-build sha256:<64-lowercase-hex> \
  --ffmpeg-runtime-dependency-fingerprint sha256:<64-lowercase-hex> \
  --ffprobe-runtime-dependency-fingerprint sha256:<64-lowercase-hex>
```

The dependency fingerprints are host-owned identities for pinned FFmpeg and
FFprobe dependency closures. They are not version labels supplied by an Agent.

Workers immediately replay durable queued/running/canceling work at startup.
Interactive Agent preflight performs a fresh trusted probe; queued recovery
uses the frozen probe registry and can finish an already-published artifact
while source files are offline.

## Platform profile

The initial render runtime targets Linux and macOS. FreeBSD remains a build
target. Windows builds, but the inherited-descriptor media runtime currently
fails closed during initialization. AIX and Solaris are not `cutlyd` build
targets for this release candidate.
