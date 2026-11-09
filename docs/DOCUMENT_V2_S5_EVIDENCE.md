# Document v2 S5 assembly evidence

## Status — 2026-11-08

**Request protocol independently reviewed; S5 is not accepted.** Evidence v2 implementation and
focused gates and a limited independent protocol review are complete. Production V2 assembler integration,
crash recovery and acceptance remain in progress. S4 provider acceptance remains unmet.
Additive S6 wiring is also in progress. A controlled local signed-runtime mixed smoke, 100-daemon-
kill recovery campaign and full one-hour soak passed on 2026-11-09; this is technical local
acceptance, not owner-signed release evidence or a replacement for independent S5 review.

Baseline is `5a186be790bf9f70326e9779fd76356dd7fe716c` with uncommitted source additions and preserved
release/archive work. No commit/push/publication/default-backend change was performed.

## Additive input boundary

New files:

- `render/backendstage/assembly_v2.go`
- `render/backendstage/assembly_codec_v2.go`
- `render/backendstage/assembly_v2_test.go`
- `schemas/cutly.chunk-assembly-request.document-v2.v2.schema.json`
- `conformance/assembly_documentv2_test.go`
- `conformance/assembly_nested_documentv2_test.go`
- `conformance/fixtures/document-v2-chunk-assembly-*`

The offline registry adds the new schema without changing existing V1 fixtures.

APIs include `ChunkAssemblyRequestV2`, `ChunkAggregateBindingV2`,
`ChunkAssemblerBackendV2`, `ChunkSourceV2`, `DecodeChunkAssemblyRequestV2`,
`CanonicalChunkAssemblyRequestV2`, `VerifyChunkAssemblyRequestV2`,
`VerifyChunkAssemblyRequestV2AgainstPlan` and `FreezeChunkAssemblyRequestV2`.
`VerifiedChunkAssemblyRequestV2` getters return owned canonical/request values.

The identity checks compare Document/plan/manifest/provider/target/output/runtime/seed before
proportional descriptor work. Provider digest must exactly equal manifest ProviderBuild.
The verifier uses native V2 plan/manifest APIs, not a V2-to-V1 translation. Timeline subsets and
exact clock-derived frame/sample ranges must lie within the plan. V2 freezes 64-frame and
65536-sample partitions, task names, ordered coverage, sizes, unit bytes, unique CAS keys,
intermediate profiles and recomputed ordered decoded roots. Integer overflow and aggregate drift
are rejected. Input admission and strict nested JSON checks precede cloning/serialization.

Descriptor/root aliases reuse only unchanged V1 wire shapes and the existing binary root algorithm;
no V1 request, scheduler, provider or backend implementation is substituted.

## Conformance and verification

Eight valid/canonical/hash fixture sets cover gap/silence, mixed, transition, audio gap,
visual silence, zero-frame, zero-sample and fractional grids. Four published invalid fixtures
and generated nested schema/codec attacks cover unknown/duplicate/null/tampered fields,
coverage/order/roots/layout, re-signed identity mismatch, overflow, limits, cancellation,
panic and owned concurrent getters.

Mixed canonical file hash:
`a52d59770252320c8006763d362d571cd8cb5d081c7060742a6a98a95dd6b3ee`.
These fixtures contain synthetic identity claims, not real media provenance or output evidence.

Implementation agent `a70b1a23e01e408e1` reported passing:

```
go test ./render/backendstage ./render/api ./core/renderplanv2 ./conformance
go test -race ./render/backendstage ./render/api ./core/renderplanv2 ./conformance
go vet ./render/backendstage ./render/api ./core/renderplanv2 ./conformance
git diff --check
```

Final arithmetic/inactive-domain additions were rerun in focused backendstage normal/race and
all listed vet packages. This is implementation testing, not an independent protocol audit.
Parent full Go build/normal and documented compile-only target checks also passed; see S4 ledger
for the separate scanner-test race discovered during the full race run. No long fuzz campaign ran.

## Explicit trust limits and missing work

- A standalone verified request does not prove external plan/document closure; hosts must call
  AgainstPlan, and verify the actual authored Document with renderplanv2.VerifyAgainstDocument.
