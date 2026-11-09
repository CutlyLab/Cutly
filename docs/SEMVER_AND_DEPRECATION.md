# SemVer and deprecation policy

Cutly package releases follow SemVer. Wire schemas and profile strings have an
additional invariant: a published profile is immutable even when the package
major version has not changed.

- Additive source APIs and new profile names may ship in a minor release.
- Backward-compatible fixes may ship in a patch release, but may not change
  canonical bytes, hashes, or accepted fields of a frozen profile.
- A source API is deprecated for at least one minor release and documented in
  the changelog before removal.
- A frozen decoder or protocol version is removed only in a new package major
  release, after an explicit migration path and at least one minor release of
  notice.
- Security fixes may reject inputs that were never valid under the documented
  contract. Any broader compatibility impact is called out in the advisory
  and changelog.

Legacy v1 and Document v2 remain distinct. Defaulting new projects and CLI
requests to v2 does not remove the explicit `legacy-v1` client or endpoint.
