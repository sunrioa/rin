# Rin JavaScript Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A zero-dependency ESM `rin.control/v2` client for Node.js 18 and newer.

```javascript
import { RinControlClient } from "./sdk/javascript/src/index.js";

const control = new RinControlClient({
  token: process.env.RIN_CONTROL_TOKEN,
});

console.log(await control.info());
console.log(await control.listWorlds());
```

The constructor supports `baseUrl`, `timeoutMs`, `maxResponseBytes`, and an
injectable `fetch`. It defaults to `http://127.0.0.1:7375`, rejects redirects,
and bounds streamed response bodies.

Requests and responses are ordinary JSON objects or arrays. See
`api/control-openapi.json` for exact fields and `src/index.d.ts` for TypeScript
declarations.

A resolved Promise is not proof that a game action completed. After submitting
an action, use `waitOperation` to reach a terminal state and inspect
`execution_confirmed` and the Host `outcome`.
