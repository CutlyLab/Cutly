# Document v2 S7a evidence

## Scope and prerequisite

- Execution date: 2026-11-07.
- Standalone Go Cutly; consumer-application integration remains outside this stage.
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
| Concrete typed operations and strict codecs | Implemented and reviewed | 32 typed kinds, explicit scopes, number-preserving canonical codec |
| Atomic apply, inverse and validation | Implemented and reviewed | Exact source split, complete typed inverses and postimage predicates |
| Revision CAS and idempotency | Implemented and reviewed | Core CAS plus durable ID/key replay and conflict checks |
| Durable journal/recovery and owned results | Implemented and reviewed | Publication-boundary faults, subprocess crash/reopen, immutable records |
| Schema and fixed conformance vectors | Implemented and reviewed | Transaction and project plan/commit/history schemas, additive fixtures |
| Focused/full/race/vet/cross-target gates | Passed | Post-repair full normal/race/vet/tidy and four-target compilation passed |
| Independent read-only review | Passed scoped review | Transaction and store reviewers independently rechecked repairs; no remaining verified P0/P1/P2 |
| Legacy compatibility | Passed | All 172 fixture changes from baseline are additions; final full legacy and TypeScript checks passed |

## Implementation validation checkpoints

- `bdgyiulhp` was an interim integration failure while files were still being authored: two
  core test-construction failures, a genuine negative-retrack-position admission mismatch,
  not-yet-frozen inverse hashes and not-yet-written hostile fixtures. The core/schema agents
  corrected these; no legacy fixture was regenerated.
- `b6k3ktqmj` exited 0: `go test ./core/transactionv2 ./project/localv2 ./conformance -count=1`
  followed by `go vet` on those three packages. Store fault-test development was still in progress,
  so this does not prove final durable-store acceptance.
- `bixoc5vy2` exited 0: full `go test ./conformance -count=1` and `git diff --check`.
- `bad4xccjr` exited 0: `CGO_ENABLED=0 go test -exec=true` for those three packages on
  linux/amd64, linux/386, windows/amd64 and js/wasm. This is compilation, not native filesystem
  execution on those platforms.
- `bdvkfxziz` exited 0: existing TypeScript tests and `npm --prefix sdk/typescript run pack:check`.
  These are legacy compatibility/package checks, not public v2 SDK implementation.
- Transaction fixtures include three fixed valid/canonical/hash/digest vectors, a base Document
  and 36 complete hostile records. Project fixtures add three valid/canonical/hash vectors and
  six complete malformed plan/commit records. All are new paths.
- Project canonical SHA-256: plan `d8d3bf96f8eea87e79e11cbc617e89a85ff7e7d23fcfa4fb6f45607b0c1f1b4f`,
  commit `d82b1233687d48692e6e7228a31da53815063c37a29d29c5a7588764cffbaa84`,
  history `3931e6c9ef2fdf1744ed37f2d743b53feadfe1bb000aae6d68ad6fa113f4e29b`.
- Schema validation is structural; codecs enforce canonical bytes and exact numeric spelling.
  Neither portable record validation nor a matching hash authorizes a filesystem mutation.
- `br3z01at0` exited 0: `go test ./... -count=1`, `go test -race ./... -count=1`,
  `go vet ./...`, `go mod tidy -diff`. Local v2 store tests ran in both full passes (150.310s
  normal, 200.309s race). Later review fixes and the final History byte-cap change still require
  refreshed focused gates; this checkpoint is not final independent acceptance.
- `b2caeofqr` passed a 15-second project-record fuzz smoke with 10 seeds and 17 executions.
  Throughput was low; it is not broad fuzz coverage. Core implementer separately reported
  96,674 decoder executions in a 3-second smoke.
- Implementation checkpoints: transaction core `bd8c5e7`, transaction schema/corpus `3c4fe95`,
  store `bf5e333`, project schema/corpus `092c565`. A concurrent temporary reviewer file was
  inadvertently included in the store checkpoint; `e43ca4a` immediately untracked it without
  deleting the reviewer's working reproduction. The reviewer later removed it after permanent
  tests were added. It is not part of the final source.

## Independent review closure

Two separate reviewers directly reviewed the transaction and project boundaries. Both reported
no outstanding verified P0/P1/P2 after independently rerunning original reproductions and tests.
This does not close or retry the unrelated blocked S2 review.

Transaction review findings:

- Portable position bounds: schema admitted int64 while core admitted int32. Fixed in `3c4fe95`;
  all position-bearing branches now check 2147483647/2147483648 parity.
- Non-rendering property extensions incorrectly marked both render domains changed. Fixed in
  `13b7d49`; full identity/inverse content remains intact while impact ignores intent-only data.
- Annotation-track/unused-asset structural changes returned timeline IDs with no render impact.
  Fixed in `64306d8`; non-rendering-only edits now return an empty impact ID list.
- A suspected Unicode escaping budget defect was withdrawn after direct reproduction: the
  existing literal U+2028/U+2029 accounting was present. Additional typed/raw boundary tests
  were added, and a 136316358-byte encoded Document was rejected against the 134217728-byte cap.
- Reviewer independently verified a fractional retime split and exact inverse, historical inverse
  after disjoint metadata edits, and postimage conflict after a changed clip. Final core race,
  transaction conformance and vet passed.

Project review findings:

- P1: v1 creation could overwrite a root reserved by a durable v2 initialization intent before
  its first document projection. `61c98c4` adds a minimal reverse-version guard under the existing
  shared lock; `ea396d3` includes interrupted initialization → rejected v1 creation → exact v2
  recovery regression. Legacy APIs/fixtures are otherwise unchanged.
- P2: generic JSON scanning applied operation/plan/history array caps to same-named keys inside
  open metadata. `ea396d3` limits those caps to typed paths while preserving generic budgets.
- P2: opening an empty-history project did not verify its current immutable Document dependency.
  `ea396d3` makes New/Status/Inspect/Create retry consistently reject missing snapshots.
- All three original reproductions and permanent regressions passed independently. Reviewer ran
  focused publication/fault/crash/subprocess/CAS/revert/concurrency tests and project conformance.
  The 128MiB aggregate History retention cap was inspected, not maximum-size stress-tested.

Post-repair cross-target task `byswbs98p` exited 0: full repository compilation on linux/amd64,
linux/386 and windows/amd64; documented portable subset on js/wasm including core, protocol,
wirejson, buildidentity, render/api, render/preparev2, both local project versions and conformance.
The unchanged FFmpeg RSS test prevents full-tree wasm compilation, as documented in S1/S2.
Cross-compilation is not filesystem/media execution on those platforms.

Final post-repair task `bp674cc7p` exited 0 on implementation checkpoint `ea396d3`:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go mod tidy -diff
git diff --check
```

The final localv2 suite ran in 105.836s normal / 169.925s race; transactionv2 in 6.138s /
42.995s; conformance in 7.209s / 36.624s. `buf5thtda` also exited 0 for final TypeScript tests
and package-local dry-run. No npm publication, tag or daemon-default switch was part of this stage.

## Stage disposition

S7a is complete for the independent pure edit/project prerequisite as of implementation checkpoint
`ea396d3`: typed operations and inverses, durable CAS/idempotency/recovery, additive schemas and
fixtures, compatibility gates, and both independent reviews are complete. This is not public S7
SDK/CLI/Agent acceptance and does not bypass S2. Public v2 edit/render lifecycle delivery still
requires the dependency-ordered runtime stages and public integration gates.

Implementation of this lane is not broad M4 completion. Public SDK/CLI/Agent/Player, actual mixed
render execution, restart recovery and the release lane remain required.
