export const SDK_VERSION = "0.7.0";
export const CONTROL_CONTRACT_VERSION = "rin.control/v2";
export const CONTROL_DEFAULT_BASE_URL = "http://127.0.0.1:7375";
export const CONTROL_MAX_RESPONSE_BYTES = 8 * 1024 * 1024;

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

/** Thin client for the loopback Control V2 daemon. */
export class RinControlClient {
  constructor(baseUrlOrOptions = CONTROL_DEFAULT_BASE_URL, providedOptions) {
    const baseUrl = isObject(baseUrlOrOptions)
      ? CONTROL_DEFAULT_BASE_URL
      : baseUrlOrOptions;
    const options = isObject(baseUrlOrOptions)
      ? baseUrlOrOptions
      : providedOptions ?? {};
    const {
      token = "",
      timeoutMs = 30000,
      maxResponseBytes = CONTROL_MAX_RESPONSE_BYTES,
      fetch: fetchImplementation = globalThis.fetch,
    } = options;
    this.token = validateControlToken(token);
    this.baseUrl = normalizeControlBaseUrl(baseUrl);
    this.timeoutMs = Number(timeoutMs);
    if (!Number.isSafeInteger(this.timeoutMs) || this.timeoutMs < 50 || this.timeoutMs > 120000) {
      throw new RinConfigurationError(
        "invalid_timeout",
        "Control timeoutMs must be between 50 and 120000",
      );
    }
    this.maxResponseBytes = Number(maxResponseBytes);
    if (!Number.isSafeInteger(this.maxResponseBytes) ||
        this.maxResponseBytes < 1024 ||
        this.maxResponseBytes > CONTROL_MAX_RESPONSE_BYTES) {
      throw new RinConfigurationError(
        "invalid_response_limit",
        "Control response limit must be between 1 KiB and 8 MiB",
      );
    }
    if (typeof fetchImplementation !== "function") {
      throw new RinConfigurationError("missing_fetch", "a Fetch API implementation is required");
    }
    this.fetch = fetchImplementation;
  }

  async info() {
    const info = await this.request("GET", "/control/v2/info");
    if (!isObject(info) || info.contract_version !== CONTROL_CONTRACT_VERSION) {
      throw new RinProtocolError(
        "control_contract_mismatch",
        "Control Daemon returned an unsupported contract",
      );
    }
    return info;
  }

  listWorlds() { return this.post("/control/v2/worlds", {}); }
  listActors(input) { return this.post("/control/v2/actors", input); }
  getActor(input) { return this.post("/control/v2/actor", input); }
  waitActor(input) { return this.post("/control/v2/wait-actor", input); }
  observeActor(input) { return this.post("/control/v2/observe", input); }
  listCapabilities(input) { return this.post("/control/v2/capabilities", input); }
  describeCapability(input) { return this.post("/control/v2/capability", input); }
  acquireController(input) { return this.post("/control/v2/controllers/acquire", input); }
  renewController(input) { return this.post("/control/v2/controllers/renew", input); }
  releaseController(input) { return this.post("/control/v2/controllers/release", input); }
  getController(input) { return this.post("/control/v2/controllers/get", input); }
  submitAction(input) { return this.post("/control/v2/actions/submit", input); }
  confirmAction(input) { return this.post("/control/v2/actions/confirm", input); }
  getOperation(input) { return this.post("/control/v2/operations/get", input); }
  waitOperation(input) { return this.post("/control/v2/operations/wait", input); }
  getTaskTimeline(input) { return this.post("/control/v2/tasks/timeline/get", input); }
  waitTaskTimeline(input) { return this.post("/control/v2/tasks/timeline/wait", input); }
  cancelOperation(input) { return this.post("/control/v2/operations/cancel", input); }
  setEmergencyStop(input) { return this.post("/control/v2/emergency-stop", input); }

  post(path, input) {
    return this.request("POST", path, input);
  }

