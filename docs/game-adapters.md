# Game Adapters

[English](game-adapters.md) | [简体中文](game-adapters.zh-CN.md)

Adapters translate engine lifecycle events into the stable Rin protocol while
keeping the same authority split:

1. The game sends only events an actor actually observed.
2. The game advertises a bounded list of actions that are currently legal.
3. Rin returns a proposal; it does not move a character or mutate the world.
4. The game applies its own rules and commits the accepted or rejected outcome.

An adapter result adds two local fields around the protocol proposal:

- `committable=true`: the proposal came from the current Sidecar session and
  its result may be sent to `/v1/action/commit` after the game handles it. This
  is not authorization from Rin to execute it.
- `committable=false`: there is no Rin Proposal that can be committed. Apply a
  local fallback only when `source=offline` and a proposal is present. A
  canceled/error result has no action to apply. Never send an `offline.*` ID
  to Rin; after recovery, report an applied fallback through `observe`.

Timeout or a lost submit/poll/cancel response is outcome-unknown, not offline:
retry the same request/job identity and do not choose a fallback until the
absence of an online Proposal is confirmed.

Persist a Proposal Attempt before the first submit. It contains the complete
byte-equivalent Propose request, its game operation/sequence identity, and the
Job ID as soon as `202` supplies one. A later interaction must resume that
attempt instead of creating a new request. Remove it only in the authoritative
transaction that applies or rejects the returned Proposal and stores the
applied marker plus Outcome Outbox entry. Both an unresolved Proposal Attempt
and a nonempty Outcome Outbox block new turns.

For new Sessions, request `outcome-reporting-v1`; only then does Commit record
an already handled outcome rather than use the legacy pre-commit semantics.
The game should apply or reject the action and write a local Outcome Outbox
entry in one authoritative transaction, then report from that Outbox to Rin.
On a network failure, retry only the same `request_id` and never apply the
action again. See [action outcome reporting](outcome-reporting.md).

## Ren'Py

Copy these files into the game's `game/` directory:

```text
adapters/renpy/rin_client.py
adapters/renpy/rin_bridge.rpy
```

The client uses only Python's standard library. Enable it explicitly:

```bash
export RIN_ENABLED=1
export RIN_BASE_URL="http://127.0.0.1:7374"
```

For a remote TLS reverse proxy, set `RIN_TOKEN`; non-loopback HTTP and tokenless remote endpoints are rejected. Optional settings:

| Variable | Default | Meaning |
| --- | --- | --- |
| `RIN_TIMEOUT_SECONDS` | `5` | One adapter HTTP request |
| `RIN_JOB_DEADLINE_SECONDS` | `25` | Total async proposal wait |
| `RIN_POLL_INTERVAL_SECONDS` | `0.1` | Job polling interval |
| `RIN_LIVE_TEST_ENABLED` | `0` | Explicitly allow transport during Ren'Py native tests |

Schedule from script code, keep rendering, then consume from a timer or call screen:

```python
request_id = rin_schedule_proposal({
    "protocol_version": "rin.protocol/v1",
    "session_id": "playthrough-1",
    "request_id": "propose.scene-12.lin",
    "actor_id": "npc.lin",
    "tick": 12,
    "intent": "Choose how to answer.",
    "tags": ["conversation"],
    "candidate_actions": [
        {"id": "respond.honest", "kind": "dialogue", "description": "Answer honestly."},
        {"id": "respond.wait", "kind": "wait", "description": "Wait for now."},
    ],
}, fallback_action_id="respond.wait")
```

`rin_proposal_status(request_id)` returns `pending`, `ready`, `unresolved`, or `missing`; `rin_consume_proposal(request_id)` returns one plain JSON-compatible result only after a safe terminal outcome. For `pending` or `unresolved`, persist the plain record from `rin_proposal_attempt(request_id)` with the game save. After restart, pass that record to `rin_resume_proposal`; it recovers a known Job first and permits at most one exact same-request POST when Rin confirms that Job is absent. An unresolved attempt is neither consumable nor locally cancelable. `rin_cancel_proposal` propagates cancellation to the Job API for a running process-local worker.

The Python client also exposes `commit_batch`, `set_actor_activity`, `arbitrate`, `timeline`, `replay`, and the structured-generation methods. Generation must run in the same process-local background pattern as proposals. `generate_json` accepts only the provider-free Rin request contract and returns one decoded JSON object plus bounded operational metadata. A game that persists request records should allowlist only the fields it needs; provider model names are useful for explicit probes but should not be copied into gameplay saves.

