import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import vm from "node:vm";

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
  assert.match(html, /id="embeddingEnabled"/);
  assert.match(html, /id="embeddingAPIKey"/);
  assert.match(html, /id="memoryMaxActiveRecords"/);
  assert.match(html, /id="memoryMaxHistory"/);
  assert.match(app, /semantic_embedding/);
  assert.match(app, /embedding_api_key/);
  assert.match(html, /重启 Rin/);
});

test("Console edits the active gameplay policy with revision control", () => {
  assert.match(app, /\/management\/v1\/policy\/config/);
  assert.match(app, /expected_revision/);
  assert.match(app, /JSON\.parse/);
  assert.match(html, /id="policyProfile"/);
  assert.match(html, /id="policyJSON"/);
});

test("Console catches asynchronous action failures and hides invalid task controls", () => {
  assert.match(app, /function runUIAction/);
  assert.match(app, /\.catch\(reportActionError\)/);
  assert.match(app, /status === "paused"/);
  assert.match(app, /\["active", "paused", "waiting-confirmation", "outcome-unknown"\]\.includes\(status\)/);
});

test("Console imports and removes learned SKILL.md documents", () => {
  assert.match(app, /\/management\/v1\/skills\/import/);
  assert.match(app, /\/management\/v1\/skills\/remove/);
  assert.match(app, /file\.size > 64 \* 1024/);
  assert.match(html, /id="importSkillButton"/);
  assert.match(html, /id="skillFileInput"/);
  assert.match(app, /function clearSkillDetail/);
  assert.match(app, /state\.selectedSkill && !state\.skills\.some/);
});

test("Console starts long goals with installed Skill trigger tags", () => {
  assert.match(app, /taskSkillTrigger/);
  assert.match(app, /skill\.triggers/);
  assert.match(app, /tags,/);
  assert.match(html, /id="taskSkillTrigger"/);
  assert.match(html, /id="taskTags"/);
});

test("Console renders machine-bound Plan condition evidence", () => {
  assert.match(app, /condition\.capability/);
  assert.match(app, /condition\.fact_id/);
  assert.match(app, /未绑定/);
});

test("Console manages complete persona data without mixing it with authority", () => {
  assert.match(html, /id="personaInitiativeEnabled"/);
  assert.match(html, /id="personaBoundaries"/);
  assert.match(html, /id="personaRelationships"/);
  assert.match(html, /id="personaBindings"/);
  assert.match(app, /initiative_policy/);
  assert.match(app, /relationship_stances/);
  assert.match(app, /人格只影响表达与决策偏好|默认绑定/);
});

test("Console paginates task timelines and filters full operation context", () => {
  assert.match(app, /after_cursor/);
  assert.match(app, /loadMoreTaskEvents/);
  assert.match(app, /mergeTimelineEvents/);
  assert.match(html, /id="operationHost"/);
  assert.match(html, /id="operationWorld"/);
  assert.match(html, /id="operationTask"/);
  assert.match(app, /host_id: \$\("#operationHost"\)/);
  assert.match(app, /task_id: \$\("#operationTask"\)/);
});

test("Console discards stale page and search responses", () => {
  assert.match(app, /function beginRequest/);
  assert.match(app, /function isCurrentRequest/);
  assert.match(app, /beginRequest\("memories"\)/);
  assert.match(app, /beginRequest\("skills"\)/);
  assert.match(app, /adapter: selected\?\.adapter_id \|\| undefined/);
  assert.match(app, /taskPlan\(result\.plan\)/);
  assert.match(app, /暂停原因/);
  assert.match(app, /step\.evidence_refs/);
});

test("Console renews actor control leases", () => {
  assert.match(app, /data-actor-action="renew"/);
  assert.match(app, /lease_ttl_millis/);
});

test("Console lists external MCP plans without internal task controls", () => {
  assert.match(app, /task_control_available/);
  assert.match(app, /外部计划/);
  assert.match(app, /controller_source/);
  assert.match(app, /当前没有内部任务或外部 MCP 计划/);
});

function consoleHarness(fetch) {
  const elements = new Map();
  const element = (selector) => {
    if (!elements.has(selector)) elements.set(selector, {
      textContent: "", value: "", hidden: true, open: false, style: {},
      classList: { add() {}, remove() {}, toggle() {} },
      showModal() { this.open = true; },
    });
    return elements.get(selector);
  };
  const context = vm.createContext({
    AbortController, DOMException, fetch, setTimeout, clearTimeout,
    sessionStorage: { getItem() { return "test-token"; }, setItem() {}, removeItem() {} },
    document: { addEventListener() {}, querySelector: element, querySelectorAll() { return []; } },
    window: {},
  });
  vm.runInContext(app, context);
  return { context, element, run: (code) => vm.runInContext(code, context) };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("overlapping refreshes share one request and refresh again after settlement", async () => {
  const harness = consoleHarness();
  const gate = deferred();
  let calls = 0;
  harness.context.loadOverview = async () => { calls++; await gate.promise; };
  const first = harness.run("refreshCurrent()");
  const second = harness.run("refreshCurrent()");
  const third = harness.run("refreshCurrent()");
  assert.equal(calls, 1);
  assert.equal(first, second);
  assert.equal(second, third);
  gate.resolve();
  await Promise.all([first, second, third]);
  await harness.run("refreshCurrent()");
  assert.equal(calls, 2);
});

test("navigation aborts old reads without treating cancellation as an outage", async () => {
  let signal;
  const harness = consoleHarness((_url, options) => new Promise((_resolve, reject) => {
    signal = options.signal;
    signal.addEventListener("abort", () => reject(new DOMException("cancelled", "AbortError")), { once: true });
  }));
  harness.run('loadOverview = () => readAPI("/management/v1/runtime", { method: "GET" }); loadActors = async () => {};');
  const oldRefresh = harness.run("refreshCurrent()");
  harness.run('selectView("actors")');
  await oldRefresh;
  assert.equal(signal.aborted, true);
  assert.equal(harness.element("#viewError").textContent, "");
  assert.equal(harness.element("#toast").textContent, "");
  assert.equal(harness.element("#serviceLabel").textContent, "");
});

test("late unauthorized reads cannot clear a replacement token", async () => {
  const response = deferred();
  const harness = consoleHarness(() => response.promise);
  const request = harness.run('readAPI("/management/v1/runtime", { method: "GET" })');
  const rejected = assert.rejects(request, { name: "AbortError" });
  harness.run('setToken("replacement-token")');
  response.resolve({ ok: false, status: 401, text: async () => '{"error":"old token"}' });
  await rejected;
  assert.equal(harness.run("state.token"), "replacement-token");
  assert.equal(harness.element("#authDialog").open, false);
});

test("navigation leaves an explicitly submitted mutation running", async () => {
  let signal;
  const response = deferred();
  const harness = consoleHarness((_url, options) => { signal = options.signal; return response.promise; });
  harness.run('loadActors = async () => {};');
  const mutation = harness.run('api("/management/v1/tasks/control", { body: { task_id: "task.one", action: "cancel" } })');
  harness.run('selectView("actors")');
  assert.equal(signal, undefined);
  response.resolve({ ok: true, text: async () => '{}' });
  await mutation;
});
