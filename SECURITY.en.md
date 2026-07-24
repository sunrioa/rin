# Security

[简体中文](SECURITY.md) | [English](SECURITY.en.md)

This document defines Rin's supported security boundary, deployment
requirements, and vulnerability-reporting process.

Rin `0.6.0` is Preview, pre-1.0 software. Preview status does not relax the
fail-closed rules in this document, but it does mean future compatibility must
be evaluated through the Changelog and migration guides.

## Defaults

- The service listens only on `127.0.0.1` by default.
- A non-loopback listener requires both `-allow-remote` and `RIN_TOKEN`.
- Rin does not terminate inbound TLS. Remote deployments must place it
  behind a TLS reverse proxy on a controlled network.
- Once a token is configured, every endpoint except `/health` uses
  constant-time Bearer-token verification.
- JSON request bodies and bundled-client response bodies are limited to
  32 MiB by default. Complete inline Snapshot compact JSON is separately
  capped at 16 MiB to leave envelope and durable-record headroom; it is
  rejected with `413 snapshot_too_large`, never truncated, when larger.
  No streaming Snapshot transport is currently provided. Unknown fields and
  multiple JSON values are rejected. The HTTP API validates the raw request
  body before JSON decoding and returns `400 invalid_json` for invalid UTF-8
  bytes or unpaired JSON Unicode surrogates.
- Every public HTTP JSON integer must be exactly representable between
  `-9007199254740991` and `9007199254740991`; schema-specific non-negative and
  narrower bounds still apply. Unsafe integers are rejected rather than
  rounded across language boundaries.
- Session IDs use safe identifiers only; HTTP requests cannot provide file
  paths.
- Events, indexes, checkpoints, snapshots, and the lock file use `0600`
  permissions. Snapshot, checkpoint, and rebuilt-index publication uses a
  synced temporary file, rename, and directory sync.
- Event logs use `retain_forever`; the file store keeps the two newest valid
  checkpoints and two newest valid Snapshot files per Session. Backups and
  deletion policies must treat every retained artifact as sensitive.
- API keys, sidecar tokens, and provider configuration are not protocol state
  and are never persisted.
- Provider URLs reject userinfo, query strings, fragments, and automatic HTTP
  redirects. Remote model endpoints require HTTPS by default.
- Official game adapters also reject redirects. Plaintext sidecar HTTP is
  limited to explicit loopback origins, while remote HTTPS requires a token.

## Trust model

Policy and model output are untrusted. The runtime accepts only candidate
actions declared by the game for the current request and verifies actor,
goal, memory, boundary, revision, and content binding. Rin does not execute
scripts, shells, dynamic plugins, or model-generated tool calls.

Snapshots are trusted, opaque serialized state and require the same controls
as event logs. Their SHA-256 canonical checksums detect accidental corruption
or unsynchronized modification; they are not signatures or provenance proof,
and an editor can recompute them. Restore therefore requires
`expected_binding` from the running game's trusted content manifest instead
of trusting the imported Snapshot to declare which content is active.

Event-chain hashes have the same adversarial limitation. They are unkeyed
SHA-256 consistency links, not signatures or MACs. A party able to replace a
complete event log can recompute the chain and its indexes, checkpoints, and
Snapshots. Rin detects an inconsistent history; it does not authenticate a
history against an external immutable anchor.

Online mode sends only the current actor's bounded traits, boundaries, active
goals, relevant memories, beliefs, recent actions, and candidate actions.
Event logs, complete sessions, receipts, snapshots, file paths, tokens, and
API keys do not enter the model packet. All game text is placed under
explicitly marked `untrusted_game_data`, and model output still requires local
allowlist validation.

The model output schema does not accept `summary` or `rationale`, and
compatibility text in every Policy Draft is discarded. The runtime rebuilds
player fields only from the game-authorized `ActionSpec.description` and a
fixed stance template; private Goal, Boundary, Memory, Belief, prompt, and
provider text are not inputs to that function. `policy_source`,
`recalled_memory_ids`, `goal_id`, `boundary_id`, and the full `proposed_goal`
are private audit/integration metadata and must not be displayed directly to
players. Only the game-authorized action Description is presentation copy;
action IDs, kinds, targets, and parameters are integration data by default.
This boundary uses input isolation and construction, not a secret-string
blacklist; the game must make every candidate action description safe for
display.

After upgrade, `rin.reducer-projection/v2` reconstructs legacy Proposal
presentation in API projections such as State, Replay, Snapshot export, and
exact retry, but it does not rewrite the authoritative event chain. Old
`proposal.created` records or old Snapshots embedded in Restore events may
still retain their original Summary/Rationale on disk, in backups, and in
external Stores. Upgrading is not privacy erasure; continue to protect that
raw data as a sensitive event log.

Structured Generation sends caller-provided messages to the model but does
not automatically attach sessions, event logs, paths, or credentials. Rin
validates only the top-level JSON object and character/byte limits. The caller
must validate its own field schema, referenced IDs, permissions, and canon,
and must never directly execute generated output.

The built-in Provider does not put prompts, credentials, or raw Provider HTTP
bodies into errors, logs, or durable Session state. A successful decoded and
validated Generation result is intentionally retained in the bounded
process-local Job record and semantic cache, then returned to the caller. That
content is untrusted and may be sensitive; a game that persists it must apply
its own allowlist and retention policy. Before decoding a successful Provider
JSON response, Rin strictly rejects invalid UTF-8 and unpaired Unicode
surrogates. A non-2xx Provider body is used only for bounded error
classification; it is not treated as content, written to Session state, or
given a content-validity guarantee.

Games must keep high-authority operations such as quests, items, combat,
currency, intimacy consent, and critical plot transitions in their own rule
layer.

Adapter proposals named `offline.*` exist only for a game's own offline
fallback. They are explicitly marked `committable=false` and cannot be
submitted as sidecar proposals. Threads, HTTP objects, and cancellation
handles must not enter Ren'Py saves; only plain JSON results and validated
snapshots may be persisted.

Provider failure inside a confirmed live Sidecar Proposal operation may use the
deterministic Policy. An ambiguous Sidecar submit, poll, timeout, or cancel is
not offline proof: an online Proposal may exist. Persist and resume the exact
Proposal Attempt/Job identity, block new turns, and do not execute fallback
content until absence is confirmed.

The bundled file store takes a non-blocking exclusive data-directory lock
before reading or writing. A second process fails to open that directory, and
embedded callers must call `(*store.File).Close()` to release the lease.
The bundled `flock` implementation currently supports only `darwin` and
`linux`. On every other GOOS, `store.OpenFile` returns
`ErrDataDirectoryLockUnsupported` and fails closed instead of running without
the lock. High-availability or multi-instance hosts must implement another,
externally coordinated Store rather than share the JSONL directory.

The bundled JSONL store is supported only on a local filesystem with reliable
`flock`, same-directory atomic rename, file `fsync`, and directory `fsync`
semantics. NFS, SMB, FUSE mounts, and cloud-synchronized directories are not
supported. Remote or shared storage requires an externally coordinated Store.

File and directory `fsync` calls narrow crash windows, and a stale derived
index is rebuilt from the authoritative event log. They are not an absolute
durability guarantee against storage hardware, kernel, filesystem, backup,
or operator failure. Stop the Sidecar or use a coordinated snapshot before
copying the data directory.

## Reporting

Use the GitHub repository's private security-reporting channel. Do not attach
tokens, API keys, saves, or complete event logs to a public issue.
