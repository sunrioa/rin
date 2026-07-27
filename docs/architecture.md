# Architecture

[English](architecture.md) | [简体中文](architecture.zh-CN.md)

Rin is an engine-neutral control plane for agent state and decisions, not the
authority that simulates or mutates the game world.

This describes Rin `0.7.0` Preview. HTTP wire shapes are authoritative in
[`api/openapi.json`](../api/openapi.json); this document explains component and
trust boundaries.

## Authority boundary

```mermaid
flowchart LR
    G["Game engine\nworld authority"] -->|Observation| R["Rin runtime\nmemory + goals + policy"]
    R -->|ActionProposal| V["Schema + boundary + freshness validation"]
    V -->|action offer only| G
    G -->|Applied/rejected outcome report| R
    R --> E["Hash-chained event log"]
    R --> S["Checksummed snapshot"]
    R -->|"bounded prompt packet"| P["Optional model provider"]
    P -->|"structured draft"| V
```

The game engine always owns world authority. Rin never directly changes
scenes, quests, items, combat, character positions, critical choices, or
saves. A policy may choose only from the current request's fully bound
`offers`; the runtime also verifies actor, epoch, observation sequence,
deadline, capability digest, goal and memory references, boundaries, session
revision, and content binding.

## Components

### Host contract

The engine-neutral [`host` contract](host-contract.md) describes the owning
game process, its exact versioned capability implementations, per-decision
offers, authoritative epochs, and action-run outcomes. Capability discovery is
not action authorization: a policy selects only a game-authored bound offer,
and the adapter rechecks its schema, digest, expiry, epoch, and current
registration immediately before dispatch onto the engine authority thread.

The Host Contract currently exists as a local Go package. It neither changes
the HTTP `rin.protocol/v2` wire shape nor claims that existing language
adapters already implement the new registry.

### Protocol

`protocol` is the only layer other languages need to reproduce. Every request
explicitly carries `rin.protocol/v2`. The HTTP layer rejects unknown JSON
fields, and identifiers cannot contain path separators.

Observation `HostValidatedPayload` is inside the authenticated Host trust
boundary. The Host validates its data against the exact referenced game schema
before sending it; Rin only validates the bounded strict-JSON envelope and
preserves the schema identity. The digest is not a proof, and model or remote
provider output must never be copied into this field without Host validation.

### Runtime

`runtime.Engine` is a deterministic state machine. Each Session has its own
lock. Policy execution happens outside that lock, so a slow remote model does
not block new observations or state reads. Protocol v2 always uses the
game-authoritative typed action lifecycle and occurrence-time merge described
below. Sessions with
`arbitration-v1` use a `world_revision` that advances with authoritative
Observations and settled Outcomes, allowing several actors to propose in
parallel during one turn. Once the game has handled an Outcome, Rin records it
even when the report arrives after state has advanced.

Detailed memory keeps a fixed window. `memory-archive-v1` deterministically
selects a low-salience batch from the older half, then records a bounded,
lossy Summary with a tick range and representative source IDs. Hierarchical
text reserves an oldest head anchor, gives additional budget to important and
more recent fragments, and reserves the newest tail; source sampling retains
the oldest and newest known IDs and distributes the remaining slots over the
represented tick range. High importance increases retention budget but is not
a promise that text survives every future merge.

When the 32-Summary cap is crossed, Runtime continues to merge the oldest four
direct Summary lineages. That membership and Summary-ID derivation stay
stable because an older `proposal.created` event may persist one of those IDs
in `recalled_memory_ids`. `belief-conflicts-v1` keeps up to eight sourced
claims per actor while retaining the legacy `beliefs` field as the currently
selected projection. Both features are reconstructed entirely by event replay
and require no vector database.

Memory compaction is cognitive forgetting only: it does not delete or redact
the authoritative event log, erase permanent Identifier History, or provide a
privacy-erasure mechanism. Replay, checkpoints, backups, and retained
Snapshots can still contain text no longer present in bounded cognition State.

