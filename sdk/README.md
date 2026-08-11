# Rin SDKs

[English](README.md) | [简体中文](README.zh-CN.md)

This directory contains thin `rin.control/v2` clients and the Go `hostkit` used
by game Hosts. Language clients connect to the resident local `rin-control`
daemon; they do not embed models, policy, or game execution logic.

| Language | Minimum runtime | Call style | Notes |
| --- | --- | --- | --- |
| Python | 3.9 | synchronous | standard library only |
| JavaScript | Node 18 | Promise | standard `fetch` |
| C# | .NET 6 / .NET Standard 2.0 | `Task` | strict bounded HTTP |
| Java | 17 | `CompletableFuture` | Host-supplied JSON codec |
| Lua | 5.1+ | callback | engine-supplied HTTP and JSON adapters |
| Go HostKit | Go 1.25 | `context.Context` | authority dispatch and V2 adapter coordination |

## Common Control operations

All five clients expose the same route set:

- list worlds, actors, observations, and capabilities;
- acquire, renew, and release the exclusive controller lease;
- submit or confirm an `ActionRequest`;
- get, long-poll, and cancel an operation;
- set the Actor emergency stop.

[`api/control-openapi.json`](../api/control-openapi.json) is the exact field
contract. SDKs intentionally use generic JSON objects instead of maintaining a
second set of protocol types that can drift.

## Transport guarantees

Every client enforces these boundaries:

- default connection to `http://127.0.0.1:7375` or another explicit loopback origin;
- a single-line bearer token of at least 32 bytes;
- no HTTP redirects;
- bounded timeout, response body, JSON depth, and safe integers;
- rejection of invalid UTF-8, non-JSON responses, and contract mismatches;
- distinct stable configuration, transport, protocol, and API errors.

`queued`, `accepted`, and `running` are intermediate states. A caller may report
game execution as complete only when the terminal operation has
`execution_confirmed=true` and a Host outcome.

## Versioning and distribution

These SDKs are source-first Preview snapshots. Pin the same Rin revision and do
not assume a similarly named public package is published or synchronized.

Language guides:

- [Python](python/README.md)
- [JavaScript](javascript/README.md)
- [C#](csharp/README.md)
- [Java](java/README.md)
- [Lua](lua/README.md)

See the [game adapter guide](../docs/game-adapters.md) for Host integration.
