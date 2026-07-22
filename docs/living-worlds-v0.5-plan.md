# Rin v0.5 Living Worlds Implementation Plan

Status: approved implementation baseline

## 1. Objective

Rin v0.5 turns the current single-character-compatible runtime into a small,
engine-neutral living-world foundation without giving a model authority over
the game world. The release must support long-running character memory,
conflicting private knowledge, bounded autonomous goals, region-aware actor
scheduling, multi-actor arbitration, and inspectable replay.

The invariant remains:

```text
model or deterministic policy -> proposal
game rules                  -> apply or reject
Rin                         -> record the observed result
```

The first production consumer remains `ai-galgame`, but every new contract is
defined in engine-neutral terms and covered by Go tests before a game adapter
uses it.

## 2. Constraints

- Keep `rin.protocol/v1`; additions are optional fields and new endpoints.
- Existing create/observe/propose/commit/snapshot requests remain valid.
- New state fields use `omitempty` so old snapshot hashes can still verify.
- Living-world behavior is enabled through session feature flags. Old sessions
  retain v0.4 retention and scheduling behavior.
- The core continues to use only the Go standard library and remains CGO-free.
- The model never invents executable actions, targets, goals, files, tools, or
  game-state mutations outside a game-supplied contract.
- Player text, prompt text, provider responses, and credentials are not added
  to operational logs or error messages.
- Game rendering, navigation, physics, combat, inventory, quests, consent,
  purchases, and canonical story state remain engine-owned.

## 3. Feature Negotiation

`CreateSessionRequest.features` accepts a bounded set of identifiers:

| Feature | Purpose |
| --- | --- |
| `memory-archive-v1` | Deterministic episodic compaction and summary recall |
| `belief-conflicts-v1` | Preserve contradictory actor-local claims |
| `goal-candidates-v1` | Allow a policy to select a bounded candidate goal |
| `actor-activity-v1` | Persist region and dormant/awake actor activity |
| `arbitration-v1` | Record deterministic multi-proposal arbitration |

Unknown feature identifiers fail session creation. `/health` advertises the
supported list so adapters can fail closed or omit unsupported features.

The current `ai-galgame` integration enables memory archives and belief
conflicts. It does not enable autonomous goal candidates or arbitration until
the content pack supplies explicit candidates and multi-actor scenes.

## 4. Memory Model

### 4.1 Episodic memory

`ActorState.memories` remains the recent, quote-capable event stream. Existing
retrieval scoring by importance, recency, tags, quotes, and recall count stays
available.

When `memory-archive-v1` is enabled and the episodic limit is exceeded:

1. Select a deterministic low-salience batch from the older half of memory.
2. Preserve importance-five events until no lower-salience candidate remains.
3. Create a level-one `MemorySummary` containing a bounded joined summary,
   unioned tags, source event IDs, tick range, importance, and compaction reason.
4. Remove only the source episodes represented by that summary.
5. When summary capacity is exceeded, merge the oldest summaries into a higher
   level instead of silently deleting them.

Summary identifiers are content hashes. Replay therefore produces the same
archive independent of map iteration order or wall-clock time.

`MemorySummary.reason` explains why detail was compacted. Source event IDs and
tick ranges let a developer trace what was retained without storing unbounded
verbatim text. Policy retrieval may return episodic or summary IDs; accepted
commits update recall counters on either type.

### 4.2 Compatibility

Sessions without `memory-archive-v1` continue to retain the newest 128 episodic
memories exactly as v0.4 did. Old event logs therefore keep their historical
replay semantics unless a newly created session explicitly opts in.

## 5. Actor-Local Knowledge

Observation visibility remains the primary privacy boundary: only actors in
`observer_ids`, and in a fact's optional visibility list, receive a memory or
claim.

With `belief-conflicts-v1`, each `(subject_id, predicate)` stores a bounded
`BeliefSet`:

