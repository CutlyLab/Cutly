import { decodeCanonicalJSON, digestCanonicalValue, encodeCanonicalValue } from "./canonical-json.js";

const MAX_PREVIEW_REQUEST_BYTES = 4 << 20;
const MAX_PREVIEW_RESULT_BYTES = 16 << 20;
const INT64_MIN = -(1n << 63n);
const INT64_MAX = (1n << 63n) - 1n;
const EXACT_FRAME_PROFILE = "cutly.agent-render-frame.v1";
const PNG_PROFILE = "cutly.png.rgba8.ffmpeg.single-frame.v1";

const exactFrameInputKeys = Object.freeze([
  "artifactUri",
  "compositionFrameIndex",
  "executionManifest",
  "id",
  "idempotencyKey",
  "plan",
]);

const receiptKeys = Object.freeze(["artifact", "profile", "request"]);
const artifactKeys = Object.freeze([
  "backend_build_fingerprint",
  "backend_id",
  "composition_frame_index",
  "content_hash",
  "decoded_content_hash",
  "decoded_video_profile",
  "evaluation",
  "evaluation_fingerprint",
  "execution_manifest_fingerprint",
  "height",
  "plan_fingerprint",
  "png_encoding_profile",
  "reference_profile",
  "size_bytes",
  "timeline_time",
  "uri",
  "width",
]);

export class PreviewFrameError extends Error {
  constructor(code, detail, cause) {
    super(`@cutly/sdk preview: ${code}: ${detail}`, cause === undefined ? undefined : { cause });
    this.name = "PreviewFrameError";
    this.code = code;
  }
}

// createExactFrameRequest constructs the standalone ExactFrameRequest v1 wire
// value consumed by CutlyClient.renderFrame. Input first crosses the canonical
// boundary, so accessors, unsafe numbers, cycles, undefined values and mutable
// prototypes cannot influence the selected identities or ranges.
export function createExactFrameRequest(input) {
  const normalized = canonicalClone(input, "exact frame request input");
  requireObject(normalized, "exact frame request input");
  requireExactKeys(normalized, exactFrameInputKeys, "exact frame request input");

  const { id, idempotencyKey, plan, executionManifest, compositionFrameIndex, artifactUri } = normalized;
  requireNonemptyString(id, "id");
  requireNonemptyString(idempotencyKey, "idempotencyKey");
  requireObject(plan, "plan");
  requireObject(executionManifest, "executionManifest");
  const planFingerprint = requireDigest(plan.fingerprint, "plan.fingerprint");
  const manifestFingerprint = requireDigest(executionManifest.fingerprint, "executionManifest.fingerprint");
  if (requireDigest(executionManifest.plan_fingerprint, "executionManifest.plan_fingerprint") !== planFingerprint) {
    fail("PREVIEW.REQUEST_INVALID", "executionManifest is not bound to plan");
  }
  const frameIndex = requireInt64(compositionFrameIndex, "compositionFrameIndex");
  requireFrameInPlan(plan, frameIndex);
  requirePNGArtifactURI(artifactUri);

  return canonicalClone({
    schema_version: 1n,
    id,
    idempotency_key: idempotencyKey,
    plan_fingerprint: planFingerprint,
    execution_manifest_fingerprint: manifestFingerprint,
    composition_frame_index: frameIndex,
    artifact_uri: artifactUri,
  }, "exact frame request");
}

// AgentPreviewAdapter connects a HeadlessPlayer-compatible token source to the
// exact-frame Agent action. Presentation is deliberately last: a response must
// be successful, strictly bound to the request, and still current according to
// HeadlessPlayer.acceptFrame before it can replace the visible frame.
export class AgentPreviewAdapter {
  #client;
  #projectId;
  #plan;
  #executionManifest;
  #requestIDForFrame;
  #artifactURIForFrame;

