# SDK and mod integration kits

[English](sdk-and-mods.md) | [简体中文](sdk-and-mods.zh-CN.md)

Thin integration kits connect game-owned adapters to Rin without moving world
authority into the sidecar or a model. They remove repetitive HTTP, timeout,
envelope, and job-polling code.

This document describes Rin `0.6.0` Preview. The authoritative wire schema is
[`api/openapi.json`](../api/openapi.json).

## Support matrix

| Language | Minimum runtime | Delivery model | JSON boundary | Typical host |
| --- | --- | --- | --- | --- |
| Python | 3.9 | synchronous | standard library | Ren'Py, tools, servers |
| JavaScript | Node 18 / Fetch host | Promise | built in | Electron, web bridges, Node |
| C# | .NET 6 | Task | `System.Text.Json` | BepInEx 6, modern .NET games |
| Java | 17 | `CompletableFuture` | injected `JsonCodec` | Fabric, JVM servers |
| Lua | 5.1 | callback | injected codec and transport | Luanti, embedded Lua engines |

Every implementation exposes the 20-route inventory generated into
[`sdk/conformance/routes.json`](../sdk/conformance/routes.json) from OpenAPI.
That inventory checks coverage; it is not a second wire contract or behavior
proof. Python and
JavaScript have no runtime dependencies. C# uses only framework APIs. Java
reuses the host's JSON library through a two-method codec. Lua injects all
host-specific services because Lua engines expose incompatible HTTP and JSON
APIs.

## Directory contract

```text
sdk/
  conformance/       language-neutral route inventory
  <language>/        source, language README, tests, optional quickstart
examples/mods/
  fabric-rin-npc/    source overlay for the official Fabric template
  bepinex-rin-npc/   BepInEx 6 source overlay
  luanti-rin-npc/    complete server mod with vendored Lua SDK
```

The SDKs are source-first and are not published to language registries. Vendor
the complete client directory from one exact Rin repository revision or
verified release tag. Do not copy a single client file without its README and
generated conformance inventory.

## Integration lifecycle

The apply-then-report steps below require the created Session to request
`outcome-reporting-v1`; otherwise the runtime intentionally preserves legacy
Commit and replay behavior.

1. Capture a bounded game-owned event and call `observe`.
2. Give Rin only candidate actions the game can safely implement.
3. Use the asynchronous Proposal Job API from real-time games.
4. Validate the returned action ID and payload against a local allowlist.
5. Marshal to the engine's owning thread and apply the action.
6. Persist the actual result in the game's Outcome Outbox as part of the apply
   transaction.
7. Call `commit` from the Outbox, including a rejection when needed. Retry a
   failed report without applying the action again.
8. Keep an authored or deterministic fallback for the confirmed no-online-
   Proposal case.

Treat an ambiguous Proposal submit, poll, timeout, or cancellation as
outcome-unknown and fail closed. Retry the same identity; do not execute the
fallback unless the integration has confirmed that no online Proposal exists.
Persist the complete Propose request and operation identity before submission,
then persist the Job ID immediately after `202`. Resume that record before any
new turn or fallback. Clear it only in the same authoritative transaction that
stores the game result, applied marker, and Outcome Outbox entry.

Provider failure inside a confirmed Sidecar Proposal operation can use Rin's
deterministic Policy. Sidecar submit/poll/cancel uncertainty is different and
must not be converted into a fallback action.

Never call online proposal or generation endpoints from a render/update loop.
One player interaction may start one job; ordinary frames should only poll a
local future, coroutine, timer, or main-thread queue.

Commit records an outcome rather than authorizing execution. See
[action outcome reporting](outcome-reporting.md) for Outbox, late-outcome,
same-`request_id` retry, and offline reconciliation rules.

All public JSON integers must remain in the exact interoperable range
`-9007199254740991` through `9007199254740991`. Commit and each Batch item must
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
  `413 snapshot_too_large`, never truncated. No streaming Snapshot transport
  is currently provided.
- Restore callers source mandatory `expected_binding` from the running trusted
  content manifest, not from the imported Snapshot.
- A Snapshot is trusted, opaque event-log-level state. Its SHA-256 canonical
  checksums detect accidental damage, but neither authenticate provenance nor
  stop a party that can recompute them.
- Treat generated dialogue as display data. Never parse it as a console
  command, reflection target, script name, item ID, or filesystem path.

Luanti is a documented exception: its engine HTTP implementation follows up
to three redirects and the mod API has no per-request opt-out. The example is
therefore loopback-only and refuses Authorization headers. Use a native bridge
before supporting authenticated remote Rin from Luanti.

## Example mods

The Fabric overlay follows the official project layout, reuses Minecraft's
Gson, and schedules effects with `MinecraftServer.execute`. Generate the build
files from the current Fabric template instead of pinning a Loom/Minecraft
combination that will age inside Rin.

The BepInEx overlay targets BepInEx 6 and .NET 6. It makes no HTTP request per
frame: `Update` drains a bounded queue and optionally detects the F8 demo key.
Subscribe to `NpcActionReady` and translate the three sample IDs through the
target game's supported APIs.

The Luanti example is a complete server mod. It calls
`core.request_http_api()` at module scope, keeps the returned API local, and
requires `secure.http_mods = rin_npc_example`.

## Verification

```bash
make test
make test-sdks
python3 tools/generate_contract.py --check
```

CI executes Go formatting, vet, race tests, and zero-CGO builds on Linux,
macOS, and Windows. It runs the Python SDK and Ren'Py adapter on Python 3.9
and the current Python 3 release, JavaScript on Node 18 and 24, Java on 17 and
25, C# against .NET 6 and 10, and Lua on 5.1 and 5.4. The contract generator
check prevents drift from OpenAPI to generated route/version projections.

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
- [Luanti Lua API source](https://github.com/luanti-org/luanti/blob/master/doc/lua_api.md)

The examples were written for Rin and do not copy implementation code from
those projects. Links document host lifecycle, metadata, and transport APIs.
Rin's SDKs, examples, and documentation are distributed under the
[MIT License](../LICENSE).
