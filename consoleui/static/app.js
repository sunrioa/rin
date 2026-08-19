"use strict";

const state = {
  token: sessionStorage.getItem("rin.controlToken") || "",
  view: "overview",
  worlds: [],
  actors: [],
  persona: null,
  memories: [],
  memoryScope: "common",
  selectedActorId: "",
  refreshing: false,
};

const titles = {
  overview: ["运行状态", "概览"], actors: ["游戏实体", "角色"], tasks: ["长目标", "任务"],
  memory: ["认知上下文", "记忆"], persona: ["角色基线", "人格"], skills: ["流程知识", "技能"],
  connections: ["接入管理", "连接"], settings: ["本地偏好", "设置"],
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

document.addEventListener("DOMContentLoaded", () => {
  bindNavigation();
  bindForms();
  $("#refreshButton").addEventListener("click", refreshCurrent);
  $("#lockButton").addEventListener("click", lockConsole);
  $$(".copy").forEach((button) => button.addEventListener("click", () => copyText(button.dataset.copy)));
  if (!state.token) $("#authDialog").showModal();
  else refreshCurrent();
  window.setInterval(() => {
    if (!document.hidden && ["overview", "actors", "tasks"].includes(state.view)) {
      refreshCurrent();
    }
  }, 5000);
});

function bindNavigation() {
  $$(".nav-item").forEach((button) => button.addEventListener("click", () => selectView(button.dataset.view)));
}

function bindForms() {
  $("#authForm").addEventListener("submit", (event) => {
    event.preventDefault();
    setToken($("#authToken").value.trim());
    $("#authDialog").close();
    refreshCurrent();
  });
  $("#settingsForm").addEventListener("submit", (event) => {
    event.preventDefault();
    setToken($("#settingsToken").value.trim());
    toast("连接凭据已更新");
    selectView("overview");
  });
  $("#taskLookupForm").addEventListener("submit", lookupTask);
  $("#newTaskButton").addEventListener("click", openTaskDialog);
  $("#taskForm").addEventListener("submit", startTask);
  $("#cancelTask").addEventListener("click", () => $("#taskDialog").close());
  $("#personaForm").addEventListener("submit", savePersona);
  $("#newMemoryButton").addEventListener("click", () => openMemoryDialog());
  $("#memoryForm").addEventListener("submit", saveMemory);
  $("#cancelMemory").addEventListener("click", () => $("#memoryDialog").close());
  $("#memorySearch").addEventListener("input", debounce(loadMemories, 220));
  $$("[data-memory-scope]").forEach((button) => button.addEventListener("click", () => {
    state.memoryScope = button.dataset.memoryScope;
    $$("[data-memory-scope]").forEach((item) => item.classList.toggle("active", item === button));
    loadMemories();
  }));
}

function selectView(view) {
  state.view = view;
  $$(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.view === view));
  $$(".view").forEach((item) => item.classList.toggle("active", item.id === view));
  $("#eyebrow").textContent = titles[view][0];
  $("#pageTitle").textContent = titles[view][1];
  refreshCurrent();
}

async function refreshCurrent() {
  if (!state.token || state.refreshing) return;
  const loaders = {
    overview: loadOverview, actors: loadActors, tasks: loadTasks, memory: loadMemories,
    persona: loadPersona, skills: loadSkills, connections: loadHealth, settings: () => {
      $("#settingsToken").value = state.token;
    },
  };
  state.refreshing = true;
  try {
    await loaders[state.view]();
    $("#lastUpdated").textContent = new Date().toLocaleTimeString();
  } catch (error) {
    setService(false);
    toast(error.message, true);
  } finally {
    state.refreshing = false;
  }
}

async function loadHealth() {
  await api("/control/v2/health", { method: "GET" });
  setService(true);
}

async function loadOverview() {
  const [health, info, worlds] = await Promise.all([
    api("/control/v2/health", { method: "GET" }),
    api("/control/v2/info", { method: "GET" }),
    api("/control/v2/worlds", { body: {} }),
  ]);
  state.worlds = Array.isArray(worlds) ? worlds : [];
  state.actors = await fetchActors(state.worlds);
  setService(health.status === "ok");
  $("#metricService").textContent = health.status === "ok" ? "运行中" : "异常";
  $("#metricVersion").textContent = info.contract_version || "rin.control/v2";
  $("#metricWorlds").textContent = state.worlds.length;
  $("#metricActors").textContent = state.actors.length;
  $("#metricControl").textContent = summarizeControl(state.actors);
  $("#overviewTable").innerHTML = worldTable(state.worlds);
  $("#attentionList").innerHTML = attentionList(state.worlds, state.actors);
}

