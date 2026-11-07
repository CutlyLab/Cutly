# Cutly public render examples

The Go and TypeScript examples run the complete public hard-cut flow:

```text
durable project → plan → host prepare → fresh-probe preflight
                → durable render → status → MP4 + evidence
```

They do not import renderer internals or construct an ExecutionManifest by
hand. Read [../KNOWN_LIMITATIONS.md](../KNOWN_LIMITATIONS.md) before using the
release candidate.

## 1. Initialize a project

Prepare a valid Document v1 whose `video` and `audio` assets carry the exact
immutable content hashes and durations of the local media, then initialize the
directory once:

```sh
mkdir -p /tmp/cutly/project
cutly init \
  --project-dir /tmp/cutly/project \
  --document ./document-v1.json
```

Repeating `cutly init` with the exact same canonical document is safe. A
different document is rejected; edits must use the Plan → Apply transaction
protocol.

## 2. Start the daemon

Create physically separate state, output, and input roots. Start `cutlyd` with
the initialized project, those roots, pinned FFmpeg/FFprobe executables, and
the five canonical build/runtime SHA-256 identities required by the host:

```sh
mkdir -p /tmp/cutly/state /tmp/cutly/output /tmp/cutly/input

cutlyd \
  --project-id demo \
  --project-dir /tmp/cutly/project \
  --render-state-dir /tmp/cutly/state \
  --render-output-dir /tmp/cutly/output \
  --render-input-root /tmp/cutly/input \
  --ffmpeg /absolute/path/to/ffmpeg \
  --ffprobe /absolute/path/to/ffprobe \
  --sdk-build-digest sha256:<64-lowercase-hex> \
  --compiler-build sha256:<64-lowercase-hex> \
  --evaluator-build sha256:<64-lowercase-hex> \
  --ffmpeg-runtime-dependency-fingerprint sha256:<64-lowercase-hex> \
  --ffprobe-runtime-dependency-fingerprint sha256:<64-lowercase-hex>
```

The media paths passed to the examples must be inside an authorized input
root. The MP4 path must be inside the dedicated output root. Identity values
are host-owned audit inputs, not arbitrary request parameters; see
[../cmd/cutlyd/README.md](../cmd/cutlyd/README.md).

## 3. Run the Go example

From the Cutly module root:

```sh
go run ./examples/go-render-agent \
  --project demo \
  --timeline timeline \
  --video-asset video --video /tmp/cutly/input/video.mp4 \
  --audio-asset audio --audio /tmp/cutly/input/audio.wav \
  --output /tmp/cutly/output/example.mp4 \
  --duration 1 --width 1280 --height 720 --fps 30
```

The example prints the verified final artifact receipt. Request and job IDs
are stable so an interrupted invocation can safely observe/retry the same job.

## 4. Run the TypeScript example

Install `@cutly/sdk` into your application, then run:

```sh
node ./examples/typescript-render-agent.mjs \
  --project demo \
  --timeline timeline \
  --video-asset video --video /tmp/cutly/input/video.mp4 \
  --audio-asset audio --audio /tmp/cutly/input/audio.wav \
  --output /tmp/cutly/output/example-ts.mp4 \
  --duration 1 --width 1280 --height 720 --fps 30
```

Every option also has a `CUTLY_*` environment equivalent, including
`CUTLY_ENDPOINT`, `CUTLY_PROJECT`, `CUTLY_VIDEO`, `CUTLY_AUDIO`, and
`CUTLY_OUTPUT`.
