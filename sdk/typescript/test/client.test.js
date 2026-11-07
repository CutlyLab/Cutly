import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  AGENT_MEDIA_TYPE,
  AgentProtocolError,
  CutlyClient,
  HTTPAgentTransport,
  createAgentRequest,
  decodeCanonicalJSON,
  encodeCanonicalValue,
} from "../src/index.js";

function resultFor(request, overrides = {}) {
  return {
    protocol_version: 1n,
    ok: true,
    request_id: request.request_id,
    tool: request.tool,
    action: request.action,
    state: "current",
    ...(request.project_id === undefined ? {} : { project_id: request.project_id }),
    changed: false,
    replayed: false,
    render_required: false,
    diagnostics: [],
    next_actions: [],
    ...overrides,
  };
}

test("Agent client validates, correlates and preserves business rejection", async () => {
  const request = createAgentRequest({
    requestId: "request-1", tool: "cutly_edit", action: "apply", projectId: "project-1",
    payload: { transaction_id: "tx-1" },
  });
  const client = new CutlyClient(async (raw) => {
    const decoded = decodeCanonicalJSON(raw);
    return encodeCanonicalValue(resultFor(decoded, {
      ok: false,
      state: "rejected",
      diagnostics: [{ code: "TX.REJECTED", severity: "error", message: "rejected" }],
    }));
  });
  const result = await client.call(request);
  assert.equal(result.ok, false);
  assert.equal(result.diagnostics[0].code, "TX.REJECTED");

  const mismatch = new CutlyClient(async (raw) => {
    const decoded = decodeCanonicalJSON(raw);
    return encodeCanonicalValue(resultFor(decoded, { request_id: "other" }));
  });
  await assert.rejects(() => mismatch.call(request), (error) => error instanceof AgentProtocolError && error.code === "AGENT.CORRELATION");
});

test("typed client actions build exact project, edit and render payloads", async () => {
  const observed = [];
  const client = new CutlyClient(async (raw) => {
    const request = decodeCanonicalJSON(raw);
    observed.push(request);
    return encodeCanonicalValue(resultFor(request));
  });
  await client.inspectProject({ requestId: "inspect", projectId: "project" });
  await client.projectHistory({ requestId: "history", projectId: "project", afterSequence: 4n, limit: 10n });
  await client.planEdit({
    requestId: "plan", projectId: "project",
    transaction: { id: "tx", idempotency_key: "key", expected_revision: 7n, operations: [] },
  });
  await client.applyEdit({ requestId: "apply", projectId: "project", transactionId: "tx", expectedRevision: 7n });
  await client.discardEdit({ requestId: "discard", projectId: "project", transactionId: "tx" });
  await client.revertEdit({ requestId: "revert", projectId: "project", transactionId: "tx", expectedRevision: 8n });
  await client.planRender({
    requestId: "render-plan", projectId: "project",
    range: { start: { value: 0n, rate: 1n }, duration: { value: 1n, rate: 1n } },
    target: { timeline_id: "timeline", width: 64n, height: 64n },
  });
  await client.prepareRender({
    requestId: "prepare", projectId: "project", plan: { fingerprint: "plan" },
    resources: [{ assetId: "video", locator: "/media/video.mp4" }], deterministicSeed: 73n,
  });
  await client.preflightRender({
    requestId: "preflight", projectId: "project",
    plan: { fingerprint: "plan" }, executionManifest: { fingerprint: "manifest" },
  });
  await client.renderFrame({
    requestId: "frame", projectId: "project",
    plan: { fingerprint: "plan" }, executionManifest: { fingerprint: "manifest" },
    frame: { id: "frame-1" },
  });
  await client.submitRender({ requestId: "render", projectId: "project", job: { id: "job" } });
  await client.renderStatus({ requestId: "status", projectId: "project", jobId: "job" });
  await client.renderHistory({ requestId: "render-history", projectId: "project", jobId: "job", afterSequence: 2n, limit: 4n });
  await client.cancelRender({ requestId: "cancel", projectId: "project", jobId: "job" });

  assert.deepEqual(observed.map((request) => request.action), [
    "inspect", "history", "plan", "apply", "discard", "revert", "plan", "prepare", "preflight", "frame", "render", "status", "history", "cancel",
  ]);
  assert.deepEqual(observed[1].payload, Object.assign(Object.create(null), { after_sequence: 4n, limit: 10n }));
  assert.equal(observed[3].payload.expected_revision, 7n);
  assert.equal(observed[6].payload.range.duration.value, 1n);
  assert.equal(observed[6].payload.target.timeline_id, "timeline");
  assert.equal(observed[7].payload.resources[0].asset_id, "video");
  assert.equal(observed[7].payload.resources[0].locator, "/media/video.mp4");
  assert.equal(observed[7].payload.deterministic_seed, 73n);
  assert.equal(observed[8].payload.execution_manifest.fingerprint, "manifest");
  assert.equal(observed[9].payload.frame.id, "frame-1");
  assert.equal(observed[10].payload.job.id, "job");
  assert.deepEqual(observed[12].payload, Object.assign(Object.create(null), {
    after_sequence: 2n, job_id: "job", limit: 4n,
  }));
  assert.equal(observed[13].payload.job_id, "job");
});

test("prepareRender omits an absent deterministic seed", async () => {
  let observed;
  const client = new CutlyClient(async (raw) => {
    observed = decodeCanonicalJSON(raw);
    return encodeCanonicalValue(resultFor(observed));
  });
  await client.prepareRender({
    requestId: "prepare-default", projectId: "project", plan: { fingerprint: "plan" }, resources: [],
  });
  assert.equal(Object.hasOwn(observed.payload, "deterministic_seed"), false);
  assert.deepEqual(observed.payload.resources, []);
});