  constructor(config) {
    requirePlainConfig(config);
    this.#client = dataProperty(config, "client");
    this.#projectId = dataProperty(config, "projectId");
    this.#requestIDForFrame = dataProperty(config, "requestIDForFrame");
    this.#artifactURIForFrame = dataProperty(config, "artifactURIForFrame");
    if (this.#client === null || typeof this.#client !== "object" || typeof this.#client.renderFrame !== "function") {
      throw new TypeError("@cutly/sdk preview: client must expose renderFrame(input, options)");
    }
    requireNonemptyString(this.#projectId, "projectId");
    if (typeof this.#requestIDForFrame !== "function" || typeof this.#artifactURIForFrame !== "function") {
      throw new TypeError("@cutly/sdk preview: requestIDForFrame and artifactURIForFrame must be functions");
    }
    this.#plan = canonicalClone(dataProperty(config, "plan"), "plan");
    this.#executionManifest = canonicalClone(dataProperty(config, "executionManifest"), "executionManifest");
    requirePreviewEnvironment(this.#plan, this.#executionManifest);
    Object.freeze(this);
  }

  async renderCurrentFrame(player, { signal } = {}) {
    if (player === null || typeof player !== "object" ||
        typeof player.requestFrame !== "function" || typeof player.acceptFrame !== "function") {
      throw new TypeError("@cutly/sdk preview: player must expose requestFrame() and acceptFrame(token)");
    }
    throwIfAborted(signal);
    const token = validatePlayerToken(player.requestFrame());
    const requestID = this.#requestIDForFrame(token);
    const artifactURI = this.#artifactURIForFrame(token);
    requireNonemptyString(requestID, "requestIDForFrame result");
    requirePNGArtifactURI(artifactURI);
    const frame = createExactFrameRequest({
      id: requestID,
      idempotencyKey: requestID,
      plan: this.#plan,
      executionManifest: this.#executionManifest,
      compositionFrameIndex: token.compositionFrameIndex,
      artifactUri: artifactURI,
    });

    const returned = await this.#client.renderFrame({
      requestId: requestID,
      projectId: this.#projectId,
      plan: this.#plan,
      executionManifest: this.#executionManifest,
      frame,
    }, { signal });
    throwIfAborted(signal);
    const result = canonicalClone(returned, "Agent frame result", MAX_PREVIEW_RESULT_BYTES);
    validateCorrelation(result, requestID, this.#projectId);
    if (result.ok !== true) {
      const code = Array.isArray(result.diagnostics) && result.diagnostics.length > 0 &&
        typeof result.diagnostics[0]?.code === "string" ? result.diagnostics[0].code : "unknown";
      fail("PREVIEW.RENDER_REJECTED", `exact-frame render was rejected (${code})`);
    }
    const artifact = await validateFrameReceipt(result, frame, this.#executionManifest);
    throwIfAborted(signal);
    const presented = player.acceptFrame(token) === true;
    return Object.freeze({ result, request: frame, artifact, token, presented });
  }
}

function validateCorrelation(result, requestID, projectId) {
  requireObject(result, "Agent frame result");
  if ((result.protocol_version !== 1n && result.protocol_version !== 1) ||
      result.request_id !== requestID || result.tool !== "cutly_render" || result.action !== "frame" ||
      result.project_id !== projectId) {
    fail("PREVIEW.RESULT_INVALID", "Agent frame result is not correlated to the request");
  }
}

async function validateFrameReceipt(result, frame, manifest) {
  if (result.state !== "complete") {
    fail("PREVIEW.RESULT_INVALID", "successful exact-frame result must be complete");
  }
  const receipt = result.receipt;
  requireObject(receipt, "frame receipt");
  requireExactKeys(receipt, receiptKeys, "frame receipt");
  if (receipt.profile !== EXACT_FRAME_PROFILE) {
    fail("PREVIEW.RECEIPT_INVALID", "frame receipt profile is unsupported");
  }
  if (!canonicalEqual(receipt.request, frame)) {
    fail("PREVIEW.RECEIPT_INVALID", "frame receipt request does not match the submitted request");
  }
  const artifact = receipt.artifact;
  requireObject(artifact, "frame artifact");
  requireExactKeys(artifact, artifactKeys, "frame artifact");
  requireDigest(artifact.content_hash, "frame artifact content_hash");
  requireDigest(artifact.decoded_content_hash, "frame artifact decoded_content_hash");
  requireDigest(artifact.evaluation_fingerprint, "frame artifact evaluation_fingerprint");
  if (artifact.uri !== frame.artifact_uri || artifact.plan_fingerprint !== frame.plan_fingerprint ||
      artifact.execution_manifest_fingerprint !== frame.execution_manifest_fingerprint ||
      artifact.composition_frame_index !== frame.composition_frame_index ||
      artifact.backend_id !== manifest.backend.backend_id ||
      artifact.backend_build_fingerprint !== manifest.backend.build_fingerprint ||
      artifact.reference_profile !== manifest.output.reference_profile ||
      artifact.decoded_video_profile !== manifest.output.decoded_video_profile ||
      artifact.png_encoding_profile !== PNG_PROFILE || artifact.width !== manifest.output.width ||
      artifact.height !== manifest.output.height) {
    fail("PREVIEW.RECEIPT_INVALID", "frame artifact is not bound to the request and execution manifest");
  }
  if (requireInt64(artifact.size_bytes, "frame artifact size_bytes") <= 0n ||
      requireInt64(artifact.width, "frame artifact width") <= 0n ||
      requireInt64(artifact.height, "frame artifact height") <= 0n) {
    fail("PREVIEW.RECEIPT_INVALID", "frame artifact size and dimensions must be positive");
  }
  requireObject(artifact.timeline_time, "frame artifact timeline_time");
  requireObject(artifact.evaluation, "frame artifact evaluation");
  requireObject(artifact.evaluation.timeline_time, "frame artifact evaluation.timeline_time");
  if (artifact.evaluation.composition_frame_index !== frame.composition_frame_index ||
      !canonicalEqual(artifact.timeline_time, artifact.evaluation.timeline_time)) {
    fail("PREVIEW.RECEIPT_INVALID", "frame artifact evaluation time does not match the requested frame");
  }
  let evaluationFingerprint;
  try {
    evaluationFingerprint = (await digestCanonicalValue(artifact.evaluation, {
      maxInputBytes: MAX_PREVIEW_RESULT_BYTES,
    })).digest;
  } catch (error) {
    fail("PREVIEW.RECEIPT_INVALID", "frame artifact evaluation cannot be fingerprinted", error);
  }
  if (artifact.evaluation_fingerprint !== evaluationFingerprint) {
    fail("PREVIEW.RECEIPT_INVALID", "frame artifact evaluation fingerprint does not match evaluation");
  }
  return artifact;
}

function requirePreviewEnvironment(plan, manifest) {
  requireObject(plan, "plan");
  requireObject(manifest, "executionManifest");
  const planFingerprint = requireDigest(plan.fingerprint, "plan.fingerprint");
  if (requireDigest(manifest.plan_fingerprint, "executionManifest.plan_fingerprint") !== planFingerprint) {
    fail("PREVIEW.REQUEST_INVALID", "executionManifest is not bound to plan");
  }
  requireDigest(manifest.fingerprint, "executionManifest.fingerprint");
  requireFrameInPlan(plan, requireInt64(plan.frame_range?.first, "plan.frame_range.first"));
  requireObject(manifest.backend, "executionManifest.backend");
  requireNonemptyString(manifest.backend.backend_id, "executionManifest.backend.backend_id");
  requireDigest(manifest.backend.build_fingerprint, "executionManifest.backend.build_fingerprint");
  requireObject(manifest.output, "executionManifest.output");
  requireNonemptyString(manifest.output.reference_profile, "executionManifest.output.reference_profile");
  requireNonemptyString(manifest.output.decoded_video_profile, "executionManifest.output.decoded_video_profile");
  if (requireInt64(manifest.output.width, "executionManifest.output.width") <= 0n ||
      requireInt64(manifest.output.height, "executionManifest.output.height") <= 0n) {
    fail("PREVIEW.REQUEST_INVALID", "executionManifest output dimensions must be positive");
  }
}

function requireFrameInPlan(plan, frameIndex) {
  requireObject(plan.frame_range, "plan.frame_range");
  const first = requireInt64(plan.frame_range.first, "plan.frame_range.first");
  const count = requireInt64(plan.frame_range.count, "plan.frame_range.count");
  if (count <= 0n || first + count > INT64_MAX || frameIndex < first || frameIndex >= first + count) {
    fail("PREVIEW.REQUEST_INVALID", "composition frame lies outside plan.frame_range");
  }
}

function validatePlayerToken(value) {
  const token = canonicalClone(value, "player frame token");
  requireObject(token, "player frame token");
  requireExactKeys(token, ["compositionFrameIndex", "profile", "requestID", "revision", "seekEpoch"], "player frame token");
  if (token.profile !== "cutly.player.exact-audio-master.v1") {
    fail("PREVIEW.PLAYER_TOKEN_INVALID", "player frame token profile is unsupported");
  }
  requireNonemptyString(token.revision, "player token revision");
  requireNonemptyString(token.seekEpoch, "player token seekEpoch");
  requireNonemptyString(token.requestID, "player token requestID");
  requireInt64(token.compositionFrameIndex, "player token compositionFrameIndex");
  return token;
}

function canonicalClone(value, name, maxInputBytes = MAX_PREVIEW_REQUEST_BYTES) {
  try {
    return decodeCanonicalJSON(encodeCanonicalValue(value, { maxInputBytes }), {
      maxInputBytes,
    });
  } catch (error) {
    fail("PREVIEW.VALUE_INVALID", `${name} is not safely canonicalizable`, error);
  }
}

function canonicalEqual(left, right) {
  try {
    const leftBytes = encodeCanonicalValue(left, { maxInputBytes: MAX_PREVIEW_REQUEST_BYTES });
    const rightBytes = encodeCanonicalValue(right, { maxInputBytes: MAX_PREVIEW_REQUEST_BYTES });
    if (leftBytes.byteLength !== rightBytes.byteLength) return false;
    return leftBytes.every((value, index) => value === rightBytes[index]);
  } catch {
    return false;
  }
}

function requirePlainConfig(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value) || Object.getPrototypeOf(value) !== Object.prototype) {
    throw new TypeError("@cutly/sdk preview: config must be a plain object");
  }
  requireExactKeys(value, ["artifactURIForFrame", "client", "executionManifest", "plan", "projectId", "requestIDForFrame"], "preview config");
}

