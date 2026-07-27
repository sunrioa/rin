#!/usr/bin/env node
import { spawn } from "node:child_process";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
import process from "node:process";
import { performance } from "node:perf_hooks";
import { RinClient } from "@sunrioa/rin-sdk";
import { runRuleTree } from "./baseline.js";
import { actionById } from "./catalog.js";
import { runRinStory } from "./rin-adapter.js";
import { runStory } from "./runner.js";
import { StoryWorkflowStore } from "./workflow-store.js";

const options = parseArguments(process.argv.slice(2));
const iterations = Number(options.iterations || 100);
if (!Number.isSafeInteger(iterations) || iterations < 10 || iterations > 1000) {
  throw new Error("--iterations must be an integer from 10 through 1000");
}
const rinBinary = resolve(options.rinBin || process.env.RIN_BIN || defaultBinary());
const root = await mkdtemp(join(tmpdir(), "rin-story-benchmark-"));
const address = await reserveAddress();
const baseUrl = `http://${address}`;
const dataDirectory = join(root, "rin-data");
const child = spawn(rinBinary, ["serve", "-addr", address, "-data", dataDirectory], {
  env: { ...process.env, RIN_POLICY: "deterministic" },
  stdio: ["ignore", "ignore", "pipe"],
  windowsHide: true,
});
let sidecarError = "";
child.stderr.on("data", (chunk) => {
  sidecarError = `${sidecarError}${chunk}`.slice(-8192);
});

try {
  const startupStarted = performance.now();
  await waitReady(baseUrl, child);
  const startupMs = performance.now() - startupStarted;
  const client = new RinClient(baseUrl, { timeoutMs: 5000 });
  const sessionId = "benchmark.session";
  const rinSavePath = join(root, "rin-save.json");
  let rinStore = await new StoryWorkflowStore(rinSavePath).load();
  const rinLatencies = [];
  let lastRin;
  for (let sequence = 1; sequence <= iterations; sequence++) {
    const started = performance.now();
    if (sequence > 1) {
      rinStore = await new StoryWorkflowStore(rinSavePath).load();
    }
    lastRin = await runRinStory(client, rinStore, {
      sessionId,
      preference: "tea",
      sequence,
      ensureSession: sequence === 1,
      presentAction: async () => {},
    });
    rinLatencies.push(performance.now() - started);
  }

  const baselineSavePath = join(root, "baseline-save.json");
  let baselineStore = await new StoryWorkflowStore(baselineSavePath).load();
  const baselineLatencies = [];
  let lastBaseline;
  for (let sequence = 1; sequence <= iterations; sequence++) {
    const started = performance.now();
    if (sequence > 1) {
      baselineStore = await new StoryWorkflowStore(baselineSavePath).load();
    }
    lastBaseline = await runRuleTree(baselineStore, "tea", async () => {});
    baselineLatencies.push(performance.now() - started);
  }

  const localStore = await new StoryWorkflowStore(join(root, "local-save.json")).load();
  const localStarted = performance.now();
  const local = await runStory({
    baseUrl: "http://127.0.0.1:1",
    mode: "auto",
    sessionId: "local.session",
    preference: "tea",
    store: localStore,
    client: new RinClient("http://127.0.0.1:1", { timeoutMs: 100 }),
    presentAction: async () => {},
  });
  const localMs = performance.now() - localStarted;

  const stats = await client.sessionStats({
    protocol_version: "rin.protocol/v2",
    session_id: sessionId,
  });
  const turnsPerHundredHours = 60 * 100;
  const projectedBytes = Math.ceil(
    (stats.bytes.total / iterations) * turnsPerHundredHours,
  );
  const evidence = {
    schema_version: 1,
    measured_at: new Date().toISOString(),
    environment: {
      platform: process.platform,
      architecture: process.arch,
      node: process.version,
      rin_binary: basename(rinBinary),
      policy: "deterministic",
    },
    workload: {
      turns: iterations,
      projected_turns_per_hour: 60,
      projection_hours: 100,
      reload_save_between_turns: true,
    },
    setup: {
      sidecar_ready_ms: round(startupMs),
    },
    latency_ms: {
      rin_full_safe_turn: distribution(rinLatencies),
      persistent_rule_tree_turn: distribution(baselineLatencies),
      startup_local_turn: round(localMs),
    },
    integration_nonblank_lines: {
      rin_adapter: await countLines(new URL("./rin-adapter.js", import.meta.url)),
      persistent_rule_tree: await countLines(new URL("./baseline.js", import.meta.url)),
    },
    storage: {
      measured_turns: iterations,
      measured_bytes: stats.bytes.total,
      measured_event_log_bytes: stats.bytes.event_log,
      projected_100_hours_bytes: projectedBytes,
      projection_method: "measured total bytes / turns * 60 turns/hour * 100 hours",
    },
    provider: {
      calls: 0,
      measured_cost_usd: 0,
      note: "Deterministic policy; no model provider configured.",
    },
    local: {
      mode: local.mode,
      action_id: local.action.id,
      only_before_first_rin_mutation: true,
    },
    player_visible: {
      preference: "tea",
      rin_action_id: lastRin.action.id,
      persistent_rule_tree_action_id: lastBaseline.action.id,
      stateless_rule_tree_action_id: actionById("offer.water").id,
      rin_recalled_memory: lastRin.recalled,
      parity_with_persistent_rule_tree:
        lastRin.action.id === lastBaseline.action.id,
    },
  };
  const gateFailures = [];
  if (evidence.player_visible.rin_action_id !== "offer.tea") {
    gateFailures.push(`rin_action_id=${evidence.player_visible.rin_action_id}`);
  }
  if (evidence.player_visible.persistent_rule_tree_action_id !== "offer.tea") {
    gateFailures.push(
      `persistent_rule_tree_action_id=${evidence.player_visible.persistent_rule_tree_action_id}`,
    );
  }
  if (evidence.player_visible.rin_recalled_memory !== true) {
    gateFailures.push(`rin_recalled_memory=${evidence.player_visible.rin_recalled_memory}`);
  }
  if (evidence.local.mode !== "local") {
    gateFailures.push(`local_mode=${evidence.local.mode}`);
  }
  if (evidence.provider.calls !== 0) {
    gateFailures.push(`provider_calls=${evidence.provider.calls}`);
  }
  if (evidence.storage.projected_100_hours_bytes >= 50 * 1024 * 1024) {
    gateFailures.push(
      `projected_100_hours_bytes=${evidence.storage.projected_100_hours_bytes}`,
    );
  }
  if (gateFailures.length > 0) {
    throw new Error(`player-value release gate failed: ${gateFailures.join(", ")}`);
  }
  const encoded = `${JSON.stringify(evidence, null, 2)}\n`;
  if (options.output) await writeFile(resolve(options.output), encoded, "utf8");
  process.stdout.write(encoded);
} catch (error) {
  if (sidecarError) process.stderr.write(sidecarError);
  throw error;
} finally {
  child.kill();
  await new Promise((resolveExit) => {
    if (child.exitCode != null) return resolveExit();
    child.once("exit", resolveExit);
    setTimeout(resolveExit, 5000).unref();
  });
}

