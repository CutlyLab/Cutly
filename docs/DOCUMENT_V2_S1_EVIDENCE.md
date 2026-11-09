# Document v2 S1 evidence

## Scope and baseline

- Execution date: 2026-11-07.
- Historical implementation baseline: commit `f9f891a`.
- Language: existing Go implementation; Rust migration deferred by the owner.
- Stage: S1 semantic render plan complete at `5e64a65`, with final gates and audit closure below.
  S2 implementation has not started at this stage-exit checkpoint.
- Original handoff `0a19a82` is absent from local history. Local surface-bridge commit is
  `cabbfbb`; equivalence and historical audit results are not newly attested here.
- Consumer-application integration is outside the standalone Cutly release boundary. No external
  consumer result is claimed by this stage ledger.

## Baseline checks

Before registering the new S1 schema, the following command passed on the working tree
(exit 0; 2026-11-07):

```sh
go test ./core/videoedit ./core/documentv2 ./core/evaluator ./protocol/canonicaljson ./internal/wirejson ./conformance ./render/referencevisual ./render/referencevisualchunk -count=1
```

This establishes current semantic/protocol/surface-bridge regression results, not S1 completion
or a substitute for the historical S0 audit. The new plan package was not included in this command.

## Independently committed boundaries

- `3d23b0d`: standalone execution scope and baseline ledger (documentation only).
- `74edd28`: additive selected-capability derivation and tests. `go test ./core/documentv2`,
  `go test -race ./core/documentv2`, and `go vet ./core/documentv2` passed. Independent read-only
  audit of the two capability-selection files found no P0/P1/P2; its checks included
  `go test ./core/documentv2 ./render/audiograph -count=1` and
  `go test -race ./core/documentv2 -run '^TestRequiredCapabilitiesForSelection' -count=1`.
  This audit does not cover the complete S1 plan boundary.

## Interim integration runs (not stage-exit evidence)

- `go test ./... -count=1`: passed while S1 was being implemented; the new plan package had no
  tests at that snapshot. Must rerun on the stable stage candidate.
- `go test -race ./... -count=1`: passed during implementation; rerun after final changes.
- `go vet ./...`, `go mod tidy -diff`, `git diff --check`: produced no diagnostics during
  implementation; rerun on the stable candidate.
- First four-target build attempt stopped at linux/amd64: `verify.go:143:2: declared and not
  used: trackIndices`, observed while that file was actively being edited. Remaining targets
  were not attempted by that loop. This is not recorded as a successful platform gate.

## Acceptance ledger

| Gate | Status | Evidence |
| --- | --- | --- |
| Baseline branch and clean worktree | Checked before implementation | `git status --short`, `git branch --show-current`, `git log -6 --oneline` |
| S1 types, codecs and resolver | Complete | `9db5e5f`, audited fixes `5e64a65`; additive `core/renderplanv2`, no legacy protocol replacements |
| Schema and canonical fixtures | Passed | Unique schema, inventory entry, gap/mixed/transition vectors and 13 hostile records; complete invalid corpus checked by conformance |
| Focused tests / race / vet | Passed after fixes | Implementer and independent reviewer runs |
| Full Go suite / race / vet / tidy | Passed after fixes | Final command chain exited 0; details below |
| Four-target cross-compilation | Passed after fixes with documented wasm subset | Three full targets; wasm core/protocol/conformance; exclusion below |
| Legacy fixture preservation | Passed after fixes | No modified/deleted legacy fixture paths relative to `f9f891a`; only new prefixed vectors |
| Independent read-only audit | Closed: no remaining verified P0/P1/P2 | Both P2 findings reproduced, repaired and independently rechecked; details below |
| Consumer-application acceptance | Out of scope | Not part of the standalone SDK stage boundary |

## Implementation checkpoint `9db5e5f`

The stable implementation passed these commands on 2026-11-07:

```sh
go test ./core/renderplanv2 ./core/documentv2 ./core/evaluator ./conformance -count=1
go test -race ./core/renderplanv2 ./core/documentv2 ./core/evaluator ./conformance -count=1
go vet ./core/renderplanv2 ./core/documentv2 ./core/evaluator ./conformance
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go mod tidy -diff
git diff --check
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go test -exec=true ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=js GOARCH=wasm go test -exec=true ./core/... ./protocol/... ./internal/wirejson ./conformance
```

The full wasm command was attempted and failed only in the unchanged
`render/ffmpegbackend/m1_memory_acceptance_test.go`: `syscall.Getrusage`,
`syscall.RUSAGE_SELF` and `syscall.Rusage.Maxrss` are unavailable on wasm. This test has the
existing `!windows` build constraint. The roadmap permits a documented platform/process subset;
all pure core/protocol/conformance packages passed wasm. No runtime support or real-platform
execution is inferred from `-exec=true` cross-compilation.

## Final audited checkpoint `5e64a65`

The independent read-only review reported two P2 findings against the implementation checkpoint:

1. A recomputed fingerprint could conceal missing capability minima inferable from the Plan
   itself (explicit audio routing and temporal retime).
2. Distinct same-track clip bindings could claim overlapping or inverted authored intervals;
   consecutive transition windows also needed a full-window overlap check.

Commit `5e64a65` closes these with structural verification and regression tests. It also verifies
text/vector transition source and scope capabilities, covers nested bus/master limiter closure,
checks deterministic cancellation at every observed checkpoint, adds defensive document-verification
panic recovery, and runs every published hostile fixture through the strict codec.

The reviewer independently reran the original route-omission, 2x audio-retime-omission and same-track
overlap reproductions with recomputed fingerprints: both Verify and Decode reject them, and Decode
returns a zero Plan. The reviewer checked overlapping/touching transition windows, source/scope
minima, chronology, ownership/admission/identity boundaries and the external fuzz harness. Final
review conclusion: both P2 findings closed; no remaining verified P0/P1/P2. Schema codepoint versus
core byte-based ID length validation remains a nonblocking note: the core codec is authoritative.
Independent after-fix package/conformance tests, vet, race, focused overlap regression and a 5-second
fuzz run (67,363 executions) passed. Full-tree gates below are coordinator results, not independently
claimed reviewer results.

Final post-fix command chain (task `b1cl8lj7q`) exited 0:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go mod tidy -diff
git diff --check
```

Final cross-target chain (task `btlk09vwq`) exited 0, using the same documented wasm exclusion:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go test -exec=true ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=js GOARCH=wasm go test -exec=true ./core/... ./protocol/... ./internal/wirejson ./conformance
```

Additional bounded fuzz run passed (378,289 executions; task `b7s92xwp5`):

```sh
go test ./core/renderplanv2 -run '^$' -fuzz '^FuzzDecodeCanonicalPlan$' -fuzztime=15s -parallel=2
```

`git diff --name-status f9f891a -- conformance/fixtures` contains only added
`document-v2-render-plan-*` files. No legacy fixture was regenerated or modified.

## Existing TypeScript compatibility check

`npm --prefix sdk/typescript test` passed all 49 tests (Node v24.21.0, npm 11.19.0).
The roadmap spelling `npm --prefix sdk/typescript pack --dry-run` failed with ENOENT because this
npm version searched the repository root for package.json. The explicit-directory equivalent
`npm pack ./sdk/typescript --dry-run` passed, listing the existing 12-file package. No package was
published and no TypeScript source was changed; these are legacy compatibility checks, not v2
TypeScript SDK implementation evidence.

S1 completion is not M4 completion. Production rendering, restart reconstruction, public v2 edit
and render APIs, Player integration, and the mixed-media release gate remain later stages.
