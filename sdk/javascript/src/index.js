export const SDK_VERSION = "0.7.0";
export const PROTOCOL_VERSION = "rin.protocol/v2";
export const DEFAULT_BASE_URL = "http://127.0.0.1:7374";
export const DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024;
export const INLINE_SNAPSHOT_MAX_BYTES = 16 * 1024 * 1024;
export const TRANSFER_CONTROL_FRAME_MAX_BYTES = 32 * 1024;
export const TRANSFER_EVENT_FRAME_MAX_BYTES = 64 * 1024 * 1024 + TRANSFER_CONTROL_FRAME_MAX_BYTES;
export const RIN_FEATURES = Object.freeze({
  memoryArchive: "memory-archive-v1",
  beliefConflicts: "belief-conflicts-v1",
  goalCandidates: "goal-candidates-v1",
  actorActivity: "actor-activity-v1",
  arbitration: "arbitration-v1",
});
export const FEATURE_PRESETS = Object.freeze({
  safeBaseline: Object.freeze([]),
  authoritative: Object.freeze([]),
  full: Object.freeze(Object.values(RIN_FEATURES)),
});
export const HOST_DURABILITY_PROFILES = Object.freeze({
  advisory: "advisory",
  idempotentAction: "idempotent-action",
  transactionalAction: "transactional-action",
});

const MAX_GENERATION_CONTENT_BYTES = 4 * 1024 * 1024;
const PROTOCOL_IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/;
const LOWER_SHA256 = /^[0-9a-f]{64}$/;
const TERMINAL_JOB_STATES = new Set(["succeeded", "failed", "stale", "canceled"]);

export class RinError extends Error {
  constructor(code, message, options = {}) {
    super(safeText(message, 500) || "Rin request failed", options);
    this.name = new.target.name;
    this.code = safeText(code, 96) || "rin_error";
  }
}

export class RinConfigurationError extends RinError {}
export class RinTransportError extends RinError {}
export class RinProtocolError extends RinError {}

export class RinAPIError extends RinError {
  constructor(code, message, { status = 0, field = "", cause } = {}) {
    super(code, message, cause ? { cause } : {});
    this.status = Number(status) || 0;
    this.field = safeText(field, 160);
  }
}

export function createRinId(prefix = "id", randomBytes = secureRandomBytes) {
  if (typeof prefix !== "string" || !PROTOCOL_IDENTIFIER.test(prefix) || prefix.length > 63) {
    throw new RinConfigurationError(
      "invalid_id_prefix",
      "ID prefix must be a protocol identifier no longer than 63 characters",
    );
  }
  if (typeof randomBytes !== "function") {
    throw new RinConfigurationError("invalid_random_source", "randomBytes must be a function");
  }
  const bytes = randomBytes(16);
  if (!(bytes instanceof Uint8Array) || bytes.byteLength !== 16) {
    throw new RinConfigurationError(
      "invalid_random_source",
      "randomBytes must return exactly 16 bytes",
    );
  }
  return `${prefix}.${Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("")}`;
}

export class HostDurability {
  constructor({
    version = 1,
    profile = HOST_DURABILITY_PROFILES.advisory,
    stableIdentity = false,
    durableBeforeNetwork = false,
    durableOutbox = false,
    idempotentApply = false,
    atomicApplyAndOutbox = false,
  } = {}) {
    if (version !== 1 || !Object.values(HOST_DURABILITY_PROFILES).includes(profile)) {
      throw new RinConfigurationError(
        "invalid_host_durability",
        "Host durability has an unsupported version or profile",
      );
    }
    for (const [name, value] of Object.entries({
      stableIdentity,
      durableBeforeNetwork,
      durableOutbox,
      idempotentApply,
      atomicApplyAndOutbox,
    })) {
      if (typeof value !== "boolean") {
        throw new RinConfigurationError(
          "invalid_host_durability",
          `${name} must be boolean`,
        );
      }
    }
    if (profile === HOST_DURABILITY_PROFILES.idempotentAction &&
        !(stableIdentity && durableBeforeNetwork && durableOutbox && idempotentApply)) {
      throw new RinConfigurationError(
        "invalid_host_durability",
        "idempotent-action requires stable durable state, Outbox, and idempotent apply",
      );
    }
    if (profile === HOST_DURABILITY_PROFILES.transactionalAction &&
        !(stableIdentity && durableBeforeNetwork && durableOutbox && atomicApplyAndOutbox)) {
      throw new RinConfigurationError(
        "invalid_host_durability",
        "transactional-action requires stable durable state, Outbox, and atomic settlement",
      );
    }
    this.version = version;
    this.profile = profile;
    this.stableIdentity = stableIdentity;
    this.durableBeforeNetwork = durableBeforeNetwork;
    this.durableOutbox = durableOutbox;
    this.idempotentApply = idempotentApply;
    this.atomicApplyAndOutbox = atomicApplyAndOutbox;
    Object.freeze(this);
  }

  require(requiredDurability) {
    if (!Object.values(HOST_DURABILITY_PROFILES).includes(requiredDurability)) {
      throw new RinConfigurationError(
        "invalid_host_durability_profile",
        "Required host durability profile is unknown",
      );
    }
    const rank = {
      [HOST_DURABILITY_PROFILES.advisory]: 0,
      [HOST_DURABILITY_PROFILES.idempotentAction]: 1,
      [HOST_DURABILITY_PROFILES.transactionalAction]: 2,
    };
    if (rank[this.profile] < rank[requiredDurability]) {
      throw new RinConfigurationError(
        "host_durability_insufficient",
        `Action requires ${requiredDurability}, but host provides ${this.profile}`,
      );
    }
  }

  static advisory(options = {}) {
    return new HostDurability({ ...options, profile: HOST_DURABILITY_PROFILES.advisory });
  }

  static idempotentAction(options = {}) {
    return new HostDurability({
      ...options,
      profile: HOST_DURABILITY_PROFILES.idempotentAction,
      stableIdentity: true,
      durableBeforeNetwork: true,
      durableOutbox: true,
      idempotentApply: true,
    });
  }

