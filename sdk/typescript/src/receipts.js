import {
  decodeCanonicalJSON,
  digestCanonicalValue,
  encodeCanonicalValue,
} from "./canonical-json.js";

const MAX_RECEIPT_BYTES = 16 << 20;
const INT64_MIN = -(1n << 63n);
const INT64_MAX = (1n << 63n) - 1n;
const UINT64_MAX = (1n << 64n) - 1n;
const INT32_MAX = (1n << 31n) - 1n;
const UTF8 = new TextEncoder();

const RESULT_KEYS = Object.freeze([
  "action", "base_revision", "changed", "diagnostics", "diff", "job_id",
  "new_revision", "next_actions", "ok", "project_id", "protocol_version",
  "receipt", "render_required", "replayed", "request_id", "state", "tool",
  "transaction_id",
]);
const RESULT_REQUIRED_KEYS = Object.freeze([
  "action", "changed", "diagnostics", "next_actions", "ok", "project_id",
  "protocol_version", "render_required", "replayed", "request_id", "state", "tool",
]);

const PLAN_KEYS = Object.freeze([
  "audio", "document_hash", "document_id", "fingerprint", "frame_range",
  "required_capabilities", "requested_range", "resources", "revision",
  "sample_range", "schema_version", "semantic_profile", "target", "timeline_id",
  "video",
]);
const PLAN_REQUIRED_KEYS = Object.freeze([
  "document_hash", "document_id", "fingerprint", "frame_range",
  "required_capabilities", "requested_range", "revision", "sample_range",
  "schema_version", "semantic_profile", "target", "timeline_id",
]);
const PLAN_CLIP_KEYS = Object.freeze([
  "asset_id", "clip_id", "clip_index", "properties", "source_range", "track_id",
  "track_index", "timeline_range", "visible_source_range", "visible_timeline_range",
]);
const PLAN_CLIP_REQUIRED_KEYS = Object.freeze([
  "asset_id", "clip_id", "clip_index", "source_range", "track_id", "track_index",
  "timeline_range", "visible_source_range", "visible_timeline_range",
]);

const MANIFEST_KEYS = Object.freeze([
  "backend", "browser_fingerprint", "compiler_build", "deterministic_seed",
  "evaluator_build", "fingerprint", "fonts", "media_runtime", "output",
  "plan_fingerprint", "plugins", "resources", "schema_version", "semantic_profile",
]);
const MANIFEST_REQUIRED_KEYS = Object.freeze([
  "backend", "compiler_build", "deterministic_seed", "evaluator_build", "fingerprint",
  "media_runtime", "output", "plan_fingerprint", "schema_version", "semantic_profile",
]);

const JOB_KEYS = Object.freeze([
  "artifact_uri", "execution_manifest_fingerprint", "frame_range", "id",
  "idempotency_key", "plan_fingerprint", "sample_range", "schema_version",
  "timeline_range",
]);
const SNAPSHOT_KEYS = Object.freeze([
  "artifact", "failure_class", "failure_message", "job", "last_sequence",
  "schema_version", "state",
]);
const SNAPSHOT_REQUIRED_KEYS = Object.freeze([
  "job", "last_sequence", "schema_version", "state",
]);
const ARTIFACT_KEYS = Object.freeze([
  "content_hash", "duration", "evidence_fingerprint", "evidence_uri",
  "execution_manifest_fingerprint", "frame_count", "sample_count", "size_bytes", "uri",
]);

const ASSET_KINDS = new Set(["video", "audio", "image", "font", "data"]);
const TRACK_KINDS = new Set(["video", "audio"]);
const JOB_STATES = new Set(["queued", "running", "canceling", "canceled", "failed", "complete"]);
const FAILURE_CLASSES = new Set([
  "non_retryable", "input_repairable", "transient", "resource", "integrity", "cancellation",
]);
const TERMINAL_STATES = new Set(["canceled", "failed", "complete"]);

export class AgentReceiptError extends Error {
  constructor(code, detail, cause) {
    super(`@cutly/sdk: ${code}: ${detail}`, cause === undefined ? undefined : { cause });
    this.name = "AgentReceiptError";
    this.code = code;
  }
}

// decodeRenderPlanReceipt owns and validates the complete receipt value. It
// also recomputes the semantic fingerprint and binds the plan to the exact
// range/target that the caller submitted.
export async function decodeRenderPlanReceipt(result, expected) {
  const binding = ownInput(expected, ["projectId", "range", "requestId", "target"], "render plan binding");
  const normalized = ownResult(result);
  requireRenderSuccess(normalized, binding, "plan", "planned");
  const receipt = requireReceipt(normalized, ["plan", "profile"], "render plan receipt");
  if (receipt.profile !== "cutly.agent-render-plan.v1") {
    fail("RECEIPT.PROFILE_INVALID", "render plan receipt profile is unsupported");
  }
  const plan = await validatePlan(receipt.plan, "render plan receipt plan");
  if (!canonicalEqual(plan.requested_range, binding.range)) {
    fail("RECEIPT.BINDING_INVALID", "render plan range does not match the submitted range");
  }
  const wantedTarget = targetWithSelectedTimeline(binding.target, plan.target.timeline_id);
  if (!canonicalEqual(plan.target, wantedTarget)) {
    fail("RECEIPT.BINDING_INVALID", "render plan target does not match the submitted target");
  }
  return receipt;
}

// decodeRenderPrepareReceipt verifies the host-frozen execution environment,
// including its content fingerprint, plan/output binding, deterministic seed,
// and the exact caller-authorized asset-to-local-path mapping.
export async function decodeRenderPrepareReceipt(result, expected) {
  const binding = ownInput(
    expected,
    ["deterministicSeed", "plan", "projectId", "requestId", "resources"],
    "render prepare binding",
    ["plan", "projectId", "requestId", "resources"],
  );
  const normalized = ownResult(result);
  requireRenderSuccess(normalized, binding, "prepare", "prepared");
  const receipt = requireReceipt(normalized, ["execution_manifest", "profile"], "render prepare receipt");
  if (receipt.profile !== "cutly.agent-render-prepare.v1") {
    fail("RECEIPT.PROFILE_INVALID", "render prepare receipt profile is unsupported");
  }
  const plan = await validatePlan(binding.plan, "render prepare plan");
  const manifest = await validateExecutionManifest(receipt.execution_manifest, plan);
  const seed = binding.deterministicSeed === undefined
    ? 0n
    : requireInteger(binding.deterministicSeed, "deterministicSeed", INT64_MIN, INT64_MAX, "RECEIPT.INPUT_INVALID");
  if (manifest.deterministic_seed !== seed) {
    fail("RECEIPT.BINDING_INVALID", "execution manifest deterministic seed does not match the request");
  }
  validatePrepareResources(binding.resources, manifest.resources ?? []);
  return receipt;
}

