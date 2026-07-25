import assert from "node:assert/strict";
import { mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { RinClient, RinTransportError } from "@sunrioa/rin-sdk";
import { runRuleTree } from "../src/baseline.js";
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
  assert.deepEqual(reloaded.game.shown_action_ids, ["offer.tea"]);
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
    commit: { request_id: "commit.fixture" },
    apply: async () => store.applyRinAction({ id: "offer.tea" }),
  });

  const persisted = JSON.parse(await readFile(store.path, "utf8"));
  assert.equal(persisted.attempt, null);
  assert.equal(persisted.game.pending_turn, null);
  assert.deepEqual(persisted.game.shown_action_ids, ["offer.tea"]);
  assert.equal(persisted.outbox[0].key, "commit.fixture");
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

test("auto mode falls back only when startup health has no transport", async () => {
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
    applyAction: async () => {},
  });
  assert.equal(result.mode, "fallback");
  assert.equal(result.action.id, "offer.coffee");
});

test("transport uncertainty after health never silently applies fallback", async () => {
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
            protocol_version: "rin.protocol/v1",
            features: ["outcome-reporting-v1"],
            recommended_features: ["outcome-reporting-v1"],
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
      applyAction: async () => assert.fail("fallback action must not run"),
    }),
    (error) => error instanceof RinTransportError,
  );
  assert.deepEqual(store.game.shown_action_ids, []);
});

function response(status, envelope) {
  const bytes = new TextEncoder().encode(JSON.stringify(envelope));
  return {
    status,
    headers: { get: (name) => name.toLowerCase() === "content-length" ? String(bytes.length) : null },
    arrayBuffer: async () => bytes.buffer,
  };
}