  static transactionalAction(options = {}) {
    return new HostDurability({
      ...options,
      profile: HOST_DURABILITY_PROFILES.transactionalAction,
      stableIdentity: true,
      durableBeforeNetwork: true,
      durableOutbox: true,
      atomicApplyAndOutbox: true,
    });
  }
}

export class ProposalAttemptCoordinator {
  constructor(client, store) {
    if (!(client instanceof RinClient)) {
      throw new RinConfigurationError("invalid_workflow", "client must be a RinClient");
    }
    requireMethods(store, [
      "loadProposalAttempt",
      "createProposalAttempt",
      "saveProposalAttempt",
      "settleProposalAttempt",
    ], "Proposal Attempt store");
    this.client = client;
    this.store = store;
  }

  async begin(operationId, request) {
    requireIdentifier("operation_id", operationId);
    const stableRequest = cloneProtocolObject(request);
    requireIdentifier("request_id", stableRequest.request_id);
    requireIdentifier("session_id", stableRequest.session_id);
    const attempt = {
      version: 1,
      operation_id: operationId,
      request: stableRequest,
      job_id: "",
    };
    if (await this.store.createProposalAttempt(attempt) !== true) {
      throw new RinConfigurationError(
        "proposal_attempt_pending",
        "A Proposal Attempt is already pending",
      );
    }
    return cloneProtocolObject(attempt);
  }

  async resume(options = {}) {
    let attempt = validateProposalAttempt(await this.store.loadProposalAttempt());
    let job;
    if (attempt.job_id) {
      try {
        job = await this.client.waitForProposal(attempt.job_id, options);
      } catch (error) {
        if (!(error instanceof RinAPIError) || error.code !== "job_not_found") throw error;
      }
    }
    if (!job) {
      const submission = await this.client.submitProposalJob(attempt.request);
      requireIdentifier("job_id", submission.job_id);
      const expected = attempt;
      const replacement = { ...attempt, job_id: submission.job_id };
      if (await this.store.saveProposalAttempt(expected, replacement) !== true) {
        throw new RinConfigurationError(
          "proposal_attempt_changed",
          "Proposal Attempt changed before its Job ID could be saved",
        );
      }
      attempt = replacement;
      job = await this.client.waitForProposal(attempt.job_id, options);
    }
    if (!isObject(job.proposal) ||
        job.proposal.session_id !== attempt.request.session_id ||
        job.proposal.request_id !== attempt.request.request_id) {
      throw new RinProtocolError(
        "invalid_job",
        "Resolved Proposal does not match the durable Attempt",
      );
    }
    return {
      attempt: cloneProtocolObject(attempt),
      proposal: cloneProtocolObject(job.proposal),
      duplicate: job.duplicate === true,
    };
  }

  async settle(attempt, proposal, report, apply) {
    const stableAttempt = validateProposalAttempt(attempt);
    const stableProposal = cloneProtocolObject(proposal);
    const stableReport = cloneProtocolObject(report);
    if (typeof apply !== "function") {
      throw new RinConfigurationError(
        "invalid_workflow",
        "apply must be an authoritative transaction callback",
      );
    }
    validateWorkflowSettlement(stableAttempt, stableProposal, stableReport);
    await this.store.settleProposalAttempt({
      attempt: stableAttempt,
      proposal: stableProposal,
      report: stableReport,
      apply,
    });
  }
}

export class OutcomeOutbox {
  constructor(client, store) {
    if (!(client instanceof RinClient)) {
      throw new RinConfigurationError("invalid_workflow", "client must be a RinClient");
    }
    requireMethods(store, ["listOutcomeReports", "acknowledgeOutcome"], "Outcome Outbox");
    this.client = client;
    this.store = store;
    this.draining = false;
  }

  async drain() {
    if (this.draining) {
      throw new RinConfigurationError(
        "outbox_busy",
        "Outcome Outbox is already being drained",
      );
    }
    this.draining = true;
    let acknowledged = 0;
    try {
      const listed = await this.store.listOutcomeReports();
      if (!Array.isArray(listed)) {
        throw new RinConfigurationError(
          "invalid_outbox",
          "Outcome Outbox must return an array",
        );
      }
      const entries = listed.slice();
      for (const entry of entries) {
        if (!isObject(entry) || !isObject(entry.report)) {
          throw new RinConfigurationError(
            "invalid_outbox",
            "Outcome Outbox entry must contain an Action Report",
          );
        }
        const report = cloneProtocolObject(entry.report);
        requireOutboxIdentifier("session_id", report.session_id);
        requireOutboxIdentifier("request_id", report.request_id);
        requireOutboxIdentifier("event_id", report.report?.event_id);
        const result = await this.client.reportAction(report);
        if (!validMutationAcknowledgement(result, report.session_id)) {
          throw new RinConfigurationError(
            "invalid_outbox_ack",
            "Rin returned a malformed or wrong-Session Outcome acknowledgement",
          );
        }
        await this.store.acknowledgeOutcome(entry, result);
        acknowledged++;
      }
      return acknowledged;
    } finally {
      this.draining = false;
    }
  }
}

function validMutationAcknowledgement(result, sessionId) {
  return isObject(result) &&
    result.session_id === sessionId &&
    PROTOCOL_IDENTIFIER.test(result.session_id) &&
    Number.isSafeInteger(result.revision) &&
    result.revision > 0 &&
    typeof result.head_hash === "string" &&
    LOWER_SHA256.test(result.head_hash) &&
    typeof result.duplicate === "boolean";
}

