export const MOTION_COMPOSITION_V2_PROFILE = "cutly.motion-composition.document-v2.v1";

export class MotionCompositionV2Error extends Error {
  constructor(code, detail) {
    super(`@cutly/sdk motion composition: ${code}: ${detail}`);
    this.name = "MotionCompositionV2Error";
    this.code = code;
  }
}

const ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,1023}$/;
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const KINDS = new Set(["video", "audio", "text", "vector"]);

export function motionRationalV2(value, denominator) {
  if (denominator !== undefined) return reduce(integer(value, "numerator"), integer(denominator, "denominator"));
  if (value !== null && typeof value === "object") return reduce(integer(value.numerator, "numerator"), integer(value.denominator, "denominator"));
  if (typeof value === "bigint" || typeof value === "number") return reduce(integer(value, "value"), 1n);
  if (typeof value !== "string" || value.trim() !== value || value === "") fail("MOTION.RATIONAL", "value must be an exact integer, decimal, fraction, or rational object");
  if (/^-?[0-9]+\/[1-9][0-9]*$/.test(value)) {
    const [left, right] = value.split("/");
    return reduce(BigInt(left), BigInt(right));
  }
  const match = /^(-?)([0-9]+)(?:\.([0-9]+))?$/.exec(value);
  if (match === null) fail("MOTION.RATIONAL", "decimal string is invalid");
  const fraction = match[3] ?? "";
  const denominatorValue = 10n ** BigInt(fraction.length);
  const numerator = BigInt(`${match[1]}${match[2]}${fraction}`);
  return reduce(numerator, denominatorValue);
}

export function motionPointV2(x, y) {
  return { x: motionRationalV2(x), y: motionRationalV2(y) };
}

export function motionEaseV2(name) {
  switch (name) {
    case "expo.out": return bezier("0.16", 1, "0.3", 1);
    case "power3.out": return bezier("0.22", 1, "0.36", 1);
    case "power2.inOut": return bezier("0.6", 0, "0.8", 1);
    default: fail("MOTION.EASING", `unsupported easing ${String(name)}`);
  }
}

export function createMotionCompositionV2(config) {
  return new MotionCompositionV2(config);
}

export class MotionCompositionV2 {
  #config;
  #assets = [];
  #assetIDs = new Set();
  #layers = [];
  #layerIDs = new Set();
  #transitions = [];
  #audioBuses = [];

  constructor(config) {
    if (config === null || typeof config !== "object") fail("MOTION.CONFIG", "config is required");
    const id = identity(config.id, "id");
    const timelineId = identity(config.timelineId ?? "main", "timelineId");
    const width = positive(config.width, "width");
    const height = positive(config.height, "height");
    const fps = positive(config.fps, "fps");
    const durationFrames = positive(config.durationFrames, "durationFrames");
    const revision = positive(config.revision ?? 1, "revision");
    this.#config = { id, timelineId, width, height, fps, durationFrames, revision, name: optionalText(config.name) };
  }

