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
before the passing rerun. Gap and metadata-number schema/corpus normal and race checks passed in
`b0nv18kk9`. Full synchronized checks remain pending: `bnbn7o0hb` and `bwucged86` hit transient
compile errors during the codec admission repair; the latter still passed preparev2, wavsource and
conformance normal tests but did not reach its race/vet commands.

Independent codec review reproduced a P2 preallocation defect: a single 20 MiB RawMessage subtree
composed of individually bounded strings reached typed shape allocation before the inherited 1 MiB
RawMessage limit rejected it. Measured total allocation was about 445 MB. The implementation now
prechecks complete raw spans using propagated field types, including nested Document raw leaves and
S2's stricter 16 KiB program configuration limit. Permanent tests cover large composites, exact
boundaries, unknown/case aliases and wrong container shapes. The implementer's reproduction reports
1,184 allocated bytes for the same 20,978,893-byte input; this is not independent closure. Its focused
prepared/renderplanv2/api normal, race and vet gates passed. Independent recheck remains pending;
final rejection alone does not satisfy early admission. The codec reviewer
`affbf76c18f4ddeaa` then terminated with a service API security-filter error before final repair
closure. No replacement review or permission change is used to bypass that interruption. The known
concrete repair and tests continue, but implementation results cannot substitute for independent
closure, and S3 exit remains gated.

Storage tests initially failed when corruption setup tried to overwrite intentionally read-only
records; tests now explicitly make those records writable before hostile modification. Concurrent
lock creation also exposed an ENOENT race under Darwin. The storage implementer reports bounded,
anchored retry and passing stress tests. The independent storage reviewer reports Darwin normal
10 repetitions, race 3 repetitions and Linux aarch64 runtime 10 repetitions in Docker with read-only
fixtures passed; these results precede the durability repair below.

Independent storage review found a P2 durability defect: replacing `environment.json` or `complete.v2`
after publication with byte-identical content on a new inode could return success although only the
original write inode had been file-synced. Directory sync does not establish replacement-file data
durability. The temporary `/tmp/s3-storage-independent-overlay.json` instruments Sync calls and
reproduces both branches while holding the original descriptor to prevent inode-number reuse. Repair
must sync the actual verified retained committed record and marker before claiming durable success,
including the idempotent retry path, or reject replacement. Implementation and independent recheck
have reached implementation freeze; independent recheck is pending. None of these interim results
constitutes S3 acceptance.

Parent verification after codec repair:

- `bj6eftcrk`: `TestEnvironmentV2` normal/race and full conformance normal/race passed, as did diff check.
- `bsn3r392o`: full prepared/preparev2/wavsource normal/race/vet passed after the storage fsync repair;
  Linux/386 and Windows/amd64 prepared compile-only checks and diff check passed.

The portable codec, schema and new fixed gap/number fixtures are a tested implementation checkpoint,
not independent codec acceptance. Existing fixture paths were not modified. Storage, concrete
resource projections and reconstruction remain separate unfinished boundaries. S3 stays open.
