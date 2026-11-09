import {
  canonicalizeNumberPreservingJSON,
  encodeCanonicalValue,
  extractNumberPreservingRootMember,
} from "./canonical-json.js";

export const AGENT_V2_SCHEMA_VERSION = 2;
export const AGENT_V2_REQUEST_PROFILE = "cutly.agent-request.document-v2.v2";
export const AGENT_V2_RESULT_PROFILE = "cutly.agent-result.document-v2.v2";
export const AGENT_V2_MEDIA_TYPE = "application/cutly-agent-document-v2+json";

const MAX_REQUEST_BYTES = 160 << 20;
const MAX_RESULT_BYTES = 192 << 20;
const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder("utf-8", { fatal: true });
const actions = Object.freeze({
  cutly_project: new Set(["inspect", "status", "history", "capabilities"]),
  cutly_edit: new Set(["plan", "apply", "discard", "revert"]),
  cutly_render: new Set(["plan", "prepare", "preflight", "frame", "submit", "status", "history", "cancel"]),
});

export class AgentProtocolV2Error extends Error {
  constructor(code, detail, cause) {
    super(`@cutly/sdk: ${code}: ${detail}`, cause === undefined ? undefined : { cause });
    this.name = "AgentProtocolV2Error";
    this.code = code;
  }
}

export function createAgentRequestV2({ requestId, tool, action, projectId, payload = Object.freeze({}) }) {
  identity(requestId, "requestId");
  identity(projectId, "projectId");
  if (!actions[tool]?.has(action)) fail("AGENT_V2.REQUEST_INVALID", "tool/action is unsupported");
  const payloadBytes = payload instanceof Uint8Array || typeof payload === "string"
    ? canonicalExact(payload, MAX_REQUEST_BYTES, "payload")
    : encodeCanonicalValue(payload, { maxInputBytes: MAX_REQUEST_BYTES });
  if (textDecoder.decode(payloadBytes)[0] !== "{") fail("AGENT_V2.REQUEST_INVALID", "payload must be an object");
  const value = {
    action,
    payload_profile: payloadProfile(tool, action),
    profile: AGENT_V2_REQUEST_PROFILE,
    project_id: projectId,
    request_id: requestId,
    schema_version: 2n,
    tool,
  };
  const fields = [];
  for (const name of Object.keys(value).sort()) {
    fields.push(`${JSON.stringify(name)}:${textDecoder.decode(encodeCanonicalValue(value[name]))}`);
  }
  fields.splice(fields.findIndex((field) => field.startsWith('"payload_profile"')), 0, `"payload":${textDecoder.decode(payloadBytes)}`);
  const canonical = textEncoder.encode(`{${fields.join(",")}}`);
  if (canonical.byteLength > MAX_REQUEST_BYTES) fail("AGENT_V2.REQUEST_LIMIT", "request is too large");
  return Object.freeze({ requestId, tool, action, projectId, canonical });
}

export function createAgentTransactionRequestV2({ requestId, projectId, transaction }) {
  if (transaction === null || typeof transaction !== "object" || !(transaction.canonical instanceof Uint8Array)) {
    fail("AGENT_V2.REQUEST_INVALID", "transaction must be a verified Transaction v2 handle");
  }
  const payload = textEncoder.encode(`{"transaction":${textDecoder.decode(canonicalExact(transaction.canonical, MAX_REQUEST_BYTES, "transaction"))}}`);
  return createAgentRequestV2({ requestId, tool: "cutly_edit", action: "plan", projectId, payload });
}

