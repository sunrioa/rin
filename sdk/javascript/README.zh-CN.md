# Rin JavaScript Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

适用于 Node.js 18+ 的零依赖 ESM `rin.control/v2` 客户端。

同一客户端也通过原始 JSON 方法提供固定的 `/plans/v1/*` 任务计划接口。

在仓库根目录运行现有的[快速开始](examples/quickstart.js)：

```bash
node ./sdk/javascript/examples/quickstart.js
```

支持的构造形式是
`new RinControlClient({ token, timeoutMs?, maxResponseBytes?, fetch? })`，或
`new RinControlClient(baseUrl, { token, timeoutMs?, maxResponseBytes?, fetch? })`。
第一种形式默认连接 `http://127.0.0.1:7375`。客户端禁止 Redirect，并限制流式
响应大小。

请求使用普通 JSON Object，响应可以是 JSON Object 或 Array；精确字段见
[`api/control-openapi.json`](../../api/control-openapi.json)，TypeScript 声明见
[`src/index.d.ts`](src/index.d.ts)。

不要把 Promise resolve 等同于游戏执行完成；提交 Action 后应使用
`waitOperation` 等待终态，并检查 `execution_confirmed` 与 Host `outcome`。
