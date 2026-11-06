import {
  CANONICAL_JSON_PROFILE,
  canonicalizeJSON,
  decodeCanonicalJSON,
  encodeCanonicalValue,
} from "./canonical-json.js";

export const AGENT_PROTOCOL_VERSION = 1n;
export const AGENT_MEDIA_TYPE = "application/cutly-agent+json";
export const MAX_AGENT_REQUEST_BYTES = 4 << 20;
export const MAX_AGENT_RESULT_BYTES = 16 << 20;

const MAX_IDENTITY_SCALARS = 4096;
const MAX_IDENTITY_BYTES = MAX_IDENTITY_SCALARS * 4;
const MAX_MESSAGE_SCALARS = 65536;
const MAX_MESSAGE_BYTES = MAX_MESSAGE_SCALARS * 4;
const MAX_LIST_ITEMS = 4096;

const ACTIONS = Object.freeze({
  cutly_project: Object.freeze(new Set(["inspect", "status", "history", "capabilities"])),
  cutly_edit: Object.freeze(new Set(["plan", "apply", "discard", "revert"])),
  cutly_render: Object.freeze(new Set(["preflight", "frame", "render", "status", "cancel"])),
});

const textEncoder = new TextEncoder();

export class AgentProtocolError extends Error {
  constructor(code, detail, cause) {
    super(`@cutly/sdk: ${code}: ${detail}`, cause === undefined ? undefined : { cause });
    this.name = "AgentProtocolError";
    this.code = code;
  }
}

// Request IDs are caller-owned so an exact retry keeps the same identity. The
// SDK deliberately does not hide crypto/randomness inside protocol values.
export function createAgentRequest(input) {
  let safeInput;
  try {
    requirePlainObject(input, "request input");
    requireExactKeys(input, new Set(["requestId", "tool", "action", "projectId", "payload"]), "request input", "AGENT.REQUEST_INVALID");
    safeInput = decodeCanonicalJSON(encodeCanonicalValue(input, { maxInputBytes: MAX_AGENT_REQUEST_BYTES }));
  } catch (error) {
    if (error instanceof AgentProtocolError) throw error;
    fail("AGENT.REQUEST_INVALID", "request input is not safely encodable", error);
  }
  const {
    requestId,
    tool,
    action,
    projectId = "",
    payload = Object.freeze(Object.create(null)),
  } = safeInput;
  requireIdentity(requestId, "request_id");
  requireToolAction(tool, action);
  if (projectId !== "") requireIdentity(projectId, "project_id");
  if (action !== "capabilities" && projectId === "") {
    fail("AGENT.REQUEST_INVALID", `project_id is required for action ${JSON.stringify(action)}`);
  }
  requirePlainObject(payload, "payload");
  const request = {
    protocol_version: AGENT_PROTOCOL_VERSION,
    request_id: requestId,
    tool,
    action,
    ...(projectId === "" ? {} : { project_id: projectId }),
    payload,
  };
  // Normalize through the exact writer now, before any transport side effect.
  return decodeCanonicalJSON(encodeCanonicalValue(request, { maxInputBytes: MAX_AGENT_REQUEST_BYTES }));
}

export class CutlyClient {
  constructor(transport) {
    if (typeof transport === "function") this.transport = { roundTrip: transport };
    else if (transport && typeof transport.roundTrip === "function") this.transport = transport;
    else throw new TypeError("@cutly/sdk: transport must be a function or expose roundTrip(request, options)");
  }

