# SDK and mod integration kits

[English](sdk-and-mods.md) | [简体中文](sdk-and-mods.zh-CN.md)

Thin integration kits connect game-owned adapters to Rin without moving world
authority into the sidecar or a model. They remove repetitive HTTP, timeout,
envelope, and job-polling code.

Every integration must declare its real
[host durability profile](host-durability.md). Calling the correct
Rin endpoints does not make a host crash-safe: stronger profiles require a
durable-before-network boundary plus either operation-keyed idempotent apply or
an actual game transaction. The checked-in host examples already persist
stable identity and bounded recovery state, but remain `advisory` because their
storage cannot be made crash-atomic with an arbitrary game-world effect.

This document describes Rin `0.7.0` Preview. The authoritative wire schema is
[`api/openapi.json`](../api/openapi.json).

## Support matrix

| Language | Minimum runtime | Delivery model | JSON boundary | Typical host |
| --- | --- | --- | --- | --- |
| Python | 3.9 | synchronous | standard library | Ren'Py, tools, servers |
| JavaScript | Node 18 / Fetch host | Promise | built in | Electron, web bridges, Node |
| C# | .NET Standard 2.0 / .NET 6 | Task | `System.Text.Json` | BepInEx 6 Mono / IL2CPP, modern .NET games |
| Java | 17 | `CompletableFuture` | injected `JsonCodec` | Fabric, JVM servers |
| Lua | 5.1 | callback | injected codec and transport | Luanti, embedded Lua engines |

Every implementation exposes the 20-route inventory generated into
[`sdk/conformance/routes.json`](../sdk/conformance/routes.json) from OpenAPI.
That inventory checks coverage; it is not a second wire contract or behavior
proof. Python and
JavaScript have no runtime dependencies. C# uses framework APIs on .NET 6;
its .NET Standard 2.0 compatibility target pins `System.Text.Json`. Java
reuses the host's JSON library through a two-method codec. Lua injects all
host-specific services because Lua engines expose incompatible HTTP and JSON
APIs.

## Directory contract

```text
sdk/
  conformance/       language-neutral route inventory
  <language>/        source, language README, tests, optional quickstart
examples/mods/
  fabric-rin-npc/    pinned, buildable Fabric server Mod
  bepinex-rin-npc/   pinned BepInEx 6 Mono and IL2CPP projects
  luanti-rin-npc/    complete server mod with vendored Lua SDK
```

The SDKs are source-first and are not published to language registries. Vendor
the complete client directory from one exact Rin repository revision or
verified release tag. Do not copy a single client file without its README and
generated conformance inventory.

For a new Mod, prefer the offline generator so the host project, complete SDK,
license notice, pinned dependencies, and SHA-256 inventory stay synchronized:

```bash
rin init host --list-hosts
rin init host --engine fabric --id guide_npc \
  --name "Guide NPC" --namespace io.github.example
```

The exact command contract, Windows path rules, and build commands are in the
[Host scaffolding guide](host-scaffolding.md). Generation and compilation do not
replace the [real-host validation matrix](host-integration-validation.md).

## Integration lifecycle

1. Capture a bounded game-owned event and call `observe`.
2. Build an Epoch, Decision Window, and only fully bound Offers the game can
   safely implement.
3. Use the asynchronous Proposal Job API from real-time games.
4. Match the returned Offer to the durable host-authored request.
5. Marshal to the engine's owning thread and apply the action.
6. Persist the actual result in the game's Outcome Outbox as part of the apply
   transaction.
7. Call `reportAction` from the Outbox, including a rejection when needed. Retry a
   failed report without applying the action again.

Treat an ambiguous Proposal submit, poll, timeout, or cancellation as
outcome-unknown and fail closed. Retry the same identity; the transport layer
must never invent a replacement action. Persist the complete Propose request
and operation identity before submission, then persist the Job ID immediately
after `202`. Resume that record before any new turn. Clear it only in the same
authoritative transaction that stores the game result, applied marker, and
Outcome Outbox entry.

The JavaScript/TypeScript and C# SDKs retain their lower-level
`ProposalAttemptCoordinator`, pluggable `OutcomeOutbox`, and opaque Snapshot
persistence helpers. JavaScript/TypeScript, C#, and Java also expose a
higher-level `WorkflowCoordinator` that validates the declared Host Capability
Profile before applying an action. They deliberately define storage contracts
instead of shipping an in-memory production default. The Java and C#
coordinators also JSON-semantically bind the returned Proposal actor, tick,
Decision Window, and
complete Action to an original Offer in the durable request; an Offer ID alone
is never authority. Equivalent JSON numbers remain equal in Java even when a
codec changes the concrete `Number` type. A transactional settlement
hook must apply the game effect, persist the applied marker and exact Action Report,
and remove the Pending Turn atomically. Idempotent hosts instead receive the
stable operation ID before the Store completes the report transaction. Outbox
drain acknowledges only a normal success or Rin's explicit exact-duplicate
success. All errors leave the exact Action Report entry intact; reports are
never converted into Observations.
`ProposalFreshness` centralizes the final pending/revision check.

