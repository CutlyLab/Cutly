import { DOCUMENT_V2_PLAYER_PROFILE } from "./player.js";

export const DOCUMENT_V2_FRAME_PREVIEW_PROFILE = "cutly.preview-frame.document-v2.v2";
export const DOCUMENT_V2_AUDIO_PREVIEW_PROFILE = "cutly.preview-audio.document-v2.v2";

const digestPattern = /^sha256:[0-9a-f]{64}$/u;
const integerPattern = /^-?(?:0|[1-9][0-9]*)$/u;

export class DocumentV2PreviewError extends Error {
  constructor(code, detail, cause) {
    super(`@cutly/sdk Document v2 preview: ${code}: ${detail}`, cause === undefined ? undefined : { cause });
    this.name = "DocumentV2PreviewError";
    this.code = code;
  }
}

// DocumentV2PreviewAdapter binds headless presentation to one immutable v2
// plan/manifest pair. Host callbacks can use the native SDK, a daemon, or an
// in-process reference provider; stale work never reaches presentation.
export class DocumentV2PreviewAdapter {
  #planFingerprint;
  #manifestFingerprint;
  #renderFrame;
  #renderAudio;

  constructor({ planFingerprint, executionManifestFingerprint, renderFrame, renderAudio }) {
    this.#planFingerprint = digest(planFingerprint, "planFingerprint");
    this.#manifestFingerprint = digest(executionManifestFingerprint, "executionManifestFingerprint");
    if (typeof renderFrame !== "function") throw new TypeError("@cutly/sdk Document v2 preview: renderFrame callback is required");
    if (renderAudio !== undefined && typeof renderAudio !== "function") throw new TypeError("@cutly/sdk Document v2 preview: renderAudio must be a function");
    this.#renderFrame = renderFrame;
    this.#renderAudio = renderAudio;
    Object.freeze(this);
  }

  async renderCurrentFrame(player, { signal } = {}) {
    playerLike(player);
    aborted(signal);
    const token = player.requestFrame();
    if (token.profile !== DOCUMENT_V2_PLAYER_PROFILE) fail("PREVIEW_V2.PLAYER_PROFILE", "DocumentV2Player is required");
    const request = Object.freeze({
      profile: DOCUMENT_V2_FRAME_PREVIEW_PROFILE,
      plan_fingerprint: this.#planFingerprint,
      execution_manifest_fingerprint: this.#manifestFingerprint,
      composition_frame_index: exactInteger(token.compositionFrameIndex, "composition_frame_index"),
    });
    const raw = await this.#renderFrame(request, { signal });
    aborted(signal);
    const frame = validateFrame(raw, request);
    const presented = player.acceptFrame(token) === true;
    return Object.freeze({ request, frame, token, presented });
  }

  async renderAudioRange(range, { signal } = {}) {
    if (this.#renderAudio === undefined) fail("PREVIEW_V2.AUDIO_UNAVAILABLE", "renderAudio callback is not configured");
    aborted(signal);
    if (range === null || typeof range !== "object") fail("PREVIEW_V2.AUDIO_RANGE", "audio range is required");
    const first = exactInteger(range.first, "first");
    const count = exactInteger(range.count, "count");
    if (BigInt(count) < 0n) fail("PREVIEW_V2.AUDIO_RANGE", "count must be non-negative");
    const request = Object.freeze({
      profile: DOCUMENT_V2_AUDIO_PREVIEW_PROFILE,
      plan_fingerprint: this.#planFingerprint,
      execution_manifest_fingerprint: this.#manifestFingerprint,
      sample_range: Object.freeze({ first, count }),
    });
    const raw = await this.#renderAudio(request, { signal });
    aborted(signal);
    return validateAudio(raw, request);
  }
}

function validateFrame(value, request) {
  plain(value, "frame result");
  exactKeys(value, ["profile", "plan_fingerprint", "execution_manifest_fingerprint", "composition_frame_index", "content_hash", "uri"], "frame result");
  if (value.profile !== DOCUMENT_V2_FRAME_PREVIEW_PROFILE || value.plan_fingerprint !== request.plan_fingerprint || value.execution_manifest_fingerprint !== request.execution_manifest_fingerprint || exactInteger(value.composition_frame_index, "composition_frame_index") !== request.composition_frame_index) {
    fail("PREVIEW_V2.FRAME_BINDING", "frame result is not bound to the exact request");
  }
  digest(value.content_hash, "content_hash");
  identity(value.uri, "uri");
  return Object.freeze({ ...value });
}

function validateAudio(value, request) {
  plain(value, "audio result");
  exactKeys(value, ["profile", "plan_fingerprint", "execution_manifest_fingerprint", "sample_range", "content_hash", "pcm_profile"], "audio result");
  plain(value.sample_range, "sample_range");
  exactKeys(value.sample_range, ["first", "count"], "sample_range");
  if (value.profile !== DOCUMENT_V2_AUDIO_PREVIEW_PROFILE || value.plan_fingerprint !== request.plan_fingerprint || value.execution_manifest_fingerprint !== request.execution_manifest_fingerprint || exactInteger(value.sample_range.first, "sample_range.first") !== request.sample_range.first || exactInteger(value.sample_range.count, "sample_range.count") !== request.sample_range.count) {
    fail("PREVIEW_V2.AUDIO_BINDING", "audio result is not bound to the exact request");
  }
  digest(value.content_hash, "content_hash");
  identity(value.pcm_profile, "pcm_profile");
  return Object.freeze({ ...value, sample_range: Object.freeze({ ...value.sample_range }) });
}

function playerLike(player) {
  if (player === null || typeof player !== "object" || typeof player.requestFrame !== "function" || typeof player.acceptFrame !== "function") throw new TypeError("@cutly/sdk Document v2 preview: player must expose requestFrame and acceptFrame");
}
function plain(value, name) { if (value === null || typeof value !== "object" || Array.isArray(value) || Object.getPrototypeOf(value) !== Object.prototype) fail("PREVIEW_V2.RESULT_INVALID", `${name} must be a plain object`); }
function exactKeys(value, names, label) { const got = Object.keys(value).sort(); const wanted = [...names].sort(); if (got.length !== wanted.length || got.some((name, index) => name !== wanted[index])) fail("PREVIEW_V2.RESULT_INVALID", `${label} has unknown or missing fields`); }
function exactInteger(value, name) { const text = typeof value === "bigint" ? value.toString() : typeof value === "number" && Number.isSafeInteger(value) ? String(value) : value; if (typeof text !== "string" || !integerPattern.test(text)) fail("PREVIEW_V2.INTEGER", `${name} must be an exact integer`); return text; }
function digest(value, name) { if (typeof value !== "string" || !digestPattern.test(value)) fail("PREVIEW_V2.DIGEST", `${name} must be canonical sha256`); return value; }
function identity(value, name) { if (typeof value !== "string" || value.length === 0 || value.length > 4096 || value.trim() !== value) fail("PREVIEW_V2.IDENTITY", `${name} is invalid`); return value; }
function aborted(signal) { if (signal?.aborted) throw signal.reason ?? new DOMException("The operation was aborted", "AbortError"); }
function fail(code, detail, cause) { throw new DocumentV2PreviewError(code, detail, cause); }
