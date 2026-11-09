export const CANONICAL_JSON_PROFILE = "cutly.json.canonical-integer.v1";
export const NUMBER_PRESERVING_JSON_PROFILE = "cutly.json.number-preserving.v1";

const DEFAULT_LIMITS = Object.freeze({
  maxInputBytes: 64 << 20,
  maxDepth: 256,
  maxValues: 1 << 20,
  maxStringBytes: 64 << 20,
  maxIntegerDigits: 4096,
});

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });

export class CanonicalJSONError extends Error {
  constructor(code, offset, detail) {
    super(`canonicaljson: ${code} at code-unit ${offset}: ${detail}`);
    this.name = "CanonicalJSONError";
    this.code = code;
    this.offset = offset;
  }
}

export function canonicalizeJSONString(input, requestedLimits = {}) {
  const limits = normalizeLimits(requestedLimits);
  const text = decodeInput(input, limits.maxInputBytes);
  const parser = new Parser(text, limits);
  return writeCanonical(parser.parse());
}

export function canonicalizeJSON(input, limits) {
  return encoder.encode(canonicalizeJSONString(input, limits));
}

export function canonicalizeNumberPreservingJSONString(input, requestedLimits = {}) {
  const limits = normalizeLimits(requestedLimits);
  const text = decodeInput(input, limits.maxInputBytes);
  const parser = new Parser(text, limits, true);
  return writeCanonical(parser.parse());
}

export function canonicalizeNumberPreservingJSON(input, limits) {
  return encoder.encode(canonicalizeNumberPreservingJSONString(input, limits));
}

// extractNumberPreservingRootMember returns one root member as its own exact
// canonical JSON value. It lets protocol clients inspect safe envelope fields
// while retaining arbitrary-precision receipt and payload bytes.
export function extractNumberPreservingRootMember(input, member, requestedLimits = {}) {
  if (typeof member !== "string" || member === "") throw new TypeError("@cutly/sdk: root member must be non-empty");
  const limits = normalizeLimits(requestedLimits);
  const text = decodeInput(input, limits.maxInputBytes);
  const parser = new Parser(text, limits, true);
  const root = parser.parse();
  if (root.kind !== "object") throw new TypeError("@cutly/sdk: root value must be an object");
  const entry = root.members.find((candidate) => candidate.name === member);
  if (entry === undefined) throw new TypeError(`@cutly/sdk: root member ${JSON.stringify(member)} is missing`);
  return encoder.encode(writeCanonical(entry.value));
}

// encodeCanonicalValue is the safe object-model boundary for TypeScript
// callers. Protocol integers are bigint (or safe integer number); undefined,
// accessors, cycles and class instances are rejected instead of inheriting
// JSON.stringify's lossy omission/coercion rules.
export function encodeCanonicalValue(value, limits) {
  const normalized = normalizeLimits(limits ?? {});
  const writer = new ValueWriter(normalized);
  return canonicalizeJSON(writer.encode(value), normalized);
}

export function decodeCanonicalJSON(input, requestedLimits = {}) {
  const limits = normalizeLimits(requestedLimits);
  const text = decodeInput(input, limits.maxInputBytes);
  const parser = new Parser(text, limits);
  return materialize(parser.parse());
}

export async function digestCanonicalJSON(input, limits) {
  const canonical = canonicalizeJSON(input, limits);
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    throw new Error("@cutly/sdk: Web Crypto SHA-256 is unavailable");
  }
  const digestBytes = new Uint8Array(await subtle.digest("SHA-256", canonical));
  let hex = "";
  for (const value of digestBytes) hex += value.toString(16).padStart(2, "0");
  return {
    profile: CANONICAL_JSON_PROFILE,
    digest: `sha256:${hex}`,
    canonical,
  };
}

export async function digestCanonicalValue(value, limits) {
  return digestCanonicalJSON(encodeCanonicalValue(value, limits), limits);
}

export async function digestNumberPreservingJSON(input, limits) {
  const canonical = canonicalizeNumberPreservingJSON(input, limits);
  return { profile: NUMBER_PRESERVING_JSON_PROFILE, digest: await sha256(canonical), canonical };
}