  async call(request, { signal } = {}) {
    const normalized = validateRequest(request);
    const encoded = encodeCanonicalValue(normalized, { maxInputBytes: MAX_AGENT_REQUEST_BYTES });
    let response;
    try {
      response = await this.transport.roundTrip(encoded.slice(), { signal });
    } catch (error) {
      if (error instanceof AgentProtocolError) throw error;
      fail("AGENT.TRANSPORT", "request failed", error);
    }
    if (typeof response !== "string" && !(response instanceof Uint8Array)) {
      fail("AGENT.TRANSPORT", "transport returned neither string nor Uint8Array");
    }
    let result;
    try {
      const canonical = canonicalizeJSON(response, { maxInputBytes: MAX_AGENT_RESULT_BYTES });
      result = decodeCanonicalJSON(canonical, { maxInputBytes: MAX_AGENT_RESULT_BYTES });
      validateResult(result);
    } catch (error) {
      if (error instanceof AgentProtocolError) throw error;
      fail("AGENT.RESULT_INVALID", "response is not a valid Agent result", error);
    }
    if (result.request_id !== normalized.request_id || result.tool !== normalized.tool || result.action !== normalized.action) {
      fail("AGENT.CORRELATION", "request_id, tool or action does not match the request");
    }
    if (normalized.project_id !== undefined && result.project_id !== normalized.project_id) {
      fail("AGENT.CORRELATION", "project_id does not match the request");
    }
    return result;
  }

  callAction(input, options) {
    return this.call(createAgentRequest(input), options);
  }

  inspectProject({ requestId, projectId }, options) {
    return this.callAction({ requestId, projectId, tool: "cutly_project", action: "inspect" }, options);
  }

  projectStatus({ requestId, projectId }, options) {
    return this.callAction({ requestId, projectId, tool: "cutly_project", action: "status" }, options);
  }

  projectHistory({ requestId, projectId, afterSequence = 0n, limit = 0n }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_project", action: "history",
      payload: { after_sequence: afterSequence, limit },
    }, options);
  }

  planEdit({ requestId, projectId, transaction }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_edit", action: "plan", payload: { transaction },
    }, options);
  }

  applyEdit({ requestId, projectId, transactionId, expectedRevision }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_edit", action: "apply",
      payload: { transaction_id: transactionId, expected_revision: expectedRevision },
    }, options);
  }

  discardEdit({ requestId, projectId, transactionId }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_edit", action: "discard", payload: { transaction_id: transactionId },
    }, options);
  }

  revertEdit({ requestId, projectId, transactionId, expectedRevision }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_edit", action: "revert",
      payload: { transaction_id: transactionId, expected_revision: expectedRevision },
    }, options);
  }

  preflightRender({ requestId, projectId, plan, executionManifest }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_render", action: "preflight",
      payload: { plan, execution_manifest: executionManifest },
    }, options);
  }

  renderFrame({ requestId, projectId, plan, executionManifest, frame }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_render", action: "frame",
      payload: { plan, execution_manifest: executionManifest, frame },
    }, options);
  }

  submitRender({ requestId, projectId, job }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_render", action: "render", payload: { job },
    }, options);
  }

  renderStatus({ requestId, projectId, jobId }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_render", action: "status", payload: { job_id: jobId },
    }, options);
  }

  cancelRender({ requestId, projectId, jobId }, options) {
    return this.callAction({
      requestId, projectId, tool: "cutly_render", action: "cancel", payload: { job_id: jobId },
    }, options);
  }
}

export class HTTPAgentTransport {
  #fetch;
  #headerEntries;