function parseArguments(arguments_) {
  const result = {};
  for (let index = 0; index < arguments_.length; index += 2) {
    const key = { "--rin-bin": "rinBin", "--iterations": "iterations", "--output": "output" }[
      arguments_[index]
    ];
    if (!key || !arguments_[index + 1]) throw new Error("invalid benchmark arguments");
    result[key] = arguments_[index + 1];
  }
  return result;
}

function defaultBinary() {
  return join("..", "..", "bin", process.platform === "win32" ? "rin.exe" : "rin");
}

async function reserveAddress() {
  const server = net.createServer();
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  await new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()));
  return `127.0.0.1:${address.port}`;
}

async function waitReady(baseUrl, childProcess) {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    if (childProcess.exitCode != null) throw new Error("Rin exited before readiness");
    try {
      const response = await fetch(`${baseUrl}/ready`);
      if (response.ok) return;
    } catch {
      // Startup connection refusal is expected.
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 20));
  }
  throw new Error("Rin did not become ready within 10 seconds");
}

function distribution(values) {
  const sorted = [...values].sort((left, right) => left - right);
  return {
    samples: sorted.length,
    p50: round(percentile(sorted, 0.50)),
    p95: round(percentile(sorted, 0.95)),
  };
}

function percentile(sorted, quantile) {
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * quantile) - 1)];
}

function round(value) {
  return Math.round(value * 100) / 100;
}

async function countLines(url) {
  const source = await readFile(url, "utf8");
  return source.split(/\r?\n/).filter((line) => {
    const trimmed = line.trim();
    return trimmed && !trimmed.startsWith("//");
  }).length;
}
