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
  --media-runtime-bundle-root /opt/cutly/media-runtime/darwin-arm64 \
  --sdk-build-digest sha256:<64-lowercase-hex> \
  --compiler-build sha256:<64-lowercase-hex> \
  --evaluator-build sha256:<64-lowercase-hex>
```

Packaged builds accept only this signed-bundle profile. Once the release owner
supplies a real bundle, an Ed25519 public-key set and one exact bundle
fingerprint are pinned through build-owned linker stamps. `cutlyd` verifies the canonical manifest, signature, target,
complete file closure, file bytes/modes and exact bundle pin before acquiring
the render-state owner. FFmpeg/FFprobe paths and their independent closure
fingerprints are derived from that verified result; they are never accepted
from an Agent or from runtime trust-key flags.

The bundle root must be a canonical physical path and physically disjoint from
the project, state, output and every input root. It must remain host-owned and
read-only or code-signing protected. Portable Go cannot execute a binary from a
retained descriptor on every target, so an untrusted principal with permission
to replace the path after verification is outside this profile's guarantees.

Release builds must stamp all three values below; missing, partial, malformed,
or orphaned stamps fail closed:

```text
github.com/CutlyLab/Cutly/internal/buildidentity.mediaRuntimeDistributionMode=packaged
github.com/CutlyLab/Cutly/internal/buildidentity.stampedMediaRuntimeTrustedKeys=<key-id>=<64-lowercase-hex-ed25519-public-key>[,...]
github.com/CutlyLab/Cutly/internal/buildidentity.stampedMediaRuntimeBundleFingerprint=sha256:<64-lowercase-hex>
```

The current tagged-release workflow does not yet have owner-provided platform
bundles, public keys, or fingerprints, so it deliberately remains an unstamped
development build. Signed-runtime wiring is complete, but default packaged
rollout is still blocked on those release artifacts; no test key is promoted as
a production trust root.

For explicit local testing only, an unstamped development build may use direct
tools and caller-declared closure identities. This mode is deliberately noisy
and is rejected by packaged builds:

```sh
cutlyd \
  ... \
  --unsafe-allow-unverified-media-runtime \
  --ffmpeg /opt/local/bin/ffmpeg \
  --ffprobe /opt/local/bin/ffprobe \
  --ffmpeg-runtime-dependency-fingerprint sha256:<64-lowercase-hex> \
  --ffprobe-runtime-dependency-fingerprint sha256:<64-lowercase-hex>
```

Workers immediately replay durable queued/running/canceling work at startup.
Interactive Agent preflight performs a fresh trusted probe; queued recovery
uses the frozen probe registry and can finish an already-published artifact
while source files are offline.

## Platform profile

The initial render runtime targets Linux and macOS. FreeBSD remains a build
target. Windows builds, but the inherited-descriptor media runtime currently
fails closed during initialization. AIX and Solaris are not `cutlyd` build
targets for this release candidate.
