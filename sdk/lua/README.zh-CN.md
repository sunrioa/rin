# Rin Lua Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

这是一个零依赖的 `rin.control/v2` Lua 客户端，供游戏运行时连接本机 Control
Daemon。游戏引擎负责提供异步 HTTP 和 JSON 编解码器；SDK 负责固定路由、Bearer
鉴权、输入校验、响应大小限制、拒绝重定向和稳定错误码。

下面是引擎 Adapter 的集成草图；其中的 HTTP、JSON 和日志函数必须由游戏运行时提供。

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

`http_fetch(request, callback)` 接收包含 `url`、`method`、`headers`、可选
`body`、`timeout` 和 `follow_redirects = false` 的请求。成功时回调包含
`status`、`headers`、`body` 的响应；超时时回调
`callback(nil, {code = "transport_timeout"})`。

客户端只接受带显式端口的本机明文 HTTP 地址，以及至少 32 字节的单行 Token。
它不会直接执行模型输出或游戏动作，游戏 Host 始终是权威执行方。