  constructor(endpoint, {
    fetch: fetchImplementation = globalThis.fetch,
    headers = {},
    maxResponseBytes = MAX_AGENT_RESULT_BYTES,
  } = {}) {
    let parsed;
    try {
      parsed = new URL(endpoint);
    } catch (error) {
      fail("AGENT.ENDPOINT_INVALID", "endpoint is not an absolute URL", error);
    }
    if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.username !== "" || parsed.password !== "" || parsed.hash !== "") {
      fail("AGENT.ENDPOINT_INVALID", "endpoint must be an http(s) URL without user info or fragment");
    }
    if (typeof fetchImplementation !== "function") {
      throw new TypeError("@cutly/sdk: Fetch API is unavailable; provide a fetch implementation");
    }
    if (!Number.isSafeInteger(maxResponseBytes) || maxResponseBytes <= 0 || maxResponseBytes > MAX_AGENT_RESULT_BYTES) {
      throw new RangeError(`@cutly/sdk: maxResponseBytes must be in [1, ${MAX_AGENT_RESULT_BYTES}]`);
    }
    this.endpoint = parsed.href;
    this.#fetch = fetchImplementation;
    const normalizedHeaders = new Headers(headers);
    normalizedHeaders.set("Content-Type", `${AGENT_MEDIA_TYPE}; profile=\"${CANONICAL_JSON_PROFILE}\"`);
    normalizedHeaders.set("Accept", `${AGENT_MEDIA_TYPE}, application/json`);
    this.#headerEntries = Object.freeze([...normalizedHeaders.entries()].map((entry) => Object.freeze(entry)));
    this.maxResponseBytes = maxResponseBytes;
    Object.freeze(this);
  }

  async roundTrip(canonicalRequest, { signal } = {}) {
    if (!(canonicalRequest instanceof Uint8Array) || canonicalRequest.byteLength > MAX_AGENT_REQUEST_BYTES) {
      fail("AGENT.REQUEST_INVALID", `encoded request must be at most ${MAX_AGENT_REQUEST_BYTES} bytes`);
    }
    const response = await this.#fetch(this.endpoint, {
      method: "POST",
      headers: new Headers(this.#headerEntries),
      body: canonicalRequest,
      signal,
      redirect: "error",
      credentials: "omit",
    });
    const bytes = await readBoundedBody(response, this.maxResponseBytes);
    if (!response.ok || response.status !== 200) {
      const detail = new TextDecoder().decode(bytes.subarray(0, 1024)).trim();
      fail("AGENT.HTTP_STATUS", `HTTP ${response.status}${detail === "" ? "" : `: ${detail}`}`);
    }
    const contentType = response.headers.get("Content-Type") ?? "";
    if (!validAgentContentType(contentType)) {
      fail("AGENT.CONTENT_TYPE", `unexpected Content-Type ${JSON.stringify(response.headers.get("Content-Type"))}`);
    }
    return bytes;
  }
}

function validAgentContentType(value) {
  // Keep this contract aligned with agent/httpapi.validContentType. Prefix
  // matching would accept unrelated values such as application/jsonp, while
  // ignoring parameters could silently negotiate a different JSON profile.
  const parts = value.split(";");
  const essence = parts.shift()?.trim().toLowerCase();
  if (essence !== AGENT_MEDIA_TYPE && essence !== "application/json") return false;
  if (parts.length === 0) return true;
  if (parts.length !== 1) return false;
  const parameter = parts[0].trim().match(/^profile\s*=\s*(?:"([^"]*)"|([^\s"]+))$/i);
  if (parameter === null) return false;
  return (parameter[1] ?? parameter[2]) === CANONICAL_JSON_PROFILE;
}

function validateRequest(request) {
  try {
    request = decodeCanonicalJSON(encodeCanonicalValue(request, { maxInputBytes: MAX_AGENT_REQUEST_BYTES }));
  } catch (error) {
    if (error instanceof AgentProtocolError) throw error;
    fail("AGENT.REQUEST_INVALID", "request is not safely encodable", error);
  }
  requirePlainObject(request, "request");
  requireExactKeys(request, new Set(["protocol_version", "request_id", "tool", "action", "project_id", "payload"]), "request", "AGENT.REQUEST_INVALID");
  if (request.protocol_version !== AGENT_PROTOCOL_VERSION && request.protocol_version !== 1) {
    fail("AGENT.REQUEST_INVALID", "protocol_version must be 1");
  }
  requireIdentity(request.request_id, "request_id");
  requireToolAction(request.tool, request.action);
  const projectId = request.project_id ?? "";
  if (projectId !== "") requireIdentity(projectId, "project_id");
  if (request.action !== "capabilities" && projectId === "") {
    fail("AGENT.REQUEST_INVALID", `project_id is required for action ${JSON.stringify(request.action)}`);
  }
  requirePlainObject(request.payload, "payload");
  return createAgentRequest({
    requestId: request.request_id,
    tool: request.tool,
    action: request.action,
    projectId,
    payload: request.payload,
  });
}

