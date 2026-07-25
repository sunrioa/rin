# Rin Lua SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Lua 5.1+ 的引擎中立 Callback 客户端。

宿主需要提供三个 Adapter：

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

可移植 Lua Client 有意只提供 Transport，因此声明为 `advisory`。不同 Lua
宿主的调度、存储刷盘与事务行为差异很大；复制一份通用 Coordinator 会暗示
SDK 无法兑现的保证。只有加入按 Operation ID 工作的持久 Bridge，并满足
[宿主能力契约](../../docs/host-capability-profiles.zh-CN.md)后，具体宿主才能
声明更强 Profile。