export class WorkflowCoordinator {
  constructor(client, store, durability = HostDurability.advisory()) {
    if (!(durability instanceof HostDurability)) {
      throw new RinConfigurationError(
        "invalid_host_durability",
        "durability must be a validated HostDurability value",
      );
    }
    requireMethods(store, [
      "loadProposalAttempt",
      "createProposalAttempt",
      "saveProposalAttempt",
      "listOutcomeReports",
      "acknowledgeOutcome",
    ], "Workflow store");
    if (durability.profile === HOST_DURABILITY_PROFILES.transactionalAction) {
      requireMethods(store, ["settleProposalAttempt"], "transactional Workflow store");
    } else {
      requireMethods(store, ["completeProposalAttempt"], "Workflow store");
    }
    this.durability = durability;
    this.store = store;
    const attemptStore = durability.profile === HOST_DURABILITY_PROFILES.transactionalAction
      ? store
      : {
        loadProposalAttempt: (...args) => store.loadProposalAttempt(...args),
        createProposalAttempt: (...args) => store.createProposalAttempt(...args),
        saveProposalAttempt: (...args) => store.saveProposalAttempt(...args),
        settleProposalAttempt: async () => {
          throw new RinConfigurationError(
            "host_durability_insufficient",
            "Atomic settlement is unavailable for this host",
          );
        },
      };
    this.attempts = new ProposalAttemptCoordinator(client, attemptStore);
    this.outbox = new OutcomeOutbox(client, store);
    this.resuming = false;
    this.settling = false;
  }

  begin(operationId, request) {
    return this.attempts.begin(operationId, request);
  }

  async resumePendingWork(options = {}) {
    if (this.resuming) {
      throw new RinConfigurationError(
        "workflow_busy",
        "Pending work is already being resumed",
      );
    }
    this.resuming = true;
    try {
      await this.outbox.drain();
      return await this.attempts.resume(options);
    } finally {
      this.resuming = false;
    }
  }

  async applyAndEnqueueOutcome({
    pendingTurn,
    proposal,
    report,
    requiredDurability = HOST_DURABILITY_PROFILES.advisory,
    apply,
  }) {
    if (this.settling) {
      throw new RinConfigurationError(
        "workflow_busy",
        "A Pending Turn is already being settled",
      );
    }
    this.settling = true;
    try {
      this.durability.require(requiredDurability);
      if (typeof apply !== "function") {
        throw new RinConfigurationError(
          "invalid_workflow",
          "apply must be a host-owned callback",
        );
      }
      if (this.durability.profile === HOST_DURABILITY_PROFILES.transactionalAction) {
        return await this.attempts.settle(
          pendingTurn,
          proposal,
          report,
          () => apply(pendingTurn.operation_id),
        );
      }
      const stableAttempt = validateProposalAttempt(pendingTurn);
      validateWorkflowSettlement(stableAttempt, proposal, report);
      await apply(stableAttempt.operation_id);
      await this.store.completeProposalAttempt({
        attempt: stableAttempt,
        proposal: cloneProtocolObject(proposal),
        report: cloneProtocolObject(report),
      });
    } finally {
      this.settling = false;
    }
  }

  drainOutbox() {
    return this.outbox.drain();
  }
}

export class OpaqueSnapshotPersistence {
  constructor(store) {
    requireMethods(store, ["putSnapshot", "getSnapshot"], "Snapshot store");
    this.store = store;
  }

  async save(key, snapshot) {
    const encoded = new TextEncoder().encode(serializeRequest(snapshot));
    if (encoded.byteLength > INLINE_SNAPSHOT_MAX_BYTES) {
      throw new RinProtocolError(
        "snapshot_too_large",
        "Complete Snapshot exceeds the 16 MiB inline limit",
      );
    }
    await this.store.putSnapshot(key, encoded.slice());
  }

  async load(key) {
    const stored = await this.store.getSnapshot(key);
    if (!(stored instanceof Uint8Array)) {
      throw new RinConfigurationError(
        "invalid_snapshot_store",
        "Snapshot store must return Uint8Array",
      );
    }
    if (stored.byteLength > INLINE_SNAPSHOT_MAX_BYTES) {
      throw new RinProtocolError(
        "snapshot_too_large",
        "Stored Snapshot exceeds the 16 MiB inline limit",
      );
    }
    let decoded;
    try {
      decoded = new TextDecoder("utf-8", { fatal: true }).decode(stored);
    } catch (cause) {
      throw new RinProtocolError("invalid_snapshot", "Stored Snapshot is not UTF-8", { cause });
    }
    let snapshot;
    try {
      snapshot = JSON.parse(decoded);
    } catch (cause) {
      throw new RinProtocolError("invalid_snapshot", "Stored Snapshot is not JSON", { cause });
    }
    if (!isObject(snapshot)) {
      throw new RinProtocolError("invalid_snapshot", "Stored Snapshot must be an object");
    }
    validateRequestJSON(snapshot);
    return snapshot;
  }
}

export class RinClient {
  constructor(baseUrl = DEFAULT_BASE_URL, options = {}) {
    const {
      token = "",
      timeoutMs = 5000,
      transferTimeoutMs = 120000,
      maxResponseBytes = DEFAULT_MAX_RESPONSE_BYTES,
      fetch: fetchImplementation = globalThis.fetch,
      now = () => Date.now(),
      sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
    } = options;

    this.token = validateToken(token);
    this.baseUrl = normalizeBaseUrl(baseUrl, this.token);
    this.timeoutMs = Number(timeoutMs);
    if (!Number.isFinite(this.timeoutMs) || this.timeoutMs < 50 || this.timeoutMs > 120000) {
      throw new RinConfigurationError("invalid_timeout", "timeoutMs must be between 50 and 120000");
    }
    this.transferTimeoutMs = Number(transferTimeoutMs);
    if (!Number.isFinite(this.transferTimeoutMs) ||
        this.transferTimeoutMs < 1000 ||
        this.transferTimeoutMs > 1800000) {
      throw new RinConfigurationError(
        "invalid_transfer_timeout",
        "transferTimeoutMs must be between 1000 and 1800000",
      );
    }
    this.maxResponseBytes = Number(maxResponseBytes);
    if (!Number.isSafeInteger(this.maxResponseBytes) || this.maxResponseBytes < 1024 || this.maxResponseBytes > 32 * 1024 * 1024) {
      throw new RinConfigurationError("invalid_response_limit", "response limit must be between 1 KiB and 32 MiB");
    }
    if (typeof fetchImplementation !== "function") {
      throw new RinConfigurationError("missing_fetch", "a Fetch API implementation is required");
    }
    this.fetch = fetchImplementation;
    this.now = now;
    this.sleep = sleep;
  }

