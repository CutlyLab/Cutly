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
are now independently complete for this storage defect. Reviewer `a91a0421285b64f18` re-ran the
instrumented replacement and idempotent retry cases three times each on Darwin and native Linux
aarch64 and confirmed both actual committed inodes receive file Sync. No verified P0/P1/P2 remains
in the assigned storage slice. This does not close the separate codec review or constitute S3 acceptance.

Audited repair SHA-256:

- `storage_v2_unix.go`: `284c6bc6d85db835c670c267733252490770601a511b19446dab9fc2991dc32a`
- `store_v2_test.go`: `40c5e3218a001ba8c0149a17a862d0e33a676d8264a6935df19e3a16f9f07d0f`

The independent reviewer also passed repaired full prepared normal/race/vet, Darwin storage race
three times, Linux aarch64 storage ten times, Windows/amd64 and FreeBSD/amd64 cross-builds, and
fresh-store startup stress on both native platforms. These tests do not simulate actual power loss.
The original Darwin ENOENT was not independently reproduced, so its kernel cause remains unproven.
Crash-leftover scratch remains bounded/fail-closed rather than automatically scavenged.

Parent verification after codec repair:

- `bj6eftcrk`: `TestEnvironmentV2` normal/race and full conformance normal/race passed, as did diff check.
- `bsn3r392o`: full prepared/preparev2/wavsource normal/race/vet passed after the storage fsync repair;
  Linux/386 and Windows/amd64 prepared compile-only checks and diff check passed.

The portable codec, schema and new fixed gap/number fixtures are a tested implementation checkpoint
at `3f98f91`, not independent codec acceptance. Existing fixture paths were not modified. The
independently accepted storage slice is committed at `6b14835`. Concrete resource projections and
reconstruction remain separate unfinished boundaries. S3 stays open.

## Concrete resource inspection projections (slice independently accepted)

New `render/resourceidentityv2` separates independently authorized expected bindings, actual opaque
native inspections, and decoded wire identity claims. PNG and font wrappers prehash admitted bytes
before native decoding/parsing. Static receipts bind pinned descriptor fingerprints and complete
portable PNG or sfnt/glyf core facts; audio reuses `wavsource.Inspect` and `probe.Fingerprint` without
inventing a new probe format. Font core inspection is explicitly not outline or shaping authority.
Live surfaces/fonts, host limits and locator permissions are not serialized. Native font workload
counters use fixed byte/count arithmetic rather than architecture-dependent allocation measurements.

The implementer passed package normal/race/vet. New schema/fixed corpus uses existing native PNG and
font vectors without changing their source bytes; its author passed conformance and resource package
normal tests. Independent resource-only review is in progress. Parent task `bzhet8sh7` failed before
running tests because active evaluator prerequisite edits temporarily referenced `DomainScope` before
its type file existed; it provides no successful resource gate evidence. The synchronized rerun
`b39c5e2jv` passed resource/conformance normal/race/vet, resource Linux/386 and js/wasm compile-only
checks, and diff check. These gates preceded independent ownership-defect closure.

Independent resource review found a P1 ownership defect: `Surface()` and `Font()` returned pointers
to the actual retained exported structs. An external caller could assign a replacement or zero value
through that pointer, changing the native resource while `Check` still accepted the old fingerprint.
Private struct fields do not prevent whole-struct assignment. The reviewer reproduced both cases
using external-package tests. The implementation now returns owned struct-header copies over private
immutable storage, with permanent external-package replacement/zero/concurrent regression tests.
Its focused normal/race/vet gates passed. Independent reviewer `a3a86eeaed4f2d6fa` inverted the
external-package reproduction and verified the repair, closing this P1 with no remaining verified
P0/P1/P2 in the resource slice. Audited `model.go` SHA-256:
`6860e3624634ad1387af1a7a7725170db14281d9313a19b84bde11348fefa1dd`.

Independent checks also passed cancellation/panic exhaustion across all 91 PNG and 237 font observed
context checkpoints, 8 MiB wrong-hash allocation-before-parser tests, package normal/race/vet,
targeted schema/corpus/inventory tests, and Linux/386 compile-only checks. These accept only the
resource identity/inspection boundary, not locator authority, font shaping or restart reconstruction.

## Mixed-document reconstruction prerequisite (in progress)