export async function digestNumberPreservingJSONWithoutRootMember(input, member, requestedLimits = {}) {
  if (typeof member !== "string" || member === "") throw new TypeError("@cutly/sdk: omitted root member must be non-empty");
  const limits = normalizeLimits(requestedLimits);
  const text = decodeInput(input, limits.maxInputBytes);
  const parser = new Parser(text, limits, true);
  const root = parser.parse();
  if (root.kind !== "object") throw new TypeError("@cutly/sdk: number-preserving digest root must be an object");
  const canonical = encoder.encode(writeCanonical({ kind: "object", members: root.members.filter((entry) => entry.name !== member) }));
  return { profile: NUMBER_PRESERVING_JSON_PROFILE, digest: await sha256(canonical), canonical };
}

async function sha256(bytes) {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) throw new Error("@cutly/sdk: Web Crypto SHA-256 is unavailable");
  const digestBytes = new Uint8Array(await subtle.digest("SHA-256", bytes));
  let hex = "";
  for (const value of digestBytes) hex += value.toString(16).padStart(2, "0");
  return `sha256:${hex}`;
}

function decodeInput(input, maxInputBytes) {
  if (typeof input === "string") {
    validateScalarString(input, 0);
    const encoded = encoder.encode(input);
    if (encoded.byteLength > maxInputBytes) resource(0, "input exceeds maxInputBytes");
    return input;
  }
  if (!(input instanceof Uint8Array)) {
    throw new TypeError("@cutly/sdk: canonical JSON input must be a string or Uint8Array");
  }
  if (input.byteLength > maxInputBytes) resource(0, "input exceeds maxInputBytes");
  try {
    return decoder.decode(input);
  } catch {
    fail("CANONICAL_JSON.INVALID_UNICODE", 0, "input is not valid UTF-8");
  }
}

class Parser {
  constructor(text, limits, preserveNumbers = false) {
    this.text = text;
    this.limits = limits;
    this.offset = 0;
    this.values = 0;
    this.stringBytes = 0;
    this.preserveNumbers = preserveNumbers;
  }

  parse() {
    this.skipWhitespace();
    const value = this.parseValue(0);
    this.skipWhitespace();
    if (this.offset !== this.text.length) this.syntax("multiple values or trailing data");
    return value;
  }

  parseValue(depth) {
    if (this.values >= this.limits.maxValues) resource(this.offset, "value count exceeds maxValues");
    this.values += 1;
    const current = this.text[this.offset];
    if (current === undefined) this.syntax("expected a value");
    if (current === "n") return this.literal("null", { kind: "null" });
    if (current === "f") return this.literal("false", { kind: "false" });
    if (current === "t") return this.literal("true", { kind: "true" });
    if (current === '"') return { kind: "string", text: this.parseString() };
    if (current === "[") return this.parseArray(depth + 1);
    if (current === "{") return this.parseObject(depth + 1);
    if (current === "-" || isDigit(current)) return this.parseNumber();
    this.syntax("unexpected token");
  }

  parseArray(depth) {
    if (depth > this.limits.maxDepth) resource(this.offset, "nesting exceeds maxDepth");
    this.offset += 1;
    this.skipWhitespace();
    const values = [];
    if (this.consume("]")) return { kind: "array", values };
    while (true) {
      values.push(this.parseValue(depth));
      this.skipWhitespace();
      if (this.consume("]")) return { kind: "array", values };
      if (!this.consume(",")) this.syntax("expected ',' or ']' in array");
      this.skipWhitespace();
    }
  }

  parseObject(depth) {
    if (depth > this.limits.maxDepth) resource(this.offset, "nesting exceeds maxDepth");
    this.offset += 1;
    this.skipWhitespace();
    const members = [];
    const seen = new Set();
    if (this.consume("}")) return { kind: "object", members };
    while (true) {
      if (this.text[this.offset] !== '"') this.syntax("expected a quoted object member name");
      const nameOffset = this.offset;
      const name = this.parseString();
      if (seen.has(name)) fail("CANONICAL_JSON.DUPLICATE_MEMBER", nameOffset, `duplicate decoded member ${JSON.stringify(name)}`);
      seen.add(name);
      this.skipWhitespace();
      if (!this.consume(":")) this.syntax("expected ':' after object member name");
      this.skipWhitespace();
      members.push({ name, value: this.parseValue(depth) });
      this.skipWhitespace();
      if (this.consume("}")) break;
      if (!this.consume(",")) this.syntax("expected ',' or '}' in object");
      this.skipWhitespace();
    }
    members.sort((left, right) => (left.name < right.name ? -1 : left.name > right.name ? 1 : 0));
    return { kind: "object", members };
  }

