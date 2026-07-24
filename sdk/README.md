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
  current head is required. For a pre-`rin.reducer-projection/v2` Proposal,
  Rin preserves those coordinates and structured fields but upgrades
  `summary`/`rationale` through the player-text gate;
- `event_exists` is a conflict from another request, not a duplicate
  acknowledgement;
- proposals remain pending until the game applies or rejects them and reports
  the result with Commit; Commit records an outcome and is not authorization.
- use proposal `summary` and `rationale` as the player-facing copy: Rin
  derives them from the game-authored action description and a fixed stance
  template. Treat `policy_source`, `recalled_memory_ids`, `goal_id`, the
  optional additive `boundary_id`, and the full `proposed_goal` as private
  audit/integration metadata and never display them directly to players.
  Action IDs, kinds, targets, and parameters are integration data unless the
  game separately authorizes them;
- all shipped SDKs use tolerant object decoding. Dynamic clients already
  preserve `boundary_id`; Unity's typed example declares it explicitly, and
  older typed clients may safely ignore this additive v1 response field.

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
and preserve unknown additive fields when storing it. Treat the entire Snapshot
as trusted, opaque state: its SHA-256 canonical checksums detect accidental
damage or an unsynchronized edit, but do not authenticate its source or stop
someone who can recompute them.

Restore requires `expected_binding` from the running game's trusted content
manifest. It must match the imported Snapshot binding and, for an existing
target Session, that Session's binding; do not populate it by reading the
Snapshot.

Complete inline Snapshot compact JSON is capped at 16 MiB. Rin returns
`413 snapshot_too_large` rather than truncating it. Every SDK defaults to a
32 MiB response limit, matching the server's default 32 MiB request-body limit
and leaving headroom for envelopes, Restore metadata, and durable EventRecord
framing. No streaming Snapshot transport is currently provided, so a lineage
that outgrows the inline ceiling cannot use these JSON methods.

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