async function loadActors() {
  if (!state.worlds.length) state.worlds = await api("/control/v2/worlds", { body: {} });
  state.actors = await fetchActors(state.worlds);
  $("#actorTable").innerHTML = actorTable(state.actors);
  bindActorRows();
  setService(true);
}

async function fetchActors(worlds) {
  const groups = await Promise.all(worlds.map(async (world) => {
    const hostId = world.host_id || world.host?.host_id;
    const worldId = world.world_id || world.id;
    if (!hostId || !worldId) return [];
    const actors = await api("/control/v2/actors", { body: { host_id: hostId, world_id: worldId } });
    return actors.map((actor) => ({ ...actor, host_id: hostId, world_id: worldId }));
  }));
  return groups.flat();
}

async function loadMemories() {
  const result = await api("/management/v1/memories/list", {
    body: { scope: state.memoryScope, search: $("#memorySearch").value.trim(), limit: 200 },
  });
  state.memories = result.records || [];
  renderMemories();
  setService(true);
}

function renderMemories() {
  const container = $("#memoryList");
  if (!state.memories.length) {
    container.innerHTML = '<div class="detail-surface empty-state">当前范围没有匹配的记忆。</div>';
    return;
  }
  container.innerHTML = state.memories.map((record) => {
    const editable = ["player", "system"].includes(record.source);
    return `<article class="memory-card">
      <header><div class="tags">${(record.tags || []).map((tag) => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}</div><span class="revision">公共</span></header>
      <p>${escapeHTML(record.content)}</p>
      <footer><small class="muted">${escapeHTML(record.source || "unknown")} · ${formatTime(record.created_at)}</small>
      <div class="card-actions">${editable ? `<button data-edit-memory="${escapeHTML(record.memory_id)}">编辑</button>` : ""}<button class="delete" data-forget-memory="${escapeHTML(record.memory_id)}">遗忘</button></div></footer>
    </article>`;
  }).join("");
  $$('[data-edit-memory]').forEach((button) => button.addEventListener("click", () => openMemoryDialog(button.dataset.editMemory)));
  $$('[data-forget-memory]').forEach((button) => button.addEventListener("click", () => forgetMemory(button.dataset.forgetMemory)));
}

function openMemoryDialog(memoryId = "") {
  const record = state.memories.find((item) => item.memory_id === memoryId);
  $("#memoryDialogTitle").textContent = record ? "编辑公共记忆" : "新增公共记忆";
  $("#memoryId").value = memoryId;
  $("#memoryContent").value = record?.content || "";
  $("#memoryTags").value = (record?.tags || []).filter((tag) => !["memory-card", "pinned"].includes(tag)).join(", ");
  $("#memoryPinned").checked = (record?.tags || []).includes("pinned");
  $("#memoryDialog").showModal();
}

async function saveMemory(event) {
  event.preventDefault();
  const memoryId = $("#memoryId").value;
  await api("/management/v1/memories/save", { body: {
    memory_id: memoryId || undefined, content: $("#memoryContent").value.trim(),
    tags: splitValues($("#memoryTags").value), pinned: $("#memoryPinned").checked,
  }});
  $("#memoryDialog").close();
  toast(memoryId ? "记忆已更新" : "记忆已创建");
  await loadMemories();
}

async function forgetMemory(memoryId) {
  const record = state.memories.find((item) => item.memory_id === memoryId);
  if (!record || !confirm("从 Rin 记忆中移除这条内容？")) return;
  await api("/management/v1/memories/forget", { body: {
    memory_id: memoryId,
  }});
  toast("记忆已遗忘");
  await loadMemories();
}

