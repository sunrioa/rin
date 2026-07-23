# Rin Lua SDK

[English](README.md) | [简体中文](README.zh-CN.md)

An engine-neutral callback client for Lua 5.1+.

Supply three host adapters:

- `http_fetch(request, callback)` returns `{status, body, headers}` and must
  honor `follow_redirects = false`;
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
