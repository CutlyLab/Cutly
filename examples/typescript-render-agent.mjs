import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { CutlyClient, HTTPAgentTransport, createRenderJob } from "@cutly/sdk";

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

  const planned = requireOK("plan", await client.planRender({
    requestId: `${configuration.job}-plan`, projectId: configuration.project, range, target,
  }, options));
  const planReceipt = requireReceipt(planned, "cutly.agent-render-plan.v1");
  const plan = requireObject(planReceipt.plan, "plan receipt plan");
  requireDigest(plan.fingerprint, "plan fingerprint");

  const locators = new Map([
    [configuration.videoAsset, resolve(configuration.video)],
    [configuration.audioAsset, resolve(configuration.audio)],
  ]);
  if (!Array.isArray(plan.resources)) throw new Error("plan receipt has no resources array");
  const resources = plan.resources.map((resource, index) => {
    requireObject(resource, `plan resource ${index}`);
    const locator = locators.get(resource.asset_id);
    if (locator === undefined) throw new Error(`plan requires unmapped asset ${JSON.stringify(resource.asset_id)}`);
    return { assetId: resource.asset_id, locator };
  });

  const prepared = requireOK("prepare", await client.prepareRender({
    requestId: `${configuration.job}-prepare`, projectId: configuration.project,
    plan, resources, deterministicSeed: configuration.seed,
  }, options));
  const prepareReceipt = requireReceipt(prepared, "cutly.agent-render-prepare.v1");
  const executionManifest = requireObject(prepareReceipt.execution_manifest, "prepare execution manifest");
  requireDigest(executionManifest.fingerprint, "execution manifest fingerprint");
  if (executionManifest.plan_fingerprint !== plan.fingerprint || executionManifest.deterministic_seed !== configuration.seed) {
    throw new Error("prepare receipt is not bound to the requested plan and seed");
  }

  const ready = requireOK("preflight", await client.preflightRender({
    requestId: `${configuration.job}-preflight`, projectId: configuration.project,
    plan, executionManifest,
  }, options));
  const preflightReceipt = requireReceipt(ready, "cutly.agent-render-preflight.v1");
  if (preflightReceipt.plan_fingerprint !== plan.fingerprint ||
      preflightReceipt.execution_manifest_fingerprint !== executionManifest.fingerprint) {
    throw new Error("preflight receipt is not bound to plan and execution manifest");
  }

  const job = createRenderJob({
    jobId: configuration.job,
    idempotencyKey: `${configuration.job}-once`,
    plan,
    executionManifest,
    artifactUri: pathToFileURL(resolve(configuration.output)).href,
  });
  const submitted = requireOK("submit", await client.submitRender({
    requestId: `${configuration.job}-submit`, projectId: configuration.project, job,
  }, options));
  let snapshot = requireSnapshot(submitted, job);

  while (snapshot.state !== "complete") {
    if (snapshot.state === "failed" || snapshot.state === "canceled") {
      throw new Error(`render ${snapshot.state}: ${snapshot.failure_class ?? ""} ${snapshot.failure_message ?? ""}`.trim());
    }
    await wait(configuration.pollMs, abort.signal);
    const status = requireOK("status", await client.renderStatus({
      requestId: `${configuration.job}-status-${snapshot.last_sequence}`,
      projectId: configuration.project,
      jobId: configuration.job,
    }, options));
    snapshot = requireSnapshot(status, job);
  }
  requireObject(snapshot.artifact, "complete render artifact");
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

function requireOK(action, result) {
  requireObject(result, `${action} result`);
  if (result.ok === true) return result;
  const diagnostics = Array.isArray(result.diagnostics)
    ? result.diagnostics.map((entry) => `${entry.code ?? "UNKNOWN"} at ${entry.path ?? ""}: ${entry.message ?? ""}`).join("; ")
    : "missing diagnostics";
  throw new Error(`${action} rejected in state ${JSON.stringify(result.state)}: ${diagnostics}`);
}

function requireReceipt(result, profile) {
  const receipt = requireObject(result.receipt, `${profile} receipt`);
  if (receipt.profile !== profile) throw new Error(`receipt profile ${JSON.stringify(receipt.profile)}, want ${profile}`);
  return receipt;
}

function requireSnapshot(result, job) {
  const snapshot = requireObject(result.receipt, "render job snapshot");
  const snapshotJob = requireObject(snapshot.job, "snapshot job");
  const states = new Set(["queued", "running", "canceling", "canceled", "failed", "complete"]);
  if (snapshot.schema_version !== 1n || snapshotJob.id !== job.id ||
      snapshotJob.plan_fingerprint !== job.plan_fingerprint ||
      snapshotJob.execution_manifest_fingerprint !== job.execution_manifest_fingerprint ||
      snapshotJob.artifact_uri !== job.artifact_uri || !states.has(snapshot.state) ||
      typeof snapshot.last_sequence !== "bigint" || snapshot.last_sequence <= 0n) {
    throw new Error("render job snapshot has invalid identity or shape");
  }
  if (snapshot.state === "complete") {
    const artifact = requireObject(snapshot.artifact, "complete render artifact");
    requireDigest(artifact.content_hash, "artifact content hash");
    requireDigest(artifact.evidence_fingerprint, "artifact evidence fingerprint");
    if (artifact.uri !== job.artifact_uri ||
        artifact.execution_manifest_fingerprint !== job.execution_manifest_fingerprint ||
        artifact.evidence_uri !== `${job.artifact_uri}.evidence.json` ||
        artifact.frame_count !== job.frame_range.count || artifact.sample_count !== job.sample_range.count) {
      throw new Error("complete artifact is not bound to the submitted job");
    }
  }
  return snapshot;
}

function requireObject(value, name) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error(`${name} must be an object`);
  return value;
}

function requireDigest(value, name) {
  if (typeof value !== "string" || !/^sha256:[0-9a-f]{64}$/.test(value)) throw new Error(`${name} is invalid`);
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
