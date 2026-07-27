import assert from "node:assert/strict";
import { mkdir, mkdtemp, readdir, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { RinClient, RinTransportError } from "@sunrioa/rin-sdk";
import { runRuleTree } from "../src/baseline.js";
import { runRinStory } from "../src/rin-adapter.js";
import { StoryWorkflowStore } from "../src/workflow-store.js";
import { runStory } from "../src/runner.js";

async function temporaryStore() {
  const directory = await mkdtemp(join(tmpdir(), "rin-story-test-"));
  return new StoryWorkflowStore(join(directory, "save.json")).load();
}

test("fair rule-tree baseline persists and applies the preference", async () => {
  const store = await temporaryStore();
  let shown;
  const result = await runRuleTree(store, "tea", async (action) => {
    shown = action;
  });

  assert.equal(result.action.id, "offer.tea");
  assert.equal(shown.id, "offer.tea");
  const reloaded = await new StoryWorkflowStore(store.path).load();
  assert.equal(reloaded.game.preference, "tea");
  assert.deepEqual(reloaded.game.applied_action_ids, ["offer.tea"]);
});

test("Proposal settlement publishes game effect and Outcome Outbox together", async () => {
  const store = await temporaryStore();
  await store.ensureSessionId("session.fixture");
  await store.beginRinTurn("tea", 1);
  const attempt = {
    version: 1,
    operation_id: "operation.fixture",
    request: { request_id: "request.fixture", session_id: "session.fixture" },
    job_id: "job.fixture",
  };
  assert.equal(await store.createProposalAttempt(attempt), true);
  await store.settleProposalAttempt({
    attempt,
    report: { request_id: "report.fixture" },
    apply: async () => store.recordRinAction({ id: "offer.tea" }),
  });

  const persisted = JSON.parse(await readFile(store.path, "utf8"));
  assert.equal(persisted.attempt, null);
  assert.equal(persisted.game.pending_turn, null);
  assert.deepEqual(persisted.game.applied_action_ids, ["offer.tea"]);
  assert.equal(persisted.outbox[0].key, "report.fixture");
});

test("failed settlement publish leaves memory and disk at the retryable Attempt", async () => {
  const store = await temporaryStore();
  await store.ensureSessionId("session.fixture");
  await store.beginRinTurn("tea", 1);
  const attempt = {
    version: 1,
    operation_id: "operation.fixture",
    request: { request_id: "request.fixture", session_id: "session.fixture" },
    job_id: "job.fixture",
  };
  await store.createProposalAttempt(attempt);
  const before = structuredClone(store.document);
  const persistedBefore = JSON.parse(await readFile(store.path, "utf8"));
  const publish = store.publish.bind(store);
  store.publish = async () => {
    throw new Error("injected rename failure");
  };

  await assert.rejects(
    store.settleProposalAttempt({
      attempt,
      report: { request_id: "report.fixture" },
      apply: async () => store.recordRinAction({ id: "offer.tea" }),
    }),
    /injected rename failure/,
  );

  assert.deepEqual(store.document, before);
  assert.deepEqual(JSON.parse(await readFile(store.path, "utf8")), persistedBefore);
  assert.throws(
    () => store.recordRinAction({ id: "offer.tea" }),
    /inside Proposal settlement/,
  );

  store.publish = publish;
  await store.settleProposalAttempt({
    attempt,
    report: { request_id: "report.fixture" },
    apply: async () => store.recordRinAction({ id: "offer.tea" }),
  });
  assert.deepEqual(store.game.applied_action_ids, ["offer.tea"]);
  assert.equal(store.document.outbox.length, 1);
});

test("failed file replacement keeps memory unchanged and removes its temporary file", async () => {
  const directory = await mkdtemp(join(tmpdir(), "rin-story-rename-test-"));
  const blockedTarget = join(directory, "save-target");
  await mkdir(blockedTarget);
  const store = new StoryWorkflowStore(blockedTarget);

  await assert.rejects(store.rememberPreference("tea"));

  assert.equal(store.game.preference, "");
  assert.deepEqual(await readdir(directory), ["save-target"]);
});

test("publication creates and reloads a nested save directory", async () => {
  const directory = await mkdtemp(join(tmpdir(), "rin-story-nested-test-"));
  const store = await new StoryWorkflowStore(
    join(directory, "slot", "chapter", "save.json"),
  ).load();

  await store.rememberPreference("coffee");

  const reloaded = await new StoryWorkflowStore(store.path).load();
  assert.equal(reloaded.game.preference, "coffee");
});

test("Rin presentation runs only after the authoritative settlement commits", async () => {
  const store = await temporaryStore();
  const fixture = successfulStoryClient();

  const result = await runRinStory(fixture.client, store, {
    sessionId: "session.fixture",
    preference: "tea",
    presentAction: async (action) => {
      const committed = await new StoryWorkflowStore(store.path).load();
      assert.deepEqual(committed.game.applied_action_ids, [action.id]);
      assert.equal(committed.document.attempt, null);
      assert.equal(committed.document.outbox.length, 1);
      assert.equal(fixture.reportCalls(), 0);
    },
  });

  assert.equal(result.action.id, "offer.tea");
  assert.equal(fixture.reportCalls(), 1);
  const completed = await new StoryWorkflowStore(store.path).load();
  assert.deepEqual(completed.game.applied_action_ids, ["offer.tea"]);
  assert.deepEqual(completed.document.outbox, []);
});

test("presentation failure leaves an honest durable result pending", async () => {
  const store = await temporaryStore();
  const fixture = successfulStoryClient();

  await assert.rejects(
    runRinStory(fixture.client, store, {
      sessionId: "session.fixture",
      preference: "tea",
      presentAction: async () => {
        throw new Error("injected presentation failure");
      },
    }),
    /injected presentation failure/,
  );

  const reloaded = await new StoryWorkflowStore(store.path).load();
  assert.deepEqual(reloaded.game.applied_action_ids, ["offer.tea"]);
  assert.equal(reloaded.document.attempt, null);
  assert.equal(reloaded.document.outbox.length, 1);
  assert.equal(fixture.reportCalls(), 0);
  const report = reloaded.document.outbox[0].report.report;
  assert.equal(report.event_id, "session.fixture.1.applied");
  assert.match(report.outcome.summary, /durably applied/);
  assert.doesNotMatch(report.outcome.summary, /displayed/);
});

test("a restart reuses the durable Session and pending turn", async () => {
  const store = await temporaryStore();
  assert.equal(await store.ensureSessionId("session.fixture"), "session.fixture");
  assert.deepEqual(await store.beginRinTurn("tea"), { sequence: 1, preference: "tea" });
  const reloaded = await new StoryWorkflowStore(store.path).load();
  assert.equal(await reloaded.ensureSessionId("session.fixture"), "session.fixture");
  assert.deepEqual(
    await reloaded.beginRinTurn("coffee"),
    { sequence: 1, preference: "tea" },
  );
  await assert.rejects(
    reloaded.ensureSessionId("session.other"),
    /already bound/,
  );
  await assert.rejects(
    runRuleTree(reloaded, "coffee", async () => {}),
    /must be reconciled/,
  );
});

test("auto mode uses local content only before any Rin mutation", async () => {
  const store = await temporaryStore();
  const unavailable = new RinClient("http://127.0.0.1:7374", {
    fetch: async () => { throw new Error("offline"); },
  });
  const result = await runStory({
    mode: "auto",
    sessionId: "session.fixture",
    preference: "coffee",
    store,
    client: unavailable,
    presentAction: async () => {},
  });
  assert.equal(result.mode, "local");
  assert.equal(result.action.id, "offer.coffee");
});

test("transport uncertainty after health never silently applies local", async () => {
  const store = await temporaryStore();
  let requests = 0;
  const client = new RinClient("http://127.0.0.1:7374", {
    fetch: async () => {
      requests++;
      if (requests === 1) {
        return response(200, {
          ok: true,
          data: {
            status: "ok",
            protocol_version: "rin.protocol/v2",
            features: [],
            recommended_features: [],
          },
        });
      }
      throw new Error("connection lost");
    },
  });
  await assert.rejects(
    runStory({
      mode: "auto",
      sessionId: "session.fixture",
      preference: "tea",
      store,
      client,
      presentAction: async () => assert.fail("local action must not run"),
    }),
    (error) => error instanceof RinTransportError,
  );
  assert.deepEqual(store.game.applied_action_ids, []);
});

function successfulStoryClient() {
  let proposalRequest;
  let reportCalls = 0;
  const client = new RinClient("http://127.0.0.1:7374", {
    fetch: async (target, options) => {
      const path = new URL(target).pathname;
      const request = options.body ? JSON.parse(options.body) : null;
      if (path === "/health") {
        return response(200, {
          ok: true,
          data: {
            status: "ok",
            protocol_version: "rin.protocol/v2",
            features: [],
            recommended_features: [],
          },
        });
      }
      if (path === "/v2/jobs/propose") {
        proposalRequest = request;
        return response(202, {
          ok: true,
          data: { job_id: "job.fixture", status: "queued", duplicate: false },
        });
      }
      if (path === "/v2/jobs/job.fixture") {
        return response(200, {
          ok: true,
          data: {
            job_id: "job.fixture",
            session_id: proposalRequest.session_id,
            request_id: proposalRequest.request_id,
            status: "succeeded",
            proposal: {
              id: "proposal.fixture",
              session_id: proposalRequest.session_id,
              request_id: proposalRequest.request_id,
              actor_id: proposalRequest.actor_id,
              tick: proposalRequest.tick,
              decision_window: proposalRequest.decision_window,
              action: proposalRequest.offers[0],
              recalled_memory_ids: [],
              policy_source: "deterministic",
            },
          },
        });
      }
      if (path === "/v2/action/report") {
        reportCalls++;
        return response(200, {
          ok: true,
          data: { session_id: request.session_id, duplicate: false },
        });
      }
      return response(200, {
        ok: true,
        data: { session_id: "session.fixture", duplicate: false },
      });
    },
  });
  return { client, reportCalls: () => reportCalls };
}

function response(status, envelope) {
  const bytes = new TextEncoder().encode(JSON.stringify(envelope));
  return {
    status,
    headers: { get: (name) => name.toLowerCase() === "content-length" ? String(bytes.length) : null },
    arrayBuffer: async () => bytes.buffer,
  };
}
