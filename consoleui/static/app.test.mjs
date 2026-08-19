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
  assert.doesNotMatch(app, /api_key\s*:/);
  assert.doesNotMatch(html, /API Key|apikey|api_key/i);
});
