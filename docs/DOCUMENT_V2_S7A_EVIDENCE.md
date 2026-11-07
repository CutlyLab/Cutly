# Document v2 S7a evidence

## Scope and prerequisite

- Execution date: 2026-11-07.
- Standalone Go Cutly; Manyfy work remains deferred.
- Owner reiterated completion of broad M4 after S2 implementation checkpoint `be42402`.
- S7a is the independent Document v2 mutation/project core lane permitted by the roadmap. It
  depends on existing Document v2 semantics, not unaccepted S2 execution contracts.
- S2 independent final audit remains blocked. This lane does not retry that review, waive its
  findings, or enter dependent S3–S6 implementation. S7 public APIs still require both runtime
  and mutation/project gates.

## Required boundary

Add typed Document v2 editing operations, atomic validation and inverse operations, revision
compare-and-swap, idempotency, and durable project journal/recovery without changing legacy
transaction or project APIs. Preserve exact authored time and Document v2 hash/canonical encoding,
including fractional JSON metadata. Do not use the integer-only protocol canonicalizer to rewrite
Document snapshots.

## Baseline

Task `b3lkib0is` exited 0 before implementing the new edit core:

```sh
go test ./core/documentv2 ./core/videoedit ./project/local ./sdk/go/cutly -count=1
git diff --check
```

This is a compatibility baseline, not new transaction/project acceptance evidence.

## Acceptance ledger

| Gate | Status | Evidence |
| --- | --- | --- |
| Concrete typed operations and strict codecs | Pending | Design in progress |
| Atomic apply, inverse and validation | Pending | Exact Document semantics required |
| Revision CAS and idempotency | Pending | Positive and conflict/replay tests required |
| Durable journal/recovery and owned results | Pending | Reopen, crash and path-safety tests required |
| Schema and fixed conformance vectors | Pending | Additive contracts only |
| Focused/full/race/vet/cross-target gates | Pending | Run on stable candidate |
| Independent read-only review | Pending | Must close P0/P1/P2 before public edit APIs |
| Legacy compatibility | Pending | Existing transaction/project fixtures stay unchanged |

Implementation of this lane is not broad M4 completion. Public SDK/CLI/Agent/Player, actual mixed
render execution, restart recovery and the release lane remain required.
