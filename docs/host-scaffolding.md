# Host scaffolding

[English](host-scaffolding.md) | [简体中文](host-scaffolding.zh-CN.md)

`rin init host` generates an offline, deterministic `rin.host/v2` contract
skeleton. It does not download templates, install dependencies, generate model
code, or pretend to integrate a game engine.

## Create

```bash
./bin/rin init host \
  -engine custom \
  -runtime java \
  -id my_game_host \
  -name "My Game Host" \
  -version 0.1.0 \
  -output ./my-game-host
```

Supported runtimes are `go`, `javascript`, `python`, `csharp`, `java`, and
`lua`. The sole engine template is currently `custom`; it represents the
generic contract, not a completed engine integration.

Inspect without writing:

```bash
./bin/rin init host -engine custom -runtime java -id my_game_host -dry-run
./bin/rin init host -list-hosts
```

The destination must not exist. The generator never overwrites a path or picks
a different name automatically.

## Files

```text
my-game-host/
  .editorconfig
  .gitignore
  README.md
  README.zh-CN.md
  LICENSE-RIN.txt
  rin-host.json
  rin-scaffold.json
  capabilities/
    dialogue.say.json
  src/
    README.md
```

- `rin-host.json`: schema 2, `rin.host/v2`, runtime, durability, and capability directory.
- `rin-scaffold.json`: generator and project metadata plus SHA-256 for every
  generated file except the manifest itself, used to detect drift.
- `capabilities/dialogue.say.json`: a sealed `CapabilitySpec` example.
- `src/README.md`: authority boundaries the concrete game must implement.
- `LICENSE-RIN.txt`: covers Rin-generated scaffold material only and does not license the game or mod.

The default capability is an example and cannot make a game display dialogue.
You must implement argument and object binding, effect preview, authority-thread
execution, and outcome reporting.

## Verification commands

Run from the Rin repository root after `make build`:

```bash
./bin/rin conformance host -path ./my-game-host
./bin/rin doctor host -path ./my-game-host
```

Conformance checks:

- a real directory rather than a symlink;
- manifest schema, Host contract, and project identity;
- generated-file SHA-256, Windows case collisions, and portable paths;
- agreement between `rin-host.json` and the manifest;
- every capability schema, digest, version, risk, and execution bound;
- no duplicate ID and version.

Doctor reports integration status and next work. These commands prove only the
contract skeleton, never real game behavior.

## Integration order

1. Define stable Host, World, Actor, and epoch identities from the game save.
2. Implement an authority dispatcher for every world read and mutation.
3. Publish bounded observations and a capability catalog.
4. Implement `Bind` and `Preview` without mutating the world.
5. Configure known effect kinds/scopes, rules, budgets, and confirmation.
6. Implement idempotent `Execute`, `Cancel`, `Verify`, and an outcome outbox.
7. Connect to `rin-control` for Host register, publish, poll, ACK, run, and outcome.
8. Run fault injection and real-game acceptance.

## Safety properties

Before writing, the generator renders every file and validates total size,
relative paths, case collisions, reserved names, symlinks, and destination
existence. It creates the final directory directly, adds
`.rin-scaffold.incomplete`, exclusively creates and verifies every generated
file, then removes the marker. Once the marker exists, a later failure retains
it. Only one concurrent creator of the same destination may succeed.

`rin-scaffold.json` is an integrity inventory, not a signature. Someone able to
modify files can recalculate SHA-256. Use your own signing and supply-chain
process for releases.

## What it does not generate

- engine SDKs, loaders, Gradle projects, Unity packages, or Unreal plugins;
- a background daemon or MCP server;
- API keys, tokens, or model-provider configuration;
- arbitrary commands, shells, dynamic code, or a private game executor;
- a claim of real-engine validation.

This boundary is deliberate. A generic tool can generate contracts, but only
an adapter author knows how to inspect and mutate a specific game safely.
