import {
  FEATURE_PRESETS,
  OutcomeOutbox,
  ProposalAttemptCoordinator,
} from "@sunrioa/rin-sdk";
import {
  ACTIONS,
  PROTOCOL_VERSION,
  STORY_BINDING,
  actionById,
} from "./catalog.js";

export async function runRinStory(client, store, {
  sessionId,
  preference,
  applyAction,
  sequence,
  ensureSession = true,
}) {
  await store.ensureSessionId(sessionId);
  const turn = await store.beginRinTurn(preference, sequence);
  const prefix = `${sessionId}.${turn.sequence}`;
  const outbox = new OutcomeOutbox(client, store);
  await outbox.drain();

  if (ensureSession) {
    await client.negotiateCapabilities(FEATURE_PRESETS.safeBaseline);
    await client.createSession({
      protocol_version: PROTOCOL_VERSION,
      request_id: `${sessionId}.create`,
      session_id: sessionId,
      binding: STORY_BINDING,
      seed: 17,
      features: FEATURE_PRESETS.safeBaseline,
      actors: [{
        id: "npc.mira",
        kind: "npc",
        display_name: "Mira",
        think_every_ticks: 5,
        enabled: true,
      }],
    });
  }
  await client.observe({
    protocol_version: PROTOCOL_VERSION,
    session_id: sessionId,
    request_id: `${prefix}.observe`,
    event_id: `${prefix}.preference`,
    tick: turn.sequence * 2 - 1,
    observer_ids: ["npc.mira"],
    source: "player",
    kind: "preference",
    summary: `The player chose ${turn.preference}.`,
    tags: [`preference.${turn.preference}`],
    importance: 4,
  });

  const coordinator = new ProposalAttemptCoordinator(client, store);
  if (!await store.loadProposalAttempt()) {
    await coordinator.begin(`${prefix}.operation`, {
      protocol_version: PROTOCOL_VERSION,
      session_id: sessionId,
      request_id: `${prefix}.propose`,
      actor_id: "npc.mira",
      tick: turn.sequence * 2,
      intent: "Offer the player's preferred drink after the time skip.",
      candidate_actions: ACTIONS,
      urgent: true,
    });
  }
  const resolved = await coordinator.resume({ deadlineMs: 5000, intervalMs: 10 });
  const action = actionById(resolved.proposal.action.id);
  const commit = {
    protocol_version: PROTOCOL_VERSION,
    session_id: sessionId,
    request_id: `${prefix}.commit`,
    proposal_id: resolved.proposal.id,
    event_id: `${prefix}.shown`,
    tick: turn.sequence * 2,
    accepted: true,
    outcome: "The game displayed the selected authored line.",
  };
  await coordinator.settle(resolved.attempt, resolved.proposal, commit, async () => {
    store.applyRinAction(action);
    await applyAction(action);
  });
  await outbox.drain();

  return {
    mode: "rin",
    action,
    recalled: resolved.proposal.recalled_memory_ids.length > 0,
    policy_source: resolved.proposal.policy_source,
    provider_cost_usd: 0,
  };
}
