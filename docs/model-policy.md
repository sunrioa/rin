# Model Policy

[English](model-policy.md) | [简体中文](model-policy.zh-CN.md)

## Enable

Rin uses `deterministic` by default and makes no model network requests.
Online mode must be enabled explicitly:

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="https://provider.example/v1"
export RIN_MODEL="your-model-id"
export RIN_MODEL_API_KEY="..."
rin serve
```

Rin calls the OpenAI-compatible
`POST <base-url>/chat/completions` endpoint. Requests use strict
`json_schema` by default. If a provider supports only JSON Object mode:

```bash
export RIN_MODEL_RESPONSE_FORMAT=json_object
```

The value may also be `none`, but returned text must still be one strict JSON
object with no extra fields.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `RIN_POLICY` | `deterministic` | `deterministic` or `model` |
| `RIN_MODEL_BASE_URL` | - | OpenAI-compatible `/v1` base URL |
| `RIN_MODEL` | - | Provider model ID |
| `RIN_MODEL_API_KEY` | - | Bearer key read only from process environment |
| `RIN_MODEL_RESPONSE_FORMAT` | `json_schema` | `json_schema`, `json_object`, or `none` |
| `RIN_MODEL_ATTEMPT_TIMEOUT` | `15s` | Maximum time for one HTTP attempt |
| `RIN_MODEL_TOTAL_TIMEOUT` | `25s` | Total budget including retry and backoff |
| `RIN_MODEL_MAX_ATTEMPTS` | `2` | Maximum attempts, capped at 5 |
| `RIN_MODEL_INITIAL_BACKOFF` | `150ms` | Initial backoff |
| `RIN_MODEL_MAX_BACKOFF` | `2s` | Maximum backoff and Retry-After |
| `RIN_MODEL_BREAKER_FAILURES` | `3` | Failed calls before opening the breaker |
| `RIN_MODEL_BREAKER_OPEN` | `20s` | Circuit-breaker open duration |
| `RIN_MODEL_CACHE_ENTRIES` | `256` | In-memory draft-cache entries |
| `RIN_MODEL_CACHE_TTL` | `10m` | Cache lifetime for the same head hash |
| `RIN_JOB_WORKERS` | `2` | Asynchronous proposal workers |
| `RIN_JOB_QUEUE_SIZE` | `64` | Proposal waiting-queue capacity |
| `RIN_JOB_MAX_RETAINED` | `512` | Maximum jobs including completed entries |
| `RIN_JOB_TTL` | `30m` | In-memory lifetime for completed jobs |
| `RIN_GENERATION_WORKERS` | `2` | Structured-generation workers |
| `RIN_GENERATION_QUEUE_SIZE` | `64` | Generation waiting-queue capacity |
| `RIN_GENERATION_MAX_RETAINED` | `512` | Maximum generation jobs including completed entries |
| `RIN_GENERATION_JOB_TTL` | `30m` | In-memory lifetime for completed generation jobs |
| `RIN_GENERATION_CACHE_ENTRIES` | `256` | Semantic generation-cache entries |
| `RIN_GENERATION_CACHE_TTL` | `30m` | Semantic generation-cache lifetime |
| `RIN_GENERATION_MAX_OUTPUT_BYTES` | `524288` | Maximum bytes for one structured result |

Durations use Go syntax such as `250ms`, `15s`, and `2m`.

## Local models

Loopback addresses may use HTTP and an empty key:

```bash
export RIN_POLICY=model
export RIN_MODEL_BASE_URL="http://127.0.0.1:11434/v1"
export RIN_MODEL="local-model"
```

Non-loopback HTTP is rejected by default. Set
`RIN_MODEL_ALLOW_INSECURE=true` only on a controlled test network.

## Runtime behavior

1. The game submits an asynchronous proposal job.
2. The local boundary guard first handles mandatory refusal or redirection.
3. The cache looks up an immutable draft by the current session head hash.
4. On a miss, Rin builds a minimal, data-isolated model packet.
5. The provider calls, retries, or opens its breaker within the total budget.
6. The JSON draft receives local allowlist validation.
7. If any step fails, Rin uses the deterministic policy and reports
   `policy_source=deterministic-fallback`.
8. The engine rechecks revision and head hash. If either changed, the job is
   `stale`.

The model decides only which allowed action to recommend and how to express
it. It cannot commit or change world state.

The structured Generation API reuses the same provider and breaker budget,
but it has no deterministic-policy fallback. The caller must provide offline
text and validate domain schema and canon before accepting a result.
