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

## Runner contract (implementation validation pending)

The internal CLI requires an explicit profile and a new output path:

```sh
go run ./internal/cmd/release-evidence --profile smoke --output /tmp/cutly-rc-smoke.json
# Explicit long observation; reserve up to 105 minutes and choose an unused output path.
go run ./internal/cmd/release-evidence --profile long --output /tmp/cutly-rc-long.json
```

These commands are the implementation contract, not yet recorded execution evidence. The smoke
profile runs a bounded set of actual RC tests plus synthetic measurements. The long profile adds
100 fault rounds and at least one hour of repeated fresh recovery test jobs. Neither profile mounts
a disk image or changes daemon defaults. Long-profile completion does not certify a continuously
running daemon's memory behavior.

Acceptance requires every selected test to execute and pass, not merely a zero `go test` exit code
when tests skip or selectors match nothing. Failed, interrupted and incomplete observations must
remain failures in the report. The report records observed source/runtime identities and bounded
logs; treat it as local diagnostic evidence, not a signed attestation. Do not put sensitive private
media into the test environment or publish logs without reviewing them.

`.github/workflows/release-evidence.yml` provides a manual-only Linux/macOS run with read-only
repository permissions. It attempts to retain the JSON report even on test failure; a runner setup
failure may occur before a report exists, in which case workflow logs—not a missing report—are the
failure evidence. No cron schedule, PR trigger, npm publishing or release creation is added.

No workflow has been dispatched and no result has been published externally by this implementation
session. Real disk-image exhaustion remains separately opt-in and is not part of the runner.
