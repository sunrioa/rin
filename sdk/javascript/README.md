# Rin JavaScript SDK

[English](README.md) | [简体中文](README.zh-CN.md)

Requires Node.js 18+ or a host that implements the standard Fetch API. The
package has no runtime dependencies and includes TypeScript declarations.

```js
import { RinClient } from "@sunrioa/rin-sdk";

const rin = new RinClient("http://127.0.0.1:7374");
console.log(await rin.health());
```

Run directly from this checkout:

```bash
node sdk/javascript/examples/quickstart.js
cd sdk/javascript && npm test
```

Calls are Promise-based. Apply engine state only after returning to the
engine's main thread and validating the proposal against a local allowlist.
