import assert from "node:assert/strict";
import test from "node:test";

import { createRenderJob } from "../src/index.js";

const digest = (fill) => `sha256:${fill.repeat(64)}`;

function fixture() {
  return {
    jobId: "job-1",
    idempotencyKey: "job-1-once",
    artifactUri: "file:///tmp/cutly-job-1.mp4",
    plan: {
      fingerprint: digest("a"),
      requested_range: { start: { value: 0n, rate: 1n }, duration: { value: 1n, rate: 1n } },
      frame_range: { first: 0n, count: 30n },
      sample_range: { first: 0n, count: 48_000n },
    },
    executionManifest: {
      fingerprint: digest("b"),
      plan_fingerprint: digest("a"),
    },
  };
}

test("createRenderJob binds immutable plan ranges and bigint wire integers", () => {
  const input = fixture();
  const job = createRenderJob(input);
  input.plan.frame_range.count = 999n;

  assert.equal(Object.getPrototypeOf(job), null);
  assert.equal(Object.isFrozen(job), true);
  assert.equal(job.schema_version, 1n);
  assert.equal(job.id, "job-1");
  assert.equal(job.idempotency_key, "job-1-once");
  assert.equal(job.plan_fingerprint, digest("a"));
  assert.equal(job.execution_manifest_fingerprint, digest("b"));
  assert.equal(job.frame_range.count, 30n);
  assert.equal(job.sample_range.count, 48_000n);
  assert.equal(job.artifact_uri, "file:///tmp/cutly-job-1.mp4");
});

test("createRenderJob rejects mismatched, incomplete, and effectful input", () => {
  const mismatched = fixture();
  mismatched.executionManifest.plan_fingerprint = digest("c");
  assert.throws(() => createRenderJob(mismatched), /not bound to plan/);

  const incomplete = fixture();
  delete incomplete.plan.frame_range;
  assert.throws(() => createRenderJob(incomplete), /plan\.frame_range must be an object/);

  const extra = fixture();
  extra.backend = "caller-selected";
  assert.throws(() => createRenderJob(extra), /unknown or missing fields/);

  const remote = fixture();
  remote.artifactUri = "https://example.com/out.mp4";
  assert.throws(() => createRenderJob(remote), /hostless non-root file URL/);

  const unclean = fixture();
  unclean.artifactUri = "file:///tmp/../out.mp4";
  assert.throws(() => createRenderJob(unclean), /hostless non-root file URL/);

  let invoked = false;
  const effectful = fixture();
  Object.defineProperty(effectful, "jobId", {
    enumerable: true,
    get() {
      invoked = true;
      return "job";
    },
  });
  assert.throws(() => createRenderJob(effectful), /not safely canonicalizable/);
  assert.equal(invoked, false);
});
