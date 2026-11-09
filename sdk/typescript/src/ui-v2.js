export const DOCUMENT_V2_UI_PROFILE = "cutly.headless-ui.document-v2.v2";

const trackKinds = new Set(["video", "audio", "text", "vector", "annotation"]);

// documentV2TimelinePrimitives projects immutable read models only. It never
// returns a mutable Document collection and never writes document JSON; edits
// must use the transaction-v2 builders exported by this package.
export function documentV2TimelinePrimitives(document, timelineId) {
  object(document, "document");
  if (document.schema_version !== 2 && document.schema_version !== 2n) fail("document.schema_version must be 2");
  const documentId = identity(document.id, "document.id");
  const revision = unsigned(document.revision, "document.revision");
  if (!Array.isArray(document.timelines)) fail("document.timelines must be an array");
  const selected = document.timelines.find((timeline) => timeline?.id === timelineId);
  if (selected === undefined) fail("timelineId does not select a Document v2 timeline");
  object(selected, "timeline");
  if (!Array.isArray(selected.tracks)) fail("timeline.tracks must be an array");
  const assets = new Map();
  if (!Array.isArray(document.assets)) fail("document.assets must be an array");
  for (const asset of document.assets) {
    object(asset, "asset");
    const id = identity(asset.id, "asset.id");
    if (assets.has(id)) fail(`duplicate asset ${JSON.stringify(id)}`);
    assets.set(id, asset.kind);
  }
  const seenTracks = new Set();
  const seenClips = new Set();
  const tracks = selected.tracks.map((track, trackIndex) => {
    object(track, `tracks[${trackIndex}]`);
    const id = identity(track.id, `tracks[${trackIndex}].id`);
    if (seenTracks.has(id)) fail(`duplicate track ${JSON.stringify(id)}`);
    seenTracks.add(id);
    if (!trackKinds.has(track.kind)) fail(`unsupported track kind ${JSON.stringify(track.kind)}`);
    if (!Array.isArray(track.clips)) fail(`tracks[${trackIndex}].clips must be an array`);
    const clips = track.clips.map((clip, clipIndex) => {
      object(clip, `tracks[${trackIndex}].clips[${clipIndex}]`);
      const clipId = identity(clip.id, `tracks[${trackIndex}].clips[${clipIndex}].id`);
      if (seenClips.has(clipId)) fail(`duplicate clip ${JSON.stringify(clipId)}`);
      seenClips.add(clipId);
      const assetId = identity(clip.asset_id, `tracks[${trackIndex}].clips[${clipIndex}].asset_id`);
      if (!assets.has(assetId)) fail(`clip ${JSON.stringify(clipId)} references a missing asset`);
      return Object.freeze({
        profile: DOCUMENT_V2_UI_PROFILE,
        kind: "clip",
        id: clipId,
        track_id: id,
        track_kind: track.kind,
        asset_id: assetId,
        asset_kind: assets.get(assetId),
        timeline_range: range(clip.timeline_range, `clip ${clipId}.timeline_range`),
        source_range: range(clip.source_range, `clip ${clipId}.source_range`),
        disabled: clip.disabled === true,
        keyframe_lane_ids: Object.freeze((clip.keyframe_lanes ?? []).map((lane, index) => identity(lane?.id, `clip ${clipId}.keyframe_lanes[${index}].id`))),
      });
    });
    return Object.freeze({
      profile: DOCUMENT_V2_UI_PROFILE,
      kind: "track",
      id,
      track_kind: track.kind,
      audio_bus_id: typeof track.audio_bus_id === "string" ? track.audio_bus_id : "",
      clips: Object.freeze(clips),
    });
  });
  return Object.freeze({
    profile: DOCUMENT_V2_UI_PROFILE,
    kind: "timeline",
    document_id: documentId,
    timeline_id: identity(selected.id, "timeline.id"),
    revision,
    tracks: Object.freeze(tracks),
  });
}

function range(value, path) {
  object(value, path);
  const start = rational(value.start, `${path}.start`);
  const duration = rational(value.duration, `${path}.duration`);
  if (duration.value <= 0n) fail(`${path}.duration must be positive`);
  return Object.freeze({ start, duration });
}

function rational(value, path) {
  object(value, path);
  const numerator = signed(value.value, `${path}.value`);
  const denominator = signed(value.rate, `${path}.rate`);
  if (denominator <= 0n) fail(`${path}.rate must be positive`);
  return Object.freeze({ value: numerator, rate: denominator });
}

function signed(value, path) {
  let result;
  if (typeof value === "bigint") result = value;
  else if (typeof value === "number" && Number.isSafeInteger(value)) result = BigInt(value);
  else if (typeof value === "string" && /^-?(?:0|[1-9][0-9]*)$/u.test(value)) result = BigInt(value);
  else fail(`${path} must be an exact integer`);
  if (result < -(1n << 63n) || result > ((1n << 63n) - 1n)) fail(`${path} is outside int64`);
  return result;
}

function unsigned(value, path) {
  const result = signed(value, path);
  if (result < 0n) fail(`${path} must be non-negative`);
  return result;
}

function identity(value, path) {
  if (typeof value !== "string" || value.trim() === "" || new TextEncoder().encode(value).byteLength > 1024) fail(`${path} must be a bounded identity`);
  return value;
}

function object(value, path) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) fail(`${path} must be an object`);
}

function fail(detail) {
  throw new TypeError(`@cutly/sdk Document v2 UI: ${detail}`);
}