function dataProperty(value, name) {
  const descriptor = Object.getOwnPropertyDescriptor(value, name);
  if (descriptor === undefined || !("value" in descriptor)) {
    throw new TypeError(`@cutly/sdk preview: ${name} must be a data property`);
  }
  return descriptor.value;
}

function requireObject(value, name) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    fail("PREVIEW.VALUE_INVALID", `${name} must be an object`);
  }
}

function requireExactKeys(value, wanted, name) {
  const actual = Object.keys(value).sort();
  if (actual.length !== wanted.length || actual.some((key, index) => key !== wanted[index])) {
    fail("PREVIEW.VALUE_INVALID", `${name} has unknown or missing fields`);
  }
}

function requireNonemptyString(value, name) {
  if (typeof value !== "string" || value.trim() === "") {
    fail("PREVIEW.VALUE_INVALID", `${name} must be a non-empty string`);
  }
  return value;
}

function requireDigest(value, name) {
  if (typeof value !== "string" || !/^sha256:[0-9a-f]{64}$/.test(value)) {
    fail("PREVIEW.VALUE_INVALID", `${name} must be a canonical SHA-256 digest`);
  }
  return value;
}

function requireInt64(value, name) {
  let integer;
  if (typeof value === "bigint") integer = value;
  else if (typeof value === "number" && Number.isSafeInteger(value)) integer = BigInt(value);
  else if (typeof value === "string" && /^-?(0|[1-9][0-9]*)$/.test(value)) integer = BigInt(value);
  else fail("PREVIEW.VALUE_INVALID", `${name} must be an exact integer`);
  if (integer < INT64_MIN || integer > INT64_MAX) {
    fail("PREVIEW.VALUE_INVALID", `${name} is outside int64`);
  }
  return integer;
}

