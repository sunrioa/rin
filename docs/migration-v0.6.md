# Migrating to Rin 0.6 Preview

[English](migration-v0.6.md) | [简体中文](migration-v0.6.zh-CN.md)

This guide applies when replacing an earlier source revision with Rin `0.6.0`.
Read the [compatibility matrix](compatibility.md) first. The OpenAPI document at
[`api/openapi.json`](../api/openapi.json) is the wire-shape authority.

## Before upgrading

1. Stop every Sidecar that can write the data directory.
2. Take a coordinated backup of the complete local data directory and the
   matching game saves. Do not copy a live File Store without coordination.
3. Record the deployed Rin commit, client/adaptor source revision, enabled
   Session Features, and game content Binding.
4. Drain the game's Outcome Outbox when possible. Otherwise persist every
   unacknowledged Outbox item and Proposal Attempt with the matching save.
5. Upgrade the Sidecar and all vendored client/adaptor files together. Do not
   copy only one SDK source file.

## Wire changes that require client review

### Safe integers

Every public JSON integer must be exactly representable in the range
`-9007199254740991` through `9007199254740991`; schema-specific non-negative or
narrower limits still apply. JavaScript integrations must reject unsafe
`number` values rather than round them, and must not send `BigInt` or a quoted
integer.

### Required outcome presence

`accepted` must appear in every Commit request and every Batch Commit item.
Send `true` after the action took effect and explicit `false` after the game
rejected it. Missing or `null` is `400 invalid_request`.

### UTF-8 and JSON shape

Encode the raw HTTP request body as UTF-8. Invalid raw bytes or unpaired JSON
Unicode surrogates fail before JSON decoding with `400 invalid_json`. Request
objects are closed and reject unknown fields. Response objects can add fields,
so client decoders must remain tolerant. Client-owned Snapshot storage must
persist the original opaque JSON and return it directly on Restore instead of
round-tripping it through a lossy typed model. This is not a server preservation
promise: 0.6 ignores unknown additive `Snapshot`/`SessionState` members and
later emits only its known projection. Restore succeeds only when `state_hash`
matches that known projection; a future hash that includes an unknown State
member fails closed with `400 invalid_snapshot`.

Successful Provider JSON is also strictly checked before decoding for invalid
UTF-8 and unpaired Unicode surrogates. Non-2xx Provider bodies are bounded
error-classification input only; they never become Generation content or
Session state.

### Error layers

A non-2xx HTTP failure uses:

```json
{"ok":false,"error":{"code":"invalid_request","message":"...","field":"..."}}
```

A successful Job lookup can instead return HTTP `200` with a terminal Job whose
`data.error` reports the asynchronous operation failure. Do not treat every
HTTP `200` Job response as a successful Proposal or Generation.

## Session behavior

### Existing Sessions

Do not add Features by editing a Snapshot or event log. An existing Session
without `outcome-reporting-v1` intentionally retains its historical fresh-head
Commit and arrival-order reducer behavior. Restoring it does not silently opt it
into new semantics.

If a game needs the apply-then-report lifecycle, start a new Session lineage
whose Create request includes `outcome-reporting-v1`, then migrate authoritative
game facts through game-owned logic. Do not manufacture or rewrite Rin history.

### New apply-then-report lifecycle

Before a Proposal submit, persist the complete Propose request, operation
identity, and later Job ID as a Proposal Attempt. A submit, poll, timeout, or
cancel response can be outcome-unknown. Resume the same identity and block new
turns; do not run an offline fallback until the integration has confirmed that
no online Proposal exists.

After receiving a Proposal:

1. Re-read authoritative game state and validate the action locally.
2. Apply it or reject it on the game-owned thread.
3. Persist the applied marker and complete Outcome Outbox entry in the same
   game transaction.
4. Report the exact Commit from the Outbox.
5. On a timeout or `mutation_outcome_unknown`, retry the same typed payload and
   IDs without applying the action again.

Provider failure inside a live Proposal operation may select Rin's
deterministic Policy. Sidecar delivery uncertainty is different and must remain
fail closed.

## Snapshot and Restore migration

Every 0.6 Restore request requires `expected_binding` from the running game's
trusted content manifest. Never copy that value from the imported Snapshot. It
must match the Snapshot Binding and any existing target Session Binding.

New Snapshots include `identifier_history` and `identifier_history_hash`.
History permanently reserves accepted request and Event IDs. A legacy Snapshot
without those fields remains importable with `coverage_complete=false`; IDs
evicted before export cannot be reconstructed, so globally unique IDs remain
mandatory forever for that lineage.

The complete compact Snapshot must fit 16 MiB. Identifier History is not
truncated to make it fit, and no streaming Snapshot transport exists.

Treat a Snapshot as trusted opaque state. Its hashes detect accidental damage,
not provenance or a party able to edit and recompute it.

## Projection and storage migration

`rin.reducer-projection/v2` changes derived presentation and bounded Summary
sampling. A legacy Proposal's exact retry can return reconstructed
`summary`/`rationale` while preserving its original action, audit IDs,
revision, and head. Original event bytes are not rewritten, and old private
strings can remain in event logs, embedded Restore payloads, and backups.

Internal v1 checkpoints are obsolete derived caches. Rin falls back to another
compatible checkpoint or genesis replay; no manual checkpoint conversion is
required. A normal lazy load is not a checkpoint-independent full audit. Run
`Engine.VerifyAll()` in maintenance tooling when a genesis-to-head audit of
every Session is required.

The bundled File Store is supported only on local `darwin` and `linux`
filesystems with reliable locking, atomic rename, and sync semantics. Do not
move it to Windows, NFS, SMB, FUSE, or a cloud-synchronized directory.

## Verification checklist

- The game and Sidecar agree on Binding and enabled Features.
- All request integers remain within the safe range.
- Both `true` and explicit `false` Commit paths are tested; omission fails.
- Unknown request fields fail while additive response fields do not break the
  client.
- HTTP errors and HTTP-200 terminal Job errors are handled separately.
- Proposal Attempt and Outcome Outbox recovery survives process loss.
- A Sidecar timeout cannot overtake an unresolved Proposal with fallback.
- Restore sources `expected_binding` from the running manifest.
- Legacy Snapshot import and exact retry behavior are covered by a saved-data
  fixture.
- The full repository and SDK test commands in the release guide pass.

## Rollback

Do not point an older binary at the only copy of data already opened by 0.6.
Stop the Sidecar and preserve the upgraded directory. Test rollback against a
copy of the pre-upgrade backup with the exact older client and game build.
Never edit JSONL, Snapshot hashes, Identifier History, or a published release
tag to force a rollback.
