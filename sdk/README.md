# Rin SDKs

[English](README.md) | [简体中文](README.zh-CN.md)

Thin, source-first clients for the `rin.protocol/v1` HTTP boundary.

The SDKs remove transport boilerplate without moving game authority into the
client library.

| Language | Runtime | JSON | Async guidance |
| --- | --- | --- | --- |
| Python | 3.9+ | standard library | call from a worker in real-time games |
| JavaScript | Node 18+ / modern browser host | built in | Promise-based |
| C# | .NET 6+ | `System.Text.Json` | `Task`-based |
| Java | 17+ | host-provided JSON text | `CompletableFuture`-based |
| Lua | 5.1+ host | injected codec and transport | callback-based |

All clients follow these rules:

- plaintext HTTP is accepted only for an explicit loopback origin;
- remote origins require HTTPS and a bearer token;
- redirects are rejected;
- request timeouts and response-size limits are mandatory;
- errors expose bounded Rin codes, not provider bodies or credentials;
- the caller creates and durably stores every Session-mutation `request_id`
  and Observe/Outcome `event_id`; SDKs never generate, rotate, or silently
  replace them;
- SDKs do not automatically retry mutations. A caller may retry only the exact
  same typed payload and IDs; changing any field under the same request ID
  returns `request_id_conflict`;
- an exact duplicate returns the first durable revision/head (or original
  Proposal/Arbitration) with `duplicate=true`. Read Session State when the
  current head is required;
- `event_exists` is a conflict from another request, not a duplicate
  acknowledgement;
- proposals remain pending until the game applies or rejects them and reports
  the result with Commit; Commit records an outcome and is not authorization.

On `mutation_outcome_unknown`, retain the non-Proposal operation and retry only
its exact typed payload and IDs; the mutation may already be durable, and other
Session mutations remain blocked until confirmation. Proposal writes use
`proposal_outcome_unknown` with the same recovery rule. Neither code authorizes
rotating the request ID, applying an action again, or advancing an Outbox.

That final rule applies to Sessions which explicitly request
`outcome-reporting-v1`; clients must not assume it for legacy Sessions.

Durable identity applies to Session mutations, not to process-local Job
metadata. Proposal Job records may be reconstructed from the durable Proposal
after eviction or restart. Generation Jobs are not event-logged and the same
request may run again after Job retention expires or the sidecar restarts.

Snapshot responses contain `identifier_history` and
`identifier_history_hash`. This history grows with the Session lineage and may
contain historical Proposal/Arbitration text, so treat it like the event log
and preserve unknown additive fields when storing it. All SDKs default to a
2 MiB response limit and allow configuration only within their documented
bound; large-lineage Snapshot transport remains subject to that limit.

The SDKs are intentionally source-first and are not yet published to PyPI,
npm, NuGet, or Maven Central. Pin this repository revision when vendoring one.
Route compatibility is defined by [`conformance/routes.json`](conformance/routes.json).

Game-specific examples live under [`examples/mods`](../examples/mods). They
show where host events enter Rin and where the game validates and applies a
proposal. They are integration templates, not universal patches for every
game version.

All SDKs follow the Commit lifecycle, Outbox, and retry rules in
[`docs/outcome-reporting.md`](../docs/outcome-reporting.md).

The SDK source is released under the [MIT License](../LICENSE).
