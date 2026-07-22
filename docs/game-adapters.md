# Game Adapters

Rin adapters keep the same authority split on every engine:

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

Threads, cancellation events, HTTP objects, and registries are process-local. Never assign them to `default`, persistent data, rollback state, or a save object. Only store accepted protocol snapshots and plain result dictionaries.

Native Ren'Py tests are offline unless `RIN_LIVE_TEST_ENABLED=1`, even if a developer shell contains a configured endpoint.

## Godot 4

Add [the client](../examples/godot/rin_client.gd) as a node or autoload. `propose_with_fallback` awaits `HTTPRequest` signals and timer ticks, so it does not block rendering. The [NPC example](../examples/godot/example_npc.gd) shows the complete propose, game apply, and commit sequence.

Godot owns navigation, animation, combat, inventory, and dialogue rendering. The adapter caps response bytes, disables redirects, and accepts plaintext HTTP only for an exact loopback host and valid port.

## Unity

Attach [RinClient.cs](../examples/unity/RinClient.cs) to a GameObject. It uses `UnityWebRequest` coroutines and a capped streaming download handler; no JSON or networking package is required. [RinNpcExample.cs](../examples/unity/RinNpcExample.cs) shows the same apply-before-commit flow.

Unity's `JsonUtility` adapter intentionally exposes a compact common schema. Games that use action parameter maps can extend the serializable request classes without changing the wire protocol.

## ai-galgame compatibility

`compat/ai-galgame/vectors.json` is derived from `unsent-letters.rebuild` version `1.2.0`. It covers:

- private-letter permission pressure selecting a local boundary refusal;
- actor-specific observation and belief visibility;
- a goal-directed optional Storylet;
- accepted commit, cooldown scheduling, and premature-proposal rejection.

Verify a local checkout and all content hashes:

```bash
python3 compat/ai-galgame/check_source.py --game-root /path/to/ai-galgame
go test ./compat
```

With a local Sidecar running, exercise the same vectors through the actual Python adapter:

```bash
python3 compat/ai-galgame/run_adapter_smoke.py
```

The vectors contain IDs, contracts, hashes, and short test events, not the game's full copyrighted story text or any provider credential.
