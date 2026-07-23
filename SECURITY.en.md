# Security

[简体中文](SECURITY.md) | [English](SECURITY.en.md)

## Defaults

- The service listens only on `127.0.0.1` by default.
- A non-loopback listener requires both `-allow-remote` and `RIN_TOKEN`.
- Rin does not terminate inbound TLS. Remote deployments must place it
  behind a TLS reverse proxy on a controlled network.
- Once a token is configured, every endpoint except `/health` uses
  constant-time Bearer-token verification.
- JSON bodies are limited to 32 MiB by default, primarily for complete
  snapshots. Unknown fields, multiple JSON values, and non-UTF-8 input are
  rejected.
- Session IDs use safe identifiers only; HTTP requests cannot provide file
  paths.
- Events and snapshots use `0600` permissions; immutable snapshot files are
  written atomically.
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

Online mode sends only the current actor's bounded traits, boundaries, active
goals, relevant memories, beliefs, recent actions, and candidate actions.
Event logs, complete sessions, receipts, snapshots, file paths, tokens, and
API keys do not enter the model packet. All game text is placed under
explicitly marked `untrusted_game_data`, and model output still requires local
allowlist validation.

Structured Generation sends caller-provided messages to the model but does
not automatically attach sessions, event logs, paths, or credentials. Rin
validates only the top-level JSON object and character/byte limits. The caller
must validate its own field schema, referenced IDs, permissions, and canon,
and must never directly execute generated output.

Games must keep high-authority operations such as quests, items, combat,
currency, intimacy consent, and critical plot transitions in their own rule
layer.

Adapter proposals named `offline.*` exist only for a game's own offline
fallback. They are explicitly marked `committable=false` and cannot be
submitted as sidecar proposals. Threads, HTTP objects, and cancellation
handles must not enter Ren'Py saves; only plain JSON results and validated
snapshots may be persisted.

Only one Rin process may write to a data directory. High-availability or
multi-instance hosts must coordinate a single writer or implement another
store.

## Reporting

Use the GitHub repository's private security-reporting channel. Do not attach
tokens, API keys, saves, or complete event logs to a public issue.