Durable request identity is deliberately separate from those bounded cognition
projections. Each managed Session retains an `identifier-history-v2` ledger
whose request entries bind a mutation kind and canonical typed-request digest
to its original result, while event entries tombstone every accepted Observe
or Outcome Event ID. `SessionState.Receipts` remains a 1,024-entry hot
projection for compatibility and diagnostics; evicting it never removes
Identifier History.

The request digest is SHA-256 over canonical JSON produced after strict typed
decoding. Exact retries therefore ignore object member order and whitespace but
must match every typed field and array order. A duplicate returns the original
Mutation revision/head or typed Proposal/Arbitration with `duplicate=true`.
Those coordinates identify the first operation rather than the current live
head.

### Decision provider

The `DecisionProvider` interface returns only a structured `DecisionDraft`.
The runtime does not trust its implementation: actions must come from the
allowlist, memory and goal IDs must exist, stance must be valid, and a
triggered actor boundary must select its game-authored response.
`DecisionDraft` has no free-form player-text fields.

The runtime is the single player-text information-flow gate. It always
rebuilds `ActionProposal.summary` from the selected game-authored
`ActionOffer.description` and `ActionProposal.rationale` from a fixed
stance template. Goal, boundary, memory, belief, prompt, and provider text are
not inputs to that function. This is a construction rule, not a secret-string
blacklist. `goal_id`, `boundary_id`, `recalled_memory_ids`, and
`policy_source`, plus the full `proposed_goal`, retain private structured
audit/integration data and must not be rendered directly in a player UI.
Except for its explicitly display-authorized `description`, an action's ID,
kind, targets, and parameters are also integration data unless the game
separately authorizes them for display.

Reducer projection `rin.reducer-projection/v2` applies the same reconstruction
to legacy `proposal.created` events, imported Snapshots, retained recent
actions, checkpoints, and durable exact-retry results. This changes only the
derived presentation projection: authoritative event bytes, event hashes,
request/result coordinates, actions, and audit IDs remain unchanged. An exact
retry of a pre-v2 Proposal can therefore return upgraded `summary` and
`rationale` while preserving its original revision and head. Raw event and
Restore payloads may still contain the old private strings and are not erased
by upgrading.

The built-in `policy.Deterministic` is the offline baseline:

1. If tags trigger a boundary, choose only its matching `refuse`, `redirect`,
   or `wait` action.
2. Otherwise, prefer the highest-priority active goal.
3. Select up to three memories by importance, recency, tags, and recall count.
4. Penalize repeated actions and break ties deterministically from a fixed
   seed and request context.

The online model policy replaces only steps 2 through 4. It never bypasses the
runtime validator.

### Model policy

The model policy builds a minimal context packet. System instructions and
game data are separate messages. Player input, story text, and content-pack
fields all live under `untrusted_game_data`; a separate `contract` lists the
only legal action, memory, and goal IDs. Even when a provider does not support
strict JSON Schema, the result still receives local unknown-field, type,
and ID-allowlist validation. The output schema has no `summary` or `rationale`
property; returning either is an unknown-field failure. The prompt explicitly
forbids copying private decision text, and the runtime player-text gate remains
authoritative even for custom policies or non-conforming providers.

Character boundaries are handled locally before calling a provider. A
triggered boundary uses `boundary-guard` directly instead of relying on the
model to refuse.

### Provider resilience

The OpenAI-compatible client uses only the standard library. Each call has an
attempt timeout and total timeout. Only temporary failures such as network
errors, 429, 408, and 5xx responses are retried. Repeated failures open a
circuit breaker; while open, calls immediately use the bounded deterministic
policy without issuing provider requests.
Raw Provider HTTP bodies, prompts, and keys are never written to errors, logs,
or durable Session state. A validated structured Generation result is retained
in its bounded process-local Job record and semantic cache until returned; it
remains untrusted caller-facing content. Attempt and total deadlines rely on
the `provider.StructuredGenerationProvider` cooperative cancellation contract: an implementation
must observe `ctx.Done()` and return promptly. Go cannot forcibly preempt a
third-party client that blocks forever.

