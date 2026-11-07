import { decodeCanonicalJSON, encodeCanonicalValue } from "./canonical-json.js";

const MAX_RENDER_JOB_INPUT_BYTES = 4 << 20;
const exactInputKeys = Object.freeze([
  "artifactUri",
  "executionManifest",
  "idempotencyKey",
  "jobId",
  "plan",
]);

// createRenderJob builds the whole-plan RenderJob v1 wire value used by
// submitRender. It first crosses the strict canonical object boundary so
// accessors, unsafe numbers, mutable prototypes, cycles, and undefined values
// cannot influence field selection.
export function createRenderJob(input) {
  let normalized;
  try {
    normalized = decodeCanonicalJSON(encodeCanonicalValue(input, {
      maxInputBytes: MAX_RENDER_JOB_INPUT_BYTES,
    }), { maxInputBytes: MAX_RENDER_JOB_INPUT_BYTES });
  } catch (error) {
    throw new TypeError("@cutly/sdk: render job input is not safely canonicalizable", { cause: error });
  }
  requireObject(normalized, "render job input");
  requireExactKeys(normalized, exactInputKeys, "render job input");

  const { jobId, idempotencyKey, plan, executionManifest, artifactUri } = normalized;
  requireNonemptyString(jobId, "jobId");
  requireNonemptyString(idempotencyKey, "idempotencyKey");
  requireArtifactURI(artifactUri);
  requireObject(plan, "plan");
  requireObject(executionManifest, "executionManifest");

  const planFingerprint = requireDigest(plan.fingerprint, "plan.fingerprint");
  const manifestFingerprint = requireDigest(executionManifest.fingerprint, "executionManifest.fingerprint");
  const manifestPlanFingerprint = requireDigest(
    executionManifest.plan_fingerprint,
    "executionManifest.plan_fingerprint",
  );
  if (manifestPlanFingerprint !== planFingerprint) {
    throw new TypeError("@cutly/sdk: executionManifest is not bound to plan");
  }
  requireObject(plan.requested_range, "plan.requested_range");
  requireObject(plan.frame_range, "plan.frame_range");
  requireObject(plan.sample_range, "plan.sample_range");

  return decodeCanonicalJSON(encodeCanonicalValue({
    schema_version: 1n,
    id: jobId,
    idempotency_key: idempotencyKey,
    plan_fingerprint: planFingerprint,
    execution_manifest_fingerprint: manifestFingerprint,
    timeline_range: plan.requested_range,
    frame_range: plan.frame_range,
    sample_range: plan.sample_range,
    artifact_uri: artifactUri,
  }, { maxInputBytes: MAX_RENDER_JOB_INPUT_BYTES }), {
    maxInputBytes: MAX_RENDER_JOB_INPUT_BYTES,
  });
}

function requireObject(value, name) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`@cutly/sdk: ${name} must be an object`);
  }
}

function requireExactKeys(value, keys, name) {
  const actual = Object.keys(value).sort();
  if (actual.length !== keys.length || actual.some((key, index) => key !== keys[index])) {
    throw new TypeError(`@cutly/sdk: ${name} has unknown or missing fields`);
  }
}

function requireNonemptyString(value, name) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new TypeError(`@cutly/sdk: ${name} must be a non-empty string`);
  }
  return value;
}

function requireDigest(value, name) {
  if (typeof value !== "string" || !/^sha256:[0-9a-f]{64}$/.test(value)) {
    throw new TypeError(`@cutly/sdk: ${name} must be a canonical SHA-256 digest`);
  }
  return value;
}

function requireArtifactURI(value) {
  requireNonemptyString(value, "artifactUri");
  let parsed;
  try {
    parsed = new URL(value);
  } catch (error) {
    throw new TypeError("@cutly/sdk: artifactUri must be an absolute file URL", { cause: error });
  }
  if (parsed.protocol !== "file:" || parsed.hostname !== "" || parsed.username !== "" ||
      parsed.password !== "" || parsed.search !== "" || parsed.hash !== "" ||
      parsed.pathname === "/" || parsed.href !== value) {
    throw new TypeError("@cutly/sdk: artifactUri must be a canonical hostless non-root file URL");
  }
  let path;
  try {
    path = decodeURIComponent(parsed.pathname);
  } catch (error) {
    throw new TypeError("@cutly/sdk: artifactUri path is not valid percent encoding", { cause: error });
  }
  const segments = path.split("/");
  if (!path.startsWith("/") || path.includes("\0") || segments.slice(1).some((part) => part === "" || part === "." || part === "..")) {
    throw new TypeError("@cutly/sdk: artifactUri path must be absolute and clean");
  }
}
