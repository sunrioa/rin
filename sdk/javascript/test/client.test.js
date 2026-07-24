import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  DEFAULT_MAX_RESPONSE_BYTES,
  PROTOCOL_VERSION,
  RinAPIError,
  RinClient,
  RinConfigurationError,
  RinProtocolError,
  SDK_VERSION,
} from "../src/index.js";

test("default response limit matches the inline transport budget", () => {
  assert.equal(DEFAULT_MAX_RESPONSE_BYTES, 32 * 1024 * 1024);
  const client = new RinClient(undefined, { fetch: () => {} });
  assert.equal(client.maxResponseBytes, DEFAULT_MAX_RESPONSE_BYTES);
});

function response(status, envelope, headers = {}) {
  const bytes = new TextEncoder().encode(JSON.stringify(envelope));
  const values = new Map(Object.entries({ "content-length": String(bytes.byteLength), ...headers }));
  return {
    status,
    headers: { get: (name) => values.get(name.toLowerCase()) ?? null },
    arrayBuffer: async () => bytes.buffer,
  };
}

function proposal(overrides = {}) {
  return {
    id: "proposal.fixture",
    session_id: "session.fixture",
    request_id: "request.fixture",
    actor_id: "actor.fixture",
    tick: 7,
    ...overrides,
  };
}

function proposalJob(status = "running", overrides = {}) {
  return {
    job_id: "job.fixture",
    session_id: "session.fixture",
    request_id: "request.fixture",
    status,
    ...overrides,
  };
}

function generationJob(status = "running", overrides = {}) {
  return {
    job_id: "job.fixture",
    request_id: "generation.fixture",
    status,
    ...overrides,
  };
}

test("all protocol routes use the expected method and bearer token", async () => {
  const requests = [];
  const fetch = async (url, options) => {
    const accepted = url.endsWith("/v1/jobs/propose") || url.endsWith("/v1/generation/jobs") ? 202 : 200;
    requests.push({ url: new URL(url), options, status: accepted });
    return response(accepted, { ok: true, data: { status: "ok" } });
  };
  const client = new RinClient(undefined, { token: "fixture", fetch });
  const payload = {
    protocol_version: PROTOCOL_VERSION,
    request_id: "request.fixture",
    utf8: "雨",
  };
  const cases = [
    ["health", () => client.health(), "GET", "/health"],
    ["create_session", () => client.createSession(payload), "POST", "/v1/session/create"],
    ["observe", () => client.observe(payload), "POST", "/v1/session/observe"],
    ["propose", () => client.propose(payload), "POST", "/v1/agent/propose"],
    ["submit_proposal_job", () => client.submitProposalJob(payload), "POST", "/v1/jobs/propose"],
    ["get_proposal_job", () => client.getProposalJob("job.fixture"), "GET", "/v1/jobs/job.fixture"],
    ["cancel_proposal_job", () => client.cancelProposalJob("job.fixture"), "DELETE", "/v1/jobs/job.fixture"],
    ["submit_generation_job", () => client.submitGenerationJob(payload), "POST", "/v1/generation/jobs"],
    ["get_generation_job", () => client.getGenerationJob("job.fixture"), "GET", "/v1/generation/jobs/job.fixture"],
    ["cancel_generation_job", () => client.cancelGenerationJob("job.fixture"), "DELETE", "/v1/generation/jobs/job.fixture"],
    ["commit", () => client.commit(payload), "POST", "/v1/action/commit"],
    ["commit_batch", () => client.commitBatch(payload), "POST", "/v1/action/commit-batch"],
    ["set_actor_activity", () => client.setActorActivity(payload), "POST", "/v1/session/activity"],
    ["arbitrate", () => client.arbitrate(payload), "POST", "/v1/world/arbitrate"],
    ["state", () => client.state(payload), "POST", "/v1/session/get"],
    ["snapshot", () => client.snapshot(payload), "POST", "/v1/session/snapshot"],
    ["restore", () => client.restore(payload), "POST", "/v1/session/restore"],
    ["timeline", () => client.timeline(payload), "POST", "/v1/session/timeline"],
    ["replay", () => client.replay(payload), "POST", "/v1/session/replay"],
    ["due_agents", () => client.dueAgents(payload), "POST", "/v1/scheduler/due"],
  ];
  for (const [, call, method, path] of cases) {
    const result = await call();
    const request = requests.at(-1);
    assert.equal(request.url.pathname, path);
    assert.equal(request.options.method, method);
    assert.equal(request.options.headers.Authorization, "Bearer fixture");
    assert.equal(request.options.headers["User-Agent"], `rin-javascript/${SDK_VERSION}`);
    assert.equal(request.options.redirect, "error");
    assert.deepEqual(
      request.options.body === undefined ? undefined : JSON.parse(request.options.body),
      method === "POST" ? payload : undefined,
    );
    assert.equal(result.status, "ok");
  }

  const manifest = JSON.parse(
    readFileSync(new URL("../../conformance/routes.json", import.meta.url), "utf8"),
  );
  const observedRoutes = requests
    .map(({ url, options, status }, index) =>
      `${cases[index][0]} ${options.method} ${url.pathname.replace("job.fixture", "{job_id}")} ${status}`)
    .sort();
  const expectedNamedRoutes = manifest.operations
    .map(({ name, method, path, status }) => `${name} ${method} ${path} ${status}`)
    .sort();
  assert.deepEqual(observedRoutes, expectedNamedRoutes);
});

