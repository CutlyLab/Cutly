# Restricted RC release evidence

## Scope

This independent release lane measures the existing restricted hard-cut RC and its decoded-evidence
streaming infrastructure. It does not implement or accept the broad Document v2 runtime. S2's
independent review remains blocked; this work does not retry that review or waive downstream gates.

A repeated-job observation interval is not a single daemon's memory-stability soak. Synthetic
streaming throughput is not media decoding or mixed-media rendering throughput. Observed system
FFmpeg/FFprobe identities are not approved signed distribution bundles or legal clearance.

## Recorded baseline

On 2026-11-07, task `bbh4hl7qu` exited 0 from standalone source `4f24e32`:

```sh
CUTLY_M1_STREAM_BENCH_MIB=4096 go test -run '^$' \
  -bench M1DecodedEvidenceStreaming -benchtime=1x -benchmem ./render/ffmpegbackend
```

Host: Darwin arm64, Apple M5; Go 1.27.3. Result:

```text
BenchmarkM1DecodedEvidenceStreaming-10  1  1998869584 ns/op  2148.70 MB/s  168208 B/op  63 allocs/op
```

The benchmark uses synthetic `dd` output through the production stream reader/hash path. The
allocation metric is Go benchmark allocations, not maximum resident memory. This one measurement
is neither a throughput history nor an hour-long soak. It does not certify general renderer memory
bounds, actual media decoding performance, signed bundles or broad M4 acceptance.

A separate reader-process RSS check, task `bw1l6x6sf`, also exited 0:

```sh
go test -count=1 -v -run '^TestM1DecodedEvidenceRSSDoesNotScaleWithDuration$' ./render/ffmpegbackend
```

Observed subprocess high-water marks: **8912896 bytes** for 16MiB input and **8814592 bytes**
for 256MiB input. This passed the existing 32MiB growth allowance. The measurements concern fresh
synthetic-stream reader subprocesses, not the encoder/daemon process tree or one-hour retention.

## Runner contract

The internal CLI requires an explicit profile and a new output path:

```sh
go run ./internal/cmd/release-evidence --profile smoke --output /tmp/cutly-rc-smoke.json
# Explicit long observation; reserve up to 105 minutes and choose an unused output path.
go run ./internal/cmd/release-evidence --profile long --output /tmp/cutly-rc-long.json
```

The smoke profile runs four bounded test invocations: one injected-fault round, backend publication recovery,
daemon real-media recovery, and public Go SDK real-media end-to-end coverage. It does not run the
synthetic benchmark/RSS checks or the ten-run decoded-root comparison.

The long profile runs 100 injected-fault rounds, initial backend/daemon recovery, the 4096MiB
synthetic streaming benchmark, reader-process RSS comparison, and the existing ten-run real-media
decoded-root comparison. It then requires 61 successful paired backend/daemon recovery observations
with 60 pauses of at least one minute and at least one hour of observation. These are fresh jobs,
not one continuously running daemon. Neither profile mounts a disk image or changes daemon defaults.

Defaults are a 15-minute smoke budget or a 100-minute long budget, a five-minute per-command timeout,
and 64KiB retained per output stream. The CLI permits bounded timeout/budget/log overrides, not
arbitrary test selectors or reduced long-profile round counts. The maximum budget is 105 minutes;
long requires more than one hour. Total retained command logs are capped at 2MiB and reports at
16MiB. Complete event streams are still checked even when retained log excerpts are truncated.

Acceptance requires every selected test to execute and pass, not merely a zero `go test` exit code
when tests skip or selectors match nothing. Failed, interrupted and incomplete observations must
remain failures in the report. The report records observed source/runtime identities and bounded
logs; treat it as local diagnostic evidence, not a signed attestation. Do not put sensitive private
media into the test environment or publish logs without reviewing them.

The report distinguishes `passed`, `failed`, `canceled`, and `timeout`, with requested/completed
progress counters and per-command results. Durations are integer nanoseconds. Metric entries carry
their observed value, unit and measurement scope; smoke reports have no synthetic metric entries.
The pre-test source snapshot records revision and dirty status, not a source-content digest. A dirty
checkout is allowed and reported honestly. Run on a clean candidate commit for release evidence.

The output path is exclusively reserved before source/tool observation or test execution; an existing
file or symlink is never replaced. Missing prerequisites and test failures are written to that same
reserved path when report writing remains possible. Abrupt process termination or storage failure
can leave an empty/incomplete report; never interpret its presence alone as success. Go flags,
workspace/toolchain overrides, `CUTLY_*` opt-ins and `GIT_*` repository/index/config overrides from
the parent environment are removed from child commands. Process-group cleanup is implemented on Linux/macOS; other platforms fail closed
rather than claiming equivalent runtime coverage.

`.github/workflows/release-evidence.yml` provides a manual-only Linux/macOS run with read-only
repository permissions. It attempts to retain the JSON report even on test failure; a runner setup
failure may occur before a report exists, in which case workflow logs—not a missing report—are the
failure evidence. No cron schedule, PR trigger, npm publishing or release creation is added.