// decodeRenderPreflightReceipt binds the live backend proof to the exact
// frozen plan and manifest. The two immutable inputs are revalidated first so
// a caller cannot use a self-consistent but tampered expected value.
export async function decodeRenderPreflightReceipt(result, expected) {
  const binding = ownInput(
    expected,
    ["executionManifest", "plan", "projectId", "requestId"],
    "render preflight binding",
  );
  const normalized = ownResult(result);
  requireRenderSuccess(normalized, binding, "preflight", "ready");
  const plan = await validatePlan(binding.plan, "render preflight plan");
  const manifest = await validateExecutionManifest(binding.executionManifest, plan);
  const receipt = requireReceipt(normalized, [
    "backend_build_fingerprint", "backend_id", "execution_manifest_fingerprint",
    "plan_fingerprint", "profile",
  ], "render preflight receipt");
  if (receipt.profile !== "cutly.agent-render-preflight.v1") {
    fail("RECEIPT.PROFILE_INVALID", "render preflight receipt profile is unsupported");
  }
  requireDigest(receipt.plan_fingerprint, "preflight plan fingerprint");
  requireDigest(receipt.execution_manifest_fingerprint, "preflight execution manifest fingerprint");
  requireDigest(receipt.backend_build_fingerprint, "preflight backend build fingerprint");
  requireNonblank(receipt.backend_id, "preflight backend ID");
  if (receipt.plan_fingerprint !== plan.fingerprint ||
      receipt.execution_manifest_fingerprint !== manifest.fingerprint ||
      receipt.backend_id !== manifest.backend.backend_id ||
      receipt.backend_build_fingerprint !== manifest.backend.build_fingerprint) {
    fail("RECEIPT.BINDING_INVALID", "preflight receipt is not bound to the frozen plan and backend");
  }
  return receipt;
}

// decodeRenderJobSnapshotReceipt is shared by render/status/cancel. It checks
// the Agent correlation, immutable job identity, state/sequence grammar, and
// the complete/failure terminal unions before returning an owned snapshot.
export function decodeRenderJobSnapshotReceipt(result, expected) {
  const binding = ownInput(
    expected,
    ["action", "job", "projectId", "requestId"],
    "render snapshot binding",
  );
  if (!new Set(["render", "status", "cancel"]).has(binding.action)) {
    fail("RECEIPT.INPUT_INVALID", "snapshot action must be render, status, or cancel");
  }
  const job = validateRenderJob(binding.job, "expected render job", "RECEIPT.INPUT_INVALID");
  const normalized = ownResult(result);
  requireRenderSuccess(normalized, binding, binding.action, undefined, job.id);
  const snapshot = normalized.receipt;
  requireObject(snapshot, "render job snapshot");
  requireKeys(snapshot, SNAPSHOT_KEYS, SNAPSHOT_REQUIRED_KEYS, "render job snapshot");
  if (snapshot.schema_version !== 1n) fail("RECEIPT.INVALID", "snapshot schema_version must be 1");
  const snapshotJob = validateRenderJob(snapshot.job, "snapshot job");
  if (!canonicalEqual(snapshotJob, job)) {
    fail("RECEIPT.BINDING_INVALID", "snapshot job does not match the submitted immutable job");
  }
  if (!JOB_STATES.has(snapshot.state)) fail("RECEIPT.INVALID", "snapshot state is unsupported");
  const lastSequence = requireInteger(snapshot.last_sequence, "snapshot last_sequence", 1n, UINT64_MAX);
  if (!validSnapshotSequence(snapshot.state, lastSequence)) {
    fail("RECEIPT.INVALID", "snapshot state and sequence are inconsistent");
  }
  if (normalized.state !== snapshot.state) {
    fail("RECEIPT.BINDING_INVALID", "Agent result state does not match the snapshot state");
  }
  const shouldRender = binding.action === "cancel" ? false : !TERMINAL_STATES.has(snapshot.state);
  if (normalized.render_required !== shouldRender) {
    fail("RECEIPT.BINDING_INVALID", "Agent render_required does not match the snapshot state/action");
  }

  if (snapshot.state === "failed") {
    if (!FAILURE_CLASSES.has(snapshot.failure_class) || !isNonblank(snapshot.failure_message)) {
      fail("RECEIPT.INVALID", "failed snapshot requires a supported failure class and message");
    }
  } else if (snapshot.failure_class !== undefined || snapshot.failure_message !== undefined) {
    fail("RECEIPT.INVALID", "failure fields are valid only for a failed snapshot");
  }

  if (snapshot.state === "complete") {
    validateRenderArtifact(snapshot.artifact, snapshotJob);
  } else if (snapshot.artifact !== undefined) {
    fail("RECEIPT.INVALID", "artifact is valid only for a complete snapshot");
  }
  return snapshot;
}

function ownInput(value, allowed, name, required = allowed) {
  const owned = ownCanonical(value, name, "RECEIPT.INPUT_INVALID");
  requireObject(owned, name, "RECEIPT.INPUT_INVALID");
  requireKeys(owned, allowed, required, name, "RECEIPT.INPUT_INVALID");
  requireNonblank(owned.requestId, `${name} requestId`, "RECEIPT.INPUT_INVALID");
  requireNonblank(owned.projectId, `${name} projectId`, "RECEIPT.INPUT_INVALID");
  return owned;
}

function ownResult(value) {
  const result = ownCanonical(value, "Agent result", "RECEIPT.RESULT_INVALID");
  requireObject(result, "Agent result", "RECEIPT.RESULT_INVALID");
  requireKeys(result, RESULT_KEYS, RESULT_REQUIRED_KEYS, "Agent result", "RECEIPT.RESULT_INVALID");
  if (result.protocol_version !== 1n || result.tool !== "cutly_render") {
    fail("RECEIPT.RESULT_INVALID", "Agent result must use cutly_render protocol v1");
  }
  for (const name of ["ok", "changed", "replayed", "render_required"]) {
    if (typeof result[name] !== "boolean") fail("RECEIPT.RESULT_INVALID", `${name} must be boolean`);
  }
  requireNonblank(result.request_id, "result request_id", "RECEIPT.RESULT_INVALID");
  requireNonblank(result.project_id, "result project_id", "RECEIPT.RESULT_INVALID");
  requireNonblank(result.state, "result state", "RECEIPT.RESULT_INVALID");
  if (!Array.isArray(result.diagnostics) || !Array.isArray(result.next_actions)) {
    fail("RECEIPT.RESULT_INVALID", "diagnostics and next_actions must be arrays");
  }
  if (result.transaction_id !== undefined) {
    fail("RECEIPT.RESULT_INVALID", "cutly_render result cannot contain transaction_id");
  }
  const hasError = result.diagnostics.some((diagnostic) =>
    diagnostic !== null && typeof diagnostic === "object" && diagnostic.severity === "error");
  if (result.ok === hasError) {
    fail("RECEIPT.RESULT_INVALID", "ok must be false exactly when an error diagnostic is present");
  }
  if (result.receipt !== undefined) requireObject(result.receipt, "Agent result receipt", "RECEIPT.RESULT_INVALID");
  return result;
}