test("false commit flags are serialized explicitly", async () => {
  const bodies = [];
  const client = new RinClient(undefined, {
    fetch: async (_url, options) => {
      bodies.push(JSON.parse(options.body));
      return response(200, { ok: true, data: {} });
    },
  });
  await client.commit({ accepted: false });
  await client.commitBatch({ items: [{ accepted: false }] });
  assert.equal(Object.hasOwn(bodies[0], "accepted"), true);
  assert.equal(bodies[0].accepted, false);
  assert.equal(Object.hasOwn(bodies[1].items[0], "accepted"), true);
  assert.equal(bodies[1].items[0].accepted, false);
});

test("remote endpoints require TLS and a token", () => {
  assert.throws(() => new RinClient("http://models.example", { token: "fixture", fetch: () => {} }), RinConfigurationError);
  assert.throws(() => new RinClient("https://models.example", { fetch: () => {} }), RinConfigurationError);
  assert.equal(new RinClient("https://models.example", { token: "fixture", fetch: () => {} }).baseUrl, "https://models.example");
});

test("invalid JSON numbers, cycles, and depth fail before transport", async () => {
  let transportCalls = 0;
  const client = new RinClient(undefined, {
    fetch: async () => {
      transportCalls += 1;
      return response(200, { ok: true, data: {} });
    },
  });
  const cycle = {};
  cycle.self = cycle;
  let deep = "leaf";
  for (let index = 0; index < 66; index += 1) deep = [deep];
  const sparse = [];
  sparse[1] = "value";
  const invalidPayloads = [
    { nested: [{ unsafe: Number.MAX_SAFE_INTEGER + 1 }] },
    { nested: Number.NaN },
    { nested: Number.POSITIVE_INFINITY },
    { nested: "\ud800" },
    { "\udfff": "invalid key" },
    cycle,
    { nested: deep },
    { nested: sparse },
    new Date("2020-01-01T00:00:00Z"),
    new Map([["key", "value"]]),
    { toJSON: () => "not an object" },
  ];
  for (const payload of invalidPayloads) {
    await assert.rejects(
      client.commit(payload),
      (error) => error instanceof RinProtocolError && error.code === "invalid_request",
    );
  }
  assert.equal(transportCalls, 0);
});

test("unsafe identifiers and oversized responses are rejected", async () => {
  const client = new RinClient(undefined, {
    maxResponseBytes: 1024,
    fetch: async () => response(200, { ok: true, data: {} }, { "content-length": "2048" }),
  });
  assert.throws(() => client.getProposalJob("\u4f5c\u4e1a"), RinConfigurationError);
  await assert.rejects(client.health(), RinProtocolError);
});

test("streamed responses are capped before the full body is buffered", async () => {
  let reads = 0;
  let canceled = false;
  const body = {
    getReader: () => ({
      read: async () => {
        reads += 1;
        return { done: false, value: new Uint8Array(600) };
      },
      cancel: async () => { canceled = true; },
      releaseLock: () => {},
    }),
  };
  const client = new RinClient(undefined, {
    maxResponseBytes: 1024,
    fetch: async () => ({ status: 200, headers: { get: () => null }, body }),
  });
  await assert.rejects(client.health(), RinProtocolError);
  assert.equal(reads, 2);
  assert.equal(canceled, true);
});

