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
reconstruction factory or the whole S3 stage. The authorization slice is committed at `8dab14d`.

## Native WAV observation prerequisite

`render/wavsource.Inspect` returns the exact existing native ProbeManifest without a synthetic
Document, live Provider or external IO. It checks expected content hash and size before WAV decode,
reuses the existing private probe projection/build/options functions, and leaves the original
Compile implementation and fixture bytes unchanged. Expected identities still require independent
host authorization; the returned observation retains no resource bytes for future execution.

Parent task `bxwqzlgtt` passed package normal/race/vet. Independent reviewer
`a3a86eeaed4f2d6fa` reported no verified P0/P1/P2 in this two-file slice. Its temporary overlay
`/tmp/wavinspect-review.kiU9eP/overlay.json` verified stereo parity, all 24 observed cancellation/panic
checkpoints, limits, oversized chunk rejection, concurrent ownership and existing fixture stability:

```sh
go test -overlay /tmp/wavinspect-review.kiU9eP/overlay.json ./render/wavsource -run 'TestAuditInspect|TestInspect|TestDocumentPreflightAndCanonicalFixtures|TestCanonicalWAVSourceVectors' -count=1 -v
go test -race -overlay /tmp/wavinspect-review.kiU9eP/overlay.json ./render/wavsource -run 'TestAuditInspect|TestInspect|TestDocumentPreflightAndCanonicalFixtures|TestCanonicalWAVSourceVectors' -count=1
go vet -overlay /tmp/wavinspect-review.kiU9eP/overlay.json ./render/wavsource
GOOS=linux GOARCH=386 CGO_ENABLED=0 go test -overlay /tmp/wavinspect-review.kiU9eP/overlay.json ./render/wavsource -c -o /tmp/wavinspect-review.kiU9eP/wavsource-linux-386.test
```

All passed. Linux/386 was compile-only. No GiB-scale runtime/OOM or external IO evidence is claimed.
Audited SHA-256: `inspect.go` = `8326d2f2dc19359919e2ec36f2d7e4e6e35b8ccf8094e4de9fd354d2513aa473`,
`inspect_test.go` = `19b435e646adbb074dcdae4e7d12ef68767e3b2c5d46042670e0c23795580ef0`.
This does not complete PNG/font projections or restart reconstruction.

## Portable codec and storage integration (pending)

The initial gap record schema/canonical corpus passed in `bvdh63gm5`. The previous attempt
`bcpz3ca91` hit a transient compile error in an actively edited storage file; that error was fixed
before the passing rerun. Added metadata-number vectors still require final synchronized checks.

Independent codec review reproduced a P2 preallocation defect: a single 20 MiB RawMessage subtree
composed of individually bounded strings reached typed shape allocation before the inherited 1 MiB
RawMessage limit rejected it. Measured total allocation was about 445 MB. Repair and independent
recheck are pending; final rejection alone does not satisfy early admission.

Storage tests initially failed when corruption setup tried to overwrite intentionally read-only
records; tests now explicitly make those records writable before hostile modification. Concurrent
lock creation also exposed an ENOENT race under Darwin. The storage implementer reports bounded,
anchored retry and passing stress tests; independent storage review remains pending. None of these
interim results constitutes S3 acceptance.
