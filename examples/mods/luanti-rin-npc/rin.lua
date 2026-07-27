local rin = {
    VERSION = "0.7.0",
    PROTOCOL_VERSION = "rin.protocol/v2",
    DEFAULT_BASE_URL = "http://127.0.0.1:7374",
    DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024,
}

local Client = {}
Client.__index = Client

local terminal_job_states = {
    succeeded = true,
    failed = true,
    stale = true,
    canceled = true,
}

local max_generation_content_bytes = 4 * 1024 * 1024
local max_safe_float_integer = 9007199254740991
local max_json_depth = 64

local function valid_utf8(value)
    local index = 1
    while index <= #value do
        local first = value:byte(index)
        if first <= 0x7f then
            index = index + 1
        elseif first >= 0xc2 and first <= 0xdf then
            local second = value:byte(index + 1)
            if not second or second < 0x80 or second > 0xbf then return false end
            index = index + 2
        elseif first >= 0xe0 and first <= 0xef then
            local second, third = value:byte(index + 1, index + 2)
            if not second or not third or third < 0x80 or third > 0xbf then return false end
            if first == 0xe0 then
                if second < 0xa0 or second > 0xbf then return false end
            elseif first == 0xed then
                if second < 0x80 or second > 0x9f then return false end
            elseif second < 0x80 or second > 0xbf then
                return false
            end
            index = index + 3
        elseif first >= 0xf0 and first <= 0xf4 then
            local second, third, fourth = value:byte(index + 1, index + 3)
            if not second or not third or not fourth or
                third < 0x80 or third > 0xbf or fourth < 0x80 or fourth > 0xbf then
                return false
            end
            if first == 0xf0 then
                if second < 0x90 or second > 0xbf then return false end
            elseif first == 0xf4 then
                if second < 0x80 or second > 0x8f then return false end
            elseif second < 0x80 or second > 0xbf then
                return false
            end
            index = index + 4
        else
            return false
        end
    end
    return true
end

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

local function validate_request_json(value, depth, active)
    if depth > max_json_depth then
        return false, "Rin payload exceeds the JSON nesting limit"
    end
    local value_type = type(value)
    if value == nil or value_type == "boolean" then
        return true
    end
    if value_type == "string" then
        if not valid_utf8(value) then return false, "Rin payload contains invalid UTF-8" end
        return true
    end
    if value_type == "number" then
        if value ~= value or value == math.huge or value == -math.huge then
            return false, "Rin payload contains a non-finite JSON number"
        end
        if value == math.floor(value) and
            (value < -max_safe_float_integer or value > max_safe_float_integer) then
            return false, "Rin payload contains an unsafe JSON integer"
        end
        return true
    end
    if value_type ~= "table" then
        return false, "Rin payload contains a non-JSON value"
    end
    if active[value] then return false, "Rin payload contains a JSON cycle" end
    local string_keys = 0
    local array_keys = 0
    local maximum_array_index = 0
    for key in pairs(value) do
        if type(key) == "string" then
            if not valid_utf8(key) then
                return false, "Rin payload table contains an invalid UTF-8 key"
            end
            string_keys = string_keys + 1
        elseif type(key) == "number" and key >= 1 and key == math.floor(key) then
            array_keys = array_keys + 1
            if key > maximum_array_index then maximum_array_index = key end
        else
            return false, "Rin payload table contains a non-JSON key"
        end
    end
    if string_keys > 0 and array_keys > 0 then
        return false, "Rin payload table mixes object and array keys"
    end
    if string_keys == 0 and array_keys == 0 then
        return false,
            "Rin payload contains an ambiguous empty Lua table; add an authored field or omit it"
    end
    if depth == 0 and array_keys > 0 then
        return false, "Rin payload must be a JSON object"
    end
    if array_keys > 0 and maximum_array_index ~= array_keys then
        return false, "Rin payload table contains a sparse JSON array"
    end
    active[value] = true
    for _, child in pairs(value) do
        local valid, message = validate_request_json(child, depth + 1, active)
        if not valid then
            active[value] = nil
            return false, message
        end
    end
    active[value] = nil
    return true
end

