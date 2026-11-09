const INT64_MIN = -(1n << 63n);
const INT64_MAX = (1n << 63n) - 1n;
const UINT64_MAX = (1n << 64n) - 1n;

export const PLAYER_PROFILE = "cutly.player.exact-audio-master.v1";
export const DOCUMENT_V2_PLAYER_PROFILE = "cutly.player.document-v2.exact-audio-master.v2";

export class PlayerStateError extends Error {
  constructor(code, detail) {
    super(`@cutly/sdk player: ${code}: ${detail}`);
    this.name = "PlayerStateError";
    this.code = code;
  }
}

// HeadlessPlayer is a transport- and DOM-independent playback state machine.
// The audio sample clock is authoritative; frame selection uses exact bigint
// rational arithmetic and therefore matches the composition-wide export grid.
export class HeadlessPlayer {
  #config;
  #desiredPlaying = false;
  #phase = "idle";
  #frameIndex = null;
  #sampleIndex = null;
  #buffered = [];
  #bufferStateKnown = false;
  #loop = null;
  #epoch = 0n;
  #requestSequence = 0n;
  #presentedRequest = null;
  #listeners = new Set();

  constructor(config) {
    this.load(config);
  }

  load(config) {
    const normalized = normalizeConfig(config);
    this.#config = normalized;
    this.#desiredPlaying = false;
    this.#frameIndex = normalized.frameRange.count === 0n ? null : normalized.frameRange.first;
    this.#sampleIndex = normalized.sampleRange.first;
    this.#buffered = [];
    this.#bufferStateKnown = false;
    this.#loop = null;
    this.#epoch += 1n;
    this.#requestSequence = 0n;
    this.#presentedRequest = null;
    this.#phase = this.#frameIndex === null ? "ended" : "ready";
    this.#emit("load");
    return this.snapshot();
  }