The existing visual compile entry uses whole-Document capability preflight and rejects authored audio.
A mixed restart proof must not solve this by pruning the Document or using separate Documents. The
additive domain-scoped evaluator will validate and hash the full original Document while compiling
only the checked selected domain, retaining original indexes and exact requested-range guards.
Legacy global capability checks and fixtures remain unchanged. The evaluator implementation is
frozen with focused normal/race/vet gates passing. Independent reviewer `a91a0421285b64f18`
accepted this evaluator slice with no verified P0/P1/P2. An independent exact `big.Rat` overlap
oracle covered both domains across 84 negative/fractional scopes, omitted closures and exclusive
endpoints; overlay normal three times and race once passed. Evaluator/documentv2/conformance/
visualcompositor/audiograph normal, evaluator/documentv2/conformance race, vet and domain tests ten
times also passed. Optional extracted legacy-baseline parity was skipped after a worktree command
guard refusal; compatibility evidence is the direct nil-scope refactor diff and existing fixtures,
not execution of an extracted baseline.

Audited SHA-256: `domain_snapshot.go` =
`04a76cba3cdc84b90ca7d2d7ca3e2382a0ccbea9d0b53b8c207d6bd494438a16`,
`domain_snapshot_test.go` = `3939157f3d6215d9a0c84239872a89a07d3204a6a1ee10c5255df89ada9bf41f`,
`program.go` = `165cddefd2ce25285013d92b823ced89cf896bdf64ab71edaa00b4f5b3cabb04`.
It requires exact same-domain positive-overlap clip closure, rejects transitions on the selected
timeline, bounds admission across the full Document/scope/offered-capabilities inputs, and returns
no unrestricted evaluator Program. It does not perform resource IO or claim hard cancellation
latency inside synchronous validation/canonicalization.

Downstream native domain compile
entries, the host-owned reconstruction factory, explicit semantic/native identity bridge and a real
same-Document PNG/vector/pinned-text/WAV subprocess restart proof remain to be implemented and audited.

## Scoped native visual compilation (slice independently accepted)

On 2026-11-08 the additive `visualcompositor.CompileDomain`,
`referencevisual.CompileDomainV2` and resource-free `PreflightDomainV2` passed independent
read-only review by `a3a86eeaed4f2d6fa`, with no verified P0/P1/P2. Preflight and compilation
share normalized configuration, fixed capabilities, scope validation and aggregate admission.
The original full Document hash and indexes survive authored audio; providers receive the
unchanged Document. Initial support requires exactly one authored timeline, all enabled visual
clips in the requested intersection, and no transitions. Temporal video is not implemented here.

New explicitly versioned scope/config/source identities prevent same-Document cross-range replay,
including transparent gaps. Legacy profiles, fixture bytes and referencevisualchunk admission remain
unchanged. Tests exercise actual PNG/vector/pinned-font Text with authored audio, original indexes,
owned getters, concurrent readers, pre-callback scope guards and cancellation/panic failure-zero.

Independent commands passed:

```sh
go test ./render/visualcompositor ./render/referencevisual -count=1
go test -race ./render/visualcompositor ./render/referencevisual -count=1
go vet ./render/visualcompositor ./render/referencevisual
git diff --check -- render/visualcompositor render/referencevisual
go test ./conformance -run '^(TestDocumentV2Fixtures|TestDocumentV2VectorViewportFixtures|TestDocumentV2PinnedFontViewportTextFixtures|TestSchemasCompileAndValidateInstances)$' -count=1
```

Audited SHA-256: visual domain source `fe8aca653a484c4d6f6bd0e68f961cf02fcee50147d1dd3830f3819a728326d7`,
referencevisual domain source `e6347dde8d32759a682aaf1324c08b67fba088939899201b0e20d9ac1fcf87c5`.
The review used source analysis and existing tests, not a new independent adversarial overlay or
exhaustive fuzzing. It does not accept audio integration, factory, codec or S3 as a whole.

## Private resource snapshot prerequisite (slice independently accepted)

The new `reconstructv2.ResourceSource` is trusted host configuration, not wire authority.
Its private snapshot helper independently compares the expected binding before Open, reads bounded
private bytes, closes returned readers even alongside Open errors, isolates pointer-valued claims,
and returns zero on read/close/cancellation/panic failure. It must only be used after joint environment
verification, scope/host-pin/aggregate admission; the same bytes must subsequently be inspected.
It cannot interrupt synchronous blocked host callbacks or prove locator confinement by itself.

Parent task `bhbzho2q1` and independent reviewer `a3a86eeaed4f2d6fa` passed package normal/race/vet
and diff checks. No verified P0/P1/P2 was found in source.go/source_test.go. An attempted independent
/tmp overlay write was blocked by a classifier service timeout and was not retried or rerouted;
no such extra test coverage is claimed. Audited source SHA-256:
`314bafacfde48a1a455b92d38bce248757ea40cdfa229e18f419edef53d09abe`.
Additional scope/font/factory work is not included in this acceptance.

