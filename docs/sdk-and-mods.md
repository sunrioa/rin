# SDK and mod integration kits

[English](sdk-and-mods.md) | [简体中文](sdk-and-mods.zh-CN.md)

Thin integration kits connect game-owned adapters to Rin without moving world
authority into the sidecar or a model. They remove repetitive HTTP, timeout,
envelope, and job-polling code.

## Support matrix

| Language | Minimum runtime | Delivery model | JSON boundary | Typical host |
| --- | --- | --- | --- | --- |
| Python | 3.9 | synchronous | standard library | Ren'Py, tools, servers |
| JavaScript | Node 18 / Fetch host | Promise | built in | Electron, web bridges, Node |
| C# | .NET 6 | Task | `System.Text.Json` | BepInEx 6, modern .NET games |
| Java | 17 | `CompletableFuture` | injected `JsonCodec` | Fabric, JVM servers |
| Lua | 5.1 | callback | injected codec and transport | Luanti, embedded Lua engines |

Every implementation covers the 20 routes in
[`sdk/conformance/routes.json`](../sdk/conformance/routes.json). Python and
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

The SDKs are source-first and are not published to language registries yet.
Vendor a tagged Rin revision or reference the source project directly. Do not
copy a single client file without its README and conformance version.

## Integration lifecycle

1. Capture a bounded game-owned event and call `observe`.
2. Give Rin only candidate actions the game can safely implement.
3. Use the asynchronous Proposal Job API from real-time games.
4. Validate the returned action ID and payload against a local allowlist.
5. Marshal to the engine's owning thread and apply the action.
6. Call `commit` with the actual outcome, including a rejection when needed.
7. Keep an authored or deterministic fallback when Rin is unavailable.

Never call online proposal or generation endpoints from a render/update loop.
One player interaction may start one job; ordinary frames should only poll a
local future, coroutine, timer, or main-thread queue.

## Credentials and transport

- Keep model-provider credentials in the Rin sidecar only.
- A game may hold `RIN_TOKEN`, which authenticates the game to Rin; it is not a
  provider API key and must not be written to saves, logs, or mod configs.
- SDKs accept plaintext HTTP only for loopback. Remote Rin origins require
  HTTPS and a token.
- Redirects are rejected, responses are size-limited, and user-visible errors
  contain bounded Rin codes rather than provider bodies.
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
```

The main Go compatibility suite checks route coverage, security markers,
engine-thread handoff, local action allowlists, and exact synchronization of
the vendored Luanti client. CI then executes Python, JavaScript, Java, C#, and
both Lua 5.1 and 5.4; the other jobs use each SDK's minimum supported runtime.

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