function requireRenderSuccess(result, binding, action, state, jobID) {
  if (result.request_id !== binding.requestId || result.project_id !== binding.projectId || result.action !== action) {
    fail("RECEIPT.CORRELATION_INVALID", "Agent result does not match request, project, or action");
  }
  if (!result.ok) {
    const detail = result.diagnostics
      .filter((entry) => entry?.severity === "error")
      .map((entry) => `${entry.code ?? "UNKNOWN"}: ${entry.message ?? "rejected"}`)
      .join("; ");
    fail("RECEIPT.RENDER_REJECTED", detail || "render action was rejected");
  }
  if (state !== undefined && result.state !== state) {
    fail("RECEIPT.RESULT_INVALID", `successful ${action} result must be in state ${state}`);
  }
  if (jobID === undefined) {
    if (result.job_id !== undefined) fail("RECEIPT.RESULT_INVALID", `${action} result cannot contain job_id`);
  } else if (result.job_id !== jobID) {
    fail("RECEIPT.CORRELATION_INVALID", "Agent result job_id does not match the expected job");
  }
  requireObject(result.receipt, `${action} receipt`);
}

function requireReceipt(result, keys, name) {
  const receipt = result.receipt;
  requireObject(receipt, name);
  requireKeys(receipt, keys, keys, name);
  return receipt;
}

async function validatePlan(value, name) {
  requireObject(value, name);
  requireKeys(value, PLAN_KEYS, PLAN_REQUIRED_KEYS, name);
  if (value.schema_version !== 3n || value.semantic_profile !== "cutly.semantic.v1") {
    fail("RECEIPT.INVALID", `${name} uses an unsupported schema or semantic profile`);
  }
  requireNonblank(value.document_id, `${name}.document_id`);
  requireNonblank(value.timeline_id, `${name}.timeline_id`);
  requireInteger(value.revision, `${name}.revision`, 0n, UINT64_MAX);
  requireDigest(value.document_hash, `${name}.document_hash`);
  requireDigest(value.fingerprint, `${name}.fingerprint`);
  const requestedRange = requireTimeRange(value.requested_range, `${name}.requested_range`, true);
  validateRenderTarget(value.target, `${name}.target`, true);
  if (value.target.timeline_id !== value.timeline_id) {
    fail("RECEIPT.INVALID", `${name} timeline identities disagree`);
  }
  const frameRange = requireTickRange(value.frame_range, `${name}.frame_range`);
  const sampleRange = requireTickRange(value.sample_range, `${name}.sample_range`);
  const wantedFrames = compileTickRange(
    requestedRange,
    value.target.frame_clock.origin,
    fraction(value.target.frame_clock.frames_per_second.value, value.target.frame_clock.frames_per_second.rate),
    `${name}.frame_range`,
  );
  const wantedSamples = compileTickRange(
    requestedRange,
    value.target.sample_clock.origin,
    fraction(value.target.sample_clock.samples_per_second, 1n),
    `${name}.sample_range`,
  );
  if (!sameTickRange(frameRange, wantedFrames) || !sameTickRange(sampleRange, wantedSamples)) {
    fail("RECEIPT.INVALID", `${name} frame/sample ranges do not match its requested range and clocks`);
  }
  const capabilities = validateCapabilitySet(value.required_capabilities, `${name}.required_capabilities`);
  const resources = value.resources === undefined ? [] : value.resources;
  if (!Array.isArray(resources)) fail("RECEIPT.INVALID", `${name}.resources must be an array`);
  let previous;
  const resourcesByID = new Map();
  resources.forEach((resource, index) => {
    requireObject(resource, `${name}.resources[${index}]`);
    requireKeys(resource, ["asset_id", "content_hash", "kind"], ["asset_id", "kind"], `${name}.resources[${index}]`);
    requireNonblank(resource.asset_id, `${name}.resources[${index}].asset_id`);
    if (!ASSET_KINDS.has(resource.kind)) fail("RECEIPT.INVALID", `${name}.resources[${index}].kind is invalid`);
    if (resource.content_hash !== undefined && resource.content_hash !== "") {
      requireDigest(resource.content_hash, `${name}.resources[${index}].content_hash`);
    }
    if (previous !== undefined && compareUTF8(resource.asset_id, previous) <= 0) {
      fail("RECEIPT.INVALID", `${name}.resources must be sorted and unique by asset_id`);
    }
    previous = resource.asset_id;
    resourcesByID.set(resource.asset_id, resource);
  });
  const state = {
    resourcesByID,
    usedResources: new Set(),
    requiredTracks: new Set(),
    requiredAssets: new Set(),
    requiredFeatures: new Set(),
    tracksByID: new Map(),
    tracksByIndex: new Map(),
    clipIDs: new Set(),
    lastTrackEnd: new Map(),
  };
  validatePlannedClipList(value.video === undefined ? [] : value.video, "video", requestedRange, state, `${name}.video`);
  validatePlannedClipList(value.audio === undefined ? [] : value.audio, "audio", requestedRange, state, `${name}.audio`);
  if (state.usedResources.size !== resources.length) {
    fail("RECEIPT.INVALID", `${name}.resources must exactly equal the assets referenced by planned clips`);
  }
  const wantedCapabilities = {
    track_kinds: [...state.requiredTracks].sort(compareUTF8),
    asset_kinds: [...state.requiredAssets].sort(compareUTF8),
    features: [...state.requiredFeatures].sort(compareUTF8),
  };
  for (const field of ["track_kinds", "asset_kinds", "features"]) {
    if (!sameStringArray(capabilities[field], wantedCapabilities[field])) {
      fail("RECEIPT.INVALID", `${name}.required_capabilities.${field} is not derived from the planned clips`);
    }
  }
  const fingerprint = await fingerprintGoValue(goPlanFingerprintInput(value), name);
  if (fingerprint !== value.fingerprint) {
    fail("RECEIPT.FINGERPRINT_INVALID", `${name} fingerprint does not match its semantic content`);
  }
  return value;
}