  snapshot() {
    return Object.freeze({
      profile: this.#config.profile,
      revision: this.#config.revision.toString(),
      phase: this.#phase,
      desiredPlaying: this.#desiredPlaying,
      frameIndex: nullableString(this.#frameIndex),
      sampleIndex: nullableString(this.#sampleIndex),
      seekEpoch: this.#epoch.toString(),
      bufferedFrameRanges: this.#buffered.map(wireRange),
      loopFrameRange: this.#loop === null ? null : wireRange(this.#loop),
      presentedRequestID: this.#presentedRequest,
    });
  }

  subscribe(listener) {
    if (typeof listener !== "function") throw new TypeError("@cutly/sdk player: listener must be a function");
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  play() {
    if (this.#frameIndex === null) return this.snapshot();
    this.#desiredPlaying = true;
    this.#derivePhase();
    this.#emit("play");
    return this.snapshot();
  }

  pause() {
    this.#desiredPlaying = false;
    if (this.#frameIndex !== null) this.#phase = "paused";
    this.#emit("pause");
    return this.snapshot();
  }

  seekFrame(frameIndex) {
    const target = parseInt64(frameIndex, "frameIndex");
    requireContained(this.#config.frameRange, target, "frameIndex");
    this.#frameIndex = target;
    this.#sampleIndex = clampToRange(
      sampleIndexAtFrameNormalized(this.#config.frameClock, this.#config.sampleClock, target),
      this.#config.sampleRange,
      true,
    );
    this.#epoch += 1n;
    this.#presentedRequest = null;
    this.#derivePhase();
    this.#emit("seek");
    return this.snapshot();
  }

  advanceAudioSample(sampleIndex) {
    let sample = parseInt64(sampleIndex, "sampleIndex");
    const sampleEnd = rangeEnd(this.#config.sampleRange);
    if (sample < this.#config.sampleRange.first) sample = this.#config.sampleRange.first;
    const reachedEnd = sample >= sampleEnd;
    if (reachedEnd) sample = sampleEnd;

    let frame = frameIndexAtSampleNormalized(this.#config.frameClock, this.#config.sampleClock, sample);
    if (this.#loop !== null && frame >= rangeEnd(this.#loop)) {
      const relative = frame - this.#loop.first;
      frame = this.#loop.first + positiveModulo(relative, this.#loop.count);
      sample = clampToRange(
        sampleIndexAtFrameNormalized(this.#config.frameClock, this.#config.sampleClock, frame),
        this.#config.sampleRange,
        true,
      );
      this.#epoch += 1n;
      this.#presentedRequest = null;
      this.#emit("loop");
    } else if (reachedEnd || frame >= rangeEnd(this.#config.frameRange)) {
      this.#sampleIndex = sampleEnd;
      this.#frameIndex = lastIndex(this.#config.frameRange);
      this.#desiredPlaying = false;
      this.#phase = "ended";
      this.#emit("ended");
      return this.snapshot();
    }

    if (frame < this.#config.frameRange.first) frame = this.#config.frameRange.first;
    this.#frameIndex = frame;
    this.#sampleIndex = sample;
    this.#derivePhase();
    this.#emit("clock");
    return this.snapshot();
  }

  setBufferedFrameRanges(ranges) {
    if (!Array.isArray(ranges)) throw new TypeError("@cutly/sdk player: buffered ranges must be an array");
    const normalized = ranges.map((range, index) => normalizeRange(range, `bufferedFrameRanges[${index}]`, false));
    normalized.sort((left, right) => compareBigInt(left.first, right.first));
    const merged = [];
    for (const range of normalized) {
      if (range.count === 0n) continue;
      if (range.first < this.#config.frameRange.first || rangeEnd(range) > rangeEnd(this.#config.frameRange)) {
        throw playerError("PLAYER.BUFFER_RANGE_INVALID", "buffered range lies outside the configured frame range");
      }
      const previous = merged[merged.length - 1];
      if (previous !== undefined && range.first <= rangeEnd(previous)) {
        const end = rangeEnd(range) > rangeEnd(previous) ? rangeEnd(range) : rangeEnd(previous);
        previous.count = end - previous.first;
      } else {
        merged.push({ ...range });
      }
    }
    this.#buffered = merged;
    this.#bufferStateKnown = true;
    this.#derivePhase();
    this.#emit("buffer");
    return this.snapshot();
  }

  setLoopFrameRange(range) {
    if (range === null || range === undefined) {
      this.#loop = null;
      this.#emit("loop-range");
      return this.snapshot();
    }
    const normalized = normalizeRange(range, "loopFrameRange", true);
    if (normalized.first < this.#config.frameRange.first || rangeEnd(normalized) > rangeEnd(this.#config.frameRange)) {
      throw playerError("PLAYER.LOOP_RANGE_INVALID", "loop range lies outside the configured frame range");
    }
    this.#loop = normalized;
    this.#emit("loop-range");
    return this.snapshot();
  }

  requestFrame() {
    if (this.#frameIndex === null) throw playerError("PLAYER.NO_FRAME", "the configured frame range is empty");
    this.#requestSequence += 1n;
    return Object.freeze({
      profile: this.#config.profile,
      revision: this.#config.revision.toString(),
      seekEpoch: this.#epoch.toString(),
      requestID: this.#requestSequence.toString(),
      compositionFrameIndex: this.#frameIndex.toString(),
    });
  }

  // acceptFrame returns false for a stale revision, seek, frame or superseded
  // request. It never lets an old asynchronous result replace current state.
  acceptFrame(token) {
    if (!token || typeof token !== "object") return false;
    if (
      token.profile !== this.#config.profile ||
      token.revision !== this.#config.revision.toString() ||
      token.seekEpoch !== this.#epoch.toString() ||
      token.requestID !== this.#requestSequence.toString() ||
      token.compositionFrameIndex !== nullableString(this.#frameIndex)
    ) {
      return false;
    }
    this.#presentedRequest = token.requestID;
    this.#emit("frame");
    return true;
  }

  #derivePhase() {
    if (this.#frameIndex === null) {
      this.#phase = "ended";
      return;
    }
    if (!this.#desiredPlaying) {
      this.#phase = this.#phase === "ready" ? "ready" : "paused";
      return;
    }
    this.#phase = this.#isCurrentFrameBuffered() ? "playing" : "buffering";
  }

  #isCurrentFrameBuffered() {
    if (!this.#bufferStateKnown || this.#frameIndex === null) return true;
    return this.#buffered.some((range) => contains(range, this.#frameIndex));
  }

  #emit(kind) {
    if (this.#listeners.size === 0) return;
    const event = Object.freeze({ kind, state: this.snapshot() });
    for (const listener of [...this.#listeners]) listener(event);
  }
}

// DocumentV2Player selects the explicit v2 player identity while reusing the
// same exact audio-master clock, seek/buffer state machine and stale-frame gate.
export class DocumentV2Player extends HeadlessPlayer {
  constructor(config) {
    super({ ...config, profile: DOCUMENT_V2_PLAYER_PROFILE });
  }

  load(config) {
    return super.load({ ...config, profile: DOCUMENT_V2_PLAYER_PROFILE });
  }
}

export function frameIndexAtSample(frameClock, sampleClock, sampleIndex) {
  const frame = normalizeFrameClock(frameClock);
  const sample = normalizeSampleClock(sampleClock);
  const index = parseInt64(sampleIndex, "sampleIndex");
  return frameIndexAtSampleNormalized(frame, sample, index);
}

function frameIndexAtSampleNormalized(frame, sample, index) {
  const time = add(sample.origin, { n: index, d: sample.samplesPerSecond });
  const relative = subtract(time, frame.origin);
  return exactInt64Result(
    floorFraction(relative.n * frame.framesPerSecond.n, relative.d * frame.framesPerSecond.d),
    "composition frame index",
  );
}

export function sampleIndexAtFrame(frameClock, sampleClock, frameIndex) {
  const frame = normalizeFrameClock(frameClock);
  const sample = normalizeSampleClock(sampleClock);
  const index = parseInt64(frameIndex, "frameIndex");
  return sampleIndexAtFrameNormalized(frame, sample, index);
}

function sampleIndexAtFrameNormalized(frame, sample, index) {
  const time = add(frame.origin, { n: index * frame.framesPerSecond.d, d: frame.framesPerSecond.n });
  const relative = subtract(time, sample.origin);
  return exactInt64Result(ceilFraction(relative.n * sample.samplesPerSecond, relative.d), "composition sample index");
}

function normalizeConfig(config) {
  if (!config || typeof config !== "object") throw new TypeError("@cutly/sdk player: config must be an object");
  const revision = parseInteger(config.revision, "revision", 0n, UINT64_MAX);
  const frameClock = normalizeFrameClock(config.frameClock);
  const sampleClock = normalizeSampleClock(config.sampleClock);
  const frameRange = normalizeRange(config.frameRange, "frameRange", false);
  const sampleRange = normalizeRange(config.sampleRange, "sampleRange", false);
  const profile = config.profile ?? PLAYER_PROFILE;
  if (profile !== PLAYER_PROFILE && profile !== DOCUMENT_V2_PLAYER_PROFILE) {
    throw playerError("PLAYER.PROFILE_INVALID", "unsupported player profile");
  }
  return { profile, revision, frameClock, sampleClock, frameRange, sampleRange };
}

function normalizeFrameClock(clock) {
  if (!clock || typeof clock !== "object") throw playerError("PLAYER.CLOCK_INVALID", "frameClock is required");
  const origin = normalizeRational(clock.origin, "frameClock.origin", false);
  const framesPerSecond = normalizeRational(clock.framesPerSecond, "frameClock.framesPerSecond", true);
  return { origin, framesPerSecond };
}

function normalizeSampleClock(clock) {
  if (!clock || typeof clock !== "object") throw playerError("PLAYER.CLOCK_INVALID", "sampleClock is required");
  const origin = normalizeRational(clock.origin, "sampleClock.origin", false);
  const samplesPerSecond = parseInteger(clock.samplesPerSecond, "sampleClock.samplesPerSecond", 1n, INT64_MAX);
  return { origin, samplesPerSecond };
}

function normalizeRational(value, name, positiveValue) {
  if (!value || typeof value !== "object") throw playerError("PLAYER.CLOCK_INVALID", `${name} is required`);
  const numerator = parseInt64(value.value, `${name}.value`);
  const denominator = parseInteger(value.rate, `${name}.rate`, 1n, INT64_MAX);
  if (positiveValue && numerator <= 0n) throw playerError("PLAYER.CLOCK_INVALID", `${name}.value must be positive`);
  return reduce({ n: numerator, d: denominator });
}

function normalizeRange(value, name, positiveCount) {
  if (!value || typeof value !== "object") throw playerError("PLAYER.RANGE_INVALID", `${name} is required`);
  const first = parseInt64(value.first, `${name}.first`);
  const count = parseInteger(value.count, `${name}.count`, 0n, INT64_MAX);
  if (positiveCount && count === 0n) throw playerError("PLAYER.RANGE_INVALID", `${name}.count must be positive`);
  const end = first + count;
  if (end < INT64_MIN || end > INT64_MAX) throw playerError("PLAYER.RANGE_INVALID", `${name} exclusive end overflows int64`);
  return { first, count };
}

function add(left, right) {
  return reduce({ n: left.n * right.d + right.n * left.d, d: left.d * right.d });
}

function subtract(left, right) {
  return reduce({ n: left.n * right.d - right.n * left.d, d: left.d * right.d });
}

function reduce(value) {
  const divisor = gcd(abs(value.n), value.d);
  return { n: value.n / divisor, d: value.d / divisor };
}

function floorFraction(numerator, denominator) {
  let quotient = numerator / denominator;
  if (numerator % denominator < 0n) quotient -= 1n;
  return quotient;
}

function ceilFraction(numerator, denominator) {
  let quotient = numerator / denominator;
  if (numerator % denominator > 0n) quotient += 1n;
  return quotient;
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

function parseInt64(value, name) {
  return parseInteger(value, name, INT64_MIN, INT64_MAX);
}

function parseInteger(value, name, minimum, maximum) {
  let parsed;
  if (typeof value === "bigint") parsed = value;
  else if (typeof value === "number" && Number.isSafeInteger(value)) parsed = BigInt(value);
  else if (typeof value === "string" && /^-?(0|[1-9][0-9]*)$/.test(value)) parsed = BigInt(value);
  else throw playerError("PLAYER.INTEGER_INVALID", `${name} must be an exact integer`);
  if (parsed < minimum || parsed > maximum) throw playerError("PLAYER.INTEGER_INVALID", `${name} is out of range`);
  return parsed;
}

function exactInt64Result(value, name) {
  if (value < INT64_MIN || value > INT64_MAX) throw playerError("PLAYER.TIME_OVERFLOW", `${name} overflows int64`);
  return value;
}

function rangeEnd(range) {
  return range.first + range.count;
}

function lastIndex(range) {
  return range.count === 0n ? null : rangeEnd(range) - 1n;
}

function contains(range, value) {
  return value >= range.first && value < rangeEnd(range);
}

function requireContained(range, value, name) {
  if (!contains(range, value)) throw playerError("PLAYER.RANGE_INVALID", `${name} lies outside the configured frame range`);
}

function clampToRange(value, range, allowEnd) {
  if (value < range.first) return range.first;
  const end = rangeEnd(range);
  if (value > end || (!allowEnd && value === end)) return allowEnd ? end : end - 1n;
  return value;
}

function positiveModulo(value, modulus) {
  const result = value % modulus;
  return result < 0n ? result + modulus : result;
}

function compareBigInt(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function nullableString(value) {
  return value === null ? null : value.toString();
}

function wireRange(range) {
  return Object.freeze({ first: range.first.toString(), count: range.count.toString() });
}

function playerError(code, detail) {
  return new PlayerStateError(code, detail);
}
