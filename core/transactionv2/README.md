# Document v2 transactions

This independent Go package implements the S7a pure mutation layer. It neither
modifies the legacy transaction engine nor opens a project, publishes a revision,
authorizes a caller, or accepts an idempotency ledger. `project/localv2` owns those
persistence and authority decisions. This is not a public SDK/default-profile
switch and does not resolve the unrelated S2 audit blocker.

## API and identity

- `Create(document, proposal) (Transaction, error)` validates the bounded base,
  fills schema/profile/document ID/revision/document hash, recomputes the proposal
  digest, and returns an owned canonical transaction. Transaction ID,
  idempotency key, operation IDs and entity IDs are caller supplied.
- `Validate(tx) []documentv2.Diagnostic` checks resource admission, exact typed
  wire shape, closed union and optional digest. It does not resolve references
  against a document. Intermediate operations may reference entities added later.
- `CanonicalJSON(tx)` (alias `Canonical`) returns canonical bytes.
- `Decode(bytes)` accepts **exactly** those canonical bytes, without trailing
  whitespace/newline. Duplicate members at any depth, case aliases, unknown
  fields, omitted required fields, explicit null typed fields, invalid UTF-8,
  unpaired UTF-16 escapes and noncanonical spellings are rejected.
- `Digest(tx)` (alias `TransactionDigest`) returns a lowercase `sha256:` identity
  excluding the optional `digest` member itself. A supplied digest must match.
- `Apply(ctx, document, tx) (Result, error)` checks document ID, revision and full
  document hash, applies ordered operations to owned data, increments the
  candidate revision exactly once and validates the complete final Document v2.

The schema is 2 and the profile is
`cutly.transaction.document-v2.number-preserving.v2`. Canonicalization uses
`videoedit.CanonicalJSON`, with sorted object keys and preserved JSON number
spelling, including fractional/exponent values in metadata, annotations and
extension values. It does **not** integer-canonicalize an embedded document.
Document hashes retain the existing `documentv2.DocumentHash` contract, including
revision and non-rendering intent. `1.50` and `1.5` intentionally have different
identity. No legacy canonical/hash behavior changes.

Agent-correctable failures return stable diagnostics and a zero candidate,
inverse, hashes and impact. Cancellation returns the context error and zero
Result. Unexpected panics are contained as `ErrInternal` with zero Result.
Cancellation is checked before work, between operations, before final identity
work and before returning success; bounded validation/canonicalization are not
interruptible in their inner loops. The package never publishes partial state.
Caller arguments must not be concurrently modified while a call reads them;
concurrent read-only applications are supported. Candidate, inverse and inputs
share no mutable memory after return.

## Closed typed operation catalog

There are 32 kinds. Exactly the payload corresponding to `op` is allowed.

| Kinds | Payload | Meaning |
| --- | --- | --- |
| `asset_add/remove/replace` | `asset` | Complete typed Asset, including supported text/vector expressions |
| `timeline_add/remove/replace` | `timeline` | Complete typed Timeline |
| `track_add/remove/replace` | `track` | Complete typed Track in explicit timeline |
| `clip_add/remove/replace`, `clip_delete` | `clip` | Complete typed Clip in explicit track; delete aliases remove |
| `bus_add/remove/replace` | `bus` | Complete typed AudioBus, including parent routing/gain/pan/limiter |
| `clip_move` | `move` | Replace timeline start, preserving exact source range |
| `clip_trim` | `trim` | Explicit source and timeline ranges; no implicit ripple |
| `clip_split` | `split` | Exact partition using caller-authored left/right clips |
| `clip_retrack` | `retrack` | Move clip to an explicit track and position |
| `clip_set_disabled` | `disabled` | Explicit boolean, including false |
| `clip_set_asset` | `asset_reference` | Explicit asset reference |
| `property_set` | `properties` | Replace/clear complete typed PropertySet |
| `lane_set/remove` | `lane` | Set/remove caller-identified typed keyframe lane |
| `transition_set/remove` | `transition` | Set/remove supported typed transition |
| `track_route` | `route` | Set audio bus ID; empty ID explicitly clears route |
| `metadata_set/remove` | `metadata` | Explicit scope and literal map key |
| `annotation_set/remove` | `annotation` | Explicit scope and typed annotation |

Add and replace require a nonnegative explicit `position` and matching value ID;
remove/delete forbid both position and value. Set inserts if absent or replaces
at its existing index if present. Replace never silently moves/reorders an entity:
its position must match the current index. Lists are not automatically sorted;
callers preserve Document v2 chronological clip order. Retrack position is the
index **after** removing the source, including a same-track move. Final Document
validation enforces track/source compatibility and global ID uniqueness, including
nested lanes, paths and annotations. Atomic swaps can use temporary missing
references, but cannot finish invalid.