function requirePNGArtifactURI(value) {
  requireNonemptyString(value, "artifactUri");
  let parsed;
  try {
    parsed = new URL(value);
  } catch (error) {
    fail("PREVIEW.VALUE_INVALID", "artifactUri must be an absolute file URL", error);
  }
  if (parsed.protocol !== "file:" || parsed.hostname !== "" || parsed.username !== "" ||
      parsed.password !== "" || parsed.search !== "" || parsed.hash !== "" || parsed.href !== value) {
    fail("PREVIEW.VALUE_INVALID", "artifactUri must be a canonical hostless file URL");
  }
  let path;
  try {
    path = decodeURIComponent(parsed.pathname);
  } catch (error) {
    fail("PREVIEW.VALUE_INVALID", "artifactUri path is not valid percent encoding", error);
  }
  const segments = path.split("/");
  const base = segments[segments.length - 1];
  if (!path.startsWith("/") || path.includes("\0") || path === "/" ||
      segments.slice(1).some((part) => part === "" || part === "." || part === "..") ||
      base === ".png" || !base.endsWith(".png")) {
    fail("PREVIEW.VALUE_INVALID", "artifactUri must name a clean absolute .png file");
  }
  return value;
}

function throwIfAborted(signal) {
  if (signal === undefined) return;
  if (signal === null || typeof signal !== "object" || typeof signal.aborted !== "boolean") {
    throw new TypeError("@cutly/sdk preview: signal must be an AbortSignal");
  }
  if (!signal.aborted) return;
  if (signal.reason !== undefined) throw signal.reason;
  const error = new Error("The operation was aborted");
  error.name = "AbortError";
  throw error;
}

function fail(code, detail, cause) {
  throw new PreviewFrameError(code, detail, cause);
}