async function loadPersona() {
  state.persona = await api("/management/v1/personas", { method: "GET" });
  const defaultBinding = (state.persona.bindings || []).find((binding) => !binding.actor_id && !binding.controller_id);
  const profile = (state.persona.profiles || []).find((item) => item.persona_id === defaultBinding?.persona_id && item.version === defaultBinding?.version) || state.persona.profiles?.[0];
  if (!profile) throw new Error("没有可编辑的默认人格");
  $("#personaRevision").textContent = `revision ${state.persona.revision}`;
  $("#personaIdentity").value = profile.identity || "";
  $("#personaVoice").value = profile.voice || "";
  $("#personaTraits").value = (profile.traits || []).join("\n");
  $("#personaValues").value = (profile.values || []).join("\n");
  $("#personaRules").value = (profile.presentation_rules || []).join("\n");
  state.persona.defaultProfile = profile;
}

async function savePersona(event) {
  event.preventDefault();
  const profile = state.persona.defaultProfile;
  Object.assign(profile, {
    identity: $("#personaIdentity").value.trim(), voice: $("#personaVoice").value.trim(),
    traits: splitLines($("#personaTraits").value), values: splitLines($("#personaValues").value),
    presentation_rules: splitLines($("#personaRules").value),
  });
  const payload = { revision: state.persona.revision, profiles: state.persona.profiles, bindings: state.persona.bindings };
  state.persona = await api("/management/v1/personas", { method: "PUT", body: payload });
  toast("默认人格已更新");
  await loadPersona();
}

async function loadSkills() {
  const result = await api("/skills/v1/list", { body: { limit: 128 } });
  const skills = result.skills || [];
  $("#skillTable").innerHTML = skills.length ? `<table><thead><tr><th>Skill</th><th>说明</th><th>来源</th><th>Adapter</th></tr></thead><tbody>${skills.map((skill) => `<tr><td><code>${escapeHTML(skill.skill_id)}@${escapeHTML(skill.version)}</code></td><td>${escapeHTML(skill.summary || "")}</td><td>${escapeHTML(skill.source || "")}</td><td>${escapeHTML((skill.adapters || []).join(", ") || "通用")}</td></tr>`).join("")}</tbody></table>` : '<div class="detail-surface empty-state">目录中还没有 Skill。</div>';
  setService(true);
}

async function loadTasks() {
  try {
    const result = await api("/management/v1/tasks/list", { body: { limit: 100 } });
    const tasks = result.tasks || [];
    $("#taskTable").innerHTML = tasks.length ? `<table><thead><tr><th>目标</th><th>状态</th><th>阶段</th><th>动作 / 模型</th><th>更新</th></tr></thead><tbody>${tasks.map((task) => `<tr data-task-row="${escapeHTML(task.task_id)}"><td><strong>${escapeHTML(task.goal)}</strong><br><code>${escapeHTML(task.task_id)}</code></td><td><span class="state ${task.status === "completed" ? "good" : task.status === "failed" ? "bad" : "warn"}">${escapeHTML(task.status)}</span></td><td>${escapeHTML(task.current_plan_step_id || task.plan_id || "-")}</td><td>${task.action_count} / ${task.model_calls}</td><td>${new Date(task.updated_at_unix_millis).toLocaleString()}</td></tr>`).join("")}</tbody></table>` : '<div class="detail-surface empty-state">内部 Agent 当前没有任务。</div>';
    $$('[data-task-row]').forEach((row) => row.addEventListener("click", () => showTask(row.dataset.taskRow)));
  } catch (error) {
    if (error.message.includes("not enabled")) {
      $("#taskTable").innerHTML = '<div class="detail-surface empty-state">内部 Agent Runtime 未启用；外部 MCP 任务仍可在 Operation 时间线中查看。</div>';
      return;
    }
    throw error;
  }
}

function openTaskDialog() {
  const selected = state.actors.find((actor) => actor.actor_id === state.selectedActorId);
  if (selected) {
    $("#taskHostId").value = selected.host_id || "";
    $("#taskWorldId").value = selected.world_id || "";
    $("#taskActorId").value = selected.actor_id || "";
  }
  $("#taskDialog").showModal();
}

async function startTask(event) {
  event.preventDefault();
  const task = await api("/management/v1/tasks/start", { body: {
    host_id: $("#taskHostId").value.trim(),
    world_id: $("#taskWorldId").value.trim(),
    actor_id: $("#taskActorId").value.trim(),
    goal: $("#taskGoal").value.trim(),
    planning_mode: $("#taskPlanningMode").value,
  } });
  $("#taskDialog").close();
  $("#taskForm").reset();
  toast("长目标已创建并进入执行队列");
  await loadTasks();
  await showTask(task.task_id);
}