export function decodeAgentResultV2(input) {
  const canonical = canonicalExact(input, MAX_RESULT_BYTES, "result");
  const get = (name) => extractNumberPreservingRootMember(canonical, name, { maxInputBytes: MAX_RESULT_BYTES });
  const scalar = (name) => JSON.parse(textDecoder.decode(get(name)));
  const schemaVersion = scalar("schema_version");
  const profile = scalar("profile");
  const ok = scalar("ok");
  const requestId = scalar("request_id");
  const tool = scalar("tool");
  const action = scalar("action");
  const projectId = scalar("project_id");
  const state = scalar("state");
  if (schemaVersion !== AGENT_V2_SCHEMA_VERSION || profile !== AGENT_V2_RESULT_PROFILE || typeof ok !== "boolean" || !actions[tool]?.has(action)) {
    fail("AGENT_V2.RESULT_INVALID", "unsupported or malformed result envelope");
  }
  for (const [name, value] of [["request_id", requestId], ["project_id", projectId], ["state", state]]) identity(value, name);
  const diagnostics = JSON.parse(textDecoder.decode(get("diagnostics")));
  if (!Array.isArray(diagnostics)) fail("AGENT_V2.RESULT_INVALID", "diagnostics must be an array");
  const hasError = diagnostics.some((diagnostic) => diagnostic?.severity === "error");
  if (ok === hasError) fail("AGENT_V2.RESULT_INVALID", "ok contradicts diagnostics");
  const receipt = get("receipt");
  if (textDecoder.decode(receipt)[0] !== "{") fail("AGENT_V2.RESULT_INVALID", "receipt must be an object");
  return Object.freeze({ schemaVersion, profile, ok, requestId, tool, action, projectId, state, diagnostics: Object.freeze(diagnostics), receipt, canonical });
}

export class CutlyAgentV2Client {
  constructor(transport) {
    if (typeof transport === "function") this.transport = { roundTrip: transport };
    else if (transport && typeof transport.roundTrip === "function") this.transport = transport;
    else throw new TypeError("@cutly/sdk: Agent v2 transport must expose roundTrip(bytes, options)");
  }

  async call(request, { signal } = {}) {
    if (request === null || typeof request !== "object" || !(request.canonical instanceof Uint8Array)) fail("AGENT_V2.REQUEST_INVALID", "request handle is invalid");
    let raw;
    try { raw = await this.transport.roundTrip(request.canonical.slice(), { signal }); }
    catch (error) { fail("AGENT_V2.TRANSPORT", "request failed", error); }
    const result = decodeAgentResultV2(raw);
    if (result.requestId !== request.requestId || result.tool !== request.tool || result.action !== request.action || result.projectId !== request.projectId) {
      fail("AGENT_V2.CORRELATION", "result identity differs from request");
    }
    return result;
  }

  callAction(input, options) { return this.call(createAgentRequestV2(input), options); }
  inspectProject({ requestId, projectId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_project", action: "inspect" }, options); }
  projectStatus({ requestId, projectId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_project", action: "status" }, options); }
  projectHistory({ requestId, projectId, afterSequence = 0n, limit = 0n }, options) { return this.callAction({ requestId, projectId, tool: "cutly_project", action: "history", payload: { after_sequence: afterSequence, limit } }, options); }
  planEdit({ requestId, projectId, transaction }, options) { return this.call(createAgentTransactionRequestV2({ requestId, projectId, transaction }), options); }
  applyEdit({ requestId, projectId, transactionId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_edit", action: "apply", payload: { transaction_id: transactionId } }, options); }
  discardEdit({ requestId, projectId, transactionId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_edit", action: "discard", payload: { transaction_id: transactionId } }, options); }
  revertEdit({ requestId, projectId, targetTransactionId, transactionId, idempotencyKey }, options) { return this.callAction({ requestId, projectId, tool: "cutly_edit", action: "revert", payload: { target_transaction_id: targetTransactionId, transaction_id: transactionId, idempotency_key: idempotencyKey } }, options); }
  capabilities({ requestId, projectId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_project", action: "capabilities" }, options); }
  planRender({ requestId, projectId, range, target }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "plan", payload: { range, target } }, options); }
  prepareRender({ requestId, projectId, environment }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "prepare", payload: environment instanceof Uint8Array || typeof environment === "string" ? textEncoder.encode(`{"environment":${textDecoder.decode(canonicalExact(environment, MAX_REQUEST_BYTES, "environment"))}}`) : { environment } }, options); }
  preflightRender({ requestId, projectId, planFingerprint, executionManifestFingerprint }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "preflight", payload: { plan_fingerprint: planFingerprint, execution_manifest_fingerprint: executionManifestFingerprint } }, options); }
  renderFrame({ requestId, projectId, planFingerprint, executionManifestFingerprint, compositionFrameIndex }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "frame", payload: { profile: "cutly.preview-frame.document-v2.v2", plan_fingerprint: planFingerprint, execution_manifest_fingerprint: executionManifestFingerprint, composition_frame_index: compositionFrameIndex } }, options); }
  submitRender({ requestId, projectId, job }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "submit", payload: { job } }, options); }
  renderStatus({ requestId, projectId, jobId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "status", payload: { job_id: jobId } }, options); }
  renderHistory({ requestId, projectId, jobId, afterSequence = 0n, limit = 0n }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "history", payload: { job_id: jobId, after_sequence: afterSequence, limit } }, options); }
  cancelRender({ requestId, projectId, jobId }, options) { return this.callAction({ requestId, projectId, tool: "cutly_render", action: "cancel", payload: { job_id: jobId } }, options); }
}

