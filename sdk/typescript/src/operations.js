import { decodeCanonicalJSON, encodeCanonicalValue } from "./canonical-json.js";

const INT64_MIN = -(1n << 63n);
const INT64_MAX = (1n << 63n) - 1n;
const UINT64_MAX = (1n << 64n) - 1n;

export const OPERATION_PROTOCOL = "cutly.transaction.v1";
export const HEADLESS_TIMELINE_PROFILE = "cutly.headless-timeline.typed-operations.v1";
export const HEADLESS_UI_PROFILE = "cutly.headless-ui-primitives.v1";

export function insertClip(input) {
  requireObject(input, "insert");
  return freezeProtocol({
    id: identifier(input.id, "id"),
    op: "insert",
    track_id: identifier(input.trackId, "trackId"),
    clip: requireObject(input.clip, "clip"),
  });
}

export function moveClip(input) {
  requireObject(input, "move");
  return freezeProtocol({
    id: identifier(input.id, "id"),
    op: "move",
    clip_id: identifier(input.clipId, "clipId"),
    at: rational(input.at, "at"),
  });
}

export function trimClip(input) {
  requireObject(input, "trim");
  const operation = {
    id: identifier(input.id, "id"),
    op: "trim",
    clip_id: identifier(input.clipId, "clipId"),
    source_range: timeRange(input.sourceRange, "sourceRange", true),
  };
  if (input.timelineRange !== undefined) operation.timeline_range = timeRange(input.timelineRange, "timelineRange", true);
  if (input.ripple === true) operation.ripple = true;
  else if (input.ripple !== undefined && input.ripple !== false) throw new TypeError("@cutly/sdk: ripple must be boolean");
  return freezeProtocol(operation);
}

export function splitClip(input) {
  requireObject(input, "split");
  return freezeProtocol({
    id: identifier(input.id, "id"),
    op: "split",
    clip_id: identifier(input.clipId, "clipId"),
    new_clip_id: identifier(input.newClipId, "newClipId"),
    at: rational(input.at, "at"),
  });
}

export function deleteClip(input) {
  requireObject(input, "delete");
  const operation = {
    id: identifier(input.id, "id"),
    op: "delete",
    clip_id: identifier(input.clipId, "clipId"),
  };
  if (input.ripple === true) operation.ripple = true;
  else if (input.ripple !== undefined && input.ripple !== false) throw new TypeError("@cutly/sdk: ripple must be boolean");
  return freezeProtocol(operation);
}

export function retrackClip(input) {
  requireObject(input, "retrack");
  return freezeProtocol({
    id: identifier(input.id, "id"),
    op: "retrack",
    clip_id: identifier(input.clipId, "clipId"),
    target_track_id: identifier(input.targetTrackId, "targetTrackId"),
  });
}

export function setClipProperty(input) {
  requireObject(input, "set_property");
  const property = identifier(input.property, "property");
  if (property !== property.trim()) throw new TypeError("@cutly/sdk: property must not have leading or trailing whitespace");
  const operation = {
    id: identifier(input.id, "id"),
    op: "set_property",
    clip_id: identifier(input.clipId, "clipId"),
    property,
  };
  if (input.value !== undefined) operation.value = input.value;
  return freezeProtocol(operation);
}

export function buildTransaction(input) {
  requireObject(input, "transaction");
  if (!Array.isArray(input.operations) || input.operations.length === 0) {
    throw new TypeError("@cutly/sdk: transaction operations must be a non-empty array");
  }
  const operationIDs = new Set();
  for (const operation of input.operations) {
    requireObject(operation, "operation");
    const id = identifier(operation.id, "operation.id");
    if (operationIDs.has(id)) throw new TypeError(`@cutly/sdk: duplicate operation id ${JSON.stringify(id)}`);
    operationIDs.add(id);
  }
  return freezeProtocol({
    id: identifier(input.id, "id"),
    idempotency_key: identifier(input.idempotencyKey, "idempotencyKey"),
    expected_revision: integer(input.expectedRevision, "expectedRevision", 0n, UINT64_MAX),
    operations: input.operations,
  });
}

// HeadlessTimeline owns only ephemeral UI state. Every edit method dispatches
// one typed operation and never receives or mutates a Document array.
export class HeadlessTimeline {
  #revision;
  #dispatch;
  #selectedTrackIDs = [];
  #selectedClipIDs = [];
  #playhead = null;

  constructor({ revision, dispatch }) {
    if (typeof dispatch !== "function") throw new TypeError("@cutly/sdk: HeadlessTimeline dispatch must be a function");
    this.#revision = integer(revision, "revision", 0n, UINT64_MAX);
    this.#dispatch = dispatch;
  }