function validatePlannedClipList(clips, kind, requested, state, name) {
  if (!Array.isArray(clips)) fail("RECEIPT.INVALID", `${name} must be an array`);
  let previousTrackIndex;
  let previousClipIndex;
  clips.forEach((clip, index) => {
    const clipName = `${name}[${index}]`;
    requireObject(clip, clipName);
    requireKeys(clip, PLAN_CLIP_KEYS, PLAN_CLIP_REQUIRED_KEYS, clipName);
    for (const field of ["track_id", "clip_id", "asset_id"]) requireNonblank(clip[field], `${clipName}.${field}`);
    const trackIndex = requireInteger(clip.track_index, `${clipName}.track_index`, 0n, INT32_MAX);
    const clipIndex = requireInteger(clip.clip_index, `${clipName}.clip_index`, 0n, INT32_MAX);
    if (previousTrackIndex !== undefined &&
        (trackIndex < previousTrackIndex || (trackIndex === previousTrackIndex && clipIndex <= previousClipIndex))) {
      fail("RECEIPT.INVALID", `${clipName} is not in canonical track/clip order`);
    }
    previousTrackIndex = trackIndex;
    previousClipIndex = clipIndex;
    if (state.clipIDs.has(clip.clip_id)) fail("RECEIPT.INVALID", `${clipName}.clip_id is duplicated`);
    state.clipIDs.add(clip.clip_id);

    const identity = { id: clip.track_id, kind, index: trackIndex };
    const byID = state.tracksByID.get(clip.track_id);
    const byIndex = state.tracksByIndex.get(trackIndex);
    if ((byID !== undefined && !sameTrackIdentity(byID, identity)) ||
        (byIndex !== undefined && !sameTrackIdentity(byIndex, identity))) {
      fail("RECEIPT.INVALID", `${clipName} has an inconsistent track identity`);
    }
    state.tracksByID.set(clip.track_id, identity);
    state.tracksByIndex.set(trackIndex, identity);

    const resource = state.resourcesByID.get(clip.asset_id);
    if (resource === undefined) fail("RECEIPT.INVALID", `${clipName} references an undeclared resource`);
    state.usedResources.add(clip.asset_id);
    state.requiredTracks.add(kind);
    state.requiredAssets.add(resource.kind);

    const timelineRange = requireTimeRange(clip.timeline_range, `${clipName}.timeline_range`, true);
    const sourceRange = requireTimeRange(clip.source_range, `${clipName}.source_range`, true);
    const visibleTimelineRange = requireTimeRange(clip.visible_timeline_range, `${clipName}.visible_timeline_range`, true);
    const visibleSourceRange = requireTimeRange(clip.visible_source_range, `${clipName}.visible_source_range`, true);
    const previousEnd = state.lastTrackEnd.get(clip.track_id);
    if (previousEnd !== undefined && compareTime(timelineRange.start, previousEnd) < 0) {
      fail("RECEIPT.INVALID", `${clipName} overlaps or precedes the previous clip on its track`);
    }
    state.lastTrackEnd.set(clip.track_id, addTime(timelineRange.start, timelineRange.duration, `${clipName}.timeline_range`));

    const visibleIntersection = intersectTimeRanges(timelineRange, requested, clipName);
    if (visibleIntersection === undefined || !sameTimeRange(visibleIntersection, visibleTimelineRange)) {
      fail("RECEIPT.INVALID", `${clipName}.visible_timeline_range is not the exact requested-range intersection`);
    }
    const visibleOffset = subtractTime(visibleIntersection.start, timelineRange.start, `${clipName}.visible offset`);
    const sourceOffset = scaleTimeByRatio(visibleOffset, sourceRange.duration, timelineRange.duration, `${clipName}.source offset`);
    const sourceStart = addTime(sourceRange.start, sourceOffset, `${clipName}.visible source start`);
    const sourceDuration = scaleTimeByRatio(visibleIntersection.duration, sourceRange.duration, timelineRange.duration, `${clipName}.visible source duration`);
    const wantedVisibleSource = { start: sourceStart, duration: sourceDuration };
    if (!sameTimeRange(visibleSourceRange, wantedVisibleSource)) {
      fail("RECEIPT.INVALID", `${clipName}.visible_source_range is not the exact linear projection`);
    }
    if (clip.properties !== undefined) requireObject(clip.properties, `${clipName}.properties`);
    if (compareTime(sourceRange.duration, timelineRange.duration) !== 0) {
      state.requiredFeatures.add(kind === "audio" ? "audio.linear_retime.v1" : "video.linear_retime.v1");
    }
  });
}

async function validateExecutionManifest(value, plan) {
  requireObject(value, "execution manifest");
  requireKeys(value, MANIFEST_KEYS, MANIFEST_REQUIRED_KEYS, "execution manifest");
  if (value.schema_version !== 1n || value.semantic_profile !== "cutly.semantic.v1") {
    fail("RECEIPT.INVALID", "execution manifest uses an unsupported schema or semantic profile");
  }
  if (value.plan_fingerprint !== plan.fingerprint || value.semantic_profile !== plan.semantic_profile) {
    fail("RECEIPT.BINDING_INVALID", "execution manifest is not bound to the semantic plan");
  }
  for (const field of ["compiler_build", "evaluator_build", "fingerprint"]) {
    requireDigest(value[field], `execution manifest ${field}`);
  }
  if (value.browser_fingerprint !== undefined && value.browser_fingerprint !== "") {
    requireDigest(value.browser_fingerprint, "execution manifest browser_fingerprint");
  }
  requireInteger(value.deterministic_seed, "execution manifest deterministic_seed", INT64_MIN, INT64_MAX);
  validateBackend(value.backend, plan.required_capabilities, plan.target);
  validateMediaRuntime(value.media_runtime);
  validateOutput(value.output, plan.target);
  validateManifestResources(
    value.resources === undefined ? [] : value.resources,
    plan.resources === undefined ? [] : plan.resources,
  );
  validateFonts(value.fonts === undefined ? [] : value.fonts);
  validatePlugins(value.plugins === undefined ? [] : value.plugins);
  const fingerprint = await fingerprintGoValue(goManifestFingerprintInput(value), "execution manifest");
  if (fingerprint !== value.fingerprint) {
    fail("RECEIPT.FINGERPRINT_INVALID", "execution manifest fingerprint does not match its frozen content");
  }
  return value;
}

function validateBackend(value, required, target) {
  requireObject(value, "execution manifest backend");
  requireKeys(value, [
    "asset_kinds", "backend_id", "build_fingerprint", "features", "max_height",
    "max_width", "track_kinds",
  ], ["backend_id", "build_fingerprint"], "execution manifest backend");
  requireNonblank(value.backend_id, "execution manifest backend_id");
  requireDigest(value.build_fingerprint, "execution manifest backend build_fingerprint");
  const tracks = validateSortedSet(value.track_kinds === undefined ? [] : value.track_kinds, TRACK_KINDS, "execution manifest backend track_kinds");
  const assets = validateSortedSet(value.asset_kinds === undefined ? [] : value.asset_kinds, ASSET_KINDS, "execution manifest backend asset_kinds");
  const features = validateSortedSet(value.features === undefined ? [] : value.features, undefined, "execution manifest backend features", capabilityID);
  for (const field of ["max_width", "max_height"]) {
    if (value[field] !== undefined) requireInteger(value[field], `execution manifest backend ${field}`, 0n, INT32_MAX);
  }
  if ((value.max_width !== undefined && value.max_width > 0n && target.width > value.max_width) ||
      (value.max_height !== undefined && value.max_height > 0n && target.height > value.max_height)) {
    fail("RECEIPT.BINDING_INVALID", "backend dimensions do not cover the semantic plan target");
  }
  for (const [offered, wanted, label] of [
    [tracks, required.track_kinds === undefined ? [] : required.track_kinds, "track kind"],
    [assets, required.asset_kinds === undefined ? [] : required.asset_kinds, "asset kind"],
    [features, required.features === undefined ? [] : required.features, "feature"],
  ]) {
    for (const item of wanted) {
      if (!offered.includes(item)) fail("RECEIPT.BINDING_INVALID", `backend does not offer required ${label} ${item}`);
    }
  }
}

function validateMediaRuntime(value) {
  requireObject(value, "execution manifest media_runtime");
  requireKeys(value, [
    "color_profile", "decoder_profile", "encoder_profile", "ffmpeg_build", "text_shaper_build",
  ], ["color_profile", "decoder_profile", "encoder_profile", "ffmpeg_build"], "execution manifest media_runtime");
  requireDigest(value.ffmpeg_build, "media runtime ffmpeg_build");
  for (const field of ["decoder_profile", "encoder_profile", "color_profile"]) {
    requireNonblank(value[field], `media runtime ${field}`);
  }
  if (value.text_shaper_build !== undefined && value.text_shaper_build !== "") {
    requireDigest(value.text_shaper_build, "media runtime text_shaper_build");
  }
}

