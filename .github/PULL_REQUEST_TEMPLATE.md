## Summary

- What changed?
- Why is it needed?

## Compatibility and security

- Public API, schema or canonical-fixture impact:
- Host trust, filesystem, process or media-runtime impact:
- Known limitations:

## Verification

- [ ] `go mod tidy -diff`
- [ ] `go vet ./...`
- [ ] `go test -race -count=1 ./...`
- [ ] `npm --prefix sdk/typescript test`
- [ ] `npm --prefix sdk/typescript run pack:check`
- [ ] Documentation and release notes updated when applicable
