local rin = {
    PROTOCOL_VERSION = "rin.protocol/v1",
    DEFAULT_BASE_URL = "http://127.0.0.1:7374",
    DEFAULT_MAX_RESPONSE_BYTES = 2 * 1024 * 1024,
}

local Client = {}
Client.__index = Client

local terminal_job_states = {
    succeeded = true,
    failed = true,
    stale = true,
    canceled = true,
}

local function safe_text(value, maximum, fallback)
    local text = tostring(value or ""):gsub("%z", " "):gsub("%s+", " ")
    text = text:match("^%s*(.-)%s*$") or ""
    if text == "" then text = fallback or "" end
    return text:sub(1, maximum)
end

local function failure(code, message, status, field)
    return {
        code = safe_text(code, 96, "rin_error"),
        message = safe_text(message, 500, "Rin request failed"),
        status = tonumber(status) or 0,
        field = safe_text(field, 160, ""),
    }
end

local function validate_token(value)
    local token = tostring(value or "")
    if #token > 4096 or token:find("[%z\r\n]") or token:match("^%s") or token:match("%s$") then
        return nil, failure("invalid_token", "Rin token must be a bounded single-line value")
    end
    return token
end

local function is_loopback(host)
    host = host:lower()
    if host == "localhost" or host == "::1" or host == "0:0:0:0:0:0:0:1" then return true end
    local first, second, third, fourth = host:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")
    if not first then return false end
    local octets = { tonumber(first), tonumber(second), tonumber(third), tonumber(fourth) }
    if octets[1] ~= 127 then return false end
    for index = 1, 4 do
        if octets[index] < 0 or octets[index] > 255 then return false end
    end
    return true
end

local function normalize_base_url(value, token)
    local base_url = tostring(value or rin.DEFAULT_BASE_URL):match("^%s*(.-)%s*$")
    while base_url:sub(-1) == "/" do base_url = base_url:sub(1, -2) end
    local scheme, authority = base_url:match("^(https?)://([^/%?#]+)$")
    if not scheme or authority:find("@", 1, true) then
        return nil, failure("invalid_base_url", "Rin base URL must be an origin")
    end

    local host, port
    if authority:sub(1, 1) == "[" then
        host, port = authority:match("^%[([^%]]+)%]:(%d+)$")
        if not host then host = authority:match("^%[([^%]]+)%]$") end
    else
        host, port = authority:match("^([^:]+):(%d+)$")
        if not host and not authority:find(":", 1, true) then host = authority end
    end
    if not host or host == "" then
        return nil, failure("invalid_base_url", "Rin base URL must be an origin")
    end
    if port and (tonumber(port) < 1 or tonumber(port) > 65535) then
        return nil, failure("invalid_base_url", "Rin base URL has an invalid port")
    end

    local loopback = is_loopback(host)
    if scheme == "http" and not loopback then
        return nil, failure("insecure_base_url", "Remote Rin endpoints must use HTTPS")
    end
    if not loopback and token == "" then
        return nil, failure("missing_token", "Remote Rin endpoints require a token")
    end
    return base_url
end

local function path_id(value)
    local text = tostring(value or "")
    if #text < 1 or #text > 96 then
        return nil, failure("invalid_identifier", "Rin path identifier is invalid")
    end
    for index = 1, #text do
        local byte = text:byte(index)
        local valid = (byte >= 48 and byte <= 57) or (byte >= 65 and byte <= 90) or
            (byte >= 97 and byte <= 122) or byte == 45 or byte == 46 or byte == 95
        if not valid then
            return nil, failure("invalid_identifier", "Rin path identifier is invalid")
        end
    end
    return text
end

local function header_value(headers, wanted)
    if type(headers) ~= "table" then return nil end
    wanted = wanted:lower()
    for key, value in pairs(headers) do
        if tostring(key):lower() == wanted then return tostring(value) end
    end
    return nil
end