  snapshot() {
    return Object.freeze({
      profile: HEADLESS_TIMELINE_PROFILE,
      revision: this.#revision.toString(),
      selectedTrackIDs: Object.freeze([...this.#selectedTrackIDs]),
      selectedClipIDs: Object.freeze([...this.#selectedClipIDs]),
      playhead: this.#playhead === null ? null : wireRational(this.#playhead),
    });
  }

  setRevision(revision) {
    this.#revision = integer(revision, "revision", 0n, UINT64_MAX);
    return this.snapshot();
  }

  select({ trackIDs = [], clipIDs = [] } = {}) {
    this.#selectedTrackIDs = identifiers(trackIDs, "trackIDs");
    this.#selectedClipIDs = identifiers(clipIDs, "clipIDs");
    return this.snapshot();
  }

  setPlayhead(value) {
    this.#playhead = rational(value, "playhead");
    return this.snapshot();
  }

  clearPlayhead() {
    this.#playhead = null;
    return this.snapshot();
  }

  insert(input) { return this.#emit(insertClip(input)); }
  move(input) { return this.#emit(moveClip(input)); }
  trim(input) { return this.#emit(trimClip(input)); }
  split(input) { return this.#emit(splitClip(input)); }
  delete(input) { return this.#emit(deleteClip(input)); }
  retrack(input) { return this.#emit(retrackClip(input)); }
  setProperty(input) { return this.#emit(setClipProperty(input)); }

  #emit(operation) {
    return this.#dispatch(Object.freeze({
      profile: HEADLESS_TIMELINE_PROFILE,
      expectedRevision: this.#revision.toString(),
      operation,
    }));
  }
}

export function waveformLane(input) {
  requireObject(input, "waveform lane");
  return freezeProtocol({
    kind: "waveform",
    id: identifier(input.id, "id"),
    track_id: identifier(input.trackId, "trackId"),
    asset_id: identifier(input.assetId, "assetId"),
    sample_range: tickRange(input.sampleRange, "sampleRange"),
    peaks_digest: digest(input.peaksDigest, "peaksDigest"),
  });
}

export function captionLane(input) {
  requireObject(input, "caption lane");
  if (!Array.isArray(input.cues)) throw new TypeError("@cutly/sdk: caption cues must be an array");
  const cues = input.cues.map((cue, index) => {
    requireObject(cue, `cues[${index}]`);
    if (typeof cue.text !== "string") throw new TypeError(`@cutly/sdk: cues[${index}].text must be a string`);
    return { id: identifier(cue.id, `cues[${index}].id`), range: timeRange(cue.range, `cues[${index}].range`, true), text: cue.text };
  });
  return freezeProtocol({
    kind: "captions",
    id: identifier(input.id, "id"),
    track_id: identifier(input.trackId, "trackId"),
    cues,
  });
}

// These immutable read models are deliberately separate from the Document
// object model. Renderers/adapters project their compiled state into them; UI
// code cannot mutate editing arrays through this surface.
export function clipPrimitive(input) {
  requireObject(input, "clip primitive");
  const value = {
    profile: HEADLESS_UI_PROFILE,
    kind: "clip",
    id: identifier(input.id, "id"),
    track_id: identifier(input.trackId, "trackId"),
    asset_id: identifier(input.assetId, "assetId"),
    timeline_range: timeRange(input.timelineRange, "timelineRange", true),
    source_range: timeRange(input.sourceRange, "sourceRange", true),
    disabled: optionalBoolean(input.disabled, false, "disabled"),
  };
  if (input.label !== undefined) value.label = text(input.label, "label");
  return freezeProtocol(value);
}

export function trackPrimitive(input) {
  requireObject(input, "track primitive");
  const id = identifier(input.id, "id");
  if (input.kind !== "video" && input.kind !== "audio") {
    throw new TypeError("@cutly/sdk: track kind must be video or audio");
  }
  if (!Array.isArray(input.clips)) throw new TypeError("@cutly/sdk: track clips must be an array");
  const clipIDs = new Set();
  let previousStart = null;
  const clips = input.clips.map((candidate, index) => {
    requirePrimitive(candidate, "clip", `clips[${index}]`);
    if (candidate.track_id !== id) throw new TypeError(`@cutly/sdk: clips[${index}] belongs to another track`);
    if (clipIDs.has(candidate.id)) throw new TypeError(`@cutly/sdk: duplicate clip id ${JSON.stringify(candidate.id)}`);
    clipIDs.add(candidate.id);
    const start = candidate.timeline_range.start;
    if (previousStart !== null && compareRational(start, previousStart) < 0) {
      throw new TypeError("@cutly/sdk: track clips must be sorted by timeline start");
    }
    previousStart = start;
    return candidate;
  });
  const value = { profile: HEADLESS_UI_PROFILE, kind: "track", id, track_kind: input.kind, clips };
  if (input.label !== undefined) value.label = text(input.label, "label");
  return freezeProtocol(value);
}

export function timelinePrimitive(input) {
  requireObject(input, "timeline primitive");
  if (!Array.isArray(input.tracks)) throw new TypeError("@cutly/sdk: timeline tracks must be an array");
  const trackIDs = new Set();
  const tracks = input.tracks.map((candidate, index) => {
    requirePrimitive(candidate, "track", `tracks[${index}]`);
    if (trackIDs.has(candidate.id)) throw new TypeError(`@cutly/sdk: duplicate track id ${JSON.stringify(candidate.id)}`);
    trackIDs.add(candidate.id);
    return candidate;
  });
  const value = {
    profile: HEADLESS_UI_PROFILE,
    kind: "timeline",
    id: identifier(input.id, "id"),
    revision: integer(input.revision, "revision", 0n, UINT64_MAX),
    tracks,
  };
  if (input.label !== undefined) value.label = text(input.label, "label");
  return freezeProtocol(value);
}

export function trimHandle(input) {
  requireObject(input, "trim handle");
  requirePrimitive(input.clip, "clip", "clip");
  if (input.edge !== "start" && input.edge !== "end") {
    throw new TypeError("@cutly/sdk: trim handle edge must be start or end");
  }
  const atEnd = input.edge === "end";
  const timelineTime = atEnd ? addRational(input.clip.timeline_range.start, input.clip.timeline_range.duration) : input.clip.timeline_range.start;
  const sourceTime = atEnd ? addRational(input.clip.source_range.start, input.clip.source_range.duration) : input.clip.source_range.start;
  return freezeProtocol({
    profile: HEADLESS_UI_PROFILE,
    kind: "trim_handle",
    clip_id: input.clip.id,
    track_id: input.clip.track_id,
    edge: input.edge,
    timeline_time: timelineTime,
    source_time: sourceTime,
  });
}

export function playheadPrimitive(input) {
  requireObject(input, "playhead primitive");
  const value = {
    profile: HEADLESS_UI_PROFILE,
    kind: "playhead",
    time: rational(input.time, "time"),
  };
  if (input.frameIndex !== undefined) value.frame_index = integer(input.frameIndex, "frameIndex", INT64_MIN, INT64_MAX);
  if (input.sampleIndex !== undefined) value.sample_index = integer(input.sampleIndex, "sampleIndex", INT64_MIN, INT64_MAX);
  return freezeProtocol(value);
}

export function selectionPrimitive({ trackIDs = [], clipIDs = [] } = {}) {
  return freezeProtocol({
    profile: HEADLESS_UI_PROFILE,
    kind: "selection",
    track_ids: identifiers(trackIDs, "trackIDs"),
    clip_ids: identifiers(clipIDs, "clipIDs"),
  });
}

function freezeProtocol(value) {
  return decodeCanonicalJSON(encodeCanonicalValue(value));
}

function requirePrimitive(value, kind, name) {
  requireObject(value, name);
  if (value.profile !== HEADLESS_UI_PROFILE || value.kind !== kind) {
    throw new TypeError(`@cutly/sdk: ${name} must be a ${HEADLESS_UI_PROFILE} ${kind} primitive`);
  }
  return value;
}

function rational(value, name) {
  requireObject(value, name);
  return Object.freeze({
    value: integer(value.value, `${name}.value`, INT64_MIN, INT64_MAX),
    rate: integer(value.rate, `${name}.rate`, 1n, INT64_MAX),
  });
}

function timeRange(value, name, positiveDuration) {
  requireObject(value, name);
  const start = rational(value.start, `${name}.start`);
  const duration = rational(value.duration, `${name}.duration`);
  if (duration.value < 0n || (positiveDuration && duration.value === 0n)) {
    throw new TypeError(`@cutly/sdk: ${name}.duration must be ${positiveDuration ? "positive" : "non-negative"}`);
  }
  const endNumerator = start.value * duration.rate + duration.value * start.rate;
  const endDenominator = start.rate * duration.rate;
  const divisor = gcd(abs(endNumerator), endDenominator);
  const reducedNumerator = endNumerator / divisor;
  const reducedDenominator = endDenominator / divisor;
  if (reducedNumerator < INT64_MIN || reducedNumerator > INT64_MAX || reducedDenominator > INT64_MAX) {
    throw new TypeError(`@cutly/sdk: ${name} end overflows RationalTime int64 fields`);
  }
  return Object.freeze({ start, duration });
}

function tickRange(value, name) {
  requireObject(value, name);
  const first = integer(value.first, `${name}.first`, INT64_MIN, INT64_MAX);
  const count = integer(value.count, `${name}.count`, 0n, INT64_MAX);
  const end = first + count;
  if (end < INT64_MIN || end > INT64_MAX) throw new TypeError(`@cutly/sdk: ${name} exclusive end overflows int64`);
  return Object.freeze({ first, count });
}

function identifier(value, name) {
  if (typeof value !== "string" || value.trim() === "") throw new TypeError(`@cutly/sdk: ${name} must be a non-blank string`);
  return value;
}

function text(value, name) {
  if (typeof value !== "string") throw new TypeError(`@cutly/sdk: ${name} must be a string`);
  return value;
}

function optionalBoolean(value, fallback, name) {
  if (value === undefined) return fallback;
  if (typeof value !== "boolean") throw new TypeError(`@cutly/sdk: ${name} must be boolean`);
  return value;
}

function identifiers(values, name) {
  if (!Array.isArray(values)) throw new TypeError(`@cutly/sdk: ${name} must be an array`);
  const result = [];
  const seen = new Set();
  for (let index = 0; index < values.length; index += 1) {
    const value = identifier(values[index], `${name}[${index}]`);
    if (!seen.has(value)) {
      seen.add(value);
      result.push(value);
    }
  }
  return result;
}

function integer(value, name, minimum, maximum) {
  let parsed;
  if (typeof value === "bigint") parsed = value;
  else if (typeof value === "number" && Number.isSafeInteger(value)) parsed = BigInt(value);
  else if (typeof value === "string" && /^-?(0|[1-9][0-9]*)$/.test(value)) parsed = BigInt(value);
  else throw new TypeError(`@cutly/sdk: ${name} must be an exact integer`);
  if (parsed < minimum || parsed > maximum) throw new TypeError(`@cutly/sdk: ${name} is out of range`);
  return parsed;
}

function wireRational(value) {
  return Object.freeze({ value: value.value.toString(), rate: value.rate.toString() });
}

function digest(value, name) {
  if (typeof value !== "string" || !/^sha256:[0-9a-f]{64}$/.test(value)) {
    throw new TypeError(`@cutly/sdk: ${name} must be a canonical SHA-256 digest`);
  }
  return value;
}

function requireObject(value, name) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`@cutly/sdk: ${name} must be an object`);
  }
  return value;
}

function gcd(left, right) {
  while (right !== 0n) {
    const next = left % right;
    left = right;
    right = next;
  }
  return left === 0n ? 1n : left;
}

function abs(value) {
  return value < 0n ? -value : value;
}

function compareRational(left, right) {
  const leftValue = integer(left.value, "rational.value", INT64_MIN, INT64_MAX);
  const leftRate = integer(left.rate, "rational.rate", 1n, INT64_MAX);
  const rightValue = integer(right.value, "rational.value", INT64_MIN, INT64_MAX);
  const rightRate = integer(right.rate, "rational.rate", 1n, INT64_MAX);
  const difference = leftValue * rightRate - rightValue * leftRate;
  return difference < 0n ? -1 : difference > 0n ? 1 : 0;
}

function addRational(left, right) {
  const leftValue = integer(left.value, "rational.value", INT64_MIN, INT64_MAX);
  const leftRate = integer(left.rate, "rational.rate", 1n, INT64_MAX);
  const rightValue = integer(right.value, "rational.value", INT64_MIN, INT64_MAX);
  const rightRate = integer(right.rate, "rational.rate", 1n, INT64_MAX);
  const numerator = leftValue * rightRate + rightValue * leftRate;
  const denominator = leftRate * rightRate;
  const divisor = gcd(abs(numerator), denominator);
  const reducedValue = numerator / divisor;
  const reducedRate = denominator / divisor;
  if (reducedValue < INT64_MIN || reducedValue > INT64_MAX || reducedRate > INT64_MAX) {
    throw new TypeError("@cutly/sdk: rational sum overflows RationalTime int64 fields");
  }
  return Object.freeze({ value: reducedValue, rate: reducedRate });
}