PropertySet covers transform, crop, opacity, audio gain/pan/channel matrix and the
existing typed extension representation. Omitted `properties.value` clears the
PropertySet; explicit null is not accepted. There are no invented effects,
templates, arbitrary field paths or generic JSON patches. Bus replacement is the
complete typed operation for bus graph, gain/pan and limiter edits.

Metadata scopes are document/asset/timeline/track. Annotation scopes are
document/timeline/track/clip. Scope IDs must exist in this document; unsupported
or foreign scopes fail. Keys are literal strings, not JSON pointers. Raw JSON
`null` is a valid metadata/annotation/extension value where Document v2 permits
it, distinct from an omitted remove payload.

## Exact split and animation

`split.at` must lie strictly inside the original timeline range. The engine uses
`evaluator.SourceTime` and checked rational subtraction to prove the left/right
source and timeline ranges exactly partition the original, including non-unit
retime ratios. Both replacements retain the source asset, left retains the
original clip ID, and right supplies a distinct ID. Unrepresentable rational
results fail; no rounding is permitted.

Both clips are complete caller-authored replacements. Nested annotation/lane IDs
and animation must be explicitly authored/remapped by the caller. The engine does
not copy IDs, shift keyframes implicitly or approximate cubic easing. It validates
the resulting full document; it does not claim caller-authored replacement
animation has the same sampled curve as the original. Unsupported synthesis is
not silently performed.

## Inverse and revert safety

Every successful result includes a complete typed inverse in reverse operation
order. Inverse payloads preserve original content and precise list positions;
there is no document-replace or generic JSON-patch inverse. Inverse IDs are
bounded deterministic digest-derived transaction IDs plus sequential operation
IDs. Revision always advances, including when applying an inverse.

Every inverse operation carries `expect.hash`, a postimage predicate checked
against the target and structural location immediately before that inverse step.
Removed targets bind the original insertion slot's neighboring IDs; replacements
bind complete target content and position. Metadata predicates bind key presence
and value; routes bind the current route. A conflict returns
`OP.V2.POSTIMAGE_CONFLICT`, not a best-effort overwrite. Full final validation
also prevents creating a dangling reference after newer edits.

The original inverse is CAS-bound to its postimage. To attempt revert after a
newer **disjoint** commit, a trusted store can call `Create(newBase, oldInverse)`
to refresh CAS/digest while retaining every postimage predicate. Disjoint edits
can succeed; edits touching a captured entity or insertion position may
conservatively conflict. These predicates are consistency checks, not signatures
or permission grants. A store must use its persisted inverse and transaction ID,
not trust a caller-supplied replacement inverse or ledger.

## Render impact

Every successful transaction changes full identity because revision advances.
`IdentityChanged` is separate from `Visual`, `Audio`, and `RenderRequired`.
Metadata, annotations and display names alone do not require rendering. Typed
properties are projected separately for visual and audio impact. Other render
changes conservatively invalidate whole timelines rather than claim narrow
sample/frame ranges. `TimelineIDs` is sorted and unique, includes removed
timelines, and is empty for non-rendering-only changes. Conservative impact may
include unaffected timelines; it is not a cache-key replacement or backend proof.

## Resource profile

Admission runs before marshaling caller-owned values. Hard caps are 128 MiB for
original/candidate documents, 16 MiB for transactions **and complete inverses**,
4096 operations, depth 64, 2,000,000 structural/token nodes, 1 MiB per UTF-8 string,
and 256 UTF-8 bytes per ID. Positions use portable signed-int32 bounds.
Conservative encoded-size accounting includes JSON escaping and may reject an
object below the byte ceiling if its structural estimate exceeds the budget.
Raw JSON contributes to aggregate depth/node budgets and is checked for Unicode,
duplicates and string sizes before canonicalization. No unbounded key-list
allocation occurs in preflight. Collections and later cloning are admitted only
after those checks; memory is bounded, not promised to equal wire size.

A tiny proposal that would capture an inverse larger than 16 MiB is rejected
atomically rather than returning an incomplete/non-revertible success. The
128 MiB document profile does not imply every whole-entity deletion can fit a
single inverse. Stores may impose tighter limits, but must not weaken these.

## Verification

Package tests cover every operation and inverse, atomic multiops and inverse
snapshots, precise original positions, CAS/document/hash/overflow conflicts,
postimage conflicts and disjoint revert, exact retime splits and nested IDs,
scoped metadata/annotations and raw numeric spelling, closed union/wire attacks,
large bases versus inverse limits, typed properties, separate render domains,
argument/result ownership, cancellation/panic after mutation, concurrent apply,
and a bounded Decode fuzz target. Schema/conformance fixtures live outside this
package and are maintained independently against this stable model.
