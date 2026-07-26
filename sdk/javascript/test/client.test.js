import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  DEFAULT_MAX_RESPONSE_BYTES,
  FEATURE_PRESETS,
  HOST_DURABILITY_PROFILES,
  HostDurability,
  OpaqueSnapshotPersistence,
  OutcomeOutbox,
  PROTOCOL_VERSION,
  ProposalAttemptCoordinator,
  RinAPIError,
  RinClient,
  RinConfigurationError,
  RinProtocolError,
  WorkflowCoordinator,
  createRinId,
  SDK_VERSION,
} from "../src/index.js";

test("default response limit matches the inline transport budget", () => {
  assert.equal(DEFAULT_MAX_RESPONSE_BYTES, 32 * 1024 * 1024);
  const client = new RinClient(undefined, { fetch: () => {} });
  assert.equal(client.maxResponseBytes, DEFAULT_MAX_RESPONSE_BYTES);
});

test("stable ID helper is protocol-safe and validates its entropy source", () => {
  const value = createRinId("report", (length) => {
    assert.equal(length, 16);
    return Uint8Array.from({ length }, (_, index) => index);
  });
  assert.equal(value, "report.000102030405060708090a0b0c0d0e0f");
  assert.match(value, /^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/);
  assert.throws(
    () => createRinId("bad prefix", () => new Uint8Array(16)),
    (error) => error instanceof RinConfigurationError && error.code === "invalid_id_prefix",
  );
  assert.throws(
    () => createRinId("request", () => new Uint8Array(15)),
    (error) => error instanceof RinConfigurationError && error.code === "invalid_random_source",
  );
});

test("capability negotiation requires protocol and the safe baseline", async () => {
  let data = {
    status: "ok",
    protocol_version: PROTOCOL_VERSION,
    release_version: SDK_VERSION,
    release_status: "preview",
    policy_mode: "deterministic",
    async_jobs: true,
    structured_generation: true,
    features: [...FEATURE_PRESETS.full],
    recommended_features: [...FEATURE_PRESETS.safeBaseline],
  };
  const client = new RinClient(undefined, {
    fetch: async () => response(200, { ok: true, data }),
  });
  const health = await client.negotiateCapabilities();
  assert.equal(health.protocol_version, PROTOCOL_VERSION);

  data = { ...data, features: [] };
  await assert.rejects(
    client.negotiateCapabilities(["memory-archive-v1"]),
    (error) => error instanceof RinConfigurationError && error.code === "missing_features",
  );
  data = { ...data, protocol_version: "rin.protocol/future" };
  await assert.rejects(
    client.negotiateCapabilities([]),
    (error) => error instanceof RinProtocolError && error.code === "protocol_mismatch",
  );
});

test("host durability profiles reject inflated durability claims", () => {
  assert.throws(
    () => new HostDurability({ profile: HOST_DURABILITY_PROFILES.transactionalAction }),
    (error) => error instanceof RinConfigurationError &&
      error.code === "invalid_host_durability",
  );
  const advisory = HostDurability.advisory({ stableIdentity: true });
  assert.throws(
    () => advisory.require(HOST_DURABILITY_PROFILES.idempotentAction),
    (error) => error instanceof RinConfigurationError &&
      error.code === "host_durability_insufficient",
  );
  HostDurability.transactionalAction().require(HOST_DURABILITY_PROFILES.idempotentAction);
});

