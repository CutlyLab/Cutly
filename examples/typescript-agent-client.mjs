import {
  CutlyClient,
  HTTPAgentTransport,
  createAgentRequest,
} from "@cutly/sdk";

const endpoint = process.env.CUTLY_ENDPOINT ?? "http://127.0.0.1:9817/v1/agent";
const client = new CutlyClient(new HTTPAgentTransport(endpoint));

const result = await client.call(createAgentRequest({
  requestId: "example-inspect-1",
  tool: "cutly_project",
  action: "inspect",
  projectId: "demo",
}));

console.log({
  ok: result.ok,
  state: result.state,
  revision: result.new_revision?.toString(),
  diagnostics: result.diagnostics,
});

if (!result.ok) process.exitCode = 3;