No workflow has been dispatched and no result has been published externally by this implementation
session. Real disk-image exhaustion remains separately opt-in and is not part of the runner.

## Local runner validation

Implementation checkpoint: `2ccae89`. On 2026-11-07, real smoke task `batl503fv` exited 0:

```sh
go run ./internal/cmd/release-evidence --profile smoke \
  --output /tmp/cutly-rc-smoke-20261107-session95292636.json
```

The report recorded `passed`, from `09:49:30.091925Z` to `09:49:58.422494Z`, on Darwin arm64 /
Go 1.27.3 with observed FFmpeg and FFprobe 9.0.2. All four invocations exited 0 and passed;
no command log excerpt was truncated. One requested injected-fault round and the public SDK
real-media test completed. No long-profile recovery observation or synthetic metric was claimed.

The source snapshot was `24b282e555a0f64b3e0c5da5ba36e1b5ae620964`, **dirty**, before the new runner
was committed; it included unrelated concurrent video-authoring changes. This is local integration
evidence, not a clean-candidate release run or a content attestation for `2ccae89`.
The local report is 46,676 bytes with SHA-256
`fc88f735589b39f0bf2214ca5876f7a871ab88155092cdf19714494bfc514afc`.
It is retained in `/tmp` locally, not checked in or published; that path is not durable distribution.

Focused normal/race/vet passed for `./internal/releaseevidence ./internal/cmd/release-evidence`.
The manual-workflow architecture check passed (`bkhvw96z9`). New-package cross-compilation passed
for Linux amd64, Linux 386, Windows amd64 and js/wasm (`bagrf6u7b`); this is not execution evidence
for those platforms. TypeScript tests and package-local `pack:check` passed (`bjs9bzq8i`).

Full-repository normal/race tests, `go vet ./...`, `go mod tidy -diff` and `git diff --check`
passed at the implementation checkpoint (`b3dk2a6or`). Full Linux amd64/Linux 386/Windows amd64
cross-compilation and the roadmap's documented js/wasm subset also passed (`bw48tggl9`). The wasm
subset includes both new internal packages; the pre-existing FFmpeg RSS test syscall limitation
still prevents treating a full wasm suite as supported.

### Independent audit closure

The narrow runner audit is closed at repair commit `0f08c2e`, with no outstanding verified P0/P1/P2.
Three P2 findings were fixed and independently retested using the unchanged external reproductions:

- Internal output-cap termination now remains `failed` with its stream-specific diagnostic, rather
  than becoming caller cancellation. Actual caller cancellation/deadline still have distinct states.
- Tool hashing checks context, opens nonblocking on Linux/macOS, and checks descriptor regularity;
  an executable FIFO cannot block awaiting a writer before the regular-file check.
- Inherited `GIT_*` overrides are stripped, keeping source observations bound to the configured repo.

Independent verification passed: all original `^TestAudit` overlay tests under `-race` (11.516s),
focused normal/race/vet, the manual-workflow architecture test and scoped diff checks. Reproductions
included real self-child output overflow, full Observe FIFO rejection, two temporary Git repositories,
native Go nested/benchmark JSON events, and failure-report persistence. The reviewer did not modify
repository files. S2 was outside this audit and remains blocked.

Post-repair full normal/race/vet/tidy/diff gates passed (`b4tbfj8ju`); portable full builds and the
same documented wasm subset passed (`bcmysk8zt`); TypeScript tests/pack check passed (`bk8ioxiqv`).

Post-repair real smoke also passed (`bjtv9rva1`), from `10:41:18.560189Z` to `10:41:29.250953Z`:
all four invocations passed with exit 0 and no retained-log truncation. Report:
`/tmp/cutly-rc-smoke-audit-fixed-20261107-95292636.json` (47,147 bytes), SHA-256
`eb934258437bab919fa7b2bb7b28a22f6bb0c629083f9b0777838dffe6a79c99`.
Its snapshot is `d97b6fa5daaa6cc7e644327fc957b0a51a2e074b`, dirty before the repair commit,
so it remains local integration evidence, not clean-candidate release evidence.

### Long observation in progress

A complete real long profile has not yet finished; fake-clock long-profile tests do not supply
one-hour runtime evidence. The first local launch (`bzwhxysiv`) was deliberately canceled after
noticing its terminal launcher had a ten-minute timeout, insufficient for the profile. It exited 1
and retained a `canceled` report at `/tmp/cutly-rc-long-audited-20261107-95292636.json`; it is not a
successful long observation.

The replacement uses a locally built runner from `0f08c2e` (source HEAD `bf64a4f`, with unrelated
working-tree changes reported as dirty), an independent launcher, and the runner's own 100-minute
budget. It writes `/tmp/cutly-rc-long-20261107-95292636-v2.json`, with local `.log` and `.exit.json`
companions. Completion is pending; the report and exit status must be inspected before claiming
any fault-round, measurement or hour-long acceptance. No workflow dispatch or external publication
was performed.
