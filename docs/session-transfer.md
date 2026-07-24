# Scalable Session Transfer Design

[English](session-transfer.md) | [简体中文](session-transfer.zh-CN.md)

## Status

This is the implementation decision for a future Session Transfer facility.
Protocol frame types, structural validators, and checksum primitives now exist,
but Rin does not yet expose export or import operations. Until the Store,
Runtime, HTTP, contract, and SDK work is complete, the existing 16 MiB inline
Snapshot limit remains authoritative.

## Problem

Rin retains the authoritative Event Log and Identifier History for the complete
lineage, while portable Snapshots travel as one JSON object with a 16 MiB
compact-JSON ceiling. A long-lived Session will eventually lose Snapshot,
Replay, and Restore through the current HTTP API. Raising the body limit only
postpones the failure while preserving unbounded allocations, proxy limits, and
full retransmission after an error.

Session Transfer must provide a bounded-memory, verifiable, recoverable
export/import path for every valid lineage while preserving game authority,
original Event Records and their hash chain, complete Identifier History, the
trusted Binding boundary, permanent identifier semantics, and inline Snapshot
compatibility.

## Decision

### 1. Use a framed stream

Transfer uses UTF-8 NDJSON framing. Every line is a closed JSON object with an
explicit `type`. Ordering is fixed: one `manifest`, one or more `event` frames,
then one `complete` frame.

The manifest carries transfer/protocol/projection versions, Session ID, Binding,
start and terminal revision/head, event count, a random `transfer_id`, and
algorithm bounds. Every event frame contains one original `EventRecord` and a
SHA-256 digest of its canonical JSON bytes. Export must not rewrite Data,
RecordedAt, Hash, or any other authoritative member.

The complete frame repeats the terminal revision, head, and event count and
carries an ordered stream SHA-256. Import publishes nothing until it reads and
verifies the complete frame.

#### Version 1 frame and hash profile

`rin.session-transfer/v1` is a complete-lineage format: `start_revision` is
zero, `start_head_hash` is empty, `event_count` is greater than zero, and
`terminal_revision` equals `event_count`. It uses only lowercase hexadecimal
SHA-256 (`hash_algorithm: "sha256"`). Revisions, counts, and lineage generation
must remain exact JSON integers no greater than `9007199254740991`.

Checksums use the compact UTF-8 JSON produced by the declared wire member order
in the protocol structs, with no insignificant whitespace. `EventRecord.Data`
retains its original compact JSON member order and value representation. The
per-event checksum is SHA-256 over the compact `EventRecord` object. The stream
checksum is SHA-256 over the compact manifest followed by LF, then each compact
event frame followed by LF, in sequence order. It excludes the `complete`
frame. Cross-language implementations must use the golden vectors in
`protocol/transfer_test.go`; parsing into an unordered object and serializing it
with implementation-default member order is not conformant.

Validators reject non-genesis starts, unsafe integers, invalid timestamps or
JSON, checksum mismatches, sequence gaps, broken `prev_hash` continuity, extra
events, and a terminal boundary that differs from the manifest or final event.
The authoritative `EventRecord.Hash` chain is separately verified during
Runtime replay; transport checksums do not replace it.

### 2. Keep every frame bounded

- HTTP reads and writes one frame at a time and never materializes the complete
  Transfer.
- EventRecord retains the Store single-record read limit.
- Manifest, complete, and envelope frames use smaller independent limits.
- SDKs write to a caller-provided stream/file sink instead of returning one
  large string.
- Import accepts a stream/file source instead of a complete byte array.

Total transfer size is not subject to the inline 16 MiB Snapshot ceiling, but a
deployment may enforce total deadlines, byte quotas, and Session storage quotas.
A limit failure cannot publish a partial Session.

### 3. Export an immutable boundary

Export captures terminal revision/head and lineage generation under the Session
lock, then releases the mutation lock. Range reads include nothing newer than
that boundary, so later mutations do not enter the Transfer.

Export verifies range continuity, the declared start, every previous hash, the
terminal anchor, and the captured lineage generation. Initial support exports
the complete local Event Log. Future incremental Transfer must carry a trusted
base revision/head and cannot concatenate data by revision alone.

### 4. Publish imports atomically

Import cannot append directly into a live Session. Store gains an optional
`TransferStore` capability that:

1. creates an invisible staging area under the same data root;
2. validates and writes frames in order;
3. syncs staging files and directories;
4. verifies the complete chain, Binding, terminal anchor, and stream checksum;
5. publishes once through a same-directory atomic rename;
6. syncs the parent directory.

Parsing, validation, cancellation, capacity, sync, or rename failures may only
remove or retain invisible staging data; they cannot expose a partial Session.
Custom Stores without `TransferStore` retain existing APIs while Import returns
`transfer_unavailable`. Runtime must not simulate a non-atomic import through
`Create` and `Append`.

### 5. Import target and Binding

The first version only imports a not-yet-existing Session under the same ID.
This avoids implicit replacement and lineage merging. The request explicitly
provides `expected_binding` from the running game's trusted manifest. It must
match the Transfer manifest and the Binding recovered from the first event.

Replacement, renaming, branch merging, and import into an existing Session are
out of scope. Existing Restore remains the rollback mechanism; complete
Transfer is the migration mechanism for an oversized lineage.

### 6. Do not truncate Identifier History

Complete Event Log replay, including history embedded in Restore events, must
reconstruct Identifier History deterministically. After atomic import, Runtime
verifies and replays from genesis before registering the Session as live.
Checkpoint creation may follow asynchronously but cannot replace the first
complete verification.

Transfer cannot export only bounded State or discard tombstones. Doing so would
make identifiers from abandoned branches reusable and break exact retry and
outcome reconciliation.

### 7. HTTP and cancellation semantics

The planned Bearer-protected operations are:

- `POST /v1/session/export`: small JSON request and NDJSON streaming response;
- `POST /v1/session/import`: trusted Binding arrives as bounded metadata and the
  request body is an NDJSON stream.

Executable contract tests must fix the exact wire shape before it enters
OpenAPI. HTTP validates media type, UTF-8, JSON depth, bytes, and unknown members
per frame, stops after disconnect/cancellation, never logs Event Data, and
rejects compression bombs. Once a response has begun, an export failure uses a
terminal error frame and can never masquerade as `complete`.

### 8. Keep inline Snapshot compatible

Existing Snapshot/Restore remains the game-save path for ordinary Sessions and
retains its JSON shape and 16 MiB limit. Session Transfer is the migration,
backup, and recovery channel for a large lineage; it does not silently change
the Snapshot endpoint media type.

## Rejected alternatives

- **Raise the Snapshot limit:** still requires the server, SDK, and game to
  allocate the complete object and adds no framing or resumption.
- **Export only current State:** loses audit, Replay, and permanent Identifier
  History.
- **Publish while receiving through Create/Append:** exposes a partial Session
  after interruption.
- **Stage only in HTTP memory:** recreates unbounded memory and cannot be safely
  diagnosed after restart.

## Implementation order

1. Define protocol frames, validators, and hash rules. **Implemented.**
2. Define `TransferStore` and implement File Store staging/atomic publication.
3. Implement the immutable Runtime export boundary and post-import genesis
   verification.
4. Add the HTTP stream.
5. Add TypeScript/C# SDK stream helpers.
6. Add over-16-MiB end-to-end, cancellation, corruption, and crash tests.
7. Update compatibility, security, migration, release, and operations docs.

The Roadmap must not mark Session Transfer supported before step 6 passes.
