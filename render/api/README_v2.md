# Document v2 execution identities

This additive S2 boundary leaves legacy manifest/request/job bytes unchanged. It is
filesystem-free and launches no executables. It defines identity contracts, not an
installed renderer, actual execution receipts, locator authority or backend proof.

## Public API

- `FreezeExecutionManifestV2(ctx, plan, draft)` returns a concrete manifest. It
  verifies the plan, owns copies, sorts resource/capability sets without deduplication,
  fills the v2 envelope/plan identities and deterministic program selections, then
  jointly verifies everything. Supplied nonempty selections must already equal the
  exact derived selection. A builder can create identity, never authority.
- `CanonicalExecutionManifestV2(manifest)` emits only an already valid manifest;
  `DecodeExecutionManifestV2(raw)` requires its exact canonical bytes.
- `VerifyExecutionManifestV2(manifest)` returns an owned
  `VerifiedExecutionManifestV2`. `VerifyExecutionManifestV2AgainstPlan(ctx, manifest,
  plan)` additionally checks exact resource/target/clock/capability/program closure.
- `CanonicalRenderRequestV2(request)`, `DecodeRenderRequestV2(raw)` and
  `VerifyRenderRequestV2(request)` independently verify the included S1 plan,
  manifest and existing RenderJob. `VerifyRenderRequestV2AgainstPlan(ctx, request,
  independentlySuppliedPlan)` additionally requires that independent plan identity.
- Manifest handles expose `Valid`, `Canonical`, `Manifest`, `Fingerprint`. Request
  handles expose `Valid`, `Canonical`, `Request`. Zero handles are invalid; byte and
  struct getters return fresh copies and support concurrent readers.
- `RenderBackendV2` receives verified handles and Document v2 capability kinds. It
  does not replace the host authorization boundary.

Inputs must not be mutated concurrently during a call. Errors, canceled contexts
and recovered context panics in context-taking APIs return zero results. Nil contexts
are invalid. Independent plan verification does not prove original Document closure;
that remains `renderplanv2.VerifyAgainstDocument` at the host boundary.

## Wire contract

Schema versions are uint32 `2`, profiles are
`cutly.execution-manifest.document-v2.v2` and
`cutly.render-request.document-v2.v2`; both explicitly name
`cutly.json.canonical-integer.v1`. The request contains `job`, `plan` and
`execution_manifest`, with no extra request fingerprint. Manifest fingerprints hash
canonical members excluding `fingerprint` entirely. Exact-case unknown/duplicate/
missing/null members, noncanonical bytes, fractional/exponent numbers, invalid
Unicode and overflow fail closed. Collections are required, non-null arrays.

New dimensions/channels are uint32, additionally bounded to 2147483647 like S1's
reused RenderTarget ints. Output carries both exact composition clocks and an
explicit sample rate equal to its sample clock. Intermediate video uses canonical
linear premultiplied RGBA16, while final decoded evidence remains RGBA8. Audio
blocks/final decoded evidence use the existing PCM16LE layout identity without an
api-to-pcm16le import cycle. Initial deterministic seed is exactly zero. Nonblank container/codec names identify
choices rather than assert encoder support; later backend preflight must reject
unsupported output tuples, never silently fall back. Host authorization likewise
must not be interpreted as proof a renderer supports these choices.

## Materialized resources and host policy

Closure must equal S1 resources exactly. A URI-only empty S1 content hash may become
a nonempty materialized SHA-256 digest; a pinned S1 hash cannot change. Sizes are
positive. Inline text/vector sources are never materialized entries; pinned fonts
are indirect resources. Data and any unversioned materialization kind are rejected.

- Video/audio: `AVMaterializationProfileV2`, required `probe_fingerprint`.
- Image: `ImageMaterializationProfileV2`, required `inspection_fingerprint`.
- Font: `FontMaterializationProfileV2`, required `inspection_fingerprint`.

These fields are pointers to preserve presence. The counterpart must be absent,
not null or an empty string. Profile constants are identity categories, not a
claim that a particular probe/inspection implementation ran. Fingerprints are
host-policy receipt identities: this package intentionally does not invent a
projection from unrelated existing receipt formats. A trusted host policy must
rehash/inspect the same retained or snapshot bytes, bind the appropriate receipt
identity, and compare the observed resource record with the frozen claim.

Legacy locator validation is reused lexically: CAS locators equal their content
hash; file locators are absolute clean paths without NULs. This cannot detect
symlinks, authorize roots, attest bytes, or secure a time-of-check/time-of-use gap.
Only a separate host authority can do that. Compiler/evaluator/backend/runtime and
provider executable digests are identities here, not proof they were host measured.
RenderJob publication URI syntax is likewise not publication permission.

## Program identities

Distinct `VisualProgramProfileV2` and `AudioProgramProfileV2` describe plan-scoped
source identities, not old whole-document referencevisual support for mixed video.
`TransparentGapProgramProfileV2` and `SilenceProgramProfileV2` explicitly represent
nonempty tick ranges with no authored contribution. Active transition participants
count as visual contribution even when ordinary visual clips are absent. A domain
is disabled only when that plan's corresponding tick range is zero; disabled
selections encode exactly `{"enabled":false}`.

Every enabled selection includes the concrete frozen descriptor from
`ProgramDescriptorForV2`, its recomputed hash, strictly decoded typed visual/audio
configuration with recomputed hash, and a concrete `ProgramSourceManifestV2` plus
recomputed hash. Configuration bytes are checked as bounded canonical objects
**before** outer JSON marshaling, so RawMessage whitespace or unknown members
cannot be laundered by a builder or verifier. The concrete configuration is
recomputed from output/plan dimensions, clocks and tick ranges.

The source record binds DocumentHash, plan fingerprint, domain, program profile,
descriptor/config fingerprints, requested range, tick range, ordered complete PlannedClips and
PlannedTransitions (including their participants/handles), audio bus closure, and
sorted exact domain materialized resources identical to the top-level records.
The source record has no self fingerprint and no execution-manifest fingerprint,
so its selection-level hash introduces no cycle. Against-plan verification requires
exact equality with recomputed records. Standalone verification checks internal
identity/config/resource consistency by reusing S1 structural verification on each
enabled domain. It checks local projections, ordered bindings, transition handles,
missing resource references and minimum capabilities. Enabled domains must agree
on the requested range and its tick grids; a disabled counterpart must have zero
ticks when another enabled source supplies that range. Both-disabled standalone
manifests have no source range to inspect and require against-plan verification.
Zero-tick domains can retain authored clips and top-level resources without enabled
source records. Standalone verification cannot infer omitted authored records from
a document or plan hash. This source identity record is not an implementation,
worker receipt, rendered-output evidence or execution authorization.

## Admission

Manifest/request wire limits are 16/32 MiB, config limit 16 KiB. S1 limits are reused
for visited nodes (200000), depth (64), collection size (16384), individual strings
(1 MiB), and integer digits (20). A bounded reflection scan precedes JSON allocation
for typed entry points; strings/raw bytes are conservatively charged six encoded
bytes per byte. Raw codecs bound input/depth/values before strict shape decoding.
No validation repairs malformed collections, stale hashes or union fields.
