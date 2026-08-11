# Rin JavaScript Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

适用于 Node.js 18+ 的零依赖 ESM `rin.control/v2` 客户端。

```javascript
import { RinControlClient } from "./sdk/javascript/src/index.js";

const control = new RinControlClient({
  token: process.env.RIN_CONTROL_TOKEN,
});

console.log(await control.info());
console.log(await control.listWorlds());
```

构造器支持 `baseUrl`、`timeoutMs`、`maxResponseBytes` 和可注入的 `fetch`。
默认只连接 `http://127.0.0.1:7375`，禁止 Redirect，并对流式响应执行大小限制。

所有请求和响应都是普通 JSON Object/Array，精确字段见
`api/control-openapi.json`。TypeScript 声明位于 `src/index.d.ts`。

不要把 Promise resolve 等同于游戏执行完成；提交 Action 后应使用
`waitOperation` 等待终态，并检查 `execution_confirmed` 与 Host `outcome`。
