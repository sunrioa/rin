# Security

[简体中文](SECURITY.md) | [English](SECURITY.en.md)

This document defines Rin's supported security boundary, deployment
requirements, and vulnerability-reporting process.

Rin `0.7.0` is Preview, pre-1.0 software. Preview status does not relax the
fail-closed rules in this document, but it does mean future compatibility must
be evaluated through the Changelog and migration guides.

## Defaults

- The service listens only on `127.0.0.1` by default.
- A non-loopback listener requires `-allow-remote`, `RIN_TOKEN`, and either
  `-tls-proxy` or `RIN_TLS_PROXY=true`. Missing any one fails before the data
  directory is opened.
- Rin does not terminate inbound TLS. Production remote deployments should
  run a TLS reverse proxy on the same host and keep Rin on loopback. A split
  deployment may listen only on a controlled private network restricted to
  the proxy. The TLS-proxy declaration is an operator assertion; it neither
  enables TLS nor protects a public plaintext port.
- Once a token is configured, every endpoint except the content-free
  `/health` and `/ready` probes uses constant-time Bearer-token verification.
  `/metrics` and `/v2/diagnostics` remain authenticated and must not be exposed
  as public reverse-proxy routes.
- JSON request bodies and bundled-client response bodies are limited to
  32 MiB by default. Complete inline Snapshot compact JSON is separately
  capped at 16 MiB to leave envelope and durable-record headroom; it is
  rejected with `413 snapshot_too_large`, never truncated, when larger.
  Large lineages use NDJSON Session Transfer under the same Bearer check.
  Import Binding comes from independent trusted headers, compressed request
  bodies are rejected, and nothing is atomically published until `complete`
  verifies. Unknown fields and multiple JSON values are rejected. The HTTP API validates the raw request
  body before JSON decoding and returns `400 invalid_json` for invalid UTF-8
  bytes or unpaired JSON Unicode surrogates.
- Every public HTTP JSON integer must be exactly representable between
  `-9007199254740991` and `9007199254740991`; schema-specific non-negative and
  narrower bounds still apply. Unsafe integers are rejected rather than
  rounded across language boundaries.
- Session IDs use safe identifiers only; HTTP requests cannot provide file
  paths.
- On Unix, events, indexes, checkpoints, snapshots, and the lock file use
  `0600`, while data directories use `0700`. Windows does not interpret POSIX
  mode bits; these files inherit the data-root ACL, which operators must
  restrict to the Sidecar account. Snapshot, checkpoint, and rebuilt-index
  publication uses a synced temporary file, rename, and directory sync.
- Event logs use `retain_forever`; the file store keeps the two newest valid
  checkpoints and two newest valid Snapshot files per Session. Backups and
  deletion policies must treat every retained artifact as sensitive.
- Session deletion requires prior Archive plus trusted Binding, exact head,
  Archive receipt, and full Session-ID confirmation. It retains a minimal
  player-content-free tombstone for idempotent retry and permanent ID
  retirement; external exports, game saves, backups, and provider copies are
  outside Rin's deletion boundary.
- API keys, sidecar tokens, and provider configuration are not protocol state
  and are never persisted.
- Request logs contain only a bounded correlation ID, HTTP method, matched
  route template, status, and duration. Operational diagnostics use aggregate
  counts and bounded status/error classifications, never player content or
  per-Session labels.
- Provider URLs reject userinfo, query strings, fragments, and automatic HTTP
  redirects. Remote model endpoints require HTTPS by default.
- Official game adapters also reject redirects, and remote HTTPS requires a
  token. Keep plaintext Sidecar HTTP on loopback; only a split-proxy deployment
  may use the three gates above behind a private-network firewall.

## Trust model

Policy and model output are untrusted. The runtime accepts only fully bound
action offers declared by the game for the current Decision Window and
verifies actor, epoch, observation sequence, capability digest, deadline,
goal, memory, boundary, revision, and content binding. Rin does not execute
scripts, shells, dynamic plugins, or model-generated tool calls.

Observation `HostValidatedPayload` is an authenticated Host trust assertion.
The Host must validate Data against the exact Schema and digest first; Rin only
validates the bounded strict-JSON envelope and preserves schema identity. A
digest is not proof of validation, and model or remote Provider output must not
enter this field without Host validation. Go adapters should use
`protocol.NewHostValidatedPayload`.

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
goals, relevant memories, beliefs, recent actions, and action offers.
Event logs, complete sessions, receipts, snapshots, file paths, tokens, and
API keys do not enter the model packet. All game text is placed under
explicitly marked `untrusted_game_data`, and model output still requires local
allowlist validation.

The model output schema does not accept `summary` or `rationale`, and
`DecisionDraft` has no free-form text fields. The runtime rebuilds
player fields only from the game-authorized `ActionOffer.description` and a
fixed stance template; private Goal, Boundary, Memory, Belief, prompt, and
provider text are not inputs to that function. `policy_source`,
`recalled_memory_ids`, `goal_id`, `boundary_id`, and the full `proposed_goal`
are private audit/integration metadata and must not be displayed directly to
players. Only the game-authorized action Description is presentation copy;
action IDs, kinds, targets, and parameters are integration data by default.
This boundary uses input isolation and construction, not a secret-string
blacklist; the game must make every action offer description safe for
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

An adapter must not synthesize a Proposal when transport fails. Threads, HTTP
objects, and cancellation handles must not enter Ren'Py
saves; only plain JSON, complete Pending Turns, report outbox entries, and
validated snapshots may be persisted.

Provider failure inside a confirmed live Sidecar Proposal operation may use the
deterministic Policy. An ambiguous Sidecar submit, poll, timeout, or cancel is
not proof that no Proposal exists. Persist and resume the exact Proposal
Attempt/Job identity and block new turns.

The bundled file store takes a non-blocking exclusive data-directory lock
before reading or writing. A second process fails to open that directory, and
embedded callers must call `(*store.File).Close()` or
`(*store.ReadOnlyFile).Close()` to release the lease.
The bundled exclusive data-directory lock supports `darwin`, `linux`, and
`windows`: Unix uses `flock`, while Windows opens an exclusive file handle
without sharing. On every other GOOS, `store.OpenFile` returns
`ErrDataDirectoryLockUnsupported` and fails closed. High-availability or
multi-instance hosts must implement another externally coordinated Store
rather than share the JSONL directory.

The bundled JSONL store is supported only on a local filesystem with reliable
exclusive file locking, same-directory atomic rename, and the platform
durability primitives described below. NFS, SMB, FUSE mounts, and
cloud-synchronized directories are not supported. Remote or shared storage
requires an externally coordinated Store.

Unix uses file/directory `fsync`. Windows uses `FlushFileBuffers` for file data
and `MoveFileExW(MOVEFILE_WRITE_THROUGH)` for published renames because Windows
does not document `FlushFileBuffers` for directory handles. These operations
narrow crash windows, and a stale derived index is rebuilt from the
authoritative event log. They are not an absolute durability guarantee against
storage hardware, kernel, filesystem, backup, or operator failure. Stop the
Sidecar or use a coordinated snapshot before copying the data directory.

## Reporting

Use the GitHub repository's private security-reporting channel. Do not attach
tokens, API keys, saves, or complete event logs to a public issue.
