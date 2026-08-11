# Rin Lua Control SDK

This zero-dependency module calls the loopback-only `rin.control/v2` API from
Lua game runtimes. The host engine supplies asynchronous HTTP and JSON
adapters, while the SDK owns route selection, bearer authentication, input
validation, response bounds, redirect rejection, and stable errors.

```lua
local rin = dofile("rin.lua")
local control = assert(rin.new({
    token = assert(os.getenv("RIN_CONTROL_TOKEN")),
    http_fetch = engine_http_fetch,
    json_encode = engine_json_encode,
    json_decode = engine_json_decode,
}))

control:list_worlds(function(worlds, err)
    if err then return engine_log(err.code .. ": " .. err.message) end
    engine_log("worlds: " .. tostring(#worlds))
end)
```

`http_fetch(request, callback)` receives a request with `url`, `method`,
`headers`, optional `body`, `timeout`, and `follow_redirects = false`. It must
call `callback(response, nil)` with `status`, `headers`, and `body`, or
`callback(nil, {code = "transport_timeout"})` on timeout.

The client accepts only plain HTTP loopback origins with an explicit port and
a single-line token of at least 32 bytes. It never executes model output or
game actions itself; the game Host remains the authoritative executor.
