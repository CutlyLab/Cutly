import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

import {
  CutlyClient,
  HTTPAgentTransport,
  createRenderJob,
  decodeRenderJobSnapshotReceipt,
  decodeRenderPlanReceipt,
  decodeRenderPreflightReceipt,
  decodeRenderPrepareReceipt,
} from "@cutly/sdk";

let timeout;

try {
  const configuration = readOptions();
  const abort = new AbortController();
  timeout = setTimeout(() => abort.abort(new Error("render example timed out")), configuration.timeoutMs);
  const client = new CutlyClient(new HTTPAgentTransport(configuration.endpoint));
  const options = { signal: abort.signal };
  const range = {
    start: { value: 0n, rate: 1n },
    duration: { value: configuration.duration, rate: 1n },
  };
  const target = {
    timeline_id: configuration.timeline,
    width: configuration.width,
    height: configuration.height,
    frame_clock: {
      origin: { value: 0n, rate: 1n },
      frames_per_second: { value: configuration.fps, rate: 1n },
    },
    sample_clock: {
      origin: { value: 0n, rate: 1n },
      samples_per_second: configuration.sampleRate,
    },
    channels: configuration.channels,
    pixel_format: "yuv420p",
    color_space: "bt709",
  };

  const planRequestId = `${configuration.job}-plan`;
  const planned = await client.planRender({
    requestId: planRequestId, projectId: configuration.project, range, target,
  }, options);
  const planReceipt = await decodeRenderPlanReceipt(planned, {
    requestId: planRequestId, projectId: configuration.project, range, target,
  });
  const plan = planReceipt.plan;

  const locators = new Map([
    [configuration.videoAsset, resolve(configuration.video)],
    [configuration.audioAsset, resolve(configuration.audio)],
  ]);
  const resources = (plan.resources ?? []).map((resource) => {
    const locator = locators.get(resource.asset_id);
    if (locator === undefined) throw new Error(`plan requires unmapped asset ${JSON.stringify(resource.asset_id)}`);
    return { assetId: resource.asset_id, locator };
  });

  const prepareRequestId = `${configuration.job}-prepare`;
  const prepared = await client.prepareRender({
    requestId: prepareRequestId, projectId: configuration.project,
    plan, resources, deterministicSeed: configuration.seed,
  }, options);
  const prepareReceipt = await decodeRenderPrepareReceipt(prepared, {
    requestId: prepareRequestId, projectId: configuration.project,
    plan, resources, deterministicSeed: configuration.seed,
  });
  const executionManifest = prepareReceipt.execution_manifest;

  const preflightRequestId = `${configuration.job}-preflight`;
  const ready = await client.preflightRender({
    requestId: preflightRequestId, projectId: configuration.project,
    plan, executionManifest,
  }, options);
  await decodeRenderPreflightReceipt(ready, {
    requestId: preflightRequestId, projectId: configuration.project, plan, executionManifest,
  });

  const job = createRenderJob({
    jobId: configuration.job,
    idempotencyKey: `${configuration.job}-once`,
    plan,
    executionManifest,
    artifactUri: pathToFileURL(resolve(configuration.output)).href,
  });
  const submitRequestId = `${configuration.job}-submit`;
  const submitted = await client.submitRender({
    requestId: submitRequestId, projectId: configuration.project, job,
  }, options);
  let snapshot = decodeRenderJobSnapshotReceipt(submitted, {
    requestId: submitRequestId, projectId: configuration.project, action: "render", job,
  });

  while (snapshot.state !== "complete") {
    if (snapshot.state === "failed" || snapshot.state === "canceled") {
      throw new Error(`render ${snapshot.state}: ${snapshot.failure_class ?? ""} ${snapshot.failure_message ?? ""}`.trim());
    }
    await wait(configuration.pollMs, abort.signal);
    const statusRequestId = `${configuration.job}-status-${snapshot.last_sequence}`;
    const status = await client.renderStatus({
      requestId: statusRequestId,
      projectId: configuration.project,
      jobId: configuration.job,
    }, options);
    snapshot = decodeRenderJobSnapshotReceipt(status, {
      requestId: statusRequestId, projectId: configuration.project, action: "status", job,
    });
  }
  console.log(JSON.stringify(snapshot.artifact, bigintJSON, 2));
} catch (error) {
  console.error(`cutly TypeScript render example: ${error instanceof Error ? error.message : String(error)}`);
  process.exitCode = 1;
} finally {
  if (timeout !== undefined) clearTimeout(timeout);
}

