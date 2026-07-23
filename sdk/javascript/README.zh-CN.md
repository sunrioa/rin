# Rin JavaScript SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Node.js 18+ 或标准 Fetch 宿主的零依赖 Promise 客户端，内含
TypeScript 声明。

```js
import { RinClient } from "@sunrioa/rin-sdk";

const rin = new RinClient("http://127.0.0.1:7374");
console.log(await rin.health());
```

从当前 Checkout 直接运行：

```bash
node sdk/javascript/examples/quickstart.js
cd sdk/javascript && npm test
```

调用基于 Promise。只有回到引擎主线程并用本地白名单验证 Proposal 后，
才能应用引擎状态。