function rin.new(options)
    options = options or {}
    if type(options) ~= "table" then
        return nil, failure("invalid_options", "Rin options must be a table")
    end
    if type(options.http_fetch) ~= "function" or type(options.json_encode) ~= "function" or
        type(options.json_decode) ~= "function" then
        return nil, failure("missing_adapter", "http_fetch, json_encode, and json_decode are required")
    end

    local token, token_error = validate_token(options.token)
    if not token then return nil, token_error end
    local base_url, url_error = normalize_base_url(options.base_url, token)
    if not base_url then return nil, url_error end
    local timeout = tonumber(options.timeout or 5)
    local max_response_bytes = tonumber(options.max_response_bytes or rin.DEFAULT_MAX_RESPONSE_BYTES)
    if not timeout or timeout ~= timeout or timeout < 0.05 or timeout > 120 then
        return nil, failure("invalid_timeout", "Timeout must be between 0.05 and 120 seconds")
    end
    if not max_response_bytes or max_response_bytes ~= math.floor(max_response_bytes) or
        max_response_bytes < 1024 or max_response_bytes > 32 * 1024 * 1024 then
        return nil, failure("invalid_response_limit", "Response limit must be between 1 KiB and 32 MiB")
    end

    return setmetatable({
        base_url = base_url,
        token = token,
        timeout = timeout,
        max_response_bytes = max_response_bytes,
        http_fetch = options.http_fetch,
        json_encode = options.json_encode,
        json_decode = options.json_decode,
        schedule = options.schedule,
        now = options.now or os.time,
    }, Client)
end

function Client:_request(method, path, payload, expected_status, callback)
    if type(callback) ~= "function" then error("Rin callback is required", 2) end
    if type(path) ~= "string" or path:sub(1, 1) ~= "/" or path:find("//", 1, true) or path:find("..", 1, true) then
        callback(nil, failure("invalid_path", "Rin request path is invalid"))
        return
    end

    local body
    if payload ~= nil then
        if type(payload) ~= "table" then
            callback(nil, failure("invalid_request", "Rin payload must be an object"))
            return
        end
        local encoded, value = pcall(self.json_encode, payload)
        if not encoded or type(value) ~= "string" then
            callback(nil, failure("invalid_request", "Rin payload is not JSON serializable"))
            return
        end
        body = value
    end

    local headers = {
        ["Accept"] = "application/json",
        ["User-Agent"] = "rin-lua/0.5",
    }
    if body then headers["Content-Type"] = "application/json; charset=utf-8" end
    if self.token ~= "" then headers["Authorization"] = "Bearer " .. self.token end
    local request = {
        url = self.base_url .. path,
        method = method,
        headers = headers,
        body = body,
        timeout = self.timeout,
        follow_redirects = false,
    }

    local delivered = false
    local function finish(data, err)
        if delivered then return end
        delivered = true
        callback(data, err)
    end
    local started, start_error = pcall(self.http_fetch, request, function(response)
        if type(response) ~= "table" then
            finish(nil, failure("transport_failed", "Rin transport returned an invalid response"))
            return
        end
        local status = tonumber(response.status)
        if not status or status ~= math.floor(status) or status < 100 or status > 599 then
            finish(nil, failure("transport_failed", "Rin transport did not return a valid status"))
            return
        end
        if status >= 300 and status < 400 then
            finish(nil, failure("redirect_rejected", "Rin endpoint attempted to redirect", status))
            return
        end
        local raw = response.body
        if type(raw) ~= "string" then
            finish(nil, failure("invalid_response", "Rin response body must be a string", status))
            return
        end
        local declared_text = header_value(response.headers, "content-length")
        local declared = declared_text and tonumber(declared_text) or nil
        if declared_text and (not declared or declared < 0 or declared ~= math.floor(declared)) then
            finish(nil, failure("invalid_response", "Rin returned an invalid Content-Length", status))
            return
        end
        if (declared and declared > self.max_response_bytes) or #raw > self.max_response_bytes then
            finish(nil, failure("response_too_large", "Rin response exceeds the configured limit", status))
            return
        end

        local decoded, envelope = pcall(self.json_decode, raw)
        if not decoded or type(envelope) ~= "table" then
            if status ~= expected_status then
                finish(nil, failure("http_error", "Rin request failed", status))
            else
                finish(nil, failure("invalid_response", "Rin returned invalid JSON", status))
            end
            return
        end
        if status ~= expected_status or envelope.ok ~= true then
            local detail = type(envelope.error) == "table" and envelope.error or {}
            finish(nil, failure(detail.code or "http_error", detail.message or "Rin request failed", status, detail.field))
            return
        end
        if type(envelope.data) ~= "table" then
            finish(nil, failure("invalid_response", "Rin response data must be an object", status))
            return
        end
        finish(envelope.data, nil)
    end)
    if not started then
        if delivered then error(start_error, 0) end
        finish(nil, failure("transport_failed", "Rin transport could not start"))
    end
