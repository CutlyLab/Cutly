# Contributing to Cutly

Thank you for helping improve Cutly. This repository is a standalone SDK with
frozen public wire formats, deterministic fixtures and host-owned media trust
boundaries. Changes should be small enough to review and explicit about any
compatibility or security impact.

By participating, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Security vulnerabilities must follow [SECURITY.md](SECURITY.md), not a public
issue.

## Before opening a change

1. Search existing issues and pull requests.
2. Open an issue before a broad protocol, architecture or compatibility change.
3. Fork the repository and branch from the current `main`.
4. Keep credentials, private media, runtime state and generated archives out of
   Git.

Go 1.25 or newer and Node.js 20 or newer are required. FFmpeg and FFprobe are
required for real-media integration tests. Production release acceptance uses
maintainer-provided signed runtimes and cannot be replaced by an arbitrary
system FFmpeg build.

## Compatibility rules

- Published schemas and canonical fixtures are immutable. Add a new version or
  profile instead of changing existing semantics in place.
- Preserve exact rational time and canonical integer JSON behavior.
- Keep structural verification separate from host authorization.
- Unsupported behavior must fail closed; do not add approximate fallback paths.
- Keep the Go module independently buildable and avoid undeclared dependencies.
- Update schemas, fixtures, SDK declarations and documentation together.

## Local checks

Run the complete default gates from the repository root:

```sh
gofmt -w <changed-go-files>
go mod tidy -diff
go vet ./...
go test -race -count=1 ./...
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run pack:check
git diff --check
```

Use the focused package tests while developing, but do not substitute them for
the full gates before a pull request. Changes to portable surfaces should also
run the cross-target checks in `.github/workflows/ci.yml`.

## Pull requests

Explain what changed, why it is needed, how it was tested and any known limits.
Call out public API, schema, fixture, security, runtime, license or release
effects. Do not mix unrelated refactors into a protocol or correctness change.

All contributions are licensed under the repository's
[Apache License 2.0](LICENSE).