- Identity checks prove neither chunk bytes nor executable provenance and grant no locator,
  process launch or publication authority.
- Actual CAS reads/leases, bounded raw input, RGBA16 output conversion, pinned H.264/AAC encoding,
  codec restart and publication are being implemented; they have not passed integrated acceptance.
- Evidence v2 schema/codec, eight canonical/hash fixture sets and five invalid fixtures are
  implemented with a neutral full-request binding to avoid an evidence/backendstage import cycle.
  The offline registry includes its fixture. Implementation `go test`, `go test -race` and `go vet`
  passed for `./render/evidence ./conformance`; protocol fixtures are not actual codec provenance.
  Independent Evidence review by `a24b96febbc3d94a0` found no verified P0/P1/P2. Original
  evidence/conformance normal/race/vet passed (`b2z09qhgw`, exit0); a `/tmp` overlay rejected
  1199 re-signed wire attacks over 69 objects, foreign self-consistent chunk graphs against the
  original binding, late cancellation and checked decoded-payload overflow. It confirmed valid
  independent worker roles and final-root repartition parity. This protocol review does not prove
  actual codec bytes. Final decoded roots, logical-pair publication/recovery and the full integrated
  fault matrix are not accepted.
- A transient import cycle from the in-progress encoder prevented fresh conformance and daemon E2E
  runs (`b3tdpkjab`, `b8tdxawx9`, both exit1). It was subsequently resolved with a neutral Evidence
  binding and additive backendstage adapter, without changing the frozen request or V1 adapters.
  New parent normal checks passed for conformance/backendstage/evidence/reconstructv2 (`bgucguh2y`).
  Real legacy daemon publication/recovery and public Go SDK HTTP E2E both executed and passed
  (`b0sdqlnds`, 0.78s and 1.43s). These regressions do not prove native mixed V2 acceptance.
- Independent request-protocol audit completed with no verified P0/P1/P2 in its limited scope.
  Reviewer `a24b96febbc3d94a0` ran normal/race/vet both on the original packages and with a `/tmp`
  overlay: 61 populated objects / 266 fields / 1063 omit/case/null/duplicate attacks, re-signed
  nonfixed audio/video partitions and single-block/aggregate overflow attacks. All were rejected.
  Final original backendstage normal/race took 16.303s / 29.185s; conformance took 4.222s / 39.999s
  (`bgxh2s5wn`, exit0). This excludes the new encoder, Evidence and S6 implementation.
- The assembler owner reports actual H.264/AAC encode/repeat-root tests at 48000, 48001, 1024
  and 65537 samples, with focused assembler race (10.775s) and backend/backendstage vet passing.
  These implementation results are not an independent audit. SIGKILL ownership recovery was found
  incomplete and is being implemented with a durable journal; process-kill cleanup is not accepted.

Protocol implementation is not S5 completion and does not authorize starting a production daemon
V2 path before the missing S4/S5 integration and acceptance gates are satisfied.

## Continuation closure — 2026-11-09

The saved working tree was resumed without restarting the earlier review agents. The confirmed
publication-integrity P1 is closed in source: after the final completion callback, publication now
revalidates both retained path identities, rehashes the open media descriptor and rereads the exact
Evidence sidecar bytes. In-place media or sidecar mutation returns a zero artifact and rolls back
only owned leaves. `TestChunkAssemblerV2PublicationFaultsAndOwnership` covers both successful
mutation callbacks and passed.

Supervisor authority is now carried consistently from the daemon's frozen backend offer into both
assembler construction paths. The actual parent-SIGKILL/supervisor and real encoder package suite
passed in `go test -count=1 ./render/ffmpegbackend` (29.186s). The stable working tree also passed
full repository normal and race tests plus vet; exact commands are recorded in the S6 ledger.

This closes the known callback-mutation defect and the daemon/supervisor capability mismatch. It
is now followed by a successful controlled local signed-runtime mixed-media daemon smoke, 100-fault
campaign and one-hour soak recorded in `DOCUMENT_V2_S6_EVIDENCE.md`. Those runs exercise the
integrated assembler and Evidence v2 path, but the ephemeral local authority is not an approved
release trust root and no new independent S5 audit was performed, so formal S5 acceptance remains
open.
