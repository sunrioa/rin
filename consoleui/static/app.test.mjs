import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = dirname(fileURLToPath(import.meta.url));
const app = readFileSync(join(root, "app.js"), "utf8");
const html = readFileSync(join(root, "index.html"), "utf8");

test("Console uses the management diagnostics contract", () => {
  assert.match(app, /\/management\/v1\/diagnostics/);
  assert.match(app, /renderDiagnostics\(diagnostics\)/);
  assert.match(app, /renderConfig\(diagnostics\)/);
  assert.match(html, /id="diagnosticList"/);
  assert.match(html, /id="configSummary"/);
});

test("Console keeps MCP actions as copy-only commands", () => {
  assert.match(app, /mcp\.commands/);
  assert.match(app, /mcpStatusSummary/);
  assert.match(html, /MCP 命令/);
  assert.match(app, /copy-command/);
  assert.doesNotMatch(app, /fetch\([^)]*mcp install/);
});

test("Console displays credential presence without a credential field", () => {
  assert.match(app, /credential_configured/);
  assert.match(app, /正文隐藏/);
  assert.match(app, /clear_api_key/);
  assert.doesNotMatch(app, /result\.api_key|snapshot\.api_key/);
  assert.match(html, /id="agentAPIKey"/);
  assert.match(html, /id="clearAgentAPIKey"/);
});

test("Console edits the complete internal Agent model contract", () => {
  assert.match(app, /\/management\/v1\/agent\/config/);
  assert.match(app, /max_context_characters/);
  assert.match(app, /max_output_tokens/);
  assert.match(app, /temperature/);
  assert.match(html, /id="agentBaseURL"/);
  assert.match(html, /id="agentThinkingMode"/);
  assert.match(html, /重启 Rin/);
});
