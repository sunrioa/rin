"use strict";

const state = {
  token: sessionStorage.getItem("rin.controlToken") || "",
  view: "overview",
  worlds: [],
  actors: [],
  persona: null,
  memories: [],
  skills: [],
  selectedSkill: null,
  selectedTask: null,
  operations: [],
  diagnostics: null,
  memoryScope: "common",
  selectedActorId: "",
  viewEpoch: 0,
  requestVersions: new Map(),
  viewController: new AbortController(),
  refresh: null,
};

const titles = {
  overview: ["运行状态", "概览"], actors: ["游戏实体", "角色"], tasks: ["长目标", "任务"],
  operations: ["权威结果", "执行"],
  memory: ["认知上下文", "记忆"], persona: ["角色基线", "人格"], skills: ["流程知识", "技能"],
  connections: ["接入管理", "连接"], settings: ["本地偏好", "设置"],
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

document.addEventListener("DOMContentLoaded", () => {
  if (window.location.protocol === "file:") {
    $("#launchDialog").showModal();
    return;
  }
  bindNavigation();
  bindForms();
  onAsync($("#refreshButton"), "click", refreshCurrent);
  $("#lockButton").addEventListener("click", lockConsole);
  $$(".copy").forEach((button) => onAsync(button, "click", () => copyText(button.dataset.copy)));
  if (!state.token) $("#authDialog").showModal();
  else refreshCurrent();
  window.setInterval(() => {
    if (!document.hidden && ["overview", "actors", "tasks", "operations"].includes(state.view)) {
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
  $("#agentConfigForm").addEventListener("submit", saveAgentConfig);
  $("#policyConfigForm").addEventListener("submit", savePolicyConfig);
  $("#clearAgentAPIKey").addEventListener("change", syncAgentCredentialInputs);
  $("#agentAPIKey").addEventListener("input", syncAgentCredentialInputs);
  $("#embeddingEnabled").addEventListener("change", syncEmbeddingInputs);
  $("#clearEmbeddingAPIKey").addEventListener("change", syncEmbeddingCredentialInputs);
  $("#embeddingAPIKey").addEventListener("input", syncEmbeddingCredentialInputs);
  onAsync($("#taskLookupForm"), "submit", lookupTask);
  onAsync($("#newTaskButton"), "click", openTaskDialog);
	 onAsync($("#taskArchive"), "change", loadTasks);
  onAsync($("#taskForm"), "submit", startTask);
  $("#taskCompletionMode").addEventListener("change", () => {
    $("#taskCompletionConditionsLabel").hidden = $("#taskCompletionMode").value !== "host-evidence";
    $("#taskCompletionRequirementsLabel").hidden = $("#taskCompletionMode").value !== "host-evidence";
  });
  $("#cancelTask").addEventListener("click", () => $("#taskDialog").close());
  onAsync($("#personaForm"), "submit", savePersona);
  $("#newMemoryButton").addEventListener("click", () => openMemoryDialog());
  onAsync($("#memoryForm"), "submit", saveMemory);
  $("#cancelMemory").addEventListener("click", () => $("#memoryDialog").close());
  $("#memorySearch").addEventListener("input", debounce(loadMemories, 220));
  $("#skillSearch").addEventListener("input", renderSkills);
  $("#newSkillButton").addEventListener("click", () => openSkillDialog());
  $("#importSkillButton").addEventListener("click", () => $("#skillFileInput").click());
  onAsync($("#skillFileInput"), "change", importSkillFile);
  onAsync($("#reloadSkillsButton"), "click", reloadSkills);
  onAsync($("#skillForm"), "submit", saveSkill);
  $("#cancelSkill").addEventListener("click", () => $("#skillDialog").close());
  onAsync($("#operationStatus"), "change", loadOperations);
  ["operationHost", "operationWorld", "operationActor", "operationTask"].forEach((id) => {
    $("#" + id).addEventListener("input", debounce(loadOperations, 220));
  });
  onAsync($("#refreshDiagnosticsButton"), "click", loadHealth);
  $$("[data-memory-scope]").forEach((button) => button.addEventListener("click", () => {
    state.memoryScope = button.dataset.memoryScope;
    $$("[data-memory-scope]").forEach((item) => item.classList.toggle("active", item === button));
    runUIAction(loadMemories);
  }));
}

function selectView(view) {
  state.view = view;
  invalidateViewRequests();
  clearViewError();
  $$(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.view === view));
  $$(".view").forEach((item) => item.classList.toggle("active", item.id === view));
  $("#eyebrow").textContent = titles[view][0];
  $("#pageTitle").textContent = titles[view][1];
  refreshCurrent();
}

function invalidateViewRequests() {
  state.viewController.abort();
  state.viewController = new AbortController();
  state.viewEpoch += 1;
  state.refresh = null;
}

function refreshCurrent() {
  if (!state.token) return Promise.resolve();
  if (state.refresh?.epoch === state.viewEpoch) return state.refresh.promise;
  const refresh = { view: state.view, epoch: state.viewEpoch };
  refresh.promise = performRefresh(refresh).finally(() => {
    if (state.refresh === refresh) state.refresh = null;
  });
  state.refresh = refresh;
  return refresh.promise;
}

async function performRefresh({ view, epoch }) {
  const loaders = {
    overview: loadOverview, actors: loadActors, tasks: loadTasks, operations: loadOperations,
    memory: loadMemories,
    persona: loadPersona, skills: loadSkills, connections: loadHealth, settings: loadSettings,
  };
  try {
    await loaders[view]();
    if (view !== state.view || epoch !== state.viewEpoch) return;
    clearViewError();
    $("#lastUpdated").textContent = new Date().toLocaleTimeString();
  } catch (error) {
    if (error.name === "AbortError") return;
    if (view !== state.view || epoch !== state.viewEpoch) return;
    if (error.offline || error.status === 401) setService(false);
    showViewError(error.message);
    toast(error.message, true);
  }
}

function beginRequest(key) {
  const version = (state.requestVersions.get(key) || 0) + 1;
  state.requestVersions.set(key, version);
  return { key, version, viewEpoch: state.viewEpoch };
}

function isCurrentRequest(request) {
  return state.requestVersions.get(request.key) === request.version &&
    request.viewEpoch === state.viewEpoch;
}

async function loadHealth() {
  const request = beginRequest("diagnostics");
  const diagnostics = await readAPI("/management/v1/diagnostics", { method: "GET" });
  if (!isCurrentRequest(request)) return;
  state.diagnostics = diagnostics;
  renderDiagnostics(diagnostics);
  setService((diagnostics.connections || []).some((item) => item.id === "control-plane" && item.status === "ok"));
}

async function loadSettings() {
  const request = beginRequest("settings");
  $("#settingsToken").value = state.token;
  const [diagnostics, agentConfig, policyConfig] = await Promise.all([
    readAPI("/management/v1/diagnostics", { method: "GET" }),
    readAPI("/management/v1/agent/config", { method: "GET" }),
    readAPI("/management/v1/policy/config", { method: "GET" }),
  ]);
  if (!isCurrentRequest(request)) return;
  state.diagnostics = diagnostics;
  renderConfig(diagnostics);
  renderAgentConfig(agentConfig);
  renderPolicyConfig(policyConfig);
  setService((diagnostics.connections || []).some((item) => item.id === "control-plane" && item.status === "ok"));
}

function renderPolicyConfig(snapshot) {
  const config = snapshot.config || {};
  $("#policyProfile").value = config.profile || "guarded";
  $("#policyRevision").value = String(config.revision || 0);
  $("#policyConfigForm").dataset.revision = String(config.revision || 0);
  $("#policyJSON").value = JSON.stringify(config, null, 2);
  $("#policyConfigNotice").hidden = true;
}

async function savePolicyConfig(event) {
  event.preventDefault();
  let config;
  try {
    config = JSON.parse($("#policyJSON").value);
  } catch (error) {
    showViewError("策略 JSON 格式错误：" + error.message);
    toast("策略 JSON 格式错误", true);
    return;
  }
  config.profile = $("#policyProfile").value;
  const expectedRevision = Number.parseInt($("#policyConfigForm").dataset.revision || "0", 10);
  try {
    const result = await api("/management/v1/policy/config", {
      method: "PUT", body: { expected_revision: expectedRevision, config },
    });
    renderPolicyConfig(result);
    $("#policyConfigNotice").textContent = "权限策略已应用，并会在下次启动时继续使用。";
    $("#policyConfigNotice").hidden = false;
    const diagnostics = await readAPI("/management/v1/diagnostics", { method: "GET" });
    state.diagnostics = diagnostics;
    renderConfig(diagnostics);
    toast("权限策略已应用");
  } catch (error) {
    showViewError(error.message);
    toast(error.message, true);
  }
}

function renderAgentConfig(snapshot) {
  state.agentLookahead = { ...(snapshot.lookahead || {}) };
  $("#agentLookaheadEnabled").checked = !state.agentLookahead.disabled;
  const model = snapshot.model || {};
  const resilience = model.resilience || {};
  const memory = snapshot.memory || {};
  const embedding = memory.semantic_embedding || {};
  $("#agentProvider").value = model.provider || "openai-compatible";
  $("#agentAuthentication").value = model.authentication || "bearer-env";
  $("#agentBaseURL").value = model.base_url || "";
  $("#agentModel").value = model.model || "";
  $("#agentResponseFormat").value = model.response_format || "json_schema";
  $("#agentThinkingMode").value = model.thinking_mode || "";
  setNumberField("agentTemperature", model.temperature);
  setNumberField("agentMaxContextCharacters", model.max_context_characters);
  setNumberField("agentMaxOutputTokens", model.max_output_tokens);
  setNumberField("agentMaxAttempts", resilience.max_attempts);
  setNumberField("agentAttemptTimeoutMillis", resilience.attempt_timeout_millis);
  setNumberField("agentTotalTimeoutMillis", resilience.total_timeout_millis);
  setNumberField("agentInitialBackoffMillis", resilience.initial_backoff_millis);
  setNumberField("agentMaxBackoffMillis", resilience.max_backoff_millis);
  setNumberField("agentFailureThreshold", resilience.failure_threshold);
  setNumberField("agentOpenDurationMillis", resilience.open_duration_millis);
  setNumberField("memoryMaxActiveRecords", memory.max_active_records_per_namespace);
  setNumberField("memoryMaxHistory", memory.max_history_per_namespace);
  $("#agentAPIKey").value = "";
  $("#clearAgentAPIKey").checked = false;
  syncAgentCredentialInputs();
  $("#embeddingEnabled").checked = Boolean(embedding.enabled);
  $("#embeddingProvider").value = embedding.provider || "openai-compatible";
  $("#embeddingAuthentication").value = embedding.authentication || "bearer-env";
  $("#embeddingBaseURL").value = embedding.base_url || "";
  $("#embeddingModel").value = embedding.model || "";
  $("#embeddingDomains").value = (embedding.allowed_domains || []).join(", ");
  setNumberField("embeddingMinLocalMatches", embedding.min_local_matches);
  setNumberField("embeddingMaxResults", embedding.max_semantic_results);
  setNumberField("embeddingMaxInputCharacters", embedding.max_input_characters);
  setNumberField("embeddingTimeoutMillis", embedding.timeout_millis);
  $("#embeddingAPIKey").value = "";
  $("#clearEmbeddingAPIKey").checked = false;
  syncEmbeddingInputs();
  syncEmbeddingCredentialInputs();
  $("#agentConfigNotice").hidden = true;
}

function setNumberField(id, value) {
  $("#" + id).value = value === undefined || value === null || value === 0 ? "" : String(value);
}

function numberField(id, integer = false) {
  const value = $("#" + id).value.trim();
  if (value === "") return 0;
  return integer ? Number.parseInt(value, 10) : Number.parseFloat(value);
}

function syncAgentCredentialInputs(event) {
  const clear = $("#clearAgentAPIKey");
  const input = $("#agentAPIKey");
  if (event?.target === clear && clear.checked) input.value = "";
  if (input.value) clear.checked = false;
  input.disabled = clear.checked;
}

function syncEmbeddingInputs() {
  const enabled = $("#embeddingEnabled").checked;
  if (enabled && !$("#embeddingDomains").value.trim()) {
    $("#embeddingDomains").value = "common-semantic, actor-episodic, actor-semantic";
  }
  $$(".field-grid.embedding-fields input, .field-grid.embedding-fields select").forEach((element) => {
    element.disabled = !enabled;
  });
  $("#embeddingEnabled").disabled = false;
}

function syncEmbeddingCredentialInputs(event) {
  const clear = $("#clearEmbeddingAPIKey");
  const input = $("#embeddingAPIKey");
  if (event?.target === clear && clear.checked) input.value = "";
  if (input.value) clear.checked = false;
  input.disabled = clear.checked;
}

async function saveAgentConfig(event) {
  event.preventDefault();
  const apiKey = $("#agentAPIKey").value;
  const embeddingEnabled = $("#embeddingEnabled").checked;
  const embeddingAPIKey = $("#embeddingAPIKey").value;
  const body = {
    lookahead: { ...state.agentLookahead, disabled: !$("#agentLookaheadEnabled").checked },
    model: {
      provider: $("#agentProvider").value,
      base_url: $("#agentBaseURL").value.trim(),
      model: $("#agentModel").value.trim(),
      response_format: $("#agentResponseFormat").value.trim(),
      thinking_mode: $("#agentThinkingMode").value.trim(),
      authentication: $("#agentAuthentication").value,
      max_context_characters: numberField("agentMaxContextCharacters", true),
      max_output_tokens: numberField("agentMaxOutputTokens", true),
      temperature: numberField("agentTemperature"),
      resilience: {
        max_attempts: numberField("agentMaxAttempts", true),
        attempt_timeout_millis: numberField("agentAttemptTimeoutMillis", true),
        total_timeout_millis: numberField("agentTotalTimeoutMillis", true),
        initial_backoff_millis: numberField("agentInitialBackoffMillis", true),
        max_backoff_millis: numberField("agentMaxBackoffMillis", true),
        failure_threshold: numberField("agentFailureThreshold", true),
        open_duration_millis: numberField("agentOpenDurationMillis", true),
      },
    },
    memory: {
      max_active_records_per_namespace: numberField("memoryMaxActiveRecords", true),
      max_history_per_namespace: numberField("memoryMaxHistory", true),
      semantic_embedding: embeddingEnabled ? {
      enabled: true,
      provider: $("#embeddingProvider").value,
      base_url: $("#embeddingBaseURL").value.trim(),
      model: $("#embeddingModel").value.trim(),
      authentication: $("#embeddingAuthentication").value,
      allowed_domains: splitValues($("#embeddingDomains").value),
      max_input_characters: numberField("embeddingMaxInputCharacters", true),
      min_local_matches: numberField("embeddingMinLocalMatches", true),
      max_semantic_results: numberField("embeddingMaxResults", true),
      timeout_millis: numberField("embeddingTimeoutMillis", true),
      } : {},
    },
  };
  if (apiKey) body.api_key = apiKey;
  if ($("#clearAgentAPIKey").checked) body.clear_api_key = true;
  if (embeddingAPIKey) body.embedding_api_key = embeddingAPIKey;
  if ($("#clearEmbeddingAPIKey").checked) body.clear_embedding_api_key = true;
  try {
    const result = await api("/management/v1/agent/config", { method: "PUT", body });
    renderAgentConfig(result);
    $("#agentConfigNotice").textContent = "已保存。请重启 Rin 使模型配置生效。";
    $("#agentConfigNotice").hidden = false;
    toast(result.requires_restart ? "模型配置已保存，等待重启生效" : "模型配置已保存");
  } catch (error) {
    showViewError(error.message);
    toast(error.message, true);
  }
}

async function loadOverview() {
  const request = beginRequest("overview");
  const [health, info, runtime] = await Promise.all([
    readAPI("/control/v2/health", { method: "GET" }),
    readAPI("/control/v2/info", { method: "GET" }),
    readAPI("/management/v1/runtime", { method: "GET" }),
  ]);
  if (!isCurrentRequest(request)) return;
  state.worlds = runtime.worlds || [];
  state.actors = runtime.actors || [];
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
  const request = beginRequest("actors");
  const runtime = await readAPI("/management/v1/runtime", { method: "GET" });
  if (!isCurrentRequest(request)) return;
  state.worlds = runtime.worlds || [];
  state.actors = runtime.actors || [];
  $("#actorTable").innerHTML = actorTable(state.actors);
  bindActorRows();
  setService(true);
}

async function loadMemories() {
  const request = beginRequest("memories");
  const result = await readAPI("/management/v1/memories/list", {
    body: { scope: state.memoryScope, search: $("#memorySearch").value.trim(), limit: 200 },
  });
  if (!isCurrentRequest(request)) return;
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
  $$('[data-forget-memory]').forEach((button) => onAsync(button, "click", () => forgetMemory(button.dataset.forgetMemory)));
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
  const request = beginRequest("persona");
  const snapshot = await readAPI("/management/v1/personas", { method: "GET" });
  if (!isCurrentRequest(request)) return;
  state.persona = snapshot;
  const defaultBinding = (state.persona.bindings || []).find((binding) => !binding.actor_id && !binding.controller_id);
  const profile = (state.persona.profiles || []).find((item) => item.persona_id === defaultBinding?.persona_id && item.version === defaultBinding?.version) || state.persona.profiles?.[0];
  if (!profile) throw new Error("没有可编辑的默认人格");
  $("#personaRevision").textContent = `revision ${state.persona.revision}`;
  $("#personaIdentity").value = profile.identity || "";
  $("#personaVoice").value = profile.voice || "";
  $("#personaTraits").value = (profile.traits || []).join("\n");
  $("#personaValues").value = (profile.values || []).join("\n");
  $("#personaRules").value = (profile.presentation_rules || []).join("\n");
  $("#personaInitiativeEnabled").checked = Boolean(profile.initiative_policy?.enabled);
  setNumberField("personaInitiativeCooldown", profile.initiative_policy?.cooldown_millis);
  setNumberField("personaInitiativeMaxActions", profile.initiative_policy?.max_consecutive_actions);
  $("#personaInitiativeTriggers").value = (profile.initiative_policy?.triggers || []).join(", ");
  $("#personaPreemptTriggers").value = (profile.initiative_policy?.preempt_triggers || []).join(", ");
  $("#personaBoundaries").value = (profile.boundaries || [])
    .map((item) => [item.boundary_id, item.rule, item.response].join(" :: ")).join("\n");
  $("#personaRelationships").value = (profile.relationship_stances || [])
    .map((item) => [item.role, item.stance].join(" :: ")).join("\n");
  $("#personaBindings").value = (state.persona.bindings || [])
    .map((item) => [item.actor_id || "*", item.controller_id || "*", item.persona_id, item.version].join(" :: "))
    .join("\n");
  state.persona.defaultProfile = profile;
}

async function savePersona(event) {
  event.preventDefault();
  const profile = state.persona.defaultProfile;
  const edited = { ...profile,
    identity: $("#personaIdentity").value.trim(), voice: $("#personaVoice").value.trim(),
    traits: splitLines($("#personaTraits").value), values: splitLines($("#personaValues").value),
    presentation_rules: splitLines($("#personaRules").value),
    initiative_policy: {
      enabled: $("#personaInitiativeEnabled").checked,
      cooldown_millis: numberField("personaInitiativeCooldown", true),
      max_consecutive_actions: numberField("personaInitiativeMaxActions", true),
      triggers: splitValues($("#personaInitiativeTriggers").value),
      preempt_triggers: splitValues($("#personaPreemptTriggers").value),
    },
    boundaries: parseStructuredLines("personaBoundaries", 3, ([boundary_id, rule, response]) => ({
      boundary_id, rule, response,
    })),
    relationship_stances: parseStructuredLines("personaRelationships", 2, ([role, stance]) => ({ role, stance })),
  };
  const bindings = parseStructuredLines("personaBindings", 4, ([actor_id, controller_id, persona_id, version]) => ({
    actor_id: actor_id === "*" ? "" : actor_id,
    controller_id: controller_id === "*" ? "" : controller_id,
    persona_id,
    version,
  }));
  if (!bindings.some((binding) => !binding.actor_id && !binding.controller_id)) {
    throw new Error("人格绑定必须保留一条 * :: * 默认绑定");
  }
  const profiles = state.persona.profiles.map((item) =>
    item.persona_id === profile.persona_id && item.version === profile.version ? edited : item);
  const payload = { revision: state.persona.revision, profiles, bindings };
  state.persona = await api("/management/v1/personas", { method: "PUT", body: payload });
  toast("默认人格已更新");
  await loadPersona();
}

async function loadSkills() {
  const request = beginRequest("skills");
  const result = await readAPI("/management/v1/skills/list", { body: { limit: 128 } });
  if (!isCurrentRequest(request)) return;
  state.skills = result.skills || [];
  renderSkills();
  if (state.selectedSkill && !state.skills.some((skill) =>
    skill.skill_id === state.selectedSkill.skillId && skill.version === state.selectedSkill.version)) {
    clearSkillDetail();
  }
  setService(true);
}

function renderSkills() {
  const query = $("#skillSearch").value.trim().toLowerCase();
  const skills = state.skills.filter((skill) => !query || [
    skill.skill_id, skill.version, skill.summary, skill.source, ...(skill.adapters || []),
  ].some((value) => String(value || "").toLowerCase().includes(query)));
  $("#skillTable").innerHTML = skills.length ? `<table><thead><tr><th>Skill</th><th>说明</th><th>来源</th><th>Adapter</th><th></th></tr></thead><tbody>${skills.map((skill) => `<tr><td><code>${escapeHTML(skill.skill_id)}@${escapeHTML(skill.version)}</code></td><td>${escapeHTML(skill.summary || "")}</td><td>${escapeHTML(skill.source || "")}</td><td>${escapeHTML((skill.adapters || []).join(", ") || "通用")}</td><td><button class="table-action" data-skill-id="${escapeHTML(skill.skill_id)}" data-skill-version="${escapeHTML(skill.version)}">查看</button></td></tr>`).join("")}</tbody></table>` : '<div class="detail-surface empty-state">没有匹配的 Skill。</div>';
  $$('[data-skill-id]').forEach((button) => onAsync(button, "click", () => showSkill(button.dataset.skillId, button.dataset.skillVersion)));
}

async function showSkill(skillId, version) {
  const result = await readAPI("/management/v1/skills/get", { body: { skill_id: skillId, version } });
  const skill = result.skill;
  state.selectedSkill = { skillId: skill.skill_id, version: skill.version };
  const editable = skill.source === "learned";
  $("#skillDetail").classList.remove("empty-state");
  const controls = editable ? '<div class="toolbar"><button id="editSkillButton" class="button secondary">编辑</button><button id="removeSkillButton" class="button danger">删除</button></div>' : '<span class="state">只读</span>';
  $("#skillDetail").innerHTML = `<div class="section-heading compact"><div><h2>${escapeHTML(skill.skill_id)} <span class="revision">${escapeHTML(skill.version)}</span></h2><p>${escapeHTML(skill.summary)}</p></div>${controls}</div><div class="skill-meta"><span>来源 ${escapeHTML(skill.source)}</span><span>Adapter ${escapeHTML((skill.adapters || []).join(", ") || "通用")}</span><span>Capability ${escapeHTML((skill.capabilities || []).join(", ") || "无")}</span></div><pre class="skill-instructions">${escapeHTML(skill.instructions)}</pre>`;
  if (editable) $("#editSkillButton").addEventListener("click", () => openSkillDialog(skill));
  if (editable) onAsync($("#removeSkillButton"), "click", () => removeSkill(skill));
}

function openSkillDialog(skill = null) {
  $("#skillDialogTitle").textContent = skill ? "编辑技能" : "新增技能";
  $("#skillId").value = skill?.skill_id || "";
  $("#skillVersion").value = skill?.version || "v1";
  $("#skillDescription").value = skill?.summary || "";
  $("#skillAdapters").value = (skill?.adapters || []).join(", ");
  $("#skillTriggers").value = (skill?.triggers || []).join(", ");
  $("#skillCapabilities").value = (skill?.capabilities || []).join(", ");
  $("#skillInstructions").value = skill?.instructions || "";
  $("#skillId").readOnly = Boolean(skill);
  $("#skillVersion").readOnly = Boolean(skill);
  $("#skillDialog").showModal();
}

async function saveSkill(event) {
  event.preventDefault();
  const skillId = $("#skillId").value.trim();
  const version = $("#skillVersion").value.trim();
  await api("/management/v1/skills/save", { body: {
    skill_id: skillId, version, description: $("#skillDescription").value.trim(),
    instructions: $("#skillInstructions").value.trim(),
    adapters: splitValues($("#skillAdapters").value),
    triggers: splitValues($("#skillTriggers").value),
    capabilities: splitValues($("#skillCapabilities").value),
  }});
  $("#skillDialog").close();
  toast("Skill 已保存");
  await loadSkills();
  await showSkill(skillId, version);
}

async function reloadSkills() {
  await api("/management/v1/skills/reload", { body: {} });
  toast("Skill 目录已重新加载");
  await loadSkills();
}

async function importSkillFile(event) {
  const input = event.target;
  const file = input.files?.[0];
  if (!file) return;
  try {
    if (file.size > 64 * 1024) throw new Error("SKILL.md 不能超过 64 KiB");
    const result = await api("/management/v1/skills/import", { body: { document: await file.text() } });
    toast("Skill 已导入");
    await loadSkills();
    await showSkill(result.skill.skill_id, result.skill.version);
  } finally {
    input.value = "";
  }
}

async function removeSkill(skill) {
  if (!confirm(`删除 learned Skill ${skill.skill_id}@${skill.version}？`)) return;
  await api("/management/v1/skills/remove", { body: {
    skill_id: skill.skill_id, version: skill.version,
  }});
  clearSkillDetail();
  toast("Skill 已删除");
  await loadSkills();
}

function clearSkillDetail() {
  state.selectedSkill = null;
  $("#skillDetail").classList.add("empty-state");
  $("#skillDetail").textContent = "选择一个 Skill 查看完整说明。";
}

function renderDiagnostics(diagnostics) {
  const connections = diagnostics.connections || [];
  $("#diagnosticList").innerHTML = connections.length ? connections.map((item) => {
    const endpoint = item.endpoint ? `<code>${escapeHTML(item.endpoint)}</code>` : "";
    const metrics = [
      item.latency_millis ? `${item.latency_millis} ms` : "",
      item.worlds ? `${item.worlds} 个世界` : "",
      item.actors ? `${item.actors} 个角色` : "",
    ].filter(Boolean).join(" · ");
    return `<div class="connection-row"><div><strong>${escapeHTML(item.kind)} · ${escapeHTML(item.id)}</strong><small>${escapeHTML(item.detail || "")}</small></div><div>${endpoint}${metrics ? `<small class="diagnostic-metrics">${escapeHTML(metrics)}</small>` : ""}</div><span class="state ${diagnosticStateClass(item.status)}">${escapeHTML(diagnosticStateLabel(item.status))}</span></div>`;
  }).join("") : '<div class="detail-surface empty-state">暂时没有可诊断的连接。</div>';
  const mcp = diagnostics.mcp || {};
  $("#mcpCommandList").innerHTML = mcpStatusSummary(mcp) + (mcp.commands || []).map((item) => `<div class="command-row"><div><strong>${escapeHTML(item.label)}</strong><small>${escapeHTML(item.id)}</small></div><code>${escapeHTML(item.command)}</code><button class="button secondary copy-command" data-copy="${escapeHTML(item.command)}">复制</button></div>`).join("");
  $$(".copy-command").forEach((button) => onAsync(button, "click", () => copyText(button.dataset.copy)));
}

function renderConfig(diagnostics) {
  const model = diagnostics.model || {};
  const memory = diagnostics.memory || {};
  const policy = diagnostics.policy || {};
  const permissions = diagnostics.permissions || {};
  const agent = diagnostics.internal_agent || {};
  $("#configSummary").innerHTML = [
    configPanel("模型", [
      ["状态", model.enabled ? "已启用" : "未启用"], ["供应商", model.provider || "-"],
      ["端点", model.endpoint || "-"], ["模型", model.model || "-"],
      ["凭据", model.credential_configured ? "已配置（正文隐藏）" : "未配置"],
    ]),
    configPanel("记忆", [
      ["存储", memory.backend || "-"], ["语义检索", memory.semantic_embedding_enabled ? "已启用" : "本地检索"],
      ["Embedding", memory.semantic_model || "-"], ["凭据", memory.semantic_credential_configured ? "已配置（正文隐藏）" : "未配置"],
    ]),
    configPanel("Policy", [
      ["Profile", policy.profile || "-"], ["Revision", policy.revision || "-"],
      ["规则", policy.rule_count ?? 0], ["预算", policy.budget_count ?? 0],
      ["确认权限", (policy.confirmation_scopes || []).join(", ") || "-"],
    ]),
    configPanel("权限", [
      ["Principal", permissions.principal_id || "-"], ["Control scopes", (permissions.control_scopes || []).join(", ") || "-"],
      ["Console scopes", (permissions.console_scopes || []).join(", ") || "-"],
      ["内部 Agent", agent.status === "ok" ? "运行中" : "未启用"],
    ]),
  ].join("");
}

function configPanel(title, rows) {
  return `<section class="config-panel"><h3>${escapeHTML(title)}</h3><dl>${rows.map(([label, value]) => `<dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd>`).join("")}</dl></section>`;
}

function mcpStatusSummary(mcp) {
  const ready = mcp.installed && mcp.binary_present && mcp.binary_current && mcp.config_valid;
  const stateName = mcp.error ? "error" : (ready ? "ok" : "warning");
  const detail = mcp.error || (mcp.installed ? `已安装 · ${ready ? "配置和版本有效" : "需要检查配置或版本"}` : "尚未安装");
  const agents = (mcp.agents || []).map((agent) => `${agent.name}: ${agent.registered ? "已注册" : agent.available ? "可安装" : "未检测到"}`).join(" · ");
  return `<div class="command-summary"><div><strong>MCP 当前状态</strong><small>${escapeHTML(detail)}${agents ? ` · ${escapeHTML(agents)}` : ""}</small></div><span class="state ${diagnosticStateClass(stateName)}">${escapeHTML(diagnosticStateLabel(stateName))}</span></div>`;
}

function diagnosticStateClass(status) { return status === "ok" ? "good" : status === "error" || status === "offline" ? "bad" : "warn"; }
function diagnosticStateLabel(status) { return { ok: "正常", warning: "需检查", offline: "离线", disabled: "未启用", error: "异常" }[status] || status || "未知"; }

async function loadOperations() {
  const request = beginRequest("operations");
  const result = await readAPI("/management/v1/operations/list", { body: {
    status: $("#operationStatus").value || undefined,
    host_id: $("#operationHost").value.trim() || undefined,
    world_id: $("#operationWorld").value.trim() || undefined,
    actor_id: $("#operationActor").value.trim() || undefined,
    task_id: $("#operationTask").value.trim() || undefined,
    limit: 200,
  }});
  if (!isCurrentRequest(request)) return;
  state.operations = result.operations || [];
  $("#operationTable").innerHTML = state.operations.length ? `<table><thead><tr><th>Operation</th><th>Capability</th><th>角色</th><th>状态</th><th>确认</th><th>更新</th></tr></thead><tbody>${state.operations.map((operation) => `<tr tabindex="0" data-operation-id="${escapeHTML(operation.operation_id)}"><td><code>${escapeHTML(operation.operation_id)}</code></td><td>${escapeHTML(operation.action_request?.capability?.id || operation.kind || "-")}</td><td>${escapeHTML(operation.actor_id)}</td><td><span class="state ${operationStateClass(operation)}">${escapeHTML(operation.status)}</span></td><td>${operation.execution_confirmed ? "是" : "否"}</td><td>${formatMillis(operation.updated_at_unix_millis)}</td></tr>`).join("")}</tbody></table>` : '<div class="detail-surface empty-state">没有匹配的 Operation。</div>';
  $$('[data-operation-id]').forEach((row) => {
    const select = () => showOperation(row.dataset.operationId);
    row.addEventListener("click", select);
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); }
    });
  });
  await loadOutcomeBacklog(request);
  setService(true);
}