  parseNumber() {
    const start = this.offset;
    if (this.consume("-") && this.offset >= this.text.length) this.syntax("incomplete integer");
    let digits = 0;
    if (this.text[this.offset] === "0") {
      this.offset += 1;
      digits = 1;
      if (isDigit(this.text[this.offset])) this.syntax("integer has a leading zero");
    } else if (isNonzeroDigit(this.text[this.offset])) {
      while (isDigit(this.text[this.offset])) {
        this.offset += 1;
        digits += 1;
        if (digits > this.limits.maxIntegerDigits) resource(start, "integer exceeds maxIntegerDigits");
      }
    } else {
      this.syntax("invalid integer");
    }
    if (this.text[this.offset] === ".") {
      if (!this.preserveNumbers) fail("CANONICAL_JSON.UNSUPPORTED_NUMBER", start, "only integer number syntax is supported");
      this.offset += 1;
      if (!isDigit(this.text[this.offset])) this.syntax("fraction requires a digit");
      while (isDigit(this.text[this.offset])) {
        this.offset += 1;
        digits += 1;
        if (digits > this.limits.maxIntegerDigits) resource(start, "number exceeds maxIntegerDigits");
      }
    }
    if (this.text[this.offset] === "e" || this.text[this.offset] === "E") {
      if (!this.preserveNumbers) fail("CANONICAL_JSON.UNSUPPORTED_NUMBER", start, "only integer number syntax is supported");
      this.offset += 1;
      if (this.text[this.offset] === "+" || this.text[this.offset] === "-") this.offset += 1;
      if (!isDigit(this.text[this.offset])) this.syntax("exponent requires a digit");
      while (isDigit(this.text[this.offset])) {
        this.offset += 1;
        digits += 1;
        if (digits > this.limits.maxIntegerDigits) resource(start, "number exceeds maxIntegerDigits");
      }
    }
    let text = this.text.slice(start, this.offset);
    if (!this.preserveNumbers && text === "-0") text = "0";
    return { kind: "number", text };
  }

  parseString() {
    const start = this.offset;
    this.offset += 1;
    let value = "";
    let decodedBytes = 0;
    const append = (text, byteCount) => {
      if (this.stringBytes + decodedBytes + byteCount > this.limits.maxStringBytes) {
        resource(start, "decoded strings exceed maxStringBytes");
      }
      value += text;
      decodedBytes += byteCount;
    };
    while (this.offset < this.text.length) {
      const current = this.text[this.offset];
      if (current === '"') {
        this.offset += 1;
        this.stringBytes += decodedBytes;
        return value;
      }
      if (current === "\\") {
        this.offset += 1;
        const escape = this.text[this.offset];
        if (escape === undefined) this.syntax("unterminated string escape");
        this.offset += 1;
        if (escape === '"' || escape === "\\" || escape === "/") append(escape, 1);
        else if (escape === "b") append("\b", 1);
        else if (escape === "f") append("\f", 1);
        else if (escape === "n") append("\n", 1);
        else if (escape === "r") append("\r", 1);
        else if (escape === "t") append("\t", 1);
        else if (escape === "u") {
          const high = this.parseHexQuad();
          if (high >= 0xd800 && high <= 0xdbff) {
            if (this.text[this.offset] !== "\\" || this.text[this.offset + 1] !== "u") {
              fail("CANONICAL_JSON.INVALID_UNICODE", this.offset, "high surrogate is not followed by a low surrogate");
            }
            this.offset += 2;
            const lowOffset = this.offset;
            const low = this.parseHexQuad();
            if (low < 0xdc00 || low > 0xdfff) {
              fail("CANONICAL_JSON.INVALID_UNICODE", lowOffset, "high surrogate is not followed by a low surrogate");
            }
            const scalar = 0x10000 + ((high - 0xd800) << 10) + low - 0xdc00;
            append(String.fromCodePoint(scalar), 4);
          } else if (high >= 0xdc00 && high <= 0xdfff) {
            fail("CANONICAL_JSON.INVALID_UNICODE", this.offset - 4, "unpaired low surrogate");
          } else {
            append(String.fromCharCode(high), utf8ScalarBytes(high));
          }
        } else {
          this.syntax("invalid string escape");
        }
        continue;
      }
      const code = this.text.charCodeAt(this.offset);
      if (code < 0x20) this.syntax("unescaped control character in string");
      if (code >= 0xd800 && code <= 0xdbff) {
        const low = this.text.charCodeAt(this.offset + 1);
        if (low < 0xdc00 || low > 0xdfff) fail("CANONICAL_JSON.INVALID_UNICODE", this.offset, "unpaired high surrogate");
        append(this.text.slice(this.offset, this.offset + 2), 4);
        this.offset += 2;
      } else if (code >= 0xdc00 && code <= 0xdfff) {
        fail("CANONICAL_JSON.INVALID_UNICODE", this.offset, "unpaired low surrogate");
      } else {
        append(current, utf8ScalarBytes(code));
        this.offset += 1;
      }
    }
    fail("CANONICAL_JSON.INVALID_SYNTAX", start, "unterminated string");
  }