Model drafts use a bounded in-memory cache keyed by lineage generation,
revision, world revision, head hash, a digest of the complete decision Actor
state, and the semantic request. Concurrent calls with the same key collapse
into one provider request. Restore and Transfer lineage changes therefore
cannot reuse a draft produced for a different authority state, even when a
world revision repeats.

The permanent Request/Event identity ledger uses one bounded 512-entry hot map
per loaded Session. Older identities are sealed into immutable encoded
segments with Bloom routing; a normal new identifier avoids decoding cold
segments, while an old exact retry decodes only candidate segments. Snapshot
and checkpoint creation capture immutable segment references under the Session
lock and materialize the complete protocol `IdentifierHistory` outside it.
The public Snapshot and Transfer formats therefore remain complete and
unchanged, but a million-turn Session no longer requires a million-entry Go
map to remain resident.

### Async jobs

`jobs.Manager` uses bounded workers and a bounded queue. A game first submits
`/v2/jobs/propose`, continues rendering and accepting input, then polls with
GET. If the session changes while an actor is thinking, the job ends as
`stale` and no obsolete proposal is written. Cancellation propagates through
context to the HTTP provider.

Job metadata remains in process memory and may expire after its retention TTL.
A successful Proposal is already in the event log. After Job eviction or a
sidecar restart, a client may resubmit the exact request; the Engine's durable
Session identity ledger returns the original Proposal even though the
process-local Job record is reconstructed. Job timestamps and intermediate
status are not durable.

Canceling a running Proposal Job waits only within the caller's request
context. If the policy or Store has not published the authoritative race result
before that deadline, DELETE returns `408 job_cancel_incomplete`; callers must
continue polling or exact-retry and must not assume that no Proposal was
persisted.

### Structured generation

`generation.Manager` provides another bounded asynchronous queue for
game-owned constrained prompts. It reuses the resilient provider but does not
read Session State or write to the event log. Same-request deduplication lasts
only while the process-local Job record is retained; semantic content after
removing the request ID is cached briefly. After eviction or restart, an exact
request may invoke the provider again. Cancellation propagates to the provider.

Encoded Generation requests, retained Job results, and semantic-cache results
share a 64 MiB default payload budget. Active requests are never evicted;
expired entries are cleaned by a joined periodic worker, and terminal Jobs or
old cache entries are evicted before a new payload is admitted.

Generation guarantees only transport, size, and a valid top-level JSON
object. Each game must still validate its own `ScenePacket`, quest, dialogue,
or ending schema. If validation fails, the game discards the result and uses
local content. Model output never becomes canon automatically.

### Game adapters

Ren'Py, Godot, and Unity adapters translate JSON/HTTP and engine-specific
asynchrony without copying the runtime state machine. The Host matches the
selected Action Offer to its durable Pending Turn, revalidates epoch and
deadline, executes or rejects under game rules, then persists an exact typed
Report. Submit, poll, timeout, or cancellation ambiguity fails closed and is
resumed with the exact request identity.

JavaScript, C#, Java, Lua, Godot, and Unity `WorkflowCoordinator` implementations own this
protocol-generic Pending Turn state machine, but they cannot create a storage
guarantee. Before apply, they enforce the integration's declared
[host durability profile](host-durability.md). The host still owns
stable identity, persistence, action validation, engine-thread dispatch, and
the world mutation.

The Ren'Py worker registry, Godot `HTTPRequest`, and Unity coroutines exist
only in process memory. A game save stores snapshots and plain results, never
threads, futures, sockets, HTTP objects, or API tokens.

### Multi-actor coordination