export class HTTPAgentV2Transport {
  constructor(endpoint, { fetch: fetchImplementation = globalThis.fetch } = {}) {
    const parsed = new URL(endpoint);
    if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.username || parsed.password || parsed.hash) throw new TypeError("@cutly/sdk: invalid Agent v2 endpoint");
    if (typeof fetchImplementation !== "function") throw new TypeError("@cutly/sdk: fetch implementation is required");
    this.endpoint = parsed.href;
    this.fetch = fetchImplementation;
  }

  async roundTrip(request, { signal } = {}) {
    const response = await this.fetch(this.endpoint, { method: "POST", headers: { "content-type": AGENT_V2_MEDIA_TYPE, accept: AGENT_V2_MEDIA_TYPE }, body: request, signal, redirect: "error" });
    const body = new Uint8Array(await response.arrayBuffer());
    if (!response.ok) fail("AGENT_V2.TRANSPORT", `HTTP status ${response.status}`);
    if (response.headers.get("content-type")?.split(";", 1)[0].trim() !== AGENT_V2_MEDIA_TYPE) fail("AGENT_V2.TRANSPORT", "unexpected Content-Type");
    if (body.byteLength > MAX_RESULT_BYTES) fail("AGENT_V2.TRANSPORT", "response is too large");
    return body;
  }
}

function payloadProfile(tool, action) { return `cutly.agent-payload.${tool.slice("cutly_".length)}.${action}.document-v2.v2`; }
function canonicalExact(input, maximum, name) {
  const bytes = typeof input === "string" ? textEncoder.encode(input) : input;
  if (!(bytes instanceof Uint8Array) || bytes.byteLength === 0 || bytes.byteLength > maximum) fail(`AGENT_V2.${name.toUpperCase()}_INVALID`, `${name} is empty or too large`);
  const canonical = canonicalizeNumberPreservingJSON(bytes, { maxInputBytes: maximum, maxDepth: 256, maxValues: 1 << 20, maxStringBytes: maximum, maxIntegerDigits: 4096 });
  if (canonical.byteLength !== bytes.byteLength || canonical.some((value, index) => value !== bytes[index])) fail(`AGENT_V2.${name.toUpperCase()}_INVALID`, `${name} must be exact canonical JSON`);
  return canonical;
}
function identity(value, name) { if (typeof value !== "string" || value.length === 0 || value.length > 4096 || value.trim() !== value) fail("AGENT_V2.IDENTITY_INVALID", `${name} is invalid`); }
function fail(code, detail, cause) { throw new AgentProtocolV2Error(code, detail, cause); }
