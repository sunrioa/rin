# Rin SDK

[English](README.md) | [简体中文](README.zh-CN.md)

这些源码 SDK 暴露同一个 `rin.protocol/v1` HTTP 边界，不会把游戏权威移动
到客户端库。

| 语言 | 运行时 | JSON | 异步建议 |
| --- | --- | --- | --- |
| Python | 3.9+ | 标准库 | 实时游戏从 Worker 调用 |
| JavaScript | Node 18+ / 现代浏览器宿主 | 内置 | 基于 Promise |
| C# | .NET 6+ | `System.Text.Json` | 基于 `Task` |
| Java | 17+ | 宿主提供 JSON 文本 | 基于 `CompletableFuture` |
| Lua | 5.1+ 宿主 | 注入 Codec 与 Transport | 基于 Callback |

所有客户端遵循以下规则：

- 只对显式 loopback Origin 接受明文 HTTP；
- 远程 Origin 要求 HTTPS 和 Bearer Token；
- 拒绝重定向；
- 强制请求超时和响应大小限制；
- 错误只暴露有界 Rin Code，不暴露供应商正文或凭据；
- Proposal 保持 Pending，直到游戏应用并 Commit。

SDK 有意采用源码优先方式，尚未发布到 PyPI、npm、NuGet 或 Maven Central。
Vendor 时应固定本仓库 Revision。路由兼容性由
[`conformance/routes.json`](conformance/routes.json) 定义。

游戏专用示例位于 [`examples/mods`](../examples/mods)。它们展示宿主事件
如何进入 Rin，以及游戏在何处验证并应用 Proposal。它们是接入模板，不是
适用于每个游戏版本的通用补丁。

SDK 源码按 [MIT License](../LICENSE) 发布。
