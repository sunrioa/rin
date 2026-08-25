# Rin JavaScript Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A zero-dependency ESM `rin.control/v2` client for Node.js 18 and newer.

The same client exposes the fixed `/plans/v1/*` task-plan routes as raw JSON methods.

From the repository root, run the checked-in
[quick start](examples/quickstart.js):

```bash
node ./sdk/javascript/examples/quickstart.js
```

The supported constructor forms are
`new RinControlClient({ token, timeoutMs?, maxResponseBytes?, fetch? })` and
`new RinControlClient(baseUrl, { token, timeoutMs?, maxResponseBytes?, fetch? })`.
The first form defaults to `http://127.0.0.1:7375`. The client rejects redirects
and bounds streamed response bodies.

Requests use ordinary JSON objects; responses may be JSON objects or arrays. See
[`api/control-openapi.json`](../../api/control-openapi.json) for exact fields and
[`src/index.d.ts`](src/index.d.ts) for TypeScript declarations.

A resolved Promise is not proof that a game action completed. After submitting
an action, use `waitOperation` to reach a terminal state and inspect
`execution_confirmed` and the Host `outcome`.
