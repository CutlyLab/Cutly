# Document v2 host authorization

`render/api` verifies a portable identity claim. `preparev2.Authority` additionally
compares it against independently selected host policy. Neither API starts a
process, reconstructs a live Program, nor authorizes artifact publication.

## Trusted construction boundary

Only host code may construct `Config`. Do not deserialize it from an Agent call,
Document, manifest, job, or environment variable. Compiler/evaluator/backend/media
choices are copied into immutable canonical bytes. Provider identity is measured
from a host-selected executable through `internal/buildidentity` policy:

- Development mode requires explicit `AllowLocalDevelopment` opt-in.
- Packaged mode requires the controlling host's release-owned provider digest pin;
  a request cannot supply or replace it.
- The native no-symlink measurement path is Linux/macOS only. Other platforms fail
  closed for this new host feature; protocol codecs remain portable. Existing
  legacy render behavior is unchanged.

A successful measurement is not safe later execution of that pathname. A worker
must bind the executable actually launched to those measured bytes. S6 owns that
launch lifecycle; this package must not be used to bypass it.

`Freeze` re-resolves the original Document against the Plan, replaces draft build,
backend and runtime choices with host pins, derives program/source identity and
checks materialized resources. `Authorize` checks a complete request against the
original Document, those same host pins and independently observed resources. A
self-issued manifest with a valid new fingerprint and a different provider digest
is structurally valid but unauthorized.

## Resource boundary

`ResourceVerifier` is trusted host code, installed once at construction. It must:

1. authorize the locator under host-owned roots or a host-owned CAS;
2. reject traversal and symlink substitution;
3. measure content size/hash and inspect or probe the same retained or privately
   snapshotted bytes;
4. derive the exact versioned inspection/probe identity expected by the host's
   materialization profile;
5. return observed identities, never echo unverified request claims.

The authority deep-copies the claim before invoking the callback and compares all
observed fields, including the locator, size and inspection/probe fingerprint.
Callbacks cannot mutate the manifest through pointer members. Missing callbacks,
errors, panics, mismatches and cancellation produce zero public results. No
callback is needed for a true resource-free gap/silence plan.

Static inspection fingerprints are opaque to the portable S2 codec: their receipt
interpretation depends on pinned host/provider policy. The image/font categories
are not independent format proofs or portable reconstruction formats. S3/S4 must
freeze concrete canonical receipt projections before durable cross-host reconstruction
or real inspector interoperability; they cannot silently reinterpret old fingerprints.

Output format/codec strings similarly bind choices, not implementation support.
S4/S5 backend preflight must reject unsupported tuples; host authorization cannot
be treated as evidence that an encoder exists or supports those choices.

This is an injection boundary, not a production file inspector. The tests use
explicit independent observation tables, not production media evidence. S3/S4
host adapters must implement the concrete resource receipt and reconstruction
contracts and reverify their retained bytes. URI-only authored resources require
host materialization before freezing, without changing the original Document or
Plan identity. Persisted locators do not authorize themselves after restart.

`AuthorizedRequest` owns canonical request bytes and returns owned copies. It is a
point-in-time authorization result, not an open resource handle or permanent
promise that a mutable locator still refers to the same bytes. Reconstruction,
execution and publication retain their own mandatory authorization gates.
