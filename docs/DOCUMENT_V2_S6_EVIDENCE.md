# Document v2 S6 production evidence

> Historical stage ledger. The integrated source disposition is recorded in
> `DOCUMENT_V2_NATIVE_SDK_ROADMAP.md`; early status statements below describe the checkpoint at
> which the evidence was captured.

## Status — 2026-11-09

**Implementation repaired, stable-source gates and local native acceptance passing; formal S6
release acceptance is not yet claimed.**
The historical working-tree baseline is `5a186be790bf9f70326e9779fd76356dd7fe716c`, with
release/archive changes preserved. No package publication or default-daemon-profile switch was
part of this stage checkpoint.

## Implemented surfaces and limited results

- Additive native `backendstage.ServiceV2` / reference-chunk execution and opt-in daemon wiring
  use original Document v2 environments, not translated V1 requests. Checkpoint hits are bound
  back to the durable job-neutral chunk index. Full-plan-only execution remains an initial
  restriction. Production payloads now use an inherited fd5 anonymous disk spool with bounded
  canonical metadata on stdout; the 16 MiB limit remains only on the compatibility inline API.
- The real isolated V2 worker measures its executable against manifest ProviderBuild before
  proportional work. Host launch configuration is delivered through a private inherited fd3.
  A parent-liveness pipe on fd4 cancels the child on EOF and terminates its process group within
  the 250ms grace, including an uncooperative renderer. Owner reports focused normal/race/vet
  passing (3.478s / 17.819s / exit0) and an actual helper-parent SIGKILL test. This is not a real
  daemon kill campaign or proof of filesystem/network/RSS sandboxing. The earlier review was
  stopped; a new independent review remains an exit gate.
- Durable `chunkcachev2` binds original plan/manifest/provider/runtime/output and exact partitions,
  excluding JobID. It uses actual CAS reader leases and heartbeat, not an in-memory substitute.
  The confirmed post-commit inode-replacement and expired-internal-lease defects are repaired with
  generation/byte revalidation and final lease checks. Focused generation tests pass. A separate
  checkpoint collision after index loss is also closed: identical task/output checkpoints may
  converge while preserving the durable historical attempt count; publish checkpoints remain
  strict. Cache acceptance still requires a new independent audit.
- Parent `go test -exec=true` compiled daemon, worker, backendstage and chunkcachev2 successfully
  at source snapshot `bm2bxnzu2` (exit0). This historical compile-only result is superseded by the
  fresh stable-source gates below; earlier failed concurrent-edit attempts are not counted.

## Resumed stable-source verification — 2026-11-09

The resumed source passed:

```
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go test -exec=true ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=true ./...
CGO_ENABLED=0 GOOS=js GOARCH=wasm go test -exec=true ./core/... ./protocol/... ./render/api/... ./render/resourceidentityv2/... ./conformance/...
git diff --check
```

The Windows gate first exposed Unix-only supervisor command/tests; explicit build constraints and
an unsupported-platform command stub repaired that failure, after which all three full target
compilations passed. Full Go formatting inspection produced no remaining paths. The production
worker and backendstage package tests pass with descriptor streaming; a 1920x1080 protocol fixture
proves a chunk larger than 16 MiB is admitted by the stream contract while the legacy inline API
fails explicitly before reconstruction.
The TypeScript SDK also passed all 49 Node tests and `npm pack --dry-run` using an isolated writable
cache; no tarball was published or retained.

## Local native-process acceptance — 2026-11-09

A controlled local acceptance authority was created outside the repository in `/private/tmp`; it is
explicitly not a production release signing root and no private key, bundle, binary or generated
fixture was committed. FFmpeg 9.0.2 was built with static x264 and zimg inputs. Both `zscale` and
`libx264` were present, and `otool -L` found no Homebrew or `/usr/local` dynamic dependencies in
the bundled `ffmpeg` or `ffprobe`. A canonical signed bundle, pinned daemon, native worker, private
fd3 host configuration and prepared 320x180 mixed fixture were generated. The fixture contains a
real H.264/BT.709 video source and a 48 kHz stereo PCM WAV source; it is not a gap/silence stand-in.