test("the deadline remains active while a streamed body is read", async () => {
  const client = new RinClient(undefined, {
    timeoutMs: 50,
    fetch: async (_url, options) => ({
      status: 200,
      headers: { get: () => null },
      body: {
        getReader: () => ({
          read: () => new Promise((_resolve, reject) => {
            options.signal.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
          }),
          cancel: async () => {},
          releaseLock: () => {},
        }),
      },
    }),
  });
  await assert.rejects(client.health(), (error) => error.code === "transport_timeout");
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

test("proposal completion returned by timeout cancellation wins the race", async () => {
  let now = 0;
  const client = new RinClient(undefined, {
    now: () => now,
    sleep: async (milliseconds) => { now += milliseconds; },
    fetch: async (url, options) => {
      const path = new URL(url).pathname;
      const data = options.method === "DELETE"
        ? proposalJob("succeeded", { proposal: proposal({ id: "proposal.race" }) })
        : proposalJob();
      assert.equal(path, "/v1/jobs/job.fixture");
      return response(200, { ok: true, data });
    },
  });

  const job = await client.waitForProposal("job.fixture", { deadlineMs: 50, intervalMs: 10 });

  assert.equal(job.proposal.id, "proposal.race");
});

test("generation completion returned by timeout cancellation wins the race", async () => {
  let now = 0;
  const client = new RinClient(undefined, {
    now: () => now,
    sleep: async (milliseconds) => { now += milliseconds; },
    fetch: async (url, options) => {
      const path = new URL(url).pathname;
      const data = options.method === "DELETE"
        ? generationJob("succeeded", { result: { content: "finished at the deadline" } })
        : generationJob("queued");
      assert.equal(path, "/v1/generation/jobs/job.fixture");
      return response(200, { ok: true, data });
    },
  });

  const job = await client.waitForGeneration("job.fixture", { deadlineMs: 50, intervalMs: 10 });

  assert.equal(job.result.content, "finished at the deadline");
});

test("timeout cancellation preserves terminal errors and validates raced success", async () => {
  let now = 0;
  let canceledData = proposalJob("stale", {
    error: { code: "proposal_stale", message: "World changed" },
  });
  const client = new RinClient(undefined, {
    now: () => now,
    sleep: async (milliseconds) => { now += milliseconds; },
    fetch: async (_url, options) => response(200, {
      ok: true,
      data: options.method === "DELETE" ? canceledData : proposalJob(),
    }),
  });

  await assert.rejects(
    client.waitForProposal("job.fixture", { deadlineMs: 50, intervalMs: 10 }),
    (error) => error instanceof RinAPIError && error.code === "proposal_stale",
  );

  now = 0;
  canceledData = proposalJob("succeeded");
  await assert.rejects(
    client.waitForProposal("job.fixture", { deadlineMs: 50, intervalMs: 10 }),
    (error) => error instanceof RinProtocolError && error.code === "invalid_job",
  );
});

test("waiters reject crossed or malformed GET job identities", async () => {
  let data = proposalJob("running", { job_id: "job.other" });
  const client = new RinClient(undefined, {
    fetch: async () => response(200, { ok: true, data }),
  });
  await assert.rejects(
    client.waitForProposal("job.fixture"),
    (error) => error instanceof RinProtocolError && error.code === "invalid_job",
  );

  for (const malformedProposal of [
    proposal({ session_id: "session.other" }),
    proposal({ request_id: "request.other" }),
    proposal({ tick: 1.5 }),
    proposal({ tick: Number.MAX_SAFE_INTEGER + 1 }),
  ]) {
    data = proposalJob("succeeded", { proposal: malformedProposal });
    await assert.rejects(
      client.waitForProposal("job.fixture"),
      (error) => error instanceof RinProtocolError && error.code === "invalid_job",
    );
  }

  data = generationJob("queued", { request_id: 42 });
  await assert.rejects(
    client.waitForGeneration("job.fixture"),
    (error) => error instanceof RinProtocolError && error.code === "invalid_job",
  );
});

test("waiters reject crossed or malformed timeout DELETE race results", async () => {
  let now = 0;
  let mode = "proposal";
  const client = new RinClient(undefined, {
    maxResponseBytes: 8 * 1024 * 1024,
    now: () => now,
    sleep: async (milliseconds) => { now += milliseconds; },
    fetch: async (_url, options) => {
      let data;
      if (mode === "proposal") {
        data = options.method === "DELETE"
          ? proposalJob("succeeded", { job_id: "job.other", proposal: proposal() })
          : proposalJob();
      } else {
        data = options.method === "DELETE"
          ? generationJob("succeeded", { result: { content: "x".repeat(4 * 1024 * 1024 + 1) } })
          : generationJob();
      }
      return response(200, { ok: true, data });
    },
  });

  await assert.rejects(
    client.waitForProposal("job.fixture", { deadlineMs: 50, intervalMs: 10 }),
    (error) => error instanceof RinProtocolError && error.code === "invalid_job",
  );

  now = 0;
  mode = "generation";
  await assert.rejects(
    client.waitForGeneration("job.fixture", { deadlineMs: 50, intervalMs: 10 }),
    (error) => error instanceof RinProtocolError && error.code === "invalid_job",
  );
});

test("a Rin error from timeout cancellation remains job_timeout", async () => {
  let now = 0;
  const client = new RinClient(undefined, {
    now: () => now,
    sleep: async (milliseconds) => { now += milliseconds; },
    fetch: async (_url, options) => options.method === "DELETE"
      ? response(503, { ok: false, error: { code: "jobs_unavailable", message: "Unavailable" } })
      : response(200, { ok: true, data: proposalJob() }),
  });

  await assert.rejects(
    client.waitForProposal("job.fixture", { deadlineMs: 50, intervalMs: 10 }),
    (error) => error instanceof RinAPIError && error.code === "job_timeout",
  );
});
