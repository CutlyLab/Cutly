# Cutly wire schemas

These Draft 2020-12 schemas describe the protocols that the current Go core actually accepts.
They are compatibility artifacts, not a place to announce future capabilities.

- `cutly.document.v1.schema.json` describes the canonical editing document.
- `cutly.document.v2.schema.json` is the strict M2 schema gate for first-class transitions, typed
  properties and keyframes, explicit audio buses, pinned-font text, and deterministic path vectors.
  It is intentionally decoded by `core/documentv2`; the shipped v1 transaction/render path remains
  unchanged and v1 input enters only through the explicit pure migration API.

Migration is deterministic but intentionally partial across semantic versions: a v1 document that
violates a stronger v2 invariant receives stable diagnostics and no partial result. This includes,
for example, URI-only fonts that cannot satisfy v2 font pinning and track/asset combinations that
v1 did not constrain. Representable unknown properties use a digest-bound legacy envelope and
remain capability-gated rather than being silently dropped.
- `cutly.transaction.v1.schema.json` describes the seven currently implemented typed operations.
- `cutly.render-plan.v3.schema.json` describes the current backend-neutral semantic plan, including
  composition clocks, compiled tick ranges, required capabilities and URI-free resources.
- `cutly.render-plan.v2.schema.json` is retained only as a legacy wire reference. Execution code
  rejects v2 and rebuilds a v3 plan from the committed Document instead of guessing a migration.
- `cutly.probe-manifest.v1.schema.json` describes immutable resource identity, pinned probe identity,
  container timing, and normalized video/audio stream facts.
- `cutly.media-runtime-bundle.v1.schema.json` describes a signed, complete FFmpeg/FFprobe closure.
  Schema validation is only the wire gate: `media/runtimebundle` also verifies the Ed25519 signature,
  canonical payload fingerprint, platform, directory closure, file metadata and every content hash
  before it returns executable paths.
- `cutly.execution-manifest.v1.schema.json` freezes backend/runtime/output choices plus controlled
  worker locators and versioned decoded-evidence byte profiles for one semantic plan.
- `cutly.render-job.v1.schema.json` binds an idempotent full-plan or chunk range to both the semantic
  plan and execution-manifest fingerprints.
- `cutly.exact-frame-request.v1.schema.json` binds one composition frame and a controlled PNG
  publication target to those same immutable identities.
- `cutly.evidence-manifest.v1.schema.json` records artifact integrity plus streaming, decoded-domain
  frame and PCM evidence bound to the exact render job, timeline range, dimensions and unit byte
  layout; encoded container bytes are not the repeatability contract.
- `cutly.agent-request.v1.schema.json` and `cutly.agent-result.v1.schema.json` define the shared
  project/edit/render Agent and CLI envelope. Business rejection is `ok:false` with an error
  diagnostic; transport failure is not disguised as a successful envelope.

Production decoders apply an additional Cutly strict-wire profile before semantic validation:
duplicate object members, case-insensitive field aliases, omitted required zero-valued fields and
multiple top-level values are rejected. JSON Schema describes the public shape but does not weaken
that boundary.

Public Go/TypeScript fingerprints additionally use `cutly.json.canonical-integer.v1`: valid Unicode
scalar strings, decoded-name duplicate rejection, UTF-16 code-unit member ordering, integer-only
number tokens without IEEE-754 rounding, minimal string escaping and fail-closed resource limits.
The shared `canonical-json-v1.vectors.canonical.json` fixture is executed by both implementations.
Agent identity limits are defined as Unicode-scalar counts (matching JSON Schema `maxLength`) plus
a four-bytes-per-scalar UTF-8 admission ceiling. "Blank" means the four JSON whitespace characters
space, tab, CR and LF; this definition is identical in Schema, Go and TypeScript and does not vary
with a host language's locale or broader Unicode trimming table.

Optional fields whose explicit zero value is semantically identical to omission (for example an
empty optional route or final interpolation token) are accepted by both the schema and production
decoder. `CanonicalJSON` removes those aliases, so they never create a second content identity.
Fields whose presence changes the grammar, such as a payload on a vector `close` command or an
empty `property_set`, remain rejected.

For fields whose Go representation uses `omitempty`, the schema describes the semantic Decode
surface: an explicitly authored empty string may be accepted when it is equivalent to omission.
`CanonicalJSON` is the sole normalization boundary and omits those zero values. Payload-bearing
owned unions remain strict exceptions (for example, vector `close` must omit `points`, and an empty
`property_set` is invalid) because accepting ignored payload fields would create ambiguous syntax.

`metadata`, `annotations`, and annotation tracks are non-rendering intent in every published
Document schema. A consumer must not derive pixels or PCM from them. Render-affecting state must be
a first-class typed field or an `affects_rendering` extension with an explicit backend capability;
otherwise capability negotiation would silently omit a material dependency.

The v2 audio-bus shape keeps `cutly.audio.graph.v1` as the deterministic gain/pan and single-parent
route DAG. Two separately capability-gated profiles extend it only when their typed fields are
present: `cutly.audio.channel-matrix.stereo.v1` is an exact probe-bound clip input-channel matrix,
and `cutly.audio.limiter.sample-peak-instant.v1` is a master-only, instantaneous stereo-linked
sample-peak limiter. Their descriptors freeze media preflight, processing order, output range,
latency and rounding boundaries. Omission means no such stage; the base graph still has no hidden
limiter, normalization, sends, effects, surround policy, or channel map. These schema contracts do
not claim that the later M2 PCM DSP implementation is complete.

`image`, `text`, and `vector` assets are immutable static sources. Their optional asset duration and
clip `source_range` are retained as edit/migration identity and do not bound sampling, request
retime, or require transition handles. Animated image formats must be normalized and probed as
`video`; only temporal `video`/`audio` source ranges are checked against known asset duration.

Enums are reserved for Cutly-owned semantic discriminants such as stream kind, CFR/VFR mode and
color range. Container names, codecs, pixel/sample formats, channel layouts, color identifiers and
HDR formats remain non-empty open strings so a new media-runtime value does not require a schema
revision. Actual support is decided by versioned renderer capabilities and preflight.

Cross-field invariants such as unique IDs, ordered non-overlapping clips, asset references, exact
range arithmetic, revision CAS and backend capability checks remain semantic conformance rules in
the Go core. JSON Schema validates the wire shape; it does not replace `ValidateDocument`, `Apply`
`Resolve` or `probe.Validate`.

## Integer wire profile

Every schema integer declares an explicit lower and upper bound matching its wire type: signed
media/time values use `int64`, revisions use `uint64`, and Go `int` fields are deliberately capped
at signed 32-bit so the published protocol is architecture-independent. Schema instances and
conformance fixtures are decoded with `json.Decoder.UseNumber`; routing them through `float64`
would silently round boundary values.

Draft 2020-12 treats `1`, `1.0`, and `1e0` as the same mathematical integer. Cutly's stricter wire
profile does not: an integer field must use a base-10 integer token accepted by its concrete Go
type. Production decoders enforce this through `internal/wirejson`, including exact `int64` and
`uint64` overflow checks. This lexical rule complements the schemas because JSON Schema itself
cannot distinguish equivalent number spellings.