function showOperation(operationId) {
  const operation = state.operations.find((item) => item.operation_id === operationId);
  if (!operation) return;
  const capability = operation.action_request?.capability;
  const outcome = operation.outcome || {};
  const policy = operation.policy_decision || {};
  const controls = [
    operation.status === "awaiting-confirmation" ? '<button class="button primary" data-operation-action="confirm">确认</button>' : "",
    !operation.terminal ? '<button class="button danger" data-operation-action="cancel">取消</button>' : "",
  ].filter(Boolean).join("");
  $("#operationDetail").classList.remove("empty-state");
  $("#operationDetail").innerHTML = `<div class="section-heading compact"><div><h2>${escapeHTML(capability?.id || operation.kind)} <span class="revision">${escapeHTML(capability?.version || "")}</span></h2><p><code>${escapeHTML(operation.operation_id)}</code></p></div><div class="toolbar">${controls}</div></div><div class="detail-grid"><dl><dt>状态</dt><dd>${escapeHTML(operation.status)}</dd><dt>执行确认</dt><dd>${operation.execution_confirmed ? "是" : "否"}</dd><dt>投递次数</dt><dd>${operation.delivery_attempts || 0}</dd><dt>角色</dt><dd>${escapeHTML(operation.actor_id)}</dd></dl><dl><dt>策略</dt><dd>${escapeHTML(policy.result || policy.disposition || "-")}</dd><dt>Outcome</dt><dd>${escapeHTML(outcome.status || "-")}</dd><dt>结果</dt><dd>${escapeHTML(outcome.summary || operation.rejection_message || "-")}</dd><dt>更新时间</dt><dd>${formatMillis(operation.updated_at_unix_millis)}</dd></dl></div>${operation.output ? `<pre class="skill-instructions">${escapeHTML(JSON.stringify(operation.output, null, 2))}</pre>` : ""}`;
  $$('[data-operation-action]').forEach((button) => button.addEventListener("click", async () => {
    try {
      await api("/management/v1/operations/control", { body: {
        operation_id: operationId, action: button.dataset.operationAction,
      }});
      toast(button.dataset.operationAction === "cancel" ? "已请求取消" : "已确认执行");
      await loadOperations();
      showOperation(operationId);
    } catch (error) {
      showViewError(error.message);
      toast(error.message, true);
    }
  }));
}