  health() { return this.request("GET", "/health"); }
  async negotiateCapabilities(requiredFeatures = FEATURE_PRESETS.safeBaseline) {
    const required = validateRequiredFeatures(requiredFeatures);
    const health = await this.health();
    if (health.protocol_version !== PROTOCOL_VERSION) {
      throw new RinProtocolError(
        "protocol_mismatch",
        `Rin reports protocol ${safeText(health.protocol_version, 96) || "unknown"}`,
      );
    }
    if (!Array.isArray(health.features) ||
        health.features.some((feature) => typeof feature !== "string")) {
      throw new RinProtocolError(
        "invalid_health",
        "Rin health features must be an array of strings",
      );
    }
    if (!Array.isArray(health.recommended_features) ||
        health.recommended_features.some((feature) => typeof feature !== "string")) {
      throw new RinProtocolError(
        "invalid_health",
        "Rin health recommended_features must be an array of strings",
      );
    }
    const available = new Set(health.features);
    const missing = required.filter((feature) => !available.has(feature));
    if (missing.length !== 0) {
      throw new RinConfigurationError(
        "missing_features",
        `Rin does not support required features: ${missing.join(", ")}`,
      );
    }
    return health;
  }
  createSession(payload) { return this.post("/v2/session/create", payload); }
  observe(payload) { return this.post("/v2/session/observe", payload); }
  propose(payload) { return this.post("/v2/agent/propose", payload); }
  submitProposalJob(payload) { return this.request("POST", "/v2/jobs/propose", payload, [202]); }
  getProposalJob(jobId) { return this.request("GET", `/v2/jobs/${pathId(jobId)}`); }
  cancelProposalJob(jobId) { return this.request("DELETE", `/v2/jobs/${pathId(jobId)}`); }
  submitGenerationJob(payload) { return this.request("POST", "/v2/generation/jobs", payload, [202]); }
  getGenerationJob(jobId) { return this.request("GET", `/v2/generation/jobs/${pathId(jobId)}`); }
  cancelGenerationJob(jobId) { return this.request("DELETE", `/v2/generation/jobs/${pathId(jobId)}`); }
  reportAction(payload) { return this.post("/v2/action/report", payload); }
  reportActionBatch(payload) { return this.post("/v2/action/report-batch", payload); }
  setActorActivity(payload) { return this.post("/v2/session/activity", payload); }
  setActorAgency(payload) { return this.post("/v2/session/agency", payload); }
  arbitrate(payload) { return this.post("/v2/world/arbitrate", payload); }
  state(payload) { return this.post("/v2/session/get", payload); }
  sessionStats(payload) { return this.post("/v2/session/stats", payload); }
  archiveSession(payload) { return this.post("/v2/session/archive", payload); }
  deleteSession(payload) { return this.post("/v2/session/delete", payload); }
  snapshot(payload) { return this.post("/v2/session/snapshot", payload); }
  restore(payload) { return this.post("/v2/session/restore", payload); }
  async exportSession(payload, sink) {
    const serialized = serializeRequest(payload);
    const target = transferSink(sink);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.transferTimeoutMs);
    try {
      const response = await this.fetch(`${this.baseUrl}/v2/session/export`, {
        method: "POST",
        headers: this.headers("application/x-ndjson", "application/json"),
        body: serialized,
        signal: controller.signal,
        redirect: "error",
      });
      if (response.status !== 200) {
        throw await transferAPIError(response, this.maxResponseBytes);
      }
      requireTransferContentType(response);
      const reader = response.body?.getReader?.();
      if (!reader) {
        throw new RinProtocolError(
          "transfer_stream_unavailable",
          "Rin export response is not available as a stream",
        );
      }
      const parser = new TransferExportParser(target.write);
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          if (!(value instanceof Uint8Array)) {
            throw new RinProtocolError("invalid_transfer", "Rin returned an invalid transfer chunk");
          }
          await parser.push(value);
        }
        return parser.finish();
      } finally {
        reader.releaseLock?.();
      }
    } catch (cause) {
      if (cause instanceof RinError) throw cause;
      const timedOut = controller.signal.aborted;
      throw new RinTransportError(
        timedOut ? "transport_timeout" : "transport_failed",
        timedOut ? "Rin transfer timed out" : "Rin transfer failed",
        { cause },
      );
    } finally {
      clearTimeout(timer);
      target.release();
    }
  }

  async importSession(source, expectedBinding) {
    const body = transferSource(source);
    const binding = validateTransferBinding(expectedBinding);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.transferTimeoutMs);
    try {
      const headers = this.headers("application/json", "application/x-ndjson");
      headers["Rin-Expected-Game-Id"] = binding.game_id;
      headers["Rin-Expected-Content-Id"] = binding.content_id;
      headers["Rin-Expected-Content-Version"] = binding.content_version;
      headers["Rin-Expected-Content-Hash"] = binding.content_hash;
      const response = await this.fetch(`${this.baseUrl}/v2/session/import`, {
        method: "POST",
        headers,
        body,
        signal: controller.signal,
        redirect: "error",
        duplex: "half",
      });
      const raw = await readBoundedBody(response, this.maxResponseBytes);
      const envelope = decodeEnvelope(raw);
      if (response.status !== 200 || envelope.ok !== true) {
        throw apiError(envelope, response.status);
      }
      if (!isObject(envelope.data)) {
        throw new RinProtocolError("invalid_response", "Rin response data must be an object");
      }
      return envelope.data;
    } catch (cause) {
      if (cause instanceof RinError) throw cause;
      const timedOut = controller.signal.aborted;
      throw new RinTransportError(
        timedOut ? "transport_timeout" : "transport_failed",
        timedOut ? "Rin transfer timed out" : "Rin transfer failed",
        { cause },
      );
    } finally {
      clearTimeout(timer);
    }
  }
  timeline(payload) { return this.post("/v2/session/timeline", payload); }
  replay(payload) { return this.post("/v2/session/replay", payload); }
  dueAgents(payload) { return this.post("/v2/scheduler/due", payload); }

  waitForProposal(jobId, options = {}) {
    return this.waitJob(jobId, this.getProposalJob.bind(this), this.cancelProposalJob.bind(this), {
      deadlineMs: 25000,
      ...options,
    }, "proposal");
  }

  waitForGeneration(jobId, options = {}) {
    return this.waitJob(jobId, this.getGenerationJob.bind(this), this.cancelGenerationJob.bind(this), {
      deadlineMs: 45000,
      ...options,
    }, "generation");
  }

  async waitJob(jobId, getter, canceler, { deadlineMs, intervalMs = 100 }, resultKind = "") {
    if (!Number.isFinite(deadlineMs) || deadlineMs < 50 || deadlineMs > 300000 ||
        !Number.isFinite(intervalMs) || intervalMs < 10 || intervalMs > 5000) {
      throw new RinConfigurationError("invalid_polling", "job deadline or interval is out of range");
    }
    const expires = this.now() + deadlineMs;
    for (;;) {
      const job = await getter(jobId);
      const resolved = resolveJob(job, resultKind, jobId);
      if (resolved) return resolved;
      const remaining = expires - this.now();
      if (remaining <= 0) {
        let canceledJob;
        try {
          canceledJob = await canceler(jobId);
        } catch (error) {
          if (!(error instanceof RinError)) throw error;
          throw new RinAPIError("job_timeout", "Rin job exceeded its deadline");
        }
        const canceledResult = resolveJob(canceledJob, resultKind, jobId);
        if (canceledResult) return canceledResult;
        throw new RinAPIError("job_timeout", "Rin job exceeded its deadline");
      }
      await this.sleep(Math.min(intervalMs, remaining));
    }
  }

  post(path, payload) {
    return this.request("POST", path, payload);
  }

  headers(accept, contentType = "") {
    const headers = { Accept: accept, "User-Agent": `rin-javascript/${SDK_VERSION}` };
    if (contentType) headers["Content-Type"] = contentType;
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    return headers;
  }

  async request(method, path, payload, expectedStatuses = [200]) {
    if (typeof path !== "string" || !path.startsWith("/") || path.includes("//") || path.includes("..")) {
      throw new RinConfigurationError("invalid_path", "Rin request path is invalid");
    }
    const headers = { Accept: "application/json", "User-Agent": `rin-javascript/${SDK_VERSION}` };
    let body;
    if (payload !== undefined) {
      if (!isObject(payload)) {
        throw new RinProtocolError("invalid_request", "Rin payload must be an object");
      }
      validateRequestJSON(payload);
      try {
        body = JSON.stringify(payload);
      } catch (cause) {
        throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable", { cause });
      }
      if (typeof body !== "string") {
        throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable");
      }
      const serialized = JSON.parse(body);
      if (!isObject(serialized)) {
        throw new RinProtocolError("invalid_request", "Rin payload must serialize to an object");
      }
      validateRequestJSON(serialized);
      headers["Content-Type"] = "application/json";
    }
    if (this.token) headers.Authorization = `Bearer ${this.token}`;

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const response = await this.fetch(`${this.baseUrl}${path}`, {
        method,
        headers,
        body,
        signal: controller.signal,
        redirect: "error",
      });
      const declared = response.headers?.get?.("content-length");
      if (declared !== null && declared !== undefined && declared !== "") {
        const length = Number(declared);
        if (!Number.isSafeInteger(length) || length < 0) {
          throw new RinProtocolError("invalid_response", "Rin returned an invalid Content-Length");
        }
        if (length > this.maxResponseBytes) {
          await cancelBody(response);
          throw new RinProtocolError("response_too_large", "Rin response exceeds the configured limit");
        }
      }
      const raw = await readBoundedBody(response, this.maxResponseBytes);

      let envelope;
      try {
        envelope = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw));
      } catch (cause) {
        throw new RinProtocolError("invalid_response", "Rin returned invalid JSON", { cause });
      }
      if (!isObject(envelope)) {
        throw new RinProtocolError("invalid_response", "Rin response must be an object");
      }
      if (!expectedStatuses.includes(response.status) || envelope.ok !== true) {
        throw apiError(envelope, response.status);
      }
      if (!isObject(envelope.data)) {
        throw new RinProtocolError("invalid_response", "Rin response data must be an object");
      }
      return envelope.data;
    } catch (cause) {
      if (cause instanceof RinError) throw cause;
      const timedOut = controller.signal.aborted;
      throw new RinTransportError(
        timedOut ? "transport_timeout" : "transport_failed",
        timedOut ? "Rin request timed out" : "Rin is unavailable",
        { cause },
      );
    } finally {
      clearTimeout(timer);
    }
  }
}

