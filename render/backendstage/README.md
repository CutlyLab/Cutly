# Durable backend stage adapter

`backendstage.Service` connects four already-frozen boundaries:

1. `prepared.Store` resolves the exact semantic plan and execution manifest.
2. A host resolver selects the matching live recovery-capable backend.
3. One job-scoped `StagePublish` task renders or recovers the external output.
4. The encoded media bytes are published into CAS before a checkpoint can commit.

The stage cache key excludes `JobID` and `ArtifactURI`, so equivalent jobs share
the encoded media CAS entry. The canonical checkpoint receipt still contains the
job-specific `RenderArtifact`. Final resolution verifies the checkpoint binding,
the CAS payload identity, and the external media/evidence pair again.

This ordering closes the useful crash windows:

- publication before CAS: the next attempt opens a verified publication snapshot;
- CAS before checkpoint: the next CAS publish converges on `AlreadyPresent`;
- checkpoint before scheduler completion: the engine loads it and the artifact
  resolver repeats both integrity checks.

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
than repeating an ambiguous in-process stage attempt.
