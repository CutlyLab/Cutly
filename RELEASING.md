# Releasing Cutly

Cutly uses one version for the Go SDK/CLI and `@cutly/sdk`. In this monorepo,
Go module release tags use the `cutly/vX.Y.Z` form.

1. Update `sdk/go/cutly.Version`, `sdk/typescript/package.json`, its metadata
   test, and this changelog to the same non-development version.
2. Run from `cutly/`:

   ```text
   go mod tidy -diff
   go vet ./...
   go test -race -count=1 ./...
   npm --prefix sdk/typescript test
   npm --prefix sdk/typescript pack --dry-run
   ```

3. Merge the audited release commit into the target repository.
4. Create and push the exact tag, for example `cutly/v0.1.0-rc.1`.
5. The release workflow verifies that the tag, Go version, and npm version
   match; rebuilds command archives; publishes npm with provenance; attests
   the archives; writes `SHA256SUMS`; and creates the GitHub release.
6. Verify the npm provenance and GitHub attestations before announcing the
   release. Never retag or replace published artifacts.

The workflow intentionally fails for `0.1.0-dev` and for any version mismatch.
