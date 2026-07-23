# Rin Lua SDK

[English](README.md) | [简体中文](README.zh-CN.md)

客户端支持 Lua 5.1+，不假设具体引擎。需要提供三个 Adapter：

- `http_fetch(request, callback)` 返回 `{status, body, headers}`，并且必须
  遵守 `follow_redirects = false`；
- `json_encode(table)` 和 `json_decode(string)` 使用引擎的 JSON Codec；
- 可选 `schedule(seconds, callback)` 和单调 `now()` 可在不阻塞游戏循环
  的情况下轮询 Job。未提供 `now` 时使用可移植但分辨率较低的 `os.time`
  墙上时钟。

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

Callback 约定为 `(data, error)`。网络工作保持异步；只能从引擎拥有的线程
应用白名单动作。
