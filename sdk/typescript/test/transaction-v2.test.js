import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  canonicalizeNumberPreservingJSON,
  buildTransactionV2,
  deleteClipV2,
  decodeTransactionV2,
  moveClipV2,
  retrackClipV2,
  setClipPropertiesV2,
  transactionDigestV2,
  trimClipV2,
} from "../src/index.js";

const fixture = (name) => new URL(`../../../conformance/fixtures/${name}`, import.meta.url);

test("Document v2 transaction preserves Go canonical number spelling and digest", async () => {
  const canonical = new Uint8Array(await readFile(fixture("document-v2-transaction-numbers.canonical.json")));
  const decoded = await decodeTransactionV2(canonical);
  assert.equal(decoded.digest, "sha256:36cac0333d487330e4f33b1e3b576794b8917e33078d2837258b0f12135b0229");
  assert.equal(await transactionDigestV2(canonical), decoded.digest);
  assert.deepEqual(canonicalizeNumberPreservingJSON(canonical), canonical);
  const text = new TextDecoder().decode(decoded.canonical);
  assert.match(text, /"decimal":1\.2500/);
  assert.match(text, /"exponent":6\.022e\+23/);
  assert.match(text, /"negative_zero":-0/);
  assert.match(text, /"small":1E-7/);
});

test("Document v2 UI builders emit the exact typed transaction union", async () => {
  const time = (value) => ({ value, rate: 1n });
  const range = (start, duration) => ({ start: time(start), duration: time(duration) });
  const operations = [
    moveClipV2({ id: "move", clipId: "clip", at: time(2n) }),
    trimClipV2({ id: "trim", clipId: "clip", timelineRange: range(1n, 3n), sourceRange: range(4n, 3n) }),
    retrackClipV2({ id: "retrack", clipId: "clip", trackId: "track-b", position: 0n }),
    setClipPropertiesV2({ id: "properties", clipId: "clip", value: { opacity: { value: { numerator: 1n, denominator: 2n } } } }),
    deleteClipV2({ id: "delete", trackId: "track-b", clipId: "clip" }),
  ];
  const transaction = await buildTransactionV2({
    id: "ui-transaction", idempotencyKey: "ui-key", documentId: "document", expectedRevision: 7n,
    expectedDocumentHash: `sha256:${"a".repeat(64)}`, operations,
  });
  assert.equal(transaction.operationCount, 5);
  assert.equal(transaction.expectedRevision, "7");
  assert.equal(await transactionDigestV2(transaction.canonical), transaction.digest);
  assert.ok(Object.isFrozen(operations[0]));
  assert.equal(operations[0].op, "clip_move");
});

test("Document v2 transaction rejects noncanonical, stale digest and duplicate payload", async () => {
  const noncanonical = new Uint8Array(await readFile(fixture("document-v2-transaction-numbers.valid.json")));
  await assert.rejects(() => decodeTransactionV2(noncanonical), /NON_CANONICAL/);
  for (const name of ["document-v2-transaction-stale-digest.invalid.json", "document-v2-transaction-second-payload.invalid.json", "document-v2-transaction-revision-decimal.invalid.json"]) {
    const raw = new Uint8Array(await readFile(fixture(name)));
    await assert.rejects(() => decodeTransactionV2(raw));
  }
});
