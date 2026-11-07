# Document v2 S3 evidence

## Status and prerequisite

- Started 2026-11-07 in isolated branch `codex/m4-sdk-completion`.
- S2 independently accepted: repairs `9d9f96d`, evidence `0de7008`, P0=0/P1=0/P2=0.
- S3 is in progress, not accepted. No restart reconstruction or broad M4 completion is claimed.
- Manyfy and release/archive expansion remain deferred. Preserve their existing work; do not push,
  publish npm, tag or switch daemon defaults without owner authorization.

## Implementation boundaries

1. Portable owned Environment v2: preserve Document JSON number spellings under its original
   canonical/hash contract; strictly verify embedded S1/S2 records and cross-bind all identities.
   Separate record admission from the unchanged v1 record limit. S1's existing conservative
   Document admission still applies; a 128 MiB Document wire ceiling does not imply every document
   beneath that ceiling is accepted by S1.
2. Durable store: separate v2 namespace, no legacy fallback, bounded regular-file reads,
   descriptor-relative namespace checks, no-replace publication, explicit uncertain durability,
   fresh-instance reopen and hostile filesystem tests.
3. Concrete inspection/probe projections: derive identities from the same host-authorized privately
   retained bytes used by native programs. Opaque S2 test receipts are not production observations.
4. Host-owned reconstruction: compare current host pins without replacing persisted claims;
   reconstruct native programs and explicitly bind their implementation identities to S2 semantic
   identities. Live programs/handles and callback authorization roots are not durable record fields.
5. Restart proof, schemas/fixed fixtures, full gates and independent P0/P1/P2 closure are mandatory
   before S3 exit and before dependent S4 work.

Existing native compilers can require whole-document resources while S1/S2 authorize selected-range
closure. Reconstruction must not fetch extra resources, modify the original Document or equate
unrelated source-manifest profiles. Unsupported combinations require an explicit diagnostic until
an additive scoped native compile path exists. Temporal-video execution remains S4 work.

## Job-free host authorization slice

`render/preparev2/environment.go` adds:

- `CheckEnvironmentPins`: structural manifest validation and current host-pin comparison without
  resource callbacks. Success alone is not resource authorization or permission to execute.
- `AuthorizeEnvironment`: original Document/Plan/manifest verification and host-pin comparison,
  followed by resource observations; no synthetic RenderJob and no re-freezing that replaces pins.
- `AuthorizedManifest`: owned canonical verified manifest, invalid zero value, no live handles.

The existing request authorization path reuses the factored private host-pin comparison, preserving
its ordering and semantics. Tests cover re-signed host pin mutations rejected before callbacks,
Document mismatch, zero values, independent output ownership, concurrent getters, missing/drifting/
panicking resource callbacks and nil/cancelled/panicking contexts.

Focused task `bgop0fbfn` exited 0:

```sh
go test ./render/preparev2 -count=1
go test -race ./render/preparev2 -count=1
go vet ./render/preparev2
git diff --check
```

Independent reviewer `a3a86eeaed4f2d6fa` completed this slice with no verified P0/P1/P2.
Direct inspection confirmed job-free checks, unchanged persisted pins, callback ordering and owned
results. Independent temporary overlay `/tmp/preparev2-review.zbm1M8/overlay.json` additionally tested
re-fingerprinted original-Document mismatch, callback errors/panics/oversize/pointer mutation,
final-resource cancellation, nested ownership and concurrent getters. Commands passed:

```sh
go test -overlay /tmp/preparev2-review.zbm1M8/overlay.json ./render/preparev2 -run '^TestAuditEnvironment' -count=1
go test -race -overlay /tmp/preparev2-review.zbm1M8/overlay.json ./render/preparev2 -count=1
go vet -overlay /tmp/preparev2-review.zbm1M8/overlay.json ./render/preparev2
```

The reviewer made no repository edits. Its review-skill invocation unexpectedly spawned a child,
which it immediately stopped without using any child review results. All conclusions above came
from its direct inspection and checks. The overlay cases are not yet all permanent regressions.

These checks do not accept the environment codec, durable store, concrete inspectors,
reconstruction factory or the whole S3 stage.
