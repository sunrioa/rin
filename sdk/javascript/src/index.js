export const PROTOCOL_VERSION = "rin.protocol/v1";
export const DEFAULT_BASE_URL = "http://127.0.0.1:7374";
export const DEFAULT_MAX_RESPONSE_BYTES = 2 * 1024 * 1024;

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

export class RinClient {
  constructor(baseUrl = DEFAULT_BASE_URL, options = {}) {
    const {
      token = "",
      timeoutMs = 5000,
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
  createSession(payload) { return this.post("/v1/session/create", payload); }
  observe(payload) { return this.post("/v1/session/observe", payload); }
  propose(payload) { return this.post("/v1/agent/propose", payload); }
  submitProposalJob(payload) { return this.request("POST", "/v1/jobs/propose", payload, [202]); }
  getProposalJob(jobId) { return this.request("GET", `/v1/jobs/${pathId(jobId)}`); }
  cancelProposalJob(jobId) { return this.request("DELETE", `/v1/jobs/${pathId(jobId)}`); }
  submitGenerationJob(payload) { return this.request("POST", "/v1/generation/jobs", payload, [202]); }
  getGenerationJob(jobId) { return this.request("GET", `/v1/generation/jobs/${pathId(jobId)}`); }
  cancelGenerationJob(jobId) { return this.request("DELETE", `/v1/generation/jobs/${pathId(jobId)}`); }
  commit(payload) { return this.post("/v1/action/commit", payload); }
  commitBatch(payload) { return this.post("/v1/action/commit-batch", payload); }
  setActorActivity(payload) { return this.post("/v1/session/activity", payload); }
  arbitrate(payload) { return this.post("/v1/world/arbitrate", payload); }
  state(payload) { return this.post("/v1/session/get", payload); }
  snapshot(payload) { return this.post("/v1/session/snapshot", payload); }
  restore(payload) { return this.post("/v1/session/restore", payload); }
  timeline(payload) { return this.post("/v1/session/timeline", payload); }
  replay(payload) { return this.post("/v1/session/replay", payload); }
  dueAgents(payload) { return this.post("/v1/scheduler/due", payload); }

  waitForProposal(jobId, options = {}) {
    return this.waitJob(jobId, this.getProposalJob.bind(this), this.cancelProposalJob.bind(this), {
      deadlineMs: 25000,
      ...options,
    });
  }

  waitForGeneration(jobId, options = {}) {
    return this.waitJob(jobId, this.getGenerationJob.bind(this), this.cancelGenerationJob.bind(this), {
      deadlineMs: 45000,
      ...options,
    });
  }

  async waitJob(jobId, getter, canceler, { deadlineMs, intervalMs = 100 }) {
    if (!Number.isFinite(deadlineMs) || deadlineMs < 50 || deadlineMs > 300000 ||
        !Number.isFinite(intervalMs) || intervalMs < 10 || intervalMs > 5000) {
      throw new RinConfigurationError("invalid_polling", "job deadline or interval is out of range");
    }
    const expires = this.now() + deadlineMs;
    for (;;) {
      const job = await getter(jobId);
      const status = String(job.status || "");
      if (status === "succeeded") return job;
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
      const remaining = expires - this.now();
      if (remaining <= 0) {
        try { await canceler(jobId); } catch (error) { if (!(error instanceof RinError)) throw error; }
        throw new RinAPIError("job_timeout", "Rin job exceeded its deadline");
      }
      await this.sleep(Math.min(intervalMs, remaining));
    }
  }

  post(path, payload) {
    return this.request("POST", path, payload);
  }

  async request(method, path, payload, expectedStatuses = [200]) {
    if (typeof path !== "string" || !path.startsWith("/") || path.includes("//") || path.includes("..")) {
      throw new RinConfigurationError("invalid_path", "Rin request path is invalid");
    }
    const headers = { Accept: "application/json", "User-Agent": "rin-javascript/0.5" };
    let body;
    if (payload !== undefined) {
      if (!isObject(payload)) {
        throw new RinProtocolError("invalid_request", "Rin payload must be an object");
      }
      try {
        body = JSON.stringify(payload);
      } catch (cause) {
        throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable", { cause });
      }
      headers["Content-Type"] = "application/json";
    }
    if (this.token) headers.Authorization = `Bearer ${this.token}`;

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    let response;
    try {
      response = await this.fetch(`${this.baseUrl}${path}`, {
        method,
        headers,
        body,
        signal: controller.signal,
        redirect: "error",
      });
    } catch (cause) {
      if (cause instanceof RinError) throw cause;
      throw new RinTransportError("transport_failed", "Rin is unavailable", { cause });
    } finally {
      clearTimeout(timer);
    }

    const declared = response.headers?.get?.("content-length");
    if (declared !== null && declared !== undefined && declared !== "") {
      const length = Number(declared);
      if (!Number.isSafeInteger(length) || length < 0) {
        throw new RinProtocolError("invalid_response", "Rin returned an invalid Content-Length");
      }
      if (length > this.maxResponseBytes) {
        throw new RinProtocolError("response_too_large", "Rin response exceeds the configured limit");
      }
    }
    const raw = await response.arrayBuffer();
    if (raw.byteLength > this.maxResponseBytes) {
      throw new RinProtocolError("response_too_large", "Rin response exceeds the configured limit");
    }

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

function safeText(value, maximum) {
  return String(value ?? "").replace(/\0/g, "").trim().split(/\s+/).filter(Boolean).join(" ").slice(0, maximum);
}
