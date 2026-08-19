# Milestone A Validation Report

[English](milestone-a-validation.md) | [简体中文](milestone-a-validation.zh-CN.md)

Date: 2026-08-19
Status: automated implementation and regression complete; human acceptance pending  
Scope: Rin, rin-mi, and ai-galgame

## Conclusion

Rin now provides an engine-neutral execution harness. Internal and external MCP
agents have separate persona and reasoning entry points, but share controller
leases, the action gateway, gameplay policy, operations, and authoritative Host
outcomes. Minecraft and a visual novel exercise the same path as real adapters.

All automated gates pass, but Milestone A is not complete. A locked desktop cannot
validate GUI behavior, TTS quality, character naturalness, continuous navigation,
or a real Windows launch. External memory providers, Mem0, Hindsight, and Graphiti
have not been started.

## Current architecture

```mermaid
flowchart TB
    EXT["External Agent<br/>own persona and memory"] --> MCP["rin-mcp<br/>stateless STDIO proxy"]
    USER["Player"] --> CONSOLE["Rin Console<br/>monitoring, goals, shared cognition"]
    INT["Internal Agent<br/>Persona / Memory / Skill / Model"] --> LOOP["AgentRuntime<br/>task and decision loop"]
    SIGNAL["Signal inbox"] --> LOOP
    MCP --> CTRL["rin-control<br/>resident control process"]
    CONSOLE --> CTRL
    LOOP --> CTRL
    CTRL --> GATE["Action Gateway<br/>identity, lease, target binding"]
    PLAN["PlanState<br/>coarse complex-task progress"] <--> LOOP
    MEM["SQLite Memory + FTS5<br/>optional remote embedding"] <--> LOOP
    SKILL["Skill Catalog<br/>standard SKILL.md"] --> LOOP
    HOST["Game Host / Adapter"] -->|"Observation + Capability"| GATE
    GATE -->|"ActionRequest"| HOST
    HOST -->|"BoundAction + Effect"| POLICY["Gameplay Policy<br/>budgets and minimal rules"]
    POLICY --> OPS["Operation Store<br/>delivery, cancel, recovery"]
    OPS <-->|"ACK / Run / Outcome"| HOST
    HOST --> ENGINE["real-time game controller"]
    ENGINE --> WORLD["authoritative world / canon"]
    WORLD --> HOST
    OPS --> TIMELINE["Task Timeline"]
    OPS --> MEM
```

Important boundaries:

- The Adapter owns the world and story canon. Rin Memory is a searchable projection.
- A model selects a target and capability; the Host binds concrete objects and effects,
  then policy decides whether execution is allowed.
- Queued, accepted, and running are not success. Only a Host outcome can set
  `execution_confirmed=true`.
- Simple actions bypass planning. Complex tasks use PlanState without invoking a
  planner for every action.
- External MCP does not require the internal model, and the internal agent cannot
  take control while an external controller owns the lease.

## Runtime decomposition

Rin's `AgentRuntime` was reduced from about 1,896 lines to 774 lines. Context
assembly, task lifecycle, plan and decision orchestration, action and operation
coordination, and signal wake scheduling now live in focused package-private files.

rin-mi extracted action dispatch, capability projection, agency scheduling,
operation recovery, and session storage from `CompanionRuntime`. The Ender Dragon
loop added dedicated portal, dimension, heading, landmark, and boss controllers;
the runtime is now about 4,260 lines. Real-time logic remains in package-private
controllers and no Minecraft types enter Rin Core. Further movement waits for
human trace replay instead of risking a large pre-acceptance refactor.

## Automated evidence

| Scope | Result |
| --- | --- |
| Rin Core | `make verify`: contracts, Vet, Race, and all Go packages pass |
| SDKs | Python, JavaScript, C#, Java, and Lua tests pass |
| Example adapters | Grid, Story, and Terminal tests pass |
| Builds | macOS arm64, Windows amd64, and Linux amd64 binaries generated |
| rin-mi | Core, Skill validation, installer, and 28/28 Fabric GameTests pass |
| rin-mi process tests | V2 Binding and Internal Agent Macro pass against real `rin-control` |
| ai-galgame | 328 Python tests, Ren'Py lint, content, and asset checks pass |
| ai-galgame process tests | External and Internal full-process E2E pass |

The visual-novel smoke suite covers seven chapters, 19 interactive turns, 12
bridges, 13 bookends, seven core conversations, six quiet moments, and about 150
planned minutes. Lint reports 285 dialogue blocks, 18 menus, 27 images, and 30 screens.

## Performance and tokens

These local measurements detect regressions; they are not cross-machine guarantees.

| Item | P50 | P95 | Gate |
| --- | ---: | ---: | ---: |
| SQLite Memory recall | 8.58ms | 9.59ms | P95 < 250ms |
| PlanState operation | 0.14ms | 0.23ms | P95 < 100ms |

The scripted provider fixture reports 100 prompt, 40 completion, 64 cache-hit, and
36 cache-miss tokens per call. This validates Usage-to-Timeline propagation only;
it does not claim savings from a real provider.

## Breaking cleanup

The retired file memory provider, its tests, and the initial `memory.json` migration
path were removed as requested. `memory.db` is now the only online source of truth
inside the Rin Memory domain. JSONL remains an explicit exchange format only.

## Human acceptance still required

1. Run two to four continuous hours: at least 90 minutes in Minecraft, with at least
   45 minutes each for internal and external control, plus 45 minutes in the visual novel.
2. Exercise Minecraft gathering, crafting, building, survival, combat, replanning,
   controller switching, emergency stop, restart, difficult terrain, dimension
   transfer, fortress/stronghold search, Eye of Ender travel, and a complete fresh-world Dragon run.
3. Exercise fixed story, AI ScenePacket, critical choice, proactive topic, canon conflict,
   save/load, rollback, and Internal/External switching; judge dialogue naturalness.
4. With an unlocked desktop, run native Ren'Py testcases and inspect 1280x720,
   1536x864, and 1920x1080 UI. Launch the release on real Windows.
5. Listen to character TTS for Japanese pronunciation, pacing, voice binding, and silent fallback.
6. Compare task timelines with play and confirm that public reasons, Memory/Skill refs,
   tokens, policy, and outcomes explain behavior without leaking private context.

## Known limitations

- GameTest can log missing `server.properties`, Yggdrasil timeouts, and upstream
  deprecation warnings; all 28 required tests still pass.
- Native Ren'Py window tests cannot run while macOS has no available display.
- Cross-compilation does not replace execution on the target operating system.
- Automated traces prove contract and outcome consistency, not subjective character quality.

## Stage commits

Current Rin stages before this report: `ce16d21`, `81f8bb5`, `c70642d`; the
Console timeline and documentation closure are in the commit containing this
report. Current rin-mi stages: `28690cd`, `f7f31da`.

Do not start the ExternalMemoryProvider SPI or a concrete external-memory adapter
until human acceptance is complete and the user explicitly confirms the milestone.
