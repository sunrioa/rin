local rin = {
    VERSION = "0.7.0",
    CONTRACT_VERSION = "rin.control/v2",
    DEFAULT_BASE_URL = "http://127.0.0.1:7375",
    DEFAULT_MAX_RESPONSE_BYTES = 8 * 1024 * 1024,
}

local Client = {}
Client.__index = Client

local max_json_depth = 64
local max_safe_integer = 9007199254740991

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

local function validate_json(value, depth, active)
    if depth > max_json_depth then
        return false, "Control payload exceeds the JSON nesting limit"
    end
    local kind = type(value)
    if value == nil or kind == "boolean" then return true end
    if kind == "string" then
        if not valid_utf8(value) then return false, "Control payload contains invalid UTF-8" end
        return true
    end
    if kind == "number" then
        if value ~= value or value == math.huge or value == -math.huge then
            return false, "Control payload contains a non-finite JSON number"
        end
        if value == math.floor(value) and (value < -max_safe_integer or value > max_safe_integer) then
            return false, "Control payload contains an unsafe JSON integer"
        end
        return true
    end
    if kind ~= "table" then return false, "Control payload contains a non-JSON value" end
    if active[value] then return false, "Control payload contains a JSON cycle" end

    local string_keys, array_keys, maximum_index = 0, 0, 0
    for key in pairs(value) do
        if type(key) == "string" then
            if not valid_utf8(key) then return false, "Control payload contains an invalid UTF-8 key" end
            string_keys = string_keys + 1
        elseif type(key) == "number" and key >= 1 and key == math.floor(key) then
            array_keys = array_keys + 1
            if key > maximum_index then maximum_index = key end
        else
            return false, "Control payload contains a non-JSON key"
        end
    end
    if string_keys > 0 and array_keys > 0 then
        return false, "Control payload mixes object and array keys"
    end
    if depth == 0 and array_keys > 0 then return false, "Control payload must be an object" end
    if array_keys > 0 and maximum_index ~= array_keys then
        return false, "Control payload contains a sparse JSON array"
    end

    active[value] = true
    for _, child in pairs(value) do
        local valid, message = validate_json(child, depth + 1, active)
        if not valid then
            active[value] = nil
            return false, message
        end
    end
    active[value] = nil
    return true
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

local function normalize_base_url(value)
    local base_url = tostring(value or rin.DEFAULT_BASE_URL):match("^%s*(.-)%s*$")
    while base_url:sub(-1) == "/" do base_url = base_url:sub(1, -2) end
    local scheme, authority = base_url:match("^(https?)://([^/%?#]+)$")
    if scheme ~= "http" or not authority or authority:find("@", 1, true) then
        return nil, failure(
            "invalid_base_url",
            "Control Daemon URL must be a plain loopback HTTP origin with an explicit port"
        )
    end

    local host, port
    if authority:sub(1, 1) == "[" then
        host, port = authority:match("^%[([^%]]+)%]:(%d+)$")
    else
        host, port = authority:match("^([^:]+):(%d+)$")
    end
    local numeric_port = tonumber(port)
    if not host or not numeric_port or numeric_port < 1 or numeric_port > 65535 or
        not is_loopback(host) then
        return nil, failure(
            "invalid_base_url",
            "Control Daemon URL must be a plain loopback HTTP origin with an explicit port"
        )
    end
    return base_url
end

local function validate_token(value)
    local token = tostring(value or "")
    if #token < 32 or #token > 4096 or token:find("[%z\r\n]") or
        token:match("^%s") or token:match("%s$") or not valid_utf8(token) then
        return nil, failure(
            "invalid_token",
            "Control token must be a bounded single-line value containing at least 32 bytes"
        )
    end
    return token
end

local function header_value(headers, wanted)
    if type(headers) ~= "table" then return nil end
    wanted = wanted:lower()
    for key, value in pairs(headers) do
        if tostring(key):lower() == wanted then return tostring(value) end
    end
    return nil
end

local function valid_content_type(headers)
    local value = header_value(headers, "content-type")
    if not value then return false end
    local media_type = value:match("^%s*([^;]+)")
    return media_type and media_type:lower() == "application/json"
end

function rin.new(options)
    options = options or {}
    if type(options) ~= "table" then
        return nil, failure("invalid_options", "Control options must be a table")
    end
    if type(options.http_fetch) ~= "function" or type(options.json_encode) ~= "function" or
        type(options.json_decode) ~= "function" then
        return nil, failure("missing_adapter", "http_fetch, json_encode, and json_decode are required")
    end
    local token, token_error = validate_token(options.token)
    if not token then return nil, token_error end
    local base_url, url_error = normalize_base_url(options.base_url)
    if not base_url then return nil, url_error end

    local timeout = tonumber(options.timeout or 30)
    local max_response_bytes = tonumber(options.max_response_bytes or rin.DEFAULT_MAX_RESPONSE_BYTES)
    if not timeout or timeout ~= timeout or timeout < 0.05 or timeout > 120 then
        return nil, failure("invalid_timeout", "Control timeout must be between 0.05 and 120 seconds")
    end
    if not max_response_bytes or max_response_bytes ~= math.floor(max_response_bytes) or
        max_response_bytes < 1024 or max_response_bytes > rin.DEFAULT_MAX_RESPONSE_BYTES then
        return nil, failure("invalid_response_limit", "Control response limit must be between 1 KiB and 8 MiB")
    end

    return setmetatable({
        base_url = base_url,
        token = token,
        timeout = timeout,
        max_response_bytes = max_response_bytes,
        http_fetch = options.http_fetch,
        json_encode = options.json_encode,
        json_decode = options.json_decode,
    }, Client)
