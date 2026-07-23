# Rin SDKs

[English](README.md) | [简体中文](README.zh-CN.md)

Thin, source-first clients for the `rin.protocol/v1` HTTP boundary.

The SDKs remove transport boilerplate without moving game authority into the
client library.

| Language | Runtime | JSON | Async guidance |
| --- | --- | --- | --- |
| Python | 3.9+ | standard library | call from a worker in real-time games |
| JavaScript | Node 18+ / modern browser host | built in | Promise-based |
| C# | .NET 6+ | `System.Text.Json` | `Task`-based |
| Java | 17+ | host-provided JSON text | `CompletableFuture`-based |
| Lua | 5.1+ host | injected codec and transport | callback-based |

All clients follow these rules:

- plaintext HTTP is accepted only for an explicit loopback origin;
- remote origins require HTTPS and a bearer token;
- redirects are rejected;
- request timeouts and response-size limits are mandatory;
- errors expose bounded Rin codes, not provider bodies or credentials;
- proposals remain pending until the game applies or rejects them and reports
  the result with Commit; Commit records an outcome and is not authorization.

That final rule applies to Sessions which explicitly request
`outcome-reporting-v1`; clients must not assume it for legacy Sessions.

The SDKs are intentionally source-first and are not yet published to PyPI,
npm, NuGet, or Maven Central. Pin this repository revision when vendoring one.
Route compatibility is defined by [`conformance/routes.json`](conformance/routes.json).

Game-specific examples live under [`examples/mods`](../examples/mods). They
show where host events enter Rin and where the game validates and applies a
proposal. They are integration templates, not universal patches for every
game version.

All SDKs follow the Commit lifecycle, Outbox, and retry rules in
[`docs/outcome-reporting.md`](../docs/outcome-reporting.md).

The SDK source is released under the [MIT License](../LICENSE).