end

function Client:_post(path, payload, status, callback)
    self:_request("POST", path, payload, status or 200, callback)
end

function Client:health(callback) self:_request("GET", "/health", nil, 200, callback) end
function Client:create_session(payload, callback) self:_post("/v1/session/create", payload, 200, callback) end
function Client:observe(payload, callback) self:_post("/v1/session/observe", payload, 200, callback) end
function Client:propose(payload, callback) self:_post("/v1/agent/propose", payload, 200, callback) end
function Client:submit_proposal_job(payload, callback) self:_post("/v1/jobs/propose", payload, 202, callback) end
function Client:get_proposal_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("GET", "/v1/jobs/" .. id, nil, 200, callback)
end
function Client:cancel_proposal_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("DELETE", "/v1/jobs/" .. id, nil, 200, callback)
end
function Client:submit_generation_job(payload, callback) self:_post("/v1/generation/jobs", payload, 202, callback) end
function Client:get_generation_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("GET", "/v1/generation/jobs/" .. id, nil, 200, callback)
end
function Client:cancel_generation_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("DELETE", "/v1/generation/jobs/" .. id, nil, 200, callback)
end
function Client:commit(payload, callback) self:_post("/v1/action/commit", payload, 200, callback) end
function Client:commit_batch(payload, callback) self:_post("/v1/action/commit-batch", payload, 200, callback) end
function Client:set_actor_activity(payload, callback) self:_post("/v1/session/activity", payload, 200, callback) end
function Client:arbitrate(payload, callback) self:_post("/v1/world/arbitrate", payload, 200, callback) end
function Client:state(payload, callback) self:_post("/v1/session/get", payload, 200, callback) end
function Client:snapshot(payload, callback) self:_post("/v1/session/snapshot", payload, 200, callback) end
function Client:restore(payload, callback) self:_post("/v1/session/restore", payload, 200, callback) end
function Client:timeline(payload, callback) self:_post("/v1/session/timeline", payload, 200, callback) end
function Client:replay(payload, callback) self:_post("/v1/session/replay", payload, 200, callback) end
function Client:due_agents(payload, callback) self:_post("/v1/scheduler/due", payload, 200, callback) end

function Client:_wait_job(job_id, getter, canceler, options, callback)
    options = options or {}
    local deadline = tonumber(options.deadline or 25)
    local interval = tonumber(options.interval or 0.1)
    if type(self.schedule) ~= "function" then
        callback(nil, failure("missing_scheduler", "A scheduler is required to wait for jobs"))
        return
    end
    if not deadline or deadline ~= deadline or deadline < 0.05 or deadline > 300 or
        not interval or interval ~= interval or interval < 0.01 or interval > 5 then
        callback(nil, failure("invalid_polling", "Job deadline or interval is out of range"))
        return
    end
    local expires = self.now() + deadline
    local poll
    poll = function()
        getter(self, job_id, function(job, err)
            if err then callback(nil, err); return end
            local status = tostring(job.status or "")
            if status == "succeeded" then callback(job, nil); return end
            if terminal_job_states[status] then
                local detail = type(job.error) == "table" and job.error or {}
                callback(nil, failure(detail.code or ("job_" .. status), detail.message or ("Rin job ended as " .. status)))
                return
            end
            if status ~= "queued" and status ~= "running" then
                callback(nil, failure("invalid_job", "Rin returned an unknown job status"))
                return
            end
            if self.now() >= expires then
                canceler(self, job_id, function() end)
                callback(nil, failure("job_timeout", "Rin job exceeded its deadline"))
                return
            end
            self.schedule(interval, poll)
        end)
    end
    poll()
end

function Client:wait_for_proposal(job_id, options, callback)
    self:_wait_job(job_id, Client.get_proposal_job, Client.cancel_proposal_job, options, callback)
end

function Client:wait_for_generation(job_id, options, callback)
    local configured = {}
    for key, value in pairs(options or {}) do configured[key] = value end
    if configured.deadline == nil then configured.deadline = 45 end
    self:_wait_job(job_id, Client.get_generation_job, Client.cancel_generation_job, configured, callback)
end

return rin
