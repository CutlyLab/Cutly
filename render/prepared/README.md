# Prepared render environments

`prepared.Store` durably records the exact semantic `RenderPlan` and frozen
`ExecutionManifest` accepted by preflight. Records are addressed by both
canonical fingerprints; queued execution never reloads a mutable project or
re-resolves the host environment.

The local profile provides canonical strict decoding, idempotent native
no-replace publication (Darwin `renameatx_np`, Linux `renameat2`, Windows
`MoveFileW`), file and directory fsync on POSIX, bounded records/directories,
cancelable striped cross-process locks, and no-follow/reparse-point rejection.
Unsupported publication platforms fail `New` closed. Windows uses exclusive
file handles, but Go has no portable directory fsync there, so power-loss
directory-entry durability remains a platform release gate.

A crash before publication can leave an owned `.partial-*` scratch directory.
Scratch enumeration is bounded and its configured limit fails closed, but this
version does not guess whether an abandoned directory is still owned by a live
process. Deployments should provision a per-owner root and remove abandoned
roots only after proving that owner is stopped; a lease-aware scavenger is a
future extension.

The configured root and its ancestors must remain host-owned. The package does
not claim to resist a privileged process replacing a trusted ancestor or
creating hostile hard links.
