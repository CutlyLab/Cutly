# Compatibility and deprecation policy

Cutly versions wire protocols independently from package releases. A published
schema/profile string is immutable: behavior changes require a new schema or
profile, not a silent reinterpretation.

## Supported profile matrix

| Surface | Legacy restricted RC | Document v2 |
| --- | --- | --- |
| Project store | `project/local` | `project/localv2` |
| Transactions | v1 typed operations | number-preserving `transactionv2` |
| Agent HTTP | `/v1/agent` | `/v2/agent` |
| Go client | `Client` | `AgentClientV2` |
| TypeScript client | `CutlyClient` | `CutlyAgentV2Client` |
| Render identity | RenderPlan v3 / Manifest v1 / Evidence v1 | semantic plan / Manifest v2 / Prepared v2 / Evidence v2 |
| Player | `HeadlessPlayer` / `AgentPreviewAdapter` | `DocumentV2Player` / `DocumentV2PreviewAdapter` |

There is no cross-decoding or fallback between columns.

## Release candidate guarantees

- `cutly.json.canonical-integer.v1`, Agent envelope v1, Document v1/v2,
  Transaction v1 and RenderPlan v3 fixtures are compatibility gates.
- Go and TypeScript SDKs execute shared canonical fixtures in CI.
- Unknown fields, duplicate members, fractional number spellings and unsupported
  tool/action pairs fail closed.
- Business rejection is `ok:false` plus an error diagnostic. Connectivity,
  malformed results and correlation mismatches are transport errors.
- V1 transactions are not implicitly applied to Document v2. The v2 edit and
  Agent protocols are independent additive profiles.

During `0.x`, source APIs may change only with a release note and migration
entry. Frozen wire fixtures and profile identities do not change in place.
The normative notice and removal rules are in
[`SEMVER_AND_DEPRECATION.md`](SEMVER_AND_DEPRECATION.md).

## Capability truthfulness

The daemon capabilities receipt lists only installed routes. A scheduler queue
does not imply workers, native video decode, text shaping, production codecs or
WebLayer support. Clients must use preflight and must not infer support from a
package being importable.
