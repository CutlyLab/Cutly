# Document v2 public API reference

This is the supported standalone Cutly Document v2 surface. Profiles are
versioned independently and never fall back to legacy v1 behavior.

## Go SDK

Import `github.com/CutlyLab/Cutly/sdk/go/cutly`.

- Document: `DecodeDocument`, `CanonicalDocument`, `ValidateDocument`.
- Transactions: `CreateTransactionV2`, `DecodeTransactionV2`,
  `CanonicalTransactionV2`, `DigestTransactionV2` and the typed operation
  aliases in `transaction_v2.go`.
- Durable projects: `CreateLocalProjectV2`, `OpenLocalProjectV2`, then
  `Inspect`, `Status`, `History`, `Plan`, `Apply`, `Discard`, and `Revert`.
- Render identities: `ResolveRenderPlanV2`, strict plan/manifest/request/
  prepared-environment codecs, and Evidence v2 verification in `render_v2.go`.
- Agent: `NewAgentHTTPTransportV2`, `NewAgentClientV2`, and the complete
  project/edit/render lifecycle in `agent_v2.go`: plan, prepare, preflight,
  exact frame, submit, status, history and cancel. `Invoke` remains available
  for compatible future actions without changing the envelope.

All request IDs and idempotency keys are caller-owned. Returned structures
own their buffers. Business rejection is `Result.OK == false`; malformed,
uncorrelated, or unavailable transport results are errors.

## TypeScript SDK

Install `@cutly/sdk` and import from its root export.

- Number-preserving Document v2 transactions: `buildTransactionV2`,
  `decodeTransactionV2`, `transactionDigestV2`, and typed operation builders.
- Agent v2: `CutlyAgentV2Client`, `HTTPAgentV2Transport`,
  `createAgentRequestV2`, and `createAgentTransactionRequestV2`.
- Player/UI: `DocumentV2Player`, `DocumentV2PreviewAdapter`,
  `documentV2TimelinePrimitives`, and typed transaction builders.

Agent v2 results expose `receipt` as exact canonical `Uint8Array`. This is
intentional: parsing arbitrary Document v2 metadata through JavaScript
`Number` would lose number spelling or precision.

## CLI

New commands use Document v2 by default. Pass `--profile legacy-v1` only for
the frozen restricted RC protocol.

```text
cutly init --project-dir DIR [--document FILE|-]
cutly project inspect|status|history|capabilities --project ID
cutly edit plan|apply|discard|revert --project ID --payload FILE|-
cutly render plan|prepare|preflight|frame|submit|status|history|cancel --project ID --payload FILE|-
cutly call --request FILE|- [--profile document-v2|legacy-v1]
```

The default endpoint is `http://127.0.0.1:9817/v2/agent`; legacy v1 uses
`/v1/agent`. JSON output is the exact canonical Agent result. Exit codes are
0 for success, 1 for input/transport failure, 2 for usage, and 3 for business
rejection.

## Agent v2

Media type: `application/cutly-agent-document-v2+json`.

Envelope profiles:

- `cutly.agent-request.document-v2.v2`
- `cutly.agent-result.document-v2.v2`

Each request carries an action-specific `payload_profile`; the schema and Go
decoder correlate it with the exact tool, action and closed payload shape.
Capability discovery lists only installed routes. Native preflight rechecks
the persisted environment against current worker/runtime/assembler authority.
Exact-frame requests execute through the isolated native worker and publish a
content-addressed deterministic PNG under the daemon-owned output root.
Executable paths, runtime trust roots, provider identities, and output
authorization remain daemon-owned.

The normative schemas are in `schemas/`; fixed cross-language fixtures are
in `conformance/fixtures/`.
