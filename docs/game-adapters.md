# Game Adapters

[English](game-adapters.md) | [简体中文](game-adapters.zh-CN.md)

Adapters translate engine lifecycle events into the stable Rin protocol while
keeping the same authority split:

1. The game sends only events an actor actually observed.
2. The game advertises a bounded list of actions that are currently legal.
3. Rin returns a proposal; it does not move a character or mutate the world.
4. The game applies its own rules and commits the accepted or rejected outcome.

An adapter result adds two local fields around the protocol proposal:

- `committable=true`: the proposal came from the current Sidecar session and may be sent to `/v1/action/commit` after the game applies it.
- `committable=false`: the game used its authored offline fallback. Apply it locally, but do not send its `offline.*` ID to Rin. When the Sidecar recovers, report the resulting event through `observe`.

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

`rin_proposal_status(request_id)` returns `pending`, `ready`, or `missing`; `rin_consume_proposal(request_id)` returns one plain JSON-compatible result. `rin_cancel_proposal` propagates cancellation to the Job API.

The Python client also exposes `commit_batch`, `set_actor_activity`, `arbitrate`, `timeline`, `replay`, and the structured-generation methods. Generation must run in the same process-local background pattern as proposals. `generate_json` accepts only the provider-free Rin request contract and returns one decoded JSON object plus bounded operational metadata. A game that persists request records should allowlist only the fields it needs; provider model names are useful for explicit probes but should not be copied into gameplay saves.

Threads, cancellation events, HTTP objects, and registries are process-local. Never assign them to `default`, persistent data, rollback state, or a save object. Only store accepted protocol snapshots and plain result dictionaries.

Native Ren'Py tests are offline unless `RIN_LIVE_TEST_ENABLED=1`, even if a developer shell contains a configured endpoint.

## Godot 4

Add [the client](../examples/godot/rin_client.gd) as a node or autoload. `propose_with_fallback` awaits `HTTPRequest` signals and timer ticks, so it does not block rendering. The [NPC example](../examples/godot/example_npc.gd) shows the complete propose, game apply, and commit sequence.

Godot owns navigation, animation, combat, inventory, and dialogue rendering. Helpers for activity, due actors, arbitration, batch commit, timeline, and replay are coroutines; call activity on simulation/region changes, not every frame. The adapter caps response bytes, disables redirects, and accepts plaintext HTTP only for an exact loopback host and valid port.

## Unity

Attach [RinClient.cs](../examples/unity/RinClient.cs) to a GameObject. It uses `UnityWebRequest` coroutines and a capped streaming download handler; no JSON or networking package is required. [RinNpcExample.cs](../examples/unity/RinNpcExample.cs) shows the same apply-before-commit flow.

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
