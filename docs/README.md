# Rin documentation

[English](README.md) | [简体中文](README.zh-CN.md)

These pages describe the current Harness V2 only. OpenAPI is authoritative for
HTTP fields and routes, while public Go types are authoritative for in-process
contracts. Documentation examples never replace authoritative game-adapter checks.

## Reading order

1. [Architecture](architecture.md): component boundaries, trust, and the full data flow.
2. [Host V2 contract](host-contract.md): observations, capabilities, actions, effects, and outcomes.
3. [Operations and policy](operations.md): authorization, confirmation, delivery, recovery, cancellation, and execution proof.
4. [Game adapters](game-adapters.md): what an engine integration must and must not implement.
5. [MCP and Control Plane](mcp-control-plane.md): external Agents, the daemon, tools, installation, and updates.
6. [Internal Agent Runtime](internal-agent-runtime.md): persona, memory, skills, models, and task execution.
7. [Rin Console](console.md): local monitoring, long goals, shared persona, and common memory cards.
8. [Task timeline](task-timeline.md): inspect public task decisions, policy, delivery, and authoritative outcomes.
9. [Task plans](task-plans.md): coordinate bounded multi-action work without bypassing the action gateway.
10. [Signal inbox](signals.md): receive short-lived Host attention hints for internal wake-up or external MCP reads.
11. [Host scaffolding](host-scaffolding.md): generate a contract skeleton for a language and engine.
12. [Integration acceptance](host-integration-validation.md): automated gates and human game testing.

Additional material:

- [SDK overview](../sdk/README.md)
- [Security boundary](../SECURITY.en.md)
- [Roadmap](../ROADMAP.en.md)

## Contract sources

| Contract | Source of truth |
| --- | --- |
| Host `rin.host/v2` | `host/*.go` |
| Control `rin.control/v2` | `api/control-openapi.json`, `controlplane/*.go` |
| Agent Task API `v1` | `api/agent-openapi.json`, `agentapi/*.go` |
| Management `rin.management/v1` | `api/management-openapi.json`, `managementapi/*.go` |
| Task Plan `rin.task-plan/v1` | `api/task-plan-openapi.json`, `taskstate/*.go` |
| Task timeline `v1` | `timeline/*.go`, `api/task-timeline-v1-fixtures.json` |
| Signal `rin.signal/v1` | `signalbox/*.go`, `api/signal-openapi.json` |
| MCP tools | `mcpbridge/server.go` |
| Gameplay policy | `policy/*.go` |

There is no promise of legacy protocol migration, engine-specific templates,
or public remote Control deployment. Prove player value in a concrete game
adapter before promoting a feature into the generic core.
