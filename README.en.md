# Rin

[简体中文](README.md) | [English](README.en.md)

> Game-native agent runtime.

Rin manages character memory, goals, decisions, asynchronous model work, and
verified replay outside the game loop. The game keeps world authority and
receives only locally validated action proposals. Rin can run as a sidecar or
be embedded as a Go package in tooling; its core uses only the Go standard
library and is independent of any specific game, engine, or model provider.

Documentation index: [English](docs/README.md) |
[简体中文](docs/README.zh-CN.md)

## Core capabilities

Rin separates character reasoning from game-world facts:

- The game submits what a character actually saw as an `Observation` instead
  of handing the model an entire save.
- A character creates an `ActionProposal` from memories, goals, boundaries,
  and the actions currently allowed by the game.
- A proposal cannot directly change plot, inventory, quests, or
  relationships. It takes effect only after the game validates it and calls
  `commit`.
- Every state change is written to a hash-chained JSONL event log that can be
  replayed and inspected.
- Snapshots bind `game/content/version/hash`; tampered or mismatched saves are
  rejected.
- Tick scheduling lets many NPCs think only when needed instead of calling a
  model every frame.
- Asynchronous jobs prefetch online-model results so slow requests,
  cancellation, and stale state never freeze the game thread.
- Generic structured Generation Jobs route plot, quest descriptions, and
  constrained dialogue through the sidecar without storing provider keys in
  the game.
- If a model is unavailable, Rin falls back to a deterministic policy and
  identifies the source with `policy_source`.
- Ren'Py, Godot 4, and Unity adapters preserve the same
  observe/propose/commit authority boundary.
- Python, JavaScript, C#, Java, and Lua SDKs plus Fabric, BepInEx, and Luanti
  example mods provide quick integration paths.
- Optional layered memory, conflicting beliefs, candidate subgoals, regional
  dormancy, and deterministic multi-actor arbitration are explicitly enabled
  through session features.
- A redacted timeline, revision replay, and `rin inspect` make long-running
  character behavior reproducible and auditable.

The same boundary works for narrative characters, RPG NPCs, companions,
simulation residents, and server-side entities.

## Quick start

Running the sidecar requires Go 1.24 or later. Ren'Py adapter tests also
require Python 3.9+.

```bash
make test
go run ./cmd/rin serve -data ./rin-data
```

The default listener is `127.0.0.1:7374`. Check the service with:

```bash
curl http://127.0.0.1:7374/health
```

Run the complete client example:

```bash
go run ./examples/basic
```

Production integrations should use a dedicated sidecar token:

```bash
export RIN_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/rin serve
```

The client then sends `Authorization: Bearer $RIN_TOKEN`. Tokens, model API
keys, and provider URLs are never written to events, snapshots, or responses.
Generation results may contain only bounded, non-secret operational metadata
such as model name, finish reason, and token counts; games may apply an
additional persistence allowlist.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Unauthenticated health check |
| `POST` | `/v1/session/create` | Create a session bound to a game-content version |
| `POST` | `/v1/session/observe` | Submit events actually observed by one or more actors |
| `POST` | `/v1/agent/propose` | Produce a character proposal from game-allowlisted actions |
| `POST` | `/v1/jobs/propose` | Submit an asynchronous proposal job |
| `GET` | `/v1/jobs/{job_id}` | Read proposal-job status and result |
| `DELETE` | `/v1/jobs/{job_id}` | Cancel a queued or running proposal job |
| `POST` | `/v1/generation/jobs` | Submit an asynchronous structured JSON generation job |
| `GET` | `/v1/generation/jobs/{job_id}` | Read a generation job and safe metadata |
| `DELETE` | `/v1/generation/jobs/{job_id}` | Cancel a generation job |
| `POST` | `/v1/action/commit` | Accept or reject a proposal and record its outcome |
| `POST` | `/v1/action/commit-batch` | Atomically commit multi-actor outcomes at one world revision |
| `POST` | `/v1/session/activity` | Update actor region and awake/dormant state |
| `POST` | `/v1/world/arbitrate` | Deterministically arbitrate conflicting parallel proposals |
| `POST` | `/v1/scheduler/due` | Query actors due to think at the current tick |
| `POST` | `/v1/session/get` | Read session state |
| `POST` | `/v1/session/snapshot` | Create and atomically save a snapshot |
| `POST` | `/v1/session/restore` | Validate and restore a snapshot |
| `POST` | `/v1/session/timeline` | Read the redacted event timeline |
| `POST` | `/v1/session/replay` | Replay to a revision and return a snapshot |

Every write request carries a caller-generated `request_id`. Repeating a
request returns the same result without mutating state again. Reusing the same
ID for another operation returns a conflict.

See the [protocol reference](docs/protocol-v1.md) for complete fields and
error semantics, and the [architecture guide](docs/architecture.md) for
responsibility boundaries.

Inspect a session offline. The command verifies the log and prints only a
redacted timeline:

```bash
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1 -revision 42
```

## Game-engine adapters

- Ren'Py: standard-library Python client, `renpy.invoke_in_thread` bridge, and
  authored offline fallback.
- Godot 4: asynchronous `HTTPRequest` signal/timer example.
- Unity: asynchronous `UnityWebRequest` coroutine with bounded response
  handling.
- General SDKs: Python 3.9+, Node/Fetch, .NET 6+, Java 17+, and Lua 5.1+.
- Example mods: Fabric server, BepInEx 6, and a loopback-sidecar-only Luanti
  server mod.

See [game adapters](docs/game-adapters.md) for installation, configuration,
and offline semantics. RPG region, visibility, quest, and multi-NPC event
conventions are in [RPG event conventions](docs/rpg-events.md).
Cross-language structure, thread boundaries, credential policy, and mod
installation are covered by [SDK and mod integration kits](docs/sdk-and-mods.md).

## Optional model policy

Rin makes no network calls by default. Enable an OpenAI-compatible model with:

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="https://provider.example/v1"
export RIN_MODEL="your-model-id"
export RIN_MODEL_API_KEY="..."
go run ./cmd/rin serve
```

Remote endpoints must use HTTPS. Models on `127.0.0.1`, `::1`, or `localhost`
may use HTTP without a key. Model calls have independent timeouts, a total
budget, bounded retries, a circuit breaker, and a bounded cache. See
[model policy](docs/model-policy.md) for details.

## Repository layout

```text
cmd/rin/       Sidecar command-line program
httpapi/       Strict JSON, authentication, and request-size limits
policy/        Deterministic offline policy with no network dependency
provider/      OpenAI-compatible client, retries, and circuit breaker
jobs/          Bounded asynchronous proposal worker queue
generation/    Bounded structured-generation worker queue and cache
adapters/      Ren'Py Python client and bridge
sdk/           Python, JavaScript, C#, Java, and Lua clients and route contract
compat/        Executable game-protocol compatibility vectors
protocol/      Cross-language v1 data contract
runtime/       Event state machine, proposal validation, snapshots, scheduling
store/         JSONL file store and in-memory store
examples/      Go, Godot, Unity, and Fabric/BepInEx/Luanti mod examples
```

## Scope boundaries

Rin does not own rendering, navigation, physics, combat, inventory, quest
rules, or arbitrary script execution, and it never treats model output as
world fact. The project does not add provider SDKs, a vector database, an ORM,
WebSockets, dynamic plugin execution, or arbitrary file access. Online models
remain optional; if either the provider or sidecar is unavailable, a game can
continue with the deterministic policy or its own offline content.

Future work is tracked in [ROADMAP.en.md](ROADMAP.en.md).

## License

Rin is released under the [MIT License](LICENSE).