function readOptions() {
  const read = (name, environment, fallback = undefined) => {
    const exact = `--${name}`;
    const inline = `${exact}=`;
    for (let index = 2; index < process.argv.length; index += 1) {
      const argument = process.argv[index];
      if (argument.startsWith(inline)) return argument.slice(inline.length);
      if (argument === exact) {
        if (index + 1 >= process.argv.length) throw new Error(`${exact} requires a value`);
        return process.argv[index + 1];
      }
    }
    return process.env[environment] ?? fallback;
  };
  const required = (name, environment) => {
    const value = read(name, environment);
    if (typeof value !== "string" || value.trim() === "") {
      throw new Error(`--${name} or ${environment} is required`);
    }
    return value;
  };
  const integer = (name, environment, fallback, minimum = 1n) => {
    const value = read(name, environment, fallback);
    if (typeof value !== "string" || !/^-?(0|[1-9][0-9]*)$/.test(value)) {
      throw new Error(`--${name} must be an exact integer`);
    }
    const parsed = BigInt(value);
    if (parsed < minimum || parsed > (1n << 63n) - 1n) throw new Error(`--${name} is out of int64 range`);
    return parsed;
  };
  const number = (name, environment, fallback, minimum = 1) => {
    const value = read(name, environment, fallback);
    if (typeof value !== "string" || !/^[0-9]+$/.test(value)) throw new Error(`--${name} must be an integer`);
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > 2_147_483_647) {
      throw new Error(`--${name} is out of timer range`);
    }
    return parsed;
  };

  const channels = integer("channels", "CUTLY_CHANNELS", "2");
  if (channels !== 1n && channels !== 2n) throw new Error("--channels must be 1 or 2");
  return Object.freeze({
    endpoint: read("endpoint", "CUTLY_ENDPOINT", "http://127.0.0.1:9817/v1/agent"),
    project: read("project", "CUTLY_PROJECT", "demo"),
    timeline: read("timeline", "CUTLY_TIMELINE", "timeline"),
    videoAsset: read("video-asset", "CUTLY_VIDEO_ASSET", "video"),
    video: required("video", "CUTLY_VIDEO"),
    audioAsset: read("audio-asset", "CUTLY_AUDIO_ASSET", "audio"),
    audio: required("audio", "CUTLY_AUDIO"),
    output: required("output", "CUTLY_OUTPUT"),
    job: read("job", "CUTLY_JOB", "example-render"),
    duration: integer("duration", "CUTLY_DURATION", "1"),
    width: integer("width", "CUTLY_WIDTH", "1280"),
    height: integer("height", "CUTLY_HEIGHT", "720"),
    fps: integer("fps", "CUTLY_FPS", "30"),
    sampleRate: integer("sample-rate", "CUTLY_SAMPLE_RATE", "48000"),
    channels,
    seed: integer("seed", "CUTLY_SEED", "0", -(1n << 63n)),
    timeoutMs: number("timeout-ms", "CUTLY_TIMEOUT_MS", "300000"),
    pollMs: number("poll-ms", "CUTLY_POLL_MS", "250"),
  });
}

function wait(milliseconds, signal) {
  return new Promise((resolvePromise, rejectPromise) => {
    if (signal.aborted) {
      rejectPromise(signal.reason ?? new Error("aborted"));
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      rejectPromise(signal.reason ?? new Error("aborted"));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolvePromise();
    }, milliseconds);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function bigintJSON(_key, value) {
  return typeof value === "bigint" ? value.toString() : value;
}