function secureRandomBytes(length) {
  const crypto = globalThis.crypto;
  if (!crypto || typeof crypto.getRandomValues !== "function") {
    throw new RinConfigurationError(
      "secure_random_unavailable",
      "Web Crypto getRandomValues is required to create Rin IDs",
    );
  }
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  return bytes;
}

function validateRequiredFeatures(features) {
  if (!Array.isArray(features) ||
      features.some((feature) => typeof feature !== "string" || feature.length === 0)) {
    throw new RinConfigurationError(
      "invalid_features",
      "required features must be an array of non-empty strings",
    );
  }
  return [...new Set(features)];
}

function requireMethods(value, methods, label) {
  if (!isObject(value) || methods.some((method) => typeof value[method] !== "function")) {
    throw new RinConfigurationError(
      "invalid_workflow_store",
      `${label} must implement ${methods.join(", ")}`,
    );
  }
}

function requireIdentifier(field, value) {
  if (!isProtocolIdentifier(value)) {
    throw new RinConfigurationError(
      "invalid_workflow",
      `${field} must be a protocol identifier`,
    );
  }
}

function requireOutboxIdentifier(field, value) {
  if (!isProtocolIdentifier(value)) {
    throw new RinConfigurationError(
      "invalid_outbox",
      `Outcome Outbox ${field} must be a protocol identifier`,
    );
  }
}