The game supplies the upper bound and semantic scope of candidate goals. A
policy may only recommend adopting one; the game applies it and reports an
accepted terminal action before Rin writes the goal into an actor. The game's region or
simulation system updates activity state. Dormant actors never wake
themselves. Arbitration stably sorts proposals at the same world revision and
records conflicts, but it does not execute actions. The game may adjust or
reject them and then report actual outcomes through an atomic Batch Action
Report. See the [Host action lifecycle](action-lifecycle.md) for the full transaction
and Outbox rules.

This lets Rin support visual novels, RPG NPCs, and simulation residents
without taking responsibility for pathfinding, collision, quest rules, or a
scene tree.

### Observability

Timeline extracts only IDs and enum states from event payloads. It does not
return the player's original words, story summaries, action outcomes, or
model content. On the bundled file store, Timeline reads a bounded revision
range and does not rerun the reducer over the complete log for every page.
Replay uses the newest usable checkpoint at or before the selected revision,
then runs the normal reducer over the remaining tail and produces a complete,
verifiable Snapshot without writing an exported Snapshot to the store.
Once the Session is loaded, Timeline and Replay capture their live-session
boundary under the Session lock, then perform range I/O and replay after
releasing that mutation lock. A first lazy load remains serialized.
`rin inspect` exposes a read-only Store view for machine-readable diagnostics.
It replays the selected Session from genesis and never creates directories,
finishes pending maintenance, writes checkpoints, or repairs derived index
files. With a healthy revision index it locates the requested trailing
Timeline window directly; a missing or invalid index is rebuilt only in
memory.

Replay State is revision-specific, but its Snapshot carries the complete
local-lineage Identifier History, including tombstones created after the
selected State revision. Otherwise, restoring an old Replay result would make
later IDs reusable. Identifier result revisions can consequently exceed the
replayed State revision.