end

function Client:_request(method, path, payload, callback)
    if type(callback) ~= "function" then error("Control callback is required", 2) end
    local body
    if payload ~= nil then
        if type(payload) ~= "table" then
            callback(nil, failure("invalid_request", "Control payload must be an object"))
            return
        end
        local valid, validation_error = validate_json(payload, 0, {})
        if not valid then
            callback(nil, failure("invalid_request", validation_error))
            return
        end
        local encoded, value = pcall(self.json_encode, payload)
        if not encoded or type(value) ~= "string" or not valid_utf8(value) or
            not value:match("^%s*{") or not value:match("}%s*$") then
            callback(nil, failure("invalid_request", "Control payload is not a JSON object"))
            return
        end
        body = value
    end

    local request = {
        url = self.base_url .. path,
        method = method,
        headers = {
            ["Accept"] = "application/json",
            ["Authorization"] = "Bearer " .. self.token,
            ["User-Agent"] = "rin-control-lua/" .. rin.VERSION,
        },
        body = body,
        timeout = self.timeout,
        follow_redirects = false,
    }
    if body then request.headers["Content-Type"] = "application/json; charset=utf-8" end

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
                timed_out and "Control Daemon request timed out" or "Control Daemon is unavailable"
            ))
            return
        end
        if type(response) ~= "table" then
            finish(nil, failure("transport_failed", "Control transport returned an invalid response"))
            return
        end
        local status = tonumber(response.status)
        if not status or status ~= math.floor(status) or status < 100 or status > 599 then
            finish(nil, failure("transport_failed", "Control transport did not return a valid status"))
            return
        end
        if status >= 300 and status < 400 then
            finish(nil, failure("redirect_rejected", "Control Daemon attempted to redirect", status))
            return
        end
        if not valid_content_type(response.headers) then
            finish(nil, failure("invalid_response", "Control response must be application/json", status))
            return
        end
        local raw = response.body
        if type(raw) ~= "string" or not valid_utf8(raw) then
            finish(nil, failure("invalid_response", "Control response must be valid UTF-8", status))
            return
        end
        local declared_text = header_value(response.headers, "content-length")
        local declared = declared_text and tonumber(declared_text) or nil
        if declared_text and (not declared or declared < 0 or declared ~= math.floor(declared)) then
            finish(nil, failure("invalid_response", "Control response has an invalid Content-Length", status))
            return
        end
        if (declared and declared > self.max_response_bytes) or #raw > self.max_response_bytes then
            finish(nil, failure("response_too_large", "Control response exceeds the configured limit", status))
            return
        end

        local decoded, data = pcall(self.json_decode, raw)
        if not decoded or type(data) ~= "table" then
            if status < 200 or status >= 300 then
                finish(nil, failure("unavailable", "Control Daemon request failed", status))
            else
                finish(nil, failure("invalid_response", "Control Daemon returned invalid JSON", status))
            end
            return
        end
        if status < 200 or status >= 300 then
            finish(nil, failure(
                data.code or "unavailable",
                data.error or data.message or "Control Daemon request failed",
                status,
                data.field
            ))
            return
        end
        finish(data, nil)
    end)
    if not started then
        if delivered then error(start_error, 0) end
        finish(nil, failure("transport_failed", "Control transport could not start"))
    end
end

function Client:_post(path, payload, callback)
    if payload == nil then payload = {} end
    self:_request("POST", path, payload, callback)
end

function Client:info(callback)
    self:_request("GET", "/control/v2/info", nil, function(data, err)
        if err then callback(nil, err); return end
        if data.contract_version ~= rin.CONTRACT_VERSION then
            callback(nil, failure("control_contract_mismatch", "Control Daemon returned an unsupported contract"))
            return
        end
        callback(data, nil)
    end)
end

function Client:list_worlds(callback) self:_post("/control/v2/worlds", {}, callback) end
function Client:list_actors(input, callback) self:_post("/control/v2/actors", input, callback) end
function Client:get_actor(input, callback) self:_post("/control/v2/actor", input, callback) end
function Client:wait_actor(input, callback) self:_post("/control/v2/wait-actor", input, callback) end
function Client:observe_actor(input, callback) self:_post("/control/v2/observe", input, callback) end
function Client:list_capabilities(input, callback) self:_post("/control/v2/capabilities", input, callback) end
function Client:describe_capability(input, callback) self:_post("/control/v2/capability", input, callback) end
function Client:acquire_controller(input, callback) self:_post("/control/v2/controllers/acquire", input, callback) end
function Client:renew_controller(input, callback) self:_post("/control/v2/controllers/renew", input, callback) end
function Client:release_controller(input, callback) self:_post("/control/v2/controllers/release", input, callback) end
function Client:get_controller(input, callback) self:_post("/control/v2/controllers/get", input, callback) end
function Client:submit_action(input, callback) self:_post("/control/v2/actions/submit", input, callback) end
function Client:confirm_action(input, callback) self:_post("/control/v2/actions/confirm", input, callback) end
function Client:get_operation(input, callback) self:_post("/control/v2/operations/get", input, callback) end
function Client:wait_operation(input, callback) self:_post("/control/v2/operations/wait", input, callback) end
function Client:get_task_timeline(input, callback) self:_post("/control/v2/tasks/timeline/get", input, callback) end
function Client:wait_task_timeline(input, callback) self:_post("/control/v2/tasks/timeline/wait", input, callback) end
function Client:cancel_operation(input, callback) self:_post("/control/v2/operations/cancel", input, callback) end
function Client:set_emergency_stop(input, callback) self:_post("/control/v2/emergency-stop", input, callback) end

return rin