test("Agent request rejects invalid pairs, missing projects and fractional payloads", () => {
  assert.throws(() => createAgentRequest({ requestId: "r", tool: "cutly_edit", action: "inspect", projectId: "p" }), /TOOL_ACTION_INVALID/);
  assert.throws(() => createAgentRequest({ requestId: "r", tool: "cutly_edit", action: "plan" }), /REQUEST_INVALID/);
  assert.throws(() => createAgentRequest({ requestId: "r", tool: "cutly_edit", action: "plan", projectId: "p", payload: { value: 1.5 } }), /REQUEST_INVALID/);
});

test("HTTP Agent transport is bounded, canonical and redirect-safe", async () => {
  let observed;
  const request = createAgentRequest({ requestId: "r", tool: "cutly_project", action: "inspect", projectId: "p" });
  const fetch = async (endpoint, options) => {
    observed = { endpoint, options };
    const decoded = decodeCanonicalJSON(options.body);
    return new Response(encodeCanonicalValue(resultFor(decoded)), {
      status: 200,
      headers: { "Content-Type": AGENT_MEDIA_TYPE },
    });
  };
  const transport = new HTTPAgentTransport("http://127.0.0.1:9817/v1/agent", { fetch, maxResponseBytes: 4096 });
  const client = new CutlyClient(transport);
  const result = await client.call(request);
  assert.equal(result.ok, true);
  assert.equal(observed.options.method, "POST");
  assert.equal(observed.options.redirect, "error");
  assert.equal(observed.options.credentials, "omit");

  const oversized = new HTTPAgentTransport("https://example.invalid/v1/agent", {
    maxResponseBytes: 8,
    fetch: async () => new Response("123456789", { status: 200, headers: { "Content-Type": "application/json" } }),
  });
  await assert.rejects(() => oversized.roundTrip(new Uint8Array()), /RESPONSE_LIMIT/);
  assert.throws(() => new HTTPAgentTransport("file:///tmp/cutly.sock", { fetch }), /ENDPOINT_INVALID/);
  assert.throws(() => new HTTPAgentTransport("https://user@example.invalid/", { fetch }), /ENDPOINT_INVALID/);
});

test("HTTP Agent transport accepts only protocol JSON content types", async () => {
  const request = createAgentRequest({ requestId: "r", tool: "cutly_project", action: "capabilities" });
  const body = encodeCanonicalValue(resultFor(request));
  for (const contentType of [
    AGENT_MEDIA_TYPE,
    `${AGENT_MEDIA_TYPE}; profile=\"cutly.json.canonical-integer.v1\"`,
    "Application/JSON; PROFILE=cutly.json.canonical-integer.v1",
  ]) {
    const transport = new HTTPAgentTransport("https://example.invalid/v1/agent", {
      fetch: async () => new Response(body, { status: 200, headers: { "Content-Type": contentType } }),
    });
    await assert.doesNotReject(() => transport.roundTrip(encodeCanonicalValue(request)));
  }
  for (const contentType of [
    "",
    "application/jsonp",
    `${AGENT_MEDIA_TYPE}-evil`,
    "text/json",
    "application/json; charset=utf-8",
    `${AGENT_MEDIA_TYPE}; profile=\"other\"`,
    `${AGENT_MEDIA_TYPE}; profile=\"cutly.json.canonical-integer.v1\"; profile=\"cutly.json.canonical-integer.v1\"`,
    `${AGENT_MEDIA_TYPE}; broken`,
    "application/json;",
    `${AGENT_MEDIA_TYPE}; profile*=UTF-8''cutly.json.canonical-integer.v1`,
  ]) {
    const transport = new HTTPAgentTransport("https://example.invalid/v1/agent", {
      fetch: async () => new Response(body, { status: 200, headers: { "Content-Type": contentType } }),
    });
    await assert.rejects(
      () => transport.roundTrip(encodeCanonicalValue(request)),
      (error) => error instanceof AgentProtocolError && error.code === "AGENT.CONTENT_TYPE",
      contentType,
    );
  }
});

test("Agent result rejects ok/error contradictions and unknown fields", async () => {
  const request = createAgentRequest({ requestId: "r", tool: "cutly_project", action: "capabilities" });
  const invalid = new CutlyClient(async (raw) => {
    const decoded = decodeCanonicalJSON(raw);
    return encodeCanonicalValue(resultFor(decoded, {
      diagnostics: [{ code: "E", severity: "error", message: "bad" }],
    }));
  });
  await assert.rejects(() => invalid.call(request), /RESULT_INVALID/);

  const unknown = new CutlyClient(async (raw) => {
    const decoded = decodeCanonicalJSON(raw);
    return encodeCanonicalValue({ ...resultFor(decoded), surprise: true });
  });
  await assert.rejects(() => unknown.call(request), /unsupported member surprise/);
});

test("Agent envelopes round-trip the Go canonical fixtures", async () => {
  for (const name of ["agent-request-v1.canonical.json", "agent-result-v1.canonical.json"]) {
    const raw = (await readFile(new URL(`../../../agent/protocol/testdata/${name}`, import.meta.url), "utf8")).trim();
    const decoded = decodeCanonicalJSON(raw);
    assert.equal(new TextDecoder().decode(encodeCanonicalValue(decoded)), raw, name);
  }
});

test("Agent request validation does not invoke accessors", async () => {
  let invoked = false;
  const request = Object.defineProperty({}, "request_id", {
    enumerable: true,
    get() { invoked = true; return "r"; },
  });
  const client = new CutlyClient(async () => { throw new Error("must not run"); });
  await assert.rejects(() => client.call(request), /REQUEST_INVALID/);
  assert.equal(invoked, false);
});
