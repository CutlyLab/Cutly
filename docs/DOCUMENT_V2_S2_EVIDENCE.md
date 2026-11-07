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

## Acceptance ledger

| Gate | Status | Evidence |
| --- | --- | --- |
| S1 prerequisite | Passed | `DOCUMENT_V2_S1_EVIDENCE.md`, `99fccaf` |
| Additive concrete types and strict codecs | Pending | No S2 contract accepted yet |
| Plan/resource/program/output/job cross-binding | Pending | Implementation and hostile tests required |
| Host-owned authorization boundary | Pending | Identity syntax alone is insufficient |
| Schemas and fixed canonical fixtures | Pending | Unique v2 names; legacy vectors remain frozen |
| Focused tests, race, vet and fuzz | Pending | Run on stable implementation |
| Full Go and target compilation gates | Pending | Record failures/exclusions explicitly |
| Legacy fixture preservation | Pending | Compare against standalone baseline `f9f891a` |
| Independent read-only audit | Pending | Close P0/P1/P2 before S3 |
| Manyfy consumer acceptance | Deferred by owner | Not part of this execution scope |
