import {
  canonicalizeNumberPreservingJSON,
  digestNumberPreservingJSONWithoutRootMember,
  encodeCanonicalValue,
} from "./canonical-json.js";

export const TRANSACTION_V2_PROFILE = "cutly.transaction.document-v2.number-preserving.v2";
export const TRANSACTION_V2_SCHEMA_VERSION = 2;
export const MAX_TRANSACTION_V2_BYTES = 16 << 20;

const digestPattern = /^sha256:[0-9a-f]{64}$/;
const identityPattern = /^\S(?:[\s\S]{0,254}\S)?$/u;
const payloadForKind = Object.freeze({
  asset_add: "asset", asset_remove: "asset", asset_replace: "asset",
  timeline_add: "timeline", timeline_remove: "timeline", timeline_replace: "timeline",
  track_add: "track", track_remove: "track", track_replace: "track",
  clip_add: "clip", clip_remove: "clip", clip_replace: "clip", clip_delete: "clip",
  bus_add: "bus", bus_remove: "bus", bus_replace: "bus",
  clip_move: "move", clip_trim: "trim", clip_split: "split", clip_retrack: "retrack",
  clip_set_disabled: "disabled", clip_set_asset: "asset_reference", property_set: "properties",
  lane_set: "lane", lane_remove: "lane", transition_set: "transition", transition_remove: "transition",
  track_route: "route", metadata_set: "metadata", metadata_remove: "metadata",
  annotation_set: "annotation", annotation_remove: "annotation",
});
const payloadNames = new Set(Object.values(payloadForKind));

export class TransactionV2Error extends Error {
  constructor(code, detail) {
    super(`@cutly/sdk: ${code}: ${detail}`);
    this.name = "TransactionV2Error";
    this.code = code;
  }
}

// decodeTransactionV2 returns lossless canonical bytes instead of a JavaScript
// object whose Number values could silently round open Document-v2 metadata.
export async function decodeTransactionV2(input) {
  const bytes = typeof input === "string" ? new TextEncoder().encode(input) : input;
  if (!(bytes instanceof Uint8Array) || bytes.byteLength === 0 || bytes.byteLength > MAX_TRANSACTION_V2_BYTES) {
    fail("TRANSACTION_V2.LIMIT", "encoded transaction is empty or too large");
  }
  const canonical = canonicalizeNumberPreservingJSON(bytes, { maxInputBytes: MAX_TRANSACTION_V2_BYTES, maxDepth: 64, maxValues: 2_000_000, maxStringBytes: 1 << 20, maxIntegerDigits: 4096 });
  if (!equalBytes(bytes, canonical)) fail("TRANSACTION_V2.NON_CANONICAL", "input must be exact canonical JSON");

  let value;
  try { value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(canonical)); }
  catch (error) { fail("TRANSACTION_V2.INVALID", "transaction cannot be inspected", error); }
  requirePlainObject(value, "transaction");
  requireExactKeys(value, new Set(["schema_version", "profile", "id", "idempotency_key", "document_id", "expected_revision", "expected_document_hash", "operations", "digest"]), "transaction");
  if (value.schema_version !== TRANSACTION_V2_SCHEMA_VERSION || value.profile !== TRANSACTION_V2_PROFILE) fail("TRANSACTION_V2.VERSION", "unsupported schema/profile");
  for (const name of ["id", "idempotency_key", "document_id"]) requireIdentity(value[name], name);
  if (!digestPattern.test(value.expected_document_hash)) fail("TRANSACTION_V2.INVALID", "expected_document_hash must be canonical sha256");
  if (typeof value.digest !== "string" || !digestPattern.test(value.digest)) fail("TRANSACTION_V2.INVALID", "digest must be canonical sha256");
  const text = new TextDecoder().decode(canonical);
  const revision = text.match(/"expected_revision":(-?(?:0|[1-9][0-9]*))(?:,|\})/u)?.[1];
  if (revision === undefined || revision.startsWith("-") || BigInt(revision) > ((1n << 64n) - 1n)) fail("TRANSACTION_V2.INVALID", "expected_revision must be uint64 integer syntax");
  if (!Array.isArray(value.operations) || value.operations.length === 0 || value.operations.length > 4096) fail("TRANSACTION_V2.INVALID", "operations must contain 1..4096 entries");
  const seen = new Set();
  for (const [index, operation] of value.operations.entries()) validateOperation(operation, index, seen);
  const computed = await digestNumberPreservingJSONWithoutRootMember(canonical, "digest", { maxInputBytes: MAX_TRANSACTION_V2_BYTES, maxDepth: 64, maxValues: 2_000_000, maxStringBytes: 1 << 20, maxIntegerDigits: 4096 });
  if (computed.digest !== value.digest) fail("TRANSACTION_V2.DIGEST", "transaction digest mismatch");
  return Object.freeze({
    schemaVersion: TRANSACTION_V2_SCHEMA_VERSION,
    profile: TRANSACTION_V2_PROFILE,
    id: value.id,
    idempotencyKey: value.idempotency_key,
    documentId: value.document_id,
    expectedRevision: revision,
    expectedDocumentHash: value.expected_document_hash,
    digest: value.digest,
    operationCount: value.operations.length,
    canonical: canonical.slice(),
  });
}

