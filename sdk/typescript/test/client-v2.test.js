import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  CutlyAgentV2Client,
  createAgentRequestV2,
  createAgentTransactionRequestV2,
  decodeAgentResultV2,
  decodeTransactionV2,
} from "../src/index.js";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

test("Agent v2 transaction requests preserve canonical numbers", async () => {
  const transaction = await decodeTransactionV2(await readFile(new URL("../../../conformance/fixtures/document-v2-transaction-numbers.canonical.json", import.meta.url)));
  const request = createAgentTransactionRequestV2({ requestId: "request", projectId: "project", transaction });
  const raw = decoder.decode(request.canonical);
  assert.match(raw, /"decimal":1\.2500/u);
  assert.match(raw, /"negative_zero":-0/u);
  assert.equal(raw, decoder.decode(encoder.encode(raw)));
});

test("Agent v2 client correlates results and retains exact receipt bytes", async () => {
  const response = encoder.encode('{"action":"status","diagnostics":[],"ok":true,"profile":"cutly.agent-result.document-v2.v2","project_id":"project","receipt":{"decimal":1.2500},"request_id":"request","schema_version":2,"state":"available","tool":"cutly_project"}');
  const client = new CutlyAgentV2Client(async () => response);
  const result = await client.projectStatus({ requestId: "request", projectId: "project" });
  assert.equal(result.ok, true);
  assert.equal(decoder.decode(result.receipt), '{"decimal":1.2500}');
});

test("Agent v2 rejects noncanonical and uncorrelated results", async () => {
  assert.throws(() => decodeAgentResultV2('{"schema_version":2, "profile":"cutly.agent-result.document-v2.v2"}'), /exact canonical/u);
  const response = encoder.encode('{"action":"status","diagnostics":[],"ok":true,"profile":"cutly.agent-result.document-v2.v2","project_id":"project","receipt":{},"request_id":"other","schema_version":2,"state":"available","tool":"cutly_project"}');
  const client = new CutlyAgentV2Client(async () => response);
  await assert.rejects(() => client.projectStatus({ requestId: "request", projectId: "project" }), /CORRELATION/u);
});

test("Agent v2 request payloads are exact canonical objects", () => {
  const request = createAgentRequestV2({ requestId: "request", projectId: "project", tool: "cutly_edit", action: "apply", payload: { transaction_id: "tx" } });
  assert.equal(decoder.decode(request.canonical), '{"action":"apply","payload":{"transaction_id":"tx"},"payload_profile":"cutly.agent-payload.edit.apply.document-v2.v2","profile":"cutly.agent-request.document-v2.v2","project_id":"project","request_id":"request","schema_version":2,"tool":"cutly_edit"}');
});

test("Agent v2 preflight and frame methods emit the frozen native payloads", async () => {
  const requests = [];
  const client = new CutlyAgentV2Client(async (raw) => {
    const request = JSON.parse(decoder.decode(raw));
    requests.push(decoder.decode(raw));
    return encoder.encode(`{"action":${JSON.stringify(request.action)},"diagnostics":[],"ok":true,"profile":"cutly.agent-result.document-v2.v2","project_id":"project","receipt":{},"request_id":${JSON.stringify(request.request_id)},"schema_version":2,"state":"available","tool":"cutly_render"}`);
  });
  const plan = `sha256:${"a".repeat(64)}`;
  const manifest = `sha256:${"b".repeat(64)}`;
  await client.preflightRender({ requestId: "preflight", projectId: "project", planFingerprint: plan, executionManifestFingerprint: manifest });
  await client.renderFrame({ requestId: "frame", projectId: "project", planFingerprint: plan, executionManifestFingerprint: manifest, compositionFrameIndex: 9007199254740993n });
  assert.match(requests[0], /"payload":\{"execution_manifest_fingerprint":"sha256:b{64}","plan_fingerprint":"sha256:a{64}"\}/u);
  assert.match(requests[1], /"payload":\{"composition_frame_index":9007199254740993,"execution_manifest_fingerprint":"sha256:b{64}","plan_fingerprint":"sha256:a{64}","profile":"cutly.preview-frame.document-v2.v2"\}/u);
});