local function is_protocol_identifier(value)
    if type(value) ~= "string" or #value < 1 or #value > 96 then return false end
    for index = 1, #value do
        local byte = value:byte(index)
        local letter_or_digit = (byte >= 48 and byte <= 57) or
            (byte >= 65 and byte <= 90) or (byte >= 97 and byte <= 122)
        if not letter_or_digit and (index == 1 or (byte ~= 45 and byte ~= 46 and byte ~= 95)) then
            return false
        end
    end
    return true
end

local function is_nonnegative_json_safe_integer(value)
    if type(value) ~= "number" or value ~= value or value < 0 then return false end
    return value <= max_safe_float_integer and value == math.floor(value)
end

local function is_lower_sha256(value)
    if type(value) ~= "string" or #value ~= 64 then return false end
    for index = 1, #value do
        local byte = value:byte(index)
        local digit = byte >= 48 and byte <= 57
        local lower_hex = byte >= 97 and byte <= 102
        if not digit and not lower_hex then return false end
    end
    return true
end

local function resolve_job(job, result_kind, expected_job_id)
    if type(job) ~= "table" then
        return nil, failure("invalid_job", "Rin returned an invalid job"), true
    end
    if not is_protocol_identifier(job.job_id) or job.job_id ~= expected_job_id then
        return nil, failure("invalid_job", "Rin returned a job with an invalid or mismatched job_id"), true
    end
    if result_kind == "proposal" and
        (not is_protocol_identifier(job.session_id) or not is_protocol_identifier(job.request_id)) then
        return nil, failure("invalid_job", "Rin returned a proposal job with invalid identity fields"), true
    end
    if result_kind == "generation" and not is_protocol_identifier(job.request_id) then
        return nil, failure("invalid_job", "Rin returned a generation job with an invalid request_id"), true
    end
    if type(job.status) ~= "string" then
        return nil, failure("invalid_job", "Rin returned an invalid job status"), true
    end
    local status = job.status
    if status == "succeeded" then
        if result_kind == "proposal" then
            local proposal = job.proposal
            if type(proposal) ~= "table" or
                not is_protocol_identifier(proposal.id) or
                not is_protocol_identifier(proposal.actor_id) or
                proposal.session_id ~= job.session_id or
                proposal.request_id ~= job.request_id or
                not is_nonnegative_json_safe_integer(proposal.tick) then
                return nil, failure(
                    "invalid_job",
                    "Successful proposal job contained invalid identity fields"
                ), true
            end
        end
        if result_kind == "generation" then
            local content = type(job.result) == "table" and job.result.content or nil
            if type(content) ~= "string" or content:match("^%s*$") or
                content:find("%z") or #content > max_generation_content_bytes then
                return nil, failure(
                    "invalid_job",
                    "Successful generation job did not include bounded content"
                ), true
            end
        end
        return job, nil, true
    end
    if terminal_job_states[status] then
        local detail = type(job.error) == "table" and job.error or {}
        return nil, failure(detail.code or ("job_" .. status), detail.message or ("Rin job ended as " .. status)), true
    end
    if status ~= "queued" and status ~= "running" then
        return nil, failure("invalid_job", "Rin returned an unknown job status"), true
    end
    return nil, nil, false
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
        local valid, validation_error = validate_request_json(payload, 0, {})
        if not valid then
            callback(nil, failure("invalid_request", validation_error))
            return
        end
        local encoded, value = pcall(self.json_encode, payload)
        if not encoded or type(value) ~= "string" then
            callback(nil, failure("invalid_request", "Rin payload is not JSON serializable"))
            return
        end
        if not valid_utf8(value) then
            callback(nil, failure("invalid_request", "Rin JSON codec returned invalid UTF-8"))
            return
        end
        body = value
    end

    local headers = {
        ["Accept"] = "application/json",
        ["User-Agent"] = "rin-lua/" .. rin.VERSION,
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
    local started, start_error = pcall(self.http_fetch, request, function(response, transport_error)
        if response == nil and type(transport_error) == "table" then
            local timed_out = transport_error.code == "transport_timeout"
            finish(nil, failure(
                timed_out and "transport_timeout" or "transport_failed",
                timed_out and "Rin request timed out" or "Rin transport failed"
            ))
            return
        end
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
        if not valid_utf8(raw) then
            finish(nil, failure("invalid_response", "Rin returned invalid UTF-8", status))
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
function Client:create_session(payload, callback) self:_post("/v2/session/create", payload, 200, callback) end
function Client:observe(payload, callback) self:_post("/v2/session/observe", payload, 200, callback) end
function Client:propose(payload, callback) self:_post("/v2/agent/propose", payload, 200, callback) end
function Client:submit_proposal_job(payload, callback) self:_post("/v2/jobs/propose", payload, 202, callback) end
function Client:get_proposal_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("GET", "/v2/jobs/" .. id, nil, 200, callback)
end
function Client:cancel_proposal_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("DELETE", "/v2/jobs/" .. id, nil, 200, callback)
end
function Client:submit_generation_job(payload, callback) self:_post("/v2/generation/jobs", payload, 202, callback) end
function Client:get_generation_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("GET", "/v2/generation/jobs/" .. id, nil, 200, callback)
end
function Client:cancel_generation_job(job_id, callback)
    local id, err = path_id(job_id)
    if not id then callback(nil, err); return end
    self:_request("DELETE", "/v2/generation/jobs/" .. id, nil, 200, callback)
end
function Client:report_action(payload, callback) self:_post("/v2/action/report", payload, 200, callback) end
function Client:report_action_batch(payload, callback) self:_post("/v2/action/report-batch", payload, 200, callback) end
function Client:set_actor_activity(payload, callback) self:_post("/v2/session/activity", payload, 200, callback) end
function Client:arbitrate(payload, callback) self:_post("/v2/world/arbitrate", payload, 200, callback) end
function Client:state(payload, callback) self:_post("/v2/session/get", payload, 200, callback) end
function Client:session_stats(payload, callback) self:_post("/v2/session/stats", payload, 200, callback) end
function Client:archive_session(payload, callback) self:_post("/v2/session/archive", payload, 200, callback) end
function Client:delete_session(payload, callback) self:_post("/v2/session/delete", payload, 200, callback) end
function Client:snapshot(payload, callback) self:_post("/v2/session/snapshot", payload, 200, callback) end
function Client:restore(payload, callback) self:_post("/v2/session/restore", payload, 200, callback) end
function Client:timeline(payload, callback) self:_post("/v2/session/timeline", payload, 200, callback) end
function Client:replay(payload, callback) self:_post("/v2/session/replay", payload, 200, callback) end
function Client:due_agents(payload, callback) self:_post("/v2/scheduler/due", payload, 200, callback) end

function Client:_wait_job(job_id, getter, canceler, options, callback, result_kind)
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
            local resolved, job_error, terminal = resolve_job(job, result_kind, job_id)
            if terminal then callback(resolved, job_error); return end
            if self.now() >= expires then
                canceler(self, job_id, function(canceled_job, cancel_error)
                    if cancel_error then
                        callback(nil, failure("job_timeout", "Rin job exceeded its deadline"))
                        return
                    end
                    local canceled_result, canceled_error, canceled_terminal =
                        resolve_job(canceled_job, result_kind, job_id)
                    if canceled_terminal then
                        callback(canceled_result, canceled_error)
                    else
                        callback(nil, failure("job_timeout", "Rin job exceeded its deadline"))
                    end
                end)
                return
            end
            self.schedule(interval, poll)
        end)
    end
    poll()
end

function Client:wait_for_proposal(job_id, options, callback)
    self:_wait_job(job_id, Client.get_proposal_job, Client.cancel_proposal_job, options, callback, "proposal")
end

function Client:wait_for_generation(job_id, options, callback)
    local configured = {}
    for key, value in pairs(options or {}) do configured[key] = value end
    if configured.deadline == nil then configured.deadline = 45 end
    self:_wait_job(job_id, Client.get_generation_job, Client.cancel_generation_job, configured, callback, "generation")
end

local Workflow = {}
Workflow.__index = Workflow

local function workflow_error(code, message)
    return failure(code, message)
end

local function semantic_equal(left, right, active)
    if type(left) ~= type(right) then return false end
    if type(left) ~= "table" then
        if type(left) ~= "number" then return left == right end
        return left == right and left == left and
            left ~= math.huge and left ~= -math.huge
    end
    active = active or {}
    if active[left] or active[right] then return false end
    active[left], active[right] = true, true
    local count = 0
    for key, value in pairs(left) do
        count = count + 1
        if right[key] == nil or not semantic_equal(value, right[key], active) then
            active[left], active[right] = nil, nil
            return false
        end
    end
    local right_count = 0
    for _ in pairs(right) do right_count = right_count + 1 end
    active[left], active[right] = nil, nil
    return count == right_count
end

local function valid_proposal_job(attempt, job)
    return type(job) == "table" and
        is_protocol_identifier(job.job_id) and job.job_id == attempt.job_id and
        job.session_id == attempt.request.session_id and
        job.request_id == attempt.request.request_id and
        type(job.status) == "string"
end

function rin.resolve_offered_action(request, proposal)
    if type(proposal) ~= "table" then return nil end
    if not is_protocol_identifier(proposal.id) or
        type(request) ~= "table" or
        proposal.session_id ~= request.session_id or
        proposal.request_id ~= request.request_id or
        proposal.actor_id ~= request.actor_id or
        not is_nonnegative_json_safe_integer(proposal.tick) or
        proposal.tick ~= request.tick or
        not semantic_equal(
            proposal.decision_window,
            request.decision_window) or
        type(proposal.action) ~= "table" or
        type(request.offers) ~= "table" then
        return nil
    end
    for _, offer in ipairs(request.offers) do
        if semantic_equal(offer, proposal.action) then return offer end
    end
    return nil
end

local function valid_attempt(attempt)
    return type(attempt) == "table" and attempt.version == 1 and
        is_protocol_identifier(attempt.operation_id) and
        type(attempt.request) == "table" and
        is_protocol_identifier(attempt.request.session_id) and
        is_protocol_identifier(attempt.request.request_id) and
        is_protocol_identifier(attempt.request.actor_id) and
        is_nonnegative_json_safe_integer(attempt.request.tick) and
        type(attempt.job_id) == "string" and
        (attempt.job_id == "" or is_protocol_identifier(attempt.job_id))
end

function rin.new_workflow(client, store)
    if getmetatable(client) ~= Client or type(store) ~= "table" then
        return nil, workflow_error("invalid_workflow", "Workflow requires a Rin Client and Store")
    end
    for _, method in ipairs({
        "load_attempt", "create_attempt", "save_attempt", "begin_action",
        "complete_action", "settle_without_action",
        "list_outcomes", "acknowledge_outcome",
    }) do
        if type(store[method]) ~= "function" then
            return nil, workflow_error(
                "invalid_workflow",
                "Workflow Store is missing " .. method)
        end
    end
    return setmetatable({
        client = client,
        store = store,
        busy = {},
        draining = {},
        settling = {},
    }, Workflow)
end

function Workflow:begin(key, operation_id, request)
    if self.busy[key] or self.draining[key] or self.settling[key] or
        not is_protocol_identifier(operation_id) or
        type(request) ~= "table" or
        not is_protocol_identifier(request.session_id) or
        not is_protocol_identifier(request.request_id) or
        not is_protocol_identifier(request.actor_id) or
        not is_nonnegative_json_safe_integer(request.tick) then
        return nil, workflow_error("invalid_workflow", "Pending Turn identity is invalid")
    end
    local attempt = {
        version = 1,
        operation_id = operation_id,
        request = request,
        job_id = "",
    }
    local ok, err = self.store:create_attempt(key, attempt)
    if not ok then
        return nil, err or workflow_error(
            "proposal_attempt_pending",
            "A Proposal Attempt is already pending")
    end
    return attempt
end

function Workflow:_clear_job_and_submit(key, attempt, may_resubmit, callback)
    local updated = {
        version = attempt.version,
        operation_id = attempt.operation_id,
        request = attempt.request,
        job_id = "",
    }
    local ok, err = self.store:save_attempt(key, updated)
    if not ok then callback(nil, err or workflow_error(
        "proposal_attempt_persist_failed", "Could not clear the retained Job identity")); return end
    self:_submit(key, updated, may_resubmit, callback)
end

function Workflow:_inspect(key, attempt, job, may_resubmit, callback)
    if not valid_proposal_job(attempt, job) then
        callback(nil, workflow_error(
            "job_identity_mismatch", "Proposal Job does not match the Pending Turn"))
        return
    end
    if job.status == "succeeded" then
        local authored_offer =
            rin.resolve_offered_action(attempt.request, job.proposal)
        if not authored_offer then
            callback(nil, workflow_error(
                "proposal_binding_mismatch",
                "Proposal does not match the complete durable Offer"))
            return
        end
        job.proposal.action = authored_offer
        job.proposal.decision_window = attempt.request.decision_window
        callback({ kind = "proposal", attempt = attempt, job = job }, nil)
        return
    end
    if terminal_job_states[job.status] then
        local detail = type(job.error) == "table" and job.error or {}
        local code = tostring(detail.code or ("job_" .. job.status))
        if code == "proposal_outcome_unknown" and may_resubmit then
            self:_clear_job_and_submit(key, attempt, false, callback)
            return
        end
        callback({
            kind = "no_proposal",
            attempt = attempt,
            error = workflow_error(code, detail.message or "Proposal Job ended without a Proposal"),
        }, nil)
        return
    end
    if job.status ~= "queued" and job.status ~= "running" then
        callback(nil, workflow_error("invalid_job", "Proposal Job has an unknown status"))
        return
    end
    self.client:wait_for_proposal(attempt.job_id, nil, function(resolved, wait_error)
        if not wait_error then
            self:_inspect(key, attempt, resolved, may_resubmit, callback)
            return
        end
        self.client:get_proposal_job(attempt.job_id, function(confirmed, confirm_error)
            if confirm_error then
                if confirm_error.code == "job_not_found" and may_resubmit then
                    self:_clear_job_and_submit(key, attempt, false, callback)
                else
                    callback(nil, confirm_error)
                end
                return
            end
            self:_inspect(key, attempt, confirmed, may_resubmit, callback)
        end)
    end)
end

function Workflow:_submit(key, attempt, may_resubmit, callback)
    self.client:submit_proposal_job(attempt.request, function(queued, submit_error)
        if submit_error then callback(nil, submit_error); return end
        local job_id = type(queued) == "table" and queued.job_id or nil
        if not is_protocol_identifier(job_id) then
            callback(nil, workflow_error("invalid_submission", "Rin returned an invalid Job identity"))
            return
        end
        local updated = {
            version = attempt.version,
            operation_id = attempt.operation_id,
            request = attempt.request,
            job_id = job_id,
        }
        local ok, err = self.store:save_attempt(key, updated)
        if not ok then
            callback(nil, err or workflow_error(
                "proposal_attempt_persist_failed", "Could not retain the Proposal Job identity"))
            return
        end
        self.client:get_proposal_job(job_id, function(job, get_error)
            if get_error then callback(nil, get_error); return end
            self:_inspect(key, updated, job, may_resubmit, callback)
        end)
    end)
end

function Workflow:resume(key, callback)
    if self.busy[key] or self.draining[key] or self.settling[key] then
        callback(nil, workflow_error("workflow_busy", "Pending work is already being resumed"))
        return
    end
    self.busy[key] = true
    local function finish(result, err)
        self.busy[key] = nil
        callback(result, err)
    end
    local attempt, load_error = self.store:load_attempt(key)
    if load_error or not valid_attempt(attempt) then
        finish(nil, load_error or workflow_error(
            "invalid_proposal_attempt", "Pending Turn is missing or malformed"))
        return
    end
    if tostring(attempt.job_id or "") == "" then
        self:_submit(key, attempt, true, finish)
        return
    end
    self.client:get_proposal_job(attempt.job_id, function(job, get_error)
        if get_error then
            if get_error.code == "job_not_found" then
                self:_clear_job_and_submit(key, attempt, false, finish)
            else
                finish(nil, get_error)
            end
            return
        end
        self:_inspect(key, attempt, job, true, finish)
    end)
end

local function valid_outcome_entry(entry)
    if type(entry) ~= "table" or not is_protocol_identifier(entry.key) or
        entry.kind ~= "report" or
        type(entry.request) ~= "table" then
        return false
    end
    if entry.request.protocol_version ~= rin.PROTOCOL_VERSION or
        not is_protocol_identifier(entry.request.session_id) or
        not is_protocol_identifier(entry.request.request_id) or
        not is_nonnegative_json_safe_integer(entry.request.tick) then
        return false
    end
    local report = entry.request.report
    return type(report) == "table" and
        is_protocol_identifier(report.proposal_id) and
        is_protocol_identifier(report.event_id) and
        (report.decision == "accepted" or report.decision == "rejected") and
        type(report.summary) == "string" and #report.summary >= 1
end

local terminal_action_states = {
    succeeded = true,
    failed = true,
    cancelled = true,
    interrupted = true,
    stale = true,
    ["outcome-unknown"] = true,
}

local function outcome_matches_proposal(attempt, proposal, entry)
    local offer = rin.resolve_offered_action(attempt.request, proposal)
    if not offer or not valid_outcome_entry(entry) or
        entry.key ~= attempt.operation_id or
        entry.request.session_id ~= attempt.request.session_id or
        entry.request.report.proposal_id ~= proposal.id then
        return false
    end
    local report = entry.request.report
    if report.decision == "rejected" then
        return report.invocation == nil and report.run == nil and
            report.outcome == nil
    end
    if report.decision ~= "accepted" or type(report.invocation) ~= "table" or
        type(report.run) ~= "table" or type(report.outcome) ~= "table" then
        return false
    end
    local invocation = {}
    for key, value in pairs(offer) do
        if key ~= "description" then invocation[key] = value end
    end
    invocation.operation_id = attempt.operation_id
    return semantic_equal(report.invocation, invocation) and
        report.run.operation_id == attempt.operation_id and
        report.outcome.operation_id == attempt.operation_id and
        terminal_action_states[report.run.status] == true and
        report.outcome.status == report.run.status and
        semantic_equal(report.outcome.epoch, offer.expected_epoch)
end

function Workflow:drain_outbox(key, callback)
    if self.busy[key] or self.draining[key] or self.settling[key] then
        callback(nil, workflow_error("workflow_busy", "Outcome Outbox is already being drained"))
        return
    end
    self.draining[key] = true
    local finished = false
    local function finish(result, err)
        if finished then return end
        finished = true
        self.draining[key] = nil
        callback(result, err)
    end
    local entries, list_error = self.store:list_outcomes(key)
    if list_error or type(entries) ~= "table" then
        finish(nil, list_error or workflow_error("invalid_outbox", "Outcome Outbox is invalid"))
        return
    end
    local snapshot = {}
    for entry_index, entry in ipairs(entries) do
        snapshot[entry_index] = entry
    end
    entries = snapshot
    local index, acknowledged = 1, 0
    local function next_entry(err)
        if err then finish(nil, err); return end
        local entry = entries[index]
        if not entry then finish(acknowledged, nil); return end
        index = index + 1
        if not valid_outcome_entry(entry) then
            finish(nil, workflow_error("invalid_outbox", "Outcome Outbox entry is malformed"))
            return
        end
        local function acknowledge(result, report_error)
            if report_error then
                next_entry(report_error)
                return
            end
            if type(result) ~= "table" or
                not is_protocol_identifier(result.session_id) or
                result.session_id ~= entry.request.session_id or
                not is_nonnegative_json_safe_integer(result.revision) or
                result.revision < 1 or
                not is_lower_sha256(result.head_hash) or
                type(result.duplicate) ~= "boolean" then
                next_entry(workflow_error(
                    "invalid_outbox_ack",
                    "Rin returned a malformed or wrong-Session Outcome acknowledgement"))
                return
            end
            local removed, remove_error = self.store:acknowledge_outcome(key, entry)
            if not removed then
                next_entry(remove_error or workflow_error(
                    "outbox_ack_failed", "Could not persist Outcome acknowledgement"))
                return
            end
            acknowledged = acknowledged + 1
            next_entry(nil)
        end
        self.client:report_action(entry.request, acknowledge)
    end
    next_entry(nil)
end

function Workflow:apply_and_enqueue(key, attempt, proposal, outcome, apply, callback)
    if self.busy[key] or self.draining[key] or self.settling[key] then
        callback(nil, workflow_error("workflow_busy", "Pending Turn is already being settled"))
        return
    end
    if not valid_attempt(attempt) or type(apply) ~= "function" or
        not outcome_matches_proposal(attempt, proposal, outcome) then
        callback(nil, workflow_error("invalid_workflow", "Outcome settlement is invalid"))
        return
    end
    self.settling[key] = true
    local function finish(result, err)
        self.settling[key] = nil
        callback(result, err)
    end
    local accepted = outcome.request.report.decision == "accepted"
    if accepted then
        local started, start_error = self.store:begin_action(key, attempt, outcome)
        if not started then
            finish(nil, start_error or workflow_error(
                "workflow_persist_failed",
                "Could not persist Active Run before execution"))
            return
        end
        local applied, apply_error = pcall(apply, attempt.operation_id)
        if not applied then
            finish(nil, workflow_error("game_apply_failed", tostring(apply_error)))
            return
        end
    end
    local completed, complete_error
    if accepted then
        completed, complete_error =
            self.store:complete_action(key, attempt, outcome)
    else
        completed, complete_error =
            self.store:settle_without_action(key, attempt, outcome)
    end
    if not completed then
        finish(nil, complete_error or workflow_error(
            "workflow_persist_failed", "Could not persist completed Pending Turn"))
        return
    end
    finish(true, nil)
end

-- Builds one fully bound, host-authored offer. Capability discovery is not
-- authorization; callers still choose the arguments and targets for this
-- exact Decision Window.
function rin.action_offer(options, window)
    return {
        offer_id = options.offer_id,
        decision_window_id = window.id,
        actor_id = options.actor_id,
        capability = {
            id = options.capability_id,
            version = options.capability_version or "1",
        },
        descriptor_digest = options.descriptor_digest,
        description = options.description,
        arguments = options.arguments,
        targets = options.targets,
        expected_epoch = window.epoch,
        observation_seq = window.observation_seq,
        deadline = window.deadline,
    }
end

-- Builds the terminal projection for an immediate host action. It copies the
-- selected offer verbatim into Invocation; Execute remains game-owned.
function rin.immediate_action_report(options)
    local report = {
        proposal_id = options.proposal.id,
        event_id = options.event_id,
        decision = options.accepted and "accepted" or "rejected",
        summary = options.summary,
        tags = options.tags,
    }
    if options.accepted then
        local offer = options.proposal.action
        local status = terminal_action_states[options.status] and
            options.status or "succeeded"
        report.invocation = {
            operation_id = options.operation_id,
            offer_id = offer.offer_id,
            decision_window_id = offer.decision_window_id,
            actor_id = offer.actor_id,
            capability = offer.capability,
            descriptor_digest = offer.descriptor_digest,
            arguments = offer.arguments,
            targets = offer.targets,
            expected_epoch = offer.expected_epoch,
            observation_seq = offer.observation_seq,
            deadline = offer.deadline,
        }
        report.run = {
            operation_id = options.operation_id,
            status = status,
            progress_seq = 1,
            progress = status == "succeeded" and 100 or 0,
            updated_at = options.occurred_at,
        }
        report.outcome = {
            operation_id = options.operation_id,
            status = status,
            summary = options.summary,
            epoch = offer.expected_epoch,
            world_seq = options.world_seq,
            occurred_at = options.occurred_at,
        }
    end
    return {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = options.session_id,
        request_id = options.request_id,
        tick = options.tick,
        report = report,
    }
end

function rin.proposal_freshness(state, proposal)
    if type(state) ~= "table" or type(proposal) ~= "table" or
        type(state.proposals) ~= "table" or
        type(state.proposals[proposal.id]) ~= "table" or
        state.proposals[proposal.id].status ~= "pending" then
        return "stale"
    end
    if proposal.based_on_world_revision ~= nil then
        return is_nonnegative_json_safe_integer(proposal.based_on_world_revision) and
            is_nonnegative_json_safe_integer(state.world_revision) and
            proposal.based_on_world_revision == state.world_revision and
            proposal.based_on_world_revision > 0 and "fresh" or "stale"
    end
    return is_nonnegative_json_safe_integer(proposal.created_revision) and
        is_nonnegative_json_safe_integer(state.revision) and
        proposal.created_revision == state.revision and "fresh" or "stale"
end

return rin