function validateResult(result) {
  requirePlainObject(result, "result");
  requireExactKeys(result, new Set([
    "protocol_version", "ok", "request_id", "tool", "action", "state", "project_id",
    "transaction_id", "job_id", "base_revision", "new_revision", "changed", "replayed",
    "render_required", "diagnostics", "diff", "receipt", "next_actions",
  ]), "result");
  if (result.protocol_version !== AGENT_PROTOCOL_VERSION) fail("AGENT.RESULT_INVALID", "protocol_version must be 1");
  if (typeof result.ok !== "boolean" || typeof result.changed !== "boolean" || typeof result.replayed !== "boolean" || typeof result.render_required !== "boolean") {
    fail("AGENT.RESULT_INVALID", "ok, changed, replayed and render_required must be booleans");
  }
  requireIdentity(result.request_id, "request_id");
  requireToolAction(result.tool, result.action);
  requireIdentity(result.state, "state");
  for (const field of ["project_id", "transaction_id", "job_id"]) {
    if (result[field] !== undefined) requireIdentity(result[field], field);
  }
  if (result.transaction_id !== undefined && result.tool !== "cutly_edit") fail("AGENT.RESULT_INVALID", "transaction_id is valid only for cutly_edit");
  if (result.job_id !== undefined && result.tool !== "cutly_render") fail("AGENT.RESULT_INVALID", "job_id is valid only for cutly_render");
  if (result.action !== "capabilities" && result.project_id === undefined) fail("AGENT.RESULT_INVALID", "project_id is required for this action");
  for (const field of ["base_revision", "new_revision"]) {
    if (result[field] !== undefined) requireUint64(result[field], field);
  }
  if (!Array.isArray(result.diagnostics) || !Array.isArray(result.next_actions)) {
    fail("AGENT.RESULT_INVALID", "diagnostics and next_actions must be arrays");
  }
  if (result.diagnostics.length > MAX_LIST_ITEMS || result.next_actions.length > MAX_LIST_ITEMS) {
    fail("AGENT.RESULT_INVALID", `diagnostics and next_actions may contain at most ${MAX_LIST_ITEMS} items`);
  }
  let hasError = false;
  for (const [index, diagnostic] of result.diagnostics.entries()) {
    requirePlainObject(diagnostic, `diagnostics[${index}]`);
    requireExactKeys(diagnostic, new Set(["code", "severity", "path", "message", "fixes"]), `diagnostics[${index}]`);
    requireIdentity(diagnostic.code, `diagnostics[${index}].code`);
    if (!new Set(["info", "warning", "error"]).has(diagnostic.severity)) fail("AGENT.RESULT_INVALID", `diagnostics[${index}].severity is invalid`);
    if (!validText(diagnostic.message, MAX_MESSAGE_BYTES, MAX_MESSAGE_SCALARS, true)) {
      fail("AGENT.RESULT_INVALID", `diagnostics[${index}].message is invalid`);
    }
    if (diagnostic.path !== undefined && !validText(diagnostic.path, MAX_IDENTITY_BYTES, MAX_IDENTITY_SCALARS, false)) {
      fail("AGENT.RESULT_INVALID", `diagnostics[${index}].path is invalid`);
    }
    if (diagnostic.fixes !== undefined) {
      if (!Array.isArray(diagnostic.fixes) || diagnostic.fixes.length > MAX_LIST_ITEMS) fail("AGENT.RESULT_INVALID", `diagnostics[${index}].fixes must be a bounded array`);
      for (const [fixIndex, fix] of diagnostic.fixes.entries()) {
        requirePlainObject(fix, `diagnostics[${index}].fixes[${fixIndex}]`);
        requireExactKeys(fix, new Set(["description", "operation", "side_effects"]), `diagnostics[${index}].fixes[${fixIndex}]`);
        if (!validText(fix.description, MAX_MESSAGE_BYTES, MAX_MESSAGE_SCALARS, true)) {
          fail("AGENT.RESULT_INVALID", `diagnostics[${index}].fixes[${fixIndex}].description is invalid`);
        }
        if (fix.operation !== undefined) requirePlainObject(fix.operation, `diagnostics[${index}].fixes[${fixIndex}].operation`);
        if (fix.side_effects !== undefined && (!Array.isArray(fix.side_effects) || fix.side_effects.length > MAX_LIST_ITEMS || fix.side_effects.some((value) => !validText(value, MAX_MESSAGE_BYTES, MAX_MESSAGE_SCALARS, false)))) {
          fail("AGENT.RESULT_INVALID", `diagnostics[${index}].fixes[${fixIndex}].side_effects is invalid`);
        }
      }
    }
    hasError ||= diagnostic.severity === "error";
  }
  if (result.ok === hasError) fail("AGENT.RESULT_INVALID", "ok must be false exactly when an error diagnostic is present");
  for (const [index, next] of result.next_actions.entries()) {
    requirePlainObject(next, `next_actions[${index}]`);
    requireExactKeys(next, new Set(["tool", "action", "project_id", "revision", "job_id"]), `next_actions[${index}]`);
    requireToolAction(next.tool, next.action);
    if (next.action !== "capabilities" && next.project_id === undefined) fail("AGENT.RESULT_INVALID", `next_actions[${index}].project_id is required`);
    if (next.project_id !== undefined) requireIdentity(next.project_id, `next_actions[${index}].project_id`);
    if (next.revision !== undefined) requireUint64(next.revision, `next_actions[${index}].revision`);
    if (next.job_id !== undefined) {
      requireIdentity(next.job_id, `next_actions[${index}].job_id`);
      if (next.tool !== "cutly_render") fail("AGENT.RESULT_INVALID", `next_actions[${index}].job_id is valid only for cutly_render`);
    }
  }
  for (const field of ["diff", "receipt"]) {
    if (result[field] !== undefined) requirePlainObject(result[field], field);
  }
}

