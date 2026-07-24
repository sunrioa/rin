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

**Release status:** `0.6.0` Preview (pre-1.0). The project documents migration
behavior but does not promise compatibility across every future minor release.
Pin an exact repository revision or verified release tag. See the
[changelog](CHANGELOG.md), [compatibility matrix](docs/compatibility.md),
[v0.6 migration guide](docs/migration-v0.6.md), and
[release guide](docs/release-guide.md).

## Core capabilities

Rin separates character reasoning from game-world facts:

- The game submits what a character actually saw as an `Observation` instead
  of handing the model an entire save.
- A character creates an `ActionProposal` from memories, goals, boundaries,
  and the actions currently allowed by the game.
- A proposal cannot directly change plot, inventory, quests, or
  relationships. The game validates and applies or rejects it, then uses
  `commit` to report the actual outcome to Rin.
- Every state change is written to a hash-chained JSONL event log that can be
  replayed and inspected.
- Snapshots bind `game/content/version/hash`. Their SHA-256 canonical
  checksums detect accidental corruption or an unsynchronized edit, while
  Restore rejects a binding mismatch.
- Tick scheduling lets many NPCs think only when needed instead of calling a
  model every frame.
- Asynchronous jobs prefetch online-model results so slow requests,
  cancellation, and stale state never freeze the game thread.
- Generic structured Generation Jobs route plot, quest descriptions, and
  constrained dialogue through the sidecar without storing provider keys in
  the game.
- If a model is unavailable, Rin falls back to a deterministic policy and
  identifies the source with `policy_source`.

The apply-then-report lifecycle and late-outcome merge require new Sessions to
request `outcome-reporting-v1`. Sessions without that Feature retain the
legacy pre-commit/staleness behavior for replay compatibility.

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
| `POST` | `/v1/action/commit` | Record an outcome the game already applied or rejected |
| `POST` | `/v1/action/commit-batch` | Atomically record multi-actor outcomes from one original world revision |
| `POST` | `/v1/session/activity` | Update actor region and awake/dormant state |
| `POST` | `/v1/world/arbitrate` | Deterministically arbitrate conflicting parallel proposals |
| `POST` | `/v1/scheduler/due` | Query actors due to think at the current tick |
| `POST` | `/v1/session/get` | Read session state |
| `POST` | `/v1/session/snapshot` | Create and atomically save a snapshot |
| `POST` | `/v1/session/restore` | Validate and restore a snapshot |
| `POST` | `/v1/session/timeline` | Read the redacted event timeline |
| `POST` | `/v1/session/replay` | Replay to a revision and return a snapshot |

This table is an overview. [`api/openapi.json`](api/openapi.json) is the single
wire-schema source for paths, methods, status codes, required fields, and JSON
shapes. The [protocol reference](docs/protocol-v1.md) defines transaction,
retry, and persistence semantics. Requests reject unknown fields, while clients
must tolerate additive response fields.

Every public JSON integer must be exactly representable from
`-9007199254740991` through `9007199254740991`; fields such as ticks and
revisions have narrower non-negative rules. Commit and every Batch Commit item
must explicitly include `accepted`, including `accepted=false`; omission or
`null` is invalid. Non-2xx failures use the Rin error envelope. A Job lookup can
instead return HTTP `200` with a terminal `data.error`, so HTTP success alone
does not mean the asynchronous operation succeeded.

Every durable Session mutation carries a caller-generated `request_id`.
Within one Session lineage, Rin permanently binds that ID to the mutation kind,
the canonical typed JSON payload, and its first durable result. An exact retry
does not mutate state: it returns the first result's revision/head (or the
original Proposal/Arbitration) with `duplicate=true`. Reusing the ID for a
different operation or payload returns `409 request_id_conflict`. Observe,
Commit, and every Batch item share a permanent, Session-scoped `event_id`
namespace. Bounded State Receipts are only a hot projection of this history.

If Rin cannot confirm whether a non-Proposal mutation reached durable storage,
it returns `mutation_outcome_unknown`. Keep the original operation pending and
retry its exact kind, payload, and IDs; changing the same request returns
`request_id_conflict`, while other Session mutations remain blocked behind the
uncertain tail. Proposal writes retain the compatible
`proposal_outcome_unknown` code and the same recovery rule.