Opening an Engine is intentionally lazy: it enumerates Session IDs but does
not read every Session history. The first operation on a Session verifies and
loads that Session through the checkpoint-and-tail recovery path.
After successful recovery, Runtime best-effort asynchronously queues a
checkpoint at the recovered head when no usable checkpoint was selected, or
when the selected checkpoint tail has reached 16,384 events. The checkpoint
may not be durable when the read returns. This derived cache write is not part
of read success and its failure is ignored; the [Store](#store) section
describes the bounded worker and concurrency contract.
`Engine.VerifyAll()` is the explicit maintenance operation for a
checkpoint-independent, genesis-to-head replay and hash-chain audit of every
Session. `Engine.Scrub(ctx, maxEvents)` provides the same
checkpoint-independent reducer and identifier validation incrementally: it
captures a Session head, preserves an in-memory cursor across calls, and never
processes more than the supplied event budget. The bundled Sidecar starts this
scrub in the background and bounds each pass by both an event budget and a
timeout. Ordinary `rin inspect` reads only its requested Session, reports
`"mode":"read-only"`, and does not implicitly perform a data-directory-wide
audit.

### Mutation and state closure

Every event is first applied to an isolated candidate state. The reducer then
validates the complete `SessionState`, including feature-gated fields,
capacities, revision and tick bounds, actor references, and paired belief
projections. Only a valid candidate may be appended to the Store and published
as the live state. A reducer or candidate-validation failure therefore leaves
both the event log and the in-memory session unchanged. Store write failures
use the separate append-confirmation and reconciliation rules described by the
outcome protocol.

The same durability boundary applies to Identifier History. A successful
append publishes State and its request/Event ID entries together. A failed or
uncertain append cannot expose a tombstone without its event or expose an event
without its tombstone; reconciliation derives both from the confirmed durable
tail.

When a Store error leaves append durability unknown, the Engine does not
publish the candidate State or Identifier History. It retains an uncertainty
barrier for the exact logical event: only the same mutation kind and canonical
typed-request digest may attempt confirmation, while every other Session
mutation fails closed behind it. Non-Proposal operations surface
`mutation_outcome_unknown`; Proposal keeps `proposal_outcome_unknown` for wire
compatibility. A successful exact retry reconciles the confirmed durable tail
and publishes State plus Identifier History once. Create and fresh Restore use
the same rule before registering the Session in memory.

Policy calls receive isolated copies of the State, Actor, and request. Policy
code may inspect or mutate those values locally, but it cannot mutate the live
session outside an event. Runtime-owned collections also close their
references when bounded retention runs: memory compaction rewrites recalled
IDs to the replacement Summary, non-archive eviction removes those references,
and Belief/BeliefSet eviction is deterministic and paired.

### Store

All Store operations for one Session must be linearizable,
and `Load` must be read-after-write consistent with `Create` and `Append`.
The Engine treats
`ErrNotFound` immediately after a failed Create as proof that no first event
was written, and an unchanged authoritative tail immediately after a failed
Append as proof that the candidate event was not written. A custom Store that
cannot make either observation authoritative must return an uncertainty error
from `Load`, never stale data; eventually consistent implementations do not
satisfy the runtime Store contract.

File-store layout:

```text
rin-data/
├── .rin.lock
└── sessions/
    └── session.id/
        ├── events.jsonl
        ├── events.idx
        ├── checkpoint-<revision>-<hash>.json.gz
        └── snapshot-<revision>-<hash>.json
```

An event hash covers sequence, type, request ID, recorded time, previous event
hash, and payload. `events.jsonl` is the authority and uses
`retain_forever`: Rin does not automatically delete or compact events because
Replay, permanent request/Event ID identity, and audit depend on the lineage.
Operators must plan capacity, backup, and archival around that policy rather
than deleting an active log behind Rin.

The chain uses unkeyed SHA-256. It detects a broken or inconsistently edited
history, but it is not a signature, MAC, or provenance proof. A party able to
replace the complete log can recompute every event hash and derived artifact.
Adversarial tamper resistance therefore requires external access controls and,
if needed, an independently protected anchor.

`events.idx` is a derived revision/offset/hash index used for head and bounded
range reads. A missing, stale, or malformed index is rebuilt atomically from
`events.jsonl`; the rebuild performs one complete log scan. A healthy index is
cached after first access, so later Timeline pages do not repeatedly scan or
materialize the complete event log. Deleting the index is safe, but the next
access pays the rebuild cost.

The base `Store` API remains source-compatible. Optional `RangeStore` supplies
`Head` and bounded `LoadRange`; optional `CheckpointStore` supplies
`LoadCheckpoint` and `SaveCheckpoint`. Checkpoint acceleration requires the
same Store to implement both interfaces, because Runtime uses `RangeStore` to
validate each checkpoint's event-chain anchor. An internal checkpoint uses
`CheckpointFormatVersion = "rin.checkpoint/v1"` and
`ReducerProjectionVersion = "rin.reducer-projection/v2"`. Projection v2
introduces fair bounded-memory text/source sampling and canonical,
game-authored Proposal presentation. Summary lineage IDs remain compatible
with v1 so persisted recalled references still replay. A v1 checkpoint is
obsolete and falls back to an older compatible candidate or genesis; the
authoritative event log is unchanged. A checkpoint is a derived cache, not a
public Snapshot, backup, or source of authority, and carries the
Session/revision/head anchor, lineage epoch, complete State and Identifier
History projection, and a checksum. Before use, Runtime validates that wrapper,
the projection version and checksum, the enclosed Snapshot, and the matching
event-chain anchor. A missing, corrupt, obsolete, or mismatched checkpoint is
skipped in favor of an older candidate or genesis replay. The checksum detects
accidental corruption; it is not authentication or provenance proof.
Checkpoint write failure never reverses an already durable mutation or fails a
successfully recovered read.

Optional `TransferStore` supplies `BeginTransfer`, which returns a
single-consumer `TransferWriter`. The bundled File Store writes each verified
EventRecord into an invisible same-root staging directory and builds its
derived index incrementally. A checksum, sequence, chain, truncation, or write
failure cannot create the target Session. `Publish` syncs the complete staged
log and index, then exposes them with one same-directory atomic rename; `Abort`
is idempotent, and startup removes abandoned `.transfer-*.tmp` directories.
The target Session lock is held for the writer lifetime, so callers must always
abort a failed or cancelled import. Stores without this optional capability
must return transfer-unavailable at the Runtime boundary rather than simulate
import through public `Create`/`Append`.

Runtime export requires `RangeStore`: it captures revision, head, Binding, and
lineage generation under the Session lock, then releases mutation serialization
and reads only bounded pages through that immutable anchor. A concurrent
mutation is not included. Runtime import checks the caller's trusted Binding,
replays State and writes complete Identifier History directly into the final
segmented Ledger while staging, checks the manifest boundary, lineage
generation, and configured identity-index byte budget, then publishes once. A
bounded-page genesis-to-head readback verifies every event hash and compares
rebuilt State with the stream; it does not build a duplicate identity map
already committed by that hash chain. Both import and export require
`RangeStore`. Runtime never falls back to aggregate `Store.Load` or public
`Create`/`Append` for Transfer.

Runtime queues a revision-1 checkpoint after Session creation (including a
fresh Session created by Restore). It then uses a hierarchical schedule:
power-of-two revisions from 256 through 8,192, followed by every 16,384
revisions. This keeps the replay tail below 16,384 events without repeatedly
serializing a large permanent Identifier History every 256 events. Successful
lazy recovery queues a repair when no usable checkpoint exists, or when the
selected checkpoint tail has reached 16,384 events; a smaller valid tail does
not cause an exact-head rewrite on every restart. Sessions below revision 256
are the deliberate exception: after falling back to an older checkpoint they
repair exact head, which cheaply replaces a rejected same-name derived
artifact.

Checkpoint construction and persistence are best-effort asynchronous work.
While holding the Session mutation lock, Runtime captures the immutable
published State reference plus immutable Identifier Ledger segment references
and copies only the bounded hot identifier maps. Full materialization,
validation, hashing, and `SaveCheckpoint` I/O run outside that lock. Within one
Engine, each managed Session has at most one worker and one latest pending
capture, so crossing several thresholds while a save is active coalesces to
the newest pending revision. Multiple Engines sharing a Store can each have
such a worker. A mutation or successful lazy read does not wait for the
derived checkpoint to finish, and the checkpoint might therefore not be
visible immediately when the call returns.

`SaveCheckpoint` may run concurrently with `Append`, `Load`, `Head`, or
`LoadRange` for the same Session. File Store checkpoints use gzip because they
are large, rebuildable projections; the authoritative event log remains plain
JSONL. A CheckpointStore must be concurrency-safe
and isolate expensive derived-artifact work from synchronization needed by
authoritative event operations. `Engine.Close(ctx)` prevents new operations
and waits for in-flight calls, transfer imports, and checkpoint workers before
the caller closes its Store. Cancellation bounds that wait but cannot preempt
a non-compliant Store that blocks `SaveCheckpoint` forever; a timed-out Engine
remains closed and `Close` may be retried after the dependency is released.
The file store keeps the two newest valid
checkpoint files per Session. Checkpoints deliberately do not use the public
16 MiB inline Snapshot ceiling, because they never cross the Snapshot JSON API
boundary. They remain sensitive event-log-level state.

Public Snapshot files are named by revision and State hash, but their contents
are not immutable by path. The file store atomically replaces the same path to
repair a damaged artifact or persist the same State revision/hash with newer
Identifier History. Consumers must validate `identifier_history_hash` and must
not treat the filename as the complete Snapshot identity. The file store keeps
the two newest valid Snapshot files per Session. This retention applies only
to those local files; it neither truncates Identifier History nor changes the
16 MiB inline Snapshot/Replay/Restore contract.

Snapshot `state_hash` covers bounded State. `identifier_history_hash`
independently covers canonical `identifier_history`, including its
`identifier-history-v2` version and `coverage_complete` marker. History retains
original Proposal/Arbitration results, so it grows linearly with mutations and
may re-expose text already evicted from cognition State. Snapshot files and
bodies require the same confidentiality and integrity controls as the full
event log. These SHA-256 values are canonical checksums: they detect accidental
damage or an edit that did not update the checksum, but they are not signatures
or provenance proof and cannot stop an editor who can recompute them. A
Snapshot is trusted, opaque serialized state, not an untrusted import format.

The file store obtains a non-blocking exclusive lease on `.rin.lock` before
opening the data directory. A second process fails to open the same directory;
the lease remains held until `(*store.File).Close`, which is idempotent and
waits for in-flight Store calls. Embedded users must therefore always call
`Close`; the `rin serve` and `rin inspect` commands do so automatically.
The bundled exclusive data-directory lock supports `darwin`, `linux`, and
`windows`. Unix uses non-blocking `flock`; Windows uses an exclusive file handle
opened without sharing, and the operating system releases the lease at process
exit. On every other GOOS, `store.OpenFile` returns
`ErrDataDirectoryLockUnsupported` and fails closed. Multi-instance deployments
must implement an externally coordinated Store instead of sharing a JSONL
directory.

The bundled file store is supported only on a local filesystem where exclusive
file locking, same-directory atomic rename, and the platform durability
primitives below have reliable local semantics. NFS, SMB, FUSE mounts, and
cloud-synchronized directories are unsupported even for one Rin process. Put
an externally coordinated Store in front of remote or shared storage instead
of pointing the JSONL store at it.

File creation and append sync `events.jsonl`; the corresponding index write is
synced separately. On Unix, new Session directories are renamed into place and
their parent directory is synced. Snapshot, checkpoint, and rebuilt-index
publication uses a synced temporary file, rename, and (on Unix) directory sync;
retention deletion is followed by another Unix directory sync. Unix temporary
files use `0600`. Windows files inherit the data-root ACL, which operators must
restrict to the Sidecar account. Unix uses file/directory `fsync`. Windows uses
`FlushFileBuffers` for files and `MoveFileExW(MOVEFILE_WRITE_THROUGH)` for
published renames; Windows does not document `FlushFileBuffers` for directory
handles. A crash after a durable event but before its index update leaves a
stale derived index that is rebuilt from the log. These are local-filesystem
crash-consistency measures, not a guarantee against storage hardware, kernel,
filesystem, backup, or operator failures.

Lazy loading changes where cost is paid; it does not make unbounded lineage
free. Engine Open is proportional to Session-directory enumeration rather
than every log body. A Session's first access must read its index and complete
Identifier History; a missing or unusable index triggers an
`O(total events)` log scan. With a usable checkpoint, state reconstruction
then scales with the checkpoint body plus its event tail. Steady-state
Timeline pagination scales with the requested range, while Replay scales with
the selected checkpoint tail and with the complete Identifier History carried
in its result. `Engine.VerifyAll()` intentionally remains
`O(total event-log bytes)` for an independent full audit. `Engine.Scrub`
spreads that same work over bounded passes; its active cursor retains one
Session projection and is discarded at the captured head.

Legacy entries whose full request digest cannot be recovered, or whose ID was
historically reused, become ambiguous tombstones: the old log remains
readable, but a later request cannot safely reuse that ID.

## NPC scheduling

Each actor declares `think_every_ticks`. After the game applies an action and
reports an accepted terminal lifecycle,
`next_think_tick = max(current, report.tick + think_every_ticks)`, so a late
report cannot move scheduling backward. A game may call
`/v2/scheduler/due` when entering a region, ending a turn, advancing time, or
handling a critical event. It should never poll a model from render frames.

An urgent event may set `urgent: true` on a propose request. Urgency bypasses
only scheduling time, never boundaries or the action allowlist.

## Save and rollback

- Game saves should store snapshots returned by Rin, not internal file paths.
- A snapshot carries the content-pack binding and state hash. Rin validates a
  cloned State before hashing or saving it, so every successfully returned
  snapshot passes the same structural validation used by Restore.
- Restore requires `expected_binding` from the running game's trusted content
  manifest; callers must not derive it from the imported Snapshot. It must
  match `snapshot.state.binding`. For an existing target Session, that Session
  is the third participant and its binding must also match; a fresh target is
  initialized only after the first two match.
- A new Snapshot also carries `identifier_history` and
  `identifier_history_hash`. The history is outside bounded State and retains
  permanent request/Event ID tombstones plus original operation results.
- A legacy Snapshot without history remains readable, but its coverage is
  permanently incomplete: only IDs still discoverable from its bounded State
  can be seeded. `coverage_complete=false` is sticky across all later Snapshot
  and Restore merges.
- Restore retains pending proposals so a saved, unhandled Proposal Attempt can
  resume, and so a game-save Outcome Outbox can
  report actions already applied before the save. Restored proposals never
  authorize execution; the game must use its persisted Attempt and
  applied-operation marker to distinguish the states, revalidate any action
  that was not already handled, and never repeat one that was.
- Committed events, memories, facts, goal progress, and scheduling ticks are
  restored.
- Restore starts a new local event-chain generation. Retained Proposal,
  Memory, Belief, Activity, and Arbitration revision metadata is rebased to
  that generation before the restored State is published. Imported historical
  Receipt revisions are set to zero; the new Restore Receipt records the local
  generation.
- Restore unions the current and imported Identifier Histories. IDs from an
  abandoned future branch remain tombstoned; incompatible verified mappings
  reject Restore instead of being overwritten.
- A duplicate result imported from another generation retains the original
  operation revision/head. Those coordinates may not be replayable in the new
  local chain and must not be treated as its current head.
- A new data directory may import a snapshot; its local event chain then
  begins with a restore event.
- When loading the same save repeatedly, callers should bind the restore
  request ID to both the saved snapshot hash and current sidecar head. This
  distinguishes a network retry from a real second rollback.
- Identifier History grows with the lineage. Complete compact inline Snapshot
  JSON is capped at 16 MiB and is never truncated; Snapshot, Replay, or Restore
  returns `413 snapshot_too_large` when that ceiling is exceeded. The server's
  default request-body limit and every bundled client's default response limit
  are 32 MiB, leaving envelope, Restore, and EventRecord headroom. Such a
  lineage cannot use the inline JSON Snapshot, Replay, or Restore endpoints.
  It retains a bounded-memory migration and backup path through the NDJSON
  Session Transfer export/import endpoints and JavaScript/C# stream helpers.
- Live `SessionState` has a separate compact-JSON budget so the State endpoint
  cannot grow beyond every bundled SDK's response limit. It defaults to
  16 MiB and may be configured up to 24 MiB with
  `-session-state-max-bytes` / `RIN_SESSION_STATE_MAX_BYTES`. Create, mutation,
  and Transfer Import calculate the next complete State before persisting the
  Event and return `413 state_too_large` without changing the log. Replay also
  enforces the configured budget; operators must explicitly raise it, within
  the 24 MiB envelope-safe ceiling, before opening older data created with a
  larger limit.

## Model integration rule

Implement model access as another `Policy`, or let a higher-level showrunner
produce a structured draft first. Provider requests must have timeouts and
cancellation. Read API keys only from the process environment or secure host
storage. Models receive no event files, snapshot paths, game scripts, or
arbitrary tool execution.