async function loadTasks() {
  const request = beginRequest("tasks");
  try {
    const result = await readAPI("/management/v1/tasks/list", { body: { limit: 100, archived: $("#taskArchive").checked } });
    if (!isCurrentRequest(request)) return;
    const tasks = result.tasks || [];
    $("#taskTable").innerHTML = tasks.length ? `<table><thead><tr><th>目标</th><th>来源</th><th>状态</th><th>阶段</th><th>动作 / 模型</th><th>更新</th></tr></thead><tbody>${tasks.map((task) => `<tr data-task-row="${escapeHTML(task.task_id)}"><td><strong>${escapeHTML(task.goal)}</strong><br><code>${escapeHTML(task.task_id)}</code>${tagList(task.tags)}</td><td>${task.task_control_available ? "内部 Agent" : `外部计划 · ${escapeHTML(task.controller_source || "-")}`}</td><td><span class="state ${task.status === "completed" ? "good" : task.status === "failed" ? "bad" : "warn"}">${escapeHTML(task.status)}</span></td><td>${escapeHTML(task.current_plan_step_id || task.plan_id || "-")}</td><td>${task.action_count} / ${task.model_calls}</td><td>${new Date(task.updated_at_unix_millis).toLocaleString()}</td></tr>`).join("")}</tbody></table>` : '<div class="detail-surface empty-state">当前没有内部任务或外部 MCP 计划。</div>';
    $$('[data-task-row]').forEach((row) => onAsync(row, "click", () => showTask(row.dataset.taskRow)));
  } catch (error) {
    if (!isCurrentRequest(request)) return;
    if (error.message.includes("not enabled")) {
      $("#taskTable").innerHTML = '<div class="detail-surface empty-state">内部 Agent Runtime 与计划存储均未启用。</div>';
      return;
    }
    throw error;
  }
}