function validateOutput(value, target) {
  const keys = [
    "audio_codec", "channels", "color_space", "decoded_audio_profile",
    "decoded_video_profile", "format", "height", "pixel_format", "reference_profile",
    "sample_rate", "video_codec", "width",
  ];
  requireObject(value, "execution manifest output");
  requireKeys(value, keys, keys, "execution manifest output");
  for (const field of ["reference_profile", "format", "pixel_format", "color_space", "video_codec", "audio_codec"]) {
    requireNonblank(value[field], `execution manifest output ${field}`);
  }
  requireInteger(value.width, "execution manifest output width", 1n, INT32_MAX);
  requireInteger(value.height, "execution manifest output height", 1n, INT32_MAX);
  requireInteger(value.sample_rate, "execution manifest output sample_rate", 1n, INT64_MAX);
  requireInteger(value.channels, "execution manifest output channels", 1n, INT32_MAX);
  if (value.decoded_video_profile !== "cutly.decoded-video.rgba8.interleaved.top-down.tight.v1" ||
      value.decoded_audio_profile !== "cutly.decoded-audio.pcm-s16le.interleaved.v1") {
    fail("RECEIPT.INVALID", "execution manifest output uses unsupported decoded evidence profiles");
  }
  if (value.width !== target.width || value.height !== target.height ||
      value.sample_rate !== target.sample_clock.samples_per_second || value.channels !== target.channels ||
      value.pixel_format !== target.pixel_format || value.color_space !== target.color_space) {
    fail("RECEIPT.BINDING_INVALID", "execution manifest output does not match the semantic plan target");
  }
}

function validateManifestResources(resources, planned) {
  if (!Array.isArray(resources) || !Array.isArray(planned) || resources.length !== planned.length) {
    fail("RECEIPT.BINDING_INVALID", "execution manifest resource count does not match the plan");
  }
  let previous;
  resources.forEach((resource, index) => {
    const name = `execution manifest resources[${index}]`;
    requireObject(resource, name);
    const keys = ["asset_id", "content_hash", "kind", "locator", "locator_scheme", "probe_fingerprint", "size_bytes"];
    requireKeys(resource, keys, keys, name);
    requireNonblank(resource.asset_id, `${name}.asset_id`);
    if (!ASSET_KINDS.has(resource.kind)) fail("RECEIPT.INVALID", `${name}.kind is invalid`);
    requireDigest(resource.content_hash, `${name}.content_hash`);
    requireDigest(resource.probe_fingerprint, `${name}.probe_fingerprint`);
    requireInteger(resource.size_bytes, `${name}.size_bytes`, 1n, INT64_MAX);
    requireNonblank(resource.locator, `${name}.locator`);
    if (resource.locator_scheme === "file") requireAbsolutePath(resource.locator, `${name}.locator`);
    else if (resource.locator_scheme === "cas") {
      if (resource.locator !== `cas://sha256/${resource.content_hash.slice("sha256:".length)}`) {
        fail("RECEIPT.INVALID", `${name}.locator is not bound to its content hash`);
      }
    } else fail("RECEIPT.INVALID", `${name}.locator_scheme is invalid`);
    if (previous !== undefined && compareUTF8(resource.asset_id, previous) <= 0) {
      fail("RECEIPT.INVALID", "execution manifest resources must be sorted and unique");
    }
    previous = resource.asset_id;
    const wanted = planned[index];
    if (resource.asset_id !== wanted.asset_id || resource.kind !== wanted.kind ||
        (wanted.content_hash !== undefined && wanted.content_hash !== "" && resource.content_hash !== wanted.content_hash)) {
      fail("RECEIPT.BINDING_INVALID", `${name} does not match the planned resource`);
    }
  });
}

function validateFonts(fonts) {
  if (!Array.isArray(fonts)) fail("RECEIPT.INVALID", "execution manifest fonts must be an array");
  let previous;
  fonts.forEach((font, index) => {
    const name = `execution manifest fonts[${index}]`;
    requireObject(font, name);
    requireKeys(font, ["content_hash", "family", "locator"], ["content_hash", "family", "locator"], name);
    requireNonblank(font.family, `${name}.family`);
    requireDigest(font.content_hash, `${name}.content_hash`);
    requireNonblank(font.locator, `${name}.locator`);
    if (font.locator.startsWith("cas://")) {
      if (font.locator !== `cas://sha256/${font.content_hash.slice("sha256:".length)}`) {
        fail("RECEIPT.INVALID", `${name}.locator is not bound to its content hash`);
      }
    } else {
      requireAbsolutePath(font.locator, `${name}.locator`);
    }
    if (previous !== undefined && compareStringPair(font.family, font.content_hash, previous.family, previous.content_hash) <= 0) {
      fail("RECEIPT.INVALID", "execution manifest fonts must be sorted and unique");
    }
    previous = font;
  });
}

function validatePlugins(plugins) {
  if (!Array.isArray(plugins)) fail("RECEIPT.INVALID", "execution manifest plugins must be an array");
  let previous;
  plugins.forEach((plugin, index) => {
    const name = `execution manifest plugins[${index}]`;
    requireObject(plugin, name);
    requireKeys(plugin, ["build_fingerprint", "content_hash", "id", "version"], ["content_hash", "id", "version"], name);
    requireNonblank(plugin.id, `${name}.id`);
    requireNonblank(plugin.version, `${name}.version`);
    requireDigest(plugin.content_hash, `${name}.content_hash`);
    if (plugin.build_fingerprint !== undefined && plugin.build_fingerprint !== "") {
      requireDigest(plugin.build_fingerprint, `${name}.build_fingerprint`);
    }
    if (previous !== undefined && compareStringPair(plugin.id, plugin.version, previous.id, previous.version) <= 0) {
      fail("RECEIPT.INVALID", "execution manifest plugins must be sorted and unique");
    }
    previous = plugin;
  });
}

function validatePrepareResources(resources, materialized) {
  if (!Array.isArray(resources) || resources.length !== materialized.length) {
    fail("RECEIPT.BINDING_INVALID", "prepare resource mapping count does not match the manifest");
  }
  const wanted = new Map();
  resources.forEach((resource, index) => {
    requireObject(resource, `prepare resources[${index}]`, "RECEIPT.INPUT_INVALID");
    requireKeys(resource, ["assetId", "locator"], ["assetId", "locator"], `prepare resources[${index}]`, "RECEIPT.INPUT_INVALID");
    requireNonblank(resource.assetId, `prepare resources[${index}].assetId`, "RECEIPT.INPUT_INVALID");
    requireNonblank(resource.locator, `prepare resources[${index}].locator`, "RECEIPT.INPUT_INVALID");
    requireAbsolutePath(resource.locator, `prepare resources[${index}].locator`, "RECEIPT.INPUT_INVALID");
    if (wanted.has(resource.assetId)) fail("RECEIPT.INPUT_INVALID", "prepare resources contain a duplicate assetId");
    wanted.set(resource.assetId, resource.locator);
  });
  for (const resource of materialized) {
    if (resource.locator_scheme !== "file" || wanted.get(resource.asset_id) !== resource.locator) {
      fail("RECEIPT.BINDING_INVALID", "execution manifest changed an authorized resource locator");
    }
    wanted.delete(resource.asset_id);
  }
  if (wanted.size !== 0) fail("RECEIPT.BINDING_INVALID", "execution manifest omitted an authorized resource locator");
}