export async function transactionDigestV2(input) {
  return (await decodeTransactionV2(input)).digest;
}

export function moveClipV2({ id, clipId, at }) {
  return operation(id, "clip_move", "move", { clip_id: identity(clipId, "clipId"), at: rational(at, "at") });
}

export function trimClipV2({ id, clipId, timelineRange, sourceRange }) {
  return operation(id, "clip_trim", "trim", {
    clip_id: identity(clipId, "clipId"),
    timeline_range: timeRange(timelineRange, "timelineRange"),
    source_range: timeRange(sourceRange, "sourceRange"),
  });
}

export function deleteClipV2({ id, trackId, clipId }) {
  return operation(id, "clip_delete", "clip", { track_id: identity(trackId, "trackId"), clip_id: identity(clipId, "clipId") });
}

export function retrackClipV2({ id, clipId, trackId, position }) {
  return operation(id, "clip_retrack", "retrack", {
    clip_id: identity(clipId, "clipId"), track_id: identity(trackId, "trackId"), position: unsigned(position, "position"),
  });
}

export function setClipPropertiesV2({ id, clipId, value }) {
  const properties = { clip_id: identity(clipId, "clipId") };
  if (value !== undefined) properties.value = value;
  return operation(id, "property_set", "properties", properties);
}

// buildTransactionV2 is intentionally async because the canonical transaction
// includes a SHA-256 digest. Its returned value is the same lossless verified
// handle produced by decodeTransactionV2.
export async function buildTransactionV2({ id, idempotencyKey, documentId, expectedRevision, expectedDocumentHash, operations }) {
  if (!Array.isArray(operations) || operations.length === 0 || operations.length > 4096) fail("TRANSACTION_V2.INVALID", "operations must contain 1..4096 entries");
  const draft = {
    schema_version: 2n,
    profile: TRANSACTION_V2_PROFILE,
    id: identity(id, "id"),
    idempotency_key: identity(idempotencyKey, "idempotencyKey"),
    document_id: identity(documentId, "documentId"),
    expected_revision: unsigned(expectedRevision, "expectedRevision"),
    expected_document_hash: expectedDocumentHash,
    operations,
  };
  if (!digestPattern.test(expectedDocumentHash)) fail("TRANSACTION_V2.INVALID", "expectedDocumentHash must be canonical sha256");
  const withoutDigest = encodeCanonicalValue(draft, { maxInputBytes: MAX_TRANSACTION_V2_BYTES, maxDepth: 64, maxValues: 2_000_000, maxStringBytes: 1 << 20, maxIntegerDigits: 4096 });
  const computed = await digestNumberPreservingJSONWithoutRootMember(withoutDigest, "digest", { maxInputBytes: MAX_TRANSACTION_V2_BYTES, maxDepth: 64, maxValues: 2_000_000, maxStringBytes: 1 << 20, maxIntegerDigits: 4096 });
  const canonical = encodeCanonicalValue({ ...draft, digest: computed.digest }, { maxInputBytes: MAX_TRANSACTION_V2_BYTES, maxDepth: 64, maxValues: 2_000_000, maxStringBytes: 1 << 20, maxIntegerDigits: 4096 });
  return decodeTransactionV2(canonical);
}