  parseHexQuad() {
    if (this.offset + 4 > this.text.length) this.syntax("incomplete Unicode escape");
    let value = 0;
    for (let index = 0; index < 4; index += 1) {
      const nibble = hexNibble(this.text.charCodeAt(this.offset + index));
      if (nibble < 0) this.syntax("invalid Unicode escape");
      value = (value << 4) | nibble;
    }
    this.offset += 4;
    return value;
  }

  literal(text, value) {
    if (this.text.slice(this.offset, this.offset + text.length) !== text) this.syntax("invalid literal");
    this.offset += text.length;
    return value;
  }

  consume(character) {
    if (this.text[this.offset] !== character) return false;
    this.offset += 1;
    return true;
  }

  skipWhitespace() {
    while (true) {
      const current = this.text[this.offset];
      if (current !== " " && current !== "\t" && current !== "\r" && current !== "\n") return;
      this.offset += 1;
    }
  }

  syntax(detail) {
    fail("CANONICAL_JSON.INVALID_SYNTAX", this.offset, detail);
  }
}

function writeCanonical(value) {
  if (value.kind === "null") return "null";
  if (value.kind === "false") return "false";
  if (value.kind === "true") return "true";
  if (value.kind === "number") return value.text;
  if (value.kind === "string") return quote(value.text);
  if (value.kind === "array") return `[${value.values.map(writeCanonical).join(",")}]`;
  if (value.kind === "object") {
    return `{${value.members.map((entry) => `${quote(entry.name)}:${writeCanonical(entry.value)}`).join(",")}}`;
  }
  throw new Error("@cutly/sdk: unreachable canonical JSON node");
}

function materialize(value) {
  if (value.kind === "null") return null;
  if (value.kind === "false") return false;
  if (value.kind === "true") return true;
  if (value.kind === "number") return BigInt(value.text);
  if (value.kind === "string") return value.text;
  if (value.kind === "array") return Object.freeze(value.values.map(materialize));
  if (value.kind === "object") {
    const result = Object.create(null);
    for (const entry of value.members) Object.defineProperty(result, entry.name, {
      value: materialize(entry.value), enumerable: true, writable: false, configurable: false,
    });
    return Object.freeze(result);
  }
  throw new Error("@cutly/sdk: unreachable decoded canonical JSON node");
}

class ValueWriter {
  constructor(limits) {
    this.limits = limits;
    this.chunks = [];
    this.outputBytes = 0;
    this.stringBytes = 0;
    this.values = 0;
    this.active = new Set();
  }

  encode(value) {
    this.write(value, "$", 0);
    return this.chunks.join("");
  }

  write(value, path, depth) {
    this.values += 1;
    if (this.values > this.limits.maxValues) resource(0, "value count exceeds maxValues");
    if (value === null) return this.append("null");
    if (value === true) return this.append("true");
    if (value === false) return this.append("false");
    if (typeof value === "string") return this.writeString(value);
    if (typeof value === "bigint") {
      const text = value.toString();
      const digits = text[0] === "-" ? text.length - 1 : text.length;
      if (digits > this.limits.maxIntegerDigits) resource(0, "integer exceeds maxIntegerDigits");
      return this.append(text);
    }
    if (typeof value === "number") {
      if (!Number.isSafeInteger(value)) throw new TypeError(`@cutly/sdk: ${path} must be a safe integer or bigint`);
      return this.append(Object.is(value, -0) ? "0" : value.toString());
    }
    if (typeof value !== "object") throw new TypeError(`@cutly/sdk: ${path} has unsupported type ${typeof value}`);
    if (depth + 1 > this.limits.maxDepth) resource(0, "nesting exceeds maxDepth");
    if (this.active.has(value)) throw new TypeError(`@cutly/sdk: ${path} contains a cycle`);
    this.active.add(value);
    try {
      if (Array.isArray(value)) return this.writeArray(value, path, depth + 1);
      return this.writeObject(value, path, depth + 1);
    } finally {
      this.active.delete(value);
    }
  }

