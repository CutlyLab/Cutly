# Document v2 S2 evidence

## Scope and prerequisite

- Execution date: 2026-11-07.
- Standalone Go implementation on `codex/video-sdk-core`; Manyfy remains out of scope.
- S1 implementation checkpoint: `5e64a65`; stage-exit evidence committed in `99fccaf`.
- S1 final full Go gates, documented cross-target subset and independent P0/P1/P2 closure are
  recorded in `DOCUMENT_V2_S1_EVIDENCE.md`.
- S2 is in progress. This document does not attest S2 completion or broad M4 readiness.

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
  pass; independent recheck remains incomplete. S3 remains gated.
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

These implementation gates do not substitute for the blocked independent stage audit.

## Acceptance ledger

| Gate | Status | Evidence |
| --- | --- | --- |
| S1 prerequisite | Passed | `DOCUMENT_V2_S1_EVIDENCE.md`, `99fccaf` |
| Additive concrete types and strict codecs | Implemented; independent closure pending | `8cb6172` |
| Plan/resource/program/output/job cross-binding | Implemented with regression tests | Earlier review finding repaired; independent recheck blocked |
| Host-owned authorization boundary | Implemented; audit pending | `5bb222d`, `66a0b0b` |
| Schemas and fixed canonical fixtures | Passed | Two unique schemas, 16 valid/canonical cases, 35 hostile records |
| Focused tests, race and vet | Passed | Combined package commands and conformance checks above |
| Full Go and target compilation gates | Passed with documented wasm subset | Commands and actual results above |
| Legacy fixture preservation | Passed | No modified/deleted legacy fixture relative to `f9f891a` |
| Independent read-only audit | Blocked before final closure | Reviewer API termination; earlier P2 implementation repair awaits independent recheck; do not enter S3 |
| Manyfy consumer acceptance | Deferred by owner | Not part of this execution scope |