## Scoped native audio compilation (slice independently accepted)

On 2026-11-08 `audiograph.CompileDomain` and its shared resource-free `PreflightDomain`
passed independent read-only review by `a91a0421285b64f18` with no verified P0/P1/P2.
The opaque DomainProgram retains the full original Document identity and indexes, requires exactly
selected graph bindings, and binds owned scope/config/source/descriptor receipts separately from
the unchanged DSP profile. Initial support rejects audio buses, selected rendering extensions,
nonunit retime and selected-timeline transitions. The whole-document WAV source policy remains
unchanged; the factory must reject unsupported full-document/range combinations before IO.

An independent `math/big.Rat` interval oracle checked 840 origin/rate/first/count combinations with
an instrumented block allocator. Rejected ranges made zero allocator and provider calls, including
silent gaps and zero blocks. Zero blocks require a half-open in-range start; nonzero block exclusive
end may equal requested end, but partial cells extending beyond it fail. Overlay normal three times
and race once passed. Independent exact legacy/domain pan-automation parity passed at 16 times.
Temporary reproduction: `/tmp/s3-audio-domain-audit-overlay.json`.

Independent full audiograph normal/race/vet, integration normal tests for audiograph/wavsource/
pcm16le/evaluator/conformance, wavsource/pcm16le race, domain tests ten times and diff checks passed.
Tests ran on Darwin; no other runtime platform is claimed. Audited domain.go SHA-256:
`653f80dc54b786f7ebe2e722e1fe7753e85c030f4debb86395dac0b03acb5101`.
This accepts the audio slice only, not the reconstruction factory or S3 stage.

## Reconstruction support scope (slice independently accepted)

The private `selectScopes` gate runs only after joint owned Environment verification. It rejects
multiple timelines, authored/planned transitions and buses, nonempty zero-tick domains, unsupported
asset/track pairs, URI-only selected resources, omitted enabled clips (including out-of-range reuse
of a selected asset), changed authored binding tuples and non-exact external/indirect-font closures.
It retains original indexes/ranges and returns owned sorted visual/audio selections without IO.
Native preflights still decide supported rendering properties; this gate is not S1 verification.

Independent reviewer `a3a86eeaed4f2d6fa` closed with no verified P0/P1/P2, including a test-delta
recheck using actual S1 Resolve and VerifyAgainstDocument for the indirect-font positive case.
Focused normal/race tests and package vet passed. The initial parent fixture had overlapping clips
(despite one being disabled); a later text fixture mixed Latin/Han under a Han script declaration.
Both fixture errors were corrected before successful runs; neither was an implementation pass.
No legacy fixture file was edited. This does not prove joint Environment/native factory execution.
Audited SHA-256: scope.go `f711fbd009b24da83fdc72dc55aba3088376690be86789c033e58aa3f44e83a2`,
scope_test.go `47933e8fc2b3bfbb9b6927d343959b89b837b5497a0c167b645349e8a7a832ed`.

Accepted visual/source slice is committed at `ae7e2da`; accepted audio slice at `325f78a`.

## Pinned-font reconstruction adapter (slice reviewed)

The private `buildFontBinding` consumes an actual owned core inspection and independently authorized
binding, then follows DecodeOutlineFont → ValidateOutlineGeometry → MaterializeOutlineSet →
BuildLayoutGraph → BindShapeProvider. Each edge checks the preceding native receipt; the resulting
text binding retains the original Document hash and font asset ID. No wire receipt substitutes for
live native lineage, and no new encoded-byte parse bypasses the inspected object. Failure, panic and
cancellation return a zero binding.

All fixed initial font-build limits must be positive and no larger than the adapter defaults. Five
phase working/retained maxima plus a second layout reservation account for graph replay during
provider binding: 96 MiB working plus 96 MiB retained under the default profile. These are conservative
logical admission charges, not measured RSS/GC ceilings. Core inspection and input snapshots require
separate factory admission.

Implementer `a60f4bedcfc8e7caf` reported focused normal/race, package vet and full package tests passed.
Independent reviewer `a91a0421285b64f18` found no verified P0/P1/P2 after source/ownership/accounting
inspection and ten normal FontAdapter test repetitions. Its combined search/hash/race command was
refused by the worktree command-shape guard and was not retried or rerouted. Independent race, vet,
full-package tests and fresh post-compaction hashes are therefore **not** claimed for this review.
No empirical RSS or pathological-layout instrumentation is claimed. This review does not accept the
factory's aggregate budget or S3 exit.

