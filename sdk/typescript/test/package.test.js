import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as sdk from "../src/index.js";

test("package metadata exposes the declaration entry and bundled license", async () => {
  const packageJSON = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  assert.equal(packageJSON.version, "0.1.0-rc.1");
  assert.equal(packageJSON.type, "module");
  assert.equal(packageJSON.types, "./src/index.d.ts");
  assert.equal(packageJSON.exports["."].types, "./src/index.d.ts");
  assert.equal(packageJSON.exports["."].import, "./src/index.js");
  assert.equal(packageJSON.license, "Apache-2.0");
  assert.equal(packageJSON.publishConfig.access, "public");
  assert.ok(packageJSON.files.includes("LICENSE"));
  const packageLicense = await readFile(new URL("../LICENSE", import.meta.url), "utf8");
  const repositoryLicense = await readFile(new URL("../../../LICENSE", import.meta.url), "utf8");
  assert.match(packageLicense, /Apache License[\s\S]*Version 2\.0/);
  assert.equal(packageLicense, repositoryLicense);
});

test("every runtime export has a public declaration", async () => {
  const declarations = await readFile(new URL("../src/index.d.ts", import.meta.url), "utf8");
  const publicValues = new Set();
  const pattern = /^export declare (?:const|class|function)\s+([A-Za-z_$][A-Za-z0-9_$]*)/gm;
  for (const match of declarations.matchAll(pattern)) publicValues.add(match[1]);
  const runtimeValues = new Set(Object.keys(sdk));
  assert.deepEqual(
    [...runtimeValues].filter((name) => !publicValues.has(name)),
    [],
    "runtime export is missing from index.d.ts",
  );
  assert.deepEqual(
    [...publicValues].filter((name) => !runtimeValues.has(name)),
    [],
    "index.d.ts value declaration is missing from the runtime",
  );
});
