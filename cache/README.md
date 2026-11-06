# Cutly local stage cache (M3 first slice)

This directory is intentionally independent of the scheduler and workers. It freezes two small
protocols that those layers can consume later:

- `key`: a versioned canonical stage-key preimage and SHA-256 identity;
- `cas`: a local immutable entry store with integrity revalidation, reader leases, disk preflight,
  quarantine, and deterministic quota collection.

## Stage key v1

The hash preimage binds the cache namespace, schema/profile, artifact kind, semantic plan
fingerprint, execution-manifest fingerprint, stage profile, canonically ordered input hashes,
optional frame/sample ranges, and output profile. Job IDs, idempotency keys, worker IDs, local
paths, and artifact URIs are not part of the type and cannot affect the digest.

## Local entry layout

```text
root/
  entries/<2 hex>/<64 hex full-StageKey path hash>/
    published/             # immutable payload directory
      payload
      manifest.json
      complete             # hashes the exact canonical manifest bytes
    leases/                # mutable reader lease records
    gc.claim/              # exclusive acquire/commit claim when present
  scratch/.partial-*/      # never read
  quarantine/<unique>/entry
  trash/<unique>/entry     # transient GC rename target
```

The 64-hex local path name hashes the complete self-describing `StageKey`, not only its digest, so
even a manually constructed key with the same digest but a different namespace or artifact kind
cannot alias storage. Publishing builds the entire non-empty entry wrapper (`published` plus an
empty `leases` directory) in scratch, reopens and rehashes it, and atomically renames that wrapper
to the missing canonical path. Installed same-version entries are non-empty, so a concurrent local
directory rename cannot replace a winner. It then fsyncs the entry, shard, and scratch parent. A
duplicate verifies the existing payload and returns `already_present`; a different payload for the
same key is a collision and never replaces bytes. Legacy reservation or otherwise incomplete key
directories have unknown ownership and remain fail-closed until an operator recovers them.

Opening creates and fsyncs a lease before revalidating the sentinel, strict canonical manifest,
payload size, and payload hash. Corruption atomically moves the complete key directory to a unique
no-replace quarantine slot and returns a typed corrupt/recompute miss.

GC binds every candidate and claim to the exact canonical manifest generation. It first publishes
an exclusive acquisition claim, performs its final lease scan, then durably changes that claim to
a commit phase before the delete rename. Readers check for any claim both before and after
publishing a lease. Claims are deliberately not recycled by wall-clock age: an unknown, legacy,
partial, or crash-abandoned owner stays fail-closed instead of admitting a reader into a possible
delete window. Active leases are skipped; expired/malformed leases are removed. Candidate order is
creation time, key digest, then canonical path.

## Deliberate first-slice limits

- Quota charge is logical payload + manifest + sentinel bytes; filesystem metadata, scratch,
  quarantine, and trash are not charged.
- The root and its parent path are trusted local configuration. The store rejects direct symlink
  roots and entry files, requires the root's immediate parent to exist, and syncs both a newly
  initialized root and its parent. It does not yet use directory capabilities to defend against a
  same-user process replacing ancestor directories or mutating a payload through another hardlink
  after `New`.
- The production durability profile has been exercised on Darwin/Linux same-filesystem storage.
  Other platforms must provide `FileSystem` semantics with atomic rename (including failure when
  the existing directory target is non-empty), durable file and directory sync, and trustworthy
  free-space reporting before making the same claim. The default Windows build is compile-only:
  its free-space implementation currently returns `ENOTSUP`.
- Abandoned scratch/quarantine slots stay reader-invisible but do not yet have a writer-heartbeat
  reaper. Removing them by age alone would be unsafe for long-running publishers.
- Crash-abandoned GC claims and legacy/incomplete publication slots require explicit operator
  recovery in this first slice. This trades possible permanent cache pinning for no unsafe
  ownership takeover.
- Individual protocol records and payload reads are bounded, but directory cardinality and GC's
  in-memory candidate/report slices do not yet have a configured ceiling. Lease expiry also
  assumes sufficiently consistent wall clocks among processes sharing one root.
- This slice does not define scheduler checkpoints, missing-range resume, worker receipts, remote
  object-store mirroring, or an SDK compatibility promise.
