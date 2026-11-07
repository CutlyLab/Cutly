# Local render checkpoints

This package implements `stagedag.CheckpointStore` for a trusted local project
or render root. A checkpoint is published only after the
referenced `cache/cas` entry has been opened under a reader lease and its full
payload hash and size match `StageOutput`.

Each semantic task locator hashes the job, plan, execution manifest, stage,
task profile, complete stage cache key and optional frame/sample ranges. The
published directory contains canonical `checkpoint.json` plus a hash-bound
completion sentinel. Publication writes and syncs a non-empty scratch
directory and renames it into a missing locator slot; an existing different
canonical value is an immutable collision.

Per-locator OS locks coordinate processes and are released by the kernel after
a crash. Corrupt checkpoint bytes, stale CAS references and symlink entry names
are atomically moved to a bounded quarantine and returned as a clean miss so
the stage is recomputed. A visible publication whose final directory sync
fails returns `ErrDurabilityUncertain`; retrying the exact checkpoint is
idempotent.

Lock files use a fixed set of locator stripes, quarantine admission is globally
serialized, and incomplete scratch publications have an explicit admission
bound. Directory reads stop after the small number of entries needed to prove
shape, so corrupt directories cannot force unbounded metadata allocation.

Security and portability boundaries:

- the root and its ancestors must be host-owned;
- derived paths are hexadecimal and named files are checked with
  `Lstat`/`SameFile`, but hostile hard links or privileged ancestor replacement
  are outside the profile;
- Windows uses an exclusive no-share lock and file sync, but Go exposes no
  portable parent-directory fsync, so real Windows power-loss testing remains
  a release gate;
- platforms without a reliable process lock fail closed during `New`;
- CAS GC may evict an artifact after `Load` releases its verification lease;
  executors must reopen dependencies and a subsequent job retry will treat the
  checkpoint as stale. This package does not claim a distributed pin protocol.