async function openTaskDialog() {
  const selected = state.actors.find((actor) => actor.actor_id === state.selectedActorId);
  if (selected) {
    $("#taskHostId").value = selected.host_id || "";
    $("#taskWorldId").value = selected.world_id || "";
    $("#taskActorId").value = selected.actor_id || "";
  }
  const result = await readAPI("/management/v1/skills/list", { body: {
    adapter: selected?.adapter_id || undefined, limit: 128,
  } });
  const taskSkills = result.skills || [];
  const triggers = new Map();
  taskSkills.forEach((skill) => (skill.triggers || []).forEach((trigger) => {
    const labels = triggers.get(trigger) || [];
    labels.push(skill.summary || skill.skill_id);
    triggers.set(trigger, labels);
  }));
  $("#taskSkillTrigger").innerHTML = '<option value="">不指定</option>' +
    [...triggers.entries()].sort(([left], [right]) => left.localeCompare(right))
      .map(([trigger, labels]) => `<option value="${escapeHTML(trigger)}">${escapeHTML(trigger)} · ${escapeHTML(labels[0])}</option>`)
      .join("");
  $("#taskDialog").showModal();
}

async function startTask(event) {
  event.preventDefault();
  const selectedTrigger = $("#taskSkillTrigger").value;
  const tags = [...new Set([selectedTrigger, ...splitValues($("#taskTags").value)].filter(Boolean))];
  const task = await api("/management/v1/tasks/start", { body: {
    host_id: $("#taskHostId").value.trim(),
    world_id: $("#taskWorldId").value.trim(),
    actor_id: $("#taskActorId").value.trim(),
    goal: $("#taskGoal").value.trim(),
    planning_mode: $("#taskPlanningMode").value,
    completion: {
      mode: $("#taskCompletionMode").value,
      conditions: $("#taskCompletionMode").value === "host-evidence"
        ? JSON.parse($("#taskCompletionConditions").value) : undefined,
      operation_requirements: $("#taskCompletionMode").value === "host-evidence"
        ? JSON.parse($("#taskCompletionRequirements").value || "[]") : undefined,
    },
    tags,
  } });
  $("#taskDialog").close();
  $("#taskForm").reset();
  $("#taskCompletionConditionsLabel").hidden = true;
  $("#taskCompletionRequirementsLabel").hidden = true;
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

async function showTask(taskId, append = false) {
  const current = state.selectedTask?.taskId === taskId ? state.selectedTask : null;
  const afterCursor = append ? current?.nextCursor : "";
  if (append && (!current?.more || !afterCursor)) return;
  const request = beginRequest("task-detail");
  const result = await readAPI("/management/v1/tasks/get", { body: {
    task_id: taskId, after_cursor: afterCursor || undefined, limit: 100,
  } });
  if (!isCurrentRequest(request)) return;
  const timeline = result.timeline || {};
  const events = append
    ? mergeTimelineEvents(current.events, timeline.events || [])
    : (timeline.events || []);
  state.selectedTask = {
    taskId, task: result.task, plan: result.plan || null, events,
    nextCursor: timeline.next_cursor || afterCursor || "",
    more: Boolean(timeline.more), truncated: Boolean(timeline.truncated),
  };
  const status = result.task.status;
  const controls = result.task.task_control_available ? [
    status === "paused" && result.task.completion_requested && result.task.completion?.mode === "human-confirmation"
      ? '<button class="button primary" data-task-action="confirm-completion">确认任务完成</button>' : "",
    status === "active" ? '<button class="button secondary" data-task-action="run">继续</button>' : "",
    status === "outcome-unknown" ? '<button class="button secondary" data-task-action="run">核对 Host 结果</button>' : "",
    status === "paused" ? '<button class="button secondary" data-task-action="resume">恢复</button>' : "",
    ["active", "paused", "waiting-confirmation", "outcome-unknown"].includes(status) ? '<button class="button danger" data-task-action="cancel">取消</button>' : "",
  ].filter(Boolean).join("") : "";
  $("#taskDetail").classList.remove("empty-state");
  const truncated = state.selectedTask.truncated
    ? '<div class="form-notice">更早的时间线事件已按保留策略清理。</div>' : "";
  const loadMore = state.selectedTask.more
    ? '<div class="form-actions"><button id="loadMoreTaskEvents" class="button secondary">加载更多事件</button></div>' : "";
  const pause = result.task.pause_code
    ? `<div class="form-notice task-pause"><strong>暂停原因</strong><code>${escapeHTML(result.task.pause_code)}</code></div>` : "";
  const source = result.task.task_control_available ? "内部 Agent" : `外部计划 · ${escapeHTML(result.task.controller_source || "-")}`;
  $("#taskDetail").innerHTML = `<div class="section-heading"><div><h2>${escapeHTML(result.task.goal)}</h2><p>${escapeHTML(result.task.task_id)} · ${escapeHTML(status)} · ${source}</p>${tagList(result.task.tags)}</div><div class="toolbar">${controls}</div></div>${pause}${taskLookaheadPanel(result.task.lookahead)}${taskPlan(result.plan)}${truncated}${taskTimeline(events)}${loadMore}`;
  if (state.selectedTask.more) {
    onAsync($("#loadMoreTaskEvents"), "click", () => showTask(taskId, true));
  }
  $$('[data-task-action]').forEach((button) => onAsync(button, "click", async () => {
    await api("/management/v1/tasks/control", { body: { task_id: taskId, action: button.dataset.taskAction, expected_revision: button.dataset.taskAction === "confirm-completion" ? result.task.revision : undefined } });
    toast("任务状态已更新");
    await Promise.all([loadTasks(), showTask(taskId)]);
  }));
}

function taskLookaheadPanel(lookahead) {
  if (!lookahead) return "";
  const labels = { preparing: "正在准备下一步", running: "正在提前规划", ready: "下一步候选已准备，等待当前动作结果", adopted: "已采用提前准备的下一步", discarded: "本轮候选未采用" };
  const reserved = lookahead.reserved_tokens ? ` · 预算预留 ${escapeHTML(lookahead.reserved_tokens)} token` : "";
  return `<div class="form-notice"><strong>${escapeHTML(labels[lookahead.status] || lookahead.status)}</strong><p>提前规划 ${escapeHTML(lookahead.calls)} 次 · 采用 ${escapeHTML(lookahead.adopted)} 次 · 未采用 ${escapeHTML(lookahead.discarded)} 次${reserved}</p></div>`;
}

function taskPlan(plan) {
  if (!plan) return '<section class="task-plan"><div class="subheading"><h3>执行计划</h3><span>等待首次规划</span></div></section>';
  const steps = (plan.steps || []).map((step) => {
    const conditions = (step.success_conditions || []).map((condition) => {
      const selector = condition.capability
        ? `${condition.capability.id}@${condition.capability.version}`
        : condition.fact_id
          ? `${condition.fact_id} = ${condition.fact_value_json}`
          : "未绑定";
      return `${escapeHTML(condition.summary)} [${escapeHTML(selector)}]`;
    }).join("；");
    const evidence = (step.evidence_refs || []).map((item) => escapeHTML(item.digest || item.condition_id)).join("；");
    const capabilities = (step.capability_hints || []).map((capability) => `${escapeHTML(capability.id)}@${escapeHTML(capability.version)}`).join("、");
    return `<li class="plan-step ${escapeHTML(step.status)}"><span class="plan-marker"></span><div><div class="plan-step-heading"><strong>${escapeHTML(step.title)}</strong><span class="state ${step.status === "completed" ? "good" : step.status === "failed" || step.status === "blocked" ? "bad" : "warn"}">${escapeHTML(step.status)}</span></div><p>${escapeHTML(step.objective)}</p><div class="plan-meta"><span>尝试 ${step.attempt || 0}/${step.max_attempts || 0}</span>${capabilities ? `<span>${capabilities}</span>` : ""}${conditions ? `<span>成功条件：${conditions}</span>` : ""}${evidence ? `<span>证据：${evidence}</span>` : ""}${step.blocked_reason ? `<span>阻塞：${escapeHTML(step.blocked_reason)}</span>` : ""}</div></div></li>`;
  }).join("");
  return `<section class="task-plan"><div class="subheading"><h3>执行计划</h3><span>${escapeHTML(plan.phase || "未命名阶段")} · ${escapeHTML(plan.status)} · 修订 ${plan.revision || 0} · 重规划 ${plan.replan_count || 0}/${plan.max_replans || 0}</span></div><ol class="plan-steps">${steps || '<li class="empty-state">计划尚未生成步骤。</li>'}</ol></section>`;
}

function mergeTimelineEvents(existing = [], incoming = []) {
  const merged = new Map();
  [...existing, ...incoming].forEach((event) => merged.set(event.cursor || event.event_id, event));
  return [...merged.values()];
}

function tagList(tags) {
  return Array.isArray(tags) && tags.length
    ? `<div class="tags">${tags.map((tag) => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}</div>`
    : "";
}

function worldTable(worlds) {
  if (!worlds.length) return '<div class="detail-surface empty-state">尚无游戏 Host 发布世界。</div>';
  return `<table><thead><tr><th>Host</th><th>世界</th><th>状态</th><th>Epoch</th></tr></thead><tbody>${worlds.map((world) => `<tr><td><code>${escapeHTML(world.host_id || world.host?.host_id || "-")}</code></td><td>${escapeHTML(world.world_id || world.id || "-")}</td><td><span class="state ${world.online === false ? "bad" : "good"}">${world.online === false ? "离线" : "在线"}</span></td><td>${escapeHTML(formatEpoch(world.epoch))}</td></tr>`).join("")}</tbody></table>`;
}

function actorTable(actors) {
  if (!actors.length) return '<div class="detail-surface empty-state">当前世界没有在线或可恢复角色。</div>';
  return `<table><thead><tr><th>角色</th><th>世界</th><th>状态</th><th>急停</th><th>控制源</th></tr></thead><tbody>${actors.map((actor) => `<tr tabindex="0" data-actor-row data-actor-id="${escapeHTML(actor.actor_id)}" class="${actor.actor_id === state.selectedActorId ? "selected" : ""}"><td><strong>${escapeHTML(actor.display_name || actor.name || actor.actor_id)}</strong><br><code>${escapeHTML(actor.actor_id)}</code></td><td>${escapeHTML(actor.world_id)}</td><td><span class="state ${actor.online === false ? "bad" : "good"}">${actor.online === false ? "离线" : "在线"}</span></td><td>${actor.emergency_stopped ? '<span class="state bad">已急停</span>' : "-"}</td><td>${escapeHTML(actor.controller_lease?.source || actor.decision_authority?.source || "-")}</td></tr>`).join("")}</tbody></table>`;
}

function bindActorRows() {
  $$('[data-actor-row]').forEach((row) => {
    const select = () => {
      state.selectedActorId = row.dataset.actorId;
      $$('[data-actor-row]').forEach((candidate) => candidate.classList.toggle("selected", candidate === row));
      showActor(row.dataset.actorId);
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

function showActor(actorId) {
  const actor = state.actors.find((item) => item.actor_id === actorId);
  if (!actor) return;
  const lease = actor.controller_lease;
  const action = actor.emergency_stopped ? "clear-emergency-stop" : "emergency-stop";
  $("#actorDetail").classList.remove("empty-state");
  const leaseActions = lease
    ? '<button class="button secondary" data-actor-action="renew">续期</button><button class="button secondary" data-actor-action="release">释放控制</button>'
    : '<button class="button primary" data-actor-action="acquire">获取控制</button>';
  $("#actorDetail").innerHTML = `<div class="section-heading compact"><div><h2>${escapeHTML(actor.display_name || actor.actor_id)}</h2><p><code>${escapeHTML(actor.actor_id)}</code></p></div><div class="toolbar">${leaseActions}<button class="button ${actor.emergency_stopped ? "secondary" : "danger"}" data-actor-action="${action}">${actor.emergency_stopped ? "解除急停" : "立即急停"}</button></div></div><div class="detail-grid"><dl><dt>Host</dt><dd>${escapeHTML(actor.host_id)}</dd><dt>世界</dt><dd>${escapeHTML(actor.world_id)}</dd><dt>在线</dt><dd>${actor.online ? "是" : "否"}</dd></dl><dl><dt>决策来源</dt><dd>${escapeHTML(actor.decision_authority?.source || "-")}</dd><dt>人格模式</dt><dd>${escapeHTML(actor.decision_authority?.persona_mode || "-")}</dd><dt>Lease</dt><dd>${escapeHTML(lease?.lease_id || "无")}</dd><dt>到期时间</dt><dd>${lease?.expires_at_unix_millis ? escapeHTML(new Date(lease.expires_at_unix_millis).toLocaleString()) : "-"}</dd></dl></div>`;
  $$('[data-actor-action]').forEach((button) => button.addEventListener("click", async () => {
    try {
      await api("/management/v1/actors/control", { body: {
        host_id: actor.host_id, world_id: actor.world_id, actor_id: actor.actor_id,
        action: button.dataset.actorAction, lease_id: lease?.lease_id || undefined,
        lease_ttl_millis: button.dataset.actorAction === "renew" ? 300000 : undefined,
      }});
      toast("角色控制状态已更新");
      await loadActors();
      showActor(actorId);
    } catch (error) {
      showViewError(error.message);
      toast(error.message, true);
    }
  }));
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

function readAPI(path, options = {}) {
  return api(path, { ...options, signal: state.viewController.signal });
}

async function api(path, options = {}) {
  const method = options.method || "POST";
  let response;
  try {
    response = await fetch(path, {
      method,
      signal: options.signal,
      headers: { "Authorization": `Bearer ${state.token}`, ...(options.body !== undefined ? { "Content-Type": "application/json" } : {}) },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });
  } catch (cause) {
    if (cause.name === "AbortError" || options.signal?.aborted) {
      throw new DOMException("Request cancelled", "AbortError");
    }
    const error = new Error("无法连接本地 Rin 服务");
    error.offline = true;
    throw error;
  }
  const text = await response.text();
  if (options.signal?.aborted) throw new DOMException("Request cancelled", "AbortError");
  let payload = {};
  try { payload = text ? JSON.parse(text) : {}; } catch { payload = { error: text }; }
  if (!response.ok) {
    if (response.status === 401) lockConsole();
    const detail = payload.error?.message || payload.error?.code || payload.error;
    const error = new Error(typeof detail === "string" ? detail : `请求失败 (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return payload;
}

function setToken(token) {
  invalidateViewRequests();
  state.token = token;
  sessionStorage.setItem("rin.controlToken", token);
  $("#settingsToken").value = token;
}

function lockConsole() {
  invalidateViewRequests();
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

function showViewError(message) {
  const element = $("#viewError");
  element.textContent = message;
  element.hidden = false;
}

function clearViewError() {
  const element = $("#viewError");
  element.textContent = "";
  element.hidden = true;
}

function summarizeControl(actors) {
  const sources = new Set(actors.map((actor) => actor.controller_lease?.source || actor.decision_authority?.source).filter(Boolean));
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

function formatMillis(value) { return value ? new Date(value).toLocaleString() : "-"; }
function operationStateClass(operation) {
  if (operation.execution_confirmed) return "good";
  if (["failed", "rejected", "stale", "outcome-unknown"].includes(operation.status)) return "bad";
  return operation.terminal ? "warn" : "";
}

function splitValues(value) { return [...new Set(value.split(",").map((item) => item.trim()).filter(Boolean))]; }
function splitLines(value) { return [...new Set(value.split("\n").map((item) => item.trim()).filter(Boolean))]; }
function parseStructuredLines(id, columns, mapRow) {
  return splitLines($("#" + id).value).map((line, index) => {
    const parts = line.split("::").map((item) => item.trim());
    if (parts.length !== columns || parts.some((item) => !item)) {
      throw new Error(`${$("#" + id).previousElementSibling?.textContent || id} 第 ${index + 1} 行格式不正确`);
    }
    return mapRow(parts);
  });
}
function escapeHTML(value) { return String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char])); }
function reportActionError(error) {
  if (error?.name === "AbortError") return;
  const message = error?.message || "操作失败";
  showViewError(message);
  toast(message, true);
}
function runUIAction(action) { return Promise.resolve().then(action).catch(reportActionError); }
function onAsync(element, eventName, action) {
  element.addEventListener(eventName, (...args) => runUIAction(() => action(...args)));
}
function debounce(fn, wait) {
  let timer;
  return () => { clearTimeout(timer); timer = setTimeout(() => runUIAction(fn), wait); };
}
async function copyText(value) { await navigator.clipboard.writeText(value); toast("命令已复制"); }
function toast(message, bad = false) { const element = $("#toast"); element.textContent = message; element.style.background = bad ? "#74322f" : "#202a25"; element.classList.add("show"); setTimeout(() => element.classList.remove("show"), 2600); }

async function loadOutcomeBacklog(request) {
  const health = await readAPI("/management/v1/outcomes/backlog");
  if (!isCurrentRequest(request)) return;
  $("#outcomeBacklogCount").textContent = `（${health.pending || 0}）`;
  const entries = health.entries || [];
  $("#outcomeBacklog").innerHTML = entries.length ? `<table><thead><tr><th>Operation</th><th>订阅者</th><th>失败次数</th><th>最早等待</th><th>状态</th><th>处理</th></tr></thead><tbody>${entries.map((entry, i) => `<tr><td><code>${escapeHTML(entry.operation_id)}</code></td><td>${escapeHTML(entry.subscriber)}</td><td>${entry.attempts || 0}</td><td>${formatMillis(entry.created_at_unix_millis)}</td><td>${entry.configured ? (entry.last_error_code ? "投递失败，等待重试" : "等待投递") : "订阅者未配置"}</td><td><button class="button secondary" data-outcome-retry="${i}" ${entry.configured ? "" : "disabled"}>立即重试</button></td></tr>`).join("")}</tbody></table><p>显示最早的 ${entries.length} 条；确认成功后自动移出积压。</p>` : '<p>当前没有待确认的结果投递。</p>';
  $$("[data-outcome-retry]").forEach((button) => onAsync(button, "click", async () => {
    const entry = entries[Number(button.dataset.outcomeRetry)];
    await api("/management/v1/outcomes/retry", { body: { operation_id: entry.operation_id, subscriber: entry.subscriber } });
    toast("已安排重新投递结果");
    await loadOperations();
  }));
}
