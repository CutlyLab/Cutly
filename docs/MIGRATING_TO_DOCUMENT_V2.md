# Migrating from legacy v1 to Document v2

Document v2 is additive. Cutly does not reinterpret a legacy project,
transaction, Agent envelope, render plan, prepared environment, or evidence
record as v2.

## Project migration

1. Decode the v1 document with the legacy decoder.
2. Convert it with the explicit `core/documentv2` migration API and review
   every diagnostic.
3. Canonicalize and validate the resulting Document v2.
4. Initialize a new directory with `cutly init` or
   `CreateLocalProjectV2`. Do not place v1 and v2 metadata in one project
   root.

Stable source IDs should be retained when their meaning is unchanged. New v2
entities such as audio buses, typed properties, transitions, annotations,
and pinned text resources require explicit IDs.

## Edit migration

Legacy transactions cannot be submitted to Agent v2. Rebuild edits with the
closed `transactionv2` operation union. A v2 transaction binds document ID,
revision, document hash, operation order, idempotency key, and digest. Plan
first, review the persisted plan/impact, then apply by transaction ID.

## Render migration

Legacy RenderPlan v3, ExecutionManifest v1, Prepared v1, and Evidence v1 stay
on `/v1/agent`. Document v2 uses its independent plan, manifest, prepared
environment, chunk assembly, and Evidence v2 identities. Prepare again under
host policy; never copy an executable digest or backend identity from a v1
record.

The native v2 daemon is opt-in and requires the host-selected worker,
prepared-environment authority, signed FFmpeg/FFprobe runtime bundle, cache,
checkpoint, and output roots. Unsupported capabilities fail at plan,
prepare, or preflight; there is no legacy fallback.

## Client migration

- Go: replace `NewHTTPTransport`/`NewClient` with
  `NewAgentHTTPTransportV2`/`NewAgentClientV2` and use the v2 transaction and
  render codecs.
- TypeScript: replace `CutlyClient`/`HTTPAgentTransport` with
  `CutlyAgentV2Client`/`HTTPAgentV2Transport`. Keep receipt bytes canonical
  until a profile-specific decoder validates them.
- CLI: omit `--profile` for v2. Add `--profile legacy-v1` only while an old
  integration is being retired.

## Unsupported migration cases

Browser/WebLayer content, arbitrary HTML/CSS/JS, distributed scheduling,
plugins, HDR, ProRes, AV1, rich-text layout, OTIO/FCPXML, and GPU-specific
effects remain outside M4. Migration must report them; it must not approximate
or rasterize them silently.