async function lookupTask(event) {
  event.preventDefault();
  const taskId = $("#taskId").value.trim();
  if (!taskId) return;
  await showTask(taskId);
}

async function showTask(taskId) {
  const result = await api("/management/v1/tasks/get", { body: { task_id: taskId } });
  $("#taskDetail").classList.remove("empty-state");
  $("#taskDetail").innerHTML = `<div class="section-heading"><div><h2>${escapeHTML(result.task.goal)}</h2><p>${escapeHTML(result.task.task_id)} · ${escapeHTML(result.task.status)}</p></div><div class="toolbar"><button class="button secondary" data-task-action="run">继续</button><button class="button secondary" data-task-action="resume">恢复</button><button class="button danger" data-task-action="cancel">取消</button></div></div>${taskTimeline(result.timeline.events || [])}`;
  $$('[data-task-action]').forEach((button) => button.addEventListener("click", async () => {
    await api("/management/v1/tasks/control", { body: { task_id: taskId, action: button.dataset.taskAction } });
    toast("任务状态已更新");
    await Promise.all([loadTasks(), showTask(taskId)]);
  }));
}

function worldTable(worlds) {
  if (!worlds.length) return '<div class="detail-surface empty-state">尚无游戏 Host 发布世界。</div>';
  return `<table><thead><tr><th>Host</th><th>世界</th><th>状态</th><th>Epoch</th></tr></thead><tbody>${worlds.map((world) => `<tr><td><code>${escapeHTML(world.host_id || world.host?.host_id || "-")}</code></td><td>${escapeHTML(world.world_id || world.id || "-")}</td><td><span class="state ${world.online === false ? "bad" : "good"}">${world.online === false ? "离线" : "在线"}</span></td><td>${escapeHTML(formatEpoch(world.epoch))}</td></tr>`).join("")}</tbody></table>`;
}

function actorTable(actors) {
  if (!actors.length) return '<div class="detail-surface empty-state">当前世界没有在线或可恢复角色。</div>';
  return `<table><thead><tr><th>角色</th><th>世界</th><th>状态</th><th>任务</th><th>控制源</th></tr></thead><tbody>${actors.map((actor) => `<tr tabindex="0" data-actor-row data-actor-id="${escapeHTML(actor.actor_id)}" class="${actor.actor_id === state.selectedActorId ? "selected" : ""}"><td><strong>${escapeHTML(actor.display_name || actor.name || actor.actor_id)}</strong><br><code>${escapeHTML(actor.actor_id)}</code></td><td>${escapeHTML(actor.world_id)}</td><td><span class="state ${actor.online === false ? "bad" : "good"}">${actor.online === false ? "离线" : "在线"}</span></td><td>${escapeHTML(actor.state?.active_task?.status || actor.state?.status || "-")}</td><td>${escapeHTML(actor.controller?.source || actor.decision_authority?.source || "-")}</td></tr>`).join("")}</tbody></table>`;
}

function bindActorRows() {
  $$('[data-actor-row]').forEach((row) => {
    const select = () => {
      state.selectedActorId = row.dataset.actorId;
      $$('[data-actor-row]').forEach((candidate) => candidate.classList.toggle("selected", candidate === row));
    };
    row.addEventListener("click", select);
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        select();
      }
    });
  });
}

function taskTimeline(events) {
  if (!events.length) return '<div class="timeline empty-state">任务尚未产生时间线事件。</div>';
  return `<div class="timeline">${events.map((event) => {
    const operation = event.operation || {};
    const policy = event.policy || {};
    const usage = event.model_usage || {};
    const memoryCount = (event.memory_context_refs || []).length;
    const skillCount = (event.skill_refs || []).length;
    const meta = [
      event.plan_step_id ? `阶段 ${event.plan_step_id}` : "",
      event.capability?.id ? `${event.capability.id}@${event.capability.version}` : "",
      operation.status ? `操作 ${operation.status}` : "",
      policy.disposition ? `策略 ${policy.disposition}` : "",
      usage.latency_ms !== undefined ? `${usage.latency_ms} ms` : "",
      usage.total_tokens !== undefined ? `${usage.total_tokens} tokens` : "",
      usage.cache_hit_tokens !== undefined ? `缓存命中 ${usage.cache_hit_tokens}` : "",
      memoryCount ? `${memoryCount} 条记忆` : "",
      skillCount ? `${skillCount} 个 Skill` : "",
    ].filter(Boolean);
    const kind = event.event_kind || "task.event";
    const status = operation.execution_confirmed ? "已确认" : event.reason_code || kind;
    return `<article class="timeline-event"><div class="timeline-marker"></div><div><header><strong>${escapeHTML(kind)}</strong><span class="state ${operation.execution_confirmed ? "good" : operation.terminal ? "warn" : ""}">${escapeHTML(status)}</span></header><p>${escapeHTML(event.public_summary || policy.human_summary || "状态已更新。")}</p>${meta.length ? `<div class="timeline-meta">${meta.map((item) => `<span>${escapeHTML(item)}</span>`).join("")}</div>` : ""}<small>${event.occurred_at_unix_millis ? new Date(event.occurred_at_unix_millis).toLocaleString() : ""}</small></div></article>`;
  }).join("")}</div>`;
}