function validateRenderJob(value, name, code = "RECEIPT.INVALID") {
  requireObject(value, name, code);
  requireKeys(value, JOB_KEYS, JOB_KEYS, name, code);
  if (value.schema_version !== 1n) fail(code, `${name}.schema_version must be 1`);
  requireNonblank(value.id, `${name}.id`, code);
  requireNonblank(value.idempotency_key, `${name}.idempotency_key`, code);
  requireDigest(value.plan_fingerprint, `${name}.plan_fingerprint`, code);
  requireDigest(value.execution_manifest_fingerprint, `${name}.execution_manifest_fingerprint`, code);
  requireTimeRange(value.timeline_range, `${name}.timeline_range`, true, code);
  const frames = requireTickRange(value.frame_range, `${name}.frame_range`, code);
  const samples = requireTickRange(value.sample_range, `${name}.sample_range`, code);
  if (frames.count === 0n && samples.count === 0n) fail(code, `${name} contains no work`);
  requireFileURI(value.artifact_uri, `${name}.artifact_uri`, code);
  return value;
}

function validateRenderArtifact(value, job) {
  requireObject(value, "complete render artifact");
  requireKeys(value, ARTIFACT_KEYS, ARTIFACT_KEYS, "complete render artifact");
  requireDigest(value.content_hash, "artifact content_hash");
  requireDigest(value.execution_manifest_fingerprint, "artifact execution_manifest_fingerprint");
  requireDigest(value.evidence_fingerprint, "artifact evidence_fingerprint");
  requireInteger(value.size_bytes, "artifact size_bytes", 1n, INT64_MAX);
  requireTime(value.duration, "artifact duration");
  if (value.duration.value <= 0n) fail("RECEIPT.INVALID", "artifact duration must be positive");
  requireInteger(value.frame_count, "artifact frame_count", 0n, INT64_MAX);
  requireInteger(value.sample_count, "artifact sample_count", 0n, INT64_MAX);
  if (value.uri !== job.artifact_uri || value.execution_manifest_fingerprint !== job.execution_manifest_fingerprint ||
      value.evidence_uri !== evidenceSidecarURI(job.artifact_uri) ||
      !rationalEqual(value.duration, job.timeline_range.duration) ||
      value.frame_count !== job.frame_range.count || value.sample_count !== job.sample_range.count) {
    fail("RECEIPT.BINDING_INVALID", "complete artifact is not bound to the immutable render job");
  }
}

// Go's EvidenceSidecarURI appends to URL.Path after ParseRequestURI has
// decoded RawPath, then clears RawPath before serializing. Re-encoding the
// decoded UTF-8 bytes here preserves that exact behavior for escaped
// unreserved characters and encoded path separators; concatenating the raw
// artifact URI would produce a different identity.
function evidenceSidecarURI(artifactURI) {
  const parsed = new URL(artifactURI);
  const path = decodeURIComponent(parsed.pathname);
  return `file://${escapeGoURLPath(`${path}.evidence.json`)}`;
}

function escapeGoURLPath(value) {
  let result = "";
  for (const byte of UTF8.encode(value)) {
    const unreserved =
      (byte >= 0x30 && byte <= 0x39) ||
      (byte >= 0x41 && byte <= 0x5a) ||
      (byte >= 0x61 && byte <= 0x7a) ||
      byte === 0x2d || byte === 0x2e || byte === 0x5f || byte === 0x7e;
    const pathReserved = "$&+,-/:;=@".includes(String.fromCharCode(byte));
    result += unreserved || pathReserved
      ? String.fromCharCode(byte)
      : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }
  return result;
}

function validateRenderTarget(value, name, requireTimeline) {
  requireObject(value, name);
  const keys = ["channels", "color_space", "frame_clock", "height", "pixel_format", "sample_clock", "timeline_id", "width"];
  requireKeys(value, keys, requireTimeline ? keys : keys.filter((key) => key !== "timeline_id"), name);
  if (value.timeline_id !== undefined && value.timeline_id !== "") requireNonblank(value.timeline_id, `${name}.timeline_id`);
  requireInteger(value.width, `${name}.width`, 1n, INT32_MAX);
  requireInteger(value.height, `${name}.height`, 1n, INT32_MAX);
  requireInteger(value.channels, `${name}.channels`, 1n, INT32_MAX);
  requireNonblank(value.pixel_format, `${name}.pixel_format`);
  requireNonblank(value.color_space, `${name}.color_space`);
  requireObject(value.frame_clock, `${name}.frame_clock`);
  requireKeys(value.frame_clock, ["frames_per_second", "origin"], ["frames_per_second", "origin"], `${name}.frame_clock`);
  requireTime(value.frame_clock.origin, `${name}.frame_clock.origin`);
  requireTime(value.frame_clock.frames_per_second, `${name}.frame_clock.frames_per_second`);
  if (value.frame_clock.frames_per_second.value <= 0n) fail("RECEIPT.INVALID", `${name} frame rate must be positive`);
  requireObject(value.sample_clock, `${name}.sample_clock`);
  requireKeys(value.sample_clock, ["origin", "samples_per_second"], ["origin", "samples_per_second"], `${name}.sample_clock`);
  requireTime(value.sample_clock.origin, `${name}.sample_clock.origin`);
  requireInteger(value.sample_clock.samples_per_second, `${name}.sample_clock.samples_per_second`, 1n, INT64_MAX);
}

function validateCapabilitySet(value, name) {
  requireObject(value, name);
  requireKeys(value, ["asset_kinds", "features", "track_kinds"], [], name);
  const trackKinds = validateSortedSet(value.track_kinds === undefined ? [] : value.track_kinds, TRACK_KINDS, `${name}.track_kinds`);
  const assetKinds = validateSortedSet(value.asset_kinds === undefined ? [] : value.asset_kinds, ASSET_KINDS, `${name}.asset_kinds`);
  const features = validateSortedSet(value.features === undefined ? [] : value.features, undefined, `${name}.features`, (item) =>
    new Set(["video.linear_retime.v1", "audio.linear_retime.v1", "audio.time_stretch_preserve_pitch.v1"]).has(item));
  return { track_kinds: trackKinds, asset_kinds: assetKinds, features };
}

function validateSortedSet(value, vocabulary, name, predicate) {
  if (!Array.isArray(value)) fail("RECEIPT.INVALID", `${name} must be an array`);
  let previous;
  for (const item of value) {
    if (typeof item !== "string" || (vocabulary !== undefined && !vocabulary.has(item)) || (predicate !== undefined && !predicate(item))) {
      fail("RECEIPT.INVALID", `${name} contains an invalid value`);
    }
    if (previous !== undefined && compareUTF8(item, previous) <= 0) fail("RECEIPT.INVALID", `${name} must be sorted and unique`);
    previous = item;
  }
  return value;
}

