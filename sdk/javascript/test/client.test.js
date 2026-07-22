import assert from "node:assert/strict";
import test from "node:test";

import {
  RinAPIError,
  RinClient,
  RinConfigurationError,
  RinProtocolError,
} from "../src/index.js";

function response(status, envelope, headers = {}) {
  const bytes = new TextEncoder().encode(JSON.stringify(envelope));
  const values = new Map(Object.entries({ "content-length": String(bytes.byteLength), ...headers }));
  return {
    status,
    headers: { get: (name) => values.get(name.toLowerCase()) ?? null },
    arrayBuffer: async () => bytes.buffer,
  };
}

test("all protocol routes use the expected method and bearer token", async () => {
  const requests = [];
  const fetch = async (url, options) => {
    requests.push({ url: new URL(url), options });
    const accepted = url.endsWith("/v1/jobs/propose") || url.endsWith("/v1/generation/jobs") ? 202 : 200;
    return response(accepted, { ok: true, data: { status: "ok" } });
  };
  const client = new RinClient(undefined, { token: "fixture", fetch });
  const cases = [
    [() => client.health(), "GET", "/health"],
    [() => client.createSession({}), "POST", "/v1/session/create"],
    [() => client.observe({}), "POST", "/v1/session/observe"],
    [() => client.propose({}), "POST", "/v1/agent/propose"],
    [() => client.submitProposalJob({}), "POST", "/v1/jobs/propose"],
    [() => client.getProposalJob("job.fixture"), "GET", "/v1/jobs/job.fixture"],
    [() => client.cancelProposalJob("job.fixture"), "DELETE", "/v1/jobs/job.fixture"],
    [() => client.submitGenerationJob({}), "POST", "/v1/generation/jobs"],
    [() => client.getGenerationJob("job.fixture"), "GET", "/v1/generation/jobs/job.fixture"],
    [() => client.cancelGenerationJob("job.fixture"), "DELETE", "/v1/generation/jobs/job.fixture"],
    [() => client.commit({}), "POST", "/v1/action/commit"],
    [() => client.commitBatch({}), "POST", "/v1/action/commit-batch"],
    [() => client.setActorActivity({}), "POST", "/v1/session/activity"],
    [() => client.arbitrate({}), "POST", "/v1/world/arbitrate"],
    [() => client.state({}), "POST", "/v1/session/get"],
    [() => client.snapshot({}), "POST", "/v1/session/snapshot"],
    [() => client.restore({}), "POST", "/v1/session/restore"],
    [() => client.timeline({}), "POST", "/v1/session/timeline"],
    [() => client.replay({}), "POST", "/v1/session/replay"],
    [() => client.dueAgents({}), "POST", "/v1/scheduler/due"],
  ];
  for (const [call, method, path] of cases) {
    await call();
    const request = requests.at(-1);
    assert.equal(request.url.pathname, path);
    assert.equal(request.options.method, method);
    assert.equal(request.options.headers.Authorization, "Bearer fixture");
    assert.equal(request.options.redirect, "error");
  }
});

test("remote endpoints require TLS and a token", () => {
  assert.throws(() => new RinClient("http://models.example", { token: "fixture", fetch: () => {} }), RinConfigurationError);
  assert.throws(() => new RinClient("https://models.example", { fetch: () => {} }), RinConfigurationError);
  assert.equal(new RinClient("https://models.example", { token: "fixture", fetch: () => {} }).baseUrl, "https://models.example");
});

test("unsafe identifiers and oversized responses are rejected", async () => {
  const client = new RinClient(undefined, {
    maxResponseBytes: 1024,
    fetch: async () => response(200, { ok: true, data: {} }, { "content-length": "2048" }),
  });
  assert.throws(() => client.getProposalJob("作业"), RinConfigurationError);
  await assert.rejects(client.health(), RinProtocolError);
});

test("API errors expose only the bounded protocol detail", async () => {
  const client = new RinClient(undefined, {
    fetch: async () => response(400, { ok: false, error: { code: "invalid_request", message: "safe", field: "actor_id" } }),
  });
  await assert.rejects(client.health(), (error) => {
    assert.ok(error instanceof RinAPIError);
    assert.equal(error.code, "invalid_request");
    assert.equal(error.status, 400);
    assert.equal(error.field, "actor_id");
    return true;
  });
});
