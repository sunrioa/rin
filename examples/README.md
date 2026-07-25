# Rin examples

[English](README.md) | [简体中文](README.zh-CN.md)

Start with [`basic`](basic/). It is intentionally small and demonstrates only
creating a Session and recording one observation against a running Sidecar.
Its process-local IDs make it a development smoke test, not a production save
architecture.

Use [`recovery`](recovery/) when designing a real integration. It demonstrates
stable identities, durable Proposal Attempts, applied-operation markers, an
authoritative Outcome Outbox, exact retry, offline reconciliation, Snapshot
binding, atomic file replacement, and restart recovery. The extra size is
isolated here so the quickstart remains readable.

Use the installable Node.js 18+ [`terminal-story`](terminal-story/) for the
Windows/macOS/Linux playable vertical slice, safe JavaScript SDK workflow,
reproducible Sidecar benchmark, and honest persistent-rule-tree comparison.

The engine and mod directories demonstrate host-specific threading and
packaging. Their persistence hooks still need to be connected to the game's
authoritative save system.
