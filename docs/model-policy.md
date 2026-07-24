# Model Policy

[English](model-policy.md) | [简体中文](model-policy.zh-CN.md)

Model access is optional and bounded; local validation and the deterministic
policy remain available without a provider.

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

## Provider resilience contract

The timeout settings are cooperative budgets, not runtime preemption. Every
`provider.Client` implementation must stop work and return promptly after its
input `ctx.Done()` closes; the built-in OpenAI-compatible client follows this
contract. Go cannot safely terminate an arbitrary blocking implementation, so
Rin deliberately does not detach each call into a goroutine that could leak
forever. A non-compliant third-party client can therefore outlive either
configured timeout and delay `Complete` until it returns.

For a context-compliant client, `RIN_MODEL_ATTEMPT_TIMEOUT` limits one HTTP attempt.
If that deadline expires while both the caller and the total budget are still
live, the result is an attempt timeout: even a success returned after the
deadline is discarded, and Rin may retry it within the remaining total
budget.

Under the same contract, `RIN_MODEL_TOTAL_TIMEOUT` covers all attempts,
`Retry-After`, and local backoff for one logical provider call. If this
internal budget expires while the caller context is still live, Rin returns
`context deadline exceeded`, discards any late success or error, and records
exactly one failed logical call for the circuit breaker. Exhausting retryable
attempts also records one failed logical call, rather than one failure per
HTTP attempt.

Caller cancellation, including a caller-owned deadline, takes precedence:
Rin returns the caller's context error, does not count a provider failure,
and releases a canceled half-open probe so another call can test recovery.

Retries are limited to per-attempt timeouts and errors classified as
transient, such as transport/read failures, HTTP 408/429/5xx responses,
malformed or empty successful provider responses, and their bounded
`Retry-After` delay. Local request/schema errors, non-transient HTTP 4xx
responses, and `response_too_large` are not retried. In particular, the
response byte limit does not change between attempts, so replaying an
oversized response would spend the same bounded budget without relaxing the
limit.

Non-retryable outcomes have separate availability semantics. A confirmed
HTTP/provider response resets the availability failure streak and may close a
half-open breaker. Local preflight failures such as schema validation,
request encoding, or request construction are neutral: they neither reset
the closed-state streak nor claim provider recovery, and a half-open
preflight failure only releases its probe slot.

After `RIN_MODEL_BREAKER_FAILURES` failed logical calls, new calls fail fast
without reaching the provider. Once `RIN_MODEL_BREAKER_OPEN` elapses, exactly
one half-open probe is admitted; concurrent probes continue to fail fast.
An in-budget success or non-transient response ends the availability probe.
A retryable failure or internal total timeout opens the breaker for another
full open interval; caller cancellation only releases the probe slot.

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
5. The provider calls, retries, or opens its breaker within the cooperative
   total budget.
6. The JSON draft may contain only action/stance and allowed audit IDs. It has
   no player-text fields and receives local unknown-field and allowlist
   validation.
7. The engine discards compatibility text from every Policy implementation and
   rebuilds player-facing `summary`/`rationale` solely from the selected
   game-authored action description and a fixed stance template.
8. If any earlier step fails, Rin uses the deterministic policy and reports
   `policy_source=deterministic-fallback`.
9. The engine rechecks revision and head hash. If either changed, the job is
   `stale`.

The model decides only which allowed action to recommend and which supplied
memory/goal IDs informed that choice. Private goal, boundary, memory, belief,
trait, intent, and recent-context text may influence selection but must never
be copied into output; the strict schema rejects `summary` and `rationale`
fields. Rin's runtime reconstruction is the final information-flow gate and
does not rely on secret-string matching. The model cannot commit or change
world state.

`policy_source`, `recalled_memory_ids`, `goal_id`, the runtime-derived
`boundary_id`, and the full `proposed_goal` are private audit/integration
fields. Their presence or values can reveal hidden character state, so player
UI must not display them directly.

The structured Generation API reuses the same provider and breaker budget,
but it has no deterministic-policy fallback. The caller must provide offline
text and validate domain schema and canon before accepting a result.