Provider failure inside Rin may use its deterministic Policy before a Proposal
is issued. Sidecar submit/poll/cancel uncertainty is different and must not be
converted into a host action.

Never call online proposal or generation endpoints from a render/update loop.
One player interaction may start one job; ordinary frames should only poll a
local future, coroutine, timer, or main-thread queue.

Action Report records an outcome rather than authorizing execution. See
[action outcome reporting](action-lifecycle.md) for Outbox, late-outcome,
same-`request_id` retry, and offline reconciliation rules.

All public JSON integers must remain in the exact interoperable range
`-9007199254740991` through `9007199254740991`. Each report and Batch item must
serialize `accepted` explicitly, including `false`. SDKs must encode UTF-8
request bodies, reject unsafe integers and non-JSON local values before
transport, and tolerate additive response members. The OpenAPI-backed server
remains the request-schema authority and rejects unknown members in closed
request objects; callers must not treat the SDKs' generic map/object boundary
as complete local schema validation. SDKs must also distinguish a non-2xx Rin
error envelope from an HTTP-200 terminal Job carrying `data.error`.

## Credentials and transport

- Keep model-provider credentials in the Rin sidecar only.
- A game may hold `RIN_TOKEN`, which authenticates the game to Rin; it is not a
  provider API key and must not be written to saves, logs, or mod configs.
- SDKs accept plaintext HTTP only for loopback. Remote Rin origins require
  HTTPS and a token.
- Redirects are rejected, responses are size-limited, and user-visible errors
  contain bounded Rin codes rather than provider bodies.
- Bundled clients default to a 32 MiB response limit. Complete inline Snapshot
  compact JSON is capped at 16 MiB and is rejected with
  `413 snapshot_too_large`, never truncated. JavaScript and C# provide
  bounded Session Transfer streams for complete large-lineage migration;
  the other packages remain JSON transport clients.
- Restore callers source mandatory `expected_binding` from the running trusted
  content manifest, not from the imported Snapshot.
- A Snapshot is trusted, opaque event-log-level state. Its SHA-256 canonical
  checksums detect accidental damage, but neither authenticate provenance nor
  stop a party that can recompute them.
- Priority SDK opaque Snapshot helpers persist the complete bounded JSON object
  rather than projecting through a lossy typed State model, so additive members
  survive a save/load/Restore cycle.
- Treat generated dialogue as display data. Never parse it as a console
  command, reflection target, script name, item ID, or filesystem path.

Luanti is a documented exception: its engine HTTP implementation follows up
to three redirects and the mod API has no per-request opt-out. The example is
therefore loopback-only and refuses Authorization headers. Use a native bridge
before supporting authenticated remote Rin from Luanti.

## Example mods

The Fabric reference is a complete Gradle project pinned to Minecraft 1.21.1
and Java 21. Its common initializer loads for integrated and dedicated logical
servers. It embeds the source-first Java SDK, stores stable world/session
identity and bounded workflow state in Saved Data, binds a fresh Host
generation at `SERVER_STARTED`, and gates all game work on the owning server
thread. The build runs an official dedicated-server GameTest. It remains
`advisory` because `markDirty()` is eventual rather than a durable transaction
boundary.

The Unity UPM reference keeps a stable playthrough/content binding while each
managed-domain lifetime advances Host/Timeline and each scene replacement
advances World. It persists raw authored arguments and an Active Run before
calling game code. Its cancellable action gate ignores callbacks from replaced
authority and reconciles an interrupted run as `cancelled` or
`outcome-unknown`. The NavMesh example maps a sealed movement offer to a
game-owned destination; it is not a visual-control or arbitrary-coordinate
interface. Strict stubs exercise this lifecycle on Linux and Windows, but an
Editor import and Mono/IL2CPP Players remain real-host gates.

The BepInEx reference pins the upstream bleeding-edge, unreleased BepInEx 6
build `6.0.0-be.785` and separates Unity Mono
(`netstandard2.0`) from IL2CPP (`net6.0`). Shared Core code persists a stable
save identity, Pending Turn, Job ID, and bounded Outcome Outbox. Mono provides
a bounded Unity main-thread queue and optional F8 demo. IL2CPP deliberately
requires a game-specific interop hook because generated assemblies are not
portable between games. `python tools/package_bepinex.py` builds deterministic
Windows-safe install ZIPs and rejects packages that accidentally contain
BepInEx, Unity, or game interop assemblies.
The F8 slice also demonstrates a game-owned two-step beacon quest. Its stage
and operation marker survive restart, invalid transitions are rejected, and
the stage is included in later Observations so memory can affect the next plan.

