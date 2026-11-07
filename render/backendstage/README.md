# Durable backend stage adapter

`backendstage.Service` connects five already-frozen boundaries:

1. `prepared.Store` resolves the exact semantic plan and execution manifest.
2. The legacy path uses a host resolver to select its live recovery-capable
   backend; reference mode instead uses a directly configured host-owned
   assembler and never sends the manifest to that resolver.
3. The default restricted hard-cut profile uses one job-scoped `StagePublish`
   task to render or recover the external output.
4. An explicitly configured reference-chunk profile instead executes fixed
   surface and PCM tasks before the final publication task.
5. Every payload is published into CAS before its checkpoint can commit.

The stage cache key excludes `JobID` and `ArtifactURI`, so equivalent jobs share
the encoded media CAS entry. The canonical checkpoint receipt still contains the
job-specific `RenderArtifact`. Final resolution verifies the checkpoint binding,
the CAS payload identity, and the external media/evidence pair again.

This ordering closes the useful crash windows:

- publication before CAS: the next attempt opens a verified publication snapshot;
- CAS before checkpoint: the next CAS publish converges on `AlreadyPresent`;
- checkpoint before scheduler completion: the engine loads it and the artifact
  resolver repeats both integrity checks.

Backend identity and capabilities are checked whenever an executor is created.
The full source-touching preflight runs only after publication inspection proves
that no recoverable output exists. A job that already crossed the publication
boundary can therefore finish CAS/checkpoint recovery even when its source files
are temporarily offline; a job that must encode still fails closed on preflight.

## Resource admission

`Config.MemoryBytes` and `Config.DiskBytes` are explicit per-job estimates and
must fit the enclosing runtime's stage limits. `DiskBytes` must conservatively
include all backend encoding/publication scratch plus:

- one media-sized anonymous recovery snapshot;
- one media-sized CAS publication payload;
- `cas.PublicationOverheadBytes` of CAS metadata headroom.

The executor enforces that known minimum after it learns the encoded size. The
host remains responsible for choosing a larger bound for codec scratch, input
materialization, filesystem allocation units, and concurrently active jobs.

V1 deliberately uses one non-retrying publish task. Backend and CAS publication
are independently idempotent; retries happen by resuming the durable job rather
than repeating an ambiguous in-process publish attempt. This remains true for
both the legacy whole-job publish and the reference profile's final codec task.

## Executable reference chunk profile

`Config.ReferenceChunks` is opt-in. It requires typed providers that return
real `cpucompositor.Surface` frames and `pcm16le.Block` sample blocks. There is
no implicit adapter from the whole-job backend and no synthetic byte fallback.
Missing providers, invalid implementation identity, an unsafe scratch root, or
insufficient resource estimates fail closed before provider work begins.
The configured provider digest must equal the frozen execution manifest's
`EvaluatorBuild`, which puts the caller-declared provider identity into every
chunk cache key via the execution-manifest fingerprint. Every recovered receipt
must carry that exact digest or it is Missing/Rejected. This is provenance and
admission metadata, not executable attestation: the service cannot prove that
the caller's digest describes the bytes actually loaded. Changing provider
bytes without changing the digest is a host contract violation and is not
detectable here.

The scratch path must be absolute, already resolved, and contain no symlinked
component. On Unix the leaf must be caller-owned mode `0700`; writable ancestors
are accepted only for root/caller-owned sticky shared roots such as `/tmp`.
Other platforms still receive the component/symlink checks, while their DACL
privacy remains a host responsibility because `os.FileMode` cannot attest it.
The executor repeats those checks immediately before every spool creation so a
long-lived service fails closed after path or permission drift. This narrows but
does not eliminate the validation-to-`CreateTemp` race; a future dirfd-relative
spool API is required for an atomic path proof.

The profile fixes the protocol partition from `render/chunkplan` (64 frames and
65,536 stereo sample frames). The executor serializes surfaces and PCM itself,
hashes the exact canonical bytes, atomically publishes them to CAS, and returns
the canonical chunk receipt. Provider calls cannot observe `JobID`, the
idempotency key, or the final publication URI because those values are excluded
from chunk cache keys.

Surface and PCM tasks allow at most two in-process attempts, and only an
explicit `stagedag.StageError` classified as `transient` can select the second
attempt. The receipt and checkpoint both record the actual producing attempt.
Cancellation, deadlines, panics, unclassified failures, resource/permanent
classes, unsafe spool cleanup, and every legacy/final publication failure stop
without an in-process retry. A durable restart can still reuse any already
committed chunk checkpoint.

Reducing a retry policy does not invalidate an already committed canonical
chunk. A checkpoint whose receipt records an older, higher attempt remains
eligible only as a cache hit; a new execution must stay within the current
policy. `stagedag.RunResult.TaskOutcomes` records per-task cache/execution
provenance, so artifact resolution recomputes `Executed`, `CacheHits`, and
`Retries` exactly instead of mistaking historical attempt telemetry for work
performed by the current run.

The final publish task depends on every chunk checkpoint. Before the backend is
allowed to encode, the executor revalidates each receipt and CAS entry and
computes the complete video/audio aggregate roots. Reference mode then requires
a live `ChunkAssemblerBackend`; a backend that implements only the legacy
`Render` method fails closed. The assembler receives no original media
locators. Its only decoded media input is a forward-only `ChunkSource` that
opens the exact ordered CAS descriptors, rejects duplicate or changed opens,
and requires every payload to be consumed completely. Assembly recovery and
publication-open callbacks also receive the same aggregate-bound request, so a
legacy publication cannot be mistaken for a chunk-derived publication. The
legacy `Backend.Render`, `InspectPublished`, and `OpenPublished` methods are
never called in reference mode.

Each open chunk has a time-based CAS lease heartbeat independent of assembler
read size or cadence. A heartbeat failure is terminal for that reader and makes
the final task failure-zero. When no final checkpoint exists, a residual
external publication is not recovery authority: every final invocation must
call the assembler, consume and verify the complete ordered chunk set, and only
then inspect/open the aggregate-bound publication.

Aggregate roots are committed in the final receipt. `SelectMissingChunks` uses
`chunkplan.SelectMissing` to classify job-specific checkpoints; corrupt or
absent CAS data selects only the affected chunk for recomputation. Equivalent
jobs share CAS keys, but checkpoints remain job-specific: this phase does not
claim cross-job compute avoidance, so a new job may invoke the provider again
and converge on `AlreadyPresent`.

Final task memory must cover the assembler plus a conservative validation
allowance derived from the exact dependency count: four maximum-size receipt
working copies, fixed checkpoint/descriptor metadata per dependency, and fixed
aggregate/request storage. Plan construction rejects an insufficient or
overflowing declared `Config.MemoryBytes`; artifact resolution rechecks the
same bound before allocating dependency state.

This phase does **not** claim resumable codec state. The chunk assembler consumes
the cached surfaces/PCM and performs a complete encode/mux/publication. If that
encoder crashes before publication, all committed surface/PCM chunks remain
reusable, but the next run rereads them and restarts the whole encode. The
receipt records this explicitly as
`pre-codec-chunks-resume-final-codec-restarts.v1`.

`AdaptFFmpeg` intentionally exposes only the legacy hard-cut backend today; it
does not pretend that source-timeline rendering consumes reference chunks. A
future FFmpeg chunk assembler must implement the aggregate-bound interface and
raw surface/PCM ingest before reference mode can use it.
