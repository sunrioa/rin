#!/usr/bin/env node
import { randomBytes } from "node:crypto";
import { homedir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline/promises";
import { stdin, stdout } from "node:process";
import { StoryWorkflowStore } from "./workflow-store.js";
import { runStory } from "./runner.js";

const options = parseArguments(process.argv.slice(2));
const preference = options.preference || await askPreference();
const savePath = options.save || defaultSavePath();
const store = await new StoryWorkflowStore(savePath).load();
const sessionId = options.session || store.game.session_id ||
  `story.session.${randomBytes(8).toString("hex")}`;
let shown = "";

try {
  const result = await runStory({
    baseUrl: options.url || process.env.RIN_URL || "http://127.0.0.1:7374",
    token: process.env.RIN_TOKEN || "",
    mode: options.mode || "auto",
    sessionId,
    preference,
    store,
    applyAction: async (action) => {
      shown = action.description;
      if (!options.json) console.log(`\n${shown}`);
    },
  });
  const output = {
    session_id: sessionId,
    mode: result.mode,
    preference,
    action_id: result.action.id,
    recalled: result.recalled,
    policy_source: result.policy_source || "rule-tree",
    provider_cost_usd: result.provider_cost_usd,
    text: shown,
  };
  if (result.local_reason) output.local_reason = result.local_reason;
  if (options.json) console.log(JSON.stringify(output));
} catch (error) {
  console.error(`terminal story: ${error.code || "error"}: ${error.message}`);
  process.exitCode = 1;
}

function parseArguments(arguments_) {
  const result = {};
  for (let index = 0; index < arguments_.length; index++) {
    const argument = arguments_[index];
    if (argument === "--json") {
      result.json = true;
      continue;
    }
    const key = {
      "--mode": "mode",
      "--preference": "preference",
      "--session": "session",
      "--save": "save",
      "--url": "url",
    }[argument];
    if (!key || !arguments_[index + 1]) {
      throw new Error(`unknown or incomplete argument: ${argument}`);
    }
    result[key] = arguments_[++index];
  }
  return result;
}

async function askPreference() {
  const terminal = createInterface({ input: stdin, output: stdout });
  try {
    const answer = (await terminal.question(
      "At the last station, tell Mira your preference (tea/coffee): ",
    )).trim().toLowerCase();
    if (answer !== "tea" && answer !== "coffee") {
      throw new Error("preference must be tea or coffee");
    }
    return answer;
  } finally {
    terminal.close();
  }
}

function defaultSavePath() {
  const root = process.platform === "win32"
    ? (process.env.LOCALAPPDATA || homedir())
    : (process.env.XDG_DATA_HOME || join(homedir(), ".local", "share"));
  return join(root, "rin-terminal-story", "save.json");
}