The opt-in runner then passed:

```
CUTLY_TEST_NATIVE_ACCEPTANCE=smoke go test -count=1 -run '^TestNativeV2ProcessAcceptance$' -v ./cmd/cutlyd
CUTLY_TEST_NATIVE_ACCEPTANCE=fault100 go test -count=1 -run '^TestNativeV2ProcessAcceptance$' -v ./cmd/cutlyd
CUTLY_TEST_NATIVE_ACCEPTANCE=soak go test -timeout=70m -count=1 -run '^TestNativeV2ProcessAcceptance$' -v ./cmd/cutlyd
```

All six `CUTLY_TEST_NATIVE_*` inputs were set to the controlled artifacts for those commands.
Smoke completed one mixed render in 9.96s. Fault100 completed 100 observed-running daemon kills and
100 durable recoveries in 3m31.208s. The full soak completed 1,737 mixed renders in 1h0m0.756s;
first/last process-tree RSS was 32,192/43,568 KiB and the sampled peak was 44,640 KiB, so retained
growth was 11,376 KiB against the runner's 131,072 KiB limit. Across all accepted runs the decoded
video root remained `sha256:c1fa87f44611e837fe1e9d1f0513d39c4eeb8c0feae7f5beb671b398d17de851`
and the decoded audio root remained
`sha256:c77c7192f4ed637b73879e25d7989631de4c55985b7e44f21c4ae319d7665240`.
An initial soak invocation was excluded: the default Go test timeout stopped it at ten minutes.
The successful run used the required 70-minute outer timeout and independently enforced a full
one-hour wall clock inside the test.

## Real process acceptance runner and permission boundary

`cmd/cutlyd/native_acceptance_test.go` adds an opt-in actual daemon/HTTP runner with smoke,
100-daemon-kill and full wall-clock one-hour soak modes. It checks artifact bytes/size, Evidence v2
identity/coverage, repeat decoded-root claims and process-tree RSS samples. It does not independently
decode the final media, prove a supplied fixture is fully mixed, or replace separate worker/encoder
fault coverage. The local results above exercise this boundary. They are technical process
acceptance, not evidence that the ephemeral signing key is an owner-approved release trust root.

The runner requires a host-supplied release-stamped daemon, worker,
actual signed runtime bundle, canonical prepared environment, private host launch configuration and
SDK build digest. It does not create trust keys, self-sign PATH executables or relax runtime policy.
Missing inputs must be explicit skips, never successful runtime acceptance.

Host input variables are `CUTLY_TEST_NATIVE_DAEMON`, `CUTLY_TEST_NATIVE_WORKER`,
`CUTLY_TEST_NATIVE_RUNTIME_BUNDLE`, `CUTLY_TEST_NATIVE_ENVIRONMENT`,
`CUTLY_TEST_NATIVE_HOST_CONFIG`, and `CUTLY_TEST_NATIVE_SDK_DIGEST`.
`CUTLY_TEST_NATIVE_ACCEPTANCE` selects `smoke`, `fault100` or `soak`.
The intended opt-in test is `TestNativeV2ProcessAcceptance`; running it requires the permitted
verified runtime inputs. The development-only new daemon fixture was removed rather than used
as a substitute for the denied action. Existing legacy regressions are not native S6 acceptance.

## Remaining exit gates

- Obtain new independent cache/worker/service/encoder review closure on the repaired source.
- Execute the remaining cross-job warm-hit, corrupt-entry/missing-chunk, worker/encoder crash and
  measured two-second terminal-cancellation matrix. The completed fault100 campaign covers daemon
  death after durable running; it is not every worker/encoder fault phase.
- Replace the local acceptance authority with owner-approved release keys/bundles and repeat the
  release-candidate run before claiming formal release acceptance.
- Accepted S3–S5 remain prerequisites. No result here declares S4–S6 or M4 complete.
