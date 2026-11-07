# Document v2 semantic render plans

This additive S1 package is not the legacy RenderPlan v3 codec, an execution manifest,
a prepared-artifact store or a renderer. It performs no resource I/O or authorization.

## API and trust boundary

- `Resolve(ctx, document, requestedRange, target)` validates the complete Document,
  selects exactly one explicit/unique timeline and resolves the requested closure.
  Errors, cancellation and recovered resolver panics return a zero Plan plus diagnostics.
- `Canonical(plan)` validates and emits canonical bytes. It does not repair ordering,
  null collections or a stale fingerprint.
- `Decode(raw)` accepts only the exact canonical integer JSON spelling, without
  whitespace, trailing newline, duplicate names, unknown names, case aliases, nulls,
  omitted required fields, fractional numbers or invalid Unicode.
- `Verify(plan)` checks what the plan itself can prove: structure, identities, sorted
  sets, authored-position consistency, visible/source projections, transition windows
  and handle spans, direct/indirect resource references, clocks and fingerprint. It
  returns an opaque owned `VerifiedPlan`; the zero handle is invalid. `Plan()` and
  `Canonical()` return fresh owned data and permit concurrent readers. Caller inputs
  must not be concurrently mutated during any API call.
- `VerifyAgainstDocument(ctx, plan, document)` re-resolves and requires complete
  canonical equality. Only this operation proves that DocumentHash is truthful and
  no authored clip, transition, property capability or bus ancestor was omitted or
  invented. Standalone verification cannot infer document closure from a hash.

No verification operation proves a backend supports the requirements, a URI has been
materialized, a content digest matches actual bytes, or an execution is authorized.

## Wire and identity contract

Schema version is uint32 `1`; profile is
`cutly.semantic.document-v2-render-plan.v1`; canonical JSON profile is
`cutly.json.canonical-integer.v1`. All fields have explicit JSON names in `model.go`.
Collections, including nested capability sets and handle arrays, are mandatory,
non-null arrays; empty means `[]`. Empty `font_asset_id`/`audio_bus_id` explicitly
means no such binding. No arbitrary property/metadata maps are embedded in plans.

`document_hash` uses `documentv2.DocumentHash` on the **original full Document**.
That historical canonical profile is intentionally distinct from the integer-only
plan profile: non-integer metadata is not reinterpreted or discarded. The plan
fingerprint is SHA-256 over all canonical plan members **excluding the fingerprint
member entirely**, not including it as an empty string. No legacy bytes change.

New track/clip/transition indices are uint32. Revision retains Document's uint64.
Reused int-valued RenderTarget width, height and channels must be positive and no
greater than 2147483647 on every architecture. Times/tick indices use existing
checked int64 arithmetic. Authored range and requested/clock time bases are retained,
including non-reduced values; computed intersections/projections are reduced.
Frame/sample ranges use original composition origins, not the requested start.
Positive sub-tick ranges may select zero ticks. Range ends are exclusive.

Visual/audio arrays retain authored track then clip order; annotation tracks are
non-rendering. Audio includes only explicit audio-track clips, including selected
video assets; video tracks never imply audio extraction. Disabled and unrelated
clips do not contribute resources or capabilities. Required capabilities are scoped
through `documentv2.RequiredCapabilitiesForSelection`. Resource IDs and audio bus
IDs are strictly sorted unique; capability collections are sorted unique tokens.
Bus IDs include selected audio routes and their complete ancestor/master closure.

Resources are logical external identities only. Inline text/vector assets are not
external resources; text contributes its indirect pinned font. Resource `content_hash`
is always present. An empty hash for a URI-only external asset means **unmaterialized**,
not content verified; no URI or filesystem locator appears in this plan. Fonts must
retain their existing nonempty pinned hash. A later execution boundary must bind
content through authorized resources, rather than inventing a hash in S1.

## Transitions and handles

Ordinary PlannedClip entries represent only positive visible intersections. An active
transition is one typed record, authored transition order, with outgoing/incoming
ClipBindings and exact sampled spans. Its interval is `[cut-pre, cut+post)`; zero-width
hard cuts are inert and omitted. Each participant binding preserves its authored
track/clip identity and ranges even when it has no ordinary visible intersection.
Temporal head/tail HandleSpans represent only sampled portions outside those authored
ranges, projected with the evaluator's source mapping. Static image/text/vector
participants have empty handle arrays. A transition participant is a reference, not
an additional draw layer; consumers must not render it twice when it also appears
in Visual. Sampled source ranges for static sources are identity bookkeeping and do
not assert a temporal media domain.

## Admission and fixtures

S1 explicitly bounds plan bytes to 8 MiB, the conservative pre-encoding Document
admission estimate to 128 MiB, visited Go nodes/JSON values to 200000, depth to 64,
individual strings/raw metadata to 1 MiB, and any collection to 16384 entries.
The Go graph scan checks context before work/allocation; strings are conservatively
charged six encoded bytes per byte. These are new S1 profile admission bounds, not
changes to Document validation or a persisted snapshot encoding contract. A valid
Document outside these limits gets `RENDER_PLAN.V2.LIMIT`. JSON numbers are bounded
by the field widths, with at most 20 integer digits in the raw codec.

Conformance files prefixed `document-v2-render-plan` include gap, mixed
image/vector/text/audio and transition-head-handle valid/canonical plans, source
Document snapshots, fixed canonical SHA-256 files and hostile complete wire messages.
Package tests pin canonical digests in source and check failure-zero, ownership,
concurrent reads, cancellation, bounds, clock overflow, legacy cross-decode,
non-integer Document metadata and both standalone/against-document guarantees.