- all distinct recent claims and their source event IDs;
- confidence and observed revision;
- the currently selected claim;
- an explicit `conflicted` flag when distinct objects coexist.

The existing `ActorState.beliefs` map remains as a compatibility projection of
the selected claim. Selection is deterministic: higher confidence wins, then
newer revision, then lexical object order. Rin does not silently convert a
rumor into world truth and never copies one actor's claims to another actor.

Model prompts receive only the requesting actor's bounded selected beliefs and
conflict summaries. No global omniscient state is introduced.

## 6. Bounded Autonomous Goals

`ProposeRequest.candidate_goals` may contain zero or more complete `Goal`
templates. A policy may reference:

- an existing active actor goal; or
- one candidate goal supplied for this request.

When a candidate is selected, the resulting `ActionProposal.proposed_goal`
embeds the exact template. The goal is not part of actor state until the game
accepts the associated action commit. Rejected or stale proposals never create
goals.

This supports character initiative while preserving authority. A game can
offer goals such as "ask about the damaged camera" or "finish the bridge
repair", but a model cannot create a purchase, romance escalation, quest,
target, or irreversible objective the game did not advertise.

## 7. World Revision and Multi-Actor Arbitration

### 7.1 World revision

Event-log revision changes for every persisted event, including proposals.
Multi-actor work also needs a revision that changes only when observable world
state changes. `SessionState.world_revision` is therefore introduced:

- increments on create, observe, accepted/rejected commit, actor activity, and
  restore;
- does not increment merely because another actor created a proposal or an
  arbitration record;
- is copied into each new proposal.

This lets several actors propose against one stable world state. A normal
single commit remains valid after unrelated proposals, but becomes stale after
an observation, activity change, restore, or another committed outcome.

### 7.2 Arbitration

`POST /v1/world/arbitrate` receives pending proposal IDs and a bounded set of
exclusive target IDs. Rin ranks proposals deterministically by active-goal
priority, proposal tick, actor ID, and proposal ID. It returns:

- `selected`: no higher-ranked proposal claimed the same exclusive target;
- `deferred`: an earlier winner claimed at least one target;
- a player-readable reason and conflicting proposal IDs.

Arbitration is advisory and persisted for debugging. It does not execute an
action or resolve a proposal.

`POST /v1/action/commit-batch` records outcomes for proposals produced against
the same world revision in one atomic event. The game must apply all selected
actions through its own systems before committing. If any item is invalid or
stale, the entire batch is rejected.

## 8. Region Activity and Scheduling

`POST /v1/session/activity` persists bounded actor updates:

- actor ID;
- region ID;
- `awake` or `dormant` state;
- game-authored reason and tick.

Dormant actors are excluded from `/v1/scheduler/due` and cannot propose until
the game wakes them. `DueAgentsRequest.region_ids` optionally restricts the
query to currently loaded regions. Empty region filters preserve current
behavior.

Games update activity on region load/unload or simulation schedule changes,
not on render frames. Crowds can continue to use deterministic policy while
named nearby actors use model policy.

## 9. Timeline and Replay

Two read-only operations support debugging:

- `/v1/session/timeline`: bounded event headers and safe structural metadata;
- `/v1/session/replay`: reconstruct and validate state at a requested revision.

Timeline responses omit observation summaries, quotes, prompt text, provider
content, tokens, and credentials. Replay returns protocol state and may expose
story data already present in that authenticated session, so remote endpoints
must use the existing bearer-token boundary.

`rin inspect` opens a data directory, verifies every hash chain through normal
runtime replay, and prints a JSON session summary. Optional revision selection
uses the same replay implementation as the HTTP endpoint.

## 10. Engine Adapters

### Ren'Py

- Add plain-dictionary methods for activity, arbitration, batch commit,
  timeline, and replay.
- Keep all HTTP and polling objects process-local.
- Enable only memory and belief features for `ai-galgame` v1.2 content.
- Preserve authored fallback when Rin is disabled or unavailable.

