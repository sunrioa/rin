# Rin 0.6 compatibility matrix

[English](compatibility.md) | [简体中文](compatibility.zh-CN.md)

## Release status

Rin `0.6.0` is Preview, pre-1.0 software. The project documents compatibility
and migration behavior, but a later pre-1.0 minor release may make an
incompatible change when it is called out in the changelog and migration guide.
Pin integrations to an exact Rin repository revision or verified release tag.

The implementation milestones named 0.1 through 0.5 are history labels, not
evidence that public tags with those names exist.

## Sources of contract truth

| Concern | Authoritative source | Notes |
| --- | --- | --- |
| HTTP paths, methods, status codes, required fields, JSON shapes | [`api/openapi.json`](../api/openapi.json) | The single wire-schema source for 0.6 |
| Transaction, retry, authority, and persistence semantics | [Protocol v1](protocol-v1.md) and [outcome reporting](outcome-reporting.md) | Narrative semantics supplement OpenAPI |
| SDK route coverage inventory | [`sdk/conformance/routes.json`](../sdk/conformance/routes.json) | Coverage inventory only; it does not override OpenAPI |
| Release changes | [Changelog](../CHANGELOG.md) | Includes breaking and security-relevant changes |
| Upgrade actions | [v0.6 migration](migration-v0.6.md) | Required before replacing an older deployment |

If prose and the wire schema disagree about a path, field presence, shape, or
HTTP status, `api/openapi.json` wins and the prose is a documentation bug.

## Public compatibility matrix

| Surface | 0.6 contract | Legacy/import behavior | Consumer requirement |
| --- | --- | --- | --- |
| Distribution | `0.6.0` Preview | Earlier numbered milestones may have no tag | Pin one commit or the verified `v0.6.0` tag |
| Wire identifier | `rin.protocol/v1` | Existing v1 events remain replayable subject to documented legacy semantics | Send the exact identifier on every JSON request |
| Routes | 20 operations across 18 paths described by OpenAPI | No implicit alias routes | Generate or implement against OpenAPI; use the route inventory only to check coverage |
| Request objects | Closed; unknown members are rejected | Older requests may become invalid when a newly required safety field is documented | Do not send speculative fields |
| Response objects | May gain additive fields | Older tolerant clients can ignore fields they do not understand | Decode tolerantly; persist each Snapshot as opaque JSON and send that original JSON back on Restore instead of round-tripping it through a lossy typed model |
| Integers | Exact JSON range `-9007199254740991` to `9007199254740991`; many fields are additionally non-negative | Values outside the range are rejected rather than rounded | Send JSON numbers, not quoted integers or JavaScript `BigInt` |
| Text requests | Raw HTTP body must be valid UTF-8 before JSON decoding | Invalid raw bytes return `400 invalid_json` | Encode requests as UTF-8 JSON |
| Commit outcome | `accepted` is required in Commit and every Batch item, including explicit `false` | Omission is invalid, not rejection | Use a presence-aware DTO/serializer |
| HTTP errors | `{ok:false,error:{code,message,field?}}` with a non-2xx status | — | Branch on bounded `error.code`, not message text |
| Job failures | Query/cancel can return HTTP `200` with a terminal Job whose `data.error` describes operation failure | Job records are bounded and process-local | Treat HTTP success and Job success as separate conditions |
| Session semantics | Features are fixed in Session state | A Session without a Feature retains its historical semantics | Negotiate through `/health`, choose Features at Session creation, and never edit a Snapshot to add one |
| Restore | `expected_binding` is required and comes from the running trusted manifest | A legacy Snapshot without Identifier History imports with permanently incomplete coverage | Keep globally unique IDs and follow the migration guide |
| Identifier identity | Request and Event IDs remain reserved for the entire lineage | Legacy incomplete history cannot recover already-evicted IDs | Never rotate an unresolved ID or reuse an abandoned-branch ID |
| Reducer projection | `rin.reducer-projection/v2` | v1 checkpoints are discarded as derived caches; event logs are not rewritten | Expect legacy Proposal presentation to be reconstructed on read/exact retry |
| Snapshot transport | 16 MiB compact JSON; surrounding request/response defaults are 32 MiB | Oversized legacy events may replay locally but cannot cross the inline API | Capacity-plan Identifier History; no streaming transport exists |
| File Store | Local `darwin`/`linux` filesystems with reliable `flock`, rename, and sync semantics | Other GOOS fail closed | Use another coordinated Store for Windows, HA, remote, or shared storage |
| SDK delivery | Source-first Python 3.9+, Node/Fetch, .NET 6+, Java 17+, Lua 5.1+ | Not published to language registries | Vendor the complete client directory and pin its Rin revision |

Snapshot forward compatibility is a client-storage guarantee, not a server
round-trip guarantee. A 0.6 Runtime accepts OpenAPI-additive members inside
`Snapshot` and `SessionState` when `state_hash` still matches the state
projection understood by 0.6. It ignores those unknown members, however, and
does not reproduce them in a later Snapshot or State response. If a future
producer includes an unknown State member in `state_hash`, 0.6 fails closed
with `400 invalid_snapshot` instead of restoring an unverifiable projection.
Unknown members still count toward the complete 16 MiB inline Snapshot limit.

## Feature compatibility

| Feature | Effect when enabled | Without it |
| --- | --- | --- |
| `outcome-reporting-v1` | Game applies/rejects first, persists an Outbox, then reports; late outcomes merge by occurrence time | Historical fresh-head Commit and arrival-order behavior |
| `memory-archive-v1` | Bounded episodic detail plus deterministic lossy summaries | Newest bounded episodic window only |
| `belief-conflicts-v1` | Bounded sourced contradictory claims and a selected compatibility projection | Selected belief projection only |
| `goal-candidates-v1` | Policy may select only a complete game-supplied candidate Goal; adoption occurs after an accepted outcome | Candidate Goals are rejected |
| `actor-activity-v1` | Persisted region and awake/dormant state | Activity endpoint is rejected |
| `arbitration-v1` | World revision, advisory multi-Proposal arbitration, and atomic Batch Commit | Legacy per-Proposal coordination |

New integrations should normally include `outcome-reporting-v1`. Enable the
other Features only when the game persists and implements their corresponding
contract.

## Provider and Generation boundary

Rin strictly checks both raw game-to-Sidecar request bodies and successful
Provider JSON before decoding. Invalid UTF-8 and unpaired Unicode surrogates
are rejected. Decoded Generation content is then checked for emptiness, NUL,
byte size, and one top-level JSON object. A non-2xx Provider body is used only
for bounded error classification; it never becomes Generation content or
Session state and carries no content-validity promise. Games must still treat
successful decoded Generation content as untrusted and validate their domain
schema and canon.

## What hashes do and do not prove

Event hashes, Snapshot checksums, and checkpoint checksums detect inconsistent
or accidentally damaged data. They are unkeyed; they are not signatures, MACs,
or provenance proof. A party able to replace a complete history can recompute a
valid chain and its derived artifacts. Protect the data directory and backups
with external access controls.
