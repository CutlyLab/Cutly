# Document v2 local projects

`project/localv2` is a standalone Go store for `documentv2.Document` and
`transactionv2.Transaction`. It has no renderer or execution-stage dependency and
does not translate edits into legacy operations.

## API

```go
service, err := localv2.Create(ctx, absoluteDirectory, document, localv2.Options{})
service, err = localv2.New(absoluteDirectory, localv2.Options{})
snapshot, err := service.Inspect(ctx)
status, err := service.Status(ctx)
history, err := service.History(ctx, afterSequence, pageLimit)
proposal, err := transactionv2.Create(snapshot.Document, transaction)
planned, err := service.Plan(ctx, proposal)
applied, err := service.Apply(ctx, proposal.ID)
discarded, err := service.Discard(ctx, proposal.ID)
reverted, err := service.Revert(ctx, historicalTransactionID, newTransactionID, newReplayKey)
```

The host must create and own a **clean absolute, non-symlink directory**, including
its ancestors. Resolve platform aliases such as macOS `/var` before passing a host
path. `Create` initializes only an empty v2 project or replays an identical initial
document. `New` opens only initialized v2 metadata; it never adopts a bare document.
Legacy `.cutly/state.json`, `.cutly/initialized` or legacy intent makes either
operation fail rather than open a v1 project as v2. Use a distinct directory for a
v2 project. Metadata lives under `.cutly/v2`; the lock remains
`.cutly/project.lock` because v1 and v2 share the root `project.cutly.json` name.

`Apply` accepts **only a persisted transaction ID**. It cannot replace a proposal
with a new payload. Transaction ID and idempotency key permanently bind a digest,
including discarded proposals. Exact retries return `Replayed`; an ID/key with
different content conflicts. Plan uses the transaction's explicit revision/hash
CAS, and Apply rechecks both under the exclusive process lock. A successful
application increments document revision exactly once. Planning and discarding
advance only state sequence. History pages use state sequences, so sequences can
have gaps. Zero page limit means 1024; larger pages are rejected. A page's total
encoded commit bytes must not exceed 128 MiB; use smaller pages if this cap rejects.

A diagnostic-only core rejection returns `OK == false` and diagnostics, without
publishing state. Errors return a zero result. Returned documents, records and
metadata are owned copies. `New` uses a background context; context-bearing
methods honor cancellation while waiting for the cross-process lock.

## Storage and recovery

* Canonical JSON sorts object keys and preserves `json.Number` spelling. Fractional
  and high-precision raw metadata survives documents, transactions and records.
  No integer-only protocol canonicalizer is applied to embedded records.
* SHA-256-derived filenames, never transaction IDs, address immutable snapshots,
  plan records, commit records and state snapshots. `os.Root.Link` publishes these
  atomically without replacement. Existing objects must match their exact bytes.
* The state index contains bounded summaries/references, not candidate documents.
  Open/method calls strictly decode and semantically revalidate the complete
  indexed plan/history chain, loading one base/candidate at a time. Typed core
  replay must reproduce candidate, inverse and impact, not merely self-hashes.
* Dependencies are staged and file/directory-synced before a small no-replace
  durable intent names old/new document, state and commit identities. Only root
  document and current state projections may be atomically replaced, under lock.
* Recovery rolls forward only old/old, new-document/old-state or new/new pairs.
  Old-document/new-state, unrelated projections, missing dependencies or malformed
  transitions fail closed. Immutable history is never overwritten.
* Before intent publication, cancellation/error/panic leaves logical project state
  unchanged. Staged immutable orphan objects may remain and are not indexed. After
  intent publication, cancellation no longer promises no mutation: the operation
  finishes without cancellation or returns `ErrCommitUnknown`. A failed publication
  directory sync can additionally report `ErrDurabilityUncertain`. Reopen and retry
  the same ID/key to determine the outcome; do not invent a replacement transaction.
* Revert creates a new commit from the persisted typed inverse, rebased to current
  CAS while retaining its core-enforced target postimage predicates. Unrelated later
  edits survive; conflicting later target edits reject. It never restores a whole
  historical snapshot or changes the original commit. Revert stages its plan and
  commit under one durable intent, so a precommit failure does not expose a plan.

Hash validation is **integrity, not authorization** against an owner controlling
and rewriting the entire root. The host remains responsible for filesystem access
control, root ownership, disk quotas and preventing malicious concurrent namespace
replacement by privileged actors. Unreferenced immutable objects and scratch files
left by abrupt process exit are not garbage-collected in this initial profile.

## Limits and platforms

`DefaultLimits()` returns a copy, not mutable global policy. Passing zero `Options`
selects defaults; a custom `Limits` must set every field positively and cannot
exceed defaults. Defaults are document 128 MiB, transaction 16 MiB, plan/commit
record 40 MiB, state 8 MiB, 4096 plans/commits, 4096 operations and 256-byte IDs.
The record ceiling admits both a 16 MiB transaction and its bounded 16 MiB inverse,
plus version/hash/impact overhead; actual encoded sizes are checked. Conservative
pre-encoding admission can reject a near-ceiling value rather than allocate past
its budget. JSON depth is at most 64, with at most 2,000,000 scanned value nodes.
Raw metadata is included in byte/depth/node bounds. Limits are checked before
canonical encoding and wire arrays are scanned before typed record allocation.

Darwin, Linux and the BSD flock targets use an exclusive nonblocking filesystem
lock with context-aware retry, no-follow leaf opens, regular-file/inode checks and
host-confined `os.Root` operations. Other platforms, including Windows, fail with
`ErrUnsupported`; there is no unsafe process-local locking fallback. Filesystem
hard-link and directory fsync support are required. Tests exercise Darwin runtime;
Linux/Windows compilation is checked separately.

`EncodePlanRecord`/`DecodePlanRecord` and `EncodeCommitRecord`/`DecodeCommitRecord`
provide strict canonical v2 record boundaries. `Version == 2`, exact field names,
required fields, intrinsic bindings and render-impact invariants are validated.
These codecs do not confer filesystem commit authority. History is an ordered
`[]CommitRecord`, not an alternative mutable journal format.

## Validation

Tests cover full lifecycle, exact-number and ownership round trips, >16 MiB
documents, stale CAS, digest/key conflicts, discard/replay, postimage-safe reverts,
no-replace collisions, symlinks/legacy refusal, recomputed tampered candidates and
inverses, chain corruption, limits, concurrent services, errors/panics/cancellation
at every mutation publication boundary, initialization recovery, subprocess lock
exclusion, and process crashes around publication and intent removal.

```sh
go test ./project/localv2
go test -race ./project/localv2 -run 'TestConcurrentCAS|TestLifecycle|TestRevert'
go vet ./project/localv2
```
