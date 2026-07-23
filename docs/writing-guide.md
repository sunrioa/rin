# Documentation Style

[English](writing-guide.md) | [简体中文](writing-guide.zh-CN.md)

Rin documentation describes reusable contracts and verified capabilities, not
the history or requirements of one consuming game.

## Canonical description

Use this short description in repository metadata and introductory material:

> Game-native agent runtime.

When one sentence of context is needed:

> Rin manages stateful agent decisions outside the game loop while the game
> retains world authority.

## Rules

1. Lead with purpose, then explain operation and constraints.
2. Describe current behavior from code, tests, or a public protocol. Put future
   work in the roadmap.
3. Keep the authority boundary explicit: Rin proposes; the game validates,
   applies, and commits.
4. Use neutral terms such as `game`, `adapter`, `actor`, `consumer`, and
   `reference integration`.
5. Do not identify private consumer repositories, content titles, unreleased
   products, personal environments, or local workflow details.
6. Name an engine, runtime, package, or protocol only when the exact name is
   required for installation or interoperability.
7. Use generic example identifiers such as `example-game`, `actor.guide`, and
   `content-pack-v1`. Never include real credentials or private endpoints.
8. Avoid unsupported claims such as "human-like", "fully autonomous", or
   "production-ready". State the implemented boundary instead.
9. Keep English and Simplified Chinese pages structurally equivalent. Code,
   field names, paths, and commands remain unchanged when they are executable.
10. Prefer a short introduction, core capabilities, quick start, scope
    boundaries, reference links, and license in that order.

## Scope-specific names

Integration guides may name the platform they integrate with, and SDK guides
may use their real package or namespace. Those names describe an executable
interface. They must not become the definition of Rin itself.

Compatibility fixtures may retain implementation identifiers in source files
when changing them would break consumers. Public prose should describe the
contract and coverage in neutral terms.
