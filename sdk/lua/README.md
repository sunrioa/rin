# Rin Lua SDK

[English](README.md) | [简体中文](README.zh-CN.md)

An engine-neutral callback client for Lua 5.1+.

Supply three host adapters:

- `http_fetch(request, callback)` calls `callback(response)` with
  `{status, body, headers}` and must honor `follow_redirects = false`. A
  transport failure calls `callback(nil, {code = "transport_failed"})`; use
  `transport_timeout` when the host HTTP API explicitly reports a timeout.
  Adapter-provided text is never exposed as a trusted SDK error message;
- `json_encode(table)` and `json_decode(string)` use the engine's JSON codec;
- optional `schedule(seconds, callback)` and a monotonic `now()` enable job
  polling without blocking the game loop. Without `now`, the portable but
  lower-resolution `os.time` wall clock is used.

```lua
local rin = dofile("rin.lua")
local client, err = rin.new({
    base_url = "http://127.0.0.1:7374",
    http_fetch = engine_http_fetch,
    json_encode = engine_json_encode,
    json_decode = engine_json_decode,
    schedule = engine_schedule,
})
assert(client, err and err.message)

client:health(function(data, request_error)
    if request_error then print(request_error.code) else print(data.status) end
end)
```

The callback convention is `(data, error)`. Network work remains asynchronous;
only apply allowlisted actions from the engine's owning thread.

`rin.new_workflow(client, store)` adds the protocol-generic Pending Turn
coordinator. The Store supplies `load_attempt`, `create_attempt`,
`save_attempt`, `begin_action`, `complete_action`, `settle_without_action`,
`list_outcomes`, and
`acknowledge_outcome`. The coordinator persists before network submission,
resumes or safely resubmits a missing Job, verifies Job/Proposal identity,
serializes settle/drain operations per key, and retries the exact Action Report
until a same-Session acknowledgement. A missing or crossed Session ACK fails
closed before the Store callback. Use
`rin.proposal_freshness(state, proposal)` immediately before game apply.

The SDK defines ordering and validation, not host durability. Lua hosts differ
in scheduling, storage flush, and transaction behavior, so the concrete Store
and apply callback determine the declared profile. An operation-keyed durable
bridge may declare a stronger profile only after satisfying the
[host durability contract](../../docs/host-durability.md).