Threads, cancellation events, HTTP objects, and registries are process-local. Never assign them to `default`, persistent data, rollback state, or a save object. Store only accepted protocol snapshots, plain result dictionaries, and the plain stable Proposal Attempt records described above.

Native Ren'Py tests are offline unless `RIN_LIVE_TEST_ENABLED=1`, even if a developer shell contains a configured endpoint.

## Godot 4

Add [the client](../examples/godot/rin_client.gd) as a node or autoload. `propose_with_fallback` awaits `HTTPRequest` signals and timer ticks, so it does not block rendering. The [NPC example](../examples/godot/example_npc.gd) shows the propose, game-application, and outcome-report sequence. Its storage methods are deliberate integration hooks, not an in-memory persistence implementation: replace `_load_authoritative_state`, initialization, Attempt, transaction, conversion, and acknowledgement hooks with the game's save system. Until the load hook reports either one valid state or a positively confirmed `not_found`, the example disables turns and performs no Rin request.

Restore the run ID, stable Create request, operation sequence, protocol-tick high-water mark, complete Proposal Attempt, applied markers, and report Outbox as one authoritative game state before enabling play. The high-water mark prevents a reset engine frame counter from producing `tick_regressed` after restart. An I/O, parse, or schema error is not `not_found`; fail closed rather than minting a new identity. On a real `not_found`, persist the complete initialized state before publishing its new run ID.

Every Restore must send `expected_binding` from the running game's trusted
content manifest, not from the save. It must equal the saved Snapshot binding
and, when a target Session exists, that Session's binding. Treat the Snapshot
as trusted, opaque state under the same protection as the event log. Its
SHA-256 canonical checksums detect accidental corruption, not provenance or a
party able to recompute them. Complete inline Snapshot compact JSON is capped
at 16 MiB; the server request and bundled-client response defaults are 32 MiB.
`413 snapshot_too_large` never truncates the save—a larger lineage waits for
the planned Step 5 streaming transport.

Godot owns navigation, animation, combat, inventory, and dialogue rendering. Helpers for activity, due actors, arbitration, batch commit, timeline, and replay are coroutines; call activity on simulation/region changes, not every frame. The adapter caps response bytes, disables redirects, and accepts plaintext HTTP only for an exact loopback host and valid port.

## Unity

Attach [RinClient.cs](../examples/unity/RinClient.cs) to a GameObject. It uses `UnityWebRequest` coroutines and a capped streaming download handler; no JSON or networking package is required. [RinNpcExample.cs](../examples/unity/RinNpcExample.cs) shows the same apply-before-report flow and the same startup recovery gate. Wire its `LoadAuthoritativeState` and persistence methods to the game's save provider; the unconfigured example intentionally remains disabled instead of treating a storage failure as a new playthrough. A restored Unity state must carry the same run ID, stable Create request, sequence, tick high-water, Proposal Attempt, applied markers, and Outcome Outbox described above.

Unity's `JsonUtility` adapter exposes serializable DTOs for activity, scheduling, arbitration, batch commit, and timeline. Since `JsonUtility` cannot represent actor-ID keyed maps, its Replay helper returns the verified Snapshot header; projects that need the complete replayed state should parse the same endpoint with their existing dictionary-capable JSON package. Games that use action parameter maps can likewise extend the serializable request classes without changing the wire protocol.

## Compatibility fixtures

The executable fixtures under `compat/` exercise a complete consumer flow
without making that consumer part of Rin's public identity. They cover:

- permission pressure selecting a local boundary refusal;
- actor-specific observation and belief visibility;
- goal-directed optional content;
- accepted commit, cooldown scheduling, and premature-proposal rejection.

The reference flow combines:

- content binding and one Rin Session per game run;
- canonical game events as actor-scoped Observations;
- free player text as an explicit Observation;
- candidate-only character directions before structured content generation;
- accepted direction Commit followed by Snapshot storage in the game save;
- Restore `expected_binding` sourced from the running trusted content manifest;
- restore IDs derived from both the saved Snapshot and current Sidecar head;
- deterministic authored fallback whenever Sidecar generation is unavailable.

Run the public compatibility suite with:

```bash
go test ./compat
```

Fixtures contain IDs, contracts, hashes, and short test events, not a
consumer's full content or any provider credential. Consumer-specific source
verification may live beside a fixture, but it is not part of the public
protocol contract.
