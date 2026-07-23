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
