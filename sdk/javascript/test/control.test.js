import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  CONTROL_CONTRACT_VERSION,
  CONTROL_MAX_RESPONSE_BYTES,
  RinAPIError,
  RinConfigurationError,
  RinControlClient,
  RinProtocolError,
} from "../src/index.js";

const fixtures = JSON.parse(readFileSync(
  new URL("../../../api/control-v2-fixtures.json", import.meta.url),
  "utf8",
));
const token = "control-fixture-token-32-bytes!!";

test("Control client encodes every V2 client route from the shared fixture", async () => {
  const requests = [];
  const fetch = async (url, options) => {
    requests.push({ url: new URL(url), options });
    const value = new URL(url).pathname === "/control/v2/info"
      ? { contract_version: CONTROL_CONTRACT_VERSION, principal: { id: "player.fixture" } }
      : { status: "ok" };
    return new Response(JSON.stringify(value), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  };
  const client = new RinControlClient(undefined, { token, fetch });
  const cases = [
    ["info", () => client.info(), "GET", "/control/v2/info", undefined],
    ["worlds", () => client.listWorlds(), "POST", "/control/v2/worlds", {}],
    ["actors", () => client.listActors(fixtures.world_target), "POST", "/control/v2/actors", fixtures.world_target],
    ["actor", () => client.getActor(fixtures.actor_target), "POST", "/control/v2/actor", fixtures.actor_target],
    ["wait_actor", () => client.waitActor(fixtures.wait_actor), "POST", "/control/v2/wait-actor", fixtures.wait_actor],
    ["observe", () => client.observeActor(fixtures.actor_target), "POST", "/control/v2/observe", fixtures.actor_target],
    ["capabilities", () => client.listCapabilities(fixtures.actor_target), "POST", "/control/v2/capabilities", fixtures.actor_target],
    ["capability", () => client.describeCapability(fixtures.describe_capability), "POST", "/control/v2/capability", fixtures.describe_capability],
    ["acquire", () => client.acquireController(fixtures.acquire_controller), "POST", "/control/v2/controllers/acquire", fixtures.acquire_controller],
    ["renew", () => client.renewController(fixtures.renew_controller), "POST", "/control/v2/controllers/renew", fixtures.renew_controller],
    ["release", () => client.releaseController(fixtures.release_controller), "POST", "/control/v2/controllers/release", fixtures.release_controller],
    ["get_controller", () => client.getController(fixtures.actor_target), "POST", "/control/v2/controllers/get", fixtures.actor_target],
    ["submit", () => client.submitAction(fixtures.submit_action), "POST", "/control/v2/actions/submit", fixtures.submit_action],
    ["confirm", () => client.confirmAction(fixtures.operation_target), "POST", "/control/v2/actions/confirm", fixtures.operation_target],
    ["get_operation", () => client.getOperation(fixtures.operation_target), "POST", "/control/v2/operations/get", fixtures.operation_target],
    ["wait_operation", () => client.waitOperation(fixtures.wait_operation), "POST", "/control/v2/operations/wait", fixtures.wait_operation],
    ["cancel", () => client.cancelOperation(fixtures.operation_target), "POST", "/control/v2/operations/cancel", fixtures.operation_target],
    ["emergency_stop", () => client.setEmergencyStop(fixtures.emergency_stop), "POST", "/control/v2/emergency-stop", fixtures.emergency_stop],
  ];

  for (const [, invoke, method, path, body] of cases) {
    await invoke();
    const request = requests.at(-1);
    assert.equal(request.url.origin, "http://127.0.0.1:7375");
    assert.equal(request.url.pathname, path);
    assert.equal(request.options.method, method);
    assert.equal(request.options.headers.Authorization, `Bearer ${token}`);
    assert.equal(request.options.redirect, "error");
    assert.deepEqual(
      request.options.body === undefined ? undefined : JSON.parse(request.options.body),
      body,
    );
  }
});

test("Control client preserves stable errors and safety bounds", async () => {
  assert.equal(CONTROL_MAX_RESPONSE_BYTES, 8 * 1024 * 1024);
  const defaultOrigin = new RinControlClient({
    token,
    fetch: async () => new Response("[]", {
      status: 200,
      headers: { "content-type": "application/json" },
    }),
  });
  assert.deepEqual(await defaultOrigin.listWorlds(), []);
  assert.throws(
    () => new RinControlClient("http://example.com:7375", { token }),
    RinConfigurationError,
  );
  assert.throws(
    () => new RinControlClient(undefined, { token: "short" }),
    RinConfigurationError,
  );
  const stale = new RinControlClient(undefined, {
    token,
    fetch: async () => new Response(JSON.stringify({
      error: "Actor observation changed",
      code: "stale",
    }), {
      status: 409,
      headers: { "content-type": "application/json" },
    }),
  });
  await assert.rejects(
    stale.getActor(fixtures.actor_target),
    (error) => error instanceof RinAPIError && error.code === "stale" && error.status === 409,
  );
  const nonJSON = new RinControlClient(undefined, {
    token,
    fetch: async () => new Response("no", {
      status: 200,
      headers: { "content-type": "text/plain" },
    }),
  });
  await assert.rejects(nonJSON.listWorlds(), RinProtocolError);
  const redirect = new RinControlClient(undefined, {
    token,
    fetch: async () => new Response("{}", {
      status: 302,
      headers: { "content-type": "application/json", location: "/elsewhere" },
    }),
  });
  await assert.rejects(
    redirect.listWorlds(),
    (error) => error.code === "redirect_rejected",
  );
});
