# Deployment and monitoring

[简体中文](operations.zh-CN.md) | [English](operations.md)

Rin exposes separate liveness, readiness, diagnostics, and metrics surfaces.
They contain counters and state classifications only; they never include
Session IDs, Actor IDs, event text, model prompts/responses, credentials, or
filesystem paths.

## Probes and authentication

| Endpoint | Authentication | Meaning |
| --- | --- | --- |
| `GET /health` | None | Cheap process liveness and compatibility identity; it does not touch the Store or Provider |
| `GET /ready` | None | The Session Store can be listed and each configured Job Manager is still running |
| `GET /v2/diagnostics` | Bearer token when `RIN_TOKEN` is set | Bounded JSON snapshot of Runtime, queue, retained Job, checkpoint, uncertainty, and Provider-breaker state |
| `GET /metrics` | Bearer token when `RIN_TOKEN` is set | Dependency-free Prometheus text exposition with fixed metric names and no high-cardinality labels |

Use `/health` for a liveness probe and `/ready` for a readiness probe. A failed
readiness check returns `503 not_ready`; do not use `/health` to decide whether
to route game traffic.

Linux/macOS:

```bash
curl --fail http://127.0.0.1:7374/health
curl --fail http://127.0.0.1:7374/ready
curl --fail -H "Authorization: Bearer $RIN_TOKEN" \
  http://127.0.0.1:7374/v2/diagnostics
```

Windows PowerShell:

```powershell
Invoke-RestMethod http://127.0.0.1:7374/health
Invoke-RestMethod http://127.0.0.1:7374/ready
$headers = @{ Authorization = "Bearer $env:RIN_TOKEN" }
Invoke-RestMethod -Headers $headers http://127.0.0.1:7374/v2/diagnostics
```

For a local Windows game integration, place `rin.exe` beside a writable
`rin-data` directory and use the checked-in launcher:

```powershell
powershell -ExecutionPolicy Bypass -File tools/start-rin.ps1 `
  -Rin .\rin.exe -DataDirectory .\rin-data
```

The launcher uses literal paths, creates only the requested data directory,
binds loopback by default, and propagates the Sidecar exit code. Check
`/ready` before starting the game.

Treat diagnostics and metrics like other authenticated API data. Bind the
Sidecar to loopback by default; if a reverse proxy exposes it remotely, require
TLS and keep `/metrics` and `/v2/diagnostics` off public routes.

## What to monitor

The JSON diagnostics snapshot reports:

- known, loaded, and currently unreadable Sessions, grouped by bounded error
  code;
- unresolved durable-mutation barriers;
- active/pending checkpoint work, checkpoint failures, and quota skips;
- Proposal and Generation queue depth/capacity, retained/max-retained Jobs, and
  status counts;
- Generation cache size and Provider Circuit Breaker state;
- HTTP request count, in-flight count, 4xx/5xx counts, and cumulative duration.

Prometheus exposition uses fixed names including
`rin_http_requests_total`, `rin_sessions_unreadable_known`,
`rin_uncertainty_barriers`, `rin_checkpoint_failures_total`,
`rin_proposal_queue_depth`, and (when Generation is configured)
`rin_provider_circuit_not_closed`.

Suggested alerts:

- readiness remains failed for more than one probe window;
- known unreadable Sessions, uncertainty barriers, or checkpoint failures
  increase;
- a queue remains near capacity or retained Jobs remain near their cap;
- the Provider Circuit Breaker remains non-closed;
- 5xx responses increase.

A full queue alone does not make the process unready: removing a healthy
instance during load can amplify saturation. Queue metrics and `429` responses
are the overload signals.

## Request correlation and shutdown

Clients may send `Rin-Request-ID` using 1–96 ASCII letters, digits, `.`, `_`, or
`-`. Rin echoes a valid value or generates one. Structured request logs contain
only this ID, method, matched route template, status, and duration; raw paths,
query strings, headers, and bodies are excluded.

On shutdown, stop routing new traffic after `/ready` fails, allow the HTTP
server to drain, and then close Job Managers. The bundled CLI performs a
bounded graceful shutdown after SIGINT/SIGTERM (or the corresponding Windows
console/service signal). Never copy or modify the live data directory without
following the backup rules in [Session lifecycle](session-lifecycle.md).