test("WorkflowCoordinator gives idempotent apply a stable operation ID", async () => {
  let attempt = null;
  const entries = [];
  const store = {
    async loadProposalAttempt() { return attempt; },
    async createProposalAttempt(value) {
      attempt = structuredClone(value);
      return true;
    },
    async saveProposalAttempt(value) { attempt = structuredClone(value); },
    async completeProposalAttempt(input) {
      entries.push({ key: "outcome.workflow", report: structuredClone(input.report) });
      attempt = null;
    },
    async listOutcomeReports() { return entries.slice(); },
    async acknowledgeOutcome(entry) { entries.splice(entries.indexOf(entry), 1); },
  };
  const client = new RinClient(undefined, {
    fetch: async () => response(200, {
      ok: true,
      data: {
        session_id: "session.fixture",
        revision: 3,
        head_hash: "a".repeat(64),
        duplicate: false,
      },
    }),
  });
  const workflow = new WorkflowCoordinator(
    client,
    store,
    HostDurability.idempotentAction(),
  );
  const pending = await workflow.begin(
    "operation.stable",
    proposeRequest("request.fixture"),
  );
  let appliedOperation = "";
  await workflow.applyAndEnqueueOutcome({
    pendingTurn: pending,
    proposal: proposal(),
    report: rejectedReport("report.fixture", "event.fixture"),
    requiredDurability: HOST_DURABILITY_PROFILES.idempotentAction,
    async apply(operationId) { appliedOperation = operationId; },
  });
  assert.equal(appliedOperation, "operation.stable");
  assert.equal(attempt, null);
  assert.equal(entries.length, 1);
  assert.equal(await workflow.drainOutbox(), 1);

  const failed = await workflow.begin(
    "operation.failed",
    proposeRequest("request.failed"),
  );
  await assert.rejects(
    workflow.applyAndEnqueueOutcome({
      pendingTurn: failed,
      proposal: proposal({ request_id: "request.failed" }),
      report: rejectedReport("report.failed", "event.failed"),
      requiredDurability: HOST_DURABILITY_PROFILES.idempotentAction,
      async apply() { throw new Error("game save failed"); },
    }),
    /game save failed/,
  );
  assert.equal(attempt.operation_id, "operation.failed");
  assert.equal(entries.length, 0);
});

test("WorkflowCoordinator rejects concurrent resume on one host instance", async () => {
  let releaseLoad;
  const loadGate = new Promise((resolve) => { releaseLoad = resolve; });
  const store = {
    async loadProposalAttempt() {
      await loadGate;
      return null;
    },
    async createProposalAttempt() { return true; },
    async saveProposalAttempt() {},
    async completeProposalAttempt() {},
    async listOutcomeReports() { return []; },
    async acknowledgeOutcome() {},
  };
  const workflow = new WorkflowCoordinator(
    new RinClient(undefined, { fetch: async () => { throw new Error("unexpected"); } }),
    store,
  );
  const first = workflow.resumePendingWork();
  await assert.rejects(
    workflow.resumePendingWork(),
    (error) => error instanceof RinConfigurationError && error.code === "workflow_busy",
  );
  releaseLoad();
  await assert.rejects(
    first,
    (error) => error instanceof RinConfigurationError &&
      error.code === "invalid_proposal_attempt",
  );
});

test("Proposal Attempt settles game effect and Outbox atomically", async () => {
  let attempt = null;
  const outbox = [];
  let applied = 0;
  const store = {
    async loadProposalAttempt() { return attempt; },
    async createProposalAttempt(value) {
      if (attempt) return false;
      attempt = structuredClone(value);
      return true;
    },
    async saveProposalAttempt(value) { attempt = structuredClone(value); },
    async settleProposalAttempt(input) {
      assert.equal(input.attempt.operation_id, "operation.fixture");
      await input.apply();
      outbox.push({ key: "outcome.fixture", report: structuredClone(input.report) });
      attempt = null;
    },
  };
  let poll = 0;
  const client = new RinClient(undefined, {
    fetch: async (url) => {
      const path = new URL(url).pathname;
      if (path === "/v2/jobs/propose") {
        return response(202, {
          ok: true,
          data: {
            protocol_version: PROTOCOL_VERSION,
            job_id: "job.fixture",
            status: "queued",
            duplicate: false,
          },
        });
      }
      poll++;
      return response(200, {
        ok: true,
        data: proposalJob("succeeded", { proposal: proposal() }),
      });
    },
    sleep: async () => {},
  });
  const coordinator = new ProposalAttemptCoordinator(client, store);
  const request = proposeRequest("request.fixture");
  await coordinator.begin("operation.fixture", request);
  request.intent = "mutated after persistence";
  assert.equal(attempt.request.intent, "Talk");
  const resolved = await coordinator.resume({ deadlineMs: 50, intervalMs: 10 });
  assert.equal(poll, 1);
  assert.equal(attempt.job_id, "job.fixture");
  await coordinator.settle(
    resolved.attempt,
    resolved.proposal,
    rejectedReport("report.fixture", "event.fixture"),
    async () => { applied++; },
  );
  assert.equal(applied, 1);
  assert.equal(attempt, null);
  assert.equal(outbox.length, 1);
});

