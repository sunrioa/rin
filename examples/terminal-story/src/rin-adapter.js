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
  presentAction,
  sequence,
  ensureSession = true,
}) {
  if (typeof presentAction !== "function") {
    throw new TypeError("presentAction must be a function");
  }
  await store.ensureSessionId(sessionId);
  const turn = await store.beginRinTurn(preference, sequence);
  const prefix = `${sessionId}.${turn.sequence}`;
  const epoch = {
    session_id: sessionId,
    world_id: "last-station",
    host: 1,
    world: 1,
    timeline: 1,
  };
  const observationSeq = turn.sequence;
  const decisionTick = turn.sequence * 2;
  const deadline = { clock: "event", value: decisionTick + 1 };
  const decisionWindow = {
    id: `${prefix}.window`,
    mode: "sequential",
    epoch,
    observation_seq: observationSeq,
    opened_at: { clock: "event", value: decisionTick },
    deadline,
    actor_ids: ["npc.mira"],
  };
  const offers = ACTIONS.map((action) => ({
    offer_id: action.id,
    decision_window_id: decisionWindow.id,
    actor_id: "npc.mira",
    capability: { id: "story.show-line", version: "1.0.0" },
    descriptor_digest: "a".repeat(64),
    description: action.description,
    arguments: { action_id: action.id },
    expected_epoch: epoch,
    observation_seq: observationSeq,
    deadline,
  }));
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
    epoch,
    observation_seq: observationSeq,
  });

  const coordinator = new ProposalAttemptCoordinator(client, store);
  if (!await store.loadProposalAttempt()) {
    await coordinator.begin(`${prefix}.operation`, {
      protocol_version: PROTOCOL_VERSION,
      session_id: sessionId,
      request_id: `${prefix}.propose`,
      actor_id: "npc.mira",
      tick: decisionTick,
      intent: "Offer the player's preferred drink after the time skip.",
      decision_window: decisionWindow,
      offers,
      urgent: true,
    });
  }
  const resolved = await coordinator.resume({ deadlineMs: 5000, intervalMs: 10 });
  const selectedOffer = resolved.proposal.action;
  const action = actionById(selectedOffer.offer_id);
  const operationId = `${prefix}.operation`;
  const report = {
    protocol_version: PROTOCOL_VERSION,
    session_id: sessionId,
    request_id: `${prefix}.report`,
    tick: decisionTick,
    report: {
      proposal_id: resolved.proposal.id,
      event_id: `${prefix}.shown`,
      decision: "accepted",
      invocation: {
        operation_id: operationId,
        offer_id: selectedOffer.offer_id,
        decision_window_id: selectedOffer.decision_window_id,
        actor_id: selectedOffer.actor_id,
        capability: selectedOffer.capability,
        descriptor_digest: selectedOffer.descriptor_digest,
        arguments: selectedOffer.arguments,
        targets: selectedOffer.targets ?? [],
        expected_epoch: selectedOffer.expected_epoch,
        observation_seq: selectedOffer.observation_seq,
        deadline: selectedOffer.deadline,
      },
      run: {
        operation_id: operationId,
        status: "succeeded",
        progress_seq: 1,
        progress: 100,
        updated_at: { clock: "event", value: decisionTick },
      },
      outcome: {
        operation_id: operationId,
        status: "succeeded",
        summary: "The game displayed the selected authored line.",
        epoch,
        world_seq: observationSeq + 1,
        occurred_at: { clock: "event", value: decisionTick },
      },
      summary: "The game displayed the selected authored line.",
    },
  };
  await coordinator.settle(resolved.attempt, resolved.proposal, report, () => {
    store.recordRinAction(action);
  });
  await presentAction(action);
  await outbox.drain();

  return {
    mode: "rin",
    action,
    recalled: resolved.proposal.recalled_memory_ids.length > 0,
    policy_source: resolved.proposal.policy_source,
    provider_cost_usd: 0,
  };
}
