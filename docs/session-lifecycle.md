# Session lifecycle and data governance

[English](session-lifecycle.md) | [简体中文](session-lifecycle.zh-CN.md)

Status: accepted design for the 0.6 preview line. The OpenAPI contract and
implementations must follow this document.

## States

A Session has one irreversible lifecycle:

```text
active -> archived -> deleted tombstone
```

An active Session accepts normal mutations. Archiving is a durable, idempotent
transition that freezes the current event-chain anchor. An archived Session is
read-only: State, Timeline, Replay, Snapshot, Stats, and Session Transfer
export remain available, while Observe, Propose, Commit, activity, arbitration,
Restore, and import fail with `409 session_archived`.

Archiving is not a backup. Operators must export the complete Session Transfer,
verify its terminal `complete` frame and `stream_sha256`, and retain it in
independent storage before deletion when recovery is required.

Deletion is allowed only from the archived state. It atomically removes all
authoritative event and derived artifact files, then leaves a minimal tombstone.
The Session ID is permanently retired and cannot be created or imported again.

## Authenticated operations

All lifecycle routes use the server's existing Bearer authentication. A server
configured without a token remains suitable only for a trusted loopback
boundary.

`POST /v1/session/stats` accepts `protocol_version` and `session_id`. It returns
lifecycle state, revision/head, event count, exact event-log bytes, artifact
bytes, total managed bytes, configured soft/hard byte limits, and whether each
limit is exceeded. Stats must not load player content merely to count bytes. A
corrupt or unreadable Session fails closed and is never reported as absent.

`POST /v1/session/archive` requires a stable `request_id`, `session_id`, the
complete trusted expected Binding, and exact `expected_revision` and
`expected_head_hash`. All preconditions must match one loaded, verified Session
while its mutation gate is held. The Store persists an archive receipt with
the request identity, frozen anchor, timestamp, and canonical request digest.
An exact retry returns it with `duplicate=true`; any changed request is
`request_id_conflict`.

`POST /v1/session/delete` requires a stable `request_id`, `session_id`, the same
trusted expected Binding, archive receipt ID, frozen revision/head, and
`confirmation` equal to the exact Session ID. Confirmation is not
authentication; it makes accidental broad or misdirected deletion harder.
Wildcards, prefixes, empty IDs, and bulk deletion are not supported.

## Durable deletion

File Store deletion uses the per-Session event and artifact locks. It first
durably publishes the minimal tombstone as a fail-closed deletion intent, then
renames the Session directory to an internal deleting name, syncs the
`sessions` directory, removes the renamed directory, and syncs the parent
again. Startup sees the tombstone and finishes an interrupted deletion before
exposing the Store. Windows uses write-through rename; supported POSIX
platforms use rename plus directory sync.

The tombstone contains no Event, Snapshot, generated text, Actor, Fact, Goal,
or Binding values. It retains only:

- protocol/tombstone format version;
- Session ID;
- delete request ID and canonical request digest;
- deletion time;
- final revision and head hash;
- SHA-256 digest of the Binding;
- archive receipt ID (it is derived from the archive request digest).

This minimum makes deletion retries deterministic and prevents stale clients
from reusing a retired lineage identity. Tombstones need a separately governed
retention policy if even these identifiers are personal data.

## Capacity policy

The default remains unlimited for compatibility. Operators may configure
per-Session soft and hard managed-byte limits:

- crossing the soft limit succeeds and is exposed by Stats/readiness metrics;
- a mutation whose conservative encoded-size reservation would exceed the hard
  limit fails before append with `507 session_quota_exceeded`;
- exact retries of durable requests remain readable and reconcilable;
- Stats, export, archive, and deletion remain available over quota;
- derived artifact creation may be skipped, but event history is never
  truncated.

Limits cover the Event Log plus Store-managed Snapshots, checkpoints, indexes,
archive markers, and uncertainty metadata. Transfer staging has its own limits
and is not charged to an existing Session before atomic publication.

## Retention and privacy boundaries

Rin deletion cannot erase copies outside its data directory: game saves,
Session Transfer exports, filesystem/volume snapshots, cloud backups,
replicas, embedding-application logs, and model-provider systems need their own
policies. Operators must expire those copies before claiming erasure.

Deleting one event from the middle of a retained chain invalidates every later
hash, request receipt, Snapshot checksum, and transfer anchor. Rin therefore
supports complete Session retirement, not selective in-place event redaction.
Keep sensitive free text out of Rin when selective erasure is required.

Backups must capture the data-directory lease consistently or use verified
Session Transfer exports. Restoring a raw filesystem backup may also restore
archived Sessions and tombstones; reconcile it against the external deletion
ledger before serving traffic.