Reviewed hashes supplied with the handoff:

- `font.go`: `384de0802ed2f82e5391127c1a686829ea2874b357ba2e7a23a17fe7fde84fa2`
- `font_test.go`: `02c511142c11b242ad8e98bf027b9dcbc912f97523abff15e32f11dfbd3dd566`

## Semantic/native identity bridge (slice independently reviewed)

Independent reviewer `a91a0421285b64f18` found no verified P0/P1/P2 in bridge.go/bridge_test.go.
Frozen bridge tests passed independently ten normal and three race repetitions, including real
nonempty WAV source evidence, gap versus disabled zero-tick domains, original/native identity
separation, deep ownership, concurrent readers, scope/document mismatches and self-hash omission.
The native audio receipt hash is verified in its original representation before explicitly projecting
scope fields to lowercase wire names; the projection has a separate hash. WAV/audio source list
cross-binding relies on the independently accepted exact enabled-closure scope gate, not pruning.

The reviewer's additional checks under `/tmp/s3-bridge-independent-overlay.json` initially could
not compile because preflight was actively edited. After dependency freeze, both normal and race
supplemental runs passed. They observed 985 bridge-build context checkpoints and sampled 32 positions
for cancellation and panic, requiring strictly zero output; every observed canonicalBridge checkpoint
was tested for nil-byte failure. Genuine same-document native audio with a different valid probe build
fingerprint was rejected against the WAV provider. Independent stdlib SHA-256 preimage checks passed
for native receipt, lowercase projection and omitted-self bridge hashes. Independent full-package/vet
acceptance is not claimed by this bridge review. Earlier blocked font/codec work was untouched.
Bridge is unsigned self-integrity evidence, not attestation, pixel/sample causality proof or durable
authority. Factory/preflight and full S3 acceptance remain separate.

Frozen hashes:

- `bridge.go`: `3eee6deff69c5fe44248e68bba9fa44a7a92cc0de282edc2f9e777928e1040c1`
- `bridge_test.go`: `f052598064f5875a05143dc72f5a39d8a285e1010e3b4b86c4478b2be63b1112`

## Native reconstruction integration (implementation under independent review)

The new host-owned Factory checks joint environment identity, supported whole enabled closure,
current pins, explicit native configuration, source-local counts and aggregate logical budgets before
resource callbacks. It then authorizes the persisted environment, independently snapshots and
inspects bytes, builds real font lineage and native visual/WAV/audio programs, and cross-checks every
actual source observation before publishing a Bundle. Bundle exposes persisted-clock frame and PCM
operations and owned identity receipts, never native program/provider handles. A semantic/native
Bridge keeps S2 selections separate from native descriptors, configurations and observed source
manifests; its unsigned fingerprint is self-integrity, not durable authority or execution attestation.

Independent source/guard review by `a3a86eeaed4f2d6fa` found no verified P0/P1/P2 in factory.go,
render.go and render_test.go; scoped Bundle normal/race tests passed. This preliminary review did
not accept preflight, bridge or mixed restart integration. Their separate reviews remain active.
Parent Bundle tests also passed ten repetitions. Existing conformance fixture paths remain unchanged.

The mixed fixture author reports a real same-Document PNG/vector/pinned-font/WAV reconstruction,
nonzero PCM and fresh-process durable-store restart passing, with full reconstruction package
normal/race/vet passing. Final fault-test hardening and independent verification are pending; these
are implementer results, not S3 acceptance. Initial concurrent compilation failures (unfinished helper,
fixture imports/index fields) and fixture failures (nonzero seed, unsorted host capability declarations)
were corrected rather than weakening production validation. Parent full integration task `b9v86ksn3` passed the normal suite, then failed the race build on
an actively edited preflight import (`strings` temporarily unused); vet/tidy were not reached.
These results predate the final admission repairs and are not synchronized acceptance evidence.
Four-target reconstruction task `b17yghma7` passed Linux/amd64 and Linux/386
compile-only checks, then failed compiling an actively edited factory test (`good` temporarily
unused) on the Windows step; wasm was not reached. A synchronized rerun is required.

Independent preflight review identified additional early-admission gaps: compositor-only preflight
missed nested text-native configuration minima, source document measurements differed from the
evaluator snapshot measurement, and nil-binding audio preflight did not enforce Audio.MaxBindings.
Further source review narrowed the document-measurement issue to WAV limits (the visual compositor
already uses the conservative source-style measure), and identified WAV retained-size admission and
document-known vector limits that also need pre-callback checks. The preflight owner is repairing
these with no-callback regressions; independent closure is pending. No previous passing focused test
is treated as evidence that these gaps were absent.

