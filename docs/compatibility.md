# Rin 0.7 compatibility matrix

[English] | [简体中文](compatibility.zh-CN.md)

Rin `0.7.0` is **Preview**, pre-1.0 software. Pin an exact commit or verified
`v0.7.0` tag. Protocol v2 deliberately removes the v1 wire and compatibility
reducers; use a new data directory or explicit export/import when evaluating
this development release.

## Sources of truth

| Concern | Authority |
| --- | --- |
| Paths, methods, status, JSON shape | [`api/openapi.json`](../api/openapi.json) |
| Host/action/recovery semantics | [Protocol v2](protocol-v2.md), [Host Contract](host-contract.md), [action lifecycle](action-lifecycle.md) |
| SDK operation inventory | [`sdk/conformance/routes.json`](../sdk/conformance/routes.json) |
| SDK live transport behavior | [`sdk/conformance/sidecar-corpus.json`](../sdk/conformance/sidecar-corpus.json) |
| Release process | [release guide](release-guide.md) |

When prose and OpenAPI disagree about a wire shape, OpenAPI wins and the prose
is a documentation defect.

## Supported surface

| Surface | 0.7 contract | Consumer rule |
| --- | --- | --- |
| Wire | `rin.protocol/v2` | Send the exact value; v1 is rejected |
| Routes | `/v2/*` | No alias routes |
| Requests | Closed objects | Unknown fields are rejected |
| Responses | Additive during Preview | Ignore unknown response fields |
| Integers | JSON-safe range | Never send quoted integers or `BigInt` |
| Action input | Complete `ActionOffer` | Model selects only `offer_id` |
| Action result | Typed decision/invocation/run/outcome | Host executes before reporting terminal effects |
| Epoch/time | Required on observations and offers | Never substitute render frames |
| Restore | Trusted `expected_binding` | Treat Snapshot as opaque sensitive data |
| Transfer | Bounded NDJSON | Use for lineages above inline Snapshot limits |
| File Store | Local reliable filesystems | Use a coordinated Store for HA/shared storage |
| SDKs | Source-first | Vendor the full directory and pin the Rin revision |
| Hosts | macOS/Linux/Windows build/test where listed | Real engine/server acceptance remains separate |
| Optional Go ports | Decision, generation, derived memory, speech, telemetry | Adapter owns vendor translation and cancellation |

## Optional features

The v2 Host lifecycle is baseline protocol and has no Feature flag. Current
optional Session features are:

- `memory-archive-v1`;
- `belief-conflicts-v1`;
- `goal-candidates-v1`;
- `actor-activity-v1`;
- `arbitration-v1`;
- `identifier-history-v1`.

Negotiate through `/health` and enable only features the game actually
persists and implements.

The Go-only optional extension ports are not wire Feature flags and do not
change Session authority. See [optional extensions](optional-extensions.md).
Because 0.7 is Preview, superseded public Go aliases are removed; consumers
must use `DecisionContext`, `DecisionDraft`, `DecisionProvider`, and
`StructuredGenerationProvider`.

## Platform matrix

| Component | macOS | Linux | Windows |
| --- | --- | --- | --- |
| Go runtime/CLI | tested | CI | CI |
| Python/Ren'Py adapter logic | tested | CI | CI |
| JavaScript SDK | tested | CI | CI |
| C# SDK/Unity compile harness | tested | CI | CI |
| Unreal Runtime Plugin static contract | not installed | CI | CI |
| OpenSpiel 2.0.1 decision semantics | tested | CI | CI |
| Java SDK/Fabric compile | tested | CI | CI |
| Lua SDK/Luanti 5.16.1 dedicated lifecycle | tested | Lua CI | real server CI |
| Shared real-Sidecar SDK corpus | five SDKs tested | five SDKs CI | Python/JavaScript/C#/Java CI |
| Real Fabric/BepInEx/Unity/Unreal host | manual evidence required | manual evidence required | manual evidence required |
| Luanti live Sidecar/multiplayer/fault injection | manual evidence required | manual evidence required | manual evidence required |

Compilation and mocked engine APIs are useful contract evidence, but they do
not prove loader compatibility, main-thread behavior, save integration, or a
long soak inside the real game.

## Hash and security boundary

Event hashes, Snapshot checksums, and checkpoint checksums detect inconsistent
or accidentally damaged data. They are not signatures, MACs, or provenance.
Protect the data directory, game saves, exports, and backups with external
access controls.