function targetWithSelectedTimeline(value, timelineID) {
  requireObject(value, "render plan requested target", "RECEIPT.INPUT_INVALID");
  validateRenderTarget(value, "render plan requested target", false);
  const result = Object.create(null);
  for (const key of Object.keys(value)) result[key] = value[key];
  if (result.timeline_id === undefined || result.timeline_id === "") result.timeline_id = timelineID;
  return ownCanonical(result, "render plan requested target", "RECEIPT.INPUT_INVALID");
}

function requireTimeRange(value, name, positive, code = "RECEIPT.INVALID") {
  requireObject(value, name, code);
  requireKeys(value, ["duration", "start"], ["duration", "start"], name, code);
  requireTime(value.start, `${name}.start`, code);
  requireTime(value.duration, `${name}.duration`, code);
  if (value.duration.value < 0n || (positive && value.duration.value === 0n)) {
    fail(code, `${name}.duration must be ${positive ? "positive" : "non-negative"}`);
  }
  addTime(value.start, value.duration, `${name}.end`, code);
  return value;
}

function requireTime(value, name, code = "RECEIPT.INVALID") {
  requireObject(value, name, code);
  requireKeys(value, ["rate", "value"], ["rate", "value"], name, code);
  requireInteger(value.value, `${name}.value`, INT64_MIN, INT64_MAX, code);
  requireInteger(value.rate, `${name}.rate`, 1n, INT64_MAX, code);
  return value;
}

function requireTickRange(value, name, code = "RECEIPT.INVALID") {
  requireObject(value, name, code);
  requireKeys(value, ["count", "first"], ["count", "first"], name, code);
  const first = requireInteger(value.first, `${name}.first`, INT64_MIN, INT64_MAX, code);
  const count = requireInteger(value.count, `${name}.count`, 0n, INT64_MAX, code);
  requireInteger(first + count, `${name}.exclusive_end`, INT64_MIN, INT64_MAX, code);
  return value;
}

function validSnapshotSequence(state, sequence) {
  if (state === "queued") return sequence === 1n;
  if (state === "running") return sequence === 2n;
  if (state === "canceling") return sequence === 2n || sequence === 3n;
  if (state === "canceled") return sequence === 3n || sequence === 4n;
  if (state === "failed") return sequence >= 2n && sequence <= 4n;
  if (state === "complete") return sequence === 3n;
  return false;
}

async function fingerprintGoValue(input, name) {
  try {
    return (await digestCanonicalValue(input, { maxInputBytes: MAX_RECEIPT_BYTES })).digest;
  } catch (error) {
    fail("RECEIPT.FINGERPRINT_INVALID", `${name} could not be fingerprinted`, error);
  }
}

function goPlanFingerprintInput(value) {
  const result = copyObject(value);
  result.fingerprint = "";
  for (const field of ["video", "audio"]) {
    if (Array.isArray(result[field])) {
      if (result[field].length === 0) delete result[field];
      else result[field] = result[field].map((clip) => {
        const normalized = copyObject(clip);
        if (isEmptyObject(normalized.properties)) delete normalized.properties;
        return normalized;
      });
    }
  }
  if (Array.isArray(result.resources)) {
    if (result.resources.length === 0) delete result.resources;
    else result.resources = result.resources.map((resource) => {
      const normalized = copyObject(resource);
      if (normalized.content_hash === "") delete normalized.content_hash;
      return normalized;
    });
  }
  const capabilities = copyObject(result.required_capabilities);
  for (const field of ["track_kinds", "asset_kinds", "features"]) {
    if (Array.isArray(capabilities[field]) && capabilities[field].length === 0) delete capabilities[field];
  }
  result.required_capabilities = capabilities;
  return result;
}

function goManifestFingerprintInput(value) {
  const result = copyObject(value);
  result.fingerprint = "";
  for (const field of ["resources", "fonts", "plugins"]) {
    if (Array.isArray(result[field]) && result[field].length === 0) delete result[field];
  }
  if (Array.isArray(result.plugins)) {
    result.plugins = result.plugins.map((plugin) => {
      const normalized = copyObject(plugin);
      if (normalized.build_fingerprint === "") delete normalized.build_fingerprint;
      return normalized;
    });
  }
  const backend = copyObject(result.backend);
  for (const field of ["track_kinds", "asset_kinds", "features"]) {
    if (Array.isArray(backend[field]) && backend[field].length === 0) delete backend[field];
  }
  for (const field of ["max_width", "max_height"]) {
    if (backend[field] === 0n) delete backend[field];
  }
  result.backend = backend;
  const mediaRuntime = copyObject(result.media_runtime);
  if (mediaRuntime.text_shaper_build === "") delete mediaRuntime.text_shaper_build;
  result.media_runtime = mediaRuntime;
  if (result.browser_fingerprint === "") delete result.browser_fingerprint;
  return result;
}

function copyObject(value) {
  const result = Object.create(null);
  for (const key of Object.keys(value)) result[key] = value[key];
  return result;
}

function isEmptyObject(value) {
  return value !== undefined && value !== null && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 0;
}

function compileTickRange(requested, origin, ticksPerSecond, name) {
  if (ticksPerSecond.n <= 0n) fail("RECEIPT.INVALID", `${name} ticks per second must be positive`);
  const end = addTime(requested.start, requested.duration, `${name} requested end`);
  const firstPosition = multiplyFraction(
    subtractFraction(timeFraction(requested.start), timeFraction(origin)),
    ticksPerSecond,
  );
  const endPosition = multiplyFraction(
    subtractFraction(timeFraction(end), timeFraction(origin)),
    ticksPerSecond,
  );
  const first = ceilFractionToInt64(firstPosition, `${name}.first`);
  const endExclusive = ceilFractionToInt64(endPosition, `${name}.exclusive_end`);
  const count = endExclusive - first;
  requireInteger(count, `${name}.count`, 0n, INT64_MAX);
  return { first, count };
}

function intersectTimeRanges(left, right, name) {
  const leftEnd = addTime(left.start, left.duration, `${name}.left end`);
  const rightEnd = addTime(right.start, right.duration, `${name}.right end`);
  const start = compareTime(right.start, left.start) > 0 ? right.start : left.start;
  const end = compareTime(rightEnd, leftEnd) < 0 ? rightEnd : leftEnd;
  const duration = subtractTime(end, start, `${name}.intersection duration`);
  if (compareTime(duration, { value: 0n, rate: 1n }) <= 0) return undefined;
  return { start, duration };
}

function addTime(left, right, name, code = "RECEIPT.INVALID") {
  return timeFromFraction(addFraction(timeFraction(left), timeFraction(right)), name, code);
}

function subtractTime(left, right, name, code = "RECEIPT.INVALID") {
  return timeFromFraction(subtractFraction(timeFraction(left), timeFraction(right)), name, code);
}

function scaleTimeByRatio(value, numerator, denominator, name) {
  if (denominator.value === 0n) fail("RECEIPT.INVALID", `${name} denominator must not be zero`);
  const scaled = divideFraction(
    multiplyFraction(timeFraction(value), timeFraction(numerator)),
    timeFraction(denominator),
  );
  return timeFromFraction(scaled, name);
}