### Godot 4

- Add coroutine helpers for activity, due-agent queries, arbitration, and batch
  commit.
- Keep navigation, animation, combat, inventory, and scene-tree mutation in
  Godot.

### Unity

- Add serializable request/response DTOs and coroutine methods for the same
  endpoints.
- Continue to use `UnityWebRequest` and bounded downloads without packages.

Adapters do not run an agent loop every frame. The engine owns simulation
ticks and decides when an actor deserves a proposal job.

## 11. Implementation Phases and Commits

### Phase A: plan and compatibility contract

- Add this document and update the roadmap.
- Record baseline Go and game test results.
- Commit: `docs: plan living worlds runtime`.

### Phase B: cognition

- Add feature negotiation and optional protocol fields.
- Implement memory archive compaction, summary retrieval, snapshot validation,
  and deterministic replay tests.
- Implement belief sets and conflicting-claim prompt projection.
- Commit: `feat: add long-term actor cognition`.

### Phase C: autonomy and world coordination

- Add candidate goals and commit-time adoption.
- Add world revision semantics.
- Add actor activity, region filtering, arbitration, and atomic batch commit.
- Commit: `feat: coordinate living world actors`.

### Phase D: observability and adapters

- Add timeline/replay APIs and `rin inspect`.
- Extend Ren'Py, Godot, and Unity adapters and examples.
- Update protocol, architecture, RPG, model-policy, and security docs.
- Commit: `feat: add living world tooling and adapters`.

### Phase E: game integration

- Enable compatible cognition features when `ai-galgame` creates a new Rin
  playthrough session.
- Extend compatibility vectors and process-level integration checks.
- Keep old saves and classic mode unchanged.
- Commit in the game repository: `feat: enable Rin living memory`.

## 12. Automated Verification

Rin acceptance requires:

- `go test ./...`;
- `go test -race ./...`;
- `go vet ./...`;
- deterministic replay produces identical state and summary IDs;
- old v0.4 fixtures and snapshots still validate;
- memory never exceeds episodic or archive bounds;
- private claims never appear in an unlisted actor;
- conflicting claims survive snapshot/restore;
- a candidate goal is added only by an accepted commit;
- dormant actors are neither due nor allowed to propose;
- arbitration order is deterministic under shuffled input;
- batch commit is atomic and rejects mixed revisions;
- timeline output contains no observation quote or summary;
- macOS arm64/amd64, Windows amd64, and Linux amd64 builds succeed;
- Ren'Py adapter tests and compatibility vectors pass.

Game acceptance requires:

- the complete Python suite;
- Rin boundary and source scans;
- key-free real-process Session -> Observation -> Proposal -> Arbitration ->
  Commit -> Snapshot -> Restore check;
- Ren'Py lint and compile when the SDK is available.

## 13. Manual Verification Deferred by Lock Screen

- Inspect memory and relationship screens at supported desktop resolutions.
- Play online across multiple chapters and confirm recalled lines feel natural.
- Save, create a different future, reload, and confirm memory rewinds.
- Stop Rin during a request and confirm offline continuation remains responsive.
- Evaluate whether autonomous questions are varied without becoming intrusive.
- Run a small Godot or Unity scene with at least three competing NPCs.

## 14. Release and Rollback

- Existing sessions do not gain living-world features automatically.
- A game removes feature identifiers to return new sessions to v0.4 semantics.
- No event migration rewrites JSONL files in place.
- A failed new endpoint cannot corrupt an existing session because all writes
  validate first and append one hash-chained event atomically.
- The game can disable Rin and continue with authored content at any point.

## 15. Stop Condition

Implementation is complete when all automated checks pass, each phase has a
local commit, `ai-galgame` can opt into cognition without changing canonical
story authority, and only the manual GUI, cross-engine scene, long-play, and
human quality checks remain.
