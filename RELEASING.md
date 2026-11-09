# Releasing Cutly

Cutly uses one version for the Go SDK/CLI and `@cutly/sdk`. The canonical
public repository is `github.com/CutlyLab/Cutly`, with `go.mod` at its root, so
Go module release tags use the root-module form `vX.Y.Z`.

## Release source

Release only from the standalone `github.com/CutlyLab/Cutly` repository. Work
on a reviewed feature branch, preserve unrelated changes, rebase on current
`origin/main`, pass the complete gates, and merge through the normal review
path. Tags are created only from the audited target `main` commit.

## Release checklist

1. Update `sdk/go/cutly.Version`, `sdk/typescript/package.json`, its metadata
   test, and [`CHANGELOG.md`](CHANGELOG.md) to the same non-development
   version.
2. Confirm the target repository root contains the audited source, its CI is
   green, `LICENSE` is Apache-2.0, and the npm trusted-publishing/token and
   provenance settings are ready. Configure `CUTLY_MEDIA_RUNTIME_TRUSTED_KEYS`,
   one `CUTLY_MEDIA_RUNTIME_FINGERPRINT_<GOOS>_<GOARCH>` variable per target,
   and the Linux/macOS acceptance-kit URL/SHA-256 variables named in the
   release workflow.
3. Run from the target repository root:

   ```text
   go mod tidy -diff
   go vet ./...
   go test -race -count=1 ./...
   npm --prefix sdk/typescript test
   npm --prefix sdk/typescript run pack:check
   ```

4. Create and push the exact root-module tag, for example
   `v0.1.0-rc.1`, from the audited target `main` commit. Never retag or replace
   a published artifact.
5. `.github/workflows/release.yml` verifies that the tag, Go version, and npm
   version match; verifies the module; builds every command archive with
   packaged trust stamps; runs Linux/macOS mixed smoke, 100 daemon kills and a
   one-hour soak; only then publishes npm with provenance, attests the archives,
   writes `SHA256SUMS`, and creates the GitHub release.
6. Verify the npm provenance, archive checksums, GitHub attestations, and Go
   module resolution from a clean consumer before announcing the release.

The workflow intentionally fails for `0.1.0-dev` and for any version mismatch.

## Local command archive verification

The shared internal archive builder creates and validates the command package.
It performs no push, tag, npm publish, or GitHub release operation.

From the standalone repository, choose an existing output directory without an archive of the same
version/target. Populate the module cache with `go mod download` first if needed; the builder's
child builds use readonly/offline module resolution. The workflow has a separate dependency-download
step for fresh runners. Then run:

```sh
go run ./internal/cmd/release-archive --repo . --target darwin/arm64 \
  --output /tmp --version 0.1.0-rc.1 \
  --media-runtime-bundle-fingerprint sha256:<64-lowercase-hex> \
  --media-runtime-trusted-keys '<key-id>=<64-lowercase-hex-public-key>' --smoke
go run ./internal/cmd/release-archive --repo . --target windows/amd64 \
  --output /tmp --version 0.1.0-rc.1 \
  --media-runtime-bundle-fingerprint sha256:<64-lowercase-hex> \
  --media-runtime-trusted-keys '<key-id>=<64-lowercase-hex-public-key>'
```

Use `--smoke` only for the current host target. It runs the packaged commands' version paths,
not a server or media render. The expected version must match the source repository SDK constant.
The workflow runs the builder on the host and passes the cross-compilation target explicitly;
setting process-wide `GOOS`/`GOARCH` would incorrectly cross-compile the helper itself.

Each `cutly-VERSION-GOOS-GOARCH` archive contains one root directory with `cutly`, `cutlyd`, and
`cutly-worker-v2` (`.exe` on Windows). Linux and macOS archives also contain the platform-supported
`cutly-ffmpeg-supervisor-v2`. The root also contains `LICENSE`, `CUTLYD.md`, `go.sum`, dependency
notices, SPDX SBOM, dependency lock, and every dependency-license file. Windows uses ZIP; the other
targets use tar.gz. Validation inspects actual archive members, document bytes and binary build
metadata. Cross-target metadata inspection is not cross-target runtime execution, and Go
buildinfo's main-module `(devel)` version is not the SDK version.

Each acceptance kit is a `.tar.gz` whose root contains `runtime-bundle/`,
`environment.json`, `host-config.json`, and `sdk-digest.txt`. The kit URL and
SHA-256 are release-owner inputs. The workflow never generates a signing key
or promotes a local development bundle into release evidence.
