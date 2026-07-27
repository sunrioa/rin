# Rin JavaScript SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A zero-dependency Promise client for Node.js 18+ or any standard Fetch host.
The package includes TypeScript declarations.

```js
import { RinClient } from "@sunrioa/rin-sdk";

const rin = new RinClient("http://127.0.0.1:7374");
const capabilities = await rin.negotiateCapabilities();
console.log(capabilities.release_version);
```

`negotiateCapabilities()` fails closed unless the Runtime speaks
`rin.protocol/v2`.
Use `createRinId("request")` and `createRinId("event")` once, persist the
result with the operation, and reuse it for every exact retry.

The bundled TypeScript declarations provide OpenAPI-aligned types for the
authoritative create/propose/report path, including `CreateSessionRequest`,
`ProposeRequest`, `ProposalResult`, `ReportActionRequest`, and `MutationResult`.
Response types deliberately tolerate additive fields.

`WorkflowCoordinator` combines the compatible `ProposalAttemptCoordinator` and
`OutcomeOutbox` primitives behind `begin`, `resumePendingWork`,
`applyAndEnqueueOutcome`, and `drainOutbox`. Supply a Workflow Store and a
validated `HostDurability`. An idempotent apply receives the stable operation
ID; only `transactional-action` invokes `settleProposalAttempt` as an atomic
game transaction. Outbox draining deletes nothing until Rin returns normal or
explicit duplicate success. The SDK intentionally supplies no unsafe
in-memory production default. See
[Host durability profiles](../../docs/host-durability.md).

`OpaqueSnapshotPersistence` stores bounded UTF-8 JSON bytes and returns the
complete object, including additive fields unknown to this SDK version. The
injected store must protect Snapshot bytes like the Event Log.

Run directly from this checkout:

```bash
node sdk/javascript/examples/quickstart.js
cd sdk/javascript && npm test
```

Calls are Promise-based. Apply engine state only after returning to the
engine's main thread and validating the proposal against a local allowlist.

Session Transfer is streamed and never returned as one large string. The
caller owns the source/sink and decides when to close it. Transfer has an
independent two-minute default deadline; configure `transferTimeoutMs` without
weakening the ordinary five-second request deadline:

```js
import { createReadStream, createWriteStream } from "node:fs";
import { Readable, Writable } from "node:stream";

const request = {
  protocol_version: "rin.protocol/v2",
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

`exportSession` resolves only after a valid terminal `complete` frame. A
terminal `error`, truncation, invalid order, or oversized frame rejects the
Promise. `importSession` sends the Binding through independent trusted headers
and accepts only a `ReadableStream` or async `Uint8Array` iterable.
