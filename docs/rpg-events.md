# RPG Event Conventions

[English](rpg-events.md) | [简体中文](rpg-events.zh-CN.md)

These conventions let RPGs, simulations, tactics games, and open-area NPC systems use Rin without giving an agent world authority.

## Identity and ticks

- Keep `session_id` bound to one playthrough and one content/mod fingerprint.
- Use stable actor IDs such as `npc.harbor.blacksmith`; never use a display name as identity.
- Advance `tick` on a game-owned clock: turn, minute, schedule slot, or simulation step. Do not use render frames.
- Set `think_every_ticks` by gameplay importance. Distant or unloaded NPCs should sleep instead of polling the model.
- A teleport, reload, rollback, or quest-state rewrite is a new observation/revision and invalidates proposals based on the old head hash.

## Regions and visibility

Region membership belongs to the game. Recommended observation kinds and tags:

| Event | `kind` | Example tags |
| --- | --- | --- |
| Actor enters loaded area | `region-enter` | `region.harbor`, `visibility.direct` |
| Actor leaves loaded area | `region-exit` | `region.harbor` |
| Visible world action | `world-action` | `visibility.direct`, `combat` |
| Heard but unseen event | `sound` | `visibility.heard`, `region.market` |
| Dialogue line | `dialogue` | `conversation`, `speaker.player` |
| Private discovery | `discovery` | `visibility.private`, `quest.relic` |

Only actors in `observer_ids` receive a memory. Proximity alone is not enough: account for walls, stealth, deafness, language, radio channels, cutscenes, and temporary incapacitation before constructing the list.

Facts use their own `visibility` allowlist. This prevents an actor who heard a noise from also receiving the hidden attacker's identity. Never send redacted secret text with a “hidden” tag; omit the fact entirely until it becomes observable.

## Tasks and quests

Quest state remains in the game. Rin may remember bounded facts such as:

```json
{
  "subject_id": "quest.repair-bridge",
  "predicate": "stage",
  "object": "materials-delivered",
  "visibility": ["npc.harbor.foreman"],
  "confidence": 100,
  "source_event_id": "event.quest.repair-bridge.12"
}
```

Use an observation when a task changes, then advertise only actions legal in the current stage. A proposal such as `offer-next-step` is dialogue intent; the game still decides whether the quest advances, rewards are granted, or inventory changes.

Rumors should be facts with lower confidence and a source event. When two actors disagree, keep both observations rather than silently promoting one to world truth.

## Candidate actions

Actions should describe capabilities the game can validate and apply:

- `dialogue`: speak, ask, warn, bargain, refuse;
- `move`: travel to an already reachable target;
- `interact`: use an available object or workstation;
- `combat`: defend, retreat, use an equipped ability;
- `social`: invite, dismiss, request help;
- `wait`, `redirect`, `refuse`: safe non-escalating outcomes.

Put target IDs and bounded parameters in the action spec. Do not advertise an action the current navigation mesh, quest stage, cooldown, inventory, consent state, or combat rules would reject. Rin's whitelist is a security boundary, not merely a prompt hint.

For high-impact actions, advertise an intent such as `request-trade` or `attempt-attack`; the authoritative game system calculates price, hit result, damage, ownership, and consequences after proposal validation.

## Apply and commit

1. Reject a stale proposal if the target moved, died, left visibility, changed faction, or lost required resources.
2. Apply the selected action through normal gameplay systems.
3. Commit the observed outcome, including failure or rejection.
4. Send resulting observations only to actors who perceived them.

Rejected proposals are useful character history. Commit with `accepted=false` when the action was still a valid character intention but the game denied it. Do not commit adapter-local `offline.*` proposals; report their actual outcome later through `observe`.

## Boundaries and player safety

Model-side intent never overrides local rules for consent, harassment, purchases, irreversible quest choices, PvP, account actions, or user-generated content. Put a safe `refuse`, `redirect`, or `wait` action in every request that can trigger a boundary.

An NPC can refuse, misunderstand, delay, or pursue a small goal, but it cannot create a new legal target, reveal unobserved facts, spend currency, or rewrite another actor's state.

## Scaling many actors

- Query `/v1/scheduler/due` on simulation ticks or region activation, not every frame.
- Submit Jobs only for loaded, relevant actors and cap concurrency in the game as well as Rin.
- Batch world events into one concise observation when every listed observer perceived the same outcome.
- Keep important named NPCs at higher frequency; use deterministic policies for crowds.
- Snapshot at game save boundaries. Restore only when the game/content binding matches.

This keeps model cost proportional to meaningful decisions rather than population size or frame rate.