function cloneProtocolObject(value) {
  return JSON.parse(serializeRequest(value));
}

function validateProposalAttempt(value) {
  if (!isObject(value) || value.version !== 1 || !isObject(value.request)) {
    throw new RinConfigurationError(
      "invalid_proposal_attempt",
      "Durable Proposal Attempt is missing or malformed",
    );
  }
  requireIdentifier("operation_id", value.operation_id);
  requireIdentifier("request_id", value.request.request_id);
  requireIdentifier("session_id", value.request.session_id);
  if (typeof value.job_id !== "string" ||
      (value.job_id.length !== 0 && !isProtocolIdentifier(value.job_id))) {
    throw new RinConfigurationError(
      "invalid_proposal_attempt",
      "Durable Proposal Attempt job_id is malformed",
    );
  }
  return cloneProtocolObject(value);
}

function validateWorkflowSettlement(attempt, proposal, report) {
  const stableProposal = cloneProtocolObject(proposal);
  const stableReport = cloneProtocolObject(report);
  if (stableProposal.session_id !== attempt.request.session_id ||
      stableProposal.request_id !== attempt.request.request_id ||
      stableReport.session_id !== attempt.request.session_id ||
      stableReport.report?.proposal_id !== stableProposal.id) {
    throw new RinConfigurationError(
      "workflow_identity_mismatch",
      "Attempt, Proposal, and Action Report identities do not match",
    );
  }
  requireIdentifier("request_id", stableReport.request_id);
  requireIdentifier("event_id", stableReport.report?.event_id);
}

function serializeRequest(payload) {
  if (!isObject(payload)) {
    throw new RinProtocolError("invalid_request", "Rin payload must be an object");
  }
  validateRequestJSON(payload);
  let body;
  try {
    body = JSON.stringify(payload);
  } catch (cause) {
    throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable", { cause });
  }
  if (typeof body !== "string") {
    throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable");
  }
  return body;
}

function transferSink(sink) {
  if (sink?.getWriter instanceof Function) {
    const writer = sink.getWriter();
    return {
      write: (chunk) => writer.write(chunk),
      release: () => writer.releaseLock?.(),
    };
  }
  if (sink?.write instanceof Function) {
    return { write: (chunk) => sink.write(chunk), release: () => {} };
  }
  throw new RinConfigurationError(
    "invalid_transfer_sink",
    "Transfer sink must provide write(Uint8Array) or be a WritableStream",
  );
}

function transferSource(source) {
  if (source?.getReader instanceof Function) return source;
  if (source?.[Symbol.asyncIterator] instanceof Function && typeof ReadableStream === "function") {
    const iterator = source[Symbol.asyncIterator]();
    return new ReadableStream({
      async pull(controller) {
        const { done, value } = await iterator.next();
        if (done) {
          controller.close();
          return;
        }
        if (!(value instanceof Uint8Array)) {
          controller.error(new RinProtocolError(
            "invalid_transfer_source",
            "Transfer source must yield Uint8Array chunks",
          ));
          return;
        }
        controller.enqueue(value);
      },
      async cancel(reason) {
        await iterator.return?.(reason);
      },
    });
  }
  throw new RinConfigurationError(
    "invalid_transfer_source",
    "Transfer source must be a ReadableStream or async Uint8Array iterable",
  );
}

function validateTransferBinding(value) {
  if (!isObject(value)) {
    throw new RinConfigurationError("invalid_binding", "Expected Binding must be an object");
  }
  const fields = {
    game_id: 96,
    content_id: 96,
    content_version: 64,
    content_hash: 128,
  };
  for (const [field, maximum] of Object.entries(fields)) {
    const text = value[field];
    if (typeof text !== "string" || text.length < 1 || text.length > maximum ||
        /[\0\r\n]/.test(text) || hasUnpairedSurrogate(text)) {
      throw new RinConfigurationError("invalid_binding", `Expected Binding ${field} is invalid`);
    }
  }
  if (!PROTOCOL_IDENTIFIER.test(value.game_id) || !PROTOCOL_IDENTIFIER.test(value.content_id)) {
    throw new RinConfigurationError("invalid_binding", "Expected Binding identifiers are invalid");
  }
  return value;
}

function requireTransferContentType(response) {
  const contentType = response.headers?.get?.("content-type") ?? "";
  if (contentType.split(";", 1)[0].trim().toLowerCase() !== "application/x-ndjson") {
    throw new RinProtocolError(
      "invalid_transfer",
      "Rin export response must be application/x-ndjson",
    );
  }
}

async function transferAPIError(response, maximum) {
  try {
    return apiError(decodeEnvelope(await readBoundedBody(response, maximum)), response.status);
  } catch (cause) {
    if (cause instanceof RinAPIError) return cause;
    if (cause instanceof RinError) return cause;
    return new RinAPIError("http_error", "Rin transfer request failed", { status: response.status });
  }
}

function decodeEnvelope(raw) {
  let envelope;
  try {
    envelope = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw));
  } catch (cause) {
    throw new RinProtocolError("invalid_response", "Rin returned invalid JSON", { cause });
  }
  if (!isObject(envelope)) {
    throw new RinProtocolError("invalid_response", "Rin response must be an object");
  }
  return envelope;
}

class TransferExportParser {
  constructor(write) {
    this.write = write;
    this.fragments = [];
    this.length = 0;
    this.manifest = null;
    this.eventCount = 0;
    this.complete = null;
  }

  async push(chunk) {
    let start = 0;
    for (let index = 0; index < chunk.byteLength; index += 1) {
      if (chunk[index] !== 0x0a) continue;
      this.append(chunk.subarray(start, index));
      await this.line();
      start = index + 1;
    }
    if (start < chunk.byteLength) this.append(chunk.subarray(start));
  }