  async request(method, path, input) {
    if (typeof path !== "string" || !path.startsWith("/control/v2/") ||
        path.includes("//") || path.includes("..")) {
      throw new RinConfigurationError("invalid_path", "Control request path is invalid");
    }
    const headers = {
      Accept: "application/json",
      Authorization: `Bearer ${this.token}`,
      "User-Agent": `rin-control-javascript/${SDK_VERSION}`,
    };
    let body;
    if (input !== undefined) {
      body = serializeRequest(input);
      headers["Content-Type"] = "application/json";
    }
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
      if (response.status >= 300 && response.status < 400) {
        await cancelBody(response);
        throw new RinTransportError(
          "redirect_rejected",
          "Control Daemon attempted to redirect",
        );
      }
      const contentType = response.headers?.get?.("content-type") ?? "";
      if (contentType.split(";", 1)[0].trim().toLowerCase() !== "application/json") {
        await cancelBody(response);
        throw new RinProtocolError(
          "invalid_response",
          "Control Daemon response must be application/json",
        );
      }
      const declared = response.headers?.get?.("content-length");
      if (declared !== null && declared !== undefined && declared !== "") {
        const length = Number(declared);
        if (!Number.isSafeInteger(length) || length < 0) {
          throw new RinProtocolError(
            "invalid_response",
            "Control Daemon returned an invalid Content-Length",
          );
        }
        if (length > this.maxResponseBytes) {
          await cancelBody(response);
          throw new RinProtocolError(
            "response_too_large",
            "Control Daemon response exceeds the configured limit",
          );
        }
      }
      const raw = await readBoundedBody(response, this.maxResponseBytes);
      let value;
      try {
        value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw));
      } catch (cause) {
        throw new RinProtocolError(
          "invalid_response",
          "Control Daemon returned invalid JSON",
          { cause },
        );
      }
      if (!isObject(value) && !Array.isArray(value)) {
        throw new RinProtocolError(
          "invalid_response",
          "Control Daemon response must be an object or array",
        );
      }
      if (response.status < 200 || response.status >= 300) {
        const error = isObject(value) ? value : {};
        throw new RinAPIError(
          safeText(error.code, 96) || controlErrorCode(response.status),
          safeText(error.error, 500) || "Control Daemon request failed",
          { status: response.status },
        );
      }
      return value;
    } catch (cause) {
      if (cause instanceof RinError) throw cause;
      const timedOut = controller.signal.aborted;
      throw new RinTransportError(
        timedOut ? "transport_timeout" : "transport_failed",
        timedOut ? "Control Daemon request timed out" : "Control Daemon is unavailable",
        { cause },
      );
    } finally {
      clearTimeout(timer);
    }
  }
}

function serializeRequest(payload) {
  if (!isObject(payload)) {
    throw new RinProtocolError("invalid_request", "Rin payload must be an object");
  }
  validateRequestJSON(payload);
  try {
    const body = JSON.stringify(payload);
    if (typeof body === "string") return body;
  } catch (cause) {
    throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable", { cause });
  }
  throw new RinProtocolError("invalid_request", "Rin payload is not JSON serializable");
}

async function readBoundedBody(response, maximum) {
  const reader = response.body?.getReader?.();
  if (!reader) {
    throw new RinProtocolError(
      "invalid_response",
      "Rin response body does not provide a bounded stream",
    );
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
    // Rejection is already authoritative; cancellation is best effort.
  }
}

function normalizeControlBaseUrl(value) {
  let parsed;
  try {
    parsed = new URL(String(value || CONTROL_DEFAULT_BASE_URL).trim());
  } catch (cause) {
    throw new RinConfigurationError(
      "invalid_base_url",
      "Control Daemon URL must be a plain loopback HTTP origin",
      { cause },
    );
  }
  if (parsed.protocol !== "http:" || parsed.username || parsed.password ||
      parsed.search || parsed.hash || (parsed.pathname !== "/" && parsed.pathname !== "") ||
      !parsed.port || !isLoopback(parsed.hostname)) {
    throw new RinConfigurationError(
      "invalid_base_url",
      "Control Daemon URL must be a plain loopback HTTP origin with an explicit port",
    );
  }
  return parsed.origin;
}

function validateControlToken(value) {
  const token = validateToken(value);
  if (new TextEncoder().encode(token).byteLength < 32) {
    throw new RinConfigurationError(
      "invalid_token",
      "Control token must contain at least 32 bytes",
    );
  }
  return token;
}

function validateToken(value) {
  const token = String(value || "");
  if (token !== token.trim() || /[\0\r\n]/.test(token) || token.length > 4096) {
    throw new RinConfigurationError("invalid_token", "Rin token must be a bounded single-line value");
  }
  return token;
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

function isLoopback(hostname) {
  const host = String(hostname).toLowerCase().replace(/^\[|\]$/g, "");
  if (host === "localhost" || host === "::1" || host === "0:0:0:0:0:0:0:1") return true;
  const octets = host.split(".");
  return octets.length === 4 &&
    octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255) &&
    Number(octets[0]) === 127;
}

function controlErrorCode(status) {
  if (status === 400) return "invalid";
  if (status === 401 || status === 403) return "forbidden";
  if (status === 404) return "not_found";
  if (status === 409) return "conflict";
  if (status === 410) return "unavailable";
  if (status === 429) return "capacity";
  return "unavailable";
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function safeText(value, maximum) {
  return String(value ?? "").replace(/\0/g, "").trim().split(/\s+/).filter(Boolean).join(" ").slice(0, maximum);
}
