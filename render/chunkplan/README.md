# Pre-codec chunk plan v1

This package is the first production-scheduler contract below a frozen
`RenderJob`. It closes the identity and recovery math before any codec:

- video is partitioned into fixed 64-frame composition-surface groups;
- audio is partitioned into fixed 65,536-sample PCM blocks;
- every task key binds plan, execution manifest, dispatch stage, exact range
  and output profile, while excluding job ID and publication URI;
- every receipt binds that key and range to exact bytes, an attempt and a
  worker invocation/executable digest;
- the package-owned `Plan` retains the frozen `RenderJob`, so recovery proves
  that task ordinals still form the complete fixed partition before accepting
  any checkpoint;
- resume selection accepts a checkpoint only when its receipt and exact CAS
  object remain valid, using one plan freeze/index and a linear candidate scan;
- aggregation requires the fixed partition with no gap, overlap or reorder and
  reuses `cutly.decoded-sequence.v1` for its root.

That decoded root covers decoded bytes and composition ranges, not worker
provenance. A final EvidenceManifest must additionally bind the ordered chunk
receipt fingerprints and its worker trust/attestation policy.

Checkpoints intentionally remain job-specific. Equivalent jobs produce the
same task cache keys, so a later executor may reuse verified CAS chunks and
commit a new job-bound checkpoint rather than trusting another job's journal.

Programmatic jobs pass fixed scheduler-equivalent string, digest and range
admission checks before JSON freezing. This keeps oversized identifiers and
publication URIs from turning validation into an unbounded marshal allocation.

## Not implemented here

This package does not decode, compose, run audio DSP, launch workers, publish
CAS data, persist checkpoints, encode, mux or publish final media. It also does
not provide execution leases, adaptive concurrency, sandboxing or attestation.
`WorkerBinding` is integrity provenance only. Production integration must add
those boundaries without weakening this contract.

Cache key v1 admits only non-negative composition indices. A job with a
negative frame or sample index fails closed until a versioned signed-index
cache-key contract exists.
