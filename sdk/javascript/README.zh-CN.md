# Rin JavaScript SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Node.js 18+ 或标准 Fetch 宿主的零依赖 Promise 客户端，内含
TypeScript 声明。

```js
import { RinClient } from "@sunrioa/rin-sdk";

const rin = new RinClient("http://127.0.0.1:7374");
const capabilities = await rin.negotiateCapabilities();
console.log(capabilities.release_version);
```

`negotiateCapabilities()` 会在 Runtime 不是 `rin.protocol/v1` 或不支持权威
Outcome Reporting preset 时 Fail Closed。请用 `createRinId("request")` 与
`createRinId("event")` 生成一次 ID，将其随操作持久化，并在每次 exact retry
中复用。

随附 TypeScript 声明为权威 create/propose/commit 流程提供与 OpenAPI 对齐的
`CreateSessionRequest`、`ProposeRequest`、`ProposalResult`、
`CommitRequest` 和 `MutationResult` 类型；响应类型会容忍未来新增字段。

从当前 Checkout 直接运行：

```bash
node sdk/javascript/examples/quickstart.js
cd sdk/javascript && npm test
```

调用基于 Promise。只有回到引擎主线程并用本地白名单验证 Proposal 后，
才能应用引擎状态。

Session Transfer 全程使用 streaming，不会以一个大字符串返回。Source/sink
归调用方所有，并由调用方决定何时关闭：

```js
import { createReadStream, createWriteStream } from "node:fs";
import { Readable, Writable } from "node:stream";

const request = {
  protocol_version: "rin.protocol/v1",
  session_id: "session.example",
};
const output = Writable.toWeb(createWriteStream("session.ndjson"));
await rin.exportSession(request, output);
await output.getWriter().close();

await rin.importSession(
  Readable.toWeb(createReadStream("session.ndjson")),
  {
    game_id: "game.example",
    content_id: "base",
    content_version: "1",
    content_hash: "trusted-build-hash",
  },
);
```

`exportSession` 只有读取到合法的终止 `complete` frame 才会 resolve；终止
`error`、截断、顺序错误或超限 frame 都会 reject Promise。`importSession`
通过独立可信 header 发送 Binding，并且只接受 `ReadableStream` 或异步
`Uint8Array` iterable。
