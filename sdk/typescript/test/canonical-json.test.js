import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  CANONICAL_JSON_PROFILE,
  CanonicalJSONError,
  canonicalizeJSON,
  canonicalizeJSONString,
  decodeCanonicalJSON,
  digestCanonicalJSON,
  digestCanonicalValue,
  encodeCanonicalValue,
} from "../src/index.js";

const input = ' {\n  "\\ue000": -0, "z": [true, null, "a\\/b\\u0001"], "\\ud83d\\ude00": 9223372036854775808, "a": false } ';
const expected = '{"a":false,"z":[true,null,"a/b\\u0001"],"😀":9223372036854775808,"":0}';

test("canonicalizes the shared integer profile without Number rounding", () => {
  assert.equal(CANONICAL_JSON_PROFILE, "cutly.json.canonical-integer.v1");
  assert.equal(canonicalizeJSONString(input), expected);
  assert.equal(new TextDecoder().decode(canonicalizeJSON(new TextEncoder().encode(input))), expected);
  assert.equal(canonicalizeJSONString(expected), expected);
});

test("rejects ambiguous and unsupported JSON", () => {
  const vectors = [
    ['{"a":1,"\\u0061":2}', "CANONICAL_JSON.DUPLICATE_MEMBER"],
    ["1.0", "CANONICAL_JSON.UNSUPPORTED_NUMBER"],
    ["1e3", "CANONICAL_JSON.UNSUPPORTED_NUMBER"],
    ['"\\ud800"', "CANONICAL_JSON.INVALID_UNICODE"],
    ['"\\udc00"', "CANONICAL_JSON.INVALID_UNICODE"],
    ['{"a":1,}', "CANONICAL_JSON.INVALID_SYNTAX"],
  ];
  for (const [raw, code] of vectors) {
    assert.throws(
      () => canonicalizeJSONString(raw),
      (error) => error instanceof CanonicalJSONError && error.code === code,
      raw,
    );
  }
  assert.throws(
    () => canonicalizeJSON(Uint8Array.from([0x22, 0xff, 0x22])),
    (error) => error instanceof CanonicalJSONError && error.code === "CANONICAL_JSON.INVALID_UNICODE",
  );
});

test("enforces resource limits before returning canonical bytes", () => {
  assert.throws(() => canonicalizeJSONString("[[[0]]]", { maxDepth: 2 }), /RESOURCE_LIMIT/);
  assert.throws(() => canonicalizeJSONString("[0,1]", { maxValues: 2 }), /RESOURCE_LIMIT/);
  assert.throws(() => canonicalizeJSONString('{"ab":"cd"}', { maxStringBytes: 3 }), /RESOURCE_LIMIT/);
  assert.throws(() => canonicalizeJSONString("1000", { maxIntegerDigits: 3 }), /RESOURCE_LIMIT/);
});

test("produces the shared SHA-256 identity", async () => {
  const result = await digestCanonicalJSON(input);
  assert.equal(result.profile, CANONICAL_JSON_PROFILE);
  assert.equal(new TextDecoder().decode(result.canonical), expected);
  assert.equal(result.digest, "sha256:7d0a35b65a58803530602efb807548b4546cff965895757cff44ed4f1d589c0b");
});

test("matches the Go cross-language fixture", async () => {
  const fixtureRaw = await readFile(new URL("../../../conformance/fixtures/canonical-json-v1.vectors.canonical.json", import.meta.url), "utf8");
  const trimmed = fixtureRaw.trim();
  assert.equal(canonicalizeJSONString(trimmed), trimmed);
  const fixture = JSON.parse(trimmed);
  assert.equal(fixture.profile, CANONICAL_JSON_PROFILE);
  for (const vector of fixture.cases) {
    assert.equal(canonicalizeJSONString(vector.input), vector.canonical, vector.name);
    const result = await digestCanonicalJSON(vector.input);
    assert.equal(result.digest, vector.digest, vector.name);
  }
});

test("round-trips protocol integers as bigint without lossy JSON coercion", async () => {
  const value = Object.freeze({ revision: 18_446_744_073_709_551_615n, nested: [0n, -1n, "x"] });
  const encoded = encodeCanonicalValue(value);
  assert.equal(new TextDecoder().decode(encoded), '{"nested":[0,-1,"x"],"revision":18446744073709551615}');
  const decoded = decodeCanonicalJSON(encoded);
  assert.equal(decoded.revision, 18_446_744_073_709_551_615n);
  assert.deepEqual(decoded.nested, [0n, -1n, "x"]);
  assert.ok(Object.isFrozen(decoded));
  assert.equal(Object.getPrototypeOf(decoded), null);
  const digest = await digestCanonicalValue(value);
  assert.deepEqual(digest.canonical, encoded);
});

test("rejects lossy or effectful JavaScript values", () => {
  assert.throws(() => encodeCanonicalValue({ value: Number.MAX_SAFE_INTEGER + 1 }), /safe integer or bigint/);
  assert.throws(() => encodeCanonicalValue({ value: undefined }), /unsupported type undefined/);
  assert.throws(() => encodeCanonicalValue(new Date()), /plain object/);
  const sparse = [];
  sparse.length = 1;
  assert.throws(() => encodeCanonicalValue(sparse), /array hole/);
  const cyclic = {};
  cyclic.self = cyclic;
  assert.throws(() => encodeCanonicalValue(cyclic), /contains a cycle/);
  let invoked = false;
  const accessor = Object.defineProperty({}, "value", { enumerable: true, get() { invoked = true; return 1; } });
  assert.throws(() => encodeCanonicalValue(accessor), /is an accessor/);
  assert.equal(invoked, false);
  assert.throws(() => encodeCanonicalValue([[[0n]]], { maxDepth: 2 }), /RESOURCE_LIMIT/);
  assert.throws(() => encodeCanonicalValue(1000n, { maxIntegerDigits: 3 }), /RESOURCE_LIMIT/);
  assert.throws(() => encodeCanonicalValue("control:\u0000", { maxInputBytes: 8 }), /RESOURCE_LIMIT/);
});
