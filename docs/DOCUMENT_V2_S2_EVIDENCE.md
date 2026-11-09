# Document v2 S2 evidence

## Scope and prerequisite

- Execution date: 2026-11-07.
- Standalone Go implementation began at the recorded S1 baseline; final S2 repair validation is
  preserved in this ledger. Consumer-application integration remains out of scope.
- S1 implementation checkpoint: `5e64a65`; stage-exit evidence committed in `99fccaf`.
- S1 final full Go gates, documented cross-target subset and independent P0/P1/P2 closure are
  recorded in `DOCUMENT_V2_S1_EVIDENCE.md`.
- S2 is complete at repair checkpoint `9d9f96d`: final full gates passed and independent
  read-only review closed with P0=0, P1=0, P2=0. This does not attest broad M4 readiness.

## Required boundary

S2 adds versioned ExecutionManifest/RenderRequest contracts without changing legacy manifest,
request, job, output or evidence semantics. It must bind the complete S1 resource closure,
program configuration/descriptor/source identities, backend capability support, host-owned builds
and media runtime, exact clocks and the distinct intermediate RGBA16/final decoded RGBA8 profiles.

Standalone structural verification is not authorization. Host execution must separately compare
trusted executable identities and authorized resource bindings before any resource use. Persisted
records cannot grant themselves authority merely by carrying a valid recomputed fingerprint.

Nonempty frame/sample ranges require explicit visual/audio programs, including transparent gap and
silence selections when no authored source is present. Disabled selections have no identity/config
fields; enabled selections have all of them. URI-only S1 resources require materialized content
identity at the execution boundary, not an invented semantic-plan hash.

## Baseline checks

Before S2 implementation, this command passed (task `b0cx8ny83`, exit 0):

```sh
go test ./render/api ./render/prepared ./internal/architecture ./conformance -count=1
git diff --check
```

This is a legacy/S1 compatibility baseline, not S2 acceptance evidence.

## Implementation checkpoints

- `5bb222d`: host-owned provider executable identity policy and measurement. Focused
  `go test ./internal/buildidentity -count=1`, race and vet passed; implementer also repeated
  Provider tests 20 times. Test binaries cross-compiled for linux/386, windows/amd64, js/wasm
  and wasip1/wasm; no runtime execution on those targets is inferred.
- Native measurement supports Linux (descriptor-relative no-follow selection, procfs descriptor
  reopen) and macOS (kernel no-follow-any selection, metadata-only open, descriptor reopen).
  Other platforms reject this new host feature explicitly. Portable codecs and legacy runtime
  remain separate; unavailable `/proc/self/fd` or `/dev/fd` also fails closed. No executable test
  fixture was launched. Measurement alone does not authorize later launch of a mutable path.
- `render/preparev2` host integration tests/race/vet passed during implementation (tasks
  `bx9391ch6`, `bierbo208`), including independently re-fingerprinted unauthorized host builds,
  resource observation mismatches, callback panic/cancellation and immutable concurrent reads.
  These are interim checks, not final S2 exit evidence.
- One interim integration compile encountered `codec_v2.go:144:6: declared and not used: domain`
  while the API file was actively edited. Subsequent host test/race/vet runs passed after the edit.
- Independent early review found a P2: standalone manifest verification accepted inconsistent
  embedded source ranges/domain bindings or omitted resources after recomputing hashes, although
  verification against the Plan rejected them. The implementation repair adds source requested
  ranges and reuses S1 verification on enabled source domains (jointly when both are enabled),
  preserving legitimate authored resource closure in zero-tick disabled domains. Regression tests
  pass. The resumed independent review rechecked the original defect: re-signed source/domain,
  range, ordering, cross-domain, transition and resource-omission mutations reject; authored
  zero-frame and zero-sample fixtures retain their three resources and pass. S3 remains gated
  until final review and post-repair gates close.
- `8cb6172`: strict manifest/request identity contracts, program/source closures, schemas and
  fixed conformance corpus. There are eight canonical cases per manifest/request and 35 hostile
  records, with authored zero-tick resources and nonzero clock origins explicitly covered.
- `66a0b0b`: host request authorization, original Document reverification, immutable host pins
  and resource-observation comparisons. No process launch, production resource inspector or
  media publication is added.
- The independent review subagent later terminated with a service API security-filter error before
  final closure. Its earlier finding is retained, but no final review or provider-policy audit
  completion is claimed. No retry through another reviewer is used to bypass that block.
- Full integration task `b3ac21tqj` exited 1 while S2 schemas/fixtures were being updated for the
  newly required source `requested_range`: conformance reported builder drift, schema rejection
  and not-yet-written hostile fixture paths. Other packages passed in that run. Rerun on the stable
  synchronized candidate; this interim run is not stage-exit evidence.