async function readBoundedBody(response, maximum) {
  const declared = response.headers.get("Content-Length");
  if (declared !== null && (!/^\d+$/.test(declared) || BigInt(declared) > BigInt(maximum))) {
    fail("AGENT.RESPONSE_LIMIT", `response exceeds ${maximum} bytes`);
  }
  if (!response.body || typeof response.body.getReader !== "function") {
    const bytes = new Uint8Array(await response.arrayBuffer());
    if (bytes.byteLength > maximum) fail("AGENT.RESPONSE_LIMIT", `response exceeds ${maximum} bytes`);
    return bytes;
  }
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maximum) {
        try {
          await reader.cancel("Cutly Agent response limit exceeded");
        } catch {
          // The protocol limit remains the primary failure even if a hostile
          // stream also rejects cancellation.
        }
        fail("AGENT.RESPONSE_LIMIT", `response exceeds ${maximum} bytes`);
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const result = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return result;
}

function requireToolAction(tool, action) {
  if (typeof tool !== "string" || typeof action !== "string" || !ACTIONS[tool]?.has(action)) {
    fail("AGENT.TOOL_ACTION_INVALID", `unsupported tool/action ${JSON.stringify(tool)}/${JSON.stringify(action)}`);
  }
}

function requireIdentity(value, field) {
  if (!validText(value, MAX_IDENTITY_BYTES, MAX_IDENTITY_SCALARS, true) || value.replace(/[ \t\r\n]/g, "") === "") {
    fail("AGENT.IDENTITY_INVALID", `${field} must be a non-blank string no larger than ${MAX_IDENTITY_SCALARS} Unicode scalars`);
  }
}

function validText(value, maximumBytes, maximumScalars, required) {
  if (typeof value !== "string" || (required && value.length === 0) || textEncoder.encode(value).byteLength > maximumBytes) return false;
  let scalars = 0;
  for (const _ of value) {
    scalars += 1;
    if (scalars > maximumScalars) return false;
  }
  return true;
}

function requireUint64(value, field) {
  if (typeof value !== "bigint" || value < 0n || value > 18_446_744_073_709_551_615n) {
    fail("AGENT.RESULT_INVALID", `${field} must be a uint64 integer`);
  }
}

function requirePlainObject(value, field) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    fail("AGENT.OBJECT_REQUIRED", `${field} must be an object`);
  }
  const prototype = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) fail("AGENT.OBJECT_REQUIRED", `${field} must be a plain object`);
}

function requireExactKeys(value, allowed, field, code = "AGENT.RESULT_INVALID") {
  for (const key of Reflect.ownKeys(value)) {
    if (typeof key !== "string" || !allowed.has(key)) fail(code, `${field} contains unsupported member ${String(key)}`);
  }
}

function fail(code, detail, cause) {
  throw new AgentProtocolError(code, detail, cause);
}