  append(fragment) {
    this.length += fragment.byteLength;
    const maximum = this.manifest === null || this.complete !== null
      ? TRANSFER_CONTROL_FRAME_MAX_BYTES
      : TRANSFER_EVENT_FRAME_MAX_BYTES;
    if (this.length > maximum) {
      throw new RinProtocolError("transfer_frame_too_large", "Rin transfer frame exceeds its limit");
    }
    if (fragment.byteLength > 0) this.fragments.push(fragment);
  }

  async line() {
    if (this.length === 0) {
      throw new RinProtocolError("invalid_transfer", "Rin transfer contains an empty frame");
    }
    const bytes = new Uint8Array(this.length);
    let offset = 0;
    for (const fragment of this.fragments) {
      bytes.set(fragment, offset);
      offset += fragment.byteLength;
    }
    this.fragments = [];
    this.length = 0;
    let frame;
    try {
      frame = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
    } catch (cause) {
      throw new RinProtocolError("invalid_transfer", "Rin transfer contains invalid JSON", { cause });
    }
    this.validate(frame, bytes.byteLength);
    const output = new Uint8Array(bytes.byteLength + 1);
    output.set(bytes);
    output[bytes.byteLength] = 0x0a;
    await this.write(output);
  }

  validate(frame, byteLength) {
    if (!isObject(frame) || typeof frame.type !== "string") {
      throw new RinProtocolError("invalid_transfer", "Rin transfer frame is not an object");
    }
    if (frame.type === "error") {
      if (byteLength > TRANSFER_CONTROL_FRAME_MAX_BYTES) {
        throw new RinProtocolError("transfer_frame_too_large", "Rin transfer error frame exceeds its limit");
      }
      const detail = isObject(frame.error) ? frame.error : {};
      throw new RinAPIError(
        safeText(detail.code, 96) || "transfer_failed",
        safeText(detail.message, 500) || "Rin transfer failed",
        { field: safeText(detail.field, 160) },
      );
    }
    if (this.complete !== null) {
      throw new RinProtocolError("invalid_transfer", "Rin transfer contains data after complete");
    }
    if (this.manifest === null) {
      if (frame.type !== "manifest" || frame.transfer_version !== "rin.session-transfer/v1" ||
          !Number.isSafeInteger(frame.event_count) || frame.event_count < 1 ||
          frame.terminal_revision !== frame.event_count ||
          !isProtocolIdentifier(frame.session_id)) {
        throw new RinProtocolError("invalid_transfer", "Rin transfer manifest is invalid");
      }
      this.manifest = frame;
      return;
    }
    if (frame.type === "event") {
      if (!isObject(frame.record) ||
          frame.record.sequence !== this.eventCount + 1 ||
          typeof frame.record_sha256 !== "string") {
        throw new RinProtocolError("invalid_transfer", "Rin transfer event order is invalid");
      }
      this.eventCount += 1;
      if (this.eventCount > this.manifest.event_count) {
        throw new RinProtocolError("invalid_transfer", "Rin transfer contains extra events");
      }
      return;
    }
    if (frame.type !== "complete" || byteLength > TRANSFER_CONTROL_FRAME_MAX_BYTES ||
        this.eventCount !== this.manifest.event_count ||
        frame.event_count !== this.manifest.event_count ||
        frame.terminal_revision !== this.manifest.terminal_revision ||
        frame.terminal_head_hash !== this.manifest.terminal_head_hash ||
        typeof frame.stream_sha256 !== "string") {
      throw new RinProtocolError("invalid_transfer", "Rin transfer complete frame is invalid");
    }
    this.complete = frame;
  }

  finish() {
    if (this.length !== 0) {
      throw new RinProtocolError("invalid_transfer", "Rin transfer ended without an LF delimiter");
    }
    if (this.complete === null) {
      throw new RinProtocolError("invalid_transfer", "Rin transfer ended without complete");
    }
    return this.complete;
  }
}

async function readBoundedBody(response, maximum) {
  const reader = response.body?.getReader?.();
  if (!reader) {
    const raw = new Uint8Array(await response.arrayBuffer());
    if (raw.byteLength > maximum) {
      throw new RinProtocolError("response_too_large", "Rin response exceeds the configured limit");
    }
    return raw;
  }

  const chunks = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!(value instanceof Uint8Array)) {
        throw new RinProtocolError("invalid_response", "Rin response stream returned an invalid chunk");
      }
      total += value.byteLength;
      if (total > maximum) {
        await reader.cancel();
        throw new RinProtocolError("response_too_large", "Rin response exceeds the configured limit");
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock?.();
  }

  const raw = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    raw.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return raw;
}

async function cancelBody(response) {
  try {
    await response.body?.cancel?.();
  } catch {
    // The response is already being rejected; cancellation is best effort.
  }
}

function normalizeBaseUrl(value, token) {
  let parsed;
  try {
    parsed = new URL(String(value || DEFAULT_BASE_URL).trim().replace(/\/+$/, ""));
  } catch (cause) {
    throw new RinConfigurationError("invalid_base_url", "Rin base URL must be an origin", { cause });
  }
  if (!["http:", "https:"].includes(parsed.protocol) || parsed.username || parsed.password ||
      parsed.search || parsed.hash || (parsed.pathname !== "/" && parsed.pathname !== "")) {
    throw new RinConfigurationError("invalid_base_url", "Rin base URL must be an origin");
  }
  const loopback = isLoopback(parsed.hostname);
  if (parsed.protocol === "http:" && !loopback) {
    throw new RinConfigurationError("insecure_base_url", "remote Rin endpoints must use HTTPS");
  }
  if (!loopback && !token) {
    throw new RinConfigurationError("missing_token", "remote Rin endpoints require a token");
  }
  return parsed.origin;
}

function isLoopback(hostname) {
  const host = String(hostname).toLowerCase().replace(/^\[|\]$/g, "");
  if (host === "localhost" || host === "::1" || host === "0:0:0:0:0:0:0:1") return true;
  const octets = host.split(".");
  return octets.length === 4 && octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255) && Number(octets[0]) === 127;
}

