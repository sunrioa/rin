# Rin

[简体中文](README.md) | [English](README.en.md)

Rin is a game-oriented agent runtime. It keeps character memory, goals, decisions, and asynchronous work outside the game loop, then returns locally validated action proposals to the game.

## Status

The source version is `0.7.0` Preview (pre-1.0), and the project is still under development. Before a verified release tag exists, pin an exact repository revision or tag. See the [compatibility matrix](docs/compatibility.md) and [changelog](CHANGELOG.md) for migration information.

## What Rin does

- The game submits what a character actually observed as an `Observation`.
- The character produces an `ActionProposal` from memory, goals, boundaries, and the actions currently allowed by the game.
- The game keeps world authority. It validates, applies, or rejects the proposal and reports the action result to Rin.
- State changes are written to a hash-chained event log that can be checked with Replay, Timeline, and `rin inspect`.
- Proposals, Generation Jobs, snapshots, and Session Transfer each have bounded size, time, and concurrency budgets.

Rin can run as a sidecar or be embedded as a Go package. It does not depend on a particular game, engine, or model provider. Online models are optional; a deterministic Policy can run without one.

## Quick start

Rin requires Go 1.25 or later. Start a local sidecar:

```bash
make test
go run ./cmd/rin serve -data ./rin-data
```

The default listener is `127.0.0.1:7374`. Check it with:

```bash
curl http://127.0.0.1:7374/health
```

Run the small example:

```bash
go run ./examples/basic
```

It covers Session creation and Observe only. The complete V2 reference using
one Adapter, Effect Policy, and shared internal-Agent/external-MCP execution
path is [`examples/terminal-story`](examples/terminal-story/README.md).

Build the long-lived control daemon and MCP thin proxy (prefers `2026-07-28`
by default):

```bash
go build -o bin/rin ./cmd/rin
go build -o bin/rin-control ./cmd/rin-control
go build -o bin/rin-mcp ./cmd/rin-mcp
export RIN_CONTROL_TOKEN="$(openssl rand -hex 32)"
export RIN_CONTROL_PRINCIPAL="player.one"
export RIN_CONTROL_SCOPES="actor.read"
export RIN_CONTROL_DATA_DIR="/absolute/path/to/rin-control-data"
./bin/rin-control
```

Configure Rin MCP once for installed Codex, Claude Code, or OpenClaw clients:

```bash
./bin/rin mcp install
./bin/rin mcp status
```

The installer offers an interactive Agent selector, uses each Agent's official
CLI to register one stable `rin-mcp` path, and stores the URL and token only once
in a mode-`0600` local configuration. After unpacking a newer Rin distribution,
`rin mcp update` preserves every Agent registration. Automation can use
`-agents codex,claude,openclaw` or `-yes`. See the
[MCP quick start](docs/mcp-control-plane.md) for installation, update, and
uninstall details.

`rin-control` stays on `127.0.0.1:7375`; game Hosts and any number of `rin-mcp`
STDIO proxies connect to it. The default scope is `actor.read`; write tools
require explicit grants, and the game Host still validates and applies every
world mutation. See the
[MCP quick start](docs/mcp-control-plane.md) for client configuration, scopes,
and Host endpoints.

Generate a Host or Mod starter project:

```bash
go run ./cmd/rin init host --list-hosts
go run ./cmd/rin init host --engine fabric --id guide_npc --name "Guide NPC" --namespace io.github.example
```

`custom` supports Go, JavaScript, Python, C#, Java, and Lua. Fabric, BepInEx Mono, BepInEx IL2CPP, and Luanti templates are also available. The generator never overwrites an existing path; see the [Host scaffolding guide](docs/host-scaffolding.md).

## Integration paths

- Ren'Py, Godot 4, Unity, and Unreal reference adapters
- Python, JavaScript, C#, Java, and Lua SDKs
- Fabric, BepInEx, and Luanti example mods
- The engine-neutral `host` contract and HostKit

See [game adapters](docs/game-adapters.md) for installation, thread boundaries, and offline behavior. Cross-language SDK rules, credentials, and Mod installation are covered in [SDK and Mod integration](docs/sdk-and-mods.md).

## Documentation

- [Documentation index](docs/README.md) / [简体中文](docs/README.zh-CN.md)
- [Protocol v2](docs/protocol-v2.md): fields, errors, and retry semantics
- [Action lifecycle](docs/action-lifecycle.md): proposals, execution, Outbox, and recovery
- [MCP quick start](docs/mcp-control-plane.md): official version negotiation, Host publication, and authority
- [Deployment and monitoring](docs/operations.md): tokens, TLS, storage, and runtime limits
- [Release guide](docs/release-guide.md) and [roadmap](ROADMAP.en.md)
- [Security](SECURITY.en.md), [changelog](CHANGELOG.md), and [third-party notices](THIRD-PARTY-NOTICES.md)

`api/openapi.json` is the Runtime HTTP contract, `api/control-openapi.json` is
the Host Control contract, and `api/agent-openapi.json` is the internal Agent
Task contract. The protocol reference
describes runtime semantics; focused documents cover adapters, long sessions,
Transfer, and optional extensions. The root README does not duplicate those
details.

## Repository layout

```text
cmd/rin/       Sidecar command-line program
cmd/rin-control/ Long-lived Host Control daemon
cmd/rin-mcp/   MCP STDIO thin proxy
api/           Runtime and Control OpenAPI 3.1 contracts
protocol/      Cross-language v2 types
runtime/       Event state machine, proposal validation, snapshots, scheduling
store/         JSONL file store and in-memory store
httpapi/       HTTP, authentication, and request-size limits
controlplane/  Host leases and principal-isolated control state
mcpbridge/     Official MCP SDK to Control Plane bridge
sdk/           Python, JavaScript, C#, Java, and Lua SDKs
adapters/      Ren'Py client and bridge
tools/         Contract projection and verification tools
examples/      Example programs, adapters, and Mods
```

## Security and deployment

Rin makes no network calls by default. A production sidecar should use a dedicated token and a same-host TLS reverse proxy:

```bash
export RIN_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/rin serve
```

A remote listener must declare `-allow-remote`, a `RIN_TOKEN` of at least 32 bytes, and `-tls-proxy` (or `RIN_TLS_PROXY=true`). These options do not provide TLS or make a public plaintext listener safe. Tokens, model keys, and provider URLs are not written to events, snapshots, or responses. See [deployment and monitoring](docs/operations.md) and [security](SECURITY.en.md).

Rin does not own rendering, navigation, physics, combat, inventory, quest rules, or arbitrary script execution. Model output is never treated as a world fact. The project does not add provider SDKs, a vector database, an ORM, WebSockets, or dynamic plugin execution.

## License

Rin is released under the [MIT License](LICENSE).