test("Outcome Outbox acknowledges only confirmed exact Action Report success", async () => {
  const entries = [{
    key: "outcome.fixture",
    report: rejectedReport("report.fixture", "event.fixture"),
  }];
  let fail = true;
  const client = new RinClient(undefined, {
    fetch: async () => fail
      ? response(500, {
        ok: false,
        error: { code: "mutation_outcome_unknown", message: "Unknown" },
      })
      : response(200, {
        ok: true,
        data: {
          session_id: "session.fixture",
          revision: 3,
          head_hash: "a".repeat(64),
          duplicate: true,
        },
      }),
  });
  const store = {
    async listOutcomeReports() { return entries.slice(); },
    async acknowledgeOutcome(entry, result) {
      assert.equal(result.duplicate, true);
      entries.splice(entries.indexOf(entry), 1);
    },
  };
  const outbox = new OutcomeOutbox(client, store);
  await assert.rejects(
    outbox.drain(),
    (error) => error instanceof RinAPIError && error.code === "mutation_outcome_unknown",
  );
  assert.equal(entries.length, 1);
  fail = false;
  assert.equal(await outbox.drain(), 1);
  assert.equal(entries.length, 0);
});

test("opaque Snapshot persistence retains additive fields and exact stored bytes", async () => {
  let stored;
  const persistence = new OpaqueSnapshotPersistence({
    async putSnapshot(_key, value) { stored = value.slice(); },
    async getSnapshot() { return stored.slice(); },
  });
  const snapshot = {
    protocol_version: PROTOCOL_VERSION,
    state_hash: "a".repeat(64),
    future_additive: { nested: ["preserved", 7] },
  };
  await persistence.save("slot.fixture", snapshot);
  const exact = new TextDecoder().decode(stored);
  const restored = await persistence.load("slot.fixture");
  assert.deepEqual(restored.future_additive, snapshot.future_additive);
  assert.equal(JSON.stringify(restored), exact);
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

function proposeRequest(requestId) {
  const epoch = {
    session_id: "session.fixture",
    world_id: "world.fixture",
    host: 1,
    world: 1,
    timeline: 1,
  };
  const window = {
    id: `window.${requestId}`,
    mode: "sequential",
    epoch,
    observation_seq: 1,
    opened_at: { clock: "step", value: 0 },
    deadline: { clock: "step", value: 100 },
    actor_ids: ["actor.fixture"],
  };
  return {
    protocol_version: PROTOCOL_VERSION,
    session_id: "session.fixture",
    request_id: requestId,
    actor_id: "actor.fixture",
    intent: "Talk",
    decision_window: window,
    offers: [{
      offer_id: "talk",
      decision_window_id: window.id,
      actor_id: "actor.fixture",
      capability: { id: "rin.dialogue.say", version: "1.0.0" },
      descriptor_digest: "a".repeat(64),
      description: "Talk",
      arguments: {},
      expected_epoch: epoch,
      observation_seq: 1,
      deadline: window.deadline,
    }],
  };
}

function rejectedReport(requestId, eventId) {
  return {
    protocol_version: PROTOCOL_VERSION,
    session_id: "session.fixture",
    request_id: requestId,
    report: {
      proposal_id: "proposal.fixture",
      event_id: eventId,
      decision: "rejected",
      summary: "The host rejected the action.",
    },
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
    const path = new URL(url).pathname;
    if (path === "/v2/session/export") {
      requests.push({ url: new URL(url), options, status: 200 });
      return new Response(validTransfer(), {
        status: 200,
        headers: { "content-type": "application/x-ndjson" },
      });
    }
    const accepted = url.endsWith("/v2/jobs/propose") || url.endsWith("/v2/generation/jobs") ? 202 : 200;
    requests.push({ url: new URL(url), options, status: accepted });
    return response(accepted, { ok: true, data: { status: "ok" } });
  };
  const client = new RinClient(undefined, { token: "fixture", fetch });
  const payload = {
    protocol_version: PROTOCOL_VERSION,
    request_id: "request.fixture",
    utf8: "雨",
  };
  const transferChunks = [];
  const importSource = () => new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(validTransfer()));
      controller.close();
    },
  });
  const binding = {
    game_id: "game.fixture",
    content_id: "base",
    content_version: "1",
    content_hash: "hash",
  };
  const cases = [
    ["health", () => client.health(), "GET", "/health"],
    ["create_session", () => client.createSession(payload), "POST", "/v2/session/create"],
    ["observe", () => client.observe(payload), "POST", "/v2/session/observe"],
    ["propose", () => client.propose(payload), "POST", "/v2/agent/propose"],
    ["submit_proposal_job", () => client.submitProposalJob(payload), "POST", "/v2/jobs/propose"],
    ["get_proposal_job", () => client.getProposalJob("job.fixture"), "GET", "/v2/jobs/job.fixture"],
    ["cancel_proposal_job", () => client.cancelProposalJob("job.fixture"), "DELETE", "/v2/jobs/job.fixture"],
    ["submit_generation_job", () => client.submitGenerationJob(payload), "POST", "/v2/generation/jobs"],
    ["get_generation_job", () => client.getGenerationJob("job.fixture"), "GET", "/v2/generation/jobs/job.fixture"],
    ["cancel_generation_job", () => client.cancelGenerationJob("job.fixture"), "DELETE", "/v2/generation/jobs/job.fixture"],
    ["report_action", () => client.reportAction(payload), "POST", "/v2/action/report"],
    ["report_action_batch", () => client.reportActionBatch(payload), "POST", "/v2/action/report-batch"],
    ["set_actor_activity", () => client.setActorActivity(payload), "POST", "/v2/session/activity"],
    ["arbitrate", () => client.arbitrate(payload), "POST", "/v2/world/arbitrate"],
    ["state", () => client.state(payload), "POST", "/v2/session/get"],
    ["session_stats", () => client.sessionStats(payload), "POST", "/v2/session/stats"],
    ["archive_session", () => client.archiveSession(payload), "POST", "/v2/session/archive"],
    ["delete_session", () => client.deleteSession(payload), "POST", "/v2/session/delete"],
    ["snapshot", () => client.snapshot(payload), "POST", "/v2/session/snapshot"],
    ["restore", () => client.restore(payload), "POST", "/v2/session/restore"],
    ["export_session", () => client.exportSession(payload, {
      write: (chunk) => transferChunks.push(chunk),
    }), "POST", "/v2/session/export"],
    ["import_session", () => client.importSession(importSource(), binding), "POST", "/v2/session/import"],
    ["timeline", () => client.timeline(payload), "POST", "/v2/session/timeline"],
    ["replay", () => client.replay(payload), "POST", "/v2/session/replay"],
    ["due_agents", () => client.dueAgents(payload), "POST", "/v2/scheduler/due"],
  ];
  for (const [, call, method, path] of cases) {
    const result = await call();
    const request = requests.at(-1);
    assert.equal(request.url.pathname, path);
    assert.equal(request.options.method, method);
    assert.equal(request.options.headers.Authorization, "Bearer fixture");
    assert.equal(request.options.headers["User-Agent"], `rin-javascript/${SDK_VERSION}`);
    assert.equal(request.options.redirect, "error");
    if (request.url.pathname === "/v2/session/import") {
      assert.equal(request.options.headers["Content-Type"], "application/x-ndjson");
      assert.equal(request.options.headers["Rin-Expected-Game-Id"], binding.game_id);
    } else {
      assert.deepEqual(
        request.options.body === undefined ? undefined : JSON.parse(request.options.body),
        method === "POST" ? payload : undefined,
      );
    }
    if (request.url.pathname === "/v2/session/export") {
      assert.equal(result.type, "complete");
    } else {
      assert.equal(result.status, "ok");
    }
  }

  const manifest = JSON.parse(
    readFileSync(new URL("../../conformance/routes.json", import.meta.url), "utf8"),
  );
  const observedRoutes = requests
    .map(({ url, options, status }, index) =>
      `${cases[index][0]} ${options.method} ${url.pathname.replace("job.fixture", "{job_id}")} ${status}`)
    .sort();
  const expectedNamedRoutes = manifest.operations
    .filter(({ profile }) => profile !== "operational")
    .map(({ name, method, path, status }) => `${name} ${method} ${path} ${status}`)
    .sort();
  assert.deepEqual(observedRoutes, expectedNamedRoutes);
  assert.equal(
    new TextDecoder().decode(
      Uint8Array.from(transferChunks.flatMap((chunk) => [...chunk])),
    ),
    validTransfer(),
  );
});

