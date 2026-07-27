# Rin SDKs

[English](README.md) | [简体中文](README.zh-CN.md)

Thin, source-first clients for the `rin.protocol/v2` HTTP boundary.

The SDKs remove transport boilerplate without moving game authority into the
client library.

The type-checked Go reference under [`hostkit`](hostkit) defines the common
Host ports and restartable Coordinator. Its ownership and lifecycle contract
is documented in the [Universal Host SDK guide](../docs/host-sdk.md).

SDK workflow helpers validate the integration's declared
[host durability profile](../docs/host-durability.md). A client library
cannot manufacture durability or a world transaction that the game does not
provide.

| Language/target | Runtime | SDK profiles | Async guidance |
| --- | --- | --- | --- |
| Python | 3.9+ | `transport` | call from a worker in real-time games |
| JavaScript | Node 18+ / modern Fetch host | `transport`, `streaming` | Promise-based |
| C# | .NET 6+ | `transport`, `streaming` | `Task`-based |
| C# compatibility | .NET Standard 2.0 | `transport` | `Task`-based |
| Java | 17+ | `transport` | `CompletableFuture`-based |
| Lua | 5.1+ host | `transport` | callback-based |

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
- proposals remain advisory until the game accepts or rejects them and reports
  the typed Invocation, Run, and Outcome; reporting is not authorization.
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
framing. Session Transfer is the supported large-lineage path. The JavaScript
and .NET 6+ C# targets expose streaming source/sink helpers. Python, Java, Lua,
and the C# .NET Standard 2.0 compatibility target implement only the
`transport` profile and do not claim large-lineage Transfer support.

Live Session State defaults to a 16 MiB compact-JSON budget. Rin rejects an
offending mutation before persistence with `413 state_too_large`; operators
may raise the budget only to 24 MiB so the response envelope stays readable.

The SDKs are intentionally source-first and are not yet published to PyPI,
npm, NuGet, or Maven Central. Pin this repository revision when vendoring one.
Route compatibility is defined by
[`api/openapi.json`](../api/openapi.json);
[`conformance/routes.json`](conformance/routes.json) is its generated coverage
inventory. Each operation is tagged `transport` or `streaming`. Every client
must cover the transport profile; only clients with bounded stream APIs may
claim the streaming profile.

[`conformance/sidecar-corpus.json`](conformance/sidecar-corpus.json) is the
shared live-transport corpus. `make test-sdk-sidecar` builds a real Sidecar,
runs strict Wire cases once, then makes Python, JavaScript, C#, Java, and Lua
perform health, first mutation, exact retry, and timeout checks against the
same process and request template. The Lua runner supplies its normal
host-owned HTTP/JSON ports rather than claiming a bundled networking stack.

Game-specific examples live under [`examples/mods`](../examples/mods). They
show where host events enter Rin and where the game validates and applies a
proposal. They are integration templates, not universal patches for every
game version.

All SDKs follow the Action Report lifecycle, Outbox, and retry rules in
[`docs/action-lifecycle.md`](../docs/action-lifecycle.md).

The SDK source is released under the [MIT License](../LICENSE).
