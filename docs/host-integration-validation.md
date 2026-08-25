# Host integration acceptance

[English](host-integration-validation.md) | [简体中文](host-integration-validation.zh-CN.md)

Contracts, unit tests, and headless game tests are release gates, but they do
not prove that an NPC behaves naturally, remains stable, or protects player
assets in a real game. Record automated and human evidence separately for each
adapter.

## Rin core gate

```bash
make verify
make build
```

`make verify` covers:

- Go vet and all package tests under the race detector;
- Host, Control, and Agent OpenAPI consistency;
- Python, JavaScript, C#, Java, and Lua SDKs;
- Grid, Story, and Terminal V2 adapter flows;
- MCP tools, authorization, and official-protocol-related tests.

A successful build proves the Rin core only. A game still needs its loader,
server, save, and UI tests.

## Minimum adapter automation

### Contract

- deterministic manifest, capability schemas, and digests;
- bounded, paginated, strict-JSON observations with correct epochs and sequences;
- unforgeable HostRefs and fail-closed stale references;
- binding without world mutation and Host-only effect derivation;
- output matching the capability output schema.

### Authority thread

- every game read and write occurs on the server/main/authority thread;
- HTTP, model, and disk waits never block the render or server tick;
- late callbacks cannot mutate a new epoch from an old world;
- exact redelivery of one operation never duplicates world effects.

### Policy and authority

- unknown effects, scopes, and ownership deny by default;
- tests for every profile, rule, budget, and confirmation path;
- expired controller leases, authority changes, and emergency stop block actions;
- multiplayer autonomy defaults off and remains region/asset/budget bounded when enabled;
- critical capabilities cannot bypass owner/admin confirmation or native game permissions.

### Operation

- `queued` is never reported as success;
- an operation never collected by a Host becomes `stale` with zero delivery attempts;
- ACK, Run, and Outcome order, duplication, and regression are validated;
- late Host outcomes reconcile `outcome-unknown`;
- cancel and emergency stop never claim rollback;
- every macro world mutation is an auditable child operation.

### Security

- tokens, API keys, filesystem paths, and private prompts never enter protocol,
  logs, saves, or fixtures;
- configuration and state reject symlinks, traversal, duplicate JSON fields, and oversized input;
- unknown third-party items, blocks, entities, or components are not trusted from names alone;
- pickup, container, combat, and destruction follow configurable asset policy.

## Fault-injection matrix

Using a copy of a real save, exit normally and terminate forcefully at each point:

1. before submitting an action;
2. after Control persistence but before Host poll;
3. after Host delivery but before ACK;
4. after ACK but before the real-time controller starts;
5. after world mutation but before writing the outcome outbox;
6. after writing the outcome but before daemon acknowledgement;
7. near controller lease, Host lease, or confirmation expiry;
8. during a long task while changing world, loading a save, or unloading the Actor.

After each recovery verify:

- operation ID, idempotency key, and applied marker remain stable;
- an applied effect is not duplicated;
- unexecuted stale-epoch work does not revive;
- the outcome outbox drains and unprovable results remain explicitly unknown;
- the Host must reregister and republish its read model before new work;
- Internal Tasks and external MCP observe the same terminal result.

## Human single-player acceptance

Complete a long playthrough in a new save and an existing save:

- installation, startup, configuration, disable, and update are understandable;
- internal mode can converse, observe, propose, and execute allowed safe actions;
- external MCP mode does not invoke an internal thinker and can use the same capabilities;
- the character composes atomic capabilities into continuous work and replans after failure;
- emergency stop halts navigation, harvesting, combat, and building promptly;
- world changes, death, sleep, container close, and disappearing targets do not deadlock;
- UI, logs, and errors distinguish queued, running, complete, failed, and unknown;
- render rate or server tick has no unacceptable stall.

## Human multiplayer acceptance

Validate on LAN and a representative dedicated server:

- autonomy defaults off and only an authorized player can enable it;
- Actor owner, controller, and other players are never confused;
- tamed, named, leashed, or protected assets are not attacked;
- unauthorized containers and player items are not read or moved;
- region protection, server permissions, command allowlists, and emergency stop
  override model intent;
- only one of two external Agents competing for an Actor gets the lease;
- multiple Actors and MCP clients do not cross-wire operations or outcomes.

## Behavioral-naturalness acceptance

Automation cannot judge whether a character feels alive. Human reviewers record:

- whether current activity, recent failure, and long-term preferences affect expression;
- whether the character proposes sensible small goals without spamming or looping;
- whether it knows when to wait, ask, refuse, or stop;
- repeated actions, repeated dialogue, and repeated exploration regions;
- whether memory references have evidence and avoid presenting inference as fact;
- whether token use, latency, and provider cost match player value.

Improve persona, memory, skills, model prompts, and observation quality for this
section. Never weaken policy or asset protection merely to appear more autonomous.

## Release evidence

Record for every acceptance run:

- Rin and adapter commits plus artifact SHA-256;
- game, engine, loader, OS, runtime, and mod list;
- single/multiplayer topology, permission profile, model, and provider;
- steps, expected result, actual result, redacted logs, and terminal operation;
- tick/frame time, model latency, tokens, task success, and human intervention;
- known issues and explicitly unverified claims.

Claim `idempotent-action` only after real fault tests prove that one operation
does not duplicate effects. Claim `transactional-action` only when game effects
and outcomes commit through one real transaction.

## Stop condition

After automated work, only full real-game play, GUI/installation experience,
and subjective character/voice/interactivity review should remain. Any issue
reproducible in code, a headless server, a fixture, or static analysis should be
resolved before human acceptance.
