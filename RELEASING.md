# Releasing Cutly

Cutly uses one version for the Go SDK/CLI and `@cutly/sdk`. The canonical
public repository is `github.com/CutlyLab/Cutly`, with `go.mod` at its root, so
Go module release tags use the root-module form `vX.Y.Z`.

## Safe bootstrap from the incubation directory

During incubation the complete public tree lives at `Manyfy/cutly`. It must be
reviewed into the root of the target repository; the surrounding Manyfy tree
is never copied. For the first import, the known target `main` baseline is
commit `84e1f36`, containing only the existing Apache-2.0 `LICENSE`. Treat that
identity as a guard, not as permission to overwrite a changed repository. If
the target has advanced or contains additional files, stop and reconcile them
through an ordinary reviewed change.

Before synchronizing:

- record the exact audited Manyfy source commit and require a clean source
  worktree;
- clone the target repository separately, fast-forward its local `main`, and
  require a clean target worktree;
- verify the first-import baseline with `git rev-parse --short HEAD` and
  `git ls-files`, and confirm that its `LICENSE` is Apache-2.0;
- create a normal feature branch in the target repository; never work directly
  on `main`, rewrite target history, or force-push.

From that clean target feature branch, copy the audited public tree without
copying repository metadata or replacing the target's existing license:

```sh
rsync --archive \
  --exclude .git \
  --exclude LICENSE \
  /absolute/path/to/Manyfy/cutly/ \
  /absolute/path/to/Cutly/
```

The source `LICENSE` and npm package metadata are also Apache-2.0 so later
reviews can detect license drift, but the bootstrap copy deliberately
preserves the target repository's original license bytes. Compare the two
trees excluding `.git` and `LICENSE`, inspect every `git status --short` entry,
and handle any intentionally retired target file explicitly; the sync command
does not bulk-delete target content. Run the full verification below, commit
on the feature branch, push that branch normally, and merge it through a PR.
Do not tag the incubation repository.

## Release checklist

1. Update `sdk/go/cutly.Version`, `sdk/typescript/package.json`, its metadata
   test, and [`CHANGELOG.md`](CHANGELOG.md) to the same non-development
   version.
2. Confirm the target repository root contains the audited source, its CI is
   green, `LICENSE` is Apache-2.0, and the npm trusted-publishing/token and
   provenance settings are ready.
3. Run from the target repository root:

   ```text
   go mod tidy -diff
   go vet ./...
   go test -race -count=1 ./...
   npm --prefix sdk/typescript test
   npm --prefix sdk/typescript pack --dry-run
   ```

4. Create and push the exact root-module tag, for example
   `v0.1.0-rc.1`, from the audited target `main` commit. Never retag or replace
   a published artifact.
5. `.github/workflows/release.yml` verifies that the tag, Go version, and npm
   version match; verifies the module; builds every command archive; only then
   publishes npm with provenance; attests the archives; writes `SHA256SUMS`;
   and creates the GitHub release.
6. Verify the npm provenance, archive checksums, GitHub attestations, and Go
   module resolution from a clean consumer before announcing the release.

The workflow intentionally fails for `0.1.0-dev` and for any version mismatch.