  imageAsset(spec) { return this.#resourceAsset(spec, "image"); }
  fontAsset(spec) { return this.#resourceAsset(spec, "font"); }

  audioAsset(spec) {
    const asset = this.#resourceAsset(spec, "audio", false);
    asset.duration = frameTime(positive(spec.durationFrames, "audio durationFrames"), this.#config.fps);
    return this;
  }

  textAsset(spec) {
    object(spec, "text asset");
    const id = this.#newAssetID(spec.id);
    const fontAssetId = identity(spec.fontAssetId, "fontAssetId");
    const width = positive(spec.width, "text width");
    const height = positive(spec.height, "text height");
    const text = requiredText(spec.text, "text");
    this.#assets.push({
      id, kind: "text", text: {
        profile: "cutly.text.pinned-font-viewport-px.v2",
        text,
        font_asset_id: fontAssetId,
        font_size: motionRationalV2(spec.fontSize),
        line_height: motionRationalV2(spec.lineHeight),
        viewport: { width_pixels: width, height_pixels: height },
        fill: rgba(spec.fill),
        language: requiredText(spec.language ?? "en", "language"),
        script: requiredText(spec.script ?? "Latn", "script"),
        direction: enumValue(spec.direction ?? "ltr", new Set(["ltr", "rtl"]), "direction"),
        alignment: enumValue(spec.alignment ?? "start", new Set(["start", "center", "end"]), "alignment"),
      },
    });
    return this;
  }

  rectangleAsset(spec) {
    object(spec, "rectangle asset");
    const id = this.#newAssetID(spec.id);
    const width = positive(spec.width, "rectangle width");
    const height = positive(spec.height, "rectangle height");
    const zero = motionRationalV2(0);
    const w = motionRationalV2(width);
    const h = motionRationalV2(height);
    const point = (x, y) => ({ x: motionRationalV2(x), y: motionRationalV2(y) });
    this.#assets.push({ id, kind: "vector", vector: {
      profile: "cutly.vector.path-viewport-px.v2",
      view_box: { x: zero, y: zero, width: w, height: h },
      viewport: { width_pixels: width, height_pixels: height },
      paths: [{
        id: `${id}-path`, fill_rule: "nonzero", fill: rgba(spec.fill), commands: [
          { op: "move_to", points: [point(0, 0)] },
          { op: "line_to", points: [point(width, 0)] },
          { op: "line_to", points: [point(width, height)] },
          { op: "line_to", points: [point(0, height)] },
          { op: "close" },
        ],
      }],
    }});
    return this;
  }

  audioBus(spec) {
    object(spec, "audio bus");
    const kind = enumValue(spec.kind, new Set(["group", "master"]), "audio bus kind");
    this.#audioBuses.push({
      id: identity(spec.id, "audio bus id"), kind,
      ...(spec.name === undefined ? {} : { name: requiredText(spec.name, "audio bus name") }),
      ...(spec.parentBusId === undefined ? {} : { parent_bus_id: identity(spec.parentBusId, "parentBusId") }),
      properties: {
        gain_linear: motionRationalV2(spec.gain ?? 1),
        pan: motionRationalV2(spec.pan ?? 0),
        ...(spec.limiter === undefined ? {} : { sample_peak_limiter: { ceiling_linear: motionRationalV2(spec.limiter) } }),
      },
    });
    return this;
  }

  scene(id, range, author) {
    const scene = new MotionSceneV2(this, identity(id, "scene id"), range);
    if (author !== undefined) {
      if (typeof author !== "function") fail("MOTION.SCENE", "author must be a function");
      author(scene);
    }
    return scene;
  }

  layer(spec) {
    object(spec, "layer");
    const id = identity(spec.id, "layer id");
    if (this.#layerIDs.has(id)) fail("MOTION.IDENTITY", `duplicate layer ${id}`);
    const trackKind = enumValue(spec.trackKind, KINDS, "trackKind");
    const startFrame = nonnegative(spec.startFrame ?? 0, "startFrame");
    const durationFrames = positive(spec.durationFrames, "durationFrames");
    if (startFrame + durationFrames > this.#config.durationFrames) fail("MOTION.RANGE", `layer ${id} exceeds the composition`);
    const state = {
      id, name: optionalText(spec.name), trackKind, assetId: identity(spec.assetId, "assetId"),
      startFrame, durationFrames, sourceStartFrame: nonnegative(spec.sourceStartFrame ?? 0, "sourceStartFrame"),
      audioBusId: spec.audioBusId === undefined ? "" : identity(spec.audioBusId, "audioBusId"), disabled: spec.disabled === true,
      transform: undefined, crop: undefined, opacity: undefined,
      animation: { position: [], scale: [], rotation: [], opacity: [] },
    };
    this.#layerIDs.add(id);
    this.#layers.push(state);
    return new MotionLayerV2(state);
  }

  dissolve(spec) {
    object(spec, "dissolve");
    this.#transitions.push({
      id: identity(spec.id, "transition id"), profile: "cutly.transition.dissolve.v1",
      outgoing_clip_id: identity(spec.outgoingLayerId, "outgoingLayerId"),
      incoming_clip_id: identity(spec.incomingLayerId, "incomingLayerId"),
      pre_duration: frameTime(nonnegative(spec.preFrames ?? 0, "preFrames"), this.#config.fps),
      post_duration: frameTime(nonnegative(spec.postFrames ?? 0, "postFrames"), this.#config.fps),
    });
    return this;
  }

  build() {
    if (this.#layers.length === 0) fail("MOTION.LAYERS", "at least one layer is required");
    for (const layer of this.#layers) {
      if (!this.#assetIDs.has(layer.assetId)) fail("MOTION.RESOURCE", `layer ${layer.id} references missing asset ${layer.assetId}`);
    }
    const tracks = this.#layers.map((layer, index) => compileLayer(layer, index, this.#config.fps));
    const document = {
      schema_version: 2n,
      id: this.#config.id,
      revision: this.#config.revision,
      assets: this.#assets,
      timelines: [{
        id: this.#config.timelineId,
        ...(this.#config.name === undefined ? {} : { name: this.#config.name }),
        tracks,
        ...(this.#transitions.length === 0 ? {} : { transitions: this.#transitions }),
      }],
      ...(this.#audioBuses.length === 0 ? {} : { audio_buses: this.#audioBuses }),
      metadata: {
        "cutly.motion": {
          profile: MOTION_COMPOSITION_V2_PROFILE,
          width: this.#config.width,
          height: this.#config.height,
          frames_per_second: this.#config.fps,
          duration_frames: this.#config.durationFrames,
        },
      },
    };
    return deepFreeze(document);
  }

  get durationFrames() { return this.#config.durationFrames; }
  get fps() { return this.#config.fps; }

  #resourceAsset(spec, kind, chain = true) {
    object(spec, `${kind} asset`);
    const id = this.#newAssetID(spec.id);
    const uri = requiredText(spec.uri, "uri");
    const contentHash = requiredText(spec.contentHash, "contentHash");
    if (!DIGEST.test(contentHash)) fail("MOTION.DIGEST", "contentHash must be canonical SHA-256");
    const asset = { id, kind, uri, content_hash: contentHash };
    this.#assets.push(asset);
    return chain ? this : asset;
  }

  #newAssetID(value) {
    const id = identity(value, "asset id");
    if (this.#assetIDs.has(id)) fail("MOTION.IDENTITY", `duplicate asset ${id}`);
    this.#assetIDs.add(id);
    return id;
  }
}

export class MotionSceneV2 {
  #composition;
  #id;
  #start;
  #duration;

  constructor(composition, id, range) {
    object(range, "scene range");
    this.#composition = composition;
    this.#id = id;
    this.#start = nonnegative(range.startFrame ?? 0, "scene startFrame");
    this.#duration = positive(range.durationFrames, "scene durationFrames");
    if (this.#start + this.#duration > composition.durationFrames) fail("MOTION.RANGE", `scene ${id} exceeds the composition`);
  }

  image(id, assetId, options = {}) { return this.#layer("video", id, assetId, options); }
  text(id, assetId, options = {}) { return this.#layer("text", id, assetId, options); }
  shape(id, assetId, options = {}) { return this.#layer("vector", id, assetId, options); }
  audio(id, assetId, options = {}) { return this.#layer("audio", id, assetId, options); }

  #layer(trackKind, id, assetId, options) {
    object(options, "scene layer options");
    const localStart = nonnegative(options.startFrame ?? 0, "local startFrame");
    const duration = positive(options.durationFrames ?? (this.#duration - localStart), "layer durationFrames");
    if (localStart + duration > this.#duration) fail("MOTION.RANGE", `layer ${id} exceeds scene ${this.#id}`);
    return this.#composition.layer({
      ...options, id, assetId, trackKind,
      startFrame: this.#start + localStart,
      durationFrames: duration,
    });
  }
}

export class MotionLayerV2 {
  #state;
  constructor(state) { this.#state = state; }

  position(x, y) { this.#transform().position = motionPointV2(x, y); return this; }
  scale(x, y = x) { this.#transform().scale = motionPointV2(x, y); return this; }
  rotate(degrees) { this.#transform().rotation_degrees = motionRationalV2(degrees); return this; }
  anchor(x, y) { this.#transform().anchor = motionPointV2(x, y); return this; }
  opacity(value) { this.#state.opacity = motionRationalV2(value); return this; }

  crop({ x, y, width, height }) {
    this.#state.crop = { x: motionRationalV2(x), y: motionRationalV2(y), width: motionRationalV2(width), height: motionRationalV2(height) };
    return this;
  }

  animatePosition(frames) { this.#state.animation.position = pointFrames(frames, "position"); return this; }
  animateScale(frames) { this.#state.animation.scale = pointFrames(frames, "scale"); return this; }
  animateRotation(frames) { this.#state.animation.rotation = scalarFrames(frames, "rotation"); return this; }
  animateOpacity(frames) { this.#state.animation.opacity = scalarFrames(frames, "opacity"); return this; }

  #transform() {
    if (this.#state.transform === undefined) this.#state.transform = identityTransform();
    return this.#state.transform;
  }
}

function compileLayer(layer, index, fps) {
  const range = { start: frameTime(layer.startFrame, fps), duration: frameTime(layer.durationFrames, fps) };
  const clip = {
    id: layer.id, asset_id: layer.assetId, timeline_range: range,
    source_range: { start: frameTime(layer.sourceStartFrame, fps), duration: range.duration },
    ...(layer.disabled ? { disabled: true } : {}),
  };
  const animated = Object.values(layer.animation).some((values) => values.length !== 0);
  if (layer.transform !== undefined || layer.crop !== undefined || layer.opacity !== undefined || animated) {
    clip.property_set = {
      transform: layer.transform ?? identityTransform(),
      opacity: { value: layer.opacity ?? motionRationalV2(1) },
      ...(layer.crop === undefined ? {} : { crop: { rect: layer.crop } }),
    };
  }
  const lanes = [];
  addLane(lanes, layer, "position", "transform.position", "point", fps);
  addLane(lanes, layer, "scale", "transform.scale", "point", fps);
  addLane(lanes, layer, "rotation", "transform.rotation_degrees", "scalar", fps);
  addLane(lanes, layer, "opacity", "opacity.value", "scalar", fps);
  if (lanes.length !== 0) clip.keyframe_lanes = lanes;
  return {
    id: `motion-layer-${String(index).padStart(4, "0")}-${layer.id}`,
    kind: layer.trackKind,
    ...(layer.name === undefined ? {} : { name: layer.name }),
    ...(layer.audioBusId === "" ? {} : { audio_bus_id: layer.audioBusId }),
    clips: [clip],
  };
}

function addLane(lanes, layer, name, target, valueType, fps) {
  const frames = layer.animation[name];
  if (frames.length === 0) return;
  lanes.push({
    id: `${layer.id}-${name}`, target, value_type: valueType,
    keyframes: frames.map((frame, index) => ({
      offset: frameTime(frame.frame, fps),
      value: valueType === "point" ? { point: frame.value } : { scalar: frame.value },
      ...(index === frames.length - 1 ? {} : interpolation(frame)),
    })),
  });
}

function interpolation(frame) {
  if (frame.interpolation === "hold") return { interpolation: "hold" };
  if (frame.easing === undefined || frame.easing === "linear") return { interpolation: "linear" };
  return { interpolation: "cubic_bezier", easing: typeof frame.easing === "string" ? motionEaseV2(frame.easing) : bezierObject(frame.easing) };
}

function pointFrames(input, name) {
  return keyframes(input, name, (value) => {
    if (!Array.isArray(value) || value.length !== 2) fail("MOTION.KEYFRAME", `${name} value must be [x, y]`);
    return motionPointV2(value[0], value[1]);
  });
}

function scalarFrames(input, name) { return keyframes(input, name, motionRationalV2); }

function keyframes(input, name, convert) {
  if (!Array.isArray(input) || input.length < 2) fail("MOTION.KEYFRAME", `${name} requires at least two keyframes`);
  let previous = -1n;
  return input.map((entry) => {
    object(entry, `${name} keyframe`);
    const frame = nonnegative(entry.frame, `${name} frame`);
    if (frame <= previous) fail("MOTION.KEYFRAME", `${name} frames must be strictly increasing`);
    previous = frame;
    return { frame, value: convert(entry.value), interpolation: entry.interpolation, easing: entry.easing };
  });
}

function identityTransform() {
  return { position: motionPointV2(0, 0), scale: motionPointV2(1, 1), rotation_degrees: motionRationalV2(0), anchor: motionPointV2(0, 0) };
}

function frameTime(frame, fps) {
  if (frame === 0n) return { value: 0n, rate: 1n };
  const divisor = gcd(frame, fps);
  return { value: frame / divisor, rate: fps / divisor };
}

function rgba(value) {
  object(value, "RGBA16 color");
  const channel = (name, fallback = 0n) => {
    const result = value[name] === undefined ? fallback : nonnegative(value[name], `color ${name}`);
    if (result > 65535n) fail("MOTION.COLOR", `${name} exceeds 65535`);
    return result;
  };
  return { r: channel("r"), g: channel("g"), b: channel("b"), a: channel("a", 65535n) };
}

function bezier(x1, y1, x2, y2) { return { x1: motionRationalV2(x1), y1: motionRationalV2(y1), x2: motionRationalV2(x2), y2: motionRationalV2(y2) }; }
function bezierObject(value) { object(value, "easing"); return bezier(value.x1, value.y1, value.x2, value.y2); }

function reduce(numerator, denominator) {
  if (denominator <= 0n) fail("MOTION.RATIONAL", "denominator must be positive");
  const divisor = gcd(numerator < 0n ? -numerator : numerator, denominator);
  const n = numerator / divisor;
  const d = denominator / divisor;
  return { numerator: n === 0n ? 0n : n, denominator: n === 0n ? 1n : d };
}

function gcd(left, right) {
  while (right !== 0n) [left, right] = [right, left % right];
  return left === 0n ? 1n : left;
}

function integer(value, path) {
  if (typeof value === "bigint") return value;
  if (typeof value === "number" && Number.isSafeInteger(value)) return BigInt(value);
  if (typeof value === "string" && /^-?(?:0|[1-9][0-9]*)$/.test(value)) return BigInt(value);
  fail("MOTION.INTEGER", `${path} must be an exact integer`);
}

function positive(value, path) { const result = integer(value, path); if (result <= 0n) fail("MOTION.INTEGER", `${path} must be positive`); return result; }
function nonnegative(value, path) { const result = integer(value, path); if (result < 0n) fail("MOTION.INTEGER", `${path} must be nonnegative`); return result; }
function object(value, path) { if (value === null || typeof value !== "object" || Array.isArray(value)) fail("MOTION.OBJECT", `${path} must be an object`); }
function identity(value, path) { if (typeof value !== "string" || !ID.test(value)) fail("MOTION.IDENTITY", `${path} is invalid`); return value; }
function requiredText(value, path) { if (typeof value !== "string" || value.length === 0) fail("MOTION.TEXT", `${path} is required`); return value; }
function optionalText(value) { return value === undefined ? undefined : requiredText(value, "text"); }
function enumValue(value, allowed, path) { if (!allowed.has(value)) fail("MOTION.ENUM", `${path} is unsupported`); return value; }
function fail(code, detail) { throw new MotionCompositionV2Error(code, detail); }

function deepFreeze(value) {
  if (value !== null && typeof value === "object" && !Object.isFrozen(value)) {
    for (const child of Object.values(value)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}