After the first repaired preflight freeze, independent reviewer `a3a86eeaed4f2d6fa` ran the final
mixed/restart/runtime tests in normal and race modes, plus package vet: all passed, with no verified
P0/P1/P2 in that assigned integration slice. Final factory_test.go hash was
`5cfd302c9dc8bc138d9cbff044d526071c886665f3c24c0862e467b93d7c0bbd` before and after execution.
Both runs actually launched the fresh-process restart helper. Third-Open cancellation/unformattable
panic, nil Bundle and same-Factory successful recovery with all resources rebound were executed.
This closes the earlier final-test execution hold, not the separately audited preflight boundary.

Parent task `btlu1i9d3` passed reconstruction-package Linux/amd64, Linux/386, Windows/amd64 and
JS/Wasm compile-only checks. No target-runtime execution is implied. Full synchronized task
`b4qizjhis` passed the normal suite and reconstruction-package race tests, but the full race suite
failed `render/ffmpegbackend.TestM1CancellationDuringDecodedEvidenceIsTerminalAndBounded`: returned
`ffmpeg backend: command failed: start decoded frames evidence: context deadline exceeded` did not
satisfy the expected deadline error classification. Vet/tidy were not reached. This failure is under
separate investigation, not dismissed as a flaky pass.

Independent preflight recheck passed its focused normal/race/vet gates and confirmed the earlier
repairs, but found a remaining P1 early-admission issue: document-known text scalar/per-line scalar
limits and mandatory raster lower bounds could still fail only after resource callbacks. Follow-up
repair includes both inner and outer work-unit lower bounds because outer work caps the child;
inner canonical reservation must not be incorrectly imposed on the outer exact canonical cap.
Final independent preflight closure by `a60f4bedcfc8e7caf` reports P0=0/P1=0/P2=0 on
preflight.go `d713579ea7b510649e5a567d655dadb24c311bef5ab3a6b2d1b3d73279384b77` and
preflight_test.go `bc5a309bc879186a2575f054fb2e54d0a409ca60b9bd83cbdd689859a252f718`.
Focused constructor/preflight normal/race and package vet passed independently. Unicode/LF scalar
boundaries and inner/outer raster N-1 rejection tests assert all three resource callback counters
remain zero. The outer work test executed the N-1 boundary, not literal value 1. These are necessary
pre-read checks, not a promise that later content-dependent font geometry succeeds.

Parent final-repair task `be2xwr3o0` passed full reconstruction normal/race, repository vet,
`go mod tidy -diff` and diff checks. Full repository rerun `bdg21w66b` passed `go test ./... -count=1`,
`go test -race ./... -count=1`, `go vet ./...`, `go mod tidy -diff` and `git diff --check` after the
separate FFmpeg cancellation repair. The earlier full-race failure was traced to converting command.Start's
context cause to text with `%v`; changing that cause to `%w` preserves error classification without
reclassifying real failures on late cancellation. A new deterministic test failed both cancellation
cases before repair. Its initial terminal assertion incorrectly also demanded ErrCommand after the
existing emitter intentionally normalized to ctx.Err(); that test assertion was corrected without
changing emitter behavior. Independent backend review found no remaining correctness findings;
new deterministic tests passed 20 repetitions and cancellation/real-failure race tests passed ten.
Reconstruction integration is saved at `f5564e4`, cancellation repair at `f0722bb`. Full gate results
include preserved deferred archive work in this checkout; they are not clean release or S3 exit
acceptance. No previously blocked codec/font review was retried or rerouted.

The earlier interrupted codec repair review still gates S3 exit. None of these new implementation or
review tasks substitutes for it. Temporal video, MP4/Evidence v2, scheduling/worker execution and the
public SDK/Player lifecycle remain downstream work, not features delivered by this package.

## Current integration gates

After the resource ownership repair and evaluator implementation freeze, task `b26sb68lz` passed
`go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, `go mod tidy -diff` and
`git diff --check`. Task `bsoheo0th` passed Linux/amd64, Linux/386 and Windows/amd64 full-package
compile-only checks plus the documented js/wasm protocol/core subset with the new resource package.
These are current-worktree integration results, including preserved deferred archive changes and the
not-yet-audited evaluator slice, not fixed-clean-revision release or S3 exit evidence. No real 32-bit,
Windows or wasm execution is claimed. The accepted resource slice is committed at `1f33036`.