Static inspection fingerprints are opaque to portable S2 verification and interpreted by pinned
host/provider policy. Concrete canonical receipt projections must be frozen before S3/S4 portable
reconstruction and real inspector interoperability. Host resource callbacks are an injection
boundary, not claimed production media inspectors. Output codec strings bind identities, not
encoder availability; unsupported output tuples require S4/S5 preflight rejection.

## Stable implementation gates

After synchronizing schemas and fixtures, task `bswmds3u9` exited 0:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go mod tidy -diff
git diff --check
```

Task `b90rmt15a` exited 0:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go test -exec=true ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=js GOARCH=wasm go test -exec=true ./core/... ./protocol/... ./internal/wirejson ./internal/buildidentity ./render/api ./render/preparev2 ./conformance
```

The wasm subset preserves S1's documented exclusion of unchanged platform-only FFmpeg RSS tests
and includes every new S2 package. Cross-compilation is not native runtime evidence. Tests run on
Go 1.27.3 / macOS arm64; no Go 1.25-toolchain execution or Linux/Windows real execution is claimed.

Focused combined tests/race/vet passed in `blq0cif1p`; final synchronized conformance/API/host tests
passed in `bkhlnyeea`. Legacy fixture comparison against `f9f891a` shows only added files, no legacy
modifications or deletions. The schema test documents jsonschema-go float64 rounding at adjacent
int64 bounds; exact integer overflow rejection belongs to the strict codec and is tested.

Commit `0951ad3` adds external manifest/request codec fuzz properties. Task `b4otr8epl` exited 0:

```sh
go test ./render/api -run '^$' -fuzz '^FuzzDecodeCanonicalManifestV2$' -fuzztime=15s -parallel=2
go test ./render/api -run '^$' -fuzz '^FuzzDecodeCanonicalRequestV2$' -fuzztime=15s -parallel=2
```

The runs completed 21 and 17 executions respectively (13 corpus seeds each). These low counts are
bounded smoke evidence only, not broad fuzz coverage or a substitute for the deterministic hostile
corpus. The added harness changes no production implementation.

These earlier implementation gates did not substitute for independent stage review. Final authorized
review closure and post-repair results follow.

## Authorized review repairs (2026-11-07)

The resumed independent reviewer identified two additional P2 defects:

1. An explicit empty `media_runtime.text_shaper_build` passed canonical syntax and typed
   verification but disappeared during typed reencoding. Both manifest and request decoders now
   require byte equality with the fully validated typed canonical encoding. Regression tests cover
   rejection with zero return and byte-identical valid round trips for both envelopes.
2. The direct provider executable measurement APIs propagated a panic from caller `Context.Err()`.
   The shared measurement entry now recovers without formatting the panic value, clears the
   identity and wraps `ErrProviderExecutableInvalid`. Tests exercise development and packaged
   policies at entry and during file reads; executable fixtures are measured, never launched.

Post-repair focused normal/race/vet passed in `boi1va1tr`:

```sh
go test ./render/api ./internal/buildidentity ./render/preparev2 ./conformance -count=1
go test -race ./render/api ./internal/buildidentity ./render/preparev2 ./conformance -count=1
go vet ./render/api ./internal/buildidentity ./render/preparev2 ./conformance
```

An initial regression-test run (`btkr0vxel`) failed because the new test used the wrong request
member name (`manifest` instead of `execution_manifest`). The test was corrected before the passing
run; this was not a production decoder failure.

Post-repair target compilation task `bqr793w16` exited 0 using the same Linux amd64/386, Windows
amd64 full-package and js/wasm subset commands listed above. These remain compile-only checks.
Final full repository task `b60jle4uk` exited 0: `go test ./... -count=1`,
`go test -race ./... -count=1`, `go vet ./...`, `go mod tidy -diff`, and `git diff --check`.
The candidate included retained deferred archive changes; passing their tests does not independently
accept that separate work. Legacy fixture comparison against `f9f891a` again showed no modified,
deleted or renamed fixtures.

### Final independent closure

Reviewer `aeb585200b210b45b` completed the original read-only scope with **P0=0, P1=0, P2=0**.
The assessment inspected production contracts, host authority, all provider measurement platform
paths, S1 structural reuse, strict wire decoding, schemas and conformance requirements. The original
source-structure finding and both new findings above were independently reproduced/rechecked;
no concrete S2 requirement remains missing within this scope.

The reviewer audited the repairs atop `4a7bae3`; they are committed unchanged in `9d9f96d`.
Parent SHA-256 comparison matched all four final audited files:

