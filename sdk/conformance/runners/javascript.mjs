#!/usr/bin/env node
// Run the JavaScript SDK against the shared live Sidecar corpus.

import assert from "node:assert/strict";

import {
  PROTOCOL_VERSION,
  RinClient,
  RinTransportError,
} from "../../javascript/src/index.js";

const body = JSON.parse(process.env.RIN_SDK_CORPUS_BODY);
const options = { token: process.env.RIN_SDK_CORPUS_TOKEN };
const client = new RinClient(process.env.RIN_SDK_CORPUS_BASE_URL, options);
const health = await client.health();
assert.equal(health.protocol_version, PROTOCOL_VERSION);
const first = await client.createSession(body);
const retry = await client.createSession(body);
assert.equal(first.duplicate, false);
assert.equal(retry.duplicate, true);
assert.equal(retry.revision, first.revision);
assert.equal(retry.head_hash, first.head_hash);

const slow = new RinClient(process.env.RIN_SDK_CORPUS_SLOW_URL, {
  ...options,
  timeoutMs: 50,
});
await assert.rejects(
  slow.createSession(body),
  (error) => error instanceof RinTransportError && error.code === "transport_timeout",
);

console.log("JavaScript SDK live Sidecar corpus passed");