function attentionList(worlds, actors) {
  const notices = [];
  worlds.filter((world) => world.online === false).forEach((world) => notices.push(["世界离线", world.world_id || "未知世界", "bad"]));
  actors.filter((actor) => actor.online === false).forEach((actor) => notices.push(["角色离线", actor.display_name || actor.actor_id, "warn"]));
  actors.filter((actor) => actor.state?.active_task?.status === "failed").forEach((actor) => notices.push(["任务失败", actor.state.active_task.goal || actor.actor_id, "bad"]));
  if (!notices.length) notices.push(["当前没有待处理问题", "Control、Host 与已发布角色状态正常。", "good"]);
  return notices.map(([title, detail, kind]) => `<div class="notice"><div><strong>${escapeHTML(title)}</strong><p>${escapeHTML(detail)}</p></div><span class="state ${kind}">${kind === "good" ? "正常" : "检查"}</span></div>`).join("");
}

async function api(path, options = {}) {
  const method = options.method || "POST";
  const response = await fetch(path, {
    method,
    headers: { "Authorization": `Bearer ${state.token}`, ...(options.body !== undefined ? { "Content-Type": "application/json" } : {}) },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  const text = await response.text();
  let payload = {};
  try { payload = text ? JSON.parse(text) : {}; } catch { payload = { error: text }; }
  if (!response.ok) {
    if (response.status === 401) lockConsole();
    throw new Error(payload.error?.message || payload.error || `请求失败 (${response.status})`);
  }
  return payload;
}

function setToken(token) {
  state.token = token;
  sessionStorage.setItem("rin.controlToken", token);
  $("#settingsToken").value = token;
}

function lockConsole() {
  state.token = "";
  sessionStorage.removeItem("rin.controlToken");
  setService(false);
  $("#authToken").value = "";
  if (!$("#authDialog").open) $("#authDialog").showModal();
}

function setService(healthy) {
  $("#serviceDot").classList.toggle("good", healthy);
  $("#serviceLabel").textContent = healthy ? "本地服务正常" : "尚未连接";
}

function summarizeControl(actors) {
  const sources = new Set(actors.map((actor) => actor.controller?.source || actor.decision_authority?.source).filter(Boolean));
  if (!sources.size) return "-";
  if (sources.size > 1) return "混合";
  return [...sources][0] === "external" ? "外部" : "内部";
}

function formatEpoch(epoch) {
  if (!epoch) return "-";
  return `${epoch.host || 0}/${epoch.world || 0}/${epoch.timeline || 0}`;
}

function formatTime(point) {
  if (!point || point.clock !== "realtime") return point?.value ?? "-";
  return new Date(point.value).toLocaleString();
}

function splitValues(value) { return [...new Set(value.split(",").map((item) => item.trim()).filter(Boolean))]; }
function splitLines(value) { return [...new Set(value.split("\n").map((item) => item.trim()).filter(Boolean))]; }
function escapeHTML(value) { return String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char])); }
function debounce(fn, wait) { let timer; return () => { clearTimeout(timer); timer = setTimeout(fn, wait); }; }
async function copyText(value) { await navigator.clipboard.writeText(value); toast("命令已复制"); }
function toast(message, bad = false) { const element = $("#toast"); element.textContent = message; element.style.background = bad ? "#74322f" : "#202a25"; element.classList.add("show"); setTimeout(() => element.classList.remove("show"), 2600); }