| File | SHA-256 |
| --- | --- |
| `render/api/codec_v2.go` | `a9633a95c8cd20d2144b0ad894dff2711f77ad1fce55a05a5369696a658e37d3` |
| `render/api/codec_alias_v2_test.go` | `6c7226cf4f0ce44a654d2da05a377a532551d0b39f4098393d508df735fa877c` |
| `internal/buildidentity/provider_policy_measure.go` | `d8742d4aa96cb1684713288beaf03d60bea328515a948213f348ff93b5fd1e4c` |
| `internal/buildidentity/provider_policy_panic_test.go` | `ff8919fd720f58b8c05d3cd14763b018b16047af1a816104b84e3e77a4480690` |

Independent final commands passed:

```sh
go test -overlay=/tmp/cutly-s2-independent-closure-overlay.json ./render/api ./internal/buildidentity -run '^TestIndependentS2' -count=1 -v
go test -race -overlay=/tmp/cutly-s2-independent-closure-overlay.json ./render/api ./render/preparev2 ./internal/buildidentity ./conformance ./core/renderplanv2 -count=1
go vet ./render/api ./render/preparev2 ./internal/buildidentity ./conformance ./core/renderplanv2
```

The temporary overlay was independently authored and checks the exact previously accepted alias,
five published fixture variants, valid round trips, original source consistency and both provider
policies at entry/after-first-buffer panic boundaries. Final race/vet task `brmwzeskx` and focused
normal/four-target compilation task `bn5r112jd` exited 0. The reviewer performed no repository edits.
Its optional review skill failed on absent `origin/HEAD` before direct inspection; this was a git-ref
error, not a renewed service refusal.

S3 may now begin. Concrete receipt projections, authorized reconstruction, real providers, encoder
availability, launch-byte binding, public SDK and mixed-media acceptance remain downstream work.

## Acceptance ledger

| Gate | Status | Evidence |
| --- | --- | --- |
| S1 prerequisite | Passed | `DOCUMENT_V2_S1_EVIDENCE.md`, `99fccaf` |
| Additive concrete types and strict codecs | Passed; independently closed | `8cb6172`, alias repair `9d9f96d` |
| Plan/resource/program/output/job cross-binding | Passed; independently closed | Original P2 rechecked with re-signed hostile cases and valid zero grids |
| Host-owned authorization boundary | Passed; independently closed | `5bb222d`, `66a0b0b`, panic repair `9d9f96d` |
| Schemas and fixed canonical fixtures | Passed | Two unique schemas, 16 valid/canonical cases, 35 hostile records |
| Focused tests, race and vet | Passed | Combined package commands and conformance checks above |
| Full Go and target compilation gates | Passed with documented wasm subset | Commands and actual results above |
| Legacy fixture preservation | Passed | No modified/deleted legacy fixture relative to `f9f891a` |
| Independent read-only audit | Passed: P0=0, P1=0, P2=0 | Authorized resumed review and independent repair closure above; S3 unblocked |
| Consumer-application acceptance | Out of scope | Not part of the standalone SDK stage boundary |

## Priority and independent-review handoff

The owner has prioritized S2 review closure over further archive/release-tool expansion. Existing
archive work is retained but its all-target acceptance and independent review are deferred. This
handoff is a checklist of missing evidence, not a replacement review or permission to bypass the
service restriction recorded above.

The outstanding independent review must cover:

1. Reproduce and recheck the original P2 described above against the repaired source-range/domain/
   resource-closure implementation at `8cb6172`. Record the concrete cases and results, including
   legitimate zero-tick disabled-domain resource closure; a hash-only check is insufficient.
2. Review the host-owned provider policy at `5bb222d` and host authorization integration at
   `66a0b0b`. Distinguish structural verification from authorization and local executable
   measurement from permission to launch a mutable executable path.
3. Complete the requirements assessment for the additive manifest/request contracts: exact
   Document/Plan bindings, enabled/disabled program identities, resource closure, clock ranges,
   intermediate/output formats, strict codecs, immutable ownership and failure-zero behavior.
4. Record every verified finding and its repair/retest disposition. Re-run relevant gates against
   the final candidate; retain the actual revision, commands, results and platform limitations.
5. Explicitly state whether the independent stage review is complete with no outstanding
   P0/P1/P2. Only then update the acceptance ledger and permit S3 to begin.

The service-side review interruption cannot be resolved by a new test pass, this checklist, or
renaming/rephrasing the task to evade restrictions. On 2026-11-07, the permission system explicitly
required owner confirmation that the local defensive-review bypass flag was a false positive. The
owner selected “确认误报并授权重试” in that confirmation, authorizing the original read-only review
scope without permission changes or evasion of further restrictions. The subsequent independent
review launch was allowed and subsequently completed as recorded above. Authorization itself was
not acceptance evidence: the independent findings closure and post-repair gates establish S2 exit.
S3 is unblocked; S4–S6 still depend on their upstream stages. Consumer integration remains outside
this stage, and publishing/default-switch authorization remains separate.