function operation(id, kind, payloadName, payload) {
  return Object.freeze({ id: identity(id, "id"), op: kind, [payloadName]: deepFreeze(payload) });
}

function rational(value, path) {
  requirePlainObject(value, path);
  const rate = integer(value.rate, `${path}.rate`);
  if (rate <= 0n) fail("TRANSACTION_V2.INVALID", `${path}.rate must be positive`);
  return Object.freeze({ value: integer(value.value, `${path}.value`), rate });
}

function timeRange(value, path) {
  requirePlainObject(value, path);
  const result = Object.freeze({ start: rational(value.start, `${path}.start`), duration: rational(value.duration, `${path}.duration`) });
  if (result.duration.value <= 0n) fail("TRANSACTION_V2.INVALID", `${path}.duration must be positive`);
  return result;
}

function integer(value, path) {
  try {
    if (typeof value === "bigint") return value;
    if (typeof value === "number" && Number.isSafeInteger(value)) return BigInt(value);
    if (typeof value === "string" && /^-?(?:0|[1-9][0-9]*)$/u.test(value)) return BigInt(value);
  } catch {}
  fail("TRANSACTION_V2.INVALID", `${path} must be an exact integer`);
}

function unsigned(value, path) {
  const result = integer(value, path);
  if (result < 0n || result > ((1n << 64n) - 1n)) fail("TRANSACTION_V2.INVALID", `${path} must be uint64`);
  return result;
}

function identity(value, path) {
  requireIdentity(value, path);
  return value;
}

function deepFreeze(value) {
  if (value === null || typeof value !== "object" || Object.isFrozen(value)) return value;
  for (const child of Object.values(value)) deepFreeze(child);
  return Object.freeze(value);
}

function validateOperation(operation, index, seen) {
  requirePlainObject(operation, `operations[${index}]`);
  requireIdentity(operation.id, `operations[${index}].id`);
  if (seen.has(operation.id)) fail("TRANSACTION_V2.INVALID", `duplicate operation id ${JSON.stringify(operation.id)}`);
  seen.add(operation.id);
  const payload = payloadForKind[operation.op];
  if (payload === undefined) fail("TRANSACTION_V2.INVALID", `unknown operation kind ${JSON.stringify(operation.op)}`);
  const allowed = new Set(["id", "op", "expect", payload]);
  requireExactKeys(operation, allowed, `operations[${index}]`);
  if (!Object.hasOwn(operation, payload) || operation[payload] === null) fail("TRANSACTION_V2.INVALID", `operation ${index} requires ${payload}`);
  for (const name of payloadNames) {
    if (name !== payload && Object.hasOwn(operation, name)) fail("TRANSACTION_V2.INVALID", `operation ${index} has a second payload`);
  }
  if (Object.hasOwn(operation, "expect")) {
    requirePlainObject(operation.expect, `operations[${index}].expect`);
    requireExactKeys(operation.expect, new Set(["hash"]), `operations[${index}].expect`);
    if (!digestPattern.test(operation.expect.hash)) fail("TRANSACTION_V2.INVALID", `operation ${index} expectation hash is invalid`);
  }
}

function requirePlainObject(value, path) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) fail("TRANSACTION_V2.INVALID", `${path} must be an object`);
}

function requireExactKeys(value, allowed, path) {
  for (const name of Object.keys(value)) if (!allowed.has(name)) fail("TRANSACTION_V2.INVALID", `${path} has unknown member ${JSON.stringify(name)}`);
  for (const name of allowed) {
    if (path === "transaction" && name !== "digest" && !Object.hasOwn(value, name)) fail("TRANSACTION_V2.INVALID", `${path} is missing ${JSON.stringify(name)}`);
  }
}

function requireIdentity(value, path) {
  if (typeof value !== "string" || new TextEncoder().encode(value).byteLength > 256 || !identityPattern.test(value)) fail("TRANSACTION_V2.INVALID", `${path} is not a bounded identity`);
}

function equalBytes(left, right) {
  if (left.byteLength !== right.byteLength) return false;
  for (let index = 0; index < left.byteLength; index += 1) if (left[index] !== right[index]) return false;
  return true;
}

function fail(code, detail, cause) {
  const error = new TransactionV2Error(code, detail);
  if (cause !== undefined) error.cause = cause;
  throw error;
}
