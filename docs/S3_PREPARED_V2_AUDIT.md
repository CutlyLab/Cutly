# S3 Prepared Environment v2 — independent audit and closure

An independent read-only auditor (separate agent context, no authorship stake) reviewed the
complete S3 implementation line-by-line against roadmap section 8: `render/prepared`
`model_v2.go`, `codec_v2.go`, `store_v2.go`, `rebuild.go`, the v1 boundary diffs, the test
suite and the pinned canonical fixture.

## Verdict at intake

P0=1 P1=2 P2=5. The P0 was a real, input-reachable defect; all P0/P1 findings and four of five
P2 findings were fixed and gated before this closure. The remaining P2 is a documented design
note, not a defect.

## Findings and dispositions

| # | Severity | Finding | Disposition |
| --- | --- | --- | --- |
| 1 | P0 | v2 global lock paths hashed into the shared digest gate buckets; nested entry→global→global gate acquisition could self-deadlock on bucket collision (deterministic for some roots, ~1/256 otherwise) | Fixed: dedicated gate buckets 258/259 for `entries-v2-global`/`scratch-v2-global`; digest buckets are now entry-locks only |
| 2 | P1 | Rebuild factory compared descriptor and source-manifest identities but never the configuration identity | Fixed: `Visual.Configuration` is now defined as the canonical `referencevisual.Config` serialization and the factory requires `ConfigurationFingerprint` to pin the rebuilt program's configuration; substitution tests extended |
| 3 | P1 | Hostile paths roadmap 8.4 demands were untested for v2 (fault injection, cross-process put, no-replace late target, lock-wait cancellation, on-disk oversize, missing marker, put-side limits) | Fixed: `store_v2_faults_test.go` ports the full v1 hostile-path suite to the v2 API and exposed a real gap — see finding 9 |
| 4 | P2 | Component canonicalization ran before any v2-limit admission | Fixed: `jsonadmission.Check` pre-admission bounds the document before the first canonical allocation |
| 5 | P2 | DescriptorV2 branch was a dead end (Compile rejects TextConfig) | Fixed: text-capable configurations now fail closed explicitly, pointing at the S4 font-authorization boundary |
| 6 | P2 | Factory did not re-verify Plan↔Document binding for hand-built environments | Fixed: `VerifyAgainstDocument` at factory entry |
| 7 | P2 | Dead exported type `EnvironmentV2Identity` | Removed |
| 8 | P2 | v1+v2 namespaces each admit the full entry/scratch budget (2x combined worst case) | Documented in `Options.MaxRecordBytesV2` docs as a per-namespace budget; not a bypass |
| 9 | P2 (found by the new fault tests) | A context canceled between the scratch sync and the durable rename still published | Fixed: `publishV2` re-checks the context immediately before the no-replace rename — the point of no return |

## Verified sound (auditor's list, unchanged by the fixes)

Six-step reverification on Put and Resolve; canonical admission including encoding-drift
rejection; v1/v2 profile disjointness both directions; path/TOCTOU/symlink hardening
(`O_NOFOLLOW`, no-replace rename, Lstat entry checks); ownership isolation both directions;
read-side stat admission; the pinned canonical fixture hash was independently recomputed and
matches; v1 paths, bytes, record encoding and tests remain byte-identical.

## Closure state

P0=0 P1=0 P2=1(open, documented as design note #8). The full `render/prepared` suite
(v1 + v2 + hostile additions) and the repo-wide regression pass on the closure commit.
