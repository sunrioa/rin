# Roadmap

[简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

Rin `0.7.0` is Preview, pre-1.0 software. This file is the single authoritative
plan for future work. Released changes belong in the [changelog](CHANGELOG.md);
protocol facts belong in OpenAPI and the relevant topic guide. Temporary design
notes are removed after their implementation lands instead of becoming a second
roadmap.

## Product Direction

Rin's first priority is helping games implement characters that persist within
one save:

- a character remembers facts, relationships, and unfinished goals it could
  reasonably know;
- a model may propose dialogue, intent, and plans, but cannot mutate the
  authoritative world directly;
- in-game AI, commands, mod APIs, and MCP converge on one Host execution path;
- online models, proactive contact, and external control can all be disabled;
- Minecraft/Fabric is the deepest current reference scenario while the protocol
  remains engine-neutral.

Rin is not a game engine, general automation platform, or hosted model agent.
Rendering, navigation, physics, combat, inventory rules, quest rules, and final
authorization always belong to the game Host.

## Supported Today

### Runtime and durable state

- Multi-actor Sessions, Observations, Memory, Belief, Goal, and boundaries.
- A deterministic policy and an optional OpenAI-compatible online provider.
- Action Proposal, Attempt, Run, Outcome, and crash recovery.
- Hash-chained event logs, Snapshot, Replay, Timeline, and Session Transfer.
- Bounded asynchronous Generation, Speech, Memory Summary, and Telemetry ports.

### Host contract

- Engine-neutral Manifest, Epoch, Capability, Offer, Invocation, and Outcome.
- JSON Schema parameter validation, Descriptor Digest, dynamic revocation, and
  execution-time TOCTOU revalidation.
- HostKit Coordinator, Pending Journal, Outcome Outbox, and long-action recovery.
- Python, JavaScript, C#, Java, and Lua clients plus multiple engine references.

### External control

- A long-lived `rin-control` daemon exclusively owns port 7375, the state
  directory, and one fixed trusted Principal.
- Any number of thin `rin-mcp` STDIO proxies may connect to the same daemon; a
  client exit does not stop the Host service.
- Host registration, leases, read models, messages, rejectable directives, exact
  offers, cancellation, acknowledgements, progress, and outcomes.
- MCP and Host APIs share stable Operation IDs, Epoch/Observation bindings, and
  the exact Host-authored Offer.
- An exclusive state lock, stale timeline rejection, orphan expiry, and bounded
  recovery.
- A dedicated [`api/control-openapi.json`](api/control-openapi.json) contract and
  a capability-matched official MCP conformance gate.

## Current Implementation Phase

Work proceeds in this order. A later item does not expand until the previous one
has automated evidence.

### 1. Control correctness and topology

- [x] MCP and HostKit exact-offer execution share budget and final authorization
  semantics.
- [x] Directives bind to the submission Epoch and Observation Sequence.
- [x] Cross-platform exclusive locking for Control state.
- [x] Unfinished operations expire without a Host instead of occupying capacity
  forever.
- [x] A long-lived `rin-control` daemon and multi-client thin `rin-mcp` proxies.
- [x] Control OpenAPI, route-drift tests, and an official MCP conformance gate.
- [x] Frequent delivery/progress updates are recoverable checkpoints while queue,
  ACK, cancellation, and Outcome remain durable boundaries.

### 2. Documentation and complexity

- [x] Remove completed one-off design notes and the duplicate MCP plan.
- [x] Keep current work, future work, and explicit non-goals in this file.
- [ ] Update one contract source and one user entry point per feature instead of
  maintaining parallel specifications.
- [ ] Before release, remove unused Preview surfaces that have neither callers nor
  compatibility commitments.

### 3. One-save persistent-character reference slice

This phase primarily lands in a real Fabric Host and proves the generic contract
through cross-repository tests.

- [ ] Bind character identity, Canon, relationships, memory provenance, and
  unfinished goals to one world save.
- [ ] Separate facts known by the player, known by the character, shared, and not
  yet spoken.
- [ ] Route in-game commands, internal AI, and MCP through one character-turn
  execution service.
- [ ] Admit validated model dialogue as a traceable Canon Event.
- [ ] Recall bounded player wording without leaking another player or stale
  timeline.
- [ ] Provide online, offline, and automatic fallback paths; offline behavior may
  be visibly simpler.
- [ ] Test restart, load, dimension change, death, and temporary Host outage.

### 4. Proactivity and single control ownership

- [ ] Proactive dialogue is off by default and configurable by actor cooldown,
  quiet hours, distance, and daily limit.
- [ ] Dormant actors wake only from explicit conditions, not unbounded background
  polling.
- [ ] A character can propose small goals, ask follow-up questions, refuse, and
  revise a short-term plan.
- [ ] One actor has one write-control lease at a time; in-game, internal AI, and
  MCP contention returns an explicit busy or handoff result.
- [ ] Autonomous behavior uses the same Capability, Epoch, budget, and Outcome
  chain as external offers.

### 5. Bounded world tasks

- [ ] Expose a small stable resource set first: held item, bounded inventory slots,
  nearby container summaries, and tool availability.
- [ ] Every world mutation is an exact Host Offer; a model cannot construct
  arbitrary item IDs, coordinates, or method names.
- [ ] Complete one interruptible long-task loop: plan, obtain resources, move,
  execute, report progress, recover failure, and explain the outcome.
- [ ] Revalidate chunk unload, container changes, resources taken by players, tool
  damage, and permission revocation.
- [ ] Expand item, container, and task coverage only after the first loop passes
  human playtesting.

## Human Acceptance Gate

After automated tests pass, humans must confirm these behaviors in a real game:

- memory feels natural over 30 to 60 minutes, avoids repeated questions, and never
  cites facts the character should not know;
- proactive contact feels present without becoming intrusive and stops completely
  when disabled;
- the character reasonably refuses, clarifies, and admits limitations instead of
  inventing success;
- players can understand who owns control when internal AI and MCP target the same
  action;
- long-task progress, cancellation, failure, and recovery are legible;
- online latency, offline fallback, restart recovery, and long sessions show no
  obvious stalls or state drift;
- GUI, multiplayer, and locked-screen cases that cannot be automated are recorded
  on the acceptance checklist.

The implementation round stops at this gate. More framework surface is not a
substitute for player evidence.

## Later Work

The following areas retain design space but are not part of the current round.

### Character quality

- Long-term relationship stages, emotional aftermath, commitments, and unfinished
  topics.
- Sourced, correctable character opinions and explicit handling of conflicting
  memories.
- Model-proposed safe micro-goals approved by the Host or player.
- Auditable Canon correction, forgetting, export, and player privacy deletion.
- More natural multi-turn conversation, speech interruption, TTS voices, and
  accessible subtitles.

### Actions and tasks

- More items, recipes, workstations, trading, combat assistance, and complex
  containers.
- Verifiable small blueprints and staged construction, not unrestricted
  natural-language building.
- Pause, resume, replanning, and resource budgets for multi-step plans.
- Multi-character cooperation and conflict arbitration only after a single
  character loop proves player value.

### Engines and SDKs

- Generated cross-language Host Control clients from Control OpenAPI.
- Equally deep reference Hosts for Godot, Unity, Unreal, Ren'Py, and other
  server-authoritative games.
- A lightweight SDK extracted from reusable Journal, Lease, and thread-handoff
  code in the current Java integration.
- Post-1.0 compatibility only after save formats and protocols reach a stability
  threshold.

### Control and security

- Multi-Principal pairing, revocation, short-lived credentials, and an audit UI.
- MCP Streamable HTTP, TLS, and remote deployment only after authentication and
  threat models are complete.
- Multi-controller leases, priorities, human takeover, and arbitration.
- Remove the MCP Preview marker after the official `2026-07-28` protocol and Go
  SDK are stable releases.
- Full official conformance scenarios; the current gate runs only scenarios that
  match Rin's exposed capabilities.

### Storage and performance

- Measure latency and write volume at 1,000, 10,000, and 65,536 operations first.
- Introduce an append journal, segmented files, or SQLite/WAL only if whole-file
  checkpoints exceed a measured budget.
- Read-model persistence, compaction, and archive need explicit recovery semantics
  rather than cache-only behavior.
- Large model responses, speech, and media remain in bounded caches outside the
  core event log.

### Tooling and content production

- Static validation for character packs, capability packs, and test scenarios.
- Trusted content-pack signatures, provenance, version rollback, and source
  records.
- Author-facing character/task preview tools without arbitrary script execution
  inside the Runtime.
- Visual novels and RPGs may share Canon and control ports; narrative hot reload
  remains game-defined.

## Explicit Non-goals

Until product evidence changes the priority, Rin will not implement:

- identity synchronization across saves, servers, or games;
- direct model world-write authority or any path around Host Offers;
- pretending an NPC has every capability of a full game client or real player;
- arbitrary natural-language megabuilds, arbitrary blueprints, or arbitrary code
  execution;
- default multi-agent debate, group autonomy, or simultaneous multi-controller
  writes;
- public unauthenticated MCP/Control APIs, hosted accounts, or cloud sync;
- a plug-and-play promise for every game and engine;
- a vector database, ORM, message broker, or vendor SDK without measured need.

## Complexity Budget

- A new dependency must replace difficult self-maintained code and have licensing,
  cross-platform, and supply-chain justification.
- A new protocol type must solve a problem that two real Hosts cannot express with
  the existing contract.
- A new public API needs a caller, failure semantics, recovery semantics, and
  automated tests.
- A new topic guide needs one distinct reader task; phase plans live only here.
- Generated artifacts must be reproducible and verified, never a hand-maintained
  second contract.
- Experiments without player-value evidence stay in examples or branches, not the
  core Runtime.

## Preview Release Gate

- Go, adapter, SDK, contract, and cross-platform build checks all pass.
- Both OpenAPI documents, generated route inventory, and narrative docs do not
  drift.
- A fresh clone builds, tests, and runs the minimal example.
- MCP uses the official SDK and passes official conformance scenarios matching its
  capabilities.
- Player-value claims do not exceed the [recorded evidence](docs/player-value.md).
- Human results for real Fabric, BepInEx, and Luanti integrations are recorded;
  unverified areas remain explicitly Preview.

The invariant remains: a model may propose intent and expression; the game engine
decides what becomes real.
