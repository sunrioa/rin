-- Run the Lua SDK against the shared live Sidecar corpus. The injected curl
-- adapter mirrors the transport port a game engine supplies in production.

local rin = dofile("sdk/lua/rin.lua")

local function required(name)
    local value = os.getenv(name)
    assert(value and value ~= "", name .. " is required")
    return value
end

local function shell_quote(value)
    assert(not value:find("[\r\n]"), "unsafe command value")
    if package.config:sub(1, 1) == "\\" then
        assert(not value:find('"', 1, true), "unsafe Windows command value")
        return '"' .. value .. '"'
    end
    return "'" .. value:gsub("'", "'\\''") .. "'"
end

local function read_file(path)
    local file = assert(io.open(path, "rb"))
    local value = file:read("*a")
    file:close()
    return value
end

local function fetch(request, callback)
    local request_path = os.tmpname()
    local response_path = os.tmpname()
    local request_file = assert(io.open(request_path, "wb"))
    request_file:write(request.body or "")
    request_file:close()
    local response_file = assert(io.open(response_path, "wb"))
    response_file:close()
    local command = table.concat({
        shell_quote(os.getenv("RIN_SDK_CURL") or "curl"),
        "--silent",
        "--output", shell_quote(response_path),
        "--write-out", shell_quote("%{http_code}"),
        "--request", shell_quote(request.method),
        "--max-time", tostring(request.timeout),
        "--header", shell_quote("Accept: application/json"),
        "--header", shell_quote("Content-Type: application/json; charset=utf-8"),
        "--header", shell_quote(
            "Authorization: " .. (request.headers.Authorization or "")),
        "--data-binary", shell_quote("@" .. request_path),
        shell_quote(request.url),
    }, " ")
    local pipe = io.popen(command, "r")
    if not pipe then
        os.remove(request_path)
        callback(nil, { code = "transport_failed" })
        return
    end
    local status = pipe:read("*a")
    local closed, _, exit_code = pipe:close()
    local body = read_file(response_path)
    os.remove(request_path)
    os.remove(response_path)
    if not closed then
        callback(nil, {
            code = tonumber(exit_code) == 28 and
                "transport_timeout" or "transport_failed",
        })
        return
    end
    callback({ status = tonumber(status), body = body, headers = {} })
end

local function decode_json(value)
    local envelope = { ok = value:match('"ok"%s*:%s*true') ~= nil }
    local data = {
        protocol_version = value:match('"protocol_version"%s*:%s*"([^"]+)"'),
        session_id = value:match('"session_id"%s*:%s*"([^"]+)"'),
        revision = tonumber(value:match('"revision"%s*:%s*(%d+)')),
        head_hash = value:match('"head_hash"%s*:%s*"([^"]+)"'),
        duplicate = value:match('"duplicate"%s*:%s*true') ~= nil,
    }
    if envelope.ok then
        envelope.data = data
    else
        envelope.error = {
            code = value:match('"code"%s*:%s*"([^"]+)"'),
            message = value:match('"message"%s*:%s*"([^"]+)"'),
            field = value:match('"field"%s*:%s*"([^"]+)"'),
        }
    end
    return envelope
end

local encoded_body = required("RIN_SDK_CORPUS_BODY")
local client_name = required("RIN_SDK_CORPUS_CLIENT")
local payload = {
    protocol_version = rin.PROTOCOL_VERSION,
    request_id = "create." .. client_name,
    session_id = "session." .. client_name,
    binding = {
        game_id = "conformance",
        content_id = "sdk-corpus",
        content_version = "1",
        content_hash = "sha256:conformance",
    },
    actors = {
        {
            id = "npc." .. client_name,
            kind = "npc",
            display_name = "SDK Corpus NPC",
            think_every_ticks = 1,
            enabled = true,
        },
    },
}
local function new_client(base_url, timeout)
    return assert(rin.new({
        base_url = base_url,
        token = required("RIN_SDK_CORPUS_TOKEN"),
        timeout = timeout,
        http_fetch = fetch,
        json_encode = function() return encoded_body end,
        json_decode = decode_json,
    }))
end

local client = new_client(required("RIN_SDK_CORPUS_BASE_URL"), 5)
local health
client:health(function(data, err) assert(not err); health = data end)
assert(health.protocol_version == rin.PROTOCOL_VERSION, "invalid health response")
local first
client:create_session(payload, function(data, err) assert(not err); first = data end)
local retry
client:create_session(payload, function(data, err) assert(not err); retry = data end)
assert(first.duplicate == false and retry.duplicate == true)
assert(first.revision == retry.revision and first.head_hash == retry.head_hash)

local timeout_error
new_client(required("RIN_SDK_CORPUS_SLOW_URL"), 0.05):create_session(
    payload,
    function(_, err) timeout_error = err end)
assert(timeout_error and timeout_error.code == "transport_timeout")

print("Lua SDK live Sidecar corpus passed")
