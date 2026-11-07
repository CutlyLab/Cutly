# Compatibility and deprecation policy

Cutly versions wire protocols independently from package releases. A published
schema/profile string is immutable: behavior changes require a new schema or
profile, not a silent reinterpretation.

## Release candidate guarantees

- `cutly.json.canonical-integer.v1`, Agent envelope v1, Document v1/v2,
  Transaction v1 and RenderPlan v3 fixtures are compatibility gates.
- Go and TypeScript SDKs execute shared canonical fixtures in CI.
- Unknown fields, duplicate members, fractional number spellings and unsupported
  tool/action pairs fail closed.
- Business rejection is `ok:false` plus an error diagnostic. Connectivity,
  malformed results and correlation mismatches are transport errors.
- V1 transactions are not implicitly applied to Document v2. A future v2 edit
  protocol will be additive.

During `0.x`, source APIs may change only with a release note and migration
entry. Frozen wire fixtures and profile identities do not change in place.
Deprecated source APIs remain for at least one minor release after their
replacement ships. A decoder removal requires both an explicit migration path
and a major wire-version transition.

## Capability truthfulness

The daemon capabilities receipt lists only installed routes. A scheduler queue
does not imply workers, native video decode, text shaping, production codecs or
WebLayer support. Clients must use preflight and must not infer support from a
package being importable.