The Luanti example is a complete server mod. It calls
`core.request_http_api()` at module scope, keeps the returned API local, and
requires `secure.http_mods = rin_npc_example`. Its ModStorage adapter retains a
host-supplied content binding, stable world/Session identity, Host/World/Timeline
generations, complete Pending Turn, Job ID, monotonic tick floor, Active Run,
and bounded Outcome Outbox across restart. The Lua SDK Workflow owns
submit/poll recovery, complete Decision Window/Offer binding, exact report
retry, and ACK-before-eviction. It persists an accepted Active Run before game
code and reconciles an interrupted run once as `outcome-unknown`, never as a
blind replay. The profile remains `advisory` because ModStorage save timing and
an arbitrary game effect do not form one synchronous transaction.
The checked-in all-zero content hash is a scaffold placeholder, not a trusted
manifest digest.

Luanti encodes both an empty Lua object and empty Lua array as JSON `null`.
Outgoing SDK requests therefore reject ambiguous empty tables. Omit optional
empty arrays and include at least one host-authored field in action arguments.

The Godot 4.6.3 reference is a runnable project. Its reusable Workflow stores a
stable save-slot identity, Host/World/Timeline generations, complete Pending
Turn, Job ID, Active Run, tick high-water, and bounded Outcome Outbox under
`user://`. It exactly binds resolved Proposals to durable Decision Windows and
Offers and recovers an uncertain action as `outcome-unknown`. Official Godot
binaries are SHA-256 pinned from GitHub Release metadata for headless lifecycle
tests on Linux and Windows.

The Unity package declares a `2021.3` minimum API level. Its coroutine
Workflow owns restartable Pending Turn, Active Run, exact Offer binding,
authority generations, settlement, and Outbox state under
`Application.persistentDataPath`; the game-facing example remains under 100
lines. A .NET harness compiles the package against Unity API stubs and
exercises lifecycle and file recovery on Linux and Windows. Until the
[real-host validation matrix](host-integration-validation.md) is executed, this
does not prove Editor import or Player compatibility with 2021.3 or later
Unity releases.

## Verification

```bash
make test
make test-sdks
python3 tools/generate_contract.py --check
```

CI executes Go formatting, vet, race tests, plus zero-CGO builds and File
Store/Sidecar lifecycle tests on Linux, macOS, and Windows. The platform tests
cover persistence, restart, and rejection of a second writer. CI runs the
Python SDK and Ren'Py adapter on Python 3.9 and the current Python 3 release,
the Lua SDK and Luanti restart/write-failure state harness on Lua 5.1 and 5.4,
and the same suites inside official Luanti 5.16.1 LuaJIT. A real Dedicated
Server reopens the source Mod and a generated scaffold on macOS, while Windows
CI repeats both lifecycles from a SHA-256-pinned official release ZIP.
CI also runs JavaScript on Node 18 and 24, Java on 17 and 25, C# against .NET
6 and 10, and the pinned Fabric project and its Saved Data restart test. Both BepInEx
backends, restart tests, and install packages build on Linux and Windows. The
contract generator check prevents drift from OpenAPI to generated route/version
projections.

The SDK tests invoke real client methods against local fake transports or HTTP
test servers and assert method/path selection, a nonempty UTF-8 JSON body,
Bearer/User-Agent headers, success-envelope data, and API status/code/field
mapping. They are not end-to-end tests against a live Sidecar. The generated
route manifest is compared with `httpapi.ContractRoutes()` for route drift;
remaining Go source-marker checks are static regression lints only. The
presence of a marker or method name does not prove runtime transport behavior.

## Primary references

- [Fabric example mod (CC0)](https://github.com/FabricMC/fabric-example-mod)
- [Fabric project structure](https://docs.fabricmc.net/develop/getting-started/project-structure)
- [BepInEx plugin tutorial](https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/index.html)
- [BepInEx configuration](https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/4_configuration.html)
- [Java 17 HttpClient](https://docs.oracle.com/en/java/javase/17/docs/api/java.net.http/java/net/http/HttpClient.html)
- [.NET HttpClient JSON extensions](https://learn.microsoft.com/en-us/dotnet/api/system.net.http.json)
- [`System.Text.Json` supported types](https://learn.microsoft.com/en-us/dotnet/standard/serialization/system-text-json/supported-types)
- [Luanti HTTP API](https://docs.luanti.org/for-creators/api/http-api/)
- [Luanti Core API](https://api.luanti.org/core-namespace-reference/)

The examples were written for Rin and do not copy implementation code from
those projects. Links document host lifecycle, metadata, and transport APIs.
Rin's SDKs, examples, and documentation are distributed under the
[MIT License](../LICENSE).