function timeFraction(value) {
  return fraction(value.value, value.rate);
}

function timeFromFraction(value, name, code = "RECEIPT.INVALID") {
  const normalized = fraction(value.n, value.d);
  requireInteger(normalized.n, `${name}.value`, INT64_MIN, INT64_MAX, code);
  requireInteger(normalized.d, `${name}.rate`, 1n, INT64_MAX, code);
  return { value: normalized.n, rate: normalized.d };
}

function fraction(numerator, denominator) {
  if (denominator === 0n) fail("RECEIPT.INVALID", "rational denominator must not be zero");
  if (denominator < 0n) {
    numerator = -numerator;
    denominator = -denominator;
  }
  if (numerator === 0n) return { n: 0n, d: 1n };
  const divisor = gcd(absBigInt(numerator), denominator);
  return { n: numerator / divisor, d: denominator / divisor };
}

function addFraction(left, right) {
  return fraction(left.n * right.d + right.n * left.d, left.d * right.d);
}

function subtractFraction(left, right) {
  return fraction(left.n * right.d - right.n * left.d, left.d * right.d);
}

function multiplyFraction(left, right) {
  return fraction(left.n * right.n, left.d * right.d);
}

function divideFraction(left, right) {
  if (right.n === 0n) fail("RECEIPT.INVALID", "cannot divide by zero");
  return fraction(left.n * right.d, left.d * right.n);
}

function ceilFractionToInt64(value, name) {
  let quotient = value.n / value.d;
  if (value.n % value.d > 0n) quotient += 1n;
  return requireInteger(quotient, name, INT64_MIN, INT64_MAX);
}

function compareTime(left, right) {
  const comparison = left.value * right.rate - right.value * left.rate;
  return comparison < 0n ? -1 : comparison > 0n ? 1 : 0;
}

function sameTimeRange(left, right) {
  return sameTime(left.start, right.start) && sameTime(left.duration, right.duration);
}

function sameTime(left, right) {
  return left.value === right.value && left.rate === right.rate;
}

function sameTickRange(left, right) {
  return left.first === right.first && left.count === right.count;
}

function sameTrackIdentity(left, right) {
  return left.id === right.id && left.kind === right.kind && left.index === right.index;
}

function sameStringArray(left, right) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function compareStringPair(leftFirst, leftSecond, rightFirst, rightSecond) {
  const first = compareUTF8(leftFirst, rightFirst);
  return first === 0 ? compareUTF8(leftSecond, rightSecond) : first;
}

function compareUTF8(left, right) {
  if (left === right) return 0;
  const leftBytes = UTF8.encode(left);
  const rightBytes = UTF8.encode(right);
  const length = Math.min(leftBytes.length, rightBytes.length);
  for (let index = 0; index < length; index += 1) {
    if (leftBytes[index] !== rightBytes[index]) return leftBytes[index] < rightBytes[index] ? -1 : 1;
  }
  return leftBytes.length < rightBytes.length ? -1 : 1;
}

function absBigInt(value) {
  return value < 0n ? -value : value;
}

function gcd(left, right) {
  while (right !== 0n) {
    const remainder = left % right;
    left = right;
    right = remainder;
  }
  return left;
}

function canonicalEqual(left, right) {
  let leftBytes;
  let rightBytes;
  try {
    leftBytes = encodeCanonicalValue(left, { maxInputBytes: MAX_RECEIPT_BYTES });
    rightBytes = encodeCanonicalValue(right, { maxInputBytes: MAX_RECEIPT_BYTES });
  } catch {
    return false;
  }
  if (leftBytes.byteLength !== rightBytes.byteLength) return false;
  return leftBytes.every((value, index) => value === rightBytes[index]);
}

function ownCanonical(value, name, code) {
  try {
    const bytes = encodeCanonicalValue(value, { maxInputBytes: MAX_RECEIPT_BYTES });
    return decodeCanonicalJSON(bytes, { maxInputBytes: MAX_RECEIPT_BYTES });
  } catch (error) {
    if (error instanceof AgentReceiptError) throw error;
    fail(code, `${name} is not safely canonicalizable`, error);
  }
}

function requireKeys(value, allowed, required, name, code = "RECEIPT.INVALID") {
  const allowedSet = new Set(allowed);
  for (const key of Object.keys(value)) {
    if (!allowedSet.has(key)) fail(code, `${name} has unknown field ${key}`);
  }
  for (const key of required) {
    if (!Object.hasOwn(value, key)) fail(code, `${name} is missing field ${key}`);
  }
}

function requireObject(value, name, code = "RECEIPT.INVALID") {
  if (value === null || typeof value !== "object" || Array.isArray(value)) fail(code, `${name} must be an object`);
  return value;
}

function requireInteger(value, name, minimum, maximum, code = "RECEIPT.INVALID") {
  if (typeof value !== "bigint" || value < minimum || value > maximum) {
    fail(code, `${name} must be an integer in [${minimum}, ${maximum}]`);
  }
  return value;
}

function requireDigest(value, name, code = "RECEIPT.INVALID") {
  if (typeof value !== "string" || !/^sha256:[0-9a-f]{64}$/.test(value)) {
    fail(code, `${name} must be a canonical SHA-256 digest`);
  }
  return value;
}

function requireNonblank(value, name, code = "RECEIPT.INVALID") {
  if (!isNonblank(value)) fail(code, `${name} must be a non-blank string`);
  return value;
}

function isNonblank(value) {
  return typeof value === "string" && value.length > 0 && !/^\p{White_Space}*$/u.test(value);
}

function capabilityID(value) {
  return typeof value === "string" && /^[a-z0-9][a-z0-9._-]*\.v[1-9][0-9]*$/.test(value);
}

function requireAbsolutePath(value, name, code = "RECEIPT.INVALID") {
  if (value === "/") return;
  if (typeof value !== "string" || !value.startsWith("/") || value.includes("\0") ||
      value.split("/").slice(1).some((part) => part === "" || part === "." || part === "..")) {
    fail(code, `${name} must be an absolute clean local path`);
  }
}

function requireFileURI(value, name, code) {
  requireNonblank(value, name, code);
  let parsed;
  try {
    parsed = new URL(value);
  } catch (error) {
    fail(code, `${name} must be an absolute file URL`, error);
  }
  if (parsed.protocol !== "file:" || parsed.hostname !== "" || parsed.username !== "" ||
      parsed.password !== "" || parsed.search !== "" || parsed.hash !== "" ||
      parsed.pathname === "/" || parsed.href !== value) {
    fail(code, `${name} must be a canonical hostless non-root file URL`);
  }
  let path;
  try {
    path = decodeURIComponent(parsed.pathname);
  } catch (error) {
    fail(code, `${name} contains invalid percent encoding`, error);
  }
  if (!path.startsWith("/") || path.includes("\0") ||
      path.split("/").slice(1).some((part) => part === "" || part === "." || part === "..")) {
    fail(code, `${name} path must be absolute and clean`);
  }
}

function rationalEqual(left, right) {
  return left.value * right.rate === right.value * left.rate;
}

function fail(code, detail, cause) {
  throw new AgentReceiptError(code, detail, cause);
}