function validateToken(value) {
  const token = String(value || "");
  if (token !== token.trim() || /[\0\r\n]/.test(token) || token.length > 4096) {
    throw new RinConfigurationError("invalid_token", "Rin token must be a bounded single-line value");
  }
  return token;
}

function pathId(value) {
  const text = String(value || "");
  if (!/^[A-Za-z0-9._-]{1,96}$/.test(text)) {
    throw new RinConfigurationError("invalid_identifier", "Rin path identifier is invalid");
  }
  return encodeURIComponent(text);
}

function apiError(envelope, status) {
  const detail = isObject(envelope.error) ? envelope.error : {};
  return new RinAPIError(
    safeText(detail.code, 96) || "http_error",
    safeText(detail.message, 500) || "Rin request failed",
    { status, field: safeText(detail.field, 160) },
  );
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function validateRequestJSON(value) {
  const active = new WeakSet();
  const visit = (current, depth) => {
    if (depth > 64) {
      throw new RinProtocolError("invalid_request", "Rin payload exceeds the JSON nesting limit");
    }
    if (current === null || typeof current === "boolean") return;
    if (typeof current === "string") {
      if (hasUnpairedSurrogate(current)) {
        throw new RinProtocolError("invalid_request", "Rin payload contains invalid Unicode");
      }
      return;
    }
    if (typeof current === "number") {
      if (!Number.isFinite(current)) {
        throw new RinProtocolError("invalid_request", "Rin payload contains a non-finite JSON number");
      }
      if (Number.isInteger(current) && !Number.isSafeInteger(current)) {
        throw new RinProtocolError("invalid_request", "Rin payload contains an unsafe JSON integer");
      }
      return;
    }
    if (typeof current !== "object") {
      throw new RinProtocolError("invalid_request", "Rin payload contains a non-JSON value");
    }
    const array = Array.isArray(current);
    const prototype = Object.getPrototypeOf(current);
    if ((!array && prototype !== Object.prototype && prototype !== null) ||
        typeof current.toJSON === "function" ||
        Object.getOwnPropertySymbols(current).length !== 0) {
      throw new RinProtocolError("invalid_request", "Rin payload contains a non-JSON value");
    }
    if (active.has(current)) {
      throw new RinProtocolError("invalid_request", "Rin payload contains a JSON cycle");
    }
    active.add(current);
    try {
      if (array) {
        const keys = Object.keys(current);
        if (keys.length !== current.length) {
          throw new RinProtocolError("invalid_request", "Rin payload contains a sparse or extended JSON array");
        }
        for (let index = 0; index < current.length; index += 1) {
          if (!Object.hasOwn(current, index)) {
            throw new RinProtocolError("invalid_request", "Rin payload contains a sparse or extended JSON array");
          }
          visit(current[index], depth + 1);
        }
      } else {
        for (const [key, child] of Object.entries(current)) {
          if (hasUnpairedSurrogate(key)) {
            throw new RinProtocolError("invalid_request", "Rin payload contains invalid Unicode");
          }
          visit(child, depth + 1);
        }
      }
    } finally {
      active.delete(current);
    }
  };
  visit(value, 0);
}

function hasUnpairedSurrogate(value) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) return true;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function resolveJob(job, resultKind = "", expectedJobId = "") {
  if (!isObject(job)) {
    throw new RinProtocolError("invalid_job", "Rin returned an invalid job");
  }
  validateJobIdentity(job, resultKind, expectedJobId);
  if (typeof job.status !== "string") {
    throw new RinProtocolError("invalid_job", "Rin returned an invalid job status");
  }
  const status = job.status;
  if (status === "succeeded") {
    if (resultKind === "proposal") {
      const proposal = job.proposal;
      if (!isObject(proposal)) {
        throw new RinProtocolError("invalid_job", "Successful proposal job did not include a proposal");
      }
      if (!isProtocolIdentifier(proposal.id) ||
          !isProtocolIdentifier(proposal.actor_id) ||
          proposal.session_id !== job.session_id ||
          proposal.request_id !== job.request_id ||
          !Number.isSafeInteger(proposal.tick) ||
          proposal.tick < 0) {
        throw new RinProtocolError("invalid_job", "Successful proposal job contained invalid identity fields");
      }
    }
    if (resultKind === "generation") {
      const content = isObject(job.result) ? job.result.content : null;
      if (typeof content !== "string" ||
          content.trim().length === 0 ||
          content.includes("\0") ||
          new TextEncoder().encode(content).byteLength > MAX_GENERATION_CONTENT_BYTES) {
        throw new RinProtocolError("invalid_job", "Successful generation job did not include content");
      }
    }
    return job;
  }
  if (TERMINAL_JOB_STATES.has(status)) {
    const detail = isObject(job.error) ? job.error : {};
    throw new RinAPIError(
      safeText(detail.code, 96) || `job_${status}`,
      safeText(detail.message, 500) || `Rin job ended as ${status}`,
    );
  }
  if (status !== "queued" && status !== "running") {
    throw new RinProtocolError("invalid_job", "Rin returned an unknown job status");
  }
  return null;
}

function validateJobIdentity(job, resultKind, expectedJobId) {
  if (!isProtocolIdentifier(job.job_id) || job.job_id !== expectedJobId) {
    throw new RinProtocolError("invalid_job", "Rin returned a job with an invalid or mismatched job_id");
  }
  if (resultKind === "proposal") {
    if (!isProtocolIdentifier(job.session_id) || !isProtocolIdentifier(job.request_id)) {
      throw new RinProtocolError("invalid_job", "Rin returned a proposal job with invalid identity fields");
    }
  } else if (resultKind === "generation" && !isProtocolIdentifier(job.request_id)) {
    throw new RinProtocolError("invalid_job", "Rin returned a generation job with an invalid request_id");
  }
}

function isProtocolIdentifier(value) {
  return typeof value === "string" && PROTOCOL_IDENTIFIER.test(value);
}

function safeText(value, maximum) {
  return String(value ?? "").replace(/\0/g, "").trim().split(/\s+/).filter(Boolean).join(" ").slice(0, maximum);
}