  writeArray(value, path, depth) {
    this.append("[");
    for (let index = 0; index < value.length; index += 1) {
      if (!Object.hasOwn(value, index)) throw new TypeError(`@cutly/sdk: ${path}[${index}] is an array hole`);
      if (index > 0) this.append(",");
      this.write(value[index], `${path}[${index}]`, depth);
    }
    this.append("]");
  }

  writeObject(value, path, depth) {
    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) {
      throw new TypeError(`@cutly/sdk: ${path} must be a plain object`);
    }
    const keys = Reflect.ownKeys(value);
    if (keys.length > this.limits.maxValues) resource(0, "object member count exceeds maxValues");
    this.append("{");
    let written = 0;
    for (const name of keys) {
      if (typeof name !== "string") throw new TypeError(`@cutly/sdk: ${path} has symbol members`);
      const descriptor = Object.getOwnPropertyDescriptor(value, name);
      if (!descriptor?.enumerable) continue;
      if (!("value" in descriptor)) throw new TypeError(`@cutly/sdk: ${path}.${name} is an accessor`);
      if (written > 0) this.append(",");
      this.writeString(name);
      this.append(":");
      this.write(descriptor.value, `${path}.${name}`, depth);
      written += 1;
    }
    this.append("}");
  }

  writeString(value) {
    validateScalarString(value, 0);
    this.stringBytes += encoder.encode(value).byteLength;
    if (this.stringBytes > this.limits.maxStringBytes) resource(0, "decoded strings exceed maxStringBytes");
    this.append(JSON.stringify(value));
  }

  append(text) {
    this.outputBytes += encoder.encode(text).byteLength;
    if (this.outputBytes > this.limits.maxInputBytes) resource(0, "encoded value exceeds maxInputBytes");
    this.chunks.push(text);
  }
}

function quote(value) {
  let output = '"';
  for (const character of value) {
    const code = character.codePointAt(0);
    if (character === '"') output += '\\"';
    else if (character === "\\") output += "\\\\";
    else if (character === "\b") output += "\\b";
    else if (character === "\f") output += "\\f";
    else if (character === "\n") output += "\\n";
    else if (character === "\r") output += "\\r";
    else if (character === "\t") output += "\\t";
    else if (code < 0x20) output += `\\u00${code.toString(16).padStart(2, "0")}`;
    else output += character;
  }
  return `${output}"`;
}

function normalizeLimits(requested) {
  const limits = { ...DEFAULT_LIMITS, ...requested };
  for (const [name, value] of Object.entries(limits)) {
    if (!Number.isSafeInteger(value) || value <= 0) resource(0, `${name} must be a positive safe integer`);
  }
  return limits;
}

function validateScalarString(value, baseOffset) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const low = value.charCodeAt(index + 1);
      if (low < 0xdc00 || low > 0xdfff) fail("CANONICAL_JSON.INVALID_UNICODE", baseOffset + index, "unpaired high surrogate");
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      fail("CANONICAL_JSON.INVALID_UNICODE", baseOffset + index, "unpaired low surrogate");
    }
  }
}

function isDigit(value) {
  return value !== undefined && value >= "0" && value <= "9";
}

function isNonzeroDigit(value) {
  return value !== undefined && value >= "1" && value <= "9";
}

function hexNibble(code) {
  if (code >= 0x30 && code <= 0x39) return code - 0x30;
  if (code >= 0x61 && code <= 0x66) return code - 0x61 + 10;
  if (code >= 0x41 && code <= 0x46) return code - 0x41 + 10;
  return -1;
}

function utf8ScalarBytes(code) {
  if (code <= 0x7f) return 1;
  if (code <= 0x7ff) return 2;
  if (code <= 0xffff) return 3;
  return 4;
}

function resource(offset, detail) {
  fail("CANONICAL_JSON.RESOURCE_LIMIT", offset, detail);
}

function fail(code, offset, detail) {
  throw new CanonicalJSONError(code, offset, detail);
}