Provider failure inside a live Rin Proposal operation can select the
deterministic Policy. Loss of the Sidecar submit, poll, timeout, or cancellation
result is different: an online Proposal may already exist. Preserve and resume
the exact Proposal Attempt/Job identity, block a new turn, and do not apply an
offline fallback until absence of an online Proposal is confirmed.

Proposal and Generation Job records have separate, bounded in-process
retention. In particular, a Generation request may run again after its Job is
evicted or the sidecar restarts; the durable Session-mutation guarantee does
not apply to Generation Jobs.

Snapshot hashes are checksums, not signatures or provenance proof: someone
who can change a Snapshot can recompute them. Treat each Snapshot as trusted,
opaque state and protect it at the same level as the event log. Restore
requires `expected_binding` from the running game's trusted content manifest;
it must match `snapshot.state.binding` and, when the target Session already
exists, that Session's binding.

Event hashes are also unkeyed SHA-256 values. They validate chain consistency,
not authenticity: a party able to replace a complete history can rebuild the
chain and its derived indexes, checkpoints, and Snapshots. Protect the data
directory and backups with external access controls.

Inline Snapshot compact JSON is limited to 16 MiB. Rin returns
`413 snapshot_too_large` instead of truncating a Snapshot. The server's
default request-body limit and every bundled client's default response limit
are 32 MiB, leaving room for the API envelope, Restore metadata, and durable
EventRecord framing. No streaming Snapshot transport is currently provided,
so a lineage that outgrows the inline limit cannot be exported, replayed, or
restored through these JSON endpoints.

See the [protocol reference](docs/protocol-v1.md) for complete fields and
error semantics, the [architecture guide](docs/architecture.md) for
responsibility boundaries, and [action outcome reporting](docs/outcome-reporting.md)
for application, recording, and retry order.

Inspect one session offline. The command validates the requested recovery
path and prints only a redacted timeline. A healthy revision index lets it
locate the requested trailing window directly rather than paging from genesis:

```bash
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1
go run ./cmd/rin inspect -data ./rin-data -session playthrough-1 -revision 42
```

The bundled file store holds a non-blocking exclusive lock on the data
directory, so stop the sidecar before running `rin inspect` or taking an
uncoordinated filesystem backup. Embedded Go callers must call
`(*store.File).Close()` to release that lock. Engine startup enumerates
Sessions lazily; the first access loads one Session from its newest usable
validated internal checkpoint, or from genesis when none is usable, then
replays its event tail. When lazy recovery used no checkpoint, or when
`head revision / selected checkpoint revision >= 2`, Runtime best-effort
queues an asynchronous checkpoint at the recovered head. It may not be
durable when the read returns, and a cache-write failure does not fail that
read. Call `Engine.VerifyAll()` when maintenance requires a
checkpoint-independent, genesis-to-head audit of every Session.

The bundled `flock` implementation currently supports only `darwin` and
`linux`. On every other GOOS, `store.OpenFile` returns
`ErrDataDirectoryLockUnsupported` and fails closed without returning a usable
File Store.

Use the bundled file store only on a local filesystem with reliable `flock`,
same-directory atomic rename, file `fsync`, and directory `fsync` semantics.
NFS, SMB, FUSE mounts, and cloud-synchronized directories are unsupported;
remote or shared storage requires an externally coordinated Store.

Event logs use `retain_forever` because Replay, durable identifier history,
and audit depend on them. The file store keeps the two newest valid internal
checkpoints and the two newest valid public Snapshot files per Session.
Capacity planning and backups must account for the unbounded event log and
Identifier History; Rin does not provide automatic event-log archival or
streaming Snapshot transport.

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
api/           Authoritative OpenAPI 3.1 wire schema and embedded contract
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
tools/         Deterministic contract projection generator
examples/      Go, Godot, Unity, and Fabric/BepInEx/Luanti mod examples
```

## Scope boundaries

Rin does not own rendering, navigation, physics, combat, inventory, quest
rules, or arbitrary script execution, and it never treats model output as
world fact. The project does not add provider SDKs, a vector database, an ORM,
WebSockets, dynamic plugin execution, or arbitrary file access. Online models
remain optional. Provider failure can use Rin's deterministic Policy, and a
game can use authored offline content when it knows no online Proposal was
created. Sidecar outcome uncertainty is not proof of absence and must remain
fail closed until the exact Attempt is reconciled.

Delivered milestones and remaining Preview work are tracked in
[ROADMAP.en.md](ROADMAP.en.md).

## License

Rin is released under the [MIT License](LICENSE).