function validTransfer() {
  const head = "a".repeat(64);
  const digest = "b".repeat(64);
  return [
    JSON.stringify({
      type: "manifest",
      transfer_version: "rin.session-transfer/v1",
      session_id: "session.fixture",
      terminal_revision: 1,
      terminal_head_hash: head,
      event_count: 1,
    }),
    JSON.stringify({
      type: "event",
      record: { sequence: 1 },
      record_sha256: digest,
    }),
    JSON.stringify({
      type: "complete",
      terminal_revision: 1,
      terminal_head_hash: head,
      event_count: 1,
      stream_sha256: digest,
    }),
    "",
  ].join("\n");
}

test("action decisions are serialized explicitly", async () => {
  const bodies = [];
  const client = new RinClient(undefined, {
    fetch: async (_url, options) => {
      bodies.push(JSON.parse(options.body));
      return response(200, { ok: true, data: {} });
    },
  });
  await client.reportAction({ report: { decision: "rejected" } });
  await client.reportActionBatch({ reports: [{ decision: "rejected" }] });
  assert.equal(bodies[0].report.decision, "rejected");
  assert.equal(bodies[1].reports[0].decision, "rejected");
});

test("transfer export applies sink backpressure and requires complete", async () => {
  const chunks = [
    ...new TextEncoder().encode(validTransfer()),
  ].map((byte) => new Uint8Array([byte]));
  let reads = 0;
  let writes = 0;
  const client = new RinClient(undefined, {
    fetch: async () => ({
      status: 200,
      headers: { get: (name) => name === "content-type" ? "application/x-ndjson" : null },
      body: {
        getReader: () => ({
          read: async () => reads < chunks.length
            ? { done: false, value: chunks[reads++] }
            : { done: true },
          releaseLock: () => {},
        }),
      },
    }),
  });
  const complete = await client.exportSession(
    { protocol_version: PROTOCOL_VERSION, session_id: "session.fixture" },
    { write: async () => { writes += 1; } },
  );
  assert.equal(complete.type, "complete");
  assert.equal(writes, 3);
  assert.equal(reads, chunks.length);

  const truncated = validTransfer().split("\n").slice(0, -2).join("\n") + "\n";
  const invalid = new RinClient(undefined, {
    fetch: async () => new Response(truncated, {
      status: 200,
      headers: { "content-type": "application/x-ndjson" },
    }),
  });
  await assert.rejects(
    invalid.exportSession(
      { protocol_version: PROTOCOL_VERSION, session_id: "session.fixture" },
      { write: () => {} },
    ),
    (error) => error instanceof RinProtocolError && error.code === "invalid_transfer",
  );
});

test("transfer error frames are terminal API failures", async () => {
  const manifest = validTransfer().split("\n")[0];
  const body = `${manifest}\n${JSON.stringify({
    type: "error",
    error: { code: "store_load_failed", message: "export stopped" },
  })}\n`;
  let writes = 0;
  const client = new RinClient(undefined, {
    fetch: async () => new Response(body, {
      status: 200,
      headers: { "content-type": "application/x-ndjson" },
    }),
  });
  await assert.rejects(
    client.exportSession(
      { protocol_version: PROTOCOL_VERSION, session_id: "session.fixture" },
      { write: () => { writes += 1; } },
    ),
    (error) => error instanceof RinAPIError && error.code === "store_load_failed",
  );
  assert.equal(writes, 1);
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
      client.reportAction(payload),
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
      assert.equal(path, "/v2/jobs/job.fixture");
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
      assert.equal(path, "/v2/generation/jobs/job.fixture");
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
